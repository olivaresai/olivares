#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# release-smoke.sh — assert, ON THE FINAL ARTIFACT, the three release-truth
# properties that the pipeline config alone cannot prove:
#
#   1. TRACEABLE: `version` reports a stamped version/commit/build date (never
#      "dev (commit none, built unknown)") and `--version` is wired.
#   2. PLUGINS EMBEDDED: the first-party connector plugins are inside the binary
#      and extract through the REAL firstparty.Extract path (`firstparty-bins
#      --require …`). This is the regression that shipped every release with an
#      EMPTY plugin set while dev builds worked.
#   3. TREE PARITY: the artifact's full cobra command tree is identical to
#      newRootCmd() at this commit (`commands` diff vs a fresh source build), so
#      a stale or divergent packaged binary cannot reach a tag.
#
# Usage:
#   scripts/release-smoke.sh [path-to-binary]
#
# With no argument it looks for the host-platform binary GoReleaser left in
# dist/ (run `task release:snapshot` first). Runs only the host-platform binary:
# cross-platform artifacts are byte-checked by checksums/SBOM, not executed here.

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "${repo_root}"

# Representative embed set asserted on the artifact: the flagship claude source
# plus one from each out-of-process family (OTel receiver, broker, L7 mesh).
# scripts/build-connectors.sh remains the canonical full list.
REQUIRED_PLUGINS="claude-source,cowork-source,kafka-source,envoy-source"

bin="${1:-}"
if [ -z "${bin}" ]; then
  goos="$(go env GOOS)"
  goarch="$(go env GOARCH)"
  # GoReleaser dist layout: dist/<build-id>_<goos>_<goarch>[_<goamd64|goarm64>]/olivares
  bin="$(find dist -type f -name olivares -path "*_${goos}_${goarch}*" 2>/dev/null | head -1 || true)"
  if [ -z "${bin}" ]; then
    echo "release-smoke: COULD NOT LOOK — no ${goos}/${goarch} binary under dist/ — run 'task release:snapshot' first, or pass a binary path" >&2
    exit 2
  fi
fi

# THREE ANSWERS, and the middle one was missing. Every path below used to exit 1, so
# "there is no artifact to look at" and "the artifact is wrong" were the same code. Not
# being able to look is not a finding: reported as one it fails a release for a reason
# that is not about the release. 0 clean · 1 finding · 2 could not look.
fail() {
  echo "release-smoke: FAIL — $*" >&2
  exit 1
}
nopuedo() {
  echo "release-smoke: COULD NOT LOOK — $*" >&2
  exit 2
}

[ -e "${bin}" ] || nopuedo "the path does not exist: ${bin}"
[ -f "${bin}" ] || nopuedo "not a regular file: ${bin}"
[ -x "${bin}" ] || chmod +x "${bin}" 2>/dev/null || nopuedo "cannot make it executable (read-only or noexec mount?): ${bin}"
echo "release-smoke: artifact under test: ${bin}"

# Runs the artifact and separates "it would not run at all" (126/127: wrong arch, noexec
# mount, missing loader) from "it ran and answered wrong". The first is the box, not the
# build; calling it a finding is how a mount option gets to veto a release.
corre() {
  local _o _r
  set +e; _o="$("$@" 2>&1)"; _r=$?; set -e
  SALIDA="$_o"; RC=$_r
  case "$_r" in 126|127)
    nopuedo "the artifact will not execute on this box (rc=$_r): $_o" ;;
  esac
  return 0
}

# --- 1. traceable build metadata ---------------------------------------------
corre "${bin}" version
[ "$RC" = 0 ] || fail "\`version\` exited rc=$RC: ${SALIDA}"
version_out="$SALIDA"
echo "  version: ${version_out}"
case "${version_out}" in
*"olivares dev (commit none, built unknown"*) fail "binary is UNSTAMPED: ${version_out}" ;;
esac
# Both legs assert the SHAPE of a real stamp, not the absence of one literal string.
# The three defaults in cmd/olivares/main.go:36-38 (dev / none / unknown) are what an
# ldflags-less build prints, and rejecting those three by name only covers the case where
# the linker flags did not run AT ALL. The reachable case they miss is an ldflags run whose
# TEMPLATE resolved to nothing: `-X main.date=` stamps the empty string, the binary prints
# `built )`, and a check that greps for "built unknown" calls that traceable. It is not
# hypothetical — on 2026-08-30 a snapshot over the bare export printed
# `0.0.0-SNAPSHOT-none (commit none, built 0001-01-01)`; the smoke cut on the COMMIT leg, so
# the zero date rode along unmeasured. A build that stamps a real commit from
# release-commit.txt (the runbook's own procedure over an export with no .git) and a zero
# date is the same run with one leg's luck removed.
built_at="${version_out##*built }"; built_at="${built_at%%)*}"
commit_id="${version_out##*commit }"; commit_id="${commit_id%%,*}"
case "${commit_id}" in
none | "") fail "commit not stamped: ${version_out}" ;;
[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]*) : ;;
*) fail "commit is not a hex sha: ${version_out}" ;;
esac
case "${built_at}" in
unknown | "") fail "build date not stamped: ${version_out}" ;;
0001-*) fail "build date is Go's zero time, not a stamp: ${version_out}" ;;
[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]*) : ;;
*) fail "build date is not a date: ${version_out}" ;;
esac
# cobra --version must exist and agree (root.Version wiring).
corre "${bin}" --version
[ "$RC" = 0 ] || fail "\`--version\` exited rc=$RC: ${SALIDA}"
flag_out="$SALIDA"
case "${flag_out}" in *"olivares version"*) : ;; *) fail "--version flag not wired: ${flag_out}" ;; esac
echo "  OK: version/commit/date stamped; --version wired"

# --- 2. first-party connector plugins embedded + extractable ------------------
"${bin}" firstparty-bins --require "${REQUIRED_PLUGINS}" \
  || fail "required first-party connector plugins are not embedded (releases must never ship an empty bins/)"
echo "  OK: required plugins embedded and extract-verified (${REQUIRED_PLUGINS})"

# --- 3. command-tree parity vs the source at this commit ----------------------
# Reference built from THIS checkout with the same public build tags a release
# uses (-tags release compiles without key injection; keys change trust anchors,
# never the command tree). Repo-local scratch: /tmp is tmpfs+noexec in the dev
# container (same reason as .examples-tmp/).
scratch="${repo_root}/.release-smoke-tmp"
mkdir -p "${scratch}"
trap 'rm -rf "${scratch}"' EXIT
# ⛔ Si la REFERENCIA no se puede construir, no hay con que comparar. Antes, `set -e`
# mataba aqui con el rc de `go build` y la corrida se leia como «el arbol diverge»: un
# hallazgo inventado a partir de una caja sin toolchain o sin red.
set +e
CGO_ENABLED=0 go build -trimpath -tags release -o "${scratch}/olivares-ref" ./cmd/olivares 2>"${scratch}/build.err"
_rcbuild=$?
set -e
[ "$_rcbuild" = 0 ] || nopuedo "cannot build the reference to compare the command tree against (rc=$_rcbuild): $(tail -3 "${scratch}/build.err" 2>/dev/null | tr '\n' ' ')"
corre "${bin}" commands
[ "$RC" = 0 ] || fail "\`commands\` exited rc=$RC on the artifact: ${SALIDA}"
printf '%s\n' "$SALIDA" >"${scratch}/tree-artifact.txt"
corre "${scratch}/olivares-ref" commands
[ "$RC" = 0 ] || nopuedo "the reference binary does not answer \`commands\` (rc=$RC): ${SALIDA}"
printf '%s\n' "$SALIDA" >"${scratch}/tree-source.txt"
if ! diff -u "${scratch}/tree-source.txt" "${scratch}/tree-artifact.txt"; then
  fail "the artifact's command tree diverges from newRootCmd() at this commit (stale binary?)"
fi
echo "  OK: command tree identical to source ($(wc -l <"${scratch}/tree-artifact.txt" | tr -d ' ') commands)"

echo "release-smoke: PASS"
