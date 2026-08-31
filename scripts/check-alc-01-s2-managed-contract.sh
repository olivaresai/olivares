#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# ALC-01-S2: managed-SCIM contract is written; inbound symbols stay.
# Reads core/; does not write it. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-alc-01-s2-managed-contract: FAIL — $*" >&2; exit 1; }
cannot() { say "check-alc-01-s2-managed-contract: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ALC01S2_JSON:-design/alc-01-s2-managed-contract.json}"
DOC="${OLIVARES_ALC01S2_DOC:-design/ALC-01-S2-MANAGED-CONTRACT-2026-08-20.md}"
HANDLERS="${OLIVARES_ALC01S2_HANDLERS:-core/api/handlers_scim.go}"
GROUPH="${OLIVARES_ALC01S2_GROUPH:-core/api/handlers_scim_groups.go}"
SERVER="${OLIVARES_ALC01S2_SERVER:-core/api/server.go}"
AUTHSCIM="${OLIVARES_ALC01S2_AUTHSCIM:-core/auth/scim.go}"
LOGIN="${OLIVARES_ALC01S2_LOGIN:-core/auth/federation_login.go}"
WIRE="${OLIVARES_ALC01S2_WIRE:-cmd/olivares/wire_noenterprise.go}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$HANDLERS" ] || cannot "missing inbound user handler"
[ -f "$GROUPH" ] || cannot "missing inbound group handler"
[ -f "$SERVER" ] || cannot "missing API server mount"
[ -f "$AUTHSCIM" ] || cannot "missing auth SCIM engine"
[ -f "$LOGIN" ] || cannot "missing SSO completion"
[ -f "$WIRE" ] || cannot "missing default wire"

grep -q 'Inbound that does NOT move' "$DOC" || fail "$DOC lost inbound-unmoved"
grep -q 'Idempotency-Key' "$DOC" || fail "$DOC lost outbound Idempotency-Key"
grep -q 'scim_authoritative' "$DOC" || fail "$DOC lost who-wins"
grep -q 'create' "$DOC" || fail "$DOC lost create"
grep -q 'update' "$DOC" || fail "$DOC lost update"
grep -q 'deprovision' "$DOC" || fail "$DOC lost deprovision"
grep -q 'HOLD on the motor' "$DOC" || fail "$DOC lost HOLD on the motor"
if grep -qiE 'managed SCIM shipped|S3 motor live|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi

python3 - "$JSON" <<'PY' || fail "JSON flags drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "alc-01-s2-managed-contract/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("contract_written") is not True:
    raise SystemExit("contract_written must stay true")
if data.get("motor_implemented") is not False:
    raise SystemExit("motor_implemented must stay false")
if data.get("inbound_unmoved") is not True:
    raise SystemExit("inbound_unmoved must stay true")
if data.get("outbound_idempotency_required") is not True:
    raise SystemExit("outbound_idempotency_required must stay true")
if data.get("local_roster_when_authoritative") != "inbound-scim":
    raise SystemExit("local roster writer drifted")
if data.get("verbs") != ["create", "update", "deprovision"]:
    raise SystemExit("verbs drifted from create/update/deprovision")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
PY

need_handler() {
	grep -qF "$2" "$1" || fail "inbound symbol missing in $1: $2"
}

need_handler "$HANDLERS" 'func (s *Server) scimCreateUser'
need_handler "$HANDLERS" 'func (s *Server) scimReplaceUser'
need_handler "$HANDLERS" 'func (s *Server) scimPatchUser'
need_handler "$HANDLERS" 'func (s *Server) scimDeleteUser'
need_handler "$SERVER" 'r.Post("/Users", s.scimCreateUser)'
need_handler "$SERVER" 'r.Put("/Users/{id}", s.scimReplaceUser)'
need_handler "$SERVER" 'r.Patch("/Users/{id}", s.scimPatchUser)'
need_handler "$SERVER" 'r.Delete("/Users/{id}", s.scimDeleteUser)'
need_handler "$AUTHSCIM" 'func (a *Authenticator) SCIMProvisionUser'
need_handler "$AUTHSCIM" 'func (a *Authenticator) SCIMUpdateUser'
need_handler "$AUTHSCIM" 'func (a *Authenticator) SCIMDeprovisionUser'
need_handler "$LOGIN" 'scimAuthoritative bool'
need_handler "$LOGIN" 'findOrProvision(ctx, id, !authoritative)'
need_handler "$WIRE" 'func newManagedSCIM()'

if ! grep -qE 'return nil' "$WIRE"; then
	fail "default wire lost the nil managed-SCIM seam"
fi

# Inbound must not grow an Idempotency-Key reader. That would move it.
for f in "$HANDLERS" "$GROUPH" "$AUTHSCIM"; do
	if grep -F 'Idempotency-Key' "$f" >/dev/null; then
		fail "inbound grew Idempotency-Key: $f"
	fi
done

say "check-alc-01-s2-managed-contract: CLEAN — contract written; inbound unmoved; motor unbuilt."
exit 0
