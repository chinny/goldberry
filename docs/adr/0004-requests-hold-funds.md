# 0004. Withdrawal requests hold funds on creation

- **Status:** Accepted
- **Date:** 2026-10-08
- **Source:** [design plan](../design/plan.md) §13.4

## Decision

A pending `withdrawal_requests` row holds its amount: available = balance − pending holds. Approval posts exactly one ledger entry; denial, cancellation and expiry post none.

## Consequences

Kids cannot over-request across several requests, and approval cannot fail on funds.
