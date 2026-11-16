-- Family lists (SPEC §4.0, §7). Every entry moves into one list.
-- parents, children and last_selection are rebuilt with their IDs.
-- Foreign keys are ON and cannot be turned off inside this transaction,
-- and DROP TABLE cascades like DELETE: the old tables are dropped
-- dependents first, once nothing references them any more. The new
-- tables reference each other under their *_new names; RENAME rewrites
-- those references (SQLite >= 3.26).

CREATE TABLE lists (
    id         INTEGER PRIMARY KEY AUTOINCREMENT, -- never reused
    name       TEXT NOT NULL,                     -- textnorm.Clean(name)
    name_key   TEXT NOT NULL UNIQUE,              -- textnorm.Key(name)
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    used_at    TEXT NOT NULL  -- last import or "Continue": the default list
);

-- Existing data, if any, becomes one list.
INSERT INTO lists (name, name_key, created_at, updated_at, used_at)
    SELECT 'Liste importée', 'liste importee', ts, ts, ts
    FROM (SELECT strftime('%Y-%m-%dT%H:%M:%SZ', 'now') AS ts)
    WHERE EXISTS (SELECT 1 FROM parents) OR EXISTS (SELECT 1 FROM children)
       OR EXISTS (SELECT 1 FROM imports);

CREATE TABLE parents_new (
    id         INTEGER PRIMARY KEY,
    list_id    INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    email      TEXT NOT NULL,  -- lower-case; identifies the parent in its list
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (list_id, email),
    UNIQUE (id, list_id)       -- target of children (parent_id, list_id)
);

CREATE TABLE children_new (
    id            INTEGER PRIMARY KEY,
    list_id       INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
    -- NULL: family entry without a valid payer email (cannot be emailed)
    parent_id     INTEGER,
    family_number TEXT NOT NULL DEFAULT '',  -- NUMFAM; '' for Excel children
    name          TEXT NOT NULL,
    name_key      TEXT NOT NULL,             -- textnorm.Key(name)
    invalid_email TEXT NOT NULL DEFAULT '',  -- rejected payer email; '' if missing
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    -- an entry and its parent are always in the same list
    FOREIGN KEY (parent_id, list_id) REFERENCES parents_new (id, list_id) ON DELETE CASCADE,
    CHECK (parent_id IS NOT NULL OR family_number <> ''),
    CHECK (parent_id IS NULL OR invalid_email = '')
);

CREATE TABLE last_selection_new (
    child_id INTEGER PRIMARY KEY REFERENCES children_new(id) ON DELETE CASCADE
);

-- At most one list exists here; with no data nothing is copied.
INSERT INTO parents_new (id, list_id, name, email, created_at, updated_at)
    SELECT id, (SELECT id FROM lists), name, email, created_at, updated_at FROM parents;
INSERT INTO children_new (id, list_id, parent_id, family_number, name, name_key,
                          invalid_email, created_at, updated_at)
    SELECT id, (SELECT id FROM lists), parent_id, family_number, name, name_key,
           invalid_email, created_at, updated_at FROM children;
INSERT INTO last_selection_new (child_id) SELECT child_id FROM last_selection;

DROP TABLE last_selection;
DROP TABLE children;
DROP TABLE parents;
ALTER TABLE parents_new RENAME TO parents;
ALTER TABLE children_new RENAME TO children;
ALTER TABLE last_selection_new RENAME TO last_selection;

CREATE INDEX children_list ON children (list_id);
CREATE UNIQUE INDEX children_child_key ON children (parent_id, name_key)
    WHERE family_number = '';
CREATE UNIQUE INDEX children_entry_key ON children (parent_id, family_number)
    WHERE family_number <> '' AND parent_id IS NOT NULL;
CREATE UNIQUE INDEX children_no_email_key ON children (list_id, family_number)
    WHERE parent_id IS NULL;

-- Import history keeps the list, and its name if the list is deleted.
ALTER TABLE imports ADD COLUMN list_id INTEGER REFERENCES lists(id) ON DELETE SET NULL;
ALTER TABLE imports ADD COLUMN list_name TEXT NOT NULL DEFAULT '';
UPDATE imports SET list_id = (SELECT id FROM lists), list_name = (SELECT name FROM lists);
CREATE INDEX imports_list ON imports (list_id);
