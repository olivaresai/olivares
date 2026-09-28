#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# archive-signed-by.sh SOURCES_FILE — binds one Debian repository to the builder's Debian archive keyring.
#
# config.xml names this script as the customize script of each Debian repository, and KIWI runs it as
# `bash --norc archive-signed-by.sh FILE` on the apt sources file it has just written for that repository:
# at build time, and again for the image's own sources. Every key file archive-keys.json (beside this
# script) locks must exist with its locked sha256; then one `Signed-By:` line naming them is appended.
#
# Why a Signed-By line: apt 3.0.3 in the Debian 13 builder takes a repository's keyring from its Signed-By
# field, else from trusted.gpg.d below the apt directory KIWI sets up, which nothing creates. It reads the
# trusted.gpg file where KIWI's own <signing> keys go only when Dir::Etc::Trusted is set, and KIWI does not
# set it (apt 2.9.24 NEWS: "/etc/apt/trusted.gpg is no longer trusted"; its sources.list(5) still names
# the old default). Nothing is fetched: the keys are the files the builder's debian-archive-keyring package
# installed, and the image installs the same package at the same path.
#
#   exit 0  the Signed-By line is written
#   exit 1  refused and nothing written: a locked key is missing or its sha256 differs from the lock, or the
#           sources file already states its trust (Signed-By or trusted)
#   exit 2  it cannot run: no sources file, or one not named *.sources (KIWI also runs a customize script on
#           a repository's .pref file when it has a priority), or archive-keys.json is unreadable, names no
#           key, or names a key by a relative path or without an exact sha256
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
exec python3 - "$here/archive-keys.json" "${1:-}" <<'EOF'
import hashlib
import json
import re
import sys

lock_path, sources = sys.argv[1], sys.argv[2]


def stop(code, message):
    print("archive-signed-by.sh: " + message, file=sys.stderr)
    sys.exit(code)


if not sources:
    stop(2, "usage: archive-signed-by.sh SOURCES_FILE")
if not sources.endswith(".sources"):
    stop(2, "%s is not an apt sources file; KIWI also runs a customize script on a .pref file" % sources)
try:
    with open(sources) as handle:
        written = handle.read()
except OSError as issue:
    stop(2, "no sources file to bind: %s" % issue)
try:
    with open(lock_path) as handle:
        keys = json.load(handle)["keys"]
    if not isinstance(keys, list) or not keys:
        raise ValueError("no key is locked")
    for key in keys:
        if not (isinstance(key.get("path"), str) and key["path"].startswith("/")
                and isinstance(key.get("sha256"), str) and re.fullmatch(r"[0-9a-f]{64}", key["sha256"])):
            raise ValueError("a key needs an absolute path and an exact sha256: %r" % (key,))
except (OSError, ValueError, KeyError, TypeError, AttributeError) as issue:
    stop(2, "%s names no usable key: %s" % (lock_path, issue))
if re.search(r"(?im)^(signed-by|trusted)[ \t]*:", written):
    stop(1, "%s already states its trust; a repository is bound to the locked keys once" % sources)
for key in keys:
    try:
        with open(key["path"], "rb") as handle:
            actual = hashlib.sha256(handle.read()).hexdigest()
    except OSError as issue:
        stop(1, "the locked key %s cannot be read in the builder: %s" % (key["path"], issue))
    if actual != key["sha256"]:
        stop(1, "the key %s has sha256 %s; archive-keys.json locks %s" % (key["path"], actual, key["sha256"]))
with open(sources, "a") as handle:
    if written and not written.endswith("\n"):
        handle.write("\n")
    handle.write("Signed-By: %s\n" % ", ".join(key["path"] for key in keys))
print("archive-signed-by.sh: %s is verified with %s" % (sources, ", ".join(key["path"] for key in keys)))
EOF
