#!/usr/bin/env bash
# Build the image, start it, and run the smoke test against it.
#   e2e/run-image.sh [image-tag]
set -euo pipefail
cd "$(dirname "$0")/.."
tag=${1:-goldberry:e2e}
docker build -t "$tag" --build-arg COMMIT="$(git rev-parse HEAD)" .
name=goldberry-e2e-$$
docker run -d --name "$name" -p 18080:8080 -e GOLDBERRY_INSECURE_COOKIES=true "$tag" >/dev/null
trap 'docker logs "$name" > e2e/server.log 2>&1 || true; docker rm -f "$name" >/dev/null' EXIT
for _ in $(seq 1 30); do
  if docker exec "$name" /usr/local/bin/goldberry healthcheck 2>/dev/null; then break; fi
  sleep 1
done
token=$(docker logs "$name" 2>&1 | grep -o '"setup_token":"[^"]*"' | head -1 | cut -d'"' -f4)
[ -n "$token" ] || { echo "no setup token in logs" >&2; exit 1; }
(cd e2e && BASE_URL=http://localhost:18080 SETUP_TOKEN="$token" node smoke.mjs)
