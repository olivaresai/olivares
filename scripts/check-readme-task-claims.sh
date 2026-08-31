#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-readme-task-claims.sh — a README that says CI walks a path must name a path CI walks.
#
# ============ WHY THIS EXISTS ===============================================================
# The seven READMEs we publish said, each on its line 70:
#
#     "CI walks the same path: `task smoke:quickstart` boots this demo estate against the real
#      binary and asserts its access-map and drift counts."
#
# `.github/` contained ZERO references to smoke:quickstart. The task existed and worked; it was
# simply never wired, so the quickstart's install→value path and its drift numbers were watched
# by nothing. Radius ×7, not ×1, because the six translations carry the same sentence.
#
# ⛔ AND THE NUMBERS WERE TRUE. That is what makes this class hard to see: nothing was wrong on
# the page, so no reader and no reviewer had anything to catch. What was false is that anything
# was keeping them true — a promise about the FUTURE, which only a gate can hold.
#
# ============ WHAT IT CHECKS, AND WHY NOT "EVERY TASK" ======================================
# Not every `task X` in a README is a promise. README.md:148 says "Build with `task build`" —
# an instruction to the reader, and `task build` is deliberately not a CI job. Demanding CI run
# every task a README mentions would be a rule that is wrong about half its cases.
#
# The distinction is in the sentence, so that is where it is read from: a task named on a line
# that mentions CI is claimed to be WATCHED. Nothing is declared here by hand — the canonical
# English README is the source, and the six translations are held to the same task SET, which
# also catches a translation that quietly drops the promise or invents another.
#
#   1 · every task named in any shipped README exists in the Taskfile  (rot, either direction)
#   2 · every task claimed as CI-watched appears in .github/workflows/ (EXACT token, never a
#       prefix: "task build" matches "task build:bin" as a substring, and that near-miss is how
#       this very gate first reported a task as wired when it was not)
#   3 · the six translations name the same task set as README.md
#
# Three answers, never two: CLEAN / BROKEN / UNVERIFIED.
set -eu
cd "$(dirname "$0")/.."

RTC_SELFTEST=0
[ "${1:-}" = "--selftest" ] && RTC_SELFTEST=1
export RTC_SELFTEST

python3 - <<'PY'
import glob, os, re, subprocess, sys

SELFTEST = os.environ.get("RTC_SELFTEST") == "1"

TASK_IN_PROSE = re.compile(r"`task ([a-z0-9][a-z0-9:_-]*)`")
# The claim marker. English only, deliberately: the canonical README is the source and the
# translations are held to its SET, so this never has to guess how seven languages say "CI".
CI_LINE = re.compile(r"\bCI\b")


def unverified(msg):
    print(f"UNVERIFIED check-readme-task-claims: {msg}")
    sys.exit(2)


def read(path):
    with open(path, encoding="utf-8") as fh:
        return fh.read()


def claims(text):
    """-> (all_tasks, ci_watched) named in one README."""
    every, watched = set(), set()
    for line in text.splitlines():
        found = set(TASK_IN_PROSE.findall(line))
        every |= found
        if CI_LINE.search(line):
            watched |= found
    return every, watched


def taskfile_names():
    """The tasks the Taskfile actually defines. `task --list-all` is the authority; parsing YAML
    by hand would disagree with it the first time an include appears."""
    # ⛔ EL COLOR DEL ENTORNO ROMPÍA ESTE PARSER, Y EL FALLO ERA SILENCIOSO Y TOTAL. Medido el
    #    2026-08-26: con `FORCE_COLOR=3` en el entorno —que es lo que hay en las sesiones de esta
    #    caja— `task` colorea AUNQUE escriba a una tubería, y cada línea sale como
    #    `\x1b[0m\x1b[33m* \x1b[0m\x1b[32mbench\x1b[0m\x1b[0m:`. El `\*` del patrón no casa con eso,
    #    así que `names` quedaba VACÍO y el gate contestaba UNVERIFIED — es decir, tumbaba el push
    #    de cualquiera que tuviera color forzado, y por una razón que no era suya.
    #
    #    Se apagan las tres variables Y se limpia el ANSI de la salida. Las dos cosas: apagarlas es
    #    lo correcto, pero un `task` futuro podría colorear por otro motivo, y un parser que sólo
    #    funciona cuando su entorno coopera es la misma clase de defecto que un requisito escrito
    #    en un comentario.
    _env = dict(os.environ)
    for _v in ("FORCE_COLOR", "CLICOLOR_FORCE", "CLICOLOR"):
        _env.pop(_v, None)
    _env["NO_COLOR"] = "1"
    _env["TERM"] = "dumb"
    try:
        out = subprocess.run(["task", "--list-all"], capture_output=True, text=True, timeout=120, env=_env)
    except (OSError, subprocess.SubprocessError) as e:
        unverified(f"could not run `task --list-all`: {e}")
    if out.returncode != 0:
        unverified(f"`task --list-all` exited {out.returncode}; cannot know what exists")
    _ansi = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")
    names = set()
    for line in out.stdout.splitlines():
        m = re.match(r"\*\s+([a-z0-9][a-z0-9:_-]*):", _ansi.sub("", line).strip())
        if m:
            names.add(m.group(1))
    if not names:
        unverified("`task --list-all` named no tasks; refusing to certify against an empty set")
    return names


def strip_comments(text):
    """Drop what is PROSE ABOUT a command rather than the command.

    Two kinds, and both were measured lying to this gate:

      · a line whose first non-space character is `#` — a YAML comment, or a shell comment inside
        a `run:` block. The step that wires the quickstart smoke carries one quoting the README
        sentence verbatim, backticked task name and all, so the claim matched the COMMENT and
        deleting the actual `run:` line left the gate green.
      · a `name:` field. A step's display name is written for a human: `- name: quickstart
        contract drill (task smoke:quickstart)` names the task in prose, and with only comments
        stripped, moving the real `run:` elsewhere still passed. Measured by mutation on
        2026-08-14: the mutant SURVIVED until this line existed.

    Both are the same defect — a gate that cannot tell a command from a sentence about one.
    """
    out = []
    for ln in text.splitlines():
        stripped = ln.lstrip()
        if stripped.startswith("#"):
            continue
        if re.match(r"-?\s*name:\s", stripped):
            continue
        # ...and the content of DOUBLE-QUOTED strings, which is the third form and the one that
        # taught the rule. An invocation is not inside quotes; prose in a shell string is — the
        # issue title `open_or_update "quickstart" "Quickstart contract drill (task
        # smoke:quickstart, ...)"` kept the gate green with the real `run:` line deleted.
        # Enumerating kinds of prose was losing; removing what CANNOT be a command wins.
        out.append(re.sub(r'"[^"\n]*"', '""', ln))
    return "\n".join(out)


AUTO_TRIGGERS = ("push", "pull_request", "pull_request_target", "schedule")


def auto_triggered(path):
    """Does this workflow run WITHOUT a human pressing a button?

    ⛔ THIS GATE CERTIFIED A CLAIM THAT ONLY A MANUAL DISPATCH SATISFIED. It matched the task name
    against the text of every workflow and never once looked at the `on:` block. mainline-ci.yml is
    `on: workflow_dispatch` and NOTHING ELSE — so "CI walks the same path" was reported as wired
    while the path was walked only when an integrator remembered to dispatch it. A reader of that
    sentence does not hear "when somebody presses a button".
    """
    try:
        src = read(path)
    except OSError:
        return False
    m = re.search(r"^on:\s*\n((?:[ \t]+.*\n|\n)*)", src, re.M)
    if not m:
        return False
    return any(re.search(rf"^\s{{2}}{t}:", m.group(1), re.M) for t in AUTO_TRIGGERS)


def workflow_text():
    """Every AUTOMATICALLY TRIGGERED workflow, with COMMENT LINES REMOVED.

    ⛔ THIS GATE FIRST REPORTED CLEAN FROM ITS OWN PROSE. The CI step that wires the quickstart
    smoke carries a comment quoting the README sentence verbatim — backticked task name and all —
    so `runs_exactly` matched the COMMENT and the gate certified a claim that nothing ran. Deleting
    the actual `run:` line left it green.

    A gate that cannot tell a command from a sentence about a command measures nothing, and the
    reassuring direction is the one it fails in. A line whose first non-space character is `#` is a
    comment in YAML and also a comment inside a `run:` block scalar, so one rule covers both."""
    files = sorted(glob.glob(".github/workflows/*.yml")) + sorted(glob.glob(".github/workflows/*.yaml"))
    if not files:
        unverified("no workflow files under .github/workflows; cannot know what CI runs")
    auto = [f for f in files if auto_triggered(f)]
    if not auto:
        unverified("no workflow has an automatic trigger; nothing here runs without a human")
    return strip_comments("\n".join(read(f) for f in auto))


def runs_exactly(wf, task):
    """`task build` must not be satisfied by `task build:bin`.

    The token ends at whitespace, end of line, a shell operator or a redirect — anything that
    is not a legal continuation of a task name."""
    return re.search(r"task\s+" + re.escape(task) + r"(?![a-z0-9:_-])", wf) is not None


def selftest():
    ok = True

    def expect(name, cond):
        nonlocal ok
        print(("selftest ok: " if cond else "selftest FAIL: ") + name)
        ok = ok and cond

    every, watched = claims(
        "CI walks the same path: `task smoke:quickstart` boots the estate.\n"
        "Build with `task build` (Go 1.26+).\n"
    )
    expect("a task on a CI line is a WATCHED claim", watched == {"smoke:quickstart"})
    expect("a task in an instruction is NOT a claim", every == {"smoke:quickstart", "build"})

    wf = "        run: task build:bin\n        run: task smoke:examples\n"
    expect("a PREFIX does not satisfy the claim (build vs build:bin)", not runs_exactly(wf, "build"))
    expect("the exact token does satisfy it", runs_exactly(wf, "build:bin"))
    expect("end of line ends the token", runs_exactly("run: task smoke:examples", "smoke:examples"))
    expect("a pipe ends the token", runs_exactly("run: task smoke:x 2>&1 | tee log", "smoke:x"))
    expect("a longer sibling does not satisfy a shorter claim",
           not runs_exactly("run: task smoke:examples", "smoke:example"))
    # The one that caught this gate lying about itself.
    commented = "      # CI walks the same path: `task smoke:quickstart`\n      run: task smoke:examples\n"
    expect("a COMMENT quoting the claim does NOT satisfy it",
           not runs_exactly(strip_comments(commented), "smoke:quickstart"))
    expect("stripping comments leaves the real command", runs_exactly(strip_comments(commented), "smoke:examples"))
    named = "      - name: quickstart drill (task smoke:quickstart)\n        run: task smoke:examples\n"
    expect("a step NAME quoting the claim does NOT satisfy it either",
           not runs_exactly(strip_comments(named), "smoke:quickstart"))
    expect("and the real command under that name still counts",
           runs_exactly(strip_comments(named), "smoke:examples"))
    quoted = '        run: report "a drill (task smoke:quickstart) failed" && task smoke:examples\n'
    expect("a task named inside a QUOTED STRING does not satisfy the claim",
           not runs_exactly(strip_comments(quoted), "smoke:quickstart"))
    expect("the command outside the quotes still does",
           runs_exactly(strip_comments(quoted), "smoke:examples"))
    print("selftest " + ("OK — every red case is red, every green case is green" if ok else "FAILED"))
    sys.exit(0 if ok else 1)


if SELFTEST:
    selftest()

canonical = "README.md"
if not os.path.isfile(canonical):
    unverified("README.md is absent; there is no canonical claim set to check")
translations = sorted(glob.glob("README.*.md"))

every_canon, watched = claims(read(canonical))
defined = taskfile_names()
wf = workflow_text()

problems = []

for task in sorted(watched):
    if not runs_exactly(wf, task):
        problems.append(
            f"{canonical}: claims CI walks `task {task}`, and no workflow under .github/workflows/ "
            f"runs it. The claim is about the FUTURE — either wire it, or stop promising it."
        )

everything = set(every_canon)
for path in translations:
    every_t, _ = claims(read(path))
    everything |= every_t
    if every_t != every_canon:
        missing = sorted(every_canon - every_t)
        extra = sorted(every_t - every_canon)
        detail = []
        if missing:
            detail.append(f"drops {', '.join(missing)}")
        if extra:
            detail.append(f"invents {', '.join(extra)}")
        problems.append(f"{path}: names a different task set than {canonical} — {'; '.join(detail)}")

for task in sorted(everything):
    if task not in defined:
        problems.append(f"a shipped README names `task {task}`, which the Taskfile does not define")

if problems:
    print(f"BROKEN check-readme-task-claims: {len(problems)} problem(s):")
    for p in problems:
        print(f"  · {p}")
    sys.exit(1)

print(
    f"OK check-readme-task-claims: {len(watched)} CI-watched claim(s) are wired, "
    f"{len(everything)} task(s) named across {1 + len(translations)} README(s) exist, "
    f"and the translations name the same set"
)
PY
