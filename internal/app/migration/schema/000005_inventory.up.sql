CREATE TABLE inventory_scopes (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES aws_accounts(id),
    kind text NOT NULL CHECK (kind IN ('account_region','lightsail_region','lightsail_instance')),
    region text NOT NULL,
    generation bigint NOT NULL DEFAULT 0,
    active_run_id uuid,
    status text NOT NULL DEFAULT 'never',
    last_attempt_at timestamptz,
    last_success_at timestamptz,
    last_error_code text NOT NULL DEFAULT '',
    last_connection_id text NOT NULL DEFAULT '',
    resource_count integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL,
    UNIQUE(account_id,kind,region)
);
CREATE TABLE inventory_runs (
    id uuid PRIMARY KEY,
    scope_id uuid NOT NULL REFERENCES inventory_scopes(id),
    job_id uuid NOT NULL,
    step_id uuid NOT NULL,
    lease_epoch bigint NOT NULL,
    generation bigint NOT NULL,
    connection_id text NOT NULL,
    fingerprint text NOT NULL,
    status text NOT NULL,
    code text NOT NULL DEFAULT '',
    page_count integer NOT NULL DEFAULT 0,
    resource_count integer NOT NULL DEFAULT 0,
    aws_request_id text NOT NULL DEFAULT '',
    started_at timestamptz NOT NULL,
    finished_at timestamptz,
    UNIQUE(step_id,lease_epoch)
);
CREATE INDEX inventory_runs_step_idx ON inventory_runs(step_id,started_at DESC);
CREATE TABLE resource_snapshots (
    id uuid PRIMARY KEY,
    scope_id uuid NOT NULL REFERENCES inventory_scopes(id),
    resource_id text NOT NULL,
    name text NOT NULL,
    region text NOT NULL,
    state text NOT NULL,
    details jsonb NOT NULL,
    observed_at timestamptz NOT NULL,
    present boolean NOT NULL DEFAULT true,
    missing_since timestamptz,
    generation bigint NOT NULL,
    UNIQUE(scope_id,resource_id)
);
CREATE INDEX resource_snapshots_scope_idx ON resource_snapshots(scope_id,present,name,id);
CREATE TABLE inventory_schedules (
    id uuid PRIMARY KEY,
    actor_id bigint NOT NULL REFERENCES members(id),
    name text NOT NULL,
    request jsonb NOT NULL,
    interval_seconds integer NOT NULL CHECK (interval_seconds BETWEEN 300 AND 86400),
    active boolean NOT NULL DEFAULT true,
    version bigint NOT NULL DEFAULT 1,
    next_run_at timestamptz NOT NULL,
    lease_owner text NOT NULL DEFAULT '',
    lease_until timestamptz,
    last_job_id uuid,
    last_error_code text NOT NULL DEFAULT '',
    last_run_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX inventory_schedules_due_idx ON inventory_schedules(next_run_at) WHERE active;
CREATE INDEX job_items_inventory_target_idx ON job_items(account_id,target_key,job_id);
