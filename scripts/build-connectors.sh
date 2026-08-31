#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# build-connectors.sh — compile the first-party SOURCE-connector plugin binaries
# that are go:embed'ded into the single olivares artifact (CB-1 transport B,
# cmd/olivares/firstparty). THE canonical list lives here; `task
# build:connectors`, the Dockerfile build stage and GoReleaser (via
# scripts/goreleaser-build-wrapper.sh, per release target) all call this script,
# so a dev build, a docker build and a released artifact can never disagree
# about which plugins ship (E1 — releases used to embed NONE).
#
# Each plugin is a standalone go-plugin binary so its dependency tree (franz-go,
# go-amqp, go-control-plane, the OTel proto stack, …) never links into the
# engine; the engine extracts and runs it as an isolated subprocess.
#
# Usage:
#   scripts/build-connectors.sh                 # host platform (dev builds)
#   scripts/build-connectors.sh <GOOS> <GOARCH> # cross-build for a release target
#
# The embed dir is CLEANED first (everything but the committed PLACEHOLDER):
# leftovers from a previous target would otherwise be silently embedded into a
# binary for a DIFFERENT platform — worse than missing, they extract and then
# fail to exec.

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "${repo_root}"

target_goos="${1:-$(go env GOOS)}"
target_goarch="${2:-$(go env GOARCH)}"

bins_dir="cmd/olivares/firstparty/bins"
mkdir -p "${bins_dir}"
find "${bins_dir}" -type f ! -name PLACEHOLDER -delete

# CGO_ENABLED=0 + -trimpath match the engine's static reproducible-leaning build;
# -s -w -buildid= keeps the (14x) embedded payload small and content-deterministic.
build() {
  CGO_ENABLED=0 GOOS="${target_goos}" GOARCH="${target_goarch}" \
    go build -trimpath -ldflags "-s -w -buildid=" -o "${bins_dir}/$1" "$2"
}

# The Claude Code/Claude estate observer — the flagship source.
build claude-source ./connectors/claude/cmd/claude-source
# The Claude Cowork OTLP/HTTP logs receiver, out-of-process (transport B) so its
# OpenTelemetry-proto dependency tree never links into the core. Its sibling
# cowork-analytics source is modelprovider-only and runs in-process.
build cowork-source ./connectors/cowork/cmd/cowork-source
# Messaging/eventing broker observers. Each carries its wire-protocol deps
# (franz-go, go-amqp, hand-rolled stdlib clients) out of the core SBOM. Sources
# are wired by kind in cmd/olivares/sources.go; the egress outputs ship for the
# plugin-output path.
build kafka-source ./connectors/kafka/cmd/kafka-source
build kafka-output ./connectors/kafka/cmd/kafka-output
build amqp-source ./connectors/amqp/cmd/amqp-source
build amqp-output ./connectors/amqp/cmd/amqp-output
build nats-source ./connectors/nats/cmd/nats-source
build mqtt-source ./connectors/mqtt/cmd/mqtt-source
build cloudqueue-source ./connectors/cloudqueue/cmd/cloudqueue-source
build cloudqueue-output ./connectors/cloudqueue/cmd/cloudqueue-output
build debezium-source ./connectors/debezium/cmd/debezium-source
# Network/mesh L7 observers: `envoy` hosts ALS/ext_authz/ext_proc, `hubble`
# is the Cilium Hubble Relay client — go-control-plane and the Cilium API stay
# out of the pure-Go core.
build envoy-source ./connectors/envoy/cmd/envoy-source
build hubble-source ./connectors/hubble/cmd/hubble-source

count="$(find "${bins_dir}" -type f ! -name PLACEHOLDER | wc -l | tr -d ' ')"
echo "build-connectors: ${count} first-party connector plugin(s) built for ${target_goos}/${target_goarch} -> ${bins_dir}/"
