# Architecture decision records

| # | Decision |
| --- | --- |
| [0001](0001-one-container-go-htmx.md) | One container; Go + HTMX, no SPA |
| [0002](0002-sqlite-default-postgres-optional.md) | SQLite by default, Postgres optional, behind one Store and a shared contract suite |
| [0003](0003-append-only-ledger.md) | Money is an append-only ledger in integer minor units |
| [0004](0004-requests-hold-funds.md) | Withdrawal requests hold funds on creation |
| [0005](0005-in-process-scheduler.md) | In-process scheduler with idempotency keys, no cron sidecar |
| [0006](0006-migrations-at-startup.md) | Migrations run at startup |
| [0007](0007-email-outbox.md) | Email via an outbox; in-app is the source of truth |
| [0008](0008-per-account-throttling.md) | Per-account throttling; kids hard-lock, admins never do |
| [0009](0009-jar-locks.md) | Jar locks: admin locks are hard, kid self-locks are soft with an override gauntlet |
| [0010](0010-agpl-licence.md) | AGPL-3.0 licence, matching Copperkeep |
