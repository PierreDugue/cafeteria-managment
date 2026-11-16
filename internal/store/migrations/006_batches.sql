-- Sending batches and email history (SPEC §4.4, §4.5, §7). History keeps
-- copies of names, addresses and texts; links to lists, parents and
-- children are cleared (SET NULL) when those are deleted, so imports and
-- list deletion never change or erase history.
CREATE TABLE batches (
    id           INTEGER PRIMARY KEY AUTOINCREMENT, -- never reused (history URLs)
    token        TEXT NOT NULL UNIQUE,  -- one-time token of the confirmation page
    list_id      INTEGER REFERENCES lists(id) ON DELETE SET NULL,
    list_name    TEXT NOT NULL,         -- copy at sending time
    list_version INTEGER NOT NULL,
    families     INTEGER NOT NULL,      -- 1: family entries (CSV), 0: children (Excel)
    provider     TEXT NOT NULL,         -- 'test' (5b); later 'graph', 'smtp'
    subject      TEXT NOT NULL,         -- as typed (placeholders), normalized
    body         TEXT NOT NULL,
    status       TEXT NOT NULL CHECK (status IN ('sending', 'stopped', 'done')),
    stop_reason  TEXT NOT NULL DEFAULT '' CHECK (stop_reason IN ('', 'user', 'interrupted', 'account')),
    created_at   TEXT NOT NULL,
    finished_at  TEXT NOT NULL DEFAULT ''
);

CREATE TABLE messages (
    id         INTEGER PRIMARY KEY,
    batch_id   INTEGER NOT NULL REFERENCES batches(id) ON DELETE CASCADE,
    parent_id  INTEGER REFERENCES parents(id) ON DELETE SET NULL,
    to_name    TEXT NOT NULL,   -- {{parent_name}} value, header-safe
    to_email   TEXT NOT NULL,
    subject    TEXT NOT NULL,   -- final rendered text
    body       TEXT NOT NULL,
    status     TEXT NOT NULL CHECK (status IN ('pending', 'sending', 'sent', 'failed')),
    error_code TEXT NOT NULL DEFAULT '',  -- '', 'send', 'rate_limited', 'account', 'unknown'
    error      TEXT NOT NULL DEFAULT '',  -- provider text, cleaned, <= 500 runes
    attempts   INTEGER NOT NULL DEFAULT 0,
    sent_at    TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
);
CREATE INDEX messages_batch ON messages (batch_id, status);
CREATE INDEX messages_status ON messages (status, batch_id, id); -- next pending
CREATE INDEX messages_parent ON messages (parent_id);            -- SET NULL on parent delete

CREATE TABLE message_children (
    message_id    INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    position      INTEGER NOT NULL,  -- order of the recipient's entries
    child_id      INTEGER REFERENCES children(id) ON DELETE SET NULL,
    child_name    TEXT NOT NULL,     -- child name or billing name (copy)
    family_number TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (message_id, position)
);
CREATE INDEX message_children_child ON message_children (child_id); -- SET NULL on child delete
