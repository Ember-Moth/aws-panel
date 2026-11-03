CREATE TABLE lightsail_creations (
    step_id uuid PRIMARY KEY REFERENCES job_steps(id),
    job_id uuid NOT NULL REFERENCES jobs(id),
    account_id uuid NOT NULL REFERENCES aws_accounts(id),
    region text NOT NULL,
    name text NOT NULL,
    token uuid NOT NULL UNIQUE,
    spec_hash text NOT NULL,
    spec jsonb NOT NULL,
    state text NOT NULL CHECK(state IN ('prepared','dispatched','rejected','identified')),
    send_count integer NOT NULL DEFAULT 0,
    first_sent_at timestamptz,
    last_sent_at timestamptz,
    resource_arn text NOT NULL DEFAULT '',
    operation_ids jsonb NOT NULL DEFAULT '[]',
    aws_request_id text NOT NULL DEFAULT '',
    code text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX lightsail_creations_account_idx ON lightsail_creations(account_id,region,name);
