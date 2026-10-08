# 0008. Per-account throttling; kids hard-lock, admins never do

- **Status:** Accepted
- **Date:** 2026-10-08
- **Source:** [design plan](../design/plan.md) §13.4

## Decision

Failed sign-ins are counted per account, not per IP (the whole house shares one NAT). Both roles back off exponentially after 3 failures; kids hard-lock after 10 and only a parent can unlock them. Admins never hard-lock.

## Consequences

A sibling cannot brute-force a 4-digit PIN, and the last admin can never be locked out (the CLI resets passwords).
