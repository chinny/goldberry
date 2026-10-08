# Goldberry — Design Plan

Oct 8, 2026 · Jeffrey Chin

> Snapshot of the living plan doc: https://claude.ai/artifact/M1fZn8vas1tyyRnjCRNZjx (exported 2026-10-08, rev 55). The doc is the source of truth; refresh this copy when it changes.

## 1. Summary

A self-hosted web app for tracking kids' allowances, shipped as **one open-source, multi-arch container image**. Parents (admins) add and remove money with optional comments. Kids sign in with a username and PIN, see their balance, and request withdrawals, which show up as notifications for parents (in the app, and by email when SMTP is set up).

Three things drive every decision below:

1. **One container, one volume.** A Go binary serves server-rendered HTML (HTMX). The only state is a database: SQLite on a mounted volume by default, or Postgres if `DATABASE_URL` points at one.
2. **Money is an append-only ledger.** A balance is never stored and edited in place; it is the sum of ledger entries. A correction is a reversing entry, so the history can always be audited.
3. **Kids are the realistic adversary.** A 4-digit PIN has 10,000 combinations, and the home LAN is where the curious 10-year-old sits. Per-account throttling is required, not optional.

In scope: admin and kid accounts, credits and debits, withdrawal requests with holds, in-app and email notifications, Spend/Save/Give jars, recurring allowance, savings goals, and parent-paid interest. *Named for Goldberry, the River-daughter in The Lord of the Rings.*

## 2. Design principles

| Principle | Consequence |
| --- | --- |
| One image, one stateful thing | A single `goldberry` container plus a volume (or an external Postgres). No Redis, no queue, no sidecars. |
| Money is integers | Every amount is `int64` minor units (cents). Floats never touch money. |
| The ledger is append-only | Balances are a `SUM`. Edits and deletes are reversing entries. |
| Available ≠ balance | Pending withdrawal requests hold funds. Kids see both numbers. |
| Identity is local and minimal | Kids have a display name, username and PIN only: no email, no birthdate. |
| The parent is the recovery path | Kids never reset themselves. A locked kid is unlocked by a parent. |
| Email is optional, never required | Every notification exists in the app first. SMTP only delivers a copy. |
| Storage is a seam, not a fork | One `Store` interface and one contract test suite, run against SQLite and Postgres in CI. |
| Defer every optional subsystem | Ship the seam (JSON API, web push, OIDC) and leave the implementation for later. |

## 3. Architecture

```mermaid
flowchart TB
  %% One container and one volume; Postgres and SMTP are optional (dashed)
  parent["Parent's phone<br/><small>admin: username + password</small>"]
  kid["Kid's tablet<br/><small>kid: username + PIN</small>"]
  proxy["Reverse proxy<br/><small>TLS termination: Traefik, Caddy or a Gateway</small>"]
  subgraph container["goldberry container · one Go binary"]
    web["Web handlers<br/><small>HTMX pages, auth, CSRF</small>"]
    jobs["Background jobs<br/><small>schedules, interest, outbox</small>"]
    svc["Service layer<br/><small>every money rule: ledger, holds, goals, interest</small>"]
    store["Store interface<br/><small>one contract suite, run against both drivers</small>"]
  end
  sqlite[("SQLite (default)<br/><small>one file on the /data volume</small>")]
  pg[("Postgres (optional)<br/><small>set GOLDBERRY_DATABASE_URL</small>")]
  smtp["SMTP relay (optional)<br/><small>Gmail with an app password</small>"]
  inbox["Parents' inboxes<br/><small>a copy of each alert</small>"]
  parent --> proxy
  kid --> proxy
  proxy --> web
  web --> svc
  jobs --> svc
  svc --> store
  store --> sqlite
  store -.-> pg
  jobs -.-> smtp
  smtp -.-> inbox
  classDef optional stroke-dasharray: 5 4
  class pg,smtp,inbox optional
```

Both kinds of user reach the same container through the reverse proxy. Only the Store talks to the database, and only the background jobs talk to SMTP.

- **Web handlers** render pages and fragments and enforce the role. They call the service layer and never the store.
- **The service layer** owns every money rule: balance checks, holds, splits, reversals, goal waterfall, interest math. It is the most heavily tested code.
- **Background jobs** run in-process on one ticker: recurring allowance, monthly interest, request expiry, the email outbox, and nightly SQLite snapshots.
- **Store** is the only package with SQL (§9).

### 3.1 Deliberately excluded

- **SPA / JS build:** HTMX covers every interaction here, and a Go template is easier to keep right than a second app.
- **Redis / queue:** at a few writes a minute, the outbox and scheduler are tables plus a ticker.
- **Identity provider (OIDC):** local accounts are the whole requirement. OIDC for admins is a later seam (§14).
- **Multi-household tenancy:** one install is one family. Another family runs another container.
- **Bank integration / real money movement:** the app records what parents did; it never moves money.

## 4. Accounts & identity

There are two roles: **admin** (parent) and **kid**. Each install is a single household, so there is no tenancy column (see §15).

**Threat model.** There are three adversaries:

- **The curious kid**: DevTools open, tries URLs and form values to see what the server accepts.
- **The motivated kid**: wants a sibling's PIN, a bigger balance, or to approve their own request.
- **The shared tablet**: a family device where one kid's session is left signed in.

Nothing here defends against a hostile network operator. TLS at the reverse proxy covers the wire (§12).

### 4.1 Credentials

|  | Admin | Kid |
| --- | --- | --- |
| Identifier | Username | Username |
| Secret | Password, minimum 10 characters | PIN of 4–6 digits (household setting, default 4) |
| Hash | argon2id | argon2id. Hashing still matters: a stolen DB file must not reveal PINs reused on a phone. |
| Backoff | Exponential after 3 failures; never hard-locks | Exponential after 3 failures; **hard lock after 10** |
| Recovery | Another admin resets it, or the CLI: `docker exec … goldberry admin reset-password <user>` | An admin resets the PIN or unlocks the account. Never self-service, never on a timer. |
| Session length | 7 days, sliding | 30 days on a remembered device, otherwise 12 h |

- Throttling is **per account, not per IP**, because the whole house shares one NAT and kids share devices. Counters live in the `auth_attempts` table.
- Failed attempts and lockouts create an admin notification. A kid trying a sibling's PIN should be visible, not silent.
- Optional household setting: a **kid picker** login screen (avatar tiles, then the PIN pad), so young kids don't have to type a username. Off by default.

### 4.2 Sessions and CSRF

- Sessions are server-side rows (`sessions` table) with a random 256-bit ID in a cookie: `HttpOnly`, `Secure`, `SameSite=Lax`.
- Use `Lax`, not `Strict`: a parent tapping "Review request" in an email must land signed in.
- `Lax` allows top-level GETs, so every state-changing request is a POST that carries a CSRF token. HTMX sends it via `hx-headers` from a `<meta>` tag.
- Logout and "sign out everywhere" revoke the rows. An admin can revoke any kid's sessions, which covers the shared tablet.

### 4.3 Authorization rules

- A kid can read only their own balances, ledger, requests and goals. Every query is scoped by `kid_id` from the **session**, never from the URL or form.
- A kid can create and cancel their own pending requests, edit their own goals, move money between their own jars, and set, override or remove their own self-locks. Nothing else writes.
- Admins can do everything, including adding other admins. At least one admin must always exist; removing the last one is refused.
- Every ledger write records the acting user. Admin actions on money cannot be anonymous.

### 4.4 First-run setup

- With zero admins, every route redirects to `/setup`.
- `/setup` needs a **one-time setup token** printed to the container log at startup. This stops another device on the LAN from claiming the instance first.
- The wizard collects the household name, currency (ISO 4217, default `USD`), timezone (default from `TZ`), and the first admin's username and password. It then offers to add kids.

## 5. Money model

Each kid has **jars** (Spend / Save / Give by default). Each jar's balance is the sum of its ledger entries. **Available** is the balance minus pending withdrawal holds.

```
balance(jar)   = SUM(ledger_entries.amount WHERE jar_id = jar)
held(jar)      = SUM(withdrawal_requests.amount WHERE jar_id = jar AND status = 'pending')
available(jar) = balance(jar) - held(jar)
```

### 5.1 Rules

- **Amounts are `int64` minor units.** The display uses the household currency's exponent (2 for USD; 0 for JPY). Parsing and formatting live in one package, `money`, with table tests.
- **The ledger is append-only.** No `UPDATE` or `DELETE` on `ledger_entries`, ever. "Undo" posts a reversal that points at the original (`reverses_id`). The UI shows the pair struck through.
- **Every entry has a `kind`:** `deposit`, `withdrawal`, `adjustment`, `allowance`, `interest`, `transfer_in`, `transfer_out`, `reversal`.
- **Every entry has an actor and an optional comment.** The comment is visible to the kid ("Mowed the lawn"). An optional `private_note` is visible to admins only.
- **No negative balances by default.** An admin debit that would push available below zero is refused. The household setting `allow_negative` turns this off for "you owe us for the broken window."
- **Balances are computed, not cached.** A household has a few thousand entries a year, so a `SUM` over an indexed column is microseconds. Add a cache only if measurement says so.

### 5.2 Jars

- Each kid has a **split rule**, e.g. Spend 70 / Save 20 / Give 10. It applies to recurring allowance and, by default, to manual deposits. An admin can override it per deposit and send everything to one jar.
- Rounding: compute each jar's share with integer division, then put the remainder cents in the first jar (Spend). The parts always sum exactly to the deposit.
- Jars are configurable per household: rename, add, archive. A jar with a non-zero balance cannot be archived.
- **Moves between jars** are a `transfer_out` / `transfer_in` pair sharing a `transfer_id`. By default kids can move money freely between any of their own jars. **Jar locks** (§5.4) are the only restriction.
- Turn jars off and the app runs with a single jar. The model doesn't change.

### 5.3 Concurrency

Race to prevent: a kid double-taps "Request $20" with $25 available, two requests both pass the check, and $40 is held against $25.

- Every money write runs **check-then-write in one DB transaction**.
- SQLite: `BEGIN IMMEDIATE` takes the write lock up front, so writers serialize.
- Postgres: `SELECT … FOR UPDATE` on the kid's `users` row serializes writes for that kid.
- Forms carry an **idempotency key** (a UUID rendered into the form), unique on `ledger_entries` and `withdrawal_requests`. A double submit becomes a no-op, not a second entry.

### 5.4 Jar locks

A lock restricts money leaving a jar. **Who set the lock decides whether the kid can get past it.**

|  | Admin lock | Self-lock (set by the kid) |
| --- | --- | --- |
| Strength | **Hard.** The kid can't override or remove it. | **Soft.** The kid can override or remove it, after the gauntlet below. |
| Typical use | "Give jar only goes out with a parent" | "Don't let me touch Save until the Switch" |
| Shown to the kid as | "Locked by a parent" (+ until date, note) | "You locked this on Mar 3: *Saving for a Switch*" |
| Removed by | Any admin | The kid (gauntlet) or any admin (no gauntlet) |

- **Scope.** A lock is on a jar and blocks money going out. With `to_jar_id` empty it blocks every transfer out *and* withdrawal requests from that jar. With `to_jar_id` set it blocks only that one route (e.g. Save → Spend), leaving requests and other routes open.
- **Optional `until` date.** The lock lifts itself at the start of that day in the household timezone. With no date it lasts until removed.
- Admin debits and approvals are never blocked by locks. Locks are guardrails for the kid, not for parents.
- A jar can have both kinds. The strictest lock that applies wins, so an admin lock blocks even if the kid's own lock has been overridden.

**The override gauntlet (self-locks only).** It is meant to be annoying but kind. The kid can still go ahead; they just have to mean it.

1. A full-screen warning in loud colors repeats the kid's own reason and date, and shows the cost: "Your *Switch* goal drops from 64% to 22%."
2. A **10-second countdown** before the button enables, with escalating copy ("Are you sure?" → "Past-you set this for a reason" → "Okay, okay").
3. **Press and hold for 3 seconds** on "Break my lock". A tap is not enough.
4. Choose: **override once** (this transfer or request only, and the lock stays) or **remove the lock**.

The shake animation and countdown respect `prefers-reduced-motion`; the countdown stays and the motion goes. Every override writes a `lock_overrides` row, which shows in the kid's activity for admins. Whether admins get a notification is the per-admin preference *Kid overrode own lock* (default: in-app only, no email).

## 6. Withdrawal requests

```mermaid
flowchart LR
  %% Funds are held from request until a parent decides
  start["Kid requests<br/><small>amount, jar, reason</small>"] --> check{"≤ available?"}
  check -- no --> refused["Form error<br/><small>nothing held</small>"]
  check -- yes --> pending["Pending<br/><small>hold placed · admins notified</small>"]
  pending --> approved["Approved by a parent<br/><small>posts one withdrawal ledger entry</small>"]
  pending --> denied["Denied by a parent<br/><small>hold released; note shown to kid</small>"]
  pending --> cancelled["Cancelled by the kid<br/><small>hold released; admins notified</small>"]
  pending --> expired["Expired (admin-set window)<br/><small>hold released; everyone notified</small>"]
```

A request reserves the money as soon as it's made. Only approval moves it, and every other outcome gives it back.

- **Create:** the kid picks a jar (default Spend), an amount and a reason, optionally linked to a goal. In one transaction the server checks `amount ≤ available(jar)` and inserts a `pending` row. Each kid can have at most 5 pending requests. Jar locks apply (§5.4): an admin lock refuses the request, and a self-lock sends the kid through the override gauntlet.
- **Decide:** any admin can approve or deny, with an optional note. The update is `… WHERE id = ? AND status = 'pending'`, so the first decision wins. The other parent's notification then reads "Approved by Sam" instead of showing buttons.
- **Approve a lower amount:** an admin may approve less than was asked ("$15, not $20"). The ledger entry uses the approved amount, and the request keeps both numbers.
- **Approval can't fail on funds.** Holds are already subtracted from available, so no other debit can spend held money. `allow_negative` applies only to admin debits; a request can never take a jar below zero.
- **Approve = handed over.** Approving records that the parent gave the money: cash, a purchase, a transfer. The app can't verify the handoff and doesn't try.
- **Expiry:** an admin sets the request window for the whole instance (Settings → Household → *Request expiry*, default 14 days, `0` = never). `expires_at` is fixed when a request is created, so a change only affects new requests. The background job moves stale requests to `expired`.

## 7. Features

### 7.1 Add and remove funds (admin)

- The form takes the kid, the amount, the jar (or "use split rule"), an optional comment, and an optional private note.
- Quick-pick chips for common reasons ("Chores", "Birthday", "Spent at store") are household-configurable and only prefill the comment.
- Removing funds posts a `withdrawal` or `adjustment` entry. It is refused when available would go below zero, unless `allow_negative` is on.
- Any entry can be **reversed** from the ledger view (one click plus a confirmation). Reversals can't themselves be reversed; post a new entry instead.
- Bulk action: "Give everyone $2" posts one entry per kid, all sharing a `batch_id`.

### 7.2 Recurring allowance

- Each kid has zero or more **schedules**: amount, cadence (`weekly` on a weekday, `biweekly` from an anchor date, or `monthly` on a day 1–31, clamped to the month's end), split rule (or a fixed jar), comment template, and an `active` flag.
- **The scheduler is in-process**: a ticker every minute, using the household timezone. A single replica is the norm (§9). The idempotency key `sched:<schedule_id>:<occurrence-date>` makes a duplicate run harmless anyway.
- **Catch-up:** after downtime, missed occurrences post with their original `effective_at` date, capped at 8. A box that was off for a week still pays Saturday's allowance once.
- Pause and resume per schedule, e.g. "no allowance while at camp."

### 7.3 Savings goals

- A goal has a name, a target amount, an optional emoji or picture, a jar (Save by default), and a priority.
- Kids and admins can create goals. Kids can edit and delete their own; admins can edit any.
- **Progress is a view, not money.** Goals don't hold funds. The jar's available balance fills goals in priority order (a waterfall), so moving money never strands it in a goal.
- Reaching a target notifies admins ("Ava reached *LEGO set*") and shows the kid a "Ask to buy it" button, which pre-fills a withdrawal request linked to the goal.

### 7.4 Parent-paid interest

- Set per kid and per jar (typically only Save) as a **monthly rate in basis points**, e.g. `100` = 1%/month. Off by default.
- Posted on the 1st of each month, for the previous month, on the **average daily balance**. A deposit on the 30th earns almost nothing, which removes the end-of-month gaming.
- Each daily balance is the end-of-day balance in the household timezone. Interest is floored to a whole cent and skipped if zero. The idempotency key is `interest:<kid>:<jar>:<YYYY-MM>`.
- Optional monthly cap (e.g. at most $5) so a generous rate on a big birthday haul stays affordable.
- The kid's Save jar shows a **"leave it and in 12 months you'll have …" projection**. That number is the lesson.

## 8. Notifications

Every notification is first a **row in `notifications`**, one per recipient. Email is an optional second channel that delivers a copy. If SMTP is down or never configured, nothing is lost.

### 8.1 Events

| Event | Admins | Kid | Email by default |
| --- | --- | --- | --- |
| Withdrawal requested | ✓ |  | Yes |
| Request approved / denied |  | ✓ | No (kids have no email) |
| Request cancelled by kid | ✓ |  | No |
| Funds added / removed |  | ✓ | No |
| Goal reached | ✓ | ✓ | Yes |
| Kid account locked (10 failed PINs) | ✓ |  | Yes |
| Recurring allowance or interest posted |  | ✓ | No (weekly digest optional) |
| SMTP send failing | ✓ |  | n/a (in-app banner) |
| Kid overrode or removed own jar lock | ✓ |  | No |

Each admin chooses per event type: in-app only, or in-app plus email.

### 8.2 In-app delivery

- A bell with an unread count in the header. HTMX polls `GET /notifications/count` every 30 s, which is cheap and needs no extra infrastructure.
- SSE (`hx-ext="sse"`) is a later upgrade if 30 s feels slow. The handler seam is the same.
- An admin's notification for a withdrawal request has **Approve / Deny buttons inline**. Most requests are handled without leaving the bell.

### 8.3 Email (SMTP)

- Configured in **Settings → Email** by an admin, or entirely by env vars (`GOLDBERRY_SMTP_*`), which override the UI and lock those fields.
- Fields: host, port, security (`starttls` | `tls` | `none`), username, password, From address, From name. A **"Send test email"** button reports the SMTP error verbatim.
- **Gmail preset:** `smtp.gmail.com`, port 587, STARTTLS. The username is the Gmail address and the password is a [Google app password](https://support.google.com/accounts/answer/185833), which needs 2-Step Verification on the account. The UI links to this. Any SMTP relay works (Fastmail, SES, a homelab Postfix).
- **The SMTP password is encrypted at rest** with AES-256-GCM, using a key derived from `GOLDBERRY_SECRET_KEY`. Without that env var, the UI refuses to store a password and points to env-var config.
- Each admin has an optional email address. Kids never have one.
- **Outbox pattern:** a notification that should email writes an `email_outbox` row in the same transaction. An in-process worker sends it with retry backoff (1 m, 5 m, 30 m, 2 h, then `failed`) and records the error. Request handlers never block on SMTP.
- Emails are plain text plus simple HTML, with a deep link to `GOLDBERRY_BASE_URL`. They contain the kid's display name, amount and reason, nothing more.

### 8.4 Later, not now

- **Web push** (VAPID) once there is a PWA manifest and HTTPS. This is the natural phone notification and needs no email.
- **Webhooks / ntfy / Home Assistant:** a generic `notifier` interface with SMTP as its first implementation, so these become new adapters rather than rewrites.

## 9. Storage

**SQLite on a volume by default; Postgres when `GOLDBERRY_DATABASE_URL` says so.** One household writes a few dozen rows a week, which SQLite handles without noticing. Postgres is there for people who already run one (or CloudNativePG on K3s) and want it in their existing backups.

|  | SQLite (default) | Postgres (optional) |
| --- | --- | --- |
| URL | `sqlite:///data/goldberry.db` | `postgres://user:pass@host:5432/goldberry?sslmode=…` |
| Driver | `modernc.org/sqlite`: pure Go, so `CGO_ENABLED=0`, a static binary and painless arm64 | `jackc/pgx/v5` via `database/sql` |
| Replicas | **Exactly 1.** The K8s Deployment uses the `Recreate` strategy | 1 by default. More work only because of idempotency keys and advisory locks |
| Write serialization | `BEGIN IMMEDIATE` plus a single-connection write pool | `SELECT … FOR UPDATE` on the kid row |
| Backup | Built-in `VACUUM INTO` snapshots (§12) | Your existing `pg_dump` / CNPG backups |

**SQLite connection pragmas:** `journal_mode=WAL`, `busy_timeout=5000`, `foreign_keys=ON`, `synchronous=NORMAL`. Use two pools: a read pool with N connections and a write pool with one.

**Never put the SQLite file on NFS or SMB.** WAL relies on shared memory and byte-range locks that network filesystems break, which silently corrupts data. On K3s use a block-backed RWO volume (local-path, Longhorn, Ceph RBD). The docs and the startup log both warn when `/data` is a network filesystem (detected via `statfs`).

### 9.1 How the pluggable store works

- **One `store.Store` interface**, defined in domain terms (`PostDeposit`, `CreateWithdrawalRequest`, `ListLedger`…), not tables. Handlers and services only see the interface.
- **Hand-written SQL in a portable subset**, not an ORM. Write queries with `?` placeholders; the Postgres dialect rebinds them to `$n`. Both engines support `ON CONFLICT … DO NOTHING` and `RETURNING` (SQLite ≥ 3.35). The rare divergent query lives in a `dialect` method.
- **sqlc was considered and rejected:** it generates a separate, incompatible package per engine, which doubles the code we are trying to share.
- **Portable-subset rules:** timestamps come from Go (never `now()` in SQL) and are stored UTC (`TIMESTAMPTZ` in Postgres, RFC 3339 `TEXT` in SQLite). IDs are UUIDv7 strings, which sort by time and don't depend on the dialect. Money is `BIGINT`. Booleans are `BOOLEAN` in Postgres and `INTEGER` 0/1 in SQLite, which both drivers scan into a Go `bool`.
- **Migrations:** `pressly/goose` with two embedded trees, `migrations/sqlite/` and `migrations/postgres/`, kept in lockstep: the same numbered files and the same intent. CI fails if the file lists differ.
- **Migrations run at startup.** This departs from Copperkeep's rule on purpose: there is one binary and normally one replica, so there is no separate migration Job to order. Postgres takes a `pg_advisory_lock` first. `goldberry migrate` also exists for anyone who wants it as an explicit step.
- **Contract test suite** (`store/storetest`): every behavior test (balances, holds, reversals, idempotency, the double-submit race) runs against both engines. CI runs Postgres as a service container. This suite is the real definition of "pluggable"; the interface only documents intent.
- **Moving between backends:** `goldberry export` writes a versioned JSONL dump of every table; `goldberry import` loads it into an empty database of either kind. It doubles as a portable backup format.

## 10. Data model

The types are shown Postgres-style; the SQLite migration uses the equivalents from §9.1. All IDs are UUIDv7 `TEXT` and all amounts are `BIGINT` minor units.

```sql
-- Household (exactly one row)
household(id, name, currency CHAR(3), timezone,
          pin_length, allow_negative, request_expiry_days, kid_picker_login,
          created_at)

-- Identity
users(id, role,                         -- admin | kid
      username UNIQUE, display_name, avatar,
      password_hash,                    -- admins only (argon2id)
      pin_hash,                         -- kids only (argon2id)
      email,                            -- admins only, optional
      disabled_at, created_at)

sessions(id_hash PK, user_id, device_label, remember,
         created_at, last_seen_at, expires_at, revoked_at)

auth_attempts(user_id PK, failures, window_start, next_allowed_at, locked_at)

-- Money
jars(id, kid_id, name, kind,           -- spend | save | give | custom
     sort_order, archived_at)

split_rules(kid_id, jar_id, basis_points)   -- sums to 10000 per kid
  PRIMARY KEY (kid_id, jar_id)

ledger_entries(                          -- APPEND-ONLY: no UPDATE, no DELETE
  id, kid_id, jar_id,
  amount BIGINT,                         -- signed: + credit, - debit
  kind,                                  -- deposit | withdrawal | adjustment | allowance
                                         -- | interest | transfer_in | transfer_out | reversal
  comment, private_note,
  actor_id,                              -- user who caused it; NULL = system (scheduler)
  reverses_id,                           -- set on kind = reversal
  transfer_id, batch_id,                 -- groups pairs / bulk posts
  request_id,                            -- set when it settles a withdrawal request
  schedule_id,
  idempotency_key UNIQUE,
  effective_at,                          -- business date (catch-up keeps the original date)
  created_at)
  INDEX (jar_id, effective_at), INDEX (kid_id, created_at)

withdrawal_requests(
  id, kid_id, jar_id, amount BIGINT, reason, goal_id,
  status,                                -- pending | approved | denied | cancelled | expired
  decided_by, decision_note, decided_at,
  idempotency_key UNIQUE,
  created_at, expires_at)
  INDEX (jar_id) WHERE status = 'pending'

schedules(id, kid_id, amount, cadence,  -- weekly | biweekly | monthly
          weekday, day_of_month, anchor_date,
          target_jar_id,                 -- NULL = use split rule
          comment_template, active, last_occurrence, created_by, created_at)

interest_rules(kid_id, jar_id, monthly_bps, monthly_cap, active)
  PRIMARY KEY (kid_id, jar_id)

goals(id, kid_id, jar_id, name, emoji, target_amount, priority,
      created_by, reached_at, archived_at, created_at)

-- Jar locks (§5.4)
jar_locks(id, jar_id,
          to_jar_id,                     -- NULL = every route out + withdrawal requests
          set_by_role,                   -- admin (hard) | kid (soft)
          set_by, reason, until_date, created_at, removed_by, removed_at)

lock_overrides(id, lock_id, kid_id, action,  -- once | removed
               ledger_entry_id, request_id, created_at)

-- Notifications
notifications(id, recipient_id, kind, payload JSON,
              request_id, read_at, created_at)
  INDEX (recipient_id, read_at)

notification_prefs(user_id, kind, email BOOLEAN)
  PRIMARY KEY (user_id, kind)

email_outbox(id, notification_id, to_addr, subject, body_text, body_html,
             attempts, next_attempt_at, last_error, sent_at, failed_at)

settings(key PK, value, secret BOOLEAN)  -- SMTP config; secret values AES-GCM encrypted

-- Audit (non-money admin actions: PIN reset, unlock, user added, settings changed)
audit_log(id, actor_id, action, target_id, detail JSON, created_at)
```

**Why `effective_at` and `created_at` both exist:** catch-up allowance and back-dated entries ("forgot to log last Saturday's chores") need a business date for the kid's ledger. `created_at` is the immutable audit time. Interest and the ledger view use `effective_at`.

**Why holds aren't ledger entries:** a hold isn't money moving. Keeping holds as `withdrawal_requests.status = 'pending'` means approval posts exactly one ledger entry, and denial posts none.

## 11. Routes & UI

The app is server-rendered with Go `html/template`, and HTMX swaps fragments. HTMX and the CSS are **vendored into the binary** (`embed.FS`), not pulled from a CDN, so the app works on a LAN with no internet. There is no JS build step.

### 11.1 Routes

| Route | Who | Purpose |
| --- | --- | --- |
| `GET /setup`, `POST /setup` | First run only | Household and first admin; needs the setup token (§4.4) |
| `GET /login`, `POST /login` | Anyone | Username, then a password or a PIN pad depending on the account's role |
| `GET /login/kids` | Anyone | Optional kid-picker tiles (household setting) |
| `POST /logout` | Signed in | Revoke the session |
| `GET /` | Kid | Home: jars, available vs pending, goals, recent activity |
| `GET /ledger` | Kid | Own history, filter by jar |
| `POST /requests`, `POST /requests/:id/cancel` | Kid | Create or cancel a withdrawal request |
| `POST /goals`, `POST /goals/:id` | Kid | Create or edit own goals |
| `POST /transfers` | Kid | Move between own jars, subject to jar locks (§5.4) |
| `POST /jars/:id/locks`, `POST /locks/:id/{override,remove}` | Kid | Set a self-lock; override or remove one after the gauntlet (§5.4) |
| `GET /admin` | Admin | Dashboard: a card per kid plus the pending-requests queue |
| `GET /admin/kids/:id` | Admin | One kid: jars, locks, ledger, schedules, interest, goals |
| `POST /admin/kids/:id/entries` | Admin | Add or remove funds |
| `POST /admin/entries/:id/reverse` | Admin | Reverse a ledger entry |
| `POST /admin/requests/:id/{approve,deny}` | Admin | Decide a request, with an optional note |
| `/admin/users/*` | Admin | Add a kid or admin, reset a PIN or password, unlock, disable, revoke sessions |
| `/admin/settings/*` | Admin | Household, jars and split rules, schedules, interest, email and SMTP test, export |
| `GET /notifications`, `GET /notifications/count`, `POST /notifications/read` | Signed in | The bell |
| `GET /healthz`, `GET /readyz`, `GET /metrics` | Unauthenticated | Liveness, DB readiness, Prometheus |

The kid routes sit at the root because a kid's home screen is the product; admin routes sit under `/admin`. Middleware enforces the role on every route group.

**JSON API: seam only.** Handlers call a `service` layer, never the store directly, so a `/api/v1` (for Home Assistant, a phone widget, or scripts) can be added later with no refactor. It isn't built in v1.

### 11.2 UI notes

- **Mobile first.** Most use is a phone (parent) or a tablet (kid). Tap targets are at least 44 px and the PIN pad uses big digits.
- **Kid home shows one big number per jar**, with "$3.00 waiting for approval" under it when holds exist. Never show a kid a balance they can't actually request.
- Goals show progress bars, and an emoji or picture beats text for young kids.
- Light and dark themes follow `prefers-color-scheme`.
- A PWA manifest and icons are included, so "Add to Home Screen" gives the kids an app icon. A service worker comes later, with web push (§8.4).
- Currency formatting comes from the household currency (`golang.org/x/text/currency`). The UI ships English-only, with templates written so i18n can be added later.

## 12. Deployment

### 12.1 The image

- `ghcr.io/chinny/goldberry:<semver>` (plus `:<major>.<minor>` and `:latest`). Built multi-arch for `linux/amd64` and `linux/arm64`, so it runs on a Pi 5, an N100 box, or any K3s node.
- Base image `gcr.io/distroless/static-debian12:nonroot`: no shell, UID 65532, roughly 15–20 MB in total.
- Built with **`ko`**. It suits a pure-Go, `CGO_ENABLED=0` binary: no Dockerfile, reproducible output, and multi-arch in one command. A `Dockerfile` is kept too for anyone building locally.
- Each release is signed with cosign (keyless, via GitHub OIDC) and gets an SBOM (syft) and SLSA provenance.
- `VOLUME /data` and `EXPOSE 8080`. Distroless has no `curl`, so the binary has an `goldberry healthcheck` subcommand for Docker's `HEALTHCHECK`.
- It needs very little: about a 32 Mi / 10m request. It is idle almost all the time.

### 12.2 Configuration

Configuration is env vars only, and a `/data/config.env` file is optional. Every variable has a sane default except the secret key.

| Variable | Default | Notes |
| --- | --- | --- |
| `GOLDBERRY_DATABASE_URL` | `sqlite:///data/goldberry.db` | Or `postgres://…` |
| `GOLDBERRY_SECRET_KEY` | *(none)* | 32+ random bytes, base64 (or GOLDBERRY\_SECRET\_KEY\_FILE). Encrypts SMTP secrets. Without it the app still runs but refuses to store SMTP passwords. |
| `GOLDBERRY_BASE_URL` | *(none)* | Public URL for email links, e.g. `https://goldberry.home.example.com` |
| `GOLDBERRY_LISTEN` | `:8080` |  |
| `GOLDBERRY_TRUSTED_PROXIES` | *(none)* | CIDRs whose `X-Forwarded-*` headers are trusted |
| `GOLDBERRY_INSECURE_COOKIES` | `false` | Drops the cookie `Secure` flag for plain-HTTP testing. Logs a warning on every start. |
| `GOLDBERRY_BACKUP_SCHEDULE` / `_RETAIN` | `03:15` / `14` | SQLite snapshots (§12.5) |
| `GOLDBERRY_SMTP_HOST`, `_PORT`, `_SECURITY`, `_USERNAME`, `_PASSWORD`, `_FROM` | *(none)* | Override and lock the UI's email settings. `_PASSWORD_FILE` is also accepted, for Docker and K8s secrets. |
| `GOLDBERRY_LOG_FORMAT` | `json` | `json` or `text` (`log/slog`) |
| `TZ` | `UTC` | Default for the household timezone at setup |

### 12.3 Docker Compose

```yaml
services:
  goldberry:
    image: ghcr.io/chinny/goldberry:1
    restart: unless-stopped
    ports: ["8080:8080"]          # or put it behind Caddy/Traefik for TLS
    environment:
      GOLDBERRY_SECRET_KEY_FILE: /run/secrets/goldberry_key
      GOLDBERRY_BASE_URL: https://goldberry.home.example.com
      TZ: America/New_York
    volumes:
      - ./data:/data
    secrets: [goldberry_key]
secrets:
  goldberry_key:
    file: ./goldberry_key.txt     # openssl rand -base64 32 > goldberry_key.txt
```

### 12.4 Kubernetes (K3s)

- A small Helm chart lives in `deploy/helm/goldberry` and is published as an OCI artifact next to the image. It contains a Deployment (`replicas: 1`, `strategy: Recreate`), a PVC (RWO, block-backed, never NFS: §9), a Secret, a Service, and an optional Ingress or Gateway API `HTTPRoute`.
- The chart refuses `replicaCount > 1` while the database is SQLite (`values.schema.json` plus a template `fail`).
- `postgres.external.url` switches to Postgres. The chart never bundles a Postgres subchart.
- Security context: `readOnlyRootFilesystem`, `runAsNonRoot`, all capabilities dropped. The only writable path is `/data`.

### 12.5 TLS and backups

- **TLS terminates at the reverse proxy** (Traefik, Caddy, a Cilium Gateway). The app speaks plain HTTP inside the cluster. Secure cookies and the future PWA push both need HTTPS, so a LAN-only install should still use a real certificate via a DNS-01 challenge, as Copperkeep does.
- **SQLite backups are built in.** A nightly `VACUUM INTO '/data/backups/goldberry-YYYYMMDD.db'` produces a consistent snapshot without stopping the app, keeping the last 14. `goldberry backup` runs one on demand. Copy `/data/backups` off the box (restic, NAS sync); a backup on the same disk is not a backup.
- **Restore:** stop the app, replace `goldberry.db`, delete `-wal` and `-shm`, start. CI runs a backup → restore → balances-match test, so the restore path is tested.
- Postgres installs use their existing `pg_dump` or CNPG backups. `goldberry export` is a portable fallback for both backends.

## 13. Repository & build

One public AGPL-3.0 repo, chinny/goldberry, with the Go module, migrations, deploy files and docs together. Docs change in the same PR as the code. AGPL §13 applies because users reach the app over a network, so every page footer links to the source of the running version: the repo URL plus the commit, baked in at build time.

### 13.1 Stack

- Latest stable Go. The router is stdlib `net/http` `ServeMux` with method and path patterns; no web framework.
- Logging is `log/slog`. Metrics use `prometheus/client_golang`.
- `alexedwards/argon2id` for hashing, `pressly/goose` for migrations, `modernc.org/sqlite`, `jackc/pgx/v5`, and `wneessen/go-mail` for SMTP.
- HTMX 2.x and a small hand-written CSS file (or vendored Pico.css), both under `web/static`.

### 13.2 Layout

```
goldberry/
├── cmd/goldberry/            # serve | migrate | backup | export | import | admin | healthcheck
├── internal/
│   ├── config/               # env parsing, _FILE secrets
│   ├── money/                # minor units, parse/format, split with remainder
│   ├── auth/                 # argon2id, sessions, throttling, CSRF
│   ├── service/              # ledger, requests, goals, schedules, interest (domain rules)
│   ├── scheduler/            # in-process ticker: allowance, interest, request expiry, backups
│   ├── notify/               # notifications, Notifier interface, smtp, outbox worker
│   ├── store/                # Store interface
│   │   ├── sqlite/
│   │   ├── postgres/
│   │   └── storetest/        # contract suite, run against both
│   └── web/                  # handlers, middleware, templates/, static/
├── migrations/{sqlite,postgres}/
├── deploy/{compose,helm/goldberry}/
├── e2e/                      # Playwright smoke tests against the built image
├── docs/{adr,operations}/
├── .ko.yaml  Dockerfile  justfile
└── README.md                 # 60-second Compose quickstart first
```

### 13.3 CI (GitHub Actions)

1. `golangci-lint`, `go vet`, `gofmt` check.
2. `go test -race ./...` with the store contract suite in a **matrix: SQLite and Postgres** (service container).
3. Migration parity: `migrations/sqlite` and `migrations/postgres` must list the same version numbers.
4. Build the image with `ko`, start it, run the e2e smoke test: setup → add kid → deposit → kid login → request → admin approve → balances correct. Then backup → restore → balances match.
5. `helm lint` and a `kind` or `k3d` install smoke test.
6. On a tag: multi-arch push to GHCR, cosign sign, SBOM, chart pushed as OCI, and a GitHub Release with notes.

Versioning is SemVer via conventional commits and release-please. Renovate keeps Go modules, HTMX and Actions current.

### 13.4 Decision records

As in Copperkeep, use `docs/adr/`. Seed it with these:

| # | Decision |
| --- | --- |
| 0001 | One container; Go + HTMX, no SPA |
| 0002 | SQLite by default, Postgres optional, behind one `Store` and a shared contract suite |
| 0003 | Money is an append-only ledger in integer minor units |
| 0004 | Withdrawal requests hold funds on creation |
| 0005 | In-process scheduler with idempotency keys, no cron sidecar |
| 0006 | Migrations run at startup (departs from Copperkeep, and why) |
| 0007 | Email via an outbox; in-app is the source of truth |
| 0008 | Per-account throttling; kids hard-lock, admins never do |
| 0009 | Jar locks: admin locks are hard, kid self-locks are soft with an override gauntlet |
| 0010 | AGPL-3.0 licence, matching Copperkeep |

## 14. Phasing

Each phase ends with something the family can use. Phase 1 alone replaces a spreadsheet.

1. **Phase 0: Skeleton (an evening).** Repo, `go mod`, `/healthz`, config loading, `ko` multi-arch build, CI matrix with an empty store contract suite on SQLite and Postgres, and a Compose file. *Exit:* `docker compose up` serves `/healthz` on amd64 and arm64.
2. **Phase 1: Walking skeleton.** First-run setup with the token, admin and kid login **with throttling**, sessions and CSRF, one jar per kid, add and remove funds with comments, reversals, kid home and ledger, and the `money` package with tests. *Exit:* a kid signs in on the family tablet and sees a deposit a parent made from their phone.
3. **Phase 2: Requests & notifications.** Withdrawal requests with holds, approve, deny, cancel and expiry, the in-app bell with inline approve/deny, the idempotency keys, and the double-submit race test. *Exit:* request → notification → approve in under 10 seconds, end to end.
4. **Phase 3: Email.** SMTP settings and env override, encrypted secret, Gmail preset, test button, outbox worker with retries, and per-admin preferences. *Exit:* a request emails both parents through Gmail with an app password.
5. **Phase 4: Jars & recurring allowance.** Configurable jars, split rules with remainder handling, transfers, schedules with catch-up, and pause. *Exit:* Saturday's allowance posts split 70/20/10 with no one touching it, including after a reboot on Friday night.
6. **Phase 5: Goals & interest.** Goals waterfall, goal-reached notifications, "Ask to buy it", monthly interest on average daily balance with a cap, and the 12-month projection.
7. **Phase 6: v1.0 release shape.** Helm chart and OCI publish, built-in backups and tested restore, export/import, PWA manifest, cosign/SBOM, docs (`install`, `upgrade`, `backup-restore`, `troubleshooting`), and README screenshots.
8. **Later.** Web push, a `/api/v1` JSON API, a Home Assistant integration, an ntfy/webhook notifier, OIDC for admins (Authentik/Pocket ID), parent-matching on goals, i18n, and chore tracking ("earn by task").

## 15. Risks, decisions & open questions

### 15.1 Open risks

| Risk | Mitigation |
| --- | --- |
| SQLite on an NFS-backed PVC corrupts the database | `statfs` check with a loud startup warning; docs and chart notes say RWO block storage only |
| A kid brute-forces a sibling's PIN | Per-account backoff, hard lock at 10 failures, admin notification, parent-only unlock |
| SQLite and Postgres behavior drifts apart | Shared contract suite in a CI matrix; migration parity check |
| Scheduler double-posts allowance or interest | Unique idempotency keys per occurrence; a duplicate run is a no-op |
| DST or timezone bugs in schedules and interest | All business dates in the household TZ; table tests across DST transitions |
| Gmail tightens app passwords or SMTP | Any SMTP relay works; `Notifier` interface; web push planned |
| `GOLDBERRY_SECRET_KEY` lost or rotated | Only the SMTP password is encrypted with it: re-enter it. The app warns when decryption fails. |
| Single-disk data loss | Nightly `VACUUM INTO` snapshots, off-box copy documented, restore tested in CI |
| Kid disputes the interest math | Each interest entry's comment shows the average balance, the rate and the days |

### 15.2 Decisions log

| Decision | Rationale |
| --- | --- |
| Go + HTMX, server-rendered | Static binary, tiny image, trivial arm64, no JS toolchain |
| SQLite default, Postgres optional | Zero ops for a family; reuse existing Postgres when it's there |
| Hand-written portable SQL, not sqlc or an ORM | sqlc generates per-engine packages; an ORM hides the money-critical SQL |
| `modernc.org/sqlite` (no CGO) | Static distroless image, painless cross-compile |
| Append-only ledger, integer cents | Auditable history, reversals instead of edits, no float bugs |
| Holds on request | Kids can't over-request across several requests; available is always honest |
| Holds are request rows, not ledger entries | Approve = one ledger entry; deny = none |
| Balances computed, not cached | A few thousand rows; a cache is a consistency bug waiting to happen |
| Single household per install | No tenancy column; one family = one container |
| PIN for kids, hashed with argon2id | The DB file must not leak PINs reused elsewhere |
| Kids hard-lock; admins only back off | A parent can unlock a kid; nobody can unlock the last admin except the CLI |
| `SameSite=Lax` plus a CSRF token | Email deep links must land signed in |
| In-app first, email via outbox | SMTP failure never loses a notification or blocks a request |
| In-process scheduler | One replica; idempotency keys make catch-up and duplicates safe |
| Migrations at startup | One binary, one replica; advisory lock on Postgres |
| Interest on average daily balance | Removes end-of-month deposit gaming |
| Goals are views over a jar | Money never gets stranded inside a goal |
| No Redis, IdP, SPA or JSON API in v1 | Defer every optional subsystem; ship the seams |
| Kids move money freely; locks restrict | Autonomy by default; restriction is an explicit, visible choice |
| Self-locks are soft, admin locks are hard | Kids practise commitment without being trapped by it; parents keep authority |
| Approve = handed over, not tracked separately | The app records decisions; it can't see cash change hands |
| AGPL-3.0, in chinny | Modified hosted versions must share their source; matches Copperkeep |

### 15.3 Open questions

- [x] **Name:** Goldberry, after Tom Bombadil's River-daughter. Repo `chinny/goldberry`, image `ghcr.io/chinny/goldberry`, env prefix `GOLDBERRY_*`.
- [x] **Licence:** AGPL-3.0.
- [x] **Org:** `chinny`.
- [x] **Request expiry:** set by an admin per instance, default 14 days (§6).
- [x] **Approve = handed over:** yes, and not tracked separately (§6).
- [x] **Kid transfers:** free by default; admin locks are hard, self-locks are soft (§5.4).
