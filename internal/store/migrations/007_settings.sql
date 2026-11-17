-- App settings (SPEC §4.6, §7): one row per key, read through the typed
-- store.Settings. Not touched by imports. Secrets (passwords, OAuth
-- tokens) never go here: OS keychain only (SPEC §8).
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
