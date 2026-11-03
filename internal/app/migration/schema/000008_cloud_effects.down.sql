ALTER TABLE inventory_scopes DROP CONSTRAINT inventory_scopes_kind_check;
ALTER TABLE inventory_scopes ADD CONSTRAINT inventory_scopes_kind_check CHECK (kind IN ('account_region','lightsail_region','lightsail_instance'));
DROP TABLE operation_effects;
DROP TABLE operation_target_locks;
ALTER TABLE job_items DROP COLUMN lock_keys;
