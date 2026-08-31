#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c03-06-needs-decision.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c03-06-needs-decision.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0306.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree" "$TMP/ent"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/ent/enterprise/rtbf" "$TMP/ent/cmd-overlay/olivares"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c03-06-needs-decision.sh"
	cp "$ROOT/design/c03-06-needs-decision.json" "$TMP/tree/design/"
	cp "$ROOT/design/C03-06-NEEDS-DECISION-2026-08-20.md" "$TMP/tree/design/"
	cat >"$TMP/ent/enterprise/rtbf/legalhold.go" <<'EOF'
package rtbf

func (h *LegalHoldOverride) EvaluateOverride(_ context.Context, holdID string, reason string, approvers int) (*OverrideDecision, error) {
	return nil, nil
}
EOF
	cat >"$TMP/ent/cmd-overlay/olivares/durablebus_enterprise.go" <<'EOF'
package olivares

func durableLicensed(licenseFile, dataDir string, getenv func(string) string) bool {
	c, err := license.Verify(src.Blob, pub)
	if err != nil {
		return false
	}
	return c.Status(time.Now()) != license.StatusExpired
}
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" OLIVARES_ENT_DIR="$TMP/ent" \
		bash "$TMP/tree/scripts/check-c03-06-needs-decision.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live classification pin is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'addonGate.Authorize(ctx, "wire")' >>"$TMP/ent/enterprise/rtbf/legalhold.go"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: gated EvaluateOverride is FAIL"
else
	bad "gated override should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/ent/cmd-overlay/olivares/durablebus_enterprise.go" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
t = p.read_text()
old = "\treturn c.Status(time.Now()) != license.StatusExpired\n"
new = "\treturn len(c.Features) > 0 && c.Status(time.Now()) != license.StatusExpired\n"
if old not in t:
    raise SystemExit("term return not found")
p.write_text(t.replace(old, new, 1))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: Features consult in durableLicensed is FAIL"
else
	bad "Features consult should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c03-06-needs-decision.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["evaluate_override_gated"] = True
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: evaluate_override_gated true is FAIL"
else
	bad "gated flag should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'durableLicensed now scoped' >>"$TMP/tree/design/C03-06-NEEDS-DECISION-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims scoped motor is FAIL"
else
	bad "scoped claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/C03-06-NEEDS-DECISION-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing decision doc is COULD NOT LOOK"
else
	bad "missing doc should be 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
run
OLIVARES_ROOT="$TMP/tree" OLIVARES_ENT_DIR="" \
	bash "$TMP/tree/scripts/check-c03-06-needs-decision.sh" \
	>"$TMP/out" 2>"$TMP/err" || true
if grep -q 'COULD NOT LOOK' "$TMP/err"; then
	ok "unset overlay dir is COULD NOT LOOK"
else
	bad "unset overlay dir should LOOK 2 ($(cat "$TMP/err"))"
fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: restored live stays CLEAN"
else
	bad "restored live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo "check-c03-06-needs-decision selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
