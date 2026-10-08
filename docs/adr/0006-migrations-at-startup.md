# 0006. Migrations run at startup

- **Status:** Accepted
- **Date:** 2026-10-08
- **Source:** [design plan](../design/plan.md) §13.4

## Decision

Embedded goose migrations run when `goldberry serve` starts (Postgres takes an advisory lock first). `goldberry migrate` exists for anyone who wants an explicit step. This departs from Copperkeep, which runs migrations as a separate Job.

## Consequences

One binary and one replica means there is no separate migration Job to order.
