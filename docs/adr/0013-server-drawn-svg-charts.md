# 0013. Charts are SVG drawn on the server

- **Status:** Accepted
- **Date:** 2026-10-10
- **Source:** [design plan](../design/plan.md) §7.5, §11 (no JS build, no CDN, pages work without JS)

## Decision

The balance graph is an inline `<svg>` that the web layer writes from the service's balance history (`internal/web/chart.go`). No charting library is used. The plot uses `preserveAspectRatio="none"` so it fills its box at any width, `vector-effect: non-scaling-stroke` keeps lines 2 px, and each dot is a zero-length round-capped path, so it stays round when the plot stretches. Axis labels are HTML beside the plot: the y labels sit in equal rows and the x labels in equal columns, and ticks are placed at row and column centres so they line up without positioning any text. Colours, dashes and sizes come from classes in `app.css`. Hover text is a native `<title>`, and each dot links to its row in the list of changes below.

## Consequences

The graph works with JavaScript off, needs no `style=` attribute (the CSP blocks them), and adds nothing to the image. Text never scales with the plot. The cost is that there is no crosshair or rich tooltip. On touch screens the native title doesn't show, so a tap goes to the change in the list instead. If a richer chart is ever wanted, it can be progressive enhancement in `app.js` over the same markup.
