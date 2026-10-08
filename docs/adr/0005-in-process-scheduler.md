# 0005. In-process scheduler with idempotency keys, no cron sidecar

- **Status:** Accepted
- **Date:** 2026-10-08
- **Source:** [design plan](../design/plan.md) §13.4

## Decision

Background jobs (request expiry, later allowance, interest, outbox, backups) run on one in-process ticker. Each posting carries a unique idempotency key, so duplicate or catch-up runs are no-ops.

## Consequences

Nothing extra to deploy; the app normally runs as a single replica.
