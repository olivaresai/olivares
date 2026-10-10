#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-lane-preflight.sh — lo que el ENTORNO del carril tiene que cumplir, comprobado en el
# segundo 1 y no en el minuto 45.
#
# CENSUS-SUBJECT: external
#   Su sujeto es el entorno de ESTE worktree y de esta caja, no el repositorio: pasar sobre un
#   árbol vacío es CORRECTO, no un verde a ciegas.
#
# WHY THIS EXISTS, measured 2026-09-02 (a repository gate). Tres muertes seguidas de un push a `main`, y
# NINGUNA era el árbol:
#
#   1. 45 MINUTOS de carril rápido para morir en la PRIMERA pata del gate pesado:
#      `test:license-worker` contestó **exit 3 = CANNOT LOOK** porque el worktree era nuevo y
#      `commercial/license-worker/node_modules` no existía. La pata hizo lo correcto —negarse a
#      dar veredicto sin herramienta—; lo que estuvo mal es CUÁNDO se supo. La cura fue `npm ci`:
#      TRES SEGUNDOS. Barrido del mismo árbol: 6 de 8 directorios con dependencias no las tenían.
#
#   2. CERO SEGUNDOS, curando lo anterior: al exportar `TMPDIR` DENTRO del worktree, la batería de
#      `lint:mid-operation` cayó 14/1 — su sujeto es «responder 2 fuera de un repositorio git», y
#      con el temporal dentro de un repo la respuesta correcta pasa a ser 0. Fuera: 15/0.
#
#   3. Y dos FALSOS ROJOS al pre-verificar: `lint:guide-docs` (rc 2, `/tmp` es noexec) y
#      `test:cloud:norace` (rc 1, sin Postgres) corridos con `task -x` FUERA del gancho, que es
#      quien monta ese entorno. **La pata sola no es la pata del gancho.**
#
# Las tres son la misma clase —entorno del carril, no del árbol— y las tres se ven en un segundo.
#
# Salida: 0 el entorno sirve · 1 falta algo, con la orden exacta para arreglarlo · 2 NO PUDE MIRAR.
set -uo pipefail
LC_ALL=C
export LC_ALL

RAIZ="$(git rev-parse --show-toplevel 2>/dev/null || true)"
[ -n "$RAIZ" ] || {
	echo "check-lane-preflight: ⛔ COULD NOT CHECK: outside a Git repository" >&2
	exit 2
}
cd "$RAIZ" || exit 2

rc=0

# ── 1 · TMPDIR: ni dentro del repositorio, ni noexec ──────────────────────────────────────────
T="${TMPDIR:-/tmp}"
[ -d "$T" ] || {
	echo "check-lane-preflight: ⛔ COULD NOT CHECK: TMPDIR=$T is missing" >&2
	exit 2
}

DENTRO="$(git -C "$T" rev-parse --show-toplevel 2>/dev/null || true)"
if [ -n "$DENTRO" ]; then
	cat >&2 <<AVISO
check-lane-preflight: ⛔ TMPDIR is INSIDE a repository ($DENTRO).
    Tests that check whether a path is inside a repository fail with this configuration, and the
    failure appears to implicate the code: on 2026-09-02, lint:mid-operation failed 14/1 while
    the tree was clean.
    repair: export TMPDIR=/workspace/.olivares-tmptest
AVISO
	rc=1
fi

# EJECUTAR, no suponer: `/tmp` es noexec en esta caja y el motor extrae ahí sus plugins.
SONDA="$(mktemp "$T/preflight.XXXXXX" 2>/dev/null)" || {
	echo "check-lane-preflight: ⛔ COULD NOT CHECK: mktemp failed in $T" >&2
	exit 2
}
printf '#!/bin/sh\nexit 0\n' >"$SONDA"
chmod +x "$SONDA" 2>/dev/null
if ! "$SONDA" 2>/dev/null; then
	cat >&2 <<AVISO
check-lane-preflight: ⛔ TMPDIR=$T cannot EXECUTE binaries (noexec or missing permission).
    A script extracted there exits 126, causing an unrelated test leg to fail.
    repair: export TMPDIR=/workspace/.olivares-tmptest
AVISO
	rc=1
fi
rm -f "$SONDA"

# ── 2 · node_modules SÓLO donde una pata PESADA lo necesita ───────────────────────────────────
# ⛔ LA PRIMERA VERSIÓN EXIGÍA node_modules EN TODO `package.json` CON DEPENDENCIAS, Y ERA
#    DEMASIADO: marcaba SEIS directorios en un árbol cuyo push estaba pasando sin cinco de ellos.
#    Sobre-bloquear cuesta pushes igual que sub-bloquear cuesta rojos, así que la lista se DERIVA
#    del gancho —de las patas del gate pesado y del directorio que cada una pasa a `private-leg.sh`—
#    en vez de escribirla a mano o barrer el árbol. Hoy sale UNA: `commercial/license-worker`
#    (`cloud/control-plane` es módulo Go y no tiene `package.json`). Si mañana entra otra pata
#    privada con dependencias npm, esta lista la recoge sola.
HOOK="$RAIZ/.githooks/pre-push"
[ -r "$HOOK" ] || {
	echo "check-lane-preflight: ⛔ COULD NOT CHECK: cannot read $HOOK" >&2
	exit 2
}

faltan=""
while IFS= read -r t; do
	[ -n "$t" ] || continue
	d="$(python3 - "$t" <<'PY' 2>/dev/null
import re, sys, os
t = sys.argv[1]
try:
    tf = open("Taskfile.yml", encoding="utf-8").read()
except OSError:
    sys.exit()
m = re.search(r"^  " + re.escape(t) + r":\n(.*?)(?=^  [a-zA-Z0-9])", tf, re.S | re.M)
if not m:
    sys.exit()
mm = re.search(r"private-leg\.sh\s+\S+\s+(\S+)", m.group(1))
if mm and os.path.exists(os.path.join(mm.group(1), "package.json")):
    print(mm.group(1))
PY
)"
	[ -n "$d" ] || continue
	[ -d "$d/node_modules" ] && continue
	case "$faltan" in *"$d"*) continue ;; esac
	faltan="$faltan$d
"
done <<LISTA
$(sed -n '/running the FULL gate locally/,$p' "$HOOK" | grep -oE '^[[:space:]]*([A-Za-z_][A-Za-z0-9_]*=[^[:space:]]+[[:space:]]+)*task [a-z0-9:._-]+' | sed -E 's/.*task //')
LISTA

if [ -n "$faltan" ]; then
	echo "check-lane-preflight: ⛔ a heavy check requires dependencies missing from this worktree:" >&2
	printf '%s' "$faltan" | sed 's/^/    /' >&2
	cat >&2 <<'AVISO'
    That leg returns 3 (CANNOT LOOK), indicating no verdict, after the entire fast lane has run.
    On 2026-09-02, this cost 45 minutes; the repair took three seconds.
    `node_modules` belongs to each WORKTREE.
    repair, for each directory: npm --prefix <directory> ci
AVISO
	rc=1
fi

# 3 · Postgres: a reachable cluster needs a UTF8 template for the test suite.
# Measured 2026-09-03 (a repository gate): a main push ran for four hours before modules/sessions
# failed with “current database's encoding is not supported with this provider”
# (SQLSTATE 0A000). The cluster used SQL_ASCII; two tests create ICU collations that
# require UTF8. Check that one-minute fact before spending 240 minutes.
#
# This is a test-suite precondition, not a product requirement. Production uses only
# built-in collations (Postgres pg_catalog."C", SQLite COLLATE BINARY), independent
# of encoding. Both `provider = icu` occurrences are in _test.go files. Do not force
# encoding in dbsetup.go and restrict customer schemas unnecessarily. This test
# precondition had zero mentions in README, CONTRIBUTING, runbooks, SQL or sqlstore
# comments. An unreachable server does not block: speak only when a reachable
# cluster has an unsuitable template.
PG_DSN="${OLIVARES_TEST_POSTGRES_SUPERUSER_DSN:-postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable}"
if command -v psql >/dev/null 2>&1; then
	# `template1` es la plantilla que hereda un `CREATE DATABASE` sin TEMPLATE explícita, que es
	# exactamente lo que hace el banco (core/internal/pgtest). Se mira ESA, no la base del DSN.
	#
	# La costura de abajo existe SÓLO para que la batería pueda apuntar a una base con otro
	# encoding: la consulta, el camino a psql y la decisión siguen siendo los de producción — lo
	# único inyectado es A QUIÉN se le pregunta. Sin ella, el único caso rojo exigiría re-inicializar
	# el clúster de la caja, y una guarda que sólo se puede probar en verde no está probada.
	PG_TPL="${OLIVARES_PREFLIGHT_TEMPLATE_DB:-template1}"
	# Bound the probe three ways to keep this gate's one-second response contract.
	# Without -w, psql prompts on /dev/tty, beyond stderr suppression, and unattended
	# pushes can hang. connect_timeout bounds libpq's connection wait; timeout(1) also
	# bounds a connected server that stops responding. -X ignores .psqlrc changes to
	# the format required by -tA. `--` keeps a leading-dash duration from becoming an
	# option: timeout --help can otherwise return 0 without ever running psql.
	#
	# Provenance: (`lane-preflight-bounded-probe-0904`, f6d5fdd51), composed with
	# the protocol probe and skip correction. Neither version subsumed the other;
	# b2bc4be1c was retired because its DSN-by-name property existed in both.
	PG_SQL="select pg_encoding_to_char(encoding) from pg_database where datname='$PG_TPL'"
	if command -v timeout >/dev/null 2>&1; then
		PG_OUT="$(PGCONNECT_TIMEOUT=3 timeout -- 10 psql -X -q -w -tA \
			-v ON_ERROR_STOP=1 -d "$PG_DSN" -c "$PG_SQL" 2>/dev/null)"
	else
		PG_OUT="$(PGCONNECT_TIMEOUT=3 psql -X -q -w -tA \
			-v ON_ERROR_STOP=1 -d "$PG_DSN" -c "$PG_SQL" 2>/dev/null)"
	fi
	PG_RC=$?
	PG_ENC="$(printf '%s' "$PG_OUT" | tr -d '[:space:]')"
	# Distinguish an absent server from an unperformed check. psql's rc 2 means it
	# could not connect, a benign expected case. Read that code, not empty output:
	# timeout, hostile .psqlrc or a missing template can also produce silence.
	# From with the same provenance above.
	if [ "$PG_RC" -eq 2 ]; then
		PG_MIRADO=no
		if [ -n "${OLIVARES_TEST_POSTGRES_SUPERUSER_DSN:-}" ]; then
			# ⛔ «SE AUTO-SALTAN» ES FALSO CON UN DSN PUESTO. Medido en el banco, no deducido:
			#    core/internal/pgtest/pgtest.go:289-298, `classify` devuelve `gateRun` CON QUE
			#    `superDSN != ""` — no comprueba que nadie conteste. Decir «se auto-saltan» aqui
			#    afirma lo contrario de lo que hace el codigo, y de esa frase colgaba una asercion
			#    en verde de la bateria.
			echo "check-lane-preflight: PostgreSQL is unreachable, but a superuser DSN is set:" \
				"tests will not skip automatically (pgtest.classify decides based on the presence of the DSN," \
				"core/internal/pgtest/pgtest.go:289-298)."
		else
			echo "check-lane-preflight: PostgreSQL is unreachable and no superuser DSN is set; tests requiring it skip automatically."
		fi
	elif [ "$PG_RC" -ne 0 ]; then
		PG_MIRADO=no
		echo "check-lane-preflight: ⚠ could not check the PostgreSQL template (psql exited $PG_RC$([ "$PG_RC" -eq 124 ] && printf '%s' ", the timeout(1) deadline: the server accepts connections but does not respond")). The cluster may exist; this prerequisite remains unverified." >&2
	elif [ -z "$PG_ENC" ]; then
		PG_MIRADO=no
		echo "check-lane-preflight: ⚠ PostgreSQL responded, but template '$PG_TPL' has no row: this prerequisite remains unverified." >&2
	elif [ "$PG_ENC" != "UTF8" ]; then
		cat >&2 <<AVISO
check-lane-preflight: ⛔ PostgreSQL template $PG_TPL uses $PG_ENC; the test suite requires UTF8.
    Two tests create \`provider = icu\` collations, which require UTF8 databases. This template
    causes SQLSTATE 0A000 failures inside the heavy gate. On 2026-09-03, discovering this
    took 240 minutes; this check can detect it in one second.
    Repair it without reinitializing the cluster or losing its databases. `TEMPLATE template0` is
    required: KERNEL measured this on 2026-09-03 with the preserved SQL_ASCII cluster.
    Without it, `ENCODING 'UTF8'` fails with "new encoding (UTF8) is incompatible with the encoding of
    the template database". `LC_COLLATE` and `LC_CTYPE` are explicit as a precaution: the template0
    locale might be incompatible with UTF8. Their necessity has not been measured here: this machine
    uses C/C.utf8 for template0, which is compatible with every encoding.
        psql "\$OLIVARES_TEST_POSTGRES_SUPERUSER_DSN" -c "UPDATE pg_database SET datistemplate=false WHERE datname='template1'"
        psql "\$OLIVARES_TEST_POSTGRES_SUPERUSER_DSN" -c "DROP DATABASE template1"
        psql "\$OLIVARES_TEST_POSTGRES_SUPERUSER_DSN" -c "CREATE DATABASE template1 TEMPLATE template0 ENCODING 'UTF8' LC_COLLATE 'C' LC_CTYPE 'C'"
        psql "\$OLIVARES_TEST_POSTGRES_SUPERUSER_DSN" -c "UPDATE pg_database SET datistemplate=true WHERE datname='template1'"
    (The DSN is shown by NAME intentionally: this heredoc expands variables, so printing
    \$PG_DSN would expose the superuser password in every push log where template1 is not UTF8.
    If that variable is unset, the gate used its 127.0.0.1 default.)
AVISO
		rc=1
	fi
else
	# ⛔ SIN `psql` NO HE PODIDO MIRAR, Y ESO NO ES «LIMPIO».
	#
	# Toda la sección de arriba vive dentro de `command -v psql`, así que en una caja sin
	# cliente de PostgreSQL este gate CALLABA del todo — y callar se lee como «miré y estaba
	# bien». Es la tercera respuesta del canon: 0 limpio · 1 hallazgo · «no pude mirar», que
	# aquí sale con rc 0 porque esto es advisory y sobre-bloquear cuesta más que avisar, pero
	# se DICE, que es la mitad que faltaba.
	#
	# El coste de no decirlo no fue teórico: el banco exigía la línea «auto-saltan», que sólo
	# se imprime con psql presente, así que `lint:lane-preflight:selftest` quedó ROJA en toda
	# caja sin psql — y va desnuda en el gancho bajo `set -euo pipefail`, o sea que mataba
	# cualquier push. El caso entró desde una caja que sí lo tenía.
	# ⛔ Y LA CONDICIÓN NO ES «HAY psql»: ES «VAN A CORRER LOS TESTS». Corregido el 2026-09-04
	#    tras el contraste de norma de la tanda 7, que señaló el rc 0 — pero la cura no es rc 2
	#    siempre, que mataría todo push en toda caja sin cliente.
	#
	#    El gate condicionaba en `command -v psql` y el banco condiciona en el DSN
	#    (`core/internal/pgtest.classify`: sin DSN devuelve `gateSkip` y la pata NO corre). Son
	#    predicados DISTINTOS, y su diferencia es un agujero alcanzable: **sin `psql` pero CON
	#    DSN** —un servidor en otro contenedor o remoto— los tests SÍ corren, contra una plantilla
	#    que este gate no ha podido mirar, y el fallo llega a las cuatro horas dentro del gate
	#    pesado. Eso es exactamente lo que este fichero existe para evitar.
	#
	#    ⇒ sin DSN no va a correr nada: se DICE y se sale 0, que es lo advisory correcto.
	#      con DSN va a correr: es «no he podido mirar» sobre un sujeto REAL, y sale 2.
	# ⛔ APP/ADMIN SIN SUPERUSUARIO NO ES «NO PUDE MIRAR»: ES UN HALLAZGO, Y DETERMINISTA.
	#    `classify` (core/internal/pgtest/pgtest.go:289-298) devuelve `gateMisconfigured` para
	#    ese caso exacto, y el banco lo trata con `tb.Fatal` — o sea que las patas MUEREN, seguro.
	#    Contestar 2 ahi degrada un hallazgo accionable a incertidumbre y rompe el contrato
	#    0/1/2 del canon. Señalado por el contraste (MEDIO-01).
	if [ -z "${OLIVARES_TEST_POSTGRES_SUPERUSER_DSN:-}" ] &&
		{ [ -n "${OLIVARES_TEST_POSTGRES_DSN:-}" ] || [ -n "${OLIVARES_TEST_POSTGRES_ADMIN_DSN:-}" ]; }; then
		cat >&2 <<'AVISO'
check-lane-preflight: ⛔ An application or admin DSN is configured, but no superuser DSN.
This is a known misconfiguration: `pgtest.classify` (core/internal/pgtest/pgtest.go:289-298) returns
`gateMisconfigured` deterministically, and the tests terminate with `tb.Fatal`. The PostgreSQL legs
will fail rather than skip.
Repair it using either option:
    export OLIVARES_TEST_POSTGRES_SUPERUSER_DSN
    or unset the other two DSNs if you did not intend to run that leg
AVISO
		PG_MIRADO=no
		exit 1
	fi
	if [ -n "${OLIVARES_TEST_POSTGRES_SUPERUSER_DSN:-}" ]; then
		cat >&2 <<'AVISO'
check-lane-preflight: ⛔ COULD NOT CHECK: a PostgreSQL DSN is configured but `psql` is unavailable
    on this machine. PostgreSQL tests will run against a server whose template could not be
    checked. If it uses SQL_ASCII, they fail with SQLSTATE 0A000 inside the heavy gate:
    on 2026-09-03, this cost 240 minutes.
    Repair it using either option:
        install the client (apt-get install -y postgresql-client)
        or unset the DSN if you did not intend to run that leg
AVISO
		PG_MIRADO=no
		exit 2
	fi
	echo "check-lane-preflight: neither \`psql\` nor a DSN is available; PostgreSQL tests will not run" \
		"(pgtest.classify returns gateSkip), so there is no template to inspect."
	PG_MIRADO=no
fi

# ── 4 · SI UNA PATA PESADA NECESITA POSTGRES, ¿HAY ALGO ESCUCHANDO? ───────────────────────────
# ⛔ POR QUE, medido el 2026-09-04 sobre el gate de la tanda 7 en la caja 3: TRES patas salieron
#    rojas en 0 s por no haber PostgreSQL, y este fichero —que existe para decir eso en el segundo
#    1— no dijo nada. La seccion de arriba mira el ENCODING de la plantilla, que solo importa si hay
#    servidor; esta mira si lo hay.
#
#    Y el mecanismo contradice lo que este mismo fichero dice mas arriba, asi que va escrito:
#    «sin DSN no corre nada» es cierto FUERA del gancho y FALSO dentro. El gancho exporta
#    `OLIVARES_PG_LOCAL_DEFAULTS=1`, y con eso `pg-test-env.sh` SINTETIZA las tres DSN hacia
#    127.0.0.1:5432. Sin `psql` no puede sondear, asi que las exporta y sale 0 igual; `pgtest` ve un
#    DSN, clasifica `gateRun`, intenta conectar y muere. `with-pg-env.sh` lo dice en su propio texto:
#    «refusing to run tests with an unknown Postgres posture».
#
# ⇒ La sonda es un TCP a donde apunte la DSN que el gancho sintetizaria. No necesita `psql` —que es
#   justo la caja donde el fallo ocurre— y no le pregunta nada a la base: pregunta si hay algo
#   escuchando, que es la unica condicion que mata a esas patas.
#
# ⛔ EL CENSO NO VEIA LAS PATAS INDENTADAS NI LAS QUE LLEVAN PREFIJO DE ENTORNO. El regex era
#    `^task`, que sobre el gancho de hoy ve 259 de 264: cinco invisibles, y una de ellas es la
#    llamada indentada a `task test` — el banco general, que TOCA Postgres. Señalado por el
#    contraste (ALTO-02) y medido aqui: `^task` 259 · con indentacion y prefijo 264.
#    Es el mismo punto ciego que `CLAUDE.md` documenta para contar las patas del gancho, y por
#    el mismo motivo: un regex anclado a columna 0 mide la COLUMNA, no la invocacion.
#
# NO SOBRE-BLOQUEA: solo habla si el gancho llama a una pata PESADA envuelta en `with-pg-env.sh`.
PATAS_PG=""
while IFS= read -r t; do
	[ -n "$t" ] || continue
	if grep -q "with-pg-env.sh" <<<"$(awk -v T="  $t:" '
		$0==T {f=1; next}
		f && /^  [a-zA-Z0-9]/ {exit}
		f {print}' Taskfile.yml)"; then
		PATAS_PG="$PATAS_PG $t"
	fi
done <<LISTA
$(sed -n '/running the FULL gate locally/,$p' "$HOOK" | grep -oE '^[[:space:]]*([A-Za-z_][A-Za-z0-9_]*=[^[:space:]]+[[:space:]]+)*task [a-z0-9:._-]+' | sed -E 's/.*task //')
LISTA

if [ -n "$PATAS_PG" ]; then
	# ⛔ EL VALOR EFECTIVO, NO EL DEFAULT DEL TEXTO. La primera version sacaba el `${VAR:-default}`
	#    con un `sed`, y eso da el DEFAULT — asi que con la DSN ya puesta en el entorno leia el
	#    valor equivocado, y cuando el helper cambiaba de forma no leia nada y callaba. Se evalua su
	#    salida en un subshell y se lee la variable, que es exactamente lo que veran las patas.
	# ⛔ CAPTURAR, COMPROBAR, Y SOLO DESPUES EVALUAR — nunca `eval "$(...)" || true`.
	#    La forma anterior perdia el estado del productor por partida doble: `eval` reporta SU
	#    propio rc (asi que un helper que muere con 2 llega al siguiente comando como 0, incluso
	#    bajo `set -e`), y el `|| true` remataba lo que quedara. Encima `2>/dev/null` borraba el
	#    motivo. Resultado: si `pg-test-env.sh` fallaba, el preflight imprimia «no he podido
	#    derivar... no lo compruebo» y terminaba en el OK COMPLETO — aprobando una postura que el
	#    gancho RECHAZA con la forma correcta (`.githooks/pre-push:2169-2172`, que sale 1).
	#
	#    Testigo estatico y alcanzable, y es el que usa la bateria: `OLIVARES_PG_PROBE=bogus`.
	#    El helper lo rechaza con 2 (`scripts/pg-test-env.sh:79-86`) y la forma vieja lo convertia
	#    en exito. El repositorio ya documenta por que esa forma pierde el estado
	#    (`scripts/pg-test-env.sh:66-69`, `scripts/with-pg-env.sh:9-17`): esto solo lo aplica aqui.
	PG_HELPER_RC=0
	PG_DSN_EFEC="$(OLIVARES_PG_LOCAL_DEFAULTS=1 bash -c '
		exports="$(bash "$1/scripts/pg-test-env.sh")" || exit $?
		# ⛔ Y EL `eval` TAMBIEN DECIDE. Un helper puede salir 0 y emitir shell INVALIDO —una
		#    comilla sin cerrar, un parentesis de mas—: entonces `eval` falla, el `printf` de la
		#    linea siguiente corre igual y el subshell termina con el rc de `printf`, que es 0.
		#    Reproducido: rc 0 con la DSN VACIA, o sea el preflight imprimiendo OK donde el gancho
		#    se moriria. Es la misma clase que el `|| true` que este bloque acaba de retirar —el
		#    estado del productor perdido por el comando que viene detras—, una linea mas abajo.
		eval "$exports" || exit 3
		printf "%s" "${OLIVARES_TEST_POSTGRES_SUPERUSER_DSN:-}"' _ "$RAIZ")" || PG_HELPER_RC=$?
	if [ "$PG_HELPER_RC" -ne 0 ]; then
		cat >&2 <<AVISO
check-lane-preflight: ⛔ COULD NOT CHECK: \`scripts/pg-test-env.sh\` exited $PG_HELPER_RC, so
the PostgreSQL server used by these heavy test legs is unknown:$PATAS_PG
The check could not complete. For the same failure, the hook refuses to run the gate
(\`.githooks/pre-push:2169-2172\`). Allowing it here would leave this configuration unchecked
until minute 45.
AVISO
		PG_MIRADO=no
		exit 2
	fi
	PG_HP="$(printf '%s' "$PG_DSN_EFEC" | sed -E 's#^[a-z]+://[^@]*@##; s#[/?].*##')"
	if [ -z "$PG_HP" ]; then
		echo "check-lane-preflight: could not derive the PostgreSQL host from the hook; not checked."
	# ⛔ UN `connect` NO PRUEBA QUE SEA POSTGRES, y la primera version aceptaba cualquier listener.
	#    Un tunel SSH, un contenedor rancio o cualquier proceso con el 5432 abierto satisfacian este
	#    gate; y `classify` (pgtest.go:289-298) hace correr la pata por la sola PRESENCIA del DSN.
	#    Resultado: el preflight callaba y las patas morian a los 45 minutos — el coste exacto que
	#    este fichero existe para no pagar.
	#
	#    Se le pide al que escucha lo minimo que solo Postgres contesta: un SSLRequest (8 bytes,
	#    longitud 8 + codigo 80877103). Un servidor Postgres responde UN byte, 'S' o 'N'. Cualquier
	#    otra cosa —silencio, cierre, o bytes distintos— no habla Postgres. No hace falta ninguna
	#    credencial, asi que esto NO comprueba que el DSN autentique ni que la plantilla sea UTF8:
	#    eso lo mira la seccion de arriba con `psql`, y su alcance se declara ahi.
	# ⛔ UN SOLO BYTE NO BASTA, y el contraste lo cazo con el ejemplo exacto del encargo: la bandera
	#    de SSH es «SSH-2.0-…» y EMPIEZA POR 'S', una de las dos respuestas validas al SSLRequest,
	#    asi que un tunel SSH pasaba por PostgreSQL. Reproducido antes de curar (rc 0 sobre un
	#    listener que anuncia SSH-2.0-OpenSSH). La fila HTTP de la bateria no lo cubria porque HTTP
	#    empieza por 'H': una sonda acertaba por la inicial, no por la propiedad.
	#
	#    Lo que separa de verdad: PostgreSQL contesta al SSLRequest EXACTAMENTE UN BYTE y despues
	#    SE CALLA, esperando el StartupMessage; un servidor de banner sigue enviando. El silencio
	#    posterior es parte de la firma, asi que se comprueba.
	elif OLIVARES_PREFLIGHT_HP="$PG_HP" python3 -c '
import os, socket, struct, sys, time
hp = os.environ["OLIVARES_PREFLIGHT_HP"]
h, _, p = hp.rpartition(":")
# ⛔ UN TIMEOUT NO ES AUSENCIA. `except OSError` reunia rechazo, DNS, timeout y cierre en una
# sola respuesta —«nadie escucha»— y un listener que acepta y se calla salia rc 1 con el
# diagnostico equivocado. Se separan: rechazo/DNS = nadie escucha (1); timeout = algo hay y no
# contesta (2). Y hay PLAZO TOTAL, porque un instrumento que promete el segundo 1 no puede
# quedarse esperando la suma de sus cotas parciales. (Contraste, MEDIO-04.)
PLAZO = 6.0
t0 = time.monotonic()
def resta():
    return max(0.2, PLAZO - (time.monotonic() - t0))
try:
    s = socket.create_connection((h or "127.0.0.1", int(p or 5432)), timeout=min(2.0, resta()))
except socket.timeout:
    sys.exit(2)                                       # algo hay y no completa el saludo
except (OSError, ValueError):
    sys.exit(1)                                       # nadie escucha (rechazo, DNS, ruta)
try:
    s.settimeout(min(2.0, resta()))
    s.sendall(struct.pack("!ii", 8, 80877103))        # SSLRequest
    r = s.recv(1)
    if r not in (b"S", b"N"):
        sys.exit(2)                                   # contesta otra cosa: no es Postgres
    s.settimeout(min(0.5, resta()))
    try:
        extra = s.recv(1)
    except socket.timeout:
        extra = b""                                   # el silencio ES la firma de Postgres
    except OSError:
        sys.exit(2)
    sys.exit(2 if extra else 0)
except (OSError, ValueError):
    sys.exit(2)
finally:
    try: s.close()
    except OSError: pass
'; then
		:
	elif [ $? -eq 2 ]; then
		cat >&2 <<AVISO
check-lane-preflight: ⛔ A listener on $PG_HP does not speak PostgreSQL (no SSLRequest response).
    The heavy gate invokes legs that REQUIRE PostgreSQL:$PATAS_PG
    They will not skip: \`pgtest.classify\` uses the presence of a DSN
    (core/internal/pgtest/pgtest.go:289-298), regardless of server availability. They will
    run against this listener and fail inside the heavy gate.
    Repair it using either option:
        start a PostgreSQL server at $PG_HP (the current listener is not PostgreSQL)
        or point OLIVARES_TEST_POSTGRES_SUPERUSER_DSN to an existing server
AVISO
		rc=1
	else
		cat >&2 <<AVISO
check-lane-preflight: ⛔ No listener on $PG_HP; the heavy gate invokes legs that REQUIRE
    PostgreSQL:$PATAS_PG
    They will not skip: \`with-pg-env.sh\` refuses an unknown configuration and exits 1.
    They fail immediately after the entire fast lane. On 2026-09-04, discovering this cost
    a complete gate run on machine 3 (three failures, 13/16).
    Repair it using either option:
        start a PostgreSQL server at $PG_HP
        or point OLIVARES_TEST_POSTGRES_SUPERUSER_DSN to an existing server
AVISO
		rc=1
	fi
fi

# ⛔ LA LÍNEA FINAL NO RECLAMA MÁS DE LO QUE SE COMPROBÓ. Decía «OK — …» detrás de un «no he
#    podido mirar», y un veredicto compuesto que enumera lo que SÍ miró se lee como completo.
if [ "$rc" -eq 0 ]; then
	# ⛔ UNA PATA QUE VA A CORRER CON LA POSTURA SIN VERIFICAR ES «NO HE PODIDO MIRAR», NO UN VERDE.
	#    Hasta aqui el guion podia terminar en «OK en lo que pude mirar» habiendo (a) decidido que
	#    no podia comprobar la plantilla y (b) descubierto DESPUES que el gancho SI sintetiza una
	#    DSN para una pata pesada. Las dos mitades eran ciertas y juntas dan un verde sobre lo que
	#    no se miro: `pgtest.classify` hace correr la pata por la PRESENCIA del DSN
	#    (core/internal/pgtest/pgtest.go:289-298) y una `template1` SQL_ASCII detras de un listener
	#    sano cuesta los 240 minutos que este fichero cifra en su cabecera.
	#
	#    La condicion no es «no pude mirar» a secas —eso sigue siendo advisory cuando no va a
	#    correr nada— sino «no pude mirar Y va a correr».
	# ⛔ «ESTA ENVUELTA» NO ES «VA A CORRER CONTRA POSTGRES», y la primera version de esta guarda
	#    confundia las dos. Lo midio el contraste Codex de esta cadena y lo REPRODUJE antes de
	#    curarlo: con una pata envuelta, `psql` presente, ningun servidor alcanzable y sin DSN, la
	#    base contesta rc 0 y mi sujeto contestaba rc 2 — un SOBRE-BLOQUEO, y esta pata va desnuda
	#    en el gancho bajo `set -e`, o sea que habria matado pushes en esa caja.
	#
	#    En ese estado el helper no emite ninguna DSN, asi que `pgtest.classify` devuelve `gateSkip`
	#    y no corre NADA contra Postgres: no hay nada que proteger. La senal correcta no es que la
	#    pata este envuelta, sino que exista una DSN EFECTIVA — la que veran las patas.
	#
	# ⚠ HUECO DECLARADO, no tapado: el segundo termino sigue siendo el DSN de ambiente, porque el
	#    banco general (`task test`, .githooks/pre-push:2237-2243) toca Postgres sin aparecer en
	#    `PATAS_PG`; con DSN puesto algo va a correr aunque no haya pata envuelta.
	if [ "${PG_MIRADO:-si}" = "no" ] && { [ -n "${PG_DSN_EFEC:-}" ] || [ -n "${OLIVARES_TEST_POSTGRES_SUPERUSER_DSN:-}" ]; }; then
		cat >&2 <<AVISO
check-lane-preflight: ⛔ COULD NOT CHECK the PostgreSQL configuration; the heavy gate invokes
legs that require it:${PATAS_PG:- (none wrapped, but a DSN is configured)}
An active listener does not establish a usable session: nonexistent databases, roles without login,
or incorrect passwords also accept connections (\`scripts/pg-test-env.sh:169-172\`). A SQL_ASCII
\`template1\` behind a healthy listener can terminate suites with SQLSTATE 0A000 after 240 minutes.
Repair it using either option:
    install the client to allow verification (apt-get install -y postgresql-client)
    or remove PostgreSQL legs from the hook if you did not intend to run them
AVISO
		exit 2
	fi
	if [ "${PG_MIRADO:-si}" = "no" ]; then
		echo "check-lane-preflight: OK for the checks completed — TMPDIR is outside the repository and" \
			"supports execution; no dependencies are missing. The PostgreSQL template was not checked." \
			"No heavy check needs it, so nothing will run against it."
	else
		echo "check-lane-preflight: OK — TMPDIR is outside the repository and supports execution; no dependencies missing."
	fi
fi
exit "$rc"
