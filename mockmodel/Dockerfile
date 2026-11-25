# Local image for the deterministic mock chat model (P0-T5). Build from the
# repository root:  docker build -f mockmodel/Dockerfile -t agentplane/mockmodel:p0 .
# (or `make mock-image`). It is never pushed anywhere: no registry credentials
# and no cloud access are used or needed.
#
# Both base images are pinned by the digest of their multi-arch (amd64 and
# arm64) OCI index, so the build is reproducible and a moved tag cannot change
# it. Provenance and how to update them: docs/local-development.md.
#   build:   golang:1.27.3-bookworm           (go.mod declares go 1.27.3)
#   runtime: gcr.io/distroless/static-debian13:nonroot  (no shell, no package
#            manager, no writable root requirement, user 65532)

FROM golang:1.27.3-bookworm@sha256:dc0963f32053e4bc451d24d87e3892d42d01021b61f093e98eb95c0352bb84b0 AS build
# GOTOOLCHAIN=local: never download a different toolchain during the build.
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-mod=readonly
WORKDIR /src
# Only what the binary needs. The command and library import the standard
# library alone (the kin-openapi dependency in go.mod is test-only).
COPY go.mod go.sum ./
COPY mockmodel ./mockmodel
COPY cmd/mockmodel ./cmd/mockmodel
RUN go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/mockmodel ./cmd/mockmodel

FROM gcr.io/distroless/static-debian13:nonroot@sha256:8ce6dd87e9823c5b48dcfb2c8604714ccb351ce59607327bcc4957448da279b5
COPY --from=build /out/mockmodel /mockmodel
# Numeric non-root user so a runtime can verify it without reading /etc/passwd.
USER 65532:65532
EXPOSE 8081
# The default listen address is loopback inside the container, which is not
# reachable from outside it. To serve, say so explicitly, for example
#   docker run --rm -p 127.0.0.1:18081:8081 agentplane/mockmodel:p0 --listen 0.0.0.0:8081
ENTRYPOINT ["/mockmodel"]
