-- Goldberry schema: identity, ledger, withdrawal requests, notifications.
-- Kept in lockstep with migrations/sqlite/00001_init.sql (plan §9.1).
-- IDs are UUIDv7 TEXT; money is BIGINT minor units; times are TIMESTAMPTZ.

-- +goose Up
CREATE TABLE household (
    id                  TEXT PRIMARY KEY,
    name                TEXT NOT NULL,
    currency            CHAR(3) NOT NULL,
    timezone            TEXT NOT NULL,
    pin_length          INTEGER NOT NULL DEFAULT 4 CHECK (pin_length BETWEEN 4 AND 6),
    allow_negative      BOOLEAN NOT NULL DEFAULT FALSE,
    request_expiry_days INTEGER NOT NULL DEFAULT 14 CHECK (request_expiry_days >= 0),
    kid_picker_login    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at          TIMESTAMPTZ NOT NULL
);

CREATE TABLE users (
    id            TEXT PRIMARY KEY,
    role          TEXT NOT NULL CHECK (role IN ('admin', 'kid')),
    username      TEXT NOT NULL UNIQUE,
    display_name  TEXT NOT NULL,
    avatar        TEXT,
    password_hash TEXT,
    pin_hash      TEXT,
    email         TEXT,
    disabled_at   TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL
);

CREATE TABLE sessions (
    id_hash      TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users (id),
    device_label TEXT,
    remember     BOOLEAN NOT NULL DEFAULT FALSE,
    created_at   TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    revoked_at   TIMESTAMPTZ
);
CREATE INDEX sessions_user ON sessions (user_id);

CREATE TABLE auth_attempts (
    user_id         TEXT PRIMARY KEY REFERENCES users (id),
    failures        INTEGER NOT NULL DEFAULT 0,
    window_start    TIMESTAMPTZ,
    next_allowed_at TIMESTAMPTZ,
    locked_at       TIMESTAMPTZ
);

CREATE TABLE jars (
    id          TEXT PRIMARY KEY,
    kid_id      TEXT NOT NULL REFERENCES users (id),
    name        TEXT NOT NULL,
    kind        TEXT NOT NULL CHECK (kind IN ('spend', 'save', 'give', 'custom')),
    sort_order  INTEGER NOT NULL DEFAULT 0,
    archived_at TIMESTAMPTZ
);
CREATE INDEX jars_kid ON jars (kid_id);

CREATE TABLE split_rules (
    kid_id       TEXT NOT NULL REFERENCES users (id),
    jar_id       TEXT NOT NULL REFERENCES jars (id),
    basis_points INTEGER NOT NULL CHECK (basis_points BETWEEN 0 AND 10000),
    PRIMARY KEY (kid_id, jar_id)
);

-- APPEND-ONLY: the application never UPDATEs or DELETEs ledger rows.
CREATE TABLE ledger_entries (
    id              TEXT PRIMARY KEY,
    kid_id          TEXT NOT NULL REFERENCES users (id),
    jar_id          TEXT NOT NULL REFERENCES jars (id),
    amount          BIGINT NOT NULL,
    kind            TEXT NOT NULL CHECK (kind IN ('deposit', 'withdrawal', 'adjustment', 'allowance',
                                                  'interest', 'transfer_in', 'transfer_out', 'reversal')),
    comment         TEXT,
    private_note    TEXT,
    actor_id        TEXT REFERENCES users (id),
    reverses_id     TEXT UNIQUE REFERENCES ledger_entries (id),
    transfer_id     TEXT,
    batch_id        TEXT,
    request_id      TEXT,
    schedule_id     TEXT,
    idempotency_key TEXT UNIQUE,
    effective_at    TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL
);
CREATE INDEX ledger_jar_effective ON ledger_entries (jar_id, effective_at);
CREATE INDEX ledger_kid_created ON ledger_entries (kid_id, created_at);

CREATE TABLE withdrawal_requests (
    id              TEXT PRIMARY KEY,
    kid_id          TEXT NOT NULL REFERENCES users (id),
    jar_id          TEXT NOT NULL REFERENCES jars (id),
    amount          BIGINT NOT NULL CHECK (amount > 0),
    approved_amount BIGINT,
    reason          TEXT NOT NULL,
    goal_id         TEXT,
    status          TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'denied', 'cancelled', 'expired')),
    decided_by      TEXT REFERENCES users (id),
    decision_note   TEXT,
    decided_at      TIMESTAMPTZ,
    idempotency_key TEXT UNIQUE,
    created_at      TIMESTAMPTZ NOT NULL,
    expires_at      TIMESTAMPTZ
);
CREATE INDEX requests_pending_jar ON withdrawal_requests (jar_id) WHERE status = 'pending';
CREATE INDEX requests_kid_created ON withdrawal_requests (kid_id, created_at);

CREATE TABLE notifications (
    id           TEXT PRIMARY KEY,
    recipient_id TEXT NOT NULL REFERENCES users (id),
    kind         TEXT NOT NULL,
    payload      TEXT NOT NULL,
    request_id   TEXT,
    read_at      TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL
);
CREATE INDEX notifications_recipient_read ON notifications (recipient_id, read_at);

CREATE TABLE notification_prefs (
    user_id TEXT NOT NULL REFERENCES users (id),
    kind    TEXT NOT NULL,
    email   BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (user_id, kind)
);

CREATE TABLE settings (
    key    TEXT PRIMARY KEY,
    value  TEXT NOT NULL,
    secret BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE audit_log (
    id         TEXT PRIMARY KEY,
    actor_id   TEXT REFERENCES users (id),
    action     TEXT NOT NULL,
    target_id  TEXT,
    detail     TEXT,
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX audit_created ON audit_log (created_at);

-- +goose Down
DROP TABLE audit_log;
DROP TABLE settings;
DROP TABLE notification_prefs;
DROP TABLE notifications;
DROP TABLE withdrawal_requests;
DROP TABLE ledger_entries;
DROP TABLE split_rules;
DROP TABLE jars;
DROP TABLE auth_attempts;
DROP TABLE sessions;
DROP TABLE users;
DROP TABLE household;
