#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
set -euo pipefail

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
scratch="$(mktemp -d "${OLIVARES_TEST_SCRATCH_ROOT:-$(dirname -- "$root")}/.olivares-config-redaction.XXXXXX")"
trap 'rm -rf -- "$scratch"' EXIT
python3 - "$root" "$scratch" <<'PY'
import os
from pathlib import Path
import subprocess
import sys

root, scratch = map(Path, sys.argv[1:])
# Synthetic marker; no real account or secret is read by this test.
private_line = "synthetic-private-value-without-assignment"
for adapter, goos, relative in (
    ("systemd", "linux", "etc/olivares/olivares.env"),
    ("openrc", "linux", "etc/olivares/olivares.env"),
    ("launchd", "darwin", "Library/Preferences/dev.olivares.olivares.env"),
):
    stage = scratch / adapter
    binary = stage / "usr/local/bin/olivares"
    binary.parent.mkdir(parents=True)
    binary.write_text("#!/bin/sh\nexit 0\n")
    binary.chmod(0o755)
    config = stage / relative
    config.parent.mkdir(parents=True)
    config.write_text("# operator settings\nOLIVARES_EXTRA_ARGS=\n" + private_line + "\n")
    config.chmod(0o640)
    env = dict(os.environ, OLIVARES_OS=goos, OLIVARES_ASSET_ROOT=str(root),
               TMPDIR=str(scratch))
    result = subprocess.run(["/bin/sh", str(root / "scripts/install-service.sh"),
                             "--system", "--init", adapter, "--root", str(stage),
                             "--binary", "/usr/local/bin/olivares"],
                            env=env, text=True, capture_output=True, timeout=20)
    assert result.returncode == 1, result.stderr
    assert "invalid config line" in result.stderr, result.stderr
    assert "line 3" in result.stderr, result.stderr
    assert private_line not in result.stdout + result.stderr, "malformed configuration value leaked"
    assert config.read_text().endswith(private_line + "\n"), "operator configuration changed"
    assert not list(stage.rglob("install-manifest.json")), "failed install reported ownership"
    print(f"ok - malformed_config_redacted_{adapter}", flush=True)
    for label, line, diagnostic in (
        ("unsafe_key", "OLIVARES_" + private_line + "=fixture-value", "invalid config key"),
        ("foreign_key", private_line + "=fixture-value", "invalid config key"),
        ("unknown_key", "OLIVARES_NOT_A_REAL_KEY=" + private_line,
         "unrecognized key: OLIVARES_NOT_A_REAL_KEY"),
    ):
        binary.write_text("#!/bin/sh\n[ -z \"${OLIVARES_NOT_A_REAL_KEY+x}\" ]\n")
        binary.chmod(0o755)
        before = "# operator settings\nOLIVARES_EXTRA_ARGS=\n" + line
        config.write_text(before)  # Also exercise the last line without a newline.
        result = subprocess.run(["/bin/sh", str(root / "scripts/install-service.sh"),
                                 "--system", "--init", adapter, "--root", str(stage),
                                 "--binary", "/usr/local/bin/olivares"],
                                env=env, text=True, capture_output=True, timeout=20)
        assert result.returncode == 1, result.stderr
        assert diagnostic in result.stderr and "line 3" in result.stderr, result.stderr
        assert private_line not in result.stdout + result.stderr, "configuration value leaked"
        assert config.read_text() == before, "operator configuration changed"
        assert not list(stage.rglob("install-manifest.json")), "failed install reported ownership"
        print(f"ok - {label}_redacted_{adapter}", flush=True)
PY
