#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for C02-17. Drives the SHIPPED export-update-bundle.sh --list-files
# path (no olivares binary, no signing). Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c02-17-airgap-set.sh"
BUNDLE="$ROOT/scripts/export-update-bundle.sh"
SETS="$ROOT/commercial/license-worker/src/download/sets.ts"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0217.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree" "$TMP/rel"
	mkdir -p "$TMP/tree/scripts" \
		"$TMP/tree/commercial/license-worker/src/download"
	cp "$BUNDLE" "$TMP/tree/scripts/export-update-bundle.sh"
	cp "$CHECK" "$TMP/tree/scripts/check-c02-17-airgap-set.sh"
	cp "$SETS" "$TMP/tree/commercial/license-worker/src/download/sets.ts"
	chmod +x "$TMP/tree/scripts/"*.sh
}

list() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/export-update-bundle.sh" \
		--list-files "$@" >"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
}

touch_arch() {
	mkdir -p "$(dirname "$1")"
	: >"$1"
}

stage
if OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c02-17-airgap-set.sh" >/dev/null 2>"$TMP/err"; then
	ok "live producer shape is CLEAN"
else
	bad "live check should be CLEAN ($(cat "$TMP/err"))"
fi

# no-fire: community flat dir, no --set
stage
touch_arch "$TMP/rel/olivares_26.8.0_linux_amd64.tar.gz"
list --dir "$TMP/rel" --version 26.8.0
if [ "$(cat "$TMP/rc")" = 0 ] && grep -q '^SET=$' "$TMP/out" \
	&& grep -q 'olivares_26.8.0_linux_amd64.tar.gz' "$TMP/out"; then
	ok "no-fire: community flat dir without --set still lists"
else
	bad "community flat should list ($(cat "$TMP/rc") $(cat "$TMP/err") $(cat "$TMP/out"))"
fi

# mixed tree without --set is a finding
stage
touch_arch "$TMP/rel/biz/olivares_26.8.0_linux_amd64.tar.gz"
touch_arch "$TMP/rel/ent/olivares_26.8.0_linux_amd64.tar.gz"
list --dir "$TMP/rel" --version 26.8.0
if [ "$(cat "$TMP/rc")" = 1 ] && grep -qi 'set prefixes\|--set' "$TMP/err"; then
	ok "mutant (omit --set on a mixed tree) is killed"
else
	bad "mixed tree without --set should be FAIL ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# --set biz packs only biz (ARCHDIR ends with /biz)
stage
touch_arch "$TMP/rel/biz/olivares_26.8.0_linux_amd64.tar.gz"
touch_arch "$TMP/rel/ent/olivares_26.8.0_linux_arm64.tar.gz"
list --dir "$TMP/rel" --version 26.8.0 --set biz
if [ "$(cat "$TMP/rc")" = 0 ] \
	&& grep -q '^SET=biz$' "$TMP/out" \
	&& grep -q '/biz$' "$TMP/out" \
	&& grep -q 'linux_amd64' "$TMP/out" \
	&& ! grep -q 'linux_arm64' "$TMP/out"; then
	ok "no-fire: --set biz lists only the biz archive"
else
	bad "--set biz should list only biz ($(cat "$TMP/rc") $(cat "$TMP/out") $(cat "$TMP/err"))"
fi

# producer layout enterprise/<ver>/<set>/
stage
touch_arch "$TMP/rel/enterprise/26.8.0/biz/olivares_26.8.0_linux_amd64.tar.gz"
touch_arch "$TMP/rel/enterprise/26.8.0/ent/olivares_26.8.0_linux_arm64.tar.gz"
list --dir "$TMP/rel" --version 26.8.0 --set biz
if [ "$(cat "$TMP/rc")" = 0 ] \
	&& grep -q 'enterprise/26.8.0/biz' "$TMP/out" \
	&& ! grep -q 'linux_arm64' "$TMP/out"; then
	ok "producer layout enterprise/<ver>/<set>/ honours --set"
else
	bad "enterprise/<ver>/biz should list ($(cat "$TMP/rc") $(cat "$TMP/out") $(cat "$TMP/err"))"
fi

# unknown slug
stage
touch_arch "$TMP/rel/biz/olivares_26.8.0_linux_amd64.tar.gz"
list --dir "$TMP/rel" --version 26.8.0 --set not-a-set
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'ALLOWED_SET_SLUGS' "$TMP/err"; then
	ok "mutant (unknown --set slug) is killed"
else
	bad "unknown slug should be FAIL ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# LOOK: --set without sets.ts
stage
rm -f "$TMP/tree/commercial/license-worker/src/download/sets.ts"
touch_arch "$TMP/rel/biz/olivares_26.8.0_linux_amd64.tar.gz"
list --dir "$TMP/rel" --version 26.8.0 --set biz
if [ "$(cat "$TMP/rc")" = 2 ] && grep -q 'COULD NOT LOOK' "$TMP/err"; then
	ok "missing sets.ts with --set is COULD NOT LOOK"
else
	bad "missing allowlist should be 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# mutant of the CHECK: drop mixed-tree refusal → check goes CLEAN on a producer that packs DIR/*/
stage
python3 - "$TMP/tree/scripts/export-update-bundle.sh" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
t = p.read_text()
t = t.replace('for f in "$ARCHDIR"/olivares_', 'for f in "$DIR"/*/olivares_')
p.write_text(t)
PY
if OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c02-17-airgap-set.sh" >/dev/null 2>"$TMP/err"; then
	bad "recursive DIR/*/ glob stayed CLEAN"
else
	ok "mutant (glob DIR/*/olivares_) is killed"
fi

# restore check no-fire on the real tree
stage
if OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c02-17-airgap-set.sh" >/dev/null 2>"$TMP/err"; then
	ok "no-fire: live producer stays CLEAN"
else
	bad "restored live check should be CLEAN ($(cat "$TMP/err"))"
fi

printf 'check-c02-17-airgap-set selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
