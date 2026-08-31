#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# TRINQUETE: guiones que pueden morir MUDOS saltandose su propio mensaje.
#
# ⛔ LA CLASE, medida el 2026-08-31 sobre un gate REAL de la casa (`check-aws-estate.sh`): bajo
# `set -euo pipefail`, una asignacion por sustitucion cuyo comando usa el rc como DATO —`grep`,
# `comm`, `diff`, `cmp`— mata el guion. Y lo que mata es la LINEA SIGUIENTE, que suele ser la que
# explica el problema:
#
#     idle="$(grep -E 'tcp_idle_timeout *=' "$F" | head -1)"
#     [ -n "$idle" ] || fail "could not read tcp_idle_timeout"      # ⛔ INALCANZABLE
#
# El mensaje que nombra la guarda es inalcanzable EXACTAMENTE cuando la condicion que describe es
# verdadera. El sintoma es el silencio: rc≠0 y stderr vacio, indistinguible de un fallo de entorno.
#
# ⛔ Y FALLA EN LAS DOS DIRECCIONES, que es lo que no se espera. Medido con un log de 6,4 MB:
# `grep … | head -1` muere SIN coincidencias **y muere igual con MUCHAS**, porque `head` cierra la
# tuberia, `grep` recibe SIGPIPE (141) y `pipefail` lo propaga. ⇒ el guion muere justo cuando el
# dato SI esta, y solo si el fichero es grande: con un fixture corto pasa. Verde en el banco, mudo
# en produccion.
#
# LA CURA es un `|| true` DENTRO de la sustitucion, que absorbe el 1 y el 141:
#     n="$( { grep -oE 'PATRON' "$F" || true; } | head -1 )"
#
# ⛔ POR QUE TRINQUETE Y NO GATE A SECAS. Hay deuda medida (censo de
# `an internal design note (not shipped)`) en ficheros de varios carriles. Poner rojo hoy
# bloquea a todos por algo que nadie introdujo hoy; cablear con linea base da el control YA y no
# para a nadie. La linea base es una LISTA y no un numero, por lo mismo que el trinquete de
# formato: un contador deja pasar la SUSTITUCION —arreglas uno, rompes otro, el total no se mueve—.
#
# ⛔ Y LA CLAVE ES `fichero:variable`, NO `fichero:linea`: las lineas se mueven con cualquier
# edicion de arriba y la linea base se volveria ruido en una semana. El nombre de la variable
# sobrevive al reformateo.
#
# ⛔ LIMITACION MEDIDA, y va aqui porque un trinquete que no declara su punto ciego se lee como
#    un detector completo: el discriminante exige que el mensaje aparezca en las 4 lineas
#    SIGUIENTES a la asignacion. Es una HEURISTICA, no una propiedad — y el recuento crece de
#    forma monotona con esa ventana, medido el 2026-08-31 sobre `main`:
#
#        ventana  4 → 6 hallazgos      ventana  8 → 10
#        ventana  6 → 8                ventana 12 → 14     ventana 20 → 19
#
#    No hay corte natural, asi que ensanchar es elegir un numero, no descubrir la verdad. Se deja
#    en 4 a proposito: el trinquete existe para que la deuda NO SUBA, y con una ventana estrecha
#    los positivos son solidos. Lo que NO se puede leer de un verde aqui es «no queda ninguno».
#
#    ⚠ EJEMPLO VIVO de lo que esta ventana NO ve, para que nadie lo descubra por sorpresa:
#    `scripts/test-exec-tmpdir.sh:395` sigue siendo vulnerable —`ini="$(grep … | head -1 | cut …)"`
#    bajo `set -euo pipefail`— y su `malo "NO HE PODIDO MIRAR…"` esta cinco lineas mas abajo. Estuvo
#    en la linea base y salio del censo **sin curarse**: otro carril reordeno el bloque y alejo el
#    mensaje. Un hallazgo que desaparece porque el codigo se movio no es un hallazgo resuelto.
#
# Tres respuestas, nunca dos:
#   0  la deuda no sube (y dice si puede bajar)
#   1  hay un incumplidor NUEVO — lo nombra con fichero, linea y la variable
#   2  NO HE PODIDO MIRAR (sin python3, sin linea base, salida ilegible). Nunca es un verde.
set -uo pipefail

# ⛔ `--gate`: la puerta de produccion no admite anulaciones, por el hallazgo que ya costo un
#    `lint:format-ratchet` entero en verde sin ejecutar nada. La bateria llama SIN `--gate`.
GATE=0
for _a in "$@"; do [ "$_a" = "--gate" ] && GATE=1; done
if [ "$GATE" -eq 1 ]; then
	for _v in MUTE_PIPEFAIL_ROOT MUTE_PIPEFAIL_BASELINE; do
		if [ -n "${!_v:-}" ]; then
			echo "check-mute-pipefail: NO HE PODIDO MIRAR: $_v esta puesta y --gate no admite anulaciones" >&2
			exit 2
		fi
	done
fi

ROOT="${MUTE_PIPEFAIL_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || true)}"
[ -n "$ROOT" ] || { echo "check-mute-pipefail: NO HE PODIDO MIRAR: no estoy en un arbol git" >&2; exit 2; }
# ⛔ DONDE VIVE LA LINEA BASE LO DECIDIERON DOS CONTROLES QUE TIRAN EN SENTIDO CONTRARIO, y la
#    primera eleccion fallaba uno de los dos en silencio:
#      · bajo `scripts/`, `check-claim-safety` la trata como un guion y exige el bit de ejecucion;
#      · bajo `design/`, pasa ese control… y NO VIAJA EN EL EXPORT (design/ publica CERO rutas),
#        asi que el gate publicado saldria rc 2 —«falta la linea base»— para el lector publico.
#        Medido con `export-public.sh --manifest`: el guion viaja, el fichero no.
#    `ci/` viaja y ya aloja datos (`ci/download-contract.txt`), igual que la linea base del
#    trinquete de formato viaja con el suyo. Satisface a los dos.
BASE="${MUTE_PIPEFAIL_BASELINE:-$ROOT/ci/mute-pipefail-baseline.txt}"
[ -f "$BASE" ] || { echo "check-mute-pipefail: NO HE PODIDO MIRAR: falta la linea base $BASE" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "check-mute-pipefail: NO HE PODIDO MIRAR: no hay python3" >&2; exit 2; }

CENSO="$(ROOT="$ROOT" GATE_BASENAME="$(basename "$0")" python3 - <<'PY'
import os, re, subprocess, sys
root = os.environ["ROOT"]
try:
    fs = subprocess.run(["git","-C",root,"ls-files","scripts/"],capture_output=True,text=True,check=True).stdout.split()
except Exception as e:
    print("ERROR ls-files:", e, file=sys.stderr); sys.exit(2)
# ⛔ EL BANCO DE ESTE GATE CONTIENE LA CLASE POR CONSTRUCCION: sus fixtures SON ejemplos de
#    asignacion que muere muda, y un escaner de texto no distingue un fixture de codigo real.
#    La exclusion se DERIVA del nombre REAL con que se invoca este fichero —`$(basename "$0")`,
#    que viaja por entorno porque quien lo sabe es bash, no este bloque—, no es una lista a mano:
#    asi no puede crecer en silencio y sigue al gate si lo renombran. El banco lo comprueba
#    RENOMBRANDO el gate, no leyendo esta linea.
#
#    ⛔ La version anterior ponia `os.path.basename(__file__ if False else "check-mute-pipefail.sh")`
#       —un LITERAL con un `if False` de andamio— y el comentario afirmaba que derivaba. Un lector
#       lo desmintio copiando el gate con otro nombre: acusaba a su propio banco. Un comentario que
#       describe lo que el codigo NO hace es peor que no tenerlo.
propio = "scripts/test-" + os.environ.get("GATE_BASENAME", "")
fs = [f for f in fs if f.endswith(".sh") and f != propio]
RC_DATO = re.compile(r'\b(grep|comm|diff|cmp)\b')
MSG     = re.compile(r'\b(fail|malo|cannot|die)\b')
for f in fs:
    try: s = open(os.path.join(root,f), encoding="utf8", errors="replace").read()
    except OSError: continue
    if "pipefail" not in s or not re.search(r"set -[a-z]*e", s): continue
    ls = s.split("\n")
    for i, l in enumerate(ls):
        m = re.match(r'^\s*([A-Za-z_]\w*)=\"?\$\(', l)
        if not m: continue
        var = m.group(1)
        # consumir la sustitucion, sus continuaciones y un `||` PROPIO pegado
        cuerpo, j = l, i
        while j+1 < len(ls) and cuerpo.count("$(") > cuerpo.count(")"):
            j += 1; cuerpo += "\n" + ls[j]
        while j+1 < len(ls) and (ls[j].rstrip().endswith("\\") or re.match(r'^\s*\|\|', ls[j+1])):
            j += 1; cuerpo += "\n" + ls[j]
        if not RC_DATO.search(cuerpo): continue
        if re.search(r'\|\|\s*(true|:|cannot|fail|malo|die)', cuerpo): continue   # ya protegida
        sig = "\n".join(ls[j+1:j+5])
        if MSG.search(sig) and re.search(r'\$\{?'+var+r'\b', sig):
            print(f"{f}\t{var}\t{i+1}")
PY
)" || { echo "check-mute-pipefail: NO HE PODIDO MIRAR: el censo fallo" >&2; exit 2; }

# La salida vacia es legitima (cero hallazgos) y no se distingue de un fallo por el texto: por eso
# el rc del censo se lee arriba y este bloque solo compara.
HOY="$(printf '%s\n' "$CENSO" | awk -F'\t' 'NF>=2 {print $1"\t"$2}' | LC_ALL=C sort -u)"
LB="$(command grep -vE '^\s*(#|$)' "$BASE" | LC_ALL=C sort -u)"

NUEVOS="$(comm -23 <(printf '%s\n' "$HOY") <(printf '%s\n' "$LB") | command grep -v '^$' || true)"
IDOS="$(comm -13 <(printf '%s\n' "$HOY") <(printf '%s\n' "$LB") | command grep -v '^$' || true)"

n_hoy=$(printf '%s\n' "$HOY" | command grep -c . || true)
n_lb=$(printf '%s\n' "$LB" | command grep -c . || true)
echo "check-mute-pipefail: $n_hoy incumplidor(es) hoy, $n_lb en la linea base."

if [ -n "$IDOS" ]; then
	echo "check-mute-pipefail: la deuda PUEDE BAJAR — estos ya no incumplen y siguen en la linea base:" >&2
	printf '%s\n' "$IDOS" | sed 's/^/    /' >&2
	echo "    Retiralos de $BASE en el mismo commit que los cura." >&2
fi

if [ -n "$NUEVOS" ]; then
	echo "check-mute-pipefail: ⛔ INCUMPLIDOR NUEVO — puede morir mudo saltandose su propio mensaje:" >&2
	while IFS=$'\t' read -r f v; do
		[ -n "$f" ] || continue
		ln="$(printf '%s\n' "$CENSO" | awk -F'\t' -v a="$f" -v b="$v" '$1==a && $2==b {print $3; exit}')"
		echo "    $f:${ln:-?}  variable \`$v\`" >&2
	done <<EOF_NUEVOS
$NUEVOS
EOF_NUEVOS
	echo "    Cura: n=\"\$( { grep … || true; } | head -1 )\" — el \`|| true\` absorbe el 1 y el 141 de SIGPIPE." >&2
	exit 1
fi

echo "check-mute-pipefail: la deuda no sube."
exit 0
