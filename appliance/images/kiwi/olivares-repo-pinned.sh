#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# olivares-repo-pinned.sh REPO_FILE — binds one of the Fedora profile's own repositories to its pinned key.
#
# config.xml names this script as the customize script of olivares-appliance-rpms (the product repository D's S3
# delivers) and olivares-appliance-build (the dracut RPM the stage step signs with a per-build key). KIWI 11.0.4 runs it
# as `bash --norc olivares-repo-pinned.sh FILE` on the dnf5 .repo file it has just written (repository/dnf5.py add_repo,
# base.py run_repo_customize). olivares-repositories.json (written by build.sh from the recipe's pin, or the explicit
# qualification key) and olivares-build-repository.json (written by the stage step) name each repository's key file
# beside this script and its fingerprint. The script checks the key's OpenPGP fingerprint, requires both signature
# checks on, and appends one `gpgkey` line naming the key at its path in the builder (/description).
#
#   exit 0  the gpgkey line is written
#   exit 1  refused and nothing written: the key's fingerprint is not the pinned one, a check is not on (gpgcheck and
#           repo_gpgcheck must both be 1), the file already names a gpgkey or turns sslverify off, or an option repeats
#   exit 2  it cannot run: no file, one not named *.repo, a section no pin names, or unreadable pins
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
exec python3 - "$here" "${1:-}" <<'EOF'
import configparser
import json
import sys
from pathlib import Path

here, repo_file = Path(sys.argv[1]), sys.argv[2]
sys.path.insert(0, str(here))
from delivery_check import fingerprint  # noqa: E402

MOUNT = "/description"


def stop(code, message):
    print("olivares-repo-pinned.sh: " + message, file=sys.stderr)
    sys.exit(code)


if not repo_file.endswith(".repo"):
    stop(2, "not a dnf .repo file: %r" % repo_file)
pins = {}
try:
    for name in ("olivares-repositories.json", "olivares-build-repository.json"):
        if (here / name).exists():
            pins.update(json.loads((here / name).read_text()))
    text = Path(repo_file).read_text()
except (OSError, ValueError) as issue:
    stop(2, "cannot read the repository file or its pins: %s" % issue)
parser = configparser.ConfigParser(interpolation=None, strict=True)
try:
    parser.read_string(text)
except configparser.Error as issue:
    stop(1, "the repository file is ambiguous: %s" % issue)
sections = parser.sections()
if len(sections) != 1 or sections[0] not in ("olivares-appliance-rpms", "olivares-appliance-build") \
        or sections[0] not in pins:
    stop(2, "the file holds %r; this script pins olivares-appliance-rpms and olivares-appliance-build, and the pins name %s"
         % (sections, sorted(pins)))
name = sections[0]
section, pin = parser[name], pins[name]
if "gpgkey" in section or section.get("sslverify", "1").strip() == "0":
    stop(1, "%s already names a gpgkey or turns sslverify off" % repo_file)
checks = (section.get("gpgcheck", section.get("pkg_gpgcheck", "")).strip(), section.get("repo_gpgcheck", "").strip())
if checks != ("1", "1"):
    stop(1, "%s: both signature checks must be on (gpgcheck, repo_gpgcheck = %s)" % (name, checks))
try:
    found = fingerprint((here / pin["key"]).read_bytes())
except (OSError, ValueError, KeyError) as issue:
    stop(1, "the key of %s cannot be read: %s" % (name, issue))
if found != pin["fingerprint"].upper():
    stop(1, "the key of %s has fingerprint %s; the pin is %s" % (name, found, pin["fingerprint"]))
with open(repo_file, "w") as handle:
    handle.write(text.rstrip("\n") + "\ngpgkey = file://%s/%s\n\n" % (MOUNT, pin["key"]))
print("olivares-repo-pinned.sh: %s bound to key %s" % (name, found))
EOF
