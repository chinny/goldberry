# Troubleshooting

**Every page redirects to /setup.** No parent exists yet. Get the setup token from the logs (`docker compose logs goldberry | grep -i "setup token"`).

**Signing in loops back to the login page.** The browser dropped the `Secure` session cookie because the site is plain HTTP. Put Goldberry behind HTTPS, or for a quick trial set `GOLDBERRY_INSECURE_COOKIES=true`.

**"That form expired."** The CSRF token didn't match: usually a page left open across a restart or sign-out. Reload and try again.

**A kid is locked out.** Ten wrong PINs hard-lock a kid account. A parent unlocks it (People → the kid → Unlock) or resets the PIN; nothing unlocks on a timer.

**Every parent is locked out / forgot the password.** Run `goldberry admin reset-password <username>` in the container; it prints a new password.

**"permission denied" creating /data/goldberry.db.** The container runs as UID 65532. With a host folder (`./data:/data`), run `sudo chown 65532:65532 data` first, or use the named volume from the Compose file. In Kubernetes the chart sets `fsGroup: 65532`.

**Startup warns about a network filesystem.** `/data` is on NFS/SMB/FUSE. Move it to a local or block-backed volume before you store real data: SQLite's WAL can corrupt on network filesystems.

**The allowance didn't post.** It posts on the first scheduler tick after midnight of the paying day in the household time zone (Settings). Check the allowance isn't paused, and that the household time zone is right. After downtime it catches up to 8 missed days automatically.

**Interest is lower than expected.** It's on the *average daily* balance for the month, so money added late in the month earns little. Each interest entry's comment shows the average, the rate and the number of days.

**Health checks.** `/healthz` (process up), `/readyz` (database reachable), `/metrics` (Prometheus). `goldberry healthcheck` exits 0 when the local server answers, for Docker's `HEALTHCHECK`.

**Logs.** JSON by default (`GOLDBERRY_LOG_FORMAT=text` for humans). Set `GOLDBERRY_DEBUG=1` for per-request logs of static files and health checks.
