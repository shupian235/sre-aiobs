CREATE TABLE IF NOT EXISTS encrypted_settings (
 id text PRIMARY KEY,
 ciphertext bytea NOT NULL,
 version bigint NOT NULL DEFAULT 1
);
ALTER TABLE alert_settings ADD COLUMN IF NOT EXISTS email_ciphertext bytea;
