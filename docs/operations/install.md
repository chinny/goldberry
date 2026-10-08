# Install

Goldberry is one container (`ghcr.io/chinny/goldberry`, linux/amd64 and linux/arm64) plus somewhere to keep its data: a volume for SQLite (the default), or an external Postgres.

Put it behind a TLS reverse proxy (Caddy, Traefik, a Cilium or Envoy Gateway). Sessions use `Secure` cookies, so plain HTTP only works with `GOLDBERRY_INSECURE_COOKIES=true`, which is for trying it out.

## Docker Compose

```sh
mkdir goldberry && cd goldberry
curl -fsSLO https://raw.githubusercontent.com/chinny/goldberry/main/deploy/compose/compose.yaml
openssl rand -base64 32 > goldberry_key.txt
docker compose up -d
docker compose logs goldberry | grep -i "setup token"
```

Open `https://<your host>/setup`, paste the token, and create the first parent. The token is printed on every start until the first parent exists, and nobody without it can claim the install.

## Kubernetes (Helm)

```sh
helm install goldberry oci://ghcr.io/chinny/charts/goldberry --version 1.0.0 \
  --namespace goldberry --create-namespace \
  --set timezone=America/New_York \
  --set baseURL=https://goldberry.home.example.com \
  --set httpRoute.enabled=true --set 'httpRoute.parentRefs[0].name=my-gateway' \
  --set 'httpRoute.hostnames[0]=goldberry.home.example.com'
kubectl logs -n goldberry deploy/goldberry | grep -i "setup token"
```

- With SQLite the chart runs exactly one replica with the `Recreate` strategy, on a `ReadWriteOnce` claim. Use a block-backed storage class (local-path, Longhorn, Ceph RBD). **Never NFS or SMB**: SQLite's WAL mode silently corrupts there. Goldberry logs a warning at startup if `/data` is a network filesystem.
- The pod runs as UID 65532 with a read-only root filesystem and every capability dropped; `/data` and `/tmp` are the only writable paths.
- The chart generates `GOLDBERRY_SECRET_KEY` and keeps it across upgrades, or use `secretKey.existingSecret`.
- `ingress.enabled` or `httpRoute.enabled` expose it; see `values.yaml` for every option.

## Postgres instead of SQLite

Set `GOLDBERRY_DATABASE_URL=postgres://user:pass@host:5432/goldberry?sslmode=require` (Compose), or `postgres.external.url` / `postgres.external.existingSecret` (Helm). Migrations run at startup under an advisory lock. With Postgres the chart allows more than one replica. Back it up with `pg_dump` or your operator (e.g. CloudNativePG).

To move an existing SQLite household to Postgres: `goldberry export -o dump.jsonl` on the old install, then `goldberry import dump.jsonl` against the empty Postgres. See [backup-restore.md](backup-restore.md).

## Configuration

Every setting is an environment variable (or `NAME_FILE` pointing at a file, for secrets); a `/data/config.env` file supplies defaults. The full table is in the [README](../../README.md#configuration).
