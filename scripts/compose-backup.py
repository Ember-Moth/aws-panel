#!/usr/bin/env python3
"""Offline PostgreSQL/keyring backup and empty-project restore for deploy/compose.yaml.

The database and keyring destinations must be separate private directories.
Restore never starts the application and refuses existing application data.
"""
import argparse
from contextlib import contextmanager
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import select
import stat
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[1]
# Must match internal/repository/kernel.CloudConfigurationLock. Vault rotation
# holds this lock before replacing the external key file or database ciphertext.
VAULT_LOCK = 92842002


class Failure(Exception):
    pass


def private_file(path):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077:
        raise Failure(f"Expected a private regular file (0600): {path}")


def digest(path):
    value = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            value.update(block)
    return value.hexdigest()


def write_private(path, body):
    with path.open("xb") as stream:
        os.fchmod(stream.fileno(), 0o600)
        stream.write(body)
        stream.flush()
        os.fsync(stream.fileno())


class Compose:
    def __init__(self, args):
        if not re.fullmatch(r"[a-z0-9][a-z0-9_-]{0,62}", args.project):
            raise Failure("Invalid Compose project name")
        self.project = args.project
        self.timeout = args.timeout
        self.base = ["docker", "compose", "--project-name", args.project,
                     "--env-file", str(args.env_file.resolve()), "-f", str(ROOT / "deploy/compose.yaml")]

    def run(self, args, *, input=None, stdout=subprocess.PIPE, stdin=None):
        result = subprocess.run(self.base + args, input=input, stdin=stdin, stdout=stdout,
                                stderr=subprocess.PIPE, timeout=self.timeout)
        if result.returncode:
            # Never echo a command's output: keyring export uses stdout.
            raise Failure(f"Compose operation failed: {' '.join(args[:6])} (exit {result.returncode})")
        return result.stdout

    def sql(self, query):
        return self.run(["exec", "-T", "postgres", "psql", "-X", "-qAt", "-v", "ON_ERROR_STOP=1",
                         "-U", "aws_panel_migrator", "-d", "aws_panel", "-c", query]).decode().strip()

    def offline(self):
        result = subprocess.run(["docker", "ps", "--filter", "label=com.docker.compose.project=" + self.project,
                                 "--format", '{{.Label "com.docker.compose.service"}}'], capture_output=True, timeout=15, check=True)
        running = set(result.stdout.decode().splitlines())
        if running - {"postgres", "redis"}:
            raise Failure("Stop the application and all maintenance/worker containers before this operation")
        if not {"postgres", "redis"}.issubset(running):
            raise Failure("Start only this project's PostgreSQL and Redis services first")
        if self.sql("SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND usename='aws_panel'") != "0":
            raise Failure("Runtime database sessions remain active; stop all application writers first")

    def tool(self, *command):
        return self.run(["run", "--rm", "--no-deps", "-T", "vault", *command])

    def shell(self, command, *, input=None):
        return self.run(["run", "--rm", "--no-deps", "-T", "--entrypoint", "/bin/sh", "vault", "-ec", command], input=input)

    @contextmanager
    def lock(self):
        command = self.base + ["exec", "-T", "postgres", "psql", "-X", "-qAt", "-v", "ON_ERROR_STOP=1", "-U", "aws_panel_migrator", "-d", "aws_panel"]
        process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, bufsize=0)
        try:
            process.stdin.write(f"SET lock_timeout='10s'; SELECT pg_advisory_lock({VAULT_LOCK}); SELECT 'BACKUP_LOCKED';\n".encode())
            process.stdin.flush()
            output = b""
            deadline = time.monotonic() + 20
            while b"BACKUP_LOCKED\n" not in output:
                if process.poll() is not None or time.monotonic() >= deadline:
                    raise Failure("Could not obtain the vault/database backup lock")
                ready, _, _ = select.select([process.stdout], [], [], 1)
                if ready:
                    chunk = os.read(process.stdout.fileno(), 1024)
                    if not chunk:
                        raise Failure("Backup lock connection closed")
                    output += chunk
            yield process
            if process.poll() is not None:
                raise Failure("Backup lock was lost; backup is incomplete")
        finally:
            if process.poll() is None:
                process.stdin.close()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.terminate()
                    process.wait(timeout=5)
            process.stdout.close()


def backup(compose, args):
    compose.offline()
    compose.tool("migrate", "status")
    compose.tool("vault", "status")
    database_dir = args.database_dir.resolve()
    key_dir = args.key_dir.resolve()
    if database_dir == key_dir:
        raise Failure("Use separate database and keyring destinations")
    if database_dir.exists() or key_dir.exists():
        raise Failure("Backup destinations must be new directories; existing backups are never overwritten")
    database_dir.mkdir(parents=True, mode=0o700)
    key_dir.mkdir(parents=True, mode=0o700)
    archive = database_dir / "database.dump"
    keyring = key_dir / "keyring.json"
    with compose.lock():
        schema = compose.sql("SELECT version FROM schema_migrations WHERE NOT dirty")
        if not schema.isdecimal():
            raise Failure("Database schema is missing or dirty")
        with archive.open("xb") as stream:
            os.fchmod(stream.fileno(), 0o600)
            # Preserve ownership and ACLs, including the append-only audit rules.
            compose.run(["exec", "-T", "postgres", "pg_dump", "-U", "aws_panel_migrator", "-d", "aws_panel", "--format=custom"], stdout=stream)
            stream.flush()
            os.fsync(stream.fileno())
        write_private(keyring, compose.shell('cat "$AWS_PANEL_KEYRING_FILE"'))
    metadata = {"format": 1, "created_at": datetime.now(timezone.utc).isoformat(), "schema": int(schema),
                "database_sha256": digest(archive), "keyring_sha256": digest(keyring),
                "application": compose.tool("version").decode().strip()}
    # Manifest is the completion marker; failed backups deliberately have none.
    write_private(database_dir / "manifest.json", (json.dumps(metadata, indent=2) + "\n").encode())
    print(f"Backup complete. Database: {database_dir}; keyring: {key_dir}")
    print("Keep these private destinations separately. Application remains stopped.")


def restore(compose, args):
    database_dir, key_dir = args.database_dir.resolve(), args.key_dir.resolve()
    manifest, archive, keyring = database_dir / "manifest.json", database_dir / "database.dump", key_dir / "keyring.json"
    for path in (manifest, archive, keyring):
        private_file(path)
    metadata = json.loads(manifest.read_bytes())
    if not isinstance(metadata, dict) or type(metadata.get("schema")) is not int or metadata["schema"] < 1:
        raise Failure("Invalid backup manifest; target was not changed")
    if type(metadata.get("format")) is not int or metadata["format"] != 1 or digest(archive) != metadata.get("database_sha256") or digest(keyring) != metadata.get("keyring_sha256"):
        raise Failure("Backup format/checksum mismatch; target was not changed")
    compose.offline()
    resolved = json.loads(compose.run(["config", "--format", "json"]))
    enabled = str(resolved["services"]["app"]["environment"].get("AWS_PANEL_CLOUD_WRITES_ENABLED", "")).lower()
    if enabled != "false":
        raise Failure("Restore requires AWS_PANEL_CLOUD_WRITES_ENABLED=false")
    if compose.sql("SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'") != "0":
        raise Failure("Restore requires an empty database; use a new Compose project/volume")
    if compose.run(["exec", "-T", "redis", "redis-cli", "DBSIZE"]).strip() != b"0":
        raise Failure("Restore requires an empty Redis instance")
    compose.shell('test ! -e "$AWS_PANEL_KEYRING_FILE"')
    # postgres-init.sh grants DML through default privileges. If those defaults
    # remain during pg_restore, CREATE TABLE inherits UPDATE/DELETE and the
    # archive's narrower GRANT SELECT,INSERT does not revoke them. Restore the
    # archive's object ACLs and default ACLs into a neutral empty target.
    compose.sql("ALTER DEFAULT PRIVILEGES FOR ROLE aws_panel_migrator IN SCHEMA public REVOKE SELECT,INSERT,UPDATE,DELETE ON TABLES FROM aws_panel; ALTER DEFAULT PRIVILEGES FOR ROLE aws_panel_migrator IN SCHEMA public REVOKE USAGE,SELECT ON SEQUENCES FROM aws_panel")
    # Exclusive creation avoids replacing a keyring if another process wins.
    compose.shell('umask 077; set -C; cat > "$AWS_PANEL_KEYRING_FILE"', input=keyring.read_bytes())
    with archive.open("rb") as stream:
        compose.run(["exec", "-T", "postgres", "pg_restore", "--exit-on-error", "--single-transaction", "--clean", "--if-exists", "-U", "aws_panel_migrator", "-d", "aws_panel"], stdin=stream)
    # A backup may predate a logout/password change. Do not revive old sessions.
    compose.sql("UPDATE sessions SET revoked_at=clock_timestamp() WHERE revoked_at IS NULL")
    if compose.sql("SELECT version FROM schema_migrations WHERE NOT dirty") != str(metadata["schema"]):
        raise Failure("Restored schema does not match the backup manifest")
    compose.tool("migrate", "status")
    compose.tool("vault", "status")
    privileges = compose.sql("SELECT has_table_privilege('aws_panel','audit_events','SELECT') AND has_table_privilege('aws_panel','audit_events','INSERT') AND NOT has_table_privilege('aws_panel','audit_events','UPDATE') AND NOT has_table_privilege('aws_panel','audit_events','DELETE') AND NOT has_table_privilege('aws_panel','schema_migrations','INSERT,UPDATE,DELETE')")
    if privileges != "t":
        raise Failure("Restored runtime privileges are not safe; keep application stopped")
    print("Restore verified: schema, decryption and audit privileges. Old sessions revoked.")
    print("Application remains stopped. Start with cloud writes disabled, reverify connections and reconcile resources before enabling writes.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("backup", "restore"))
    parser.add_argument("--project", required=True)
    parser.add_argument("--env-file", required=True, type=Path)
    parser.add_argument("--database-dir", required=True, type=Path)
    parser.add_argument("--key-dir", required=True, type=Path)
    parser.add_argument("--timeout", type=int, default=1200, help="Per-command timeout in seconds")
    args = parser.parse_args()
    if args.timeout < 10 or args.timeout > 86400:
        parser.error("timeout must be between 10 and 86400 seconds")
    os.umask(0o077)
    try:
        (backup if args.operation == "backup" else restore)(Compose(args), args)
    except (Failure, OSError, ValueError, subprocess.SubprocessError) as error:
        # Never print raw subprocess output or key material.
        print(f"Backup/restore stopped: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
