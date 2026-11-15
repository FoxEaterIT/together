-- 0006_totp
-- 2FA TOTP enrollment, single-use recovery codes and short-lived login
-- challenges. The TOTP secret is stored in clear text on purpose: the SQLite
-- file is already the trust boundary of a self-hosted installation, exactly
-- like API tokens. It is never returned after confirmation.

CREATE TABLE user_totp (
    user_id INTEGER PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    secret TEXT NOT NULL,
    confirmed_at TEXT,
    last_used_step INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);

CREATE TABLE user_recovery_codes (
    id INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    code_hash TEXT NOT NULL,
    used_at TEXT,
    created_at TEXT NOT NULL,
    UNIQUE (user_id, code_hash)
);

CREATE INDEX user_recovery_codes_user ON user_recovery_codes (user_id, used_at);

CREATE TABLE login_challenges (
    id INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    scope TEXT NOT NULL CHECK (scope IN ('verify', 'enroll')),
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    consumed_at TEXT
);

CREATE INDEX login_challenges_expiry ON login_challenges (expires_at);
CREATE INDEX login_challenges_user ON login_challenges (user_id, scope);

ALTER TABLE settings ADD COLUMN require_2fa INTEGER NOT NULL DEFAULT 0 CHECK (require_2fa IN (0, 1));
