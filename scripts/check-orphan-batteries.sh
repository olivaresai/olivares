#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# ¿Hay baterias que NADIE ejecuta?
#
# ⛔ POR QUE EXISTE, con la medida que lo pide: el 2026-08-30 aparecieron CUATRO baterias huerfanas
# en TRES sesiones distintas, y las cuatro las encontro un LECTOR humano, una por una:
#   · `test-claim-safety.sh` (antes de que r26 la cableara)
#   · la del publicador (the planner)
#   · `test-claim-lector*.sh`
#   · `test-classify-paths-parity.sh` — y esta es la que duele: era el testigo escrito
#     para que un invariante no derivara, y aterrizo con el defecto que existia para cazar.
#
# **Un banco que nadie corre no corta — y ademas informa VERDE mientras no corta.** Esa es la
# clase entera: no falla, no avisa, y su 17/0 se lee como cobertura.
#
# El precedente de la forma es `check-gate-parity.sh`, que hace lo mismo para patas del gancho
# frente al CI. Esto lo hace para BATERIAS frente a quien las invoca.
#
# ⛔ Y LA AUTORIDAD ES EL GRAFO, NO EL TEXTO. Una bateria puede no aparecer en el gancho y aun asi
# correr, porque su tarea cuelga de otra por `deps:`. Preguntarselo a `grep` contesta «¿esta
# escrito?»; preguntarselo a `task --dry` contesta «¿se ejecuta?», que es la pregunta.
#
# Veredictos: 0 = ninguna huerfana · 1 = hay huerfanas (se nombran con su causa) · 2 = NO HE PODIDO
# MIRAR. Nunca 0 por silencio.
set -u
RAIZ="${OLIVARES_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || echo "")}"
[ -n "$RAIZ" ] || { echo "check-orphan-batteries: NO HE PODIDO MIRAR: no estoy en un repositorio." >&2; exit 2; }
cd "$RAIZ" || { echo "check-orphan-batteries: NO HE PODIDO MIRAR: no entro en $RAIZ." >&2; exit 2; }
command -v task >/dev/null 2>&1 || { echo "check-orphan-batteries: NO HE PODIDO MIRAR: sin go-task." >&2; exit 2; }
TF="${OLIVARES_TASKFILE:-Taskfile.yml}"; GANCHO="${OLIVARES_HOOK:-.githooks/pre-push}"
[ -r "$TF" ] || { echo "check-orphan-batteries: NO HE PODIDO MIRAR: no leo $TF." >&2; exit 2; }
[ -r "$GANCHO" ] || { echo "check-orphan-batteries: NO HE PODIDO MIRAR: no leo $GANCHO." >&2; exit 2; }

# 1 · universo: las baterias que el arbol TIENE (rastreadas, no lo que haya suelto en el disco)
BATS=$(git ls-files 'scripts/test-*.sh' 2>/dev/null | sort)
[ -n "$BATS" ] || { echo "check-orphan-batteries: NO HE PODIDO MIRAR: cero baterias rastreadas." >&2; exit 2; }
N=$(printf '%s\n' "$BATS" | wc -l)

# 2 · lo que el gancho invoca, expandido por el GRAFO (deps incluidas)
PATAS=$(grep -oE '^[[:space:]]*task ([A-Za-z][A-Za-z0-9:_.-]*)' "$GANCHO" | awk '{print $2}' | sort -u)
[ -n "$PATAS" ] || { echo "check-orphan-batteries: NO HE PODIDO MIRAR: el gancho no invoca ninguna tarea." >&2; exit 2; }
ALCANZ=$(mktemp "${TMPDIR:-/tmp}/orb.XXXXXX") || exit 2
trap 'rm -f "$ALCANZ"' EXIT
# ⛔ Y UNA TAREA DEL GANCHO QUE NO EXISTE NO SE TRAGA EN SILENCIO. `task --dry` sale != 0 y su
# expansion viene vacia; sin esta comprobacion el gancho podia invocar una tarea inexistente, el
# alcance salia mas corto, y este gate contestaba rc 0 igualmente: **el detector de fail-open tenia
# el suyo propio**. Se rehusa con la raiz nombrada, porque una raiz rota invalida TODO el alcance.
ROTAS=""
# ⛔ EL ALCANCE SIGUE LA INDIRECCION DEL ENVOLTORIO, o el gate acusa a 198 baterias inocentes.
#    Medido el 2026-08-31, y me lo comi yo: `bc4b13d0c` partio `lint:addon-sets-gate` en un
#    envoltorio que toma el veredicto acotado del export y una tarea `…:legs` con las patas. El
#    envoltorio la invoca por COMANDO —`bash scripts/hub-only-gate.sh <gate> … <tarea>`—, no por
#    `deps` ni por `cmds: - task:`, asi que `task --dry` no la expande y las 198 baterias que
#    cuelgan de `:legs` pasaron de alcanzadas a huerfanas SIN que nadie tocara una sola de ellas.
#    Aterrizo en `main` en rojo y paro el push de las cinco cajas.
#    Se recorre a PUNTO FIJO porque un envoltorio puede envolver a otro; `VISTAS` corta el ciclo.
PENDIENTES="$PATAS"
VISTAS=""
while [ -n "$PENDIENTES" ]; do
  SIGUIENTE=""
  while IFS= read -r t; do
    [ -n "$t" ] || continue
    case " $VISTAS " in *" $t "*) continue ;; esac
    VISTAS="$VISTAS $t"
    if ! task --dry "$t" >/dev/null 2>&1; then ROTAS="${ROTAS}${t} "; continue; fi
    SALIDA=$(task --dry "$t" 2>&1)
    printf '%s\n' "$SALIDA" | sed -n 's/^task: \[\(.*\)\].*/\1/p' >>"$ALCANZ"
    ENVUELTAS=$(printf '%s\n' "$SALIDA" |
      command grep -oE 'hub-only-gate\.sh[^|;&]*' |
      awk '{print $NF}' |
      command grep -E '^[A-Za-z][A-Za-z0-9:_.-]*$' | sort -u)
    [ -n "$ENVUELTAS" ] && SIGUIENTE="${SIGUIENTE}
${ENVUELTAS}"
  done <<EOF
$PENDIENTES
EOF
  PENDIENTES=$(printf '%s\n' "$SIGUIENTE" | sed '/^[[:space:]]*$/d' | sort -u)
done
if [ -n "$ROTAS" ]; then
  echo "check-orphan-batteries: NO HE PODIDO MIRAR: el gancho invoca tarea(s) que NO existen: ${ROTAS}" >&2
  echo "  Una raiz rota deja el alcance corto y este gate contestaria 0 sobre un universo incompleto." >&2
  exit 2
fi
sort -u -o "$ALCANZ" "$ALCANZ"
[ -s "$ALCANZ" ] || { echo "check-orphan-batteries: NO HE PODIDO MIRAR: la expansion del grafo salio vacia." >&2; exit 2; }

# 3 · para cada bateria: que tarea la invoca, y esa tarea la alcanza el gancho?
SIN_TAREA=""; SIN_LLAMADA=""
while IFS= read -r b; do
  [ -n "$b" ] || continue
  # ⛔ DUEÑA SOLO SI LA INVOCA, NO SI LA NOMBRA. La primera version casaba la ruta en CUALQUIER
  # linea del bloque de la tarea — `desc:` incluida—, asi que una bateria mencionada en una
  # descripcion se daba por ejecutada y salia del censo. **Un falso VERDE en un detector de
  # huerfanas es la ironia exacta que este gate existe para cortar.** Ahora solo cuenta dentro de
  # `cmds:`, que es lo unico que `task` ejecuta.
  duenas=$(awk -v b="$b" '
      /^  [A-Za-z0-9:_.-]+:$/ { t=substr($1,1,length($1)-1); en=0 }
      /^    cmds:/            { en=1; next }
      /^    [a-z]+:/          { if ($0 !~ /^    cmds:/) en=0 }
      en && index($0,b)       { if (t!="") print t }' "$TF" | sort -u)
  if [ -z "$duenas" ]; then SIN_TAREA="${SIN_TAREA}${b}"$'\n'; continue; fi
  viva=no
  while IFS= read -r d; do
    [ -n "$d" ] || continue
    grep -qxF "$d" "$ALCANZ" && { viva=si; break; }
  done <<EOF
$duenas
EOF
  [ "$viva" = no ] && SIN_LLAMADA="${SIN_LLAMADA}${b} (tarea: $(printf '%s' "$duenas" | tr '\n' ' '))"$'\n'
done <<EOF
$BATS
EOF

# ⛔ CENSO CON LINEA BASE, NO ROJO DIRECTO — y no es blandura: las que ya existen son demasiadas, asi que un
# rojo directo seria inadoptable y acabaria desactivado, que es la peor forma de no tener gate. El
# arbol ya usa esta forma (`lint:list-truncation-witness` con `docs/list-truncation-baseline.txt`).
# Lo que corta es el CRECIMIENTO: una bateria nueva que nadie invoca. Las de hoy quedan
# NOMBRADAS en la base, que es lo contrario de esconderlas — se pueden contar y bajar.
# ⛔ Y AQUI NO VA UNA CIFRA. Decia «hoy hay 47» y «las 47 de hoy». No era falso el dia que se
# escribio, y por eso es peor: envejece en silencio. Lo acoto the reviewer al leerlo desde un arbol
# donde el numero ya era otro — la cura que existe para que no se congelen cifras dejo una cifra
# congelada en su propia prosa. El numero lo DERIVA la salida, que imprime el conteo de hoy y el
# de la linea base en la misma linea; ahi no puede envejecer sin que el gate lo note.
BASE_F="${OLIVARES_ORPHAN_BASELINE:-docs/orphan-batteries-baseline.txt}"
TODAS=$(printf '%s%s' "$SIN_TAREA" "$(printf '%s' "$SIN_LLAMADA" | sed 's/ (tarea:.*//')" | sed '/^$/d' | sort -u)
if [ -n "${OLIVARES_ORPHAN_WRITE_BASELINE:-}" ]; then
  printf '%s\n' "$TODAS" > "$BASE_F"; echo "check-orphan-batteries: linea base reescrita ($(printf '%s\n' "$TODAS" | wc -l))."; exit 0
fi
if [ -r "$BASE_F" ]; then
  NUEVAS=$(comm -23 <(printf '%s\n' "$TODAS") <(grep -v '^#' "$BASE_F" | sed '/^$/d' | sort -u))
  IDAS=$(comm -13 <(printf '%s\n' "$TODAS") <(grep -v '^#' "$BASE_F" | sed '/^$/d' | sort -u))
else
  echo "check-orphan-batteries: NO HE PODIDO MIRAR: no leo la linea base $BASE_F." >&2
  echo "  Sin ella no se puede distinguir una huerfana NUEVA de las que ya estaban." >&2; exit 2
fi

echo "check-orphan-batteries: ${N} bateria(s) rastreada(s) · $(wc -l < "$ALCANZ") tarea(s) alcanzables desde el gancho"
echo "  huerfanas hoy: $(printf '%s\n' "$TODAS" | sed '/^$/d' | wc -l) · en la linea base: $(grep -cv '^#' "$BASE_F")"
[ -n "$IDAS" ] && { echo "  ✔ CABLEADAS desde la ultima base (bajalas de $BASE_F):"; printf '%s\n' "$IDAS" | sed 's/^/      /'; }
if [ -z "$NUEVAS" ]; then
  echo "check-orphan-batteries: limpio — ninguna bateria NUEVA sin quien la ejecute."; exit 0
fi
SIN_TAREA=$(comm -12 <(printf '%s\n' "$NUEVAS") <(printf '%s' "$SIN_TAREA" | sed '/^$/d' | sort -u))
SIN_LLAMADA=$(printf '%s' "$SIN_LLAMADA" | sed '/^$/d' | while IFS= read -r l; do
                printf '%s\n' "$NUEVAS" | grep -qxF "${l%% (tarea:*}" && printf '%s\n' "$l"; done)
echo "check-orphan-batteries: ⛔ HALLAZGO — bateria(s) NUEVA(s) que nadie ejecuta:" >&2
[ -n "$SIN_TAREA" ] && { echo "  SIN TAREA (ninguna entrada del Taskfile las invoca):" >&2
                         printf '%s\n' "$SIN_TAREA" | sed '/^$/d;s/^/      /' >&2; }
[ -n "$SIN_LLAMADA" ] && { echo "  CON TAREA PERO SIN LLAMADA (existe la tarea; el gancho no la alcanza, ni por deps):" >&2
                           printf '%s\n' "$SIN_LLAMADA" | sed '/^$/d;s/^/      /' >&2; }
echo "  Un banco que nadie corre no corta, y su verde se lee como cobertura." >&2
exit 1
