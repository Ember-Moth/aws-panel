ALTER TABLE proxy_profiles ADD COLUMN protocol text NOT NULL DEFAULT 'socks5' CHECK (protocol IN ('socks5','http'));
ALTER TABLE aws_accounts ADD COLUMN default_route_mode text NOT NULL DEFAULT 'direct' CHECK (default_route_mode IN ('direct','socks5','http'));
ALTER TABLE aws_accounts ADD COLUMN default_proxy_id uuid REFERENCES proxy_profiles(id);
ALTER TABLE aws_accounts ADD CONSTRAINT accounts_default_proxy_check CHECK ((default_route_mode='direct' AND default_proxy_id IS NULL) OR (default_route_mode IN ('socks5','http') AND default_proxy_id IS NOT NULL));
-- Preserve explicit existing connection routes; only derive the default for new connections.
WITH common_routes AS (
 SELECT account_id,min(route_mode) AS route_mode,min(proxy_id::text) AS proxy_id
 FROM connections GROUP BY account_id
 HAVING count(DISTINCT (route_mode || '/' || coalesce(proxy_id::text,'')))=1
)
UPDATE aws_accounts a SET default_route_mode=r.route_mode,default_proxy_id=r.proxy_id::uuid FROM common_routes r WHERE r.account_id=a.id;
CREATE INDEX accounts_default_proxy_idx ON aws_accounts(default_proxy_id);
ALTER TABLE connections DROP CONSTRAINT connections_route_mode_check;
ALTER TABLE connections DROP CONSTRAINT connections_check;
ALTER TABLE connections ADD CONSTRAINT connections_route_mode_check CHECK (route_mode IN ('direct','socks5','http','inherit'));
ALTER TABLE connections ADD CONSTRAINT connections_route_proxy_check CHECK ((route_mode IN ('direct','inherit') AND proxy_id IS NULL) OR (route_mode IN ('socks5','http') AND proxy_id IS NOT NULL));
