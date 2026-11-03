CREATE TABLE dashboard_snapshots (
 account_id uuid NOT NULL REFERENCES aws_accounts(id), region text NOT NULL,kind text NOT NULL,
 connection_id text NOT NULL,fingerprint text NOT NULL,status text NOT NULL,code text NOT NULL DEFAULT '',
 data jsonb NOT NULL DEFAULT '{}'::jsonb,last_attempt_at timestamptz NOT NULL,last_success_at timestamptz,
 PRIMARY KEY(account_id,region,kind)
);
CREATE TABLE dashboard_runs (
 step_id uuid PRIMARY KEY REFERENCES job_steps(id),job_id uuid NOT NULL REFERENCES jobs(id),account_id uuid NOT NULL REFERENCES aws_accounts(id),
 connection_id text NOT NULL,fingerprint text NOT NULL,spec_hash text NOT NULL,catalog jsonb NOT NULL,
 next_region integer NOT NULL DEFAULT 0,failures integer NOT NULL DEFAULT 0,
 started_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,finished_at timestamptz
);
CREATE INDEX dashboard_runs_account_idx ON dashboard_runs(account_id,started_at DESC);
