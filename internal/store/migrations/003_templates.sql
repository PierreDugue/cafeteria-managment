-- Saved message templates (SPEC §4.3, §7). Not touched by imports.
CREATE TABLE templates (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,          -- textnorm.Clean(name)
    name_key   TEXT NOT NULL UNIQUE,   -- textnorm.Key(name): unique ignoring case/accents/spaces
    subject    TEXT NOT NULL,
    body       TEXT NOT NULL,          -- plain text, "\n" line endings
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
