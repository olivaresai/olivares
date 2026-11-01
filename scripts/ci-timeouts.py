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


def valor_visible(valor, limite=80):
    """The value shown in a refusal, bounded before any `repr`.

    `cannot_check` truncates the message, but the formatting runs first: a multi-megabyte string
    or bytes value was rendered in full only to be cut. Cut first, then quote, so the refusal
    stays a bounded amount of work whatever the value holds (the regression review).
    """
    if isinstance(valor, (str, bytes)) and len(valor) > limite:
        return "%r... (%d chars)" % (valor[:limite], len(valor))
    return repr(valor)


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
        # A document that is not a mapping (a sequence, or a bare scalar) cannot hold jobs. It
        # was skipped silently while the plain reader refuses the same file; skipping it let a
        # readable sibling workflow turn the missing rows into CLEAN (the regression review).
        if not isinstance(d, dict):
            cannot_check("%s: document is not a mapping" % os.path.basename(f))
        # `jobs:` may parse as a string or a sequence (`jobs: |`, `jobs: [a]`). Walking it raised
        # AttributeError, the helper exited 1, and the gate reported a crash instead of the
        # documented NOPUEDO (the regression review).
        trabajos = d.get("jobs")
        if trabajos is not None and not isinstance(trabajos, dict):
            cannot_check("%s: jobs is not a mapping" % os.path.basename(f))
        for nombre, job in (trabajos or {}).items():
            # A scalar job holds no cap and no steps; skipping it hid a shape the plain reader
            # refuses, so the two paths disagreed on the same file (the regression review).
            if not isinstance(job, dict):
                cannot_check("%s: job %s is not a mapping"
                             % (os.path.basename(f), valor_visible(nombre)))
            # Presence, not truthiness: a declared null cap (`.get()` returned None) used to
            # drop the whole job from the rows (the regression review).
            if "timeout-minutes" not in job:
                continue
            techo = job["timeout-minutes"]
            # `int(techo)` raised on a string and truncated a float or a bool; GitHub accepts
            # only an integer, so any other type is a value the reader cannot attribute. A
            # traceback is not the documented NOPUEDO (the regression review).
            if type(techo) is not int:
                cannot_check("%s: timeout-minutes is not an integer (%s)"
                             % (os.path.basename(f), valor_visible(techo)))
            # GitHub reads a positive number of minutes. Zero or a negative cap was printed as a
            # row the plain reader refuses (the regression review).
            if techo < 1:
                cannot_check("%s: timeout-minutes is not a positive integer (%s)"
                             % (os.path.basename(f), valor_visible(techo)))
            pasos_val = job.get("steps")
            # `for s in 5` raised TypeError; a string was walked per character and a mapping per
            # key, so the job printed with zero steps and hid its guards.
            if pasos_val is not None and not isinstance(pasos_val, list):
                cannot_check("%s: steps is not a list (%s)"
                             % (os.path.basename(f), valor_visible(pasos_val)))
            pasos = pasos_val or []
            # A scalar step carries no guard, but it is still an item of the list: filtering it
            # out printed a different step count than the plain reader (the regression review).
            conguarda = [s for s in pasos
                         if isinstance(s, dict) and "timeout-minutes" in s]
            for s in conguarda:
                guarda = s["timeout-minutes"]
                if type(guarda) is not int:
                    cannot_check("%s: a step timeout-minutes is not an integer (%s)"
                                 % (os.path.basename(f), valor_visible(guarda)))
                if guarda < 1:
                    cannot_check("%s: a step timeout-minutes is not a positive integer (%s)"
                                 % (os.path.basename(f), valor_visible(guarda)))
            suma = sum(s["timeout-minutes"] for s in conguarda)
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
# A structural key's colon must be followed by whitespace: `jobs:#c` is a plain scalar (`#` needs
# a space before it), NBSP is not a separator in YAML, and `timeout-minutes:30` is not a key the
# reader can attribute. `\s*` accepted all three and turned an unreadable file into rows
# (the regression review). CRLF needs no pattern support: text mode's universal newlines turn `\r\n`
# into `\n` before any match, so a `\r?` here would be dead.
RE_JOBS = re.compile(r"^jobs:(?: +(#.*)?)?$")
# YAML also reads `"jobs":` and `jobs :` as the jobs key, and the reader knows neither: it ignored
# the key, and a valid sibling file turned the missing rows into CLEAN. PyYAML walks the quoted
# key and refuses a non-mapping value, so refusing the spelling is the safe direction (the regression
# review). Only the top-level jobs key is spelled out here; every other quoted key stays unread
# as before.
RE_JOBS_CLAVE = re.compile(r"""^(?:["']jobs["'][ \t]*|jobs[ \t]+):""")
RE_TOP = re.compile(r"^\S")
# A column-0 line that is not a mapping key is block structure this reader cannot attribute: a
# sequence or flow document (`- a`, `{jobs: ...}`), a malformed marker (`----`), or junk. PyYAML
# rejects those streams, and ignoring the line let a later `jobs:` block be read from a file it
# rejects (the regression review). `---foo: bar` and `...foo: bar` are mapping keys and stay readable.
RE_TOP_CLAVE = re.compile(r"^[^\s#][^#]*?:(?:[ \t]|$)")
# At column 0, a sequence entry or an explicit key is block structure even when the line holds a
# colon: `- a: b` is a sequence of mappings and `? x: y` an explicit key. A flow node has no
# plain mapping key either. `---foo: bar` stays readable: a dash is not a sequence marker unless
# whitespace (or the end of the line) follows it.
RE_TOP_ESTRUCTURA = re.compile(r"^(?:-(?:[ \t]|$)|\?(?:[ \t]|$)|[\[{])")
RE_JOB = re.compile(r"^  ([A-Za-z_][\w.\-]*):(?: +(#.*)?)?$")
RE_TIMEOUT = re.compile(r"^( *)timeout-minutes: +(\S+) *(?:#.*)?$")
RE_STEPS = re.compile(r"^    steps:(?: +(#.*)?)?$")
RE_ITEM = re.compile(r"^( *)-(?: |$)")
# Any column-4 line that is not a list item ends the steps: the job's next key, quoted or not. A
# pattern that only knew unquoted names let `    "strategy":` stay inside the steps, so a cap
# under it was summed as a step guard (30 against 45 where PyYAML reads 30 against 40).
RE_FIN_STEPS = re.compile(r"^    (?!-(?: |$))\S")
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
# Three more shapes the reader cannot attribute, refused instead of guessed. A quoted,
# anchored or otherwise unusual job id did not match RE_JOB, so the PREVIOUS job kept reading and
# the new job's ceiling overwrote its own (a false CLEAN); an alias hides a step's guard; and a
# leading zero is octal in YAML 1.1, so `010` is 8 there and 10 here (the larger ceiling is the
# unsafe direction).
#   RE_COL2   a column-2 line inside `jobs:` that RE_JOB did not take.
#   RE_COL1   a column-1 line inside `jobs:`: the mapping is indented by one space, so the job
#             ids are at column 1 and what sits at column 2 is nested in them, a block scalar body or a
#             nested key, never a job. Reading it as a job printed a row PyYAML does not have, and
#             the body of a `z: |` job replaced the ceiling of the one before it.
#   RE_ANCLA  an anchor or alias where a YAML node starts: line start or after `- `, `key: `, a flow
#             `[` or `,`, with an optional `!tag` before it. The mention count below does NOT
#             cover this: the anchored node's own timeout is attributed once, and every alias to it
#             adds timeouts nobody counts (`steps: [*s, *s]`). `&&`, `*.txt` and `cp *foo` are not
#             node starts, and the lines of a `run: |` block (RE_BLOQUE) are script text, not YAML
#             (measured on the real tree: `*pattern)` case arms, `, *args` splats).
#   RE_SALTO  U+0085, U+2028 and U+2029 end a line for YAML but not for `split("\n")`, so a job line
#             hidden behind one inside a comment is invisible to this reader.
#   RE_BLOQUE a block scalar header: the indicator sits at the VALUE position (after the optional
#             `- ` dashes, node properties such as `!!str`, and `key: `), with only a comment after
#             it. group(1) is the line start, group(2) the dashes, group(3) the node properties and
#             group(4) the key. The block belongs to the key's column when there is one, and to the
#             last dash's column when there is not, so the body of a keyless `- |` at the dash
#             column + 2 is still body and not structure. An indicator alone on its line has no
#             parent on that line, so the parent's column cannot be derived: refused, never
#             guessed. An explicit indentation indicator (`|2`, `|2+`, `|-2`) is refused too: it
#             derives the body's indent from the parent, while this reader only knows "more
#             indented than the key", so it could read body lines as structure. The same
#             characters inside a trailing comment (`j: # see: |`) are not a header.
#   comilla_sin_cerrar  a quoted string that may stay open at the end of its line. The continuation
#             lines of such a scalar are string text, so a header-looking one (`b: |`) would open a
#             fake block and hide an alias on the closing line; no workflow of the tree has one. It
#             follows every reading of the line instead of matching where the scalar opens, because
#             an opener has too many spellings to list (behind a `!tag`, `"k":"a`, `{? "a`, `[`,
#             `,`, a flow collection as the key, `"a\` at the end of the line) and each list missed
#             one, and a classifier of "opener or text" fails on the quotes that are ambiguous from
#             the line alone (`a-"b: "c`). Those are both, see clase_comilla.
#   RE_ENTERO a positive plain decimal with no leading zero; `\d` would accept `²` and `٣`,
#             `[0-9]` does not. Zero minutes is not a value GitHub reads, and it used to print a
#             row PyYAML's path now refuses (the regression review).
RE_COL1 = re.compile(r"^ \S")
RE_COL2 = re.compile(r"^  \S")
RE_ANCLA = re.compile(r"(?:^\s*(?:-\s+)*|:\s+|[\[,]\s*)(?:![^\s]*\s+)?[&*][\w-]")
RE_BLOQUE = re.compile(r"^( *)((?:- +)*)((?:[!&][^\s#]*\s+)*)((?:[^\s#][^#]*?:\s+)?)[|>](?:[+-])?(?:[ \t]+#.*|[ \t]*)$")
# Any indicator at the value position, whatever its suffix. A suffix the strict form rejects
# (`| junk`, `|0`, `|2`, `|#c`, `>good`, and `a#b: |`, whose key's `#` is a plain scalar char, not
# a comment) is invalid YAML or not modelled by this reader, but it used to ignore the line, read
# the body as structure and print a row: an approval PyYAML does not give (the regression review).
RE_BLOQUE_LAXO = re.compile(r"^ *(?:- +)*(?:[!&][^\s#]*\s+)*(?:[^\s](?:(?![ \t]#).)*?:\s+)?[|>]")
RE_SALTO = re.compile("[\\x85\\u2028\\u2029]")
# A document marker or directive is structure this reader cannot follow: `safe_load` reads one
# document and raises on a second, and a directive changes how the document is read. Malformed
# spellings (`---#c`, `%YAML1.2`, an indented ` ---`) are refused too: PyYAML raises on them, and
# skipping the line printed rows from a stream it rejects. A marker needs whitespace, a comment or
# the end of the line after it, so `---foo: bar` stays a mapping key and is read (the regression review).
RE_DOC = re.compile(r"^\s*(?:---|\.\.\.)(?=[ \t#]|$)|^\s*%")
RE_ENTERO = re.compile(r"[1-9][0-9]*")
# A line longer than this is refused before any pattern runs: RE_ANCLA backtracks, and a line of
# 100 KB held the reader for 48.7 s. The longest line of the 37 workflows has 796
# characters. The body of a block scalar is script text, so its lines are not read and not capped.
MAX_LINEA = 4096
# A double-quoted token whose escapes spell a name this reader reads (timeout-minutes, steps, jobs)
# is refused wherever it stands. The mention count sees only the literal, so an escaped key is
# invisible to it, and the reader cannot tell a key from a value by position: a `?` line, a tag, a
# flow map or a key split over lines all hide it. Every token that starts at a quote is decoded, so
# an inner quote cannot shadow one. Ordinary text (run: scripts) decodes to itself and stays readable.
NOMBRES_CLAVE = ("timeout-minutes", "steps", "jobs")
RE_CITA = re.compile(r'(?="((?:[^"\\]|\\.){0,256})")')
# The only escapes that can spell an ASCII letter of a key name. Every other YAML escape (\n, \t,
# \/, \", \\ ...) stays as text: none of them yields a letter, a digit or a dash of the names.
RE_HEX = re.compile(r'\\(x[0-9A-Fa-f]{2}|u[0-9A-Fa-f]{4}|U[0-9A-Fa-f]{8})')


def clave_escapada(ln):
    """True when ln holds a quoted token whose escapes spell a key name this reader reads."""
    for m in RE_CITA.finditer(ln):
        if "\\" in m.group(1) and RE_HEX.sub(_letra, m.group(1)) in NOMBRES_CLAVE:
            return True
    return False


def _letra(m):
    codigo = int(m.group(1)[1:], 16)
    return chr(codigo) if codigo < 128 else "?"  # a non-ASCII spelling can never match a name


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


def nodo_inicio_flujo(ln, i, pila, cierre=False):
    """True when the `[` or `{` at ln[i] opens a YAML node, not text of a plain scalar.

    A flow indicator opens a node only at a node start: the line start, after a `- ` or
    `? ` indicator, after `key: `, after a tag or anchor, or after `[`, `{` or `,` inside a flow
    collection. Anywhere else it is a character of a plain scalar (`echo [x]`). A `[`, `{` or `,`
    outside a flow collection is such a character too, so `[ {` in a shell line is text. Inside a
    flow collection the colon may touch the indicator (JSON style `{x:[a]}`); at block level it
    may not, because `x:[a]` is a plain scalar. With `cierre`, the caller is a closer or a comma:
    a tag or anchor cannot start one, and the loose tag test would read `!cancelled() }` in an
    `if:` line as a node start. A line start is not a node start for one either: a line beginning
    `}}/postgres?sslmode=disable` is how a multi-line `${{ ... }}` expression continues, which the
    reader already treats as text (the regression review).
    """
    k = i
    while k and ln[k - 1] == " ":
        k -= 1
    antes = ln[:k]
    if not antes.strip(" -?"):
        # An opener at the line start opens a value written on the line below its key. For a
        # closer or a comma only a doubled `}}` is excused: that is how a multi-line `${{ ... }}`
        # expression continues in the real tree (merge-gate-check.yml). A lone `}` has nothing to
        # close in a file this reader can read, because a collection left open on its line was
        # already refused. A line starting with `]` or `,` is refused too: it can only be a
        # plain-scalar continuation, which this reader does not attribute — over-refusal, the
        # allowed direction of the one-way rule (the regression review).
        return not cierre or ln[i:i + 2] != "}}"
    if antes.endswith(":") and (k < i or pila):
        return True
    if antes[-1] in "[{,":
        return bool(pila)
    if antes[-1] == "?" and antes.rsplit(" ", 1)[-1] == "?":
        # An explicit-key indicator stands alone (the line start or `key: ?` case is handled
        # above). A question mark glued to a word (`really?`) is plain-scalar text, and a flow
        # opener after it is text too (the regression review).
        return True
    if cierre:
        return False
    return bool(re.search("[!&]", antes.rsplit(" ", 1)[-1]))


def flujo_sin_cerrar(ln):
    """True when a flow collection opens at a node start and is still open at the end of the line.

    A flow collection may span lines, and a `#` after a space hides its closing bracket, so read
    line by line `env: { # }` left the reader in the job and a cap inside the collection replaced
    the job's own (1000 against 10, the regression); a header-looking line inside one opened a fake block
    that hid an escaped key. A collection that does not close on its line is a shape the reader
    cannot attribute, so it refuses it the way `comilla_sin_cerrar` refuses an open quote. Brackets
    inside a quoted string are text, and a `#` after a space ends the line. A closer or a comma
    where a node starts is invalid YAML (`env: ]`, `env: ,`) and so is a second closer after a
    collection just closed (`env: [x]]`); inside plain-scalar text (`echo a, b ]`) they are text.
    """
    pila = []
    roto = False
    recien = False  # the previous indicator closed a collection: nothing here is open to close
    comilla = None
    i, n = 0, len(ln)
    while i < n:
        c = ln[i]
        if comilla is not None:
            if comilla == '"':
                if c == "\\":
                    i += 2
                    continue
                if c == '"':
                    comilla = None
            elif c == "'":
                if i + 1 < n and ln[i + 1] == "'":
                    i += 2
                    continue
                comilla = None
            i += 1
            continue
        if c == "#" and (i == 0 or ln[i - 1] in " \t"):
            break
        if c in "\"'":
            if clase_comilla(ln, i) != "texto":
                comilla = c
            elif recien:
                # A quote where a separator would be is text after a collection that already
                # closed: the value cannot continue with a scalar (the regression review).
                roto = True
            i += 1
            continue
        if c in "[{":
            if nodo_inicio_flujo(ln, i, pila):
                pila.append(c)
            elif recien:
                # `[x] [y]`: the value ended at the first closer.
                roto = True
            recien = False
        elif c in "]}":
            # Only the matching closer closes the collection. A mismatched one leaves it open and
            # marks the line unsupported, so `env: {]` and `env: {a] }` are both refused: PyYAML
            # rejects them, and the later `}` must not turn the first one into a read. A closer
            # with nothing open is invalid where a node starts, or right after a collection that
            # just closed; inside plain-scalar text it is a character.
            if pila and ((c == "]") == (pila[-1] == "[")):
                pila.pop()
                recien = True
            elif pila or nodo_inicio_flujo(ln, i, pila, cierre=True) or recien:
                roto = True
        elif c == ",":
            # A comma separates entries inside a collection. At a node start outside one it is
            # invalid YAML (`env: ,`); inside a plain scalar (`echo a, b`) it is text.
            if pila:
                recien = False
            elif nodo_inicio_flujo(ln, i, pila, cierre=True) or recien:
                roto = True
        elif c not in " \t":
            # Text where a separator or the end of the line would be: the collection closed and
            # the value cannot continue with a plain scalar. PyYAML rejects the line; reading it
            # printed a row (the regression review).
            if recien:
                roto = True
            recien = False
        i += 1
    return bool(pila) or roto


def colones_planos(ln):
    """Count the `: ` separators YAML would see in a block-context line.

    A plain scalar cannot hold an unquoted `: `: PyYAML rejects `env: FOO: bar` and
    `runs-on: ubuntu: latest` with "mapping values are not allowed here", while the reader
    ignored the line and printed a row (the regression review). Only a colon followed by a space or
    the end of the line separates here; `https://` is one plain scalar. A quote hides a colon
    only when it certainly opens a string (`key: "a: b"`): a quote inside a plain scalar is text
    for YAML, so `run: echo "a: b"` is refused exactly as PyYAML refuses it. A colon inside a
    flow collection or after a `#` comment is not a block separator.
    """
    pila = []  # flow collections opened at a node start, as in `flujo_sin_cerrar`
    comilla = None
    colones = 0
    i, n = 0, len(ln)
    while i < n:
        c = ln[i]
        if comilla is not None:
            if comilla == '"':
                if c == "\\":
                    i += 2
                    continue
                if c == '"':
                    comilla = None
            elif c == "'":
                if i + 1 < n and ln[i + 1] == "'":
                    i += 2
                    continue
                comilla = None
            i += 1
            continue
        if c == "#" and (i == 0 or ln[i - 1] in " \t"):
            break
        if c in "\"'":
            if clase_comilla(ln, i) == "abre":
                comilla = c
        elif c in "[{":
            if nodo_inicio_flujo(ln, i, pila):
                pila.append(c)
        elif c in "]}":
            if pila and ((c == "]") == (pila[-1] == "[")):
                pila.pop()
        elif c == ":" and not pila and (i + 1 == n or ln[i + 1] in " \t"):
            colones += 1
        i += 1
    return colones


def por_lectura_plana(fs):
    filas = []
    for f in fs:
        base = os.path.basename(f)
        try:
            # `utf-8-sig` strips a BOM at the start of the stream, which YAML allows and PyYAML
            # ignores. With plain `utf-8` the first line kept it (`jobs:` did not match) and the
            # file printed no rows although PyYAML reads it (the regression review).
            with open(f, encoding="utf-8-sig") as fh:
                lineas = fh.read().split("\n")
        except Exception as exc:
            cannot_check("%s could not be read: %s" % (base, exc))
        if any("\t" in ln for ln in lineas):
            cannot_check("%s contains TABs: the plain-text reader cannot handle them" % base)
        if any(RE_SALTO.search(ln) for ln in lineas):
            cannot_check("%s contains Unicode line breaks: the plain-text reader cannot handle them" % base)

        vistas = False  # the file has at least one line that is not blank or a comment
        en_jobs = False
        job = None
        techo = None
        en_steps = False
        guion = None  # column of the "- " of the current steps list (ESTILOS_LISTA)
        bloque = None  # column of the `key: |` key (after any `- `) whose block scalar is being read
        pasos = 0
        conguarda = 0
        suma = 0
        visto_mas = False  # the current job has at least one line at its mapping's child level
        menciones = 0
        atribuidas = 0

        def cerrar():
            if job is not None and not visto_mas:
                # A job id with no lines under it is a null value, not a job without a cap: the
                # PyYAML path refuses it as a non-mapping. Printing only the sibling jobs let a
                # readable tree turn the missing one into CLEAN (the regression review).
                cannot_check("%s: job %r is not a mapping" % (base, job))
            if job is not None and techo is not None:
                filas.append((base, job, techo, suma, pasos, conguarda))

        for ln in lineas:
            if not ln.strip() or ln.lstrip().startswith("#"):
                continue
            vistas = True
            sangria = len(ln) - len(ln.lstrip(" "))
            # The body of a block scalar is script text, not YAML: a `timeout-minutes` written
            # there is neither a ceiling nor a guard. Inside jobs it is still counted as a
            # mention, so the attribution check below refuses it instead of letting it pass
            # unread; outside jobs the gate does not read it at all. The block is
            # tracked everywhere, because a top-level `env: X: |` body must not be read either.
            if bloque is not None and sangria > bloque:
                if en_jobs and RE_MENCION.search(ln):
                    menciones += 1
                continue
            if en_jobs and job is not None and sangria > 2:
                visto_mas = True
            if len(ln) > MAX_LINEA:
                cannot_check("%s: %d characters in a line, over the %d the plain reader checks"
                             % (base, len(ln), MAX_LINEA))
            # Checked here, after the body skip, so a marker or directive inside a block scalar is
            # text: only structural lines are refused (the regression review).
            if RE_DOC.match(ln):
                cannot_check("%s: document marker or directive, unsupported format" % base)
            # A `jobs:` with a value is not the bare mapping the reader knows: `jobs: |` is a
            # scalar whose body may hide the real jobs, `jobs: {}` is inline. The body tracker
            # used to skip the scalar and print nothing, so a readable sibling file turned the
            # missing rows into CLEAN; refuse the header itself (the regression review).
            if ln.startswith("jobs:") and not RE_JOBS.match(ln):
                cannot_check("%s: jobs with a value (%r), unsupported format" % (base, ln.strip()))
            if RE_JOBS_CLAVE.match(ln):
                cannot_check("%s: jobs written with a quoted key or a space before the colon, "
                             "unsupported format" % base)
            # A column-0 line that is not a plain key ends the document this reader knows. Ignoring
            # it kept the reader inside the previous job, so an explicit `? env` key's cap replaced
            # the job's own (the regression review). Sequence/flow documents, malformed markers and junk
            # are refused here too; PyYAML rejects every one of those streams.
            if RE_TOP.match(ln) and (RE_TOP_ESTRUCTURA.match(ln) or not RE_TOP_CLAVE.match(ln)):
                cannot_check("%s: unsupported top-level line (%r), only mapping keys are read"
                             % (base, ln.strip()))
            b = RE_BLOQUE.match(ln)
            if b is None:
                bloque = None
                if RE_BLOQUE_LAXO.match(ln):
                    cannot_check("%s: block scalar indicator with an unsupported suffix (%r)"
                                 % (base, ln.strip()))
            elif b.group(4):
                # A plain key cannot hold `: `: PyYAML rejects `if: echo a: |`, but the regex took
                # the whole text as the key and skipped the following line, which could hide a
                # cap. A quoted key holding `: ` is refused too; the real tree has none
                # (the regression review).
                clave = re.sub(r":\s+$", "", b.group(4))
                if ": " in clave:
                    cannot_check("%s: block scalar key with an unquoted colon (%r), unsupported format"
                                 % (base, ln.strip()))
                # The key's column: its body is indented further than the key.
                bloque = len(b.group(1)) + len(b.group(2)) + len(b.group(3))
            elif b.group(2):
                # No key: the scalar is the dash's node, so its body starts past the dash.
                bloque = len(b.group(1)) + b.group(2).rindex("-")
            else:
                # The indicator stands alone: its parent is on the line above, so this line does
                # not say where its body may end. Refuse rather than swallow the rest of the job.
                cannot_check("%s: block scalar indicator on its own line (%r), unsupported format"
                             % (base, ln.strip()))
            # A value cannot hold a second unquoted `: `. PyYAML refuses the line ("mapping
            # values are not allowed here"); the reader ignored it and printed a row (the regression
            # review). Checked after the block header so its key keeps its own, more precise
            # refusal, and after the body skip because script text is not YAML.
            if colones_planos(ln) > 1:
                cannot_check("%s: unquoted colon in a value (%r), unsupported format"
                             % (base, ln.strip()))
            # An open quote or flow collection can hide a `jobs:` line below it, inside the block
            # or above it, and PyYAML rejects the file either way; both are refused everywhere.
            if comilla_sin_cerrar(ln):
                cannot_check("%s: multi-line quoted scalar (%r), unsupported format" % (base, ln.strip()))
            if flujo_sin_cerrar(ln):
                cannot_check("%s: multi-line flow collection (%r), unsupported format" % (base, ln.strip()))
            if RE_MENCION.search(ln):
                menciones += 1
            if clave_escapada(ln):
                cannot_check("%s: quoted key with a backslash escape (%r), unsupported format" % (base, ln.strip()))
            if RE_JOBS.match(ln):
                en_jobs = True
                continue
            if en_jobs and RE_TOP.match(ln):
                # otra clave de nivel superior: se acaba el bloque jobs
                cerrar()
                job, techo, en_steps, pasos, conguarda, suma = None, None, False, 0, 0, 0
                visto_mas = False
                en_jobs = False
                continue
            if not en_jobs:
                continue
            if RE_ANCLA.search(ln):
                cannot_check("%s: anchor or alias in jobs (%r), unsupported format" % (base, ln.strip()))
            m = RE_JOB.match(ln)
            if m:
                cerrar()
                job, techo, en_steps, pasos, conguarda, suma = m.group(1), None, False, 0, 0, 0
                visto_mas = False
                continue
            if RE_COL1.match(ln):
                cannot_check("%s: jobs indented by one space (%r), only job ids at column 2 are read" % (base, ln.strip()))
            if RE_COL2.match(ln):
                cannot_check("%s: unsupported job line (%r), only a plain job id is read" % (base, ln.strip()))
            if job is None:
                continue
            # A `steps:` that is not the bare key is not the list the reader knows. The line used
            # to be ignored as a job key, so the job printed with zero steps and no guards, and a
            # zero-space `steps:#c` was read as the list itself (the regression review).
            if ln.startswith("    steps:") and not RE_STEPS.match(ln):
                cannot_check("%s/%s: steps with a value (%r), unsupported format"
                             % (base, job, ln.strip()))
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
        if not vistas:
            # `safe_load` of an empty or comment-only stream is None, not a mapping, and the
            # PyYAML path refuses it. The plain reader printed no rows, so a readable sibling
            # turned the silence into CLEAN (the regression review).
            cannot_check("%s: document is not a mapping" % base)
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
