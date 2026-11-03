"""Task HTTP, real Asynq delivery, streaming and restart acceptance checks."""
import json
import re
import time
import urllib.request
import uuid
from identity import Client, MEMBER_PASSWORD


def payload(client, path, method="GET", data=None, expected=200):
    status, _, body = client.request("/api/v1" + path, method, data=data)
    assert status == expected, f"{method} {path}: {status} {body}"
    return json.loads(body)["data"] if body else None


def submit_diagnostic(admin, behavior, count=2):
    plan = payload(admin, "/operations/diagnostics/plans", "POST", {"count": count, "behavior": behavior}, 201)
    assert plan["diagnostic"] and plan["created_at"].endswith("+08:00")
    request = {"plan_id": plan["id"], "idempotency_key": str(uuid.uuid4())}
    job = payload(admin, "/jobs", "POST", request, 202)
    assert payload(admin, "/jobs", "POST", request, 202)["id"] == job["id"], "duplicate submission created another job"
    return job


def wait_job(admin, job_id, wanted="succeeded"):
    deadline = time.monotonic() + 25
    while True:
        detail = payload(admin, "/jobs/" + job_id)
        if detail["job"]["status"] in ("succeeded", "failed", "partial_success", "cancelled", "needs_attention"):
            assert detail["job"]["status"] == wanted, detail
            return detail
        assert time.monotonic() < deadline, f"task did not settle: {detail['job']['status']}"
        time.sleep(0.2)


def check_operations(admin):
    assert admin.request("/api/v1/jobs", "POST", data={}, csrf=False)[0] == 403
    job = submit_diagnostic(admin, "response_lost")
    detail = wait_job(admin, job["id"])
    for item in detail["items"]:
        assert item["steps"][0]["attempt_count"] == 2
        assert item["steps"][0]["attempts"][1]["mode"] == "reconcile"
    assert payload(admin, "/jobs")["items"]
    for path in ("/jobs", "/jobs/" + job["id"], "/jobs/diagnostics/new"):
        status, _, page = admin.request(path)
        assert status == 200 and "<html" in page
    status, _, partial = admin.request("/partials/jobs/" + job["id"])
    assert status == 200 and 'id="job-content"' in partial and "<html" not in partial
    events = payload(admin, f"/jobs/{job['id']}/events")["events"]
    assert [event["seq"] for event in events] == list(range(1, len(events) + 1))
    status, headers, stream = admin.request(f"/api/v1/jobs/{job['id']}/stream", headers={"Last-Event-ID": "2"})
    assert status == 200 and headers["Content-Type"].startswith("text/event-stream")
    ids = [int(value) for value in re.findall(r"^id: (\d+)$", stream, re.M)]
    assert ids and ids[0] == 3 and "event: complete" in stream, "SSE replay failed"

    mixed = submit_diagnostic(admin, "partial")
    result = wait_job(admin, mixed["id"], "partial_success")
    failed = result["items"][1]["steps"][0]
    payload(admin, f"/jobs/{mixed['id']}/retry", "POST", {"version": result["job"]["version"], "step_ids": [failed["id"]]}, 204)
    retried = wait_job(admin, mixed["id"], "partial_success")
    assert retried["items"][0]["steps"][0]["attempt_count"] == 1, "retry repeated a successful target"
    assert retried["items"][1]["steps"][0]["attempt_count"] == 3

    reader = Client(admin.origin)
    assert reader.login("account-reader", MEMBER_PASSWORD)[0] == 200
    assert payload(reader, "/jobs")["items"] == []
    for suffix in ("", "/events", "/stream"):
        assert reader.request(f"/api/v1/jobs/{job['id']}" + suffix)[0] == 403
    assert reader.request(f"/api/v1/jobs/{job['id']}/cancel", "POST")[0] == 403
    assert reader.request("/api/v1/operations/diagnostics/plans", "POST", data={"count": 1, "behavior": "success"})[0] == 403

    # Revoking a live session must also stop an already-open event stream.
    member = payload(admin, "/members", "POST", {"username": "stream-admin", "display_name": "流测试管理员", "role": "admin", "password": MEMBER_PASSWORD}, 201)
    observer = Client(admin.origin)
    assert observer.login("stream-admin", MEMBER_PASSWORD)[0] == 200
    live = submit_diagnostic(admin, "wait", 8)
    request = urllib.request.Request(admin.origin + f"/api/v1/jobs/{live['id']}/stream")
    with observer.opener.open(request, timeout=10) as response:
        while True:
            line = response.readline().decode()
            assert line, "live stream closed before its first event"
            if line.startswith("event: progress"):
                break
        payload(admin, f"/members/{member['id']}", "PATCH", {"display_name": member["display_name"], "role": "admin", "active": False, "version": member["version"]})
        remainder = response.read().decode()
        assert "event: access_revoked" in remainder, "session revocation left SSE authorized"
    wait_job(admin, live["id"])

    # The next app restart happens while this persisted job is still unfinished.
    return submit_diagnostic(admin, "wait", 8)["id"]
