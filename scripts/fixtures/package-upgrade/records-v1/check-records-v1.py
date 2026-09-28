#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Parse PKG-L1 package records by the v1 grammar.

The grammar is the agreed m-5 schema: pkg-l1-m5-schema/RESPONSE-D-M5-CONTRAST.md
(f5774e95), which amends M5-SCHEMA-PROPOSAL-D.md (209bd0f9) by 9b's contrast (5c5a8183).

Usage: check-records-v1.py [--grammar] FILE...
  Each FILE is a pkg-phases file (lines beginning "v1 ") or one key=value record
  (first line "schema=olivares.pkg-<name>/v1"). Exit 0 when every file is accepted,
  1 when any is refused (the reasons go to stderr), 2 on a usage or read error.
  By default a record or line must be usable as evidence: boot= must be the kernel
  boot_id (a UUID). --grammar accepts boot=unknown, which the scripts write when
  /proc cannot be read; such a record is well-formed but never evidence.
"""
import re
import sys

LINE_MAX = 512      # bytes, including the newline
RECORD_MAX = 1024   # bytes, the whole record

V = r"[0-9A-Za-z.+~:-]{1,40}"
UUID = r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"
UTC = r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z"
SCRIPT = r"postinstall|preremove|postremove|apk-preupgrade"
RECORD_NAMES = r"upgrade-state|deconfigured|pending|install-state"
BATCH = r"[0-9a-f]{32}\.[1-9][0-9]{0,19}|absent|none"
PID = r"[1-9][0-9]{0,9}"
START = r"[0-9]{1,20}|unknown"

LINE_FIELDS = [
    ("script", SCRIPT),
    ("maintscript", r"preinst|postinst|prerm|postrm|unknown|n/a"),
    ("format", r"deb|rpm|apk|unknown"),
    ("action", r"[a-z][a-z-]{0,23}"),
    ("result", r"ok|refused:[a-z][a-z-]{0,39}|record-too-large|record-not-written:(?:%s)|old-manifest-unread" % RECORD_NAMES),
    ("old", V),
    ("new", V),
    ("stopped", r"yes|no|n/a"),
    ("prior", r"active|inactive|failed|unknown|n/a"),
    ("boot", UUID + "|unknown"),
    ("pid", PID),
    ("pid_start", START),
    ("ppid", r"[0-9]{1,10}"),
    ("ppid_start", START),
    ("batch", BATCH),
    ("contract", r"upgrade-dispatch-v1"),
]
LINE_RE = re.compile(
    r"v1 (?P<utc>%s)" % UTC + "".join(r" %s=(?P<%s>%s)" % (k, k, rx) for k, rx in LINE_FIELDS)
)
SHORT_RE = re.compile(r"v1 %s script=(?:%s) result=record-too-large" % (UTC, SCRIPT))

COMMON = [
    ("boot", UUID + "|unknown"),
    ("pid", PID),
    ("pid_start", START),
    ("ppid", r"[0-9]{1,10}"),
    ("ppid_start", START),
    ("batch", BATCH),
    ("maintscript", r"preinst|postinst|prerm|postrm|unknown|n/a"),
    ("format", r"deb|rpm|apk|unknown"),
    ("old", V),
    ("new", V),
    ("contract", r"upgrade-dispatch-v1"),
]
OWN = {
    "upgrade-state": [("enabled", r"[a-z-]{1,64}"), ("active", r"[a-z-]{1,64}")],
    "deconfigured": [("active", r"[a-z-]{1,64}")],
    "pending": [
        ("condition", r"[a-z][a-z_-]{0,63}"),
        ("reason", r"[a-z][a-z-]{0,63}"),
        ("prior", r"[a-z/_-]{1,900}"),
        ("instruction", r"[ -~]{0,400}"),
    ],
    "install-state": [("group_created", r"true|false"), ("user_created", r"true|false")],
}


def check_line(line, strict):
    """Return a reason string, or None when the line is accepted."""
    if len(line.encode()) + 1 > LINE_MAX:
        return "oversized line (%d bytes > %d)" % (len(line.encode()) + 1, LINE_MAX)
    if SHORT_RE.fullmatch(line):
        return None
    m = LINE_RE.fullmatch(line)
    if not m:
        if not line.startswith("v1 "):
            return "unknown line version"
        for k, rx in LINE_FIELDS:
            if not re.search(r" %s=(?:%s)(?: |$)" % (re.escape(k), rx), line):
                return "missing or malformed field %s" % k
        return "fields out of the fixed v1 order"
    if strict and m.group("boot") == "unknown":
        return "boot=unknown is never evidence"
    return None


def check_record(text, strict):
    if len(text.encode()) > RECORD_MAX:
        return "oversized record (%d bytes > %d)" % (len(text.encode()), RECORD_MAX)
    if not text.endswith("\n"):
        return "record does not end with a newline"
    lines = text[:-1].split("\n")
    m = re.fullmatch(r"schema=olivares\.pkg-(%s)/v1" % RECORD_NAMES, lines[0])
    if not m:
        return "unknown schema: %r" % lines[0][:60]
    name = m.group(1)
    if lines[1:] == ["result=record-too-large"]:
        return None
    fields = COMMON + OWN[name]
    if len(lines) - 1 != len(fields):
        return "want %d fields after schema=, found %d" % (len(fields), len(lines) - 1)
    for (key, rx), got in zip(fields, lines[1:]):
        k, sep, v = got.partition("=")
        if not sep or k != key:
            return "field %r where %s= belongs" % (got[:40], key)
        if not re.fullmatch(rx, v):
            return "malformed %s=%r" % (key, v[:60])
        if key == "boot" and strict and v == "unknown":
            return "boot=unknown is never evidence"
    return None


def check_file(path, strict):
    try:
        with open(path, "rb") as f:
            data = f.read()
    except OSError as exc:
        print("%s: unreadable: %s" % (path, exc), file=sys.stderr)
        raise SystemExit(2)
    try:
        text = data.decode("ascii")
    except UnicodeDecodeError:
        return ["not ASCII"]
    if text.startswith("schema="):
        reason = check_record(text, strict)
        return [reason] if reason else []
    if not text:
        return ["empty file"]
    if not text.endswith("\n"):
        return ["last line has no newline"]
    reasons = []
    for n, line in enumerate(text[:-1].split("\n"), 1):
        reason = check_line(line, strict)
        if reason:
            reasons.append("line %d: %s" % (n, reason))
    return reasons


def main(argv):
    strict = True
    if argv and argv[0] == "--grammar":
        strict = False
        argv = argv[1:]
    if not argv:
        print(__doc__, file=sys.stderr)
        return 2
    refused = False
    for path in argv:
        for reason in check_file(path, strict):
            refused = True
            print("%s: refused: %s" % (path, reason), file=sys.stderr)
    return 1 if refused else 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
