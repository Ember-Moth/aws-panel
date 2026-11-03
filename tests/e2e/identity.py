"""HTTP acceptance checks against a disposable, loopback-only application."""
import http.cookiejar
import json
import re
import urllib.error
import urllib.parse
import urllib.request

ADMIN_PASSWORD = "Smoke-admin-passphrase-2026!"
MEMBER_PASSWORD = "Smoke-member-passphrase-2026!"
UPDATED_PASSWORD = "Smoke-updated-passphrase-2026!"


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class Client:
    def __init__(self, origin):
        self.origin = origin
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}),
            urllib.request.HTTPCookieProcessor(self.jar),
            NoRedirect(),
        )
        self.csrf = None

    def request(self, path, method="GET", data=None, form=None, csrf=True, headers=None):
        request_headers = dict(headers or {})
        body = None
        if method not in ("GET", "HEAD"):
            request_headers.setdefault("Origin", self.origin)
            if csrf and self.csrf:
                request_headers.setdefault("X-CSRF-Token", self.csrf)
        if data is not None:
            body = json.dumps(data).encode()
            request_headers["Content-Type"] = "application/json"
        if form is not None:
            body = urllib.parse.urlencode(form).encode()
            request_headers["Content-Type"] = "application/x-www-form-urlencoded"
        request = urllib.request.Request(self.origin + path, data=body, headers=request_headers, method=method)
        try:
            response = self.opener.open(request, timeout=10)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            return response.status, response.headers, response.read().decode()

    def login(self, username, password):
        status, headers, body = self.request("/api/v1/auth/csrf")
        assert status == 200, "pre-login CSRF unavailable"
        if headers.get("Set-Cookie"):
            assert "HttpOnly" in headers.get("Set-Cookie", ""), "pre-login cookie is not HttpOnly"
        self.csrf = json.loads(body)["data"]["csrf_token"]
        status, headers, body = self.request("/api/v1/auth/login", "POST", data={"username": username, "password": password})
        if status == 200:
            self.csrf = json.loads(body)["data"]["csrf_token"]
        return status, headers, body


def check_identity(origin):
    anonymous = Client(origin)
    status, headers, body = anonymous.request("/")
    assert status == 303 and headers.get("Location") == "/login", "dashboard is public"
    for path in ("/api/v1/system", "/api/v1/members", "/api/v1/audit", "/partials/system-status"):
        status, headers, body = anonymous.request(path, headers={"HX-Request": "true", "X-Role": "admin"})
        assert status == 401 and headers.get("HX-Redirect") == "/login", "htmx bypasses authentication"
        assert "<html" not in body, "login document was embedded in a fragment"
    assert anonymous.request("/health/live")[0] == 200
    assert anonymous.request("/health/ready")[0] == 200
    admin = Client(origin)
    status, _, body = admin.request("/api/v1/auth/csrf")
    assert status == 200
    admin.csrf = json.loads(body)["data"]["csrf_token"]
    credentials = {"username": "admin", "password": ADMIN_PASSWORD}
    assert admin.request("/api/v1/auth/login", "POST", data=credentials, csrf=False)[0] == 403
    assert admin.request("/api/v1/auth/login", "POST", data=credentials, headers={"Origin": "https://unrelated.example.com"})[0] == 403
    status, headers, body = admin.request("/api/v1/auth/login", "POST", data=credentials, headers={"X-Forwarded-For": "8.8.8.8"})
    assert status == 200, "administrator could not sign in"
    assert "HttpOnly" in headers.get("Set-Cookie", "") and "SameSite=Lax" in headers.get("Set-Cookie", "")
    assert ADMIN_PASSWORD not in body and "password_hash" not in body
    admin_data = json.loads(body)["data"]
    assert admin_data["expires_at"].endswith("+08:00")
    assert admin_data["member"]["created_at"].endswith("+08:00")
    admin.csrf = admin_data["csrf_token"]
    administrator = admin_data["member"]
    assert admin.request("/api/v1/members")[0] == 200
    assert admin.request("/api/v1/members", "POST", data={}, csrf=False)[0] == 403
    assert admin.request("/api/v1/members", "POST", data={}, headers={"Origin": "https://unrelated.example.com"})[0] == 403
    last_admin = dict(display_name=administrator["display_name"], role="viewer", active=True, version=administrator["version"])
    assert admin.request("/api/v1/members/" + str(administrator["id"]), "PATCH", data=last_admin)[0] == 409

    def create(name, role):
        status, _, body = admin.request("/api/v1/members", "POST",
            data={"username": name, "display_name": name, "password": MEMBER_PASSWORD, "role": role})
        assert status == 201, "member creation failed"
        assert MEMBER_PASSWORD not in body and "password_hash" not in body
        return json.loads(body)["data"]

    viewer = create("viewer-http", "viewer")
    operator = create("operator-http", "operator")
    reader = Client(origin)
    assert reader.login(viewer["username"], MEMBER_PASSWORD)[0] == 200
    for method, path, data in (
        ("GET", "/api/v1/members", None), ("GET", "/api/v1/audit", None),
        ("POST", "/api/v1/members", {"username": "forged", "role": "admin"}),
        ("PATCH", "/api/v1/members/" + str(administrator["id"]), last_admin),
    ):
        assert reader.request(path, method, data=data, headers={"X-Role": "admin", "HX-Request": "true"})[0] == 403
    status, _, page = reader.request("/")
    assert status == 200 and 'href="/members"' not in page and 'href="/profile"' in page
    assert reader.request("/profile")[0] == 200

    invalid_form = {"username": "!", "display_name": "<script>alert(1)</script>", "password": MEMBER_PASSWORD, "role": "viewer", "_csrf": admin.csrf}
    status, _, page = admin.request("/members", "POST", form=invalid_form, headers={"HX-Request": "true"})
    assert status == 400 and 'id="member-form"' in page and "<html" not in page
    assert MEMBER_PASSWORD not in page and "<script>alert(1)</script>" not in page
    assert "&lt;script&gt;" in page
    valid_form = dict(invalid_form, username="form-member", display_name="表单成员")
    status, headers, body = admin.request("/members", "POST", form=valid_form, headers={"HX-Request": "true"})
    assert status == 200 and headers.get("HX-Redirect") == "/members", "HTML member creation failed"
    assert MEMBER_PASSWORD not in body
    stale_update = dict(display_name=viewer["display_name"], role="viewer", active=False, version=viewer["version"] + 99)
    assert admin.request("/api/v1/members/" + str(viewer["id"]), "PATCH", data=stale_update)[0] == 409
    update = dict(display_name=viewer["display_name"], role="viewer", active=False, version=viewer["version"])
    status, _, body = admin.request("/api/v1/members/" + str(viewer["id"]), "PATCH", data=update)
    assert status == 200
    viewer = json.loads(body)["data"]
    assert reader.request("/api/v1/system")[0] == 401, "disabled session remains valid"
    assert reader.login(viewer["username"], MEMBER_PASSWORD)[0] == 401, "disabled member can sign in"
    update.update(active=True, version=viewer["version"])
    status, _, body = admin.request("/api/v1/members/" + str(viewer["id"]), "PATCH", data=update)
    assert status == 200
    viewer = json.loads(body)["data"]
    assert reader.login(viewer["username"], MEMBER_PASSWORD)[0] == 200
    second_reader = Client(origin)
    assert second_reader.login(viewer["username"], MEMBER_PASSWORD)[0] == 200
    status, _, _ = reader.request("/api/v1/auth/password", "POST",
        data={"current_password": MEMBER_PASSWORD, "new_password": UPDATED_PASSWORD})
    assert status == 204
    assert reader.request("/api/v1/auth/session")[0] == 401
    assert second_reader.request("/api/v1/auth/session")[0] == 401, "password change failed to revoke every session"
    assert reader.login(viewer["username"], MEMBER_PASSWORD)[0] == 401
    assert reader.login(viewer["username"], UPDATED_PASSWORD)[0] == 200

    worker = Client(origin)
    assert worker.login(operator["username"], MEMBER_PASSWORD)[0] == 200
    update = dict(display_name=operator["display_name"], role="viewer", active=True, version=operator["version"])
    changed_status, _, changed_body = admin.request("/api/v1/members/" + str(operator["id"]), "PATCH", data=update)
    assert changed_status == 200
    operator = json.loads(changed_body)["data"]
    assert worker.request("/api/v1/system")[0] == 401, "role change left an old session valid"
    update.update(role="operator", version=operator["version"])
    assert admin.request("/api/v1/members/" + str(operator["id"]), "PATCH", data=update)[0] == 200
    assert reader.request("/api/v1/auth/logout", "POST")[0] == 204
    assert reader.request("/api/v1/system")[0] == 401

    # Exercise the HTML login route independently of the JSON client.
    html_client = Client(origin)
    status, _, page = html_client.request("/login")
    assert status == 200
    match = re.search(r'name="_csrf" value="([^"]+)"', page)
    assert match, "login form has no CSRF field"
    form = {"username": "admin", "password": ADMIN_PASSWORD, "_csrf": match.group(1)}
    status, headers, _ = html_client.request("/login", "POST", form=form, csrf=False, headers={"HX-Request": "true"})
    assert status == 200 and headers.get("HX-Redirect") == "/", "HTML sign-in did not navigate"
    assert html_client.request("/members")[0] == 200
    assert html_client.request("/members/new")[0] == 200
    assert html_client.request("/members/" + str(viewer["id"]) + "/edit")[0] == 200
    assert html_client.request("/audit")[0] == 200

    before = None
    entries = []
    while True:
        path = "/api/v1/audit" + (("?before=" + str(before)) if before else "")
        status, _, body = admin.request(path)
        assert status == 200
        for secret in (ADMIN_PASSWORD, MEMBER_PASSWORD, UPDATED_PASSWORD, "$argon2id$"):
            assert secret not in body, "secret appeared in audit API"
        payload = json.loads(body)["data"]
        entries.extend(payload["items"])
        before = payload.get("next_before")
        if not before:
            break
    assert any(entry["action"] == "identity.password.changed" for entry in entries)
    assert any(entry["action"] == "identity.member.updated" for entry in entries)
    assert any(entry["action"] == "identity.login" and entry["outcome"] == "failure" for entry in entries)
    successful_logins = [entry for entry in entries if entry["action"] == "identity.login" and entry["outcome"] == "success"]
    assert successful_logins and all(entry["client_ip"] == "127.0.0.1" for entry in successful_logins), "untrusted forwarded IP was used"
    return admin
