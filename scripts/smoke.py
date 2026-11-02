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
import tempfile
import time
import urllib.error
import urllib.request

root = Path(__file__).resolve().parents[1]
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
        subprocess.run(["go", "test", "-count=1", "./tests/integration"], cwd=root, env=environment, check=True)
        app_log = (work / "app.log").open("w")
        app = subprocess.Popen([str(binary), "run"], cwd=root, env=environment, stdout=app_log, stderr=subprocess.STDOUT)
        origin = f"http://127.0.0.1:{http_port}"
        deadline = time.monotonic() + 25
        while True:
            if app.poll() is not None:
                raise RuntimeError("Application exited during startup: " + (work / "app.log").read_text())
            try:
                _, raw = get(origin, "/api/v1/system")
                status = json.loads(raw)["data"]
                if status["worker_heartbeat"]:
                    break
            except (OSError, urllib.error.HTTPError):
                pass
            if time.monotonic() > deadline:
                raise RuntimeError("Worker did not record its startup heartbeat")
            time.sleep(0.2)
        assert status["timezone"] == "Asia/Shanghai"
        assert status["server_time"].endswith("+08:00")
        assert get(origin, "/health/ready")[0] == 200
        page = get(origin, "/")[1].decode()
        assert 'hx-get="/partials/system-status"' in page and "<html" in page
        partial = get(origin, "/partials/system-status")[1].decode()
        assert 'id="system-status"' in partial and "<html" not in partial
        assert "运行正常" in partial
        for asset in ("htmx.min.js", "tabler.min.css", "tabler.min.js"):
            assert get(origin, "/static/vendor/" + asset)[0] == 200
        if options.preview:
            print(f"Local preview: {origin} (temporary database; Ctrl-C cleans up)", flush=True)
            try:
                while True:
                    time.sleep(1)
            except KeyboardInterrupt:
                pass
        app.send_signal(signal.SIGTERM)
        assert app.wait(timeout=30) == 0, "application did not stop cleanly"
        print("PASS: migration gate, PostgreSQL timezone, queue heartbeat, HTML/htmx, assets and graceful shutdown.")
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
