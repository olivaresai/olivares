#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c03-06-needs-decision.sh. Both firing directions.
#
# ⛔ QUE MIDE AHORA (2026-09-05). El guion dejo de comparar la ausencia del 20/08 con ficheros
# vivos del overlay, asi que los mutantes que PLANTABAN la capacidad en un checkout ya no
# dicen nada de el: la etapa que los sustituye es la INVERSA y es la que importa —un checkout
# Enterprise que SI tiene la composicion de compra NO cambia el veredicto del registro.
# Lo demas que se exige es integridad: banderas, PINES EXACTOS y la adjudicacion que lo
# supera. Ver an internal design note (not shipped)

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
	cp "$ROOT/design/OVERLAY-FACT-GATES-ADJUDICATION-2026-09-05.md" "$TMP/tree/design/"
	# Un checkout Enterprise que YA TIENE lo que el acta observo ausente. Existe para probar
	# que NO influye: es el sujeto de la comparacion retirada.
	cat >"$TMP/ent/enterprise/rtbf/legalhold.go" <<'EOF'
package rtbf

func (h *LegalHoldOverride) EvaluateOverride(_ context.Context, holdID string, reason string, approvers int) (*OverrideDecision, error) {
	return nil, addonGate("rtbf").Authorize(nil, "override")
}
EOF
	cat >"$TMP/ent/cmd-overlay/olivares/durablebus_enterprise.go" <<'EOF'
package olivares

func durableLicensed(licenseFile, dataDir string, getenv func(string) string) bool {
	return combinedPurchaseView(holder.claims, holder.grants)(activation.PackIdentityScale)
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

expect() {
	if [ "$(cat "$TMP/rc")" = "$1" ]; then
		ok "$2"
	else
		bad "$2 — got $(cat "$TMP/rc"), wanted $1 [$(tail -1 "$TMP/err")]"
	fi
}

acta_set() {
	python3 - "$TMP/tree/design/c03-06-needs-decision.json" "$1" "$2" <<'PY'
import json, sys
p, k, v = sys.argv[1], sys.argv[2], sys.argv[3]
d = json.load(open(p, encoding="utf-8"))
d[k] = json.loads(v)
json.dump(d, open(p, "w", encoding="utf-8"), indent=2)
PY
}

stage
run
expect 0 "no-fire: the historical record is intact"
if grep -q 'HISTORICAL RECORD PRESERVED' "$TMP/out" && grep -q 'NOT evidence about current main' "$TMP/out"; then
	ok "the verdict says, in words, that it does not attest current main"
else
	bad "the CLEAN message must name itself historical ($(cat "$TMP/out"))"
fi

# ── LA ETAPA QUE SUSTITUYE A LOS MUTANTES DE OVERLAY ──────────────────────────────────────
stage
rm -rf "$TMP/ent"
run
expect 0 "no-fire: no Enterprise checkout at all gives the same verdict"

stage
run
rc_with=$(cat "$TMP/rc")
rm -rf "$TMP/ent"
run
if [ "$rc_with" = "$(cat "$TMP/rc")" ]; then
	ok "no-fire: a checkout that ALREADY has the capability does not move a historical verdict"
else
	bad "the verdict depended on the neighbouring tree ($rc_with vs $(cat "$TMP/rc"))"
fi

# ── integridad del registro ───────────────────────────────────────────────────────────────
stage
acta_set evaluate_override_gated true
run
expect 1 "firing: evaluate_override_gated flipped"

stage
acta_set durable_addon_scoped true
run
expect 1 "firing: durable_addon_scoped flipped"

stage
acta_set narrow_to_identity_scale true
run
expect 1 "firing: narrow_to_identity_scale flipped"

stage
acta_set overlay '"8d1720414b1356aea958002ba30f30fe2664e041"'
run
expect 1 "firing: the overlay pin was replaced with another 40-hex — a record whose subject can be swapped records nothing"

stage
acta_set hub '"0000000000000000000000000000000000000000"'
run
expect 1 "firing: the hub pin was replaced"

stage
acta_set lote '"C99-99"'
run
expect 1 "firing: the acta no longer says which lote it belongs to"

stage
echo 'durableLicensed now scoped' >>"$TMP/tree/design/C03-06-NEEDS-DECISION-2026-08-20.md"
run
expect 1 "firing: the historical doc claims a motor the lote did not have"

stage
python3 - "$TMP/tree/design/C03-06-NEEDS-DECISION-2026-08-20.md" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read().replace("EvaluateOverride is NO-GATE", "")
open(p, "w", encoding="utf-8").write(t)
PY
run
expect 1 "firing: the historical doc lost NO-GATE"

stage
python3 - "$TMP/tree/design/OVERLAY-FACT-GATES-ADJUDICATION-2026-09-05.md" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read().replace("daa083e56f331af6158475fc304fee633acbfc2b", "an integrated commit")
open(p, "w", encoding="utf-8").write(t)
PY
run
expect 1 "firing: the adjudication stopped naming the commit that changed the fact"

stage
rm -f "$TMP/tree/design/OVERLAY-FACT-GATES-ADJUDICATION-2026-09-05.md"
run
expect 2 "LOOK: the record survives but the adjudication that supersedes it is missing"

stage
rm -f "$TMP/tree/design/C03-06-NEEDS-DECISION-2026-08-20.md"
run
expect 2 "LOOK: missing decision doc"

stage
rm -f "$TMP/tree/design/c03-06-needs-decision.json"
run
expect 2 "LOOK: missing acta"

stage
run
expect 0 "no-fire: restored record stays CLEAN"

echo "check-c03-06-needs-decision selftest: $pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1
exit 0
