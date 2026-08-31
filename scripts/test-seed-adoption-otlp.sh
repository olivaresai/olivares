#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Bateria de `scripts/seed-adoption-otlp.py`.
#
# ⛔ POR QUE EXISTE, Y NO ES SIMETRIA. Su hermano `verify-seed-payloads.py` pasó por tres lecturas
#    adversariales y cada una encontró algo; este guion lo escribió LA MISMA MANO EL MISMO DÍA y no
#    tenía ni un caso. Un guion sin banco no es «más simple»: es el que nadie ha medido. Los cuatro
#    defectos que los lectores encontraron en el hermano son las cuatro familias que esta batería
#    busca aquí, y por eso cada caso cita la suya.
#
# ⛔ LA MAYORIA NO NECESITA MOTOR, PERO LA IDEMPOTENCIA SI, Y ESA RAMA FALTABA. La forma del sobre,
#    su reproducibilidad y el contrato de `rc` se prueban sin nada levantado. **Lo que solo un motor
#    decide —que una segunda EJECUCION no mueva mis filas— vive en la RAMA VIVA del final**, tras
#    `OLIVARES_VERIFY_ENGINE`/`_TOKEN`/`_TENANT`/`_OTLP`. La cabecera anterior la prometia y no
#    existia ni un uso de esas variables: el 12/0 era hermetico entero. Cuando la rama no corre, se
#    DICE — un caso saltado impreso como `ok` es la familia que este banco persigue.
set -u -o pipefail

if ! command -v python3 >/dev/null 2>&1; then
	printf 'test-seed-adoption-otlp: NO HE PODIDO MIRAR: no hay python3 en el PATH\n' >&2
	exit 2
fi

RAIZ="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
GUION="$RAIZ/scripts/seed-adoption-otlp.py"
TRABAJO="$(mktemp -d)"
# ⛔ CONSTRUCTOR UNICO DE MUTANTES, Y FAIL-CLOSED. Al mover la redaccion a `scripts/lib/`, dos
#    mutantes siguieron apuntando al GUION mientras sus lineas ya vivian en la LIBRERIA: el
#    `assert` saltaba, el fichero no se escribia, y el caso lo contaba VERDE porque su condicion
#    era `if ! funcion "$mutante"` y una funcion que no puede cargar un fichero inexistente
#    TAMBIEN falla. Medido en `main` hoy: **26 pasan, 0 fallan** con DOS `AssertionError` dentro.
#    Un banco que anuncia verde con excepciones dentro es peor que uno rojo.
muta_fichero() { # $1 fuente · $2 destino · $3 viejo · $4 nuevo → rc 0 si el mutante quedo construido
	python3 - "$1" "$2" "$3" "$4" <<'PYMUT' || return 1
import sys
fuente, destino, viejo, nuevo = sys.argv[1:5]
src = open(fuente).read()
if viejo not in src:
    print(f"    ⛔ el ancla no esta en {fuente}: {viejo[:70]!r}", file=sys.stderr)
    sys.exit(1)
mut = src.replace(viejo, nuevo, 1)
if mut == src:
    print("    ⛔ el reemplazo no cambio nada", file=sys.stderr)
    sys.exit(1)
if fuente.endswith(".py"):
    compile(mut, destino, "exec")  # un mutante que no compila no acredita nada
open(destino, "w").write(mut)
PYMUT
	[ -s "$2" ] || return 1
	! cmp -s "$1" "$2" || return 1
	return 0
}

trap 'rm -rf "$TRABAJO"' EXIT

ok=0
fail=0
paso() { printf 'ok   %s\n' "$1"; ok=$((ok + 1)); }
malo() { printf 'FAIL %s\n' "$1"; fail=$((fail + 1)); }

# ⛔ LA SALIDA NO SE TIRA. Un caso que exige «rc 1» se conforma con un rc 1 de CUALQUIER causa —un
#    motor caido, una bandera mal escrita—, y eso es «un mutante acredita la pata que NOMBRA» dentro
#    del propio banco. Se guarda y `casa` exige que el veredicto DIGA lo que el caso afirma.
SALIDA="$TRABAJO/salida.txt"
rc_de() {
	local g="$1"
	shift
	python3 "$g" "$@" >"$SALIDA" 2>&1
	printf '%s' "$?"
}
casa() { command grep -qE "$1" "$SALIDA"; }

# muerte_valida <fichero de salida> — rc 0 si lo que hay es una muerte LEGIBLE del sujeto, y no un
# reventon del mutante.
# ⛔ ESTA MITAD FALTABA, y la encontro un lector. `muta_fichero` es fail-closed en la CONSTRUCCION
#    —si el ancla se movio, el fichero no se escribe— y este banco lo documenta seis veces. Nadie
#    cubria la EJECUCION: un mutante que se construye bien y luego revienta con un `KeyError` no
#    imprime el mensaje que el caso busca, asi que el `else` lo contaba como MUERTO y la bateria
#    seguia en 48/0. Es mi propia doctrina —«un mutante que revienta no acredita nada»— sin aplicar
#    en una rama de mi propio arnes: la muerte prematura sale por el mismo canal que el caso espera.
muerte_valida() {
	if [ ! -s "$1" ]; then
		return 1
	fi
	if command grep -qE 'Traceback \(most recent call last\)' "$1"; then
		return 1
	fi
	return 0
}

# ── 1 · FAMILIA «--sembrar INERTE»: cada bandera tiene que hacer LO QUE DICE ──────────────────
# El hermano llevaba una bandera que, tras un refactor, hacia lo mismo con y sin ella y nadie lo vio
# hasta la segunda lectura. Aqui se comprueba por CONDUCTA, no contando menciones en el fuente.
#
# ⛔ Y LA PRIMERA VERSION DE ESTE CASO ERA DEMASIADO DEBIL: exigia solo que el sobre CAMBIARA. Un
#    mutante que ignoraba `--dias` —fijando el rango a 29 dias— SOBREVIVIO, porque el generador
#    aleatorio es compartido y produce otra secuencia aunque el rango este roto: el sobre cambiaba
#    igual. «Cambia» prueba que la bandera se LEE; no prueba que haga lo que promete. Asi que cada
#    bandera se comprueba por su SEMANTICA, que es lo unico que un mutante no puede fingir.
if python3 - "$GUION" <<'PY'
import importlib.util, sys, time
s = importlib.util.spec_from_file_location("a", sys.argv[1])
m = importlib.util.module_from_spec(s)
s.loader.exec_module(m)
DIA = 86_400_000_000_000
ANCLA = m.ancla_de("2026-08-30")
fallos = []


def recursos(sobre):
    return sobre["resourceMetrics"]


def attrs(r):
    return {a["key"]: a["value"]["stringValue"] for a in r["resource"]["attributes"]}


# --equipos N -> EXACTAMENTE N equipos distintos
so = m.sobre_otlp(m.EQUIPOS[:2], 8, 30, "demo", ANCLA)
eq = {attrs(r)["team"] for r in recursos(so)}
if len(eq) != 2:
    fallos.append(f"--equipos 2 produjo {len(eq)} equipos")

# --por-equipo N -> EXACTAMENTE N sesiones por equipo
so = m.sobre_otlp(m.EQUIPOS[:3], 4, 30, "demo", ANCLA)
cuenta = {}
for r in recursos(so):
    cuenta[attrs(r)["team"]] = cuenta.get(attrs(r)["team"], 0) + 1
if set(cuenta.values()) != {4}:
    fallos.append(f"--por-equipo 4 produjo {sorted(set(cuenta.values()))} por equipo")

# --dias N -> TODAS las marcas dentro de [0, N-1] dias RESPECTO AL ANCLA
# ⛔ ESTE CASO SE PONIA ROJO SOLO, Y EL DIA PEOR. Medía la antiguedad contra `time.time_ns()`
#    —el reloj de pared— mientras el sobre va anclado a un `2026-08-30` escrito a mano dos lineas
#    mas arriba. Hoy da `viejo=2` y pasa JUSTO en el limite; mañana da 3 y FALLA, y pasado 4.
#    Simulado antes de curarlo, adelantando el reloj: hoy pasa, mañana falla, pasado falla.
#    Y no es un rojo cualquiera: esta bateria la corre el gancho desde que la cablee, asi que
#    habria puesto en rojo el carril rapido de TODA la flota el dia del acto. Una fecha escrita a
#    mano no envejece con el codigo que la usa; la referencia tiene que ser la MISMA que genera
#    los sellos.
#
#    Y se acota TAMBIEN el lado del futuro: `viejo > 2` dejaba pasar un sello POSTERIOR al ancla
#    —antiguedad negativa—, que es un generador roto en la otra direccion y nadie lo miraba.
so = m.sobre_otlp(m.EQUIPOS[:6], 8, 3, "demo", ANCLA)
sellos = [int(x["sum"]["dataPoints"][0]["timeUnixNano"])
          for r in recursos(so) for x in r["scopeMetrics"][0]["metrics"]]
edades = [(ANCLA - t) // DIA for t in sellos]
if max(edades) > 2:
    fallos.append(f"--dias 3 dejo una marca de {max(edades)} dias respecto al ancla")
if min(edades) < 0:
    fallos.append(f"--dias 3 dejo una marca {-min(edades)} dias EN EL FUTURO respecto al ancla")

# --prefijo P -> TODOS los session.id empiezan por P
so = m.sobre_otlp(m.EQUIPOS[:2], 2, 30, "otro", ANCLA)
malos = [attrs(r)["session.id"] for r in recursos(so)
         if not attrs(r)["session.id"].startswith("otro-")]
if malos:
    fallos.append(f"--prefijo otro dejo ids sin el prefijo: {malos[:2]}")

if fallos:
    print(fallos)
    sys.exit(1)
sys.exit(0)
PY
then
	paso "las cuatro banderas hacen lo que dicen (equipos, sesiones por equipo, antiguedad, prefijo)"
else
	malo "alguna bandera no cumple su semantica: es la familia del --sembrar inerte"
fi

# ── 2 · la FORMA del sobre, que es de lo que depende que el conector lo entienda ──────────────
# `session.id` va en atributos de RECURSO (no de datapoint) y la temporalidad es DELTA (1). Las dos
# las pide el conector; si alguien las mueve, el sobre se acepta con 200 y no ingiere nada — un
# verde que no mide, otra vez la familia A-02.
if python3 - "$GUION" <<'PY'
import importlib.util, sys
s = importlib.util.spec_from_file_location("a", sys.argv[1])
m = importlib.util.module_from_spec(s)
s.loader.exec_module(m)
ANCLA = m.ancla_de("2026-08-30")
rm = m.sobre_otlp(m.EQUIPOS[:2], 2, 7, "t", ANCLA)["resourceMetrics"][0]
claves = {a["key"] for a in rm["resource"]["attributes"]}
metricas = rm["scopeMetrics"][0]["metrics"]
fallos = []
if "session.id" not in claves:
    fallos.append("session.id no esta en los atributos de RECURSO")
if "team" not in claves:
    fallos.append("falta el atributo team (sin el, la pestaña de equipos sale sin nombre)")
if any(x["sum"]["aggregationTemporality"] != 1 for x in metricas):
    fallos.append("aggregationTemporality != 1 (DELTA)")
if not any(x["name"] == "claude_code.session.count" for x in metricas):
    fallos.append("falta claude_code.session.count, que es lo que cuenta sesiones")
if fallos:
    print(fallos)
    sys.exit(1)
sys.exit(0)
PY
then
	paso "el sobre lleva session.id en RECURSO, team, y temporalidad DELTA"
else
	malo "la forma del sobre no es la que el conector lee"
fi

# ── 3 · DOS CONSTRUCCIONES SEPARADAS DAN EL MISMO SOBRE, byte a byte ──────────────────────────
# ⛔ ES EL CASO QUE NO EXISTIA Y QUE HABRIA CAZADO EL FALLO QUE ME RETRACTE DE PUBLICAR. Yo probaba
#    que los `session.id` fueran estables — y lo eran— pero el generador llamaba a `time.time_ns()`,
#    asi que los SELLOS cambiaban y el sobre NO era identico. El receptor contesta 200 y el store
#    SUMA el delta, de modo que el re-pase DUPLICABA mientras mi banco decia que todo bien. Medido
#    despues por un lector: 356 -> 366 -> 376.
#
#    La propiedad que de verdad sostiene la idempotencia no es «los ids se repiten»: es **el sobre
#    entero es el mismo**. Y se comprueba construyendolo DOS VECES POR SEPARADO, con una pausa en
#    medio para que un reloj vivo se delate.
if python3 - "$GUION" <<'PY'
import importlib.util, json, sys, time
s = importlib.util.spec_from_file_location("a", sys.argv[1])
m = importlib.util.module_from_spec(s)
s.loader.exec_module(m)
ancla = m.ancla_de("2026-08-30")
a = m.sobre_otlp(m.EQUIPOS[:3], 4, 30, "demo", ancla)
time.sleep(1.1)  # si algo mira el reloj, este segundo lo delata
b = m.sobre_otlp(m.EQUIPOS[:3], 4, 30, "demo", ancla)
c = m.sobre_otlp(m.EQUIPOS[:3], 4, 30, "otro", ancla)
fallos = []
if json.dumps(a, sort_keys=True) != json.dumps(b, sort_keys=True):
    fallos.append("dos construcciones con los MISMOS argumentos salen distintas")
if json.dumps(a, sort_keys=True) == json.dumps(c, sort_keys=True):
    fallos.append("dos prefijos distintos dan el MISMO sobre")
d = m.sobre_otlp(m.EQUIPOS[:3], 4, 30, "demo", m.ancla_de("2026-08-29"))
if json.dumps(a, sort_keys=True) == json.dumps(d, sort_keys=True):
    fallos.append("dos anclas distintas dan el mismo sobre: el ancla seria inerte")
ids = [x["value"]["stringValue"] for r in a["resourceMetrics"]
       for x in r["resource"]["attributes"] if x["key"] == "session.id"]
if len(set(ids)) != len(ids):
    fallos.append("hay session.id repetidos dentro del mismo sobre")
if fallos:
    print(fallos)
    sys.exit(1)
sys.exit(0)
PY
then
	paso "dos construcciones separadas dan el MISMO sobre; otro prefijo u otra ancla, uno distinto"
else
	malo "el sobre no es reproducible: la idempotencia que promete la cabecera no se sostiene"
fi

# ── 3-bis · MUTANTE: el generador vuelve a mirar el reloj ─────────────────────────────────────
# Es el defecto exacto del que me retracte. Si sobrevive, el caso 3 no cubre nada.
# ⛔ CONSTRUIDO CON `muta_fichero`, QUE ES FAIL-CLOSED, Y NO CON UN `assert` CRUDO. El NO de
#    the reviewer lo midio: con un `assert` dentro del heredoc, si el ancla se mueve el fichero NO se
#    escribe, el sujeto real falla por libreria/fichero ausente, y el `if ! ...` cuenta eso como
#    VERDE. Un espaciado neutro en `redaccion.py` producia Traceback + AssertionError y el banco
#    seguia diciendo 29/0 rc 0. Es mi propia clase de esta tarde aplicada al fichero que faltaba.
m0="$TRABAJO/m0.py"
if ! muta_fichero "$GUION" "$m0" \
	'            ts = ancla_ns - r.randint(0, max(dias - 1, 0)) * dia' \
	'            ts = time.time_ns() - r.randint(0, max(dias - 1, 0)) * dia  # MUTANTE: reloj vivo'; then
	malo "NO se pudo construir el mutante 0 (reloj vivo): su ancla no esta en el sujeto"
elif python3 - "$m0" <<'PY2'
import importlib.util, json, sys, time
s = importlib.util.spec_from_file_location("a", sys.argv[1])
m = importlib.util.module_from_spec(s)
s.loader.exec_module(m)
ancla = m.ancla_de("2026-08-30")
a = m.sobre_otlp(m.EQUIPOS[:2], 2, 30, "demo", ancla)
time.sleep(1.1)
b = m.sobre_otlp(m.EQUIPOS[:2], 2, 30, "demo", ancla)
sys.exit(0 if json.dumps(a, sort_keys=True) == json.dumps(b, sort_keys=True) else 1)
PY2
then
	malo "el mutante del reloj vivo SOBREVIVIO: el caso 3 no detecta el defecto que lo motivo"
else
	paso "el mutante que devuelve el reloj vivo al generador MUERE en el caso 3 (sobres distintos)"
fi

# ⛔ Y ADEMAS m0 SE ACREDITA POR SU MENSAJE, no solo por la desigualdad: si el guion muriera antes
#    por otra causa, la desigualdad tambien saldria y el caso pasaria por el motivo equivocado.
muerto0="$(python3 - <<'PY2'
ports = set()
for f in ("/proc/net/tcp", "/proc/net/tcp6"):
    try:
        for ln in open(f).read().splitlines()[1:]:
            p = ln.split()
            if len(p) > 3 and p[3] == "0A":
                ports.add(int(p[1].split(":")[1], 16))
    except OSError:
        pass
print(next(p for p in range(29901, 30100) if p not in ports))
PY2
)"
r="$(rc_de "$m0" "http://127.0.0.1:$muerto0" tok ten --otlp "http://127.0.0.1:$muerto0/v1/metrics" --control-dedup)"
if [ "$r" = "1" ] && casa 'dos construcciones del MISMO sobre salen distintas'; then
	paso "m0 muere NOMBRANDO su causa: dos construcciones del mismo sobre salen distintas"
elif [ "$r" = "1" ]; then
	malo "m0 murio con rc 1 pero por otra causa: no acredita la pata que nombra"
else
	malo "m0 dio rc $r contra un puerto muerto: no llego a su asercion"
fi

# ⛔ m1 Y m2 SE JUZGAN POR UN OBSERVABLE EXACTO, NO POR MENSAJE, Y ESO ES DELIBERADO. m1 aplasta
#    RC_NO_PUDE_MIRAR a 0: su observable es EL PROPIO rc de la rama de ceguera, que es lo que el
#    mutante cambia — exigirle un mensaje seria exigirle que dijera algo que no le toca decir. m2
#    hace que una clave AUSENTE vuelva a valer cero: su observable es el `SystemExit` de
#    `sesiones_de`/`equipos_de` sobre un diccionario fabricado, comprobado en el caso 8 por
#    funcion y no por proceso. Un observable exacto acredita igual que un mensaje; lo que no
#    acredita es «un no-cero cualquiera».

# ── 3-ter · LA URL DEL RECEPTOR NO SALE ENTERA POR NINGUNA SALIDA ─────────────────────────────
# ⛔ Un lector encontro que la URL se imprimia COMPLETA en error y en exito, y que una credencial
#    sintetica embebida en el userinfo reaparecia en stderr. `sanea()` conserva esquema, host y
#    puerto —lo que hace falta para diagnosticar— y tapa el resto. Las dos direcciones: tiene que
#    OCULTAR la credencial y tiene que SEGUIR diciendo el host, o deja de servir para diagnosticar.
if python3 - "$GUION" <<'PY'
import importlib.util, sys
s = importlib.util.spec_from_file_location("a", sys.argv[1])
m = importlib.util.module_from_spec(s)
s.loader.exec_module(m)
fallos = []
sucia = "https://usuario:sk-abcdefghijklmnopqrstuvwx@collector.example:4318/v1/metrics?token=zzz"
limpia = m.sanea(sucia)
for prohibido in ("sk-abcdefghijklmnopqrstuvwx", "usuario", "token=zzz"):
    if prohibido in limpia:
        fallos.append(f"la URL saneada aun contiene {prohibido!r}")
if "collector.example" not in limpia or "4318" not in limpia:
    fallos.append(f"la URL saneada perdio el host o el puerto y no sirve para diagnosticar: {limpia!r}")
if fallos:
    print(fallos)
    sys.exit(1)
sys.exit(0)
PY
then
	paso "la URL del receptor se imprime saneada: sin credencial ni consulta, con host y puerto"
else
	malo "la URL sale entera por alguna salida: una credencial embebida acabaria en stderr"
fi

# ── 3-quater · LA FUGA, por sus DOS rutas, y con su mutante ───────────────────────────────────
# ⛔ ESTE CASO NO EXISTIA Y LA FUGA VIVIO DOS VERSIONES. El banco probaba `sanea()` AISLADA, y
#    `sanea()` estaba bien: lo que fallaba era todo lo demas. Dos rutas, las dos reproducidas con
#    codigo corriendo antes de curarlas:
#      (a) el `Request` se construia FUERA del `try`, asi que `Request('://usuario:sk-…@host/x')`
#          lanzaba `ValueError: unknown url type: <la URL entera>` que nadie capturaba;
#      (b) `HTTPError` devolvia `e.read()` CRUDO y `main` lo imprimia: un receptor que conteste 400
#          repitiendo la URL filtra por stderr.
#    Probar la funcion de saneado y no las SALIDAS es, otra vez, medir lo de al lado.
if python3 - "$GUION" <<'PY'
import importlib.util, io, contextlib, sys, threading, http.server, traceback
s = importlib.util.spec_from_file_location("a", sys.argv[1])
m = importlib.util.module_from_spec(s)
s.loader.exec_module(m)
SEC = "sk-abcdefghijklmnopqrstuvwx"
fugas = []
# (a) rutas donde la URL malformada revienta dentro de urllib
for u in (f"://usuario:{SEC}@host/v1/metrics", SEC,
          f"https://usuario:{SEC}@/v1/metrics?token=zzz",
          f"https://usuario:{SEC}@no.invalid:4318/v1/metrics"):
    err, out, cap = io.StringIO(), io.StringIO(), ""
    try:
        with contextlib.redirect_stderr(err), contextlib.redirect_stdout(out):
            m.postear(u, {"resourceMetrics": []})
    except SystemExit:
        pass
    except Exception as e:
        cap = f"{type(e).__name__}: {e}\n" + traceback.format_exc()
    if SEC in err.getvalue() + out.getvalue() + cap:
        fugas.append(f"URL malformada {u[:28]}…")
# (b) el cuerpo de un 400 que repite la URL
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        self.send_response(400)
        self.end_headers()
        self.wfile.write(f"rejected endpoint https://usuario:{SEC}@localhost/v1/metrics".encode())
    def log_message(self, *a):
        pass
srv = http.server.HTTPServer(("127.0.0.1", 0), H)
threading.Thread(target=srv.serve_forever, daemon=True).start()
try:
    _, cuerpo = m.postear(f"http://127.0.0.1:{srv.server_port}/v1/metrics", {"resourceMetrics": []})
    if SEC in cuerpo:
        fugas.append("cuerpo del HTTPError devuelto crudo")
finally:
    srv.shutdown()
if fugas:
    print("FUGA:", fugas)
    sys.exit(1)
sys.exit(0)
PY
then
	paso "ninguna de las dos rutas de fuga saca la credencial (URL malformada ni cuerpo de un 400)"
else
	malo "la credencial sale por alguna salida: la redaccion no cubre todos los caminos"
fi

# ── 3-quinquies · MUTANTE: se quita la redaccion de la frontera ───────────────────────────────
# ⛔ EL MUTANTE ATACA AHORA LA LIBRERIA, no una copia local: desde la v8 la redaccion vive en
#    `scripts/lib/redaccion.py` y este guion solo la envuelve. Un mutante que siga apuntando a la
#    funcion que ya no esta aqui no muta nada y se cuenta verde — me paso al escribirlo.
# ⛔ CONSTRUIDO CON `muta_fichero`, QUE ES FAIL-CLOSED, Y NO CON UN `assert` CRUDO. El NO de
#    the reviewer lo midio: con un `assert` dentro del heredoc, si el ancla se mueve el fichero NO se
#    escribe, el sujeto real falla por libreria/fichero ausente, y el `if ! ...` cuenta eso como
#    VERDE. Un espaciado neutro en `redaccion.py` producia Traceback + AssertionError y el banco
#    seguia diciendo 29/0 rc 0. Es mi propia clase de esta tarde aplicada al fichero que faltaba.
mkdir -p "$TRABAJO/libnula"
if ! muta_fichero "$RAIZ/scripts/lib/redaccion.py" "$TRABAJO/libnula/redaccion.py" \
	'        fuera = str(texto)' \
	'        return str(texto)  # MUTANTE: la frontera no redacta nada
        fuera = str(texto)'; then
	malo "NO se pudo construir el mutante de la frontera nula: su ancla no esta en redaccion.py"
elif ! OLIVARES_LIB_DIR="$TRABAJO/libnula" cred_arbitraria "$GUION" >/dev/null 2>&1; then
	paso "el mutante que anula la frontera de la libreria FUGA: los casos de credencial lo cazan"
else
	malo "anular la frontera no produce fuga: los testigos no ejercitan lo que dicen"
fi

# ── 3-sexies · UNA CREDENCIAL ARBITRARIA, QUE ES LO QUE EL BANCO NO PROBABA ────────────────────
# ⛔ ES EL NO DE the reviewer (A-01 sobre `89dc52767`) Y TENIA RAZON EN TODO. El testigo de 3-quater usa
#    SIEMPRE `sk-…`, una forma que el respaldo por regex tapa aunque `_SENSIBLES` no sepa nada del
#    secreto: la prueba pasaba **por el camino equivocado** y dejaba sin cubrir el caso que importa,
#    una credencial que no se parece a ningun token conocido.
#
# ⛔ Y HAY UNA SEGUNDA LECCION, QUE ME MORDIO AL ESCRIBIR ESTE CASO: mi primer testigo corria las
#    dos rutas EN EL MISMO PROCESO, y las llamadas de la ruta (a) dejaban el secreto en
#    `_SENSIBLES`, asi que la ruta (b) llegaba ya tapada y daba verde. La ruta (b) se prueba EN
#    AISLAMIENTO o no se prueba: el cuerpo de un 400 lo escribe el SERVIDOR y puede traer una
#    credencial que este guion no ha visto nunca.
SEC_ARB="Zq8plano-nada-especial-2026"

cred_arbitraria() { # $1 = guion sujeto; rc 0 = sin fugas
	SUJETO="$1" SEC_ARB="$SEC_ARB" OLIVARES_LIB_DIR="${OLIVARES_LIB_DIR:-}" python3 - <<'PY2'
import contextlib, importlib.util, io, os, sys, traceback
SEC = os.environ["SEC_ARB"]
spec = importlib.util.spec_from_file_location("m", os.environ["SUJETO"])
m = importlib.util.module_from_spec(spec); sys.modules["m"] = m; spec.loader.exec_module(m)
fugas = []
for u in (f"://usuario:{SEC}@host/v1/metrics", SEC,
          f"https://usuario:{SEC}@/v1/metrics?token=zzz",
          f"https://usuario:{SEC}@no.invalid:4318/v1/metrics"):
    err, out, cap = io.StringIO(), io.StringIO(), ""
    try:
        with contextlib.redirect_stderr(err), contextlib.redirect_stdout(out):
            m.postear(u, {"resourceMetrics": []})
    except SystemExit:
        pass
    except Exception as e:
        cap = f"{type(e).__name__}: {e}\n" + traceback.format_exc()
    if SEC in err.getvalue() + out.getvalue() + cap:
        fugas.append("url-malformada")
print("FUGAS:" + ",".join(fugas) if fugas else "SIN-FUGAS")
sys.exit(1 if fugas else 0)
PY2
}

cuerpo_400_aislado() { # $1 = guion sujeto; rc 0 = tapado. Proceso NUEVO a proposito.
	SUJETO="$1" SEC_ARB="$SEC_ARB" OLIVARES_LIB_DIR="${OLIVARES_LIB_DIR:-}" python3 - <<'PY2'
import http.server, importlib.util, os, sys, threading
SEC = os.environ["SEC_ARB"]
spec = importlib.util.spec_from_file_location("m", os.environ["SUJETO"])
m = importlib.util.module_from_spec(spec); sys.modules["m"] = m; spec.loader.exec_module(m)
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        self.send_response(400); self.end_headers()
        self.wfile.write(f"rejected endpoint https://usuario:{SEC}@localhost/v1/metrics".encode())
    def log_message(self, *a): pass
srv = http.server.HTTPServer(("127.0.0.1", 0), H)
threading.Thread(target=srv.serve_forever, daemon=True).start()
try:
    _, cuerpo = m.postear(f"http://127.0.0.1:{srv.server_port}/v1/metrics", {"resourceMetrics": []})
finally:
    srv.shutdown()
sys.exit(1 if SEC in cuerpo else 0)
PY2
}

if cred_arbitraria "$GUION" >/dev/null 2>&1; then
	paso "una credencial ARBITRARIA no sale por ninguna ruta de URL malformada"
else
	malo "una credencial que no casa ningun regex FUGA por una URL malformada"
fi
if cuerpo_400_aislado "$GUION" >/dev/null 2>&1; then
	paso "el cuerpo de un 400 con una credencial NUNCA VISTA sale tapado (proceso aislado)"
else
	malo "el cuerpo de un 400 filtra una credencial que el guion no habia visto"
fi

# ── 3-octies · LA EXCEPCION ENCADENADA, QUE MI PROPIO BANCO DESCARTABA ─────────────────────────
# ⛔ the reviewer, A-01 sobre `817bc4a4d`, y el hallazgo es de los que enseñan: stdout y stderr salian
#    LIMPIOS y aun asi el secreto viajaba. Un `raise SystemExit(...)` dentro de un `except`
#    ENCADENA la excepcion original en `__context__`, y esa lleva la URL entera con la credencial
#    aunque el mensaje ya no la lleve. Redactar el texto y dejar el contexto colgando es tapar la
#    puerta y dejar la ventana.
#
#    Y mi banco no podia verlo porque hacia `except SystemExit: pass` — **descartaba justo el
#    objeto que la llevaba**. Un testigo que captura y tira no mide: hay que INSPECCIONAR.
contexto_limpio() { # $1 = guion sujeto; rc 0 = ninguna excepcion encadenada lleva el secreto
	SUJETO="$1" SEC_ARB="$SEC_ARB" OLIVARES_LIB_DIR="${OLIVARES_LIB_DIR:-}" python3 - <<'PY2'
import contextlib, importlib.util, io, os, sys
SEC = os.environ["SEC_ARB"]
spec = importlib.util.spec_from_file_location("m", os.environ["SUJETO"])
m = importlib.util.module_from_spec(spec); sys.modules["m"] = m; spec.loader.exec_module(m)
malas = []
for u in (f"://usuario:{SEC}@host/v1/metrics", f"https://usuario:{SEC}@no.invalid:4318/v1/metrics"):
    try:
        with contextlib.redirect_stderr(io.StringIO()), contextlib.redirect_stdout(io.StringIO()):
            m.postear(u, {"resourceMetrics": []})
    except BaseException as e:
        # Se RECORRE la cadena entera, que es lo que el banco anterior no hacia.
        vistos, cur = 0, e
        while cur is not None and vistos < 12:
            if SEC in f"{cur!r}" + f"{cur}":
                malas.append(type(cur).__name__)
                break
            cur = cur.__context__ or cur.__cause__
            vistos += 1
sys.exit(1 if malas else 0)
PY2
}

if contexto_limpio "$GUION"; then
	paso "ninguna excepcion ENCADENADA lleva el secreto (se recorre __context__/__cause__, no se descarta)"
else
	malo "el secreto viaja en una excepcion encadenada aunque el mensaje salga limpio"
fi

# ── 3-nonies · UNA CABECERA REFLEJADA, CON UN VALOR ARBITRARIO ─────────────────────────────────
# ⛔ TERCERA FRONTERA, y ni los valores recordados ni el userinfo posicional ni los regex de formas
#    la cubrian: un receptor que conteste 400 reflejando `Authorization: Bearer <lo-que-sea>`
#    devuelve el token. Se reconoce por el NOMBRE de la cabecera —conjunto cerrado y conocido— y se
#    tapa el valor entero sin mirar a que se parece.
cabecera_reflejada() { # $1 = guion sujeto; rc 0 = tapado. Proceso NUEVO.
	# ⛔ `OLIVARES_LIB_DIR` SE REENVIA A MANO, y esto me costo un mutante que parecia sobrevivir.
	#    Un prefijo de variable delante de una FUNCION de bash la fija en el shell pero **no la
	#    exporta**, asi que el `python3` hijo no la veia y cargaba la libreria REAL: el mutante se
	#    construia, no se aplicaba, y el banco lo contaba como «sobrevivio». Lo destape probando la
	#    libreria mutada en directo —fugaba— contra el caso —no fugaba—: cuando el sujeto y el
	#    banco discrepan, el sospechoso es el arnes.
	SUJETO="$1" SEC_ARB="$SEC_ARB" OLIVARES_LIB_DIR="${OLIVARES_LIB_DIR:-}" python3 - <<'PY2'
import contextlib, http.server, importlib.util, io, os, sys, threading
SEC = os.environ["SEC_ARB"]
spec = importlib.util.spec_from_file_location("m", os.environ["SUJETO"])
m = importlib.util.module_from_spec(spec); sys.modules["m"] = m; spec.loader.exec_module(m)


# ⛔ EL RECEPTOR DEVUELVE UNA CREDENCIAL QUE EL GUION NO CONOCE, y eso es el caso. Si reflejara
#    NUESTRO token, lo taparia el mecanismo de «secretos declarados» y este testigo daria verde sin
#    ejercer la frontera de cabecera — me paso al escribirlo y el mutante sobrevivio. Un receptor
#    puede repetir la credencial de un proxy o de un salto anterior, y esa no la conocemos: por eso
#    la cabecera se reconoce por su NOMBRE y se tapa el valor entero, sea cual sea.
AJENA = "Up7-credencial-de-otro-salto-2026"


class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        self.send_response(400); self.end_headers()
        # ⛔ LA CABECERA REFLEJADA ES PARAMETRO, NO CONSTANTE, y esto es lo que hace util al
        #    mutante por entrada: con `X-Api-Key` fija, quitar `authorization` de la alternancia
        #    NO fugaba —porque la cabecera del testigo la seguia tapando `api-key`— y el caso
        #    concluia «esa entrada no la cubre nadie» sobre una entrada perfectamente cubierta.
        #    Medido: 2 rojos de 3. Cada mutante tiene que reflejar SU cabecera.
        self.wfile.write((f"upstream rejected: {os.environ.get('CAB','X-Api-Key')}: " + AJENA).encode())

    def log_message(self, *a):
        pass


srv = http.server.HTTPServer(("127.0.0.1", 0), H)
threading.Thread(target=srv.serve_forever, daemon=True).start()
err, out = io.StringIO(), io.StringIO()
try:
    with contextlib.redirect_stderr(err), contextlib.redirect_stdout(out):
        try:
            # ⛔ SE EJERCE `postear`, NO `leer`, y la diferencia es el caso: `leer` sólo emite el
            #    `HTTPError` —cuyo texto es «HTTP Error 400: Bad Request», SIN el cuerpo—, asi que
            #    por ahi la credencial reflejada no sale ni con la frontera quitada. El testigo
            #    tiene que mirar donde el cuerpo SI viaja. Me costo un mutante que parecia inmortal.
            _, cuerpo = m.postear(f"http://127.0.0.1:{srv.server_port}/v1/metrics",
                                  {"resourceMetrics": []})
        except BaseException:
            cuerpo = ""
finally:
    srv.shutdown()
sys.exit(1 if AJENA in (cuerpo + err.getvalue() + out.getvalue()) else 0)
PY2
}

if cabecera_reflejada "$GUION"; then
	paso "un 400 que refleja una cabecera con una credencial AJENA sale tapado"
else
	malo "una cabecera reflejada saca una credencial que el guion no conoce: falta esa frontera"
fi

# ⛔ CONSTRUIDO CON `muta_fichero`, QUE ES FAIL-CLOSED, Y NO CON UN `assert` CRUDO. El NO de
#    the reviewer lo midio: con un `assert` dentro del heredoc, si el ancla se mueve el fichero NO se
#    escribe, el sujeto real falla por libreria/fichero ausente, y el `if ! ...` cuenta eso como
#    VERDE. Un espaciado neutro en `redaccion.py` producia Traceback + AssertionError y el banco
#    seguia diciendo 29/0 rc 0. Es mi propia clase de esta tarde aplicada al fichero que faltaba.
mkdir -p "$TRABAJO/libmut"
if ! muta_fichero "$RAIZ/scripts/lib/redaccion.py" "$TRABAJO/libmut/redaccion.py" \
	'        fuera = _RX_CABECERA.sub(' \
	'        fuera = fuera if True else _RX_CABECERA.sub('; then
	malo "NO se pudo construir el mutante de la frontera de CABECERA: su ancla no esta en redaccion.py"
elif ! OLIVARES_LIB_DIR="$TRABAJO/libmut" cabecera_reflejada "$GUION"; then
	paso "el mutante que quita la frontera de CABECERA FUGA: el caso 3-nonies lo caza"
else
	malo "quitar la frontera de cabecera no produce fuga: ese caso no ejercita lo que dice"
fi

# ⛔ CONSTRUIDO CON `muta_fichero`, QUE ES FAIL-CLOSED, Y NO CON UN `assert` CRUDO. El NO de
#    the reviewer lo midio: con un `assert` dentro del heredoc, si el ancla se mueve el fichero NO se
#    escribe, el sujeto real falla por libreria/fichero ausente, y el `if ! ...` cuenta eso como
#    VERDE. Un espaciado neutro en `redaccion.py` producia Traceback + AssertionError y el banco
#    seguia diciendo 29/0 rc 0. Es mi propia clase de esta tarde aplicada al fichero que faltaba.
mX="$TRABAJO/mX.py"
if ! muta_fichero "$GUION" "$mX" \
	'        motivo = f"{type(e).__name__}: {e}"' \
	'        raise SystemExit(salir(RC_NO_PUDE_MIRAR, redacta(f"no alcanzo el receptor OTLP en {sanea(url)}: {type(e).__name__}: {e}", url)))'; then
	malo "NO se pudo construir el mutante del encadenado: su ancla no esta en el sujeto"
elif ! contexto_limpio "$mX"; then
	paso "el mutante que vuelve a lanzar DENTRO del except encadena el secreto: el caso 3-octies lo caza"
else
	malo "volver a encadenar no produce contexto con secreto: ese caso no ejercita lo que dice"
fi

# ── 3-septies · LOS DOS MUTANTES DE LA LIBRERIA, cada uno matando SU ruta ─────────────────────
# ⛔ APUNTABAN AL GUION Y SUS LINEAS VIVEN EN `redaccion.py` (the reviewer, A-03). Ver la razon en
#    `muta_fichero`: sin constructor fail-closed esto salia verde sin haberse aplicado nunca.
mkdir -p "$TRABAJO/lib-pelada" "$TRABAJO/lib-posicional"
if muta_fichero "$RAIZ/scripts/lib/redaccion.py" "$TRABAJO/lib-pelada/redaccion.py" \
	'        if "://" not in url and "//" not in url and len(url) >= 8:' \
	'        if False:  # MUTANTE: la credencial pelada ya no se recuerda'; then
	if ! OLIVARES_LIB_DIR="$TRABAJO/lib-pelada" cred_arbitraria "$GUION" >/dev/null 2>&1; then
		paso "el mutante que deja de recordar la credencial PELADA FUGA: el caso 3-sexies lo caza"
	else
		malo "quitar el recuerdo de la credencial pelada no produce fuga: ese caso no ejercita nada"
	fi
else
	malo "NO se pudo construir el mutante de la credencial pelada: su ancla no esta en redaccion.py"
fi

if muta_fichero "$RAIZ/scripts/lib/redaccion.py" "$TRABAJO/lib-posicional/redaccion.py" \
	'        fuera = _RX_USERINFO.sub("//<oculto>@", fuera)' \
	'        pass  # MUTANTE: el texto ajeno ya no se tapa por posicion'; then
	if ! OLIVARES_LIB_DIR="$TRABAJO/lib-posicional" cuerpo_400_aislado "$GUION" >/dev/null 2>&1; then
		paso "el mutante que deja de tapar por POSICION FUGA por el cuerpo del 400"
	else
		malo "quitar la redaccion posicional no produce fuga: ese caso no ejercita nada"
	fi
else
	malo "NO se pudo construir el mutante posicional: su ancla no esta en redaccion.py"
fi

# ── 3-decies · UN MUTANTE POR ENTRADA DE CABECERA ─────────────────────────────────────────────
# ⛔ MI MUTANTE ANTERIOR QUITABA EL REGEX ENTERO, y eso solo prueba que la regla existe — no que
#    CADA nombre de la lista este cubierto (the reviewer, A-03). Retirar `x-auth-token` dejaba su valor
#    saliendo y ninguna fila lo decia. Ahora hay un mutante por entrada: se quita ESE nombre de la
#    alternancia y se comprueba que su cabecera fuga, con la fila nombrandolo.
# Las cinco FORMAS que se prueban contra las TRES entradas que quedan (dos eran redundantes por
# `\b`, ver la razon en la libreria): cada nombre de aqui debe fugar al quitar su entrada.
for CAB_NOMBRE in authorization api-key auth-token; do
	D="$TRABAJO/lib-cab-$CAB_NOMBRE"
	mkdir -p "$D"
	# ⛔ LA MUTACION SE ANCLA A LA LINEA ENTERA DEL REGEX, no al nombre suelto, y por dos razones
	#    que me mordieron: (1) esos mismos nombres aparecen en la PROSA que explica esto, asi que
	#    mutar la primera aparicion tocaba el comentario y no el codigo; y (2) la ultima entrada no
	#    lleva `|` detras, asi que `"$CAB_NOMBRE|"` no casaba para ella y su mutante no se construia
	#    — el banco lo dijo en rojo, que es lo que se le pidio hacer.
	# ⛔ UNA barra, no dos: el fichero tiene `\b` y mi literal llevaba `\\b`. El banco lo dijo en
	#    rojo tres veces —«su nombre no esta en el regex»— en vez de contarlas verdes, que es
	#    exactamente para lo que se le puso el fail-closed.
	LINEA_RX='    r"(?i)\b(authorization|api-key|auth-token)"'
	LINEA_MUT="$(printf '%s' "$LINEA_RX" | sed -E "s/\\|?${CAB_NOMBRE}\\|?/|/; s/\\(\\|/(/; s/\\|\\)/)/")"
	if muta_fichero "$RAIZ/scripts/lib/redaccion.py" "$D/redaccion.py" \
		"$LINEA_RX" "$LINEA_MUT" ; then
		if ! CAB="$CAB_NOMBRE" OLIVARES_LIB_DIR="$D" cabecera_reflejada "$GUION" >/dev/null 2>&1; then
			paso "quitar \`$CAB_NOMBRE\` de la alternancia FUGA su cabecera: esa entrada esta cubierta"
		else
			malo "quitar \`$CAB_NOMBRE\` no produce fuga: esa entrada de la lista no la cubre nadie"
		fi
	else
		malo "NO se pudo construir el mutante de \`$CAB_NOMBRE\`: su nombre no esta en el regex"
	fi
done

# ── 4 · el rc de «no he podido mirar»: un receptor donde no hay nadie ─────────────────────────
# El puerto libre se elige MIRANDO `/proc/net/tcp` con python3, no con `awk`: el de esta caja es
# mawk y no tiene `strtonum`, asi que una sonda con el devuelve la lista VACIA y eso se lee como
# «no hay nada escuchando». Costo dos lecturas falsas el 2026-08-30.
muerto="$(python3 - <<'PY'
ports = set()
for f in ("/proc/net/tcp", "/proc/net/tcp6"):
    try:
        for ln in open(f).read().splitlines()[1:]:
            p = ln.split()
            if len(p) > 3 and p[3] == "0A":
                ports.add(int(p[1].split(":")[1], 16))
    except OSError:
        pass
print(next(p for p in range(29501, 29800) if p not in ports))
PY
)"
r="$(rc_de "$GUION" "http://127.0.0.1:$muerto" tok ten --otlp "http://127.0.0.1:$muerto/v1/metrics")"
if [ "$r" = "2" ]; then
	paso "motor/receptor inalcanzable => rc 2 (no he podido mirar), no 0 ni 1"
else
	malo "inalcanzable deberia salir 2 y salio $r"
fi

# ── 5 · MUTANTE: la ceguera se confunde con limpieza ──────────────────────────────────────────
# La regla 5 del canon, y el defecto que sus lectores encontraron dos veces en el hermano.
# ⛔ CONSTRUIDO CON `muta_fichero`, QUE ES FAIL-CLOSED, Y NO CON UN `assert` CRUDO. El NO de
#    the reviewer lo midio: con un `assert` dentro del heredoc, si el ancla se mueve el fichero NO se
#    escribe, el sujeto real falla por libreria/fichero ausente, y el `if ! ...` cuenta eso como
#    VERDE. Un espaciado neutro en `redaccion.py` producia Traceback + AssertionError y el banco
#    seguia diciendo 29/0 rc 0. Es mi propia clase de esta tarde aplicada al fichero que faltaba.
m1="$TRABAJO/m1.py"
if ! muta_fichero "$GUION" "$m1" \
	'RC_LIMPIO, RC_RECHAZADO, RC_NO_PUDE_MIRAR = 0, 1, 2' \
	'RC_LIMPIO, RC_RECHAZADO, RC_NO_PUDE_MIRAR = 0, 1, 0'; then
	malo "NO se pudo construir el mutante 1 (rc 2 aplastado a 0): su ancla no esta en el sujeto"
fi
r="$(rc_de "$m1" "http://127.0.0.1:$muerto" tok ten --otlp "http://127.0.0.1:$muerto/v1/metrics")"
if [ "$r" = "0" ]; then
	paso "el mutante que aplasta 'no pude mirar' a 'limpio' es DETECTABLE por el caso 4"
else
	malo "el mutante 1 no produjo el 0 que el caso 4 caza (dio $r)"
fi

# ── 6 · MUTANTE: el control de deduplicacion se queda con UNA sola mitad ──────────────────────
# ⛔ ES LA FAMILIA A-05 —«un control acredita lo que NOMBRA»— aplicada aqui. `--control-dedup`
#    afirma dos cosas: que la cifra SUBE en la primera ejecucion y que NO se mueve en la SEGUNDA.
#    Sin la primera, un receptor que descartara TODO pasaria la prueba de «no duplica» con nota.
#    ⚠ Y «segunda EJECUCION» no es «reenvio»: la version anterior de este control mandaba el mismo
#    sobre en memoria dos veces y por eso blindo una afirmacion falsa. El caso 3 es quien garantiza
#    que las dos ejecuciones produzcan el mismo sobre; este garantiza que el control siga
#    afirmando las dos cosas.
if python3 - "$GUION" <<'PY'
import sys, re
src = open(sys.argv[1]).read()
cuerpo = src[src.index("def control_dedup"):src.index("def main(")]
tiene_subida = "uno != por_equipo" in cuerpo
tiene_quietud = "dos != uno" in cuerpo
if not (tiene_subida and tiene_quietud):
    print("control_dedup ha perdido una de sus dos mitades:",
          "sube" if tiene_subida else "SIN la asercion de subida",
          "|", "quieta" if tiene_quietud else "SIN la asercion de quietud")
    sys.exit(1)
sys.exit(0)
PY
then
	paso 'el control conserva sus DOS mitades (sube en la 1.a ejecucion, no se mueve en la 2.a)'
else
	malo 'el control se ha quedado con una mitad: un receptor que descarte todo lo pasaria'
fi

# ── 7 · la cabecera sigue nombrando el hallazgo que decide si el sembrado sirve ───────────────
# Si alguien recorta la cabecera y se lleva la lente por defecto, se pierde LO UNICO que convierte
# este trabajo en una captura util. No es prosa: es el requisito operativo.
if command grep -q "useState<LensId>('analytics')" "$GUION" && command grep -q 'telemetry' "$GUION"; then
	paso "la cabecera sigue nombrando la lente por defecto vacia y su remedio"
else
	malo "se ha perdido el aviso de la lente por defecto: sembrar sin el produce una captura vacia"
fi

# ── 8 · AUSENTE no es CERO, por sus dos direcciones ───────────────────────────────────────────
# ⛔ ES LA FAMILIA QUE UN LECTOR ME ENCONTRO EN EL HERMANO Y QUE YO TENIA AQUI TAMBIEN.
#    `d.get("sessions", 0)` convierte «el motor ya no devuelve ese campo» en «no hay sesiones», y a
#    partir de ahi el guion acusa al SEMBRADO de lo que es un cambio de contrato. Las dos
#    direcciones importan: ausente tiene que ser 2, y un cero legitimo tiene que seguir siendo un
#    veredicto — si la guarda tratara el 0 como ausencia, un estate vacio saldria «no pude mirar» y
#    nadie se enteraria de que no hay datos.
# 2>/dev/null: los fixtures de abajo hacen que el guion imprima sus «no he podido mirar» a stderr,
# que es justo lo que se les pide; sin silenciarlo, el banco parece roto cuando esta pasando.
if python3 - "$GUION" 2>/dev/null <<'PY'
import importlib.util, sys
s = importlib.util.spec_from_file_location("a", sys.argv[1])
m = importlib.util.module_from_spec(s)
s.loader.exec_module(m)
fallos = []


def rc_de_llamada(fn, arg):
    try:
        fn(arg)
    except SystemExit as e:
        return e.code
    return "sin salir"


# AUSENTE -> 2
for etiqueta, d in [("sin telemetry", {}),
                    ("sin totals", {"telemetry": {}}),
                    ("totals sin sessions", {"telemetry": {"totals": {"commits": 3}}})]:
    r = rc_de_llamada(m.sesiones_de, d)
    if r != 2:
        fallos.append(f"{etiqueta}: esperaba 2 y dio {r!r}")
r = rc_de_llamada(m.equipos_de, {})
if r != 2:
    fallos.append(f"teams ausente: esperaba 2 y dio {r!r}")

# CERO LEGITIMO -> sigue siendo un veredicto, no una ceguera
try:
    v = m.sesiones_de({"telemetry": {"totals": {"sessions": 0}}})
    if v != 0:
        fallos.append(f"sessions=0 devolvio {v!r}")
except SystemExit as e:
    fallos.append(f"sessions=0 salio {e.code}: un cero legitimo NO es ceguera")
try:
    v = m.equipos_de({"teams": []})
    if v != []:
        fallos.append(f"teams=[] devolvio {v!r}")
except SystemExit as e:
    fallos.append(f"teams=[] salio {e.code}: una lista vacia legitima NO es ceguera")

if fallos:
    print(fallos)
    sys.exit(1)
sys.exit(0)
PY
then
	paso "un campo AUSENTE sale 2 y un cero legitimo sigue siendo veredicto"
else
	malo "no separa ausente de cero: un cambio de contrato se leeria como «no hay datos»"
fi

# ── 9 · MUTANTE: se vuelve al `.get(campo, 0)` y el caso 8 tiene que matarlo ───────────────────
# ⛔ DOS SUSTITUCIONES, DOS PASADAS DEL CONSTRUCTOR FAIL-CLOSED. La version anterior hacia la
#    segunda con un `replace` SIN comprobar —solo la primera llevaba `assert`—, asi que si el
#    `return tot["sessions"]` cambiaba de forma, el mutante se construia A MEDIAS y el caso lo
#    juzgaba igual: mediria el `if False` sin el `.get`, que no es el defecto que nombra.
m2="$TRABAJO/m2.py"
if ! muta_fichero "$GUION" "$TRABAJO/m2-paso1.py" \
	'    if "sessions" not in tot:' \
	'    if False:  # MUTANTE: ausente vuelve a ser cero'; then
	malo "NO se pudo construir el mutante 2 (paso 1): su ancla no esta en el sujeto"
elif ! muta_fichero "$TRABAJO/m2-paso1.py" "$m2" \
	'    return tot["sessions"]' \
	'    return tot.get("sessions", 0)'; then
	malo "NO se pudo construir el mutante 2 (paso 2): el .get que lo completa no se aplico"
else
	# ⛔ EL JUICIO VA DENTRO DEL `else`, Y ANTES ESTABA FUERA DEL `if` (the reviewer sobre `74605016c`).
	#    Con el ancla del paso 2 movida, el banco imprimia el FAIL correcto del constructor y A
	#    CONTINUACION cargaba un `m2.py` INEXISTENTE: el `FileNotFoundError` daba rc 1, que es
	#    exactamente lo que este caso lee como «el mutante MUERE», y contaba `ok` detras. Un
	#    artefacto no construido recibia juicio POSITIVO — mi propia clase, en el sitio que crei
	#    haber cerrado esta tarde. Dos constructores exigen que el juicio cuelgue de los DOS.
	salida_m2="$(python3 - "$m2" <<'PY' 2>&1
import importlib.util, sys
s = importlib.util.spec_from_file_location("a", sys.argv[1])
m = importlib.util.module_from_spec(s)
s.loader.exec_module(m)
try:
    m.sesiones_de({"telemetry": {"totals": {"commits": 3}}})
except SystemExit as e:
    sys.exit(0 if e.code == 2 else 1)
sys.exit(1)
PY
)"
	r=$?
	# Y se distingue el DEFECTO del CRASH: un `Traceback` da rc 1 igual que el mutante bueno.
	if command grep -q 'Traceback' <<<"$salida_m2"; then
		malo "el mutante 2 murio con una EXCEPCION, no con el defecto: eso no acredita el caso 8"
	elif [ "$r" = "1" ]; then
		paso 'el mutante que devuelve al get-con-defecto MUERE: el caso 8 cubre algo'
	else
		malo "el mutante del get-con-defecto SOBREVIVIO (rc $r)"
	fi
fi

# ── N · LOS MENSAJES DE ESTE BANCO NO LLEVAN BACKTICKS SIN ESCAPAR ────────────────────────────
# ⛔ NO ES ESTILO: EN `paso "…`palabra`…"` LA SHELL EJECUTA `palabra` COMO COMANDO. Me paso CUATRO
#    veces el 2026-08-30, en cuatro ficheros distintos, y el sintoma es un hueco en la salida —
#    `«el mutante que abre la guarda MUERE:  no se puede colar»`— o un `command not found` suelto.
#    Se ve en la SALIDA, no en el codigo, asi que revisarlo a ojo no funciona: por eso es un caso.
#    Comillas SIMPLES, o backtick escapado.
# ⛔ Sin tuberia, y no es estilo: bajo `set -o pipefail` un `productor | grep -q X` devuelve
# **141 CUANDO ACIERTA** — `grep -q` sale en el primer acierto y el productor muere con SIGPIPE—,
# asi que este `if` tomaba la rama FALSA justo cuando debia tomar la verdadera: un autocontrol
# que deja de detectar y no lo dice. Lo cazo `lint:sigpipe-booleans`, que estaba ROJO en main
# para toda la flota por esta unica linea. La forma la sugiere el propio hallazgo del gate.
if command grep -q '[^\\]`' <(command grep -nE "^[[:space:]]*(paso|malo) \"" "$0"); then
	command grep -nE "^[[:space:]]*(paso|malo) \"" "$0" | command grep '[^\\]`' >&2
	malo "hay mensajes con backticks SIN escapar dentro de comillas dobles: la shell los ejecuta"
else
	paso "ningun mensaje del banco lleva un backtick sin escapar dentro de comillas dobles"
fi

# ── 6-bis · EL CONTROL DE DEDUPLICACION, HERMETICO, Y SU MUTANTE DEL FILTRO ────────────────────
# ⛔ ESTOS DOS CASOS ESTABAN DETRAS DE `OLIVARES_VERIFY_ENGINE` Y POR ESO NO ATABAN NADA. El lector
#    lo dijo con la medida delante: el mutante fiel que quita el filtro por equipo dejaba el banco
#    en 15/15 cuando no hay motor, que es como corre en el gate y en la maquina de cualquiera. Un
#    trinquete que solo baja cuando alguien exporta cuatro variables no es un trinquete.
#
#    Lo que hacia falta no era un motor: era un DOBLE que sirva las DOS rutas que el control usa
#    —el POST del receptor y `/v1/m/adoption/teams`— y que ademas presente un equipo AJENO con
#    filas. Ese equipo ajeno es la pieza que hace mortal al mutante: con el filtro puesto, el
#    control ve sus propias filas ir 0 -> 10 -> 10; sin el, lee la PRIMERA fila de la lista, que
#    son 99 sesiones que no son suyas, y muere en su propia guarda. La rama viva de abajo se queda
#    solo con lo que un doble no puede decidir: que el receptor REAL no duplique.
cat > "$TRABAJO/doble.py" <<'PY2'
import http.server, json, os, subprocess, sys, threading

AJENO, AJENO_N = "equipo-ajeno-de-otro-carril", 99
vistas = {}          # equipo -> set de session.id


def cosecha(sobre):
    for rm in sobre.get("resourceMetrics", []) or []:
        crudo = json.dumps(rm)
        equipos, sesiones = set(), set()
        def anda(n):
            if isinstance(n, dict):
                k, v = n.get("key"), n.get("value")
                if isinstance(v, dict) and isinstance(v.get("stringValue"), str):
                    if k == "team":
                        equipos.add(v["stringValue"])
                    elif k == "session.id":
                        sesiones.add(v["stringValue"])
                for x in n.values():
                    anda(x)
            elif isinstance(n, list):
                for x in n:
                    anda(x)
        anda(rm)
        del crudo
        for e in equipos or {""}:
            vistas.setdefault(e, set()).update(sesiones)


class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        try:
            cosecha(json.loads(self.rfile.read(n).decode() or "{}"))
        except Exception:
            pass
        self.send_response(200); self.end_headers(); self.wfile.write(b"{}")

    def do_GET(self):
        # ⛔ EL AJENO VA EL PRIMERO A PROPOSITO: sin el filtro por equipo, el control se queda con
        #    esta fila y su guarda tiene que dispararse.
        eq = [{"team": AJENO, "totals": {"sessions": AJENO_N}}]
        eq += [{"team": k, "totals": {"sessions": len(v)}} for k, v in sorted(vistas.items()) if k]
        cuerpo = json.dumps({"teams": eq}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(cuerpo)))
        self.end_headers(); self.wfile.write(cuerpo)

    def log_message(self, *a):
        pass


srv = http.server.HTTPServer(("127.0.0.1", 0), H)
threading.Thread(target=srv.serve_forever, daemon=True).start()
base = f"http://127.0.0.1:{srv.server_port}"
# El SLA lo decide CADA CASO por el entorno: sin el, el control ya no puede declarar
# idempotencia (esperar mas no demuestra ausencia), y hay un caso que lo comprueba.
_sla = os.environ.get("SLA", "")
_arg = ["--sla-persistencia", _sla] if _sla else []
r = subprocess.run([sys.executable, sys.argv[1], base, "tok", "ten",
                    "--otlp", base + "/v1/metrics", "--control-dedup"] + _arg,
                   capture_output=True, text=True)
srv.shutdown()
sys.stdout.write(r.stdout); sys.stderr.write(r.stderr)
sys.exit(r.returncode)
PY2

r="$(SLA=8 python3 "$TRABAJO/doble.py" "$GUION" >"$SALIDA" 2>&1; printf '%s' "$?")"
if [ "$r" = "0" ] && casa 'filas PROPIAS del equipo .*: 0 -> 10 .* -> 10'; then
	paso "el control de deduplicacion es HERMETICO: 0 -> 10 -> 10 en filas propias, sin motor"
elif [ "$r" = "0" ]; then
	malo "salio 0 sin la traza 0->10->10: el verde no dice que midio"
else
	malo "el control hermetico salio $r (mira $SALIDA): el doble no reproduce lo que el control usa"
fi

# ── 6-ter · MUTANTE: se pierde el filtro por equipo, SIN motor ─────────────────────────────────
# ⛔ PRIMERO SE COMPRUEBA QUE EL SUJETO AUN TIENE EL FILTRO, y esto es la cura de A-03 (the reviewer
#    sobre `89dc52767`). El lector demostro el agujero: si alguien RETIRA el filtro del guion real,
#    el constructor del mutante deja de casar, el banco muere en el `assert` del apply y sale un rc
#    2 generico — no un rojo que diga «se ha perdido el filtro por equipo». Una regresion tiene que
#    acusarse por su NOMBRE, no por el fallo colateral de la herramienta que la mide.
if command grep -qF 'if (t.get("team") or "") == marca:' "$GUION"; then
	paso "el sujeto conserva el filtro por equipo (precondicion del mutante, comprobada antes)"
else
	malo "el guion YA NO filtra por equipo: el control volveria a contar el agregado del tenant"
fi

# ⛔ CONSTRUIDO CON `muta_fichero`, QUE ES FAIL-CLOSED, Y NO CON UN `assert` CRUDO. El NO de
#    the reviewer lo midio: con un `assert` dentro del heredoc, si el ancla se mueve el fichero NO se
#    escribe, el sujeto real falla por libreria/fichero ausente, y el `if ! ...` cuenta eso como
#    VERDE. Un espaciado neutro en `redaccion.py` producia Traceback + AssertionError y el banco
#    seguia diciendo 29/0 rc 0. Es mi propia clase de esta tarde aplicada al fichero que faltaba.
mT="$TRABAJO/mT.py"
if ! muta_fichero "$GUION" "$mT" \
	'            if (t.get("team") or "") == marca:' \
	'            if True:  # MUTANTE: se pierde el filtro por equipo'; then
	malo "NO se pudo construir el mutante del filtro por equipo: su ancla no esta en el sujeto"
fi
r="$(SLA=8 python3 "$TRABAJO/doble.py" "$mT" >"$SALIDA" 2>&1; printf '%s' "$?")"
if [ "$r" != "0" ] && casa 'ya tenia 99 sesiones antes de empezar'; then
	paso "el mutante que quita el filtro por equipo MUERE sin motor, nombrando su guarda (rc $r)"
elif [ "$r" != "0" ]; then
	malo "el mutante murio con rc $r pero sin nombrar la guarda: no acredita el filtro"
else
	malo "el mutante que quita el filtro por equipo SOBREVIVIO sin motor: sigue sin trinquete"
fi

# ── RAMA VIVA · lo que solo un motor puede decidir ────────────────────────────────────────────
# ⛔ ESTA RAMA LA PROMETIA LA CABECERA Y NO EXISTIA. Escribi «lo que si exige motor va detras de una
#    variable y, cuando no corre, LO DICE» — y no habia ni un uso de `OLIVARES_VERIFY_ENGINE` en
#    todo el banco: el 12/0 era hermetico entero. Una cabecera que promete una mitad que no esta es
#    peor que no tenerla, porque quien lee el verde cree que cubre algo que nadie cubrio.
if [ -n "${OLIVARES_VERIFY_ENGINE:-}" ] && [ -n "${OLIVARES_VERIFY_TOKEN:-}" ] &&
	[ -n "${OLIVARES_VERIFY_TENANT:-}" ] && [ -n "${OLIVARES_VERIFY_OTLP:-}" ]; then
	# El control cuenta FILAS PROPIAS (su equipo lleva un nonce), asi que su veredicto es
	# atribuible aunque otro carril este sembrando el mismo tenant a la vez.
	r="$(rc_de "$GUION" "$OLIVARES_VERIFY_ENGINE" "$OLIVARES_VERIFY_TOKEN" "$OLIVARES_VERIFY_TENANT" \
		--otlp "$OLIVARES_VERIFY_OTLP" --control-dedup)"
	if [ "$r" = "0" ] && casa 'filas PROPIAS del equipo .*: 0 -> 10 .* -> 10'; then
		paso "contra motor vivo: 0 -> 10 -> 10 en filas PROPIAS (idempotente y atribuible)"
	elif [ "$r" = "0" ]; then
		malo "salio 0 pero sin la traza de filas propias 0->10->10: el verde no dice que midio"
	else
		malo "el control contra motor vivo salio $r"
	fi

	# ⛔ AQUI NO VA UN MUTANTE, Y LA RAZON ES LA QUE HACE HONESTA A ESTA RAMA. Puse uno —el reloj
	#    vivo— y muere ANTES de tocar el motor, en la asercion hermetica «dos construcciones del
	#    MISMO sobre salen distintas». Eso no es un fallo del caso: es que el defecto se caza mas
	#    temprano y mas barato, y forzarlo a morir aqui seria fabricar cobertura.
	#
	#    Y la propiedad que SOLO un motor decide —«el mismo sobre entregado dos veces no anade»— no
	#    la puedo mutar: vive en el receptor y en el store, no en este guion. Asi que el valor de
	#    esta rama es la MEDIDA, no un mutante: que mis filas propias vayan 0 -> 10 -> 10. Decirlo
	#    asi es lo unico que impide que alguien lea un 14/14 y crea que aqui hay algo que no hay.
	if casa 'la 2.a ejecucion'; then
		paso "la rama viva deja su traza de las dos ejecuciones en la salida"
	else
		malo "la rama viva no dejo traza de la 2.a ejecucion: el verde no dice que midio"
	fi

	# ⛔ EL MUTANTE DEL FILTRO YA NO VIVE AQUI, y esa mudanza es el arreglo. Estaba detras de
	#    esta puerta, asi que sin las cuatro variables el banco daba 15/15 con el filtro RETIRADO:
	#    un trinquete que solo baja cuando alguien exporta un motor no sujeta nada. Vive ahora en el
	#    caso 6-ter, hermetico, y muere alli por su guarda. Aqui se queda SOLO lo que un doble no
	#    puede decidir: que el receptor REAL no duplique.

else
	printf 'SALTADO  la RAMA VIVA no se ha corrido: exporta OLIVARES_VERIFY_ENGINE / _TOKEN /\n'
	printf '         _TENANT / _OTLP para ejercerla. Esto NO es un ok, y ahora dice EXACTAMENTE que\n'
	printf '         falta: la idempotencia contra un RECEPTOR REAL. El filtro por equipo ya NO\n'
	printf '         depende de esta rama — lo sujeta el caso 6-ter, hermetico, en todos los pases.\n'
fi

# ── 6-quater · UN RECEPTOR SANO PERO LENTO, Y OTRO QUE DUPLICA ────────────────────────────────
# ⛔ «AUN NO HA CAMBIADO» NO ES «YA NO CAMBIA». `estabilizar()` tomaba su primera lectura ANTES de
#    dormir y devolvia en cuanto dos coincidian; con la persistencia tardando mas de un segundo,
#    las dos valian lo de antes del POST y declaraba quietud SIN HABER VISTO MOVERSE NADA.
#
#    Y el sesgo no es simetrico, que es lo que hace daño: si el retraso afecta a la PRIMERA entrega
#    el control sale ROJO acusando a un motor sano —«el receptor no esta ingiriendo lo mio»—, y si
#    afecta a la SEGUNDA sale VERDE dando la idempotencia por buena mientras el store acaba con el
#    DOBLE de filas. Se inclina siempre hacia «idempotente», que es justo lo que este control
#    existe para no creerse. Nueve carriles y load1 de dos digitos son lo normal en esta caja.
#
#    Hacen falta DOS dobles, porque son dos direcciones y un solo fixture solo mata un mutante: uno
#    que DEDUPLICA con retraso (el caso sano) y otro que NO deduplica con retraso (el peligroso).
#    Se derivan del doble hermetico con python, no con `sed`: mi primera version los parcheaba por
#    regex y colo un `return` que cortaba la cosecha tras el primer `resourceMetric` — el fixture
#    entregaba UNA sesion de diez y el caso salia rojo culpando al sujeto. Un fixture mal construido
#    acusa al codigo que mide.
python3 - "$TRABAJO/doble.py" "$TRABAJO/doble-lento.py" "$TRABAJO/doble-dup.py" <<'PYD'
import ast, sys
src = open(sys.argv[1]).read()

VIEJO_PUB = '        for e in equipos or {""}:\n            vistas.setdefault(e, set()).update(sesiones)'
NUEVO_PUB = '        for e in equipos or {""}:\n            pendientes.append((time.monotonic() + RETRASO, e, set(sesiones)))'
VIEJO_EQ = '        eq = [{"team": AJENO, "totals": {"sessions": AJENO_N}}]'
NUEVO_EQ = ('        ahora = time.monotonic()\n'
            '        for reg in [x for x in pendientes if x[0] <= ahora]:\n'
            '            vistas.setdefault(reg[1], set()).update(reg[2]); pendientes.remove(reg)\n'
            + VIEJO_EQ)


def cambia(t, viejo, nuevo):
    # Fail-closed y RUIDOSO: sin esto el derivador moriria en silencio y el `[ ! -s ]` del
    # llamante diria «no se pudieron derivar» sin decir QUE ancla se movio.
    if t.count(viejo) != 1:
        sys.stderr.write("    ⛔ ancla %dx (esperaba 1): %r\n" % (t.count(viejo), viejo[:70]))
        sys.exit(1)
    return t.replace(viejo, nuevo, 1)


# ── el LENTO: acusa ya (el 200 lo manda do_POST) y PERSISTE despues. Deduplica, como el real.
lento = cambia(src, 'vistas = {}          # equipo -> set de session.id',
               'vistas = {}          # equipo -> set de session.id\n'
               'import os, time\n'
               'RETRASO = float(os.environ.get("RETRASO", "3"))   # segundos hasta PERSISTIR\n'
               'pendientes = []      # (visible_en, equipo, sesiones)')
lento = cambia(lento, VIEJO_PUB, NUEVO_PUB)
lento = cambia(lento, VIEJO_EQ, NUEVO_EQ)
ast.parse(lento)
open(sys.argv[2], "w").write(lento)

# ── el DUPLICADOR: mismo retraso, pero cuenta con MULTIPLICIDAD (un store sin clave unica)
dup = cambia(lento, '            pendientes.append((time.monotonic() + RETRASO, e, set(sesiones)))',
             '            pendientes.append((time.monotonic() + RETRASO, e, list(sesiones)))')
dup = cambia(dup, '            vistas.setdefault(reg[1], set()).update(reg[2]); pendientes.remove(reg)',
             '            vistas[reg[1]] = vistas.get(reg[1], 0) + len(reg[2]); pendientes.remove(reg)')
dup = cambia(dup, '        eq += [{"team": k, "totals": {"sessions": len(v)}} for k, v in sorted(vistas.items()) if k]',
             '        eq += [{"team": k, "totals": {"sessions": v}} for k, v in sorted(vistas.items()) if k]')
ast.parse(dup)
open(sys.argv[3], "w").write(dup)
PYD
if [ ! -s "$TRABAJO/doble-lento.py" ] || [ ! -s "$TRABAJO/doble-dup.py" ]; then
	malo "NO HE PODIDO MIRAR: no se pudieron derivar los dobles lento/duplicador del doble hermetico"
else
	r="$(RETRASO=3 SLA=8 python3 "$TRABAJO/doble-lento.py" "$GUION" >"$SALIDA" 2>&1; printf '%s' "$?")"
	if [ "$r" = "0" ] && casa 'filas PROPIAS del equipo .*: 0 -> 10 .* -> 10'; then
		paso "con la persistencia tardando 3s el control da 0 -> 10 -> 10 y rc 0: no acusa a un motor sano"
	else
		malo "el control acusa a un receptor sano que solo va lento (rc $r): el sesgo al falso rojo sigue vivo"
	fi

	r="$(RETRASO=3 SLA=8 python3 "$TRABAJO/doble-dup.py" "$GUION" >"$SALIDA" 2>&1; printf '%s' "$?")"
	if [ "$r" = "1" ] && casa 'movio mis filas de 10 a 20'; then
		paso "contra un receptor que NO deduplica y tarda 3s, el control lo CAZA: 10 -> 20 y rc 1"
	else
		malo "un receptor que duplica con retraso salio $r: el control da la idempotencia por buena"
	fi

	# ⛔ UN MUTANTE POR DIRECCION, cada uno contra SU doble. Con un solo fixture, el mutante del
	#    suelo «sobrevivia» —lo medi— porque un doble que deduplica no puede enseñar un duplicado.
	if ! muta_fichero "$GUION" "$TRABAJO/mMov.py" \
		'        t = 0
        for _ in range(presupuesto):' \
		'        t = 0
        return mias(), 0  # MUTANTE: no espera a VER movimiento
        for _ in range(presupuesto):'; then
		malo "NO se pudo construir el mutante de la espera de movimiento: su ancla no esta en el sujeto"
	elif [ -s "$TRABAJO/mMov.py" ] && ! cmp -s "$GUION" "$TRABAJO/mMov.py"; then
		r="$(RETRASO=3 SLA=8 python3 "$TRABAJO/doble-lento.py" "$TRABAJO/mMov.py" >"$SALIDA" 2>&1; printf '%s' "$?")"
		if [ "$r" = "1" ] && casa 'dejo 0 filas propias'; then
			paso "sin esperar a ver movimiento, el control ACUSA al receptor sano (rc 1, 0 filas): el falso rojo tiene mutante"
		else
			malo "el mutante que no espera movimiento SOBREVIVIO (rc $r): el caso del receptor lento no acredita nada"
		fi
	else
		malo "NO se pudo construir el mutante de la espera de movimiento: sin artefacto no hay juicio"
	fi

	if muta_fichero "$GUION" "$TRABAJO/mSuelo.py" \
		'            if v is not None and n == v and t >= minimo:' \
		'            if v is not None and n == v:  # MUTANTE: el suelo de espera ya no manda'; then
		r="$(RETRASO=3 SLA=8 python3 "$TRABAJO/doble-dup.py" "$TRABAJO/mSuelo.py" >"$SALIDA" 2>&1; printf '%s' "$?")"
		if [ "$r" = "0" ] && casa 'no mueve'; then
			paso "sin el suelo de espera, el control da por idempotente un store que DUPLICA: el falso verde tiene mutante"
		else
			malo "el mutante del suelo SOBREVIVIO (rc $r): el caso del duplicador no acredita la espera minima"
		fi
	else
		malo "NO se pudo construir el mutante del suelo de espera: sin artefacto no hay juicio"
	fi
fi

# ── 6-sexies · SIN SLA DECLARADO NO HAY VEREDICTO DE IDEMPOTENCIA ─────────────────────────────
# ⛔ ESPERAR MAS NO DEMUESTRA AUSENCIA, y subir el suelo no lo arregla. Mi cura anterior derivaba
#    la espera de lo que tardo la primera entrega (6 s) y el lector la rompio con un retraso de 7 s
#    SOLO en la segunda: el control decia «0 -> 10 -> 10, idempotente» y un segundo despues el
#    store llegaba a 20. Cualquier suelo que yo elija se rompe con un retraso un poco mayor.
#    La garantia no puede salir de mi paciencia: sale de un SLA que declara quien conoce el
#    receptor. Sin el, esto es rc 2 —«no puedo dar un veredicto»—, que es la regla 5 del canon.
r="$(RETRASO=3 python3 "$TRABAJO/doble-lento.py" "$GUION" >"$SALIDA" 2>&1; printf '%s' "$?")"
if [ "$r" = "2" ] && casa 'NO es prueba de idempotencia'; then
	paso "sin --sla-persistencia el control sale 2 y dice por que: no confunde «no se movio» con «no se movera»"
elif [ "$r" = "0" ]; then
	malo "sin SLA el control DECLARA idempotencia (rc 0): esperar mas se sigue leyendo como prueba"
else
	malo "sin SLA el control salio $r sin nombrar la razon: el rojo no dice que le falta"
fi

# Y con el SLA declarado, la MISMA observacion si es un veredicto.
r="$(RETRASO=3 SLA=8 python3 "$TRABAJO/doble-lento.py" "$GUION" >"$SALIDA" 2>&1; printf '%s' "$?")"
if [ "$r" = "0" ] && casa 'SLA de persistencia declarado'; then
	paso "con --sla-persistencia el control SI declara idempotencia, y dice contra que plazo"
else
	malo "con SLA declarado el control salio $r: el plazo no esta cambiando el veredicto"
fi

# ⛔ EL ESCENARIO DEL LECTOR, TAL CUAL: 7 s SOLO en la segunda entrega. Con un SLA de 10 el control
#    espera lo suficiente y lo CAZA; era el caso que mi suelo derivado de 6 s dejaba pasar.
r="$(RETRASO=7 SLA=10 python3 "$TRABAJO/doble-dup.py" "$GUION" >"$SALIDA" 2>&1; printf '%s' "$?")"
if [ "$r" = "1" ] && casa 'movio mis filas de 10 a 20'; then
	paso "un duplicado que tarda 7s lo caza el control cuando el SLA declarado lo cubre (rc 1)"
else
	malo "el duplicado de 7s salio $r: el escenario que rompio la version anterior sigue vivo"
fi

# Su mutante: si vuelve a declarar idempotencia sin SLA, el caso de arriba muere.
if muta_fichero "$GUION" "$TRABAJO/mSla.py" \
	'    if sla <= 0:' \
	'    if False:  # MUTANTE: vuelve a declarar idempotencia sin SLA declarado'; then
	r="$(RETRASO=3 python3 "$TRABAJO/doble-lento.py" "$TRABAJO/mSla.py" >"$SALIDA" 2>&1; printf '%s' "$?")"
	if [ "$r" = "0" ]; then
		paso "sin la guarda del SLA el control vuelve a decir 0: el caso 6-sexies acredita esa guarda"
	else
		malo "el mutante del SLA SOBREVIVIO (rc $r): el caso 6-sexies no acredita nada"
	fi
else
	malo "NO se pudo construir el mutante del SLA: sin artefacto no hay juicio"
fi

# ── 6-septies · UN DUPLICADO RECHAZADO NO ES UNA PRUEBA DE IDEMPOTENCIA ───────────────────────
# ⛔ LAS DOS ENTREGAS DEL CONTROL DESCARTABAN SU CODIGO HTTP, mientras `main` si lo miraba en el
#    mismo fichero. Y el caso peligroso es el SEGUNDO: un 400 en el duplicado da EXACTAMENTE el
#    mismo sintoma que la idempotencia —la cifra no se mueve— asi que, con un SLA declarado, un
#    rechazo se leia como veredicto bueno. Un rechazo no prueba nada.
python3 - "$TRABAJO/doble.py" "$TRABAJO/doble-rechaza2.py" <<'PYR'
import ast, sys
src = open(sys.argv[1]).read()
V = '''    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)'''
N = '''    _entregas = []

    def do_POST(self):
        # Acepta la PRIMERA y rechaza la SEGUNDA: el caso que se leia como idempotencia.
        H._entregas.append(1)
        if len(H._entregas) >= 2:
            self.send_response(400); self.end_headers(); self.wfile.write(b'{"error":"nope"}')
            return
        n = int(self.headers.get("Content-Length") or 0)'''
assert src.count(V) == 1, "ancla do_POST %dx" % src.count(V)
mut = src.replace(V, N, 1)
ast.parse(mut)
open(sys.argv[2], "w").write(mut)
PYR
if [ ! -s "$TRABAJO/doble-rechaza2.py" ]; then
	malo "NO se pudo derivar el doble que rechaza la 2.a entrega: sin artefacto no hay juicio"
else
	r="$(SLA=8 python3 "$TRABAJO/doble-rechaza2.py" "$GUION" >"$SALIDA" 2>&1; printf '%s' "$?")"
	if [ "$r" = "1" ] && casa 'rechazo la 2.a entrega'; then
		paso "un receptor que RECHAZA el duplicado sale rc 1 nombrandolo: no se certifica sobre lo que no entro"
	elif [ "$r" = "0" ]; then
		malo "un duplicado RECHAZADO se certifica como idempotente (rc 0): el rechazo se lee como prueba"
	else
		malo "el duplicado rechazado salio $r sin nombrar la causa: el rojo no dice que paso"
	fi

	# Mutante: si se deja de mirar el codigo de la 2.a, vuelve el falso verde.
	if muta_fichero "$GUION" "$TRABAJO/mCod.py" \
		'    if not (200 <= cod2 < 300):' \
		'    if False:  # MUTANTE: el codigo de la 2.a entrega deja de mirarse'; then
		r="$(SLA=8 python3 "$TRABAJO/doble-rechaza2.py" "$TRABAJO/mCod.py" >"$SALIDA" 2>&1; printf '%s' "$?")"
		if [ "$r" = "0" ]; then
			paso "sin mirar el codigo de la 2.a entrega, el rechazo vuelve a leerse como idempotencia: el caso lo caza"
		else
			malo "el mutante del codigo de la 2.a entrega SOBREVIVIO (rc $r): el caso no acredita esa guarda"
		fi
	else
		malo "NO se pudo construir el mutante del codigo de la 2.a entrega: sin artefacto no hay juicio"
	fi
fi

# ── 6-octies · EL TOKEN NO SALE, Y SE COMPRUEBA EJERCIENDO `main` COMO PROGRAMA ────────────────
# ⛔⛔ ESTE CASO ESTABA MAL Y LO DIJO UN LECTOR (44): recordaba el token A MANO
#     (`m._RED.recuerda(A.token)`) y afirmaba «se ejerce el MISMO camino que main». **Re-implementar
#     no es ejercitar**: acreditaba la libreria de redaccion, no que el guion la use. Y su mutante
#     comprobaba con `grep` que la linea hubiera desaparecido del fichero — PRESENCIA, no conducta:
#     un mutante que nadie ejecuta no muere de nada.
#
# ⛔⛔ Y AL EJERCERLO DE VERDAD SALIO ALGO PEOR, QUE ES EL MOTIVO DE ESCRIBIRLO ENTERO: el mutante
#     que retira `_RED.recuerda(a.token)` de `main` **NO FUGA**. Medido, ejecutando el guion contra
#     un destino muerto con un token con `\r`: sujeto y mutante dan los dos rc 2 y `<oculto>`.
#     Lo que tapa ese `ValueError: Invalid header value b'Bearer …'` es el patron `Bearer` de
#     `scripts/lib/redaccion.py`, NO el recuerdo del token. La justificacion que el commit original
#     escribio para esa llamada era falsa: describia un caso que otra cosa ya cubria.
#
#     La llamada SE QUEDA —es defensa en profundidad y cuesta una linea, para un token que asome
#     sin `Bearer` delante— pero se queda DICIENDO LO QUE ES. Un mutante que sobrevive no se
#     esconde: se explica. Lo que este caso acredita es lo que de verdad corta, y su mutante va
#     sobre la LIBRERIA, que es donde vive la cura.
TOKEN_CONTROL=$'tok-SECRETO-DE-BANCO-con\rcontrol'
MUERTO='http://127.0.0.1:1/'

corre_sujeto() { # $1 = raiz que contiene scripts/ ; imprime "<rc>|<fugas>|<lineas>"
	local salida rc
	salida="$(cd "$1" && timeout 60 python3 scripts/seed-adoption-otlp.py "$MUERTO" \
		"$TOKEN_CONTROL" t --otlp "${MUERTO}v1/metrics" --equipos 1 --por-equipo 1 --dias 1 2>&1)"
	rc=$?
	# ⛔ SE MIDE EN BYTES, NO EN LINEAS. `wc -l` cuenta SALTOS: la salida de este guion es UNA
	#    linea sin salto final, asi que daba 0 y mi propia guarda de «no llego a ejecutarse»
	#    disparaba sobre una corrida perfecta. El arnes hizo bien en negarse a contarlo como paso
	#    —esa mitad funciono— pero el numero que le di estaba mal.
	printf '%s|%s|%s' "$rc" "$(command grep -c 'SECRETO-DE-BANCO' <<<"$salida")" \
		"$(printf '%s' "$salida" | wc -c)"
}

# El sujeto, tal cual esta en el arbol.
IFS='|' read -r rc fugas bytes <<<"$(corre_sujeto "$RAIZ")"
# ⛔ rc 127 (o cualquier salida vacia) NO es un veredicto: es que el programa no llego a correr.
#    Un banco que acepte eso como «paso» acredita el vacio — la clase que un lector acaba de cazar
#    en otro arnes de esta misma casa, informando «SOBREVIVIO» sobre un rc 2.
if [ "$rc" = "127" ] || [ "$bytes" -lt 1 ]; then
	malo "NO HE PODIDO MIRAR: el guion no llego a ejecutarse (rc $rc, $bytes bytes): sin corrida no hay veredicto"
elif [ "$rc" != "2" ]; then
	malo "ejerciendo main con un token con caracter de control esperaba rc 2 y dio $rc"
elif [ "$fugas" != "0" ]; then
	malo "el token sale LITERAL al ejercer main ($fugas veces): la frontera no cubre el ValueError de cabecera"
else
	paso "ejerciendo main como programa con un token con \`\\r\`, el token no sale y el rc es 2"
fi

# ── 6-octies-bis · MUTANTE SOBRE LO QUE DE VERDAD CORTA, Y MUERE FUGANDO ───────────────────────
# El arbol de mentira lleva el guion Y la libreria, porque la cura vive en la segunda. Mutar el
# guion aqui no probaria nada: ya se midio que sobrevive.
ARBOL_MUT="$TRABAJO/arbol-bearer"
mkdir -p "$ARBOL_MUT/scripts/lib"
cp "$RAIZ/scripts/seed-adoption-otlp.py" "$ARBOL_MUT/scripts/" 2>/dev/null
cp "$RAIZ"/scripts/lib/*.py "$ARBOL_MUT/scripts/lib/" 2>/dev/null
if [ ! -s "$ARBOL_MUT/scripts/seed-adoption-otlp.py" ] || [ ! -s "$ARBOL_MUT/scripts/lib/redaccion.py" ]; then
	malo "NO HE PODIDO MIRAR: no se pudo copiar el arbol para el mutante de la libreria"
elif ! muta_fichero "$RAIZ/scripts/lib/redaccion.py" "$ARBOL_MUT/scripts/lib/redaccion.py" \
	'        fuera = _RX_BEARER.sub(lambda m: m.group(1) + " <oculto>", fuera)' \
	'        pass  # MUTANTE: el `Bearer` suelto deja de taparse'; then
	malo "NO HE PODIDO MIRAR: no se pudo construir el mutante del \`Bearer\` suelto"
else
	IFS='|' read -r rcm fugasm bytesm <<<"$(corre_sujeto "$ARBOL_MUT")"
	if [ "$rcm" = "127" ] || [ "$bytesm" -lt 1 ]; then
		malo "NO HE PODIDO MIRAR: el mutante no llego a ejecutarse (rc $rcm): eso NO es «sobrevivio»"
	elif [ "$fugasm" -lt 1 ]; then
		malo "el mutante que retira el tapado del \`Bearer\` NO fuga el token: entonces no es eso lo que corta, y este caso acredita otra cosa"
	else
		paso "el mutante que retira el tapado del \`Bearer\` MUERE fugando el token literal: es lo que corta"
	fi
fi

# ── 6-nonies · UNA FILA AJENA FLACA NO TUMBA EL VEREDICTO DEL SEMBRADO ────────────────────────
# ⛔ `flacos` salia de TODOS los equipos con nombre del tenant, y su rc 1 dice «el sembrado salio
#    corto»: una fila AJENA preexistente —de otro carril, de una demo anterior, de un cliente— con
#    pocas sesiones tumbaba el veredicto de un sembrado que habia ido BIEN. El universo de la
#    medida no era el universo de la afirmacion.
#
#    El doble sirve las tres rutas de `main` con TODOS los equipos propios completos y UNO ajeno
#    flaco. Antes de la cura eso salia rc 1 culpando al sembrado; ahora sale rc 0 y el ajeno se
#    NOMBRA, porque para una captura importa aunque no decida.
cat > "$TRABAJO/doble-ajeno.py" <<'PY9'
import http.server, json, os, subprocess, sys, threading

POR_EQUIPO = 8
MIOS = ["platform", "billing", "growth", "sre", "data", "mobile"]
AJENO = "equipo-de-otro-carril"


class H(http.server.BaseHTTPRequestHandler):
    def _j(self, o):
        c = json.dumps(o).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(c)))
        self.end_headers(); self.wfile.write(c)

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        self.rfile.read(n)
        self._j({})

    def do_GET(self):
        if "/adoption/summary" in self.path:
            self._j({"telemetry": {"totals": {"sessions": len(MIOS) * POR_EQUIPO}}})
        elif "/adoption/teams" in self.path:
            eq = [{"team": AJENO, "totals": {"sessions": 1}}]
            eq += [{"team": m, "totals": {"sessions": POR_EQUIPO}} for m in MIOS]
            self._j({"teams": eq})
        elif "/adoption/trend" in self.path:
            self._j({"days": [{"d": i} for i in range(30)]})
        else:
            self._j({})

    def log_message(self, *a):
        pass


srv = http.server.HTTPServer(("127.0.0.1", 0), H)
threading.Thread(target=srv.serve_forever, daemon=True).start()
base = "http://127.0.0.1:%d" % srv.server_port
r = subprocess.run([sys.executable, sys.argv[1], base, "tok", "ten",
                    "--otlp", base + "/v1/metrics", "--por-equipo", str(POR_EQUIPO)],
                   capture_output=True, text=True)
srv.shutdown()
sys.stdout.write(r.stdout); sys.stderr.write(r.stderr)
sys.exit(r.returncode)
PY9
if [ ! -s "$TRABAJO/doble-ajeno.py" ]; then
	malo "NO HE PODIDO MIRAR: no se pudo escribir el doble del equipo ajeno"
else
	r="$(python3 "$TRABAJO/doble-ajeno.py" "$GUION" >"$SALIDA" 2>&1; printf '%s' "$?")"
	if [ "$r" = "0" ] && casa 'AJENOS por debajo'; then
		paso "un equipo AJENO flaco no tumba el veredicto del sembrado, y aun asi se NOMBRA en el informe"
	elif [ "$r" = "1" ]; then
		malo "una fila ajena flaca sigue tumbando el sembrado (rc 1): el veredicto mide un universo que no es el suyo"
	else
		malo "el caso del equipo ajeno salio $r (mira $SALIDA): el doble no reproduce lo que main lee"
	fi

	# Su mutante: volviendo a medir sobre TODOS, la fila ajena tumba el rc.
	# ⛔ EL ANCLA SE MOVIO CON MI PROPIA CURA DE LOS `or 0`, que reescribio esta linea unas horas
	#    despues de escribir el mutante. El fail-closed lo dijo —«no se pudo construir»— en vez de
	#    contar un verde, que es exactamente para lo que esta. Se re-ancla a la linea de ahora.
	if muta_fichero "$GUION" "$TRABAJO/mAjeno.py" \
		'              if t.get("team") in mios and sesiones_de_fila(t) < a.por_equipo]' \
		'              if sesiones_de_fila(t) < a.por_equipo]  # MUTANTE: sobre TODO el tenant'; then
		r="$(python3 "$TRABAJO/doble-ajeno.py" "$TRABAJO/mAjeno.py" >"$SALIDA" 2>&1; printf '%s' "$?")"
		if [ "$r" = "1" ]; then
			paso "midiendo sobre todo el tenant, la fila ajena tumba el sembrado (rc 1): el caso lo acredita"
		else
			malo "el mutante que mide sobre todo el tenant SOBREVIVIO (rc $r): el caso no acredita el acotado"
		fi
	else
		malo "NO se pudo construir el mutante del universo: sin artefacto no hay juicio"
	fi
fi

# ── EL SEMBRADO CUBRE EL CONTRATO DEL MOTOR, Y LO COMPRUEBA CONTRA EL ──────────────────────────
# ⛔ La lista `METRICAS` tenia CINCO de las SIETE metricas del plano OTLP y NINGUNA llevaba
#    dimensiones. El dano no es «faltan dos numeros»: el agregador se desglosa por esas dims, asi
#    que la evidencia sembrada salia con `accepted`/`rejected`, el desglose por herramienta, el
#    tiempo activo y la mezcla de modelos VACIOS — y una pagina con cuatro paneles a cero se lee
#    como «el producto no lo trae». La lista se compara ahora con el CONTRATO, no con la memoria.
salida="$(python3 - "$GUION" "$RAIZ" <<'PY'
import importlib.util, sys
spec = importlib.util.spec_from_file_location("sd", sys.argv[1])
m = importlib.util.module_from_spec(spec); sys.modules["sd"] = m
try:
    spec.loader.exec_module(m)
except SystemExit:
    pass
motor, razon = m.contrato_del_motor(sys.argv[2])
if razon:
    print("NOPUDE", razon); raise SystemExit(0)
mias = {n for n, _ in m.METRICAS}
print("FALTAN", " ".join(sorted(motor - mias)) or "-")
print("SOBRAN", " ".join(sorted(mias - motor)) or "-")
PY
)"
if command grep -q '^NOPUDE' <<<"$salida"; then
	malo "no he podido leer el contrato del motor: $salida"
elif ! command grep -q '^FALTAN -$' <<<"$salida"; then
	malo "el sembrado NO cubre el contrato OTLP del motor: $(command grep '^FALTAN' <<<"$salida")"
elif ! command grep -q '^SOBRAN -$' <<<"$salida"; then
	malo "el sembrado emite metricas que el motor no reconoce: $(command grep '^SOBRAN' <<<"$salida")"
else
	paso "las metricas sembradas son EXACTAMENTE las del contrato OTLP del motor (leido, no recordado)"
fi

# Y las dimensiones, que son la mitad que de verdad llena la pagina.
salida="$(python3 - "$GUION" <<'PY'
import importlib.util, sys
spec = importlib.util.spec_from_file_location("sd", sys.argv[1])
m = importlib.util.module_from_spec(spec); sys.modules["sd"] = m
try:
    spec.loader.exec_module(m)
except SystemExit:
    pass
env = m.sobre_otlp(["platform"], 1, 3, "p", 1_700_000_000_000_000_000)
dims = {}
for x in env["resourceMetrics"][0]["scopeMetrics"][0]["metrics"]:
    for dp in x["sum"]["dataPoints"]:
        for a in dp.get("attributes", []):
            dims.setdefault(x["name"], set()).add(a["key"])
esperado = {
    "claude_code.lines_of_code.count": {"type"},
    "claude_code.token.usage": {"type", "model"},
    "claude_code.code_edit_tool.decision": {"tool_name", "decision"},
    "claude_code.active_time.total": {"type"},
}
for n, e in esperado.items():
    if dims.get(n) != e:
        print("MAL", n, sorted(dims.get(n, [])), "esperado", sorted(e)); raise SystemExit(0)
# Y las dos caras de cada desglose, o el panel sale a medias.
caras = set()
for x in env["resourceMetrics"][0]["scopeMetrics"][0]["metrics"]:
    if x["name"] == "claude_code.lines_of_code.count":
        caras = {a["value"]["stringValue"] for dp in x["sum"]["dataPoints"]
                 for a in dp.get("attributes", []) if a["key"] == "type"}
print("OK" if caras == {"added", "removed"} else f"MAL lineas {sorted(caras)}")
PY
)"
if [ "$salida" = "OK" ]; then
	paso "cada metrica con desglose viaja con sus dimensiones, y las lineas traen anadidas Y borradas"
else
	malo "las dimensiones del sobre no son las que el receptor lee: $salida"
fi

# ── LA UNIDAD DEL CABLE SON SEGUNDOS, Y EL RECEPTOR MULTIPLICA POR MIL ────────────────────────
# ⛔ Lo destapo una MEDIDA contra el motor, no una lectura: sembrando milisegundos, el agregado
#    salia `active_time_ms = 62.481.616.000` = **723 dias de actividad para 12 sesiones**. Y esa es
#    la clase peor de dato malo en una captura que se publica como prueba: **una cifra a cero se
#    lee como «falta dato»; una cifra absurda se lee como DATO**.
#
#    La trampa esta en que `adoptionMetricUnit` declara «ms» — pero esa es la unidad ALMACENADA.
#    El receptor CONVIERTE: `connectors/claude/metrics.go:151`, `dp.GetAsInt() * 1000`. Leer la
#    unidad del consumidor y sembrar en ella es exactamente el error que se comete.
salida="$(python3 - "$GUION" <<'PY'
import importlib.util, sys
spec = importlib.util.spec_from_file_location("sd", sys.argv[1])
m = importlib.util.module_from_spec(spec); sys.modules["sd"] = m
try:
    spec.loader.exec_module(m)
except SystemExit:
    pass
env = m.sobre_otlp(["platform"], 4, 7, "p", 1_700_000_000_000_000_000)
peor = 0
for rm in env["resourceMetrics"]:
    for x in rm["scopeMetrics"][0]["metrics"]:
        if x["name"] == "claude_code.active_time.total":
            for dp in x["sum"]["dataPoints"]:
                peor = max(peor, int(dp["asInt"]))
# 86.400 s es UN dia, y ese es el techo defendible para lo que un datapoint puede declarar de una
# sesion. Convertido por el receptor (x1000) son 86.400.000 ms, que siguen siendo UN dia: la
# conversion no cambia la duracion, cambia la unidad en que se guarda.
# ⛔ AQUI PONIA «mas de mil dias», y estaba MAL POR MIL — en el mismisimo commit que curaba una
#    unidad equivocada. Lo caza el mismo defecto que el commit describe: una cifra escrita en prosa
#    al lado de un predicado correcto, que nadie recalcula y que ensena la unidad al reves al
#    siguiente lector. El predicado no cambia; el que estaba mal era yo explicandolo.
print("MAL" if peor > 86_400 else "OK", peor)
PY
)"
if [ "${salida%% *}" = "OK" ]; then
	paso "el tiempo activo se siembra en SEGUNDOS (peor datapoint ${salida##* }s < 1 dia): el x1000 del receptor no lo dispara"
else
	malo "el tiempo activo se siembra fuera de escala (${salida##* }): el receptor multiplica por 1000 y la captura mostraria anos de actividad"
fi

# ── UNA FILA SIN `sessions` ES «NO PUDE MIRAR», Y AHORA HAY QUIEN LO ACREDITE ─────────────────
# ⛔ `sesiones_de_fila` distingue AUSENTE de CERO desde hace dos claims, y esta bateria NO LO
#    PROBABA: un lector restauro el `or 0` y el mutante SOBREVIVIO 43/0. Es la familia de siempre
#    —una cura sin testigo es una costumbre— y aqui muerde donde mas duele: con `or 0`, «el motor
#    dejo de devolver el campo» se convierte en «ese equipo tiene 0 sesiones», y `flacos` lo
#    convierte a su vez en un rc 1 que ACUSA AL SEMBRADO de algo que nadie ha medido. Un cambio de
#    contrato del motor sale como un fallo mio.
cat > "$TRABAJO/doble-sin-sessions.py" <<'PYS'
import http.server, json, subprocess, sys, threading

POR_EQUIPO = 8
MIOS = ["platform", "billing", "growth", "sre", "data", "mobile"]


class H(http.server.BaseHTTPRequestHandler):
    def _j(self, o):
        c = json.dumps(o).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(c)))
        self.end_headers(); self.wfile.write(c)

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        self.rfile.read(n)
        self._j({})

    def do_GET(self):
        if "/adoption/summary" in self.path:
            self._j({"telemetry": {"totals": {"sessions": len(MIOS) * POR_EQUIPO}}})
        elif "/adoption/teams" in self.path:
            # Todas completas MENOS una: `totals` existe y `sessions` NO. Eso no es cero: es que
            # la respuesta cambio de forma.
            eq = [{"team": m, "totals": {"sessions": POR_EQUIPO}} for m in MIOS[1:]]
            eq.insert(0, {"team": MIOS[0], "totals": {"lines_added": 10}})
            self._j({"teams": eq})
        elif "/adoption/trend" in self.path:
            self._j({"days": [{"d": i} for i in range(30)]})
        else:
            self._j({})

    def log_message(self, *a):
        pass


srv = http.server.HTTPServer(("127.0.0.1", 0), H)
threading.Thread(target=srv.serve_forever, daemon=True).start()
base = "http://127.0.0.1:%d" % srv.server_port
r = subprocess.run([sys.executable, sys.argv[1], base, "tok", "ten",
                    "--otlp", base + "/v1/metrics", "--por-equipo", str(POR_EQUIPO)],
                   capture_output=True, text=True)
srv.shutdown()
sys.stdout.write(r.stdout); sys.stderr.write(r.stderr)
sys.exit(r.returncode)
PYS
if [ ! -s "$TRABAJO/doble-sin-sessions.py" ]; then
	malo "NO HE PODIDO MIRAR: no se pudo escribir el doble de la fila sin sessions"
else
	r="$(python3 "$TRABAJO/doble-sin-sessions.py" "$GUION" >"$SALIDA" 2>&1; printf '%s' "$?")"
	if [ "$r" = "2" ] && casa 'ausente no es cero'; then
		paso "una fila con \`totals\` y SIN \`sessions\` sale rc 2 diciendo «ausente no es cero», no rc 1 culpando al sembrado"
	elif [ "$r" = "1" ]; then
		malo "la fila sin \`sessions\` sale rc 1: un cambio de contrato del motor se esta leyendo como sembrado corto"
	else
		malo "la fila sin \`sessions\` da rc $r sin nombrar la causa: $(head -2 "$SALIDA" | tr '\n' ' ')"
	fi

	# ── Y SU MUTANTE: el `or 0` que el lector restauro ────────────────────────────────────
	# ⛔ EL ANCLA NO PUEDE SER `if "sessions" not in tot:`: esa linea sale DOS VECES en el guion
	#    —`sesiones_de` y `sesiones_de_fila` la comparten— y un mutante que toque las dos muere por
	#    la funcion equivocada. Se ancla al bloque ENTERO, que incluye el mensaje de la fila y por
	#    tanto es unico. Y el mutante es el `or 0` REAL, no un `if False`: con `if False` la funcion
	#    caeria en un KeyError, y un mutante que revienta no acredita nada.
	python3 - "$GUION" "$TRABAJO/mFila.py" <<'PYM'
import sys
src = open(sys.argv[1]).read()
viejo = (
    '    if "sessions" not in tot:\n'
    '        raise SystemExit(salir(RC_NO_PUDE_MIRAR,\n'
    '                               f"{ruta}: la fila del equipo {t.get(\'team\')!r} trae `totals` SIN "\n'
    '                               "`sessions`: ausente no es cero"))\n'
    '    return tot["sessions"]\n'
)
assert src.count(viejo) == 1, f"ancla de la fila: {src.count(viejo)} coincidencias"
nuevo = '    return tot.get("sessions") or 0  # MUTANTE: ausente vuelve a caer a cero\n'
open(sys.argv[2], "w").write(src.replace(viejo, nuevo, 1))
PYM
	if [ ! -s "$TRABAJO/mFila.py" ]; then
		malo "NO HE PODIDO MIRAR: no se pudo construir el mutante del \`or 0\` de la fila"
	else
		rm2="$(python3 "$TRABAJO/doble-sin-sessions.py" "$TRABAJO/mFila.py" >"$SALIDA" 2>&1; printf '%s' "$?")"
		if [ "$rm2" = "127" ]; then
			malo "NO HE PODIDO MIRAR: el mutante de la fila no llego a ejecutarse (rc 127): eso NO es «sobrevivio»"
		elif ! muerte_valida "$SALIDA"; then
			malo "NO HE PODIDO MIRAR: el mutante de la fila REVENTO (Traceback) en vez de morir: un mutante que revienta no acredita nada"
		elif casa 'ausente no es cero'; then
			malo "el mutante que devuelve el \`or 0\` SIGUE diciendo «ausente no es cero» (rc $rm2): no acredita nada"
		else
			paso "el mutante que devuelve el \`or 0\` MUERE: deja de distinguir ausente de cero y este caso lo caza"
		fi
	fi
fi

# ── UN MUTANTE QUE REVIENTA NO ES UN MUTANTE MUERTO ───────────────────────────────────────────
# ⛔ SENUELO, y existe porque `muerte_valida` sin un caso que la vea cortar seria otra costumbre.
#    El senuelo desactiva la guarda de `sesiones_de_fila` con un `if False:` —que es justo la
#    variante que descarte al escribir el mutante bueno— y con eso la funcion cae en un `KeyError`.
#    No imprime el mensaje que el caso busca, asi que ANTES de esta cura el arnes lo contaba como
#    MUERTO y la bateria seguia verde. Ahora tiene que decir NO HE PODIDO MIRAR.
python3 - "$GUION" "$TRABAJO/mRevienta.py" <<'PYR'
import sys
src = open(sys.argv[1]).read()
viejo = (
    '    if "sessions" not in tot:\n'
    '        raise SystemExit(salir(RC_NO_PUDE_MIRAR,\n'
    '                               f"{ruta}: la fila del equipo {t.get(\'team\')!r} trae `totals` SIN "\n'
)
assert src.count(viejo) == 1, f"ancla del senuelo: {src.count(viejo)} coincidencias"
nuevo = (
    '    if False:  # SENUELO: la guarda se va y la funcion cae en KeyError\n'
    '        raise SystemExit(salir(RC_NO_PUDE_MIRAR,\n'
    '                               f"{ruta}: la fila del equipo {t.get(\'team\')!r} trae `totals` SIN "\n'
)
open(sys.argv[2], "w").write(src.replace(viejo, nuevo, 1))
PYR
if [ ! -s "$TRABAJO/mRevienta.py" ]; then
	malo "NO HE PODIDO MIRAR: no se pudo construir el senuelo que revienta"
else
	rs="$(python3 "$TRABAJO/doble-sin-sessions.py" "$TRABAJO/mRevienta.py" >"$SALIDA" 2>&1; printf '%s' "$?")"
	if ! command grep -q 'KeyError' "$SALIDA"; then
		malo "el senuelo no llego a reventar (rc $rs): sin KeyError no prueba nada; revisa el senuelo"
	elif muerte_valida "$SALIDA"; then
		malo "muerte_valida DA POR BUENA una salida con Traceback: un reventon se contaria como muerte"
	else
		paso "un mutante que REVIENTA se rechaza como muerte valida: el senuelo del KeyError lo demuestra"
	fi
fi

printf '\ntest-seed-adoption-otlp: %d pasan, %d fallan\n' "$ok" "$fail"
[ "$fail" -eq 0 ] || exit 1
exit 0
