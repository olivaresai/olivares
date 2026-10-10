#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c02-12-boot-seams.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c02-12-boot-seams.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0212.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" "$TMP/tree/cmd/olivares"
	cp "$CHECK" "$TMP/tree/scripts/check-c02-12-boot-seams.sh"
	chmod +x "$TMP/tree/scripts/check-c02-12-boot-seams.sh"
	cp "$ROOT/design/c02-12-boot-seams.json" "$TMP/tree/design/"
	cat >"$TMP/tree/design/C02-12-BOOT-SEAMS-2026-08-19.md" <<'EOF'
NOT CLOSED. newDurableBus still aborts boot on community.
EOF
	cat >"$TMP/tree/design/BACKLOG-COMPLETITUD-2026-08-16.md" <<'EOF'
| C02-12 | Auditar los 44 seams de wire_noenterprise.go
EOF
	cat >"$TMP/tree/design/ARTEFACTOS-POR-PACK-2026-08-08.md" <<'EOF'
preserved_on_every_lapse. ningún seam retirado por tag puede cambiar el CONTRATO DE ARRANQUE.
EOF
	# Since #149 the seams are the fields of one editionPorts value: the battery
	# stages the live port declaration and the live Community wire and mutates them.
	cp "$ROOT/cmd/olivares/edition_ports.go" "$ROOT/cmd/olivares/wire_noenterprise.go" "$TMP/tree/cmd/olivares/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c02-12-boot-seams.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

# mutate FILE OLD NEW replaces the first OLD and stops the battery when OLD is absent,
# so a mutant that silently stopped applying cannot pass as a kill.
mutate() {
	python3 -c '
import sys
p, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
t = open(p, encoding="utf-8").read()
if old not in t:
    raise SystemExit("mutant target moved: %r" % old)
open(p, "w", encoding="utf-8").write(t.replace(old, new, 1))
' "$@"
}

# json_set CODE runs CODE with the staged JSON bound to d, then writes it back.
json_set() {
	python3 -c '
import json, sys
p, code = sys.argv[1], sys.argv[2]
d = json.load(open(p, encoding="utf-8"))
exec(code)
json.dump(d, open(p, "w", encoding="utf-8"))
' "$TMP/tree/design/c02-12-boot-seams.json" "$1"
}

# expect NAME RC [MESSAGE]
expect() {
	if [ "$(cat "$TMP/rc")" = "$2" ] && { [ -z "${3:-}" ] || grep -qF "$3" "$TMP/err"; }; then
		ok "$1"
	else
		bad "$1 (want rc $2${3:+ and '$3'}; got $(cat "$TMP/rc") $(cat "$TMP/err"))"
	fi
}

WIRE_T="$TMP/tree/cmd/olivares/wire_noenterprise.go"
PORTS_T="$TMP/tree/cmd/olivares/edition_ports.go"

stage
run
expect "no-fire: pinned open audit is CLEAN" 0

stage
json_set 'd["invariant_closed"] = True'
run
expect "firing: invariant_closed true is FAIL" 1

stage
json_set 'd["constructors"] = 44'
run
expect "firing: claiming 44 constructors is FAIL" 1

stage
json_set 'd["boot_aborting"] = []; d["boot_aborting_count"] = 0'
run
expect "firing: hiding the durableBus abort is FAIL" 1

stage
mutate "$WIRE_T" 'return nil, fmt.Errorf(' '_ = fmt.Sprintf('
run
expect "firing: dropping the fmt.Errorf return is FAIL" 1

stage
echo 'invariant closed' >>"$TMP/tree/design/C02-12-BOOT-SEAMS-2026-08-19.md"
run
expect "firing: doc claiming invariant closed is FAIL" 1

# Causal negative controls for the 2026-09-06 failure class, on the port struct: an
# unaccounted port, a named seam gone while the count survives, a count that names one
# seam fewer than it claims, and the pinned abort migrating into a post-audit seam with
# the file-level error count unchanged. Each asserts the message of the check that must
# fire, so a red for another reason does not pass as this one.
stage
mutate "$PORTS_T" $'\tagentServers ' $'\tunaccounted editionPort[any]\n\tagentServers '
run
expect "firing: an unaccounted port is FAIL on the count" 1 'live port count 58 != pinned 57'

stage
mutate "$PORTS_T" $'\tbindModuleDependencies ' $'\trenamedBinding '
run
expect "firing: a named post-audit port missing at the pinned count is FAIL by name" 1 'edition ports lost bindModuleDependencies'

stage
mutate "$PORTS_T" $'\tloginEnforcementLinked ' $'\tloginRenamedPredicate '
run
expect "firing: the named login-enforcement port missing is FAIL by name" 1 'edition ports lost loginEnforcementLinked'

stage
json_set 'd["post_audit_constructors"].remove("bindModuleDependencies")'
run
expect "firing: a count that names one seam fewer is FAIL on the arithmetic" 1 'constructors 51 != audited 46 + 4 post-audit seams'

stage
mutate "$WIRE_T" 'return nil, fmt.Errorf(' 'return nil, errors.New('
mutate "$WIRE_T" 'return []api.Module{sessioncockpit.NewPlaceholder()}' 'return nil, fmt.Errorf("edition mount")'
run
expect "firing: the pinned abort migrating into a post-audit seam is FAIL by name" 1 'moduleRegistrars must not abort boot'

stage
mutate "$WIRE_T" $'\t\tname: "community",\n' $'\t\tname: "community",\n\t\tcaepTransmitter: nil,\n'
run
expect "firing: Community filling the CAEP port is FAIL" 1 'Community fills caepTransmitter'

stage
rm -f "$TMP/tree/design/c02-12-boot-seams.json"
run
expect "missing JSON is LOOK (2)" 2

if OLIVARES_ROOT="$ROOT" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
	ok "no-fire: live checkout stays CLEAN"
else
	bad "no-fire live went RED ($(cat "$TMP/err"))"
fi

echo
echo "test-c02-12-boot-seams: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
