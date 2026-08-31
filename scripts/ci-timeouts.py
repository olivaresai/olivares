# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Imprime, por cada JOB de un workflow, su techo y el de sus pasos.

Salida: una linea TSV por job con techo declarado

    JOB<TAB>fichero<TAB>job<TAB>techo<TAB>suma_pasos<TAB>pasos<TAB>pasos_con_guarda

Si no puede mirar, imprime UNA linea `NOPUEDO<TAB>razon` y nada mas. Nunca
adivina: un veredicto vacio y uno incompleto se escriben distinto, porque quien
lo consume decide entre 1 (hallazgo) y 2 (no he podido mirar).

⛔ POR QUE HAY UN CAMINO SIN PyYAML, Y NO ES COMODIDAD (2026-08-23).
La leccion ya estaba escrita en `taskfile-shape.py:14-27` el 2026-08-20 y este
gate, creado el 08-22, no la aplico. Medido el 2026-08-23 en un contenedor de
desarrollo SIN PyYAML, sobre `origin/main` puro y sin ningun cambio local:

    check-ci-timeout-arithmetic: 2 NO PUDE MIRAR — sin PyYAML
    task lint:ci-timeout-arithmetic -> 201

`.githooks/pre-push:1205` invoca esa tarea, y la linea 1218 de ese mismo fichero
es `# --- FAST LOCAL GATE ENDS HERE ---`: el gate vive en el carril RAPIDO, que
corre para TODA rama. ⇒ en una caja sin PyYAML el hook rechazaba **cualquier
push, de cualquier rama**, y en esta caja no hay `pip` en ninguna forma, asi que
no era una dependencia que el carril pudiera resolver.

Y el defecto no se veia porque **el gate no es reproducible entre cajas**: el
mismo commit da `CLEAN` donde PyYAML esta y `2` donde no. El 2026-08-23 el
integrador publico `check-ci-timeout-arithmetic: CLEAN` mientras esta caja daba
`rc=2` sobre el mismo arbol.

LIMITES DEL CAMINO DE REPUESTO, declarados porque aqui no se pueden contrastar:
  * Es una LECTURA CONSERVADORA, no un parser de YAML. Los 18 workflows de este
    repositorio no tienen tabs, ni anclas, ni alias, ni merge keys, ni `jobs:` en
    flujo, y sus 22 techos de job estan a indentacion 4 y sus 27 guardas de paso
    a indentacion 8 (medido el 2026-08-23). Si encuentra CUALQUIER cosa que no
    sepa leer con certeza, **no adivina: imprime NOPUEDO** y el gate vuelve a
    rehusar, que es el estado de hoy y nunca un falso verde.
  * En ESTA caja no se puede contrastar contra PyYAML, porque PyYAML es justamente
    lo que falta. Por eso existe `OLIVARES_CI_TIMEOUTS_NO_YAML=1`: la bateria corre
    cada caso por las DOS vias y exige el mismo veredicto, asi que en cualquier caja
    que SI tenga la biblioteca el camino de repuesto queda contrastado contra el de
    siempre en cada push. Declarar el limite no bastaba: un camino que solo se
    ejercita donde no hay con que compararlo es un camino que envejece a ciegas.
"""
import glob
import os
import re
import sys

DIRECTORIO = sys.argv[1]


def no_puedo(razon):
    print("NOPUEDO\t%s" % str(razon).replace("\n", " ")[:160])
    raise SystemExit(0)


def ficheros():
    fs = sorted(glob.glob(os.path.join(DIRECTORIO, "*.yml")))
    fs += sorted(glob.glob(os.path.join(DIRECTORIO, "*.yaml")))
    return fs


def por_yaml(fs):
    """El camino de siempre. Devuelve las filas, o None si PyYAML no esta.

    `OLIVARES_CI_TIMEOUTS_NO_YAML=1` lo salta a proposito. No es un flag de
    conveniencia: es lo que permite que la bateria corra CADA caso por las dos
    vias y exija el MISMO veredicto. Sin el, en una caja con PyYAML el camino de
    repuesto no se ejercita nunca y envejece sin que nadie lo vea — que es como
    dos copias de un mismo control acaban discrepando en silencio.
    """
    if os.environ.get("OLIVARES_CI_TIMEOUTS_NO_YAML") == "1":
        return None
    try:
        import yaml
    except Exception:
        return None
    filas = []
    for f in fs:
        try:
            with open(f, encoding="utf-8") as fh:
                d = yaml.safe_load(fh)
        except Exception as exc:
            no_puedo("%s no parsea: %s" % (os.path.basename(f), exc))
        if not isinstance(d, dict):
            continue
        for nombre, job in (d.get("jobs") or {}).items():
            if not isinstance(job, dict):
                continue
            techo = job.get("timeout-minutes")
            if techo is None:
                continue
            pasos = [s for s in (job.get("steps") or []) if isinstance(s, dict)]
            conguarda = [s for s in pasos if s.get("timeout-minutes")]
            suma = sum(s.get("timeout-minutes") or 0 for s in conguarda)
            filas.append((os.path.basename(f), nombre, int(techo), suma,
                          len(pasos), len(conguarda)))
    return filas


# --- Camino de repuesto -----------------------------------------------------
# La forma que sabe leer, y SOLO esa:
#     jobs:                        col 0
#       <job>:                     col 2
#         timeout-minutes: N       col 4
#         steps:                   col 4
#           - <clave>: ...         col 6 ("      - ", el guion ocupa 6-7)
#             timeout-minutes: N   col 8  <- clave del item, alineada tras el "- "
# Cualquier `timeout-minutes:` a otra columna, o un valor que no sea un entero
# literal, es una forma que no sabe leer -> NOPUEDO.
RE_JOBS = re.compile(r"^jobs:\s*(#.*)?$")
RE_TOP = re.compile(r"^[A-Za-z_\"']")
RE_JOB = re.compile(r"^  ([A-Za-z_][\w.\-]*):\s*(?:#.*)?$")
RE_TIMEOUT = re.compile(r"^( *)timeout-minutes:\s*(\S+)\s*(?:#.*)?$")
RE_STEPS = re.compile(r"^    steps:\s*(?:#.*)?$")
RE_ITEM = re.compile(r"^      - ")
RE_CLAVE_JOB = re.compile(r"^    [A-Za-z_][\w.\-]*:")
# ⛔ LA RED QUE CIERRA EL CONTRATO, y sin ella la promesa de arriba era FALSA (contraste Codex
# `sol max`, 2026-08-23, dos falsos verdes VERIFICADOS). El escaner reconocia las formas que sabia
# e IGNORABA el resto, que no es lo mismo que rehusar: una clave de paso escrita en la linea del
# guion —`      - timeout-minutes: 20`, YAML perfectamente valido— no casaba con RE_TIMEOUT, la
# suma salia 0, y el gate certificaba CLEAN sobre un job con techo 10 y un paso de 20. En vez de
# enumerar cada forma hostil, se invierte la carga: se cuentan TODAS las menciones del sujeto y se
# exige que cada una haya quedado ATRIBUIDA. La que sobre es, por definicion, una forma que no se
# ha sabido leer -> NOPUEDO.
#
# Barre la linea entera a proposito, comentario final incluido. Cuesta un NOPUEDO de mas si alguien
# escribe `timeout-minutes` en un comentario al final de una linea de codigo, y ese precio es el
# correcto: rehusar de mas es recuperable, un falso verde en un gate deny-closed no lo es. Medido
# sobre los 18 workflows de hoy: 51 menciones = 22 techos de job + 27 guardas de paso + 2 lineas
# de comentario puro (que se saltan antes), asi que la red no produce ni un rehuse falso.
RE_MENCION = re.compile(r"timeout-minutes")


def por_lectura_plana(fs):
    filas = []
    for f in fs:
        base = os.path.basename(f)
        try:
            with open(f, encoding="utf-8") as fh:
                lineas = fh.read().split("\n")
        except Exception as exc:
            no_puedo("%s no se puede leer: %s" % (base, exc))
        if any("\t" in ln for ln in lineas):
            no_puedo("%s tiene TABs: la lectura plana no los sabe leer" % base)

        en_jobs = False
        job = None
        techo = None
        en_steps = False
        pasos = 0
        conguarda = 0
        suma = 0
        menciones = 0
        atribuidas = 0

        def cerrar():
            if job is not None and techo is not None:
                filas.append((base, job, techo, suma, pasos, conguarda))

        for ln in lineas:
            if not ln.strip() or ln.lstrip().startswith("#"):
                continue
            if RE_MENCION.search(ln):
                menciones += 1
            if RE_JOBS.match(ln):
                en_jobs = True
                continue
            if en_jobs and RE_TOP.match(ln):
                # otra clave de nivel superior: se acaba el bloque jobs
                cerrar()
                job, techo, en_steps, pasos, conguarda, suma = None, None, False, 0, 0, 0
                en_jobs = False
                continue
            if not en_jobs:
                continue
            m = RE_JOB.match(ln)
            if m:
                cerrar()
                job, techo, en_steps, pasos, conguarda, suma = m.group(1), None, False, 0, 0, 0
                continue
            if job is None:
                continue
            if RE_STEPS.match(ln):
                en_steps = True
                continue
            if RE_CLAVE_JOB.match(ln) and not RE_STEPS.match(ln):
                en_steps = False
            if en_steps and RE_ITEM.match(ln):
                pasos += 1
            t = RE_TIMEOUT.match(ln)
            if t:
                col, valor = len(t.group(1)), t.group(2)
                if not valor.isdigit():
                    no_puedo("%s/%s: timeout-minutes no literal (%r)" % (base, job, valor))
                valor = int(valor)
                if col == 4:
                    techo = valor
                    atribuidas += 1
                elif col == 8 and en_steps:
                    conguarda += 1
                    suma += valor
                    atribuidas += 1
                else:
                    no_puedo("%s/%s: timeout-minutes a columna %d, forma desconocida"
                             % (base, job, col))
        cerrar()
        if menciones != atribuidas:
            no_puedo("%s: %d mencion(es) de timeout-minutes y solo %d atribuida(s) — hay una forma "
                     "que no se leer, y adivinarla seria un falso verde"
                     % (base, menciones, atribuidas))
    return filas


def main():
    fs = ficheros()
    if not fs:
        no_puedo("no hay workflows en %s" % DIRECTORIO)
    filas = por_yaml(fs)
    if filas is None:
        filas = por_lectura_plana(fs)
    for fila in filas:
        print("JOB\t%s\t%s\t%d\t%d\t%d\t%d" % fila)


main()
