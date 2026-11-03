-- Refuse rollback while launch secrets exist instead of discarding credentials.
ALTER TABLE secrets DROP CONSTRAINT secrets_owner_type_check;
ALTER TABLE secrets ADD CONSTRAINT secrets_owner_type_check CHECK (owner_type IN ('connection','proxy'));
