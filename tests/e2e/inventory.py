"""HTTP checks over disposable cache fixtures; no live AWS requests."""
import json
import uuid
from identity import Client, MEMBER_PASSWORD
from operations import payload


def check_inventory(admin):
    anonymous = Client(admin.origin)
    for path in ("/api/v1/resources", "/api/v1/resources/options", "/api/v1/resources/scopes", "/api/v1/inventory/schedules"):
        assert anonymous.request(path)[0] == 401
    accounts = payload(admin, "/accounts")
    account_a = next(account for account in accounts if account["aws_account_id"] == "333333333333")
    account_b = next(account for account in accounts if account["aws_account_id"] == "555555555555")
    result = payload(admin, "/resources")
    assert len(result["items"]) == 3
    stale = next(item for item in result["items"] if item["region"] == "us-west-2")
    restricted = next(item for item in result["items"] if item["account_id"] == account_b["id"])
    detail = payload(admin, "/resources/" + stale["id"])
    assert detail["scope"]["status"] == "failed" and detail["scope"]["stale"]
    assert detail["scope"]["code"] == "AWS_ACCESS_DENIED"
    assert detail["resource"]["present"] and detail["resource"]["observed_at"].endswith("+08:00")
    assert len(payload(admin, "/resources?kind=account_region")["items"]) == 2
    assert len(payload(admin, "/resources?kind=lightsail_region")["items"]) == 2
    assert len(payload(admin, "/resources?region=us-west-2&state=stopped")["items"]) == 1
    assert len(payload(admin, "/resources?q=%25%27%20OR%201%3D1--")["items"]) == 0
    assert admin.request("/api/v1/resources?page=0")[0] == 400
    assert admin.request("/api/v1/resources?kind=unknown")[0] == 400
    for path in ("/resources", "/resources?kind=account_region", "/resources?kind=lightsail_region", "/resources/scopes", "/resources/refresh", "/resources/" + stale["id"], "/resources/schedules", "/resources/schedules/new"):
        status, _, page = admin.request(path)
        assert status == 200 and "<html" in page, f"inventory page failed: {path}"
        assert "<b>原样文本</b>" not in page, "AWS resource label was rendered as markup"
    assert "&lt;b&gt;原样文本&lt;/b&gt;" in admin.request("/resources")[2]
    member = next(value for value in payload(admin, "/members")["items"] if value["username"] == "account-reader")
    payload(admin, f"/members/{member['id']}/account-grants", "POST", {"account_id": account_a["id"], "access": "read"}, 204)
    reader = Client(admin.origin)
    assert reader.login("account-reader", MEMBER_PASSWORD)[0] == 200
    visible = payload(reader, "/resources")["items"]
    assert len(visible) == 2 and all(item["account_id"] == account_a["id"] for item in visible)
    assert reader.request("/api/v1/resources/" + restricted["id"])[0] == 403
    assert reader.request("/api/v1/resources?account_id=" + account_b["id"])[0] == 403
    assert len(payload(reader, "/resources/options")["accounts"]) == 1

    connection = payload(admin, "/accounts/" + account_a["id"])["connections"][0]
    request = {"targets": [{"connection_id": connection["id"], "kind": "lightsail_instance", "region": "us-east-1"}], "idempotency_key": str(uuid.uuid4())}
    # The UI fixture deliberately has a disabled connection, so this cannot
    # leave the machine or call real AWS even if the test is run online.
    assert admin.request("/api/v1/resources/refresh", "POST", data=request, csrf=False)[0] == 403
    assert admin.request("/api/v1/resources/refresh", "POST", data=request)[0] == 409
    form = {"connection_id": connection["id"], "kind": "lightsail_instance", "region": "us-east-1", "idempotency_key": str(uuid.uuid4()), "_csrf": admin.csrf}
    status, _, fragment = admin.request("/resources/refresh", "POST", form=form, headers={"HX-Request": "true"})
    assert status == 409 and 'id="inventory-form"' in fragment and "<html" not in fragment

    schedules = payload(admin, "/inventory/schedules")["items"]
    schedule = next(value for value in schedules if value["name"].startswith("预览定时同步"))
    assert admin.request("/resources/schedules/" + schedule["id"] + "/edit")[0] == 200
    assert reader.request("/api/v1/inventory/schedules/" + schedule["id"])[0] == 403
    update = {"name": "预览缓存定时同步（已停用）", "targets": schedule["targets"], "interval_minutes": 30, "active": False, "version": schedule["version"]}
    saved = payload(admin, "/inventory/schedules/" + schedule["id"], "PATCH", update)
    assert saved["interval_minutes"] == 30 and not saved["active"]
    assert admin.request("/api/v1/inventory/schedules/" + schedule["id"], "PATCH", data=update)[0] == 409
    deleted = next(value for value in schedules if value["name"].startswith("待删除"))
    payload(admin, "/inventory/schedules/" + deleted["id"], "DELETE", {"version": deleted["version"]}, 204)
    assert admin.request("/api/v1/inventory/schedules/" + deleted["id"])[0] == 404
    audit = payload(admin, "/audit")["items"]
    assert any(entry["action"] == "inventory.schedule.updated" for entry in audit)


def check_regions(admin, plan_id):
    status, _, form_page = admin.request("/regions/enable")
    assert status == 200 and 'data-region-selector' in form_page and 'name="regions"' not in form_page
    options = payload(admin, "/regions/options")
    assert options["writes_enabled"] is False
    connection = next(account for account in options["accounts"] if account["aws_account_id"] == "333333333333")["connections"][0]["id"]
    assert admin.request("/api/v1/regions/catalogue?connection_id="+connection)[0] == 409
    assert admin.request("/api/v1/regions/plans", "POST", data={"targets": []}, csrf=False)[0] == 403
    assert admin.request("/api/v1/regions/plans", "POST", data={"targets": []})[0] == 400
    plan = payload(admin, "/regions/plans/" + plan_id)
    assert plan["can_submit"] is False and plan["targets"][0]["checked_at"].endswith("+08:00")
    status, _, page = admin.request("/regions/plans/" + plan_id)
    assert status == 200 and "提交区域启用任务" in page and "disabled" in page
    status, _, body = admin.request(f"/api/v1/regions/plans/{plan_id}/submit", "POST", data={"idempotency_key": str(uuid.uuid4())})
    assert status == 409 and json.loads(body)["error"]["code"] == "CLOUD_WRITES_DISABLED"
    status, _, body = admin.request("/api/v1/jobs", "POST", data={"plan_id": plan_id, "idempotency_key": str(uuid.uuid4())})
    assert status == 409 and "CLOUD_WRITES_DISABLED" in body
    reader = Client(admin.origin)
    assert reader.login("account-reader", MEMBER_PASSWORD)[0] == 200
    assert reader.request("/api/v1/regions/options")[0] == 403
    assert reader.request("/api/v1/regions/catalogue?connection_id="+connection)[0] == 403
    assert reader.request("/api/v1/regions/plans/" + plan_id)[0] == 403
