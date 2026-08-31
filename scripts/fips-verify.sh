#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# FIPS 140-3 build + runtime self-check (gap SCP-09). Proves four things, honestly:
#   1. The control plane COMPILES with GOFIPS140=v1.0.0 (CMVP cert #5247) and
#      CGO_ENABLED=0 — i.e. native FIPS 140-3 mode keeps the pure-Go static build.
#   2. The resulting binary actually LINKS the validated crypto/internal/fips140
#      v1.0.0 module (a stray GOFIPS140=off would otherwise pass #1 silently).
#   3. The runtime toggle works: under GODEBUG=fips140=on a process reports
#      crypto/fips140.Enabled()==true and Version()=="v1.0.0".
#   4. The CONTROLPLANE BINARY ITSELF self-reports that state via `version`
# — the line Dockerfile.fips tells a buyer to check.
#
# It also confirms the build does NOT mutate go.work / go.work.sum (the FIPS modules
# ship inside the Go toolchain at $GOROOT/lib/fips140 — no module graph change).
#
# This does NOT claim a CMVP validation or any ATO — it verifies the BUILD selects a
# module that IS validated, and that the toggle is wired. See docs/SCP-09-FIPS-STIG.md.
#
# Usage (from the repo root):
#   scripts/fips-verify.sh                 # build to a temp dir under the repo
#   FIPS_OUTDIR=/path scripts/fips-verify.sh
set -euo pipefail

# The ambient git environment OUTRANKS `-C`: with GIT_DIR exported — which git does
# from every LINKED worktree, i.e. from every parallel session — this script's throwaway
# repositories would be driven into the LIVE repository instead. Measured 2026-08-06;
# it left the branch of PR #526 pointing at a fixture commit. Fail closed: a missing
# sanitiser is "I could not isolate", never "isolation was not needed".
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

cd "$(git rev-parse --show-toplevel 2>/dev/null || dirname "$(dirname "$(readlink -f "$0")")")"

command -v go >/dev/null 2>&1 || { echo "error: go not found" >&2; exit 1; }

OUTDIR="${FIPS_OUTDIR:-$(mktemp -d "${TMPDIR:-/tmp}/fips-verify.XXXXXX")}"
mkdir -p "$OUTDIR"
cleanup() { [ -n "${FIPS_OUTDIR:-}" ] || rm -rf "$OUTDIR"; }
trap cleanup EXIT

# ${TMPDIR:-/tmp} may be mounted noexec (the dev container's /tmp is), and this
# script EXECUTES what it builds ("$OUTDIR/olivares-fips" version below). Probe;
# with an explicit FIPS_OUTDIR refuse loudly rather than silently relocating the
# operator's choice, otherwise fall back to a repo-local (exec) tempdir.
printf '#!/bin/sh\nexit 0\n' >"$OUTDIR/.execprobe" && chmod +x "$OUTDIR/.execprobe"
if ! "$OUTDIR/.execprobe" >/dev/null 2>&1; then
  rm -f "$OUTDIR/.execprobe"
  if [ -n "${FIPS_OUTDIR:-}" ]; then
    echo "error: FIPS_OUTDIR=$FIPS_OUTDIR cannot execute binaries (noexec mount?) and this script must RUN what it builds" >&2
    exit 1
  fi
  rm -rf "$OUTDIR"
  OUTDIR="$(mktemp -d "$PWD/.tmpexec.XXXXXX")" || exit 1
fi
rm -f "$OUTDIR/.execprobe"

# A `-tags release` build refuses to compile until the first-party connector
# plugins exist in the embed dir (the bins/claude-source fail-closed guard in
# cmd/olivares/firstparty/embed_binaries_release.go), so build them first —
# goreleaser does the same per target via scripts/goreleaser-build-wrapper.sh.
# In a fresh checkout (CI) the embed dir holds only PLACEHOLDER.
echo "==> prereq: building first-party connector plugins (scripts/build-connectors.sh)"
bash scripts/build-connectors.sh

echo "==> 0/5 recording go.work / go.work.sum checksums (must be unchanged after build)"
before="$(sha256sum go.work go.work.sum)"

echo "==> 1/5 building with GOFIPS140=v1.0.0 CGO_ENABLED=0 -tags release (FIPS mode + both trust anchors, exactly as goreleaser ships the olivares-fips artifact)"
# The FIPS RELEASE artifact is built `-tags release` (drops the dev seed) with the
# two public anchors injected. Verify that EXACT shape here: mint two throwaway
# Ed25519 keypairs, inject their distinct public halves, and assert both fingerprints.
LICENSE_PUBKEY="$(go run ./cmd/olivares license keygen 2>/dev/null | awk '/public_key/{print $2}')"
OTA_PUBKEY="$(go run ./cmd/olivares license keygen 2>/dev/null | awk '/public_key/{print $2}')"
LICENSE_FP="$(printf '%s' "$LICENSE_PUBKEY" | base64 -d | sha256sum | cut -c1-8)"
OTA_FP="$(printf '%s' "$OTA_PUBKEY" | base64 -d | sha256sum | cut -c1-8)"
GOFIPS140=v1.0.0 CGO_ENABLED=0 go build -tags release \
  -ldflags "-X github.com/olivaresai/olivares/core/license.releasePublicKeyB64=$LICENSE_PUBKEY -X github.com/olivaresai/olivares/core/release.artifactVerifyKeyB64=$OTA_PUBKEY" \
  -o "$OUTDIR/olivares-fips" ./cmd/olivares
echo "    built: $OUTDIR/olivares-fips (license $LICENSE_FP; OTA $OTA_FP)"

echo "==> 2/5 confirming the validated v1.0.0 FIPS module is linked into the binary"
# Write the symbol table fully before grepping so closing the pipe early can't make
# `go tool nm` print a spurious "broken pipe" to stderr.
nm_out="$OUTDIR/olivares-fips.nm"
go tool nm "$OUTDIR/olivares-fips" > "$nm_out" 2>/dev/null || true
if grep -q 'crypto/internal/fips140/v1.0.0' "$nm_out"; then
  echo "    OK: crypto/internal/fips140/v1.0.0 symbols present (CMVP cert #5247 module)"
else
  echo "    FATAL: FIPS 140-3 v1.0.0 module NOT linked — GOFIPS140 was not honoured" >&2
  exit 1
fi

echo "==> 3/5 confirming go.work / go.work.sum are byte-for-byte unchanged"
after="$(sha256sum go.work go.work.sum)"
if [ "$before" = "$after" ]; then
  echo "    OK: workspace module graph untouched"
else
  echo "    FATAL: the FIPS build mutated go.work/go.work.sum" >&2
  diff <(printf '%s\n' "$before") <(printf '%s\n' "$after") >&2 || true
  exit 1
fi

echo "==> 4/5 demonstrating the runtime toggle (GODEBUG=fips140=on)"
demo="$OUTDIR/fipsdemo"
mkdir -p "$demo"
cat > "$demo/main.go" <<'GO'
package main

import (
	"crypto/fips140"
	"fmt"
	"os"
)

func main() {
	fmt.Printf("fips140.Enabled()=%v version=%q\n", fips140.Enabled(), fips140.Version())
	if !fips140.Enabled() {
		os.Exit(1)
	}
}
GO
# GOWORK=off: the demo is a throwaway module; when FIPS_OUTDIR lands inside the
# repo tree the workspace would otherwise reject it as a non-member module.
( cd "$demo" && GOWORK=off go mod init fipsdemo >/dev/null 2>&1 || true
  GOWORK=off GOFIPS140=v1.0.0 CGO_ENABLED=0 go build -o fipsdemo . )
out="$(GODEBUG=fips140=on "$demo/fipsdemo")"
echo "    GODEBUG=fips140=on -> $out"
case "$out" in
  *"Enabled()=true"*'version="v1.0.0"'*) echo "    OK: runtime FIPS mode enabled, module v1.0.0" ;;
  *) echo "    FATAL: runtime did not report FIPS enabled v1.0.0" >&2; exit 1 ;;
esac

echo "==> 5/5 confirming the olivares binary self-reports its FIPS mode AND the embedded release key (version)"
vout="$(GODEBUG=fips140=on "$OUTDIR/olivares-fips" version)"
echo "    version -> $vout"
case "$vout" in
  *"fips140=on module=v1.0.0"*) echo "    OK: the binary self-reports fips140=on module=v1.0.0" ;;
  *) echo "    FATAL: 'olivares version' did not report fips140=on module=v1.0.0" >&2; exit 1 ;;
esac
# The -tags release build must carry both distinct injected anchors.
case "$vout" in
  *"license-key=release/$LICENSE_FP"*) echo "    OK: embedded license anchor $LICENSE_FP" ;;
  *) echo "    FATAL: FIPS release binary did not report license-key=release/$LICENSE_FP" >&2; exit 1 ;;
esac
case "$vout" in
  *"ota-key=release/$OTA_FP"*) echo "    OK: embedded OTA anchor $OTA_FP" ;;
  *) echo "    FATAL: FIPS release binary did not report ota-key=release/$OTA_FP" >&2; exit 1 ;;
esac

echo
echo "✅ FIPS 140-3 verification passed: builds with GOFIPS140=v1.0.0 -tags release,"
echo "   links the CMVP-validated module #5247, leaves go.work untouched, the runtime"
echo "   toggle reports enabled, and the binary self-reports fips140=on plus distinct"
echo "   embedded license and OTA verification anchors via 'version'."
echo "   (No CMVP/FedRAMP/ATO certification is claimed.)"
