DROP TABLE instance_traffic;
DELETE FROM inventory_runs WHERE scope_id IN (SELECT id FROM inventory_scopes WHERE kind IN ('lightsail_traffic','ec2_traffic'));
DELETE FROM inventory_scopes WHERE kind IN ('lightsail_traffic','ec2_traffic');
ALTER TABLE inventory_scopes DROP CONSTRAINT inventory_scopes_kind_check;
ALTER TABLE inventory_scopes ADD CONSTRAINT inventory_scopes_kind_check CHECK (kind IN ('account_region','lightsail_region','lightsail_instance','lightsail_static_ip','ec2_instance','ec2_address'));
