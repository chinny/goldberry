# Upgrade

Goldberry follows semantic versioning. Image tags: `1.2.3`, `1.2`, `1`, `latest`. Pin to a major (`:1`) to get fixes and features without breaking changes.

1. **Back up first.** SQLite: `goldberry backup` (or copy last night's file from `/data/backups`). Postgres: `pg_dump`.
2. **Pull and restart.**
   - Compose: `docker compose pull && docker compose up -d`
   - Helm: `helm upgrade goldberry oci://ghcr.io/chinny/charts/goldberry --version <new> --reuse-values`
3. Migrations run on startup (under an advisory lock on Postgres) and are logged. `goldberry migrate` runs them on their own if you prefer an explicit step.

Downgrades aren't supported once a newer version has migrated the database: restore the backup you took in step 1 instead.

Release notes and the changelog are on the [GitHub releases page](https://github.com/chinny/goldberry/releases). Images are signed with cosign (keyless); verify with:

```sh
cosign verify ghcr.io/chinny/goldberry:1.0.0 \
  --certificate-identity-regexp 'https://github.com/chinny/goldberry/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```
