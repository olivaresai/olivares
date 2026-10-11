# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Olivares AI agent runtime: Debian 13, libc, shell, git, Node LTS, Python and uvx.
# The engine remains a reproducible static Go binary. No compilers or agent CLIs
# ship in the runtime; managed tools and account homes live in the data volume.
# Runs as uid/gid 65532. Compose adds a read-only rootfs, dropped capabilities
# and no-new-privileges. Child confinement is enforced by the engine.
#
# Build from the repository root; signed releases use Dockerfile.release with
# GoReleaser's prebuilt binary. See deploy/compose/docker-compose.yml for startup.

# ---- web stage: build the React/Vite UI into the Go embed dir -----------------
# Pin the digest in production; the tag is a readable default (matches the project
# convention, see connectors/ebpf/deploy/*.yaml).
FROM node:26-bookworm-slim AS web
WORKDIR /src
# Node.js stopped bundling corepack in version 25, so this stage installs a pinned
# corepack and enables its pnpm shims only; corepack then runs the pnpm that
# web/package.json's packageManager names (lint:dockerfile-node-toolchain holds both rules).
RUN npm install -g corepack@0.34.6 && corepack enable pnpm
# Copy only what the web build needs first, for layer caching.
COPY web/package.json web/pnpm-lock.yaml ./web/
RUN cd web && pnpm install --frozen-lockfile
COPY web/ ./web/
# Vite creates core/internal/webui/dist from source, including its parent directories.
RUN cd web && pnpm run build

# ---- go stage: compile the single static binary with the UI embedded ----------
FROM golang:1.27.0-bookworm AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-mod=readonly
# The full source comes in BEFORE `go mod download`. The go command reads go.work
# first and fails when a module it lists has no go.mod in this stage, so the stage
# loads the go.work the context really carries (the curated export ships a trimmed
# one) rather than a separate hand-kept list of module manifests.
# Accepted cache consequence: a change anywhere in the context also invalidates the
# module-download layer, not only the layers after it.
COPY . .
RUN go mod download
# Then the freshly built web bundle from the web stage, over the copied placeholder.
COPY --from=web /src/core/internal/webui/dist/ ./core/internal/webui/dist/
ARG VERSION
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
RUN VERSION_LDFLAGS="$(sh scripts/build-ldflags.sh "${VERSION}" "${COMMIT}")" && \
    go build -trimpath \
      -ldflags "-s -w -buildid= ${VERSION_LDFLAGS}" \
      -o /out/olivares ./cmd/olivares

# ---- final stage: non-root agent runtime ----------------------------------
# Runtime support only; agent CLIs are installed by Olivares into the data volume.
# Node 24 is LTS; this official image uses Debian 13 (trixie) and includes npm/npx.
FROM node:25-trixie-slim@sha256:aabbe39553d15ede8a97cc60c9e1a97034ff772afcf696ea42b94e7f5f2ec71b
# hadolint ignore=DL3008
RUN apt-get update \
    && apt-get upgrade -y \
    && apt-get install -y --no-install-recommends ca-certificates git python3 python3-venv \
    && npm install --global --ignore-scripts npm@11.21.0 \
    && npm cache clean --force \
    && rm -rf /var/lib/apt/lists/* /usr/local/include/node \
    && groupadd --gid 65532 nonroot \
    && useradd --uid 65532 --gid 65532 --home-dir /var/lib/olivares/home --no-create-home nonroot \
    && install -d -o 65532 -g 65532 -m 0700 /var/lib/olivares /var/lib/olivares/home
COPY --from=ghcr.io/astral-sh/uv:0.12.21@sha256:a7aed3216253ee804de3e2d8afa5073baa1a177335345d43845cd4165e43b711 /uv /uvx /usr/local/bin/
COPY packaging/container/uv-LICENSE-MIT.txt /usr/share/doc/uv/LICENSE-MIT
# A writable home is required for npm/npx and uvx on Compose's read-only rootfs.
# Per-account homes are set by the product when launching subscription tools.
ENV HOME=/var/lib/olivares/home
# The directory the CMD serves, so a bare `olivares first-boot` (or any data-dir default) finds it.
ENV OLIVARES_DATA_DIR=/var/lib/olivares
WORKDIR /var/lib/olivares
# OCI labels for provenance (cosign/SBOM tooling and registries read these).
LABEL org.opencontainers.image.title="olivares" \
      org.opencontainers.image.description="Olivares AI — self-hosted control plane for AI agents" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.source="https://github.com/olivaresai/olivares" \
      org.opencontainers.image.vendor="Olivares.AI"
COPY --from=build /out/olivares /usr/local/bin/olivares
# Seed the named-volume mountpoint with the runtime uid and a private mode. On a
# fresh Docker volume the engine otherwise receives a root-owned directory and
# the non-root process cannot create its SQLite/TLS/audit state.
COPY --chown=65532:65532 --chmod=0700 packaging/container/data-dir/ /var/lib/olivares/
# The shipped backup service (deploy/compose/docker-compose.backup.yml) runs as this
# image's user and writes bundles to the `olivares-backups` named volume at /backups.
# Seed that mountpoint the same way, or a fresh volume is root-owned and unwritable.
COPY --chown=65532:65532 --chmod=0700 packaging/container/data-dir/ /backups/
# The licence travels INSIDE the image. Until 2026-08-04 it did not: the OCI images
# carried an org.opencontainers.image.licenses LABEL and no licence TEXT, so the main
# distribution path handed an operator an AGPL binary with neither the grant that lets
# them run it nor the warranty disclaimer that protects everyone who wrote it. A label
# is metadata; AGPL sections 4 and 5 ask for the document.
COPY LICENSE NOTICE LICENSING.md DISCLAIMER.md /usr/share/doc/olivares/
COPY LICENSES /usr/share/doc/olivares/LICENSES
# The seeded volume and engine use the same non-root uid/gid.
USER 65532:65532
# EXPOSE documents the ports; serve owns the dual-stack listener defaults.
EXPOSE 8443 8444
ENTRYPOINT ["/usr/local/bin/olivares"]
CMD ["serve", "--data-dir", "/var/lib/olivares"]
