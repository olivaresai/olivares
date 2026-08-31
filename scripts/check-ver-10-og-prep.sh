#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# VER-10 unique leftover unique vs #1038: hub-safe pin. 13 locales, not 14;
# not 14 OG-per-page; looked: yes. Live web remasure is opt-in so
# lint:addon-sets does not LOOK 2 without the web clone.
# Three answers: 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-ver-10-og-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-ver-10-og-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

DOC="${OLIVARES_VER10_DOC:-design/VER-10-OG-WEB-2026-08-20.md}"
[ -r "$DOC" ] || cannot "missing $DOC"
command -v python3 >/dev/null || cannot "no python3"

grep -q 'Unique leftover unique vs `#1038`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1038"
grep -q 'looked: yes' "$DOC" \
  || fail "prepare doc lost looked: yes"

python3 - "$DOC" <<'PY' || exit $?
import re, sys

text = open(sys.argv[1], encoding="utf-8").read()

def fail(msg):
    print(f"check-ver-10-og-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-ver-10-og-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

def kv(key):
    m = re.search(rf"^{re.escape(key)}:\s*(\S+)\s*$", text, flags=re.M)
    if not m:
        cannot(f"measure lost {key}")
    return m.group(1)

if kv("ver-10-looked") != "yes":
    fail("ver-10-looked is not yes — VER-10 claiming cannot-look after looking")

sha = kv("measured-web-sha")
if not re.fullmatch(r"[0-9a-f]{40}", sha):
    fail(f"measured-web-sha is not a 40-hex object id: {sha!r}")

locales = kv("measured-locales")
if locales == "14":
    fail("measured-locales is 14 — the backlog premise, not this tree")
if locales != "13":
    fail(f"measured-locales want 13, got {locales!r}")

if re.search(r"^measured-og-per-page:\s*14\s*$", text, flags=re.M):
    fail("measured-og-per-page is 14 — the false per-page claim")

png = kv("measured-og-png")
try:
    n_png = int(png)
except ValueError:
    fail(f"measured-og-png is not an integer: {png!r}")
if n_png == 14:
    fail("measured-og-png is 14 — that is the false per-page claim, not a PNG count")
if n_png < 1:
    fail("measured-og-png is empty")

print("check-ver-10-og-prep: CLEAN — looked; 13 locales; not 14 OG/page.")
PY
