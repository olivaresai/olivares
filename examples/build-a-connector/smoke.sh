#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# smoke.sh — runs the EXACT commands this example's README documents and asserts
# that the scaffolded connector compiles, its lifecycle test passes, and it is
# license-boundary clean (never imports /core). It is the example's
# reproducibility contract: if the connector SDK scaffold or the boundary
# guarantee breaks, this fails — so "build your first connector" can't go stale.
#
# Surface: sdk/scaffold (the public SDK + the olivares-connector-new generator).
#
# Usage:  examples/build-a-connector/smoke.sh
# Requires: go (the SDK is stdlib-only; the generated source connector builds
#           offline with no third-party dependencies).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

fail() { echo "FAIL: $*" >&2; exit 1; }
note() { echo "==> $*"; }

# `go test` builds and EXECS a test binary in $TMPDIR; the dev container's /tmp is
# tmpfs+noexec, so route scratch to an exec-capable repo-local dir. On a normal
# host /tmp is fine — set OLIVARES_SMOKE_TMPDIR=/tmp to use it.
TMPBASE="${OLIVARES_SMOKE_TMPDIR:-$ROOT/.examples-tmp}"
mkdir -p "$TMPBASE"
WORK="$(mktemp -d "$TMPBASE/connector.XXXXXX")"
# mktemp -d crea 0700. El motor bajo prueba, si corre como root, lanza cada plugin de
# conector bajo un uid DEDICADO no-root, y ese hijo tiene que atravesar TODA la cadena
# hasta su binario. Un solo eslabon en 0700 lo para con EACCES, que se lee como noexec.
chmod 711 "$WORK"
export TMPDIR="$WORK"
GEN="$WORK/widget-audit"
trap 'rm -rf "$WORK"' EXIT

# ---------------------------------------------------------------------------
# Step 1 — scaffold a complete, out-of-tree connector repository.
# ---------------------------------------------------------------------------
# -sdk-path points the generated go.mod at this checkout's sdk/ so it builds
# immediately (there are no public SDK module tags yet). An external author drops
# in the replace directive once, exactly as the generated README explains.
note "scaffold a source connector with olivares-connector-new"
go run "$ROOT/sdk/scaffold/cmd/olivares-connector-new" \
  -dir "$GEN" \
  -name acme.widget-audit \
  -module example.com/acme/widget-audit \
  -kind source \
  -sdk-path "$ROOT/sdk" >"$WORK/gen.log" 2>&1 || { cat "$WORK/gen.log" >&2; fail "scaffold generation failed"; }

for want in go.mod widgetaudit.go widgetaudit_test.go README.md scripts/check-boundary.sh; do
  [ -e "$GEN/$want" ] || fail "scaffold did not emit $want"
done
note "generated: $(cd "$GEN" && find . -type f | sort | tr '\n' ' ')"

# ---------------------------------------------------------------------------
# Step 2 — it compiles and its lifecycle test passes (offline, GOWORK=off so it
# resolves as the standalone module a third party would have, not through our
# workspace).
# ---------------------------------------------------------------------------
note "build + test the generated connector (offline, standalone module)"
( cd "$GEN" && GOWORK=off go build ./... ) || fail "the generated connector did not compile"
echo "    GOWORK=off go test ./...   (en $GEN; sin -timeout: rige el defecto de Go, 10m)"
( cd "$GEN" && GOWORK=off go test ./... ) || fail "the generated connector's lifecycle test failed"

# ---------------------------------------------------------------------------
# Step 3 — it is license-boundary clean: an Apache connector must NEVER reach the
# AGPL engine (/core). The generated repo ships the same check, standalone.
# ---------------------------------------------------------------------------
note "verify the license boundary (no /core in the build graph)"
OUT="$( cd "$GEN" && GOWORK=off bash scripts/check-boundary.sh 2>&1 )" || { echo "$OUT" >&2; fail "boundary check failed"; }
echo "    $OUT"
echo "$OUT" | grep -q "Boundary check OK" || fail "boundary check did not confirm a clean graph"

echo
echo "PASS — scaffolded a connector, compiled it, ran its lifecycle test, and proved"
echo "  it never imports /core — all offline, with zero third-party dependencies."
