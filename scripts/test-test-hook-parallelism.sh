#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# test-test-hook-parallelism.sh — batería de `check-test-hook-parallelism.sh`.
#
# El caso que manda es el 2: el MUTANTE REALISTA. No un fichero de juguete — los dos ficheros
# REALES del paquete, sacados de git, con `t.Parallel()` añadido al test que de verdad instala el
# gancho. Es el mutante que escribiría quien introduce el defecto, y es la condición que puso
# quien lo encargó (la atribución va en el trailer del commit, no aquí: nombrar un carril en el
# fuente sube el trinquete de vocabulario del export y bloquea el push — medido hoy, con este
# mismo fichero).
#
# Y los casos 3-6 son la otra mitad de esa condición: **el control inverso**. Un gate que sólo se
# prueba en la dirección que enrojece no se sabe si discrimina o sólo grita.

set -u -o pipefail
export LC_ALL=C

RAIZ="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)"

# ⛔ Esta batería empareja `mktemp -d` con git, así que es miembro de la clase de `lint:git-env`:
# un GIT_DIR heredado manda sobre el `cd` y operaría sobre el repositorio VIVO. Lo aprendí hoy con
# la batería hermana, que aterrizó SIN esto y puso `lint:git-env` en BROKEN — y ese gate corre sin
# `|| true`, o sea que habría dejado a los cinco carriles sin empujar.
# shellcheck source=/dev/null
. "${RAIZ}/scripts/lib/git-env.sh" || {
	echo "test-test-hook-parallelism: NO HE PODIDO MIRAR — no puedo sourcear scripts/lib/git-env.sh" >&2
	exit 2
}

GATE="$RAIZ/scripts/check-test-hook-parallelism.sh"
pasa=0
falla=0

TRABAJO="$(mktemp -d "${TMPDIR:-/tmp}/hookpar.XXXXXX")"
trap 'chmod -R u+rwX "$TRABAJO" 2>/dev/null; rm -rf "$TRABAJO"' EXIT

# ⛔ NADA de `printf … | grep -q`: bajo `pipefail`, `grep -q` cierra la tuberia al primer acierto
# y el productor muere con SIGPIPE, asi que el EXITO devuelve 141. Lo cazo `lint:sigpipe-booleans`
# en mi propio push -- cuarta vez en el dia que un gate nuevo mio tropieza con otro. La forma sin
# tuberia es una herestring.
comprobar() { # <desc> <rc-esperado> <raiz> [patron]
	local desc="$1" esperado="$2" raiz="$3" patron="${4:-}"
	local salida rc
	salida="$(bash "$GATE" "$raiz" 2>&1)"
	rc=$?
	if [ "$rc" != "$esperado" ]; then
		printf '  FALLA  %-56s rc=%s (esperaba %s)\n' "$desc" "$rc" "$esperado"
		falla=$((falla + 1)); return
	fi
	if [ -n "$patron" ] && ! grep -q -- "$patron" <<<"$salida"; then
		printf '  FALLA  %-56s rc=%s pero no dice «%s»\n' "$desc" "$rc" "$patron"
		falla=$((falla + 1)); return
	fi
	printf '  ok     %-56s rc=%s\n' "$desc" "$rc"
	pasa=$((pasa + 1))
}

siembra() { # <dir> <fichero>  <- contenido por stdin
	mkdir -p "$1"; cat > "$1/$2"
}

# ── 1 · el árbol real ────────────────────────────────────────────────────────────────────────
comprobar "el arbol real esta limpio" 0 "$RAIZ" "limpio"

# ── 2 · EL CASO QUE MANDA: mutante REALISTA sobre los ficheros REALES ────────────────────────
# Se sacan de git en vez de copiarlos a mano: una copia a mano envejece y acabaría probando otro
# fichero con el mismo nombre. Y se trabaja sobre una copia: el arbol vivo no se toca.
PROD="core/internal/store/sqlstore/directoryactivation.go"
TEST="core/internal/store/sqlstore/directoryactivation_test.go"
FUNC="TestDirectoryActivationSQLiteCommitBoundary"
VAR="directoryActivationCommitTestHook"
REAL="$TRABAJO/real/core/internal/store/sqlstore"
if git -C "$RAIZ" show "HEAD:$PROD" > /dev/null 2>&1 && git -C "$RAIZ" show "HEAD:$TEST" > /dev/null 2>&1; then
	mkdir -p "$REAL"
	git -C "$RAIZ" show "HEAD:$PROD" > "$REAL/${PROD##*/}"
	git -C "$RAIZ" show "HEAD:$TEST" > "$REAL/${TEST##*/}"
	# control NEGATIVO primero: la copia sin mutar tiene que salir limpia, o el caso 2 mide la copia
	comprobar "la copia REAL sin mutar sale limpia" 0 "$TRABAJO/real" "limpio"
	if grep -q "^func ${FUNC}(" "$REAL/${TEST##*/}"; then
		awk -v f="$FUNC" '{ print; if ($0 ~ ("^func " f "\\(")) print "\tt.Parallel()" }' \
			"$REAL/${TEST##*/}" > "$REAL/.mut" && mv "$REAL/.mut" "$REAL/${TEST##*/}"
		comprobar "MUTANTE REALISTA: t.Parallel en el test real muere" 1 "$TRABAJO/real" "$VAR"
		comprobar "  y NOMBRA el test que lo introduce" 1 "$TRABAJO/real" "$FUNC"
	else
		printf '  FALLA  %-56s el test real %s ya no existe\n' "mutante realista" "$FUNC"
		falla=$((falla + 1))
	fi
else
	printf '  ok     %-56s (sin git o sin esos ficheros aqui)\n' "mutante realista — saltado"
	pasa=$((pasa + 1))
fi

# ── 3 · CONTROL INVERSO A: asigna la global pero es SERIAL ───────────────────────────────────
D="$TRABAJO/serial/pkg"
siembra "$D" "prod.go" <<'GO'
package pkg

var beforeInsertTestHook func() error
GO
siembra "$D" "prod_test.go" <<'GO'
package pkg

import "testing"

func TestSerialInstallsTheHook(t *testing.T) {
	beforeInsertTestHook = func() error { return nil }
	_ = beforeInsertTestHook
}
GO
comprobar "CONTROL INVERSO A: asigna la global pero es SERIAL -> limpio" 0 "$TRABAJO/serial" "limpio"

# ── 4 · CONTROL INVERSO B: es PARALELO pero no asigna ninguna global ─────────────────────────
D="$TRABAJO/paralelo/pkg"
siembra "$D" "prod.go" <<'GO'
package pkg

var beforeInsertTestHook func() error
GO
siembra "$D" "prod_test.go" <<'GO'
package pkg

import "testing"

func TestParallelTouchesNothing(t *testing.T) {
	t.Parallel()
	local := 1
	local = 2
	_ = local
}
GO
comprobar "CONTROL INVERSO B: paralelo que no asigna nada -> limpio" 0 "$TRABAJO/paralelo" "limpio"

# ── 5 · el identificador BLANCO: la clase entera de falsos positivos ─────────────────────────
# Sin esta exclusion el censo del arbol daba 41 hallazgos y los 41 eran `_`.
D="$TRABAJO/blanco/pkg"
siembra "$D" "prod.go" <<'GO'
package pkg

type iface interface{ M() }

type impl struct{}

func (impl) M() {}

var _ iface = impl{}
GO
siembra "$D" "prod_test.go" <<'GO'
package pkg

import "testing"

func TestParallelAssignsBlank(t *testing.T) {
	t.Parallel()
	_ = 1
}
GO
comprobar "el identificador blanco _ NO es un hallazgo" 0 "$TRABAJO/blanco" "limpio"

# ── 6 · sombra LOCAL: mismo nombre, declarado con := en la funcion ───────────────────────────
D="$TRABAJO/sombra/pkg"
siembra "$D" "prod.go" <<'GO'
package pkg

var commitTestHook func() error
GO
siembra "$D" "prod_test.go" <<'GO'
package pkg

import "testing"

func TestParallelShadowsTheName(t *testing.T) {
	t.Parallel()
	commitTestHook := func() error { return nil }
	commitTestHook = func() error { return nil }
	_ = commitTestHook
}
GO
comprobar "una LOCAL con el mismo nombre no es la global" 0 "$TRABAJO/sombra" "limpio"

# ── 7 · el mutante que prueba que 5 y 6 no son verdes por vacuidad ───────────────────────────
D="$TRABAJO/mutante/pkg"
siembra "$D" "prod.go" <<'GO'
package pkg

var commitTestHook func() error
GO
siembra "$D" "prod_test.go" <<'GO'
package pkg

import "testing"

func TestParallelAssignsTheGlobal(t *testing.T) {
	t.Parallel()
	commitTestHook = func() error { return nil }
}
GO
comprobar "MUTANTE sintetico: paralelo + global -> muere y la nombra" 1 "$TRABAJO/mutante" "commitTestHook"

# ── 8 · las tres respuestas ──────────────────────────────────────────────────────────────────
comprobar "una raiz que no existe es 2, no 0" 2 "$TRABAJO/no-existe" "NO HE PODIDO MIRAR"

D="$TRABAJO/ilegible/pkg"
siembra "$D" "prod.go" <<'GO'
package pkg

var h func() error
GO
siembra "$D" "prod_test.go" <<'GO'
package pkg

import "testing"

func TestX(t *testing.T) { t.Parallel() }
GO
chmod 000 "$D/prod.go"
if [ -r "$D/prod.go" ]; then
	printf '  ok     %-56s (corriendo como root)\n' "un fichero ilegible es 2 — saltado"
	pasa=$((pasa + 1))
else
	comprobar "un fichero ilegible es 2, nunca limpio" 2 "$TRABAJO/ilegible" "NO HE PODIDO MIRAR"
fi
chmod 644 "$D/prod.go"

# ── 9 · un arbol SIN Go pasa, y lo dice ──────────────────────────────────────────────────────
mkdir -p "$TRABAJO/vacio"
comprobar "un arbol sin ficheros Go pasa y lo dice" 0 "$TRABAJO/vacio" "no tiene ficheros Go"

# ── 10 · LOS TRES DEL BRIEF, que salieron de contestarme a mano las preguntas que le
#         habria pedido a un contraste con el motor caido. Los tres encontraron algo.

# 10a · `var a, b func()` declara DOS nombres: tomar el primero perdia el resto.
D="$TRABAJO/multinombre/pkg"
siembra "$D" "prod.go" <<'GO'
package pkg

var (
	hookA func() error
)
var hookB, hookC func() error
GO
siembra "$D" "prod_test.go" <<'GO'
package pkg

import "testing"

func TestParallelAssignsAll(t *testing.T) {
	t.Parallel()
	hookA = func() error { return nil }
	hookB = func() error { return nil }
	hookC = func() error { return nil }
}
GO
salida="$(bash "$GATE" "$TRABAJO/multinombre" 2>&1)"
if [ "$(printf '%s' "$salida" | grep -c 'asigna la var')" = "3" ]; then
	printf '  ok     %-56s 3 de 3\n' "declaracion multi-nombre: caza TODOS los nombres"
	pasa=$((pasa + 1))
else
	printf '  FALLA  %-56s solo %s\n' "declaracion multi-nombre: caza TODOS los nombres" \
		"$(printf '%s' "$salida" | grep -c 'asigna la var')"
	falla=$((falla + 1))
fi

# 10b · asignacion DENTRO de un subtest paralelo: el padre es serial y aun asi es peligroso.
D="$TRABAJO/dentro-subtest/pkg"
siembra "$D" "prod.go" <<'GO'
package pkg

var commitTestHook func() error
GO
siembra "$D" "prod_test.go" <<'GO'
package pkg

import "testing"

func TestSerialBodyParallelSubtest(t *testing.T) {
	t.Run("a", func(t *testing.T) {
		t.Parallel()
		commitTestHook = func() error { return nil }
	})
}
GO
comprobar "asignacion DENTRO de un subtest paralelo: HALLAZGO" 1 "$TRABAJO/dentro-subtest" "commitTestHook"

# 10c · el simetrico: asignacion SERIAL con un subtest paralelo AJENO -> limpio.
#       Marcarlo seria sobre-acusar sobre `t.Run` + `t.Parallel`, que es idiomatico.
D="$TRABAJO/subtest-ajeno/pkg"
siembra "$D" "prod.go" <<'GO'
package pkg

var commitTestHook func() error
GO
siembra "$D" "prod_test.go" <<'GO'
package pkg

import "testing"

func TestAssignsSeriallyButHasAParallelSubtest(t *testing.T) {
	commitTestHook = func() error { return nil }
	t.Run("independiente", func(t *testing.T) {
		t.Parallel()
		_ = 1
	})
}
GO
comprobar "subtest paralelo AJENO a la asignacion: limpio" 0 "$TRABAJO/subtest-ajeno" "limpio"

# --- 7 · REGRESION MEDIDA: el `t.Parallel()` que vive en un COMENTARIO --------------------
# Caso REAL, no inventado: guardledgerconcurrency_pg_test.go lleva un comentario que ADVIERTE
# del peligro citando «el paquete tiene 171 llamadas a `t.Parallel()`». La primera version del
# gate leia la linea cruda, tomaba ese comentario por codigo, y acusaba una asignacion de un
# test SERIAL — rechazando el push de cualquiera que tocase el fichero. Buscar el sintoma
# dentro del texto que HABLA del sintoma da un falso positivo garantizado.
D="$TRABAJO/comentario/pkg"
siembra "$D" "prod.go" <<'GO'
package pkg

var beforeInsertTestHook func() error
GO
siembra "$D" "prod_test.go" <<'GO'
package pkg

import "testing"

func TestSerialPeroHablaDeParalelismo(t *testing.T) {
	// Este test es SERIAL a proposito: el paquete tiene 171 llamadas a `t.Parallel()`
	// y basta con que alguien anada una aqui para que la global pase a ser compartida.
	beforeInsertTestHook = func() error { return nil }
	_ = beforeInsertTestHook
}
GO
comprobar "t.Parallel() en un COMENTARIO no vuelve paralelo al test" 0 "$TRABAJO/comentario" "limpio"

# y el mismo fichero con un t.Parallel() DE VERDAD tiene que morir, o el caso de arriba
# solo prueba que el gate se ha quedado ciego
sed 's|^\tbeforeInsertTestHook|\tt.Parallel()\n\tbeforeInsertTestHook|' \
	"$D/prod_test.go" > "$D/.m" && mv "$D/.m" "$D/prod_test.go"
comprobar "  control inverso: con t.Parallel() REAL, muere" 1 "$TRABAJO/comentario" "beforeInsertTestHook"

# --- 8 · la misma trampa en un LITERAL de cadena ------------------------------------------
D="$TRABAJO/cadena/pkg"
siembra "$D" "prod.go" <<'GO'
package pkg

var beforeInsertTestHook func() error
GO
siembra "$D" "prod_test.go" <<'GO'
package pkg

import "testing"

func TestSerialConLiteral(t *testing.T) {
	msg := "recuerda llamar a t.Parallel() en los tests nuevos"
	beforeInsertTestHook = func() error { return nil }
	_ = beforeInsertTestHook
	_ = msg
}
GO
comprobar "t.Parallel() dentro de un LITERAL tampoco cuenta" 0 "$TRABAJO/cadena" "limpio"

# --- 9 · ROTO POR REVISION CRUZADA: comentario de BLOQUE y raw string MULTILINEA ----------
# Los encontro otro carril atacando el arreglo del caso 7. Son FALSOS POSITIVOS, o sea el modo
# CARO: el gate rechaza un push legitimo. Y el primero es el MISMO caso que motivo el arreglo
# —un comentario que ADVIERTE del peligro— escrito con /* */ en vez de //. La causa era una
# sola: codigo() miraba LINEA A LINEA y el lexico de Go no lo es.
D="$TRABAJO/lexico/pkg"
siembra "$D" "prod.go" <<'GO'
package pkg

var beforeInsertTestHook func() error
GO
siembra "$D" "a_test.go" <<'GO'
package pkg

import "testing"

func TestSerialConComentarioDeBloque(t *testing.T) {
	/* Este test es SERIAL. El paquete tiene 171 llamadas a t.Parallel()
	   y basta con anadir una aqui para compartir la global. */
	beforeInsertTestHook = func() error { return nil }
	_ = beforeInsertTestHook
}
GO
siembra "$D" "b_test.go" <<'GO'
package pkg

import "testing"

func TestSerialConRawStringMultilinea(t *testing.T) {
	plantilla := `func Ejemplo(t *testing.T) {
	t.Parallel()
}`
	beforeInsertTestHook = func() error { return nil }
	_ = beforeInsertTestHook
	_ = plantilla
}
GO
comprobar "t.Parallel() en comentario de BLOQUE no vuelve paralelo al test" 0 "$TRABAJO/lexico" "limpio"

# control inverso, o los dos de arriba solo prueban que el gate se quedo ciego
siembra "$D" "c_test.go" <<'GO'
package pkg

import "testing"

func TestDeVerdadParalelo(t *testing.T) {
	t.Parallel()
	beforeInsertTestHook = func() error { return nil }
	_ = beforeInsertTestHook
}
GO
comprobar "  control inverso: con los tres, SOLO muere el paralelo de verdad" 1 "$TRABAJO/lexico" "TestDeVerdadParalelo"

# --- 10 · RETIRADO: no podia fallar ------------------------------------------------------
# Escribi un caso «un raw abierto NO ciega el fichero siguiente» y al mutar el reset por
# fichero la bateria SIGUIO VERDE: `awk` se invoca UNA VEZ POR FICHERO
# (check-test-hook-parallelism.sh:145-153), asi que el estado no puede cruzar de uno a otro
# y el caso era decoracion. Se retira en vez de dejarlo: una asercion que no puede ponerse
# roja no prueba nada y ademas hace creer que ese riesgo esta cubierto por una prueba.
#
# 2026-08-25 · LA PREMISA DE ESTA RETIRADA CAMBIO; LA RETIRADA SIGUE VALIENDO. No se reescribe
# lo de arriba —es historia: registra que decidio una sesion y por que— pero su cita ya no
# describe el arbol: el gate dejo de ser `awk` y es `scripts/hookpar` (`go/ast`), asi que
# «check-test-hook-parallelism.sh:145-153» apunta a un fichero reescrito entero. Misma clase que
# la cita de linea que un merge movio y quedo señalando codigo ajeno sin que nada enrojeciera.
#
# La razon DE FONDO no cambia, y por eso el caso sigue retirado: en la implementacion nueva el
# estado tampoco cruza de un fichero a otro. Verificado antes de escribir esto, no supuesto —
# `parser.ParseFile` se llama UNA VEZ POR FICHERO dentro del bucle de `revisarPaquete`
# (scripts/hookpar/main.go:150), y cada uno recibe su propio `*ast.File`. Luego la asercion
# seguiria sin poder ponerse roja.

printf 'test-hook-parallelism gate: %s passed, %s failed\n' "$pasa" "$falla"
[ "$falla" -eq 0 ]
