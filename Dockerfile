# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Olivares AI agent runtime: Debian 13, libc, shell, git, gh, ssh, Node LTS, Python and uvx.
# The engine remains a reproducible static Go binary. No compilers or agent CLIs
# ship in the runtime; managed tools and account homes live in the data volume.
# Runs as uid/gid 65532. Compose adds a read-only rootfs, dropped capabilities
# and no-new-privileges. Child confinement is enforced by the engine.
#
# Build from the repository root; signed releases use Dockerfile.release with
# GoReleaser's prebuilt binary. See deploy/compose/docker-compose.yml for startup.

# ---- web stage: build the React/Vite UI into the Go embed dir -----------------
# Pinned by digest; Dependabot's docker entry keeps the digest current.
FROM node:26-bookworm-slim@sha256:28147155d47dc31abe0a46e0d12b59b2fb3d0a2dcc5dc65fce76a074180b16fe AS web
WORKDIR /src
# Node.js stopped bundling corepack in version 25, so this stage installs a pinned
# corepack and enables its pnpm shims only; corepack then runs the pnpm that
# web/package.json's packageManager names (lint:dockerfile-node-toolchain holds both rules).
RUN npm install -g corepack@0.34.6 && corepack enable pnpm
# Copy only what the web build needs first, for layer caching.
COPY web/package.json web/pnpm-lock.yaml ./web/
RUN cd web && pnpm install --frozen-lockfile
COPY web/ ./web/
COPY docs/editions.md ./docs/editions.md
# Vite creates core/internal/webui/dist from source, including its parent directories.
RUN cd web && pnpm run build

# ---- go stage: compile the single static binary with the UI embedded ----------
# A toolchain bump moves this tag and its digest together; with both present,
# docker pulls the digest.
FROM golang:1.26.10-bookworm@sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af AS build
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
FROM node:24-trixie-slim@sha256:43b9ba1142b8dcf33fa8f922ca1be85714adc444e0586c7cf7726daaeabc1bea
# gh comes from GitHub's own signed apt repository: Debian 13's gh 2.46.0 is built with
# go1.23.4 and carries that toolchain's advisories. apt verifies the repository against the
# keyring (signing key 7F38BBB59D064DBCB3D84D725612B36462313325), whose content is pinned here.
# The repository lists only its newest gh, so the version is a floor (below), not an exact pin.
# npm still bundles a vulnerable undici and ip-address (neither has dependencies of its own);
# replace only those two until npm ships the fixes.
# hadolint ignore=DL3008
RUN apt-get update \
    && apt-get upgrade -y \
    && apt-get install -y --no-install-recommends ca-certificates git python3 python3-venv \
       vim less procps curl jq iproute2 tar gzip openssh-client netcat-openbsd \
    && curl -fsSL -o /usr/share/keyrings/githubcli-archive-keyring.gpg \
       https://cli.github.com/packages/githubcli-archive-keyring.gpg \
    && { test "$(sha256sum /usr/share/keyrings/githubcli-archive-keyring.gpg)" = \
         "6084d5d7bd8e288441e0e94fc6275570895da18e6751f70f057485dc2d1a811b  /usr/share/keyrings/githubcli-archive-keyring.gpg" \
         || { echo "the GitHub CLI keyring changed: check its fingerprints, then update its sha256" >&2; exit 1; }; } \
    && printf '%s\n' 'Types: deb' 'URIs: https://cli.github.com/packages' 'Suites: stable' 'Components: main' \
       'Signed-By: /usr/share/keyrings/githubcli-archive-keyring.gpg' > /etc/apt/sources.list.d/github-cli.sources \
    && apt-get update \
    && apt-get install -y --no-install-recommends gh \
    && npm install --global --ignore-scripts npm@11.21.1 \
    && npm install --ignore-scripts --no-save --package-lock=false --prefix /tmp/olivares-npm-fixes \
       undici@6.29.1 ip-address@10.7.5 \
    && for p in undici ip-address; do \
         rm -rf "/usr/local/lib/node_modules/npm/node_modules/$p" \
         && mv "/tmp/olivares-npm-fixes/node_modules/$p" "/usr/local/lib/node_modules/npm/node_modules/$p" \
         || exit 1; \
       done \
    && rm -rf /tmp/olivares-npm-fixes \
    && npm cache clean --force \
    && rm -rf /var/lib/apt/lists/* /usr/local/include/node \
    && groupadd --gid 65532 nonroot \
    && useradd --uid 65532 --gid 65532 --home-dir /var/lib/olivares/home --no-create-home nonroot \
    && install -d -o 65532 -g 65532 -m 0700 /var/lib/olivares /var/lib/olivares/home
# The apt layer above must carry the Debian security updates of its build day. A cached layer
# from before DSA-6572-1 (2026-10-20, ca-certificates, trixie 20250419+deb13u1) fails here
# instead of shipping; raise the floor with the next ca-certificates DSA.
RUN v="$(dpkg-query -W -f='${Version}' ca-certificates)"; \
    if ! dpkg --compare-versions "$v" ge 20250419+deb13u1; then \
      echo "ca-certificates $v is older than 20250419+deb13u1 (DSA-6572-1): rebuild without a cached apt layer" >&2; exit 1; \
    fi; \
    v="$(dpkg-query -W -f='${Version}' gh)"; \
    if ! dpkg --compare-versions "$v" ge 2.103.3; then \
      echo "gh $v is older than 2.103.3, the first from GitHub's repository: rebuild without a cached apt layer" >&2; exit 1; \
    fi
COPY --from=ghcr.io/astral-sh/uv:0.13.5@sha256:32a7bb39b496babbd997b0a39a4d66c126bdaf5661b302bf6f06abda8deca6ff /uv /uvx /usr/local/bin/
COPY packaging/container/uv-LICENSE-MIT.txt /usr/share/doc/uv/LICENSE-MIT
# A confined session reaches an SSH host only through the session proxy's CONNECT tunnel
# (HTTPS_PROXY names the proxy); without it (an unconfined session) ssh connects directly.
# This is the main file, not a drop-in: in the session's user namespace root's files belong
# to nobody, and ssh refuses an included file it does not own.
RUN printf '%s\n' \
      'Match exec "printenv HTTPS_PROXY | grep -q ."' \
      '  ProxyCommand nc -X connect -x "$(echo "$HTTPS_PROXY" | cut -d/ -f3)" %h %p' \
      >> /etc/ssh/ssh_config
COPY packaging/container/bashrc.sh /etc/olivares-bashrc
RUN sed -i '1i . /etc/olivares-bashrc' /etc/bash.bashrc
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
