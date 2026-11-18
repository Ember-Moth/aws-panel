ALTER TABLE inventory_scopes DROP CONSTRAINT inventory_scopes_kind_check;
ALTER TABLE inventory_scopes ADD CONSTRAINT inventory_scopes_kind_check CHECK (kind IN ('account_region','lightsail_region','lightsail_instance','lightsail_static_ip','ec2_instance','ec2_address','lightsail_traffic','ec2_traffic'));
-- Daily network bytes per instance in UTC days. Rows are upserted by each
-- traffic sync and kept after AWS metric retention expires.
CREATE TABLE instance_traffic (
    scope_id uuid NOT NULL REFERENCES inventory_scopes(id),
    resource_id text NOT NULL,
    day date NOT NULL,
    name text NOT NULL,
    bundle_id text NOT NULL DEFAULT '',
    allowance_bytes bigint NOT NULL DEFAULT 0 CHECK (allowance_bytes >= 0),
    bytes_in bigint NOT NULL CHECK (bytes_in >= 0),
    bytes_out bigint NOT NULL CHECK (bytes_out >= 0),
    generation bigint NOT NULL,
    observed_at timestamptz NOT NULL,
    PRIMARY KEY (scope_id, resource_id, day)
);
CREATE INDEX instance_traffic_resource_idx ON instance_traffic(resource_id, day);
CREATE INDEX instance_traffic_day_idx ON instance_traffic(day, scope_id);
