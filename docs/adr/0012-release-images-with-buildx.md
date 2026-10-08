# 0012. Release images are built with the Dockerfile and buildx, not ko

- **Status:** Accepted (supersedes the `ko` part of plan §12.1)
- **Date:** 2026-10-08

## Context

The plan picked `ko` for a reproducible multi-arch image with no Dockerfile. In practice a `ko` image has no `/data` directory owned by the nonroot user (UID 65532), no `VOLUME` and no `HEALTHCHECK`. A Docker named volume mounted at `/data` then comes up owned by root, and Goldberry can't create its database: the Compose quickstart fails on first run.

## Decision

Releases build the existing Dockerfile with `docker buildx` for `linux/amd64` and `linux/arm64`. The final stage stays `distroless/static-debian12:nonroot` with the same static `CGO_ENABLED=0` binary, and adds `/data` owned by 65532, `VOLUME /data`, `HEALTHCHECK goldberry healthcheck` and OCI labels. buildx also attaches SBOM and SLSA provenance attestations; cosign signs the digest keylessly. `.ko.yaml` and the `ko` CI job are removed.

## Consequences

The Compose quickstart works with a named volume. Builds need Docker/buildx (and QEMU for the other architecture) rather than only Go. Kubernetes still sets `fsGroup: 65532` in the chart, so either image style would work there.
