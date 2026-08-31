#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c02-hold-key-until-producer.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c02-hold-key-until-producer.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c02hold.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" \
		"$TMP/tree/design" \
		"$TMP/tree/commercial/license-worker/src/download"
	# ⛔ La lib del sello va CON el checker: el guion en escena hace `source` de
	# scripts/lib/overlay-seal.sh (a repository gate) y sin ella muere ahi, asi que la bateria mediria
	# un fallo de montaje creyendo medir su sujeto. Lo caza el contraste sol max (A-03).
	mkdir -p "$TMP/tree/scripts/lib"
	cp "$ROOT/scripts/lib/overlay-seal.sh" "$TMP/tree/scripts/lib/"
	cp "$CHECK" "$TMP/tree/scripts/check-c02-hold-key-until-producer.sh"
	chmod +x "$TMP/tree/scripts/check-c02-hold-key-until-producer.sh"
	cp "$ROOT/design/c02-hold-key-until-producer.json" "$TMP/tree/design/"
	cat >"$TMP/tree/design/C02-HOLD-KEY-UNTIL-PRODUCER-2026-08-20.md" <<'EOF'
HOLD. Overlay producer not on overlay main. half-stitch.
EOF
	cat >"$TMP/tree/commercial/license-worker/src/download/artifacts.ts" <<'EOF'
export function artifactKey(version: string, os: string, arch: string, set: string): string {
  return `enterprise/${version}/${set}/olivares_${version}_${os}_${arch}.tar.gz`;
}
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" OLIVARES_ENT_DIR="" \
		bash "$TMP/tree/scripts/check-c02-hold-key-until-producer.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

flip() {
	python3 - "$TMP/tree/design/c02-hold-key-until-producer.json" "$1" "$2" <<'PY'
import json, sys
p, key, raw = sys.argv[1], sys.argv[2], sys.argv[3]
d = json.load(open(p, encoding="utf-8"))
if raw == "true":
    d[key] = True
elif raw == "false":
    d[key] = False
elif raw.lstrip("-").isdigit():
    # con signo tambien: un mutante de distancia negativa tiene que llegar como ENTERO,
    # no como cadena, o estaria probando la asercion de tipo en vez de la de rango.
    d[key] = int(raw)
else:
    d[key] = raw
json.dump(d, open(p, "w", encoding="utf-8"))
PY
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: remasured HOLD is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
flip producer_on_overlay_main true
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: claiming producer on overlay main is FAIL"
else
	bad "producer on overlay main should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
flip set_key_on_hub_main false
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: denying hub set key is FAIL"
else
	bad "set key off hub main should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
flip land_key_before_producer false
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: denying the half-stitch is FAIL"
else
	bad "land_key_before_producer false should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
cat >"$TMP/tree/commercial/license-worker/src/download/artifacts.ts" <<'EOF'
export function artifactKey(version: string, os: string, arch: string): string {
  return `enterprise/${version}/olivares_${version}_${os}_${arch}.tar.gz`;
}
EOF
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: three-argument artifactKey is FAIL"
else
	bad "three-arg key should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'producer on overlay main' >>"$TMP/tree/design/C02-HOLD-KEY-UNTIL-PRODUCER-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claiming producer on overlay main is FAIL"
else
	bad "producer claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# a repository gate: la distancia dejo de estar pineada a 0 y pasa a «entero no negativo, re-medido
# en el acto». Las DOS direcciones, porque con solo la de disparo un guion que siguiera
# exigiendo 0 pasaria esta bateria sin cambiar una linea.
# ⛔ Y lo que esta etapa NO demuestra, dicho tras el contraste `sol max`: 999 no es una
# distancia LEGITIMA, es una que el contrato ESTATICO acepta. Estas etapas corren con el
# overlay limpiado a proposito, asi que la igualdad viva no participa. Quien recupera la
# deteccion de un positivo equivocado es la comparacion viva, y esa es OPCIONAL.
stage
flip pr75_behind_overlay_main -1
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: a negative behind-distance is FAIL"
else
	bad "behind=-1 should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
flip pr75_behind_overlay_main thirty-six
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: a behind-distance that is not an integer is FAIL"
else
	bad "a non-integer behind-distance should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
flip pr75_behind_overlay_main 999
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: any well-formed distance passes the STATIC contract (0 is no longer pinned)"
else
	bad "behind=999 should stay CLEAN 0 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/c02-hold-key-until-producer.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing JSON is LOOK (2)"
else
	bad "missing JSON should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

if OLIVARES_ROOT="$ROOT" OLIVARES_ENT_DIR="" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
	ok "no-fire: live checkout stays CLEAN"
else
	bad "no-fire live went RED ($(cat "$TMP/err"))"
fi

echo
echo "test-c02-hold-key-until-producer: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
