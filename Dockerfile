# syntax=docker/dockerfile:1
# Local and release builds. `ko` (see .ko.yaml) builds the same binary onto
# the same base; this file additionally creates /data owned by the nonroot
# user, declares the volume and wires Docker's HEALTHCHECK.
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
COPY --from=build /out/goldberry /usr/local/bin/goldberry
COPY --from=build --chown=65532:65532 /out/data /data
ENV GOLDBERRY_DATABASE_URL=sqlite:///data/goldberry.db
VOLUME /data
EXPOSE 8080
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/usr/local/bin/goldberry", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/goldberry"]
CMD ["serve"]
