#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# INT-12 unique leftover unique vs #1038: hub-safe refuse of ent#58 as-is.
# Does not remasure the overlay clone, so lint:addon-sets does not LOOK 2
# without OLIVARES_ENT_DIR.
# Three answers: 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-int-12-no-land-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-int-12-no-land-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

DOC="${OLIVARES_INT12_DOC:-design/INT-12-NO-LAND-ENT58-2026-08-20.md}"
[ -r "$DOC" ] || cannot "missing $DOC"
command -v python3 >/dev/null || cannot "no python3"

grep -q 'Unique leftover unique vs `#1038`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1038"
grep -q 'no se aterriza tal cual' "$DOC" \
  || fail "prepare doc lost the refuse"

python3 - "$DOC" <<'PY' || exit $?
import re, sys

text = open(sys.argv[1], encoding="utf-8").read()

def fail(msg):
    print(f"check-int-12-no-land-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-int-12-no-land-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

def kv(key):
    m = re.search(rf"^{re.escape(key)}:\s*(\S+)\s*$", text, flags=re.M)
    if not m:
        cannot(f"measure lost {key}")
    return m.group(1)

if kv("int-12-land-as-is") != "no":
    fail("int-12-land-as-is is not no — landing ent#58 as-is is the finding")
if kv("int-12-pr") != "58":
    fail("int-12-pr is not 58")

head = kv("int-12-head")
if not re.fullmatch(r"[0-9a-f]{40}", head):
    fail(f"int-12-head is not a 40-hex object id: {head!r}")

if kv("allows-additional-active-idp-on-overlay-main") != "yes":
    fail("compile premise restored: overlay main already has AllowsAdditionalActiveIdP")
if kv("snapshot-on-overlay-main") != "deliberately-ungated":
    fail("overlay main Snapshot posture lost — #58 gates it; as-is would revert doctrine")
if kv("int-12-pin-moves-backwards") != "yes":
    fail("int-12-pin-moves-backwards is not yes — the refuse is unexplained")

print("check-int-12-no-land-prep: CLEAN — ent#58 not landed as-is; hub-safe pin.")
PY
