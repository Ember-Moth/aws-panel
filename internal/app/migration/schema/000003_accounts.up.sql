CREATE TABLE aws_accounts (
    id uuid PRIMARY KEY,
    partition text NOT NULL CHECK (partition IN ('aws','aws-cn','aws-us-gov')),
    aws_account_id text NOT NULL CHECK (aws_account_id ~ '^[0-9]{12}$'),
    name text NOT NULL,
    active boolean NOT NULL DEFAULT true,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    deleted_at timestamptz
);
CREATE UNIQUE INDEX aws_accounts_identity_idx ON aws_accounts(partition,aws_account_id) WHERE deleted_at IS NULL;
CREATE TABLE secrets (
    id uuid PRIMARY KEY,
    owner_type text NOT NULL CHECK (owner_type IN ('connection','proxy')),
    owner_id uuid NOT NULL,
    purpose text NOT NULL,
    format integer NOT NULL CHECK (format=1),
    key_id text NOT NULL,
    nonce bytea NOT NULL,
    ciphertext bytea NOT NULL,
    revision bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL,
    UNIQUE(owner_type,owner_id,purpose)
);
CREATE INDEX secrets_key_id_idx ON secrets(key_id);
CREATE TABLE proxy_profiles (
    id uuid PRIMARY KEY,
    name text NOT NULL,
    host text NOT NULL,
    port integer NOT NULL CHECK (port BETWEEN 1 AND 65535),
    secret_id uuid REFERENCES secrets(id),
    active boolean NOT NULL DEFAULT true,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE TABLE connections (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES aws_accounts(id),
    name text NOT NULL,
    auth_type text NOT NULL CHECK (auth_type IN ('access_key','temporary','assume_role')),
    region text NOT NULL,
    route_mode text NOT NULL CHECK (route_mode IN ('direct','socks5')),
    proxy_id uuid REFERENCES proxy_profiles(id),
    source_connection_id uuid REFERENCES connections(id),
    role_arn text NOT NULL DEFAULT '',
    secret_id uuid NOT NULL REFERENCES secrets(id),
    access_key_suffix text NOT NULL DEFAULT '',
    credential_expires_at timestamptz,
    active boolean NOT NULL DEFAULT true,
    version bigint NOT NULL DEFAULT 1,
    verification_status text NOT NULL DEFAULT 'pending',
    verification_stage text NOT NULL DEFAULT '',
    verification_code text NOT NULL DEFAULT '',
    verification_fingerprint text NOT NULL DEFAULT '',
    principal_arn text NOT NULL DEFAULT '',
    verified_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK ((route_mode='direct' AND proxy_id IS NULL) OR (route_mode='socks5' AND proxy_id IS NOT NULL)),
    CHECK ((auth_type='assume_role' AND source_connection_id IS NOT NULL AND role_arn<>'') OR (auth_type<>'assume_role' AND source_connection_id IS NULL AND role_arn=''))
);
CREATE INDEX connections_account_idx ON connections(account_id);
CREATE INDEX connections_proxy_idx ON connections(proxy_id);
CREATE INDEX connections_source_idx ON connections(source_connection_id);
CREATE TABLE account_grants (
    member_id bigint NOT NULL REFERENCES members(id),
    account_id uuid NOT NULL REFERENCES aws_accounts(id),
    access text NOT NULL CHECK (access IN ('read','operate')),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY(member_id,account_id)
);
