# Goldberry tasks. `just` lists them.
set dotenv-load := false

pg_url := env_var_or_default("GOLDBERRY_TEST_POSTGRES_URL", "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")

default:
    @just --list

# Run the app locally on :8080 with a SQLite file in ./data (plain HTTP).
run:
    GOLDBERRY_DATABASE_URL=sqlite://./data/goldberry.db GOLDBERRY_INSECURE_COOKIES=true GOLDBERRY_LOG_FORMAT=text go run ./cmd/goldberry

# Build the binary into ./bin.
build:
    CGO_ENABLED=0 go build -trimpath -o bin/goldberry ./cmd/goldberry

# Unit tests plus the store contract suite on SQLite.
test:
    go test -race ./...

# The store contract suite on Postgres (needs a server; see pg_url).
test-pg:
    GOLDBERRY_TEST_POSTGRES_URL='{{pg_url}}' go test -race ./internal/store/...

# Start a throwaway Postgres 16 in Docker for test-pg.
pg-up:
    docker run -d --rm --name goldberry-pg -e POSTGRES_PASSWORD=postgres -p 5432:5432 postgres:16

# gofmt, vet, golangci-lint and migration parity.
lint:
    test -z "$(gofmt -l .)"
    go vet ./...
    golangci-lint run ./...
    scripts/check-migrations.sh

# Build the image with the Dockerfile.
image tag="goldberry:dev":
    docker build -t {{tag}} --build-arg COMMIT=$(git rev-parse HEAD) .

# Multi-arch image build with buildx (no push), as the release does it.
image-multiarch:
    docker buildx build --platform linux/amd64,linux/arm64 --build-arg COMMIT=$(git rev-parse HEAD) .

# Lint and render the Helm chart.
helm:
    helm lint deploy/helm/goldberry --strict
    helm template gb deploy/helm/goldberry > /dev/null

# Snapshot the local SQLite database (./data) now.
backup:
    GOLDBERRY_DATABASE_URL=sqlite://./data/goldberry.db go run ./cmd/goldberry backup

# Build the image, run it, and drive the Playwright smoke test against it.
e2e:
    cd e2e && npm ci
    e2e/run-image.sh
