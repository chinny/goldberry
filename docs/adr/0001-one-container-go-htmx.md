# 0001. One container; Go + HTMX, no SPA

- **Status:** Accepted
- **Date:** 2026-10-08
- **Source:** [design plan](../design/plan.md) §13.4

## Decision

Goldberry ships as one static Go binary in one image that serves server-rendered `html/template` pages, with HTMX swapping fragments. HTMX and CSS are vendored into the binary with `embed.FS`, so the app works on a LAN with no internet.

## Consequences

No JS toolchain, a tiny distroless image, trivial arm64 builds. Rich client-side interactions must fit the HTMX model.
