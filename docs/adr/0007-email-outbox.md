# 0007. Email via an outbox; in-app is the source of truth

- **Status:** Accepted
- **Date:** 2026-10-08
- **Source:** [design plan](../design/plan.md) §13.4

## Decision

Every notification is first a row in `notifications`. Email is an optional copy written to an `email_outbox` row in the same transaction and sent by an in-process worker with retry backoff.

## Consequences

SMTP failure never loses a notification or blocks a request handler.
