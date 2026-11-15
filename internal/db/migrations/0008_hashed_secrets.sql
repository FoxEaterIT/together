-- Bearer secrets are verified, never read back, so the database keeps only
-- their SHA-256 hash. token_prefix keeps the first characters so a person can
-- tell two personal tokens apart in the profile; it is not enough to use one.
-- Token values exist only in the HTTP response that creates them.
--
-- TOTP secrets must be recoverable to compute a code, so they cannot be hashed;
-- they are sealed with AES-256-GCM under data/secret.key and the column name
-- says so. The Go backfill in internal/auth rewrites existing rows on start.

ALTER TABLE api_tokens RENAME COLUMN token TO token_hash;
ALTER TABLE api_tokens ADD COLUMN token_prefix TEXT NOT NULL DEFAULT '';

ALTER TABLE invites RENAME COLUMN token TO token_hash;

ALTER TABLE user_totp RENAME COLUMN secret TO secret_enc;
