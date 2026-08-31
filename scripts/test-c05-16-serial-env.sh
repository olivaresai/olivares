#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c05-16-serial-env.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c05-16-serial.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/commercial/license-worker/src/dodo" \
           "$TMP/tree/commercial/license-worker/src/license" \
           "$TMP/tree/commercial/license-worker/src/store" \
           "$TMP/tree/commercial/license-worker/migrations" \
           "$TMP/tree/scripts"
  cp "$ROOT/commercial/license-worker/src/dodo/provider-id.ts" \
     "$TMP/tree/commercial/license-worker/src/dodo/"
  cp "$ROOT/commercial/license-worker/src/license/issue-context.ts" \
     "$TMP/tree/commercial/license-worker/src/license/"
  cp "$ROOT/commercial/license-worker/src/store/store.ts" \
     "$TMP/tree/commercial/license-worker/src/store/"
  cp "$ROOT/commercial/license-worker/migrations/0029_dodo_serial_purpose.sql" \
     "$TMP/tree/commercial/license-worker/migrations/"
  # 0034 is staged too since: the check now also demands the database-level refusal
  # of a NEW env-less serial, which is strictly more than it asked before.
  cp "$ROOT/commercial/license-worker/migrations/0034_dodo_serial_purpose_on_write.sql" \
     "$TMP/tree/commercial/license-worker/migrations/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-c05-16-serial-env.sh"
}
run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c05-16-serial-env.sh" >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
  return "$rc"
}

stage
if run; then ok "live namespaced serial is CLEAN"; else bad "live should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/commercial/license-worker/src/dodo/provider-id.ts" <<'PY'
import sys
p=sys.argv[1]
t=open(p,encoding="utf-8").read()
t=t.replace("cred_${purpose}_${businessId}_${paymentId}", "cred_${businessId}_${paymentId}")
open(p,"w",encoding="utf-8").write(t)
PY
if run; then bad "purpose-less credentialSerial stayed CLEAN"; else ok "mutant (drop purpose from serial) is killed"; fi

stage
python3 - "$TMP/tree/commercial/license-worker/src/license/issue-context.ts" <<'PY'
import sys
p=sys.argv[1]
t=open(p,encoding="utf-8").read()
old="credentialSerial(purpose, purchase.businessId, purchase.paymentId)"
new="`cred_${purchase.businessId}_${purchase.paymentId}`"
if old not in t:
    raise SystemExit("issuance serial call not found")
open(p,"w",encoding="utf-8").write(t.replace(old,new,1))
PY
if run; then bad "issuanceFor env-less serial stayed CLEAN"; else ok "mutant (issuanceFor drops purpose) is killed"; fi

stage
python3 - "$TMP/tree/commercial/license-worker/src/store/store.ts" <<'PY'
import sys
p=sys.argv[1]
t=open(p,encoding="utf-8").read()
# Split the validator: the NARROW predicate sits on the options.initial (write) arm and
# the WIDE one on the read arm, because 0029 declares the legacy serial valid for rows written
# before it while the runtime refused them on every READ. So the mutant is "widen the WRITE
# side" - the defect this fence exists to catch - not "remove one call".
old="? serialMatchesIssuance(input.issuance.serial"
new="? serialMatchesStoredIssuance(input.issuance.serial"
if old not in t:
    raise SystemExit("store serial fence not found")
open(p,"w",encoding="utf-8").write(t.replace(old,new,1))
PY
if run; then bad "store env-less serial stayed CLEAN"; else ok "mutant (batch accepts env-less serial) is killed"; fi

stage
python3 - "$TMP/tree/commercial/license-worker/src/store/store.ts" <<'PYEOF'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read()
old = "const serialOk = options.initial"
if old not in t:
    raise SystemExit("write/read discriminator not found")
open(p, "w", encoding="utf-8").write(t.replace(old, "const serialOk = Boolean(options)", 1))
PYEOF
if run; then bad "removing the write/read discriminator stayed CLEAN"; else ok "mutant (discriminator removed) is killed"; fi

stage
python3 - "$TMP/tree/commercial/license-worker/src/dodo/provider-id.ts" <<'PYEOF'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read()
old = "export function serialMatchesStoredIssuance"
if old not in t:
    raise SystemExit("read-side predicate not found")
open(p, "w", encoding="utf-8").write(t.replace(old, "export function serialMatchesStoredRemoved", 1))
PYEOF
if run; then bad "losing the read-side predicate stayed CLEAN"; else ok "mutant (read side gone) is killed"; fi

stage
python3 - "$TMP/tree/commercial/license-worker/migrations/0034_dodo_serial_purpose_on_write.sql" <<'PYEOF'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read()
old = "dodo_issuance_serial_names_purpose"
if old not in t:
    raise SystemExit("0034 trigger name not found")
open(p, "w", encoding="utf-8").write(t.replace(old, "dodo_issuance_something_else"))
PYEOF
if run; then bad "0034 without its refusal stayed CLEAN"; else ok "mutant (0034 drops the DB refusal) is killed"; fi

stage
if ! run; then bad "no-fire: live namespaced serial should stay CLEAN ($(cat "$TMP/err"))"
else ok "no-fire: live namespaced serial stays CLEAN"; fi

stage
rm -f "$TMP/tree/commercial/license-worker/src/dodo/provider-id.ts"
if run; then bad "missing provider-id.ts stayed CLEAN"; else
  if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing provider-id.ts is COULD NOT LOOK"
  else bad "missing file should be exit 2 ($(cat "$TMP/err"))"; fi
fi

printf 'check-c05-16-serial-env selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
