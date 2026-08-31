#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-d1-slot-census.sh — CFG-09. The directory and the census must
# name the same prefixes. A file whose prefix is do_not_reuse, or
# below next_free, is the silent D1 bomb. Three answers.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-d1-slot-census: FAIL — $*" >&2; exit 1; }
cannot() { say "check-d1-slot-census: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"
CENSUS=design/d1-slot-census.json
MIG=commercial/license-worker/migrations
[ -r "$CENSUS" ] || cannot "missing $CENSUS"
[ -d "$MIG" ] || cannot "missing $MIG"

python3 - "$CENSUS" "$MIG" <<'PY' || exit $?
import json, os, re, sys

census_path, mig = sys.argv[1], sys.argv[2]

def fail(msg):
    print(f"check-d1-slot-census: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)
def cannot(msg):
    print(f"check-d1-slot-census: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    doc = json.loads(open(census_path, encoding="utf-8").read())
except Exception as e:
    cannot(f"census is not JSON: {e}")

if doc.get("schema") != "olivares.d1-slot-census.v1":
    fail(f"unknown schema {doc.get('schema')!r}")

occupied = doc.get("occupied") or []
by_prefix = {}
by_file = {}
for row in occupied:
    p, f = row.get("prefix"), row.get("file")
    if not p or not f:
        fail("occupied row missing prefix/file")
    if p in by_prefix:
        fail(f"census lists prefix {p} twice")
    by_prefix[p] = f
    by_file[f] = p

do_not = set(doc.get("do_not_reuse") or [])
next_free = doc.get("next_free")
if not re.fullmatch(r"\d{4}", str(next_free or "")):
    fail(f"next_free {next_free!r} is not a 4-digit prefix")

pat = re.compile(r"^(\d{4})_.+\.sql$")
on_disk = []
for name in sorted(os.listdir(mig)):
    if not name.endswith(".sql"):
        continue
    m = pat.match(name)
    if not m:
        fail(f"{name} is not NNNN_*.sql")
    on_disk.append((m.group(1), name))

disk_files = {f for _, f in on_disk}
census_files = set(by_file)
if disk_files != census_files:
    extra = sorted(disk_files - census_files)
    missing = sorted(census_files - disk_files)
    if extra:
        fail(f"migrations not in census: {', '.join(extra)}")
    if missing:
        fail(f"census names missing files: {', '.join(missing)}")

for prefix, name in on_disk:
    if prefix in do_not:
        fail(f"{name} reuses banned prefix {prefix} (0022 is Exit 1; gaps under 0028 are not free)")
    if prefix < next_free and prefix not in by_prefix:
        fail(f"{name} uses {prefix} < next_free {next_free}")

print(f"check-d1-slot-census: CLEAN — {len(on_disk)} occupied; next_free={next_free}; no banned prefix.")
PY
