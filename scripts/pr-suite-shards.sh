#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# pr-suite-shards.sh — READ the declared partition of the pull-request functional suite,
# and run one shard of it.
#
# The partition itself lives in ci/pr-suite-shards.txt and nowhere else. This script is
# the only reader: the workflow, the Taskfile leg and the gate all go through it, so
# there is one parser to be right rather than three to agree.
#
# Subcommands:
#   shards                  the shard names, one per line, in the order they are declared
#   matrix                  the same names as a JSON array
#   packages <shard>        the import paths that shard owns, one per line
#   legs <shard>            the Taskfile legs that shard runs after its packages
#   groups <shard>          the name groups that shard runs, as `<package> <group>` lines
#   partitioned             the packages that `group` records split, one per line
#   tests <package>         the top-level Test/Fuzz/Example functions of that package
#   run-expr <pkg> <group>  the `-run` expression that group resolves to
#   number <key>            a declared clock: go-timeout-minutes | step-ceiling-minutes |
#                           job-ceiling-minutes
#   run [shard]             `go test` over that shard's packages, then its name groups,
#                           then its legs. The shard may come from OLIVARES_TEST_SHARD
#                           instead of the argument.
#
# A NAME GROUP splits ONE package across shards by test NAME, for the case no package-level
# split reaches: a single package longer than the job that carries it. A named group is a
# list of prefixes and resolves to `-run '^Test(<alternation>)'`; the REMAINDER group, written
# `*`, resolves to the EXACT names of everything the other groups of that package do not
# name — computed here from the source enumeration, never written by hand, so a test added
# tomorrow is in the partition the moment it is written. `-run` selects fuzz targets and
# examples too, so they are in the universe and the remainder carries them.
#
# THE PACKAGE UNIVERSE is enumerated module by module, because `go test ./...` in a
# workspace only covers the CURRENT module (golang/go#50745) — the same reason
# scripts/go-work-each.sh exists. Only packages WITH tests are listed: a package without
# tests contributes no coverage and only lengthens the command line.
#
# OLIVARES_PR_SUITE_SHARDS overrides the spec path, OLIVARES_PR_SUITE_PACKAGES overrides
# the universe with a file of import paths, and OLIVARES_PR_SUITE_TESTS overrides the test
# universe with a file of `<import path> <function>` lines. The three exist for the mutation
# battery, which must be able to build a tree with a package in two shards, or a test in
# none, without creating either. `run` PRINTS which universe it used: an override that
# stayed quiet is an override nobody notices.
set -uo pipefail

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || { echo "pr-suite-shards: COULD NOT LOOK — cannot enter $ROOT" >&2; exit 2; }

SPEC="${OLIVARES_PR_SUITE_SHARDS:-ci/pr-suite-shards.txt}"
SPEC_TESTS="${OLIVARES_PR_SUITE_TESTS:-}"
# ⛔ ONE ARGUMENT, NOT ONE COMMAND LINE. Linux caps a SINGLE argv entry at MAX_ARG_STRLEN —
# 32 pages, 131072 bytes — independently of ARG_MAX, and the remainder's expression is the
# one that grows with the tree. The limit is checked where it can still be read as a
# partition problem; over it, `exec` fails with a message about the argument list and never
# about the tests.
RUN_EXPR_LIMIT=100000
# Where `go list` writes its diagnosis, so that the refusal can carry it. One file for the
# whole run, removed by the EXIT trap below.
GOLIST_ERR=""
fail()   { printf 'pr-suite-shards: FAIL — %s\n' "$*" >&2; exit 1; }
cannot() { printf 'pr-suite-shards: COULD NOT LOOK — %s\n' "$*" >&2; exit 2; }

[ -r "$SPEC" ] || cannot "cannot read $SPEC"
command -v python3 >/dev/null 2>&1 || cannot "python3 is not on PATH"

# ── The universe ──────────────────────────────────────────────────────────────────────
# A file given by the caller, or `go list` module by module. The two are never mixed.
universe() {
  if [ -n "${OLIVARES_PR_SUITE_PACKAGES:-}" ]; then
    [ -r "$OLIVARES_PR_SUITE_PACKAGES" ] ||
      cannot "OLIVARES_PR_SUITE_PACKAGES=$OLIVARES_PR_SUITE_PACKAGES is not readable"
    grep -v '^[[:space:]]*$' "$OLIVARES_PR_SUITE_PACKAGES" | LC_ALL=C sort -u
    return
  fi
  [ -f go.work ] || cannot "go.work is not at $ROOT and no package file was given"
  command -v go >/dev/null 2>&1 || cannot "no Go toolchain: the packages cannot be enumerated"
  local m mods raw
  mods="$(go work edit -json | sed -n 's/.*"DiskPath": "\(.*\)".*/\1/p')" ||
    cannot "go work edit could not say which modules this workspace has"
  [ -n "$mods" ] || cannot "go.work names no module: there is no universe to enumerate"
  raw="$(mktemp "${TMPDIR:-/tmp}/pr-suite-walk.XXXXXX")" ||
    cannot "cannot create a scratch file for the module walk"
  # ⛔ THE WALK IS NOT THE LEFT SIDE OF A PIPELINE. It used to be, and a `cannot` inside it
  # exited only the subshell: the module that failed and every module after it vanished, the
  # PARTIAL universe reached the caller, and a partition of what could be read certified
  # itself as a partition of the suite.
  while IFS= read -r m; do
    [ -n "$m" ] || continue
    ( cd "$m" && go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./... ) >> "$raw" ||
      { rm -f "$raw"; cannot "go list failed in $m: the universe cannot be finished, so none of it can be certified"; }
  done <<< "$mods"
  grep -v '^[[:space:]]*$' "$raw" | LC_ALL=C sort -u
  rm -f "$raw"
}

universe_source() {
  if [ -n "${OLIVARES_PR_SUITE_PACKAGES:-}" ]; then
    printf 'the file %s\n' "$OLIVARES_PR_SUITE_PACKAGES"
  else
    printf 'go list over the go.work modules\n'
  fi
}

# ── The tests of one package ──────────────────────────────────────────────────────────
# What `-run` can select: the top-level Test, Fuzz and Example functions. Read from the
# SOURCE and never from `go test -list`, which LINKS the test binary — minutes for the two
# packages this exists for. `go list` applies the same build constraints as `go test`, so a
# file behind a build tag is out of the universe here exactly as it is out of the run, and it
# compiles nothing.
#
# ⛔ THE NAME IS READ TO ITS END, NEVER TO THE END OF A CHARACTER CLASS. `[A-Za-z0-9_]*`
# stops at the first rune it does not cover, so `func TestUeberSync` with a non-ASCII letter
# collapsed to the bare name `Test`: one name invented, one real test dropped, and the gate
# certifying the loss because it shares this enumerator. Worse, WHICH runes a class covers
# depends on the locale and on the `grep` the machine has — the same package enumerated one
# way on a laptop and another on a runner. The parse below has neither property, and a `func
# Test…` line it cannot read whole is a refusal with its file and its line, not a guess.
#
# TestMain is not a test but the binary's entry point: `-run` never selects it and it always
# runs, so counting it would inflate every group by one. Go's own rule for the rest is that
# the rune after Test, Fuzz or Example is not a lower-case letter, so TestifyHelper is a
# helper and not a test.
#
# An EMPTY answer is a fact about the package — it has no test — and the caller decides
# whether that is a finding. An INABILITY is never that answer: every way this function can
# fail to look ends in COULD NOT LOOK with the reason.
# ⛔ GO'S OWN LAST LINE, APPENDED TO THE REFUSAL. "no space left on device" and "is not in
# std" are two different things to do next, and discarding go's stderr made them the same
# sentence. Nothing here can decide which one it is; the message must carry it instead.
golist_reason() {
  local last
  { [ -n "$GOLIST_ERR" ] && [ -s "$GOLIST_ERR" ]; } || return 0
  # The last line, with its leading indentation removed: go indents the continuation of a
  # multi-line diagnosis, and a tab in the middle of a refusal reads as a broken message.
  last="$(tail -1 "$GOLIST_ERR" | sed 's/^[[:space:]]*//')"
  [ -n "$last" ] || return 0
  printf ' — go said: %s' "$last"
}

package_tests() {
  local pkg="$1" dir files out cannots bads
  if [ -n "$SPEC_TESTS" ]; then
    [ -r "$SPEC_TESTS" ] || cannot "OLIVARES_PR_SUITE_TESTS=$SPEC_TESTS is not readable"
    awk -v p="$pkg" '$1 == p && $2 != "TestMain" { print $2 }' "$SPEC_TESTS" | LC_ALL=C sort -u
    return 0
  fi
  command -v go >/dev/null 2>&1 ||
    cannot "no Go toolchain: the tests of $pkg cannot be enumerated"
  if [ -z "$GOLIST_ERR" ]; then
    GOLIST_ERR="$(mktemp "${TMPDIR:-/tmp}/pr-suite-golist.XXXXXX")" ||
      cannot "cannot create a scratch file for go list's diagnosis"
  fi
  dir="$(go list -f '{{.Dir}}' "$pkg" 2>"$GOLIST_ERR")" ||
    cannot "go list could not locate $pkg: a group names a package this tree cannot resolve$(golist_reason)"
  { [ -n "$dir" ] && [ -d "$dir" ]; } ||
    cannot "go list could not locate $pkg: it named no directory that exists"
  files="$(go list -f '{{range .TestGoFiles}}{{.}}
{{end}}{{range .XTestGoFiles}}{{.}}
{{end}}' "$pkg" 2>"$GOLIST_ERR")" ||
    cannot "go list could not read the test files of $pkg$(golist_reason)"
  files="$(printf '%s\n' "$files" | grep -v '^[[:space:]]*$')" || files=""
  [ -n "$files" ] || return 0

  out="$(PKG_DIR="$dir" PKG_FILES="$files" python3 - <<'PY'
import os, re, sys

directory = os.environ["PKG_DIR"]
files = [f for f in os.environ["PKG_FILES"].split("\n") if f.strip()]

# A declaration this reader will account for: `func`, space or tab, the whole name, then the
# parameter list. The name runs to the first character that cannot be part of one, and the
# class is written as an exclusion so that no alphabet is left out of it.
HEAD = re.compile(r"^func[ \t]+(Test|Fuzz|Example)")
DECL = re.compile(r"^func[ \t]+([^\s(\[{]+)[ \t]*\(")

names, bad = set(), []
for name in files:
    try:
        with open(os.path.join(directory, name), encoding="utf-8") as fh:
            lines = fh.readlines()
    except (OSError, UnicodeDecodeError) as exc:
        print("CANNOT %s cannot be read as UTF-8 Go source: %s" % (name, exc))
        continue
    for n, line in enumerate(lines, 1):
        head = HEAD.match(line)
        if not head:
            continue
        decl = DECL.match(line)
        if not decl:
            bad.append("%s:%d: %s" % (name, n, line.rstrip()))
            continue
        ident = decl.group(1)
        rest = ident[len(head.group(1)):]
        if rest and rest[0].islower():
            continue          # TestifyHelper is a helper; Go does not run it either
        if ident == "TestMain":
            continue          # the binary's entry point, which -run never selects
        names.add(ident)

for b in bad:
    print("BAD " + b)
for nm in sorted(names):
    print("NAME " + nm)
PY
  )" || cannot "the test enumerator failed to run over $pkg"

  cannots="$(printf '%s\n' "$out" | sed -n 's/^CANNOT //p')"
  [ -z "$cannots" ] || cannot "$(printf '%s\n' "$cannots" | head -1)"
  bads="$(printf '%s\n' "$out" | sed -n 's/^BAD //p')"
  if [ -n "$bads" ]; then
    printf 'pr-suite-shards: FAIL — %s declares a test this reader will not guess at:\n' "$pkg" >&2
    printf '%s\n' "$bads" | sed 's/^/  · /' >&2
    printf '%s\n' "  A name read to the end of a character class is a name no test has, and a" \
                  "  declaration this reader skips is a test no shard runs. Write it as" \
                  "  \`func TestName(t *testing.T)\` so the partition can account for it." >&2
    exit 1
  fi
  printf '%s\n' "$out" | sed -n 's/^NAME //p'
}

tests_source() {
  if [ -n "$SPEC_TESTS" ]; then
    printf 'the file %s\n' "$SPEC_TESTS"
  else
    printf 'go list over the source of each partitioned package\n'
  fi
}

# ── The spec ──────────────────────────────────────────────────────────────────────────
# One python reader, used by every subcommand. It prints records the shell can consume:
#   SHARD <name> · PKG <shard> <pattern> · LEG <shard> <task> · OPTIONAL <pattern>
#   GROUP <shard> <package> <name> <family…> · NUM <key> <value> · BAD <reason>
# A line it cannot classify is a BAD record, never a skipped one: a directive with a typo
# would otherwise remove packages from the partition without removing them from the tree.
spec_records() {
  SPEC="$SPEC" python3 - <<'PY'
import os, re

path = os.environ["SPEC"]
shards, out, bad = [], [], []
NUM_KEYS = {"go-timeout-minutes", "step-ceiling-minutes", "job-ceiling-minutes"}
nums = {}
with open(path, encoding="utf-8") as fh:
    for n, raw in enumerate(fh, 1):
        line = raw.split("#", 1)[0].strip()
        if not line:
            continue
        f = line.split()
        kind = f[0]
        if kind == "shard" and len(f) == 2:
            if f[1] in shards:
                bad.append(f"line {n}: shard {f[1]!r} is declared twice")
            else:
                shards.append(f[1])
                out.append(f"SHARD {f[1]}")
        elif kind in ("pkg", "leg") and len(f) == 3:
            out.append(f"{kind.upper()} {f[1]} {f[2]}")
        elif kind == "optional" and len(f) == 2:
            out.append(f"OPTIONAL {f[1]}")
        elif kind == "group" and len(f) >= 5:
            fams = f[4:]
            # A family becomes part of a regular expression, so anything that is not a plain
            # identifier would change what `-run` selects without changing what the file
            # says. `*` is the remainder and is the only family that may stand alone.
            if "*" in fams and len(fams) > 1:
                bad.append(f"line {n}: group {f[3]!r} mixes the remainder `*` with families: "
                           "the remainder is whatever the others do not name, so it cannot "
                           "name anything itself")
            else:
                for fam in fams:
                    if fam != "*" and not re.fullmatch(r"[A-Za-z0-9_]+", fam):
                        bad.append(f"line {n}: the family {fam!r} of group {f[3]!r} is not a "
                                   "plain identifier: it would change what -run selects "
                                   "without changing what this line says")
                out.append(f"GROUP {f[1]} {f[2]} {f[3]} " + " ".join(fams))
        elif kind in NUM_KEYS and len(f) == 2:
            if not re.fullmatch(r"[0-9]+", f[1]) or int(f[1]) <= 0:
                bad.append(f"line {n}: {kind} is {f[1]!r}, not a positive whole number of minutes")
            elif kind in nums:
                bad.append(f"line {n}: {kind} is declared twice")
            else:
                nums[kind] = f[1]
                out.append(f"NUM {kind} {f[1]}")
        else:
            bad.append(f"line {n}: {line!r} is not a record this reader knows")

# A shard name used by a pkg/leg record before it is declared is a typo with a silent
# cost: the packages it names would belong to nothing and the shard would not exist.
for rec in out:
    f = rec.split()
    if f[0] in ("PKG", "LEG", "GROUP") and f[1] not in shards:
        bad.append(f"{f[0].lower()} names shard {f[1]!r}, which is never declared")

# Two groups of one package under the same name are two different `-run` expressions with
# one name: the log would say which group failed and nobody could tell which one it was.
seen_groups = set()
for rec in out:
    f = rec.split()
    if f[0] != "GROUP":
        continue
    key = (f[2], f[3])
    if key in seen_groups:
        bad.append(f"the package {f[2]} declares two groups called {f[3]!r}")
    seen_groups.add(key)
if not shards:
    bad.append("no shard is declared")
for k in sorted(NUM_KEYS - set(nums)):
    bad.append(f"{k} is not declared")

for b in bad:
    print("BAD " + b)
for o in out:
    print(o)
PY
}

RECORDS="$(spec_records)" || cannot "the spec reader failed to run"
BADS="$(printf '%s\n' "$RECORDS" | sed -n 's/^BAD //p')"
if [ -n "$BADS" ]; then
  printf 'pr-suite-shards: FAIL — %s cannot be read as a partition:\n' "$SPEC" >&2
  printf '%s\n' "$BADS" | sed 's/^/  · /' >&2
  exit 1
fi

shard_names() { printf '%s\n' "$RECORDS" | sed -n 's/^SHARD //p'; }

# The packages a `group` record splits. They are owned by their groups and by nothing else:
# the pattern assignment below skips them, so `packages <shard>` never hands one whole to a
# shard that a group already runs by name.
partitioned_packages() {
  printf '%s\n' "$RECORDS" | awk '$1 == "GROUP" { print $3 }' | LC_ALL=C sort -u
}
shard_groups() {
  printf '%s\n' "$RECORDS" | awk -v s="$1" '$1 == "GROUP" && $2 == s { print $3, $4 }'
}
group_families() {
  printf '%s\n' "$RECORDS" |
    awk -v pkg="$1" -v g="$2" '$1 == "GROUP" && $3 == pkg && $4 == g {
      out = ""
      for (i = 5; i <= NF; i++) out = out (out == "" ? "" : " ") $i
      print out
    }'
}
known_shard() {
  local s
  while IFS= read -r s; do [ "$s" = "$1" ] && return 0; done < <(shard_names)
  return 1
}

# ── The assignment ────────────────────────────────────────────────────────────────────
# Longest matching pattern wins. A tie between two DIFFERENT shards is not resolved here:
# it is the "package in two shards" finding, and the gate is what reports it. This reader
# refuses to invent an owner, because a runner that picks one arbitrarily would run the
# package twice or not at all depending on which side it picked.
assign() {
  RECORDS="$RECORDS" WANT="${1:-}" python3 - "$2" <<'PY'
import os, sys

want = os.environ["WANT"]
pairs, split = [], set()
for rec in os.environ["RECORDS"].splitlines():
    f = rec.split()
    if f and f[0] == "PKG":
        pairs.append((f[1], f[2]))
    elif f and f[0] == "GROUP":
        split.add(f[2])

def match(pat, pkg):
    if pat.endswith("/..."):
        base = pat[:-4]
        return pkg == base or pkg.startswith(base + "/")
    return pkg == pat

def weight(pat):
    return len(pat[:-4] if pat.endswith("/...") else pat)

for line in open(sys.argv[1], encoding="utf-8"):
    pkg = line.strip()
    if not pkg:
        continue
    # A package split by test name has no owner HERE: its groups own it, one shard each.
    if pkg in split:
        continue
    best, bw = set(), -1
    for name, pat in pairs:
        if not match(pat, pkg):
            continue
        w = weight(pat)
        if w > bw:
            best, bw = {name}, w
        elif w == bw:
            best.add(name)
    if len(best) > 1:
        print("pr-suite-shards: FAIL — %s is claimed by %s with patterns of the same length: "
              "it has no owner, so it would run twice or not at all"
              % (pkg, " and ".join(sorted(best))), file=sys.stderr)
        sys.exit(1)
    if not best:
        print("pr-suite-shards: FAIL — %s belongs to no shard: nothing would run it and "
              "nothing would say so" % pkg, file=sys.stderr)
        sys.exit(1)
    owner = next(iter(best))
    if want in ("", owner):
        print("%s\t%s" % (owner, pkg))
PY
}


# ── A group becomes a `-run` expression ───────────────────────────────────────────────
# A NAMED group is its families, anchored at the front: `^Test(Alpha|Beta)` selects every
# top-level test whose name begins with TestAlpha or TestBeta. It is not anchored at the end
# on purpose — a family is a prefix, which is what makes it survive a test being added to it.
#
# The REMAINDER is the complement, and the complement of a set of prefixes is not a prefix:
# there is no negation in RE2, so it is written as the EXACT names, anchored at both ends.
# That expression is COMPUTED from the source enumeration every time it runs, so the day a
# test is added it is already in it, and no hand-written "everything else" can be wrong.
#
# Neither form carries a `/`, so neither filters SUBTESTS: `go test` splits a -run pattern on
# unbracketed slashes and matches each element against one level of the test's name. With one
# element, a selected test runs with every subtest it has, which is the behaviour the whole
# suite has today.
run_expression() {
  local pkg="$1" g="$2" fams tests
  fams="$(group_families "$pkg" "$g")"
  [ -n "$fams" ] || fail "the package $pkg declares no group called $g"
  if [ "$fams" != "*" ]; then
    printf '%s' "$fams" | python3 -c '
import sys
fams = sorted(set(sys.stdin.read().split()), key=lambda f: (-len(f), f))
print("^Test(" + "|".join(fams) + ")")
'
    return
  fi
  tests="$(package_tests "$pkg")" || exit $?
  NAMED="$(named_of "$pkg")" TESTS="$tests" python3 -c '
import os
named = [f.split() for f in os.environ["NAMED"].splitlines() if f.strip()]
tests = [t for t in os.environ["TESTS"].split() if t]
rest = [t for t in tests
        if not any(t.startswith("Test" + fam) for fams in named for fam in fams)]
print("^(" + "|".join(sorted(rest)) + ")$" if rest else "")
'
}

# The families of every group of a package EXCEPT the remainder, one group per line. What the
# remainder is the complement OF.
named_of() {
  printf '%s\n' "$RECORDS" |
    awk -v pkg="$1" '$1 == "GROUP" && $3 == pkg {
      out = ""
      for (i = 5; i <= NF; i++) out = out (out == "" ? "" : " ") $i
      if (out != "*") print out
    }'
}

UNIVERSE_FILE=""
cleanup() {
  [ -n "$UNIVERSE_FILE" ] && rm -f "$UNIVERSE_FILE"
  [ -n "$GOLIST_ERR" ] && rm -f "$GOLIST_ERR"
  return 0
}
trap cleanup EXIT

need_universe() {
  [ -n "$UNIVERSE_FILE" ] && return 0
  UNIVERSE_FILE="$(mktemp "${TMPDIR:-/tmp}/pr-suite-universe.XXXXXX")" ||
    cannot "cannot create a scratch file for the package universe"
  universe > "$UNIVERSE_FILE" || exit $?
  [ -s "$UNIVERSE_FILE" ] ||
    cannot "the package universe came back empty — refusing to shard nothing"
}

case "${1:-}" in
  shards)
    shard_names
    ;;

  matrix)
    # The shard names as a JSON array, for a caller that builds a matrix from this file
    # instead of typing the names a second time. NO WORKFLOW CALLS IT TODAY, and that is a
    # decision rather than an omission: deriving the matrix needs a setup job, a job costs a
    # runner on every pull request, and the property it would buy is already pinned —
    # check-pr-suite-shards.sh compares the workflow's matrix with this list INCLUDING ORDER
    # and refuses on any difference. This subcommand is a convenience for a person reading
    # the partition; the gate is the guarantee.
    shard_names | python3 -c 'import json,sys;print(json.dumps([l.strip() for l in sys.stdin if l.strip()]))'
    ;;

  packages)
    S="${2:-}"; [ -n "$S" ] || cannot "usage: $0 packages <shard>"
    known_shard "$S" || fail "unknown shard: $S (declared: $(shard_names | paste -sd, -))"
    need_universe
    assign "$S" "$UNIVERSE_FILE" | cut -f2 || exit 1
    ;;

  groups)
    S="${2:-}"; [ -n "$S" ] || cannot "usage: $0 groups <shard>"
    known_shard "$S" || fail "unknown shard: $S (declared: $(shard_names | paste -sd, -))"
    shard_groups "$S"
    ;;

  partitioned)
    partitioned_packages
    ;;

  tests)
    P="${2:-}"; [ -n "$P" ] || cannot "usage: $0 tests <package>"
    package_tests "$P"
    ;;

  run-expr)
    P="${2:-}"; G="${3:-}"
    { [ -n "$P" ] && [ -n "$G" ]; } || cannot "usage: $0 run-expr <package> <group>"
    RE="$(run_expression "$P" "$G")" || exit $?
    # A remainder every one of whose names some other group already claims resolves to
    # NOTHING, and this prints nothing rather than `^()$` — an expression that matches no test
    # and that `go test` answers 0 to. Printing nothing is how a caller tells the two apart,
    # and no caller treats it as an ordinary case: the runner refuses the shard on it and the
    # gate reports it as a finding, because a group that selects nothing is a job that reports
    # success having run none of its tests. Exit 0 here says "there is no expression", not
    # "there is nothing wrong".
    [ -n "$RE" ] || exit 0
    # Bytes, not characters: the kernel's limit is on bytes and a name outside ASCII costs
    # more than one, so ${#RE} would under-count exactly the expressions that are at risk.
    RE_BYTES="$(printf '%s' "$RE" | wc -c)"
    [ "$RE_BYTES" -le "$RUN_EXPR_LIMIT" ] ||
      fail "the -run expression of group ${G} of ${P} is ${RE_BYTES} bytes, over the ${RUN_EXPR_LIMIT}-byte limit this reader declares (the kernel refuses a single argument over MAX_ARG_STRLEN, 131072 bytes): the shard would die on exec with a message about the argument list and never about the tests. Split the remainder by naming more families."
    printf '%s\n' "$RE"
    ;;

  legs)
    S="${2:-}"; [ -n "$S" ] || cannot "usage: $0 legs <shard>"
    known_shard "$S" || fail "unknown shard: $S (declared: $(shard_names | paste -sd, -))"
    printf '%s\n' "$RECORDS" | sed -n "s/^LEG $S //p"
    ;;

  number)
    K="${2:-}"; [ -n "$K" ] || cannot "usage: $0 number <key>"
    V="$(printf '%s\n' "$RECORDS" | sed -n "s/^NUM $K //p")"
    [ -n "$V" ] || cannot "no declared number called $K"
    printf '%s\n' "$V"
    ;;

  run)
    # The shard comes from the argument or from OLIVARES_TEST_SHARD, so a workflow can set
    # it from its matrix without the Taskfile leg having to pass it through go-task's
    # templating — one less place for the name to be rewritten on the way.
    S="${2:-${OLIVARES_TEST_SHARD:-}}"
    [ -n "$S" ] || cannot "no shard given: pass one as an argument or set OLIVARES_TEST_SHARD"
    known_shard "$S" || fail "unknown shard: $S (declared: $(shard_names | paste -sd, -))"
    need_universe
    PKGS="$(assign "$S" "$UNIVERSE_FILE" | cut -f2)" || exit 1
    TO="$(printf '%s\n' "$RECORDS" | sed -n 's/^NUM go-timeout-minutes //p')"
    mapfile -t LEGS < <(printf '%s\n' "$RECORDS" | sed -n "s/^LEG $S //p")
    # ⛔ NOT `GROUPS`. That name is a special variable of the shell — the caller's Unix group
    # ids — and bash IGNORES an assignment to it, so `mapfile -t GROUPS` left the group ids in
    # place and every shard ran its name groups over numbers instead of over its groups.
    mapfile -t SHARD_GROUP_LIST < <(shard_groups "$S")
    rc=0
    echo "pr-suite-shards: shard ${S} — universe from $(universe_source)"
    [ "${#SHARD_GROUP_LIST[@]}" -eq 0 ] ||
      echo "pr-suite-shards: shard ${S} — ${#SHARD_GROUP_LIST[@]} name group(s), tests from $(tests_source)"
    if [ -z "$PKGS" ]; then
      # A shard with no packages is not a fast shard: it is a job that starts a database
      # to run nothing and reports success. It is only acceptable when the shard has legs
      # or name groups to run instead, and even then it says so out loud.
      { [ "${#LEGS[@]}" -gt 0 ] || [ "${#SHARD_GROUP_LIST[@]}" -gt 0 ]; } ||
        fail "shard ${S} owns no package in this tree and declares no group and no leg: it would exit 0 having run nothing"
      echo "pr-suite-shards: shard ${S} owns no whole package in THIS tree"
    else
      mapfile -t PKG_ARGS <<< "$PKGS"
      echo "pr-suite-shards: shard ${S} — ${#PKG_ARGS[@]} package(s), go test -timeout ${TO}m"
      # ⛔ A RED HERE DOES NOT CANCEL THE REST OF THE SHARD. It used to `exit 1`, so one red
      # package took the shard's name groups and legs with it and they published nothing —
      # the very shape `fail-fast: false` exists to avoid one level up, where a red shard
      # must not cancel its siblings. The author of the change needs the whole shard's
      # report at that moment, not the first thing that broke.
      bash scripts/with-pg-env.sh go test -count=1 -timeout "${TO}m" "${PKG_ARGS[@]}" || rc=1
    fi
    # ⛔ ONE `go test` PER GROUP, AFTER the whole packages and never beside them. `-run` is an
    # option of the COMMAND and not of a package, so a group cannot share an invocation with
    # packages that must run entire. The cost is that the two do not overlap; the return is
    # that the test binary this split exists for no longer runs beside another one, which is
    # the arithmetic the 84.9 % memory peak of the single shard came from.
    for entry in "${SHARD_GROUP_LIST[@]}"; do
      [ -n "$entry" ] || continue
      GPKG="${entry%% *}"; GNAME="${entry##* }"
      RE="$("$0" run-expr "$GPKG" "$GNAME")" || exit $?
      # ⛔ COUNT WHAT IT SELECTS, BEFORE RUNNING IT. `go test -run` over an expression that
      # matches no test prints ok and EXITS 0, so a group whose families went stale in a
      # rename — or a remainder with nothing left in it — would report a success having run
      # none of its tests, and the required check would be green over a suite that did not
      # run. Counting first is what the race partition does for the same reason, and it is
      # the only check that still stands when the gate was never run at all.
      GTESTS="$("$0" tests "$GPKG")" || exit $?
      # `wc` cannot fail the way `grep -c` can, and this number only goes in the message.
      GTOTAL="$(printf '%s\n' "$GTESTS" | sed '/^[[:space:]]*$/d' | wc -l)"
      GSEL=0
      if [ -n "$RE" ]; then
        # ⛔ `grep -c` SAYS TWO DIFFERENT THINGS WITH A NON-ZERO STATUS. 1 is a count of zero;
        # 2 or more is that it could not look at all. `|| true` read the second as the first,
        # so an inability arrived as "this group selects no test" — a red naming a partition
        # defect that is not there, on a partition that is correct. The count that decides
        # whether this shard runs anything has to be a count, or it is a refusal.
        GREP_RC=0
        GSEL="$(printf '%s\n' "$GTESTS" | grep -cE "$RE")" || GREP_RC=$?
        case "$GREP_RC" in
          0|1) [ -n "$GSEL" ] || GSEL=0 ;;
          *)   cannot "grep exited ${GREP_RC} counting what group ${GNAME} of ${GPKG} selects: the count that decides whether this shard runs anything could not be made, so nothing about it can be certified" ;;
        esac
      fi
      [ "$GSEL" -gt 0 ] ||
        fail "shard ${S}: group ${GNAME} of ${GPKG} selects no test of the ${GTOTAL} this tree has for that package. \`go test -run\` over an expression that matches nothing exits 0, so this shard would report success having run none of them."
      RE_BYTES="$(printf '%s' "$RE" | wc -c)"
      echo "pr-suite-shards: shard ${S} — group ${GNAME} of ${GPKG}: ${GSEL} of ${GTOTAL} test(s), go test -timeout ${TO}m -run (${RE_BYTES} bytes)"
      bash scripts/with-pg-env.sh go test -count=1 -timeout "${TO}m" -run "$RE" "$GPKG" || rc=1
    done
    for leg in "${LEGS[@]}"; do
      [ -n "$leg" ] || continue
      echo "pr-suite-shards: shard ${S} — leg ${leg}"
      task "${leg}" || rc=1
    done
    exit "$rc"
    ;;

  *)
    cannot "usage: $0 {shards|matrix|packages <shard>|groups <shard>|partitioned|tests <package>|run-expr <package> <group>|legs <shard>|number <key>|run <shard>}"
    ;;
esac
