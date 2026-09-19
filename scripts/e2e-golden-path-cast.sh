#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# e2e-golden-path-cast.sh — record the golden path ON A REAL TERMINAL and publish it as
# an asciicast v2 file anyone can replay.
#
# ⛔ WHY A PTY AND NOT A TEE. `scripts/e2e-golden-path.sh > log` measures a PIPE, and a
#    CLI that is honest about the difference — this one is: the renderer chooses its
#    width and its plain form from the terminal it has — is then recorded in a shape no
#    operator ever sees. Under `script` the harness gets a controlling terminal of a
#    declared size, so the cast shows what the product looks like to a person.
#
# ⛔ THE RAW LOG IS REDACTED BEFORE IT IS CONVERTED, and then removed. The harness never
#    prints the one-time setup token (it reads it from a file), but a recording of a
#    terminal is exactly the artifact that must not be the first place that stops being
#    true.
#
# Usage: scripts/e2e-golden-path-cast.sh OUT.cast [harness options...]
# Requires: script(1) from util-linux, python3.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${1:?usage: e2e-golden-path-cast.sh OUT.cast [harness options...]}"
shift || true

command -v script >/dev/null 2>&1 || { echo "e2e-golden-path-cast.sh: script(1) is not on PATH" >&2; exit 2; }

COLS="${CAST_COLS:-120}"
ROWS="${CAST_ROWS:-40}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

set +e
COLUMNS="$COLS" LINES="$ROWS" TERM=xterm-256color \
  script --quiet --return --command "COLUMNS=$COLS LINES=$ROWS bash $ROOT/scripts/e2e-golden-path.sh $*" \
    --log-out "$WORK/raw" --log-timing "$WORK/timing"
RC=$?
set -e

# Same filter the harness applies to every capture it writes.
sed -E -e 's/olst_[A-Za-z0-9_-]{6,}/olst_<redacted>/g' \
       -e 's/(Bearer|bearer|token=)[[:space:]]*[A-Za-z0-9._-]{16,}/\1 <redacted>/g' \
       "$WORK/raw" >"$WORK/raw.redacted"
mv "$WORK/raw.redacted" "$WORK/raw"

python3 - "$WORK/raw" "$WORK/timing" "$OUT" "$COLS" "$ROWS" <<'PY'
"""Convert a script(1) PTY recording into asciicast v2.

script writes the bytes in one file and "<delay> <count>" pairs in the other, so the
cast is a faithful re-timing of the same bytes rather than a re-render of them: nothing
here interprets an escape sequence, and a player shows exactly what the terminal did.
"""
import json
import sys

raw_path, timing_path, out_path, cols, rows = sys.argv[1:6]
with open(raw_path, 'rb') as fh:
    raw = fh.read()

events, at, pos = [], 0.0, 0
with open(timing_path, encoding='ascii', errors='replace') as fh:
    for line in fh:
        parts = line.split()
        if len(parts) != 2:
            continue
        try:
            delay, count = float(parts[0]), int(parts[1])
        except ValueError:
            continue
        at += delay
        chunk = raw[pos:pos + count]
        pos += count
        if not chunk:
            continue
        events.append([round(at, 6), 'o', chunk.decode('utf-8', 'replace')])

# Anything script(1) wrote after its last timing pair still belongs to the recording.
if pos < len(raw):
    events.append([round(at, 6), 'o', raw[pos:].decode('utf-8', 'replace')])

with open(out_path, 'w', encoding='utf-8') as out:
    out.write(json.dumps({
        'version': 2,
        'width': int(cols),
        'height': int(rows),
        'timestamp': 0,
        'env': {'TERM': 'xterm-256color', 'SHELL': '/bin/bash'},
    }) + '\n')
    for e in events:
        out.write(json.dumps(e) + '\n')
print('%s: %d frames, %.1f s' % (out_path, len(events), events[-1][0] if events else 0.0))
PY

echo "cast: $OUT (harness exit $RC)"
exit "$RC"
