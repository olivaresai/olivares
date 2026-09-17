#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-int-12-no-land.sh — INT-12. ent#58 is OPEN/DRAFT. Landing it as-is
# moves the overlay public pin BACKWARDS (762215684 is an ancestor of the
# pin already on overlay main) and the compile premise (AllowsAdditionalActiveIdP
# missing) no longer holds. A measure that says land-as-is: yes is the hole.
#
# Three answers: 0 CLEAN · 1 finding · 2 could not look.
set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-int-12-no-land: FAIL — $*" >&2; exit 1; }
cannot() { say "check-int-12-no-land: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"
DOC="${OLIVARES_INT12_DOC:-design/INT-12-NO-LAND-ENT58-2026-08-19.md}"

# ⛔ EL EXPORT CURA $DOC FUERA, Y ESTE GUION SI VIAJA — la asimetria ya estaba escrita doce
# lineas mas abajo («este fichero viaja al arbol publico y el doc no»), resuelta para el nombre
# del clon hermano y NO para la ausencia del propio doc. Coste medido el 2026-08-31 por el
# pre-mortem de Codex sol max (ALTO-1): `mainline-ci` corre `lint:int-12-no-land` en el job
# hook-only-legs con `push: branches: [main]`, asi que el PRIMER push al repositorio publico
# contesta rc 2 y el paso lo propaga. Rojo determinista, no una carrera.
#
# Envolver el target en hub-leg.sh NO sirve, y esta medido antes de escribir esto: hub-leg
# clasifica por la ausencia del GUION, el guion SI esta en el export, asi que lo ejecuta y
# propaga su 2 igual. Lo que falta aqui es la ENTRADA, y esa distincion es la cura.
#
# Sin marcador la ausencia sigue siendo checkout roto y sigue siendo 2. Con el, 0 SCOPED
# nombrando lo que no se ha mirado. El clasificador es el de hub-leg.sh —firma del generador
# MAS ausencia de todo camino hub-only—, no un fichero-marcador suelto: la revision X-07 dejo
# escrito que un marcador a pelo es una contraseña que cualquier copia teclea.
if [ ! -r "$DOC" ] \
   && [ "$(bash "$ROOT/scripts/hub-leg.sh" --classify --root "$ROOT" 2>/dev/null)" = "public" ]; then
  say "check-int-12-no-land: SCOPED — public export; $DOC is curated out. INT-12 is a hub-side"
  say "  landing decision and there is nothing here to measure."
  exit 0
fi
[ -r "$DOC" ] || cannot "missing $DOC"
command -v python3 >/dev/null || cannot "no python3"
command -v git >/dev/null || cannot "git is not on PATH"

ENT=""
ENT_EXPLICITO=0
if [ "${OLIVARES_ENT_DIR+x}" = x ]; then
  ENT="$OLIVARES_ENT_DIR"
  ENT_EXPLICITO=1
else
  # El nombre del clon hermano se LEE del doc que este guion ya lee, no se escribe
  # aqui: este fichero viaja al arbol publico y el doc no.
  _sib="$(sed -n 's/^sibling-clone-dir: *//p' "$DOC" | head -1)"
  [ -n "$_sib" ] || cannot "$DOC lost sibling-clone-dir"
  ENT="$(CDPATH= cd -- "$ROOT/.." && pwd -P)/$_sib"
fi
ALLOW_NO_OVERLAY="${OLIVARES_INT12_ALLOW_NO_OVERLAY:-0}"
case "$ALLOW_NO_OVERLAY" in
  0|1) ;;
  *) cannot "OLIVARES_INT12_ALLOW_NO_OVERLAY must be 0 or 1" ;;
esac
export OLIVARES_ENT_DIR_RESOLVED="$ENT"
if [ -d "$ENT" ] && git -C "$ENT" rev-parse --git-dir >/dev/null 2>&1; then
	# ⛔ a repository gate · LA FRESCURA SE EXIGE, NO SE SUPONE. El 2026-08-29 los checkers compararon
	# contra un `origin/main` con un merge de retraso y dijeron CLEAN: el clon TRAE por SSH (falla
	# EN SILENCIO sin clave) y EMPUJA por HTTPS. Un ref congelado no es un veredicto. El sello lo
	# escribe UNA pata por acto (`scripts/fetch-overlay-seal.sh`); aqui se exige que sea de ESTE
	# acto y que describa ESTE clon, y si no se sale 2 — «no he podido mirar» —, nunca 0.
	. "$ROOT/scripts/lib/overlay-seal.sh" || cannot "cannot load scripts/lib/overlay-seal.sh"
	overlay_seal_require "$ENT" || cannot "$OVERLAY_SEAL_WHY"
	# ⚠ Solo cuando el clon EXISTE: este guion tiene un tercer estado con nombre —sin clon
	# hermano dice NOTICE y sale CLEAN— y exigirle frescura a un overlay ausente convertiria
	# ese estado en un 2 que no le corresponde.
fi
export OLIVARES_ENT_EXPLICITO="$ENT_EXPLICITO"
export OLIVARES_INT12_ALLOW_NO_OVERLAY_RESOLVED="$ALLOW_NO_OVERLAY"
export OLIVARES_HUB_GIT_DIR="${OLIVARES_HUB_GIT_DIR:-$ROOT}"

python3 - "$DOC" <<'PY'
import datetime, os, re, subprocess, sys

doc_path = sys.argv[1]
text = open(doc_path, encoding="utf-8").read()

def cannot(message):
    print(f"check-int-12-no-land: COULD NOT LOOK — {message}", file=sys.stderr)
    sys.exit(2)

def finding(message):
    print(message, file=sys.stderr)
    sys.exit(1)

def kv(key):
    m = re.search(rf"^{re.escape(key)}:\s*(\S+)\s*$", text, flags=re.M)
    if not m:
        cannot(f"measure lost {key}")
    return m.group(1)

def kv_opcional(key):
    # ⛔ UN CAMPO QUE PUEDE NO ESTAR NO ES UN CAMPO PERDIDO. `kv()` sale 2 cuando falta, y eso
    # es correcto para la medida de agosto; para los campos del CIERRE la ausencia TIENE
    # significado (un acta anterior a este cierre) y no puede leerse como «no he podido mirar».
    m = re.search(rf"^{re.escape(key)}:\s*(\S+)\s*$", text, flags=re.M)
    return m.group(1) if m else None

land = kv("int-12-land-as-is")
if land != "no":
    finding("int-12-land-as-is is not no — landing ent#58 as-is is the finding")
if kv("int-12-pr") != "58":
    finding("int-12-pr is not 58")

def sha(key):
    v = kv(key)
    if not re.fullmatch(r"[0-9a-f]{40}", v):
        finding(f"{key} is not a 40-hex object id: {v!r}")
    return v

head = sha("int-12-head")
pin58 = sha("int-12-pin")
ovl_pin = sha("overlay-main-pin")
ovl_sha = sha("overlay-main-sha")
hub = sha("hub-main-sha")

if pin58 == ovl_pin:
    finding("int-12-pin equals overlay-main-pin — as-is would not move the gitlink; measure is stale or the refuse is unexplained")
if pin58 == hub:
    finding("int-12-pin equals hub-main-sha — #58 is not a current pin")

if kv("allows-additional-active-idp-on-overlay-main") != "yes":
    finding("compile premise restored: overlay main already has AllowsAdditionalActiveIdP")
if kv("snapshot-on-overlay-main") != "deliberately-ungated":
    finding("overlay main Snapshot posture lost — #58 gates it; as-is would revert doctrine")

try:
    behind58 = int(kv("int-12-pin-behind-hub"))
    behind_ovl = int(kv("overlay-main-pin-behind-hub"))
    behind_pair = int(kv("int-12-pin-behind-overlay-main-pin"))
except ValueError:
    finding("a behind-* count is not an integer")
if behind58 <= behind_ovl:
    finding(
        f"int-12 pin behind hub ({behind58}) is not greater than overlay-main pin "
        f"({behind_ovl}) — as-is would not regress the gitlink"
    )
if behind_pair <= 0:
    finding("int-12-pin-behind-overlay-main-pin must be >0 (regression distance)")

if not re.search(r"(?i)no se mergea|no se aterriza|no-land|land-as-is: no", text):
    finding("document no longer says #58 must not land as-is")

# ⛔ EL ESTADO TERMINAL DEL SUJETO, Y POR QUE ES UNA ENTRADA Y NO UNA CONSTANTE. Hasta el
# 2026-09-16 este guion sabia comparar un REGISTRO con un objeto VIVO y no sabia que el sujeto
# pudiera ACABARSE. `#58` se cerro sin mergear a las 11:58:36Z de ese dia, y un borrador cerrado
# no se aterriza tal cual: ni por `gh pr merge` —GitHub lo rehusa— ni por descuido. Lo que el
# gate necesitaba no era otra re-medida del pin: era que el acta pudiera DECIR que el mundo se
# movio. Tres respuestas y ninguna nueva:
#   `open` (o el campo AUSENTE, que es toda acta anterior a hoy) -> el camino de agosto, INTACTO.
#   `closed` -> se comprueba el cierre y que el arbol de #58 NO haya aterrizado.
#   cualquier otro valor -> 1 NOMBRANDO el valor, porque un gate que adivina no mide.
ESTADO = kv_opcional("int-12-pr-state")
if ESTADO is not None and ESTADO not in ("open", "closed"):
    finding(
        f"int-12-pr-state is {ESTADO!r} — this gate knows 'open' and 'closed' and refuses to guess"
    )
CERRADO = ESTADO == "closed"
CERRADO_EN = None

if CERRADO:
    # ⛔ EL LADO ESTATICO DEL CIERRE: un registro de cierre INCOMPLETO o INCOHERENTE es peor que
    # uno rancio —lo dijo la re-medida del 2026-08-30 sobre los seis campos acoplados— porque se
    # lee como un veredicto. Faltar un campo del cierre NO es «no he podido mirar»: el acta esta
    # aqui y se lee entera; lo que falla es lo que DICE. Por eso son hallazgos (1), no 2.
    merged = kv_opcional("int-12-pr-merged")
    CERRADO_EN = kv_opcional("int-12-pr-closed-at")
    quien = kv_opcional("int-12-closure-measured-by")
    if merged is None:
        finding(
            "int-12-pr-state is closed and int-12-pr-merged is missing — an incomplete closure "
            "record is not a verdict"
        )
    if merged != "no":
        finding(
            f"int-12-pr-merged is {merged!r} — a merged #58 means it LANDED, which is the finding "
            "this gate exists to name"
        )
    if CERRADO_EN is None:
        finding(
            "int-12-pr-state is closed and int-12-pr-closed-at is missing — a closure without an "
            "instant cannot be checked"
        )
    if not re.fullmatch(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z", CERRADO_EN):
        finding(
            f"int-12-pr-closed-at is not an ISO-8601 UTC instant (YYYY-MM-DDTHH:MM:SSZ): {CERRADO_EN!r}"
        )
    try:
        cuando = datetime.datetime.strptime(CERRADO_EN, "%Y-%m-%dT%H:%M:%SZ").replace(
            tzinfo=datetime.timezone.utc
        )
    except ValueError:
        finding(f"int-12-pr-closed-at is not a real instant: {CERRADO_EN!r}")
    ahora = datetime.datetime.now(datetime.timezone.utc)
    if cuando > ahora:
        finding(
            f"int-12-pr-closed-at {CERRADO_EN!r} is in the future (now "
            f"{ahora.strftime('%Y-%m-%dT%H:%M:%SZ')}) — a closure that has not happened yet is not a closure"
        )
    if quien is None:
        finding(
            "int-12-pr-state is closed and int-12-closure-measured-by is missing — an unattributed "
            "closure is not a measure"
        )
    cabeza_al_cierre = kv_opcional("int-12-pr-head-at-close")
    if cabeza_al_cierre is not None and cabeza_al_cierre != head:
        finding(
            f"int-12-pr-head-at-close {cabeza_al_cierre!r} != int-12-head {head!r} — the closure "
            "record describes another head than the measure"
        )

def git(*args, cwd):
    p = subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True)
    return p.returncode, p.stdout, p.stderr

hub_dir = os.environ.get("OLIVARES_HUB_GIT_DIR", ".")
rc, out, _ = git("rev-list", "--count", f"{pin58}..{ovl_pin}", cwd=hub_dir)
if rc != 0:
    cannot("could not count int-12-pin..overlay-main-pin on the hub")
live_pair = int(out.strip() or "0")
if live_pair != behind_pair:
    finding(f"live pin-behind-overlay-main-pin {live_pair} != measured {behind_pair}")

rc, _, _ = git("merge-base", "--is-ancestor", pin58, ovl_pin, cwd=hub_dir)
if rc != 0:
    finding("int-12-pin is not an ancestor of overlay-main-pin — direction of regression lost")

ent = os.environ.get("OLIVARES_ENT_DIR_RESOLVED", "")
explicit = os.environ.get("OLIVARES_ENT_EXPLICITO", "0") == "1"
allow_no_overlay = (
    os.environ.get("OLIVARES_INT12_ALLOW_NO_OVERLAY_RESOLVED", "0") == "1"
)

def gitlink(repo, spec):
    rc, out, _ = git("ls-tree", spec, "--", "public", cwd=repo)
    if rc != 0 or not out.strip():
        return None
    parts = out.split()
    # <mode> commit <sha>\tpublic
    if len(parts) >= 3 and parts[1] == "commit":
        return parts[2][:40]
    return None

def resuelve(repo, spec):
    rc, out, _ = git("rev-parse", "--verify", "--quiet", f"{spec}^{{commit}}", cwd=repo)
    if rc != 0 or not out.strip():
        return None
    return out.strip()

def es_ancestro(repo, viejo, nuevo):
    rc, _, _ = git("merge-base", "--is-ancestor", viejo, nuevo, cwd=repo)
    return rc == 0

def linea_medida():
    # ⛔ ESTA LINEA ES EVIDENCIA Y ADEMAS ES UN CONTRATO CON CI. Lo primero: la distancia de
    # regresion se cuenta sobre el HUB entre dos objetos FIJOS (`git rev-list --count`, arriba),
    # asi que sigue siendo una medida de ESTE acto y no una cita del acta. Lo segundo:
    # `mainline-ci.yml` (paso `leg-int-12`) decide QUE MITAD se midio grepeando la salida, y
    # `behind overlay-main pin` es su tercera rama —«las DOS mitades medidas»—. Si el camino del
    # cierre no la imprimiera, ese paso caeria a su `else` y saldria 2 el dia que CI gane un clon
    # hermano. El orden de sus ramas hace el resto: el NOTICE de omision se comprueba ANTES, asi
    # que un runner sin overlay se sigue leyendo como «solo el lado estatico». Grep de contraste:
    #   grep -n 'behind overlay-main pin' .github/workflows/mainline-ci.yml
    print(
        f"check-int-12-no-land: MEASURE — 2026-08-30 record, re-verified on this hub: #58 pin "
        f"{pin58[:12]} is {behind_pair} behind overlay-main pin {ovl_pin[:12]}"
    )

def clean_cerrado():
    linea_medida()
    print(
        f"check-int-12-no-land: CLEAN — ent#58 closed {CERRADO_EN}, never merged; nothing to land"
    )
    sys.exit(0)

def absent_overlay(why):
    if explicit:
        cannot(f"OLIVARES_ENT_DIR={ent!r}: {why}")
    if not allow_no_overlay:
        cannot(
            f"{why}; an overlay-free runner must opt in with "
            "OLIVARES_INT12_ALLOW_NO_OVERLAY=1"
        )
    print(f"check-int-12-no-land: NOTICE — live overlay remasure skipped: {why}")
    if CERRADO:
        print(
            "check-int-12-no-land: NOTICE — overlay absence was explicitly allowed by "
            "OLIVARES_INT12_ALLOW_NO_OVERLAY=1; static closure evidence is intact."
        )
        clean_cerrado()
    print(
        "check-int-12-no-land: CLEAN — overlay absence was explicitly allowed by "
        "OLIVARES_INT12_ALLOW_NO_OVERLAY=1; static no-land evidence is intact."
    )
    sys.exit(0)

if not ent or not os.path.exists(ent):
    absent_overlay(f"no overlay repo at {ent!r}")

rc, _, _ = git("rev-parse", "--git-dir", cwd=ent)
if rc != 0:
    cannot(f"overlay path {ent!r} is not a git repo")

ref_main = "origin/main"
live_ovl = gitlink(ent, ref_main)
live_58 = gitlink(ent, "refs/pull/58/head")
if live_ovl is None:
    ref_main = "HEAD"
    live_ovl = gitlink(ent, ref_main)
if live_ovl is None:
    cannot(f"could not read the public gitlink from overlay repo {ent!r}")

if CERRADO:
    # ⛔ LA MITAD VIVA DEL CAMINO CERRADO NO PREGUNTA POR LA IDENTIDAD DEL PIN, Y ESA ES LA CURA.
    # El pin de `public` del overlay se mueve en CADA re-pin (32d53da9 -> c6382f84 el 2026-09-15,
    # y seguira), asi que exigir que sea el del acta convierte cada re-pin en un rojo del gancho
    # que CI —sin clon hermano— no ve. Lo que hay que refutar no es «el pin es otro»: es
    # «#58 aterrizo tal cual», y eso se mira con dos señales que ningun re-pin mueve.
    if live_ovl == pin58:
        finding(
            f"overlay main's public gitlink IS the #58 pin {pin58[:12]} — ent#58 landed as-is "
            "despite the closure record"
        )
    cabeza58 = resuelve(ent, "refs/pull/58/head")
    if cabeza58 is None:
        # ⚠ TERCERA RESPUESTA DENTRO DE LA MITAD VIVA, Y NO UN ROJO. Ningun clon de esta caja trae
        # `refs/pull/*` (no esta en su refspec), y un clon superficial tampoco. No haber podido
        # mirar media medida no es un hallazgo: se dice, se nombra lo que SI se midio, y se sigue.
        print(
            "check-int-12-no-land: NOTICE — refs/pull/58/head is not in this clone: the ancestry "
            "cross-check is unavailable; the public-gitlink comparison did run"
        )
    else:
        if es_ancestro(ent, cabeza58, ref_main):
            finding(
                f"ent#58 head {cabeza58[:12]} is reachable from overlay {ref_main} — it landed"
            )
        if live_58 is not None and live_58 != pin58:
            finding(f"live #58 public pin {live_58} != measured {pin58}")
    clean_cerrado()

if live_ovl != ovl_pin:
    finding(f"live overlay-main public pin {live_ovl} != measured {ovl_pin}")
if live_58 is not None and live_58 != pin58:
    finding(f"live #58 public pin {live_58} != measured {pin58}")

print(
    f"check-int-12-no-land: CLEAN — land-as-is=no; pin {pin58[:12]} is "
    f"{behind_pair} behind overlay-main pin {ovl_pin[:12]}; "
    f"AllowsAdditionalActiveIdP already on overlay main"
)
sys.exit(0)
PY
