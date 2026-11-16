-- Parents, children and import history (SPEC §4.1, §7).

CREATE TABLE parents (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    email      TEXT NOT NULL UNIQUE,  -- lower-case, identifies the parent
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE children (
    id         INTEGER PRIMARY KEY,
    parent_id  INTEGER NOT NULL REFERENCES parents(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    name_key   TEXT NOT NULL,         -- textnorm.Key(name), used for matching
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (parent_id, name_key)
);

CREATE TABLE imports (
    id               INTEGER PRIMARY KEY,
    file_name        TEXT NOT NULL,
    imported_at      TEXT NOT NULL,
    rows_read        INTEGER NOT NULL,
    parents_added    INTEGER NOT NULL,
    parents_updated  INTEGER NOT NULL,
    parents_deleted  INTEGER NOT NULL,
    children_added   INTEGER NOT NULL,
    children_updated INTEGER NOT NULL,
    children_deleted INTEGER NOT NULL
);
