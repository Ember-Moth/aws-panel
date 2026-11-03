"""HTTP/UI contract checks using disabled connections and an explicit preview fixture."""
import json
import uuid
from identity import Client, MEMBER_PASSWORD
from inventory import payload

ROOT_PASSWORD_FIXTURE = "HTTP root fixture secret 2026!"


def check_lightsail(admin, plan_id):
    options = payload(admin, "/lightsail/options")
    assert not options["writes_enabled"] and options["accounts"]
    page=admin.request("/lightsail/create")[2]
    assert 'name="region" required' in page and 'name="login_method"' in page and 'name="root_password"' in page
    assert 'name="region" value=' not in page
    assert 'href="/lightsail/create"' in admin.request("/resources")[2]
    for data in ({"targets": []}, {"targets": [], "unexpected": 1}):
        assert admin.request("/api/v1/lightsail/plans", "POST", data=data)[0] == 400
    assert admin.request("/api/v1/lightsail/plans", "POST", data={"targets": []}, csrf=False)[0] == 403
    connection = next(account for account in options["accounts"] if account["aws_account_id"] == "333333333333")["connections"][0]["id"]
    status, _, raw = admin.request(f"/api/v1/lightsail/catalogue?connection_id={connection}&region=us-east-1")
    assert status == 409 and json.loads(raw)["error"]["code"] == "CONNECTION_DISABLED"
    assert admin.request(f"/api/v1/lightsail/catalogue?connection_id={connection}&region=invalid")[0] == 400
    assert admin.request(f"/api/v1/lightsail/regions?connection_id={connection}")[0] == 409
    request = {"connection_id": connection, "region": "us-east-1", "zone": "us-east-1a", "blueprint_id": "debian_12", "bundle_id": "small_3_0", "ip_type": "dualstack", "key_pair_name": "", "names": "one-web\ntwo-web"}
    status, _, fragment = admin.request("/lightsail/plans", "POST", form=request, headers={"HX-Request": "true"})
    assert status == 409 and 'id="lightsail-form"' in fragment and "one-web\ntwo-web" in fragment and "<html" not in fragment
    password=ROOT_PASSWORD_FIXTURE
    invalid_login=request | {"login_method":"root_password","root_password":password,"root_password_confirm":"mismatched fixture 2026!"}
    status, _, fragment = admin.request("/lightsail/plans", "POST", form=invalid_login, headers={"HX-Request":"true"})
    assert status==400 and password not in fragment and "mismatched fixture" not in fragment and 'name="root_password"' in fragment
    plan = payload(admin, "/lightsail/plans/" + plan_id)
    assert plan["monthly_cents"] == 850 and len(plan["targets"]) == 2 and not plan["can_submit"]
    status, _, page = admin.request("/lightsail/plans/" + plan_id)
    assert status == 200 and "$8.50" in page and "无法查询" in page and "disabled" in page
    status, _, raw = admin.request(f"/api/v1/lightsail/plans/{plan_id}/submit", "POST", data={"idempotency_key": str(uuid.uuid4())})
    assert status == 409 and json.loads(raw)["error"]["code"] == "CLOUD_WRITES_DISABLED"
    reader = Client(admin.origin)
    assert reader.login("account-reader", MEMBER_PASSWORD)[0] == 200
    for path in ("/lightsail/create", "/api/v1/lightsail/options", "/api/v1/lightsail/catalogue?connection_id=" + connection + "&region=us-east-1", "/api/v1/lightsail/regions?connection_id=" + connection, "/api/v1/lightsail/plans/" + plan_id):
        assert reader.request(path)[0] == 403
    assert 'href="/lightsail/create"' not in reader.request("/resources")[2]
