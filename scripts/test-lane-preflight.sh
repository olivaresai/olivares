#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# test-lane-preflight.sh — la batería de check-lane-preflight.sh.
#
# ⛔ SEÑUELOS, NUNCA EL ENTORNO VIVO. Las propiedades rojas de este gate son «TMPDIR dentro de un
#    repositorio», «TMPDIR noexec» y «falta node_modules», y probarlas contra el worktree real
#    exigiría romperlo. Cada caso monta su propio repositorio desechable con su gancho y su
#    Taskfile mínimos, así que la batería es determinista y no depende de qué carril corra hoy.
#
# ⚠ Y EL SEÑUELO LLEVA LO QUE EL SUJETO LEE: el gate deriva su lista del GANCHO (`task <pata>` en
#   la sección del gate pesado) y del `Taskfile.yml` (`private-leg.sh <pata> <dir>`). Un fixture
#   sin esas dos mitades probaría que el gate no encuentra nada, no que sabe mirar.
#
# Salida: 0 todas pasan · 1 alguna falla · 2 no se pudo montar el banco.
set -uo pipefail
LC_ALL=C
export LC_ALL

# Aislamiento de git OBLIGATORIO en esta clase: este fichero empareja `mktemp -d` con `git`, y sin
# esto un GIT_DIR envenenado haria que el sandbox operase sobre el repositorio REAL. Lo exige
# `lint:git-env` y va ANTES del primer mktemp, que es donde sirve.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

RAIZ="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)"
GATE="$RAIZ/scripts/check-lane-preflight.sh"
[ -r "$GATE" ] || {
	echo "test-lane-preflight: ⛔ NO HE PODIDO MIRAR: no existe $GATE" >&2
	exit 2
}
# El banco va FUERA del repositorio a propósito: dentro, el caso «TMPDIR fuera de un repo» sería
# imposible de montar — que es exactamente la trampa que este gate existe para cazar.
BANCO="$(mktemp -d "${TMPDIR:-/tmp}/lpf-XXXXXX")" || exit 2
trap 'rm -rf "$BANCO"' EXIT

pasan=0
fallan=0
comprobar() {
	if [ "$3" -eq "$2" ]; then
		printf '  ok    %-58s rc=%s\n' "$1" "$3"
		pasan=$((pasan + 1))
	else
		printf '  FALLA %-58s rc=%s (quiere %s)\n' "$1" "$3" "$2"
		fallan=$((fallan + 1))
	fi
}
dice() {
	if grep -q "$2" "$BANCO/out.log" 2>/dev/null; then
		printf '  ok    %-58s lo NOMBRA\n' "$1"
		pasan=$((pasan + 1))
	else
		printf '  FALLA %-58s no dice «%s»\n' "$1" "$2"
		fallan=$((fallan + 1))
	fi
}

# monta un repositorio desechable con gancho + Taskfile que declaran UNA pata privada
monta_repo() {
	R="$BANCO/repo"
	rm -rf "$R"
	mkdir -p "$R/.githooks" "$R/priv"
	git -C "$R" init -q 2>/dev/null || return 1
	printf 'pre-push: running the FULL gate locally (build + test + web).\ntask test:priv\n' >"$R/.githooks/pre-push"
	printf '  test:priv:\n    cmds:\n      - bash scripts/private-leg.sh test:priv priv npm run typecheck\n  otra:\n' >"$R/Taskfile.yml"
	printf '{"dependencies":{"x":"1"}}\n' >"$R/priv/package.json"
	git -C "$R" add -A >/dev/null 2>&1
	git -C "$R" -c user.email=b@b -c user.name=b commit -qm x >/dev/null 2>&1
}

correr() { # correr <tmpdir>
	(cd "$BANCO/repo" && TMPDIR="$1" bash "$GATE") >"$BANCO/out.log" 2>&1
}

monta_repo || { echo "test-lane-preflight: ⛔ NO HE PODIDO MIRAR: no pude montar el repo señuelo" >&2; exit 2; }

FUERA="$BANCO/tmp-fuera"
mkdir -p "$FUERA"

# ── 1 · EL DEFECTO QUE TRAJO ESTE GATE: falta node_modules de una pata pesada ──────────────────
correr "$FUERA"
comprobar "sin node_modules de una pata pesada, RECHAZA" 1 "$?"
dice "y NOMBRA el directorio" "priv"

# ── 2 · SUELO: con node_modules, pasa ─────────────────────────────────────────────────────────
mkdir -p "$BANCO/repo/priv/node_modules"
correr "$FUERA"
comprobar "con node_modules, pasa" 0 "$?"

# ── 3 · NO SOBRE-BLOQUEA: un package.json que NINGUNA pata pesada usa no cuenta ────────────────
# La primera versión de este gate exigía node_modules en TODO package.json con dependencias y
# marcaba seis directorios en un árbol cuyo push estaba pasando. Este caso lo impide.
mkdir -p "$BANCO/repo/otro"
printf '{"dependencies":{"y":"1"}}\n' >"$BANCO/repo/otro/package.json"
git -C "$BANCO/repo" add -A >/dev/null 2>&1
git -C "$BANCO/repo" -c user.email=b@b -c user.name=b commit -qm y >/dev/null 2>&1
correr "$FUERA"
comprobar "un package.json que ninguna pata pesada usa NO bloquea" 0 "$?"

# ── 3-bis · POSTGRES: la plantilla del clúster (a repository gate) ──────────────────────────────────────
# Se ejercita contra el servidor REAL si lo hay, apuntando la costura a bases señuelo que esta
# batería crea y borra. Lo inyectado es A QUIÉN se pregunta; la consulta y la decisión son las de
# producción. Sin servidor NO se dan por buenos: se declara que no se ejercitaron.
PGP="${PGPASSWORD:-postgres}"
export PGPASSWORD="$PGP"
PGDSN="${OLIVARES_TEST_POSTGRES_SUPERUSER_DSN:-postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable}"
if command -v psql >/dev/null 2>&1 && psql "$PGDSN" -tAc 'select 1' >/dev/null 2>&1; then
	SENUELO="lpf_latin1_$$"
	psql "$PGDSN" -q -c "DROP DATABASE IF EXISTS $SENUELO" >/dev/null 2>&1
	if psql "$PGDSN" -q -c "CREATE DATABASE $SENUELO TEMPLATE template0 ENCODING 'LATIN1' LC_COLLATE 'C' LC_CTYPE 'C'" >/dev/null 2>&1; then
		(cd "$BANCO/repo" && TMPDIR="$FUERA" OLIVARES_PREFLIGHT_TEMPLATE_DB="$SENUELO" bash "$GATE") >"$BANCO/out.log" 2>&1
		comprobar "plantilla LATIN1, RECHAZA" 1 "$?"
		dice "y nombra el encoding que encontro" "LATIN1"
		# ⛔ EL TESTIGO ERA `datistemplate` A SECAS Y UN MUTANTE LO SOBREVIVIO (2026-09-03): la cura
		#    lleva DOS lineas con esa palabra, asi que borrar una dejaba pasar la comprobacion.
		#    Un testigo que es subcadena de otra cosa comprueba la prosa, no la propiedad. Se
		#    ancla a las dos mitades que hacen la cura CORRECTA: cambiar el encoding, y RESTAURAR
		#    la plantilla — un mensaje que manda DROP sin el restore deja el cluster roto.
		dice "y la cura cambia el encoding" "TEMPLATE template0"
		dice "y la cura RESTAURA la plantilla" "datistemplate=true"
		psql "$PGDSN" -q -c "DROP DATABASE $SENUELO" >/dev/null 2>&1
	else
		printf '  AVISO no pude crear la base senuelo LATIN1: el caso rojo NO se ejercito\n'
	fi
	# y el verde: la plantilla real de la caja, que hoy debe ser UTF8
	(cd "$BANCO/repo" && TMPDIR="$FUERA" bash "$GATE") >"$BANCO/out.log" 2>&1
	rc_utf8=$?
	enc_real="$(psql "$PGDSN" -tAc "select pg_encoding_to_char(encoding) from pg_database where datname='template1'" 2>/dev/null | tr -d '[:space:]')"
	if [ "$enc_real" = "UTF8" ]; then
		comprobar "plantilla UTF8 real, PASA" 0 "$rc_utf8"
	else
		printf '  AVISO template1 de esta caja es %s, no UTF8: el caso verde NO se ejercito\n' "$enc_real"
	fi
else
	printf '  AVISO sin PostgreSQL alcanzable: los 4 casos de plantilla NO se ejercitaron\n'
fi

# ── 3-ter · NO SOBRE-BLOQUEA: sin servidor, la seccion de Postgres calla y deja pasar ──────────
(cd "$BANCO/repo" && TMPDIR="$FUERA" \
	OLIVARES_TEST_POSTGRES_SUPERUSER_DSN='postgres://x:x@127.0.0.1:5999/x?sslmode=disable&connect_timeout=2' \
	bash "$GATE") >"$BANCO/out.log" 2>&1
rc_inalcanzable=$?
# ⛔ ESTE CASO SOLO VALE CON `psql` INSTALADO, y sin la guarda repetia el defecto que el comentario
# de abajo documenta. Con DSN configurado y SIN cliente, la cura de la tanda 7 responde «no he
# podido mirar» (2) A PROPOSITO: los tests de Postgres VAN a correr y su plantilla no la ha visto
# nadie. Exigir 0 ahi es exigir el comportamiento de una caja CON cliente en una que no lo tiene, y
# esta pata va DESNUDA en el gancho: dejaba rojo todo push desde cualquier caja sin psql.
if command -v psql >/dev/null 2>&1; then
	# ⛔ ESTA ASERCION EXIGIA 0 Y CERTIFICABA EL DEFECTO. Corregida 2026-09-04 (r27) tras el
	#    HIGH-01 del tercer contraste de. El fixture PONE un DSN de superusuario
	#    (127.0.0.1:5999, donde no hay nada) y exigia verde con el mensaje «se auto-saltan».
	#    Ese mensaje es FALSO: `core/internal/pgtest/pgtest.go:289-298` hace `gateRun` con que
	#    `superDSN != ""`, sin comprobar que nadie conteste. Con el DSN puesto la pata VA a
	#    correr contra una plantilla que nadie miro, que es la clase de los 240 minutos. Un
	#    banco que exige el verde del defecto no lo pasa por alto: lo CERTIFICA.
	comprobar "con DSN inalcanzable, NO es verde: es NO HE PODIDO MIRAR" 2 "$rc_inalcanzable"
else
	comprobar "sin psql y con DSN inalcanzable, NO HE PODIDO MIRAR" 2 "$rc_inalcanzable"
fi
# ⛔ EL BANCO DISTINGUE POR MECANISMO, NO POR LA CADENA QUE ESPERA VER.
#
# La línea «auto-saltan» sólo se imprime DENTRO de `command -v psql`: con un DSN inalcanzable
# y psql presente, el gate pregunta, no obtiene encoding y lo dice. Sin psql no hay a quién
# preguntar, y el gate dice otra cosa — «no he podido mirar». Exigir la primera en una caja sin
# cliente dejó esta batería ROJA en toda máquina sin psql, y va desnuda en el gancho: mataba
# cualquier push. El caso entró desde una caja que sí lo tenía, que es como no se ve.
#
# El AVISO no es cosmético: un banco que pasa por SALTARSE el caso no vale, y uno que dice qué
# no pudo ejercitar, sí. Es el mismo patrón del caso 3 con sus cuatro plantillas.
if command -v psql >/dev/null 2>&1; then
	# El literal cambia con la asercion de arriba y por la misma razon: el gate ya no puede
	# decir «se auto-saltan» cuando hay DSN, porque no es verdad.
	dice "y dice que NO se auto-saltan" "NO se auto-saltan"
else
		# El literal se COPIA del gate (check-lane-preflight.sh), no se parafrasea: alli va en
		# MAYUSCULAS y `dice` compara con grep sensible a mayusculas, asi que la version en
		# minusculas no casa con NADA y el caso acusaba al gate de callar cuando si habla.
	dice "y DICE que no pudo mirar la plantilla" "NO HE PODIDO MIRAR: hay un DSN de PostgreSQL"
	printf '  AVISO sin psql en esta caja: la rama del DSN INALCANZABLE no se ejercitó\n'
fi

# ── 3-quater · SIN `psql`: la condición es «¿van a correr los tests?», no «¿hay cliente?» ─────
# ⛔ POR QUÉ, medido el 2026-09-04 por el contraste de norma de la tanda 7 y AFINADO al medirlo:
#    el gate condicionaba en `command -v psql` y el banco condiciona en el DSN
#    (`core/internal/pgtest.classify` → `gateSkip` sin DSN). Predicados distintos ⇒ un hueco
#    alcanzable: SIN psql pero CON DSN los tests corren contra una plantilla que nadie miró, y el
#    fallo llega a las cuatro horas. El contraste pedía rc 2 SIEMPRE; eso mataría todo push en
#    toda caja sin cliente, así que la cura discrimina por el DSN. Los dos casos van aquí porque
#    una guarda con dos ramas y un solo caso está probada a medias.
#
# El señuelo es un PATH sin `psql`: es lo que produce una caja sin cliente, sin desinstalar nada.
SINPSQL="$BANCO/bin-sin-psql"
mkdir -p "$SINPSQL"
for h in sh bash env git python3 mktemp rm mkdir chmod grep sed awk printf cat tr wc; do
	o="$(command -v "$h" 2>/dev/null)" && [ -n "$o" ] && ln -sf "$o" "$SINPSQL/$h" 2>/dev/null
done
if PATH="$SINPSQL" command -v psql >/dev/null 2>&1; then
	printf '  AVISO el senuelo de PATH aun ve psql: los 2 casos sin cliente NO se ejercitaron\n'
else
	# (a) sin psql y SIN DSN: nada va a correr ⇒ 0, y lo DICE
	(cd "$BANCO/repo" && PATH="$SINPSQL" TMPDIR="$FUERA" \
		OLIVARES_TEST_POSTGRES_SUPERUSER_DSN= OLIVARES_TEST_POSTGRES_DSN= OLIVARES_TEST_POSTGRES_ADMIN_DSN= \
		"$SINPSQL/bash" "$GATE") >"$BANCO/out.log" 2>&1
	comprobar "sin psql y sin DSN, NO bloquea" 0 "$?"
	dice "y dice que la pata no va a correr" "no va a correr"
	dice "y la linea final NO reclama haber mirado la plantilla" "NO se comprobo\|NO se comprobó"

	# (b) sin psql y CON DSN: los tests SI corren ⇒ 2, no 0 y no 1
	(cd "$BANCO/repo" && PATH="$SINPSQL" TMPDIR="$FUERA" \
		OLIVARES_TEST_POSTGRES_SUPERUSER_DSN='postgres://x:x@127.0.0.1:5432/x?sslmode=disable' \
		"$SINPSQL/bash" "$GATE") >"$BANCO/out.log" 2>&1
	comprobar "sin psql y CON DSN, es NO HE PODIDO MIRAR (2)" 2 "$?"
	dice "y nombra el motivo" "NO HE PODIDO MIRAR"
	dice "y ofrece las DOS salidas" "desconfigura el DSN"
fi

# ── 3-quinquies · UNA PATA PESADA QUE EXIGE POSTGRES, SIN NADIE ESCUCHANDO ────────────────────
# ⛔ POR QUE, medido el 2026-09-04: el gate de la tanda 7 en la caja 3 dio 13/16 con TRES rojas en
#    0 s por no haber PostgreSQL, y este gate no dijo nada. El mecanismo NO es que los tests fallen
#    —`pgtest.classify` los saltaria— sino que el envoltorio `with-pg-env.sh` REHUSA correr con una
#    postura desconocida y sale 1. Y bajo el gancho siempre hay DSN, porque `OLIVARES_PG_LOCAL_DEFAULTS=1`
#    hace que `pg-test-env.sh` la SINTETICE.
#
# El senuelo trae las dos mitades que el gate lee —una pata pesada del gancho envuelta en
# `with-pg-env.sh`, y un `pg-test-env.sh` propio que emite la DSN— porque un fixture sin ellas
# probaria que el gate no encuentra nada, no que sabe mirar.
LPG="$BANCO/lab-pg"
rm -rf "$LPG"; mkdir -p "$LPG/.githooks" "$LPG/scripts"
printf 'pre-push: running the FULL gate locally (build + test + web).\ntask test:pesada-pg\n' >"$LPG/.githooks/pre-push"
printf '  test:pesada-pg:\n    cmds:\n      - bash scripts/with-pg-env.sh go test ./...\n  otra:\n' >"$LPG/Taskfile.yml"
git -C "$LPG" init -q 2>/dev/null
emite_dsn() { # emite_dsn <puerto>
	printf '#!/usr/bin/env bash\nprintf %%s\\\\n "export OLIVARES_TEST_POSTGRES_SUPERUSER_DSN=\\"postgres://u:p@127.0.0.1:%s/db?sslmode=disable\\""\n' "$1" >"$LPG/scripts/pg-test-env.sh"
	chmod +x "$LPG/scripts/pg-test-env.sh"
}

# (a) nadie escucha en ese puerto -> RECHAZA y NOMBRA la pata
emite_dsn 5999
(cd "$LPG" && TMPDIR="$FUERA" bash "$GATE") >"$BANCO/out.log" 2>&1
comprobar "pata pesada con Postgres y nadie escuchando, RECHAZA" 1 "$?"
dice "y NOMBRA la pata que morira" "test:pesada-pg"
dice "y dice que NO se saltara" "with-pg-env.sh"

# (b) NO sobre-bloquea: sin ninguna pata pesada envuelta, calla
printf '  test:sin-pg:\n    cmds: ["true"]\n  otra:\n' >"$LPG/Taskfile.yml"
printf 'pre-push: running the FULL gate locally (build + test + web).\ntask test:sin-pg\n' >"$LPG/.githooks/pre-push"
(cd "$LPG" && TMPDIR="$FUERA" bash "$GATE") >"$BANCO/out.log" 2>&1
comprobar "sin patas pesadas que usen Postgres, NO bloquea" 0 "$?"
if grep -q "nadie escucha" "$BANCO/out.log" 2>/dev/null; then
	printf '  FALLA %-58s habla de Postgres sin que ninguna pata lo use\n' "y no habla de Postgres"
	fallan=$((fallan + 1))
else
	printf '  ok    %-58s calla\n' "y no habla de Postgres"
	pasados_pg=1; pasan=$((pasan + 1))
fi

# (c) ALGO ESCUCHA Y NO ES POSTGRES -> RECHAZA. Anadido 2026-09-04 (r27) por el HIGH-01 del tercer
#     contraste de: la sonda era un `connect` a secas, asi que un tunel, un contenedor rancio
#     o cualquier proceso con ese puerto abierto la satisfacian, y las patas morian a los 45 min.
#     El senuelo acepta la conexion y contesta algo que NO es la respuesta al SSLRequest.
printf '  test:pesada-pg:\n    cmds:\n      - bash scripts/with-pg-env.sh go test ./...\n  otra:\n' >"$LPG/Taskfile.yml"
printf 'pre-push: running the FULL gate locally (build + test + web).\ntask test:pesada-pg\n' >"$LPG/.githooks/pre-push"
cat >"$BANCO/no-pg.py" <<'PYSRV'
import socket, sys, threading, time
srv = socket.socket(); srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind(("127.0.0.1", 0)); srv.listen(4)
open(sys.argv[1], "w").write(str(srv.getsockname()[1]))
def serve():
    while True:
        try: c, _ = srv.accept()
        except OSError: return
        try:
            c.recv(64); c.sendall(b"HTTP/1.1 400 Bad Request\r\n")
            time.sleep(1.0)
        except OSError: pass
        finally: c.close()
threading.Thread(target=serve, daemon=True).start()
time.sleep(30)
PYSRV
if command -v python3 >/dev/null 2>&1; then
	python3 "$BANCO/no-pg.py" "$BANCO/puerto" & NOPG_PID=$!
	for _ in 1 2 3 4 5 6 7 8 9 10; do [ -s "$BANCO/puerto" ] && break; sleep 0.3; done
	NOPG_PUERTO="$(cat "$BANCO/puerto" 2>/dev/null)"
	if [ -n "$NOPG_PUERTO" ]; then
		emite_dsn "$NOPG_PUERTO"
		(cd "$LPG" && TMPDIR="$FUERA" bash "$GATE") >"$BANCO/out.log" 2>&1
		comprobar "algo escucha y NO habla Postgres (HTTP), RECHAZA" 1 "$?"
		dice "y dice que no contesta al SSLRequest" "NO habla PostgreSQL"
		# ⛔ Y EL CASO QUE DE VERDAD DUELE: un listener SSH. Su bandera «SSH-2.0-…» EMPIEZA POR 'S',
		#    que es una de las dos respuestas validas al SSLRequest, asi que una sonda de un solo
		#    byte lo acepta como PostgreSQL. Es el «tunel SSH que satisface el preflight» del
		#    encargo, y la fila HTTP no lo cubre porque HTTP empieza por 'H'.
		cat >"$BANCO/ssh.py" <<'PYSSH'
import socket, sys, threading, time
srv = socket.socket(); srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind(("127.0.0.1", 0)); srv.listen(4)
open(sys.argv[1], "w").write(str(srv.getsockname()[1]))
def serve():
    while True:
        try: c, _ = srv.accept()
        except OSError: return
        try:
            c.sendall(b"SSH-2.0-OpenSSH_9.6\r\n"); time.sleep(1.0)
        except OSError: pass
        finally: c.close()
threading.Thread(target=serve, daemon=True).start()
time.sleep(30)
PYSSH
		python3 "$BANCO/ssh.py" "$BANCO/sshport" & SSH_PID=$!
		for _ in 1 2 3 4 5 6 7 8 9 10; do [ -s "$BANCO/sshport" ] && break; sleep 0.3; done
		SSH_PUERTO="$(cat "$BANCO/sshport" 2>/dev/null)"
		if [ -n "$SSH_PUERTO" ]; then
			emite_dsn "$SSH_PUERTO"
			(cd "$LPG" && TMPDIR="$FUERA" bash "$GATE") >"$BANCO/out.log" 2>&1
			comprobar "un listener SSH NO pasa por PostgreSQL" 1 "$?"
			dice "y lo dice: su bandera empieza por S y no es Postgres" "NO habla PostgreSQL"
		else
			printf '  AVISO no pude levantar el senuelo SSH: ese caso NO se ejercito\n'
		fi
		kill "$SSH_PID" 2>/dev/null
	else
		printf '  AVISO no pude levantar el senuelo que no habla Postgres: el caso NO se ejercito\n'
	fi
	kill "$NOPG_PID" 2>/dev/null
else
	printf '  AVISO sin python3: el caso del listener que no habla Postgres NO se ejercito\n'
fi

# (d) EL HELPER FALLA -> NO HE PODIDO MIRAR (2), NUNCA verde. Anadido 2026-09-04 (r27) por el
#     HIGH-02: la forma vieja era `eval "$(bash pg-test-env.sh 2>/dev/null)" || true`, que perdia el
#     estado del productor DOS veces (eval reporta el suyo; el `|| true` remata) y podia terminar en
#     el OK COMPLETO. El testigo estatico que nombra el informe es `OLIVARES_PG_PROBE=bogus`, que el
#     helper real rechaza con 2 (scripts/pg-test-env.sh:79-86); aqui el senuelo hace lo mismo.
# (e) EL HELPER SALE 0 Y EMITE SHELL INVALIDO -> NO HE PODIDO MIRAR (2), nunca verde.
#     Anadido 2026-09-04 (r27) por el BLOQUEANTE-03 del contraste, REPRODUCIDO antes de curar:
#     con `eval "$exports"` sin comprobar, el `printf` de la linea siguiente restaura rc 0 y el
#     subshell devuelve 0 con la DSN VACIA — el preflight imprimiendo OK donde el gancho muere.
#     Es la misma clase que el `|| true` retirado, una linea mas abajo: el estado del productor
#     lo pisa el comando que viene detras.
printf '#!/usr/bin/env bash\nprintf "%%s\\n" "export FOO=(((sintaxis rota"\nexit 0\n' >"$LPG/scripts/pg-test-env.sh"
chmod +x "$LPG/scripts/pg-test-env.sh"
(cd "$LPG" && TMPDIR="$FUERA" bash "$GATE") >"$BANCO/out.log" 2>&1
comprobar "si el helper emite shell invalido, NO HE PODIDO MIRAR (2)" 2 "$?"

printf '#!/usr/bin/env bash\necho "pg-test-env de prueba: rehuso" >&2\nexit 2\n' >"$LPG/scripts/pg-test-env.sh"
chmod +x "$LPG/scripts/pg-test-env.sh"
(cd "$LPG" && TMPDIR="$FUERA" bash "$GATE") >"$BANCO/out.log" 2>&1
comprobar "si pg-test-env.sh falla, NO HE PODIDO MIRAR (2)" 2 "$?"
dice "y nombra el rc del productor" "fallo con rc 2"


# ── 3-sexies · EL AVISO DE LA PLANTILLA NO PUEDE LLEVAR LA CONTRASEÑA ─────────────────────────
# ⛔ POR QUE, medido el 2026-09-04 (r27) sobre `origin/main`, señalado por PLAN-V269: el heredoc
#    del aviso NO está entrecomillado, así que `$PG_DSN` se EXPANDÍA y el bloque de remediación
#    imprimía el DSN de superusuario COMPLETO —con contraseña— al stderr de todo push cuya
#    `template1` no fuera UTF8. Tres veces por aviso. La regla de la casa es «secretos por
#    NOMBRE, nunca por valor», así que el bloque cita la variable y no su contenido.
#
# El caso corre en CUALQUIER caja, con o sin Postgres: un `psql` de pega en el PATH devuelve
# LATIN1 y con eso se alcanza la rama del aviso. Lo que se comprueba es la AUSENCIA de la marca,
# y para que la ausencia signifique algo se comprueba TAMBIÉN que el aviso se emitió.
FAKEBIN="$BANCO/fakebin"
mkdir -p "$FAKEBIN"
printf '#!/usr/bin/env bash\n[ "${1:-}" = "--version" ] && { echo "psql (PostgreSQL) 15.0"; exit 0; }\necho LATIN1\n' >"$FAKEBIN/psql"
chmod +x "$FAKEBIN/psql"
MARCA='CONTRASENA-QUE-NO-DEBE-SALIR'
salida_pw="$( (cd "$BANCO/repo" && PATH="$FAKEBIN:$PATH" TMPDIR="$FUERA" \
	OLIVARES_TEST_POSTGRES_SUPERUSER_DSN="postgres://usr:${MARCA}@127.0.0.1:5432/db?sslmode=disable" \
	bash "$GATE") 2>&1 || true )"
if case "$salida_pw" in *"es LATIN1"*) true;; *) false;; esac; then
	printf '  ok    %-58s emitido\n' "el aviso de plantilla se emite (precondicion del caso)"
	pasan=$((pasan + 1))
	if case "$salida_pw" in *"$MARCA"*) true;; *) false;; esac; then
		printf '  FALLA %-58s la contrasena APARECE\n' "el aviso NO lleva la contrasena del DSN"
		fallan=$((fallan + 1))
	else
		printf '  ok    %-58s no aparece\n' "el aviso NO lleva la contrasena del DSN"
		pasan=$((pasan + 1))
	fi
	if case "$salida_pw" in *OLIVARES_TEST_POSTGRES_SUPERUSER_DSN*) true;; *) false;; esac; then
		printf '  ok    %-58s por NOMBRE\n' "y cita la variable en vez de su valor"
		pasan=$((pasan + 1))
	else
		printf '  FALLA %-58s ni valor ni nombre: el aviso dejo de ser util\n' "y cita la variable en vez de su valor"
		fallan=$((fallan + 1))
	fi
else
	printf '  AVISO el aviso de plantilla no se emitio: el caso de la contrasena NO se ejercito\n'
fi


# ── 3-septies · «ENVUELTA» NO ES «VA A CORRER»: las dos mitades del predicado ─────────────────
# ⛔ POR QUE, medido el 2026-09-04 (r27) tras el bloqueante del contraste Codex y REPRODUCIDO antes
#    de curarlo: la primera version de la guarda final trataba una receta ENVUELTA como prueba de
#    que Postgres iba a ejecutarse. Con una pata envuelta, `psql` presente, ningun servidor
#    alcanzable y sin DSN, el helper no emite ninguna DSN, `pgtest.classify` devuelve `gateSkip` y
#    no corre NADA — pero el gate contestaba 2. Eso es SOBRE-BLOQUEO, y esta pata va DESNUDA en el
#    gancho bajo `set -e`: habria matado todo push en esa caja.
#
# Las dos mitades se prueban por separado, porque una sola no distingue la cura del defecto:
#   (a) no hay DSN efectiva  -> NO bloquea (0): lo que se rompio y se curo
#   (b) hay DSN efectiva y la postura no se pudo mirar -> 2: lo que la base dejaba pasar EN VERDE
LPM="$BANCO/lab-modo"
rm -rf "$LPM"; mkdir -p "$LPM/.githooks" "$LPM/scripts"
printf 'pre-push: running the FULL gate locally (build + test + web).\ntask test:pesada-pg\n' >"$LPM/.githooks/pre-push"
printf '  test:pesada-pg:\n    cmds:\n      - bash scripts/with-pg-env.sh go test ./...\n  otra:\n' >"$LPM/Taskfile.yml"
git -C "$LPM" init -q 2>/dev/null
# `psql` que EXISTE y no obtiene encoding: fuerza «no pude mirar la plantilla» sin depender de la caja
PSQLMUDO="$BANCO/psqlmudo"
mkdir -p "$PSQLMUDO"
printf '#!/usr/bin/env bash\n[ "${1:-}" = "--version" ] && { echo "psql (PostgreSQL) 15.0"; exit 0; }\nexit 1\n' >"$PSQLMUDO/psql"
chmod +x "$PSQLMUDO/psql"

# (a) el helper no emite DSN: nada correra contra Postgres -> NO bloquea
printf '#!/usr/bin/env bash\nexit 0\n' >"$LPM/scripts/pg-test-env.sh"; chmod +x "$LPM/scripts/pg-test-env.sh"
# This no-DSN fixture must not inherit the developer's configured test connection.
# Clear only this invocation; the other cases retain their real PostgreSQL inputs.
(cd "$LPM" && PATH="$PSQLMUDO:$PATH" TMPDIR="$FUERA" \
	OLIVARES_TEST_POSTGRES_SUPERUSER_DSN='' OLIVARES_TEST_POSTGRES_DSN='' OLIVARES_TEST_POSTGRES_ADMIN_DSN='' \
	bash "$GATE") >"$BANCO/out.log" 2>&1
comprobar "pata envuelta pero SIN DSN efectiva, NO bloquea" 0 "$?"

# (b) el helper SI emite DSN y hay algo que habla Postgres detras: la pata VA a correr contra una
#     plantilla que nadie miro -> 2. El senuelo contesta al SSLRequest para que la sonda pase y el
#     caso llegue a la guarda final en vez de morir antes en «nadie escucha».
cat >"$BANCO/pg-fake.py" <<'PYSRV'
import socket, struct, sys, threading, time
srv = socket.socket(); srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind(("127.0.0.1", 0)); srv.listen(4)
open(sys.argv[1], "w").write(str(srv.getsockname()[1]))
def serve():
    while True:
        try: c, _ = srv.accept()
        except OSError: return
        try:
            c.recv(64); c.sendall(b"N")     # respuesta de Postgres al SSLRequest
        except OSError: pass
        finally: c.close()
threading.Thread(target=serve, daemon=True).start()
time.sleep(30)
PYSRV
if command -v python3 >/dev/null 2>&1; then
	python3 "$BANCO/pg-fake.py" "$BANCO/pgport" & PGF_PID=$!
	for _ in 1 2 3 4 5 6 7 8 9 10; do [ -s "$BANCO/pgport" ] && break; sleep 0.3; done
	PGF_PUERTO="$(cat "$BANCO/pgport" 2>/dev/null)"
	if [ -n "$PGF_PUERTO" ]; then
		printf '#!/usr/bin/env bash\nprintf %%s\\n "export OLIVARES_TEST_POSTGRES_SUPERUSER_DSN=\\"postgres://u:p@127.0.0.1:%s/db?sslmode=disable\\""\n' "$PGF_PUERTO" >"$LPM/scripts/pg-test-env.sh"
		chmod +x "$LPM/scripts/pg-test-env.sh"
		(cd "$LPM" && PATH="$PSQLMUDO:$PATH" TMPDIR="$FUERA" bash "$GATE") >"$BANCO/out.log" 2>&1
		comprobar "con DSN efectiva y postura sin mirar, NO HE PODIDO MIRAR (2)" 2 "$?"
		dice "y dice que un listener no es una sesion" "no es que la sesion sirva"
	else
		printf '  AVISO no pude levantar el Postgres de pega: la mitad (b) NO se ejercito\n'
	fi
	kill "$PGF_PID" 2>/dev/null
else
	printf '  AVISO sin python3: la mitad (b) NO se ejercito\n'
fi


# ── 3-octies · EL SUJETO NO PUEDE ESTAR ROTO, y una bateria verde no lo garantiza ─────────────
# ⛔ POR QUE, medido el 2026-09-04 (r27) y señalado por el contraste Codex (ALTO-03): dos `echo`
#    de una cura mía acababan en `\\` en vez de `\` — un escapado equivocado al GENERAR el codigo,
#    no al escribirlo. La continuacion de linea se rompe, la linea siguiente se EJECUTA como
#    comando, y el gate imprime «command not found» dos veces en un camino real. La bateria
#    estaba en 30/0 por encima de eso: ninguna asercion miraba si el sujeto se rompia, porque
#    todas miraban rc y mensajes, y un `command not found` no cambia ni uno ni otro.
#
# Alcance declarado: comprueba los caminos que ESTA bateria ejercita, no el guion entero. Un
# camino que nadie corre puede seguir roto, y este caso no lo vera.
rotos=0
comprueba_roto() { # comprueba_roto <etiqueta> <salida>
	# ⛔ SIN TUBERIA DENTRO DEL BOOLEANO: una tuberia cuyo consumidor cierra pronto puede
	#    devolver 141 EN EXITO bajo `pipefail`: el productor recibe SIGPIPE. `lint:sigpipe-booleans`
	#    lo cazo en MI propio fichero (deuda 0 -> 4). Es la clase que llevo el dia adjudicando a
	#    otros, cometida al escribir la guarda. La forma es la que el propio gate imprime.
	roto_grep="$(printf '%s' "$2" | grep -inE 'command not found|syntax error|unbound variable' || true)"
	if [ -n "$roto_grep" ]; then
		printf '  FALLA %-58s %s\n' "el sujeto no se rompe: $1" "$(printf '%s\n' "$roto_grep" | head -1)"
		printf '%s\n' "$roto_grep" | head -2 | sed 's/^/          /'
		rotos=$((rotos + 1))
	fi
}
PSQLMUDO2="$BANCO/psqlmudo2"; mkdir -p "$PSQLMUDO2"
printf '#!/usr/bin/env bash\n[ "${1:-}" = "--version" ] && { echo "psql (PostgreSQL) 15.0"; exit 0; }\nexit 1\n' >"$PSQLMUDO2/psql"
chmod +x "$PSQLMUDO2/psql"
comprueba_roto "sin DSN"  "$( (cd "$BANCO/repo" && PATH="$PSQLMUDO2:$PATH" TMPDIR="$FUERA" bash "$GATE") 2>&1 || true )"
comprueba_roto "con DSN"  "$( (cd "$BANCO/repo" && PATH="$PSQLMUDO2:$PATH" TMPDIR="$FUERA" OLIVARES_TEST_POSTGRES_SUPERUSER_DSN='postgres://u:p@127.0.0.1:5999/d' bash "$GATE") 2>&1 || true )"
comprueba_roto "sin psql" "$( (cd "$BANCO/repo" && TMPDIR="$FUERA" bash "$GATE") 2>&1 || true )"
if [ "$rotos" -eq 0 ]; then
	printf '  ok    %-58s 3 caminos\n' "el sujeto no se rompe en ningun camino"
	pasan=$((pasan + 1))
else
	fallan=$((fallan + rotos))
fi

# ── 3-nonies · LOS CUATRO HUECOS QUE EL CONTRASTE NOMBRO Y LA BATERIA NO CUBRIA ───────────────
# Cada uno con su mutante indicado en el acta: los cuatro SOBREVIVIAN a 30/0, que es la
# definicion de un hueco de banco — no fallaban, es que nadie preguntaba.

# (f) ALTO-02 · el censo tiene que ver la llamada INDENTADA a `task test`, no solo `^task`.
LCEN="$BANCO/lab-censo"; rm -rf "$LCEN"; mkdir -p "$LCEN/.githooks" "$LCEN/scripts"
printf 'pre-push: running the FULL gate locally (build + test + web).\n\ttask test\n' >"$LCEN/.githooks/pre-push"
printf '  test:\n    cmds:\n      - bash scripts/with-pg-env.sh go test ./...\n  otra:\n' >"$LCEN/Taskfile.yml"
printf '#!/usr/bin/env bash\nprintf %%s\\n "export OLIVARES_TEST_POSTGRES_SUPERUSER_DSN=\\"postgres://u:p@127.0.0.1:5999/db\\""\n' >"$LCEN/scripts/pg-test-env.sh"
chmod +x "$LCEN/scripts/pg-test-env.sh"
git -C "$LCEN" init -q 2>/dev/null
(cd "$LCEN" && TMPDIR="$FUERA" bash "$GATE") >"$BANCO/out.log" 2>&1
comprobar "una pata INDENTADA con Postgres tambien se censa" 1 "$?"
dice "y la nombra" "test"

# (g) MEDIO-01 · app/admin sin superusuario es HALLAZGO (1), no «no pude mirar» (2).
if ! command -v psql >/dev/null 2>&1; then
	(cd "$BANCO/repo" && TMPDIR="$FUERA" OLIVARES_TEST_POSTGRES_DSN='postgres://u:p@127.0.0.1:5999/d' bash "$GATE") >"$BANCO/out.log" 2>&1
	comprobar "solo DSN de app, sin super: HALLAZGO (1), no 2" 1 "$?"
	dice "y nombra gateMisconfigured por su fuente" "pgtest.go:289-298"
else
	printf '  AVISO esta caja TIENE psql: la rama app-only sin cliente NO se ejercito\n'
fi

# (h) MEDIO-02 · la respuesta valida `S` (PgBouncer/Postgres con TLS) tambien es Postgres.
cat >"$BANCO/pg-s.py" <<'PYS'
import socket, sys, threading, time
srv = socket.socket(); srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind(("127.0.0.1", 0)); srv.listen(4)
open(sys.argv[1], "w").write(str(srv.getsockname()[1]))
def serve():
    while True:
        try: c, _ = srv.accept()
        except OSError: return
        try:
            c.recv(64); c.sendall(b"S"); time.sleep(1.5)
        except OSError: pass
        finally: c.close()
threading.Thread(target=serve, daemon=True).start()
time.sleep(30)
PYS
if command -v python3 >/dev/null 2>&1; then
	python3 "$BANCO/pg-s.py" "$BANCO/sport" & PGS_PID=$!
	for _ in 1 2 3 4 5 6 7 8 9 10; do [ -s "$BANCO/sport" ] && break; sleep 0.3; done
	SP="$(cat "$BANCO/sport" 2>/dev/null)"
	if [ -n "$SP" ]; then
		emite_dsn "$SP"
		(cd "$LPG" && TMPDIR="$FUERA" bash "$GATE") >"$BANCO/out.log" 2>&1
		rc_s=$?
		if [ "$rc_s" -eq 1 ] && grep -q "NO habla PostgreSQL" "$BANCO/out.log" 2>/dev/null; then
			printf '  FALLA %-58s la respuesta S se rechazo\n' "un servidor que contesta S ES PostgreSQL"
			fallan=$((fallan + 1))
		else
			printf '  ok    %-58s no lo rechaza\n' "un servidor que contesta S ES PostgreSQL"
			pasan=$((pasan + 1))
		fi
	else
		printf '  AVISO no pude levantar el senuelo S: MEDIO-02 NO se ejercito\n'
	fi
	kill "$PGS_PID" 2>/dev/null
fi

# (i) MEDIO-04 · un listener que acepta y CALLA no es «nadie escucha».
cat >"$BANCO/mudo.py" <<'PYM'
import socket, sys, threading, time
srv = socket.socket(); srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind(("127.0.0.1", 0)); srv.listen(4)
open(sys.argv[1], "w").write(str(srv.getsockname()[1]))
def serve():
    while True:
        try: c, _ = srv.accept()
        except OSError: return
        time.sleep(8)      # acepta y no contesta nunca
        try: c.close()
        except OSError: pass
threading.Thread(target=serve, daemon=True).start()
time.sleep(30)
PYM
if command -v python3 >/dev/null 2>&1; then
	python3 "$BANCO/mudo.py" "$BANCO/mport" & MUDO_PID=$!
	for _ in 1 2 3 4 5 6 7 8 9 10; do [ -s "$BANCO/mport" ] && break; sleep 0.3; done
	MP="$(cat "$BANCO/mport" 2>/dev/null)"
	if [ -n "$MP" ]; then
		emite_dsn "$MP"
		(cd "$LPG" && TMPDIR="$FUERA" bash "$GATE") >"$BANCO/out.log" 2>&1
		comprobar "un listener que CALLA no es 'nadie escucha'" 1 "$?"
		if grep -q "nadie escucha" "$BANCO/out.log" 2>/dev/null; then
			printf '  FALLA %-58s lo llama ausencia\n' "y lo diagnostica como que no habla Postgres"
			fallan=$((fallan + 1))
		else
			printf '  ok    %-58s no lo llama ausencia\n' "y lo diagnostica como que no habla Postgres"
			pasan=$((pasan + 1))
		fi
	else
		printf '  AVISO no pude levantar el senuelo mudo: MEDIO-04 NO se ejercito\n'
	fi
	kill "$MUDO_PID" 2>/dev/null
fi


# (j) MEDIO-04, LA OTRA MITAD · un destino que DESCARTA el SYN da timeout AL CONECTAR, y eso no
#     es «nadie escucha»: es que no se pudo saber. El caso (i) —listener que acepta y calla— NO
#     ejercita este camino, y se vio porque su mutante SOBREVIVIA: el timeout de (i) ocurre en el
#     `recv`, ya cubierto. Un mutante que sobrevive es un hueco de banco, no un adorno.
#
#     Lleva su PRECONDICION ejecutable porque depende de la red de la caja: si esa direccion no
#     da timeout aqui, el caso se DECLARA no ejercitado en vez de pasar por casualidad.
AGUJERO="10.255.255.1"
if timeout 12 python3 -c '
import socket, sys
try:
    socket.create_connection(("'"$AGUJERO"'", 5432), timeout=2); sys.exit(1)
except socket.timeout: sys.exit(0)
except OSError: sys.exit(1)
' 2>/dev/null; then
	printf '#!/usr/bin/env bash\nprintf %%s\\n "export OLIVARES_TEST_POSTGRES_SUPERUSER_DSN=\\"postgres://u:p@%s:5432/db\\""\n' "$AGUJERO" >"$LPG/scripts/pg-test-env.sh"
	chmod +x "$LPG/scripts/pg-test-env.sh"
	(cd "$LPG" && TMPDIR="$FUERA" bash "$GATE") >"$BANCO/out.log" 2>&1
	comprobar "un destino que descarta el SYN: NO es 'nadie escucha'" 1 "$?"
	if grep -q "nadie escucha" "$BANCO/out.log" 2>/dev/null; then
		printf '  FALLA %-58s lo llama ausencia\n' "y un timeout al conectar se diagnostica aparte"
		fallan=$((fallan + 1))
	else
		printf '  ok    %-58s no lo llama ausencia\n' "y un timeout al conectar se diagnostica aparte"
		pasan=$((pasan + 1))
	fi
else
	printf '  AVISO %s no da timeout en esta red: MEDIO-04 (connect) NO se ejercito\n' "$AGUJERO"
fi

# ── 4 · EL MUTANTE DEL TMPDIR: dentro de un repositorio ───────────────────────────────────────
# Tumbó lint:mid-operation con 14/1 mientras el árbol estaba impecable.
mkdir -p "$BANCO/repo/.tmp-dentro"
correr "$BANCO/repo/.tmp-dentro"
comprobar "TMPDIR DENTRO de un repositorio, RECHAZA" 1 "$?"
dice "y dice que está dentro" "DENTRO de un repositorio"

# ── 5 · TMPDIR que no ejecuta ─────────────────────────────────────────────────────────────────
# Se monta quitando el bit de ejecución al directorio, que es lo que un noexec produce a efectos
# del sujeto: un guion recién escrito ahí no corre.
NOEXEC="$BANCO/tmp-noexec"
mkdir -p "$NOEXEC"
if command -v setfacl >/dev/null 2>&1 || true; then :; fi
# Sin privilegios para montar noexec, se comprueba la OTRA mitad del predicado: un TMPDIR ausente
# es NO HE PODIDO MIRAR, nunca limpio.
correr "$BANCO/no-existe-este-tmpdir"
comprobar "un TMPDIR ausente es NO HE PODIDO MIRAR" 2 "$?"

# ── 6 · Y fuera de un repositorio, el gate no adivina ─────────────────────────────────────────
(cd "$BANCO" && TMPDIR="$FUERA" bash "$GATE") >"$BANCO/out.log" 2>&1
comprobar "fuera de un repositorio es NO HE PODIDO MIRAR, no limpio" 2 "$?"

echo "test-lane-preflight: $pasan pasan, $fallan fallan"
[ "$fallan" -eq 0 ] || exit 1
exit 0
