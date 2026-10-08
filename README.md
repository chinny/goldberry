# goldberry

A self-hosted allowance tracker for kids. Parents add and remove money with a comment; kids sign in with a PIN, see what they can spend, and ask for money. Requests show up in the parents' notification bell to approve or deny.

One container, one volume: a Go binary serving server-rendered HTML (HTMX), with SQLite on the volume by default or Postgres if you point it at one. *Named for Goldberry, the River-daughter in The Lord of the Rings.*

## Quickstart (Docker Compose, 60 seconds)

```sh
git clone https://github.com/chinny/goldberry && cd goldberry/deploy/compose
openssl rand -base64 32 > goldberry_key.txt
docker compose up -d --build          # drop --build once a release is published
docker compose logs goldberry | grep -i "setup token"
```

Open `http://<host>:8080/setup`, paste the setup token, and create the first parent account. Then add your kids.

> Sessions use `Secure` cookies, so put Goldberry behind a TLS reverse proxy (Caddy, Traefik, a Gateway). For a quick plain-HTTP trial on your LAN, set `GOLDBERRY_INSECURE_COOKIES: "true"` in `compose.yaml`.

## What works today (v0.x)

Phases 0–2 and 4 of the [design plan](docs/design/plan.md#14-phasing) (email, Phase 3, is parked):

- **First-run setup** guarded by a one-time token printed to the container log.
- **Accounts:** parents sign in with a password, kids with a 4–6 digit PIN on a big PIN pad. Throttling is per account; kids hard-lock after 10 wrong PINs and only a parent can unlock them. Parents can reset PINs, sign a kid out of every device, and disable accounts.
- **Money:** an append-only ledger in integer cents. Add or remove funds with a comment the kid sees and a private note they don't. Undo is a visible reversal, never an edit. Balances can't go below zero unless you allow it.
- **Requests:** a kid asks for money from what's available; the amount is held until a parent approves (optionally a lower amount, with a note), denies, the kid cancels, or it expires (14 days by default).
- **Notifications:** an in-app bell for both parents and kids, with Approve/Deny inline. The first parent to decide wins; the other sees "Approved by Mom".
- **Jars:** every kid has Spend, Save and Give, split 70/20/10 (leftover cents go to Spend). Parents can rename, add and archive jars and change the split; deposits can use the split or go to one jar. Kids move money between their own jars.
- **Jar locks:** a parent's lock is hard. A kid can lock their own jar ("Saving for a Switch") and break it only through the gauntlet: a loud warning, a 10-second countdown and a 3-second press-and-hold, enforced by the server ([ADR 0011](docs/adr/0011-server-enforced-gauntlet.md)). Parents are told when a lock is broken.
- **Recurring allowance:** weekly, every two weeks or monthly, into the split or one jar. It pays itself on the household's calendar, catches up after downtime (up to 8 payments), never double-pays, and can be paused.
- Light and dark themes, phone-first, works without JavaScript, no CDN (works offline on a LAN).

Coming next, per the plan: goals and parent-paid interest (Phase 5), email via SMTP (Phase 3), Helm chart, backups and v1.0 (Phase 6).

## Configuration

Environment variables (or `/data/config.env`). Any variable can also be given as `NAME_FILE` pointing at a file.

| Variable | Default | Notes |
| --- | --- | --- |
| `GOLDBERRY_DATABASE_URL` | `sqlite:///data/goldberry.db` | Or `postgres://user:pass@host:5432/goldberry` |
| `GOLDBERRY_SECRET_KEY` | *(none)* | 32+ random bytes, base64. Will encrypt SMTP secrets (Phase 3). |
| `GOLDBERRY_BASE_URL` | *(none)* | Public URL, for email links |
| `GOLDBERRY_LISTEN` | `:8080` | |
| `GOLDBERRY_TRUSTED_PROXIES` | *(none)* | CIDRs whose `X-Forwarded-For` is trusted for logging |
| `GOLDBERRY_INSECURE_COOKIES` | `false` | Plain-HTTP testing only |
| `GOLDBERRY_LOG_FORMAT` | `json` | `json` or `text` |
| `TZ` | `UTC` | Default household time zone at setup |

**Never put the SQLite file on NFS or SMB**: use a local or block-backed volume. Goldberry warns at startup if `/data` is a network filesystem.

### Operations

- Health: `GET /healthz` (liveness), `GET /readyz` (database), `GET /metrics` (Prometheus).
- Locked out as the only parent: `docker compose exec goldberry goldberry admin reset-password <username>` prints a new password.
- `goldberry migrate` applies migrations without starting the server (they also run at startup).

## Development

Needs Go (see `go.mod`) and [`just`](https://github.com/casey/just).

```sh
just run        # http://localhost:8080, SQLite in ./data
just test       # unit tests + the store contract suite on SQLite
just pg-up && just test-pg   # the same contract suite on Postgres
just lint       # gofmt, vet, golangci-lint, migration parity
just e2e        # build the image and run the Playwright smoke test against it
```

Layout follows the plan (§13.2): `cmd/goldberry` (CLI), `internal/{config,money,auth,service,scheduler,notify,store,web}`, `migrations/{sqlite,postgres}` (kept in lockstep), `deploy/compose`, `e2e/`, and `docs/` with the [design plan](docs/design/plan.md), the [design board](docs/design/README.md) and [ADRs](docs/adr/README.md).

The service layer owns every money rule; handlers never touch the store. `internal/store/storetest` is the contract suite both database engines must pass — it is the real definition of "pluggable".

## Licence

[AGPL-3.0](LICENSE). Every page links to the exact source of the running build. Fonts (Atkinson Hyperlegible Next, Bricolage Grotesque) are SIL OFL 1.1; HTMX is BSD-2-Clause.
