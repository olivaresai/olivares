#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail

_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# La bateria copia an internal design note (not shipped) a su arbol de fixtures, y el
# export lo cura fuera: en el arbol publico moria con `cp: cannot stat`, rc 1. Misma cura y
# mismo clasificador que la pata (check-int-12-no-land.sh), por la misma razon.
if [ ! -r "$ROOT/design/INT-12-NO-LAND-ENT58-2026-08-19.md" ] \
   && [ "$(bash "$ROOT/scripts/hub-leg.sh" --classify --root "$ROOT" 2>/dev/null)" = "public" ]; then
  printf '%s\n' "test-int-12-no-land: SCOPED — public export; the INT-12 acta is curated out."
  exit 0
fi
CHECK="$ROOT/scripts/check-int-12-no-land.sh"
# El acto de ESTA corrida: el sello y el sujeto tienen que compartirlo o no hay frescura.
OLIVARES_ACT_ID="${OLIVARES_ACT_ID:-int12-selftest-$$}"
export OLIVARES_ACT_ID
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/int-12.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

ACTA="design/INT-12-NO-LAND-ENT58-2026-08-19.md"

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts"
  cp "$ROOT/$ACTA" "$TMP/tree/design/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-int-12-no-land.sh"
  # ⛔ LAS DEPENDENCIAS DEL SUJETO VIAJAN CON EL. El fixture copiaba el guion y el documento y
  #    nada mas, asi que cuando el checker gano `lib/overlay-seal.sh` (a repository gate, 2026-08-29) el
  #    caso «configured overlay should be checked CLEAN» empezo a morir con «No such file or
  #    directory» — y el fallo NO se lee como lo que es: parece del overlay, no del banco.
  #    Medido sobre main el 2026-09-02: 6 passed, 1 failed, y `lint:int-12-no-land` rc 2.
  #    Una lista de ficheros escrita a mano no sigue al sujeto: se deriva de lo que el sujeto
  #    SOURCEA, y si falta se dice en voz alta en vez de dejar que reviente dentro del caso.
  mkdir -p "$TMP/tree/scripts/lib"
  while IFS= read -r _dep; do
    [ -n "$_dep" ] || continue
    if [ ! -r "$ROOT/scripts/lib/$_dep" ]; then
      echo "test-int-12-no-land: NO HE PODIDO MIRAR: el sujeto sourcea scripts/lib/$_dep y no existe" >&2
      exit 2
    fi
    cp "$ROOT/scripts/lib/$_dep" "$TMP/tree/scripts/lib/"
  done <<DEPS
$(grep -oE 'scripts/lib/[a-z0-9._-]+\.sh' "$CHECK" | sed 's#.*/##' | sort -u)
DEPS
}

# ⛔ EL ACTA VIVA YA DICE `closed`, ASI QUE EL CAMINO ABIERTO NECESITA SU PROPIO FIXTURE O DEJA DE
#    MEDIRSE. El 2026-09-16 `#58` se cerro sin mergear y el acta gano `int-12-pr-state`. Si las
#    filas de agosto siguieran montando el acta viva, TODAS pasarian a ejercer el camino cerrado
#    y el camino abierto se quedaria sin banco el mismo dia en que se promete no tocarlo. Las
#    tres formas del acta se montan explicitamente y cada fila dice cual monta.
mutar() { python3 - "$TMP/tree/design/$(basename "$ACTA")" "$1" "$2" <<'PY'
import sys
p, viejo, nuevo = sys.argv[1], sys.argv[2], sys.argv[3]
t = open(p, encoding="utf-8").read()
if viejo not in t:
    print(f"test-int-12-no-land: NO HE PODIDO MIRAR: el acta no contiene {viejo!r}", file=sys.stderr)
    sys.exit(2)
open(p, "w", encoding="utf-8").write(t.replace(viejo, nuevo))
PY
}

stage_abierto() { stage; mutar 'int-12-pr-state: closed' 'int-12-pr-state: open'; }
stage_sin_estado() { stage; mutar 'int-12-pr-state: closed
' ''; }

run() {
  local ent_state="${1:-unset}"
  local allow_state="${2:-unset}"
  local rc=0
  (
    unset OLIVARES_ENT_DIR || true
    unset OLIVARES_INT12_ALLOW_NO_OVERLAY || true
    case "$ent_state" in
      unset) ;;
      empty) export OLIVARES_ENT_DIR="" ;;
      *) export OLIVARES_ENT_DIR="$ent_state" ;;
    esac
    if [ "$allow_state" != unset ]; then
      export OLIVARES_INT12_ALLOW_NO_OVERLAY="$allow_state"
    fi
    OLIVARES_ROOT="$TMP/tree" OLIVARES_HUB_GIT_DIR="$ROOT" OLIVARES_ACT_ID="$OLIVARES_ACT_ID" \
      bash "$TMP/tree/scripts/check-int-12-no-land.sh"
  ) >"$TMP/out" 2>"$TMP/err" || rc=$?
  printf '%s\n' "$rc" >"$TMP/rc"
  return 0
}

# fake_overlay [pin_de_main] [modo]
#   pin_de_main: gitlink `public` que lleva `origin/main` del clon falso. Por defecto, el del
#     acta — el mundo de agosto. `c6382f84…` reproduce el mundo del 2026-09-16T17:20:31Z, en el
#     que el pin SE MOVIO y el gancho mato todo push local.
#   modo: con58 (hay `refs/pull/58/head`, no aterrizado) · sin58 (el clon no trae el ref, que es
#     lo que hacen TODOS los clones de esta caja) · aterrizado (`refs/pull/58/head` es alcanzable
#     desde `origin/main`: #58 aterrizo, y eso es el hallazgo).
fake_overlay() {
  local pin_main="${1:-}" modo="${2:-con58}"
  local ovl_pin pin58 tree commit commit58
  rm -rf "$TMP/ent"
  git init -q "$TMP/ent"
  git -C "$TMP/ent" config user.name "INT-12 fixture"
  git -C "$TMP/ent" config user.email "int12@example.invalid"
  ovl_pin="$(sed -n 's/^overlay-main-pin: *//p' "$TMP/tree/design/$(basename "$ACTA")")"
  pin58="$(sed -n 's/^int-12-pin: *//p' "$TMP/tree/design/$(basename "$ACTA")")"
  [ -n "$pin_main" ] || pin_main="$ovl_pin"

  tree="$(printf '160000 commit %s\tpublic\n' "$pin58" | git -C "$TMP/ent" mktree --missing)"
  commit58="$(printf 'fixture pull 58\n' | git -C "$TMP/ent" commit-tree "$tree")"

  tree="$(printf '160000 commit %s\tpublic\n' "$pin_main" | git -C "$TMP/ent" mktree --missing)"
  if [ "$modo" = aterrizado ]; then
    commit="$(printf 'fixture overlay main\n' | git -C "$TMP/ent" commit-tree "$tree" -p "$commit58")"
  else
    commit="$(printf 'fixture overlay main\n' | git -C "$TMP/ent" commit-tree "$tree")"
  fi
  git -C "$TMP/ent" update-ref refs/remotes/origin/main "$commit"
  [ "$modo" = sin58 ] || git -C "$TMP/ent" update-ref refs/pull/58/head "$commit58"
  # ⛔ Y EL SELLO DE FRESCURA, que el sujeto exige desde a repository gate (2026-08-29). Sin el, el caso
  #    «configured overlay is checked and CLEAN» contesta 2 —«no overlay freshness seal»— y el
  #    banco lleva rojo desde ese dia sin que el mensaje diga que el que falta es EL BANCO.
  #    Forma, de scripts/lib/overlay-seal.sh:39 — `<epoch> <act-id> <sha40> rc=<n>`; el acto tiene
  #    que ser el MISMO que ve el sujeto (A-04: un sello de otro acto no vale por reciente que sea).
  printf '%s %s %s rc=0\n' "$(date -u +%s)" "$OLIVARES_ACT_ID" \
    "$(git -C "$TMP/ent" rev-parse refs/remotes/origin/main)" > "$TMP/tree/.overlay-fetch-seal"
}

# El pin que MOVIO el mundo: ent PR #116 lo dejo aqui el 2026-09-15 y el acta sigue registrando
# el de agosto. Es el objeto que hizo rojo el gancho a las 17:20:31Z del 2026-09-16.
PIN_MOVIDO=c6382f84362b6ef06feaf742708eb6598b088468

printf '\n--- camino ABIERTO (acta con int-12-pr-state: open) — los veredictos de agosto, intactos\n'

stage_abierto
run unset 1
if [ "$(cat "$TMP/rc")" = "0" ] &&
   grep -q 'NOTICE.*live overlay remasure skipped' "$TMP/out" &&
   grep -q 'CLEAN.*OLIVARES_INT12_ALLOW_NO_OVERLAY=1' "$TMP/out"; then
  ok "overlay-free opt-in is NOTICE plus CLEAN"
else bad "overlay-free opt-in should be explicit NOTICE+CLEAN ($(cat "$TMP/err") $(cat "$TMP/out"))"; fi

stage_abierto
run unset unset
if [ "$(cat "$TMP/rc")" = "2" ] &&
   grep -q 'COULD NOT LOOK.*OLIVARES_INT12_ALLOW_NO_OVERLAY=1' "$TMP/err"; then
  ok "missing overlay without opt-in is COULD NOT LOOK"
else bad "missing overlay without opt-in should fail closed ($(cat "$TMP/err"))"; fi

stage_abierto
run empty 1
if [ "$(cat "$TMP/rc")" = "2" ] && grep -q "COULD NOT LOOK.*OLIVARES_ENT_DIR=''" "$TMP/err"; then
  ok "explicit unresolved overlay is COULD NOT LOOK even with opt-in"
else bad "explicit unresolved overlay should be exit 2 ($(cat "$TMP/err"))"; fi

stage_abierto
run "$TMP/does-not-exist" 1
if [ "$(cat "$TMP/rc")" = "2" ] &&
   grep -q "COULD NOT LOOK.*OLIVARES_ENT_DIR='$TMP/does-not-exist'" "$TMP/err"; then
  ok "explicit missing overlay path is COULD NOT LOOK even with opt-in"
else bad "explicit missing overlay path should be exit 2 ($(cat "$TMP/err"))"; fi

stage_abierto
fake_overlay
run "$TMP/ent" unset
if [ "$(cat "$TMP/rc")" = "0" ] &&
   grep -q 'CLEAN.*land-as-is=no' "$TMP/out" &&
   ! grep -q 'NOTICE' "$TMP/out"; then
  ok "configured overlay is checked and CLEAN"
else bad "configured overlay should be checked CLEAN ($(cat "$TMP/err") $(cat "$TMP/out"))"; fi

# ⛔ LA SEMANTICA DEL CAMINO ABIERTO NO SE AFLOJA, Y ESTA FILA LO PRUEBA. Mientras `#58` este
#    ABIERTO, que el pin vivo se mueva SIGUE siendo un hallazgo: el acta dejo de describir el
#    mundo y alguien tiene que re-medirla. Lo que cambia con el cierre es el sujeto, no el rigor.
stage_abierto
fake_overlay "$PIN_MOVIDO"
run "$TMP/ent" unset
if [ "$(cat "$TMP/rc")" = "1" ] &&
   grep -q "live overlay-main public pin $PIN_MOVIDO != measured" "$TMP/err"; then
  ok "open path still refuses a moved live pin"
else bad "open path must keep refusing a moved live pin rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage_abierto
rm -f "$TMP/tree/design/$(basename "$ACTA")"
run unset 1
if [ "$(cat "$TMP/rc")" = "2" ] && grep -q 'COULD NOT LOOK' "$TMP/err"; then
  ok "missing measure is COULD NOT LOOK"
else bad "missing measure should be exit 2 ($(cat "$TMP/err"))"; fi

stage_abierto
mutar 'int-12-land-as-is: no' 'int-12-land-as-is: yes'
run unset 1
if [ "$(cat "$TMP/rc")" = "1" ]; then ok "mutant (land #58 as-is) is killed"
else bad "land-as-is yes stayed CLEAN rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage_abierto
mutar 'allows-additional-active-idp-on-overlay-main: yes' 'allows-additional-active-idp-on-overlay-main: no'
run unset 1
if [ "$(cat "$TMP/rc")" = "1" ]; then ok "mutant (restore compile premise) is killed"
else bad "missing AllowsAdditionalActiveIdP stayed CLEAN rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage_abierto
python3 - "$TMP/tree/design/$(basename "$ACTA")" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read()
# Present the #58 pin as if it were hub main (no regression).
hub = None
for ln in t.splitlines():
    if ln.startswith("hub-main-sha:"):
        hub = ln.split(":",1)[1].strip()
t = t.replace("int-12-pin: 76221568428d8e4c882731d8660b787b63ea9826", f"int-12-pin: {hub}")
open(p, "w", encoding="utf-8").write(t)
PY
run unset 1
if [ "$(cat "$TMP/rc")" = "1" ]; then ok "mutant (claim #58 pin is hub main) is killed"
else bad "current-pin claim stayed CLEAN rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage_abierto
mutar 'snapshot-on-overlay-main: deliberately-ungated' 'snapshot-on-overlay-main: gated'
run unset 1
if [ "$(cat "$TMP/rc")" = "1" ]; then ok "mutant (Snapshot already gated on overlay main) is killed"
else bad "Snapshot gated stayed CLEAN rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

printf '\n--- acta SIN int-12-pr-state — toda acta anterior al cierre toma el camino de agosto\n'

stage_sin_estado
fake_overlay
run "$TMP/ent" unset
if [ "$(cat "$TMP/rc")" = "0" ] && grep -q 'CLEAN.*land-as-is=no' "$TMP/out"; then
  ok "absent state takes the old path (CLEAN)"
else bad "absent state should take the old path rc=$(cat "$TMP/rc") ($(cat "$TMP/err") $(cat "$TMP/out"))"; fi

stage_sin_estado
fake_overlay "$PIN_MOVIDO"
run "$TMP/ent" unset
if [ "$(cat "$TMP/rc")" = "1" ] && grep -q 'live overlay-main public pin' "$TMP/err"; then
  ok "absent state keeps the old verdict on a moved pin"
else bad "absent state must keep the old verdict rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

printf '\n--- camino CERRADO (acta viva: #58 cerrado el 2026-09-16T11:58:36Z, sin merge)\n'

# ⛔ LA FILA QUE EXISTE POR LA AVERIA DEL 2026-09-16T17:20:31Z: pin vivo MOVIDO, clon SIN
#    `refs/pull/58/head` (como todos los de esta caja). Antes: rc 1 y todo push local muerto.
#    Ahora: 0, porque lo que se refuta es que #58 aterrizara, no que el pin siga donde estaba.
stage
fake_overlay "$PIN_MOVIDO" sin58
run "$TMP/ent" unset
if [ "$(cat "$TMP/rc")" = "0" ] &&
   grep -q 'CLEAN — ent#58 closed 2026-09-16T11:58:36Z, never merged; nothing to land' "$TMP/out" &&
   grep -q 'NOTICE.*refs/pull/58/head is not in this clone' "$TMP/out"; then
  ok "closed: a MOVED live pin is CLEAN and the line names the closure"
else bad "closed with a moved live pin should be CLEAN rc=$(cat "$TMP/rc") ($(cat "$TMP/err") $(cat "$TMP/out"))"; fi

stage
fake_overlay "$PIN_MOVIDO" con58
run "$TMP/ent" unset
if [ "$(cat "$TMP/rc")" = "0" ] &&
   grep -q 'CLEAN — ent#58 closed' "$TMP/out" &&
   ! grep -q 'NOTICE' "$TMP/out"; then
  ok "closed: with refs/pull/58/head present and not landed, CLEAN without NOTICE"
else bad "closed with the pull ref present should be CLEAN and silent rc=$(cat "$TMP/rc") ($(cat "$TMP/err") $(cat "$TMP/out"))"; fi

stage
run unset 1
if [ "$(cat "$TMP/rc")" = "0" ] &&
   grep -q 'NOTICE.*live overlay remasure skipped' "$TMP/out" &&
   grep -q 'CLEAN — ent#58 closed' "$TMP/out"; then
  ok "closed: overlay-free opt-in is NOTICE plus the closure CLEAN"
else bad "closed overlay-free opt-in should be NOTICE+closure CLEAN rc=$(cat "$TMP/rc") ($(cat "$TMP/err") $(cat "$TMP/out"))"; fi

stage
run unset unset
if [ "$(cat "$TMP/rc")" = "2" ] &&
   grep -q 'COULD NOT LOOK.*OLIVARES_INT12_ALLOW_NO_OVERLAY=1' "$TMP/err"; then
  ok "closed: a missing overlay without opt-in still fails closed"
else bad "closed without overlay and without opt-in should be exit 2 rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

# ⛔ EL HALLAZGO QUE ESTE DOCUMENTO EXISTE PARA NOMBRAR, primera forma: el gitlink `public` de
#    `origin/main` ES el pin de #58, o sea que el arbol de #58 esta puesto.
stage
fake_overlay 76221568428d8e4c882731d8660b787b63ea9826 sin58
run "$TMP/ent" unset
if [ "$(cat "$TMP/rc")" = "1" ] && grep -q "public gitlink IS the #58 pin" "$TMP/err"; then
  ok "closed: overlay main carrying the #58 pin is the finding"
else bad "closed with the #58 pin live should be rc 1 rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

# Segunda forma: la cabeza de #58 es alcanzable desde `origin/main` — aterrizo de verdad.
stage
fake_overlay "$PIN_MOVIDO" aterrizado
run "$TMP/ent" unset
if [ "$(cat "$TMP/rc")" = "1" ] && grep -q "is reachable from overlay origin/main — it landed" "$TMP/err"; then
  ok "closed: #58 head reachable from overlay main is the finding"
else bad "closed with #58 landed should be rc 1 rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
mutar 'int-12-pr-merged: no' 'int-12-pr-merged: yes'
run unset 1
if [ "$(cat "$TMP/rc")" = "1" ] && grep -q "a merged #58 means it LANDED" "$TMP/err"; then
  ok "closed but recorded merged is killed"
else bad "closed+merged should be rc 1 rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
mutar 'int-12-pr-closed-at: 2026-09-16T11:58:36Z' 'int-12-pr-closed-at: ayer-por-la-tarde'
run unset 1
if [ "$(cat "$TMP/rc")" = "1" ] && grep -q "is not an ISO-8601 UTC instant" "$TMP/err"; then
  ok "closed with a bogus timestamp is killed"
else bad "a bogus closed-at should be rc 1 rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
mutar 'int-12-pr-closed-at: 2026-09-16T11:58:36Z' 'int-12-pr-closed-at: 2026-02-30T11:58:36Z'
run unset 1
if [ "$(cat "$TMP/rc")" = "1" ] && grep -q "is not a real instant" "$TMP/err"; then
  ok "closed with a well-formed but impossible date is killed"
else bad "an impossible closed-at should be rc 1 rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
mutar 'int-12-pr-closed-at: 2026-09-16T11:58:36Z' 'int-12-pr-closed-at: 2099-01-01T00:00:00Z'
run unset 1
if [ "$(cat "$TMP/rc")" = "1" ] && grep -q "is in the future" "$TMP/err"; then
  ok "closed with a future timestamp is killed"
else bad "a future closed-at should be rc 1 rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
mutar 'int-12-pr-merged: no
' ''
run unset 1
if [ "$(cat "$TMP/rc")" = "1" ] && grep -q "int-12-pr-merged is missing" "$TMP/err"; then
  ok "closed without int-12-pr-merged is an incomplete record, not a verdict"
else bad "closed without merged should be rc 1 rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
mutar 'int-12-pr-closed-at: 2026-09-16T11:58:36Z
' ''
run unset 1
if [ "$(cat "$TMP/rc")" = "1" ] && grep -q "int-12-pr-closed-at is missing" "$TMP/err"; then
  ok "closed without an instant is killed"
else bad "closed without closed-at should be rc 1 rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
mutar 'int-12-closure-measured-by: r116-int12-gate-closure
' ''
run unset 1
if [ "$(cat "$TMP/rc")" = "1" ] && grep -q "unattributed" "$TMP/err"; then
  ok "closed without an author of the closure is killed"
else bad "closed without measured-by should be rc 1 rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
mutar 'int-12-pr-head-at-close: 1cc9f110d73420df4190a024f4b3da64b35d2aa8' \
      'int-12-pr-head-at-close: 0123456789012345678901234567890123456789'
run unset 1
if [ "$(cat "$TMP/rc")" = "1" ] && grep -q "describes another head" "$TMP/err"; then
  ok "closed at a head the measure does not describe is killed"
else bad "a foreign head-at-close should be rc 1 rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
mutar 'int-12-pr-state: closed' 'int-12-pr-state: draft'
run unset 1
if [ "$(cat "$TMP/rc")" = "1" ] && grep -q "int-12-pr-state is 'draft'" "$TMP/err"; then
  ok "an unknown state is refused BY NAME"
else bad "an unknown state should be rc 1 naming it rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

# El cierre no afloja lo estatico: los mutantes de agosto siguen muertos con el acta cerrada.
stage
mutar 'int-12-land-as-is: no' 'int-12-land-as-is: yes'
run unset 1
if [ "$(cat "$TMP/rc")" = "1" ]; then ok "closed: mutant (land #58 as-is) is still killed"
else bad "closed+land-as-is yes stayed CLEAN rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
mutar 'snapshot-on-overlay-main: deliberately-ungated' 'snapshot-on-overlay-main: gated'
run unset 1
if [ "$(cat "$TMP/rc")" = "1" ]; then ok "closed: mutant (Snapshot already gated) is still killed"
else bad "closed+Snapshot gated stayed CLEAN rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

printf '\ncheck-int-12-no-land selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
