#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# goreleaser-build-wrapper.sh — the `tool:` GoReleaser invokes instead of `go`
# for the olivares builds (.goreleaser.yaml). Before each target's `go build` it
# refreshes cmd/olivares/firstparty/bins with the first-party connector plugins
# compiled FOR THAT TARGET (GOOS/GOARCH are in the env GoReleaser sets), then
# execs the real `go` with the original arguments (E1: releases previously
# ran no connector build at all, so every published artifact embedded ZERO
# plugins and `serve` warned "connector not embedded in this build").
#
# The flock serializes [connector build + go build] per target: GoReleaser builds
# targets IN PARALLEL and the go:embed dir is shared mutable state — without the
# lock, target A's engine build could embed target B's plugin binaries. The lock
# fd survives the exec, so it is held until `go build` itself exits.
# (flock(1) is util-linux; release builds run on Linux — CI runner, dev
# container, Docker build stage. Darwin artifacts are cross-built FROM Linux.)

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"

# Anything that is not a target build (e.g. `go env`, or a build with no
# cross-target env) passes straight through to the toolchain.
if [ "${1:-}" != "build" ] || [ -z "${GOOS:-}" ] || [ -z "${GOARCH:-}" ]; then
  exec go "$@"
fi

exec 9>"${repo_root}/.connector-embed.lock"
flock 9
bash "${repo_root}/scripts/build-connectors.sh" "${GOOS}" "${GOARCH}"
exec go "$@"
