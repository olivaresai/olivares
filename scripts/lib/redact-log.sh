#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Engine logs carry the one-time first-boot token in their banner. Any failure path that shows part
# of a log pipes it through here first, so a failed capture or test cannot publish the token it was
# about to consume. The token is read from the log by path; it never reaches argv.
#
# Fail closed: an unreadable log, a setup banner whose token line the parser no longer recognizes, or
# a "Token: " line outside a recognized banner prints a refusal placemark instead of the text. A
# banner that drifts must not turn the redactor into a printer. The shape backstop covers the token
# only when the log carries no token line at all.
redact_log() { # redact_log LOGFILE; stdin is the text to print
  python3 -I -c '
import re
import sys

path = sys.argv[1]
try:
    with open(path, encoding="utf-8", errors="replace") as f:
        lines = f.read().splitlines()
except OSError:
    sys.stdout.write("[log withheld: cannot read the engine log]\n")
    sys.exit(0)
secrets = set()
read = set()
unreadable = False
for i, line in enumerate(lines):
    if line != "=== FIRST-BOOT SETUP ===":
        continue
    unreadable = True
    for j in range(i + 1, len(lines)):
        if lines[j].startswith("  Token: "):
            value = lines[j][len("  Token: "):].strip()
            if value:
                secrets.add(value)
                read.add(j)
                unreadable = False
            break
data = sys.stdin.read()
# Every banner of a restarted log is read. A "Token: " line no banner gave up (a prefixed or
# reworded header) is a token the redactor cannot name: withhold, as for a banner it cannot parse.
if unreadable or any("Token: " in line and j not in read for j, line in enumerate(lines)):
    sys.stdout.write("[log withheld: the first-boot token could not be read]\n")
    sys.exit(0)
for secret in secrets:
    data = data.replace(secret, "[redacted]")
sys.stdout.write(re.sub(r"\bolst_[A-Z2-7]{16,}\b", "[redacted]", data))
' "$1"
}

log_tail() { # log_tail LOGFILE [LINES]
  tail -"${2:-20}" "$1" 2>/dev/null | redact_log "$1" >&2 || echo "  (no log)" >&2
}
