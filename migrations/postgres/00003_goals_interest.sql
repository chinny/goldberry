-- Phase 5: savings goals and parent-paid interest (plan §7.3, §7.4).
-- Kept in lockstep with migrations/sqlite/00003_goals_interest.sql.

-- +goose Up
CREATE TABLE goals (
    id            TEXT PRIMARY KEY,
    kid_id        TEXT NOT NULL REFERENCES users (id),
    jar_id        TEXT NOT NULL REFERENCES jars (id),
    name          TEXT NOT NULL,
    emoji         TEXT,
    target_amount BIGINT NOT NULL CHECK (target_amount > 0),
    priority      INTEGER NOT NULL DEFAULT 0,
    created_by    TEXT REFERENCES users (id),
    reached_at    TIMESTAMPTZ,
    archived_at   TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL
);
CREATE INDEX goals_kid ON goals (kid_id);

CREATE TABLE interest_rules (
    kid_id      TEXT NOT NULL REFERENCES users (id),
    jar_id      TEXT NOT NULL REFERENCES jars (id),
    monthly_bps INTEGER NOT NULL CHECK (monthly_bps BETWEEN 0 AND 10000),
    monthly_cap BIGINT,
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    PRIMARY KEY (kid_id, jar_id)
);

-- +goose Down
DROP TABLE interest_rules;
DROP TABLE goals;
