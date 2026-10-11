#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
set -euo pipefail

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
scratch="$(mktemp -d "${OLIVARES_TEST_SCRATCH_ROOT:-$(dirname -- "$root")}/.olivares-install-probe.XXXXXX")"
trap 'rm -rf -- "$scratch"' EXIT
python3 - "$root" "$scratch" <<'PY'
import hashlib
import os
from pathlib import Path
import subprocess
import sys
import tarfile

root, scratch = map(Path, sys.argv[1:])
fixture = scratch / "release"
tools = scratch / "tools"
fixture.mkdir()
tools.mkdir()
(tools / "curl").write_text('''#!/bin/sh
set -eu
url= dest=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) dest="$2"; shift 2 ;;
    https://*) url="$1"; shift ;;
    *) shift ;;
  esac
done
cp "$PROBE_FIXTURE/${url##*/}" "$dest"
''')
# Crypto verification has its own gate. This fixture controls only candidate
# execution after the installer has checked the archive's SHA-256.
(tools / "cosign").write_text('#!/bin/sh\nexit 0\n')
for tool in tools.iterdir():
    tool.chmod(0o755)

env = dict(os.environ, PATH=f"{tools}:/usr/bin:/bin", CI="1",
           OLIVARES_OS="linux", OLIVARES_ARCH="amd64", OLIVARES_COSIGN="",
           OLIVARES_GITHUB_URL="https://fixture.invalid", PROBE_FIXTURE=str(fixture),
           TMPDIR=str(scratch))
for name, candidate, existing in (
    ("failed_candidate_preserves_installed_binary", b"#!/bin/sh\nexit 1\n", True),
    ("failed_candidate_leaves_fresh_install_empty", b"#!/bin/sh\nexit 1\n", False),
    ("successful_candidate_replaces_installed_binary",
     b"#!/bin/sh\nprintf 'Olivares AI 1.1\\n'\n", True),
):
    case = scratch / name
    bindir = case / "bin"
    bindir.mkdir(parents=True)
    installed = bindir / "olivares"
    previous = b"#!/bin/sh\nprintf 'Olivares AI 1.0\\n'\n"
    if existing:
        installed.write_bytes(previous)
        installed.chmod(0o755)
    config = case / "olivares.env"
    data = case / "store.db"
    config.write_bytes(b"# operator settings\nOLIVARES_EXTRA_ARGS=--listen=127.0.0.1:8443\n")
    data.write_bytes(b"existing user data\n")
    before = (config.read_bytes(), data.read_bytes())
    binary = fixture / "olivares"
    binary.write_bytes(candidate)
    binary.chmod(0o755)
    archive = fixture / "olivares_1.1_linux_amd64.tar.gz"
    with tarfile.open(archive, "w:gz") as handle:
        handle.add(binary, arcname="olivares")
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    (fixture / "checksums.txt").write_text(f"{digest}  {archive.name}\n")
    (fixture / "checksums.txt.sig").write_text("fixture signature\n")
    (fixture / "checksums.txt.pem").write_text("fixture certificate\n")
    result = subprocess.run(["/bin/sh", str(root / "scripts/install.sh"),
                             "--version", "1.1", "--bindir", str(bindir)],
                            env=env, capture_output=True, text=True, timeout=20)
    if "successful" in name:
        assert result.returncode == 0, result.stderr
        assert installed.read_bytes() == candidate
        assert "installed verified binary" in result.stdout
        assert "Olivares AI 1.1" in result.stdout
    else:
        assert result.returncode == 1, result.stderr
        assert "did not report its version" in result.stderr, result.stderr
        if existing:
            assert installed.read_bytes() == previous, "failed candidate replaced the working binary"
        else:
            assert not installed.exists(), "failed candidate was left at the installed path"
        assert "installed verified binary" not in result.stdout, "failure reported an installed binary"
    assert not list(bindir.glob(".olivares-install.*")), "candidate staging file leaked"
    assert (config.read_bytes(), data.read_bytes()) == before
    print(f"ok - {name}", flush=True)
PY
