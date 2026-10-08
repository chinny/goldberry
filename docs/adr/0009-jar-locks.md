# 0009. Jar locks: admin locks are hard, kid self-locks are soft with an override gauntlet

- **Status:** Accepted
- **Date:** 2026-10-08
- **Source:** [design plan](../design/plan.md) §13.4

## Decision

A lock restricts money leaving a jar. Admin locks cannot be bypassed by the kid; self-locks can, after a deliberately annoying gauntlet (warning, 10 s countdown, 3 s press-and-hold). Admin debits are never blocked.

## Consequences

Kids practise commitment without being trapped by it; parents keep authority.
