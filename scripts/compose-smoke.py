#!/usr/bin/env python3
"""Build and verify isolated Compose deployments, backup/restore and recovery.

Uses only disposable credentials, generated project names and local diagnostics.
Requires Docker Compose and Python 3 on the machine running the Docker daemon.
"""
import argparse
import copy
import importlib.util
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "tests/e2e"))
from identity import Client, ADMIN_PASSWORD, MEMBER_PASSWORD
from accounts import check_accounts, FIXTURE_SECRETS
from operations import payload, submit_diagnostic

spec = importlib.util.spec_from_file_location("compose_backup", ROOT / "scripts/compose-backup.py")
maintenance = importlib.util.module_from_spec(spec)
spec.loader.exec_module(maintenance)


def port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", help="Reuse this existing application image instead of building one")
    parser.add_argument("--report", type=Path, help="Write a non-secret acceptance report")
    args = parser.parse_args()
    os.umask(0o077)
    # Shell environment takes precedence over --env-file; remove inherited
    # application variables so acceptance never selects operator credentials.
    for name in tuple(os.environ):
        if name.startswith("AWS_PANEL_") or name in ("COMPOSE_FILE", "COMPOSE_PROJECT_NAME", "COMPOSE_PROFILES", "COMPOSE_ENV_FILES"):
            os.environ.pop(name)
    suffix = uuid.uuid4().hex[:12]
    image = args.image or "aws-panel:acceptance-" + suffix
    report = {"image": image, "checks": [], "aws": "not contacted", "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())}
    projects = []
    sensitive = [ADMIN_PASSWORD, MEMBER_PASSWORD, *FIXTURE_SECRETS]

    def checked(name):
        report["checks"].append(name)
        print("PASS: " + name, flush=True)

    def wait_job(client, job_id, wanted="succeeded", timeout=90):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            detail = payload(client, "/jobs/" + job_id)
            if detail["job"]["status"] in ("succeeded", "failed", "partial_success", "cancelled", "needs_attention"):
                assert detail["job"]["status"] == wanted, (job_id, detail["job"]["status"])
                return detail
            time.sleep(0.25)
        raise AssertionError("task recovery timed out: " + job_id)

    def expected_failure(compose, command, contains=None):
        result = subprocess.run(compose.base + command, capture_output=True, timeout=60)
        assert result.returncode != 0, "command unexpectedly succeeded: " + command[0]
        if contains:
            assert contains in result.stdout + result.stderr, "unexpected rejection: " + command[0]

    def maintain(compose, operation, database_dir, key_dir, success=True, reason=None):
        command = [sys.executable, str(ROOT / "scripts/compose-backup.py"), operation,
                   "--project", compose.project, "--env-file", str(compose.env_file),
                   "--database-dir", str(database_dir), "--key-dir", str(key_dir)]
        result = subprocess.run(command, capture_output=True, timeout=180)
        if success and result.returncode:
            raise AssertionError(result.stderr.decode())
        if not success and result.returncode == 0:
            raise AssertionError("unsafe backup/restore unexpectedly succeeded")
        if reason is not None and reason not in result.stderr.decode():
            raise AssertionError("backup/restore failed for an unexpected reason")

    with tempfile.TemporaryDirectory(prefix="aws-panel-compose-") as directory:
        work = Path(directory)
        def project(label):
            http_port = port()
            env_file = work / (label + ".env")
            runtime_password, migration_password = secrets.token_hex(24), secrets.token_hex(24)
            sensitive.extend((runtime_password, migration_password))
            env_file.write_text(f"AWS_PANEL_DB_PASSWORD={runtime_password}\nAWS_PANEL_MIGRATION_DB_PASSWORD={migration_password}\nAWS_PANEL_HTTP_PORT={http_port}\nAWS_PANEL_PUBLIC_URL=http://127.0.0.1:{http_port}\nAWS_PANEL_IMAGE={image}\nAWS_PANEL_CLOUD_WRITES_ENABLED=false\nAWS_PANEL_DIAGNOSTICS=true\n")
            value = maintenance.Compose(argparse.Namespace(project="aws-panel-test-" + label + "-" + suffix, env_file=env_file, timeout=300))
            value.env_file, value.origin = env_file, f"http://127.0.0.1:{http_port}"
            projects.append(value)
            return value
        source, restored, upgrade = project("source"), project("restore"), project("upgrade")
        try:
            if not args.image:
                source.run(["build", "app"], stdout=None)
            report["image_id"] = subprocess.check_output(["docker", "image", "inspect", image, "--format", "{{.Id}}"], text=True).strip()
            source.run(["up", "-d", "--wait", "postgres", "redis"])
            expected_failure(source, ["run", "--rm", "--no-deps", "-T", "app", "run"], b"migrate up")
            source.run(["run", "--rm", "--no-deps", "-T", "migrate"])
            source.run(["run", "--rm", "--no-deps", "-T", "migrate"])
            source.tool("vault", "init")
            source.run(["run", "--rm", "--no-deps", "-T", "app", "admin", "init", "--username", "admin", "--password-stdin"], input=(ADMIN_PASSWORD + "\n").encode())
            source.run(["up", "-d", "--wait", "app"])
            assert source.run(["exec", "-T", "app", "id", "-u"]).strip() == b"65532"
            source.run(["exec", "-T", "app", "/bin/sh", "-ec", 'test -r "$AWS_PANEL_KEYRING_FILE"; if touch /var/lib/aws-panel/keys/forbidden-write 2>/dev/null; then rm /var/lib/aws-panel/keys/forbidden-write; exit 1; fi'])
            expected_failure(source, ["exec", "-T", "postgres", "psql", "-v", "ON_ERROR_STOP=1", "-U", "aws_panel", "-d", "aws_panel", "-c", "CREATE TABLE forbidden_runtime_ddl(id int)"], b"permission denied")
            expected_failure(source, ["exec", "-T", "postgres", "psql", "-v", "ON_ERROR_STOP=1", "-U", "aws_panel", "-d", "aws_panel", "-c", "UPDATE audit_events SET action='forbidden' WHERE false"], b"permission denied")
            checked("empty-volume startup, idempotent migrations, UID 65532, read-only keyring and runtime privilege boundaries")
            admin = Client(source.origin)
            assert admin.login("admin", ADMIN_PASSWORD)[0] == 200
            check_accounts(admin)
            expected = source.sql("SELECT json_build_object('accounts',(SELECT count(*) FROM aws_accounts),'secrets',(SELECT count(*) FROM secrets),'members',(SELECT count(*) FROM members))")
            assert json.loads(expected)["secrets"] == 2
            source.tool("vault", "rotate")
            checked("container HTTP account flow and encrypted credential rotation")
            # A queue outage leaves a committed job/outbox without any cloud call.
            source.run(["stop", "redis"])
            queued = submit_diagnostic(admin, "wait", 8)
            source.run(["stop", "app"])
            source.run(["up", "-d", "--wait", "redis"])
            assert source.sql("SELECT status FROM jobs WHERE id='" + queued["id"] + "'") == "queued"
            expected_failure(source, ["run", "--rm", "--no-deps", "-T", "--entrypoint", "/bin/sh", "app", "-ec", 'export AWS_PANEL_KEYRING_FILE=/tmp/wrong-keyring.json; aws-panel vault init >/dev/null; exec aws-panel run'], '凭据存储不可用'.encode())
            checked("Redis outage retains committed jobs; wrong keyring prevents container startup")
            database_dir, key_dir = work / "database-backup", work / "key-backup"
            maintain(source, "backup", database_dir, key_dir)
            maintain(source, "backup", database_dir, key_dir, success=False, reason="new directories")
            maintain(source, "restore", database_dir, key_dir, success=False, reason="empty database")
            checked("coherent offline backup; existing backups and populated databases are protected")
            restored.run(["up", "-d", "--wait", "postgres", "redis"])
            corrupt = work / "corrupt-backup"
            shutil.copytree(database_dir, corrupt)
            with (corrupt / "database.dump").open("ab") as stream:
                stream.write(b"corruption-fixture")
            maintain(restored, "restore", corrupt, key_dir, success=False, reason="checksum mismatch")
            assert restored.sql("SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public'") == "0"
            maintain(restored, "restore", database_dir, key_dir)
            assert json.loads(restored.sql("SELECT json_build_object('accounts',(SELECT count(*) FROM aws_accounts),'secrets',(SELECT count(*) FROM secrets),'members',(SELECT count(*) FROM members))")) == json.loads(expected)
            assert restored.sql("SELECT count(*) FROM sessions WHERE revoked_at IS NULL") == "0"
            assert restored.sql("SELECT count(*) FROM pg_roles WHERE rolname='aws_panel' AND (rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls)") == "0"
            restored.run(["up", "-d", "--wait", "app"])
            stale = Client(restored.origin)
            for cookie in admin.jar:
                stale.jar.set_cookie(copy.copy(cookie))
            assert stale.request("/api/v1/auth/session")[0] == 401
            recovered = Client(restored.origin)
            assert recovered.login("admin", ADMIN_PASSWORD)[0] == 200
            wait_job(recovered, queued["id"])
            assert restored.sql("SELECT count(*) FROM operations_diagnostic_effects") == "8"
            assert restored.sql("SELECT max(request_count) FROM operations_diagnostic_effects") == "1"
            checked("restore into fresh volumes: full decryption, preserved ACLs, old-session revocation and outbox recovery into empty Redis")
            maintain(restored, "backup", work / "active-db", work / "active-key", success=False, reason="Stop the application")
            # Kill a running container; persisted effects must be reconciled.
            interrupted = submit_diagnostic(recovered, "wait", 8)
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline:
                detail = payload(recovered, "/jobs/" + interrupted["id"])
                if detail["job"]["status"] in ("running", "waiting"):
                    break
                time.sleep(0.05)
            assert detail["job"]["status"] in ("running", "waiting")
            restored.run(["stop", "-t", "0", "app"])
            restored.run(["up", "-d", "--wait", "app"])
            assert recovered.request("/api/v1/auth/session")[0] == 200
            wait_job(recovered, interrupted["id"])
            assert restored.sql("SELECT max(request_count) FROM operations_diagnostic_effects") == "1"
            checked("forced container stop/restart recovers pending work without repeating its recorded effect")
            # Persist an unavailable executor version while delivery is offline.
            restored.run(["stop", "redis"])
            incompatible = submit_diagnostic(recovered, "success", 1)
            restored.run(["stop", "app"])
            restored.sql("UPDATE jobs SET executor_version=999 WHERE id='" + incompatible["id"] + "'")
            restored.run(["up", "-d", "--wait", "redis", "app"])
            detail = wait_job(recovered, incompatible["id"], "needs_attention")
            assert detail["items"][0]["steps"][0]["code"] == "EXECUTOR_UNAVAILABLE"
            checked("unsupported persisted executor version remains needs_attention")
            # Reconstruct the real M0 schema, then run the current migration CLI.
            upgrade.run(["up", "-d", "--wait", "postgres"])
            sql = (ROOT / "internal/app/migration/schema/000001_platform.up.sql").read_bytes()
            sql += b"\nCREATE TABLE schema_migrations(version bigint PRIMARY KEY,dirty boolean NOT NULL); INSERT INTO schema_migrations VALUES(1,false); INSERT INTO platform_settings(key,value) VALUES('upgrade-fixture','{\"preserved\":true}');"
            upgrade.run(["exec", "-T", "postgres", "psql", "-v", "ON_ERROR_STOP=1", "-U", "aws_panel_migrator", "-d", "aws_panel"], input=sql)
            upgrade.run(["run", "--rm", "--no-deps", "-T", "migrate"])
            upgrade.tool("migrate", "status")
            assert upgrade.sql("SELECT value->>'preserved' FROM platform_settings WHERE key='upgrade-fixture'") == "true"
            checked("M0 schema upgrades to the current schema while preserving existing data")
            for compose in (source, restored):
                logs = compose.run(["logs", "--no-color", "app"]).decode()
                assert all(value not in logs for value in sensitive), "secret appeared in application logs"
            report["status"] = "passed"
            print("PASS: Compose acceptance complete. No real AWS requests or resources.", flush=True)
        except Exception as error:
            report["status"] = "failed"
            report["error"] = str(error)
            for value in sensitive:
                report["error"] = report["error"].replace(value, "[redacted fixture]")
            print("FAIL: " + report["error"], file=sys.stderr, flush=True)
        finally:
            for compose in reversed(projects):
                try:
                    compose.run(["down", "--volumes", "--remove-orphans"])
                except Exception:
                    print("Cleanup required for project " + compose.project, file=sys.stderr)
                    report["status"] = "failed"
            if not args.image:
                subprocess.run(["docker", "image", "rm", image], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            if args.report:
                args.report.parent.mkdir(parents=True, exist_ok=True)
                args.report.write_text(json.dumps(report, indent=2) + "\n")
    return 0 if report.get("status") == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
