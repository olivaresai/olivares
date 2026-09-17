#!/usr/bin/env sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-verifier-truth.sh — the anti-theater lint.
#
# A VERIFIER must never assert success it did not check. This gate scans every
# non-test, non-generated Go file under the public Go roots and, INSIDE the body
# of assurance functions named Verify* / Attest* / *Completeness* / Ensure* /
# Confirm* / Prove* / Certif*, forbids:
#
#   x := true            success initialized as a literal (the "assume, then
#                        maybe adjust" shape — keyDestroyed := true)
#   ... || true          a tautology (!policy.WORMCoordination || true)
#   Field: true          a struct-literal boolean claim asserted without a check
#                        (ResidualScanResult{..., Clean: true})
#
# The honest shape initializes claims FALSE and flips them only behind evidence
# (x := false; if checked { x = true }) — plain `x = true` reassignment inside a
# guarded branch is allowed.
#
# Escape hatch (use sparingly, justify inline):
#   someLine := true //verifier-truth:allow <why this literal is not a claim>
#
# Wired into the public Go-toolchain gate (Taskfile.yml lint:verifier-truth).
set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

fail=0
# The file list is built and CHECKED before the loop. `for f in $(find ...)` discards
# find's status — set -e does not reach into a for-list command substitution — so a
# source root that was renamed or moved left the list empty, the loop ran zero times,
# fail stayed 0 and the gate reported that every verifier derives its success from
# evidence, having read no verifier at all.
VT_ROOTS="core modules connectors cmd sdk"
if [ -d cloud ]; then
  VT_ROOTS="$VT_ROOTS cloud"
elif [ -f .olivares-public-export ]; then
  # sanctioned ONLY in a marked curated export; in the hub a vanished root is fatal
  echo "check-verifier-truth: no cloud/ (curated public export) — sweeping the shipped roots."
else
  echo "check-verifier-truth: cloud/ is MISSING and this tree carries no public-export marker; refusing." >&2
  exit 2
fi
for d in $VT_ROOTS; do
  [ -d "$d" ] || { echo "check-verifier-truth: source root '$d' is missing; the sweep would be" >&2
    echo "  silently short. Fix the root list in this script if it moved." >&2; exit 2; }
done
VT_FILES="$(find $VT_ROOTS -type f -name '*.go' \
  ! -name '*_test.go' ! -name '*.gen.go' \
  ! -path '*/node_modules/*' ! -path '*/vendor/*' | sort)"
[ -n "$VT_FILES" ] || { echo "check-verifier-truth: the sweep found no Go sources at all; UNVERIFIED." >&2; exit 2; }
scanned=0
for f in $VT_FILES; do
  scanned=$((scanned + 1))
  hits="$(awk '
    BEGIN { depth = 0; inv = 0 }
    {
      line = $0
      # Enter a matched verifier function (top-level func or method).
      if (inv == 0 && line ~ /^func (\([^)]*\) *)?(Verify[A-Za-z0-9_]*|Attest[A-Za-z0-9_]*|[A-Za-z0-9_]*Completeness[A-Za-z0-9_]*|Ensure[A-Za-z0-9_]*|Confirm[A-Za-z0-9_]*|Prove[A-Za-z0-9_]*|Certif[A-Za-z0-9_]*)\(/) {
        inv = 1
        depth = 0
      }
      if (inv == 1) {
        # Track brace depth to find the end of the function body.
        n = gsub(/{/, "{", line); m = gsub(/}/, "}", line)
        depth += n - m
        if (line !~ /\/\/verifier-truth:allow/) {
          if (line ~ /:= *true([^A-Za-z0-9_]|$)/ ||
              line ~ /\|\| *true([^A-Za-z0-9_]|$)/ ||
              line ~ /[A-Z][A-Za-z0-9_]*: *true([^A-Za-z0-9_]|$)/) {
            printf "%d: %s\n", NR, $0
          }
        }
        if (depth <= 0 && NR > 1 && line ~ /}/) { inv = 0 }
      }
    }
  ' "$f")"
  if [ -n "$hits" ]; then
    echo "verifier-truth: literal success in a verifier — every claim needs a check ($f):" >&2
    echo "$hits" | sed 's/^/  /' >&2
    fail=1
  fi
done

if [ "$fail" -ne 0 ]; then
  echo "" >&2
  echo "verifier-truth: FAIL — an assurance verifier asserts a literal boolean claim." >&2
  echo "Initialize claims false and flip them behind evidence, or justify the line with" >&2
  echo "//verifier-truth:allow <reason>." >&2
  exit 1
fi
echo "verifier-truth: OK across ${scanned} files (no literal success claims in public assurance verifiers)"
