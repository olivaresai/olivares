#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Assemble separately verified stable/security renders into one public tree."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import shutil
import sys


class ContractError(Exception):
    pass


def regular(path: Path, label: str) -> None:
    try:
        mode = path.lstat().st_mode
    except OSError as exc:
        raise ContractError(f"cannot inspect {label}: {exc}") from exc
    if not path.is_file() or path.is_symlink() or mode & 0o022:
        raise ContractError(f"{label} is missing, linked, or writable by group/others: {path}")


def clean_tree(root: Path, label: str) -> None:
    if not root.is_absolute() or not root.is_dir() or root.is_symlink():
        raise ContractError(f"{label} must be an absolute regular directory")
    for directory, dirs, files in os.walk(root, followlinks=False):
        base = Path(directory)
        for name in [*dirs, *files]:
            path = base / name
            if path.is_symlink() or (not path.is_dir() and not path.is_file()):
                raise ContractError(f"{label} contains a non-regular entry: {path.relative_to(root)}")


def exact_json(path: Path) -> dict[str, object]:
    regular(path, "repository manifest")
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        raise ContractError(f"repository manifest is unreadable: {exc}") from exc
    if not isinstance(value, dict):
        raise ContractError("repository manifest is not an object")
    return value


def identical(left: Path, right: Path, label: str) -> None:
    regular(left, label)
    regular(right, label)
    if left.read_bytes() != right.read_bytes():
        raise ContractError(f"{label} differs from the materialized production anchor")


def copy_regular(source: Path, target: Path) -> None:
    regular(source, "publish input")
    target.parent.mkdir(parents=True, exist_ok=True)
    with source.open("rb") as reader, target.open("xb") as writer:
        shutil.copyfileobj(reader, writer)
    target.chmod(0o644)


def copy_directory(source: Path, target: Path) -> None:
    if target.exists():
        raise ContractError(f"assembly destination already exists: {target}")
    target.mkdir(parents=True, mode=0o755)
    for directory, dirs, files in os.walk(source, followlinks=False):
        dirs.sort()
        files.sort()
        relative = Path(directory).relative_to(source)
        destination = target / relative
        destination.mkdir(parents=True, exist_ok=True, mode=0o755)
        for name in files:
            copy_regular(Path(directory) / name, destination / name)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--stable", required=True, type=Path)
    parser.add_argument("--security", required=True, type=Path)
    parser.add_argument("--key-dir", required=True, type=Path)
    parser.add_argument("--out", required=True, type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        for path, label in (
            (args.stable, "stable render"),
            (args.security, "security render"),
            (args.key_dir, "materialized key directory"),
        ):
            clean_tree(path, label)
        if not args.out.is_absolute() or args.out.exists() or not args.out.parent.is_dir():
            raise ContractError("--out must be a new absolute path below an existing parent")

        stable_manifest = exact_json(args.stable / "stable/repository-manifest.json")
        security_manifest = exact_json(args.security / "security/repository-manifest.json")
        if stable_manifest.get("channel") != "stable" or security_manifest.get("channel") != "security":
            raise ContractError("rendered channel identities are not stable/security")
        for field in ("schema", "schema_version", "version", "source_date_epoch", "valid_until_epoch", "keys"):
            if stable_manifest.get(field) != security_manifest.get(field):
                raise ContractError(f"stable/security renders disagree on {field}")
        if (args.stable / "security").exists() or (args.security / "stable").exists():
            raise ContractError("a rendered tree contains the other channel")

        apk_name = str(dict(stable_manifest.get("keys", {})).get("apk_public_key_name", ""))
        if not apk_name.endswith(".rsa.pub") or "/" in apk_name or "\\" in apk_name:
            raise ContractError("repository manifest carries an unsafe APK key name")
        for rendered in (args.stable, args.security):
            identical(
                rendered / "keys/olivares-package-repository.asc",
                args.key_dir / "olivares-packages.asc",
                "OpenPGP public key",
            )
            identical(rendered / f"keys/{apk_name}", args.key_dir / apk_name, "APK public key")

        args.out.mkdir(mode=0o755)
        copy_directory(args.stable / "keys", args.out / "keys")
        copy_directory(args.stable / "stable", args.out / "stable")
        copy_directory(args.security / "security", args.out / "security")
        copy_regular(args.key_dir / "olivares-packages.gpg", args.out / "olivares-packages.gpg")
        copy_regular(args.key_dir / "olivares-packages.asc", args.out / "olivares-packages.asc")
        copy_regular(args.key_dir / apk_name, args.out / apk_name)
        count = sum(1 for path in args.out.rglob("*") if path.is_file())
        if count < 40:
            raise ContractError(f"assembled tree has only {count} files")
        print(
            f"assemble-package-repository-publish-tree: OK — version {stable_manifest['version']}, "
            f"stable+security, {count} files"
        )
        return 0
    except (ContractError, OSError, ValueError) as exc:
        print(f"assemble-package-repository-publish-tree: HALLAZGO — {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
