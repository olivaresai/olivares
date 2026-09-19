#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Build and retain the complete console output in the existing hosted pr-web job."""

import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile


ROOT = Path(__file__).resolve().parent.parent
DIST = Path("core/internal/webui/dist")
HELPER = "scripts/capture-web-generation.py"
# Conservative inventory: web tests/docs can differ after curation. This is NOT
# the bundle-source.stamp; the importer compares the actual bundle inputs.
INPUT_PATHS = ["web", ".node-version", "package.json", "pnpm-lock.yaml",
               "pnpm-workspace.yaml", ".npmrc", ".taskrc.yml", ".olivares-public-export",
               "Taskfile.yml", ".github/workflows/pr-ci.yml", HELPER,
               "scripts/web-bundle-source-digest.sh"]
REQUIRED = {"web/package.json", "web/pnpm-lock.yaml", "web/vite.config.ts",
            "web/tsconfig.json", "web/index.html", ".node-version", "package.json",
            "pnpm-lock.yaml", "Taskfile.yml", ".github/workflows/pr-ci.yml", HELPER}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def command(args):
    return subprocess.check_output(args, cwd=ROOT, text=True).strip()


def digest(data):
    return hashlib.sha256(data).hexdigest()


def directory(path):
    require(stat.S_ISDIR(path.lstat().st_mode), "non-directory or symlink ancestor")


def regular_bytes(path):
    # Reject every symlink ancestor, not only the final file. Fixed checkout root
    # is resolved above; no caller supplies a source or deletion path.
    for parent in path.relative_to(ROOT).parents:
        directory(ROOT / parent)
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode) and info.st_mode & 0o444,
            "nonregular or unreadable file")
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as stream:
        opened = os.fstat(stream.fileno())
        require((opened.st_dev, opened.st_ino) == (info.st_dev, info.st_ino),
                "file changed while opening")
        return stream.read()


def inputs():
    raw = subprocess.check_output(["git", "ls-files", "-z", "--", *INPUT_PATHS], cwd=ROOT)
    names = sorted(set(p.decode() for p in raw.split(b"\0") if p))
    require(REQUIRED <= set(names), "required build input absent from index")
    require(any(p.startswith("web/src/") for p in names), "empty web source inventory")
    # Vite reads ignored .env files too. Such local inputs cannot be omitted from
    # provenance; public CI must either track them or refuse this capture.
    optional = [ROOT / ".npmrc", ROOT / "web/.npmrc", ROOT / "pnpm-workspace.yaml",
                ROOT / ".taskrc.yml", ROOT / ".olivares-public-export"]
    optional += list(ROOT.glob(".env*")) + list((ROOT / "web").glob(".env*"))
    require(all(not (p.exists() or p.is_symlink()) or p.relative_to(ROOT).as_posix() in names
                for p in optional),
            "untracked build configuration")
    subprocess.run(["git", "diff", "--quiet", "--exit-code", "HEAD", "--", *INPUT_PATHS],
                   cwd=ROOT, check=True)
    extra = subprocess.check_output(
        ["git", "ls-files", "--others", "--exclude-standard", "-z", "--", *INPUT_PATHS], cwd=ROOT)
    require(not extra, "untracked build input")
    return [{"path": p, "sha256": digest(regular_bytes(ROOT / p))} for p in names]


def outputs():
    for parent in [Path("core"), Path("core/internal"), Path("core/internal/webui"), DIST]:
        directory(ROOT / parent)
    entries = []
    pending = [ROOT / DIST]
    while pending:
        current = pending.pop()
        directory(current)
        # Path.rglob suppresses permission failures while descending. An unread
        # directory is not empty: propagate scandir open/iteration failures.
        with os.scandir(current) as children:
            for child in children:
                path = Path(child.path)
                mode = path.lstat().st_mode
                relative = path.relative_to(ROOT / DIST).as_posix()
                require(stat.S_ISDIR(mode) or stat.S_ISREG(mode), "nonregular output")
                record = {"path": relative, "type": "directory" if stat.S_ISDIR(mode) else "file"}
                if record["type"] == "file":
                    content = regular_bytes(path)
                    record.update(size=len(content), sha256=digest(content))
                else:
                    pending.append(path)
                entries.append(record)
    return sorted(entries, key=lambda item: item["path"])


def capture():
    checkout = command(["git", "rev-parse", "HEAD"])
    require(checkout == os.environ["GITHUB_SHA"] and re.fullmatch(r"[0-9a-f]{40}", checkout),
            "unexpected checkout identity")
    pr_head = os.environ["PR_HEAD"]
    require(re.fullmatch(r"[0-9a-f]{40}", pr_head), "invalid PR identity")
    repository = os.environ["GITHUB_REPOSITORY"]
    require(re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository), "invalid repository")
    run_id, attempt = os.environ["GITHUB_RUN_ID"], os.environ["GITHUB_RUN_ATTEMPT"]
    require(run_id.isdecimal() and attempt.isdecimal(), "invalid run identity")
    runner = Path(os.environ["RUNNER_TEMP"]).resolve(strict=True)
    final = runner / "web-generation-artifact"
    require(not final.exists() and not final.is_symlink(), "artifact destination already exists")
    before = inputs()
    versions = {tool: command([tool, "--version"]) for tool in ["node", "pnpm", "task"]}
    require(all(versions.values()), "missing tool version")

    # The literal embed output is the only deletion target. Validate ancestors
    # and the old tree before clearing, so a successful partial generator cannot
    # inherit obsolete files and a symlink cannot redirect deletion.
    for parent in [Path("core"), Path("core/internal"), Path("core/internal/webui")]:
        directory(ROOT / parent)
    if (ROOT / DIST).exists() or (ROOT / DIST).is_symlink():
        outputs()
        shutil.rmtree(ROOT / DIST)
    subprocess.run(["task", "build:web"], cwd=ROOT, check=True)
    require(command(["git", "rev-parse", "HEAD"]) == checkout, "checkout changed during build")
    require(inputs() == before, "build inputs changed")
    inventory = outputs()
    require(any(p["path"] == "index.html" and p.get("size", 0) > 0 for p in inventory),
            "missing or empty generated index")
    require(any(p["path"].startswith("assets/") and p.get("size", 0) > 0 for p in inventory),
            "missing generated assets")

    # Only a completely written archive and manifest are published. Upload is
    # conditional on this command's success, not on a later freshness check.
    with tempfile.TemporaryDirectory(prefix=".web-generation-", dir=runner) as temporary:
        staging = Path(temporary) / "artifact"
        staging.mkdir()
        archive_path = staging / "dist.tar.gz"
        with tarfile.open(archive_path, "w:gz", format=tarfile.PAX_FORMAT) as archive:
            for item in inventory:
                member = tarfile.TarInfo("dist/" + item["path"])
                member.mode = 0o755 if item["type"] == "directory" else 0o644
                if item["type"] == "directory":
                    member.type = tarfile.DIRTYPE
                    archive.addfile(member)
                else:
                    content = regular_bytes(ROOT / DIST / item["path"])
                    require(digest(content) == item["sha256"], "output changed during capture")
                    member.size = len(content)
                    archive.addfile(member, io.BytesIO(content))
        require(outputs() == inventory and inputs() == before, "capture inputs or outputs changed")
        manifest = {
            "schema": "olivares.web-generation/v1",
            "qualification": "development generation only; freshness and product acceptance remain due",
            "checkout_sha": checkout, "pr_head_sha": pr_head, "repository": repository,
            "run_id": run_id, "run_attempt": attempt, "build_command": ["task", "build:web"],
            "build_exit": 0, "tool_versions": versions, "inputs": before, "outputs": inventory,
            "archive": {"path": "dist.tar.gz", "sha256": digest(archive_path.read_bytes())},
        }
        (staging / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
        (staging / "SHA256SUMS").write_text("".join(
            digest((staging / p).read_bytes()) + "  " + p + "\n"
            for p in ["dist.tar.gz", "manifest.json"]))
        staging.rename(final)
    print("capture-web-generation: complete development artifact retained")


if __name__ == "__main__":
    try:
        capture()
    except (OSError, ValueError, KeyError, subprocess.SubprocessError):
        # Build output is already in the job log. Do not serialize environment,
        # arbitrary exception text, or file contents into generation artifacts.
        print("capture-web-generation: capture failed; no new artifact published", file=sys.stderr)
        sys.exit(1)
