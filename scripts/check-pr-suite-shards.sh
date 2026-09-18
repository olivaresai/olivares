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
trap 'rm -f "$UNI"' EXIT

if [ -n "${OLIVARES_PR_SUITE_PACKAGES:-}" ]; then
  [ -r "$OLIVARES_PR_SUITE_PACKAGES" ] ||
    cannot "OLIVARES_PR_SUITE_PACKAGES=$OLIVARES_PR_SUITE_PACKAGES is not readable"
  grep -v '^[[:space:]]*$' "$OLIVARES_PR_SUITE_PACKAGES" | LC_ALL=C sort -u > "$UNI"
  SOURCE="the file $OLIVARES_PR_SUITE_PACKAGES"
else
  [ -f go.work ] || cannot "go.work is not at $ROOT and no package file was given"
  command -v go >/dev/null 2>&1 || cannot "no Go toolchain: the packages cannot be enumerated"
  while IFS= read -r m; do
    [ -n "$m" ] || continue
    ( cd "$m" && go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./... ) ||
      cannot "go list failed in $m"
  done < <(go work edit -json | sed -n 's/.*"DiskPath": "\(.*\)".*/\1/p') |
    grep -v '^[[:space:]]*$' | LC_ALL=C sort -u > "$UNI"
  SOURCE="go list over the go.work modules"
fi
[ -s "$UNI" ] || cannot "the package universe came back empty — a partition of nothing certifies nothing"

# ── the spec, through the ONE reader ─────────────────────────────────────────────────
SHARDS="$(bash "$READER" shards)" || { finding "$SPEC cannot be read as a partition (the reader refused; its reasons are above)"; exit 1; }
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
pairs, optional = [], set()
with open(spec, encoding="utf-8") as fh:
    for raw in fh:
        f = raw.split("#", 1)[0].split()
        if len(f) == 3 and f[0] == "pkg":
            pairs.append((f[1], f[2]))
        elif len(f) == 2 and f[0] == "optional":
            optional.add(f[1])

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
    legs[s] = [l for l in out.stdout.split("\n") if l.strip()]
for s in shards:
    if counts.get(s, 0) == 0 and not legs[s]:
        print("FINDING shard %s owns no package and declares no leg: it is a job that starts a "
              "database, runs nothing and reports success." % s)

print("REPARTO " + " ".join("%s=%d" % (s, counts.get(s, 0)) for s in shards))
print("TOTAL %d" % len(pkgs))
PY
)" || cannot "the partition reader failed to run"

NOTICES="$(printf '%s\n' "$PART" | sed -n 's/^NOTICE //p')"
FINDINGS="$(printf '%s\n' "$PART" | sed -n 's/^FINDING //p')"
DETAILS="$(printf '%s\n' "$PART" | sed -n 's/^DETAIL //p')"
REPARTO="$(printf '%s\n' "$PART" | sed -n 's/^REPARTO //p')"
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
if [ -n "$NOTICES" ]; then
  while IFS= read -r line; do [ -n "$line" ] && say "check-pr-suite-shards: notice — $line"; done <<< "$NOTICES"
fi
exit 0
