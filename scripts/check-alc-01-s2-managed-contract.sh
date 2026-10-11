#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# ALC-01-S2: managed-SCIM contract is written; inbound symbols stay.
# Reads core/; does not write it. 0 CLEAN · 1 finding · 2 LOOK.
#
# The SSO completion's call is read whole: every findOrProvision call in CompleteSSO
# (core/auth/federation_login.go) is findOrProvision(ctx, id, !authoritative, tenant).
# !authoritative keeps a SCIM-authoritative provider from JIT-creating accounts (D4);
# tenant binds the sign-in to the resolved tenant's providers (fad340b164, 2026-09-21).
# This check pinned the three-argument call of 0b7bba1936 when it was written (67f2bc4c25,
# 2026-08-20), before that binding existed. Each lost half is its own finding.

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
PORTS="${OLIVARES_ALC01S2_PORTS:-cmd/olivares/edition_ports.go}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$HANDLERS" ] || cannot "missing inbound user handler"
[ -f "$GROUPH" ] || cannot "missing inbound group handler"
[ -f "$SERVER" ] || cannot "missing API server mount"
[ -f "$AUTHSCIM" ] || cannot "missing auth SCIM engine"
[ -f "$LOGIN" ] || cannot "missing SSO completion"
[ -f "$WIRE" ] || cannot "missing default wire"
[ -f "$PORTS" ] || cannot "missing edition ports"

grep -q 'Inbound that does NOT move' "$DOC" || fail "$DOC lost inbound-unmoved"
grep -q 'Idempotency-Key' "$DOC" || fail "$DOC lost outbound Idempotency-Key"
grep -q 'scim_authoritative' "$DOC" || fail "$DOC lost who-wins"
grep -q 'create' "$DOC" || fail "$DOC lost create"
grep -q 'update' "$DOC" || fail "$DOC lost update"
grep -q 'deprovision' "$DOC" || fail "$DOC lost deprovision"
grep -q 'HOLD on the motor' "$DOC" || fail "$DOC lost HOLD on the motor"
if grep -qiE 'managed SCIM shipped|S3 motor live|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims a close this batch does not have"
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
need_handler "$SERVER" 'coreRoute(r, "POST", "/Users", s.scimRoute("user:write"), s.scimCreateUser)'
need_handler "$SERVER" 'coreRoute(r, "PUT", "/Users/{id}", s.scimRoute("user:write"), s.scimReplaceUser)'
need_handler "$SERVER" 'coreRoute(r, "PATCH", "/Users/{id}", s.scimRoute("user:write"), s.scimPatchUser)'
need_handler "$SERVER" 'coreRoute(r, "DELETE", "/Users/{id}", s.scimRoute("user:write"), s.scimDeleteUser)'
need_handler "$AUTHSCIM" 'func (a *Authenticator) SCIMProvisionUser'
need_handler "$AUTHSCIM" 'func (a *Authenticator) SCIMUpdateUser'
need_handler "$AUTHSCIM" 'func (a *Authenticator) SCIMDeprovisionUser'
need_handler "$LOGIN" 'scimAuthoritative bool'
need_handler "$PORTS" 'managedSCIM editionPort[any]'

# The SSO completion's call, whole (see the header). Comments are not code, a call split over
# lines is read to its closing parenthesis, and a call the reader cannot close is COULD NOT LOOK.
CONTRACT_CALL='findOrProvision(ctx, id, !authoritative, tenant)'
LOGIN_OUT="$(python3 - "$LOGIN" "$CONTRACT_CALL" <<'PY'
import re, sys

path, contract = sys.argv[1], sys.argv[2]
ACCEPTED = ["ctx", "id", "!authoritative", "tenant"]
try:
    lines = open(path, encoding="utf-8").read().split("\n")
except OSError as exc:
    print(f"CANNOT could not read {path}: {exc}")
    raise SystemExit(0)


def code(line):
    """The line without its // comment; quotes are respected."""
    out, quote, i = [], None, 0
    while i < len(line):
        ch = line[i]
        if quote:
            out.append(ch)
            if ch == "\\" and quote != "`" and i + 1 < len(line):
                out.append(line[i + 1])
                i += 2
                continue
            if ch == quote:
                quote = None
        elif ch in "\"'`":
            quote = ch
            out.append(ch)
        elif line.startswith("//", i):
            break
        else:
            out.append(ch)
        i += 1
    return "".join(out)


start = next((i for i, l in enumerate(lines) if l.startswith("func (a *Authenticator) CompleteSSO(")), None)
if start is None:
    print(f"CompleteSSO, the SSO completion, is gone from {path}; the contract is {contract}")
    raise SystemExit(0)
end = next((i for i in range(start + 1, len(lines)) if lines[i] == "}"), None)
if end is None:
    print(f"CANNOT CompleteSSO in {path} has no closing brace this reader can find")
    raise SystemExit(0)
body = "\n".join(code(l) for l in lines[start + 1:end])

calls = []
for m in re.finditer(r"\bfindOrProvision\(", body):
    depth, j, args, cur = 1, m.end(), [], []
    while j < len(body):
        ch = body[j]
        if ch in "([{":
            depth += 1
        elif ch in ")]}":
            depth -= 1
            if depth == 0:
                break
        if ch == "," and depth == 1:
            args.append("".join(cur))
            cur = []
        else:
            cur.append(ch)
        j += 1
    if depth:
        print(f"CANNOT a findOrProvision call in CompleteSSO ({path}) never closes; its arguments cannot be read")
        raise SystemExit(0)
    args.append("".join(cur))
    calls.append([" ".join(a.split()) for a in args])

if not calls:
    print(f"CompleteSSO no longer calls findOrProvision; the contract is {contract}")
for args in calls:
    shown = "findOrProvision(" + ", ".join(args) + ")"
    if args == ACCEPTED:
        continue
    if args[:2] != ACCEPTED[:2]:
        print(f"CompleteSSO calls {shown}: it does not correlate the completion's own (ctx, id); "
              f"the contract is {contract}")
    if len(args) < 3 or args[2] != "!authoritative":
        print(f"SCIM-authoritative no-JIT lost: CompleteSSO calls {shown}; its third argument must be "
              f"!authoritative, or a SCIM-authoritative provider JIT-creates accounts at sign-in "
              f"(the contract is {contract})")
    if len(args) != 4 or args[3] != "tenant":
        print(f"tenant binding lost: CompleteSSO calls {shown}; its fourth and last argument must be "
              f"tenant, or the sign-in is not bound to the resolved tenant's providers "
              f"(the contract is {contract})")
PY
)" || cannot "the SSO completion reader failed to run on $LOGIN"
case "$LOGIN_OUT" in
CANNOT*) cannot "${LOGIN_OUT#CANNOT }" ;;
esac
if [ -n "$LOGIN_OUT" ]; then
	say "check-alc-01-s2-managed-contract: FAIL — the SSO completion does not keep its contract:" >&2
	printf '%s\n' "$LOGIN_OUT" | sed 's/^/  · /' >&2
	exit 1
fi

grep -q '^func editionPortsForBuild()' "$WIRE" \
	|| fail "the default wire no longer fills the Community edition; the nil check below would prove nothing"
if grep -q 'managedSCIM' "$WIRE"; then
	fail "default wire lost the nil managed-SCIM seam"
fi

# Inbound must not grow an Idempotency-Key reader. That would move it.
for f in "$HANDLERS" "$GROUPH" "$AUTHSCIM"; do
	if grep -F 'Idempotency-Key' "$f" >/dev/null; then
		fail "inbound grew Idempotency-Key: $f"
	fi
done

say "check-alc-01-s2-managed-contract: CLEAN — contract written; inbound unmoved; motor unbuilt;"
say "  the SSO completion calls $CONTRACT_CALL (no JIT under SCIM authority; tenant bound)."
exit 0
