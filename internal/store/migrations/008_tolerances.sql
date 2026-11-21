-- "Tolérance" flags set by the user, one per family number of a list
-- (SPEC §4.2, §7). Not from the file: an import removes the flag of a
-- family number the list no longer has; deleted with the list.
CREATE TABLE tolerances (
    list_id       INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
    family_number TEXT NOT NULL CHECK (family_number <> ''),
    created_at    TEXT NOT NULL,
    PRIMARY KEY (list_id, family_number)
);
