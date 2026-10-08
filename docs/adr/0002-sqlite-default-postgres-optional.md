# 0002. SQLite by default, Postgres optional, behind one Store and a shared contract suite

- **Status:** Accepted
- **Date:** 2026-10-08
- **Source:** [design plan](../design/plan.md) §13.4

## Decision

SQLite (`modernc.org/sqlite`, no CGO) on the `/data` volume is the default. Postgres (`pgx`) is used when `GOLDBERRY_DATABASE_URL` is a `postgres://` URL. Both sit behind one `store.Store` interface written in hand-written portable SQL; the `storetest` contract suite runs against both in CI.

## Consequences

Zero ops for a family, reuse of an existing Postgres when present. Every query must stay in the portable subset or move to a dialect method. sqlc and ORMs were rejected.
