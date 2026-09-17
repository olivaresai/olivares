#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C02 remasure: hub 4-arg set key is on main; overlay producer is not.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c02-hold-key-until-producer: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c02-hold-key-until-producer: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C02HOLD_JSON:-design/c02-hold-key-until-producer.json}"
DOC="${OLIVARES_C02HOLD_DOC:-design/C02-HOLD-KEY-UNTIL-PRODUCER-2026-08-20.md}"
ART="${OLIVARES_C02HOLD_ART:-commercial/license-worker/src/download/artifacts.ts}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$ART" ] || cannot "missing $ART"

grep -q 'HOLD' "$DOC" || fail "$DOC lost HOLD"
grep -q 'Overlay producer not on overlay main' "$DOC" \
	|| fail "$DOC lost overlay-producer HOLD"
grep -q 'half-stitch' "$DOC" || fail "$DOC lost half-stitch"
if grep -qiE 'producer on overlay main|FIRMA A claimed|bytes are real' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi
grep -q 'export function artifactKey(version: string, os: string, arch: string, set: string)' "$ART" \
	|| fail "hub origin lost the four-argument set-keyed artifactKey"
if grep -q 'export function artifactKey(version: string, os: string, arch: string): string' "$ART"; then
	fail "hub origin reverted to a three-argument artifactKey"
fi

python3 - "$JSON" "$DOC" <<'PY' || fail "JSON/doc failed the C02 HOLD remasure"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c02-hold-key-until-producer/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("producer_on_overlay_main") is not False:
    raise SystemExit("producer must stay off overlay main")
if data.get("set_key_on_hub_main") is not True:
    raise SystemExit("set key is on hub main")
if data.get("land_key_before_producer") is not True:
    raise SystemExit("land_key_before_producer is the measured half-stitch")
if data.get("merged") is not False:
    raise SystemExit("merged must stay false — overlay producer still OPEN")
if data.get("executed") is not False:
    raise SystemExit("executed must stay false")
if data.get("overlay_pr") != 75:
    raise SystemExit("overlay_pr must stay 75")
if data.get("hub_pr") != 1125:
    raise SystemExit("hub_pr must stay 1125")
# a repository gate / decisión r4 2026-08-29: la distancia se compara con LO MEDIDO EN EL ACTO, no con 0.
# Pinear 0 codificaba «esta PR está al nivel de overlay main», cierto al escribirlo y falso en
# cuanto el overlay avanza: los merges ent#126/#132/#133 lo movieron 36 commits y dejaron esta
# aserción estática peleada con la comprobación VIVA de más abajo — ningún valor satisfacía a
# las dos, así que el registro no se podía re-medir. La viva sigue siendo la que manda; aquí
# sólo se exige que el campo EXISTA y sea un entero no negativo, y se re-mide en el mismo acto
# que mueve el overlay (runbook §6-bis.7). `do_not_restack` se respeta: las PRs no se rebasan.
_behind = data.get("pr75_behind_overlay_main")
if not isinstance(_behind, int) or isinstance(_behind, bool) or _behind < 0:
    raise SystemExit("PR 75 behind overlay main must be a non-negative integer, re-measured in the act")
if data.get("pr75_ahead_of_overlay_main") != 8:
    raise SystemExit("PR 75 ahead of overlay main must stay 8")
if data.get("hub_artifact_key_arity") != 4:
    raise SystemExit("hub artifactKey arity must stay 4")
if data.get("overlay_blobs_directory") != "enterprise/{{ .Version }}":
    raise SystemExit("overlay blobs directory pin drifted")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("overlay_main_sha", "pr75_sha", "hub_sha", "hub_pr_sha"):
    val = data.get(key)
    if not isinstance(val, str) or not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
if data["overlay_main_sha"] == data["pr75_sha"]:
    raise SystemExit("overlay main and PR 75 cannot share an object id")
PY

# ⛔ LT1 · EL SUJETO VIVO YA NO ES `origin/main`. Ver la cabecera de la misma reforma en
# `scripts/check-c02-70-no-land-snapshot.sh`: el acta se conserva INMUTABLE, el Modulo
# (`scripts/lib/overlay-measurement.sh`) valida su identidad y sus distancias contra los
# objetos VIEJOS que ella nombra, captura el 40-hex del main ACTUAL sellado, exige historia
# descendiente y `behind` no decreciente, y publica la observacion terminal. Los predicados
# semanticos de abajo son los de siempre y ahora se miden sobre el SHA capturado.
#
# ⚠ `$OLIVARES_ENT_DIR` se resuelve AQUI, en codigo, para que el clasificador de actas siga
# viendo esta acta como VIVA (`check-overlay-actas-class.sh:56`).
ENT="${OLIVARES_ENT_DIR:-}"
. "$ROOT/scripts/lib/overlay-measurement.sh" || cannot "cannot load scripts/lib/overlay-measurement.sh"

if [ -z "$ENT" ]; then
	olivares_overlay_measure_open c02-hold-key-until-producer "$JSON" static-only \
		|| cannot "$OLIVARES_OVERLAY_MEASUREMENT_WHY"
	_frc=0
	olivares_overlay_measure_finish 0 "static-only: schema and shape verified, overlay not read" || _frc=$?
	case "$_frc" in
	2) cannot "$OLIVARES_OVERLAY_FINAL_WHY" ;;
	1) fail "$OLIVARES_OVERLAY_FINAL_WHY" ;;
	esac
	say "check-c02-hold-key-until-producer: NOTICE — live overlay remasure skipped"
	say "check-c02-hold-key-until-producer: CLEAN — HOLD; hub 4-arg on main; producer off overlay main."
	exit 0
fi

if [ -d "$ENT" ] && git -C "$ENT" rev-parse --git-dir >/dev/null 2>&1; then
	# a repository gate: la frescura la exige el Modulo antes de capturar nada. Un ref congelado no es
	# un veredicto — el clon TRAE por SSH (falla EN SILENCIO sin clave) y EMPUJA por HTTPS.
	_mrc=0
	olivares_overlay_measure_open c02-hold-key-until-producer "$JSON" current "$ENT" || _mrc=$?
	if [ "$_mrc" = 2 ]; then
		cannot "$OLIVARES_OVERLAY_MEASUREMENT_WHY"
	elif [ "$_mrc" != 0 ]; then
		fail "$OLIVARES_OVERLAY_MEASUREMENT_WHY"
	fi

	# ⛔ LAS TRES RESPUESTAS VIAJAN POR CODIGO DE SALIDA. Este bloque era
	# `python3 - … <<PY … PY || fail`, asi que un `.goreleaser.yaml` ilegible y un eje de set
	# en el directorio de blobs salian los DOS 1. Un «no he podido mirar» contado como
	# hallazgo hace que `remeasure-overlay-actas.sh` intente curar con una re-medida algo que
	# no es un numero rancio. Ahora: objeto ilegible = 2, contradiccion demostrada = 1.
	_live_err="$(mktemp "${TMPDIR:-/tmp}/c02hold.live.XXXXXX")"
	_live_rc=0
	python3 - "$ENT" "$JSON" "$OLIVARES_OVERLAY_CURRENT_SHA" 2>"$_live_err" <<'PY' || _live_rc=$?
import json, subprocess, sys

ent, path, main = sys.argv[1], sys.argv[2], sys.argv[3]
data = json.load(open(path, encoding="utf-8"))


def fail(msg):
    print(msg, file=sys.stderr)
    raise SystemExit(1)


def look(msg):
    print(msg, file=sys.stderr)
    raise SystemExit(2)


def git(*args):
    p = subprocess.run(
        ["git", "--no-replace-objects", "-C", ent, *args], capture_output=True, text=True
    )
    return p.returncode, p.stdout, p.stderr


rc, out, err = git("show", "%s:.goreleaser.yaml" % main)
if rc != 0:
    look("could not read the overlay goreleaser at %s: %s"
         % (main[:9], (err or "").strip()[:160] or "missing path"))
if 'directory: "enterprise/{{ .Version }}"' not in out:
    fail("overlay main blobs directory is no longer the monolith prefix")
if "enterprise/{{ .Version }}/{{" in out:
    fail("overlay main goreleaser now has a set axis in the blobs directory")
print("c02-hold: overlay main %s still ships the monolith blobs prefix" % main[:9])
PY
	cat "$_live_err" >&2
	rm -f "$_live_err"
	# El cierre corre SIEMPRE, y el veredicto lo decide UNA traduccion contratada del Modulo:
	# una custodia perdida manda sobre un hallazgo del lector (y conserva los dos hechos), y un
	# cierre que devuelve 1 se queda en 1. Las siete patas usan la misma.
	_frc=0
	olivares_overlay_measure_finish "$_live_rc" \
		"the captured current overlay main contradicts the C02 HOLD remasure: producer off main; blobs directory is the monolith prefix" || _frc=$?
	case "$_frc" in
	2) cannot "$OLIVARES_OVERLAY_FINAL_WHY" ;;
	1) fail "$OLIVARES_OVERLAY_FINAL_WHY" ;;
	esac
else
	cannot "OLIVARES_ENT_DIR does not resolve to a git repo"
fi

say "check-c02-hold-key-until-producer: CLEAN — HOLD; hub 4-arg on main; producer off overlay main."
exit 0
