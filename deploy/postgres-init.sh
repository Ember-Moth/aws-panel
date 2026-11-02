#!/usr/bin/env sh
set -eu
# psql reads the secret directly from its environment, not command-line args.
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<'SQL'
\getenv runtime_password AWS_PANEL_DB_PASSWORD
\getenv database_name POSTGRES_DB
CREATE ROLE aws_panel LOGIN PASSWORD :'runtime_password';
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT CONNECT ON DATABASE :"database_name" TO aws_panel;
GRANT USAGE ON SCHEMA public TO aws_panel;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO aws_panel;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO aws_panel;
SQL
