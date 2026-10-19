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

The pre-push hook of that day ran that task in its fast lane, which ran for every
branch, so on a box without PyYAML the hook refused every push of every branch,
and there was no `pip` on that box, so it was not a dependency the hook could
resolve. That hook was rewritten on 2026-10-05 (8dfa44cb0) and no longer runs the
task. The gate now runs on every development pull request, in the `ci-timeouts`
job of the development pull-request workflow, which installs PyYAML first.

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
    (2026-10-19, #2635: merge-gate-check.yml grew a second list style, the
    compact one with step guards at indentation 6; the reader now accepts both
    and still refuses every other column.)
  * En ESTA caja no se puede contrastar contra PyYAML, porque PyYAML es justamente
    lo que missing_items. Por eso existe `OLIVARES_CI_TIMEOUTS_NO_YAML=1`: la bateria corre
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


def cannot_check(razon):
    print("NOPUEDO\t%s" % str(razon).replace("\n", " ")[:160])
    raise SystemExit(0)


def source_files():
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
            cannot_check("%s could not be parsed: %s" % (os.path.basename(f), exc))
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
#
# The step list has a second style, the compact one (#2635): the "- " sits at the column of
# `steps:` and the step keys at column 6. merge-gate-check.yml writes its jobs that way (and its
# `check` job in the indented style above). The column of the "- " is read from the first item
# after `steps:` (ESTILOS_LISTA, nothing else is accepted) and the step guard sits two columns to
# its right; a `timeout-minutes` anywhere else is still unattributed -> NOPUEDO.
ESTILOS_LISTA = (4, 6)
RE_JOBS = re.compile(r"^jobs:\s*(#.*)?$")
RE_TOP = re.compile(r"^\S")
RE_JOB = re.compile(r"^  ([A-Za-z_][\w.\-]*):\s*(?:#.*)?$")
RE_TIMEOUT = re.compile(r"^( *)timeout-minutes:\s*(\S+)\s*(?:#.*)?$")
RE_STEPS = re.compile(r"^    steps:\s*(?:#.*)?$")
RE_ITEM = re.compile(r"^( *)- ")
# Any column-4 line that is not a list item ends the steps: the job's next key, quoted or not. A
# pattern that only knew unquoted names let `    "strategy":` stay inside the steps, so a cap
# under it was summed as a step guard (30 against 45 where PyYAML reads 30 against 40).
RE_FIN_STEPS = re.compile(r"^    (?!- )\S")
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
# Three more shapes the reader cannot attribute, refused instead of guessed (dev#2637). A quoted,
# anchored or otherwise unusual job id did not match RE_JOB, so the PREVIOUS job kept reading and
# the new job's ceiling overwrote its own (a false CLEAN); an alias hides a step's guard; and a
# leading zero is octal in YAML 1.1, so `010` is 8 there and 10 here (the larger ceiling is the
# unsafe direction).
#   RE_COL2   a column-2 line inside `jobs:` that RE_JOB did not take.
#   RE_ANCLA  an anchor or alias where a YAML node starts: line start or after `- `, `key: `, a flow
#             `[` or `,`, with an optional `!tag` before it. The mention count below does NOT
#             cover this: the anchored node's own timeout is attributed once, and every alias to it
#             adds timeouts nobody counts (`steps: [*s, *s]`). `&&`, `*.txt` and `cp *foo` are not
#             node starts, and the lines of a `run: |` block (RE_BLOQUE) are script text, not YAML
#             (measured on the real tree: `*pattern)` case arms, `, *args` splats).
#   RE_SALTO  U+0085, U+2028 and U+2029 end a line for YAML but not for `split("\n")`, so a job line
#             hidden behind one inside a comment is invisible to this reader.
#   RE_BLOQUE a block scalar header: the indicator sits at the VALUE position (after the optional
#             `- ` dashes and `key: `), with only a comment after it. group(1) is the line start up to
#             the key, so the block belongs to the key's column and not to the dash's. The same
#             characters inside a trailing comment (`j: # see: |`) are not a header.
#   comilla_sin_cerrar  a quoted string that may stay open at the end of its line. The continuation
#             lines of such a scalar are string text, so a header-looking one (`b: |`) would open a
#             fake block and hide an alias on the closing line; no workflow of the tree has one. It
#             follows every reading of the line instead of matching where the scalar opens, because
#             an opener has too many spellings to list (behind a `!tag`, `"k":"a`, `{? "a`, `[`,
#             `,`, a flow collection as the key, `"a\` at the end of the line) and each list missed
#             one, and a classifier of "opener or text" fails on the quotes that are ambiguous from
#             the line alone (`a-"b: "c`). Those are both, see clase_comilla.
#   RE_ENTERO a plain decimal with no leading zero; `\d` would accept `²` and `٣`, `[0-9]` does not.
RE_COL2 = re.compile(r"^  \S")
RE_ANCLA = re.compile(r"(?:^\s*(?:-\s+)*|:\s+|[\[,]\s*)(?:![^\s]*\s+)?[&*][\w-]")
RE_BLOQUE = re.compile(r"^( *(?:- +)*)(?:[^\s#][^#]*?:\s+)?[|>][-+0-9]*\s*(?:#.*)?$")
RE_SALTO = re.compile("[\\x85\\u2028\\u2029]")
RE_ENTERO = re.compile(r"0|[1-9][0-9]*")


def clase_comilla(ln, i):
    """What a quote at ln[i] can be outside a quoted string: "abre", "texto" or "duda" (either).

    "abre": right after `key: `. A plain scalar cannot hold `: `, so the quote starts a string; it
    is the only place where the line alone settles it. "duda": at the line start or after `- ` /
    `? ` indicators (the line may continue a flow collection or a plain scalar, where a quote is
    text), right after `:`, `[`, `{`, `,` or `?`, or after a word holding
    `!` or `&` (a tag or an anchor). It is an opener in a flow collection or a JSON-like key and
    text in a plain scalar (`a:"b`, `a,'b`), and the line cannot say which. "texto": anywhere else,
    such as after a letter or digit (`run's`, `x="a"b`), where a quote is a character of a plain
    scalar. A `-` glued to a quote is text too: `echo a - "b`.
    """
    k = i
    while k and ln[k - 1] == " ":
        k -= 1
    antes = ln[:k]
    if antes.endswith(":") and k < i:
        return "abre"
    if not antes.strip(" -?") or antes[-1] in ":[{,?" or re.search("[!&]", antes.rsplit(" ", 1)[-1]):
        return "duda"
    return "texto"


def comilla_sin_cerrar(ln):
    """True when some reading of `ln` leaves a quoted string open at the end of the line.

    The line is read once per way it may be read: a "duda" quote is both an opener and text, and
    all the readings run side by side. Inside a string the next quote of its kind closes it (a
    backslash escapes in double quotes, so a trailing `\\` leaves the line open). Right after a
    single-quoted string closes, another `'` is also an escaped quote that continues it (`'a''`).
    A `#` after a space ends the readings that are outside a string. Refusing when any reading is
    open is the safe side: reading an opener as text is what would let a continuation line open a
    fake block.
    """
    lecturas = {""}  # "" outside, '"' or "'" inside a string, '"\\' after a backslash, "'~" just closed
    for i, c in enumerate(ln):
        sig = set()
        for m in lecturas:
            if m in ("", "'~"):
                if m == "'~" and c == "'":
                    sig.add("'")
                if c == "#" and (i == 0 or ln[i - 1] == " "):
                    continue
                clase = clase_comilla(ln, i) if c in "\"'" else "texto"
                if clase != "abre":
                    sig.add("")
                if clase != "texto":
                    sig.add(c)
            elif m == '"\\':
                sig.add('"')
            elif c == m:
                sig.add("'~" if c == "'" else "")
            elif c == "\\" and m == '"':
                sig.add('"\\')
            else:
                sig.add(m)
        if not sig:
            return False
        lecturas = sig
    return any(m in ('"', "'", '"\\') for m in lecturas)


def por_lectura_plana(fs):
    filas = []
    for f in fs:
        base = os.path.basename(f)
        try:
            with open(f, encoding="utf-8") as fh:
                lineas = fh.read().split("\n")
        except Exception as exc:
            cannot_check("%s could not be read: %s" % (base, exc))
        if any("\t" in ln for ln in lineas):
            cannot_check("%s contains TABs: the plain-text reader cannot handle them" % base)
        if any(RE_SALTO.search(ln) for ln in lineas):
            cannot_check("%s contains Unicode line breaks: the plain-text reader cannot handle them" % base)

        en_jobs = False
        job = None
        techo = None
        en_steps = False
        guion = None  # column of the "- " of the current steps list (ESTILOS_LISTA)
        bloque = None  # column of the `key: |` key (after any `- `) whose block scalar is being read
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
            sangria = len(ln) - len(ln.lstrip(" "))
            if bloque is None or sangria <= bloque:
                b = RE_BLOQUE.match(ln)
                bloque = len(b.group(1)) if b else None
                if RE_ANCLA.search(ln):
                    cannot_check("%s: anchor or alias in jobs (%r), unsupported format" % (base, ln.strip()))
                if comilla_sin_cerrar(ln):
                    cannot_check("%s: multi-line quoted scalar in jobs (%r), unsupported format" % (base, ln.strip()))
            m = RE_JOB.match(ln)
            if m:
                cerrar()
                job, techo, en_steps, pasos, conguarda, suma = m.group(1), None, False, 0, 0, 0
                continue
            if RE_COL2.match(ln):
                cannot_check("%s: unsupported job line (%r), only a plain job id is read" % (base, ln.strip()))
            if job is None:
                continue
            if RE_STEPS.match(ln):
                en_steps = True
                guion = None
                continue
            if RE_FIN_STEPS.match(ln):
                en_steps = False
            item = RE_ITEM.match(ln) if en_steps else None
            if item:
                col = len(item.group(1))
                if guion is None:
                    if col not in ESTILOS_LISTA:
                        cannot_check("%s/%s: steps list starts at column %d, unsupported format"
                                     % (base, job, col))
                    guion = col
                if col == guion:
                    pasos += 1
            t = RE_TIMEOUT.match(ln)
            if t:
                col, valor = len(t.group(1)), t.group(2)
                if not RE_ENTERO.fullmatch(valor):
                    cannot_check("%s/%s: timeout-minutes is not a literal (%r)" % (base, job, valor))
                valor = int(valor)
                if col == 4:
                    techo = valor
                    atribuidas += 1
                elif en_steps and guion is not None and col == guion + 2:
                    conguarda += 1
                    suma += valor
                    atribuidas += 1
                else:
                    cannot_check("%s/%s: timeout-minutes at column %d, unsupported format"
                             % (base, job, col))
        cerrar()
        if menciones != atribuidas:
            cannot_check("%s: %d mention(s) of timeout-minutes and only %d attributed — an unsupported format "
                     "was found; guessing would produce a false pass"
                     % (base, menciones, atribuidas))
    return filas


def main():
    fs = source_files()
    if not fs:
        cannot_check("no workflows in %s" % DIRECTORIO)
    filas = por_yaml(fs)
    if filas is None:
        filas = por_lectura_plana(fs)
    for fila in filas:
        print("JOB\t%s\t%s\t%d\t%d\t%d\t%d" % fila)


main()
