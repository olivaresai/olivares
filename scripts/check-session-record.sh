#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-session-record.sh — a branch that changes CODE must also add or update a file under
# `sessions/`. Without that record, a PR cannot be attributed to a lane, and attribution is what the
# whole claim protocol rests on.
#
# ⛔ WHY IT EXISTS, and the case is worth keeping because I got the diagnosis WRONG without it.
# Measured 2026-08-17: #824 and #853 both touch `cmd/olivares/` and conflict on five shared files.
# #824 carries `an internal design note (not shipped)`, which names its lane on its first line. #853 carries NO
# session file at all, so I had to infer its owner from its contents — and I inferred wrong, publishing
# to two mailboxes that TWO LANES had spent two days building the same bootstrap. They are SIBLING
# BRANCHES OF ONE LANE (merge-base `fb1be4a03`), and its relay had ordered exactly that. Refuted
# it from the tree.
#
# The wrong half of that finding cost a correction. The RIGHT half is this: a code PR with no session
# record is unattributable, and no instrument said so. Detecting "duplicated work" needs judgement;
# detecting "this branch changed Go and touched nothing under sessions/" is a cheap, total predicate —
# so that is what this checks, and it does not pretend to the other.
#
# THREE ANSWERS, like every gate here: 0 clean · 1 a branch with code and no record · 2 COULD NOT LOOK.
# Zero code files changed is CLEAN (a docs-only branch owes no record), but a missing trunk is 2, never
# 0: "I could not compare" is not "there is nothing to compare".
set -uo pipefail

say() { printf '%s\n' "$*"; }
cannot_look() { say "check-session-record: COULD NOT LOOK — $*" >&2; exit 2; }

command -v git >/dev/null 2>&1 || cannot_look "no git on PATH"
ROOT="${OLIVARES_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || true)}"
[ -n "$ROOT" ] || cannot_look "not inside a git work tree"
cd "$ROOT" || cannot_look "cannot enter $ROOT"

# The trunk is a parameter so the battery can drive throwaway repositories, and its ABSENCE is a
# refusal rather than an empty diff read as clean — the shape that made a sibling gate report CLEAN
# in CI where `actions/checkout` leaves no `origin/main` behind.
TRUNK="${OLIVARES_SESSION_TRUNK:-origin/main}"
git rev-parse --verify --quiet "$TRUNK" >/dev/null 2>&1 \
	|| cannot_look "no '$TRUNK' to compare against (actions/checkout without 'ref:' leaves none; repair: git fetch --no-tags origin main)"

BASE="$(git merge-base "$TRUNK" HEAD 2>/dev/null || true)"
[ -n "$BASE" ] || cannot_look "no merge base between HEAD and $TRUNK"

CHANGED="$(git diff --name-only "$BASE"...HEAD 2>/dev/null || true)"

# ⛔ UNA FICHA NUEVA DECLARA SU CARRIL, Y SE EXIGE SOLO A LAS QUE ESTE PUSH AÑADE.
#
# Por que existe: la propiedad de una rama se prueba en su ficha, nunca en el nombre del directorio
# de trabajo ni en su numero. Una limpieza automatica que uso el patron de la RUTA como prueba de
# propiedad retiro 65 arboles de trabajo, y quince no eran de quien los retiraba; tres estaban
# abiertos por otra sesion en ese momento. Y al reves: integrar el trabajo de otro no te hace su
# dueno. La linea `Carril:` es la unica prueba que no depende de adivinar.
#
# ⛔ SOLO LAS ANADIDAS (`--diff-filter=A`), y es deliberado: 46 de 65 fichas existentes no la
#    declaran. Exigirsela a todas convertiria este gate en una campana retroactiva que rompe cada
#    push de cualquier carril. Un trinquete frena lo NUEVO; no reescribe el pasado.
#
# ⛔ LA FORMA SE ACEPTA CON Y SIN NEGRITAS, y esa tolerancia no es laxitud: mi propia sonda buscaba
#    `**Carril:**` y era CIEGA a `Carril:` sin negritas, asi que dio por «no declaradas» cuatro
#    fichas que SI lo declaraban — y estuve a punto de estamparles mi carril encima. Un gate que
#    solo acepta una forma fabrica ese error en cada carril que lo lea.
NUEVAS="$(git diff --name-only --diff-filter=A "$BASE"...HEAD -- 'sessions/S*.md' 2>/dev/null || true)"
sin_carril=""
while IFS= read -r f; do
	[ -n "$f" ] || continue
	# ⛔ SIN TUBERIA, y no es estilo. `git show … | grep -q` bajo `pipefail` devuelve 141 EN EXITO:
	#    `grep -q` cierra al primer acierto, `git show` muere de SIGPIPE y el booleano se invierte
	#    justo cuando la linea SI esta. Lo cazo `lint:sigpipe-booleans` en el pre-vuelo de este
	#    mismo commit — el que anade un trinquete sobre el rigor ajeno.
	if ! grep -qE '^(\*\*)?Carril:(\*\*)?[[:space:]]+[^[:space:]]' <(git show "HEAD:$f" 2>/dev/null); then
		sin_carril="$sin_carril $f"
	fi
done <<EOF_NUEVAS
$NUEVAS
EOF_NUEVAS

if [ -n "$sin_carril" ]; then
	say "check-session-record: FICHA NUEVA SIN CARRIL —$sin_carril" >&2
	say "  Una ficha que no nombra su carril deja la rama sin dueno demostrable, y entonces la" >&2
	say "  propiedad se adivina por el nombre. Medido el 2026-08-27: de 65 worktrees retirados por" >&2
	say "  patron de ruta, QUINCE eran de otros carriles y TRES eran el cwd vivo de otra sesion." >&2
	say "  repair: anade al principio una linea 'Carril: <NOMBRE>' o '**Carril:** <NOMBRE>'," >&2
	say "  con el nombre tal cual firma ese carril en el buzon." >&2
	exit 1
fi
if [ -z "$CHANGED" ]; then
	say "check-session-record: CLEAN — this branch changes nothing against $TRUNK."
	exit 0
fi

# What counts as CODE. Deliberately a suffix list and not "everything that is not a doc": a new
# extension must be classified on purpose, and an unclassified one is ignored rather than silently
# charged. Generated console bundles are excluded — they are an artefact of building, not authorship.
#
# ⛔ AND A SUFFIX LIST CANNOT SEE A FILE THAT HAS NO SUFFIX, which left the highest-leverage file in
# the repository unattributable. Measured 2026-08-20 on two REAL runs, not on reasoning:
#
#   branch changing scripts/misspell-census.sh (a .sh)      → RED, "NO SESSION RECORD"   ← works
#   branch changing .githooks/pre-push and Taskfile.yml     → "CLEAN — none of them code"
#
# The hook decides WHICH GATES RUN FOR EVERY LANE, and the Taskfile defines what each of those gates
# actually executes. A branch that rewrites either of them shipped with no lane attached, which is
# precisely the state this checker exists to refuse — and the failure it was written from was an
# owner inferred wrong from contents.
#
# The stated reason above still holds and is not being overturned: it argues that a NEW EXTENSION
# must be classified on purpose. These two are not a new extension. One has no extension at all and
# the other has one (.yml) that must stay unclassified, because classifying .yml would sweep in every
# workflow, chart and fixture — the "everything that is not a doc" rule this file rejects. So they
# are named as PATHS, classified on purpose, which is what the rationale asks for.
code=0
record=0
while IFS= read -r f; do
	[ -n "$f" ] || continue
	case "$f" in
	sessions/*) record=1; continue ;;
	core/internal/webui/dist/*) continue ;;
	.githooks/* | Taskfile.yml) code=1 ;;
	*.go | *.ts | *.tsx | *.js | *.mjs | *.jsx | *.sql | *.sh | *.rs | *.py) code=1 ;;
	esac
done <<EOF_CHANGED
$CHANGED
EOF_CHANGED

n_code="$(printf '%s\n' "$CHANGED" | grep -c . || true)"

if [ "$code" -eq 0 ]; then
	say "check-session-record: CLEAN — $n_code file(s) changed, none of them code; a branch that ships no code owes no session record."
	exit 0
fi

if [ "$record" -eq 1 ]; then
	say "check-session-record: CLEAN — code changed and this branch adds or updates a file under sessions/."
	exit 0
fi

say "check-session-record: NO SESSION RECORD — this branch changes code and touches nothing under sessions/." >&2
say "  A PR with no session file cannot be attributed to a lane, and the claim protocol rests on" >&2
say "  attribution. Measured 2026-08-17: with one PR missing its record, the integrator inferred its" >&2
say "  owner from the contents and inferred WRONG, publishing a duplicated-work finding to two" >&2
say "  mailboxes that had to be retracted." >&2
say "  repair: add sessions/<SNNN>-<slug>.md or sessions/status/<SNNN>.md naming the lane on its first line." >&2
exit 1
