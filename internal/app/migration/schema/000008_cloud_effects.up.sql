ALTER TABLE job_items ADD COLUMN lock_keys jsonb NOT NULL DEFAULT '[]'::jsonb;
UPDATE job_items SET lock_keys=jsonb_build_array(lock_key) WHERE lock_key<>'';
CREATE TABLE operation_target_locks (
    lock_key text PRIMARY KEY,
    item_id uuid NOT NULL REFERENCES job_items(id) ON DELETE CASCADE
);
INSERT INTO operation_target_locks(lock_key,item_id)
SELECT lock_key,id FROM job_items WHERE lock_key<>'' AND status IN ('queued','running','waiting','needs_attention');
CREATE INDEX operation_target_locks_item_idx ON operation_target_locks(item_id);

CREATE TABLE operation_effects (
    step_id uuid PRIMARY KEY REFERENCES job_steps(id),
    job_id uuid NOT NULL REFERENCES jobs(id),
    account_id uuid NOT NULL REFERENCES aws_accounts(id),
    region text NOT NULL,
    purpose text NOT NULL,
    spec_hash text NOT NULL,
    token uuid NOT NULL UNIQUE,
    state text NOT NULL CHECK (state IN ('prepared','dispatched','rejected','confirmed')),
    send_count integer NOT NULL DEFAULT 0,
    first_sent_at timestamptz,
    last_sent_at timestamptz,
    resource_id text NOT NULL DEFAULT '',
    request_id text NOT NULL DEFAULT '',
    code text NOT NULL DEFAULT '',
    proof jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX operation_effects_account_idx ON operation_effects(account_id,region,purpose);

ALTER TABLE inventory_scopes DROP CONSTRAINT inventory_scopes_kind_check;
ALTER TABLE inventory_scopes ADD CONSTRAINT inventory_scopes_kind_check CHECK (kind IN ('account_region','lightsail_region','lightsail_instance','lightsail_static_ip','ec2_instance','ec2_address'));
