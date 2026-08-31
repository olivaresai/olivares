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

ENT="${OLIVARES_ENT_DIR:-}"
if [ -z "$ENT" ]; then
	say "check-c02-hold-key-until-producer: NOTICE — live overlay remasure skipped"
	say "check-c02-hold-key-until-producer: CLEAN — HOLD; hub 4-arg on main; producer off overlay main."
	exit 0
fi

if [ -d "$ENT" ] && git -C "$ENT" rev-parse --git-dir >/dev/null 2>&1; then
	# ⛔ a repository gate · LA FRESCURA SE EXIGE, NO SE SUPONE. El 2026-08-29 este mismo bloque comparo
	# contra un `origin/main` con un merge de retraso y dijo CLEAN: el clon TRAE por SSH (falla EN
	# SILENCIO sin clave) y EMPUJA por HTTPS. Un ref congelado no es un veredicto. El sello lo
	# escribe UNA pata por acto (`scripts/fetch-overlay-seal.sh`), y aqui se exige que sea de ESTE
	# acto y que describa ESTE clon; si no, se sale 2 — «no he podido mirar» —, nunca 0.
	. "$ROOT/scripts/lib/overlay-seal.sh" || cannot "cannot load scripts/lib/overlay-seal.sh"
	overlay_seal_require "$ENT" || cannot "$OVERLAY_SEAL_WHY"
	python3 - "$ENT" "$JSON" <<'PY' || fail "live overlay remasure diverged from the pin"
import json, subprocess, sys

ent, path = sys.argv[1], sys.argv[2]
data = json.load(open(path, encoding="utf-8"))
ref75 = "origin/hub-comercio/c02-producer-by-set"

def git(*args):
    p = subprocess.run(["git", "-C", ent, *args], capture_output=True, text=True)
    return p.returncode, p.stdout, p.stderr

rc, out, _ = git("rev-parse", "origin/main")
if rc != 0:
    raise SystemExit("could not resolve overlay origin/main")
if out.strip() != data["overlay_main_sha"]:
    raise SystemExit("live overlay main %s != pinned %s" % (out.strip(), data["overlay_main_sha"]))
rc, out, _ = git("rev-parse", ref75)
if rc != 0:
    raise SystemExit("could not resolve overlay PR 75")
if out.strip() != data["pr75_sha"]:
    raise SystemExit("live PR 75 %s != pinned %s" % (out.strip(), data["pr75_sha"]))
rc, out, _ = git("rev-list", "--count", "%s..origin/main" % ref75)
if rc != 0:
    raise SystemExit("could not count PR 75 behind overlay main")
behind = int((out or "0").strip() or "0")
if behind != data["pr75_behind_overlay_main"]:
    raise SystemExit("live PR 75 behind %d != pinned %d" % (behind, data["pr75_behind_overlay_main"]))
rc, out, _ = git("show", "origin/main:.goreleaser.yaml")
if rc != 0:
    raise SystemExit("could not read overlay goreleaser")
if 'directory: "enterprise/{{ .Version }}"' not in out:
    raise SystemExit("overlay main blobs directory is no longer the monolith prefix")
if "enterprise/{{ .Version }}/{{" in out:
    raise SystemExit("overlay main goreleaser now has a set axis in the blobs directory")
PY
else
	cannot "OLIVARES_ENT_DIR does not resolve to a git repo"
fi

say "check-c02-hold-key-until-producer: CLEAN — HOLD; hub 4-arg on main; producer off overlay main."
exit 0
