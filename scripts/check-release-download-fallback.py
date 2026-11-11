#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Every script or workflow that fetches a github.com release asset needs a release-API fallback.

github.com's release-download front answered 504 for hours on 2026-11-08 and 2026-11-09 while the release
API served the same assets, and four download paths failed one after the other (ensure-nfpm, the managed
tool installer, the appliance cosign install, compat.sh). The fallback is the one ensure-goreleaser.sh
has: the asset URL from api.github.com/repos/<owner>/<repo>/releases/tags/<tag>, then
`Accept: application/octet-stream`.

A tracked file under scripts/ or .github/ that names `releases/download/` outside a comment must contain
both api.github.com/repos/ and application/octet-stream, or be in ALLOW with the reason it needs none.
An ALLOW entry that no longer applies fails too, so the list can only shrink. Test and fixture files
(test-*, test_*, testdata/) are not scanned: they name the URL as data. A file is judged as a whole: a
second, unguarded fetch in a file that already has a fallback is for review. Only files the export ships
are judged (the development tree asks the exporter's own --manifest; the public tree has no exporter and
holds only shipped files), so ALLOW never names a hub-only path.

Exit 0 clean, 1 findings, 2 could not look.
"""
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent
# The files that ship: the exporter's own manifest in the development tree; the public tree has no
# exporter and every tracked file there ships.
# export-closure: hub-only scripts/export-public.sh — run only inside the presence test below
SHIPPED = """
if [ -f scripts/export-public.sh ]; then
  bash scripts/export-public.sh --manifest
else
  git ls-files
fi
"""
# shortcut: judged per file, not per fetch; judge per fetch the day one file mixes a guarded and an unguarded fetch.
NEEDLE = "releases/download/"
GUARDS = ("api.github.com/repos/", "application/octet-stream")

# path -> why a fallback is wrong or out of scope here.
ALLOW = {
    ".github/actions/olivares-policy-gate/scripts/run-gate.sh":
        "customer-run action (conftest, kyverno); no workflow of ours runs it",
    "scripts/check-aur-olivares-bin.sh": "compares the PKGBUILD source string; fetches nothing",
    "scripts/check-emitted-urls.sh":
        "catalog of public URLs that it probes and grades by status; a fallback would hide the status",
    "scripts/check-release-installer.sh": "regex over installer text; fetches nothing",
    "scripts/check-release-version.sh": "version rules over text; fetches nothing",
    "scripts/install-bootstrap.sh": "end-user installer: a published contract, not a CI path",
    "scripts/install.sh": "end-user installer: a published contract, not a CI path",
    "scripts/installer-matrix-lib.sh":
        "probe of the public URL; a failed probe ends in a blind verdict, not a green run",
    "scripts/release-finalize-stable.sh":
        "postcondition check that the public URL serves the verified bytes; a fallback would defeat it",
    "scripts/render-release-index.sh": "writes the URL into the index; fetches nothing",
    "scripts/update-cosign-digests.sh":
        "maintainer-run procedure that refreshes the digest table; in no PR or release gate, a 504 means run it again",
}


def shipped_files() -> list[str]:
    done = subprocess.run(["bash", "-c", SHIPPED], cwd=ROOT, capture_output=True, text=True)
    if done.returncode != 0:
        raise OSError(f"listing the shipped files exited {done.returncode}: {done.stderr.strip()[:200]}")
    return [p for p in done.stdout.splitlines() if p.startswith(("scripts/", ".github/"))]


def scanned(path: str) -> bool:
    name = Path(path).name
    return not (name.startswith(("test-", "test_")) or "/testdata/" in path)


def fetch_lines(text: str) -> list[int]:
    return [n for n, line in enumerate(text.splitlines(), 1)
            if NEEDLE in line and not line.lstrip().startswith("#")]


def main() -> int:
    try:
        files = [p for p in shipped_files() if scanned(p)]
        unguarded = {}
        for path in files:
            text = (ROOT / path).read_text(errors="replace")
            hits = fetch_lines(text)
            if hits and not all(g in text for g in GUARDS):
                unguarded[path] = hits
    except OSError as exc:
        print(f"check-release-download-fallback: COULD NOT LOOK: {exc}", file=sys.stderr)
        return 2
    findings = [f"{path}:{hits[0]}: fetches {NEEDLE} with no release-API fallback (add the fallback of "
                f"scripts/ensure-goreleaser.sh, or name the file in ALLOW with its reason)"
                for path, hits in sorted(unguarded.items()) if path not in ALLOW]
    findings += [f"{path}: stale ALLOW entry (the file is guarded, has no such fetch, or is gone)"
                 for path in sorted(ALLOW) if path not in unguarded]
    for finding in findings:
        print(finding, file=sys.stderr)
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main())
