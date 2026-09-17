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
	# 51 constructors, one fmt.Errorf, CAEP error tuple + nil,nil.
	#
	# The pin lives in three places: JSON, the guard script, and this throwaway
	# fixture. Moving it is three deliberate edits. Count = 46 audited (44 generic
	# + the two GO-block seams) plus the NAMED post-audit constructors in the JSON;
	# the guard looks each name up in the wire, not only the number. A new seam is
	# added here by name, not by raising the generic 44 loop. 2026-09-13: the fifth
	# named post-audit seam is loginEnforcementComponentLinked, a bare bool false
	# predicate (51 = 46 + 5). The boot invariant stays open.
	{
		echo 'package main'
		i=1
		while [ "$i" -le 44 ]; do
			printf 'func seam%02d() {}\n' "$i"
			i=$((i + 1))
		done
		cat <<'GO'
func newDurableBus() (any, error) {
	return nil, fmt.Errorf("community durable bus")
}
func newCAEPTransmitter() (caepTransmitter, error) {
	return nil, nil
}
func editionModuleRegistrars(_ EditionConfig) []api.Module { return nil }
func editionAgentServers() []editionAuxServer { return nil }
func editionWebFS(base fs.FS) fs.FS { return base }
func editionBindModuleDependencies(context.Context, EditionConfig, []api.Module, EditionDependencies, *slog.Logger) ([]io.Closer, error) {
	return nil, nil
}
func loginEnforcementComponentLinked() bool { return false }
GO
	} >"$TMP/tree/cmd/olivares/wire_noenterprise.go"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c02-12-boot-seams.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: pinned open audit is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c02-12-boot-seams.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["invariant_closed"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: invariant_closed true is FAIL"
else
	bad "invariant_closed true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c02-12-boot-seams.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["constructors"] = 44
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: claiming 44 constructors is FAIL"
else
	bad "constructors 44 should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c02-12-boot-seams.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["boot_aborting"] = []
d["boot_aborting_count"] = 0
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: hiding newDurableBus abort is FAIL"
else
	bad "empty boot_aborting should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/cmd/olivares/wire_noenterprise.go" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read().replace("return nil, fmt.Errorf(\"community durable bus\")", "return nil, nil")
open(p, "w", encoding="utf-8").write(t)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: dropping the fmt.Errorf return is FAIL"
else
	bad "dropped fmt.Errorf should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'invariant closed' >>"$TMP/tree/design/C02-12-BOOT-SEAMS-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claiming invariant closed is FAIL"
else
	bad "false close should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# Causal negative controls for the 2026-09-06 failure class: the WIRE moves and the
# evidence does not (an unaccounted constructor), a seam the evidence names is gone while
# the count survives, a count that names one seam fewer than it claims, and the pinned
# abort migrating from newDurableBus into a post-audit seam with the file-level error
# count unchanged. Each asserts the message of the check that must fire, so a red for
# another reason does not pass as this one.
stage
printf 'func seamUnaccounted() {}\n' >>"$TMP/tree/cmd/olivares/wire_noenterprise.go"
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'live constructor count 52 != pinned 51' "$TMP/err"; then
	ok "firing: an unaccounted constructor in the wire is FAIL on the count"
else
	bad "unaccounted constructor should FAIL 1 on the count ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/cmd/olivares/wire_noenterprise.go" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read().replace("func editionBindModuleDependencies(", "func editionRenamedSeam(")
open(p, "w", encoding="utf-8").write(t)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'wire lost editionBindModuleDependencies' "$TMP/err"; then
	ok "firing: a named post-audit seam missing at the pinned count is FAIL by name"
else
	bad "missing named seam should FAIL 1 by name ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/cmd/olivares/wire_noenterprise.go" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read().replace("func loginEnforcementComponentLinked(", "func loginEnforcementRenamedPredicate(")
open(p, "w", encoding="utf-8").write(t)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'wire lost loginEnforcementComponentLinked' "$TMP/err"; then
	ok "firing: the named login-enforcement predicate missing at the pinned count is FAIL by name"
else
	bad "missing login-enforcement predicate should FAIL 1 by name ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c02-12-boot-seams.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["post_audit_constructors"].remove("editionBindModuleDependencies")
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'constructors 51 != audited 46 + 4 post-audit seams' "$TMP/err"; then
	ok "firing: a count that names one seam fewer is FAIL on the arithmetic"
else
	bad "unnamed count should FAIL 1 on the arithmetic ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/cmd/olivares/wire_noenterprise.go" <<'PY'
import sys
w = sys.argv[1]
t = open(w, encoding="utf-8").read()
t = t.replace("return nil, fmt.Errorf(\"community durable bus\")", "return nil, nil")
t = t.replace(
    "EditionDependencies, *slog.Logger) ([]io.Closer, error) {\n\treturn nil, nil",
    "EditionDependencies, *slog.Logger) ([]io.Closer, error) {\n\treturn nil, fmt.Errorf(\"edition bind\")")
open(w, "w", encoding="utf-8").write(t)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'editionBindModuleDependencies must not abort boot' "$TMP/err"; then
	ok "firing: the pinned abort migrating into a post-audit seam is FAIL by name"
else
	bad "abort migrated into a post-audit seam should FAIL 1 by name ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/c02-12-boot-seams.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing JSON is LOOK (2)"
else
	bad "missing JSON should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

if OLIVARES_ROOT="$ROOT" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
	ok "no-fire: live checkout stays CLEAN"
else
	bad "no-fire live went RED ($(cat "$TMP/err"))"
fi

echo
echo "test-c02-12-boot-seams: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
