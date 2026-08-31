#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-nul-in-sources.sh — a NUL byte in a source file turns off this repository's ratchets,
# and nothing else notices.
#
# WHY THIS EXISTS, and it is not hypothetical: it was measured on 2026-08-07 by walking into
# it. Two raw NUL bytes went into commercial/license-worker/src/dodo/cohort.ts and four into
# test/fakes.ts while composing a composite map key. They passed `tsc --noEmit`, 223 unit
# tests, `lint:spdx`, and the entire fast-lint lane. The code even WORKED — U+0000 is a fine
# separator for a composite key, since no provider id or RFC3339 timestamp can contain one.
#
# WHAT IT BREAKS IS THE THING NOBODY TESTS. grep classifies a file containing a NUL as BINARY:
#
#     $ command grep -n 'fragKey' test/fakes.ts
#     grep: test/fakes.ts: binary file matches        <- no line, no number, no content
#
# It still reports a match, so `grep -q` and `grep -c` keep working — and any pipeline that
# reads LINES gets nothing. This repository gates a great deal by grepping its own sources:
# test/dodo-source-anchors.test.ts forbids response shapes at the source, check-spdx.sh reads
# headers, check-export-closure.sh and check-secrets.sh walk the tree, and the pre-push hook's
# fast lane is largely text ratchets. A NUL in a gated file can therefore silently blind the
# gate that guards it, while every other signal stays green. That is the exact failure class
# this project keeps paying for: not a red, but a check that quietly stopped looking.
#
# THE SCOPE, AND WHY ITS INCOMPLETENESS IS SAFE. Real binaries legitimately contain NULs, so
# they are excluded. The exclusion is a DECLARED list of binary formats plus anything
# .gitattributes marks `binary`, and the gate PRINTS both, because a check that will not say
# what it did not look at is the fail-open shape docs/SECURITY-HARDENING.md forbids. If a NEW binary format
# lands and is not on the list, this gate goes RED and names the file — a false positive, which
# is loud and costs one line of configuration. It can never go quiet on a text file, which is
# the direction that matters. Measured at the time of writing: 10118 tracked files, 222 of them
# binary, 0 text files carrying a NUL.
#
# Exit 0 = CLEAN: every in-scope tracked file is free of NUL bytes.
# Exit 1 = DIRTY: at least one is named, with its byte offset.
# Exit 2 = COULD NOT LOOK: no git, no repository, or the file list could not be read.
#          NOT a clean verdict.
set -u
set -o pipefail

say() { printf '%s\n' "$*"; }
cannot_look() {
	say "check-nul: COULD NOT LOOK — $1" >&2
	say "check-nul: this is not a clean verdict. Fix the tooling and run again." >&2
	exit 2
}

command -v git >/dev/null 2>&1 || cannot_look "no git on PATH"
command -v python3 >/dev/null 2>&1 || cannot_look "no python3 on PATH"
ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || cannot_look "not inside a git working tree"
cd "$ROOT" || cannot_look "cannot enter $ROOT"

# Formats whose content is NUL-bearing by construction. Extended by, never replacing, whatever
# .gitattributes declares `binary`. Kept here rather than derived because the derivation would
# have to be "does it contain a NUL", which is the question being asked.
BINARY_EXT="png jpg jpeg gif webp ico bmp tiff woff woff2 ttf otf eot zip gz tgz bz2 xz zst \
7z rar pdf mp4 webm mov avi mkv mp3 wav ogg flac wasm so dylib dll exe bin dat class jar \
mcpb pyc pdn psd sketch fig ico icns"

# Paths .gitattributes marks binary — asked of git rather than parsed, so an attribute added by
# any mechanism (pattern, macro, later override) is honoured.
git ls-files -z >"${TMPDIR:-/tmp}/.nul-files.$$" 2>/dev/null || cannot_look "git ls-files failed"

python3 - "$BINARY_EXT" "${TMPDIR:-/tmp}/.nul-files.$$" <<'PY'
import os, subprocess, sys

binary_ext = {e.strip().lower() for e in sys.argv[1].split() if e.strip()}
listing = sys.argv[2]
try:
    with open(listing, "rb") as fh:
        paths = [p.decode("utf-8", "surrogateescape") for p in fh.read().split(b"\0") if p]
except OSError as e:
    print(f"check-nul: COULD NOT LOOK — cannot read the file list ({e})", file=sys.stderr)
    sys.exit(2)
finally:
    try: os.unlink(listing)
    except OSError: pass

if not paths:
    print("check-nul: COULD NOT LOOK — git listed no tracked files", file=sys.stderr)
    sys.exit(2)

# Ask git which paths carry the `binary` attribute. One call, NUL-delimited, so a path with a
# newline in it cannot split a record.
declared = set()
try:
    out = subprocess.run(
        ["git", "check-attr", "--stdin", "-z", "binary"],
        input="\0".join(paths).encode("utf-8", "surrogateescape"),
        capture_output=True, check=False,
    ).stdout
    fields = out.split(b"\0")
    # records are (path, attr, value)
    for i in range(0, len(fields) - 2, 3):
        if fields[i + 2] == b"set":
            declared.add(fields[i].decode("utf-8", "surrogateescape"))
except Exception:
    # A missing check-attr is not a reason to pass: fall through with an empty set, which can
    # only make the gate STRICTER (a declared-binary file would be flagged, loudly).
    pass

def is_binary_by_name(p: str) -> bool:
    return os.path.splitext(p)[1].lstrip(".").lower() in binary_ext

skipped_ext = skipped_attr = skipped_unreadable = 0
scanned = 0
findings = []

for p in paths:
    if p in declared:
        skipped_attr += 1
        continue
    if is_binary_by_name(p):
        skipped_ext += 1
        continue
    try:
        with open(p, "rb") as fh:
            data = fh.read()
    except FileNotFoundError:
        # Tracked but absent from the working tree (a sparse checkout, a deleted-but-staged
        # path). Counted and named in the scope line, never silently treated as clean.
        skipped_unreadable += 1
        continue
    except OSError:
        skipped_unreadable += 1
        continue
    scanned += 1
    at = data.find(b"\0")
    if at >= 0:
        line = data.count(b"\n", 0, at) + 1
        findings.append((p, at, line, data.count(b"\0")))

total = len(paths)
print(
    f"check-nul: scope — {total} tracked file(s); {scanned} scanned; "
    f"{skipped_ext} skipped by binary extension, {skipped_attr} by a .gitattributes `binary` "
    f"attribute, {skipped_unreadable} unreadable in this working tree."
)

if skipped_unreadable:
    print(
        f"check-nul: NOTE — {skipped_unreadable} tracked path(s) could not be read here, so this "
        f"verdict does not cover them.",
        file=sys.stderr,
    )

if not findings:
    print("check-nul: CLEAN — no NUL byte in any scanned source.")
    sys.exit(0)

print(f"check-nul: DIRTY — {len(findings)} file(s) carry a NUL byte.", file=sys.stderr)
for p, at, line, n in findings:
    print(f"  {p}: {n} NUL byte(s), first at offset {at} (line {line})", file=sys.stderr)
print("", file=sys.stderr)
print("  consequence: grep reports such a file as `binary file matches` and prints NO line, so", file=sys.stderr)
print("               every source-anchor ratchet that reads lines from it stops seeing it —", file=sys.stderr)
print("               silently, while tsc, the unit suites and lint:spdx all stay green.", file=sys.stderr)
print("  repair     : if the byte is intentional (a composite-key separator is a good use of", file=sys.stderr)
print("               U+0000), write it as an ESCAPE — \\u0000 / \\0 — not as a raw byte. If the", file=sys.stderr)
print("               file is genuinely binary, declare it: add its extension to BINARY_EXT in", file=sys.stderr)
print("               this script, or mark the path `binary` in .gitattributes.", file=sys.stderr)
sys.exit(1)
PY
