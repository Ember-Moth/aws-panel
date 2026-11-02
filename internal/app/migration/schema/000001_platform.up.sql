CREATE TABLE platform_settings (
    key text PRIMARY KEY,
    value jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO platform_settings (key, value)
VALUES ('runtime', '{"name":"AWS Panel"}'::jsonb);

CREATE TABLE platform_heartbeats (
    component text PRIMARY KEY,
    observed_at timestamptz NOT NULL
);
