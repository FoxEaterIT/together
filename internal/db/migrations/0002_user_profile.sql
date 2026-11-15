-- 0002_user_profile_tracking
-- Aggiorna `users.updated_at` per il profilo self e la gestione admin.
ALTER TABLE users ADD COLUMN updated_at TEXT;