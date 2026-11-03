#!/usr/bin/env python3
"""Run real PostgreSQL/Redis smoke checks in temporary, loopback-only instances."""
from contextlib import closing
import argparse
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

root = Path(__file__).resolve().parents[1]
sys.path.insert(0,str(root/"tests"/"e2e"))
from identity import ADMIN_PASSWORD, MEMBER_PASSWORD, UPDATED_PASSWORD, check_identity
from accounts import check_accounts, FIXTURE_SECRETS
from operations import check_operations, wait_job
from inventory import check_inventory, check_regions
from lightsail import check_lightsail, ROOT_PASSWORD_FIXTURE
from compute import check_compute
from dashboard import check_dashboard
binary = root / "bin" / "aws-panel"
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--preview", action="store_true", help="keep the temporary instance open until Ctrl-C")
options = parser.parse_args()
for tool in ("initdb", "pg_ctl", "createdb", "psql", "redis-server", "go"):
    if not shutil.which(tool):
        raise SystemExit("Required local smoke-test tool is missing: " + tool)
if not binary.exists():
    raise SystemExit("Run make build first.")


def port():
    with closing(socket.socket()) as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def get(origin, path):
    with opener.open(origin + path, timeout=3) as response:
        return response.status, response.read()


with tempfile.TemporaryDirectory(prefix="aws-panel-smoke-", dir="/tmp") as directory:
    work = Path(directory)
    pgdata = work / "pg"
    sockets = work / "sockets"
    sockets.mkdir()
    pg_port, redis_port, http_port = port(), port(), port()
    environment = os.environ.copy()
    # Explicitly overwrite database and Redis credentials to avoid touching
    # the operator's configured services or loading their password files.
    environment.update({
        "AWS_PANEL_DB_HOST": "127.0.0.1", "AWS_PANEL_DB_PORT": str(pg_port),
        "AWS_PANEL_DB_NAME": "aws_panel_test", "AWS_PANEL_DB_USER": "aws_panel",
        "AWS_PANEL_DB_PASSWORD": "smoke-runtime-only", "AWS_PANEL_DB_PASSWORD_FILE": "",
        "AWS_PANEL_DB_SSL_MODE": "disable",
        "AWS_PANEL_REDIS_ADDRESS": f"127.0.0.1:{redis_port}",
        "AWS_PANEL_REDIS_PASSWORD": "", "AWS_PANEL_REDIS_PASSWORD_FILE": "",
        "AWS_PANEL_HTTP_ADDRESS": f"127.0.0.1:{http_port}",
        "AWS_PANEL_PUBLIC_URL": f"http://127.0.0.1:{http_port}",
        "AWS_PANEL_TRUSTED_PROXIES": "",
        "AWS_PANEL_KEYRING_FILE": str(work / "keyring.json"),
        "AWS_PANEL_DIAGNOSTICS": "true",
        "AWS_PANEL_CLOUD_WRITES_ENABLED": "false",
        "AWS_PANEL_TEST_REDIS_ADDRESS": f"127.0.0.1:{redis_port}",
        "AWS_PANEL_TEST_DB_PORT": str(pg_port),
        "AWS_PANEL_TEST_DB_NAME": "aws_panel_test", "AWS_PANEL_TEST_DB_PASSWORD": "smoke-runtime-only",
        "AWS_PANEL_TEST_MIGRATION_USER": "aws_panel_migrator", "AWS_PANEL_TEST_MIGRATION_PASSWORD": "",
        "TZ": "America/Los_Angeles",
    })
    app = None
    redis = None
    pg_started = False
    try:
        subprocess.run(["initdb", "-D", str(pgdata), "-U", "aws_panel_migrator", "--auth=trust", "--encoding=UTF8", "--no-locale"], check=True, capture_output=True)
        subprocess.run(["pg_ctl", "-D", str(pgdata), "-l", str(work / "postgres.log"), "-o", f"-h 127.0.0.1 -p {pg_port} -k {sockets}", "-w", "start"], check=True, capture_output=True)
        pg_started = True
        subprocess.run(["createdb", "-h", "127.0.0.1", "-p", str(pg_port), "-U", "aws_panel_migrator", "aws_panel_test"], check=True, capture_output=True)
        init_environment = environment | {"POSTGRES_USER": "aws_panel_migrator", "POSTGRES_DB": "aws_panel_test", "PGHOST": "127.0.0.1", "PGPORT": str(pg_port)}
        subprocess.run(["sh", "deploy/postgres-init.sh"], cwd=root, env=init_environment, check=True, capture_output=True)
        redis_log = (work / "redis.log").open("w")
        redis = subprocess.Popen(["redis-server", "--bind", "127.0.0.1", "--port", str(redis_port), "--save", "", "--appendonly", "no", "--dir", str(work)], stdout=redis_log, stderr=subprocess.STDOUT)
        missing_schema = subprocess.run([str(binary), "run"], env=environment, cwd=root, capture_output=True, text=True, timeout=20)
        assert missing_schema.returncode != 0 and "migrate up" in missing_schema.stderr, "unexpected pre-migration startup result: " + missing_schema.stdout + missing_schema.stderr
        migration_environment = environment | {"AWS_PANEL_DB_USER": "aws_panel_migrator", "AWS_PANEL_DB_PASSWORD": ""}
        subprocess.run([str(binary), "migrate", "up"], cwd=root, env=migration_environment, check=True)
        subprocess.run([str(binary), "migrate", "status"], cwd=root, env=environment, check=True)
        subprocess.run(["go", "test", "-race", "-count=1", "./tests/integration", "./internal/auth/ratelimit"], cwd=root, env=environment, check=True)
        subprocess.run([str(binary), "vault", "init"], cwd=root, env=environment, check=True)
        bootstrap = subprocess.run([str(binary),"admin","init","--username","admin","--display-name","测试管理员","--password-stdin"],cwd=root,env=environment,input=ADMIN_PASSWORD+"\n",text=True,capture_output=True)
        assert bootstrap.returncode == 0 and ADMIN_PASSWORD not in bootstrap.stdout + bootstrap.stderr, "administrator bootstrap failed"
        repeated = subprocess.run([str(binary),"admin","init","--username","another","--password-stdin"],cwd=root,env=environment,input=ADMIN_PASSWORD+"\n",text=True,capture_output=True)
        assert repeated.returncode != 0, "administrator bootstrap is repeatable"
        app_log = (work / "app.log").open("w")
        app = subprocess.Popen([str(binary), "run"], cwd=root, env=environment, stdout=app_log, stderr=subprocess.STDOUT)
        origin = f"http://127.0.0.1:{http_port}"
        deadline = time.monotonic() + 25
        while True:
            if app.poll() is not None:
                raise RuntimeError("Application exited during startup: " + (work / "app.log").read_text())
            try:
                if get(origin, "/health/ready")[0] == 200:
                    break
            except (OSError, urllib.error.HTTPError):
                pass
            if time.monotonic() > deadline:
                raise RuntimeError("Application did not become ready")
            time.sleep(0.2)
        admin = check_identity(origin)
        check_accounts(admin)
        subprocess.run(["psql", "-h", "127.0.0.1", "-p", str(pg_port), "-U", "aws_panel_migrator", "-d", "aws_panel_test", "-v", "ON_ERROR_STOP=1", "-f", "tests/e2e/inventory-fixture.sql"], cwd=root, env=migration_environment, check=True, capture_output=True)
        check_inventory(admin)
        region_plan = subprocess.check_output(["psql", "-h", "127.0.0.1", "-p", str(pg_port), "-U", "aws_panel_migrator", "-d", "aws_panel_test", "-Atc", "SELECT id FROM plans WHERE operation='regions.enable' AND input_hash='browser-region-preview-only' LIMIT 1"], cwd=root, env=migration_environment, text=True).strip()
        check_regions(admin, region_plan)
        subprocess.run(["psql", "-h", "127.0.0.1", "-p", str(pg_port), "-U", "aws_panel_migrator", "-d", "aws_panel_test", "-v", "ON_ERROR_STOP=1", "-f", "tests/e2e/lightsail-fixture.sql"], cwd=root, env=migration_environment, check=True, capture_output=True)
        lightsail_plan = subprocess.check_output(["psql", "-h", "127.0.0.1", "-p", str(pg_port), "-U", "aws_panel_migrator", "-d", "aws_panel_test", "-Atc", "SELECT id FROM plans WHERE operation='lightsail.create' AND input_hash='browser-lightsail-preview-only' LIMIT 1"], cwd=root, env=migration_environment, text=True).strip()
        check_lightsail(admin, lightsail_plan)
        check_compute(admin)
        check_dashboard(admin)
        restart_job = check_operations(admin)
        for operation in ("status", "rotate", "reencrypt"):
            subprocess.run([str(binary), "vault", operation], cwd=root, env=environment, check=True)
        status_code, _, raw = admin.request("/api/v1/system")
        assert status_code == 200
        status = json.loads(raw)["data"]
        assert status["worker_heartbeat"], "Worker did not record its startup heartbeat"
        assert status["timezone"] == "Asia/Shanghai"
        assert status["server_time"].endswith("+08:00")
        assert get(origin, "/health/ready")[0] == 200
        page = admin.request("/")[2]
        assert 'hx-get="/partials/system-status"' in page and "<html" in page
        partial = admin.request("/partials/system-status")[2]
        assert 'id="system-status"' in partial and "<html" not in partial
        assert "运行正常" in partial
        for asset in ("htmx.min.js", "tabler.min.css", "tabler.min.js"):
            assert get(origin, "/static/vendor/" + asset)[0] == 200
        # Sessions must survive process restarts, while revoked sessions stay dead.
        app.send_signal(signal.SIGTERM)
        assert app.wait(timeout=30) == 0
        app = subprocess.Popen([str(binary),"run"],cwd=root,env=environment,stdout=app_log,stderr=subprocess.STDOUT)
        deadline = time.monotonic() + 20
        while True:
            try:
                if get(origin,"/health/ready")[0] == 200:
                    break
            except OSError:
                pass
            if time.monotonic() > deadline:
                raise RuntimeError("Application did not restart")
            time.sleep(0.2)
        assert admin.request("/api/v1/auth/session")[0] == 200, "persisted session lost on restart"
        wait_job(admin, restart_job)
        app_log.flush()
        logs = (work/"app.log").read_text()
        for secret in (ADMIN_PASSWORD,MEMBER_PASSWORD,UPDATED_PASSWORD,*FIXTURE_SECRETS,ROOT_PASSWORD_FIXTURE):
            assert secret not in logs, "password appeared in application logs"
        if options.preview:
            print(f"Local preview: {origin} (temporary database; Ctrl-C cleans up)", flush=True)
            print(f"Region preview fixture: {origin}/regions/plans/{region_plan} (cloud writes disabled)", flush=True)
            print(f"Lightsail preview fixture: {origin}/lightsail/plans/{lightsail_plan} (cloud writes disabled)", flush=True)
            print("Disposable login: admin; fixture password is defined in tests/e2e/identity.py",flush=True)
            try:
                while True:
                    time.sleep(1)
            except KeyboardInterrupt:
                pass
        app.send_signal(signal.SIGTERM)
        assert app.wait(timeout=30) == 0, "application did not stop cleanly"
        print("PASS: identity, accounts, vault, durable tasks, outbox/lease recovery, SSE revocation, real Asynq delivery and restart persistence. AWS is simulated.")
    finally:
        if app and app.poll() is None:
            app.terminate()
            try: app.wait(timeout=30)
            except subprocess.TimeoutExpired: app.kill(); app.wait()
        if redis and redis.poll() is None:
            redis.terminate()
            redis.wait(timeout=10)
        if pg_started:
            subprocess.run(["pg_ctl", "-D", str(pgdata), "-m", "fast", "-w", "stop"], capture_output=True)
