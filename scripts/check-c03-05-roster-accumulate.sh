#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C03-05: Module.Sync continues after a failed Snapshot.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c03-05-roster-accumulate: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c03-05-roster-accumulate: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0305_JSON:-design/c03-05-roster-accumulate.json}"
DOC="${OLIVARES_C0305_DOC:-design/C03-05-ROSTER-ACCUMULATE-2026-08-20.md}"
GO="${OLIVARES_C0305_GO:-modules/governance/roster.go}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$GO" ] || cannot "missing roster source"

grep -q 'HOLD on gating Snapshot' "$DOC" || fail "$DOC lost HOLD on gating Snapshot"
grep -q 'accumulates' "$DOC" || fail "$DOC lost accumulates"
if grep -qiE 'Snapshot gated|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi

python3 - "$JSON" <<'PY' || fail "JSON flags drifted"
import json, re, sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("sync_accumulates") is not True:
    raise SystemExit("sync_accumulates must be true")
if data.get("snapshot_gated") is not False:
    raise SystemExit("snapshot_gated must stay false")
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
PY

python3 - "$GO" <<'PY' || fail "Sync no longer accumulates Snapshot errors"
import re, sys
text = open(sys.argv[1], encoding="utf-8").read()
m = re.search(r"func \(m \*Module\) Sync\(ctx context.Context\) error \{(.+?)\n\}", text, re.S)
if not m:
    raise SystemExit("Sync not found")
body = m.group(1)
if "b.Provider.Snapshot" not in body:
    raise SystemExit("Sync lost Snapshot")
if "errs = append(errs, err)" not in body:
    raise SystemExit("Sync lost append")
if "continue" not in body:
    raise SystemExit("Sync lost continue")
if "errors.Join(errs...)" not in body:
    raise SystemExit("Sync lost Join")
# A return err immediately after Snapshot is the abort this lote refuses.
if re.search(r"graph, err := b\.Provider\.Snapshot\(ctx\)\s+if err != nil \{\s+return err", body):
    raise SystemExit("Sync aborts on the first Snapshot error")
PY

say "check-c03-05-roster-accumulate: CLEAN — Sync accumulates Snapshot errors; Snapshot ungated."
exit 0
