"""M2 HTTP acceptance; only disposable credentials, no real AWS requests."""
import json
from identity import Client, MEMBER_PASSWORD

FIXTURE_SECRETS = ("fixture-aws-secret-for-http-acceptance-123", "fixture-proxy-password", "AKIAHTTPFIXTURE123456")


def check_accounts(admin):
    def request(path, method="GET", data=None, expected=200):
        status, _, body = admin.request("/api/v1" + path, method, data=data)
        assert status == expected, f"{method} {path} returned {status}: {body}"
        assert all(secret not in body for secret in FIXTURE_SECRETS), "response leaked a secret"
        return json.loads(body)["data"] if body else None

    account = request("/accounts", "POST", dict(name="HTTP 测试账号", partition="aws", aws_account_id="333333333333"), 201)
    account_id = account["id"]
    proxy = request("/proxies", "POST", dict(name="测试出口", host="127.0.0.1", port=1,
        auth=dict(username="fixture-proxy-user", password=FIXTURE_SECRETS[1])), 201)
    connection_request = dict(name="HTTP 测试连接", auth_type="access_key", region="us-east-1", route_mode="socks5", proxy_id=proxy["id"],
        credentials=dict(access_key_id=FIXTURE_SECRETS[2], secret_access_key=FIXTURE_SECRETS[0]))
    connection = request(f"/accounts/{account_id}/connections", "POST", connection_request, 201)
    connection_id = connection["id"]
    assert connection["access_key_masked"].endswith("3456") and connection["status"] == "pending"
    assert request("/proxies")[0]["connections"] == 1
    assert admin.request("/api/v1/accounts", "POST", data={}, csrf=False)[0] == 403

    member = request("/members", "POST", dict(username="account-reader", display_name="账号只读成员", role="viewer", password=MEMBER_PASSWORD), 201)
    reader = Client(admin.origin)
    assert reader.login("account-reader", MEMBER_PASSWORD)[0] == 200
    assert json.loads(reader.request("/api/v1/accounts")[2])["data"] == []
    assert reader.request(f"/api/v1/accounts/{account_id}")[0] == 403
    request(f"/members/{member['id']}/account-grants", "POST", dict(account_id=account_id, access="read"), 204)
    status, _, body = reader.request(f"/api/v1/accounts/{account_id}")
    assert status == 200 and "fixture-proxy" not in body and proxy["id"] not in body
    assert json.loads(body)["data"]["can_manage"] is False
    for path in ("/proxies", f"/connections/{connection_id}"):
        assert reader.request("/api/v1" + path)[0] == 403
    assert reader.request(f"/api/v1/connections/{connection_id}/verify", "POST")[0] == 403
    assert reader.request(f"/connections/{connection_id}/edit")[0] == 403

    for path in ("/accounts", "/accounts/new", f"/accounts/{account_id}", f"/accounts/{account_id}/edit",
                 f"/accounts/{account_id}/access", f"/accounts/{account_id}/connections/new", f"/connections/{connection_id}/edit", "/proxies", "/proxies/new", f"/proxies/{proxy['id']}/edit"):
        status, _, page = admin.request(path)
        assert status == 200 and "<html" in page, f"page failed: {path}"
        assert all(secret not in page for secret in FIXTURE_SECRETS), "page echoed credentials"

    invalid = dict(name="", auth_type="access_key", region="us-east-1", route_mode="socks5", proxy_id=proxy["id"],
                   access_key_id=FIXTURE_SECRETS[2], secret_access_key=FIXTURE_SECRETS[0], active="true", _csrf=admin.csrf)
    status, _, page = admin.request(f"/accounts/{account_id}/connections", "POST", form=invalid, headers={"HX-Request":"true"})
    assert status == 400 and 'id="connection-form"' in page and "<html" not in page
    assert all(secret not in page for secret in FIXTURE_SECRETS)
    update = dict(connection_request, credentials=None, active=False, version=connection["version"])
    connection = request(f"/connections/{connection_id}", "PATCH", update)
    verification = request(f"/connections/{connection_id}/verify", "POST")
    assert not verification["ok"] and verification["code"] == "CONNECTION_DISABLED"
    assert request(f"/accounts/{account_id}")["connections"][0]["active"] is False
    request(f"/members/{member['id']}/account-grants", "POST", dict(account_id=account_id, access="none"), 204)
    assert reader.request(f"/api/v1/accounts/{account_id}")[0] == 403
    # Keep one disabled fixture for restart, vault-rotation and browser checks.
    for path in ("/audit", f"/connections/{connection_id}/edit"):
        assert all(secret not in admin.request(path)[2] for secret in FIXTURE_SECRETS)
