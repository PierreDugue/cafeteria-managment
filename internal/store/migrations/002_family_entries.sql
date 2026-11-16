-- Family entries from the CSV export (SPEC §4.1.1, §7). Rebuilds
-- children: parent_id becomes nullable, new family columns and keys.
CREATE TABLE children_new (
    id            INTEGER PRIMARY KEY,
    -- NULL: family entry without a valid payer email (cannot be emailed)
    parent_id     INTEGER REFERENCES parents(id) ON DELETE CASCADE,
    family_number TEXT NOT NULL DEFAULT '',  -- NUMFAM; '' for Excel children
    name          TEXT NOT NULL,
    name_key      TEXT NOT NULL,             -- textnorm.Key(name)
    invalid_email TEXT NOT NULL DEFAULT '',  -- rejected payer email; '' if missing
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    CHECK (parent_id IS NOT NULL OR family_number <> ''),
    CHECK (parent_id IS NULL OR invalid_email = '')
);
INSERT INTO children_new (id, parent_id, name, name_key, created_at, updated_at)
    SELECT id, parent_id, name, name_key, created_at, updated_at FROM children;
DROP TABLE children;
ALTER TABLE children_new RENAME TO children;
CREATE UNIQUE INDEX children_child_key ON children (parent_id, name_key)
    WHERE family_number = '';
CREATE UNIQUE INDEX children_entry_key ON children (parent_id, family_number)
    WHERE family_number <> '' AND parent_id IS NOT NULL;
CREATE UNIQUE INDEX children_no_email_key ON children (family_number)
    WHERE parent_id IS NULL;
