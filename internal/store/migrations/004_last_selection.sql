-- Entries selected the last time the user continued to the message page
-- (SPEC §4.2, §7). The cascade drops the rows of entries an import deletes,
-- so a reused ID can never be pre-checked by mistake.
CREATE TABLE last_selection (
    child_id INTEGER PRIMARY KEY REFERENCES children(id) ON DELETE CASCADE
);
