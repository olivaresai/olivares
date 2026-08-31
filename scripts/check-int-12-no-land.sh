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

ENT="${OLIVARES_ENT_DIR:-}"
ENT_EXPLICITO=1
if [ -z "$ENT" ]; then
  ENT_EXPLICITO=0
  # El nombre del clon hermano se LEE del doc que este guion ya lee, no se escribe
  # aqui: este fichero viaja al arbol publico y el doc no. El fallback se conserva
  # a proposito — quitarlo dejaria OLIVARES_ENT_DIR vacia y el guion pasaria a decir
  # NO PUDE MIRAR en vez de comprobar, que es peor que el literal.
  _sib="$(sed -n 's/^sibling-clone-dir: *//p' "$DOC" | head -1)"
  [ -n "$_sib" ] || cannot "$DOC lost sibling-clone-dir"
  ENT="$(CDPATH= cd -- "$ROOT/.." && pwd -P)/$_sib"
fi
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
export OLIVARES_HUB_GIT_DIR="${OLIVARES_HUB_GIT_DIR:-$ROOT}"

python3 - "$DOC" <<'PY'
import os, re, subprocess, sys

doc_path = sys.argv[1]
text = open(doc_path, encoding="utf-8").read()

def kv(key):
    m = re.search(rf"^{re.escape(key)}:\s*(\S+)\s*$", text, flags=re.M)
    if not m:
        print(f"measure lost {key}", file=sys.stderr)
        sys.exit(2)
    return m.group(1)

land = kv("int-12-land-as-is")
if land != "no":
    print("int-12-land-as-is is not no — landing ent#58 as-is is the finding", file=sys.stderr)
    sys.exit(1)
if kv("int-12-pr") != "58":
    print("int-12-pr is not 58", file=sys.stderr)
    sys.exit(1)

def sha(key):
    v = kv(key)
    if not re.fullmatch(r"[0-9a-f]{40}", v):
        print(f"{key} is not a 40-hex object id: {v!r}", file=sys.stderr)
        sys.exit(1)
    return v

head = sha("int-12-head")
pin58 = sha("int-12-pin")
ovl_pin = sha("overlay-main-pin")
ovl_sha = sha("overlay-main-sha")
hub = sha("hub-main-sha")

if pin58 == ovl_pin:
    print("int-12-pin equals overlay-main-pin — as-is would not move the gitlink; measure is stale or the refuse is unexplained", file=sys.stderr)
    sys.exit(1)
if pin58 == hub:
    print("int-12-pin equals hub-main-sha — #58 is not a current pin", file=sys.stderr)
    sys.exit(1)

if kv("allows-additional-active-idp-on-overlay-main") != "yes":
    print("compile premise restored: overlay main already has AllowsAdditionalActiveIdP", file=sys.stderr)
    sys.exit(1)
if kv("snapshot-on-overlay-main") != "deliberately-ungated":
    print("overlay main Snapshot posture lost — #58 gates it; as-is would revert doctrine", file=sys.stderr)
    sys.exit(1)

try:
    behind58 = int(kv("int-12-pin-behind-hub"))
    behind_ovl = int(kv("overlay-main-pin-behind-hub"))
    behind_pair = int(kv("int-12-pin-behind-overlay-main-pin"))
except ValueError:
    print("a behind-* count is not an integer", file=sys.stderr)
    sys.exit(1)
if behind58 <= behind_ovl:
    print(
        f"int-12 pin behind hub ({behind58}) is not greater than overlay-main pin "
        f"({behind_ovl}) — as-is would not regress the gitlink",
        file=sys.stderr,
    )
    sys.exit(1)
if behind_pair <= 0:
    print("int-12-pin-behind-overlay-main-pin must be >0 (regression distance)", file=sys.stderr)
    sys.exit(1)

if not re.search(r"(?i)no se mergea|no se aterriza|no-land|land-as-is: no", text):
    print("document no longer says #58 must not land as-is", file=sys.stderr)
    sys.exit(1)

def git(*args, cwd):
    p = subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True)
    return p.returncode, p.stdout, p.stderr

hub_dir = os.environ.get("OLIVARES_HUB_GIT_DIR", ".")
rc, out, _ = git("rev-list", "--count", f"{pin58}..{ovl_pin}", cwd=hub_dir)
if rc != 0:
    print("could not count int-12-pin..overlay-main-pin on the hub", file=sys.stderr)
    sys.exit(2)
live_pair = int(out.strip() or "0")
if live_pair != behind_pair:
    print(
        f"live pin-behind-overlay-main-pin {live_pair} != measured {behind_pair}",
        file=sys.stderr,
    )
    sys.exit(1)

rc, _, _ = git("merge-base", "--is-ancestor", pin58, ovl_pin, cwd=hub_dir)
if rc != 0:
    print("int-12-pin is not an ancestor of overlay-main-pin — direction of regression lost", file=sys.stderr)
    sys.exit(1)

ent = os.environ.get("OLIVARES_ENT_DIR_RESOLVED", "")
explicit = os.environ.get("OLIVARES_ENT_EXPLICITO", "0") == "1"

def gitlink(repo, spec):
    rc, out, _ = git("ls-tree", spec, "--", "public", cwd=repo)
    if rc != 0 or not out.strip():
        return None
    parts = out.split()
    # <mode> commit <sha>\tpublic
    if len(parts) >= 3 and parts[1] == "commit":
        return parts[2][:40]
    return None

def notice_and_clean(why):
    print(f"check-int-12-no-land: NOTICE — live overlay remasure skipped: {why}")
    print("check-int-12-no-land: CLEAN — as-is is refused; pin would regress.")
    sys.exit(0)

if not ent or not os.path.exists(ent):
    if explicit:
        print(f"OLIVARES_ENT_DIR={ent!r} does not exist", file=sys.stderr)
        sys.exit(2)
    notice_and_clean(f"no overlay repo at {ent}")

rc, _, _ = git("rev-parse", "--git-dir", cwd=ent)
if rc != 0:
    if explicit:
        print(f"OLIVARES_ENT_DIR={ent} is not a git repo", file=sys.stderr)
        sys.exit(2)
    notice_and_clean("overlay path is not a git repo")

live_ovl = gitlink(ent, "origin/main")
live_58 = gitlink(ent, "refs/pull/58/head")
if live_ovl is None:
    live_ovl = gitlink(ent, "HEAD")
if live_ovl is None:
    if explicit:
        print("could not read overlay public gitlink", file=sys.stderr)
        sys.exit(2)
    notice_and_clean("no public gitlink on overlay origin/main")

if live_ovl != ovl_pin:
    print(f"live overlay-main public pin {live_ovl} != measured {ovl_pin}", file=sys.stderr)
    sys.exit(1)
if live_58 is not None and live_58 != pin58:
    print(f"live #58 public pin {live_58} != measured {pin58}", file=sys.stderr)
    sys.exit(1)

print(
    f"check-int-12-no-land: CLEAN — land-as-is=no; pin {pin58[:12]} is "
    f"{behind_pair} behind overlay-main pin {ovl_pin[:12]}; "
    f"AllowsAdditionalActiveIdP already on overlay main"
)
sys.exit(0)
PY
