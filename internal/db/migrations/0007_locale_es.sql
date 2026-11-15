-- 0007_locale_es
-- Spanish is now a supported interface language. SQLite cannot change a CHECK
-- constraint in place, so the users table is rebuilt with the same columns,
-- defaults and constraints plus 'es' in the locale list. The migration runner
-- disables foreign key enforcement for pending migrations, which is SQLite's
-- documented procedure when a referenced table has to be rebuilt, and verifies
-- every reference again before returning.

CREATE TABLE users_new (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    email TEXT NOT NULL COLLATE NOCASE UNIQUE,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('admin', 'manager', 'member', 'guest')),
    locale TEXT NOT NULL DEFAULT 'en' CHECK (locale IN ('en', 'it', 'es')),
    email_notifications INTEGER NOT NULL DEFAULT 0 CHECK (email_notifications IN (0, 1)),
    guest_expires_on TEXT,
    invited_by INTEGER REFERENCES users (id),
    created_at TEXT NOT NULL,
    disabled_at TEXT,
    updated_at TEXT
);

INSERT INTO users_new (
    id, name, email, password_hash, role, locale, email_notifications,
    guest_expires_on, invited_by, created_at, disabled_at, updated_at
)
SELECT
    id, name, email, password_hash, role, locale, email_notifications,
    guest_expires_on, invited_by, created_at, disabled_at, updated_at
FROM users;

DROP TABLE users;

ALTER TABLE users_new RENAME TO users;
