# Backup and restore

## SQLite (the default)

Every night at `GOLDBERRY_BACKUP_SCHEDULE` (default `03:15`, in `TZ`) Goldberry writes a consistent snapshot with `VACUUM INTO` to `/data/backups/goldberry-YYYYMMDD.db`, without stopping, and keeps the newest `GOLDBERRY_BACKUP_RETAIN` (default 14). Take one on demand with:

```sh
docker compose exec goldberry goldberry backup          # Compose
kubectl exec deploy/goldberry -- goldberry backup        # Kubernetes
```

**Copy `/data/backups` off the machine** (restic, a NAS sync, rclone): a backup on the same disk is not a backup.

### Restore

1. Stop Goldberry (`docker compose stop`, or scale the Deployment to 0).
2. Replace `/data/goldberry.db` with the snapshot, and delete `goldberry.db-wal` and `goldberry.db-shm` if they exist.
3. Start it again. CI runs a backup → restore → balances-match test, so this path is exercised on every change.

## Postgres

Use your existing `pg_dump` or operator backups (CloudNativePG, etc.). Goldberry's built-in snapshots are SQLite-only.

## Portable export (both engines)

```sh
goldberry export -o goldberry-dump.jsonl     # every table, typed, one JSON object per line
goldberry import goldberry-dump.jsonl        # into an empty database of either kind
```

The dump is versioned, loads in one transaction (all or nothing), and refuses a database that already has users. Sign-in sessions aren't included, so everyone signs in again after an import. Use it to move between SQLite and Postgres, or as an extra, engine-independent backup.
