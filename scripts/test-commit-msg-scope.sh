#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
#
# test-commit-msg-scope.sh — el SCOPE del asunto, y por que esta bateria existe.
#
# ⛔ EL DEFECTO QUE CIERRA, medido y no hipotetico. El `commit-msg` tiene DOS jueces: usa
#    `commitlint` si encuentra `node_modules/.bin/commitlint` (`:79`, `:83`) y si no cae a un regex
#    de respaldo (`:120`). Ese binario NO esta en el clon del hub, asi que el mismo mensaje pasa o no
#    **segun el directorio desde el que commitees**.
#
#    HUELLA EN `main`, re-medida el 2026-08-30 sobre 4000 commits: de **123 scopes distintos, el juez
#    estricto rechazaba 14** — `i18n` `a11y` `c05` `e2e` `e2e-visual` `cfg05` `s1008` `s1021`
#    `lote-docs2` `lote80c` y los cuatro `lot(e) r26-NN` con ESPACIO. Los ocho commits del 27-08
#    (`531c10ef3` … `8ff1dd3f3`) entraron por el juez permisivo.
#
# ⛔ POR QUE SE RELAJA COMMITLINT EN VEZ DE ENDURECER EL RESPALDO, con la medida delante. `kebab-case`
#    de commitlint **no admite digitos pegados a letras**, asi que rechaza `i18n`, `a11y`, `c05`,
#    `e2e` y `cfg05`, que son nombres BUENOS y usados. Medido ejecutando commitlint (no leyendolo)
#    sobre los 123 scopes reales:
#
#        scope-case: kebab-case ....  acepta 109 · rechaza 14
#        scope-case: lower-case ....  acepta 123 · rechaza  0
#
#    Alinear al reves —endurecer el respaldo a `[a-z0-9]+(-[a-z0-9]+)*` sin tocar commitlint— NO
#    junta a los dos jueces: los deja discrepando en esos catorce. Se probo esa via el 2026-08-30 y
#    la refuto la medida de arriba.
#
# ⛔ LOS DOS JUECES NO COINCIDEN EXACTAMENTE, Y SE DICE EN VEZ DE FINGIRLO. `lower-case` acepta `_`,
#    `.` y ESPACIO; este respaldo no. Coinciden en los 123 scopes que el repo usa salvo los cuatro
#    con espacio, donde el respaldo es MAS severo **a proposito**: son errores de dedo y que los pare
#    es mejor que que pasen. La coincidencia exacta exigiria UN SOLO juez, que es otra decision.
#
set -uo pipefail

HOOK="${HOOK_SRC:-.githooks/commit-msg}"
[ -r "$HOOK" ] || { echo "test-commit-msg-scope: 2 NO HE PODIDO MIRAR — no leo $HOOK" >&2; exit 2; }

WORK="$(mktemp -d "${TMPDIR:-/tmp}/commitmsg-scope.XXXXXX")" || {
  echo "test-commit-msg-scope: 2 NO HE PODIDO MIRAR — sin temporal" >&2; exit 2; }
# shellcheck disable=SC2064
trap "rm -rf '$WORK'" EXIT

pass=0; fail=0
ok()  { pass=$((pass+1)); printf '  ok    %-52s %s\n' "$1" "${2:-}"; }
bad() { fail=$((fail+1)); printf '  FAIL  %-52s %s\n' "$1" "${2:-}"; }

# Corre SOLO el respaldo: se fuerza sacando commitlint de la vista, porque lo que esta bateria
# gobierna es el respaldo. Que el otro juez exista o no es justamente el defecto de arriba.
run_respaldo() { # run_respaldo <mensaje> -> rc
  local m="$1" f="$WORK/msg"
  printf '%s\n' "$m" > "$f"
  ( cd "$WORK" && PATH="/usr/bin:/bin" bash "$OLDPWD/$HOOK" "$f" >/dev/null 2>&1 )
  printf '%s' "$?"
}

# ⛔ CONTROL POSITIVO, Y ESTA PRIMERO A PROPOSITO. Tres veces el 2026-08-28 lei el silencio de una
#    herramienta que NO habia corrido como un «acepta»: `npx --no-install` cancelo por paquete
#    ausente, y `--config` fuera del arbol hizo que commitlint crasheara sin juzgar. Si esta linea
#    no enrojece, ninguna de las de abajo significa nada.
if [ "$(run_respaldo 'noesuntipo(x): y')" != "0" ]; then
  ok "CONTROL POSITIVO: un tipo invalido es RECHAZADO" "(si esto pasara, el resto no valdria)"
else
  bad "CONTROL POSITIVO: un tipo invalido PASO" "la guarda no esta juzgando: el resto no vale"
fi

# Los que el repo USA — los dos primeros son commits reales de `main` que el juez estricto rechazaba.
for m in \
  'fix(a11y): the contrast-debt origin is the JSX element line' \
  'fix(e2e-visual): route the two egress-policy fixtures' \
  'chore(hooks): a scope of plain letters' \
  'feat(web-ui): a scope with one hyphen' \
  'test(license-worker): a scope with one hyphen' \
  'docs(session): the scope this repo uses for session records' \
  'chore(s-1026): digits after a hyphen' \
  'chore(s1026): digits glued to letters — passes by RULE, discouraged by CONVENTION'
do
  if [ "$(run_respaldo "$m")" = "0" ]; then ok "acepta: ${m%%:*}"; else bad "RECHAZA lo que el repo usa: ${m%%:*}"; fi
done

# Los que NO: mayuscula, y los tres donde el respaldo es MAS severo que `lower-case`, a proposito.
for m in \
  'chore(aB): uppercase in the scope' \
  'chore(a_b): underscore' \
  'chore(a.b): a dot' \
  'chore(a b): a space'
do
  if [ "$(run_respaldo "$m")" != "0" ]; then ok "rechaza: ${m%%:*}"; else bad "ACEPTA lo que no debe: ${m%%:*}"; fi
done

# Un asunto sin scope sigue siendo valido: la regla es sobre el scope, no sobre su presencia.
if [ "$(run_respaldo 'docs: a subject with no scope at all')" = "0" ]; then
  ok "un asunto SIN scope sigue pasando"
else
  bad "un asunto sin scope fue rechazado" "la regla es del scope, no de su presencia"
fi

printf '\ntest-commit-msg-scope: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" = "0" ]
