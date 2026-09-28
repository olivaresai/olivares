#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Sign each rpm signature header with the package-repository OpenPGP key.

Fedora 44 ships rpm-sign 6.0.1. The signer command is the one in that
generation's manual, not a local invention:

    rpmsign --addsign --key-id KEYID PACKAGE_FILE

https://rpm.org/docs/6.0.x/man/rpmsign.1 (retrieved 2026-09-27).
``--key-id`` overrides ``%_openpgp_sign_id``. ``%_openpgp_sign`` selects
the OpenPGP implementation; ``gpg`` is the documented default.
``%_gpg_path`` is the documented GnuPG keyring location. Common options,
including ``--define``, are rpm-common(8), which rpmsign(1) points at.

rpmsign(8) names no passphrase option: it asks gpg, and gpg asks its agent. A
passphrase file (OLIVARES_PACKAGE_REPO_OPENPGP_PASSPHRASE_FILE, the production
key's) is seeded into that agent with gpg-preset-passphrase, which gpg-agent
accepts under allow-preset-passphrase (GnuPG manual, read 2026-09-27). A probe
signature with --pinentry-mode error then proves gpg can sign unattended before
rpmsign runs.

rpmsign rewrites only the signature header. RPM format v4 (rpm.org/docs/6.0.x
manual/format_v4, read 2026-09-27): a 96-byte lead, the signature header
zero-padded to a multiple of 8, then the header and the payload. Every byte
after the padded signature header must be the same before and after signing.
"""

from __future__ import annotations

import argparse
import contextlib
import json
import os
import re
import shutil
import stat
import subprocess
import sys
import tempfile
from collections.abc import Iterator
from pathlib import Path
from typing import NoReturn


RPM_LEAD_MAGIC = b"\xed\xab\xee\xdb"
RPM_LEAD_SIZE = 96
RPM_HEADER_MAGIC = b"\x8e\xad\xe8\x01"
KEY_SCHEMA = "olivares.ai/package-repository-key/v1"
KEY_PURPOSE = "apt-rpm-apk-repository-metadata"
# rpmsign(1) OPERATIONS and SIGN OPTIONS. The self-test matches this tuple.
DOCUMENTED_SIGN_ARGV = ("rpmsign", "--addsign", "--key-id")
FINGERPRINT = re.compile(r"[0-9A-F]{40}")
SIGNATURE_QUERY = (
    "%{RSAHEADER:pgpsig}\\n%{DSAHEADER:pgpsig}\\n"
    "%{SIGPGP:pgpsig}\\n%{SIGGPG:pgpsig}\\n%{OPENPGP}\\n"
)


def stderr(message: str) -> None:
    print(f"rpm-payload-sign: {message}", file=sys.stderr)


def refuse(message: str) -> NoReturn:
    stderr(f"refused — {message}")
    raise SystemExit(1)


def blind(message: str) -> NoReturn:
    stderr(f"could not check — {message}")
    raise SystemExit(2)


def require_tool(name: str) -> str:
    path = None
    for directory in os.environ.get("PATH", "").split(os.pathsep):
        if not directory:
            continue
        candidate = Path(directory) / name
        if candidate.is_file() and os.access(candidate, os.X_OK):
            path = str(candidate)
            break
    if path is None:
        blind(f"{name} is absent")
    return path


def secret_file(name: str) -> Path:
    raw = os.environ.get(name, "")
    if not raw:
        blind(f"required key file variable is absent: {name}")
    path = Path(raw)
    if not path.is_absolute():
        blind(f"{name} is not an absolute path")
    if not path.is_file() or path.is_symlink():
        blind(f"key input from {name} is missing, not regular, or is a symlink")
    mode = stat.S_IMODE(path.stat().st_mode)
    if mode & 0o077:
        refuse(f"key input from {name} is mode {mode:04o}; require no group or other access")
    return path


def load_descriptor() -> str:
    raw = os.environ.get("OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE", "")
    if not raw:
        blind("OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE is absent")
    path = Path(raw)
    if not path.is_absolute() or not path.is_file() or path.is_symlink():
        blind("repository key descriptor is missing, not absolute, not regular, or is a symlink")
    try:
        descriptor = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError):
        refuse("repository key descriptor is not readable JSON")
    required = {
        "schema",
        "purpose",
        "environment",
        "openpgp_fingerprint",
        "apk_public_key_name",
        "apk_public_key_sha256",
    }
    if not isinstance(descriptor, dict) or set(descriptor) != required:
        refuse("repository key descriptor fields are not exact")
    if descriptor["schema"] != KEY_SCHEMA or descriptor["purpose"] != KEY_PURPOSE:
        refuse("repository key descriptor has the wrong schema or purpose")
    environment = descriptor["environment"]
    if environment not in ("test", "preprod", "production"):
        refuse("repository key descriptor environment is not test, preprod, or production")
    test_only = os.environ.get("OLIVARES_PACKAGE_REPO_TEST_ONLY", "")
    if environment == "test" and test_only != "1":
        refuse("test repository key is forbidden outside an explicit test-only battery")
    if environment != "test" and test_only:
        refuse("OLIVARES_PACKAGE_REPO_TEST_ONLY cannot accompany a non-test key")
    fingerprint = str(descriptor["openpgp_fingerprint"])
    if not FINGERPRINT.fullmatch(fingerprint):
        refuse("repository OpenPGP fingerprint must be uppercase 40-hex")
    return fingerprint


def run(command: list[str], env: dict[str, str] | None = None, check: bool = False) -> subprocess.CompletedProcess[bytes]:
    return subprocess.run(
        command,
        env=env,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=check,
    )


# gpg-agent binds $GNUPGHOME/S.gpg-agent* sockets; Linux sun_path holds 107 bytes
# plus the terminating NUL. A long TMPDIR must not turn a check into a refusal.
GPG_AGENT_SOCKET = "/S.gpg-agent.browser"
SUN_PATH_MAX = 107


@contextlib.contextmanager
def gnupg_home() -> Iterator[Path]:
    """Yield a new GnuPG home path whose agent socket fits sun_path.

    The home is inside an owned 0700 temporary directory under TMPDIR, or under
    /tmp when TMPDIR is too long. Its agent is stopped and the directory removed
    afterwards. When neither fits, the check could not run: exit 2.
    """
    for base in (os.environ.get("TMPDIR") or tempfile.gettempdir(), "/tmp"):
        if not os.path.isdir(base):
            continue
        with tempfile.TemporaryDirectory(prefix="gpg-", dir=base) as temporary:
            home = Path(temporary) / "h"
            if len(os.fsencode(home)) + len(GPG_AGENT_SOCKET) > SUN_PATH_MAX:
                continue
            try:
                yield home
            finally:
                gpgconf = shutil.which("gpgconf")
                if gpgconf and home.is_dir():
                    run([gpgconf, "--homedir", str(home), "--kill", "gpg-agent"])
            return
    blind(f"no TMPDIR or /tmp path leaves room for the gpg-agent socket ({SUN_PATH_MAX} bytes)")


def import_secret(gpg: str, home: Path, secret: Path) -> dict[str, str]:
    home.mkdir(mode=0o700, exist_ok=False)
    os.chmod(home, 0o700)
    (home / "gpg.conf").write_text("batch\nno-tty\n", encoding="utf-8")
    (home / "gpg-agent.conf").write_text("allow-loopback-pinentry\nallow-preset-passphrase\n", encoding="utf-8")
    os.chmod(home / "gpg.conf", 0o600)
    os.chmod(home / "gpg-agent.conf", 0o600)
    env = os.environ.copy()
    env["GNUPGHOME"] = str(home)
    env["LC_ALL"] = "C"
    imported = run([gpg, "--homedir", str(home), "--batch", "--import", str(secret)], env)
    if imported.returncode != 0:
        blind("OpenPGP secret key import failed")
    return env


def rpm_body(data: bytes, name: str) -> bytes:
    """Return the bytes after the lead and the padded signature header.

    The 96-byte lead is checked for its magic and then not compared: rpm reads
    only the magic and type from it ("the rest of the data in the Lead is
    historical only", rpm.org format_v4), so an rpm with another lead name
    installs the same. Everything rpm verifies, the header and the payload,
    is in the returned bytes.
    """
    if len(data) < RPM_LEAD_SIZE + 16 or data[:4] != RPM_LEAD_MAGIC:
        refuse(f"{name} has no rpm lead")
    if data[RPM_LEAD_SIZE : RPM_LEAD_SIZE + 4] != RPM_HEADER_MAGIC:
        refuse(f"{name} has no rpm signature header")
    count = int.from_bytes(data[RPM_LEAD_SIZE + 8 : RPM_LEAD_SIZE + 12], "big")
    size = int.from_bytes(data[RPM_LEAD_SIZE + 12 : RPM_LEAD_SIZE + 16], "big")
    end = RPM_LEAD_SIZE + 16 + 16 * count + size
    end += (8 - end % 8) % 8
    if end > len(data):
        refuse(f"{name} signature header runs past the end of the file")
    return data[end:]


def unlock_for_rpmsign(gpg: str, home: Path, env: dict[str, str], fingerprint: str, passphrase: Path | None) -> None:
    """Seed the agent with the passphrase, then prove gpg signs unattended."""
    if passphrase is not None:
        gpgconf = require_tool("gpgconf")
        listed = run([gpgconf, "--list-dirs", "libexecdir"], env)
        preset = Path(listed.stdout.decode("utf-8", "replace").strip()) / "gpg-preset-passphrase"
        if listed.returncode != 0 or not preset.is_file() or not os.access(preset, os.X_OK):
            blind("gpg-preset-passphrase is absent from gpgconf's libexecdir")
        keys = run([gpg, "--homedir", str(home), "--batch", "--with-colons", "--with-keygrip", "--list-secret-keys", fingerprint], env)
        grips = [
            line.split(":")[9]
            for line in keys.stdout.decode("utf-8", "replace").splitlines()
            if line.startswith("grp:") and len(line.split(":")) > 9
        ]
        if keys.returncode != 0 or not grips:
            blind("the imported secret key has no keygrip to unlock")
        # gpg --passphrase-file reads the first line; the agent gets the same text.
        secret = passphrase.read_bytes().split(b"\n", 1)[0]
        for grip in grips:
            seeded = subprocess.run(
                [str(preset), "--preset", grip],
                env=env,
                input=secret + b"\n",
                stdout=subprocess.DEVNULL,
                stderr=subprocess.PIPE,
                check=False,
            )
            if seeded.returncode != 0:
                blind("gpg-preset-passphrase could not seed the agent")
    probe = home / "unlock-probe"
    probe.write_bytes(b"rpm-payload-sign unlock probe\n")
    signed = run(
        [
            gpg, "--homedir", str(home), "--batch", "--yes", "--pinentry-mode", "error",
            "--local-user", f"{fingerprint}!", "--detach-sign", "--output", str(probe) + ".sig", str(probe),
        ],
        env,
    )
    if signed.returncode != 0:
        blind("gpg cannot sign unattended with the repository key; rpmsign would stop at a passphrase prompt")


def signature_text(rpm_bin: str, rpm_path: Path) -> str:
    result = run([rpm_bin, "-qp", "--qf", SIGNATURE_QUERY, str(rpm_path)])
    if result.returncode != 0:
        blind(f"rpm could not read the signature header of {rpm_path.name}")
    return result.stdout.decode("utf-8", "replace")


def header_has_signature(text: str) -> bool:
    for line in text.splitlines():
        token = line.strip()
        if token and token != "(none)":
            return True
    return False


def sign_command(rpmsign: str, fingerprint: str, home: Path, rpm_path: Path) -> list[str]:
    """Return the rpmsign argv. rpmsign is the resolved path from require_tool."""
    arguments = [
        "--addsign",
        "--key-id",
        fingerprint,
        "--define",
        "_openpgp_sign gpg",
        "--define",
        f"_openpgp_sign_id {fingerprint}",
        "--define",
        f"_gpg_path {home}",
        str(rpm_path),
    ]
    # The program is compared by its file name and the arguments on their own,
    # so a resolved /usr/bin/rpmsign matches the documented bare name.
    if Path(rpmsign).name != DOCUMENTED_SIGN_ARGV[0]:
        blind(f"signer program is not {DOCUMENTED_SIGN_ARGV[0]}: {rpmsign}")
    if tuple(arguments[: len(DOCUMENTED_SIGN_ARGV) - 1]) != DOCUMENTED_SIGN_ARGV[1:]:
        blind("signer argv drifted from the documented rpmsign --addsign --key-id prefix")
    return [rpmsign, *arguments]


def sign_one(
    rpmsign: str,
    rpm_bin: str,
    rpmkeys: str,
    gpg: str,
    home: Path,
    env: dict[str, str],
    fingerprint: str,
    rpm_path: Path,
    public_key: Path,
) -> None:
    original = rpm_body(rpm_path.read_bytes(), rpm_path.name)
    signed = run(sign_command(rpmsign, fingerprint, home, rpm_path), env)
    if signed.returncode != 0:
        refuse(f"rpmsign --addsign failed for {rpm_path.name}")
    if rpm_body(rpm_path.read_bytes(), rpm_path.name) != original:
        refuse(f"rpmsign changed {rpm_path.name} outside its signature header")
    if not header_has_signature(signature_text(rpm_bin, rpm_path)):
        refuse(f"{rpm_path.name} signature header has no OpenPGP signature after rpmsign --addsign")
    db = home.parent / f"rpmdb-{rpm_path.name}"
    db.mkdir(mode=0o700)
    imported = run([rpmkeys, "--dbpath", str(db), "--import", str(public_key)])
    if imported.returncode != 0:
        blind(f"rpmkeys --import failed while checking {rpm_path.name}")
    checked = run([rpmkeys, "--dbpath", str(db), "-Kv", str(rpm_path)])
    check_text = checked.stdout.decode("utf-8", "replace") + checked.stderr.decode("utf-8", "replace")
    if checked.returncode != 0 or not signature_verifies(check_text):
        refuse(f"rpmkeys --checksig did not accept {rpm_path.name}")


def signature_verifies(text: str) -> bool:
    lowered = text.lower()
    if "no signature" in lowered or "nokey" in lowered or "not ok" in lowered or ": bad" in lowered:
        return False
    return "signature" in lowered and ": ok" in lowered


def export_public(gpg: str, home: Path, env: dict[str, str], fingerprint: str, destination: Path) -> None:
    exported = run(
        [gpg, "--homedir", str(home), "--batch", "--armor", "--export", fingerprint],
        env,
    )
    if exported.returncode != 0 or b"BEGIN PGP PUBLIC KEY BLOCK" not in exported.stdout:
        blind("OpenPGP public key export failed")
    destination.write_bytes(exported.stdout)
    os.chmod(destination, 0o644)


def rpm_inputs(raws: list[str]) -> list[Path]:
    paths: list[Path] = []
    for raw in raws:
        path = Path(raw)
        if not path.is_absolute() or not path.is_file() or path.is_symlink():
            blind(f"rpm is not an absolute regular file: {raw}")
        if path.suffix != ".rpm":
            refuse(f"not an rpm path: {path.name}")
        paths.append(path)
    return paths


def verify_with(public_key: Path, raws: list[str]) -> int:
    """Check each rpm's header signature with the pinned public key alone.

    The signer's own check uses a key exported from the secret it signed with;
    this one imports only the reviewed repository key into a fresh rpmdb, so a
    missing, foreign or broken header signature is refused (rpmkeys -Kv).
    """
    rpmkeys = require_tool("rpmkeys")
    if not public_key.is_absolute() or not public_key.is_file() or public_key.is_symlink():
        blind("--verify-with is not an absolute regular file")
    paths = rpm_inputs(raws)
    scratch = Path(os.environ.get("TMPDIR") or tempfile.gettempdir())
    if not scratch.is_dir():
        blind("TMPDIR is not an existing directory")
    for rpm_path in paths:
        with tempfile.TemporaryDirectory(prefix="rpm-payload-verify-", dir=scratch) as temporary:
            db = Path(temporary) / "pinned-rpmdb"
            db.mkdir(mode=0o700)
            imported = run([rpmkeys, "--dbpath", str(db), "--import", str(public_key)])
            if imported.returncode != 0:
                blind(f"rpmkeys --import of the pinned key failed while checking {rpm_path.name}")
            checked = run([rpmkeys, "--dbpath", str(db), "-Kv", str(rpm_path)])
        text = checked.stdout.decode("utf-8", "replace") + checked.stderr.decode("utf-8", "replace")
        if checked.returncode != 0 or not signature_verifies(text):
            refuse(f"{rpm_path.name} has no header signature that verifies with the pinned repository key")
    print(f"rpm-payload-sign: {len(paths)} rpm header signature(s) verify with the pinned repository key")
    return 0


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description="Sign rpm signature headers with the package-repository key.")
    parser.add_argument("--rpm", action="append", required=True, help="Absolute path of an rpm to sign in place.")
    parser.add_argument(
        "--verify-with",
        default="",
        help="Do not sign: require each rpm's header signature to verify with only this public key.",
    )
    args = parser.parse_args(argv)
    if args.verify_with:
        return verify_with(Path(args.verify_with), args.rpm)
    rpmsign = require_tool("rpmsign")
    rpm_bin = require_tool("rpm")
    rpmkeys = require_tool("rpmkeys")
    gpg = require_tool("gpg")
    fingerprint = load_descriptor()
    secret = secret_file("OLIVARES_PACKAGE_REPO_OPENPGP_SECRET_KEY_FILE")
    passphrase = None
    if os.environ.get("OLIVARES_PACKAGE_REPO_OPENPGP_PASSPHRASE_FILE"):
        passphrase = secret_file("OLIVARES_PACKAGE_REPO_OPENPGP_PASSPHRASE_FILE")
    rpm_paths = rpm_inputs(args.rpm)
    scratch_root = os.environ.get("TMPDIR") or tempfile.gettempdir()
    scratch = Path(scratch_root)
    if not scratch.is_dir():
        blind("TMPDIR is not an existing directory")
    with tempfile.TemporaryDirectory(prefix="rpm-payload-sign-", dir=scratch) as temporary, gnupg_home() as home:
        env = import_secret(gpg, home, secret)
        unlock_for_rpmsign(gpg, home, env, fingerprint, passphrase)
        public_key = Path(temporary) / "public.asc"
        export_public(gpg, home, env, fingerprint, public_key)
        for rpm_path in rpm_paths:
            sign_one(rpmsign, rpm_bin, rpmkeys, gpg, home, env, fingerprint, rpm_path, public_key)
    print(f"rpm-payload-sign: signed {len(rpm_paths)} rpm signature header(s)")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main(sys.argv[1:]))
    except BrokenPipeError:
        raise SystemExit(1)
