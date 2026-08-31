#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
#
# check-run-block-syntax.sh — a repository gate. Que el shell de cada bloque `run:` de un workflow PARSEE.
#
# ⛔ POR QUE EXISTE, y es un defecto que ya costo una corrida entera el 2026-08-29. El job
#    `hook-only-legs` murio en su PRIMER estreno real (run 33284926144, paso 6) con **exit 2 y NI
#    UNA LINEA de salida**: un `if … else …` sin su `fi`. Bash sale 2 ante un error de sintaxis y no
#    imprime nada del guion, asi que el log ensena el script y despues «Process completed with exit
#    code 2». El job midio 1 de 14 pasos.
#
# ⛔ Y LO QUE LO HACE UNA CLASE, no un descuido: **NADA en este arbol podia verlo**. `lint:actions`
#    corre actionlint, que valida el WORKFLOW —sintaxis YAML, expresiones, contextos, acciones— y
#    **no parsea el shell de dentro de `run:`**. Se cito ese gate como testigo del paso en commit
#    tras commit y estuvo verde todos ellos. Un gate dice lo que su MECANISMO DE DESCUBRIMIENTO
#    alcanza, y aqui el mecanismo no llegaba.
#
#    Sin esto, un error de sintaxis en un `run:` **solo se descubre cuando ese job corre** — que
#    para `release.yml` es el dia del release, y para `drills-nightly.yml` la madrugada siguiente.
#    Barrido del arbol el 2026-08-29: 273 bloques analizables, 0 rotos. Cuesta menos de un segundo.
#
# ⛔ LIMITE DECLARADO, y es el precio de poder mirar: las expresiones `${{ … }}` de GitHub NO son
#    shell. Antes de parsear se sustituyen por un TOKEN NEUTRO (`GHEXPR`), asi que este gate
#    comprueba la sintaxis del bloque **con las expresiones ya resueltas a una palabra**. No ve
#    errores que dependan del VALOR que GitHub inyecte —una expresion que expanda a comillas
#    desbalanceadas sigue siendo invisible— y lo dice en vez de fingir que mira eso tambien.
#
# ⛔ SEGUNDO LIMITE: solo se juzgan los bloques cuyo shell es `bash` o `sh`. Un paso con
#    `shell: pwsh`, `python` o `node` se CUENTA y se declara omitido: bash no puede opinar de su
#    sintaxis, y llamarlo «limpio» seria certificar lo que no se mira.
#
# Salidas: 0 = todos parsean · 1 = alguno NO parsea (se nombra fichero, job, paso e id) · 2 = NO HE
#          PODIDO MIRAR (sin python3, sin PyYAML, un workflow ilegible, cero bloques analizables).
set -uo pipefail
LC_ALL=C
export LC_ALL

# ⛔ UN ARGUMENTO QUE NO SE HONRA SALE 2, Y NO ES PEDANTERIA: ES LA CLASE QUE ESTE GATE VIGILA.
#
# Medido el 2026-08-30 sobre mi propio uso: llame a este guion con `"$W/.github/workflows"` como
# argumento, esperando medir el arbol extraido de `origin/main`. El guion IGNORABA el posicional,
# midio el arbol de MI worktree —que si llevaba la cura— y contesto **rc=0 con total conviccion**
# sobre un sujeto que no era el que le habia nombrado. Estuve a un mensaje de publicar «main ya no
# esta roto», que era falso: `bash -n` sobre el bloque de main da 2.
#
# El universo equivocado no produce un error: produce un CERO CONVINCENTE, que es exactamente lo
# que este gate existe para cortar un piso mas abajo. Tercera herramienta de la misma noche con la
# misma forma (`check-md-links.py` toma raices y acepta ficheros; un `pgrep -f` de r26).
#
# ⇒ La raiz se fija con OLIVARES_ROOT y el directorio con OLIVARES_RUNBLOCK_WFDIR. Cualquier
# argumento posicional es una intencion que este guion NO puede cumplir, y callarla seria mentir.
if [ "$#" -gt 0 ]; then
	echo "check-run-block-syntax: ⛔ NO HE PODIDO MIRAR: he recibido $# argumento(s) posicional(es) y no honro ninguno." >&2
	echo "  La raiz se fija con OLIVARES_ROOT; el directorio de workflows, con OLIVARES_RUNBLOCK_WFDIR." >&2
	echo "  Recibido: $*" >&2
	exit 2
fi

RAIZ="${OLIVARES_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || echo "")}"
[ -n "$RAIZ" ] || { echo "check-run-block-syntax: ⛔ NO HE PODIDO MIRAR: no estoy en un repositorio." >&2; exit 2; }
cd "$RAIZ" || { echo "check-run-block-syntax: ⛔ NO HE PODIDO MIRAR: no entro en $RAIZ." >&2; exit 2; }
WFDIR="${OLIVARES_RUNBLOCK_WFDIR:-.github/workflows}"
MIN="${OLIVARES_RUNBLOCK_MIN:-50}"
command -v python3 >/dev/null 2>&1 || { echo "check-run-block-syntax: ⛔ NO HE PODIDO MIRAR: no hay python3." >&2; exit 2; }
[ -d "$WFDIR" ] || { echo "check-run-block-syntax: ⛔ NO HE PODIDO MIRAR: no existe $WFDIR." >&2; exit 2; }

python3 - "$WFDIR" "$MIN" <<'PY'
import glob, os, re, subprocess, sys, tempfile

wfdir, minimo = sys.argv[1], int(sys.argv[2])
def cannot(m):
    print(f"check-run-block-syntax: ⛔ NO HE PODIDO MIRAR: {m}", file=sys.stderr); sys.exit(2)
try:
    import yaml
except Exception:
    cannot("no puedo importar yaml para leer los workflows")

EXPR = re.compile(r"\$\{\{.*?\}\}", re.S)
ficheros = sorted(glob.glob(os.path.join(wfdir, "*.yml")) + glob.glob(os.path.join(wfdir, "*.yaml")))
if not ficheros:
    cannot(f"cero workflows en {wfdir}")

analizados = 0
omitidos = []
malos = []
for f in ficheros:
    try:
        doc = yaml.safe_load(open(f, encoding="utf-8"))
    except Exception as e:
        cannot(f"{f} no parsea como YAML: {e}")
    if not isinstance(doc, dict):
        continue
    # el shell por defecto puede venir del workflow o del job; el del paso manda sobre los dos
    wf_shell = (((doc.get("defaults") or {}).get("run") or {}).get("shell"))
    for jn, j in (doc.get("jobs") or {}).items():
        if not isinstance(j, dict):
            continue
        job_shell = (((j.get("defaults") or {}).get("run") or {}).get("shell")) or wf_shell
        for i, s in enumerate(j.get("steps") or [], 1):
            if not isinstance(s, dict) or "run" not in s:
                continue
            shell = (s.get("shell") or job_shell or "bash").split()[0]
            quien = f"{os.path.basename(f)} · {jn} · paso {i} ({s.get('id') or s.get('name') or '<sin nombre>'})"
            if shell not in ("bash", "sh"):
                omitidos.append(f"{quien} — shell: {shell}")
                continue
            cuerpo = EXPR.sub("GHEXPR", str(s["run"]))
            analizados += 1
            with tempfile.NamedTemporaryFile("w", suffix=".sh", delete=False) as t:
                t.write(cuerpo); ruta = t.name
            r = subprocess.run([shell, "-n", ruta], capture_output=True, text=True)
            os.unlink(ruta)
            if r.returncode != 0:
                err = (r.stderr.strip().splitlines() or ["<sin mensaje>"])[-1]
                err = err.replace(ruta, "<bloque>")
                malos.append(f"{quien}\n      {err}")

# CONTROL POSITIVO. Con cero bloques analizados, «todos parsean» seria cierto y vacio a la vez —
# el mismo cero que en este arbol ya ha disfrazado dos censos rotos. Un conjunto vacio no aprueba.
if analizados < minimo:
    cannot(f"solo {analizados} bloque(s) analizable(s) (minimo {minimo}); la extraccion no esta casando")

if malos:
    print(f"check-run-block-syntax: ⛔ {len(malos)} bloque(s) `run:` NO parsean:", file=sys.stderr)
    for m in malos:
        print(f"    {m}", file=sys.stderr)
    print("  Bash sale 2 ante un error de sintaxis y NO imprime nada del guion: en CI eso es un", file=sys.stderr)
    print("  paso rojo sin una sola linea, y el job muere ahi. Se ve aqui en <1 s o alli cuando ese", file=sys.stderr)
    print("  job corra — que para release.yml es el dia del release.", file=sys.stderr)
    sys.exit(1)

extra = f" · {len(omitidos)} omitido(s) por shell no-bash" if omitidos else ""
print(f"check-run-block-syntax: {analizados} bloque(s) `run:` parsean{extra} (expresiones ${{{{ }}}} sustituidas por un token)")
for o in omitidos:
    print(f"  omitido: {o}")
PY
