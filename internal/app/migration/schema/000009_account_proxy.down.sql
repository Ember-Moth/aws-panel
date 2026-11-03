ALTER TABLE connections DROP CONSTRAINT connections_route_proxy_check;
ALTER TABLE connections DROP CONSTRAINT connections_route_mode_check;
ALTER TABLE connections ADD CONSTRAINT connections_route_mode_check CHECK (route_mode IN ('direct','socks5'));
ALTER TABLE connections ADD CONSTRAINT connections_check CHECK ((route_mode='direct' AND proxy_id IS NULL) OR (route_mode='socks5' AND proxy_id IS NOT NULL));
ALTER TABLE proxy_profiles ADD CONSTRAINT rollback_socks5_only CHECK (protocol='socks5');
ALTER TABLE aws_accounts DROP CONSTRAINT accounts_default_proxy_check;
ALTER TABLE aws_accounts DROP COLUMN default_proxy_id;
ALTER TABLE aws_accounts DROP COLUMN default_route_mode;
ALTER TABLE proxy_profiles DROP COLUMN protocol;
