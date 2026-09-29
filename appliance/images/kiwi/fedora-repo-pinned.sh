#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# fedora-repo-pinned.sh REPO_FILE — binds one Fedora repository to its pinned metadata and to the locked Fedora 44 key.
#
# config.xml names this script as the customize script of each Fedora repository, and KIWI 11.0.4 runs it as
# `bash --norc fedora-repo-pinned.sh FILE` on the dnf5 .repo file it has just written (repository/dnf5.py add_repo,
# base.py run_repo_customize). fedora-repositories.json (beside this script) pins the repository's repomd.xml by sha256
# and the key by sha256. Fedora does not sign repomd.xml, so the pinned sha256 is what binds the metadata: the script
# reads BASEURL/repodata/repomd.xml once and refuses a repository whose metadata moved since the pin. Then it appends
# one `gpgkey` line naming the locked key file, which the builder's fedora-gpg-keys package installed.
#
#   exit 0  the gpgkey line is written
#   exit 1  refused and nothing written: the metadata moved or cannot be read, a locked key is missing or its sha256
#           differs, or the file already states its trust or turns a check off (gpgkey, gpgcheck = 0, sslverify = 0)
#   exit 2  it cannot run: no file, one not named *.repo, a section this lock does not pin, or an unreadable lock
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
exec python3 - "$here/fedora-repositories.json" "${1:-}" <<'EOF'
import configparser
import hashlib
import json
import sys
import urllib.request

lock_path, repo_file = sys.argv[1], sys.argv[2]


def stop(code, message):
    print("fedora-repo-pinned.sh: " + message, file=sys.stderr)
    sys.exit(code)


if not repo_file.endswith(".repo"):
    stop(2, "not a dnf .repo file: %r" % repo_file)
try:
    text = open(repo_file).read()
    lock = json.load(open(lock_path))
    pins, key = lock["repositories"], lock["key"]
    key_path, key_sha256 = key["path"], key["sha256"]
except (OSError, ValueError, KeyError, TypeError) as issue:
    stop(2, "cannot read the repository file or its lock: %s" % issue)
parser = configparser.ConfigParser(interpolation=None)
parser.read_string(text)
sections = parser.sections()
if len(sections) != 1 or sections[0] not in pins:
    stop(2, "the file holds %r; fedora-repositories.json pins %s" % (sections, sorted(pins)))
name = sections[0]
section, pin = parser[name], pins[name]
for option, refused in (("gpgkey", None), ("gpgcheck", "0"), ("sslverify", "0")):
    if option in section and (refused is None or section[option].strip() == refused):
        stop(1, "%s already states %s = %s" % (repo_file, option, section[option]))
try:
    with open(key_path, "rb") as handle:
        found = hashlib.sha256(handle.read()).hexdigest()
except OSError as issue:
    stop(1, "the locked key %s cannot be read: %s" % (key_path, issue))
if found != key_sha256:
    stop(1, "the key %s has sha256 %s; fedora-repositories.json locks %s" % (key_path, found, key_sha256))
baseurl = section.get("baseurl", "").strip()
if baseurl.rstrip("/") + "/" != pin["baseurl"].rstrip("/") + "/":
    stop(1, "%s names baseurl %r; the pin is for %r" % (repo_file, baseurl, pin["baseurl"]))
try:
    with urllib.request.urlopen(baseurl.rstrip("/") + "/repodata/repomd.xml", timeout=60) as response:
        served = hashlib.sha256(response.read()).hexdigest()
except OSError as issue:
    stop(1, "the metadata of %s cannot be read: %s" % (name, issue))
if served != pin["repomd_sha256"]:
    stop(1, "the metadata of %s moved since %s: repomd.xml sha256 %s, pinned %s; re-pin it, never loosen it"
         % (name, pin["recorded_at"], served, pin["repomd_sha256"]))
with open(repo_file, "w") as handle:
    handle.write(text.rstrip("\n") + "\ngpgkey = file://%s\n\n" % key_path)
print("fedora-repo-pinned.sh: %s metadata %s as pinned at %s, key %s" % (name, served[:16], pin["recorded_at"], key_path))
EOF
