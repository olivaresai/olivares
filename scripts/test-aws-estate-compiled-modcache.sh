#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Bounded bootstrap regression for the compiled AWS-estate witnesses.
# Does not run the static HCL mutants. Does not credit a runtime go test.

set -euo pipefail

# Bounded fault-status hook: exit before any prepare or go test so the
# parent can prove it records this status instead of the `if !` negation.
if [ -n "${OLIVARES_AWS_ESTATE_COMPILED_MODCACHE_FORCE_RC:-}" ]; then
	case "$OLIVARES_AWS_ESTATE_COMPILED_MODCACHE_FORCE_RC" in
	7) ;;
	*)
		echo "test-aws-estate-compiled-modcache: only the fixed failure status 7 is supported" >&2
		exit 2
		;;
	esac
	printf 'forced-child-exit=%s\n' "$OLIVARES_AWS_ESTATE_COMPILED_MODCACHE_FORCE_RC"
	exit "$OLIVARES_AWS_ESTATE_COMPILED_MODCACHE_FORCE_RC"
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2; pwd)"
# shellcheck source=lib/aws-estate-compiled-modcache.sh
. "$ROOT/scripts/lib/aws-estate-compiled-modcache.sh" \
	|| { echo "test-aws-estate-compiled-modcache: missing helper" >&2; exit 2; }

_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/aws-estate-modcache-reg.XXXXXX")"
trap 'chmod -R u+w "$TMP" 2>/dev/null || true; rm -rf "$TMP"' EXIT

pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

MOD="$ROOT/cloud/control-plane"
if [ ! -f "$MOD/go.mod" ] || [ ! -f "$MOD/go.sum" ]; then
	echo "test-aws-estate-compiled-modcache: no pinned cloud/control-plane graph" >&2
	exit 2
fi

# (1) GOPROXY=off on an empty owned cache is NOT MEASURED. No go test is run.
EMPTY="$TMP/empty-gomodcache"
mkdir -p "$EMPTY"
ERR="$TMP/refuse.err"
OUT="$TMP/refuse.out"
set +e
GOPROXY=off GOMODCACHE="$EMPTY" \
	aws_estate_prepare_compiled_modcache "$MOD" "$TMP/owned-from-empty" \
	>"$OUT" 2>"$ERR"
prep_rc=$?
set -e
if [ "$prep_rc" != 2 ]; then
	bad "refused prepare: rc=$prep_rc, want 2 ($(tr '\n' ' ' <"$ERR"))"
elif ! command grep -qF 'NOT MEASURED' "$ERR"; then
	bad "refused prepare: rc=2 but did not say NOT MEASURED; got: $(tr '\n' ' ' <"$ERR")"
elif [ -s "$OUT" ]; then
	bad "refused prepare: printed a cache path on failure: $(head -c 200 "$OUT")"
else
	ok "refused prepare: GOPROXY=off on an empty cache is NOT MEASURED (rc=2)"
fi

# The empty inherited cache must not have been filled as a side effect of refusal.
if [ -e "$EMPTY/go.opentelemetry.io" ] || [ -d "$EMPTY/cache/download/go.opentelemetry.io" ]; then
	bad "refused prepare: wrote OpenTelemetry modules into the empty GOMODCACHE"
else
	ok "refused prepare: did not populate the empty GOMODCACHE"
fi

# (2) The compiled-only selftest under that policy exits nonzero with zero
#     credited runtime cases. It must not report the four witnesses as ok.
CHILD_CACHE="$TMP/child-empty-gomodcache"
mkdir -p "$CHILD_CACHE"
CHILD_LOG="$TMP/compiled-only.log"
set +e
OLIVARES_AWS_ESTATE_COMPILED_ONLY=1 \
OLIVARES_AWS_ESTATE_SKIP_REFUSED_PREP_CONTROL=1 \
GOPROXY=off \
GOMODCACHE="$CHILD_CACHE" \
bash "$ROOT/scripts/test-aws-estate.sh" >"$CHILD_LOG" 2>&1
child_rc=$?
set -e
if [ "$child_rc" = 0 ]; then
	bad "compiled-only refused prep: exit 0 (must not pass when the graph is unavailable)"
elif command grep -qE '^ok   compilado:' "$CHILD_LOG"; then
	bad "compiled-only refused prep: credited a compiled runtime case"
elif ! command grep -qF 'NOT MEASURED' "$CHILD_LOG"; then
	bad "compiled-only refused prep: rc=$child_rc without NOT MEASURED; $(tail -c 400 "$CHILD_LOG")"
elif ! command grep -qE 'selftest: 0 passed,' "$CHILD_LOG"; then
	bad "compiled-only refused prep: credited cases: $(command grep -E 'selftest:' "$CHILD_LOG" || true)"
else
	ok "compiled-only refused prep: rc=$child_rc, 0 passed, NOT MEASURED, no compilado ok"
fi

# (3) The outer selftest must report this child's exact nonzero status, not
#     the 0 of `if ! cmd`. Not a compiled-runtime or mutant-404 verdict.
FORCE_LOG="$TMP/force-rc.log"
set +e
OLIVARES_AWS_ESTATE_COMPILED_ONLY=1 \
OLIVARES_AWS_ESTATE_COMPILED_MODCACHE_FORCE_RC=7 \
bash "$ROOT/scripts/test-aws-estate.sh" >"$FORCE_LOG" 2>&1
force_rc=$?
set -e
if [ "$force_rc" = 0 ]; then
	bad "forced child: outer selftest exited 0"
elif ! command grep -qF 'compiled-modcache bootstrap regression — rc=7' "$FORCE_LOG"; then
	bad "forced child: did not report exact exit 7; $(tail -c 400 "$FORCE_LOG")"
elif command grep -qF 'compiled-modcache bootstrap regression — rc=0' "$FORCE_LOG"; then
	bad "forced child: reported rc=0 (the if-not negation), not the child"
elif command grep -qE '^ok   compilado:' "$FORCE_LOG"; then
	bad "forced child: credited a compiled runtime case"
elif command grep -qF 'got 404 want 204' "$FORCE_LOG"; then
	bad "forced child: mislabelled as a mutant 404"
else
	ok "forced child: outer selftest rc=$force_rc reports exact child exit 7"
fi

printf 'aws-estate compiled-modcache: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
