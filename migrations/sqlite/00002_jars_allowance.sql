-- Phase 4: recurring allowance, jar locks and lock overrides (plan §5.4, §7.2).
-- Kept in lockstep with migrations/postgres/00002_jars_allowance.sql.
-- Calendar dates (anchor, until, last occurrence) are 'YYYY-MM-DD' TEXT in the
-- household time zone on both engines.

-- +goose Up
CREATE TABLE schedules (
    id               TEXT PRIMARY KEY,
    kid_id           TEXT NOT NULL REFERENCES users (id),
    amount           INTEGER NOT NULL CHECK (amount > 0),
    cadence          TEXT NOT NULL CHECK (cadence IN ('weekly', 'biweekly', 'monthly')),
    weekday          INTEGER CHECK (weekday BETWEEN 0 AND 6),
    day_of_month     INTEGER CHECK (day_of_month BETWEEN 1 AND 31),
    anchor_date      TEXT,
    target_jar_id    TEXT REFERENCES jars (id),
    comment_template TEXT,
    active           INTEGER NOT NULL DEFAULT 1,
    last_occurrence  TEXT,
    created_by       TEXT REFERENCES users (id),
    created_at       TEXT NOT NULL
);
CREATE INDEX schedules_kid ON schedules (kid_id);

CREATE TABLE jar_locks (
    id          TEXT PRIMARY KEY,
    jar_id      TEXT NOT NULL REFERENCES jars (id),
    to_jar_id   TEXT REFERENCES jars (id),
    set_by_role TEXT NOT NULL CHECK (set_by_role IN ('admin', 'kid')),
    set_by      TEXT NOT NULL REFERENCES users (id),
    reason      TEXT,
    until_date  TEXT,
    created_at  TEXT NOT NULL,
    removed_by  TEXT REFERENCES users (id),
    removed_at  TEXT
);
CREATE INDEX jar_locks_active ON jar_locks (jar_id) WHERE removed_at IS NULL;

CREATE TABLE lock_overrides (
    id              TEXT PRIMARY KEY,
    lock_id         TEXT NOT NULL REFERENCES jar_locks (id),
    kid_id          TEXT NOT NULL REFERENCES users (id),
    action          TEXT NOT NULL CHECK (action IN ('once', 'removed')),
    ledger_entry_id TEXT,
    request_id      TEXT,
    created_at      TEXT NOT NULL
);
CREATE INDEX lock_overrides_kid ON lock_overrides (kid_id, created_at);

-- +goose Down
DROP TABLE lock_overrides;
DROP TABLE jar_locks;
DROP TABLE schedules;
