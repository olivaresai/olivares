#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-alc-01-s2-managed-contract.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-alc-01-s2-managed-contract.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/alc01s2.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/core/api" "$TMP/tree/core/auth" \
		"$TMP/tree/cmd/olivares"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-alc-01-s2-managed-contract.sh"
	cp "$ROOT/design/alc-01-s2-managed-contract.json" "$TMP/tree/design/"
	cp "$ROOT/design/ALC-01-S2-MANAGED-CONTRACT-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/core/api/handlers_scim.go" "$TMP/tree/core/api/"
	cp "$ROOT/core/api/handlers_scim_groups.go" "$TMP/tree/core/api/"
	cp "$ROOT/core/api/server.go" "$TMP/tree/core/api/"
	cp "$ROOT/core/auth/scim.go" "$TMP/tree/core/auth/"
	cp "$ROOT/core/auth/federation_login.go" "$TMP/tree/core/auth/"
	cp "$ROOT/cmd/olivares/wire_noenterprise.go" "$TMP/tree/cmd/olivares/"
	cp "$ROOT/cmd/olivares/edition_ports.go" "$TMP/tree/cmd/olivares/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		bash "$TMP/tree/scripts/check-alc-01-s2-managed-contract.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live managed-SCIM contract is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/core/api/handlers_scim.go" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
p.write_text(p.read_text().replace(
    "func (s *Server) scimCreateUser",
    "func (s *Server) scimMintUser",
    1,
))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: inbound create renamed is FAIL"
else
	bad "renamed create should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
printf '\n_ = r.Header.Get("Idempotency-Key")\n' >>"$TMP/tree/core/api/handlers_scim.go"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: inbound Idempotency-Key is FAIL"
else
	bad "inbound header should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/alc-01-s2-managed-contract.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["motor_implemented"] = True
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: motor_implemented true is FAIL"
else
	bad "motor flag should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/alc-01-s2-managed-contract.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["verbs"] = ["create", "update"]
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: verbs dropped deprovision is FAIL"
else
	bad "short verbs should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'managed SCIM shipped' >>"$TMP/tree/design/ALC-01-S2-MANAGED-CONTRACT-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims motor shipped is FAIL"
else
	bad "shipped claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/ALC-01-S2-MANAGED-CONTRACT-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing contract doc is COULD NOT LOOK"
else
	bad "missing doc should be 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# THE SSO COMPLETION'S CALL, whole. CompleteSSO (core/auth/federation_login.go) calls
# findOrProvision(ctx, id, !authoritative, tenant): a SCIM-authoritative provider never
# JIT-creates (!authoritative), and the sign-in is bound to the resolved tenant's providers
# (tenant, added by fad340b164 on 2026-09-21). This check was written on 2026-08-20
# (67f2bc4c25) and pinned the three-argument call 0b7bba1936 had introduced; that shape predates
# the tenant binding and is no longer the contract. Each case below rewrites the one accepted
# call of the staged source, and the helper refuses to run a case whose rewrite did not apply.
ACCEPTED='a.findOrProvision(ctx, id, !authoritative, tenant)'
login_call() { # login_call <replacement>: CompleteSSO's accepted call becomes that text
	python3 - "$TMP/tree/core/auth/federation_login.go" "$ACCEPTED" "$1" <<'PY'
import sys
from pathlib import Path
p, old, new = Path(sys.argv[1]), sys.argv[2], sys.argv[3]
s = p.read_text()
if s.count(old) != 1:
    raise SystemExit("BATTERY BROKEN: the accepted call is not in the staged source exactly once")
p.write_text(s.replace(old, new, 1))
PY
}
said() { grep -qF -- "$1" "$TMP/err"; }

# Each write registration must remain pinned, including its SCIM write permission.
for registration in \
	'coreRoute(r, "POST", "/Users", s.scimRoute("user:write"), s.scimCreateUser)' \
	'coreRoute(r, "PUT", "/Users/{id}", s.scimRoute("user:write"), s.scimReplaceUser)' \
	'coreRoute(r, "PATCH", "/Users/{id}", s.scimRoute("user:write"), s.scimPatchUser)' \
	'coreRoute(r, "DELETE", "/Users/{id}", s.scimRoute("user:write"), s.scimDeleteUser)'; do
	stage
	python3 - "$TMP/tree/core/api/server.go" "$registration" <<'PY'
from pathlib import Path
import sys
p, registration = Path(sys.argv[1]), sys.argv[2]
s = p.read_text()
if s.count(registration) != 1:
    raise SystemExit("BATTERY BROKEN: the SCIM registration is not in the staged source exactly once")
p.write_text(s.replace(registration, "", 1))
PY
	run
	if [ "$(cat "$TMP/rc")" = 1 ] && said "inbound symbol missing in core/api/server.go: $registration"; then
		ok "firing: missing SCIM registration is FAIL: $registration"
	else
		bad "missing registration should FAIL 1 naming the registration ($(cat "$TMP/rc") $(cat "$TMP/err"))"
	fi
done

stage
if [ "$(grep -cF "$ACCEPTED" "$TMP/tree/core/auth/federation_login.go")" != 1 ]; then
	echo "BATTERY BROKEN: the live source does not carry the accepted call exactly once" >&2
	exit 1
fi
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "accepted: CompleteSSO's findOrProvision(ctx, id, !authoritative, tenant) is CLEAN"
else
	bad "the accepted no-JIT + tenant call should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
login_call 'a.findOrProvision(ctx, id, true, tenant)'
run
if [ "$(cat "$TMP/rc")" = 1 ] && said 'SCIM-authoritative no-JIT lost'; then
	ok "negative: JIT allowed under a SCIM-authoritative provider is FAIL, named no-JIT"
else
	bad "a call without !authoritative should FAIL 1 naming no-JIT ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
login_call 'a.findOrProvision(ctx, id, !authoritative)'
run
if [ "$(cat "$TMP/rc")" = 1 ] && said 'tenant binding lost'; then
	ok "negative: the tenant binding dropped is FAIL, named tenant binding"
else
	bad "a call without the tenant should FAIL 1 naming the binding ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
login_call 'a.findOrProvision(ctx, id, !authoritative, "")'
run
if [ "$(cat "$TMP/rc")" = 1 ] && said 'tenant binding lost'; then
	ok "negative: the tenant replaced by the global scope \"\" is FAIL, named tenant binding"
else
	bad "a call bound to \"\" should FAIL 1 naming the binding ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
login_call 'a.findOrProvision(ctx, id, true)'
run
if [ "$(cat "$TMP/rc")" = 1 ] && said 'SCIM-authoritative no-JIT lost' && said 'tenant binding lost'; then
	ok "negative: both dropped is FAIL with BOTH messages, each its own"
else
	bad "a call without either should name both losses ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
login_call "$ACCEPTED"$'\n\t_, _ = a.findOrProvision(ctx, id, true, tenant)'
run
if [ "$(cat "$TMP/rc")" = 1 ] && said 'SCIM-authoritative no-JIT lost'; then
	ok "negative: a second SSO-completion call that allows JIT is FAIL beside the accepted one"
else
	bad "every call site must hold, not one ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
login_call 'a.findOrProvision(ctx, id, !authoritative)'
run
if [ "$(cat "$TMP/rc")" = 1 ] && said 'findOrProvision(ctx, id, !authoritative, tenant)'; then
	ok "historic: the three-argument call (0b7bba1936, pinned by 67f2bc4c25) is no longer the contract"
else
	bad "the historic shape should FAIL 1 and name the accepted call ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
login_call 'a.findOrProvision(ctx, id, !authoritative, tenant'
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "an SSO-completion call the check cannot read is COULD NOT LOOK"
else
	bad "an unreadable call should be 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: restored live stays CLEAN"
else
	bad "restored live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo "check-alc-01-s2-managed-contract selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
