#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# test-overlay-gate-wiring.sh — the bench for cmd/olivares/tools/checkoverlaygatewiring,
# which judges where the live overlay reader is called and where it is not.
#
# Invariant under test: one `task lint:overlay-live-facts` runs in .githooks/pre-push
# immediately after the second same-act `task lint:overlay-seal` and before
# `task lint:addon-sets` and `task lint:addon-sets-gate`; the Community derivation
# aggregate reaches no live read; the hermetic reader battery keeps its caller. The live
# reader needs the sibling private clone and this act's seal, which exist only at that
# paired dev boundary, so a Community-only lane can never answer anything but 2 there.
#
# Second, narrow invariant: the exact-candidate facts battery is ONE plain `hub-leg.sh` item of
# lint:addon-sets-gate:legs, and the judge sees that very line reached and executed. It is a
# source-fact battery, not an authority gate; this bench only keeps it from becoming orphaned,
# skipped or echo-only.
#
# The judge is a compiled Go tool, so the clean path is reproducible wherever this leg
# runs. `go build` and never `go run`: `go run` collapses the tool's exit code and with it
# the third answer.
#
# Each control moves one dimension over disposable copies of the two real files, and each
# asserts its own subject change: an anchor must occur exactly as often as the mutation
# claims, and every mutant carries a postcondition stating what it now is.
#
# Verdicts, the judge's and this bench's: 0 wiring intact · 1 finding · 2 COULD NOT LOOK.
# The real-source verdict is propagated by status, not summarized: a 2 leaves this bench
# as 2, and an unexpected status is reported as unexamined rather than scored.
set -uo pipefail

NAME=test-overlay-gate-wiring
SELF="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/$(basename -- "$0")"
ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
HOOK="$ROOT/.githooks/pre-push"
TASKFILE="$ROOT/Taskfile.yml"

blind() {
	printf '%s: COULD NOT LOOK: %s\n' "$NAME" "$1" >&2
	exit 2
}

[ -r "$HOOK" ] || blind "cannot read $HOOK."
[ -r "$TASKFILE" ] || blind "cannot read $TASKFILE."
command -v go >/dev/null 2>&1 || blind "no Go toolchain, so the judge cannot be built."
# python3 builds fixtures, never the judgment, and only from its standard library. The
# portability defect this bench corrects was an interpreter package, not the interpreter:
# scripts/test-overlay-live-facts.sh already requires python3 in this same leg.
command -v python3 >/dev/null 2>&1 || blind "no python3, so the fixtures cannot be built."

# shellcheck source=lib/exec-workdir.sh
# exec-workdir.sh proves a directory can create AND execute: /tmp is mounted noexec here.
. "$ROOT/scripts/lib/exec-workdir.sh" || blind "missing scripts/lib/exec-workdir.sh."

WORK="$(olivares_pick_exec_workdir ogwiring)" || blind "no directory where a built binary can run."
trap 'rm -rf "$WORK"' EXIT
# A signal ENDS this bench. The workspace used to be removed by a HUP/INT/TERM handler that
# then returned, so a torn-down run kept going over a deleted tree and printed pass counts
# (measured 2026-09-14, process-group SIGHUP and SIGTERM during the self-clean child: "76
# passed, 0 failed, 4 not examined"; a signal during the last shell-only rows would have left
# nothing to fail and exited 0). The EXIT trap still removes the workspace.
trap 'printf "%s: COULD NOT LOOK: interrupted by SIGHUP\n" "$NAME" >&2; exit 2' HUP
trap 'printf "%s: COULD NOT LOOK: interrupted by SIGINT\n" "$NAME" >&2; exit 2' INT
trap 'printf "%s: COULD NOT LOOK: interrupted by SIGTERM\n" "$NAME" >&2; exit 2' TERM

JUDGE="$WORK/checkoverlaygatewiring"
if ! (CDPATH= cd -- "$ROOT/cmd/olivares" 2>/dev/null &&
	go build -o "$JUDGE" ./tools/checkoverlaygatewiring); then
	blind "the judge does not build; a bench cannot answer for a tool it could not compile."
fi

pass=0
fail=0
unexam=0
ok() {
	printf 'ok    %s\n' "$1"
	pass=$((pass + 1))
}
bad() {
	printf 'FAIL  %s\n' "$1" >&2
	fail=$((fail + 1))
}
# A control whose subject could not be judged is not a pass and not a finding: it is an
# unexamined dimension, and it leaves this bench unable to claim it verified anything.
unexamined() {
	printf 'BLIND %s\n' "$1" >&2
	unexam=$((unexam + 1))
}

judge() { # <hook> <taskfile>; output in $WORK/judge.out, returns the tool's own code
	"$JUDGE" "$1" "$2" >"$WORK/judge.out" 2>&1
}

# classify <wanted status> <actual status> -> pass | finding | unexamined
#   One rule for every boundary in this bench: the judge over a fixture, the judge over the
#   real source, and a child bench's own exit. A status that matches is the control's
#   result; 0 and 1 are definite verdicts, so getting the wrong one is a finding; anything
#   else — a refusal where a verdict was wanted, a signal, an unknown status — means the
#   dimension was not judged, which is not a failure and is certainly not a pass.
classify() {
	if [ "$2" -eq "$1" ]; then
		printf 'pass\n'
		return
	fi
	case "$2" in
	0 | 1) printf 'finding\n' ;;
	*) printf 'unexamined\n' ;;
	esac
}

# ── the fixture builder ───────────────────────────────────────────────────────────────
MUTATE="$WORK/mutate.py"
cat >"$MUTATE" <<'PY'
"""Build one mutant of the two judged files and assert what it actually changed.

argv: <hook path> <taskfile path> <mutation source> <postcondition expression>

Both run over the text variables `hook` and `taskfile`. `once` and `nth` refuse a pattern
that is not present exactly as often as the mutation claims, so a control cannot decay into
"no anchor found, nothing changed" while still reporting the finding its first half caused.
Standard library only: this builds fixtures, it does not judge them.
"""
import io
import re
import sys


def die(message):
    sys.stderr.write("mutate: %s\n" % message)
    raise SystemExit(1)


def once(text, old, new):
    seen = text.count(old)
    if seen != 1:
        die("pattern occurs %d time(s), expected exactly 1: %r" % (seen, old[:72]))
    return text.replace(old, new, 1)


def nth(text, old, new, k, total):
    seen = text.count(old)
    if seen != total:
        die("pattern occurs %d time(s), expected exactly %d: %r" % (seen, total, old[:72]))
    at = -1
    for _ in range(k):
        at = text.index(old, at + 1)
    return text[:at] + new + text[at + len(old):]


def count(text, needle):
    return text.count(needle)


def pos(text, needle, k=1):
    at = -1
    for _ in range(k):
        at = text.index(needle, at + 1)
    return at


hook_path, taskfile_path, mutation, postcondition = sys.argv[1:5]
hook = io.open(hook_path, encoding="utf-8").read()
taskfile = io.open(taskfile_path, encoding="utf-8").read()
before = (hook, taskfile)

# The anchors the controls share. LIVE_CMD is the dedicated live task's only command;
# BENCH_CMD is the add-on battery leg; AGGREGATE_HEAD opens the Community derivation
# aggregate and is what the dependency and status controls rewrite.
LIVE_CMD = "      - bash scripts/hub-leg.sh lint:overlay-live-facts scripts/check-overlay-live-facts.sh\n"
BENCH_CMD = "      - bash scripts/hub-leg.sh lint:addon-sets-gate scripts/test-overlay-live-facts.sh\n"
AGGREGATE_HEAD = "\n  lint:addon-sets:\n    desc: >-\n"
# CAND_CMD is the exact-candidate facts battery leg; SELFTEST_CMD is the only command of the
# live-facts selftest task, a real task outside the battery that a relocation control targets.
CAND_CMD = "      - bash scripts/hub-leg.sh lint:addon-sets-gate scripts/test-overlay-candidate-facts.sh\n"
SELFTEST_CMD = "      - bash scripts/hub-leg.sh lint:overlay-live-facts:selftest scripts/test-overlay-live-facts.sh\n"
LEGS_HEAD = "\n  lint:addon-sets-gate:legs:\n    desc: >-\n"

# ── where a control inserts a command, located by STRUCTURE ────────────────────────────
# Until 2026-09-10 every insertion keyed on one command line of this task, and that line
# named an incidental helper script. The filename was never part of what the controls
# test — they need "one more item in THIS task's command list" — but it was part of their
# text, so the export closure gate read the bench as a published caller of a curated-out
# path. It was right to: a static reader cannot tell an anchor from a call. The location
# is therefore structural now, and the grammar is deliberately narrow rather than a YAML
# parser. It supports exactly this shape and refuses anything else:
#
#   * exactly one column-2 task header `  lint:addon-sets:`, whose task runs to the next
#     column-2 key or to end of file;
#   * inside that task, exactly one column-4 `cmds:` field introducing a block sequence;
#   * a NONEMPTY list whose items begin at the field's indentation plus two with "- ",
#     where blank lines and more deeply indented lines continue the item above them.
#
# Refusing is the load-bearing half. A builder that guesses a location writes a mutant
# that tests something other than what its label says, and the bench then reports a real
# verdict about the wrong subject — the failure this bench exists to make impossible.
# Every refusal happens BEFORE any fixture byte is written.
TASK_HEADER = "  lint:addon-sets:"
CMDS_FIELD = "    cmds:"
ITEM_INDENT = " " * (len(CMDS_FIELD) - len(CMDS_FIELD.lstrip(" ")) + 2)


def line_span(text, at):
    """The [start, end) span of the line holding `at`; end is past its newline."""
    start = text.rfind("\n", 0, at) + 1
    end = text.find("\n", at)
    return start, (len(text) if end < 0 else end + 1)


def aggregate_block(text):
    """The [start, end) span of the lint:addon-sets task, or refuse."""
    found = [m.start() for m in re.finditer(r"^%s[ \t]*$" % re.escape(TASK_HEADER), text, re.M)]
    if len(found) != 1:
        die("aggregate: expected exactly 1 task header %r, found %d" % (TASK_HEADER, len(found)))
    start = found[0]
    after = re.compile(r"^  \S", re.M).search(text, line_span(text, start)[1])
    return start, (after.start() if after else len(text))


def aggregate_cmds(text):
    """(insertion offset, item line offsets, block end) for the aggregate, or refuse.

    The insertion offset is the byte immediately after the `cmds:` line, i.e. the head of
    the command list, so an inserted item never lands between an item and its own
    continuation lines.
    """
    bstart, bend = aggregate_block(text)
    block = text[bstart:bend]
    found = [m.start() for m in re.finditer(r"^%s[ \t]*$" % re.escape(CMDS_FIELD), block, re.M)]
    if len(found) != 1:
        die("aggregate: expected exactly 1 field %r in the task, found %d" % (CMDS_FIELD, len(found)))
    at = bstart + line_span(block, found[0])[1]
    items = []
    walk = at
    while walk < bend:
        lstart, lend = line_span(text, walk)
        line = text[lstart:lend]
        walk = lend
        if line.strip() == "":
            continue
        if line.startswith(ITEM_INDENT + "- "):
            items.append(lstart)
        elif items and line.startswith(ITEM_INDENT + " "):
            continue
        else:
            die("aggregate: unsupported line in the command list: %r" % line)
    if not items:
        die("aggregate: the lint:addon-sets command list is empty")
    return at, items, bend


def check_item(item):
    """The item must already carry the list's own indentation, or refuse."""
    if not item.endswith("\n"):
        die("aggregate: the item to insert must end with a newline: %r" % item[:72])
    lines = item.splitlines(True)
    if not lines[0].startswith(ITEM_INDENT + "- "):
        die("aggregate: the item to insert must start with %r: %r" % (ITEM_INDENT + "- ", lines[0]))
    for line in lines[1:]:
        if line.strip() == "" or line.startswith(ITEM_INDENT + " "):
            continue
        if line.startswith(ITEM_INDENT + "- "):
            die("aggregate: the item to insert holds more than one command: %r" % line)
        die("aggregate: unsupported continuation line in the item to insert: %r" % line)


def add_to_aggregate(text, item):
    """Insert one command item at the head of the Community derivation aggregate's list.

    Every original byte survives: the item is spliced at one offset and the result is
    re-located from scratch, so a control cannot report a verdict over a fixture whose
    task, command list or following task definitions were disturbed by its own setup.
    """
    check_item(item)
    at, items, bend = aggregate_cmds(text)
    original_items = [text[line_span(text, i)[0]:line_span(text, i)[1]] for i in items]
    new = text[:at] + item + text[at:]
    if new[:at] != text[:at] or new[at + len(item):] != text[at:]:
        die("aggregate: the insertion did not preserve the surrounding bytes")
    if new[bend + len(item):] != text[bend:]:
        die("aggregate: the insertion changed the following task definitions")
    at2, items2, bend2 = aggregate_cmds(new)
    if at2 != at or bend2 != bend + len(item):
        die("aggregate: the insertion moved the command list of the task it targets")
    if not (at2 <= at < at + len(item) <= bend2):
        die("aggregate: the insertion did not land in the lint:addon-sets command list")
    current_items = [new[line_span(new, i)[0]:line_span(new, i)[1]] for i in items2]
    if current_items[0] != item.splitlines(True)[0] or current_items[1:] != original_items:
        die("aggregate: the insertion changed the task's original commands")
    return new


def in_aggregate(text, needle):
    """The needle occurs exactly once, and inside the lint:addon-sets task body.

    A control that inserts bytes somewhere the judged walk never reaches would pass for the
    wrong reason, so every insertion asserts its actual reachable location.
    """
    if text.count(needle) != 1:
        return False
    start = text.index("\n  lint:addon-sets:\n")
    following = re.compile(r"\n  [A-Za-z0-9]").search(text, start + 1)
    end = following.start() if following else len(text)
    return start < text.index(needle) < end


exec(mutation)

if (hook, taskfile) == before:
    die("the mutation changed nothing: the mutant would test the original")
if not eval(postcondition):
    die("the mutant does not hold its postcondition: %s" % postcondition)

io.open(hook_path, "w", encoding="utf-8").write(hook)
io.open(taskfile_path, "w", encoding="utf-8").write(taskfile)
PY

fixture() { # <slug> -> prints the fixture directory; only $WORK is ever written
	local dir="$WORK/$1"
	rm -rf "$dir"
	mkdir -p "$dir" || return 1
	cp "$HOOK" "$dir/pre-push" || return 1
	cp "$TASKFILE" "$dir/Taskfile.yml" || return 1
	printf '%s\n' "$dir"
}

build() { # <slug> <mutation> <postcondition> -> prints the directory
	local dir
	dir="$(fixture "$1")" || {
		printf 'could not build the fixture directory\n' >&2
		return 1
	}
	python3 "$MUTATE" "$dir/pre-push" "$dir/Taskfile.yml" "$2" "$3" || return 1
	printf '%s\n' "$dir"
}

# expect <slug> <label> <wanted rc> <mutation> <postcondition>
#   The mutation must apply, the mutant must be what it claims to be, and the judge must
#   answer exactly the wanted status. A build or parse failure is never a successful
#   control, and a status nobody expected leaves the dimension unexamined.
expect() {
	local dir rc want="$3"
	dir="$(build "$1" "$4" "$5")" || {
		unexamined "$2: no usable mutant, so this dimension was not judged"
		return
	}
	judge "$dir/pre-push" "$dir/Taskfile.yml"
	rc=$?
	case "$(classify "$want" "$rc")" in
	pass) ok "$2 (rc=$rc)" ;;
	finding)
		bad "$2: judge answered rc=$rc, wanted rc=$want"
		sed 's/^/      /' "$WORK/judge.out" >&2
		;;
	*)
		unexamined "$2: judge answered rc=$rc, wanted rc=$want"
		sed 's/^/      /' "$WORK/judge.out" >&2
		;;
	esac
}

causal() { expect "$1" "$2" 1 "$3" "$4"; }   # the dimension is load-bearing: a finding
refuses() { expect "$1" "$2" 2 "$3" "$4"; }  # the form is unreadable: COULD NOT LOOK
permits() { expect "$1" "$2" 0 "$3" "$4"; }  # the change is data: the verdict must not move

# ── the exact-candidate facts battery: form here, reach and execution by the judge ─────────
# The judge is not taught a second battery. Its own battery predicate — lint:addon-sets-gate
# reaches scripts/test-overlay-live-facts.sh through an admitted, executing form — is asked
# about the candidate LINE through a proxy Taskfile: the live-facts leg removed (the judge must
# then answer 1, proving nothing else supplies that caller) and the candidate leg renamed into
# its place (the judge must answer 0). The exact-form predicate comes first, because the judge
# splits `a || true` into two commands and would still see the leg run.
CANDWIRE="$WORK/candidate-wiring.py"
cat >"$CANDWIRE" <<'PY'
"""The candidate battery leg is one exact item of lint:addon-sets-gate:legs's own cmds list.

argv: <taskfile path>. 0 it is · 1 it is not (removed, echoed, suffixed, moved) · 2 the task
or its list cannot be located. Reachability and execution are the judge's, over the proxy.
"""
import io
import re
import sys

TASK = "  lint:addon-sets-gate:legs:"
CMDS = "    cmds:"
ITEM = "      - bash scripts/hub-leg.sh lint:addon-sets-gate scripts/test-overlay-candidate-facts.sh\n"


def answer(rc, message):
    print("candidate-wiring: %s" % message)
    raise SystemExit(rc)


try:
    text = io.open(sys.argv[1], encoding="utf-8").read()
except (OSError, ValueError) as exc:
    answer(2, "cannot read the Taskfile (%s)" % exc)
heads = [m.start() for m in re.finditer(r"^%s[ \t]*$" % re.escape(TASK), text, re.M)]
if len(heads) != 1:
    answer(2, "expected exactly 1 task header %r, found %d" % (TASK.strip(), len(heads)))
start = heads[0]
following = re.compile(r"^  \S", re.M).search(text, text.index("\n", start) + 1)
end = following.start() if following else len(text)
fields = [m for m in re.finditer(r"^%s[ \t]*$" % re.escape(CMDS), text[start:end], re.M)]
if len(fields) != 1:
    answer(2, "expected exactly 1 %r field in %s, found %d" % (CMDS.strip(), TASK.strip(), len(fields)))
list_at = start + fields[0].end()
seen = text.count(ITEM)
if seen != 1:
    answer(1, "the leg %r occurs %d time(s), expected exactly 1" % (ITEM.strip(), seen))
at = text.index(ITEM)
if not list_at < at < end or re.search(r"^    \S", text[list_at:at], re.M):
    answer(1, "the leg is not an item of %s's cmds list" % TASK.strip())
answer(0, "the leg is one exact item of %s's cmds list" % TASK.strip())
PY

CANDPROXY="$WORK/candidate-proxy.py"
cat >"$CANDPROXY" <<'PY'
"""argv: <taskfile> <bare out> <swapped out>. Bare: the live-facts battery leg removed.
Swapped: bare, with the candidate leg renamed to the live-facts battery leg. Refuses (1)
unless each leg occurs exactly once, so the proxy always speaks about the candidate line."""
import io
import sys

BENCH = "      - bash scripts/hub-leg.sh lint:addon-sets-gate scripts/test-overlay-live-facts.sh\n"
ITEM = "      - bash scripts/hub-leg.sh lint:addon-sets-gate scripts/test-overlay-candidate-facts.sh\n"
text = io.open(sys.argv[1], encoding="utf-8").read()
if text.count(BENCH) != 1 or text.count(ITEM) != 1:
    sys.stderr.write("candidate-proxy: each leg must occur exactly once (live %d, candidate %d)\n"
                     % (text.count(BENCH), text.count(ITEM)))
    raise SystemExit(1)
bare = text.replace(BENCH, "", 1)
swapped = bare.replace(ITEM, BENCH, 1)
if bare.count(BENCH) != 0 or swapped.count(BENCH) != 1 or ITEM in swapped:
    sys.stderr.write("candidate-proxy: the proxy does not hold its postcondition\n")
    raise SystemExit(1)
io.open(sys.argv[2], "w", encoding="utf-8").write(bare)
io.open(sys.argv[3], "w", encoding="utf-8").write(swapped)
PY

candidate_wiring() { # <hook> <taskfile> -> 0 wired · 1 finding · 2 could not look; detail in $WORK/cand.out
	local dir="$WORK/cand-proxy" rc
	python3 "$CANDWIRE" "$2" >"$WORK/cand.out" 2>&1
	rc=$?
	case "$rc" in
	0) ;;
	1) return 1 ;;
	*) return 2 ;;
	esac
	rm -rf "$dir"
	mkdir -p "$dir" || return 2
	python3 "$CANDPROXY" "$2" "$dir/bare.yml" "$dir/swapped.yml" >>"$WORK/cand.out" 2>&1 || return 2
	"$JUDGE" "$1" "$dir/bare.yml" >"$dir/bare.out" 2>&1
	rc=$?
	sed 's/^/bare: /' "$dir/bare.out" >>"$WORK/cand.out"
	if [ "$rc" -ne 1 ] || ! grep -qF 'the hermetic live-reader battery lost its caller' "$dir/bare.out"; then
		echo "candidate-wiring: without its leg the judge answered $rc, not the lost-caller finding, so the proxy cannot isolate the candidate leg" >>"$WORK/cand.out"
		return 2
	fi
	"$JUDGE" "$1" "$dir/swapped.yml" >"$dir/swapped.out" 2>&1
	rc=$?
	sed 's/^/swapped: /' "$dir/swapped.out" >>"$WORK/cand.out"
	case "$rc" in
	0 | 1) return "$rc" ;;
	*) return 2 ;;
	esac
}

cand_expect() { # <slug> <label> <wanted rc> <mutation> <postcondition>
	local dir rc want="$3"
	dir="$(build "cand-$1" "$4" "$5")" || {
		unexamined "$2: no usable mutant, so this dimension was not judged"
		return
	}
	candidate_wiring "$dir/pre-push" "$dir/Taskfile.yml"
	rc=$?
	case "$(classify "$want" "$rc")" in
	pass) ok "$2 (rc=$rc)" ;;
	finding)
		bad "$2: candidate wiring answered rc=$rc, wanted rc=$want"
		sed 's/^/      /' "$WORK/cand.out" >&2
		;;
	*)
		unexamined "$2: candidate wiring answered rc=$rc, wanted rc=$want"
		sed 's/^/      /' "$WORK/cand.out" >&2
		;;
	esac
}

# ── the real source, propagated by status ─────────────────────────────────────────────
echo "== the tree as it stands =="
judge "$HOOK" "$TASKFILE"
real_rc=$?
sed 's/^/      /' "$WORK/judge.out"
case "$real_rc" in
0)
	ok "the live overlay read is wired where the decision puts it"
	;;
1)
	bad "the live overlay read is NOT wired as decided"
	echo "$NAME: $pass passed, $fail failed, $unexam not examined (real source carries a finding)"
	exit 1
	;;
2)
	blind "the judge could not read the real source. Its verdict is this bench's verdict."
	;;
*)
	printf '%s: COULD NOT LOOK: the judge exited with status %s over the real source, which is\n' "$NAME" "$real_rc" >&2
	printf '%s: neither a verdict nor a refusal. This run is unexamined.\n' "$NAME" >&2
	exit 2
	;;
esac

echo "== the exact-candidate facts battery, as it stands =="
candidate_wiring "$HOOK" "$TASKFILE"
cand_rc=$?
sed 's/^/      /' "$WORK/cand.out"
case "$cand_rc" in
0)
	ok "the exact-candidate facts battery is one exact leg the judge sees reached and executed"
	;;
1)
	bad "the exact-candidate facts battery is NOT wired as one executed leg of lint:addon-sets-gate:legs"
	echo "$NAME: $pass passed, $fail failed, $unexam not examined (real source carries a finding)"
	exit 1
	;;
*)
	blind "the exact-candidate facts battery wiring could not be judged over the real source."
	;;
esac

echo
echo "== causal controls — each moves one dimension and asserts what it moved =="

causal deletion "deletion: the hook stops calling the dedicated live task" \
	'hook = once(hook, "\ntask lint:overlay-live-facts\n", "\n")' \
	'count(hook, "\ntask lint:overlay-live-facts\n") == 0'

causal duplicate "duplicate: a second dedicated live invocation in the same act" \
	'hook = once(hook, "\ntask lint:addon-sets-gate\n", "\ntask lint:addon-sets-gate\ntask lint:overlay-live-facts\n")' \
	'count(hook, "\ntask lint:overlay-live-facts\n") == 2'

# The postcondition is what makes this a relocation rather than a deletion: one live call,
# on the wrong side of the second seal.
causal moved "moved-before-seal: the live read runs before the second seal refresh" \
	'hook = once(hook, "\ntask lint:overlay-live-facts\n", "\n")
hook = nth(hook, "\ntask lint:overlay-seal\n", "\ntask lint:overlay-live-facts\ntask lint:overlay-seal\n", 2, 2)' \
	'count(hook, "\ntask lint:overlay-live-facts\n") == 1 and pos(hook, "\ntask lint:overlay-live-facts\n") < pos(hook, "\ntask lint:overlay-seal\n", 2)'

causal moved_after "moved-after-derivation: the live read runs after the add-on lane" \
	'hook = once(hook, "\ntask lint:overlay-live-facts\n", "\n")
hook = once(hook, "\ntask lint:addon-sets-gate\n", "\ntask lint:addon-sets-gate\ntask lint:overlay-live-facts\n")' \
	'count(hook, "\ntask lint:overlay-live-facts\n") == 1 and pos(hook, "\ntask lint:overlay-live-facts\n") > pos(hook, "\ntask lint:addon-sets\n")'

causal reintroduced "reintroduction: the Community aggregate runs the live reader again" \
	'taskfile = add_to_aggregate(taskfile, "      - bash scripts/hub-leg.sh lint:addon-sets scripts/check-overlay-live-facts.sh\n")' \
	'count(taskfile, "hub-leg.sh lint:addon-sets scripts/check-overlay-live-facts.sh") == 1'

causal reintroduced_delegated "reintroduction: the aggregate delegates to the live task" \
	'taskfile = add_to_aggregate(taskfile, "      - task: lint:overlay-live-facts\n")' \
	'count(taskfile, "      - task: lint:overlay-live-facts\n") == 1'

# ── the split dispatcher: BOTH branches are reachable, and each is its own control ──────
# lint:addon-sets does not run a script: it hands two task names to edition-split-gate.sh,
# which picks one at run time from a classification this judge does not perform. One tree
# runs one branch, so a live read planted in EITHER is a live read the Community derivation
# aggregate reaches. Walking only the branch a guess prefers would answer a different
# question — "not reachable in the tree I assumed" — while still printing 0.
causal split_public "reintroduction: the dispatcher's PUBLIC branch delegates to the live task" \
	'taskfile = once(taskfile, "\n  lint:addon-sets:public:\n    desc: >-\n", "\n  lint:addon-sets:public:\n    deps: [lint:overlay-live-facts]\n    desc: >-\n")' \
	'count(taskfile, "\n  lint:addon-sets:public:\n    deps: [lint:overlay-live-facts]\n") == 1'

causal split_private "reintroduction: the dispatcher's PRIVATE branch delegates to the live task" \
	'taskfile = once(taskfile, "\n  lint:addon-sets:legs:\n    desc: >-\n", "\n  lint:addon-sets:legs:\n    deps: [lint:overlay-live-facts]\n    desc: >-\n")' \
	'count(taskfile, "\n  lint:addon-sets:legs:\n    deps: [lint:overlay-live-facts]\n") == 1'

# The dispatcher's own contract is `$# -eq 4`. A call with any other count is a call this
# judge must not complete from three arguments and a guess, and neither is a wrapper it has
# never been taught — even one whose name merely rhymes with the dispatcher's.
refuses split_short "the dispatcher called with fewer arguments than its contract" \
	'taskfile = once(taskfile, "        lint:addon-sets:public lint:addon-sets:legs\n", "        lint:addon-sets:public\n")' \
	'"        lint:addon-sets:public lint:addon-sets:legs\n" not in taskfile'

# export-closure: fixture scripts/edition-split-gate-v2.sh — a SYNTHETIC name written into a
# throwaway Taskfile mutant, never a dependency: the control needs a wrapper the judge has NOT
# been taught, so the path must exist in neither the hub nor the export, and must not come to
# exist. The class is `fixture`, not `absent-by-design` (that one requires the hub to HAVE the
# path) and not silence: the gate reads this published file as shell text, and a name it cannot
# resolve is a dangling reference until somebody says which of the two it is. If a file of this
# name ever appeared, this would stop being a fixture and become a real reference — which is the
# distinction the gate makes, and the reason the declaration sits at the call site.
refuses split_unknown_wrapper "an unmodelled wrapper handed the same two task arguments" \
	'taskfile = once(taskfile, "        bash scripts/edition-split-gate.sh lint:addon-sets\n", "        bash scripts/edition-split-gate-v2.sh lint:addon-sets\n")' \
	'count(taskfile, "scripts/edition-split-gate-v2.sh") == 1'

refuses split_expanded_branch "the dispatcher delegating to a branch this judge cannot resolve" \
	'taskfile = once(taskfile, "        lint:addon-sets:public lint:addon-sets:legs\n", "        lint:addon-sets:public \"$LEGS\"\n")' \
	'count(taskfile, "lint:addon-sets:public \"$LEGS\"") == 1'

causal unbenched "removed battery: the add-on battery drops the hermetic live-reader controls" \
	'taskfile = once(taskfile, BENCH_CMD, "")' \
	'BENCH_CMD not in taskfile'

causal hollow "hollow task: the dedicated task stops running the live reader" \
	'taskfile = once(taskfile, LIVE_CMD, "      - true\n")' \
	'LIVE_CMD not in taskfile'

# The three witnesses a behavioural probe recorded as CLEAN against the previous judge,
# applied byte for byte: a Taskfile command that is a comment, an echo naming the battery
# script, and an inline comment in the hook.
causal comment_command "false green 1: the live task's command is a comment string" \
	'taskfile = once(taskfile, LIVE_CMD, "      - \"# bash scripts/hub-leg.sh lint:overlay-live-facts scripts/check-overlay-live-facts.sh\"\n")' \
	'"\"# bash scripts/hub-leg.sh lint:overlay-live-facts" in taskfile'

causal echoed_battery "false green 2: the battery echoes the script instead of running it" \
	'taskfile = once(taskfile, BENCH_CMD, "      - echo scripts/test-overlay-live-facts.sh\n")' \
	'count(taskfile, "      - echo scripts/test-overlay-live-facts.sh\n") == 1'

causal inline_comment "false green 3: the hook's live command is an inline comment" \
	'hook = once(hook, "\ntask lint:overlay-live-facts\n", "\n: # ; task lint:overlay-live-facts\n")' \
	'count(hook, "\n: # ; task lint:overlay-live-facts\n") == 1 and count(hook, "\ntask lint:overlay-live-facts\n") == 0'

# The battery's wrapper takes a quoted sentence and then the task it execs. A task name
# written inside that sentence is data; following it would let prose supply a call graph.
causal quoted_reason "quoted data is not a task edge: the real target moves into the reason" \
	'taskfile = once(taskfile, "        commercial/module-slug-package.json)\" lint:addon-sets-gate:legs\n", "        commercial/module-slug-package.json, runs lint:addon-sets-gate:legs)\" lint:overlay-live-facts\n")' \
	'"runs lint:addon-sets-gate:legs)\" lint:overlay-live-facts" in taskfile'

# A YAML literal scalar is a shell PROGRAM, not one line. The comment on its first line
# ends at that newline, so the reader on the next line is a second command the aggregate
# really runs.
causal multiline_command "a literal command block runs a second line after a comment" \
	'taskfile = add_to_aggregate(taskfile, "      - |\n        echo ok # harmless\n        bash scripts/check-overlay-live-facts.sh\n")' \
	'in_aggregate(taskfile, "        bash scripts/check-overlay-live-facts.sh\n") and "        echo ok # harmless\n" in taskfile'

echo
echo "== non-firing controls — data and plain redirection must not move the verdict =="

permits singlequoted "single-quoted text naming the live reader is data, not an execution" \
	'taskfile = add_to_aggregate(taskfile, "      - echo \x27scripts/check-overlay-live-facts.sh\x27\n")' \
	'in_aggregate(taskfile, "      - echo \x27scripts/check-overlay-live-facts.sh\x27\n")'

permits ordinary_redirection "descriptor and file redirection is not an execution" \
	'taskfile = add_to_aggregate(taskfile, "      - bash scripts/check-tier-card.sh >/dev/null 2>&1\n")' \
	'in_aggregate(taskfile, "      - bash scripts/check-tier-card.sh >/dev/null 2>&1\n")'

permits static_redirect_target "a plain pathname target denotes a file, not an execution" \
	'taskfile = add_to_aggregate(taskfile, "      - echo ok > scripts/check-overlay-live-facts.sh\n")' \
	'in_aggregate(taskfile, "      - echo ok > scripts/check-overlay-live-facts.sh\n")'

echo
echo "== blindness controls — a judge that cannot see must not report clean =="

echo "-- inputs it cannot read --"
unreadable() { # <label> <hook> <taskfile>
	local rc
	judge "$2" "$3"
	rc=$?
	case "$(classify 2 "$rc")" in
	pass) ok "$1 (rc=2)" ;;
	finding)
		bad "$1: judge answered rc=$rc instead of COULD NOT LOOK"
		sed 's/^/      /' "$WORK/judge.out" >&2
		;;
	*)
		unexamined "$1: judge answered rc=$rc"
		sed 's/^/      /' "$WORK/judge.out" >&2
		;;
	esac
}
unreadable "a hook path that does not exist" "$WORK/there-is-no-such-hook" "$TASKFILE"
unreadable "a Taskfile path that is a directory" "$HOOK" "$WORK"

echo "-- malformed and absent subjects --"

refuses badyaml "an unparseable Taskfile" \
	'taskfile = taskfile + "\n  - unbalanced: [\n"' \
	'taskfile.endswith("unbalanced: [\n")'

refuses notasks "a Taskfile whose tasks: is not a mapping" \
	'taskfile = "version: \"3\"\ntasks: []\n"' \
	'taskfile.count("\n") == 2'

refuses noaggregate "a Taskfile with no lint:addon-sets task" \
	'taskfile = once(taskfile, "\n  lint:addon-sets:\n", "\n  lint:addon-sets-renamed:\n")' \
	'"\n  lint:addon-sets:\n" not in taskfile'

refuses missingdep "a judged task depending on a task that does not exist" \
	'taskfile = once(taskfile, AGGREGATE_HEAD, "\n  lint:addon-sets:\n    deps: [lint:this-task-does-not-exist]\n    desc: >-\n")' \
	'"deps: [lint:this-task-does-not-exist]" in taskfile'

echo "-- Task and command fields this judge does not model --"

refuses taskstatus "a reached task carrying status:, which can suppress its commands" \
	'taskfile = once(taskfile, AGGREGATE_HEAD, "\n  lint:addon-sets:\n    status: [test -f /nonexistent]\n    desc: >-\n")' \
	'"    status: [test -f /nonexistent]\n" in taskfile'

refuses taskdir "a reached task carrying dir:, which changes where its commands run" \
	'taskfile = once(taskfile, LIVE_CMD, LIVE_CMD.rstrip("\n") + "\n    dir: /tmp\n")' \
	'"    dir: /tmp\n" in taskfile'

refuses cmdattribute "a command map carrying an execution attribute beside cmd:" \
	'taskfile = add_to_aggregate(taskfile, "      - cmd: bash scripts/check-tier-card.sh\n        platforms: [linux]\n")' \
	'"        platforms: [linux]\n" in taskfile'

refuses depvars "a delegation map carrying vars: beside task:" \
	'taskfile = add_to_aggregate(taskfile, "      - task: lint:overlay-live-facts\n        vars: {WHY: because}\n")' \
	'"        vars: {WHY: because}\n" in taskfile'

echo "-- executable forms this judge does not model --"

refuses envbash "the live reader run through env, which this judge does not resolve" \
	'taskfile = add_to_aggregate(taskfile, "      - env bash scripts/check-overlay-live-facts.sh\n")' \
	'"      - env bash scripts/check-overlay-live-facts.sh\n" in taskfile'

refuses commandbash "the live reader run through command, which this judge does not resolve" \
	'taskfile = add_to_aggregate(taskfile, "      - command bash scripts/check-overlay-live-facts.sh\n")' \
	'"      - command bash scripts/check-overlay-live-facts.sh\n" in taskfile'

# A leading assignment is not harmless because it is stripped: its substitution runs first.
refuses assign_prefix "the live reader in a stripped assignment prefix" \
	'taskfile = add_to_aggregate(taskfile, "      - CAPTURE=\"$(scripts/check-overlay-live-facts.sh)\" echo ok\n")' \
	'in_aggregate(taskfile, "      - CAPTURE=\"$(scripts/check-overlay-live-facts.sh)\" echo ok\n")'

refuses assign_only "the live reader in a command that is only an assignment" \
	'taskfile = add_to_aggregate(taskfile, "      - CAPTURE=\"$(scripts/check-overlay-live-facts.sh)\"\n")' \
	'in_aggregate(taskfile, "      - CAPTURE=\"$(scripts/check-overlay-live-facts.sh)\"\n")'

# The substitution runs before the shell opens anything, so a later redirection failure
# would not undo it.
refuses redirect_subst "the live reader inside an unquoted redirection target" \
	'taskfile = add_to_aggregate(taskfile, "      - echo ok >$(scripts/check-overlay-live-facts.sh)\n")' \
	'in_aggregate(taskfile, "      - echo ok >$(scripts/check-overlay-live-facts.sh)\n")'

refuses substarg "the live reader inside a double-quoted command substitution" \
	'taskfile = add_to_aggregate(taskfile, "      - bash scripts/check-tier-card.sh \"$(scripts/check-overlay-live-facts.sh)\"\n")' \
	'"$(scripts/check-overlay-live-facts.sh)" in taskfile'

refuses hiddenexec "a live read hidden behind an unresolvable bash -c" \
	'taskfile = add_to_aggregate(taskfile, "      - bash -c \x27scripts/check-overlay-live-facts.sh\x27\n")' \
	'"bash -c" in taskfile'

refuses shortwrapper "the scoped wrapper invoked without its delegated task" \
	'taskfile = once(taskfile, "        commercial/module-slug-package.json)\" lint:addon-sets-gate:legs\n", "        commercial/module-slug-package.json)\"\n")' \
	'"lint:addon-sets-gate:legs\n" not in taskfile'

refuses nocalls "a hook whose task invocations are all commented out" \
	'hook = "".join("#" + l if "task " in l and not l.lstrip().startswith("#") else l for l in hook.splitlines(True))' \
	'"\ntask lint:overlay-live-facts\n" not in hook'

refuses expansion "a hook invoking task through an unresolvable expansion" \
	'hook = once(hook, "\ntask lint:overlay-live-facts\n", "\ntask ${leg}\n")' \
	'"\ntask ${leg}\n" in hook'

refuses unsupportedflag "a hook handing task an option this judge does not model" \
	'hook = once(hook, "\ntask lint:overlay-live-facts\n", "\ntask --dry lint:overlay-live-facts\n")' \
	'"\ntask --dry lint:overlay-live-facts\n" in hook'

refuses unmodelledform "a judged task named by a command form this judge does not model" \
	'hook = once(hook, "\ntask lint:overlay-live-facts\n", "\nxargs task lint:overlay-live-facts\n")' \
	'"\nxargs task lint:overlay-live-facts\n" in hook'

refuses hooksubst "a judged task named inside a hook command substitution" \
	'hook = once(hook, "\ntask lint:overlay-live-facts\n", "\nOGW=\"$(task lint:overlay-live-facts)\"\n")' \
	'"OGW=\"$(task lint:overlay-live-facts)\"" in hook'

refuses hook_redirect_subst "a judged task named inside a hook redirection target" \
	'hook = once(hook, "\ntask lint:overlay-live-facts\n", "\necho ok >$(task lint:overlay-live-facts)\n")' \
	'"\necho ok >$(task lint:overlay-live-facts)\n" in hook'

refuses heredoc "a judged task named inside an embedded document" \
	'hook = once(hook, "\ntask lint:overlay-live-facts\n", "\ncat <<\x27OGW\x27\ntask lint:overlay-live-facts\nOGW\n")' \
	'"cat <<\x27OGW\x27" in hook'

echo
echo "== exact-candidate facts battery — orphaned, skipped or echo-only is never wired =="

cand_expect removed "orphaned: the candidate battery leg is removed" 1 \
	'taskfile = once(taskfile, CAND_CMD, "")' \
	'CAND_CMD not in taskfile'

cand_expect echoed "echo-only: the leg prints the battery instead of running it" 1 \
	'taskfile = once(taskfile, CAND_CMD, "      - echo scripts/test-overlay-candidate-facts.sh\n")' \
	'CAND_CMD not in taskfile and count(taskfile, "      - echo scripts/test-overlay-candidate-facts.sh\n") == 1'

cand_expect commented "echo-only: the leg is a comment string" 1 \
	'taskfile = once(taskfile, CAND_CMD, "      - \"# bash scripts/hub-leg.sh lint:addon-sets-gate scripts/test-overlay-candidate-facts.sh\"\n")' \
	'CAND_CMD not in taskfile and "\"# bash scripts/hub-leg.sh lint:addon-sets-gate scripts/test-overlay-candidate-facts.sh\"" in taskfile'

cand_expect swallowed "skipped: the leg swallows the battery exit with || true" 1 \
	'taskfile = once(taskfile, CAND_CMD, CAND_CMD.rstrip("\n") + " || true\n")' \
	'CAND_CMD not in taskfile and count(taskfile, CAND_CMD.rstrip("\n") + " || true\n") == 1'

cand_expect moved "orphaned from the battery: the leg moves to another task" 1 \
	'taskfile = once(taskfile, CAND_CMD, "")
taskfile = once(taskfile, SELFTEST_CMD, SELFTEST_CMD + CAND_CMD)' \
	'count(taskfile, CAND_CMD) == 1 and pos(taskfile, CAND_CMD) == pos(taskfile, SELFTEST_CMD) + len(SELFTEST_CMD)'

cand_expect status "skipped: a status: condition on the battery task is could-not-look, never wired" 2 \
	'taskfile = once(taskfile, LEGS_HEAD, "\n  lint:addon-sets-gate:legs:\n    status: [test -f /nonexistent]\n    desc: >-\n")' \
	'"  lint:addon-sets-gate:legs:\n    status: [test -f /nonexistent]\n" in taskfile'

cand_expect noheader "blind: the battery task is renamed away" 2 \
	'taskfile = once(taskfile, "\n  lint:addon-sets-gate:legs:\n", "\n  lint:addon-sets-gate:legs-renamed:\n")' \
	'"\n  lint:addon-sets-gate:legs:\n" not in taskfile'

cand_expect reordered "data: the leg moved to the head of the same list stays wired" 0 \
	'taskfile = once(taskfile, CAND_CMD, "")
taskfile = once(taskfile, LEGS_HEAD, LEGS_HEAD)
cut = taskfile.index("    cmds:\n", taskfile.index(LEGS_HEAD)) + len("    cmds:\n")
taskfile = taskfile[:cut] + CAND_CMD + taskfile[cut:]' \
	'count(taskfile, CAND_CMD) == 1 and pos(taskfile, CAND_CMD) == pos(taskfile, "    cmds:\n", 1 + taskfile[:pos(taskfile, LEGS_HEAD)].count("    cmds:\n")) + len("    cmds:\n")'

echo
echo "== harness controls — the structural fixture insertion boundary =="
# These grade THIS BENCH'S OWN SETUP. No row below reports a judge verdict or a product
# outcome, and none is counted as one.
#
# The insertion point is located structurally, so the step that can now fail quietly is
# the LOCATION. Two ways it can lie, and both are graded here: it can accept a subject
# whose shape it does not actually support — and then a control's mutant tests something
# other than what its label says while the bench still prints a real status — or it can
# drift to a different list when a supported subject changes anywhere else. Refusal must
# therefore happen BEFORE any fixture byte is written, which is what "no mutant" means in
# these labels.

RESHAPE="$WORK/reshape.py"
cat >"$RESHAPE" <<'PY'
"""Reshape one fixture Taskfile so the insertion boundary can be exercised.

argv: <taskfile path> <transform source>. The transform runs over the text variable
`taskfile`, with HEADER and CMDS bound to the two field literals the builder locates. It
writes an unsupported or an otherwise altered subject ON PURPOSE and judges nothing.
"""
import io
import re
import sys

path, transform = sys.argv[1:3]
HEADER = "  lint:addon-sets:\n"
CMDS = "    cmds:\n"
taskfile = io.open(path, encoding="utf-8").read()
exec(transform)
io.open(path, "w", encoding="utf-8").write(taskfile)
PY

PLACED="$WORK/placed.py"
cat >"$PLACED" <<'PY'
"""Assert that the injected item heads the aggregate's own command list.

argv: <taskfile before the insertion> <taskfile after it>. It re-derives the task and the
list from the text instead of trusting the offsets the builder returned, so a builder that
reports a location it did not use cannot pass this control.
"""
import io
import re
import sys

ITEM = "      - echo harness-injected\n"
CMDS = "    cmds:\n"
TAILS = ("      - echo harness-tail-one\n", "      - echo harness-tail-two\n")


def fail(message):
    sys.stderr.write("placed: %s\n" % message)
    raise SystemExit(1)


def block(text):
    start = text.index("\n  lint:addon-sets:\n") + 1
    after = re.compile(r"^  \S", re.M).search(text, text.index("\n", start) + 1)
    return start, (after.start() if after else len(text))


before = io.open(sys.argv[1], encoding="utf-8").read()
after = io.open(sys.argv[2], encoding="utf-8").read()

if after.count(ITEM) != 1:
    fail("the injected item occurs %d time(s), expected exactly 1" % after.count(ITEM))
at = after.index(ITEM)
bstart, bend = block(after)
if not bstart < at < bend:
    fail("the injected item is outside the lint:addon-sets task")
head = after.index(CMDS, bstart) + len(CMDS)
if at != head:
    fail("the injected item is not the first entry of the command list")
if after[:at] + after[at + len(ITEM):] != before:
    fail("the insertion did not preserve every other byte of the subject")
for tail in TAILS:
    if after.count(tail) != 1:
        fail("the changed command tail is not intact: %r" % tail)
    if after.index(tail) < at:
        fail("the injected item did not land ahead of the changed tail: %r" % tail)
obstart, obend = block(before)
for line in re.findall(r"^      - .*$", before[obstart:obend], re.M):
    if after.count(line + "\n") != before.count(line + "\n"):
        fail("an original command of the task changed: %r" % line)
print("placed: the injected item heads the intended command list and the tail is intact")
PY

# The item every harness control tries to insert. It names no repository path: what these
# rows grade is where an item lands, not what a command does.
HARNESS_ITEM='taskfile = add_to_aggregate(taskfile, "      - echo harness-injected\n")'
HARNESS_POST='in_aggregate(taskfile, "      - echo harness-injected\n")'

harness_fixture() { # <slug> <reshape transform> -> prints the directory
	local dir="$WORK/harness-$1"
	rm -rf "$dir"
	mkdir -p "$dir" || return 1
	cp "$HOOK" "$dir/pre-push" || return 1
	cp "$TASKFILE" "$dir/Taskfile.yml" || return 1
	python3 "$RESHAPE" "$dir/Taskfile.yml" "$2" || return 1
	cp "$dir/pre-push" "$dir/pre-push.subject" || return 1
	cp "$dir/Taskfile.yml" "$dir/Taskfile.yml.subject" || return 1
	printf '%s\n' "$dir"
}

untouched() { # <dir> -> the fixture still holds the subject that was prepared
	cmp -s "$1/pre-push" "$1/pre-push.subject" && cmp -s "$1/Taskfile.yml" "$1/Taskfile.yml.subject"
}

setup_refuses() { # <slug> <label> <reshape transform> <expected cause>
	local dir out rc
	dir="$(harness_fixture "$1" "$3")" || {
		unexamined "harness, fixture setup: $2 — the subject could not be prepared"
		return
	}
	out="$dir/mutate.out"
	python3 "$MUTATE" "$dir/pre-push" "$dir/Taskfile.yml" "$HARNESS_ITEM" "$HARNESS_POST" >"$out" 2>&1
	rc=$?
	if [ "$rc" -eq 0 ]; then
		bad "harness, fixture setup: $2 — the builder accepted an unsupported subject"
		return
	fi
	if ! grep -qF -- "$4" "$out"; then
		bad "harness, fixture setup: $2 — it refused, but not for the expected cause"
		printf '      wanted: %s\n' "$4" >&2
		sed 's/^/      /' "$out" >&2
		return
	fi
	if ! untouched "$dir"; then
		bad "harness, fixture setup: $2 — it refused and wrote a mutant anyway"
		return
	fi
	ok "harness, fixture setup: $2 (refused, no mutant)"
}

setup_places() { # <slug> <label> <reshape transform>
	local dir out
	dir="$(harness_fixture "$1" "$3")" || {
		unexamined "harness, fixture setup: $2 — the subject could not be prepared"
		return
	}
	out="$dir/mutate.out"
	if ! python3 "$MUTATE" "$dir/pre-push" "$dir/Taskfile.yml" "$HARNESS_ITEM" "$HARNESS_POST" >"$out" 2>&1; then
		bad "harness, fixture setup: $2 — the builder refused a supported subject"
		sed 's/^/      /' "$out" >&2
		return
	fi
	if python3 "$PLACED" "$dir/Taskfile.yml.subject" "$dir/Taskfile.yml" >>"$out" 2>&1; then
		ok "harness, fixture setup: $2"
	else
		bad "harness, fixture setup: $2 — the item did not land in the intended list"
		sed 's/^/      /' "$out" >&2
	fi
}

setup_refuses noheader "no lint:addon-sets task header" \
	'taskfile = taskfile.replace(HEADER, "  lint:addon-sets-absent:\n", 1)' \
	"aggregate: expected exactly 1 task header '  lint:addon-sets:', found 0"

setup_refuses twoheaders "two lint:addon-sets task headers" \
	'taskfile = taskfile.replace(HEADER, HEADER + "    desc: a second header\n" + HEADER, 1)' \
	"aggregate: expected exactly 1 task header '  lint:addon-sets:', found 2"

setup_refuses nocmds "the task carries no cmds field" \
	'cut = taskfile.index(CMDS, taskfile.index(HEADER))
taskfile = taskfile[:cut] + "    cmds-absent:\n" + taskfile[cut + len(CMDS):]' \
	"aggregate: expected exactly 1 field '    cmds:' in the task, found 0"

setup_refuses twocmds "the task carries two cmds fields" \
	'cut = taskfile.index(CMDS, taskfile.index(HEADER))
taskfile = taskfile[:cut] + CMDS + "      - echo harness-second-list\n" + taskfile[cut:]' \
	"aggregate: expected exactly 1 field '    cmds:' in the task, found 2"

# The supported half. The tail of this task's command list is where an unrelated change
# most plausibly lands, and the old anchor sat in the middle of that list: this asserts
# the item still heads THIS list, with the changed tail behind it and every original
# command intact.
setup_places tailchange "a supported subject with a changed command tail" \
	'start = taskfile.index(HEADER)
after = re.compile(r"^  \S", re.M).search(taskfile, taskfile.index("\n", start) + 1)
end = after.start() if after else len(taskfile)
block = taskfile[start:end]
cut = start + block.index("\n", block.rindex("\n      - ") + 1) + 1
taskfile = taskfile[:cut] + "      - echo harness-tail-one\n      - echo harness-tail-two\n" + taskfile[cut:]'

# ── the whole-bench boundary ──────────────────────────────────────────────────────────
# The controls above judge the tool. These judge this program's own exit: a built,
# runnable judge answering 0, 1 and 2 over real inputs must leave the bench at 0, 1 and 2.
# A child invocation does not recurse into this section.
if [ "${OLIVARES_OGW_CHILD:-}" != "1" ]; then
	echo
	echo "== whole-bench boundary — the bench's exit is the judge's verdict =="

	# The child gets this same program, the shared helper, both judged files and a link to
	# the real module, so its judge really builds and really runs.
	build_child() { # <slug> <mutation> <postcondition> -> prints the child tree
		local dir="$WORK/self-$1"
		rm -rf "$dir"
		mkdir -p "$dir/scripts/lib" "$dir/.githooks" || return 1
		cp "$SELF" "$dir/scripts/$NAME.sh" &&
			cp "$ROOT/scripts/lib/exec-workdir.sh" "$dir/scripts/lib/exec-workdir.sh" &&
			cp "$HOOK" "$dir/.githooks/pre-push" &&
			cp "$TASKFILE" "$dir/Taskfile.yml" &&
			ln -s "$ROOT/cmd" "$dir/cmd" || return 1
		if [ -n "$2" ] &&
			! python3 "$MUTATE" "$dir/.githooks/pre-push" "$dir/Taskfile.yml" "$2" "$3"; then
			return 1
		fi
		printf '%s\n' "$dir"
	}

	run_child() { # <dir> <output file>; returns the child bench's own status
		OLIVARES_OGW_CHILD=1 bash "$1/scripts/$NAME.sh" >"$2" 2>&1
	}

	account_child() { # <label> <wanted exit> <actual exit> <output file>
		case "$(classify "$2" "$3")" in
		pass) ok "$1 (child exit=$3)" ;;
		finding)
			bad "$1: the child bench exited $3, wanted $2"
			sed 's/^/      /' "$4" >&2
			;;
		*)
			unexamined "$1: the child bench exited $3, wanted $2"
			sed 's/^/      /' "$4" >&2
			;;
		esac
	}

	selfbench() { # <slug> <label> <wanted exit> <mutation> <postcondition>
		local dir rc out="$WORK/self-$1.out"
		dir="$(build_child "$1" "$4" "$5")" || {
			unexamined "$2: could not build the child tree, so this boundary was not judged"
			return
		}
		run_child "$dir" "$out"
		rc=$?
		account_child "$2" "$3" "$rc" "$out"
	}

	selfbench clean "an unmodified tree leaves the bench at 0" 0 "" ""

	selfbench finding "a real-source finding leaves the bench at 1" 1 \
		'hook = once(hook, "\ntask lint:overlay-live-facts\n", "\n")' \
		'count(hook, "\ntask lint:overlay-live-facts\n") == 0'

	selfbench blind "unsupported real source leaves the bench at 2, not 1" 2 \
		'taskfile = add_to_aggregate(taskfile, "      - env bash scripts/check-overlay-live-facts.sh\n")' \
		'"      - env bash scripts/check-overlay-live-facts.sh\n" in taskfile'

	selfbench candidate_orphan "a real Taskfile that drops the candidate battery leaves the bench at 1" 1 \
		'taskfile = once(taskfile, CAND_CMD, "")' \
		'CAND_CMD not in taskfile'

	# This real unreadable-Taskfile fixture intentionally expects 2. Other unexpected
	# statuses must remain unexamined at the same accounting boundary as every child.
	child_blind_case() {
		local dir rc out="$WORK/self-blindchild.out"
		dir="$(build_child blindchild "" "")" || {
			unexamined "a child that cannot read its Taskfile: could not build the child tree"
			return
		}
		rm -f "$dir/Taskfile.yml"
		run_child "$dir" "$out"
		rc=$?
		account_child "a real child that cannot read its Taskfile refuses" 2 "$rc" "$out"
	}
	child_blind_case

	# Inject statuses into the actual shared accounting function, with subshell counters
	# isolated from this bench. These verify bookkeeping, not another product execution.
	child_accounting_case() { # <label> <wanted> <injected status> <expected counters>
		local counts
		counts=$(
			pass=0 fail=0 unexam=0
			account_child "$1" "$2" "$3" /dev/null >/dev/null 2>&1
			printf '%s %s %s' "$pass" "$fail" "$unexam"
		)
		if [ "$counts" = "$4" ]; then
			ok "harness, injected child accounting: $1"
		else
			bad "harness, injected child accounting: $1 counted $counts, wanted $4"
		fi
	}
	child_accounting_case "an expected refusal passes" 2 2 "1 0 0"
	child_accounting_case "a wrong clean result is a finding" 2 0 "0 1 0"
	child_accounting_case "a wrong finding is a finding" 0 1 "0 1 0"
	child_accounting_case "an unexpected refusal is unexamined" 0 2 "0 0 1"
	child_accounting_case "a signal in the refusal control is unexamined" 2 143 "0 0 1"
	child_accounting_case "an unknown status in the refusal control is unexamined" 2 42 "0 0 1"

	# Harness controls for the classifier itself. The right-hand statuses here are INJECTED
	# into `classify`, not produced by any judge: a child cannot be made to exit 42 or be
	# killed without editing the program under test. They assert the mapping and nothing
	# else; no injected value is reported anywhere as a product execution result.
	classifier_case() { # <label> <wanted> <injected status> <expected verdict>
		local got
		got="$(classify "$2" "$3")"
		if [ "$got" = "$4" ]; then
			ok "harness, injected status: $1"
		else
			bad "harness, injected status: $1 classified $got, wanted $4"
		fi
	}
	classifier_case "an intentionally expected 2 passes on 2" 2 2 pass
	classifier_case "a child wanted clean that finds is a finding" 0 1 finding
	classifier_case "a child wanted finding that is clean is a finding" 1 0 finding
	classifier_case "a child wanted refusing that is clean is a finding" 2 0 finding
	classifier_case "a child wanted clean that goes blind is unexamined" 0 2 unexamined
	classifier_case "a child wanted finding that goes blind is unexamined" 1 2 unexamined
	classifier_case "a child killed by a signal is unexamined" 0 137 unexamined
	classifier_case "any other status is unexamined" 1 42 unexamined
fi

echo
echo "$NAME: $pass passed, $fail failed, $unexam not examined"
# An unexamined dimension outranks a failure: this bench cannot claim it looked.
[ "$unexam" -eq 0 ] || exit 2
[ "$fail" -eq 0 ] || exit 1
exit 0
