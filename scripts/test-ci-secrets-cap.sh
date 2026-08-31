#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Que el paso `gitleaks` de mainline-ci convierta quedarse sin tiempo en un ROJO CON MOTIVO,
# y no en la muerte del job que GitHub etiqueta `cancelled`.
#
# Por que existe: el 2026-08-22, NUEVE de las catorce ultimas corridas murieron en 45,4 min
# clavados —el techo del job— y las nueve se leian como supersesion. Un gate de secretos
# bloqueante llevaba dias sin dar veredicto y ningun rojo lo decia. La cota interna del paso
# existe para que no caber sea la TERCERA respuesta del contrato (2 = NO PUDE MIRAR).
#
# Se extrae el `run:` REAL del workflow y se ejecuta con `task` sustituido, para que lo probado
# sea el script que corre en CI y no una copia que puede derivar.
set -uo pipefail

RAIZ="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WF="$RAIZ/.github/workflows/mainline-ci.yml"
fallos=0
ok() { printf '  ok    %s\n' "$1"; }
mal() { printf '  FAIL  %s — %s\n' "$1" "$2"; fallos=$((fallos + 1)); }

[ -r "$WF" ] || { echo "test-ci-secrets-cap: 2 NO PUDE MIRAR — sin $WF" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "test-ci-secrets-cap: 2 NO PUDE MIRAR — sin python3" >&2; exit 2; }

# ⛔ CAMINO DE REPUESTO SIN PyYAML, Y NO ES COMODIDAD. Este guion corre en el carril RAPIDO
# (`.githooks/pre-push`), o sea en TODA rama, y con `import yaml` como unica via devolvia 2 en
# cuanto la biblioteca faltaba => rechazaba cualquier push desde un contenedor sin ella, donde
# ademas no hay `pip` en ninguna forma. Es la TERCERA vez que la misma dependencia bloquea el
# carril: `taskfile-shape.py` lo cerro el 2026-08-20, `check-ci-timeout-arithmetic.sh` el
# 2026-08-23, y este quedaba justo DETRAS del segundo, asi que arreglar uno destapo al siguiente.
#
# La lectura de repuesto es CONSERVADORA y reconoce UNA forma: `id: gitleaks` sola en su linea a
# ocho espacios y, dentro del MISMO item, un `run: |` cuyo cuerpo va a diez. Ante cualquier otra
# cosa NO adivina: devuelve vacio, y la guarda de abajo lo convierte en el mismo 2 de siempre.
# Un falso verde en un gate de secretos es peor que seguir bloqueado.
PASO="$(python3 - "$WF" <<'PASOEOF'
import sys, io, os
ruta = sys.argv[1]
lineas = io.open(ruta, encoding="utf-8").read().split("\n")
def por_yaml():
    if os.environ.get("OLIVARES_CI_TIMEOUTS_NO_YAML") == "1":
        return None
    try:
        import yaml
    except Exception:
        return None
    d = yaml.safe_load(io.open(ruta, encoding="utf-8"))
    for st in d["jobs"]["secrets"]["steps"]:
        if st.get("id") == "gitleaks":
            return st["run"]
    return ""
def por_lectura_plana():
    idx = None
    for i, l in enumerate(lineas):
        if l == "        id: gitleaks":
            if idx is not None:
                return ""
            idx = i
    if idx is None:
        return ""
    run = None
    for i in range(idx + 1, len(lineas)):
        l = lineas[i]
        if l.startswith("      - "):
            break
        if l == "        run: |":
            run = i
            break
    if run is None:
        return ""
    cuerpo = []
    for i in range(run + 1, len(lineas)):
        l = lineas[i]
        if l.strip() == "":
            cuerpo.append("")
            continue
        if not l.startswith("          "):
            break
        cuerpo.append(l[10:])
    return "\n".join(cuerpo) + "\n" if cuerpo else ""
texto = por_yaml()
if texto is None:
    texto = por_lectura_plana()
sys.stdout.write(texto or "")
PASOEOF
)" || {
	# ⛔ EL MENSAJE ATRIBUIA MAL SU PROPIA CAUSA, y no es cosmetica. Este `||` recoge CUALQUIER
	# fallo del interprete —modulo ausente incluido— y lo reportaba como «no extraigo el paso
	# gitleaks», o sea como un problema de FORMA del workflow. Quien lo depura se va a
	# `.github/workflows/`, que esta bien, en vez de a la dependencia, que es lo que falla.
	#
	# No es hipotesis: le paso a OTRO CARRIL esta madrugada. Vio ese mensaje, estuvo a punto de
	# publicar que la causa era otra, y solo lo evito ejecutando el guion a mano para ver el
	# traceback que quedaba arriba, suelto. El defecto entro con `d2f21c592` (2026-08-22) y sigue
	# VIVO en `main`, asi que engaña hoy a cualquiera que empuje.
	#
	# Las dos mitades mandan a mirar sitios distintos, asi que se escriben distinto — que es el
	# contrato de tres respuestas que este guion ya declara arriba.
	echo "test-ci-secrets-cap: 2 NO PUDE MIRAR — no he podido EJECUTAR el lector del workflow (interprete o entorno), asi que no digo nada sobre su forma" >&2
	exit 2
}
[ -n "$PASO" ] || { echo "test-ci-secrets-cap: 2 NO PUDE MIRAR — el lector CORRIO y no encontro el paso gitleaks: o el workflow no lo declara con la forma esperada, o la lectura de repuesto no supo leerlo" >&2; exit 2; }

case "$PASO" in
*PIPESTATUS*) : ;;
*) echo "test-ci-secrets-cap: 2 NO PUDE MIRAR — el paso extraido no lee PIPESTATUS" >&2; exit 2 ;;
esac

# El techo del job tiene que quedar POR ENCIMA de la cota interna, o la cota no llega a disparar
# nunca y todo esto es decorativo.
# El techo del job sale de `scripts/ci-timeouts.py`, que YA tiene su camino sin PyYAML y ya es el
# lector de techos de este repositorio. Reutilizarlo evita una TERCERA copia de la misma lectura,
# que es como dos guardas del mismo hecho acaban discrepando.
TECHO="$(python3 "$(dirname "${BASH_SOURCE[0]:-$0}")/ci-timeouts.py" "$(dirname "$WF")" 2>/dev/null \
	| awk -F'\t' '$1=="JOB" && $2=="mainline-ci.yml" && $3=="secrets" {print $4}')"
COTA="$(printf '%s' "$PASO" | grep -oE '[0-9]+m \\?$|[0-9]+m ' | grep -oE '[0-9]+' | head -1)"
if [ -n "${COTA:-}" ] && [ -n "${TECHO:-}" ] && [ "$COTA" -lt "$TECHO" ]; then
	ok "la cota interna (${COTA}m) queda por debajo del techo del job (${TECHO}m)"
else
	mal "cota vs techo" "cota='${COTA:-?}' techo='${TECHO:-?}': la cota no puede disparar antes"
fi

# ⛔ EL SUSTITUTO DE `task` TIENE QUE PODER EJECUTARSE, y en este contenedor /tmp esta montado
# noexec. Sin esta busqueda, `mktemp -d` cae en /tmp, bash SALTA el sustituto por no poder
# ejecutarlo y corre el `task` DE VERDAD: el test pasaba a medir el barrido real de gitleaks en
# vez del script del paso. Salia rojo por la razon equivocada, y podria haber salido verde.
EXEC_DIR=""
for cand in "${TMPDIR:-}" /workspace/.olivares-tmptest "$RAIZ/.ci-secrets-cap-tmp"; do
	[ -n "$cand" ] || continue
	mkdir -p "$cand" 2>/dev/null || continue
	sonda="$cand/.sonda-exec-$$"
	printf '#!/usr/bin/env bash\nexit 7\n' > "$sonda" 2>/dev/null || continue
	chmod +x "$sonda" 2>/dev/null || { rm -f "$sonda"; continue; }
	"$sonda" >/dev/null 2>&1
	[ "$?" = "7" ] && { EXEC_DIR="$cand"; rm -f "$sonda"; break; }
	rm -f "$sonda"
done
if [ -z "$EXEC_DIR" ]; then
	echo "test-ci-secrets-cap: 2 NO PUDE MIRAR — ningun directorio de trabajo permite ejecutar;" >&2
	echo "test-ci-secrets-cap:   sin el, el sustituto de \`task\` se salta y se mide el barrido real." >&2
	exit 2
fi

corre() { # $1 = comportamiento del `task` sustituido; imprime "<rc>|<salida>"
	local modo="$1" caja rc out
	caja="$(mktemp -d "$EXEC_DIR/ci-secrets-cap.XXXXXX")"
	mkdir -p "$caja/bin"
	case "$modo" in
	cuelga) printf '#!/usr/bin/env bash\nsleep 30\n' > "$caja/bin/task" ;;
	limpio) printf '#!/usr/bin/env bash\necho "check-secrets: CLEAN"\nexit 0\n' > "$caja/bin/task" ;;
	sucio) printf '#!/usr/bin/env bash\necho "check-secrets: DIRTY"\nexit 1\n' > "$caja/bin/task" ;;
	esac
	chmod +x "$caja/bin/task"
	# La cota real es de minutos; aqui se reduce a 2s para que el caso se pueda probar.
	printf '%s\n' "$PASO" | sed -E 's/(--kill-after=)[0-9]+s ([0-9]+m)/\13s 2s/' > "$caja/paso.sh"
	out="$(PATH="$caja/bin:$PATH" RUNNER_TEMP="$caja" bash -e "$caja/paso.sh" 2>&1)" && rc=0 || rc=$?
	rm -rf -- "$caja"
	printf '%s|%s' "$rc" "$(printf '%s' "$out" | tr '\n' ' ')"
}

echo "test-ci-secrets-cap: no caber es un rojo CON MOTIVO, no un cancelled"

r="$(corre cuelga)"
if [ "${r%%|*}" = "2" ] && case "${r#*|}" in *"NO PUDE MIRAR"*) true ;; *) false ;; esac; then
	ok "agotar la cota sale 2 y NOMBRA que no pudo mirar"
else
	mal "agotar la cota" "rc=${r%%|*} salida='${r#*|}'"
fi

r="$(corre limpio)"
[ "${r%%|*}" = "0" ] && ok "un barrido limpio sigue saliendo 0" || mal "barrido limpio" "rc=${r%%|*}"

r="$(corre sucio)"
[ "${r%%|*}" = "1" ] && ok "un hallazgo real sigue saliendo 1, no se lo come la cota" \
	|| mal "hallazgo real" "rc=${r%%|*} — el codigo del barrido no llega intacto"

# CONTROL NEGATIVO: leer el codigo del PIPE en vez de PIPESTATUS. Un barrido agotado tiene que
# pasar a verde; si no pasa, este test no distingue las dos formas y no prueba nada.
PASO_VIEJO="$(printf '%s\n' "$PASO" | sed 's/rc=\${PIPESTATUS\[0\]}/rc=$?/')"
if [ "$PASO_VIEJO" = "$PASO" ]; then
	mal "CONTROL NEGATIVO" "no he sabido construir la forma vieja: el mutante no aplica"
else
	PASO_ORIG="$PASO"; PASO="$PASO_VIEJO"
	r="$(corre cuelga)"
	PASO="$PASO_ORIG"
	[ "${r%%|*}" = "0" ] && ok "CONTROL NEGATIVO: leyendo el pipe, un barrido agotado sale VERDE" \
		|| mal "CONTROL NEGATIVO" "la forma vieja dio rc=${r%%|*} y esperaba 0: este test no ve la diferencia"
fi

if [ "$fallos" -eq 0 ]; then
	echo "test-ci-secrets-cap: 0 CLEAN — 5 casos"
	exit 0
fi
echo "test-ci-secrets-cap: 1 — $fallos caso(s) mal"
exit 1
