# Goldberry

Self-hosted kids' allowance tracker. Go + server-rendered HTMX, one container, SQLite (default) or Postgres.

- **Source of truth:** `docs/design/plan.md` (snapshot of the living plan doc) and `docs/design/` (board sources, PNG renders, tokens). Decisions go in `docs/adr/`.
- **Layers:** `web` → `service` → `store`. Handlers never call the store; every money rule lives in `service`. Only `store` has SQL.
- **Money:** `int64` minor units; `ledger_entries` is append-only (no UPDATE/DELETE, corrections are reversals).
- **Migrations:** add the same numbered file to both `migrations/sqlite` and `migrations/postgres`; `scripts/check-migrations.sh` enforces it.
- **Tests:** any new store or money behaviour gets a case in `internal/store/storetest` so it runs on both engines. `just test`, `just test-pg`, `just lint`, `just e2e`.
- **UI:** follow the board and the tokens in `internal/web/static/app.css`. No inline `style=` (the CSP blocks it), no CDN assets, pages must work without JS.
- **Docs change in the same PR as the code.**
