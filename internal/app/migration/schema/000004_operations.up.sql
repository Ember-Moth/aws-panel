CREATE TABLE plans (
    id uuid PRIMARY KEY,
    actor_id bigint NOT NULL REFERENCES members(id),
    operation text NOT NULL,
    executor_version integer NOT NULL,
    input_version integer NOT NULL,
    input jsonb NOT NULL,
    input_hash text NOT NULL,
    items jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX plans_expiry_idx ON plans(expires_at);
CREATE TABLE jobs (
    id uuid PRIMARY KEY,
    plan_id uuid NOT NULL UNIQUE REFERENCES plans(id),
    actor_id bigint NOT NULL REFERENCES members(id),
    actor_name text NOT NULL,
    operation text NOT NULL,
    title text NOT NULL,
    executor_version integer NOT NULL,
    input_version integer NOT NULL,
    input jsonb NOT NULL,
    input_hash text NOT NULL,
    idempotency_key text NOT NULL,
    writes_cloud boolean NOT NULL,
    diagnostic boolean NOT NULL,
    status text NOT NULL,
    cancel_requested boolean NOT NULL DEFAULT false,
    event_seq bigint NOT NULL DEFAULT 0,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    UNIQUE(actor_id, operation, idempotency_key)
);
CREATE INDEX jobs_created_idx ON jobs(created_at DESC, id);
CREATE TABLE job_items (
    id uuid PRIMARY KEY,
    job_id uuid NOT NULL REFERENCES jobs(id),
    ordinal integer NOT NULL,
    target_key text NOT NULL,
    lock_key text NOT NULL DEFAULT '',
    label text NOT NULL,
    account_id uuid REFERENCES aws_accounts(id),
    connection_id text NOT NULL,
    region text NOT NULL,
    binding jsonb NOT NULL,
    status text NOT NULL,
    UNIQUE(job_id, ordinal),
    UNIQUE(job_id, target_key)
);
CREATE INDEX job_items_account_idx ON job_items(account_id, job_id);
CREATE UNIQUE INDEX job_items_active_target_idx ON job_items(lock_key)
WHERE lock_key<>'' AND status IN ('queued','running','waiting','needs_attention');
CREATE TABLE job_steps (
    id uuid PRIMARY KEY,
    job_id uuid NOT NULL REFERENCES jobs(id),
    item_id uuid NOT NULL REFERENCES job_items(id),
    ordinal integer NOT NULL,
    kind text NOT NULL,
    label text NOT NULL,
    input jsonb NOT NULL,
    writes_cloud boolean NOT NULL,
    status text NOT NULL CHECK (status IN ('queued','running','waiting','succeeded','failed','needs_attention','cancelled')),
    mode text NOT NULL CHECK (mode IN ('execute','reconcile')),
    uncertain boolean NOT NULL DEFAULT false,
    idempotency_token uuid NOT NULL UNIQUE,
    generation bigint NOT NULL DEFAULT 0,
    attempt_count integer NOT NULL DEFAULT 0,
    attempt_limit integer NOT NULL,
    deadline_at timestamptz NOT NULL,
    next_run_at timestamptz,
    scheduled_at timestamptz,
    lease_owner text NOT NULL DEFAULT '',
    lease_epoch bigint NOT NULL DEFAULT 0,
    lease_expires_at timestamptz,
    code text NOT NULL DEFAULT '',
    result jsonb NOT NULL DEFAULT '{}',
    updated_at timestamptz NOT NULL,
    UNIQUE(item_id, ordinal)
);
CREATE INDEX job_steps_due_idx ON job_steps(next_run_at) WHERE status IN ('queued','waiting');
CREATE INDEX job_steps_lease_idx ON job_steps(lease_expires_at) WHERE status='running';
CREATE INDEX job_steps_job_idx ON job_steps(job_id, item_id, ordinal);
CREATE TABLE job_attempts (
    id bigserial PRIMARY KEY,
    job_id uuid NOT NULL REFERENCES jobs(id),
    step_id uuid NOT NULL REFERENCES job_steps(id),
    number integer NOT NULL,
    lease_epoch bigint NOT NULL,
    mode text NOT NULL,
    binding jsonb NOT NULL,
    started_at timestamptz NOT NULL,
    finished_at timestamptz,
    outcome text NOT NULL DEFAULT 'running',
    code text NOT NULL DEFAULT '',
    result jsonb NOT NULL DEFAULT '{}',
    UNIQUE(step_id, number)
);
CREATE TABLE job_events (
    job_id uuid NOT NULL REFERENCES jobs(id),
    seq bigint NOT NULL,
    type text NOT NULL,
    step_id uuid,
    item_id uuid,
    code text NOT NULL DEFAULT '',
    message text NOT NULL,
    occurred_at timestamptz NOT NULL,
    PRIMARY KEY(job_id, seq)
);
CREATE TABLE operations_outbox (
    id uuid PRIMARY KEY,
    job_id uuid NOT NULL REFERENCES jobs(id),
    step_id uuid NOT NULL REFERENCES job_steps(id),
    generation bigint NOT NULL,
    available_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    published_at timestamptz,
    publish_owner text NOT NULL DEFAULT '',
    publish_until timestamptz,
    attempts integer NOT NULL DEFAULT 0,
    last_error text NOT NULL DEFAULT '',
    UNIQUE(step_id, generation)
);
CREATE INDEX operations_outbox_due_idx ON operations_outbox(available_at) WHERE published_at IS NULL;
-- Only the explicitly enabled local task diagnostic uses these simulated effects.
CREATE TABLE operations_diagnostic_effects (
    token uuid PRIMARY KEY,
    request_count integer NOT NULL,
    ready_at timestamptz NOT NULL
);
