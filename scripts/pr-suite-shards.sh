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
#   number <key>            a declared clock: go-timeout-minutes | step-ceiling-minutes |
#                           job-ceiling-minutes
#   run [shard]             `go test` over that shard's packages, then its legs. The
#                           shard may come from OLIVARES_TEST_SHARD instead of the argument.
#
# THE PACKAGE UNIVERSE is enumerated module by module, because `go test ./...` in a
# workspace only covers the CURRENT module (golang/go#50745) — the same reason
# scripts/go-work-each.sh exists. Only packages WITH tests are listed: a package without
# tests contributes no coverage and only lengthens the command line.
#
# OLIVARES_PR_SUITE_SHARDS overrides the spec path and OLIVARES_PR_SUITE_PACKAGES
# overrides the universe with a file of import paths. Both exist for the mutation
# battery, which must be able to build a tree with a package in two shards without
# creating the package. `run` PRINTS which universe it used: an override that stayed
# quiet is an override nobody notices.
set -uo pipefail

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || { echo "pr-suite-shards: COULD NOT LOOK — cannot enter $ROOT" >&2; exit 2; }

SPEC="${OLIVARES_PR_SUITE_SHARDS:-ci/pr-suite-shards.txt}"
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
  local m
  while IFS= read -r m; do
    [ -n "$m" ] || continue
    ( cd "$m" && go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./... ) ||
      cannot "go list failed in $m"
  done < <(go work edit -json | sed -n 's/.*"DiskPath": "\(.*\)".*/\1/p') |
    grep -v '^[[:space:]]*$' | LC_ALL=C sort -u
}

universe_source() {
  if [ -n "${OLIVARES_PR_SUITE_PACKAGES:-}" ]; then
    printf 'the file %s\n' "$OLIVARES_PR_SUITE_PACKAGES"
  else
    printf 'go list over the go.work modules\n'
  fi
}

# ── The spec ──────────────────────────────────────────────────────────────────────────
# One python reader, used by every subcommand. It prints records the shell can consume:
#   SHARD <name> · PKG <shard> <pattern> · LEG <shard> <task> · OPTIONAL <pattern>
#   NUM <key> <value> · BAD <reason>
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
    if f[0] in ("PKG", "LEG") and f[1] not in shards:
        bad.append(f"{f[0].lower()} names shard {f[1]!r}, which is never declared")
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
pairs = []
for rec in os.environ["RECORDS"].splitlines():
    f = rec.split()
    if f and f[0] == "PKG":
        pairs.append((f[1], f[2]))

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

UNIVERSE_FILE=""
cleanup() { [ -n "$UNIVERSE_FILE" ] && rm -f "$UNIVERSE_FILE"; }
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
    # instead of typing the names a second time.
    shard_names | python3 -c 'import json,sys;print(json.dumps([l.strip() for l in sys.stdin if l.strip()]))'
    ;;

  packages)
    S="${2:-}"; [ -n "$S" ] || cannot "usage: $0 packages <shard>"
    known_shard "$S" || fail "unknown shard: $S (declared: $(shard_names | paste -sd, -))"
    need_universe
    assign "$S" "$UNIVERSE_FILE" | cut -f2 || exit 1
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
    echo "pr-suite-shards: shard ${S} — universe from $(universe_source)"
    if [ -z "$PKGS" ]; then
      # A shard with no packages is not a fast shard: it is a job that starts a database
      # to run nothing and reports success. It is only acceptable when the shard has legs
      # to run instead, and even then it says so out loud.
      [ "${#LEGS[@]}" -gt 0 ] ||
        fail "shard ${S} owns no package in this tree and declares no leg: it would exit 0 having run nothing"
      echo "pr-suite-shards: shard ${S} owns no package in THIS tree; running its legs only"
    else
      mapfile -t PKG_ARGS <<< "$PKGS"
      echo "pr-suite-shards: shard ${S} — ${#PKG_ARGS[@]} package(s), go test -timeout ${TO}m"
      bash scripts/with-pg-env.sh go test -count=1 -timeout "${TO}m" "${PKG_ARGS[@]}" || exit 1
    fi
    rc=0
    for leg in "${LEGS[@]}"; do
      [ -n "$leg" ] || continue
      echo "pr-suite-shards: shard ${S} — leg ${leg}"
      task "${leg}" || rc=1
    done
    exit "$rc"
    ;;

  *)
    cannot "usage: $0 {shards|matrix|packages <shard>|legs <shard>|number <key>|run <shard>}"
    ;;
esac
