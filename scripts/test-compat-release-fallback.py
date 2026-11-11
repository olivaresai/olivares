#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Offline test: scripts/userspace/compat.sh fetches the baseline release assets through the release API
when github.com's release-download front answers 504.

compat.sh runs in a scratch checkout with a stub baseline resolver, a stub curl on PATH that answers 504
for every github.com URL and serves the assets through a stub release API, and a stub verifier that
stops the run at the first step after the downloads (the real checksum and signature checks are not
part of this test and are unchanged).
"""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
TAG = "0.9"
NAMES = ["olivares_0.9_linux_amd64.tar.gz", "checksums.txt", "checksums.txt.sig", "checksums.txt.pem", "release-commit.txt"]
API = f"https://api.github.com/repos/olivaresai/olivares/releases/tags/{TAG}"
ASSET_URL = "https://api.github.com/repos/olivaresai/olivares/releases/assets"
REACHED_VERIFY = 42  # the stub verifier's exit status: every download finished

# The stub curl takes the URL, the output file and the Accept header from the real command line.
STUB_CURL = """#!/usr/bin/env bash
url=; out=; accept=
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o|--output) out=$2; shift 2 ;;
    -H|--header) [[ "$2" == Accept:* ]] && accept=$2; shift 2 ;;
    http*) url=$1; shift ;;
    *) shift ;;
  esac
done
printf '%s|%s\\n' "$url" "$accept" >> "$STUB_LOG"
case "$url" in
  https://github.com/*) echo 'curl: (22) The requested URL returned error: 504' >&2; exit 22 ;;
  "$STUB_API") printf '{"assets":[%s]}' "$STUB_ASSETS" ;;
  "$STUB_ASSET_URL"/*) [[ "$accept" == "Accept: application/octet-stream" ]] || exit 22
    printf 'payload of %s\\n' "${url##*/}" > "$out" ;;
  *) echo "curl stub: unexpected $url" >&2; exit 22 ;;
esac
"""


class CompatReleaseApiFallback(unittest.TestCase):
    def test_504_on_the_direct_urls_fetches_every_asset_through_the_release_api(self):
        with tempfile.TemporaryDirectory() as tmp:
            tmp = Path(tmp)
            repo = tmp / "repo"
            (repo / "scripts/userspace").mkdir(parents=True)
            shutil.copy(ROOT / "scripts/userspace/compat.sh", repo / "scripts/userspace/compat.sh")
            (repo / "RELEASE-VERSION").write_text("0.10\n")
            (repo / "scripts/userspace/resolve_baseline.py").write_text(f"print('{{\"tag_name\": \"{TAG}\"}}')\n")
            (repo / "scripts/cosign-verified.sh").write_text(
                'ls "$(dirname "${@: -1}")" > "$STUB_ASSETS_SEEN"; exit %d\n' % REACHED_VERIFY)
            git = ["git", "-C", str(repo), "-c", "user.name=t", "-c", "user.email=t@example.invalid"]
            subprocess.run(git + ["init", "-q"], check=True)
            subprocess.run(git + ["commit", "-q", "--allow-empty", "-m", "fixture"], check=True)
            shim = tmp / "bin"
            shim.mkdir()
            (shim / "curl").write_text(STUB_CURL)
            (shim / "curl").chmod(0o755)
            out = tmp / "out"
            out.mkdir()
            candidate = tmp / "candidate"
            candidate.write_text("candidate\n")
            log = tmp / "curl.log"
            seen = tmp / "assets.seen"
            assets = ",".join(f'{{"name":"{n}","url":"{ASSET_URL}/{n}"}}' for n in NAMES)
            env = dict(
                os.environ,
                PATH=f"{shim}:{os.environ['PATH']}",
                USERSPACE_OUT=str(out),
                STUB_LOG=str(log),
                STUB_API=API,
                STUB_ASSETS=assets,
                STUB_ASSET_URL=ASSET_URL,
                STUB_ASSETS_SEEN=str(seen),
            )
            env.pop("GITHUB_REF", None)
            done = subprocess.run(["bash", str(repo / "scripts/userspace/compat.sh"), str(candidate)],
                                  env=env, text=True, capture_output=True)
            self.assertEqual(done.returncode, REACHED_VERIFY, done.stdout + done.stderr)
            self.assertEqual(sorted(seen.read_text().split()), sorted(NAMES))
            for name in NAMES:
                self.assertEqual((out / "published-assets" / name).read_text(), f"payload of {name}\n")
            calls = log.read_text().splitlines()
            self.assertIn(f"{API}|", calls)
            self.assertIn(f"{ASSET_URL}/checksums.txt|Accept: application/octet-stream", calls)


if __name__ == "__main__":
    unittest.main()
