#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-community-cockpit-strings.sh — a community (or any non-`ids`) artifact carries
# the session-cockpit literals of scripts/community-session-cockpit-strings.allow (eight
# today) and no trace of the commercial engine.
#
# THE PROPERTY. Such an artifact registers an AGPL placeholder that mounts one route and
# answers 501 session_cockpit_unavailable, so it MUST contain cockpit strings: "no
# cockpit string may appear" is false by construction, which is why this is an
# ALLOWLIST. Contract: docs/contracts/COCKPIT-07-edition-cut.md §7.
#
# The count lives in ONE place, the .allow file, and this header deliberately does not
# repeat it: an earlier version said "five" in three places and two of them went stale
# the day the placeholder gained an sdk.Descriptor.
#
# WHAT ITS DISCOVERY REACHES, said first because a gate says what its discovery
# mechanism reaches, not what it wishes it checked (canon §0-COBERTURA): it reads the
# STRING TABLE of one binary. It sees LITERALS. It does not see linkage, and a private
# symbol with no literal is invisible to it BY CONSTRUCTION. It is one of THREE
# instruments for one property — absence by package path (overlay
# scripts/test-addon-set-cut.sh, which says outright that `strings` does not demonstrate
# linkage) and per-SKU wireproof (overlay scripts/check-wireproof.sh) measure the cut
# itself. Never cite this one alone.
#
# ⛔ WHY THE COMPARISON IS "RESIDUE" AND NOT "EQUALS", measured on the real binary the
# first time this ran: Go packs its string table CONTIGUOUSLY, so `strings` returns runs
# like `failedsession-cockpit:availability:readreflect:` — one run holding an allowed
# literal glued to two unrelated ones. An equality test against the allowlist calls that
# a finding, every time, on a perfectly clean binary. So each run has the allowed
# literals (and the narrow structural exemptions below) DELETED from it, and only what
# REMAINS is judged.
#
# ⛔ ITS IRREDUCIBLE BLIND SPOT, named because a gate that hides one is worse than one
# that has none. In a Go binary an allowed literal and a private one can be
# INDISTINGUISHABLE once packed: `session-cockpitInput` is what you get both from the
# private symbol of that name AND from `session-cockpit` sitting next to an unrelated
# `Input…` in .rodata. No rule over the following character separates them, because both
# worlds produce a letter. This gate therefore does NOT claim to catch every private
# literal; it catches every private SHAPE (the FORBIDDEN list below) plus anything the
# allowlist does not explain. Linkage is measured by the overlay's test-addon-set-cut.sh
# (absence by package path) and check-wireproof.sh (per SKU), and those are not optional
# because of this paragraph.
#
# THREE ANSWERS, by EXIT CODE and never by prose: 0 clean · 1 a trace that is not
# allowed, or a required literal missing · 2 COULD NOT LOOK.
#
#   scripts/check-community-cockpit-strings.sh [binary]   check that binary
#   scripts/check-community-cockpit-strings.sh            build a community binary first
#   scripts/check-community-cockpit-strings.sh --list     print the rules it applies
set -uo pipefail
export LC_ALL=C

# ⛔ AISLAMIENTO DEL ENTORNO GIT, y no es higiene: `GIT_DIR` se HEREDA. git lo exporta a todo hook
# `pre-push`, así que este guion —que corre DENTRO del gancho y además crea repos/directorios
# desechables con `mktemp -d`— vería el `.git` de OTRO árbol y mediría el binario o el árbol
# equivocado. Lo cazó `lint:git-env` rechazando mi push: *«pairs 'mktemp -d' with git, does not
# source lib/git-env.sh, and does not refuse a poisoned GIT_DIR»*.
#
# Fail-closed: un saneador que no se puede cargar es «no he podido aislar», nunca «no hacía falta».
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "$(basename "$0"): FATAL: no puedo cargar $_olivares_git_env (aislamiento git-env)" >&2
	exit 2
}
unset _olivares_git_env

say()  { printf '%s\n' "$*"; }
nope() { say "check-community-cockpit-strings: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || true)}"
[ -n "$ROOT" ] || nope "not inside a git work tree and OLIVARES_ROOT is unset"
cd "$ROOT" || nope "cannot enter $ROOT"

# The allowlist path is overridable so a BATTERY never has to edit the tracked file.
#
# ⛔ IT USED TO, and both the adversarial contrast and this gate's own class flagged it: the
# "empty allowlist" case rewrote scripts/community-session-cockpit-strings.allow in the real
# worktree and restored it on the next line. An interrupt, a kill or two concurrent runs could
# leave the live gate with an empty rule set — a battery that checks integrity must not be able
# to alter its own subject's authority.
ALLOW="${OLIVARES_COCKPIT_ALLOWLIST:-$ROOT/scripts/community-session-cockpit-strings.allow}"
[ -r "$ALLOW" ] || nope "the allowlist is missing at $ALLOW — a gate whose own list is gone has checked nothing"
command -v python3 >/dev/null 2>&1 || nope "no python3 on PATH (the residue analysis needs it)"

if [ "${1:-}" = "--list" ]; then
	MODE=--list
	BIN=""
else
	MODE=check
	BIN="${1:-}"
	if [ -z "$BIN" ]; then
		command -v go >/dev/null 2>&1 || nope "no Go toolchain on PATH and no binary given"
		[ -n "${TMPDIR:-}" ] && mkdir -p "$TMPDIR" 2>/dev/null
		scratch="$(mktemp -d 2>/dev/null)" || nope "cannot create a scratch dir (TMPDIR=${TMPDIR:-unset}); /tmp is noexec here"
		trap 'rm -rf "$scratch"' EXIT
		BIN="$scratch/olivares-community"
		build_err="$(cd "$ROOT/cmd/olivares" && go build -o "$BIN" . 2>&1)" || {
			say "check-community-cockpit-strings: COULD NOT LOOK — the community binary did not build." >&2
			printf '%s\n' "$build_err" | sed 's/^/    /' >&2
			exit 2
		}
	fi
	[ -r "$BIN" ] || nope "cannot read the binary at $BIN"
fi

python3 - "$MODE" "$ALLOW" "$BIN" <<'PY'
import re, sys

mode, allow_path, binpath = sys.argv[1], sys.argv[2], sys.argv[3]

# MARKERS: what makes a run worth judging at all. Broad on purpose — narrowing them is
# how a rename slips past, and the residue step is what keeps the breadth affordable.
#
# ⛔ `xterm` IS CASE-SENSITIVE AND NEEDS A NON-LETTER BEFORE IT, and that is a calibration,
# not a preference. Measured on the real community binary: a case-insensitive `xterm`
# matched FOUR times, and every one of them was `maxTerm` inside the CodeMirror/Lezer
# chunk of the embedded console. A gate whose first real run is four false positives is a
# gate somebody switches off, so the marker was calibrated against the artifact instead
# of being reasoned about. Real xterm ships as `@xterm/xterm`, `xterm.css`, `xterm-` class
# names — all lowercase, all preceded by a separator.
MARKERS = re.compile(r'cockpit|(?<![A-Za-z])xterm', re.IGNORECASE)

# STRUCTURAL EXEMPTIONS. These are NOT literals a product surface shows; they are the
# compiler's own naming of the PUBLIC AGPL package that is SUPPOSED to be in this
# binary (modules/sessioncockpit — the placeholder). Each is deliberately anchored to
# `modules/`: anything naming `enterprise/sessioncockpit*` is a finding, which is the
# whole distinction this gate exists to make.
EXEMPT = [
    # the AGPL package path and any symbol qualified by it
    # ⛔ NO `/` IN THE TRAILING CLASS, AND THE CONTRAST IS WHY. With `/` in it the pattern
    # ran straight past the end of the public package path and swallowed an
    # `enterprise/sessioncockpit/...` symbol glued behind it — Codex sol built the exact
    # string and the gate answered OK. A Go symbol qualified by this package continues with
    # `.`, `(`, `*`, `)` or `[`, never with `/`; the compiler's SOURCE path does continue
    # with `/`, and that is the NEXT exemption's job, anchored to end in `.go`.
    (r'github\.com/olivaresai/olivares/modules/sessioncockpit[A-Za-z0-9_.*()\[\]\-]*',
     'the AGPL placeholder package path and its symbols'),
    # the source path the compiler embeds for panics/traces
    (r'[^\s]*/modules/sessioncockpit/[A-Za-z0-9_.\-]+\.go',
     'the AGPL placeholder source path'),
    # itab / type names for the placeholder type
    # ⛔ THE LOOK-BEHIND IS THE FIX FOR A FALSE GREEN. Unanchored, this pattern matched
    # the TAIL of `enterprise/sessioncockpit.Placeholder` and left a residue with no
    # marker, so a private symbol passed. The char before must not be a path or
    # identifier char: `R *sessioncockpit.Placeholder` (a space, then `*`) is exempt,
    # `enterprise/sessioncockpit.Placeholder` (a `/`) is not.
    (r'(?<![A-Za-z0-9_./-])\*?sessioncockpit\.Placeholder',
     'the AGPL placeholder type name, never one qualified by a path'),
]

allowed = []
for line in open(allow_path, encoding='utf-8', errors='replace'):
    line = line.rstrip('\n')
    if not line.strip() or line.lstrip().startswith('#'):
        continue
    allowed.append(line)
if not allowed:
    print('check-community-cockpit-strings: COULD NOT LOOK — the allowlist parsed to ZERO literals; '
          'an empty allowlist would call every binary clean.', file=sys.stderr)
    sys.exit(2)

# REQUIRED is EVERY allowlisted literal, not a chosen two.
#
# ⛔ IT USED TO BE TWO, AND THAT MADE THE POSITIVE CONTROL NEARLY EMPTY: a binary
# carrying only `session-cockpit` and `session_cockpit_unavailable` passed rc 0 while the
# contract says the placeholder ships eight literals. The control is supposed to prove
# the placeholder is THERE; proving two of its eight strings exist does not.
def required_literals(allowed):
    return list(allowed)

if mode == '--list':
    print('markers: %s' % MARKERS.pattern)
    print('allowed literals (%d):' % len(allowed))
    for a in allowed:
        print('  %s' % a)
    print('structural exemptions (the AGPL placeholder, never enterprise/):')
    for pat, why in EXEMPT:
        print('  %-70s  # %s' % (pat, why))
    print('required present (every allowed literal):')
    for r in required_literals(allowed):
        print('  %s' % r)
    sys.exit(0)

data = open(binpath, 'rb').read()
runs = [m.group().decode('ascii') for m in re.finditer(rb'[\x20-\x7e]{4,}', data)]

# Longest first, so deleting "session-cockpit:availability:read" does not leave the tail
# of a shorter prefix behind and turn a clean run into a finding.
allowed_sorted = sorted(allowed, key=len, reverse=True)
exempt_res = [(re.compile(p), why) for p, why in EXEMPT]

# ⛔ AN ALLOWED LITERAL IS NOT REMOVED WHEN A `/` OR `:` FOLLOWS IT, and that rule is the
# difference between this gate working and this gate being decorative.
#
# `session-cockpit` is allowed. `/v1/m/session-cockpit/input-sessions` is a PRIVATE route
# that CONTAINS it, so a blind removal leaves `/v1/m/ /input-sessions` — no marker, no
# finding, and the leak this gate exists to catch walks straight through. Its own battery
# caught that: the "ruta privada completa → rojo" case went green after the residue
# rewrite, which is a false NEGATIVE and the only kind of red that matters here.
#
# The discriminator is what actually follows the literal in each world. A private surface
# continues with a path or permission separator; Go's packed string table continues with
# whatever literal happens to sit next in .rodata — a letter, a digit, a paren — which is
# how `events)session-cockpitreflectlite.Set` arises and why it must still be forgiven.
# The longest allowed literal is tried first, so `session-cockpit:availability:read` is
# consumed whole before the bare `session-cockpit` can refuse to yield on its colon.
#
# ⛔ THE SET IS `[/:._-]`, AND `.` `_` `-` WERE ADDED BY THE ADVERSARIAL CONTRAST WITH A
# MEASUREMENT BEHIND THEM. The first version was `[/:]` only, and Codex sol produced the
# counter-example in one line: a private web chunk named `session-cockpit.js` continues
# with `.`, so the literal WAS removed, the marker vanished and the gate said OK.
#
# Before widening the set, what actually follows these literals in the REAL community
# binary was counted: `session-cockpit` is followed by `:` once (the permission, and the
# longer literal consumes it first) and by `r` once (packing) — and by nothing else. So the
# widening costs ZERO false positives on the artifact it polices, which is what makes the
# bias defensible rather than merely convenient. A false RED here is loud and a human
# resolves it by adding a literal with its reason; a false GREEN is silent, and silence is
# the thing this gate exists to prevent.
CONTINUATION = re.compile(r'[/:._-]')


def strip_literal(text, lit):
    out, i = [], 0
    while True:
        j = text.find(lit, i)
        if j < 0:
            out.append(text[i:])
            return ''.join(out)
        end = j + len(lit)
        nxt = text[end:end + 1]
        if nxt and CONTINUATION.match(nxt):
            # Part of a longer, cockpit-qualified name: leave it standing so the marker
            # survives into the residue and the run is judged.
            out.append(text[i:end])
        else:
            out.append(text[i:j])
            out.append(' ')
        i = end

# FORBIDDEN shapes, judged on the ORIGINAL run and never on the residue.
#
# ⛔ WHY A SECOND PASS EXISTS AT ALL, and it is a limit of the first one rather than a
# belt-and-braces flourish. Residue subtraction asks "what is left after removing what is
# allowed", and an allowed literal can be a PREFIX of a private one — so removing it can
# erase the very marker that would have flagged the private string. The continuation rule
# handles the separators it knows; an adversarial contrast produced
# `session-cockpit?input-sessions` with one it did not.
#
# These patterns are shapes that are private NO MATTER what packs around them, so they
# are decided before any subtraction can hide them.
FORBIDDEN = [
    (re.compile(r'enterprise/sessioncockpit'), 'a symbol or path of the commercial engine'),
    (re.compile(r'olivares\.cockpit'), 'the private proto package'),
    (re.compile(r'cockpit_agent'), 'the private proto file'),
    (re.compile(r'SessionCockpitAgent'), 'the private gRPC service'),
    (re.compile(r'/v1/m/session-cockpit/.'), 'a private route below the namespace'),
    (re.compile(r'session-cockpit[^A-Za-z0-9]input'), 'an input surface joined to the namespace'),
    (re.compile(r'@xterm/'), 'the terminal renderer package'),
]

findings = []
for run in runs:
    if not MARKERS.search(run):
        continue
    hit = None
    for rx, why in FORBIDDEN:
        m = rx.search(run)
        if m:
            hit = (m, why)
            break
    if hit:
        m, why = hit
        lo, hi = max(0, m.start() - 60), min(len(run), m.end() + 60)
        findings.append((run[lo:hi], why))
        continue
    residue = run
    for rx, _why in exempt_res:
        residue = rx.sub(' ', residue)
    for lit in allowed_sorted:
        residue = strip_literal(residue, lit)
    m = MARKERS.search(residue)
    if m:
        # Report a WINDOW, not the run. Go glues its whole embedded console bundle into
        # single runs of tens of kilobytes; printing one drowns the finding it contains.
        lo, hi = max(0, m.start() - 60), min(len(residue), m.end() + 60)
        findings.append((residue[lo:hi], 'marcador %s sin cubrir' % m.group()))

req = required_literals(allowed)
present = {r: any(r in run for run in runs) for r in req}
missing = [r for r, ok in present.items() if not ok]

if findings:
    print('check-community-cockpit-strings: TRAZA(S) FUERA DE LA ALLOWLIST en %s:' % binpath, file=sys.stderr)
    for window, why in findings[:40]:
        print('  %-46s …%s…' % (why, window), file=sys.stderr)
    if len(findings) > 40:
        print('  … y %d más' % (len(findings) - 40), file=sys.stderr)
    print('', file=sys.stderr)
    print('  A community artifact carries only the placeholder. A private route, a proto name,', file=sys.stderr)
    print('  an xterm chunk, an enterprise/sessioncockpit symbol or a provider mark here means', file=sys.stderr)
    print('  the cut leaked into a build that must not have it.', file=sys.stderr)
    print('  repair: keep the private surface behind `enterprise && addon_ids`; or, if this', file=sys.stderr)
    print('  literal is genuinely part of the placeholder, add it to', file=sys.stderr)
    print('  scripts/community-session-cockpit-strings.allow WITH ITS REASON.', file=sys.stderr)
    sys.exit(1)

if missing:
    print('check-community-cockpit-strings: FALTA(N) EL/LOS LITERAL(ES) OBLIGATORIO(S) en %s:' % binpath, file=sys.stderr)
    for m in missing:
        print('  %s' % m, file=sys.stderr)
    print('', file=sys.stderr)
    print('  These are the positive control. Their absence means this binary never carried the', file=sys.stderr)
    print('  501 placeholder at all, so "no unexpected trace" would have been true and empty.', file=sys.stderr)
    sys.exit(1)

judged = sum(1 for run in runs if MARKERS.search(run))
print('check-community-cockpit-strings: OK — %d run(s) con marcador en %s, todas cubiertas por los '
      '%d literales permitidos y las exenciones del placeholder AGPL; los %d obligatorios, presentes.'
      % (judged, binpath, len(allowed), len(req)))
PY
