# syntax=docker/dockerfile:1
# The release image (ADR 0012): built multi-arch with buildx. A static
# CGO_ENABLED=0 binary on distroless, /data owned by the nonroot user, the
# volume declared and Docker's HEALTHCHECK wired to `goldberry healthcheck`.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
ARG COMMIT=
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X github.com/chinny/goldberry/internal/buildinfo.Version=${VERSION} -X github.com/chinny/goldberry/internal/buildinfo.Commit=${COMMIT}" \
      -o /out/goldberry ./cmd/goldberry \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
LABEL org.opencontainers.image.title="goldberry" \
      org.opencontainers.image.description="Self-hosted allowance tracker for kids" \
      org.opencontainers.image.source="https://github.com/chinny/goldberry" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.version="${VERSION}"
COPY --from=build /out/goldberry /usr/local/bin/goldberry
COPY --from=build --chown=65532:65532 /out/data /data
ENV GOLDBERRY_DATABASE_URL=sqlite:///data/goldberry.db
VOLUME /data
EXPOSE 8080
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/usr/local/bin/goldberry", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/goldberry"]
CMD ["serve"]
