#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# La batería del integrador sobre un lote, DERIVADA de `.githooks/pre-push`.
#
# ⛔ POR QUÉ EXISTE, Y NO ES HIGIENE: el integrador empuja a `main` con `--no-verify`
#    —bypass sancionado, porque el gate completo sobre `main` es aritmética imposible—, así
#    que **el hook NUNCA corre** y la lista que el integrador teclea a mano es el ÚNICO gate.
#
#    Medido el 2026-08-20: la lista canónica son **109 tareas** (99 rápidas + 10 pesadas).
#    En el lote 24 se tecleraron **10**, y de las **diez pesadas se saltaron cinco**:
#    `sdk:check`, `test:cloud:norace`, `test:license-worker`, `test:web`, `web:check`.
#    `test:license-worker` estaba en la batería del lote 22 y no en la del 24 — nadie lo
#    decidió; se tecleó otra lista. Resultado: `main` rojo durante horas con el e2e de la
#    cadena hermética ejercitando el camino de rechazo en silencio.
#
#    Es la SEGUNDA instancia de esta raíz en ~30 horas (la anterior: «recorté la
#    verificación», `main` rojo 20 min). Una es el sistema funcionando; dos es un patrón.
#
# ⇒ LA REGLA: **la batería no se teclea, se deriva.** Este guion extrae la lista del hook,
#    así que no puede desviarse de lo que un push a `main` correría. Si el hook cambia, esto
#    cambia con él. Si alguien quiere correr menos, tiene que decir QUÉ y POR QUÉ — y queda
#    escrito en la salida, no en la memoria de quien lo corrió.
#
# Uso:  bash scripts/hub-battery.sh [--fast-only|--heavy-only] [--list] [--push[=<ref-remoto>]]
#
# --push DECLARA el push que esta bateria esta verificando, en el formato del protocolo
# pre-push (`<ref-local> <sha-local> <ref-remoto> <sha-remoto>`), y lo expone en
# OLIVARES_PUSH_REFS_FILE. Sin el, las sondas que distinguen «trabajo sin publicar» de
# «trabajo publicandose» -- hoy check-unpublished-work.sh -- ven la punta del lote como
# trabajo perdido y salen ROJAS POR DEFINICION, en todos los lotes. No es un rojo del lote:
# es que el integrador empuja con `--no-verify`, el hook nunca corre para el, y por tanto
# nadie escribia ese fichero. La bateria ES el gate del integrador, asi que le toca a ella
# declarar lo mismo que declararia el hook. Marcar la sonda como «roja esperada» seria
# cablear una expectativa: taparia tambien el dia en que el hallazgo fuese real.
# Sale: 0 todo verde · 1 alguna tarea roja · 2 NO HE PODIDO MIRAR (sin hook, sin task)

set -u
RAIZ="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)"

# ⛔ AISLAMIENTO DEL ENTORNO DE GIT, y no es ceremonia: esta bateria lee el repositorio REAL con
#    `git -C "$RAIZ" rev-parse` para declarar el push, y `-C` cambia de directorio pero NO gana a un
#    `GIT_DIR` heredado. Una sesion lanzada desde un worktree ENLAZADO exporta `GIT_DIR` a sus hijos
#    (medido el 2026-08-06), asi que sin esto la bateria podria reportar la punta de OTRO arbol y
#    decirlo con toda confianza. Se sourcea igual que los otros treinta y tantos guiones del arbol.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=scripts/lib/git-env.sh
. "$_olivares_git_env" || { echo "hub-battery: NO HE PODIDO MIRAR: no puedo sourcear $_olivares_git_env" >&2; exit 2; }
HOOK="${RAIZ}/.githooks/pre-push"

# ⛔ EL `panic: test timed out` DE GO NO ES UN ROJO, Y SALE POR LA PUERTA DE LOS ROJOS.
# El `timeout` EXTERNO sale 124 y ya se trata mas abajo. El presupuesto INTERNO de `go test
# -timeout` no: Go entra en panico, `task` devuelve un fallo corriente, y el veredicto queda
# indistinguible de una suite que falla de verdad. Medido el 2026-08-21 sobre `mainline-ci`:
# `race-modules` salia ROJO con CERO `--- FAIL` y CERO `DATA RACE`, solo cuatro panicos de
# presupuesto. Un rojo cronico que no dice nada del arbol acaba leyendose como ruido, y entonces
# tampoco se lee el dia que es real.
#
# EL DISCRIMINADOR ES ESTRECHO A PROPOSITO. Este mismo fichero ya documenta la vez que convertir
# un hallazgo en «no he podido mirar» fue PEOR que el defecto original. Asi que exige las
# tres cosas: el panico ANCLADO a principio de linea, y la ausencia TOTAL de fallos de test y de
# carreras. Un timeout que llega DESPUES de un fallo real sigue siendo ROJO.
#
# Y el anclaje aqui SI vale, al reves que sobre un log de CI: esta es la salida local de `task`,
# sin prefijo de fecha. Sobre los logs de Actions el mismo ancla no casaria nunca — es el defecto
# con el que publique «race-rest: cero fallos» esta misma manana.
es_timeout_interno_de_go() {
  command grep -qE '^panic: test timed out after ' "$1" || return 1
  command grep -qE '^[[:space:]]*--- FAIL: ' "$1" && return 1
  command grep -qE '^WARNING: DATA RACE$' "$1" && return 1
  return 0
}

[ -r "$HOOK" ] || { echo "hub-battery: ⛔ NO HE PODIDO MIRAR: no leo ${HOOK}" >&2; exit 2; }
command -v task >/dev/null 2>&1 || { echo "hub-battery: ⛔ NO HE PODIDO MIRAR: no hay 'task' en PATH" >&2; exit 2; }

# La frontera rápido/pesado es la comprobación de clase del propio hook. Se localiza, no se
# codifica: un número de línea a mano es exactamente la clase de dato que envejece en silencio.
CORTE="$(command grep -n '^if \[ "\$gate_class" != "full" \]; then' "$HOOK" | head -1 | cut -d: -f1)"
if [ -z "${CORTE:-}" ]; then
  echo "hub-battery: ⛔ NO HE PODIDO MIRAR: no encuentro la frontera rápido/pesado en el hook." >&2
  echo "             (buscaba la línea 'if [ \"\$gate_class\" != \"full\" ]; then')" >&2
  echo "             El hook cambió de forma: arregla ESTE guion antes de seguir integrando." >&2
  exit 2
fi

# ⛔ DERIVAR LA LISTA INCLUYE DERIVAR SU DISPOSICION. Medido el 2026-08-21: la version
# anterior usaba `grep -oE '^task <nombre>'`, que CORTA ANTES del resto de la linea, y el hook
# tiene una linea distinta —una, frente a 110 bloqueantes—:
#     .githooks/pre-push:797:  task lint:unpublished-work || true
# Esa tarea salia ⛔ ROJA en la bateria y el hook NO bloquea con ella. Es decir, la bateria era
# MAS ESTRICTA que el gate del que deriva, que es justo el defecto que existe para no tener. Y no
# es una tarea cualquiera: `check-unpublished-work` censa TODOS los worktrees del contenedor, asi
# que un lote mio enrojecia por ramas de otro carril que yo no puedo publicar — la «coordination
# barrier wearing a quality gate's clothes» que el propio hook describe en su cabecera.
# Salida: `<nombre><TAB>block|advisory`.
# ⛔ EL ANCLA `^task` PERDIA DOS TAREAS REALES, Y UNA ERA LA SUITE COMPLETA DE GO.
# Medido el 2026-08-21: el hook invoca 117 tareas al principio de linea y TRES indentadas. De esas
# tres, `task --list-all` (376) no es un gate, pero `lint:commit-identity` (555) y `task test`
# (1491) SI — y `test` es la mayor pata de correccion que existe. Iban dentro de un `if/else` que
# elige ajustes de memoria, asi que estaban tabuladas y ESTE guion no las veia: los lotes 52, 53 y
# 54 se declararon «116 verdes» SIN haber corrido la suite completa.
#
# `[a-z0-9][a-z0-9:_-]*` en vez de `[a-z0-9:_-]+` para que `task --list-all` no entre: el guion
# medio esta en la clase, y sin anclar el primer caracter una bandera se leeria como nombre de
# tarea. Un nombre de tarea inventado da «task: no encuentro» y la bateria lo contaria como rojo.
listar() {
	awk -v a="$1" -v b="$2" 'NR>=a && NR<b' "$HOOK" \
	  | command grep -oE '^[[:space:]]*task [a-z0-9][a-z0-9:_-]*([[:space:]]*\|\|[[:space:]]*true)?' \
	  | sed -E 's/^[[:space:]]*task //' \
	  | sed -E 's/^([a-z0-9][a-z0-9:_-]*)[[:space:]]*\|\|[[:space:]]*true$/\1\tadvisory/; s/^([a-z0-9][a-z0-9:_-]*)$/\1\tblock/' \
	  | sort -u
}
FAST="$(listar 1 "$CORTE")"
HEAVY="$(listar "$CORTE" 999999)"
n_fast=$(printf '%s\n' "$FAST" | command grep -c .)
n_heavy=$(printf '%s\n' "$HEAVY" | command grep -c .)

# ⛔ CONTROL DE COBERTURA, NOMBRE A NOMBRE. Existe porque su ausencia costo tres lotes.
#
# Se barren los nombres de tarea que aparecen en el hook con un patron DELIBERADAMENTE PERMISIVO
# —`task <nombre>` en cualquier posicion de la linea— y se exige que CADA UNO este en lo derivado.
#
# ⚠ Una comparacion de CUENTAS con tolerancia NO sirve, y lo probe: mi primera version pedia
# `derivadas >= esperadas - esperadas/3`, y al mutar el ancla de vuelta a `^task` —que pierde dos
# tareas— el control SOBREVIVIO, porque 117 sigue siendo mas de dos tercios de 120. Un control cuyo
# margen es mayor que el defecto que persigue no es un control.
#
# Lo que lo motivo: `task test` vive dentro de un `if/else` de memoria y va TABULADO, asi que el
# ancla `^task` lo perdia. Los lotes 52, 53 y 54 se declararon «116 verdes» sin haber corrido la
# suite completa de Go. `task --list-all` no entra porque su token empieza por guion y la clase
# exige que el primer caracter sea alfanumerico.
# ⚠ SE EXCLUYEN LAS LINEAS DE COMENTARIO, y no es cosmetica: la cabecera de este hook explica sus
# decisiones en ingles, y frases como «the task in question» o «the task installs its …» hacian que
# el barrido permisivo inventara tareas llamadas `in`, `installs`, `its` y `through`. Un control que
# grita por prosa se desactiva en una semana.
faltantes=""
while IFS= read -r nombre; do
	[ -n "$nombre" ] || continue
	printf '%s\n%s\n' "$FAST" "$HEAVY" | cut -f1 | command grep -qxF "$nombre" || faltantes="${faltantes}${nombre} "
done <<<"$(command grep -vE '^[[:space:]]*#' "$HOOK" 2>/dev/null |
	command grep -oE '(^|[[:space:]])task [a-z0-9][a-z0-9:_-]*' |
	sed -E 's/^[[:space:]]*task //' | sort -u)"
if [ -n "$faltantes" ]; then
	echo "hub-battery: ⛔ NO HE PODIDO MIRAR: el hook invoca tarea(s) que NO he derivado:" >&2
	printf 'hub-battery:              %s\n' "$faltantes" >&2
	echo "             listar() ha dejado de ver una forma de invocacion. Arregla ESTE guion antes de seguir." >&2
	exit 2
fi

[ "$n_fast" -gt 0 ] && [ "$n_heavy" -gt 0 ] || {
  echo "hub-battery: ⛔ NO HE PODIDO MIRAR: extraje ${n_fast} rápidas y ${n_heavy} pesadas." >&2
  echo "             Una lista vacía no es 'no hay nada que correr': es que el parseo falló." >&2
  exit 2
}

MODO="--all"
PUSH_REF=""
DECLARA_PUSH=0
for arg in "$@"; do
  case "$arg" in
    --list | --fast-only | --heavy-only | --all | --selftest) MODO="$arg" ;;
    --push)     DECLARA_PUSH=1; PUSH_REF="refs/heads/main" ;;
    --push=*)   DECLARA_PUSH=1; PUSH_REF="${arg#--push=}" ;;
    *) echo "hub-battery: ⛔ NO HE PODIDO MIRAR: argumento desconocido '$arg'." >&2
       echo "             No adivino que querias correr; una bateria que corre otra cosa" >&2
       echo "             que la pedida es peor que una que no corre." >&2
       exit 2 ;;
  esac
done

case "$MODO" in
  --selftest)
    # El banco vive AQUI, no en un guion aparte, para que no haya dos implementaciones del
    # discriminador que puedan derivar. Seis casos, tres de ellos en la direccion que NO dispara.
    # ⛔ UN FICHERO, NO UN DIRECTORIO, y la razon es un GATE. `check-git-env-isolation.sh` deriva
    #    su clase de emparejar `mktemp -d` con una invocacion de git: es un PROXY de «este guion
    #    construye un repositorio desechable». Este banco no construye ninguno —le basta un fichero
    #    con la salida simulada— asi que el `-d` era una señal FALSA, y una señal falsa en un gate
    #    derivado cuesta lo mismo que una verdadera.
    st_tmp="$(mktemp "${TMPDIR:-/tmp}/hub-battery-selftest.XXXXXX")" || exit 2
    trap 'rm -f "$st_tmp"' EXIT INT TERM
    st_fallos=0
    st_caso() {
      printf '%s\n' "$3" > "$st_tmp"
      if es_timeout_interno_de_go "$st_tmp"; then st_got=0; else st_got=1; fi
      if [ "$st_got" = "$2" ]; then printf '  ✅ %s\n' "$1"
      else printf '  ⛔ %s: esperaba %s y dio %s\n' "$1" "$2" "$st_got"; st_fallos=$((st_fallos + 1)); fi
    }
    st_caso 'panico limpio de presupuesto ⇒ CIEGA' 0 'panic: test timed out after 150m0s
running tests:
	TestFoo (150m0s)'
    st_caso 'panico DESPUES de un fallo real ⇒ ROJA' 1 '--- FAIL: TestBar (0.01s)
panic: test timed out after 150m0s'
    st_caso 'panico con un SUBTEST fallado ⇒ ROJA' 1 '    --- FAIL: TestBar/sub (0.01s)
panic: test timed out after 150m0s'
    st_caso 'panico con una CARRERA ⇒ ROJA' 1 'WARNING: DATA RACE
panic: test timed out after 150m0s'
    st_caso 'fallo corriente sin panico ⇒ ROJA' 1 '--- FAIL: TestBaz (0.02s)
FAIL'
    st_caso 'la FRASE a media linea ⇒ ROJA (ejercita el ANCLA)' 1 'ok  x  0.1s  el banco imprime panic: test timed out after 5s como texto'
    if [ "$st_fallos" -eq 0 ]; then echo "hub-battery --selftest: 6/6 casos"; exit 0
    else echo "hub-battery --selftest: ${st_fallos} fallo(s)"; exit 1; fi ;;
  --list)       printf 'FAST (%d)\n' "$n_fast"; printf '%s\n' "$FAST" | sed 's/^/  /'
                printf 'HEAVY (%d)\n' "$n_heavy"; printf '%s\n' "$HEAVY" | sed 's/^/  /'; exit 0 ;;
  --fast-only)  LISTA="$FAST" ;;
  --heavy-only) LISTA="$HEAVY" ;;
  *)            LISTA="$(printf '%s\n%s\n' "$FAST" "$HEAVY")" ;;
esac

# ── Declaracion del push, si se pidio ────────────────────────────────────────
# TRES RESPUESTAS: si se pidio declarar el push y no se puede resolver que se
# publicaria, se REFUSA. Declarar un push equivocado seria peor que no declararlo:
# eximiria de la sonda a una punta que si esta perdiendose.
REFS_FILE=""
if [ "$DECLARA_PUSH" -eq 1 ]; then
  if [ -z "$PUSH_REF" ]; then
    echo "hub-battery: ⛔ NO HE PODIDO MIRAR: --push= sin ref remoto." >&2; exit 2
  fi
  case "$PUSH_REF" in
    refs/*) : ;;
    *) echo "hub-battery: ⛔ NO HE PODIDO MIRAR: '$PUSH_REF' no es un ref completo" >&2
       echo "             (se espera refs/heads/<rama> o refs/tags/<tag>)." >&2; exit 2 ;;
  esac
  LSHA="$(git -C "$RAIZ" rev-parse --verify HEAD 2>/dev/null)" || LSHA=""
  if [ -z "$LSHA" ]; then
    echo "hub-battery: ⛔ NO HE PODIDO MIRAR: no resuelvo HEAD en ${RAIZ}." >&2; exit 2
  fi
  LREF="$(git -C "$RAIZ" symbolic-ref -q HEAD 2>/dev/null)" || LREF=""
  if [ -z "$LREF" ]; then
    echo "hub-battery: ⛔ NO HE PODIDO MIRAR: HEAD esta suelto (detached); no se que rama" >&2
    echo "             se publicaria." >&2; exit 2
  fi
  # El sha remoto es informativo para el protocolo; ceros si el ref aun no existe alli.
  RSHA="$(git -C "$RAIZ" rev-parse --verify -q "refs/remotes/origin/${PUSH_REF#refs/heads/}" 2>/dev/null)" \
    || RSHA=""
  [ -n "$RSHA" ] || RSHA="0000000000000000000000000000000000000000"
  REFS_FILE="$(mktemp "${TMPDIR:-/tmp}/hub-battery-refs.XXXXXX")" || {
    echo "hub-battery: ⛔ NO HE PODIDO MIRAR: no puedo crear el fichero de refs." >&2; exit 2; }
  printf '%s %s %s %s\n' "$LREF" "$LSHA" "$PUSH_REF" "$RSHA" >"$REFS_FILE"
  export OLIVARES_PUSH_REFS_FILE="$REFS_FILE"
  trap 'rm -f "$REFS_FILE"' EXIT INT TERM
  echo "hub-battery: declarando el push -> ${LREF} ${LSHA} ${PUSH_REF} ${RSHA}"
fi

# ── El ENTORNO que el hook monta para esas tareas, derivado igual que la lista ──────────
# ⛔ CORREGIDO EL 2026-08-20, y es un agujero en TODA declaracion de verde que hice hoy.
# Esta bateria extraia del hook las TAREAS y no el ENTORNO que el hook les monta. Sin
# `OLIVARES_TEST_POSTGRES_SUPERUSER_DSN` los `_pg_test.go` se SALTAN y `go test` imprime
# `ok`: un «111/111 LIMPIA» significaba «111 verdes SIN las patas de Postgres».
# Medido por el carril del kernel sobre core/internal/store/sqlstore: con DSN afloran TRES
# rojos que sin DSN salen `--- SKIP` y el paquete dice `ok`.
#
# El hook hace exactamente esto (seccion LOCAL DEFAULTS de .githooks/pre-push): el opt-in
# vive ahi y no dentro del ayudante, porque en CI el 127.0.0.1:5432 de un runner compartido
# es el cluster de OTRO job. Se copia el mecanismo, no el veredicto.
avisos=0
# ⛔ TMPDIR, y no es higiene. Medido el 2026-08-21: `/tmp` en este contenedor es
# `tmpfs … noexec`, y `lint:guide-docs` COMPILA su gate en TMPDIR y lo ejecuta ⇒ salia
#   ⚠️ CIEGA: «built the gate under TMPDIR=/tmp but the shell could not execute it».
# Ni el hook ni este guion FIJABAN TMPDIR: los dos solo la usan como ${TMPDIR:-/tmp}.
# Y `lint:guide-docs` es de la lista PESADA, asi que un push de rama no la corre y el
# integrador empuja a `main` con --no-verify ⇒ LA UNICA QUE LA CORRE EN ESTA CAJA ES
# ESTA BATERIA. Una bateria ciega ahi significa que esa tarea no la corre NADIE.
# Toda lectura va con su forma por defecto: `lint:unbound-env` lo exige y tiene razon
# medida — en los runners estas variables no siempre existen (HOME falta en seis de nueve),
# y bajo `set -u` el guion muere con el nombre del PASO y no el de la variable.
_hb_tmp="${TMPDIR:-}"
if [ -z "$_hb_tmp" ] || ! ( : >"${_hb_tmp}/.hb-exec-test" 2>/dev/null && chmod +x "${_hb_tmp}/.hb-exec-test" 2>/dev/null ); then
	_hb_tmp="${RAIZ}/../.olivares-tmptest"
	mkdir -p "$_hb_tmp" || { echo "hub-battery: ⛔ NO HE PODIDO MIRAR: no puedo crear $_hb_tmp." >&2; exit 2; }
	_hb_tmp="$(cd "$_hb_tmp" && pwd -P)"
	TMPDIR="$_hb_tmp"
	export TMPDIR
	echo "hub-battery: TMPDIR fijado a $_hb_tmp (/tmp es noexec en este contenedor)."
fi
rm -f "${_hb_tmp}/.hb-exec-test" 2>/dev/null || true

PG_PARCIAL=0
if [ -r "${RAIZ}/scripts/pg-test-env.sh" ]; then
	export OLIVARES_PG_LOCAL_DEFAULTS=1
	if pg_exports="$(bash "${RAIZ}/scripts/pg-test-env.sh" 2>/dev/null)"; then
		if [ -n "$pg_exports" ]; then
			eval "$pg_exports"
			echo "hub-battery: entorno Postgres montado — las patas de PG SI se ejecutan."

# ── PREVUELO DE DEPENDENCIAS NODE. Existe por una medida, no por prolijidad.
#
# `test:license-worker` salio CIEGA en TRES lotes seguidos (52, 53 y 54 del relevo 23), siempre por
# lo mismo: `npm ci` es POR WORKTREE, no por contenedor, y cada lote se monta en un worktree nuevo.
# El gate lo dice bien y trae el remedio exacto — pero lo dice en el MINUTO 35, cuando ya se ha
# pagado el resto de la bateria. La comprobacion responde en el minuto 0.
#
# NO INSTALA NADA —una bateria que muta su entorno deja de medirlo— y NO cambia el veredicto: la
# pata ciega ya se contabiliza al final como «no es un verde». Esto adelanta la noticia.
#
# ⛔ DOS DEFECTOS QUE COSTARON ESCRIBIRLO DOS VECES, y los dos daban FALSO VERDE:
#   1. `grep '"dependencies"…{…"'` NO CASA NUNCA: `grep` es por lineas y en un package.json real
#      la llave y la primera clave estan en lineas distintas. El prevuelo decia «no falta nada»
#      sobre un arbol al que le faltaba todo. Aqui se LEE el JSON.
#   2. Avisar de TODOS los arboles node del repo son SIETE lineas de las que importa UNA, y un
#      aviso que siempre salta se aprende a ignorar. Se acota a los directorios que nombran las
#      tareas DE ESTA CORRIDA, y se descartan las que se auto-instalan: `check:web` corre
#      `pnpm --dir web install --frozen-lockfile` y por eso `web` nunca sale ciega, mientras
#      `test:license-worker` invoca `npm run` directamente y si.
#
# El nombre del manifiesto se compara EXACTO: `ls-files '*package.json'` tambien traeria
# `module-slug-package.json`, que no es un manifiesto npm y cuyo directorio no es instalable.
faltan_deps=""
if command -v python3 >/dev/null 2>&1; then
	faltan_deps="$(printf '%s\n%s\n' "$FAST" "$HEAVY" |
		OLIVARES_RAIZ="$RAIZ" python3 -c '
import json, os, re, sys
raiz = os.environ["OLIVARES_RAIZ"]
# stdin SE CONSUME ENTERO ANTES de cualquier salida posible. Si este proceso terminara con el
# productor a medias, `printf` moriria de SIGPIPE y la tuberia devolveria 141 EN EXITO — el mismo
# defecto que `lint:sigpipe-booleans` le rechazo hoy a otro carril. Aqui no mata (este guion no
# lleva `pipefail`), pero depender de eso es depender del tamano del bufer.
tareas = {l.strip().split("\t")[0] for l in sys.stdin if l.strip()}
try:
    texto = open(os.path.join(raiz, "Taskfile.yml"), encoding="utf-8").read()
except Exception:
    sys.exit(0)                      # ya se leyo stdin: salir aqui no rompe al productor
bloques = re.split(r"\n  (?=[a-zA-Z0-9:_-]+:\n)", texto)


def dirs_de(bloque):
    hallados = set()
    for d in re.findall(r"--(?:dir|prefix)[= ]([A-Za-z0-9_./-]+)", bloque):
        hallados.add(d)
    for d in re.findall(r"private-leg\.sh\s+\S+\s+([A-Za-z0-9_./-]+)", bloque):
        hallados.add(d)
    for d in re.findall(r"^\s*dir:\s*([A-Za-z0-9_./-]+)", bloque, re.M):
        hallados.add(d)
    return hallados


# El COBIJO se calcula sobre TODA la corrida, no tarea a tarea: si CUALQUIER tarea del lote
# instala un directorio, ese directorio esta cubierto para las demas. `check:web` corre
# `pnpm --dir web install --frozen-lockfile`, asi que `test:web` —que solo hace `exec vitest`—
# no puede salir ciega por `web`. Calcularlo por tarea daba `web` como falta y era ruido: el
# aviso que siempre salta es el que se aprende a ignorar.
candidatos = set()
cobijados = set()
for b in bloques:
    cab = b.lstrip().split(":\n", 1)[0].strip()
    if cab not in tareas:
        continue
    if re.search(r"(npm|pnpm|yarn)[^\n]*\b(install|ci)\b", b):
        cobijados |= dirs_de(b)
        continue
    candidatos |= dirs_de(b)
candidatos -= cobijados
for d in sorted(candidatos):
    manifiesto = os.path.join(raiz, d, "package.json")
    if not os.path.isfile(manifiesto):
        continue
    try:
        datos = json.load(open(manifiesto, encoding="utf-8"))
    except Exception:
        continue
    if not (datos.get("dependencies") or datos.get("devDependencies")):
        continue
    if os.path.isdir(os.path.join(raiz, d, "node_modules")):
        continue
    print(d)
' 2>/dev/null)"
else
	echo "hub-battery: ⚠️  sin python3: no compruebo las dependencias node de este worktree." >&2
fi
if [ -n "$faltan_deps" ]; then
	echo "hub-battery: ⚠️  DEPENDENCIAS NODE AUSENTES para tareas DE ESTA CORRIDA — saldran CIEGAS" >&2
	echo "             (no rojas). Resuelvelo AHORA y no en el minuto 35:" >&2
	while IFS= read -r d; do
		[ -n "$d" ] || continue
		printf '               npm --prefix %s ci\n' "$d" >&2
	done <<<"$faltan_deps"
fi
		else
			echo "hub-battery: ⚠️  SIN servidor Postgres alcanzable: las patas de PG se SALTARAN." >&2
			echo "             Un verde de esta corrida NO cubre RLS, HA, migraciones ni arranque." >&2
			PG_PARCIAL=1
		fi
	else
		echo "hub-battery: ⛔ NO HE PODIDO MIRAR: pg-test-env.sh fallo; postura de Postgres desconocida." >&2
		exit 2
	fi
else
	echo "hub-battery: ⚠️  no encuentro scripts/pg-test-env.sh; las patas de PG se SALTARAN." >&2
	PG_PARCIAL=1
fi

total=$(printf '%s\n' "$LISTA" | command grep -c .)
echo "hub-battery: ${total} tarea(s) derivadas de .githooks/pre-push (frontera en la línea ${CORTE})"
rojas=0; verdes=0; ciegas=0

# TRES RESPUESTAS, tambien aqui. Hasta el 2026-08-20 este bucle colapsaba todo rc!=0 en
# «ROJA», y eso ES el defecto que el resto de los gates de este repo tienen prohibido:
# `test:license-worker` sale con 3 —«CANNOT LOOK: node_modules ausente, no se compilo ni se
# probo nada»— y se reportaba como hallazgo. Un lote se leia como sucio por una cadena de
# herramientas que faltaba, y la respuesta correcta (instalarla) no aparecia por ningun lado.
#
# `task` aplasta su PROPIO codigo de salida a 201, pero IMPRIME el real:
#   task: Failed to run task "<nombre>": exit status 3
# Ese es el discriminador, y no la frase «CANNOT LOOK».
#
# ⛔ CORREGIDO EL 2026-08-20, y el fallo era mio y peor que el que arreglaba. La primera
# version buscaba la FRASE en cualquier parte de la salida. `lint:session-numbers` imprime su
# self-test, y uno de sus casos que PASA se llama «a ref store with a missing object is CANNOT
# LOOK, not clean» ⇒ la bateria clasifico como CIEGA un rojo REAL (una colision de numero de
# sesion). Convertir un hallazgo en «no he podido mirar» es peor que el defecto original:
# el rojo original al menos se veia.
#
# Es la misma clase que el resto: buscar una cadena EN TODO EL TEXTO cuando el sujeto es UNA
# linea concreta. El ancla `^task: Failed...$` la fija a la linea de veredicto de `task`.
SALIDA_T="$(mktemp "${TMPDIR:-/tmp}/hub-battery-out.XXXXXX")" || {
  echo "hub-battery: ⛔ NO HE PODIDO MIRAR: no puedo crear el fichero de salida." >&2; exit 2; }
trap 'rm -f "$SALIDA_T"' EXIT INT TERM

# CUATRO respuestas desde el 2026-08-21: verde · ROJA · CIEGA · AVISO.
# AVISO es una tarea que el hook corre con `|| true`: se ejecuta, se DICE lo que encontro, y NO
# bloquea. No es «no la corras» — su medida vale: la de `lint:unpublished-work` encontro esta
# misma noche un anexo de 115 lineas de otro carril que se habria perdido al retirar su worktree.
while IFS= read -r fila; do
  [ -n "$fila" ] || continue
  t="${fila%%	*}"
  d="${fila##*	}"
  case "$d" in
    block|advisory) ;;
    *) echo "hub-battery: ⛔ NO HE PODIDO MIRAR: disposicion desconocida '${d}' para '${t}'." >&2
       echo "             listar() dejo de derivarla; arregla ESTE guion antes de seguir." >&2
       exit 2 ;;
  esac
  # ⛔ EL PRESUPUESTO SE DERIVA, Y SOLO PUEDE SUBIR. Medido el 2026-08-21, el dia en que `task test`
  # entro por fin en esta lista: corre `cmd/olivares` con `-race` y `-timeout 150m`, y CLAUDE.md lo
  # documenta en 50-65 min. Con el tope plano de 1800 s NO PODIA TERMINAR NUNCA y salia ⛔ ROJA — la
  # primera vez que la suite completa corrio aqui, el rojo no era del codigo.
  #
  # Se lee el mayor `-timeout <N>m` que la tarea declare y se le da un 20 % de margen, pero NUNCA
  # por debajo de los 1800 s de siempre: `test:cloud:norace` declara `-timeout 10m` y tomarlo al pie
  # de la letra daria 720 s, MAS ESTRICTO que hoy. Un presupuesto derivado que APRIETA es un defecto.
  presu=$(command grep -A 30 "^  ${t}:" "$RAIZ/Taskfile.yml" 2>/dev/null |
    command grep -oE '\-timeout [0-9]+m' | command grep -oE '[0-9]+' | sort -rn | head -1)
  if [ -n "${presu:-}" ] && [ "$presu" -gt 0 ] 2>/dev/null; then
    presu=$(( presu * 60 * 12 / 10 ))
    [ "$presu" -lt 1800 ] && presu=1800
  else
    presu=1800
  fi
  timeout "$presu" task "$t" >"$SALIDA_T" 2>&1
  rc_t=$?
  if [ "$rc_t" -eq 0 ]; then
    verdes=$((verdes + 1))
  elif [ "$d" = "advisory" ]; then
    avisos=$((avisos + 1)); echo "  📣 AVISO (el hook NO bloquea con esta): ${t}"
    command grep -m3 -E '⛔|CANNOT LOOK|NO HE PODIDO MIRAR|hallazgo' "$SALIDA_T" | sed 's/^/       /'
  elif [ "$rc_t" -eq 124 ]; then
    # ⛔ UN TIMEOUT NO ES UN ROJO: es «no he podido mirar». `timeout` sale 124, y tratarlo como fallo
    # es la misma confusion que GitHub reportando un job agotado como `cancelled` — un rojo cronico
    # disfrazado, medido en `mainline-ci` este mismo dia.
    ciegas=$((ciegas + 1))
    echo "  ⚠️  CIEGA (agoto su presupuesto de ${presu}s; NO ha fallado): ${t}"
    echo "       un timeout no dice si el arbol esta limpio. Sube el presupuesto o corre la tarea aparte."
  elif es_timeout_interno_de_go "$SALIDA_T"; then
    ciegas=$((ciegas + 1))
    echo "  ⚠️  CIEGA (go agoto su PROPIO -timeout; 0 fallos y 0 carreras): ${t}"
    command grep -m1 -E '^panic: test timed out after ' "$SALIDA_T" | sed 's/^/       /'
    echo "       no dice si el arbol esta limpio. Subele el -timeout a esa suite o correla sola."
  elif command grep -qE '^task: Failed to run task "[^"]+": exit status (2|3)$' "$SALIDA_T"; then
    ciegas=$((ciegas + 1)); echo "  ⚠️  CIEGA (no he podido mirar): ${t}"
    command grep -m2 -E 'CANNOT LOOK|NO HE PODIDO MIRAR|Fix it where|Arreglalo' "$SALIDA_T" \
      | sed 's/^/       /'
  else
    rojas=$((rojas + 1)); echo "  ⛔ ROJA: ${t}"
    # ⛔ Y SE DICE POR QUE. Antes solo se imprimian lineas que casaran `⛔|CANNOT LOOK|…`, marcadores
    # de NUESTROS gates: una suite de Go que falla no lleva ninguno, asi que el rojo salia MUDO y
    # habia que reproducirlo a mano. Si no casa nada, se imprime la COLA, donde Go pone su veredicto.
    if command grep -qE '⛔|CANNOT LOOK|NO HE PODIDO MIRAR|FAIL|hallazgo' "$SALIDA_T"; then
      command grep -m4 -E '⛔|CANNOT LOOK|NO HE PODIDO MIRAR|FAIL|hallazgo' "$SALIDA_T" | sed 's/^/       /'
    else
      tail -4 "$SALIDA_T" | sed 's/^/       /'
    fi
  fi
done <<EOF
$LISTA
EOF

if [ "${PG_PARCIAL:-0}" = "1" ]; then
  echo "hub-battery: ${verdes} verde(s) · ${rojas} roja(s) · ${ciegas} ciega(s) · ${avisos} aviso(s) de ${total} — ⚠️ PARCIAL: SIN las patas de Postgres"
else
  echo "hub-battery: ${verdes} verde(s) · ${rojas} roja(s) · ${ciegas} ciega(s) · ${avisos} aviso(s) de ${total}"
fi
[ "$rojas" -eq 0 ] || exit 1
# Una ciega no es un verde. Publicar con la cadena de herramientas ausente seria declarar
# limpio lo que nadie miro, que es justo lo que la salida 2 existe para impedir.
[ "$ciegas" -eq 0 ] || { echo "hub-battery: ⛔ hay ${ciegas} tarea(s) que NO SE PUDIERON MIRAR: no es un verde." >&2; exit 2; }
exit 0
