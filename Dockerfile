# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Olivares AI control plane — hardened, distroless, single static binary.
#
# Security posture (docs/SECURITY-HARDENING.md, §2):
#   - Final image is gcr.io/distroless/static — NO shell, NO package manager, NO
#     libc beyond the static binary's own. Whole classes of "exec into the
#     container" and "pull a tool at runtime" attacks are simply not possible.
#   - Runs as NON-ROOT (uid 65532, the distroless `nonroot` user).
#   - CGO_ENABLED=0 → a fully static, memory-safe Go binary (modernc.org/sqlite is
#     pure Go, so no libc is required even for the embedded single-node store).
#   - Reproducible-leaning build: -trimpath, pinned toolchain (go.work toolchain
#     go1.26.4), build id stripped, and the build date derived from
#     SOURCE_DATE_EPOCH so the same commit yields a byte-identical binary
#     (digest-stable verifiable — see docs/SECURITY-HARDENING.md §"Reproducible builds").
#
# Build (from the repo root, so the go.work workspace + web/ are in context):
#   docker build \
#     --build-arg VERSION="$(git describe --tags --always --dirty)" \
#     --build-arg COMMIT="$(git rev-parse --short HEAD)" \
#     --build-arg SOURCE_DATE_EPOCH="$(git log -1 --pretty=%ct)" \
#     -t olivares:dev .
#
# Run (bind to all interfaces INSIDE the container; the host controls exposure):
#   docker run --rm -p 8443:8443 -p 8444:8444 -v olivares-data:/data \
#     olivares:dev serve --listen 0.0.0.0:8443 --grpc-listen 0.0.0.0:8444 --data-dir /data
#
# This Dockerfile is also driven by .goreleaser.yaml for signed releases (the
# binary is prebuilt by goreleaser and only COPYed in that path).

# ---- web stage: build the React/Vite UI into the Go embed dir -----------------
# Pin the digest in production; the tag is a readable default (matches the project
# convention, see connectors/ebpf/deploy/*.yaml).
FROM node:26-bookworm-slim AS web
WORKDIR /src
# Enable corepack/pnpm without a network round-trip beyond the registry.
RUN corepack enable
# Copy only what the web build needs first, for layer caching.
COPY web/package.json web/pnpm-lock.yaml ./web/
RUN cd web && pnpm install --frozen-lockfile
COPY web/ ./web/
# Vite's build.outDir is core/internal/webui/dist — it must exist before tsc/vite
# run so the Go embed has files. The build writes there.
COPY core/internal/webui/dist/index.html ./core/internal/webui/dist/index.html
RUN cd web && pnpm run build

# ---- go stage: compile the single static binary with the UI embedded ----------
FROM golang:1.26.6-bookworm AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-mod=readonly
# Download modules first (cached) — copy the workspace manifests for every module.
COPY go.work go.work.sum ./
COPY core/go.mod core/go.sum ./core/
COPY cmd/olivares/go.mod cmd/olivares/go.sum ./cmd/olivares/
COPY connectors/go.mod connectors/go.sum ./connectors/
COPY modules/go.mod modules/go.sum ./modules/
COPY sdk/go.mod sdk/go.sum ./sdk/
COPY sdk/plugin/go.mod sdk/plugin/go.sum ./sdk/plugin/
COPY terraform-provider-olivares/go.mod terraform-provider-olivares/go.sum ./terraform-provider-olivares/
RUN go mod download
# Bring in the full source, then the freshly built web bundle from the web stage.
COPY . .
COPY --from=web /src/core/internal/webui/dist/ ./core/internal/webui/dist/
ARG VERSION=dev
ARG COMMIT=none
# SOURCE_DATE_EPOCH (seconds since epoch) makes the embedded build date — and thus
# the binary — reproducible for a given commit. Defaults to 0 (1970) when unset.
ARG SOURCE_DATE_EPOCH=0
# First-party connector plugins into the go:embed dir BEFORE the engine build —
# same canonical script as `task build:connectors` and the goreleaser wrapper
# (E1: skipping this shipped images whose serve warned "connector not
# embedded in this build" for every plugin source, the claude source included).
# Host platform == image platform here (no cross-build in this Dockerfile).
RUN bash scripts/build-connectors.sh
RUN BUILD_DATE="$(date -u -d "@${SOURCE_DATE_EPOCH}" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo unknown)" && \
    go build -trimpath \
      -ldflags "-s -w -buildid= -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${BUILD_DATE}" \
      -o /out/olivares ./cmd/olivares

# ---- final stage: distroless, non-root, static -------------------------------
# Runtime base PINNED BY DIGEST (SCP-11) — kept in sync with Dockerfile.release and
# bumped by the scheduled patch-velocity workflow. `crane digest …:nonroot` resolves it.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
# OCI labels for provenance (cosign/SBOM tooling and registries read these).
LABEL org.opencontainers.image.title="olivares" \
      org.opencontainers.image.description="Olivares AI — self-hosted control plane for AI agents" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.source="https://github.com/olivaresai/olivares" \
      org.opencontainers.image.vendor="Olivares.AI"
COPY --from=build /out/olivares /usr/local/bin/olivares
# The licence travels INSIDE the image. Until 2026-08-04 it did not: the OCI images
# carried an org.opencontainers.image.licenses LABEL and no licence TEXT, so the main
# distribution path handed an operator an AGPL binary with neither the grant that lets
# them run it nor the warranty disclaimer that protects everyone who wrote it. A label
# is metadata; AGPL sections 4 and 5 ask for the document.
COPY LICENSE NOTICE LICENSING.md DISCLAIMER.md /usr/share/doc/olivares/
COPY LICENSES /usr/share/doc/olivares/LICENSES
# distroless `nonroot` is uid/gid 65532. The data dir is provided by a volume the
# operator chowns to 65532; we never run as root.
USER 65532:65532
# Documented surface only — the engine binds 127.0.0.1 by DEFAULT (docs/SECURITY-HARDENING.md); a
# container operator must opt into 0.0.0.0 explicitly. EXPOSE is a hint, not a bind.
EXPOSE 8443 8444
ENTRYPOINT ["/usr/local/bin/olivares"]
CMD ["serve", "--listen", "0.0.0.0:8443", "--grpc-listen", "0.0.0.0:8444", "--data-dir", "/data"]
