#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Offline test: scripts/ensure-nfpm.sh answers a github.com release-download 504 through the release API.

The script is copied with only its digest pins swapped for the fixture's, so the pinned-digest check
that decides what is installed still runs. A stub curl on PATH answers 504 for the direct URL and
serves the asset through a stub release API.
"""
import hashlib
import io
import os
from pathlib import Path
import platform
import re
import subprocess
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
VERSION = "2.47.0"
ARCH = {"x86_64": "x86_64", "amd64": "x86_64", "aarch64": "arm64", "arm64": "arm64"}[platform.machine()]
ASSET = f"nfpm_{VERSION}_Linux_{ARCH}.tar.gz"
API = f"https://api.github.com/repos/goreleaser/nfpm/releases/tags/v{VERSION}"
ASSET_URL = "https://api.github.com/repos/goreleaser/nfpm/releases/assets/1"

# The stub curl takes the URL, -o target and Accept header from the real command line.
STUB_CURL = """#!/usr/bin/env bash
url=; out=; accept=
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o) out=$2; shift 2 ;;
    -H) [[ "$2" == Accept:* ]] && accept=$2; shift 2 ;;
    http*) url=$1; shift ;;
    *) shift ;;
  esac
done
printf '%s|%s\\n' "$url" "$accept" >> "$STUB_LOG"
case "$url" in
  https://github.com/*) echo 'curl: (22) The requested URL returned error: 504' >&2; exit 22 ;;
  "$STUB_API") printf '{"assets":[{"name":"%s","url":"%s"}]}' "$STUB_ASSET" "$STUB_ASSET_URL" ;;
  "$STUB_ASSET_URL") [[ "$accept" == "Accept: application/octet-stream" ]] || exit 22
    cp "$STUB_PAYLOAD" "$out" ;;
  *) echo "curl stub: unexpected $url" >&2; exit 22 ;;
esac
"""


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def tarball(binary: bytes) -> bytes:
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as tar:
        info = tarfile.TarInfo("nfpm")
        info.size, info.mode = len(binary), 0o755
        tar.addfile(info, io.BytesIO(binary))
    return buf.getvalue()


class EnsureNfpmReleaseApiFallback(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.tmp = Path(self._tmp.name)
        binary = b"#!/bin/sh\necho nfpm version fixture\n"
        self.good = tarball(binary)
        script = (ROOT / "scripts/ensure-nfpm.sh").read_text()
        script = re.sub(r"TARBALL_SHA=\w+", f"TARBALL_SHA={sha256(self.good)}", script)
        script = re.sub(r"BIN_SHA=\w+", f"BIN_SHA={sha256(binary)}", script)
        self.script = self.tmp / "ensure-nfpm.sh"
        self.script.write_text(script)
        shim = self.tmp / "bin"
        shim.mkdir()
        (shim / "curl").write_text(STUB_CURL)
        (shim / "curl").chmod(0o755)
        self.log = self.tmp / "curl.log"
        self.env = dict(
            os.environ,
            PATH=f"{shim}:{os.environ['PATH']}",
            STUB_LOG=str(self.log),
            STUB_API=API,
            STUB_ASSET=ASSET,
            STUB_ASSET_URL=ASSET_URL,
            STUB_PAYLOAD=str(self.tmp / "payload"),
        )
        self.env.pop("OLIVARES_NFPM", None)
        self.env.pop("OLIVARES_NFPM_DIR", None)

    def run_script(self, payload: bytes, dest: Path):
        (self.tmp / "payload").write_bytes(payload)
        return subprocess.run(["bash", str(self.script), "--dest", str(dest)],
                              env=self.env, text=True, capture_output=True)

    def test_504_on_the_direct_url_installs_through_the_release_api(self):
        dest = self.tmp / "good"
        done = self.run_script(self.good, dest)
        self.assertEqual(done.returncode, 0, done.stdout + done.stderr)
        self.assertEqual(done.stdout.strip(), str(dest / "nfpm"))
        calls = self.log.read_text().splitlines()
        self.assertIn(f"{API}|", calls)
        self.assertIn(f"{ASSET_URL}|Accept: application/octet-stream", calls)

    def test_release_api_bytes_still_face_the_pinned_digest(self):
        dest = self.tmp / "bad"
        done = self.run_script(tarball(b"not the pinned binary"), dest)
        self.assertEqual(done.returncode, 2, done.stdout + done.stderr)
        self.assertIn("does not match the pinned digest", done.stderr)
        self.assertFalse((dest / "nfpm").exists())


if __name__ == "__main__":
    unittest.main()
