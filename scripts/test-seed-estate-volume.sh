#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Bateria de `scripts/seed-estate-volume.py`.
#
# ⛔ ESTE BANCO ES HERMETICO ENTERO, Y ESO ES UNA DECISION, NO UNA COMODIDAD. La leccion que este
#    carril ha cobrado SEIS veces hoy es que un trinquete detras de una variable de entorno no
#    sujeta nada: en el gate, y en la maquina de quien lea el verde, esa mitad no corre. Asi que el
#    motor se sustituye por un DOBLE que reproduce las tres conductas de las que depende el guion:
#      · listar devuelve `items`,
#      · un POST crea una fila,
#      · y tres superficies dan 409 sobre un SUJETO ya usado (medido contra motor real: `health`,
#        `killswitch` y `redteam` admiten una sola fila por agente).
#    Lo que un doble NO puede decidir —que el motor real acepte estos payloads— ya esta acreditado
#    aparte: `verify-seed-payloads.py` los ejercio uno a uno (18 declarados / 18 ejercidos).
set -u -o pipefail

if ! command -v python3 >/dev/null 2>&1; then
	printf 'test-seed-estate-volume: NO HE PODIDO MIRAR: no hay python3\n' >&2
	exit 2
fi

RAIZ="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
GUION="$RAIZ/scripts/seed-estate-volume.py"
# export-closure: absent-by-design docs/launch/objetivos-sembrado.json — es el catalogo de
# objetivos de sembrado del LANZAMIENTO, y esta curacion retira `docs/launch` ENTERO
# (scripts/export-public.sh). Aqui es DATO, no una llamada: en el arbol publicado la ruta
# simplemente nunca casa, nada la ejecuta y por tanto no hay llamada que guardar. Declararlo
# hub-only seria la clase equivocada, y retirar el guion COLGO a sus dos llamadores y esos a
# TRES mas — la cascada probo que lo ausente es el fichero de datos, no el guion.
OBJETIVOS="$RAIZ/docs/launch/objetivos-sembrado.json"
LIBDIR="$RAIZ/scripts/lib"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT

ok=0
fail=0
paso() { printf 'ok   %s\n' "$1"; ok=$((ok + 1)); }
malo() { printf 'FAIL %s\n' "$1"; fail=$((fail + 1)); }
SALIDA="$T/salida.txt"
casa() { command grep -qE "$1" "$SALIDA"; }

# ── El doble ──────────────────────────────────────────────────────────────────────────────────
cat > "$T/doble.py" <<'PY'
import http.server, json, subprocess, sys, threading, uuid

# `ruta -> campo de sujeto unico`, tal y como el motor real se comporta (409 sobre un sujeto
# repetido). Sin esto el doble seria mas permisivo que el motor y el banco mediria una fantasia.
UNICO = {"/v1/m/health/checks": "subject_ref",
         "/v1/m/governance/killswitch": "scope_ref",
         "/v1/m/redteam/targets": "agent_ref"}
almacen = {}
# argv[3]: lista "CODIGO:/ruta" separada por comas, para forzar conductas del servidor que el
# guion tiene que saber distinguir: un 500 al listar (ceguera, nunca «cero filas») y un 409
# permanente (el servidor rechaza duplicados y el guion no puede quedarse callado).
forzado = {}
for par in (sys.argv[3].split(",") if len(sys.argv) > 3 and sys.argv[3] else []):
    if ":" in par:
        cod, ruta = par.split(":", 1)
        forzado[ruta] = int(cod)


class H(http.server.BaseHTTPRequestHandler):
    def _ruta(self):
        return self.path.split("?")[0]

    def do_GET(self):
        r = self._ruta()
        if forzado.get(r) == 500:
            self.send_response(500); self.end_headers(); self.wfile.write(b"boom"); return
        cuerpo = json.dumps({"items": almacen.get(r, [])}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(cuerpo)))
        self.end_headers(); self.wfile.write(cuerpo)

    def do_POST(self):
        r = self._ruta()
        n = int(self.headers.get("Content-Length") or 0)
        try:
            fila = json.loads(self.rfile.read(n).decode() or "{}")
        except Exception:
            self.send_response(400); self.end_headers(); self.wfile.write(b'{"error":"bad json"}'); return
        if forzado.get(r) == 409:
            self.send_response(409); self.end_headers()
            self.wfile.write(b'{"error":{"message":"forced duplicate"}}'); return
        campo = UNICO.get(r)
        if campo and any(f.get(campo) == fila.get(campo) for f in almacen.get(r, [])):
            self.send_response(409); self.end_headers()
            self.wfile.write(b'{"error":{"message":"a row for this subject already exists"}}'); return
        fila["id"] = str(uuid.uuid4())
        almacen.setdefault(r, []).append(fila)
        self.send_response(201); self.end_headers(); self.wfile.write(json.dumps(fila).encode())

    def log_message(self, *a):
        pass


srv = http.server.HTTPServer(("127.0.0.1", 0), H)
threading.Thread(target=srv.serve_forever, daemon=True).start()
base = f"http://127.0.0.1:{srv.server_port}"
# Cuantas veces se corre el guion contra el MISMO doble. Dos corridas es como se mide la
# idempotencia: el almacen persiste entre ellas, igual que un motor.
veces = int(sys.argv[4]) if len(sys.argv) > 4 and sys.argv[4] else 1
rc = 0
for v in range(veces):
    print(f"===== corrida {v + 1} de {veces} =====")
    p = subprocess.run([sys.executable, sys.argv[1], base, "tok", "ten", "--objetivos", sys.argv[2]],
                       capture_output=True, text=True)
    rc = p.returncode
    sys.stdout.write(p.stdout); sys.stderr.write(p.stderr)
srv.shutdown()
sys.exit(rc)
PY
corre() { python3 "$T/doble.py" "$1" "$2" "${3:-}" "${4:-}" >"$SALIDA" 2>&1; printf '%s' "$?"; }

# ── 1 · CAMINO LIMPIO contra el doble: todas llegan a su objetivo ─────────────────────────────
r="$(corre "$GUION" "$OBJETIVOS")"
if [ "$r" = "0" ] && casa 'todas las superficies declaradas llegan a su objetivo'; then
	paso "contra el doble, las 13 superficies declaradas llegan a su objetivo (rc 0)"
elif [ "$r" = "0" ]; then
	malo "salio 0 sin la linea de veredicto: un cero mudo no dice que midio"
else
	malo "el camino limpio salio $r (mira $SALIDA)"
fi

# ── 2 · IDEMPOTENCIA: la segunda corrida crea CERO ────────────────────────────────────────────
# ⛔ No se comprueba «que no falle»: se comprueba EL NUMERO. Un sembrador que vuelva a crear seria
#    verde en rc y estaria acumulando una fila por captura, que es el defecto A-01 que este carril
#    ya midio en `seed-demo-work.py` (+51 decisiones y +5 work items POR CORRIDA).
r="$(corre "$GUION" "$OBJETIVOS" "" 2)"
if [ "$r" = "0" ] && [ "$(command grep -c '^  0 filas creadas en esta corrida' "$SALIDA")" = "1" ]; then
	paso "la 2.a corrida contra el mismo almacen crea CERO filas (idempotente, medido)"
else
	malo "la 2.a corrida no creo cero: $(command grep -c 'filas creadas' "$SALIDA") lineas de creacion (rc $r)"
fi

# ── 3 · UNA SUPERFICIE POR DEBAJO => rc 1 nombrandola A ELLA y a su objetivo ───────────────────
# El objetivo se sube a un numero que el doble no puede alcanzar (mas agentes de los que hay
# libres para un sujeto unico), que es exactamente como se queda corta una superficie de verdad.
python3 - "$OBJETIVOS" "$T/imposible.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
for s in d["superficies"]:
    if s["id"] == "killswitch":
        s["objetivo"] = 99
json.dump(d, open(sys.argv[2], "w"))
PY
r="$(corre "$GUION" "$T/imposible.json")"
if [ "$r" = "1" ] && casa 'killswitch. se queda en [0-9]+ de 99' && casa 'sujeto UNICO'; then
	paso "una superficie por debajo sale rc 1 nombrandola, su objetivo y por que no pudo subir"
elif [ "$r" = "1" ]; then
	malo "salio 1 sin nombrar la superficie y su objetivo: el rojo no dice donde mirar"
else
	malo "una superficie por debajo deberia salir 1 y salio $r"
fi

# ── 4 · DECLARADA SIN GENERADOR => rc 2 (fail-closed, direccion 1) ─────────────────────────────
python3 - "$OBJETIVOS" "$T/sobra.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
d["superficies"].append({"id": "superficie-inventada", "objetivo": 3,
                         "listar": "/v1/m/nada", "bajo": "items"})
json.dump(d, open(sys.argv[2], "w"))
PY
r="$(corre "$GUION" "$T/sobra.json")"
if [ "$r" = "2" ] && casa 'SIN generador' && casa 'superficie-inventada'; then
	paso "una superficie declarada sin generador sale 2 y la NOMBRA (no la siembra en silencio)"
else
	malo "declarada-sin-generador deberia salir 2 nombrandola y salio $r"
fi

# ── 5 · GENERADOR SIN DECLARAR => rc 2 (fail-closed, direccion 2) ──────────────────────────────
# ⛔ ES LA DIRECCION QUE UN MUTANTE ROMPE SIN QUE SE NOTE. Un generador sin fila en el JSON
#    sembraria filas SIN objetivo y SIN aparecer en el reparto: trabajo invisible que nadie puede
#    auditar, y el guion saldria en verde.
python3 - "$OBJETIVOS" "$T/falta.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
d["superficies"] = [s for s in d["superficies"] if s["id"] != "knowledge"]
json.dump(d, open(sys.argv[2], "w"))
PY
r="$(corre "$GUION" "$T/falta.json")"
if [ "$r" = "2" ] && casa 'SIN declarar' && casa 'knowledge'; then
	paso "un generador sin declarar sale 2 y lo NOMBRA (no siembra fuera del reparto)"
else
	malo "generador-sin-declarar deberia salir 2 nombrandolo y salio $r"
fi

# ── 6 · SIN FICHERO DE OBJETIVOS => 2, y JSON roto => 2 ────────────────────────────────────────
python3 "$GUION" http://127.0.0.1:1 t t --objetivos "$T/no-existe.json" >"$SALIDA" 2>&1
r=$?
if [ "$r" = "2" ] && casa 'NO HE PODIDO MIRAR' && casa 'no encuentro el fichero de objetivos'; then
	paso "sin fichero de objetivos => rc 2 diciendo cual falta (no un 0 con cero superficies)"
else
	malo "sin fichero de objetivos deberia salir 2 y salio $r"
fi
printf '{ esto no es json\n' >"$T/roto.json"
python3 "$GUION" http://127.0.0.1:1 t t --objetivos "$T/roto.json" >"$SALIDA" 2>&1
r=$?
if [ "$r" = "2" ] && casa 'no es JSON valido'; then
	paso "un fichero de objetivos ilegible => rc 2, no un verde sin superficies"
else
	malo "objetivos ilegibles deberia salir 2 y salio $r"
fi

# ── 7 · UNA LISTA QUE NO SE PUEDE LEER NO ES UNA LISTA VACIA ───────────────────────────────────
# ⛔ ES LA REGLA 5, Y ES EL DEFECTO MAS CARO DE ESTA CASA. Si el GET de una superficie da 500 y el
#    guion lo contara como «0 filas», sembraria el objetivo entero encima de lo que ya hubiera.
r="$(corre "$GUION" "$OBJETIVOS" "500:/v1/m/knowledge/kbs")"
if [ "$r" = "2" ] && casa 'NO HE PODIDO MIRAR' && casa 'knowledge'; then
	paso "una lista que devuelve 500 => rc 2 nombrando la superficie, no cero filas"
else
	malo "un 500 al listar deberia salir 2 nombrando la superficie y salio $r"
fi

# ── 8 · UN 409 PERMANENTE NO PUEDE QUEDARSE CALLADO ────────────────────────────────────────────
# ⛔ ESTE CASO SALE DE UNA CORRIDA REAL, no de la imaginacion: `redteam` se quedo en 1 de 6
#    creando CERO filas y sin UNA SOLA LINEA que dijera por que, porque el bucle trataba el 409
#    como «ya estaba» y seguia girando. Un 409 repetido no es exito: es un sujeto agotado.
r="$(corre "$GUION" "$OBJETIVOS" "409:/v1/m/notify/routes")"
if [ "$r" = "1" ] && casa 'conflictos 409' && casa 'alerting'; then
	paso "un 409 permanente sale rc 1 CONTANDO los conflictos y nombrando la superficie"
elif [ "$r" = "1" ]; then
	malo "salio 1 pero sin decir que fueron conflictos 409: se queda corto en silencio"
else
	malo "un 409 permanente deberia salir 1 y salio $r"
fi

# ── 9 · EL GUARDIAN DE SECRETOS ABORTA ANTES DE ENVIAR ─────────────────────────────────────────
# Un generador que emita un VALOR en vez de un localizador tiene que morir con 2 y NOMBRAR la
# forma que le corto. Se muta el generador, que es donde un descuido real entraria.
mS="$T/mS.py"
python3 - "$GUION" "$mS" <<'PY'
import sys
src = open(sys.argv[1]).read()
viejo = '''            "secret_refs": [{"name": f"MCP_TOKEN_{i}", "ref_kind": "env",
                             "ref": f"MCP_TOKEN_{i}", "hint": "provisioned by the platform"}],'''
nuevo = '''            "secret_refs": [{"name": f"MCP_TOKEN_{i}", "ref_kind": "env",
                             "ref": f"MCP_TOKEN_{i}", "hint": "provisioned by the platform"}],
            "token": "ghp_0123456789abcdefghijklmnopqrstuvwxyz",  # MUTANTE: un VALOR, no un localizador'''
mut = src.replace(viejo, nuevo, 1)
assert mut != src, "el mutante del secreto NO se aplico"
open(sys.argv[2], "w").write(mut)
PY
r="$(corre "$mS" "$OBJETIVOS")"
if [ "$r" = "2" ] && casa 'guardian de secretos' && casa 'github-token|lleva un VALOR'; then
	paso "un generador que emite un VALOR secreto aborta con 2 nombrando la forma que lo corto"
else
	malo "el guardian de secretos no corto el mutante (rc $r): un valor saldria por el cable"
fi

# ── 10-14 · LOS MUTANTES, uno por dimension, cada uno con su apply-assert ──────────────────────
# ⛔ CADA MUTANTE ACREDITA LA PATA QUE NOMBRA, y por eso se le exige morir POR EL MENSAJE de su
#    caso y no solo por el rc. Un mutante que muere en la pata anterior deja la suya sin cubrir y
#    la bateria sale verde: esa leccion la ha cobrado este carril seis veces hoy.
# ⛔ UN JUICIO DE MUTANTE NO SE EMITE SI EL MUTANTE NO SE CONSTRUYO (the reviewer, A-04). El banco
#    imprimia «FAIL m1 no se aplico» y a continuacion contaba «ok el mutante MUERE» sobre un
#    fichero inexistente: dos lineas contradictorias y una de ellas verde. `exige_mutante` corta
#    esa secuencia — si no hay artefacto, el caso es ROJO y no hay veredicto que leer.
muta() { # $1 = fichero destino, $2 = viejo, $3 = nuevo
	muta_de "$GUION" "$@"
}
exige_mutante() { # $1 = fichero del mutante, $2 = nombre para el mensaje; rc 0 si esta construido
	if [ -s "$1" ] && ! cmp -s "$GUION" "$1"; then
		return 0
	fi
	malo "NO se pudo construir el mutante $2: su ancla no esta en el sujeto — sin artefacto no hay juicio"
	return 1
}
muta_de() { # $1 = fuente, $2 = destino, $3 = viejo, $4 = nuevo
	python3 - "$1" "$2" "$3" "$4" <<'PY'
import sys
src = open(sys.argv[1]).read()
mut = src.replace(sys.argv[3], sys.argv[4], 1)
assert mut != src, f"el mutante NO se aplico: no encuentro {sys.argv[3][:60]!r}"
open(sys.argv[2], "w").write(mut)
PY
}

muta "$T/m1.py" '    faltan = max(0, objetivo - antes)' '    faltan = objetivo  # MUTANTE: siembra el objetivo entero cada vez'
if exige_mutante "$T/m1.py" m1; then
r="$(corre "$T/m1.py" "$OBJETIVOS" "" 2)"
if [ "$(command grep -c '^  0 filas creadas en esta corrida' "$SALIDA")" = "0" ]; then
	paso "el mutante que siembra el objetivo entero cada vez MUERE en el caso 2 (idempotencia)"
else
	malo "el mutante de la idempotencia SOBREVIVIO: el caso 2 no cubre nada"
fi
fi

muta "$T/m2.py" '    faltan_decl = sorted(set(GENERADORES) - set(decl))' '    faltan_decl = []  # MUTANTE: no se mira la segunda direccion'
if exige_mutante "$T/m2.py" m2; then
r="$(corre "$T/m2.py" "$T/falta.json")"
# ⛔ SE EXIGE EL DIAGNOSTICO, NO UN rc CUALQUIERA (A-04). Esta comprobacion decia `rc != 2 ||
#    mensaje ausente`, un OR que pasaba en cuanto el rc cambiara — y sin la guarda el guion moria
#    en `KeyError` con rc 1, asi que el banco acreditaba un CRASH como si fuera la deteccion.
#    Ahora el guion salta lo no declarado en silencio, que es el defecto real, y aqui se exige
#    verlo: sin mensaje, sin rc 2, y `knowledge` FUERA del reparto porque nadie la sembro.
# ⛔ EL ORDEN DECIDE, Y AQUI ESTABA AL REVES. La guarda de `Traceback` existia —el comentario de
#    arriba dice que se puso justo por eso— pero iba en el `elif`, DETRAS de una condicion que un
#    reventon satisface: sin mensaje y sin rc 2, el `paso` disparaba primero y la guarda no se
#    alcanzaba NUNCA para el caso que la motivo. Un control escrito, correcto e inalcanzable.
#    Medido: sustituyendo este mutante por uno que revienta con `NameError`, la bateria salia 34/0
#    contando el crash como muerte. La comprobacion de reventon va SIEMPRE la primera.
if casa 'Traceback'; then
	malo "el mutante murio con una EXCEPCION, no con el defecto: eso acredita un crash, no la guarda"
elif [ "$r" != "2" ] && ! casa 'SIN declarar' && ! casa '^  knowledge '; then
	paso "sin la guarda, knowledge se siembra FUERA del reparto y nadie lo dice (rc $r): el caso 5 lo caza"
else
	malo "el mutante de la direccion 2 no produjo el defecto esperado (rc $r): mira $SALIDA"
fi
fi

muta "$T/m3.py" '    faltan_gen = sorted(set(decl) - set(GENERADORES))' '    faltan_gen = []  # MUTANTE: no se mira la primera direccion'
if exige_mutante "$T/m3.py" m3; then
r="$(corre "$T/m3.py" "$T/sobra.json")"
if [ "$r" != "2" ] || ! casa 'SIN generador'; then
	paso "el mutante que deja de mirar declarada-sin-generador MUERE en el caso 4 (rc $r)"
else
	malo "el mutante de la direccion 1 SOBREVIVIO: el caso 4 no acredita esa direccion"
fi
fi

muta "$T/m4.py" '                    conflictos += 1' '                    pass  # MUTANTE: el 409 se traga sin contarlo'
if exige_mutante "$T/m4.py" m4; then
r="$(corre "$T/m4.py" "$OBJETIVOS" "409:/v1/m/notify/routes")"
# ⛔ ESTE CASO SE ACREDITA POR AUSENCIA —busca que el mutante DEJE de decir «conflictos 409»— y esa
#    forma tiene la clase de nacimiento: un mutante que revienta tampoco lo dice. Medido igual que
#    el de arriba: con un mutante que revienta, salia `paso`. La guarda va delante.
if casa 'Traceback'; then
	malo "el mutante del 409 murio con una EXCEPCION, no con el defecto: eso acredita un crash"
elif ! casa 'conflictos 409'; then
	paso "el mutante que se traga los 409 MUERE en el caso 8: el rojo deja de decir por que"
else
	malo "el mutante del 409 SOBREVIVIO: el caso 8 no protege el mensaje"
fi
fi

muta "$T/m5.py" '            return None, f"GET {ruta} -> {st} {cuerpo[:120]}"' '            return [], None  # MUTANTE: no poder leer se cuenta como lista vacia'
if exige_mutante "$T/m5.py" m5; then
r="$(corre "$T/m5.py" "$OBJETIVOS" "500:/v1/m/knowledge/kbs")"
if [ "$r" != "2" ]; then
	paso "el mutante que confunde ceguera con lista vacia MUERE en el caso 7 (rc $r, ya no es 2)"
else
	malo "el mutante de la ceguera SOBREVIVIO: el caso 7 no distingue 2 de 0"
fi
fi

# ── 15-16 · LAS DOS DIMENSIONES QUE ME QUEDABAN SIN MUTANTE ────────────────────────────────────
# ⛔ ESTO SALE DE UNA AUTO-LECTURA, no de un lector, y es la primera vez hoy que me la hago antes
#    de publicar en vez de despues del NO. Repasando las ocho dimensiones del guion, DOS tenian
#    caso y no tenian mutante: el mensaje de rc 1 —que es la condicion cabecera de este entregable,
#    «nombra la superficie y su objetivo»— y el diagnostico del fichero de objetivos ausente. Un
#    caso sin mutante mide que HOY pasa, no que manana siga pasando.
muta "$T/m6.py" '        return salir(RC_POR_DEBAJO, "; ".join(f"`{s}` se queda en {d} de {o}" for s, d, o in debajo))' '        return salir(RC_POR_DEBAJO, "alguna superficie se quedo corta")  # MUTANTE: sin nombres'
if exige_mutante "$T/m6.py" m6; then
r="$(corre "$T/m6.py" "$T/imposible.json")"
if [ "$r" = "1" ] && ! casa 'killswitch. se queda en [0-9]+ de 99'; then
	paso "el mutante que borra los nombres del rc 1 MUERE en el caso 3: el rojo deja de decir cual"
else
	malo "el mutante del mensaje de rc 1 SOBREVIVIO (rc $r): la condicion cabecera no esta atada"
fi
fi

muta "$T/m7.py" '        return None, None, f"no encuentro el fichero de objetivos `{ruta}`"' '        return None, None, "error"  # MUTANTE: el diagnostico ya no dice que falta'
if exige_mutante "$T/m7.py" m7; then
python3 "$T/m7.py" http://127.0.0.1:1 t t --objetivos "$T/no-existe.json" >"$SALIDA" 2>&1
r=$?
if [ "$r" = "2" ] && ! casa 'no encuentro el fichero de objetivos'; then
	paso "el mutante que borra el diagnostico del fichero ausente MUERE en el caso 6"
else
	malo "el mutante del diagnostico de objetivos SOBREVIVIO (rc $r)"
fi
fi

# ── 17-18 · EL MODO DE MARCADOR, que es un defecto que me encontre yo releyendo ────────────────
# ⛔ EL DEFECTO ERA ESTE: el bucle preguntaba `if marca in usadas` con `usadas` construida por
#    IGUALDAD EXACTA sobre el campo declarado, y eso estaba MUERTO en 2 de 13 superficies
#    (`console-bindings` guarda `mcp.github#<marca>`, `killswitch` guarda un ID de agente). Ahora
#    cada superficie DECLARA su modo y una guarda estructural lo comprueba antes de sembrar.
#
#    Y la parte que mas ensena: se me escapo la primera vez porque mi sonda pregunto si la marca
#    estaba CONTENIDA y el guion pregunta si es IGUAL. Dos predicados, un falso verde.
muta "$T/m8.py" '    return {"name": marca, "destination": "siem", "min_severity": SEVERIDADES[i % 3]}' '    return {"name": "fijo-" + str(i), "destination": "siem", "min_severity": SEVERIDADES[i % 3]}  # MUTANTE: la marca ya no va al campo'
if exige_mutante "$T/m8.py" m8; then
r="$(corre "$T/m8.py" "$OBJETIVOS")"
if [ "$r" = "2" ] && casa 'modo de marcador' && casa 'alerting'; then
	paso "un generador que deja de poner la marca en su campo sale 2, nombrando la superficie"
elif [ "$r" = "2" ]; then
	malo "salio 2 sin nombrar la superficie ni el modo: la guarda no dice cual se rompio"
else
	malo "el generador incoherente deberia salir 2 y salio $r: la comprobacion de idempotencia quedaria muerta"
fi
fi

# ⛔ Y ESTE MUTANTE ESTUVO MAL ESCRITO Y LO CORRIJO AQUI, porque el error es instructivo: la
#    primera version retiraba la guarda del guion SANO y comprobaba que salia 0. Eso no prueba
#    nada — el guion sano sale 0 con guarda o sin ella. Un mutante solo acredita la pata que
#    NOMBRA si se aplica sobre el caso que esa pata tiene que cazar: aqui, el generador roto de
#    m8. Sin la guarda, ese defecto pasa desapercibido y la corrida ya NO sale 2.
muta_de "$T/m8.py" "$T/m9.py" '    malas = comprueba_marcadores()' '    malas = []  # MUTANTE: la guarda estructural ya no corre'
# ⛔ JUZGA m9, QUE ES EL QUE CORRE. Decia `exige_mutante "$T/m8.py" m8` y a continuacion corria
#    `m9.py`: si m9 no se construia, el juicio daba el visto bueno mirando OTRO artefacto y el
#    `corre` caia sobre un fichero inexistente. La misma clase que este banco cura en el caso 17,
#    reaparecida dos casos mas abajo, que es la razon de que ahora haya un caso por artefacto.
if exige_mutante "$T/m9.py" m9; then
r="$(corre "$T/m9.py" "$OBJETIVOS")"
if [ "$r" != "2" ]; then
	paso "sin la guarda, el generador roto de m8 pasa desapercibido (rc $r): la guarda es la que caza"
else
	malo "el mutante de la guarda SOBREVIVIO: el caso 17 estaria saliendo 2 por otra razon"
fi
fi

# ── 19-20 · EL TOKEN NO SALE POR NINGUNA SALIDA, Y SU MUTANTE ─────────────────────────────────
# ⛔ ES EL A-05 DE the reviewer sobre `fc3394154`, y tenia razon: `_pide` devolvia el cuerpo HTTP CRUDO
#    y `salir` lo imprimia, asi que un receptor que refleje la cabecera `Authorization` filtraba el
#    token. No habia redaccion de salidas: cero.
#
#    El testigo usa un token que NO se parece a ningun secreto conocido, a proposito: el punto del
#    hallazgo hermano en la adopcion fue que un respaldo por FORMA da un verde falso. Aqui lo que
#    tapa es que el guion DECLARA su propio token como sensible, que es lo unico que funciona
#    cuando el texto que lo repite lo escribe otro.
refleja_auth() { # $1 = guion sujeto; rc 0 = tapado
	SUJETO="$1" python3 - <<'PY2'
import contextlib, http.server, importlib.util, io, os, sys, threading
TOKEN = "Tk-arbitrario-sin-forma-conocida-2026"
spec = importlib.util.spec_from_file_location("m", os.environ["SUJETO"])
m = importlib.util.module_from_spec(spec); sys.modules["m"] = m; spec.loader.exec_module(m)


class H(http.server.BaseHTTPRequestHandler):
    def _eco(self):
        # ⛔ SE REFLEJA EL TOKEN PELADO, sin el prefijo `Bearer`, Y ESO ES EL CASO. Con el prefijo lo
        #    tapa el regex generico de la libreria y el mutante de abajo SOBREVIVE — lo medi. Un
        #    servidor que conteste «bad token: <valor>» es realista y solo lo cubre que el guion
        #    DECLARE su propio token: es la mitad que este caso tiene que acreditar.
        a = self.headers.get("Authorization", "").replace("Bearer ", "")
        self.send_response(400); self.end_headers()
        self.wfile.write(f"rejected: bad token {a}".encode())
    do_GET = _eco
    do_POST = _eco

    def log_message(self, *a):
        pass


srv = http.server.HTTPServer(("127.0.0.1", 0), H)
threading.Thread(target=srv.serve_forever, daemon=True).start()
err, out = io.StringIO(), io.StringIO()
try:
    with contextlib.redirect_stderr(err), contextlib.redirect_stdout(out):
        try:
            m.main(["x", f"http://127.0.0.1:{srv.server_port}", TOKEN, "ten"])
        except SystemExit:
            pass
finally:
    srv.shutdown()
sys.exit(1 if TOKEN in err.getvalue() + out.getvalue() else 0)
PY2
}

if refleja_auth "$GUION"; then
	paso "un receptor que REFLEJA la cabecera Authorization no consigue sacar el token"
else
	malo "el token sale por alguna salida cuando el receptor lo refleja: no hay redaccion de frontera"
fi

muta "$T/m10.py" '        redacta.recuerda(token, tenant)' '        pass  # MUTANTE: el token ya no se declara sensible'
if exige_mutante "$T/m10.py" m10; then
if ! refleja_auth "$T/m10.py"; then
	paso "el mutante que deja de declarar el token FUGA: el caso 19 lo caza"
else
	malo "no declarar el token no produce fuga: el caso 19 no ejercita lo que dice"
fi
fi

# ── 21-22 · UNA URL MALFORMADA NO PUEDE FUGAR POR EL TRACEBACK ────────────────────────────────
# ⛔ the reviewer, A-05, y es la MISMA clase que la adopcion pago en su v6: `di()` y `salir()` redactan
#    lo que pasa por ellas, y una excepcion NO CAPTURADA no pasa por ninguna — sale por el
#    traceback que imprime el interprete, con la URL y su credencial dentro. La redaccion mas
#    cuidada del mundo no tapa lo que sale por un camino que no la cruza.
#
#    Dos mitades: el `Request` se construye DENTRO del `try`, y `main` instala un `excepthook` que
#    redacta. Proceso NUEVO en los dos casos, porque el secreto no debe conocerse de antes.
SEC_URL="Zp4-credencial-arbitraria-2026"
url_malformada() { # $1 = guion sujeto; rc 0 = sin fuga
	local out rc
	out="$(python3 "$1" "://usuario:$SEC_URL@host" tok ten --solo-medir 2>&1)"
	rc=$?
	# ⛔ SIN TUBERIA: bajo `pipefail`, `productor | grep -q` devuelve 141 cuando el grep ACIERTA
	#    pronto y sale —SIGPIPE al productor—, asi que la condicion se invierte justo cuando debia
	#    dispararse. `lint:sigpipe-booleans` lo cuenta como deuda y esa pata mata el gancho.
	command grep -qF "$SEC_URL" <<<"$out" && return 1
	[ "$rc" != "0" ] || return 1
	return 0
}

if url_malformada "$GUION"; then
	paso "una URL malformada con credencial arbitraria no fuga por el traceback, y sale rc != 0"
else
	malo "la credencial sale por una excepcion no capturada, o una URL ilegible dio rc 0"
fi

# ⛔ EL MUTANTE RETIRA LAS DOS MITADES, Y ESO NO ES PEREZA: ES LO MEDIDO. Probe a matar cada una
#    por separado y NINGUNA muere, porque cada una tapa el caso ella sola —con el `Request` dentro
#    del `try`, el `ValueError` se captura y sale por `salir()`; con el `excepthook`, el traceback
#    sale ya redactado (`unknown url type: '://<oculto>@host/…'`)—. Son defensa en profundidad de
#    verdad, no una duplicada: la primera cubre ESTE camino, la segunda cubre cualquier camino
#    futuro que nadie capture. Exigir que muera cada una por separado seria fabricar cobertura;
#    exigir que muera el PAR es lo que de verdad se puede afirmar.
#
#    (Y hubo un mutante peor antes: sacaba SOLO la linea del `Request` y dejaba el `try:` sin
#    cuerpo, asi que moria con `IndentationError` — por sintaxis, no por el defecto. De ahi el
#    `compile()`: un mutante que no compila no acredita nada.)
python3 - "$GUION" "$T/m11.py" <<'PY2'
import sys
src = open(sys.argv[1]).read()
viejo_try = """        try:
            pet = urllib.request.Request(self.base + ruta, data=datos, method=metodo)
            pet.add_header("Authorization", "Bearer " + self.token)
            pet.add_header("X-Olivares-Tenant", self.tenant)
            if datos is not None:
                pet.add_header("Content-Type", "application/json")
"""
nuevo_try = """        pet = urllib.request.Request(self.base + ruta, data=datos, method=metodo)
        pet.add_header("Authorization", "Bearer " + self.token)
        pet.add_header("X-Olivares-Tenant", self.tenant)
        if datos is not None:
            pet.add_header("Content-Type", "application/json")
        try:
"""
mut = src.replace(viejo_try, nuevo_try, 1)
assert mut != src, "el mutante NO movio el Request"
viejo_hook = '    instala_excepthook(redacta, "seed-estate-volume")'
mut2 = mut.replace(viejo_hook, "    pass  # MUTANTE: sin frontera para excepciones no capturadas", 1)
assert mut2 != mut, "el mutante NO retiro el excepthook"
compile(mut2, "m11", "exec")  # un mutante que no compila no acredita nada
open(sys.argv[2], "w").write(mut2)
PY2
if ! url_malformada "$T/m11.py"; then
	paso "el mutante que retira LAS DOS mitades FUGA por el traceback: el caso 21 caza el par"
else
	malo "retirar las dos mitades no produce fuga: el caso 21 no ejercita lo que dice"
fi

# ── O · LA CIFRA VIAJA DENTRO DE LA CITA, Y LA DIFERENCIA SE DECLARA ──────────────────────────
# ⛔ CUARTA VUELTA DE ESTA PROCEDENCIA, y la anterior fallaba por una razon que este carril ya
#    tenia escrita: el gate contaba ANCLAS, no CIFRAS. La v5 exigia que la cita existiera una sola
#    vez — pero la cita ata la FRASE, no el VALOR: mutar `alerting.objetivo` de 6 a 7 conservaba la
#    cita y el banco seguia 27/0. «Presencia no es valor», y me lo aplico a mi propio gate.
#
#    Ahora se comprueban TRES cosas, y la del medio es la que faltaba:
#      1. la `cita` aparece EXACTAMENTE una vez en el blob del plan;
#      2. la `cifra_del_plan` declarada esta DENTRO de esa cita — asi el numero es parte de lo que
#         se verifica contra la fuente, no una anotacion al margen;
#      3. si `objetivo` difiere de `cifra_del_plan`, hay un `razon_si_difiere` ESCRITO. Elegir en
#         silencio es lo que hizo falsa la procedencia tres veces.
exige_mutante_dato() { # $1 = fichero mutado, $2 = nombre; rc 0 si existe y no es vacio
	# ⛔ ESTA FUNCION VIVIA EN LA SECCION QUE ESTA REEMPLAZO Y SE PERDIO CON ELLA. El efecto fue
	#    silencioso y de la clase que este banco existe para cortar: `command not found` por stderr,
	#    los dos mutantes SIN juzgar, y el banco anunciando «26 pasan, 0 fallan». Un caso que no
	#    llega a correr no es un caso que pasa. Se redefine aqui, junto a quien la usa.
	[ -s "$1" ] && return 0
	malo "NO se pudo construir el mutante de dato $2: sin artefacto no hay juicio"
	return 1
}
juzga_objetivos() { # $1 = fichero de objetivos; imprime «id<TAB>veredicto» por superficie
	OBJ="$1" PLANTXT="$T/plan.txt" python3 -c '
import json, os
plan = open(os.environ["PLANTXT"], encoding="utf8").read()
for s in json.load(open(os.environ["OBJ"], encoding="utf8"))["superficies"]:
    cita, cifra, razon = s.get("cita"), s.get("cifra_del_plan"), (s.get("razon_si_difiere") or "").strip()
    obj = s.get("objetivo")
    if not cita:
        # ⛔ SIN CITA, LA RAZON ES EL UNICO ANCLA — y tiene que NOMBRAR el numero que ampara. El juez
        #    anterior daba estas cuatro por buenas con solo tener razon, asi que `knowledge 6->7`
        #    dejaba el banco verde: la razon seguia diciendo «el 6 es mio» mientras el objetivo era
        #    7, y nadie comparaba las dos. Lo destapo el mutante por superficie, que es justo para
        #    lo que se puso.
        if not razon:
            print("%s\tSIN-CITA-NI-RAZON" % s["id"])
        elif str(obj) not in razon:
            print("%s\tRAZON-NO-NOMBRA-EL-VALOR(%s)" % (s["id"], obj))
        else:
            print("%s\tSIN-CITA-CON-RAZON" % s["id"])
        continue
    n = plan.count(cita)
    if n != 1:
        print("%s\tCITA-%dx" % (s["id"], n)); continue
    if not cifra or str(cifra) not in cita:
        print("%s\tCIFRA-FUERA-DE-LA-CITA" % s["id"]); continue
    if str(obj) != str(cifra):
        if not razon:
            print("%s\tDIFIERE-SIN-RAZON(%s vs %s)" % (s["id"], obj, cifra)); continue
        # La razon tiene que NOMBRAR el numero que ampara. Sin esto, el juez acepta cualquier
        # texto no vacio: cambiar 12 por 13 conservaba una razon escrita para el 12 y el banco
        # seguia verde — o sea volvia a contar ANCLAS y no valores, que es el defecto que la v6
        # decia cerrar. Con esto, mover la cifra INVALIDA su razon sola.
        if str(obj) not in razon:
            print("%s\tRAZON-NO-NOMBRA-EL-VALOR(%s)" % (s["id"], obj)); continue
    print("%s\tok" % s["id"])
'
}
BLOB="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["plan_blob"])' "$OBJETIVOS" 2>/dev/null || true)"
if [ -z "$BLOB" ]; then
	malo "NO HE PODIDO MIRAR: el fichero de objetivos no declara plan_blob"
elif ! git -C "$RAIZ" show "$BLOB" 2>/dev/null \
     | python3 -c 'import sys;print(sys.stdin.read().replace(chr(92)+chr(110),chr(10)))' >"$T/plan.txt" \
     || [ ! -s "$T/plan.txt" ]; then
	malo "NO HE PODIDO MIRAR: no puedo leer el blob del plan ($BLOB) — sin fuente no hay juicio"
else
	juzga_objetivos "$OBJETIVOS" >"$T/juicio.txt" 2>"$T/juicio.err" || true
	if [ ! -s "$T/juicio.txt" ]; then
		malo "NO HE PODIDO MIRAR: el juicio de objetivos salio vacio (mira $T/juicio.err)"
	else
		ntot=$(wc -l <"$T/juicio.txt")
		command awk -F'\t' '$2 != "ok" && $2 != "SIN-CITA-CON-RAZON"' "$T/juicio.txt" >"$T/juicio-malas.txt" || true
		if [ ! -s "$T/juicio-malas.txt" ]; then
			paso "las $ntot superficies citan un literal unico, con su cifra DENTRO, y declaran toda diferencia"
		else
			malo "superficies cuya procedencia no se sostiene: $(command awk -F'\t' '{printf "%s=%s ", $1, $2}' "$T/juicio-malas.txt")"
		fi
	fi
fi

# ⛔ MUTANTE DE CIFRA, que es el que la v5 no tenia. Se cambia el VALOR de un objetivo sin tocar su
#    cita: el gate viejo seguia verde porque la cita seguia existiendo. Este tiene que ponerse rojo.
# ⛔ UN MUTANTE POR CADA OBJETIVO, no solo por `alerting` (the reviewer). Con uno solo, el caso decia
#    «la cita ya no basta» y era falso para las demas: `agents 12->13` y `knowledge 6->7` dejaban el
#    banco en verde, porque el juez aceptaba una razon VIEJA por el mero hecho de no estar vacia.
#    Un mutante en una fila no acredita las trece.
malas_cifra=""
for SUP in $(python3 -c '
import json, sys
for s in json.load(open(sys.argv[1], encoding="utf8"))["superficies"]:
    print(s["id"])
' "$OBJETIVOS"); do
	python3 - "$OBJETIVOS" "$T/cifra-$SUP.json" "$SUP" <<'PYC'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf8"))
for s in d["superficies"]:
    if s["id"] == sys.argv[3]:
        s["objetivo"] = int(s["objetivo"]) + 1
        break
else:
    sys.exit(1)
json.dump(d, open(sys.argv[2], "w", encoding="utf8"), ensure_ascii=False, indent=2)
PYC
	if [ ! -s "$T/cifra-$SUP.json" ]; then
		malas_cifra="$malas_cifra $SUP(no-construido)"
		continue
	fi
	juzga_objetivos "$T/cifra-$SUP.json" >"$T/j-$SUP.txt" 2>&1 || true
	command awk -F'\t' -v s="$SUP" '$1 == s && $2 != "ok" && $2 != "SIN-CITA-CON-RAZON"' "$T/j-$SUP.txt" \
		>"$T/j-$SUP-malas.txt" || true
	[ -s "$T/j-$SUP-malas.txt" ] || malas_cifra="$malas_cifra $SUP"
done
if [ -z "$malas_cifra" ]; then
	paso "subir en 1 el objetivo de CUALQUIERA de las superficies pone su fila en rojo: el juez mira el VALOR"
else
	malo "superficies cuyo objetivo se puede cambiar sin que nadie se entere:$malas_cifra"
fi

# Y el mutante de la tilde sigue: una cita rota tiene que seguir cazandose.
python3 - "$OBJETIVOS" "$T/objetivos-sin-tilde.json" <<'PYT'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf8"))
tocado = 0
for s in d["superficies"]:
    if s.get("cita") and "á" in s["cita"]:
        s["cita"] = s["cita"].replace("á", "a"); tocado += 1
assert tocado, "el mutante de la tilde NO se aplico: ninguna cita lleva tilde"
json.dump(d, open(sys.argv[2], "w", encoding="utf8"), ensure_ascii=False, indent=2)
PYT
if exige_mutante_dato "$T/objetivos-sin-tilde.json" "objetivos sin tilde"; then
	juzga_objetivos "$T/objetivos-sin-tilde.json" >"$T/juicio-tilde.txt" 2>&1 || true
	if command grep -q 'CITA-0x' "$T/juicio-tilde.txt"; then
		paso "quitar la tilde a una cita la deja sin coincidencia: el caso O distingue viva de muerta"
	else
		malo "el mutante de la tilde SOBREVIVIO: el caso O no distingue una cita viva de una muerta"
	fi
fi

# ── P · LA REDACCION DENTRO DEL EXCEPTHOOK, AISLADA DE SU INSTALACION ─────────────────────────
# ⛔ EL CASO 21 RETIRA LAS DOS MITADES A LA VEZ, y por eso acredita el PAR y no la redaccion. Un
#    hook instalado que imprima el traceback SIN redactar fuga exactamente igual, y este banco lo
#    contaba verde: la mitad que de verdad tapa el secreto no tenia mutante propio. Este si lo es,
#    y muta la LIBRERIA, no el guion — que es donde vive la redaccion desde que se compartio.
muta_de "$GUION" "$T/m12.py" \
	'        try:
            pet = urllib.request.Request(self.base + ruta, data=datos, method=metodo)' \
	'        pet = urllib.request.Request(self.base + ruta, data=datos, method=metodo)
        try:'
mkdir -p "$T/lib12"
muta_de "$LIBDIR/redaccion.py" "$T/lib12/redaccion.py" \
	'        print(redactor(f"{etiqueta}' \
	'        print((f"{etiqueta}'
if exige_mutante "$T/m12.py" m12 && [ -s "$T/lib12/redaccion.py" ] \
   && ! cmp -s "$LIBDIR/redaccion.py" "$T/lib12/redaccion.py"; then
	# ⛔ `export`, no un prefijo: una variable delante de una FUNCION de bash no llega al hijo, y
	#    ese fallo hizo «sobrevivir» a un mutante que nunca se aplico.
	export OLIVARES_LIB_DIR="$T/lib12"
	if ! url_malformada "$T/m12.py"; then
		paso "un excepthook instalado que imprime SIN redactar FUGA: la redaccion del hook tiene mutante propio"
	else
		malo "el hook sin redaccion no fuga: el caso 21 cubre la instalacion, no la redaccion"
	fi
	unset OLIVARES_LIB_DIR
else
	malo "NO se pudo construir el par m12 + libreria mutada: sin artefacto no hay juicio"
fi

# ── N · LOS MENSAJES DE ESTE BANCO NO LLEVAN BACKTICKS SIN ESCAPAR ─────────────────────────────
# ⛔ Generador, no cuidado: los backticks dentro de comillas dobles los EJECUTA la shell, y este
#    carril lo hizo cuatro veces en cuatro ficheros el mismo dia.
if command grep -nE '^[[:space:]]*(paso|malo) "[^"]*`' "${BASH_SOURCE[0]:-$0}" >"$T/backticks.txt"; then
	malo "hay mensajes con backtick sin escapar dentro de comillas dobles (mira $T/backticks.txt)"
else
	paso "ningun mensaje del banco lleva un backtick sin escapar dentro de comillas dobles"
fi

# ── Q · UNA REDIRECCION NO SE LLEVA EL TOKEN A OTRO ORIGEN ────────────────────────────────────
# ⛔ MEDIDO, NO DEDUCIDO: `urllib.request.urlopen` con el abridor por defecto sigue los 30x y COPIA
#    TODAS las cabeceras al nuevo destino. Con un servidor que contesta 302 hacia otro puerto, el
#    segundo origen recibio literalmente `Bearer <el token>`. Si la consola contesta una
#    redireccion —mala configuracion, un proxy delante, o un `Location` que alguien controle— el
#    token de operador sale del edificio sin que ningun guion haya hecho nada mal. Es una fuga por
#    la frontera aunque no se imprima nunca, asi que vive en la misma libreria que el resto.
fuga_por_redireccion() { # $1 = modulo de redaccion a cargar; imprime «FUGA» o «TAPADO»
	LIBDIRQ="$1" python3 - <<'PYQ'
import http.server, importlib.util, os, sys, threading, urllib.error, urllib.request
spec = importlib.util.spec_from_file_location("red", os.path.join(os.environ["LIBDIRQ"], "redaccion.py"))
red = importlib.util.module_from_spec(spec); spec.loader.exec_module(red)
recibidas = {}


class Otro(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        recibidas["auth"] = self.headers.get("Authorization")
        self.send_response(200); self.send_header("Content-Length", "2"); self.end_headers()
        self.wfile.write(b"{}")

    def log_message(self, *a):
        pass


otro = http.server.HTTPServer(("127.0.0.1", 0), Otro)
threading.Thread(target=otro.serve_forever, daemon=True).start()


class Consola(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(302)
        self.send_header("Location", "http://127.0.0.1:%d/robado" % otro.server_port)
        self.end_headers()

    def log_message(self, *a):
        pass


con = http.server.HTTPServer(("127.0.0.1", 0), Consola)
threading.Thread(target=con.serve_forever, daemon=True).start()
pet = urllib.request.Request("http://127.0.0.1:%d/v1/x" % con.server_port)
pet.add_header("Authorization", "Bearer TOKEN-QUE-NO-DEBE-VIAJAR")
try:
    red.abre(pet, timeout=10).read()
except Exception:
    pass
print("FUGA" if recibidas.get("auth") else "TAPADO")
PYQ
}
if [ "$(fuga_por_redireccion "$RAIZ/scripts/lib")" = "TAPADO" ]; then
	paso "una redireccion a OTRO origen no se lleva la cabecera Authorization: la frontera de transporte corta"
else
	malo "el token viaja a otro origen al seguir un 30x: la funcion abre no rehusa la redireccion"
fi

# Su mutante: si `abre` vuelve a ser `urlopen` a secas, la fuga reaparece.
mkdir -p "$T/lib-redir"
if muta_de "$RAIZ/scripts/lib/redaccion.py" "$T/lib-redir/redaccion.py" \
	'    global _ABRIDOR' \
	'    return __import__("urllib.request", fromlist=["x"]).urlopen(pet, timeout=timeout)  # MUTANTE
    global _ABRIDOR'; then
	if [ "$(fuga_por_redireccion "$T/lib-redir")" = "FUGA" ]; then
		paso "con el abridor por defecto el token SI viaja a otro origen: el caso Q acredita la guarda"
	else
		malo "el mutante que vuelve a urlopen no produce fuga: el caso Q no acredita nada"
	fi
else
	malo "NO se pudo construir el mutante del abridor: sin artefacto no hay juicio"
fi

# ── Q-bis · UNA REDIRECCION DEL MISMO ORIGEN SI SE SIGUE ──────────────────────────────────────
# ⛔ TESTIGO POSITIVO, y existe porque mi v1 introdujo una REGRESION que mi propio banco no podia
#    ver: rechazaba TODOS los 30x, no solo el cambio de origen. Un `Location: /final` del MISMO
#    origen —que una consola emite por una ruta canonica o por una barra final— daba 200 antes y
#    pasaba a fallar. Y no lo veia porque el caso Q solo monta OTRO puerto: nunca probaba el salto
#    legitimo. Un banco que solo tiene el caso negativo no distingue «cerrado» de «roto».
sigue_mismo_origen() { # $1 = dir de la libreria; imprime «OK <codigo>» o «FALLO <motivo>»
	LIBDIRQ="$1" python3 - <<'PYQ2'
import http.server, importlib.util, os, sys, threading
spec = importlib.util.spec_from_file_location("red", os.path.join(os.environ["LIBDIRQ"], "redaccion.py"))
red = importlib.util.module_from_spec(spec); spec.loader.exec_module(red)
import urllib.request

VISTO = {}


class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/final":
            VISTO["auth"] = self.headers.get("Authorization")
            cuerpo = b'{"ok":true}'
            self.send_response(200)
            self.send_header("Content-Length", str(len(cuerpo)))
            self.end_headers(); self.wfile.write(cuerpo)
        else:
            # Location RELATIVO y del MISMO origen: el caso legitimo.
            self.send_response(302); self.send_header("Location", "/final"); self.end_headers()

    def log_message(self, *a):
        pass


srv = http.server.HTTPServer(("127.0.0.1", 0), H)
threading.Thread(target=srv.serve_forever, daemon=True).start()
pet = urllib.request.Request("http://127.0.0.1:%d/v1/x" % srv.server_port)
pet.add_header("Authorization", "Bearer TOKEN-DEL-MISMO-ORIGEN")
try:
    r = red.abre(pet, timeout=10)
    print("OK %d" % r.getcode() if VISTO.get("auth") else "FALLO llego sin la cabecera")
except Exception as e:
    print("FALLO %s: %s" % (type(e).__name__, str(e)[:80]))
PYQ2
}
res="$(sigue_mismo_origen "$RAIZ/scripts/lib")"
if [ "${res%% *}" = "OK" ]; then
	paso "una redireccion RELATIVA del mismo origen se sigue y alcanza el destino ($res): no hay regresion"
else
	malo "una redireccion legitima del mismo origen ya no llega: $res"
fi

# Su mutante: si se vuelve a rechazar TODO 30x, este caso positivo muere.
mkdir -p "$T/lib-todos"
if muta_de "$RAIZ/scripts/lib/redaccion.py" "$T/lib-todos/redaccion.py" \
	'                if _origen(destino) == _origen(req.full_url):' \
	'                if False:  # MUTANTE: se rechaza TODO 30x, tambien el del mismo origen'; then
	resm="$(sigue_mismo_origen "$T/lib-todos")"
	if [ "${resm%% *}" = "FALLO" ]; then
		paso "rechazando TODO 30x el salto legitimo se rompe: el caso Q-bis acredita la distincion de origen"
	else
		malo "rechazar todo 30x no rompe el caso positivo: Q-bis no acredita nada ($resm)"
	fi
else
	malo "NO se pudo construir el mutante de rechazo total: sin artefacto no hay juicio"
fi

# ── R · NINGUNA PIEZA DE RED LLAMA A `urlopen` DIRECTAMENTE ───────────────────────────────────
# ⛔ Se cuenta sobre el ARBOL y no sobre una lista: cuatro guiones mandan `Authorization` y los
#    cuatro tienen que pasar por la frontera. Excluye comentarios — la prosa que explica esto
#    contiene la palabra `urlopen`, y contarla seria medir mi propio comentario.
sueltos=""
for g in "$RAIZ"/scripts/seed-adoption-otlp.py "$RAIZ"/scripts/seed-demo-work.py \
	"$RAIZ"/scripts/seed-estate-volume.py "$RAIZ"/scripts/verify-seed-payloads.py; do
	[ -f "$g" ] || continue
	if command grep -nE '^[^#]*urllib\.request\.urlopen\(' "$g" >/dev/null 2>&1; then
		sueltos="$sueltos $(basename "$g")"
	fi
done
if [ -z "$sueltos" ]; then
	paso "los cuatro guiones que mandan Authorization pasan por la frontera, ninguno llama a urlopen suelto"
else
	malo "guiones que siguen llamando a urlopen directamente y fugarian en un 30x:$sueltos"
fi

# ── T · EL SECRETO EN LA RUTA: LIMITE DECLARADO Y COBERTURA EXPLICITA ─────────────────────────
# ⛔ Un secreto SIN FORMA reconocible dentro de un SEGMENTO DE RUTA no lo cubre `recuerda_url` por
#    defecto. MEDIDO: con `https://host/v1/agents/<20 chars>/run`, userinfo y query salen tapados y
#    la ruta sale ENTERA. El defecto no era la cobertura —un secreto en la ruta es indistinguible
#    de un id de recurso sin saber la API— sino que el limite estaba SIN DECLARAR: quien leia
#    «guarda lo sensible de una URL» suponia lo contrario.
#
#    Y NO se tapa por heuristica: un filtro de 16+ caracteres con letras y digitos respeta todas las
#    rutas reales de estos guiones pero tambien tapa `agent-claude-invoice-11`, el MARCADOR con el
#    que los mensajes reconocen una fila sembrada. Taparlo no protege nada y deja el diagnostico sin
#    el dato que lo hace util.
#
#    El caso mide LAS DOS mitades: que por defecto NO cubre —para que el limite siga siendo
#    verdad y nadie lo lea como cubierto— y que con `con_ruta=True` SI.
ruta_tapada() { # $1 = dir de la libreria, $2 = "0"|"1" pedir con_ruta; imprime FUGA|TAPADO
	LIBT="$1" CONRUTA="$2" python3 - <<'PYT'
import importlib.util, os
spec = importlib.util.spec_from_file_location(
    "red", os.path.join(os.environ["LIBT"], "redaccion.py"))
red = importlib.util.module_from_spec(spec); spec.loader.exec_module(red)
SEC = "Xk9Qm2Lp7Rt4Vb1Nz6Yw"
url = "https://host/v1/agents/%s/run" % SEC
r = red.Redactor()
r.recuerda_url(url, con_ruta=True) if os.environ["CONRUTA"] == "1" else r.recuerda_url(url)
print("FUGA" if SEC in r(url) else "TAPADO")
PYT
}
por_defecto="$(ruta_tapada "$RAIZ/scripts/lib" 0)"
con_ruta="$(ruta_tapada "$RAIZ/scripts/lib" 1)"
if [ "$por_defecto" = "FUGA" ] && [ "$con_ruta" = "TAPADO" ]; then
	paso "la ruta no se tapa por defecto (limite declarado, cierto) y SI con con_ruta=True"
elif [ "$con_ruta" != "TAPADO" ]; then
	malo "con con_ruta=True la ruta sigue fugando: la cobertura explicita no funciona"
else
	malo "la ruta se tapa por defecto: el limite declarado en la docstring ya NO es cierto, o se tapa de mas"
fi

# ── S · UNA CREDENCIAL PERCENT-ENCODED SE TAPA EN LAS DOS FORMAS ──────────────────────────────
# ⛔ `urlsplit` devuelve lo que hay EN la URL —codificado—, y una excepcion lo suele traer ya
#    DECODIFICADO: la comparacion literal no casa y la credencial sale entera. MEDIDO antes de
#    curar con `sk-con/barra+y=signos`, que en la URL viaja como `sk-con%2Fbarra%2By%3Dsignos`:
#    el texto con la forma codificada salia tapado y el de la forma decodificada FUGABA.
#    Recordar una sola de las dos es recordar la que casualmente no aparece.
tapa_ambas_formas() { # $1 = dir de la libreria; imprime «cod=… dec=…»
	LIBDIRS="$1" python3 - <<'PYS'
import importlib.util, os, urllib.parse
spec = importlib.util.spec_from_file_location(
    "red", os.path.join(os.environ["LIBDIRS"], "redaccion.py"))
red = importlib.util.module_from_spec(spec); spec.loader.exec_module(red)
SEC = "sk-con/barra+y=signos"
enc = urllib.parse.quote(SEC, safe="")
url = "https://usuario:%s@host/v1/x" % enc
r = red.Redactor(); r.recuerda_url(url)
cod = r("fallo en %s" % url)
dec = r("ValueError: bad auth %s at host" % SEC)
print("cod=%s dec=%s" % (
    "FUGA" if (SEC in cod or enc in cod) else "tapado",
    "FUGA" if (SEC in dec or enc in dec) else "tapado"))
PYS
}
res="$(tapa_ambas_formas "$RAIZ/scripts/lib")"
if [ "$res" = "cod=tapado dec=tapado" ]; then
	paso "una credencial percent-encoded se tapa tanto en su forma codificada como en la decodificada"
else
	malo "la credencial percent-encoded no se tapa en las dos formas: $res"
fi

# Su mutante: si solo se recuerda lo que devuelve el parser, la forma decodificada fuga.
mkdir -p "$T/lib-pct"
if muta_de "$RAIZ/scripts/lib/redaccion.py" "$T/lib-pct/redaccion.py" \
	'                if plano != pieza and len(plano) >= 4:' \
	'                if False:  # MUTANTE: solo se recuerda la forma codificada'; then
	resm="$(tapa_ambas_formas "$T/lib-pct")"
	if [ "$resm" = "cod=tapado dec=FUGA" ]; then
		paso "recordando solo la forma codificada, la decodificada FUGA: el caso S acredita las dos"
	else
		malo "el mutante de la forma decodificada no reprodujo la fuga ($resm): el caso S no acredita nada"
	fi
else
	malo "NO se pudo construir el mutante de la forma decodificada: sin artefacto no hay juicio"
fi

printf '\ntest-seed-estate-volume: %d pasan, %d fallan\n' "$ok" "$fail"
[ "$fail" -eq 0 ] || exit 1
exit 0
