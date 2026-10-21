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


RPM_DELIVERY_SCHEMA = "olivares.ai/rpm-delivery/v1"
RPM_ARCHES = ["aarch64", "x86_64"]
CANONICAL_ORIGIN = "https://packages.olivares.ai"
# Where a dnf host keeps the repository key (the install guide stores it there before the repo file
# exists). The repo file names this file, never an address the repository answers, so a swapped tree
# cannot hand a trusting host another key. It is the path the appliance image pins the same key at.
RPM_KEY_PATH = "/etc/pki/rpm-gpg/RPM-GPG-KEY-olivares-package-repository"
REPO_TEMPLATE = Path(__file__).resolve().parent.parent / "packaging/repositories/dnf-repo.template"


class ContractError(Exception):
    pass


def sources_file(origin: str, key_asc: str) -> str:
    """The apt deb822 file a host drops into /etc/apt/sources.list.d, key inline."""
    block = "".join(f" {line}\n" if line else " .\n" for line in key_asc.strip().splitlines())
    return f"Types: deb\nURIs: {origin}/stable/apt\nSuites: stable\nComponents: main\nSigned-By:\n{block}"


def repo_file(origin: str, template: str) -> str:
    """The dnf file a host drops into /etc/yum.repos.d, from the one repository template."""
    text = "".join(f"{line}\n" for line in template.splitlines() if not line.startswith("#"))
    values = {
        "REPO_ID": "olivares",
        "REPO_NAME": "Olivares AI",
        "BASEURL": f"{origin}/stable/rpm/$basearch",
        "GPGKEY": f"file://{RPM_KEY_PATH}",
    }
    for name, value in values.items():
        text = text.replace("{{" + name + "}}", value)
    if "{{" in text:
        raise ContractError("the repository template left a placeholder")
    return text


def user_files(tree: Path) -> dict[str, str]:
    """The two files for the canonical origin, from the key this tree publishes."""
    key_asc = (tree / "olivares-packages.asc").read_text(encoding="utf-8")
    return {
        "olivares.sources": sources_file(CANONICAL_ORIGIN, key_asc),
        "olivares.repo": repo_file(CANONICAL_ORIGIN, REPO_TEMPLATE.read_text(encoding="utf-8")),
    }


def write_user_files(tree: Path) -> None:
    for name, text in user_files(tree).items():
        with (tree / name).open("x", encoding="utf-8") as writer:
            writer.write(text)
        (tree / name).chmod(0o644)


def check_user_files(tree: Path) -> None:
    """The tree's two user files are byte for byte what write_user_files writes, and the key
    they carry is the key the repository metadata is signed with."""
    for name in ("olivares-packages.asc", "keys/olivares-package-repository.asc", "olivares.sources", "olivares.repo"):
        path = tree / name
        if not path.is_file() or path.is_symlink():
            raise ContractError(f"publish tree lacks a regular file: {name}")
    if (tree / "olivares-packages.asc").read_bytes() != (tree / "keys/olivares-package-repository.asc").read_bytes():
        raise ContractError("olivares-packages.asc is not the key of keys/olivares-package-repository.asc")
    for name, expected in user_files(tree).items():
        if (tree / name).read_text(encoding="utf-8") != expected:
            raise ContractError(f"{name} is not what the assembler writes for {CANONICAL_ORIGIN}")


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


def require_s3_rpm(rendered: Path, channel: str, fingerprint: str) -> None:
    """The published rpm trees are S3's: repomd.xml.asc and delivery.json per arch.

    The unsigned render_rpm layout (Packages/, no delivery.json) is refused, so
    it cannot become the published rpm repository.
    """
    rpm_root = rendered / channel / "rpm"
    arches = sorted(path.name for path in rpm_root.iterdir()) if rpm_root.is_dir() else []
    if arches != RPM_ARCHES:
        raise ContractError(f"{channel} rpm trees are not exactly {', '.join(RPM_ARCHES)}")
    for arch in arches:
        tree = rpm_root / arch
        if (tree / "Packages").exists():
            raise ContractError(f"{channel}/rpm/{arch} is the unsigned rpm render, not S3's signed tree")
        regular(tree / "repodata" / "repomd.xml", f"{channel}/rpm/{arch} repomd.xml")
        regular(tree / "repodata" / "repomd.xml.asc", f"{channel}/rpm/{arch} repomd.xml.asc")
        delivery = exact_json(tree / "delivery.json")
        packages = delivery.get("packages")
        if delivery.get("schema") != RPM_DELIVERY_SCHEMA or delivery.get("key_fingerprint") != fingerprint:
            raise ContractError(f"{channel}/rpm/{arch} delivery.json is not S3's for the repository key")
        if not isinstance(packages, list) or not packages:
            raise ContractError(f"{channel}/rpm/{arch} delivery.json names no package")
        for package in packages:
            regular(tree / str(dict(package).get("file", "")), f"{channel}/rpm/{arch} delivered rpm")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--stable", type=Path)
    parser.add_argument("--security", type=Path)
    parser.add_argument("--key-dir", type=Path)
    parser.add_argument("--out", type=Path)
    # The publisher checks an assembled tree before it reads or writes R2; the fixtures of its
    # tests write the two user files into a hand-made tree.
    modes = parser.add_mutually_exclusive_group()
    modes.add_argument("--check-user-files", type=Path, metavar="TREE")
    modes.add_argument("--write-user-files", type=Path, metavar="TREE")
    args = parser.parse_args()
    if not (args.check_user_files or args.write_user_files) and not (args.stable and args.security and args.key_dir and args.out):
        parser.error("assembling needs --stable, --security, --key-dir and --out")
    return args


def main() -> int:
    args = parse_args()
    try:
        if args.check_user_files:
            check_user_files(args.check_user_files)
            print("assemble-package-repository-publish-tree: user files OK")
            return 0
        if args.write_user_files:
            write_user_files(args.write_user_files)
            return 0
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

        fingerprint = str(dict(stable_manifest.get("keys", {})).get("openpgp_fingerprint", ""))
        require_s3_rpm(args.stable, "stable", fingerprint)
        require_s3_rpm(args.security, "security", fingerprint)
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
        write_user_files(args.out)
        check_user_files(args.out)
        count = sum(1 for path in args.out.rglob("*") if path.is_file())
        if count < 40:
            raise ContractError(f"assembled tree has only {count} files")
        print(
            f"assemble-package-repository-publish-tree: OK — version {stable_manifest['version']}, "
            f"stable+security, {count} files"
        )
        return 0
    except (ContractError, OSError, ValueError) as exc:
        print(f"assemble-package-repository-publish-tree: FINDING — {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
