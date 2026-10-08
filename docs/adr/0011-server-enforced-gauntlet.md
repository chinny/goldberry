# 0011. The lock-breaking gauntlet is enforced by the server

- **Status:** Accepted
- **Date:** 2026-10-08
- **Source:** [design plan](../design/plan.md) §5.4, §4 (threat model: the curious kid with DevTools)

## Decision

Breaking a kid's own jar lock needs a token from `GET /gauntlet`: an HMAC (per-install key in `settings`) over the kid, the jar and the time it was issued. A transfer, request or lock removal accepts it only 13 seconds after issue (the 10 s countdown plus the 3 s press-and-hold) and for 10 minutes. The countdown, escalating copy and hold are drawn by `app.js`, but skipping or editing the script gains nothing: an early token is refused and the kid is sent back to a fresh countdown. Each override writes a `lock_overrides` row and an in-app notification to the parents.

Parent locks are hard: no token gets past them, and admins are never subject to locks.

## Consequences

The gauntlet works the same with or without JavaScript (without it, the button is live but the server still makes the kid wait). A kid can't bypass a lock with DevTools or by replaying a request. The token is stateless, so nothing is stored per attempt.
