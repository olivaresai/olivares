#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-cli-render.sh — how many output paths in `cmd/olivares` still build a table, a
# key/value block, a status marker, a transport envelope or a next-step line BY HAND,
# instead of asking the terminal renderer for one.
#
# WHY A GATE AND NOT A CLEAN-UP. The clean-up is finite and the habit is not. Measured on
# 2026-09-18 over the command tree: 51 hand-built column blocks in 29 files, 7 hand-padded
# column formats, 1 hand-drawn glyph marker, 11 transport envelopes reaching the operator
# and 17 next-step lines each written its own way. Every one of them was somebody doing the
# reasonable local thing. The terminal is inconsistent because nothing counts.
#
# THE RATCHET, and why it bites in BOTH directions. `docs/cli-render-baseline.txt` holds one
# row per file that still hand-formats, with the count and the reason it is still there. A
# file over its row is a new hand-formatted path and fails. A file UNDER its row also fails,
# asking for the row to be lowered in the same commit: a ratchet that lets a number regain
# slack in silence is a counter, not a ratchet. A file with no row at all fails on its first
# hand-formatted path — which is the case this gate exists for.
#
# WHAT IT COUNTS, one kind per rule, each decidable by reading one line:
#
#   table     a column block someone aligned by hand: a *text/tabwriter.Writer constructed
#             outside the renderer, or obtained from any function whose SIGNATURE hands one
#             out. The renderer's Table and Fields primitives are the same shape with a
#             header, a record fallback and no truncation.
#
#             THE FACTORY HALF IS NOT AN EXTRA, IT IS THE DOMINANT SPELLING, and leaving it
#             out is what was measured on 2026-09-18: this tree builds
#             its tables through `newTabWriter(out)` (`cmd/olivares/cmd_compliance.go`) at
#             62 call sites in 12 files, of which the stdlib rule counted ONE. A new table
#             written the way this codebase writes them left the gate green — the habit the
#             gate exists to stop had an unguarded door.
#
#             THIS ONE RULE IS DECIDED BY A PARSER (`scripts/cli-render-tabwriter`, go/ast)
#             AND THE OTHER FOUR BY A LINE OF TEXT, and the difference is not taste. The
#             other four are LITERAL SPELLINGS in the printed line and a line is the whole
#             evidence. `table` is the only rule whose subject is a TYPE, and a type has to
#             be RESOLVED: through whatever local name the file gave the package, and
#             through whatever function hands one out. On 2026-09-19 three compiling,
#             ordinary-Go ways past the regex form were built — `import tw
#             "text/tabwriter"` (the identifier `tabwriter` never appears, so renaming the
#             PACKAGE was enough, which falsified this header's old "a signature cannot go
#             stale"); a factory held in a variable and called through it; and a factory in
#             a sibling module of this workspace. Each of the three is a resolution
#             question, and none of them is a grep.
#
#             THE LIMITS THAT REMAIN, written down here because the previous version of
#             this header claimed a limit it never stated:
#               · a factory in a module OUTSIDE this workspace — a third-party dependency
#                 in the module cache — is not read; its import resolves to no directory.
#               · a *tabwriter.Writer reached through an INTERFACE, or through a func-typed
#                 struct field assigned in another package: at the call site the signature
#                 names the interface, not the writer.
#               · a local variable that SHADOWS a factory name is counted as the factory.
#                 Counting one line too many is the direction this gate errs in on purpose.
#               · two hand-alignment shapes fall outside all five kinds and are pre-existing
#                 (r1 A8, r2 F8): a `strings.Repeat("-", n)` rule line, and columns spaced
#                 by hand with no `%-Ns` verb. Neither is charged by this gate today.
#   column    a `%-<n>s`-style padded format. Hand alignment that ignores SGR width and that
#             cuts nothing only by luck.
#   status    a printf-built `[ok]`/`[fail]`/`[warn]`/`[--]` token or a ✓/✗ glyph. The glyph
#             is a replacement character on a host without the font; the bracket token is the
#             renderer's StatusLine.
#   envelope  a transport status that IS the operator's sentence: `failed: HTTP` leading it,
#             or `HTTP <code>: %s` pasting the raw body straight after the status. In the
#             COMMAND layer only — packages under `internal/` return typed refusals that the
#             command layer shapes, so an envelope there is data and not a sentence.
#
#             The rule is deliberately narrow, and each thing it does NOT count was measured
#             on a real line of this tree. `the engine rejected this request: <what to do>
#             (HTTP 422 invalid_argument)` is the shape this repository already decided is
#             correct, and counting it would ask a command to undo its own fix. `this endpoint
#             must return HTTP 200` is a remediation sentence. `ready: <origin> returned HTTP
#             200` is the readiness probe's published first line, which a container
#             healthcheck parses. A gate with false positives teaches people to write a
#             baseline row for something that was never a defect.
#   next      a hand-written `Next:`/`next:` line. The renderer's Next primitive gives every
#             one of them the same spelling, the same blank line and the same casing.
#
# Comment lines are never counted: a comment is not an output path. The renderer's own
# package is never counted: it IS the renderer.
#
# THREE ANSWERS, never two: 0 at or under the baseline · 1 a finding · 2 I could not look.
set -u -o pipefail
LC_ALL=C
export LC_ALL

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
cd "$ROOT" || { printf 'check-cli-render: NO HE PODIDO MIRAR: no puedo entrar en %s\n' "$ROOT" >&2; exit 2; }

PKG="${OLIVARES_CLI_PKG:-cmd/olivares}"
BASELINE="${OLIVARES_CLI_RENDER_BASELINE:-docs/cli-render-baseline.txt}"
FLOOR="${OLIVARES_CLI_RENDER_FLOOR:-200}"

MODE="gate"
case "${1:-}" in
	''|--gate) MODE="gate" ;;
	--census)  MODE="census" ;;
	# An unknown flag is NOT ignored: a caller that asks for a mode this script does not
	# have is asking for a verdict it will not get, and a silent fallback to --gate reads
	# as an answer to the question they asked.
	*) printf 'check-cli-render: NO HE PODIDO MIRAR: opcion desconocida %q (modos: --census, --gate)\n' "$1" >&2; exit 2 ;;
esac

[ -d "$PKG" ] || { printf 'check-cli-render: NO HE PODIDO MIRAR: no existe %s\n' "$PKG" >&2; exit 2; }

# POPULATION FLOOR. A glob that stopped seeing the package reports zero findings and looks
# like success. The same floor, and the same reason, as the transport-exemption gate.
SEEN=$(find "$PKG" -name '*.go' ! -name '*_test.go' | wc -l)
if [ "$SEEN" -lt "$FLOOR" ]; then
	printf 'check-cli-render: NO HE PODIDO MIRAR: el barrido ve %d ficheros .go y el paquete\n' "$SEEN" >&2
	printf '  tiene cientos. Un escaneo que no ve el paquete no puede declararlo limpio.\n' >&2
	exit 2
fi

# THE TABLE RULE'S READER. It is a standalone Go module built with GOWORK=off, for the
# reasons scripts/check-error-mappers.sh already records: the gate must not drag the
# workspace's build graph into a fast lint, and a broken module elsewhere must not stop
# this one from looking. /tmp is noexec in the dev container, so the binary lands under
# TMPDIR and the 126/127 case is named rather than left as "permission denied".
TWSRC="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)/cli-render-tabwriter"
[ -d "$TWSRC" ] || { printf 'check-cli-render: NO HE PODIDO MIRAR: falta el lector de la regla table en %s\n' "$TWSRC" >&2; exit 2; }
command -v go >/dev/null 2>&1 || {
	printf 'check-cli-render: NO HE PODIDO MIRAR: no hay toolchain de Go en el PATH, asi que la\n' >&2
	printf '  regla table no se ha podido construir. Una puerta que no se ha mirado no esta limpia.\n' >&2
	exit 2
}
[ -n "${TMPDIR:-}" ] && mkdir -p "$TMPDIR" 2>/dev/null
TWDIR="$(mktemp -d 2>/dev/null)" || {
	printf 'check-cli-render: NO HE PODIDO MIRAR: no pude crear un scratch (TMPDIR=%s)\n' "${TMPDIR:-unset}" >&2
	exit 2
}
trap 'rm -rf "$TWDIR"' EXIT
TWBIN="$TWDIR/cli-render-tabwriter"
TWBUILD=$(cd "$TWSRC" && GOWORK=off go build -o "$TWBIN" . 2>&1) || {
	printf 'check-cli-render: NO HE PODIDO MIRAR: el lector de la regla table no compila.\n' >&2
	printf '%s\n' "$TWBUILD" | sed 's/^/    /' >&2
	exit 2
}
TABLE_LINES=$("$TWBIN" -pkg "$PKG" -root . -skip "$PKG/internal/termrender")
TWRC=$?
if [ "$TWRC" -eq 126 ] || [ "$TWRC" -eq 127 ]; then
	printf 'check-cli-render: NO HE PODIDO MIRAR: el lector se construyo bajo TMPDIR=%s y el shell\n' "${TMPDIR:-/tmp}" >&2
	printf '  no pudo ejecutarlo (exit %d). /tmp esta montado noexec en este contenedor.\n' "$TWRC" >&2
	exit 2
fi
[ "$TWRC" -eq 0 ] || { printf 'check-cli-render: NO HE PODIDO MIRAR: el lector de la regla table salio %d\n' "$TWRC" >&2; exit 2; }

CENSUS=$(OLIVARES_CLI_PKG="$PKG" OLIVARES_CLI_TABLE_LINES="$TABLE_LINES" python3 - <<'PY'
import os, re, sys

pkg = os.environ["OLIVARES_CLI_PKG"]

# The renderer itself is the answer, not a finding.
SKIP_DIR = os.path.join(pkg, "internal", "termrender")

# The `table` kind arrives ALREADY DECIDED, as `<path>\t<line>\t<why>` records from
# scripts/cli-render-tabwriter: it is the one rule whose subject is a type, and a type is
# resolved by a parser rather than matched in a line. See the header.
TABLE_LINES = {}
for record in os.environ.get("OLIVARES_CLI_TABLE_LINES", "").split("\n"):
    if not record.strip():
        continue
    path, lineno = record.split("\t")[:2]
    TABLE_LINES.setdefault(path, []).append(int(lineno))

RULES = [
    ("column",   re.compile(r"%-\d+[sdvqxt]"),                                True),
    ("status",   re.compile(r"\[(?:ok|OK|fail|FAIL|warn|WARN|pass|PASS|--)\]|[✓✗✔✘]"), True),
    # command layer only, and only when the envelope IS the sentence: see the header.
    ("envelope", re.compile(r"failed: HTTP|HTTP (?:%d|[1-5]\d\d): %"),        False),
    ("next",     re.compile(r'"(?:\\n)*\s*[Nn]ext:'),                         True),
]

def is_comment(line):
    s = line.lstrip()
    return s.startswith("//") or s.startswith("*") or s.startswith("/*")

def in_internal(path):
    return (os.sep + "internal" + os.sep) in path

# The files are read once; the four line-shaped rules are decided here and the table rule
# is merged in from the reader above.
sources = []
for dirpath, dirnames, filenames in os.walk(pkg):
    dirnames.sort()
    if dirpath == SKIP_DIR or dirpath.startswith(SKIP_DIR + os.sep):
        continue
    for name in sorted(filenames):
        if not name.endswith(".go") or name.endswith("_test.go"):
            continue
        path = os.path.join(dirpath, name)
        try:
            text = open(path, encoding="utf-8", errors="replace").read()
        except OSError as exc:
            print("READERR\t%s\t%s" % (path, exc), file=sys.stderr)
            sys.exit(3)
        sources.append((path, text.split("\n")))

# THE COUNT. The reader has already decided the table rule; a path it names but the walk
# below never saw would be a file this census cannot describe, so it is an error and not a
# silent extra row.
rows = {}
for path, lines in sources:
    kinds = {}
    for lineno in TABLE_LINES.pop(path, []):
        kinds.setdefault("table", []).append(lineno)
    for lineno, line in enumerate(lines, 1):
        if is_comment(line):
            continue
        for kind, rx, everywhere in RULES:
            if not everywhere and in_internal(path):
                continue
            if rx.search(line):
                kinds.setdefault(kind, []).append(lineno)
    if kinds:
        rows[path] = kinds

if TABLE_LINES:
    print("TABLELEFT\t%s" % ",".join(sorted(TABLE_LINES)), file=sys.stderr)
    sys.exit(4)

for path in sorted(rows):
    kinds = rows[path]
    total = sum(len(v) for v in kinds.values())
    detail = ",".join("%s=%d" % (k, len(kinds[k])) for k in sorted(kinds))
    lines = ",".join(str(n) for k in sorted(kinds) for n in kinds[k])
    print("%s\t%d\t%s\t%s" % (path, total, detail, lines))
PY
) || { printf 'check-cli-render: NO HE PODIDO MIRAR: el escaneo fallo\n' >&2; exit 2; }

TOTAL=$(printf '%s\n' "$CENSUS" | command grep -c . || true)
PATHS=$(printf '%s\n' "$CENSUS" | command grep . | awk -F'\t' '{s+=$2} END {print s+0}')

if [ "$MODE" = "census" ]; then
	printf '# check-cli-render --census — %s\n' "$(date -u +%Y-%m-%dT%H:%MZ)"
	printf '# %d file(s), %d hand-formatted output path(s), over %d non-test .go file(s) in %s\n' \
		"$TOTAL" "$PATHS" "$SEEN" "$PKG"
	printf '# file\tpaths\tkinds\tlines\n'
	printf '%s\n' "$CENSUS" | command grep .
	exit 0
fi

if [ ! -r "$BASELINE" ]; then
	printf 'check-cli-render: NO HE PODIDO MIRAR: no puedo leer la linea base %s.\n' "$BASELINE" >&2
	printf '  Sin ella el trinquete no tiene contra que medir, y «no hay fichero» no es «esta limpio».\n' >&2
	printf '  Para crearla: bash scripts/check-cli-render.sh --census\n' >&2
	exit 2
fi

VERDICT=$(BASELINE="$BASELINE" CENSUS="$CENSUS" python3 - <<'PY'
import os, sys

baseline = {}
malformed = []
with open(os.environ["BASELINE"], encoding="utf-8") as fh:
    for lineno, raw in enumerate(fh, 1):
        line = raw.rstrip("\n")
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        parts = line.split("\t")
        # count, path, reason. A row without a reason is a number somebody typed, not a
        # decision somebody made, so it is malformed on purpose.
        if len(parts) < 3 or not parts[0].strip().isdigit() or not parts[2].strip():
            malformed.append("%d: %s" % (lineno, line))
            continue
        baseline[parts[1].strip()] = int(parts[0].strip())

if malformed:
    print("MALFORMED")
    for m in malformed:
        print("  " + m)
    sys.exit(0)

actual = {}
for line in os.environ["CENSUS"].split("\n"):
    if not line.strip():
        continue
    path, total, detail, _lines = line.split("\t", 3)
    actual[path] = (int(total), detail)

over, under, new, gone = [], [], [], []
for path in sorted(set(actual) | set(baseline)):
    have = actual.get(path, (0, ""))[0]
    want = baseline.get(path)
    detail = actual.get(path, (0, ""))[1]
    if want is None:
        new.append("%s: %d hand-formatted path(s) [%s] and no row in the baseline" % (path, have, detail))
    elif have > want:
        over.append("%s: %d > %d [%s]" % (path, have, want, detail))
    elif have < want:
        under.append("%s: %d < %d — lower the row to %d in this commit" % (path, have, want, have))
    if want is not None and have == 0 and path not in actual:
        gone.append(path)

print("TOTAL\t%d\t%d" % (sum(v[0] for v in actual.values()), sum(baseline.values())))
for label, rows in (("NEW", new), ("OVER", over), ("UNDER", under)):
    for row in rows:
        print("%s\t%s" % (label, row))
PY
) || { printf 'check-cli-render: NO HE PODIDO MIRAR: no pude leer la linea base\n' >&2; exit 2; }

if command grep -q '^MALFORMED$' <<<"$VERDICT"; then
	printf 'check-cli-render: NO HE PODIDO MIRAR: %s tiene filas ilegibles.\n' "$BASELINE" >&2
	printf '  Una fila es «<cuenta>\\t<fichero>\\t<razon>», y la razon no es opcional: un numero\n' >&2
	printf '  sin razon es alguien tecleando una cifra, no alguien tomando una decision.\n' >&2
	printf '%s\n' "$VERDICT" | command grep -v '^MALFORMED$' >&2
	exit 2
fi

HAVE=$(printf '%s\n' "$VERDICT" | awk -F'\t' '$1=="TOTAL" {print $2}')
WANT=$(printf '%s\n' "$VERDICT" | awk -F'\t' '$1=="TOTAL" {print $3}')
FINDINGS=$(printf '%s\n' "$VERDICT" | command grep -E '^(NEW|OVER|UNDER)\b' || true)

if [ -n "$FINDINGS" ]; then
	printf 'check-cli-render: %d ruta(s) formateadas a mano frente a una linea base de %d.\n' "$HAVE" "$WANT" >&2
	printf '%s\n' "$FINDINGS" | sed 's/^NEW\t/    NEW   /; s/^OVER\t/    OVER  /; s/^UNDER\t/    UNDER /' >&2
	printf '\n  NEW/OVER: pasa esa salida por el renderer (cmd/olivares/internal/termrender):\n' >&2
	printf '    Table para una lista, Fields para un bloque clave/valor, StatusLine para un\n' >&2
	printf '    veredicto, Summary para el cierre, Error para un fallo, Next para el paso siguiente.\n' >&2
	printf '  UNDER: baja la fila en ESTE commit. Un trinquete que recupera holgura en silencio\n' >&2
	printf '    es un contador.\n' >&2
	exit 1
fi

printf 'check-cli-render: LIMPIO — %d ruta(s) formateadas a mano en %d fichero(s), en su linea base\n' \
	"$HAVE" "$TOTAL"
printf '  de %d, sobre %d fichero(s) .go de %s.\n' "$WANT" "$SEEN" "$PKG"
exit 0
