#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-eco-08-witnesses.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-eco-08-witnesses.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/eco08.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/sessions/campaign-prompts"
	cp "$CHECK" "$TMP/tree/scripts/check-eco-08-witnesses.sh"
	chmod +x "$TMP/tree/scripts/check-eco-08-witnesses.sh"
	cp "$ROOT/design/eco-08-witnesses.json" "$TMP/tree/design/"
	cat >"$TMP/tree/design/ECO-08-WITNESSES-HOLD-2026-08-19.md" <<'EOF'
NOT EXECUTED. Window closed. Six live subjects listed. Apply UNVERIFIED.
EOF
	cat >"$TMP/tree/design/PRICING-CANON.md" <<'EOF'
the apply of the scheduled change stays UNVERIFIED until 2026-08-31
EOF
	cat >"$TMP/tree/sessions/campaign-prompts/OPS-CEREMONIA-TESTIGOS-2026-08-31.md" <<'EOF'
Window 2026-08-31 ~22:00Z
sub_0NkP3LVKahuHbk9zhoK5Z
sub_0NkP75ayilVta6oedIuN2
sub_0NkOxYJS3oGLDht7HuoWF
sub_0NkP3LQNqDAjMzlZ8un3y
sub_0NkP3LaF1OSkPQuHYC7UQ
sub_0NkP0ctMBwPDCSX4NZmSo
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-eco-08-witnesses.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: witness HOLD is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/eco-08-witnesses.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["executed"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: executed true is FAIL"
else
	bad "executed true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/eco-08-witnesses.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["window_open"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: window_open true is FAIL"
else
	bad "window_open true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/eco-08-witnesses.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["live_subjects"] = d["live_subjects"][:5]
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: drop one live subject is FAIL"
else
	bad "dropped subject should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'apply verified' >>"$TMP/tree/design/ECO-08-WITNESSES-HOLD-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claiming apply verified is FAIL"
else
	bad "apply claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/sessions/campaign-prompts/OPS-CEREMONIA-TESTIGOS-2026-08-31.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing runbook is LOOK (2)"
else
	bad "missing runbook should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

if OLIVARES_ROOT="$ROOT" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
	ok "no-fire: live checkout stays CLEAN"
else
	bad "no-fire live went RED ($(cat "$TMP/err"))"
fi

echo
echo "test-eco-08-witnesses: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
