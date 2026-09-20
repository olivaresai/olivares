#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-pr-suite-shards.sh — the union of the pull-request shards IS the suite, exactly once.
#
# ⛔ THE DEFECT THIS EXISTS FOR. Splitting a suite across jobs moves the failure from
# "slow" to "silent". A package that belongs to no shard is not reported as missing by
# anything: every shard is green, the required check is green, and the package simply
# stopped being tested. That is worse than the hour it replaced, because the hour was
# visible. `go test` will not catch it either — a shard runs the packages it was given
# and has no opinion about the ones it was not.
#
# FOUR THINGS ARE PINNED, and each is a distinct way this breaks:
#
#   1. EVERY test-bearing package of the workspace belongs to EXACTLY ONE shard. A package
#      in none is coverage lost in silence; a package in two is paid for twice and, worse,
#      means the assignment rule did not decide — so which shard runs it depends on the
#      order of the file.
#   2. EVERY declared pattern matches something. A pattern that matches nothing is a lie
#      that survives its refactor: it claims an area that no longer exists, and the shard
#      it is in looks fuller than it is. The exception is written in the spec with the word
#      `optional` and this gate PRINTS it — an exemption that keeps quiet is an exemption
#      nobody reviews.
#   3. THE WORKFLOW'S MATRIX IS THE SPEC'S SHARD LIST, in the same order. The names are in
#      two files, so they can drift; this is what makes the drift impossible rather than
#      unlikely. A shard declared and not in the matrix never runs and nothing says so.
#   4. THE CEILINGS THE WORKFLOW DECLARES ARE THE ONES THE SPEC DECLARES, and the Go
#      timeout stays under the step ceiling. Go's timeout fires with a goroutine dump and
#      the package name; the step ceiling only cancels. Inverting them costs the diagnosis.
#   5. EVERY TEST OF A PACKAGE SPLIT BY NAME BELONGS TO EXACTLY ONE GROUP. This is (1) one
#      level down and the failure is worse, because `go test -run` over an expression that
#      matches nothing EXITS 0: an orphaned test is not merely unreported, the shard that
#      should have run it publishes a SUCCESS. Hence: a remainder group per partitioned
#      package, no test in two groups, no group that selects nothing, no package split by
#      name and ALSO sent whole to a shard by a pattern, and a `-run` expression the kernel
#      will still carry as one argument.
#
# Three answers: 0 CLEAN · 1 FINDING · 2 COULD NOT LOOK.
set -uo pipefail

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || { echo "check-pr-suite-shards: COULD NOT LOOK — cannot enter $ROOT" >&2; exit 2; }

SPEC="${OLIVARES_PR_SUITE_SHARDS:-ci/pr-suite-shards.txt}"
WF="${OLIVARES_PR_CI_WF:-.github/workflows/pr-ci.yml}"
TASKFILE="${OLIVARES_TASKFILE:-Taskfile.yml}"
READER="${OLIVARES_PR_SUITE_READER:-scripts/pr-suite-shards.sh}"

say()    { printf '%s\n' "$*"; }
finding(){ printf 'check-pr-suite-shards: FAIL — %s\n' "$*" >&2; }
cannot() { printf 'check-pr-suite-shards: COULD NOT LOOK — %s\n' "$*" >&2; exit 2; }

for f in "$SPEC" "$WF" "$TASKFILE" "$READER"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null 2>&1 || cannot "python3 is not on PATH"

# ── the universe, through the ONE reader ─────────────────────────────────────────────
# Never a second enumeration: two enumerations of the same set is how a difference hides.
UNI="$(mktemp "${TMPDIR:-/tmp}/pr-suite-check.XXXXXX")" || cannot "cannot create a scratch file"
RAW="$(mktemp "${TMPDIR:-/tmp}/pr-suite-walk.XXXXXX")" || cannot "cannot create a scratch file"
trap 'rm -f "$UNI" "$RAW"' EXIT

if [ -n "${OLIVARES_PR_SUITE_PACKAGES:-}" ]; then
  [ -r "$OLIVARES_PR_SUITE_PACKAGES" ] ||
    cannot "OLIVARES_PR_SUITE_PACKAGES=$OLIVARES_PR_SUITE_PACKAGES is not readable"
  grep -v '^[[:space:]]*$' "$OLIVARES_PR_SUITE_PACKAGES" | LC_ALL=C sort -u > "$UNI"
  SOURCE="the file $OLIVARES_PR_SUITE_PACKAGES"
else
  [ -f go.work ] || cannot "go.work is not at $ROOT and no package file was given"
  command -v go >/dev/null 2>&1 || cannot "no Go toolchain: the packages cannot be enumerated"
  MODS="$(go work edit -json | sed -n 's/.*"DiskPath": "\(.*\)".*/\1/p')" ||
    cannot "go work edit could not say which modules this workspace has"
  [ -n "$MODS" ] || cannot "go.work names no module: there is no universe to certify"
  # ⛔ THE WALK IS NOT THE LEFT SIDE OF A PIPELINE, and it was. `cannot` exits 2, but from
  # inside the left side of a pipeline that exit reaches only the subshell: the module that
  # failed and every module after it disappeared from the universe, the partial list landed
  # in $UNI, `[ -s "$UNI" ]` was satisfied because it was not empty, and this gate certified
  # that a partition covers a suite it could not finish reading. A partition of what could be
  # read is not a partition of the suite.
  : > "$RAW"
  while IFS= read -r m; do
    [ -n "$m" ] || continue
    ( cd "$m" && go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./... ) >> "$RAW" ||
      cannot "go list failed in $m: the universe cannot be finished, so none of it can be certified"
  done <<< "$MODS"
  grep -v '^[[:space:]]*$' "$RAW" | LC_ALL=C sort -u > "$UNI"
  SOURCE="go list over the go.work modules"
fi
[ -s "$UNI" ] || cannot "the package universe came back empty — a partition of nothing certifies nothing"

# ── the spec, through the ONE reader ─────────────────────────────────────────────────
# The reader answers on the same three-valued contract this gate does, so its 2 has to stay
# a 2: an inability reported as a defect sends somebody to look for a defect that is not
# there, and — worse — it is a red that a retry can turn green without anything being fixed.
READER_RC=0
SHARDS="$(bash "$READER" shards)" || READER_RC=$?
if [ "$READER_RC" -eq 2 ]; then
  cannot "the reader could not read $SPEC as a partition (its reason is above)"
elif [ "$READER_RC" -ne 0 ]; then
  finding "$SPEC cannot be read as a partition (the reader refused; its reasons are above)"
  exit 1
fi
[ -n "$SHARDS" ] || cannot "the reader produced no shard names"

# ── the workflow: the matrix and the two ceilings ─────────────────────────────────────
# A conservative read, in the shape scripts/ci-timeouts.py already justifies for this
# repository: no tabs, no anchors, no flow mappings. Anything it cannot read with
# certainty is a refusal, never a guess.
WFOUT="$(WF="$WF" python3 - <<'PY'
import os, re, sys

wf = os.environ["WF"]
try:
    lines = open(wf, encoding="utf-8").read().splitlines()
except OSError as exc:
    print(f"CANNOT could not read {wf}: {exc}"); sys.exit(0)
if any("\t" in l for l in lines):
    print(f"CANNOT {wf} contains a tab: this reader will not guess its indentation"); sys.exit(0)

try:
    start = lines.index("jobs:") + 1
except ValueError:
    print(f"CANNOT {wf} has no `jobs:` block"); sys.exit(0)

jobs, current = {}, None
for line in lines[start:]:
    if line.strip() and not line.startswith("  "):
        break
    m = re.match(r'^  ([A-Za-z0-9][A-Za-z0-9_.-]*)\s*:\s*$', line)
    if m:
        current = m.group(1); jobs[current] = []; continue
    if line.startswith("  ") and not line.startswith("   ") and line.strip() \
       and not line.lstrip().startswith("#"):
        print("CANNOT " + wf + " has a key under `jobs:` this reader cannot classify: "
              + line.strip()); sys.exit(0)
    if current is not None:
        jobs[current].append(line)

# The shard job is the one whose matrix has a `shard:` key. Deriving it beats declaring
# it: a renamed job would otherwise keep a stale name true in the spec.
found = []
for name, body in jobs.items():
    vals, job_ceiling, step_ceilings = None, None, []
    for i, line in enumerate(body):
        m = re.match(r'^        shard:\s*\[(.*)\]\s*$', line)
        if m:
            raw = m.group(1)
            if not re.fullmatch(r'[A-Za-z0-9_, -]*', raw):
                print("CANNOT the matrix of job %r is not a plain inline list: %s" % (name, raw))
                sys.exit(0)
            vals = [v.strip() for v in raw.split(",") if v.strip()]
        m = re.match(r'^    timeout-minutes:\s*([0-9]+)\s*$', line)
        if m:
            job_ceiling = int(m.group(1))
        m = re.match(r'^        timeout-minutes:\s*([0-9]+)\s*$', line)
        if m:
            step_ceilings.append(int(m.group(1)))
    if vals is not None:
        found.append((name, vals, job_ceiling, step_ceilings))

if len(found) > 1:
    print("CANNOT more than one job declares a `shard:` matrix: " +
          ", ".join(n for n, _, _, _ in found)); sys.exit(0)
if not found:
    print("NOMATRIX")
    print("JOBS " + ",".join(sorted(jobs)))
    sys.exit(0)

name, vals, jc, sc = found[0]
print("JOB " + name)
print("MATRIX " + ",".join(vals))
print("JOBCEILING " + ("" if jc is None else str(jc)))
print("STEPCEILINGS " + ",".join(str(s) for s in sc))
print("JOBS " + ",".join(sorted(jobs)))
PY
)" || cannot "the workflow reader failed to run"

case "$WFOUT" in CANNOT*) cannot "${WFOUT#CANNOT }" ;; esac

RC=0
if printf '%s\n' "$WFOUT" | grep -qx 'NOMATRIX'; then
  finding "no job in $WF declares a \`shard:\` matrix, so $SPEC partitions a suite that nothing runs in parts. A shard list nobody reads is a list that stops being true the day after it is written."
  RC=1
  WF_MATRIX=""
  WF_JOB=""
else
  WF_JOB="$(printf '%s\n' "$WFOUT" | sed -n 's/^JOB //p')"
  WF_MATRIX="$(printf '%s\n' "$WFOUT" | sed -n 's/^MATRIX //p')"
  WF_JOBCEIL="$(printf '%s\n' "$WFOUT" | sed -n 's/^JOBCEILING //p')"
  WF_STEPCEILS="$(printf '%s\n' "$WFOUT" | sed -n 's/^STEPCEILINGS //p')"
fi

SPEC_MATRIX="$(printf '%s\n' "$SHARDS" | paste -sd, -)"
if [ -n "$WF_JOB" ] && [ "$WF_MATRIX" != "$SPEC_MATRIX" ]; then
  finding "the matrix of job '$WF_JOB' and the shard list disagree."
  say "  declared in $SPEC : $SPEC_MATRIX" >&2
  say "  matrix of $WF_JOB  : $WF_MATRIX" >&2
  say "  A shard declared and not in the matrix never runs; a matrix value with no shard" >&2
  say "  runs a shard that owns nothing and reports success. Move them together." >&2
  RC=1
fi

# ── the ceilings ─────────────────────────────────────────────────────────────────────
GOTO="$(bash "$READER" number go-timeout-minutes)" || cannot "the reader could not give the go timeout"
STEPC="$(bash "$READER" number step-ceiling-minutes)" || cannot "the reader could not give the step ceiling"
JOBC="$(bash "$READER" number job-ceiling-minutes)" || cannot "the reader could not give the job ceiling"

if [ "$GOTO" -ge "$STEPC" ]; then
  finding "the Go timeout (${GOTO}m) is not under the step ceiling (${STEPC}m): the step would be cancelled before Go could name the package that hung, and a cancelled step is indistinguishable from a superseded run."
  RC=1
fi
if [ "$STEPC" -ge "$JOBC" ]; then
  finding "the step ceiling (${STEPC}m) is not under the job ceiling (${JOBC}m): a job ceiling that fires first cancels the remaining steps, so the red comes out mute."
  RC=1
fi
if [ -n "$WF_JOB" ]; then
  if [ "${WF_JOBCEIL:-}" != "$JOBC" ]; then
    finding "job '$WF_JOB' declares timeout-minutes ${WF_JOBCEIL:-none} and $SPEC declares ${JOBC}."
    RC=1
  fi
  case ",$WF_STEPCEILS," in
    *",$STEPC,"*) ;;
    *) finding "no step of job '$WF_JOB' declares timeout-minutes ${STEPC}, which is the step ceiling $SPEC declares (the workflow declares: ${WF_STEPCEILS:-none}). A job whose long step has no ceiling of its own can only die the way nobody can read."
       RC=1 ;;
  esac
fi

# ── the legs are real tasks ──────────────────────────────────────────────────────────
# Same predicate check-gate-parity.sh uses to know what IS a task: a name that is not a
# key of the Taskfile is a leg that exits 201 the first time a shard reaches it.
while IFS= read -r s; do
  [ -n "$s" ] || continue
  while IFS= read -r leg; do
    [ -n "$leg" ] || continue
    if ! grep -qE "^  ${leg//:/\\:}:" "$TASKFILE"; then
      finding "shard ${s} declares the leg '${leg}', which is not a task of $TASKFILE: the shard would die on it the first time it ran."
      RC=1
    fi
  done < <(bash "$READER" legs "$s")
done < <(printf '%s\n' "$SHARDS")

# ── the partition itself ─────────────────────────────────────────────────────────────
PART="$(SHARDS="$SHARDS" SPEC="$SPEC" READER="$READER" UNI="$UNI" python3 - <<'PY'
import os, re, subprocess, sys

spec = os.environ["SPEC"]
reader = os.environ["READER"]
pairs, optional, groups = [], set(), []
with open(spec, encoding="utf-8") as fh:
    for raw in fh:
        f = raw.split("#", 1)[0].split()
        if len(f) == 3 and f[0] == "pkg":
            pairs.append((f[1], f[2]))
        elif len(f) == 2 and f[0] == "optional":
            optional.add(f[1])
        elif len(f) >= 5 and f[0] == "group":
            groups.append({"shard": f[1], "pkg": f[2], "name": f[3], "fams": f[4:]})

split = {g["pkg"] for g in groups}

def match(pat, pkg):
    if pat.endswith("/..."):
        base = pat[:-4]
        return pkg == base or pkg.startswith(base + "/")
    return pkg == pat

def weight(pat):
    return len(pat[:-4] if pat.endswith("/...") else pat)

pkgs = [l.strip() for l in open(os.environ["UNI"], encoding="utf-8") if l.strip()]
shards = [s for s in os.environ["SHARDS"].split("\n") if s.strip()]

# (1) a pattern written in two shards has no owner: the longest wins and the other lies.
seen = {}
for name, pat in pairs:
    if pat in seen:
        print("FINDING the pattern %s is declared in shard %s and in shard %s. One of the two "
              "claims packages it will never run, and which one depends on the order of the file."
              % (pat, seen[pat], name))
    seen[pat] = name

owner, orphans, ties = {}, [], []
for pkg in pkgs:
    # Split by test name: its groups own it, one shard each. It is not an orphan and it is
    # not claimed by a pattern — section (5) below is what proves it is covered.
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
        ties.append((pkg, sorted(best)))
        owner[pkg] = sorted(best)[0]
    elif not best:
        orphans.append(pkg)
    else:
        owner[pkg] = next(iter(best))

if ties:
    print("FINDING %d package(s) are claimed by TWO shards with patterns of the same length, so "
          "the assignment rule does not decide: they would run twice, or once in whichever shard "
          "the file happens to list first." % len(ties))
    for pkg, names in ties[:20]:
        print("DETAIL %s -> %s" % (pkg, " and ".join(names)))
if orphans:
    print("FINDING %d package(s) with tests belong to NO shard. Nothing runs them and every shard "
          "is green: that is coverage lost without a red." % len(orphans))
    for pkg in orphans[:20]:
        print("DETAIL %s" % pkg)

# (2) a pattern that matches nothing
for pat in sorted({p for _, p in pairs}):
    if any(match(pat, k) for k in pkgs):
        continue
    if pat in optional:
        print("NOTICE the pattern %s matches nothing in THIS tree; the spec declares it optional "
              "because the module it names is not published everywhere." % pat)
        continue
    print("FINDING the pattern %s matches no package: it is stale. A shard that names a package "
          "which does not exist looks fuller than it is, and the area it claimed has no owner."
          % pat)

# (3) an empty shard
counts = {s: 0 for s in shards}
for v in owner.values():
    counts[v] = counts.get(v, 0) + 1
legs = {}
for s in shards:
    out = subprocess.run(["bash", os.environ["READER"], "legs", s],
                         capture_output=True, text=True)
    if out.returncode != 0:
        # The same contract as everywhere else: a reader that could not answer is not a
        # shard without legs, and reading it as one would clear an empty shard as a full one.
        print("CANNOT the reader could not list the legs of shard %s: %s"
              % (s, out.stderr.strip().splitlines()[-1] if out.stderr.strip() else "no reason given"))
        sys.exit(0)
    legs[s] = [l for l in out.stdout.split("\n") if l.strip()]
group_shards = {g["shard"] for g in groups}
for s in shards:
    if counts.get(s, 0) == 0 and not legs[s] and s not in group_shards:
        print("FINDING shard %s owns no package, declares no name group and declares no leg: it "
              "is a job that starts a database, runs nothing and reports success." % s)


# (5) THE NAME GROUPS ─────────────────────────────────────────────────────────────────
# The universe of a partitioned package comes through the SAME reader the runner uses, so
# the gate cannot certify a set the run will not see.
group_report = []
for pkg in sorted(split):
    if pkg not in pkgs:
        print("FINDING a group names the package %s, which this tree does not have with tests: "
              "its shards would run a package that is not here, and the tests the file says "
              "are covered are covered by nothing." % pkg)
        continue

    # A pattern that names this package EXACTLY was written to send it whole to one shard.
    # Beside a name split it is not a second opinion, it is the same tests a second time.
    for name, pat in pairs:
        base = pat[:-4] if pat.endswith("/...") else pat
        if base == pkg:
            print("FINDING the package %s is split into name groups and the pattern %s of shard "
                  "%s sends it whole to that shard. That record is INERT — both readers take a "
                  "package with groups out of the pattern assignment, so it owns nothing and "
                  "runs nothing — and an inert record is why shard %s looks fuller than it is: "
                  "it claims a package no longer assigned by any pattern. Delete it or stop "
                  "splitting the package." % (pkg, pat, name, name))

    mine = [g for g in groups if g["pkg"] == pkg]
    rest = [g for g in mine if g["fams"] == ["*"]]
    if len(rest) > 1:
        print("FINDING the package %s declares two remainder groups (%s): every test no other "
              "group names would run twice, in two shards, and the file would read as if each "
              "of them ran it once." % (pkg, " and ".join(sorted(g["name"] for g in rest))))
    if not rest:
        print("FINDING the package %s is split into name groups and none of them is the "
              "remainder `*`: a test added tomorrow would match no group and would never run, "
              "and every shard would stay green while it did not." % pkg)

    out = subprocess.run(["bash", reader, "tests", pkg], capture_output=True, text=True)
    if out.returncode != 0:
        print("CANNOT the reader could not enumerate the tests of %s: %s"
              % (pkg, out.stderr.strip().splitlines()[-1] if out.stderr.strip() else "no reason given"))
        sys.exit(0)
    tests = sorted({l.strip() for l in out.stdout.splitlines() if l.strip()})
    if not tests:
        print("FINDING the package %s is split into name groups and has no top-level test: "
              "every group's -run would match nothing, and `go test` answers 0 to that." % pkg)
        continue

    hits, sizes = {}, {}
    for g in mine:
        if g["fams"] == ["*"]:
            continue
        own = [t for t in tests if any(t.startswith("Test" + fam) for fam in g["fams"])]
        sizes[g["name"]] = len(own)
        if not own:
            print("FINDING the group %s of %s names no test in this tree: its families are "
                  "stale, its `-run` would match nothing, `go test` would exit 0 and the shard "
                  "would report a success having run no test." % (g["name"], pkg))
        for t in own:
            hits.setdefault(t, []).append(g["name"])

    dobles = sorted(t for t, v in hits.items() if len(v) > 1)
    if dobles:
        print("FINDING %d test(s) of %s are named by TWO name groups (a family is a prefix of "
              "another group's family): they would run twice, in two shards, and the group that "
              "bounds its shard is not the one the file says." % (len(dobles), pkg))
        for t in dobles[:20]:
            print("DETAIL %s -> %s" % (t, " and ".join(hits[t])))

    huerfanos = sorted(t for t in tests if t not in hits)
    if rest:
        sizes[rest[0]["name"]] = len(huerfanos)
        # An empty remainder is not an idle group. Its `-run` selects nothing, and `go test`
        # answers 0 to that, so the shard that carries it reports a success having run none
        # of the package's tests. Name more letters, or stop declaring the group.
        if not huerfanos:
            print("FINDING the remainder group %s of %s selects no test: the other groups "
                  "already name all %d of them, so its `-run` would match nothing and the "
                  "shard that carries it would report success having run none."
                  % (rest[0]["name"], pkg, len(tests)))
    elif huerfanos:
        print("FINDING %d test(s) of %s belong to NO name group. Nothing would run them, no "
              "shard would notice, and `go test -run` answers 0 to an expression that selects "
              "nothing — so the silence would arrive as a green check." % (len(huerfanos), pkg))
        for t in huerfanos[:20]:
            print("DETAIL %s" % t)

    # The expression itself, built by the reader — the same call the runner makes — because a
    # limit checked here and applied there is a limit that does not hold.
    for g in mine:
        ex = subprocess.run(["bash", reader, "run-expr", pkg, g["name"]],
                            capture_output=True, text=True)
        if ex.returncode == 2:
            print("CANNOT the reader could not build the -run expression of group %s of %s: %s"
                  % (g["name"], pkg, ex.stderr.strip()))
            sys.exit(0)
        if ex.returncode != 0:
            for line in ex.stderr.strip().splitlines():
                print("FINDING " + line.replace("pr-suite-shards: FAIL — ", ""))

    group_report.append("%s: %s" % (pkg, " ".join(
        "%s->%s(%d)" % (g["name"], g["shard"], sizes.get(g["name"], 0)) for g in mine)))

for line in group_report:
    print("GROUPS " + line)

print("REPARTO " + " ".join("%s=%d" % (s, counts.get(s, 0)) for s in shards))
print("TOTAL %d" % len(pkgs))
PY
)" || cannot "the partition reader failed to run"

# The partition reader can also refuse to look — a package it cannot enumerate is not a
# partition it can certify — and COULD NOT LOOK is never CLEAN.
PARTCANNOT="$(printf '%s\n' "$PART" | sed -n 's/^CANNOT //p')"
[ -z "$PARTCANNOT" ] || cannot "$(printf '%s' "$PARTCANNOT" | head -1)"

NOTICES="$(printf '%s\n' "$PART" | sed -n 's/^NOTICE //p')"
FINDINGS="$(printf '%s\n' "$PART" | sed -n 's/^FINDING //p')"
DETAILS="$(printf '%s\n' "$PART" | sed -n 's/^DETAIL //p')"
REPARTO="$(printf '%s\n' "$PART" | sed -n 's/^REPARTO //p')"
GROUPSPLIT="$(printf '%s\n' "$PART" | sed -n 's/^GROUPS //p')"
TOTAL="$(printf '%s\n' "$PART" | sed -n 's/^TOTAL //p')"

if [ -n "$FINDINGS" ]; then
  while IFS= read -r line; do [ -n "$line" ] && finding "$line"; done <<< "$FINDINGS"
  [ -n "$DETAILS" ] && printf '%s\n' "$DETAILS" | sed 's/^/    /' >&2
  RC=1
fi

[ "$RC" -eq 0 ] || exit 1

say "check-pr-suite-shards: CLEAN — ${TOTAL} package(s) with tests, each in exactly one shard."
say "  universe: ${SOURCE}"
say "  split: ${REPARTO}"
say "  matrix of ${WF_JOB} = ${WF_MATRIX}; ceilings go ${GOTO}m < step ${STEPC}m < job ${JOBC}m."
if [ -n "$GROUPSPLIT" ]; then
  while IFS= read -r line; do
    [ -n "$line" ] && say "  name groups — $line"
  done <<< "$GROUPSPLIT"
fi
if [ -n "$NOTICES" ]; then
  while IFS= read -r line; do [ -n "$line" ] && say "check-pr-suite-shards: notice — $line"; done <<< "$NOTICES"
fi
exit 0
