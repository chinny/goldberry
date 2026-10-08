# 0003. Money is an append-only ledger in integer minor units

- **Status:** Accepted
- **Date:** 2026-10-08
- **Source:** [design plan](../design/plan.md) §13.4

## Decision

Every amount is `int64` minor units. Balances are `SUM`s over `ledger_entries`, which is never updated or deleted; corrections are reversal entries pointing at the original.

## Consequences

Full audit history and no float bugs. Undo is a visible pair of entries rather than an edit.
