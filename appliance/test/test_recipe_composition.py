#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Inert controls at the real recipe shell and same-job owner Interfaces."""
import configparser
import contextlib
import fnmatch
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import shutil
import signal
import struct
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
KIWI = ROOT / "appliance/images/kiwi"
LOCK = ROOT / "appliance/images/toolchain/input-lock.json"
IMAGE = "sha256:" + "a" * 64
ATTEMPT = "b" * 32
ENV = {"GITHUB_RUN_ID": "100", "GITHUB_RUN_ATTEMPT": "2", "GITHUB_JOB": "image"}
sys.path.insert(0, str(ROOT / "appliance/test/runner"))
import admit  # noqa: E402  (the prerequisite's release owner, driven in-process below)
from owned_resources import LABEL, Result  # noqa: E402

# An inert Docker client. With FAKE_IMAGE_BIN it also runs each container command against an
# emulated admitted image: only the lock's qualified programs are on PATH, the image's lock is
# the repository lock, and each --volume maps its container path onto the host path.
FAKE_DOCKER = r'''
import json, os, re, subprocess, sys, time
args = sys.argv[1:]
with open(os.environ["FAKE_EFFECTS"], "a") as log:
    log.write(json.dumps(args) + "\n")
if args[0] == "build":
    sys.exit(77)
if "--cidfile" in args:
    with open(args[args.index("--cidfile") + 1], "w") as cid:
        cid.write("c" * 64)
if args[0] == "run" and os.environ.get("FAKE_DOCKER_SLEEP"):
    time.sleep(float(os.environ["FAKE_DOCKER_SLEEP"]))
code = int(os.environ.get("FAKE_DOCKER_EXIT", "0"))
if code or args[0] != "run" or not os.environ.get("FAKE_IMAGE_BIN"):
    sys.exit(code)
index, mounts = 1, {"/toolchain/input-lock.json": os.environ["FAKE_IMAGE_LOCK"]}
while args[index].startswith("--"):
    if args[index] == "--volume":
        host, target = args[index + 1].split(":")
        mounts[target] = host
    index += 1 if args[index] == "--privileged" else 2
paths = re.compile("|".join(re.escape(t) for t in sorted(mounts, key=len, reverse=True)) + r"(?=/|$|[\s\"'])")
command = [paths.sub(lambda m: mounts[m.group(0)], part) for part in args[index + 1:]]
try:
    sys.exit(subprocess.run(command, env={"PATH": os.environ["FAKE_IMAGE_BIN"]}).returncode)
except FileNotFoundError:
    print("fake image: program not qualified by the lock: " + command[0], file=sys.stderr)
    sys.exit(127)
'''

# An emulated dpkg-scanpackages for the admitted image: it writes one index stanza per .deb in the
# current directory from the package's own control file, and skips a package it cannot read with a
# warning and exit 0, as the real one does when dpkg-deb cannot read it.
FAKE_SCANPACKAGES = r'''
import io, os, sys, tarfile
def control(path):
    data = open(path, "rb").read()
    if data[:8] != b"!<arch>\n":
        raise ValueError("not an ar archive")
    pos = 8
    while pos + 60 <= len(data):
        name, size = data[pos:pos + 16].decode().strip().rstrip("/"), int(data[pos + 48:pos + 58])
        body, pos = data[pos + 60:pos + 60 + size], pos + 60 + size + size % 2
        if name == "control.tar.gz":
            with tarfile.open(fileobj=io.BytesIO(body), mode="r:gz") as tar:
                return tar.extractfile("./control").read().decode()
    raise ValueError("no control.tar.gz member")
for name in sorted(os.listdir(".")):
    if name.endswith(".deb") and "--skip-all" not in sys.argv:
        try:
            text = control(name)
        except Exception as issue:
            print("dpkg-scanpackages: warning: %s: %s, skipping package" % (name, issue), file=sys.stderr)
            continue
        sys.stdout.write(text.rstrip("\n") + "\nFilename: ./" + name + "\n\n")
'''

# The KIWI dracut modules an admitted builder carries at the lock's dracut_modules_dir, as a small
# tree: the three the installer medium needs and one it does not.
MODULE_FILES = {
    "55kiwi-dump/module-setup.sh": 0o755, "55kiwi-dump/kiwi-dump-image.sh": 0o755,
    "59kiwi-dump-reboot/module-setup.sh": 0o755, "59kiwi-lib/module-setup.sh": 0o755,
    "59kiwi-lib/kiwi-lib.sh": 0o644, "55kiwi-repart/module-setup.sh": 0o755,
}


def modules_fixture(directory):
    for rel, mode in MODULE_FILES.items():
        (directory / rel).parent.mkdir(parents=True, exist_ok=True)
        (directory / rel).write_text("# " + rel + "\n")
        (directory / rel).chmod(mode)
    return directory


def read_deb(path):
    """The members, control fields and data entries of a .deb, read without the packer's code."""
    import tarfile
    data = Path(path).read_bytes()
    assert data[:8] == b"!<arch>\n", "not an ar archive"
    members, pos = [], 8
    while pos < len(data):
        header = data[pos:pos + 60]
        assert header[58:60] == b"`\n", "bad ar member header"
        size = int(header[48:58])
        members.append((header[:16].decode().strip(), header, data[pos + 60:pos + 60 + size]))
        pos += 60 + size + size % 2
    tars = {}
    for name, _, body in members[1:]:
        with tarfile.open(fileobj=io.BytesIO(body), mode="r:gz") as tar:
            tars[name] = {m.name: (m, tar.extractfile(m).read() if m.isfile() else None) for m in tar.getmembers()}
    fields = dict(line.split(": ", 1) for line in tars["control.tar.gz"]["./control"][1].decode().splitlines()
                  if ": " in line and not line.startswith(" "))
    return members, fields, tars


# Every kind of file an attempt directory holds, by writer: attempt.py (phases, disk, records,
# recoveries), the prerequisite (workflow.py, collector, admit.py), build.sh (recipe-work),
# boot-battery.sh (boot/<case>) and nocloud-probe.sh (nocloud/). Paths are attempt-relative.
EVIDENCE_FILES = (
    "recipe-result.json", "disk-samples.jsonl", "build.log", "build.log.json", "cleanup.json",
    "release.log", "release.log.json", "recovery/001/cleanup.json", "recovery/001/receipt.json",
    "recovery/001/builder-release.json", "recovery/001/release.log", "recovery/001/release.log.json",
    "prerequisite/receipt.json", "prerequisite/workflow-result.json", "prerequisite/preflight.log",
    "prerequisite/admission.json", "prerequisite/builder-release.json", "prerequisite/SHA256SUMS",
    "prerequisite/work/builder.iid", "prerequisite/work/loop-probe.img",
    "recipe-work/stage.cid", "recipe-work/build.cid", "recipe-work/description/config.xml",
    "recipe-work/dracut_package.py", "recipe-work/packages/dracut-kiwi-oem-dump_11.0.4_all.deb",
    "recipe-work/packages/olivares_0.0.0-dev_amd64.deb",
    "recipe-work/packages/Packages", "recipe-work/packages/Packages.gz",
    "boot/bios/serial.log", "boot/bios/qemu.cmd", "boot/bios/qemu.stdout", "boot/bios/qemu.stderr",
    "boot/bios/seconds", "boot/uefi/vars.fd", "boot/iso-install/installed.qcow2",
    "boot/iso-install/qemu-img.txt", "boot/counterfeit.Ab12Cd",
    "nocloud/seed.iso", "nocloud/seed.iso.log", "nocloud/seed/user-data", "nocloud/guest/console.log",
    "nocloud/guest/probe.log", "nocloud/guest/vars.fd", "nocloud/guest/qemu.cmd", "nocloud/guest/qemu.stdout",
    "nocloud/guest/qemu.stderr", "nocloud/guest/seconds", "nocloud/state.json", "nocloud/ready",
    "nocloud/product.active", "nocloud/journal.txt",
)


def admission():
    return {"state": "admitted", "image_id": IMAGE, "accelerator": "tcg",
            "expected_context": {"attempt_id": ATTEMPT, "image_id": IMAGE,
                "accelerator": "tcg", "run_id": "100", "run_attempt": "2", "job": "image"}}


def receipt():
    return {"attempt": {"id": ATTEMPT}, "binding": {"run_id": "100", "run_attempt": "2", "job": "image"},
            "observations": {"container_toolchain": {"image_id": IMAGE, "image_retained": True,
                                                     "image_removed": False, "cleaned": True}},
            "judgement": {"state": "eligible"}}


def load_attempt(name):
    spec = importlib.util.spec_from_file_location(name, KIWI / "attempt.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def workflow():
    import yaml
    return yaml.safe_load((ROOT / ".github/workflows/appliance-image.yml").read_text())


def evidence_globs():
    """The attempt-directory patterns of the workflow's evidence upload, one path per line."""
    upload = next(s for s in workflow()["jobs"]["image"]["steps"]
                  if "appliance-image-evidence" in s.get("with", {}).get("name", ""))
    root = "${{ runner.temp }}/appliance-image-${{ github.run_id }}-${{ github.run_attempt }}/"
    paths = [line.strip() for line in upload["with"]["path"].splitlines() if line.strip()]
    return [path[len(root):] for path in paths if path.startswith(root)]


class Daemon:
    """An inert Docker daemon behind the owned-resources process adapter; it records every call."""

    def __init__(self, containers=None, images=None):
        self.containers = dict(containers or {})  # ID -> {"label": attempt or None, "running": bool}
        self.images = dict(images or {})  # ID -> attempt label
        self.calls = []
        self.fail = set()  # verbs that fail, such as ("image", "rm")
        self.once = []  # verbs that fail on their next call only
        self.report = Result(0, "")  # what the probe container prints

    def __call__(self, argv, timeout=10, metadata=None):
        argv = [str(part) for part in argv]
        self.calls.append(argv)
        kind, verb = argv[1], argv[2]
        if (kind, verb) in self.fail or (kind, verb) in self.once:
            if (kind, verb) in self.once:
                self.once.remove((kind, verb))
            return Result(1, "inert daemon refused " + kind + " " + verb)
        if kind in ("build", "create"):
            label = argv[argv.index("--label") + 1].partition("=")[2]
            if kind == "build":
                Path(argv[argv.index("--iidfile") + 1]).write_text(IMAGE + "\n")
                self.images[IMAGE] = label
                return Result(0, "built")
            self.containers["4" * 64] = {"label": label, "running": False}
            return Result(0, "4" * 64 + "\n")
        if kind == "start":
            return self.report
        table = self.containers if kind == "container" else self.images
        if verb == "ls":
            wanted = argv[argv.index("--filter") + 1][len("label="):].partition("=")[2] if "--filter" in argv else None
            rows = [i for i, v in table.items() if wanted is None or (v["label"] if kind == "container" else v) == wanted]
            return Result(0, "\n".join(sorted(rows)))
        name = argv[3] if verb != "stop" else argv[-1]
        if name not in table:
            return Result(1, "Error: No such " + kind)
        if verb == "inspect":
            label = table[name]["label"] if kind == "container" else table[name]
            row = {"Id": name, "Architecture": "amd64", "Config": {"Labels": {LABEL: label} if label else {}}}
            if kind == "container":
                row["State"] = {"Running": table[name]["running"]}
            return Result(0, json.dumps([row]))
        if verb == "stop":
            table[name]["running"] = False
            return Result(0, name)
        if verb == "rm":
            del table[name]
            return Result(0, name)
        raise AssertionError("unexpected docker call: " + " ".join(argv))

    def removed(self):
        return [argv[3] for argv in self.calls if argv[2] == "rm"]


def snapshot(directory):
    return {p.relative_to(directory).as_posix(): p.read_bytes() for p in directory.rglob("*") if p.is_file()}


def rewritten(before, directory):
    return sorted(n for n, data in before.items() if not (directory / n).is_file() or (directory / n).read_bytes() != data)


def unverified(evidence):
    """Rows of any SHA256SUMS under the attempt that no longer match; paths are attempt-relative."""
    bad = []
    for manifest in sorted(evidence.rglob("SHA256SUMS")):
        for row in manifest.read_text().splitlines():
            digest, name = row.split("  ", 1)
            path = evidence / name
            if not path.is_file() or hashlib.sha256(path.read_bytes()).hexdigest() != digest:
                bad.append(manifest.relative_to(evidence).as_posix() + ":" + name)
    return bad


class RecipeComposition(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="a2-consumer-")
        self.addCleanup(self.temp.cleanup)
        self.work = Path(self.temp.name)
        self.bin = self.work / "bin"
        self.bin.mkdir()
        self.debs = self.work / "debs"
        self.debs.mkdir()
        for name in ("olivares", "olivares-appliance-base"):
            (self.debs / (name + "_test.deb")).write_text("inert package")
        self.log = self.work / "effects.jsonl"
        self.answer = self.work / "answer.json"
        self.answer.write_text(json.dumps(admission()))
        self.receipt = self.work / "receipt.json"
        self.receipt.write_text(json.dumps(receipt()))
        # Only the admission process is replaced. All other Python runs are real and pure.
        (self.bin / "python3").write_text("#!/bin/sh\ncase \"$1\" in */runner/admit.py) cat \"$FAKE_ADMISSION\"; exit \"${FAKE_ADMISSION_EXIT:-0}\";; esac\nexec " + sys.executable + " \"$@\"\n")
        (self.bin / "docker").write_text("#!" + sys.executable + "\n" + FAKE_DOCKER)
        for executable in self.bin.iterdir():
            executable.chmod(0o755)
        self.env = dict(os.environ, **ENV, PATH=str(self.bin) + os.pathsep + os.environ['PATH'],
                        FAKE_ADMISSION=str(self.answer), FAKE_EFFECTS=str(self.log),
                        WORK_DIR=str(self.work / 'stage'))

    def build(self, extra=(), **env):
        return subprocess.run(['bash', str(KIWI / 'build.sh'), '--base', 'debian13', '--deb-dir', str(self.debs),
            '--target-dir', str(self.work / 'output'), '--receipt', str(self.receipt),
            '--attempt', ATTEMPT, '--image-id', IMAGE, '--accelerator', 'tcg', *extra],
            env=dict(self.env, **env), text=True, capture_output=True, timeout=8)

    def effects(self):
        return [json.loads(s) for s in self.log.read_text().splitlines()] if self.log.exists() else []

    def qualified_image(self, programs, failing=None, modules=None, scan_skips=False):
        """The admitted image as its lock qualifies it; every other program is absent.

        A program named in FAILING exits with the given status, as a real tool would. Every other
        program records its call; dpkg-scanpackages also writes the emulated index. With MODULES,
        mkdir and cp also run for real, and cp reads the lock's dracut_modules_dir from MODULES.
        With SCAN_SKIPS the emulated index skips every package, as for a package it cannot read."""
        image = self.work / "image-bin"
        image.mkdir()
        calls = self.work / "image-calls.jsonl"
        real = {"bash": shutil.which("bash"), "python3": sys.executable}
        scan = self.work / "fake-scanpackages.py"
        scan.write_text(FAKE_SCANPACKAGES)
        declared = json.loads(LOCK.read_text())["toolchain"]["dracut_modules_dir"]
        after = {"dpkg-scanpackages": "exec %s %s%s\n" % (sys.executable, scan, " --skip-all" if scan_skips else "")}
        if modules:
            after["mkdir"] = 'exec %s "$@"\n' % shutil.which("mkdir")
            after["cp"] = 'src=$2; case $src in %s/*) src=%s/${src#%s/};; esac; exec %s "$1" "$src" "$3"\n' % (
                declared, modules, declared, shutil.which("cp"))
        for program in programs:
            if program in (failing or {}):
                body = "exit %d\n" % failing[program]
            elif program in real:
                body = "exec " + real[program] + ' "$@"\n'
            else:
                body = sys.executable + " -c 'import json,sys; open(sys.argv[1],\"a\").write(json.dumps(sys.argv[2:])+\"\\n\")' " \
                       + str(calls) + " " + program + ' "$@"\n' + after.get(program, "")
            (image / program).write_text("#!/bin/sh\n" + body)
            (image / program).chmod(0o755)
        return image, calls

    def test_builder_qualifies_every_program_and_file_the_recipe_uses_in_it(self):
        toolchain = json.loads(LOCK.read_text())["toolchain"]
        image, calls = self.qualified_image(toolchain["required_programs"],
                                            modules=modules_fixture(self.work / "builder-modules"))
        result = self.build(FAKE_IMAGE_BIN=str(image), FAKE_IMAGE_LOCK=str(LOCK))
        self.assertEqual(result.returncode, 0, result.stderr[-2000:])
        entrypoints = {c[c.index(IMAGE) + 1] for c in self.effects() if c[0] == "run"}
        self.assertLessEqual(entrypoints, set(toolchain["required_programs"]))
        used = [json.loads(line) for line in calls.read_text().splitlines()]
        self.assertIn("dpkg-scanpackages", [call[0] for call in used])
        self.assertIn("kiwi-ng", [call[0] for call in used])
        # The dracut modules come only from the owner-declared path its qualification covers.
        modules = toolchain["dracut_modules_dir"]
        self.assertEqual([call[2] for call in used if call[0] == "cp"], [modules + "/."])
        self.assertTrue(any(name.startswith(modules + "/") for name in toolchain["required_files"]))

    def test_description_lists_the_dracut_package_kiwi_requires_for_the_installer_medium(self):
        # KIWI 11.0.4 refuses an oem type with installiso before any image work unless the image
        # package list names one of these (kiwi/runtime_checker_metadata.yml, dracut_oem_dump):
        # check_dracut_module_for_oem_install_in_package_list, which stopped hosted run 36230913656.
        import xml.etree.ElementTree as ET
        image = ET.parse(KIWI / "config.xml").getroot()
        server = [s for s in image.iter("packages") if s.get("type") == "image"
                  and "debian13-server-amd64" in (s.get("profiles") or "debian13-server-amd64").split(",")]
        names = [p.get("name") for s in server for p in s.iter("package")]
        self.assertIn("dracut-kiwi-oem-dump", names)
        self.assertEqual(set(names) & {"dracut-kiwi-oem-dump", "kiwi-dracut-oem-dump"}, {"dracut-kiwi-oem-dump"})
        # One way in for the modules: the package, never also an archive over the same files.
        self.assertEqual([a.get("name") for a in image.iter("archive")], [])

    def test_description_installs_what_the_installer_initrd_modules_need(self):
        # KIWI builds the installer initrd with dracut --add "kiwi-dump kiwi-dump-reboot", and dracut 106 exits 1 when
        # an added module, or a module it depends on, cannot be installed. Each package below was measured necessary
        # against Debian 13 and dracut 106-6 (r6 dracut-closure-check.py): kiwi-dump depends on network, whose
        # module ships in dracut-network; the network backend the image can run is systemd-networkd, which needs ip
        # (iproute2), else dracut falls back to network-legacy, which also needs dhclient; kiwi-lib depends on crypt,
        # which needs cryptsetup (cryptsetup-bin). kiwi-dump-reboot boots the installed system with kexec
        # (kexec-tools); without it the installer ends in "Failed to kexec boot system" and a forced reboot.
        import xml.etree.ElementTree as ET
        image = ET.parse(KIWI / "config.xml").getroot()
        server = [s for s in image.iter("packages") if s.get("type") == "image"
                  and "debian13-server-amd64" in (s.get("profiles") or "debian13-server-amd64").split(",")]
        names = [p.get("name") for s in server for p in s.iter("package")]
        for package in ("dracut-network", "iproute2", "cryptsetup-bin", "kexec-tools"):
            with self.subTest(package=package):
                self.assertIn(package, names)

    def test_only_the_local_repository_is_trusted_without_a_signature(self):
        # The local repository build.sh stages has a Packages index and no Release file, and apt refuses such a
        # repository unless it is trusted: "The repository 'file:/packages ./ Release' does not have a Release
        # file." KIWI 11.0.4 writes `trusted: yes` into a repository's apt sources only when its
        # repository_gpgcheck is false (kiwi/repository/apt.py, add_repo). That trust is scoped to the one
        # repository whose packages this build staged itself; every Debian repository keeps its signature check.
        import xml.etree.ElementTree as ET
        image = ET.parse(KIWI / "config.xml").getroot()
        checks = {r.get("alias"): (r.get("type"), r.get("repository_gpgcheck")) for r in image.iter("repository")
                  if in_profile(r, DEBIAN)}
        self.assertEqual({alias: check for alias, (kind, check) in checks.items() if kind == "deb-dir"},
                         {"olivares-appliance-packages": "false"})
        self.assertEqual({alias: check for alias, (kind, check) in checks.items() if kind != "deb-dir"},
                         {"debian-trixie": "true", "debian-trixie-updates": "true", "debian-trixie-security": "true"})

    def test_the_description_sets_no_console_keytable(self):
        # KIWI 11.0.4 applies <keytable> with `systemd-firstboot --keymap`, which accepts a keymap only as a *.map or
        # *.map.gz file under /usr/share/keymaps, /usr/share/kbd/keymaps or /usr/lib/kbd/keymaps (systemd 257,
        # kbd-util.c). No Debian 13 package ships one named us (console-data ships us.kmap.gz), so the build stopped
        # there: "Keymap us is not installed" (hosted run 36246387195). Debian builds systemd with -Dvconsole=false,
        # so the file firstboot would write is not applied at boot either; with no keymap loaded the console keeps
        # the kernel's built-in US map.
        import xml.etree.ElementTree as ET
        image = ET.parse(KIWI / "config.xml").getroot()
        self.assertEqual([k.text for k in image.iter("keytable")], [])

    def test_the_locales_package_generates_the_description_locale(self):
        # KIWI applies <locale> with `systemd-firstboot --locale`, which refuses a locale glibc cannot load (systemd
        # 257, locale-util.c). The locales package generates, when it is configured, the locales /etc/locale.gen
        # selects, and its config script keeps a selection that is already there; with no such file it generates none.
        # post_bootstrap.sh runs after the bootstrap packages and before locales is installed, and selects the locale.
        import xml.etree.ElementTree as ET
        image = ET.parse(KIWI / "config.xml").getroot()
        self.assertEqual([l.text for p in profile_preferences(image, DEBIAN) for l in p.iter("locale")], ["en_US"])
        script = KIWI / "post_bootstrap.sh"
        self.assertTrue(os.access(script, os.X_OK), "KIWI runs an executable script with its own interpreter")
        with tempfile.TemporaryDirectory(prefix="a2-post-bootstrap-") as temp:
            target, redirected = Path(temp) / "locale.gen", Path(temp) / "post_bootstrap.sh"
            release = Path(temp) / "debian_version"
            release.write_text("13.7\n")
            text = script.read_text()
            self.assertIn("/etc/locale.gen", text)
            redirected.write_text(text.replace("/etc/locale.gen", str(target)).replace("/etc/debian_version", str(release)))
            result = subprocess.run(["bash", str(redirected)], capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual([l for l in target.read_text().splitlines() if l[:1].isalpha()], ["en_US.UTF-8 UTF-8"])
        self.assertIn('cp "$here/post_bootstrap.sh" "$stage/"', (KIWI / "build.sh").read_text())

    def test_apt_installs_only_authenticated_packages(self):
        # KIWI 11.0.4 writes APT::Get::AllowUnauthenticated "true" into its apt configuration unless the description
        # sets rpm-check-signatures (kiwi/repository/apt.py, check_signatures; the element name is KIWI's for every
        # package manager). Every repository is now verified (Signed-By) or trusted for this build's own packages, so
        # nothing needs the exception.
        import xml.etree.ElementTree as ET
        image = ET.parse(KIWI / "config.xml").getroot()
        server = [p for p in image.iter("preferences") if "debian13-server-amd64" in (p.get("profiles") or "").split(",")]
        self.assertEqual([c.text for p in server for c in p.iter("rpm-check-signatures")], ["true"])

    def test_stage_step_builds_the_dracut_package_from_the_lock_modules_and_indexes_it(self):
        toolchain = json.loads(LOCK.read_text())["toolchain"]
        fixture = modules_fixture(self.work / "builder-modules")
        image, calls = self.qualified_image(toolchain["required_programs"], modules=fixture)
        result = self.build(FAKE_IMAGE_BIN=str(image), FAKE_IMAGE_LOCK=str(LOCK))
        self.assertEqual(result.returncode, 0, result.stderr[-2000:])
        version = toolchain["kiwi"]["upstream_version"]
        package = self.work / "stage/packages" / ("dracut-kiwi-oem-dump_%s_all.deb" % version)
        self.assertTrue(package.is_file(), sorted(p.name for p in (self.work / "stage/packages").iterdir()))
        members, fields, tars = read_deb(package)
        self.assertEqual([m[0] for m in members], ["debian-binary", "control.tar.gz", "data.tar.gz"])
        self.assertEqual((fields["Package"], fields["Version"], fields["Architecture"]),
                         ("dracut-kiwi-oem-dump", version, "all"))
        files = {n[len("./usr/lib/dracut/modules.d/"):]: (m.mode, body) for n, (m, body) in tars["data.tar.gz"].items()
                 if m.isfile()}
        self.assertEqual({rel: mode for rel, (mode, _) in files.items()}, MODULE_FILES)
        for rel, (_, body) in files.items():
            self.assertEqual(body, (fixture / rel).read_bytes(), rel)
        # KIWI's check after prepare reads this version with dpkg-query and compares it as dotted
        # integers after the first "-" (kiwi/runtime_checker.py): at least 9.20.1.
        self.assertGreaterEqual(tuple(int(x) for x in fields["Version"].split("-", 1)[0].split(".")), (9, 20, 1))
        # The package is indexed where KIWI's local repository reads it, and the build says so.
        index = (self.work / "stage/packages/Packages").read_text()
        self.assertIn("Package: dracut-kiwi-oem-dump\nVersion: %s\n" % version, index)
        self.assertIn("dracut-kiwi-oem-dump %s is in" % version, result.stdout + result.stderr)
        manifest = json.loads((self.work / "output/build.manifest.json").read_text())
        self.assertIn(package.name, [i["file"] for i in manifest["inputs"]])

    def test_the_build_names_each_local_package_by_digest_before_kiwi_trusts_it(self):
        # The local repository is trusted without a signature, so the build log names every package in it by
        # sha256 after the stage step and before KIWI reads it.
        toolchain = json.loads(LOCK.read_text())["toolchain"]
        image, _ = self.qualified_image(toolchain["required_programs"], modules=modules_fixture(self.work / "builder-modules"))
        result = self.build(FAKE_IMAGE_BIN=str(image), FAKE_IMAGE_LOCK=str(LOCK))
        self.assertEqual(result.returncode, 0, result.stderr[-2000:])
        packages = sorted((self.work / "stage/packages").glob("*.deb"))
        self.assertEqual(len(packages), 3)
        head, _, listed = result.stdout.partition("local repository before KIWI trusts it")
        self.assertTrue(listed, result.stdout[-2000:])
        for deb in packages:
            self.assertIn("%s  %s" % (hashlib.sha256(deb.read_bytes()).hexdigest(), deb.name), listed)

    def test_a_dracut_package_the_index_skips_stops_the_build_before_kiwi(self):
        # dpkg-scanpackages skips a package dpkg-deb cannot read, with a warning and exit 0; the
        # stage step must then stop, instead of KIWI failing later on a package apt cannot find.
        toolchain = json.loads(LOCK.read_text())["toolchain"]
        image, _ = self.qualified_image(toolchain["required_programs"],
                                        modules=modules_fixture(self.work / "builder-modules"), scan_skips=True)
        result = self.build(FAKE_IMAGE_BIN=str(image), FAKE_IMAGE_LOCK=str(LOCK))
        self.assertEqual(result.returncode, 1, result.stderr[-2000:])
        self.assertIn("does not list dracut-kiwi-oem-dump", result.stderr)
        self.assertEqual(len(self.effects()), 1, "the KIWI command never runs after a failed stage")

    def test_builder_failure_after_admission_is_a_defect_with_its_raw_status(self):
        # 2 is GNU tar's fatal status, 125 a container runtime error: after current admission
        # both are observed builder failures. The raw status stays in the build log.
        for status in (2, 125, 1):
            with self.subTest(status=status):
                output = self.work / ("output-%d" % status)
                result = self.build(("--target-dir", str(output)), FAKE_DOCKER_EXIT=str(status),
                                    WORK_DIR=str(self.work / ("stage-%d" % status)))
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn("exited with status %d" % status, result.stderr)
                self.assertFalse((output / "build.manifest.json").exists())

    def test_a_failing_tool_in_the_admitted_builder_is_a_defect(self):
        toolchain = json.loads(LOCK.read_text())["toolchain"]
        image, _ = self.qualified_image(toolchain["required_programs"], failing={"cp": 2})
        result = self.build(FAKE_IMAGE_BIN=str(image), FAKE_IMAGE_LOCK=str(LOCK))
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("exited with status 2", result.stderr)
        self.assertEqual(len(self.effects()), 1, "the KIWI command never runs after a failed stage")

    def test_a_missing_prerequisite_is_still_exit_two_before_any_container(self):
        (self.debs / "olivares_test.deb").unlink()
        self.assertEqual(self.build().returncode, 2)
        self.assertEqual(self.effects(), [])

    def test_an_interrupted_builder_command_ends_the_build_by_its_signal(self):
        process = subprocess.Popen(["bash", str(KIWI / "build.sh"), "--base", "debian13", "--deb-dir", str(self.debs),
            "--target-dir", str(self.work / "output"), "--receipt", str(self.receipt), "--attempt", ATTEMPT,
            "--image-id", IMAGE, "--accelerator", "tcg"], env=dict(self.env, FAKE_DOCKER_SLEEP="30"),
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
        deadline = time.monotonic() + 8
        while not self.effects() and time.monotonic() < deadline:
            time.sleep(0.05)
        os.killpg(process.pid, signal.SIGTERM)
        self.assertEqual(process.wait(timeout=10), -signal.SIGTERM)
        self.assertEqual(len(self.effects()), 1)
        self.assertFalse((self.work / "output/build.manifest.json").exists())

    def test_actual_shell_consumes_one_immutable_builder(self):
        result = self.build()
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.effects()
        self.assertEqual(len(calls), 2)
        self.assertTrue(all(c[0] == 'run' and IMAGE in c for c in calls))
        self.assertTrue(all('--cidfile' in c and '--label' in c for c in calls))
        self.assertFalse(any('build' == c[0] for c in calls))
        manifest = json.loads((self.work / 'output/build.manifest.json').read_text())
        self.assertEqual((manifest['builder_image_id'], manifest['attempt_id'], manifest['accelerator']),
                         (IMAGE, ATTEMPT, 'tcg'))

    def test_admission_refusal_precedes_filesystem_and_container(self):
        result = self.build(FAKE_ADMISSION_EXIT='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.effects(), [])
        self.assertFalse((self.work / 'stage').exists())
        self.assertFalse((self.work / 'output').exists())

    def test_missing_receipt_refuses_before_admission_or_effect(self):
        self.receipt.unlink()
        self.assertEqual(self.build().returncode, 2)
        self.assertEqual(self.effects(), [])

    def test_reply_mismatches_never_reach_container(self):
        mutations = [('state', 'eligible'), ('image_id', 'sha256:'+'d'*64),
                     ('accelerator', 'kvm'), ('context:attempt_id', 'e'*32),
                     ('context:image_id', 'sha256:'+'e'*64), ('context:accelerator', 'kvm'),
                     ('context:job', 'other'), ('context:run_id', '99'),
                     ('context:run_attempt', '1'), ('context:missing', None)]
        for key, value in mutations:
            with self.subTest(key=key):
                a = admission()
                if key == 'context:missing':
                    a.pop('expected_context')
                elif key.startswith('context:'):
                    a['expected_context'][key.split(':')[1]] = value
                else:
                    a[key] = value
                self.answer.write_text(json.dumps(a))
                self.assertNotEqual(self.build().returncode, 0)
                self.assertEqual(self.effects(), [])
                self.assertFalse((self.work / 'stage').exists())

    def test_mutable_id_unknown_accelerator_and_attempt_refuse(self):
        for args in [('--image-id','mutable:tag'), ('--accelerator','auto'), ('--attempt','not-owned')]:
            with self.subTest(args=args):
                self.assertEqual(self.build(args).returncode, 2)
                self.assertEqual(self.effects(), [])

    def test_build_error_is_not_success(self):
        self.assertNotEqual(self.build(FAKE_DOCKER_EXIT='9').returncode, 0)

    def test_shape_has_no_second_builder(self):
        self.assertFalse((KIWI / 'Containerfile').exists())
        body = (KIWI / 'build.sh').read_text()
        for forbidden in ('plan_build_image=', 'pip install', 'APPLIANCE_BUILDER_IMAGE'):
            self.assertNotIn(forbidden, body)

    def test_lifecycle_always_releases_after_each_failed_phase(self):
        module = load_attempt('recipe_attempt')
        for failure in ('preflight', 'build', 'format', 'boot', 'nocloud', None):
            with self.subTest(failure=failure):
                evidence = self.work / ('owner-' + str(failure))
                calls = []
                def preflight(directory, argv):
                    directory = Path(directory)
                    directory.mkdir(parents=True)
                    record = receipt()
                    if failure == 'preflight':
                        # The collector releases a builder it did not judge eligible.
                        record['observations']['container_toolchain'].update(
                            image_retained=False, image_removed=True, cleaned=True)
                    (directory/'receipt.json').write_text(json.dumps(record))
                    calls.append(('preflight', argv))
                    return 2 if failure == 'preflight' else 0
                def run(argv, log, timeout):
                    calls.append(('run', argv))
                    phase = ('release' if '--release' in argv else
                             'build' if any(str(x).endswith('/kiwi/build.sh') for x in argv) else
                             'format' if any(str(x).endswith('/formats/assemble.sh') for x in argv) else
                             'boot' if any(str(x).endswith('/boot-battery.sh') for x in argv) else 'nocloud')
                    return 7 if failure == phase else 0
                with patch.dict(os.environ, ENV), patch.object(module, 'execute_preflight', preflight), \
                     patch.object(module, 'run_command', run), patch.object(module, 'cleanup_containers', lambda *args: True):
                    code = module.execute(evidence, 'tcg', self.debs, self.work/'target', 'nonfree')
                self.assertEqual(code, 0 if failure is None else (2 if failure == 'preflight' else 1))
                release = [argv for kind, argv in calls if kind == 'run' and '--release' in argv]
                self.assertEqual(len(release), 0 if failure == 'preflight' else 1, calls)
                if release:
                    self.assertIn(IMAGE, release[0])
                    self.assertIn(ATTEMPT, release[0])
                self.assertIn('--retain-builder', calls[0][1])
                for kind, argv in calls:
                    if kind == 'run' and '--release' not in argv:
                        self.assertIn('APPLIANCE_BOOT_ACCEL=tcg', argv)
                if failure is None:
                    boots = [argv for kind,argv in calls if any(str(x).endswith('/boot-battery.sh') for x in argv)]
                    self.assertEqual(len(boots), 1)
                    self.assertEqual(boots[0][-4:], ['bios','uefi','uefi-secureboot','iso-install'])

    def test_nocloud_runs_after_a_boot_battery_that_measured_a_failure(self):
        # Hosted run 36270893718: one boot assertion failed, the recipe stopped at the boot phase, and the NoCloud battery,
        # which boots the same finished images with its own seed, never ran, so its evidence was never made. A boot battery
        # that measured and failed (exit 1) now leaves NoCloud to run; the recipe still fails, at the boot phase. A battery
        # that could not measure (2) or did not finish (a timeout, 124) still stops the recipe there.
        module = load_attempt('recipe_nocloud')
        for boot, nocloud_runs, expected in ((1, True, 1), (2, False, 2), (124, False, 1), (0, True, 0)):
            with self.subTest(boot=boot):
                evidence = self.work / ('nocloud-after-' + str(boot))
                calls = []
                def preflight(directory, argv):
                    Path(directory).mkdir(parents=True)
                    (Path(directory) / 'receipt.json').write_text(json.dumps(receipt()))
                    return 0
                def run(argv, log, timeout):
                    kind = ('release' if '--release' in argv else
                            'boot' if any(str(x).endswith('/boot-battery.sh') for x in argv) else
                            'nocloud' if any(str(x).endswith('/nocloud-probe.sh') for x in argv) else 'other')
                    calls.append(kind)
                    return boot if kind == 'boot' else 0
                with patch.dict(os.environ, ENV), patch.object(module, 'execute_preflight', preflight), \
                     patch.object(module, 'run_command', run), patch.object(module, 'cleanup_containers', lambda *args: True):
                    code = module.execute(evidence, 'tcg', self.debs, self.work / 'target', 'nonfree')
                self.assertEqual(code, expected)
                self.assertEqual('nocloud' in calls, nocloud_runs, calls)
                result = json.loads((evidence / 'recipe-result.json').read_text())
                self.assertEqual(result['phase'], 'nocloud' if boot == 0 else 'boot')
                self.assertEqual(result['phase_exit'], boot)
                names = [p['name'] for p in result['phases']]
                self.assertEqual(names[-2:] if nocloud_runs else names[-1:], ['boot', 'nocloud'] if nocloud_runs else ['boot'])

    def test_release_failure_is_visible_and_cross_job_cannot_release(self):
        module = load_attempt('recipe_release')
        evidence = self.work/'release-test'
        (evidence/'prerequisite').mkdir(parents=True)
        record = receipt()
        path = evidence/'prerequisite/receipt.json'
        path.write_text(json.dumps(record))
        calls = []
        def fail(argv, log, timeout):
            calls.append(argv)
            return 9
        with patch.dict(os.environ, ENV), patch.object(module, 'run_command', fail), \
             patch.object(module, 'cleanup_containers', lambda *args: True):
            self.assertEqual(module.release(evidence), 1)
            self.assertEqual(json.loads((evidence/'cleanup.json').read_text())['release_exit'], 9)
            record['binding']['job'] = 'another-job'
            path.write_text(json.dumps(record))
            self.assertEqual(module.release(evidence), 1)
            self.assertEqual(len(calls), 1, 'foreign job must not invoke release')

    def test_failed_child_cleanup_is_not_hidden_by_successful_image_release(self):
        module = load_attempt('recipe_cleanup')
        evidence = self.work/'child-test'
        (evidence/'prerequisite').mkdir(parents=True)
        (evidence/'prerequisite/receipt.json').write_text(json.dumps(receipt()))
        with patch.dict(os.environ, ENV), patch.object(module, 'run_command', lambda *args: 0), \
             patch.object(module, 'cleanup_containers', lambda *args: False):
            self.assertEqual(module.release(evidence), 1)
            self.assertFalse(json.loads((evidence/'cleanup.json').read_text())['containers_released'])

    def test_repeated_release_rechecks_child_absence(self):
        module = load_attempt('recipe_repeat')
        evidence = self.work/'repeat'
        (evidence/'prerequisite').mkdir(parents=True)
        (evidence/'prerequisite/receipt.json').write_text(json.dumps(receipt()))
        (evidence/'prerequisite/builder-release.json').write_text(json.dumps({
            'state':'released','image_removed':True,'attempt_id':ATTEMPT,'image_id':IMAGE}))
        with patch.dict(os.environ, ENV), patch.object(module, 'run_command', side_effect=AssertionError('no second image removal')):
            with patch.object(module, 'cleanup_containers', lambda *args: False):
                self.assertEqual(module.release(evidence), 1)
            with patch.object(module, 'cleanup_containers', lambda *args: True):
                self.assertEqual(module.release(evidence), 0)

    def test_boot_consumers_refuse_unknown_accelerator_before_qemu(self):
        for script in ('boot-battery.sh', 'nocloud-probe.sh'):
            with self.subTest(script=script):
                result = subprocess.run(['bash', str(ROOT/'appliance/test'/script), '--check-preflight', '/absent'],
                    env=dict(self.env, APPLIANCE_BOOT_ACCEL='auto'), text=True, capture_output=True, timeout=3)
                self.assertEqual(result.returncode, 2)
                self.assertIn('unknown accelerator', result.stdout)

    def test_every_workflow_job_owns_one_builder_and_always_releases(self):
        import yaml
        # The qualification fixture is ONE job with one builder; the publication route adds
        # two dispatch-gated jobs (build, then publish) that never run beside it. The
        # invariant each of them must keep is the one this test has always asserted: exactly
        # one attempt owner, and exactly one always-release retirement of it.
        workflow = yaml.safe_load((ROOT/'.github/workflows/appliance-image.yml').read_text())
        self.assertEqual(list(workflow['jobs']), ['image', 'release-image', 'publish-image'])
        self.assertEqual(workflow['jobs']['image'].get('if'), "inputs.publish != true")
        for name, job in workflow['jobs'].items():
            steps = job.get('steps', [])
            owners = [s for s in steps if 'kiwi/attempt.py' in s.get('run','') and '--release-only' not in s['run']]
            releases = [s for s in steps if '--release-only' in s.get('run','')]
            # A job either owns no builder at all (the publisher) or exactly one, retired by
            # exactly one always() step.
            self.assertLessEqual(len(owners), 1, name)
            self.assertEqual(len(releases), len(owners), name)
            if releases:
                self.assertEqual(releases[0]['if'], 'always()', name)

    def test_the_job_takes_an_edition_input_and_hands_it_to_the_attempt(self):
        job = workflow()["jobs"]["image"]
        spec = workflow()[True]["workflow_dispatch"]["inputs"]["edition"]
        self.assertEqual((spec["type"], spec["options"], spec.get("default")), ("choice", ["server", "desktop"],
                                                                                "server"))
        attempt = next(s for s in job["steps"] if "kiwi/attempt.py" in s.get("run", "")
                       and "--release-only" not in s["run"])
        self.assertIn("'EDITION': '${{ inputs.edition }}'", str(attempt.get("env", {})))
        self.assertIn('--edition "$EDITION"', attempt["run"])

    def test_job_ceiling_sits_above_its_step_ceilings_within_the_hosted_limit(self):
        job = workflow()["jobs"]["image"]
        steps = sum(step.get("timeout-minutes", 0) for step in job["steps"])
        self.assertGreater(job["timeout-minutes"], steps)
        self.assertLessEqual(job["timeout-minutes"], json.loads(LOCK.read_text())["runner"]["job_ceiling_minutes"])
        # The repository's own push gate reads the same file.
        workflows = self.work / "workflows"
        workflows.mkdir()
        shutil.copy(ROOT / ".github/workflows/appliance-image.yml", workflows)
        gate = subprocess.run(["bash", str(ROOT / "scripts/check-ci-timeout-arithmetic.sh")], text=True,
                              env=dict(os.environ, OLIVARES_CI_WORKFLOWS_DIR=str(workflows)),
                              capture_output=True, timeout=30)
        self.assertEqual(gate.returncode, 0, gate.stdout + gate.stderr)

    def test_evidence_upload_keeps_guest_timing_command_and_stderr_but_no_disks(self):
        globs = evidence_globs()
        for name in ("seconds", "qemu.cmd", "qemu.stderr", "*.jsonl"):
            self.assertIn("**/" + name, globs)
        for disk in ("**/*.qcow2", "**/*.fd", "**/*", ""):
            self.assertNotIn(disk, globs)

    def test_hashed_evidence_and_the_upload_are_one_set(self):
        # The real seal over every kind of attempt file, against the workflow's upload globs:
        # a file is uploaded exactly when SHA256SUMS hashes it, or when it is a SHA256SUMS.
        module = load_attempt("recipe_retained")
        globs = evidence_globs()
        self.assertTrue(globs and all(g.startswith("**/") and "/" not in g[3:] for g in globs), globs)
        evidence = self.work / "evidence"
        for name in EVIDENCE_FILES:
            (evidence / name).parent.mkdir(parents=True, exist_ok=True)
            (evidence / name).write_text(name + "\n")
        module.seal(evidence)
        hashed = {row.split("  ", 1)[1] for row in (evidence / "SHA256SUMS").read_text().splitlines()}
        manifests = {p.relative_to(evidence).as_posix() for p in evidence.rglob("SHA256SUMS")}
        uploaded = {p.relative_to(evidence).as_posix() for p in evidence.rglob("*")
                    if p.is_file() and any(fnmatch.fnmatchcase(p.name, g[3:]) for g in globs)}
        self.assertEqual(sorted(uploaded - hashed - manifests), [], "uploaded but not hashed")
        self.assertEqual(sorted(hashed - uploaded), [], "hashed but not uploaded")
        for used in globs:
            self.assertTrue(any(fnmatch.fnmatchcase(Path(n).name, used[3:]) for n in hashed | manifests), used)

    def test_image_upload_keeps_exactly_the_declared_files(self):
        # The image upload keeps the files formats.json declares, by their names. A glob such as
        # dist/appliance/*.iso also keeps KIWI's installer medium: iso.sh finds it in the target
        # directory and delivers the declared ISO beside it, so the artifact would hold four files.
        upload = next(s for s in workflow()["jobs"]["image"]["steps"]
                      if "appliance-image-artifacts" in s.get("with", {}).get("name", ""))
        paths = [line.strip() for line in upload["with"]["path"].splitlines() if line.strip()]
        text = (ROOT / "appliance/images/formats/formats.json").read_text()
        declared = json.loads("\n".join(line for line in text.splitlines() if not line.lstrip().startswith("//")))
        self.assertEqual(sorted(paths), sorted("dist/appliance/" + a["file"] for a in declared["artifacts"]))
        self.assertEqual([p for p in paths if any(c in p for c in "*?[")], [], "a pattern can keep more than the declared file")


class ArchiveKeys(unittest.TestCase):
    """The Debian repositories are verified with the builder's archive keyring, locked by sha256.

    apt 3.0.3 in the Debian 13 builder takes a repository's keyring from its Signed-By field, else from the
    trusted.gpg.d directory below KIWI's apt directory, which nothing creates; it reads trusted.gpg, where KIWI's
    own <signing> keys go, only when Dir::Etc::Trusted is set, and KIWI does not set it (apt methods/sqv.cc,
    apt-pkg/init.cc). Hosted run 36237834215 stopped there: "The signatures couldn't be verified because no
    keyring is specified". KIWI runs a repository's customize script on the sources file it writes."""

    # debian-archive-keyring 2025.1, the version the builder's dpkg inventory records: its .deb (sha256 9ea7778e)
    # is the one the trixie main index lists, whose InRelease is the toolchain lock's inrelease_sha256.
    KEYRING = "/usr/share/keyrings/debian-archive-keyring.pgp"
    KEYRING_SHA256 = "506b815cbb32d9b6066b4a2aa524071e071761e7e7f68c3ac74f3061ba852017"

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="a2-keys-")
        self.addCleanup(self.temp.cleanup)
        self.work = Path(self.temp.name)
        self.description = self.work / "description"
        self.description.mkdir()
        shutil.copy2(KIWI / "archive-signed-by.sh", self.description / "archive-signed-by.sh")
        self.key = self.work / "keyrings/debian-archive-keyring.pgp"
        self.key.parent.mkdir()
        self.key.write_bytes(b"\x99\x01\x0d an archive keyring")
        self.lock({"path": str(self.key), "sha256": hashlib.sha256(self.key.read_bytes()).hexdigest()})
        self.sources = self.work / "debian-trixie.sources"
        self.written = ("Types: deb\nURIs: http://deb.debian.org/debian\nSuites: trixie\n"
                        "Components: main non-free-firmware\n")
        self.sources.write_text(self.written)

    def lock(self, *keys):
        (self.description / "archive-keys.json").write_text(json.dumps({"keys": list(keys)}))

    def customize(self):
        # KIWI's own call (kiwi/repository/base.py, run_repo_customize).
        return subprocess.run(["bash", "--norc", str(self.description / "archive-signed-by.sh"), str(self.sources)],
                              capture_output=True, text=True, timeout=10)

    def test_each_debian_repository_is_bound_to_the_locked_builder_keyring(self):
        import xml.etree.ElementTree as ET
        image = ET.parse(KIWI / "config.xml").getroot()
        debian = [r for r in image.iter("repository") if in_profile(r, DEBIAN)]
        customize = {r.get("alias"): (r.get("type"), r.get("customize")) for r in debian}
        self.assertEqual({alias: script for alias, (kind, script) in customize.items() if kind == "apt-deb"},
                         {"debian-trixie": "archive-signed-by.sh", "debian-trixie-updates": "archive-signed-by.sh",
                          "debian-trixie-security": "archive-signed-by.sh"})
        self.assertEqual({alias: script for alias, (kind, script) in customize.items() if kind != "apt-deb"},
                         {"olivares-appliance-packages": None})
        # Keys come from the builder's own keyring package, never from a download or the description.
        self.assertEqual([s.get("key") for r in debian for s in r.iter("signing")], [])
        locked = json.loads((KIWI / "archive-keys.json").read_text())
        self.assertEqual([(k["path"], k["sha256"]) for k in locked["keys"]], [(self.KEYRING, self.KEYRING_SHA256)])

    def test_build_stages_the_key_check_beside_the_description(self):
        build = (KIWI / "build.sh").read_text()
        self.assertIn('cp "$here/archive-signed-by.sh" "$here/archive-keys.json" "$stage/"', build)

    def test_a_locked_key_binds_the_repository_with_signed_by(self):
        result = self.customize()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.sources.read_text(), self.written + "Signed-By: %s\n" % self.key)

    def test_a_key_whose_digest_differs_is_refused_and_nothing_is_written(self):
        self.key.write_bytes(b"\x99\x01\x0d another keyring")
        result = self.customize()
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("archive-keys.json locks", result.stderr)
        self.assertEqual(self.sources.read_text(), self.written)

    def test_a_missing_key_is_refused_and_nothing_is_written(self):
        self.key.unlink()
        result = self.customize()
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(self.sources.read_text(), self.written)

    def test_a_repository_that_already_states_its_trust_is_refused(self):
        for line in ("Signed-By: /etc/apt/keyrings/other.pgp\n", "trusted: yes\n"):
            with self.subTest(line=line):
                self.sources.write_text(self.written + line)
                result = self.customize()
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertEqual(self.sources.read_text(), self.written + line)

    def test_a_file_that_is_not_an_apt_sources_file_cannot_run(self):
        # KIWI also runs a repository's customize script on the repository's .pref file when the repository has a
        # priority; a Signed-By line does not belong there.
        preferences = self.work / "debian-trixie.pref"
        preferences.write_text("Package: *\nPin: origin \"deb.debian.org\"\nPin-Priority: 500\n")
        self.sources = preferences
        result = self.customize()
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertEqual(preferences.read_text(), "Package: *\nPin: origin \"deb.debian.org\"\nPin-Priority: 500\n")

    def test_a_lock_that_names_no_usable_key_cannot_run(self):
        digest = hashlib.sha256(self.key.read_bytes()).hexdigest()
        for keys in ((), ({"path": "keyrings/debian-archive-keyring.pgp", "sha256": digest},),
                     ({"path": str(self.key)},)):
            with self.subTest(keys=keys):
                self.lock(*keys)
                result = self.customize()
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertEqual(self.sources.read_text(), self.written)


class DracutPackage(unittest.TestCase):
    """The stage step's dracut package builder, run the way the builder runs it."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="a2-dracut-")
        self.addCleanup(self.temp.cleanup)
        self.work = Path(self.temp.name)
        self.root = self.work / "stage"
        self.modules = modules_fixture(self.root / "usr/lib/dracut/modules.d")
        self.out = self.work / "packages"
        self.out.mkdir()
        self.version = json.loads(LOCK.read_text())["toolchain"]["kiwi"]["upstream_version"]

    def run_packer(self, *args, lock=LOCK):
        return subprocess.run([sys.executable, str(KIWI / "dracut_package.py"), "--lock", str(lock), *args],
                              capture_output=True, text=True, timeout=20)

    def build_package(self):
        return self.run_packer("--root", str(self.root), "--output", str(self.out))

    def test_package_is_root_owned_reproducible_and_carries_the_modules_unchanged(self):
        result = self.build_package()
        self.assertEqual(result.returncode, 0, result.stderr)
        package = self.out / ("dracut-kiwi-oem-dump_%s_all.deb" % self.version)
        first = package.read_bytes()
        package.unlink()
        self.assertEqual(self.build_package().returncode, 0)
        self.assertEqual(package.read_bytes(), first, "two builds of the same modules differ")
        members, fields, tars = read_deb(package)
        self.assertEqual(members[0][2], b"2.0\n")
        for name, header, _ in members:
            self.assertEqual(header[28:40].split(), [b"0", b"0"], name)
        for tar in tars.values():
            for name, (info, _) in tar.items():
                self.assertEqual((info.uid, info.gid, info.uname, info.gname), (0, 0, "root", "root"), name)
        self.assertEqual((fields["Maintainer"], fields["Depends"]), ("Olivares.AI <enterprise@olivares.ai>", "dracut-core"))
        data = tars["data.tar.gz"]
        for rel, mode in MODULE_FILES.items():
            info, body = data["./usr/lib/dracut/modules.d/" + rel]
            self.assertEqual((info.mode, body), (mode, (self.modules / rel).read_bytes()), rel)
        self.assertEqual(len([n for n, (i, _) in data.items() if i.isfile()]), len(MODULE_FILES))
        md5sums = tars["control.tar.gz"]["./md5sums"][1].decode().splitlines()
        self.assertEqual(sorted(line.split("  ", 1)[1] for line in md5sums),
                         sorted("usr/lib/dracut/modules.d/" + rel for rel in MODULE_FILES))

    def test_a_builder_without_the_installer_modules_is_refused(self):
        for rel in ("55kiwi-dump/module-setup.sh", "59kiwi-dump-reboot/module-setup.sh", "59kiwi-lib/module-setup.sh"):
            with self.subTest(missing=rel):
                kept = (self.modules / rel).read_bytes()
                (self.modules / rel).unlink()
                result = self.build_package()
                (self.modules / rel).write_bytes(kept)
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn(rel, result.stderr)
                self.assertEqual(list(self.out.iterdir()), [])

    def test_the_version_is_the_exact_locked_kiwi_version(self):
        # The positive control first: an exit 2 from a packer that is not there proves nothing.
        result = self.build_package()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual([p.name for p in self.out.iterdir()], ["dracut-kiwi-oem-dump_%s_all.deb" % self.version])
        (self.out / ("dracut-kiwi-oem-dump_%s_all.deb" % self.version)).unlink()
        # A Debian revision or an epoch would break KIWI's dotted-integer comparison after prepare.
        for version in ("11.0.4-1", "1:11.0.4", "11.0", ""):
            with self.subTest(version=version):
                lock = json.loads(LOCK.read_text())
                lock["toolchain"]["kiwi"]["upstream_version"] = version
                path = self.work / "lock.json"
                path.write_text(json.dumps(lock))
                result = self.run_packer("--root", str(self.root), "--output", str(self.out), lock=path)
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertEqual(list(self.out.iterdir()), [])

    def test_an_existing_package_is_never_overwritten(self):
        self.assertEqual(self.build_package().returncode, 0)
        again = self.build_package()
        self.assertEqual(again.returncode, 1, again.stderr)
        self.assertIn("already exists", again.stderr)

    def test_the_index_check_reads_the_package_at_the_locked_version(self):
        index = self.work / "Packages"
        other = "Package: olivares\nVersion: 0.0.0-dev\nFilename: ./olivares_0.0.0-dev_amd64.deb\n\n"
        cases = (
            (other + "Package: dracut-kiwi-oem-dump\nVersion: %s\nFilename: ./p.deb\n\n" % self.version, 0),
            (other, 1),
            ("Package: dracut-kiwi-oem-dump\nVersion: 9.24.0\nFilename: ./p.deb\n\n", 1),
        )
        for text, expected in cases:
            with self.subTest(expected=expected, text=text[-60:]):
                index.write_text(text)
                self.assertEqual(self.run_packer("--indexed", str(index)).returncode, expected)
        index.unlink()
        self.assertEqual(self.run_packer("--indexed", str(index)).returncode, 2)
        self.assertEqual(self.run_packer().returncode, 2, "neither mode given")


# An emulated rpmbuild for the Fedora builder: it records its arguments, keeps the spec, and writes the one package the spec
# names to the --define "_rpmdir DIR" directory under the --define "_build_name_fmt" name, as rpmbuild -bb does.
FAKE_RPMBUILD = r'''
import json, os, re, sys
args = sys.argv[1:]
with open(os.environ["FAKE_RPMBUILD_LOG"], "a") as log:
    log.write(json.dumps(args) + "\n")
defines = dict(args[i + 1].split(" ", 1) for i, a in enumerate(args) if a == "--define")
spec = open(args[-1]).read()
open(os.environ["FAKE_RPMBUILD_SPEC"], "w").write(spec)
fields = dict(re.findall(r"(?m)^(Name|Version|Release|BuildArch):\s*(\S+)", spec))
name = defines["_build_name_fmt"].replace("%%{NAME}", fields["Name"]).replace("%%{VERSION}", fields["Version"]) \
    .replace("%%{RELEASE}", fields["Release"]).replace("%%{ARCH}", fields["BuildArch"])
open(os.path.join(defines["_rpmdir"], name), "wb").write(b"\xed\xab\xee\xdb fake rpm")
'''


def primary_gz(entries):
    """A createrepo_c repodata directory's repomd.xml and gzip primary for ENTRIES [(name, version, release, href)]."""
    import gzip as gz
    packages = "".join(
        '<package type="rpm"><name>%s</name><arch>noarch</arch><version epoch="0" ver="%s" rel="%s"/>'
        '<location href="%s"/></package>' % entry for entry in entries)
    primary = gz.compress(('<?xml version="1.0" encoding="UTF-8"?><metadata xmlns="http://linux.duke.edu/metadata/common" '
                           'packages="%d">%s</metadata>' % (len(entries), packages)).encode(), mtime=0)
    repomd = ('<?xml version="1.0" encoding="UTF-8"?><repomd xmlns="http://linux.duke.edu/metadata/repo">'
              '<data type="primary"><location href="repodata/primary.xml.gz"/></data></repomd>')
    return repomd, primary


# An emulated createrepo_c for the Fedora builder: gzip metadata (repodata/repomd.xml and primary.xml.gz) for the RPMs in the
# directory it is given, each read from its file name NAME-VERSION-RELEASE.ARCH.rpm.
FAKE_CREATEREPO = r'''
import gzip, os, sys
args = sys.argv[1:]
assert args[:2] == ["--general-compress-type", "gz"], args
directory = args[2]
rows = []
for name in sorted(os.listdir(directory)):
    if name.endswith(".rpm"):
        stem = name[:-4]
        stem, arch = stem.rsplit(".", 1)
        base, version, release = stem.rsplit("-", 2)
        rows.append('<package type="rpm"><name>%s</name><arch>%s</arch><version epoch="0" ver="%s" rel="%s"/>'
                    '<location href="%s"/></package>' % (base, arch, version, release, name))
os.makedirs(os.path.join(directory, "repodata"), exist_ok=True)
open(os.path.join(directory, "repodata/primary.xml.gz"), "wb").write(gzip.compress((
    '<metadata xmlns="http://linux.duke.edu/metadata/common" packages="%d">%s</metadata>' % (len(rows), "".join(rows))).encode()))
open(os.path.join(directory, "repodata/repomd.xml"), "w").write(
    '<repomd xmlns="http://linux.duke.edu/metadata/repo"><data type="primary"><location href="repodata/primary.xml.gz"/>'
    '</data></repomd>')
'''
FEDORA_LOCK = ROOT / "appliance/images/toolchain/fedora44/input-lock.json"

# Emulated S3 scripts of D (scripts/rpm-payload-sign.py and scripts/render-rpm-repodata.py at 0621ff27), as the stage step
# calls them: each records its arguments, the key descriptor's environment and fingerprint and the test-only latch, and
# refuses a secret key readable by others, as the real ones do. The signer marks each RPM; the renderer writes gzip
# metadata as createrepo_c and a repomd.xml.asc.
FAKE_S3_COMMON = r'''
import json, os, stat, sys
from pathlib import Path
descriptor = json.load(open(os.environ["OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE"]))
secret = Path(os.environ["OLIVARES_PACKAGE_REPO_OPENPGP_SECRET_KEY_FILE"])
if stat.S_IMODE(secret.stat().st_mode) & 0o077 or b"PRIVATE KEY BLOCK" not in secret.read_bytes():
    sys.exit(1)
with open(@LOG@, "a") as log:
    log.write(json.dumps({"script": Path(__file__).name, "argv": sys.argv[1:], "environment": descriptor["environment"],
                          "fingerprint": descriptor["openpgp_fingerprint"],
                          "test_only": os.environ.get("OLIVARES_PACKAGE_REPO_TEST_ONLY")}) + "\n")
'''
FAKE_RPM_PAYLOAD_SIGN = FAKE_S3_COMMON + r'''
for i, a in enumerate(sys.argv[1:]):
    if a == "--rpm":
        path = Path(sys.argv[i + 2])
        path.write_bytes(path.read_bytes() + b"signed-by:" + descriptor["openpgp_fingerprint"].encode())
'''
FAKE_RENDER_RPM_REPODATA = FAKE_S3_COMMON + r'''
repo = sys.argv[sys.argv.index("--repo") + 1]
import subprocess
subprocess.run([sys.executable, "-c", @CREATEREPO@, "--general-compress-type", "gz", repo], check=True)
Path(repo, "repodata/repomd.xml.asc").write_text("-----BEGIN PGP SIGNATURE-----\nfake\n-----END PGP SIGNATURE-----\n")
'''


class FedoraBuild(unittest.TestCase):
    """build.sh builds the Fedora 44 profile by default, from the Fedora builder and the product's RPMs; the Debian profile
    stays buildable by name."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="a2-fedora-build-")
        self.addCleanup(self.temp.cleanup)
        self.work = Path(self.temp.name)
        self.bin = self.work / "bin"
        self.bin.mkdir()
        self.rpms = self.work / "rpms"
        self.rpms.mkdir()
        for name in ("olivares-0.0.0-1.x86_64.rpm", "olivares-appliance-base-0.0.0-1.noarch.rpm"):
            (self.rpms / name).write_text("inert package")
        self.log = self.work / "effects.jsonl"
        (self.work / "answer.json").write_text(json.dumps(admission()))
        (self.work / "receipt.json").write_text(json.dumps(receipt()))
        # release_outputs.py is its own seam (ReleaseSbom, ReleaseBundle): here it is recorded, answers as the source pin
        # it prints, and its bundle step leaves a 4096-byte archive under the name it writes.
        (self.bin / "python3").write_text("#!/bin/sh\ncase \"$1\" in */runner/admit.py) cat \"$FAKE_ADMISSION\"; exit 0;;\n"
                                          "  */kiwi/release_outputs.py) printf '%s\\n' \"$*\" >> \"$FAKE_RELEASE_OUTPUTS\"\n"
                                          "    [ \"$2\" = bundle ] && head -c 4096 /dev/zero > \"$4/olivares-appliance-$6-source.tar.zst\"\n"
                                          "    [ \"$2\" = pin ] && echo '{\"commit\": \"pinned\"}'; exit 0;; esac\n"
                                          "exec " + sys.executable + " \"$@\"\n")
        (self.bin / "docker").write_text("#!" + sys.executable + "\n" + FAKE_DOCKER)
        for executable in self.bin.iterdir():
            executable.chmod(0o755)
        self.env = dict(os.environ, **ENV, PATH=str(self.bin) + os.pathsep + os.environ["PATH"],
                        FAKE_ADMISSION=str(self.work / "answer.json"), FAKE_EFFECTS=str(self.log),
                        WORK_DIR=str(self.work / "stage"), VERSION=RELEASE_VERSION,
                        FAKE_RELEASE_OUTPUTS=str(self.work / "release-outputs.log"))

    def plan(self, *extra):
        return subprocess.run(["bash", str(KIWI / "build.sh"), "--print-plan", *extra], capture_output=True, text=True,
                              timeout=10, env=self.env)

    def test_the_shipping_default_is_the_fedora_44_profile_on_the_fedora_builder(self):
        result = self.plan()
        self.assertEqual(result.returncode, 0, result.stderr)
        lock = json.loads(FEDORA_LOCK.read_text())
        self.assertIn("base:          fedora44\n", result.stdout)
        self.assertIn("profiles:      fedora44-server-amd64 fedora44-firmware\n", result.stdout)
        self.assertIn("kiwi:          %s\n" % lock["toolchain"]["kiwi"]["upstream_version"], result.stdout)
        self.assertIn("base image:    registry.fedoraproject.org/fedora:44@%s\n" % FEDORA_BASE_AMD64, result.stdout)
        self.assertIn("stage_fedora.py", result.stdout)
        self.assertNotIn("dpkg-scanpackages", result.stdout)
        self.assertIn("release:       yes, package repository key %s\n" % RELEASE_KEY_FINGERPRINT, result.stdout)
        qualification = self.plan("--qualification-key", "A" * 40)
        self.assertIn("release:       no, qualification key %s\n" % ("A" * 40), qualification.stdout)
        self.assertEqual(self.plan("--qualification-key", "not-a-fingerprint").returncode, 2)
        free = self.plan("--firmware", "free")
        self.assertIn("profiles:      fedora44-server-amd64\n", free.stdout)

    def test_the_desktop_edition_plans_the_desktop_profile_and_only_on_fedora(self):
        result = self.plan("--edition", "desktop")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("edition:       desktop\n", result.stdout)
        self.assertIn("profiles:      fedora44-desktop-amd64 fedora44-firmware\n", result.stdout)
        free = self.plan("--edition", "desktop", "--firmware", "free")
        self.assertIn("profiles:      fedora44-desktop-amd64\n", free.stdout)
        self.assertEqual(self.plan("--edition", "workstation").returncode, 2)
        self.assertEqual(self.plan("--edition", "desktop", "--base", "debian13").returncode, 2)

    def test_the_debian_profile_is_still_built_by_name(self):
        result = self.plan("--base", "debian13")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("profiles:      debian13-server-amd64 firmware-nonfree\n", result.stdout)
        self.assertIn("base image:    debian:13@sha256:", result.stdout)
        self.assertIn("dpkg-scanpackages", result.stdout)
        self.assertEqual(self.plan("--base", "centos10").returncode, 2)

    def fedora_build(self, package_dir, *extra, **env):
        return subprocess.run(["bash", str(KIWI / "build.sh"), "--package-dir", str(package_dir), "--target-dir",
                               str(self.work / "output"), "--receipt", str(self.work / "receipt.json"), "--attempt", ATTEMPT,
                               "--image-id", IMAGE, "--accelerator", "tcg", *extra], capture_output=True, text=True,
                              timeout=60, env=dict(self.env, **env))

    def key(self):
        home = Path(tempfile.mkdtemp(prefix="g.", dir=os.environ.get("TMPDIR")))
        home.chmod(0o700)
        self.addCleanup(shutil.rmtree, home, True)
        self.addCleanup(subprocess.run, ["gpgconf", "--homedir", str(home), "--kill", "all"], capture_output=True)
        return (home,) + throwaway_key(home)

    def test_a_fedora_build_requires_the_signed_delivery(self):
        debs = self.work / "debs"
        debs.mkdir()
        for name in ("olivares_test.deb", "olivares-appliance-base_test.deb"):
            (debs / name).write_text("inert package")
        result = self.fedora_build(debs)
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertIn("delivery.json", result.stderr)
        self.assertFalse(self.log.exists(), "no container runs for a build that cannot run")

    def test_a_release_build_refuses_a_delivery_signed_by_another_key(self):
        home, fingerprint, public = self.key()
        signed_delivery(self.work / "delivery", home, fingerprint, public)
        result = self.fedora_build(self.work / "delivery")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn(RELEASE_KEY_FINGERPRINT, result.stderr)
        self.assertFalse(self.log.exists(), "no container runs for a refused delivery")
        other = self.fedora_build(self.work / "delivery", "--qualification-key", "B" * 40)
        self.assertEqual(other.returncode, 1, "a qualification key other than the delivery's")

    def test_the_fedora_stage_step_signs_the_dracut_rpm_in_its_own_repository_before_kiwi(self):
        home, fingerprint, public = self.key()
        delivery = signed_delivery(self.work / "delivery", home, fingerprint, public)
        s3 = self.work / "s3-scripts"
        s3.mkdir()
        # build.sh copies the scripts into the build, and the emulated container keeps only PATH: the log path travels
        # in the scripts themselves.
        log = repr(str(s3 / "s3-calls.jsonl"))
        (s3 / "rpm-payload-sign.py").write_text(FAKE_RPM_PAYLOAD_SIGN.replace("@LOG@", log))
        (s3 / "render-rpm-repodata.py").write_text(FAKE_RENDER_RPM_REPODATA.replace("@CREATEREPO@", repr(FAKE_CREATEREPO))
                                                   .replace("@LOG@", log))
        toolchain = json.loads(FEDORA_LOCK.read_text())["toolchain"]
        image = self.work / "image-bin"
        image.mkdir()
        fixture = modules_fixture(self.work / "builder-modules")
        declared = toolchain["dracut_modules_dir"]
        bodies = {"bash": 'exec %s "$@"\n' % shutil.which("bash"), "python3": 'exec %s "$@"\n' % sys.executable,
                  "mkdir": 'exec %s "$@"\n' % shutil.which("mkdir"),
                  "cp": 'src=$2; case $src in %s/*) src=%s/${src#%s/};; esac; exec %s "$1" "$src" "$3"\n' % (
                      declared, fixture, declared, shutil.which("cp")),
                  # The emulated container keeps only PATH (FAKE_DOCKER), so the fake's own paths travel in its body.
                  "rpmbuild": "FAKE_RPMBUILD_LOG=%s FAKE_RPMBUILD_SPEC=%s exec %s %s \"$@\"\n" % (
                      self.work / "rpmbuild.jsonl", self.work / "spec", sys.executable, self.work / "fake-rpmbuild.py"),
                  "gpg": 'exec %s "$@"\n' % shutil.which("gpg"), "gpgconf": 'exec %s "$@"\n' % shutil.which("gpgconf")}
        (self.work / "fake-rpmbuild.py").write_text(FAKE_RPMBUILD)
        for program in toolchain["required_programs"]:
            (image / program).write_text("#!/bin/sh\n" + bodies.get(program, "exit 0\n"))
            (image / program).chmod(0o755)
        result = self.fedora_build(self.work / "delivery", "--qualification-key", fingerprint,
                                   FAKE_IMAGE_BIN=str(image), FAKE_IMAGE_LOCK=str(FEDORA_LOCK), S3_SCRIPTS_DIR=str(s3))
        self.assertEqual(result.returncode, 0, result.stderr[-3000:])
        version = toolchain["kiwi"]["upstream_version"]
        dracut = "dracut-kiwi-oem-dump-%s-1.noarch.rpm" % version
        # The product repository is D's tree as delivered: its signed metadata is never rewritten or added to.
        for rel in ["repodata/repomd.xml", "repodata/repomd.xml.asc", "repodata/primary.xml.gz"] + \
                [p["file"] for p in delivery["packages"]]:
            self.assertEqual((self.work / "stage/packages" / rel).read_bytes(), (self.work / "delivery" / rel).read_bytes(), rel)
        self.assertEqual(sorted(p.name for p in (self.work / "stage/packages").glob("*.rpm")),
                         sorted(p["file"] for p in delivery["packages"]))
        # The dracut RPM is signed with a per-build key through D's S3 scripts, in a repository of its own.
        self.assertEqual([p.name for p in (self.work / "stage/build-packages").glob("*.rpm")], [dracut])
        calls = [json.loads(line) for line in (s3 / "s3-calls.jsonl").read_text().splitlines()]
        self.assertEqual([c["script"] for c in calls], ["rpm-payload-sign.py", "render-rpm-repodata.py"])
        self.assertEqual(calls[0]["argv"], ["--rpm", str(self.work / "stage/build-packages" / dracut)])
        self.assertEqual(calls[1]["argv"], ["render", "--repo", str(self.work / "stage/build-packages")])
        self.assertTrue(all(c["environment"] == "test" and c["test_only"] == "1" for c in calls))
        build_key = calls[0]["fingerprint"]
        self.assertNotEqual(build_key, fingerprint)
        self.assertIn("dracut-kiwi-oem-dump %s is in" % version, result.stdout + result.stderr)
        repositories = json.loads((self.work / "stage/description/olivares-repositories.json").read_text())
        self.assertEqual(repositories["olivares-appliance-rpms"],
                         {"key": "olivares-package-repository.asc", "fingerprint": fingerprint})
        built = json.loads((self.work / "stage/description/olivares-build-repository.json").read_text())
        self.assertEqual(built["olivares-appliance-build"], {"key": "olivares-build-key.asc", "fingerprint": build_key})
        self.assertFalse(list((self.work / "stage").rglob("*secret*")), "the per-build secret key is discarded")
        # ... and never printed: the build log is kept as evidence.
        self.assertNotIn("PRIVATE KEY", result.stdout + result.stderr)
        overlay = self.work / "stage/description/fedora44-server-amd64"
        self.assertEqual((self.work / "stage/description/images.sh").read_bytes(), (KIWI / "images.sh").read_bytes())
        # config.sh erases the per-build key from the image's rpm keyring by this record (C2).
        self.assertEqual((overlay / BUILD_KEY_RECORD).read_text(), build_key + "\n")
        self.assertEqual((overlay / "etc/pki/rpm-gpg/RPM-GPG-KEY-olivares-package-repository").read_bytes(), public)
        self.assertTrue((overlay / "etc/yum.repos.d/olivares.repo").is_file())
        head, _, listed = result.stdout.partition("local repositories before KIWI reads them")
        for directory in ("packages", "build-packages"):
            for rpm in (self.work / "stage" / directory).glob("*.rpm"):
                self.assertIn("%s  %s/%s" % (hashlib.sha256(rpm.read_bytes()).hexdigest(), directory, rpm.name), listed)
        runs = [c for c in [json.loads(s) for s in self.log.read_text().splitlines()] if c[0] == "run"]
        self.assertEqual([c[c.index(IMAGE) + 1] for c in runs], ["bash", "kiwi-ng"])
        kiwi = runs[-1]
        self.assertIn(str(self.work / "stage/build-packages") + ":/build-packages", kiwi)
        self.assertEqual([kiwi[i + 1] for i, a in enumerate(kiwi) if a == "--profile"],
                         ["fedora44-server-amd64", "fedora44-firmware"])
        manifest = json.loads((self.work / "output/build.manifest.json").read_text())
        self.assertEqual(sorted(i["file"] for i in manifest["inputs"]),
                         sorted(["build-packages/" + dracut] + ["packages/" + p["file"] for p in delivery["packages"]]))
        self.assertEqual((manifest["base"], manifest["release"], manifest["package_repository_key"], manifest["build_key"]),
                         ("fedora44", False, fingerprint, build_key))
        # The source pin is named before any container; the SBOM is written from the image's rpm database after KIWI.
        calls = [line.split() for line in (self.work / "release-outputs.log").read_text().splitlines()]
        self.assertEqual([c[1] for c in calls], ["pin", "sbom", "bundle", "verify-bundle"])
        bundle = calls[2]
        self.assertEqual(bundle[2:], calls[1][2:], "the bundle reads the same inputs as the SBOM")
        verify = calls[3]
        self.assertEqual((verify[verify.index("--target-dir") + 1], verify[verify.index("--version") + 1]),
                         (str(self.work / "output"), RELEASE_VERSION))
        sbom = calls[1]
        for option, value in (("--target-dir", str(self.work / "output")), ("--version", RELEASE_VERSION),
                              ("--lock", str(FEDORA_LOCK)), ("--repositories", str(KIWI / "fedora-repositories.json")),
                              ("--delivery", str(self.work / "stage/packages")),
                              ("--build-packages", str(self.work / "stage/build-packages")), ("--source-root", str(ROOT))):
            self.assertEqual(sbom[sbom.index(option) + 1], value, option)
        self.assertIn("--fetch", sbom)
        self.assertEqual(manifest["version"], RELEASE_VERSION)
        # The bundle's measured size is in the build evidence: the build log and the manifest's outputs.
        bundle_name = "olivares-appliance-%s-source.tar.zst" % RELEASE_VERSION
        self.assertIn("source bundle: %s, 4096 bytes (a release asset may not pass 2147483648)" % bundle_name,
                      result.stdout)
        self.assertIn({"file": bundle_name, "bytes": 4096, "sha256": hashlib.sha256(b"\0" * 4096).hexdigest()},
                      manifest["outputs"])

    def test_a_fedora_build_takes_x_y_z_or_0_0_0_dev_as_its_version(self):
        for version in VERSIONS_REFUSED:
            with self.subTest(refused=version):
                result = self.fedora_build(self.work / "delivery", VERSION=version)
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertIn("VERSION is X.Y.Z or 0.0.0-dev, not '%s'" % version, result.stderr)
                self.assertFalse(self.log.exists(), "no container runs for a build that cannot name its outputs")
        for version in VERSIONS_ACCEPTED:
            with self.subTest(accepted=version):
                # Past the version rule, this build stops at the next check: no delivery was made.
                result = self.fedora_build(self.work / "delivery", VERSION=version)
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertNotIn("VERSION", result.stderr)
                self.assertIn("delivery.json", result.stderr)


# D's package-repository key, production descriptor (packaging/repositories/production-key-descriptor.json since 416f3c1d,
# unchanged at ccf7ea20 and at S3's 0621ff27). The published keys/olivares-package-repository.asc answered 404 at
# 2026-09-27T11:16:14Z (r12b-fedora/sources/GETS.tsv), so the release pin is the descriptor's fingerprint.
RELEASE_KEY_FINGERPRINT = "AE27B5B818C9BCFE485B4D8B155E5EC6550A6EF8"
# The outputs are named from VERSION alone: X.Y.Z (a release) or 0.0.0-dev (a qualification), and every other value is
# refused before any container or output (RECIPE-OUTPUTS-INTERFACE addendum 3).
VERSIONS_ACCEPTED = ("0.0.0-dev", "26.10.0")
VERSIONS_REFUSED = ("1.0", "v26.10.0", "26.10.0-rc1", "", "0.0.0-DEV")
PRODUCT_PACKAGES = (("olivares", "0.0.0", "1", "x86_64"), ("olivares-appliance-base", "0.0.0", "1", "noarch"),
                    ("olivares-selinux", "1.0.0", "1.fc44", "noarch"))


def throwaway_key(home):
    """A throwaway ed25519 signing key in the GnuPG home HOME, for tests only: (fingerprint, armored public key)."""
    gpg = ["gpg", "--homedir", str(home), "--batch", "--pinentry-mode", "loopback", "--passphrase", ""]
    subprocess.run(gpg + ["--quick-generate-key", "Olivares test only <test@invalid.olivares.ai>", "ed25519", "sign", "0"],
                   check=True, capture_output=True, timeout=120)
    listed = subprocess.run(gpg + ["--with-colons", "--list-secret-keys"], check=True, capture_output=True, text=True,
                            timeout=30).stdout
    fingerprint = next(line.split(":")[9] for line in listed.splitlines() if line.startswith("fpr:"))
    public = subprocess.run(gpg + ["--armor", "--export", fingerprint], check=True, capture_output=True, timeout=30).stdout
    return fingerprint, public


def rpm_bytes(name, version, release, arch, signed=True):
    """A minimal RPM file: the 96-byte lead, a signature header that carries an RSAHEADER (268) entry when SIGNED, and a
    main header naming the package, laid out as rpm writes them (lead, signature header padded to 8, main header)."""
    def header(entries):
        index, store = b"", b""
        for tag, kind, value in entries:
            index += struct.pack(">IIII", tag, kind, len(store), 1 if kind in (6, 7) else len(value))
            store += value if kind == 7 else value.encode() + b"\0"
        return b"\x8e\xad\xe8\x01\0\0\0\0" + struct.pack(">II", len(entries), len(store)) + index + store
    # magic, major 3, minor 0, type 0 (binary), archnum 1, name[66], osnum 1, signature type 5, 16 reserved bytes
    lead = b"\xed\xab\xee\xdb\x03\x00\x00\x00\x00\x01" + name.encode()[:65].ljust(66, b"\0") + b"\x00\x01\x00\x05" \
        + b"\0" * 16
    assert len(lead) == 96
    sig_entries = [(269, 6, "0" * 40)] + ([(268, 7, b"\x89\x01\x00signature")] if signed else [])
    signature = header(sig_entries)
    signature += b"\0" * (-len(signature) % 8)
    main = header([(1000, 6, name), (1001, 6, version), (1002, 6, release), (1022, 6, arch)])
    return lead + signature + main + b"payload"


def signed_delivery(directory, home, fingerprint, public, packages=PRODUCT_PACKAGES, sign=True, signed_rpms=True):
    """An S3-shaped delivery: the RPMs, createrepo_c gzip repodata, repomd.xml.asc signed with the key in HOME, the public key
    as S3 publishes it, and delivery.json in D's shape (olivares.ai/rpm-delivery/v1: key_fingerprint, repomd_sha256,
    repomd_asc_sha256, packages with name, nevra, arch, file, sha256). Returns the manifest."""
    import gzip as gz
    directory = Path(directory)
    (directory / "repodata").mkdir(parents=True)
    rows, listed = [], []
    for name, version, release, arch in packages:
        file = "%s-%s-%s.%s.rpm" % (name, version, release, arch)
        data = rpm_bytes(name, version, release, arch, signed=signed_rpms)
        (directory / file).write_bytes(data)
        digest = hashlib.sha256(data).hexdigest()
        rows.append({"name": name, "nevra": file[:-4], "arch": arch, "file": file, "sha256": digest})
        listed.append('<package type="rpm"><name>%s</name><arch>%s</arch><version epoch="0" ver="%s" rel="%s"/>'
                      '<checksum type="sha256" pkgid="YES">%s</checksum><location href="%s"/></package>'
                      % (name, arch, version, release, digest, file))
    primary = gz.compress(('<?xml version="1.0" encoding="UTF-8"?><metadata xmlns="http://linux.duke.edu/metadata/common" '
                           'packages="%d">%s</metadata>' % (len(listed), "".join(listed))).encode(), mtime=0)
    (directory / "repodata/primary.xml.gz").write_bytes(primary)
    repomd = ('<?xml version="1.0" encoding="UTF-8"?><repomd xmlns="http://linux.duke.edu/metadata/repo"><data type="primary">'
              '<checksum type="sha256">%s</checksum><location href="repodata/primary.xml.gz"/></data></repomd>\n'
              % hashlib.sha256(primary).hexdigest())
    (directory / "repodata/repomd.xml").write_text(repomd)
    if sign:
        subprocess.run(["gpg", "--homedir", str(home), "--batch", "--pinentry-mode", "loopback", "--passphrase", "",
                        "--local-user", fingerprint + "!", "--armor", "--detach-sign", "--output",
                        str(directory / "repodata/repomd.xml.asc"), str(directory / "repodata/repomd.xml")],
                       check=True, capture_output=True, timeout=60)
    (directory / "olivares-package-repository.asc").write_bytes(public)
    signature = directory / "repodata/repomd.xml.asc"
    manifest = {"schema": "olivares.ai/rpm-delivery/v1", "key_fingerprint": fingerprint,
                "repomd_sha256": hashlib.sha256(repomd.encode()).hexdigest(),
                "repomd_asc_sha256": hashlib.sha256(signature.read_bytes() if sign else b"").hexdigest(), "packages": rows}
    (directory / "delivery.json").write_text(json.dumps(manifest, indent=2) + "\n")
    return manifest


class SignedDelivery(unittest.TestCase):
    """delivery_check.py: build.sh's check, before KIWI, of the product repository D's S3 delivers (D, 2026-09-27: signed
    RPM headers, repomd.xml.asc with the approved key, both checks in the recipe)."""

    @classmethod
    def setUpClass(cls):
        cls.home = Path(tempfile.mkdtemp(prefix="g.", dir=os.environ.get("TMPDIR")))
        cls.home.chmod(0o700)
        cls.fingerprint, cls.public = throwaway_key(cls.home)
        cls.other_home = Path(tempfile.mkdtemp(prefix="g.", dir=os.environ.get("TMPDIR")))
        cls.other_home.chmod(0o700)
        cls.other_fingerprint, cls.other_public = throwaway_key(cls.other_home)

    @classmethod
    def tearDownClass(cls):
        for home in (cls.home, cls.other_home):
            subprocess.run(["gpgconf", "--homedir", str(home), "--kill", "all"], capture_output=True, timeout=30)
            shutil.rmtree(home, ignore_errors=True)

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="a2-delivery-")
        self.addCleanup(self.temp.cleanup)
        self.dir = Path(self.temp.name) / "delivery"

    def check(self, expected=None):
        return subprocess.run([sys.executable, str(KIWI / "delivery_check.py"), "--package-dir", str(self.dir),
                               "--expected-fingerprint", expected or self.fingerprint],
                              capture_output=True, text=True, timeout=60)

    def test_a_delivery_signed_with_the_expected_key_is_accepted(self):
        signed_delivery(self.dir, self.home, self.fingerprint, self.public)
        result = self.check()
        self.assertEqual(result.returncode, 0, result.stderr)
        record = json.loads(result.stdout)
        self.assertEqual((record["key_fingerprint"], sorted(p["name"] for p in record["packages"])),
                         (self.fingerprint, ["olivares", "olivares-appliance-base", "olivares-selinux"]))

    def test_a_key_other_than_the_expected_one_is_refused(self):
        signed_delivery(self.dir, self.home, self.fingerprint, self.public)
        result = self.check(expected=self.other_fingerprint)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("fingerprint", result.stderr)

    def test_a_manifest_that_names_another_key_than_its_key_file_is_refused(self):
        signed_delivery(self.dir, self.home, self.fingerprint, self.public)
        manifest = json.loads((self.dir / "delivery.json").read_text())
        manifest["key_fingerprint"] = self.other_fingerprint
        (self.dir / "delivery.json").write_text(json.dumps(manifest))
        self.assertEqual(self.check().returncode, 1)
        self.assertEqual(self.check(expected=self.other_fingerprint).returncode, 1)

    def test_a_delivery_without_the_policy_package_is_refused(self):
        # RECIPE-INTERFACE item 1: olivares-selinux is staged with olivares and olivares-appliance-base.
        signed_delivery(self.dir, self.home, self.fingerprint, self.public, packages=PRODUCT_PACKAGES[:2])
        result = self.check()
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("olivares-selinux", result.stderr)

    def test_a_package_set_other_than_the_two_product_packages_is_refused(self):
        for packages in (PRODUCT_PACKAGES + (("extra", "1", "1", "noarch"),), PRODUCT_PACKAGES[:1]):
            with self.subTest(count=len(packages)):
                shutil.rmtree(self.dir, ignore_errors=True)
                signed_delivery(self.dir, self.home, self.fingerprint, self.public, packages=packages)
                result = self.check()
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn("olivares", result.stderr)

    def test_a_delivery_without_repomd_signature_is_refused(self):
        signed_delivery(self.dir, self.home, self.fingerprint, self.public, sign=False)
        result = self.check()
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("repomd.xml.asc", result.stderr)

    def test_a_signature_by_another_key_or_over_other_bytes_is_refused(self):
        signed_delivery(self.dir, self.home, self.fingerprint, self.public)
        repomd = self.dir / "repodata/repomd.xml"
        repomd.write_text(repomd.read_text() + "<!-- changed after signing -->\n")
        manifest = json.loads((self.dir / "delivery.json").read_text())
        manifest["repomd_sha256"] = hashlib.sha256(repomd.read_bytes()).hexdigest()
        (self.dir / "delivery.json").write_text(json.dumps(manifest))
        self.assertEqual(self.check().returncode, 1)
        shutil.rmtree(self.dir)
        signed_delivery(self.dir, self.other_home, self.other_fingerprint, self.other_public)
        (self.dir / "olivares-package-repository.asc").write_bytes(self.public)
        manifest = json.loads((self.dir / "delivery.json").read_text())
        manifest["key_fingerprint"] = self.fingerprint
        (self.dir / "delivery.json").write_text(json.dumps(manifest))
        self.assertEqual(self.check().returncode, 1, "a repomd signed by a key other than the pinned one")

    def test_a_repomd_or_package_whose_sha256_differs_is_refused(self):
        for target in ("repodata/repomd.xml", "olivares-0.0.0-1.x86_64.rpm"):
            with self.subTest(target=target):
                shutil.rmtree(self.dir, ignore_errors=True)
                signed_delivery(self.dir, self.home, self.fingerprint, self.public)
                manifest = json.loads((self.dir / "delivery.json").read_text())
                if target.endswith(".rpm"):
                    manifest["packages"][0]["sha256"] = "0" * 64
                else:
                    manifest["repomd_sha256"] = "0" * 64
                (self.dir / "delivery.json").write_text(json.dumps(manifest))
                self.assertEqual(self.check().returncode, 1)

    def test_an_unsigned_rpm_is_refused(self):
        signed_delivery(self.dir, self.home, self.fingerprint, self.public, signed_rpms=False)
        result = self.check()
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("signature", result.stderr)

    def test_a_directory_without_a_manifest_cannot_be_checked(self):
        self.dir.mkdir()
        self.assertEqual(self.check().returncode, 2)

    def rewrite(self, change):
        manifest = json.loads((self.dir / "delivery.json").read_text())
        change(manifest)
        (self.dir / "delivery.json").write_text(json.dumps(manifest))

    def test_the_manifest_holds_exactly_ds_fields(self):
        # D's shape (2026-09-27): these fields, no more and no less, at the top and in each package.
        top = ("schema", "key_fingerprint", "repomd_sha256", "repomd_asc_sha256", "packages")
        row = ("name", "nevra", "arch", "file", "sha256")
        changes = [("top without " + f, lambda m, f=f: m.pop(f)) for f in top]
        changes += [("top with an extra field", lambda m: m.update(signed_at="2026-09-27T00:00:00Z"))]
        changes += [("package without " + f, lambda m, f=f: m["packages"][0].pop(f)) for f in row]
        changes += [("package with an extra field", lambda m: m["packages"][1].update(license="AGPL-3.0-only"))]
        for label, change in changes:
            with self.subTest(label):
                shutil.rmtree(self.dir, ignore_errors=True)
                signed_delivery(self.dir, self.home, self.fingerprint, self.public)
                self.rewrite(change)
                result = self.check()
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn("delivery.json", result.stderr)

    def test_the_repomd_signature_digest_is_checked_against_the_file(self):
        signed_delivery(self.dir, self.home, self.fingerprint, self.public)
        self.rewrite(lambda m: m.update(repomd_asc_sha256="0" * 64))
        result = self.check()
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("repomd_asc_sha256", result.stderr)

    def test_a_package_arch_other_than_x86_64_or_noarch_is_refused_by_name(self):
        packages = (("olivares", "0.0.0", "1", "aarch64"),) + PRODUCT_PACKAGES[1:]
        signed_delivery(self.dir, self.home, self.fingerprint, self.public, packages=packages)
        result = self.check()
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("olivares", result.stderr)
        self.assertIn("aarch64", result.stderr)

    def test_a_package_arch_that_disagrees_with_its_package_is_refused(self):
        signed_delivery(self.dir, self.home, self.fingerprint, self.public)
        self.rewrite(lambda m: m["packages"][0].update(arch="noarch" if m["packages"][0]["arch"] == "x86_64" else "x86_64"))
        result = self.check()
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("arch", result.stderr)


# Emulations for qualification-delivery.sh, which runs on the hosted runner from the pinned source root. go builds the
# three binaries and installs nfpm; nfpm writes NAME-VERSION-1.ARCH.rpm from a configuration's name, arch and version, as
# its rpm packager names and maps them (amd64 is x86_64). D's two S3 scripts are emulated so that they really sign: the
# signer gives each RPM a signature header entry, and the renderer writes createrepo_c-shaped gzip metadata with sha256
# checksums and signs repomd.xml with the descriptor's secret key through gpg. D's own test-key generator runs as it is.
FAKE_GO = r"""
import json, os, shutil, sys
args = sys.argv[1:]
with open(os.environ["FAKE_TOOL_LOG"], "a") as log:
    log.write(json.dumps(["go"] + args + [os.environ.get("CGO_ENABLED", "")]) + "\n")
if args[0] == "build":
    out = args[args.index("-o") + 1]
    os.makedirs(os.path.dirname(out) or ".", exist_ok=True)
    open(out, "w").write("inert binary of " + args[-1])
elif args[0] == "install" and args[1] == "github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.47.0":
    os.makedirs(os.environ["GOBIN"], exist_ok=True)
    shutil.copy(os.environ["FAKE_NFPM"], os.path.join(os.environ["GOBIN"], "nfpm"))
    os.chmod(os.path.join(os.environ["GOBIN"], "nfpm"), 0o755)
else:
    sys.exit(2)
"""
FAKE_NFPM = r"""
import json, os, re, struct, sys
@RPM_BYTES@
args = sys.argv[1:]
with open(os.environ["FAKE_TOOL_LOG"], "a") as log:
    log.write(json.dumps(["nfpm"] + args) + "\n")
assert args[0] == "package" and args[args.index("--packager") + 1] == "rpm", args
config = json.loads(re.sub(r"(?m)^#.*$", "", open(args[args.index("--config") + 1]).read()))
expand = lambda value: re.sub(r"\$\{(\w+)\}", lambda m: os.environ[m.group(1)], value)
arch = {"amd64": "x86_64", "all": "noarch"}[expand(config["arch"])]
name, version = config["name"], expand(config["version"])
version = version.replace("-", "~", 1)  # a semver prerelease is an rpm ~ prerelease: 0.0.0-dev is 0.0.0~dev
target = args[args.index("--target") + 1]
open(os.path.join(target, "%s-%s-1.%s.rpm" % (name, version, arch)), "wb").write(rpm_bytes(name, version, "1", arch, False))
"""
FAKE_SIGNING_COMMON = r"""
import gzip, hashlib, json, os, re, stat, struct, subprocess, sys, tempfile
from pathlib import Path
if os.environ.get("OLIVARES_PACKAGE_REPO_TEST_ONLY") != "1":
    sys.exit(1)
descriptor = json.load(open(os.environ["OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE"]))
secret = Path(os.environ["OLIVARES_PACKAGE_REPO_OPENPGP_SECRET_KEY_FILE"])
if stat.S_IMODE(secret.stat().st_mode) & 0o077 or b"PRIVATE KEY BLOCK" not in secret.read_bytes():
    sys.exit(1)
with open(@LOG@, "a") as log:
    log.write(json.dumps({"script": Path(__file__).name, "argv": sys.argv[1:], "environment": descriptor["environment"],
                          "fingerprint": descriptor["openpgp_fingerprint"]}) + "\n")
"""
FAKE_SIGNING_RPM_PAYLOAD_SIGN = FAKE_SIGNING_COMMON + r"""
@RPM_BYTES@
for i, a in enumerate(sys.argv[1:]):
    if a == "--rpm":
        path = Path(sys.argv[i + 2])
        stem, arch = path.name[:-4].rsplit(".", 1)
        name, version, release = stem.rsplit("-", 2)
        path.write_bytes(rpm_bytes(name, version, release, arch, @SIGNED@))
"""
FAKE_SIGNING_RENDER_RPM_REPODATA = FAKE_SIGNING_COMMON + r"""
assert sys.argv[1] == "render", sys.argv
repo = Path(sys.argv[sys.argv.index("--repo") + 1])
listed = []
for rpm in sorted(repo.glob("*.rpm")):
    stem, arch = rpm.name[:-4].rsplit(".", 1)
    name, version, release = stem.rsplit("-", 2)
    listed.append('<package type="rpm"><name>%s</name><arch>%s</arch><version epoch="0" ver="%s" rel="%s"/>'
                  '<checksum type="sha256" pkgid="YES">%s</checksum><location href="%s"/></package>'
                  % (name, arch, version, release, hashlib.sha256(rpm.read_bytes()).hexdigest(), rpm.name))
(repo / "repodata").mkdir()
primary = gzip.compress(('<?xml version="1.0" encoding="UTF-8"?><metadata xmlns="http://linux.duke.edu/metadata/common" '
                         'packages="%d">%s</metadata>' % (len(listed), "".join(listed))).encode(), mtime=0)
(repo / "repodata/primary.xml.gz").write_bytes(primary)
(repo / "repodata/repomd.xml").write_text(
    '<?xml version="1.0" encoding="UTF-8"?><repomd xmlns="http://linux.duke.edu/metadata/repo"><data type="primary">'
    '<checksum type="sha256">%s</checksum><location href="repodata/primary.xml.gz"/></data></repomd>\n'
    % hashlib.sha256(primary).hexdigest())
home = tempfile.mkdtemp(prefix="g.")
gpg = ["gpg", "--homedir", home, "--batch", "--no-tty", "--pinentry-mode", "loopback", "--passphrase", ""]
subprocess.run(gpg + ["--import", str(secret)], check=True, capture_output=True)
subprocess.run(gpg + ["--local-user", descriptor["openpgp_fingerprint"] + "!", "--armor", "--detach-sign", "--output",
                      str(repo / "repodata/repomd.xml.asc"), str(repo / "repodata/repomd.xml")], check=True,
               capture_output=True)
subprocess.run(["gpgconf", "--homedir", home, "--kill", "all"], capture_output=True)
"""


# An emulated docker for qualification-delivery.sh: `build` records its arguments and writes an image id to --iidfile;
# `run` records its arguments and, as rpmbuild -bb of olivares-selinux.spec would, writes the noarch package under the
# directory mounted at /rpmbuild (_topdir).
FAKE_QUALIFICATION_DOCKER = r"""
import json, os, struct, sys
@RPM_BYTES@
args = sys.argv[1:]
with open(os.environ["FAKE_TOOL_LOG"], "a") as log:
    log.write(json.dumps(["docker"] + args) + "\n")
if args[0] == "build":
    open(args[args.index("--iidfile") + 1], "w").write("sha256:" + "d" * 64)
elif args[0] == "run":
    mounts = dict(reversed(v.split(":")[:2]) for i, v in enumerate(args) if i and args[i - 1] == "--volume")
    rpms = os.path.join(mounts["/rpmbuild"], "RPMS", "noarch")
    os.makedirs(rpms)
    open(os.path.join(rpms, "olivares-selinux-1.0.0-1.fc44.noarch.rpm"), "wb").write(
        rpm_bytes("olivares-selinux", "1.0.0", "1.fc44", "noarch", False))
else:
    sys.exit(2)
"""


def qualification_fakes(work, signed=True):
    """PATH fakes (go; nfpm through go install) and an S3 scripts directory (D's real test-key generator from scripts/, and
    the two signing emulations) under WORK. Returns (bin, s3, tool log, S3 call log)."""
    import inspect
    source = "import struct\n" + inspect.getsource(rpm_bytes)
    work = Path(work)
    bin_dir, s3 = work / "fake-bin", work / "s3-scripts"
    for directory in (bin_dir, s3):
        shutil.rmtree(directory, ignore_errors=True)
        directory.mkdir()
    tools, calls = work / "tools.jsonl", work / "s3-calls.jsonl"
    (bin_dir / "go").write_text("#!%s\n%s" % (sys.executable, FAKE_GO))
    (bin_dir / "docker").write_text("#!%s\n%s" % (sys.executable, FAKE_QUALIFICATION_DOCKER.replace("@RPM_BYTES@", source)))
    (work / "fake-nfpm").write_text("#!%s\n%s" % (sys.executable, FAKE_NFPM.replace("@RPM_BYTES@", source)))
    for executable in (bin_dir / "go", bin_dir / "docker", work / "fake-nfpm"):
        executable.chmod(0o755)
    shutil.copy2(ROOT / "scripts/generate-package-repository-test-key.sh", s3 / "generate-package-repository-test-key.sh")
    (s3 / "rpm-payload-sign.py").write_text(FAKE_SIGNING_RPM_PAYLOAD_SIGN.replace("@LOG@", repr(str(calls)))
                                            .replace("@RPM_BYTES@", source).replace("@SIGNED@", repr(signed)))
    (s3 / "render-rpm-repodata.py").write_text(FAKE_SIGNING_RENDER_RPM_REPODATA.replace("@LOG@", repr(str(calls))))
    return bin_dir, s3, tools, calls


class OvaVersion(unittest.TestCase):
    """ova.sh writes VERSION into the OVA envelope: X.Y.Z or 0.0.0-dev (unset: 0.0.0-dev), anything else refused before
    any output (addendum 3)."""

    def test_ova_takes_x_y_z_or_0_0_0_dev(self):
        with tempfile.TemporaryDirectory(prefix="a2-ova-version-") as temp:
            for label, version in [("refused", v) for v in VERSIONS_REFUSED] + [("accepted", v) for v in
                                                                                  VERSIONS_ACCEPTED + (None,)]:
                with self.subTest(**{label: version}):
                    out = Path(tempfile.mkdtemp(dir=temp))
                    env = {k: v for k, v in os.environ.items() if k != "VERSION"}
                    if version is not None:
                        env["VERSION"] = version
                    result = subprocess.run(["bash", str(ROOT / "appliance/images/formats/ova.sh"), "--target-dir",
                                             str(Path(temp) / "no-image"), "--output-dir", str(out)], env=env,
                                            capture_output=True, text=True, timeout=30)
                    self.assertEqual(result.returncode, 2, result.stderr)
                    self.assertEqual(list(out.iterdir()), [])
                    if label == "refused":
                        self.assertIn("VERSION is X.Y.Z or 0.0.0-dev, not '%s'" % version, result.stderr)
                    else:
                        self.assertNotIn("VERSION", result.stderr, "past the rule, the missing image stops it")


class QualificationDelivery(unittest.TestCase):
    """qualification-delivery.sh OUT_DIR, the one entry point image.yml calls for a qualification build (interface §1): the
    two product RPMs from the source's nfpm configurations, signed with a throwaway key through D's S3 scripts, delivery.json
    in D's shape, and the key and its fingerprint. Exit 0 written, 1 refused, 2 could not run."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="a2-qualification-", dir=os.environ.get("TMPDIR"))
        self.addCleanup(self.temp.cleanup)
        self.work = Path(self.temp.name)
        # The pinned source root the entry point runs from: the two nfpm configurations, the policy package's spec and
        # sources, and the recipe's builder it builds that package in.
        self.source = self.work / "source"
        for config in ("packaging/nfpm/olivares-appliance-base.yaml", "appliance/images/kiwi/product-dev.nfpm.yaml",
                       "appliance/selinux/olivares-selinux.spec", "appliance/selinux/olivares.te",
                       "appliance/selinux/olivares.fc", "appliance/selinux/olivares.if",
                       "appliance/images/toolchain/fedora44/Containerfile"):
            (self.source / config).parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(ROOT / config, self.source / config)
        self.out = self.work / "qualification"
        # The entry point runs with this suite's TMPDIR, not one nested below it: GnuPG's socket paths below the entry
        # point's key directory, and below the signing emulation's home, must fit a Unix socket name.
        self.scratch = Path(os.environ.get("TMPDIR") or tempfile.gettempdir())

    def deliver(self, *args, signed=True, **env):
        bin_dir, s3, self.tools, self.calls = qualification_fakes(self.work, signed=signed)
        environment = dict(os.environ, PATH=str(bin_dir) + os.pathsep + os.environ["PATH"], FAKE_TOOL_LOG=str(self.tools),
                           FAKE_NFPM=str(self.work / "fake-nfpm"), S3_SCRIPTS_DIR=str(s3), VERSION="26.10.0",
                           TMPDIR=str(self.scratch))
        environment.update(env)
        environment = {k: v for k, v in environment.items() if v is not None}
        return subprocess.run(["bash", str(KIWI / "qualification-delivery.sh"), *(args or (str(self.out),))],
                              cwd=self.source, capture_output=True, text=True, timeout=180, env=environment)

    def private_directories(self):
        """The entry point's private key directories (mktemp qXXX) now in its TMPDIR."""
        return {p.name for p in self.scratch.iterdir() if re.fullmatch(r"q[0-9A-Za-z]{3}", p.name)}

    def test_the_qualification_delivery_is_one_the_build_accepts(self):
        before = self.private_directories()
        result = self.deliver()
        self.assertEqual(result.returncode, 0, result.stderr[-3000:])
        self.assertEqual(sorted(p.name for p in self.out.iterdir()),
                         ["packages", "qualification-key.asc", "qualification-key.fingerprint"])
        fingerprint = (self.out / "qualification-key.fingerprint").read_text()
        self.assertRegex(fingerprint, r"^[0-9A-F]{40}\n$")
        fingerprint = fingerprint.strip()
        manifest = json.loads((self.out / "packages/delivery.json").read_text())
        self.assertEqual(sorted(manifest), ["key_fingerprint", "packages", "repomd_asc_sha256", "repomd_sha256", "schema"])
        self.assertEqual(manifest["key_fingerprint"], fingerprint)
        self.assertEqual(sorted((p["name"], p["nevra"], p["arch"]) for p in manifest["packages"]),
                         [("olivares", "olivares-26.10.0-1.x86_64", "x86_64"),
                          ("olivares-appliance-base", "olivares-appliance-base-26.10.0-1.x86_64", "x86_64"),
                          ("olivares-selinux", "olivares-selinux-1.0.0-1.fc44.noarch", "noarch")])
        # olivares-selinux is built from its spec in the recipe's own builder (toolchain/fedora44, whose packages the
        # lock pins), by rpmbuild, as the runner's user and without a network.
        docker = [json.loads(line) for line in self.tools.read_text().splitlines() if json.loads(line)[0] == "docker"]
        self.assertEqual([d[1] for d in docker], ["build", "run"])
        build, run = docker
        self.assertEqual((build[build.index("--file") + 1], build[-1]),
                         ("appliance/images/toolchain/fedora44/Containerfile", "appliance/images/toolchain/fedora44"))
        self.assertIn("sha256:" + "d" * 64, run)
        self.assertEqual(run[run.index("--network") + 1], "none")
        self.assertEqual(run[run.index("--user") + 1], "%d:%d" % (os.getuid(), os.getgid()))
        self.assertIn(str(self.source / "appliance/selinux") + ":/sources:ro", run)
        self.assertEqual(run[run.index("sha256:" + "d" * 64) + 1:],
                         ["rpmbuild", "-bb", "--define", "_topdir /rpmbuild", "--define", "_sourcedir /sources",
                          "/sources/olivares-selinux.spec"])
        self.assertEqual((self.out / "qualification-key.asc").read_bytes(),
                         (self.out / "packages/olivares-package-repository.asc").read_bytes())
        checked = subprocess.run([sys.executable, str(KIWI / "delivery_check.py"), "--package-dir", str(self.out / "packages"),
                                  "--expected-fingerprint", fingerprint], capture_output=True, text=True, timeout=60)
        self.assertEqual(checked.returncode, 0, checked.stderr)
        tools = [json.loads(line) for line in self.tools.read_text().splitlines()]
        self.assertEqual(sorted(t[t.index("-o") + 1] for t in tools if t[:2] == ["go", "build"]),
                         ["bin/appliance-answers", "bin/appliance-firstboot", "bin/olivares"])
        self.assertTrue(all(t[-1] == "0" for t in tools if t[:2] == ["go", "build"]), "static binaries, CGO_ENABLED=0")
        self.assertEqual(sorted(t[t.index("--config") + 1] for t in tools if t[0] == "nfpm"),
                         ["appliance/images/kiwi/product-dev.nfpm.yaml", "packaging/nfpm/olivares-appliance-base.yaml"])
        calls = [json.loads(line) for line in self.calls.read_text().splitlines()]
        self.assertEqual([c["script"] for c in calls], ["rpm-payload-sign.py", "render-rpm-repodata.py"])
        self.assertTrue(all(c["environment"] == "test" and c["fingerprint"] == fingerprint for c in calls))
        # The secret half never reaches OUT_DIR and does not outlive the run.
        for path in self.out.rglob("*"):
            if path.is_file():
                self.assertNotIn(b"PRIVATE KEY", path.read_bytes(), path)
        self.assertEqual(self.private_directories() - before, set(), "the private key directory is removed")

    def test_attempt_takes_the_key_file_the_entry_point_writes(self):
        home = Path(tempfile.mkdtemp(prefix="g.", dir=os.environ.get("TMPDIR")))
        home.chmod(0o700)
        self.addCleanup(shutil.rmtree, home, True)
        self.addCleanup(subprocess.run, ["gpgconf", "--homedir", str(home), "--kill", "all"], capture_output=True)
        fingerprint, public = throwaway_key(home)
        key = self.work / "qualification-key.asc"
        key.write_bytes(public)
        callers = FedoraCallers("test_attempt_qualifies_and_builds_on_the_fedora_builder")
        callers.setUp()
        self.addCleanup(callers.temp.cleanup)
        _, build, _ = callers.attempt_calls("fedora44", self.work / "packages", qualification_key=str(key))
        self.assertEqual(build[build.index("--qualification-key") + 1], fingerprint)
        module = load_attempt("recipe_attempt_key_file")
        not_a_key = self.work / "not-a-key.asc"
        not_a_key.write_text("not a key")
        with patch.dict(os.environ, ENV), patch.object(module, "execute_preflight", lambda *a: self.fail("no preflight")):
            self.assertEqual(module.execute(self.work / "evidence", "tcg", self.work / "packages", self.work / "target",
                                            "nonfree", qualification_key=str(not_a_key)), 2)

    def test_it_cannot_run_without_a_new_out_dir_a_version_or_d_s_scripts(self):
        self.assertEqual(self.deliver("").returncode, 2, "no OUT_DIR")
        self.out.mkdir()
        self.assertEqual(self.deliver().returncode, 2, "an existing OUT_DIR")
        self.out.rmdir()
        for version in VERSIONS_REFUSED:
            with self.subTest(refused=version):
                result = self.deliver(VERSION=version)
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertIn("VERSION is X.Y.Z or 0.0.0-dev, not '%s'" % version, result.stderr)
                self.assertFalse(self.out.exists())
        result = self.deliver(S3_SCRIPTS_DIR=str(self.work / "nowhere"))
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertIn("render-rpm-repodata.py", result.stderr)
        self.assertFalse(self.out.exists(), "nothing is written for a run that cannot run")
        elsewhere = subprocess.run(["bash", str(KIWI / "qualification-delivery.sh"), str(self.out)], cwd=self.work,
                                   capture_output=True, text=True, timeout=30)
        self.assertEqual(elsewhere.returncode, 2, "not the pinned source root")

    def test_an_unset_version_is_the_qualification_version_0_0_0_dev(self):
        result = self.deliver(VERSION=None)
        self.assertEqual(result.returncode, 0, result.stderr[-3000:])
        manifest = json.loads((self.out / "packages/delivery.json").read_text())
        self.assertEqual(sorted(p["nevra"] for p in manifest["packages"]),
                         ["olivares-0.0.0~dev-1.x86_64", "olivares-appliance-base-0.0.0~dev-1.x86_64",
                          "olivares-selinux-1.0.0-1.fc44.noarch"])

    def test_a_delivery_its_own_check_refuses_exits_1(self):
        result = self.deliver(signed=False)
        self.assertEqual(result.returncode, 1, result.stderr[-3000:])
        self.assertIn("signature", result.stderr)


class FedoraCallers(unittest.TestCase):
    """The callers of the Fedora recipe move to RPMs: attempt.py and its hosted preflight qualify the builder the base's lock
    names, the image workflow delivers signed RPMs, and Taskfile and the README name the Fedora inputs."""

    QUALIFICATION = "C" * 40

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="a2-callers-")
        self.addCleanup(self.temp.cleanup)
        self.work = Path(self.temp.name)

    def attempt_calls(self, base, packages, **options):
        module = load_attempt("recipe_attempt_" + base)
        calls = []

        def preflight(directory, argv):
            Path(directory).mkdir(parents=True)
            (Path(directory) / "receipt.json").write_text(json.dumps(receipt()))
            calls.append(("preflight", argv))
            return 0

        def run(argv, log, timeout):
            calls.append(("run", argv))
            return 0
        with patch.dict(os.environ, ENV), patch.object(module, "execute_preflight", preflight), \
                patch.object(module, "run_command", run), patch.object(module, "cleanup_containers", lambda *args: True):
            code = module.execute(self.work / ("evidence-" + base), "tcg", packages, self.work / "target", "nonfree",
                                  base=base, **options)
        self.assertEqual(code, 0, calls)
        build = [argv for kind, argv in calls if kind == "run" and any(str(x).endswith("/kiwi/build.sh") for x in argv)]
        release = [argv for kind, argv in calls if kind == "run" and "--release" in argv]
        return calls[0][1], build[0], release[0]

    def attempt_with_version(self, version, name):
        """attempt.py's execute on the Fedora base with VERSION set to VERSION (None: unset): (exit, commands run)."""
        module = load_attempt("recipe_attempt_version_" + name)
        calls = []

        def preflight(directory, argv):
            Path(directory).mkdir(parents=True)
            (Path(directory) / "receipt.json").write_text(json.dumps(receipt()))
            calls.append(argv)
            return 0

        def run(argv, log, timeout):
            calls.append(argv)
            return 0
        with patch.dict(os.environ, ENV), patch.object(module, "execute_preflight", preflight), \
                patch.object(module, "run_command", run), patch.object(module, "cleanup_containers", lambda *args: True):
            os.environ.pop("VERSION", None)
            if version is not None:
                os.environ["VERSION"] = version
            code = module.execute(self.work / ("evidence-" + name), "tcg", self.work / "delivery", self.work / "target",
                                  "nonfree")
        return code, calls

    def test_attempt_takes_x_y_z_or_0_0_0_dev_before_any_container(self):
        for index, version in enumerate(VERSIONS_REFUSED):
            with self.subTest(refused=version):
                code, calls = self.attempt_with_version(version, "refused%d" % index)
                self.assertEqual((code, calls), (2, []), "refused before the preflight builds its container")
        for index, version in enumerate(VERSIONS_ACCEPTED + (None,)):
            with self.subTest(accepted=version):
                code, calls = self.attempt_with_version(version, "accepted%d" % index)
                self.assertEqual(code, 0, calls)
                build = [argv for argv in calls if any(str(x).endswith("/kiwi/build.sh") for x in argv)][0]
                self.assertIn("VERSION=" + (version or "0.0.0-dev"), build, "unset means the qualification default")

    def test_attempt_qualifies_and_builds_on_the_fedora_builder(self):
        packages = self.work / "delivery"
        preflight, build, release = self.attempt_calls("fedora44", packages, qualification_key=self.QUALIFICATION)
        self.assertEqual(preflight[preflight.index("--lock") + 1], str(FEDORA_LOCK))
        self.assertEqual(release[release.index("--lock") + 1], str(FEDORA_LOCK))
        self.assertEqual((build[build.index("--base") + 1], build[build.index("--package-dir") + 1],
                          build[build.index("--qualification-key") + 1]), ("fedora44", str(packages), self.QUALIFICATION))
        self.assertNotIn("--deb-dir", build)

    def test_attempt_carries_the_desktop_edition_to_build_formats_and_guests(self):
        packages = self.work / "delivery"
        module = load_attempt("recipe_attempt_desktop")
        calls = []

        def preflight(directory, argv):
            Path(directory).mkdir(parents=True)
            (Path(directory) / "receipt.json").write_text(json.dumps(receipt()))
            calls.append(("preflight", argv))
            return 0

        def run(argv, log, timeout):
            calls.append(("run", argv))
            return 0
        with patch.dict(os.environ, ENV), patch.object(module, "execute_preflight", preflight), \
                patch.object(module, "run_command", run), patch.object(module, "cleanup_containers", lambda *a: True):
            code = module.execute(self.work / "evidence-desktop", "tcg", packages, self.work / "target",
                                  "nonfree", edition="desktop")
        self.assertEqual(code, 0, calls)
        build = next(argv for kind, argv in calls if kind == "run"
                     and any(str(x).endswith("/kiwi/build.sh") for x in argv))
        self.assertEqual(build[build.index("--edition") + 1], "desktop")
        assemblies = [argv for kind, argv in calls if kind == "run"
                      and any(str(x).endswith("/formats/assemble.sh") for x in argv)]
        self.assertEqual(len(assemblies), 3)
        for argv in assemblies:
            self.assertEqual(argv[argv.index("--edition") + 1], "desktop")
        guests = [argv for kind, argv in calls if kind == "run"
                  and any(str(x).endswith(("/boot-battery.sh", "/nocloud-probe.sh")) for x in argv)]
        self.assertEqual(len(guests), 2)
        for argv in guests:
            self.assertIn("APPLIANCE_EDITION=desktop", argv)
        self.assertEqual(module.execute(self.work / "evidence-desktop-2", "tcg", packages, self.work / "target",
                                        "nonfree", edition="workstation"), 2)
        self.assertEqual(module.execute(self.work / "evidence-desktop-3", "tcg", packages, self.work / "target",
                                        "nonfree", base="debian13", edition="desktop"), 2)

    def test_attempt_keeps_the_debian_builder_for_the_debian_base(self):
        debs = self.work / "debs"
        preflight, build, release = self.attempt_calls("debian13", debs)
        self.assertEqual(preflight[preflight.index("--lock") + 1], str(LOCK))
        self.assertEqual(release[release.index("--lock") + 1], str(LOCK))
        self.assertEqual((build[build.index("--base") + 1], build[build.index("--deb-dir") + 1]), ("debian13", str(debs)))

    def test_deb_dir_is_only_for_the_debian_base(self):
        result = subprocess.run(["bash", str(KIWI / "build.sh"), "--print-plan", "--deb-dir", str(self.work)],
                                capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertEqual(subprocess.run(["bash", str(KIWI / "build.sh"), "--print-plan", "--base", "debian13", "--deb-dir",
                                         str(self.work)], capture_output=True, text=True, timeout=10).returncode, 0)

    def test_the_preflight_builds_the_containerfile_its_lock_names(self):
        import collector
        for lock_path, directory in ((FEDORA_LOCK, "appliance/images/toolchain/fedora44"), (LOCK, "appliance/images/toolchain")):
            with self.subTest(lock=lock_path.parent.name):
                recorded = []

                def command(argv, timeout=None):
                    recorded.append(argv)
                    return Result(code=1, output="", timed_out=False, joined=True)
                work = self.work / ("probe-" + lock_path.parent.name)
                work.mkdir()
                collector.container_probe(json.loads(lock_path.read_text()), work, ATTEMPT, command=command)
                build = recorded[0]
                self.assertEqual((build[build.index("--file") + 1], build[-1]),
                                 (str(ROOT / directory / "Containerfile"), str(ROOT / directory)))

    def test_the_judge_reads_a_fedora_lock_and_admission_refuses_the_debian_builder_for_it(self):
        import judge
        fedora, debian = json.loads(FEDORA_LOCK.read_text()), json.loads(LOCK.read_text())
        self.assertEqual(judge.validate_lock(fedora), [])
        self.assertEqual(judge.validate_lock(debian), [])
        broken = json.loads(FEDORA_LOCK.read_text())
        del broken["distribution"]["repositories"]
        self.assertNotEqual(judge.validate_lock(broken), [])
        # A receipt the Debian builder's preflight wrote names the Debian lock's digest: admission with the Fedora lock refuses.
        record = {"attempt": {"lock_digest": judge.sha256_file(LOCK)}}
        expected = {"lock_digest": judge.sha256_file(FEDORA_LOCK)}
        self.assertIn("admission.mismatch:lock_digest", judge.check_admission(record, expected))

    def test_the_collector_qualifies_the_fedora_builder_by_its_rpm_inventory(self):
        import collector
        lock_bytes = FEDORA_LOCK.read_bytes()
        lock = json.loads(lock_bytes)
        # The builder's provenance hashes the lock file; the collector hashes the canonical dump: they are one form.
        self.assertEqual(lock_bytes, (json.dumps(lock, indent=2, sort_keys=True) + "\n").encode())
        qualification = {"kiwi_version": "11.0.4", "dependency_check": True, "cli_help_check": True, "imports_check": True,
                         "rpm_inventory": ["rpm-0:6.0.2-1.fc44.x86_64"], "python_inventory": ["kiwi==11.0.4"],
                         "build_provenance": {"lock_sha256": hashlib.sha256(lock_bytes).hexdigest()}}
        self.assertTrue(collector.qualified(lock, qualification))
        self.assertFalse(collector.qualified(lock, dict(qualification, rpm_inventory=[])))
        self.assertFalse(collector.qualified(json.loads(LOCK.read_text()), qualification), "Debian needs dpkg_inventory")
        installer = (FEDORA_TOOLCHAIN / "install.py").read_text()
        for needed in ('"report"', "rpm_inventory", "build-provenance.json", "qualification.json"):
            self.assertIn(needed, installer)

    def test_the_workflow_delivers_signed_rpms_with_a_throwaway_key(self):
        import yaml
        steps = yaml.safe_load((ROOT / ".github/workflows/appliance-image.yml").read_text())["jobs"]["image"]["steps"]
        runs = [s.get("run", "") for s in steps]
        package = next(i for i, r in enumerate(runs) if "nfpm" in r)
        attempt = next(i for i, r in enumerate(runs) if "attempt.py" in r and "--release-only" not in r)
        text = runs[package]
        self.assertEqual(text.count("--packager rpm"), 2)
        self.assertNotIn("--packager deb", text)
        for needed in ("OLIVARES_PACKAGE_REPO_TEST_ONLY=1", "scripts/generate-package-repository-test-key.sh",
                       "scripts/rpm-payload-sign.py", "scripts/render-rpm-repodata.py render --repo",
                       "appliance/images/kiwi/write_delivery.py"):
            self.assertIn(needed, text)
        self.assertLess(package, attempt, "S3's signing precedes the build")
        self.assertIn("--package-dir", runs[attempt])
        self.assertIn("--qualification-key", runs[attempt])
        self.assertNotIn("--deb-dir", runs[attempt])
        self.assertIn("createrepo-c", " ".join(runs))

    def test_write_delivery_writes_the_manifest_the_build_accepts(self):
        home = Path(tempfile.mkdtemp(prefix="g.", dir=os.environ.get("TMPDIR")))
        home.chmod(0o700)
        self.addCleanup(shutil.rmtree, home, True)
        self.addCleanup(subprocess.run, ["gpgconf", "--homedir", str(home), "--kill", "all"], capture_output=True)
        fingerprint, public = throwaway_key(home)
        tree = self.work / "rpm"
        expected = signed_delivery(tree, home, fingerprint, public)
        (tree / "delivery.json").unlink()
        (tree / "olivares-package-repository.asc").unlink()
        key = self.work / "public.asc"
        key.write_bytes(public)
        written = subprocess.run([sys.executable, str(KIWI / "write_delivery.py"), "--package-dir", str(tree), "--key-file",
                                  str(key)], capture_output=True, text=True, timeout=60)
        self.assertEqual(written.returncode, 0, written.stderr)
        manifest = json.loads((tree / "delivery.json").read_text())
        self.assertEqual(sorted(manifest), sorted(expected))
        self.assertEqual({k: manifest[k] for k in ("schema", "key_fingerprint", "repomd_sha256", "repomd_asc_sha256")},
                         {k: expected[k] for k in ("schema", "key_fingerprint", "repomd_sha256", "repomd_asc_sha256")})
        self.assertEqual(sorted(manifest["packages"], key=lambda r: r["name"]),
                         sorted(expected["packages"], key=lambda r: r["name"]))
        checked = subprocess.run([sys.executable, str(KIWI / "delivery_check.py"), "--package-dir", str(tree),
                                  "--expected-fingerprint", fingerprint], capture_output=True, text=True, timeout=60)
        self.assertEqual(checked.returncode, 0, checked.stderr)

    def test_taskfile_and_readme_name_the_fedora_inputs(self):
        import yaml
        task = yaml.safe_load((ROOT / "Taskfile.yml").read_text())["tasks"]["appliance:build"]
        command = task["cmds"][1]
        self.assertIn('--base "{{.BASE}}"', command)
        self.assertIn('--package-dir "{{.PACKAGE_DIR}}"', command)
        self.assertNotIn("--deb-dir", command)
        self.assertEqual(task["vars"]["BASE"], '{{.BASE | default "fedora44"}}')
        readme = (ROOT / "appliance/README.md").read_text()
        self.assertIn("profile `fedora44-server-amd64`", readme)
        self.assertNotIn("profile `debian13-server-amd64`\ntoday", readme)


def fedora_metadata(directory, lock, change=None):
    """Fedora 44 metadata as the two pinned trees serve it, holding every package LOCK locks: for each tree a gzip primary
    and the repomd.xml naming its checksum and open-checksum. CHANGE(name, entry) may return a changed entry (or None to
    drop the package). Returns {tree alias: repomd sha256}."""
    import gzip as gz
    directory = Path(directory)
    entries = dict(lock["distribution"]["system_packages"], **lock["image_packages"])
    pins = {}
    for alias, short in (("fedora-44-releases", "releases"), ("fedora-44-updates", "updates")):
        rows = []
        for name, entry in sorted(entries.items()):
            entry = dict(entry)
            if change:
                entry = change(name, entry)
            if entry is None or entry["repo"] != alias:
                continue
            rows.append('<package type="rpm"><name>%s</name><arch>%s</arch><version epoch="%s" ver="%s" rel="%s"/>'
                        '<checksum type="sha256" pkgid="YES">%s</checksum><location href="%s"/><format>'
                        '<rpm:sourcerpm xmlns:rpm="http://linux.duke.edu/metadata/rpm">%s-%s-%s.src.rpm</rpm:sourcerpm>'
                        '</format></package>' % (name, entry["arch"], entry["epoch"] or "0", entry["version"],
                                                 entry["release"], entry["sha256"], entry["href"], name, entry["version"],
                                                 entry["release"]))
        xml = ('<?xml version="1.0" encoding="UTF-8"?><metadata xmlns="http://linux.duke.edu/metadata/common" '
               'packages="%d">%s</metadata>' % (len(rows), "".join(rows))).encode()
        packed = gz.compress(xml, mtime=0)
        (directory / short).mkdir(parents=True)
        (directory / short / "primary.xml.gz").write_bytes(packed)
        repomd = ('<?xml version="1.0" encoding="UTF-8"?><repomd xmlns="http://linux.duke.edu/metadata/repo">'
                  '<data type="primary"><checksum type="sha256">%s</checksum><open-checksum type="sha256">%s</open-checksum>'
                  '<location href="repodata/primary.xml.gz"/></data></repomd>\n'
                  % (hashlib.sha256(packed).hexdigest(), hashlib.sha256(xml).hexdigest()))
        (directory / short / "repomd.xml").write_text(repomd)
        pins[alias] = hashlib.sha256(repomd.encode()).hexdigest()
    return pins


def fedora_source_metadata(directory, lock, change=None, binary_change=None, served=None):
    """The Fedora 44 source trees (releases and updates, source/tree) as they serve the SRPM of every binary LOCK locks
    (after BINARY_CHANGE, as fedora_metadata's CHANGE), named as fedora_metadata's rpm:sourcerpm names them and kept in the
    same tree as the binary; an SRPM the lock pins is served with its pinned sha256. CHANGE(file, entry) may return a
    changed entry or None. SERVED, a dict, receives each served entry by file. Returns {source tree alias: repomd sha256}."""
    import gzip as gz
    directory = Path(directory)
    entries = dict(lock["distribution"]["system_packages"], **lock["image_packages"])
    pins = {}
    for alias, short in (("fedora-44-releases", "releases-source"), ("fedora-44-updates", "updates-source")):
        rows = {}
        for name, entry in sorted(entries.items()):
            entry = dict(entry)
            if binary_change:
                entry = binary_change(name, entry)
            if entry is None or entry["repo"] != alias:
                continue
            file = "%s-%s-%s.src.rpm" % (name, entry["version"], entry["release"])
            pinned = entry.get("srpm") or {}
            epoch = "" if (entry["epoch"] or "0") == "0" else entry["epoch"] + ":"
            source = {"name": name, "epoch": entry["epoch"] or "0", "version": entry["version"], "release": entry["release"],
                      "sha256": pinned.get("sha256") if pinned.get("nevra") == "%s-%s%s-%s.src" % (
                          name, epoch, entry["version"], entry["release"]) else
                      hashlib.sha256(file.encode()).hexdigest(), "href": "Packages/%s/%s" % (name[0].lower(), file)}
            if change:
                source = change(file, source)
            if source is not None and served is not None:
                served[file] = dict(source, repo=alias + "-source")
            if source is not None:
                rows[file] = ('<package type="rpm"><name>%s</name><arch>src</arch><version epoch="%s" ver="%s" rel="%s"/>'
                              '<checksum type="sha256" pkgid="YES">%s</checksum><location href="%s"/></package>'
                              % (source["name"], source["epoch"], source["version"], source["release"], source["sha256"],
                                 source["href"]))
        xml = ('<?xml version="1.0" encoding="UTF-8"?><metadata xmlns="http://linux.duke.edu/metadata/common" '
               'packages="%d">%s</metadata>' % (len(rows), "".join(rows.values()))).encode()
        packed = gz.compress(xml, mtime=0)
        (directory / short).mkdir(parents=True)
        (directory / short / "primary.xml.gz").write_bytes(packed)
        repomd = ('<?xml version="1.0" encoding="UTF-8"?><repomd xmlns="http://linux.duke.edu/metadata/repo">'
                  '<data type="primary"><checksum type="sha256">%s</checksum><open-checksum type="sha256">%s</open-checksum>'
                  '<location href="repodata/primary.xml.gz"/></data></repomd>\n'
                  % (hashlib.sha256(packed).hexdigest(), hashlib.sha256(xml).hexdigest()))
        (directory / short / "repomd.xml").write_text(repomd)
        pins[alias + "-source"] = hashlib.sha256(repomd.encode()).hexdigest()
    return pins


class FedoraRelock(unittest.TestCase):
    """relock-fedora.py: a new lock from the trees' current metadata, after the updates tree moved. It re-verifies every
    hash, never loosens a pin and never overwrites; its outputs replace the lock, bootstrap.sh and the description's pins
    by a reviewed commit."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="a2-relock-")
        self.addCleanup(self.temp.cleanup)
        self.work = Path(self.temp.name)
        self.lock = json.loads(FEDORA_LOCK.read_text())

    def relock(self, change=None, source_change=None):
        pins = fedora_metadata(self.work / "metadata", self.lock, change)
        self.served = {}
        self.source_pins = fedora_source_metadata(self.work / "metadata", self.lock, source_change, change, self.served)
        result = subprocess.run([sys.executable, str(FEDORA_TOOLCHAIN / "relock-fedora.py"), "--metadata",
                                 str(self.work / "metadata"), "--output", str(self.work / "out"), "--utc",
                                 "2026-09-28T00:00:00Z"], capture_output=True, text=True, timeout=120)
        return result, pins

    def test_a_relock_pins_the_current_metadata_and_moves_only_forward(self):
        newer = self.lock["image_packages"]["kernel"]

        def bump(name, entry):
            if name == "kernel":
                entry.update(release=entry["release"].replace("200", "201"), sha256="e" * 64,
                             href=entry["href"].replace("200", "201"))
            return entry
        result, pins = self.relock(bump)
        self.assertEqual(result.returncode, 0, result.stderr)
        out = self.work / "out"
        text = (out / "input-lock.json").read_text()
        lock = json.loads(text)
        self.assertEqual(text, json.dumps(lock, indent=2, sort_keys=True) + "\n")
        self.assertEqual({a: r["repomd_sha256"] for a, r in lock["distribution"]["repositories"].items()}, pins)
        self.assertEqual(lock["distribution"]["key"], self.lock["distribution"]["key"], "the key pin never moves")
        self.assertEqual(lock["recorded_at"], "2026-09-28T00:00:00Z")
        kernel = lock["image_packages"]["kernel"]
        self.assertEqual((kernel["release"], kernel["sha256"]), (newer["release"].replace("200", "201"), "e" * 64))
        self.assertEqual(kernel["nevra"], "kernel-%s-%s.%s" % (kernel["version"], kernel["release"], kernel["arch"]))
        dnf5 = lock["image_packages"]["dnf5"]
        self.assertEqual(dnf5["sha256"], self.lock["image_packages"]["dnf5"]["sha256"])
        # The retained source of each locked RPM: Koji's copy signed with the Fedora 44 key, byte-equal to the tree's
        # (measured for dnf5-5.4.5.0-1.fc44 at 11:51:08Z, r12b-fedora/sources/GETS.tsv; the relock of 2026-09-29 pins
        # dnf5-5.4.6.0-1.fc44, whose downloaded bytes match its pinned sha256).
        self.assertEqual(dnf5["koji_signed"], "https://kojipkgs.fedoraproject.org/packages/dnf5/5.4.6.0/1.fc44/data/signed/"
                                              "6d9f90a6/x86_64/dnf5-5.4.6.0-1.fc44.x86_64.rpm")
        bootstrap = (out / "bootstrap.sh").read_text()
        self.assertIn("printf '%%s  %%s\\n' '%s' input-lock.json" % hashlib.sha256(text.encode()).hexdigest(), bootstrap)
        repositories = json.loads((out / "fedora-repositories.json").read_text())
        self.assertEqual({a: r["repomd_sha256"] for a, r in repositories["repositories"].items()}, pins)

    def test_metadata_that_fails_its_own_checksums_is_refused(self):
        pins = fedora_metadata(self.work / "metadata", self.lock)
        fedora_source_metadata(self.work / "metadata", self.lock)
        primary = self.work / "metadata/updates/primary.xml.gz"
        primary.write_bytes(primary.read_bytes() + b"tampered")
        result = subprocess.run([sys.executable, str(FEDORA_TOOLCHAIN / "relock-fedora.py"), "--metadata",
                                 str(self.work / "metadata"), "--output", str(self.work / "out"), "--utc",
                                 "2026-09-28T00:00:00Z"], capture_output=True, text=True, timeout=120)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertFalse((self.work / "out").exists())

    def test_a_pin_is_never_loosened(self):
        cases = {
            "a locked package gone from the metadata": lambda name, entry: None if name == "dnf5" else entry,
            "an older build than the locked one": lambda name, entry: dict(entry, release="0.fc44") if name == "dnf5"
            else entry,
            "the same NEVRA with other bytes": lambda name, entry: dict(entry, sha256="f" * 64) if name == "dnf5" else entry,
        }
        for label, change in cases.items():
            with self.subTest(case=label):
                shutil.rmtree(self.work / "metadata", ignore_errors=True)
                result, _ = self.relock(change)
                self.assertEqual(result.returncode, 1, label + ": " + result.stderr)
                self.assertFalse((self.work / "out").exists())

    def test_an_existing_output_is_never_overwritten(self):
        (self.work / "out").mkdir()
        result, _ = self.relock()
        self.assertEqual(result.returncode, 2, result.stderr)

    def test_a_new_root_is_added_from_the_same_metadata(self):
        # The metadata also serves a package no lock names yet; --add SECTION=NAME pins it like every other entry.
        served = json.loads(json.dumps(self.lock))
        template = served["image_packages"]["less"]
        served["image_packages"]["newtool"] = dict(template, nevra="newtool-2-1.fc44.x86_64", version="2", release="1.fc44",
                                                   sha256="a" * 64, href="Packages/n/newtool-2-1.fc44.x86_64.rpm",
                                                   sourcerpm="newtool-2-1.fc44.src.rpm", srpm={})
        fedora_metadata(self.work / "metadata", served)
        fedora_source_metadata(self.work / "metadata", served)
        run = lambda *extra: subprocess.run([sys.executable, str(FEDORA_TOOLCHAIN / "relock-fedora.py"), "--metadata",
                                             str(self.work / "metadata"), "--output", str(self.work / "out"), "--utc",
                                             "2026-09-28T00:00:00Z", *extra], capture_output=True, text=True, timeout=120)
        result = run("--add", "system_packages=newtool")
        self.assertEqual(result.returncode, 0, result.stderr)
        added = json.loads((self.work / "out/input-lock.json").read_text())["distribution"]["system_packages"]["newtool"]
        self.assertEqual((added["nevra"], added["sha256"], added["repo"]), ("newtool-2-1.fc44.x86_64", "a" * 64,
                                                                            template["repo"]))
        self.assertEqual(added["srpm"]["nevra"], "newtool-2-1.fc44.src")
        installs = [line.rstrip(" \\") for line in (self.work / "out/bootstrap.sh").read_text().splitlines()]
        self.assertIn("    newtool-2-1.fc44.x86_64", installs)
        for label, extra, code in (("a name no tree serves", ("--add", "system_packages=absent"), 1),
                                   ("a name the lock pins already", ("--add", "system_packages=less"), 2),
                                   ("a section the lock has not", ("--add", "other=newtool"), 2)):
            with self.subTest(label):
                shutil.rmtree(self.work / "out", ignore_errors=True)
                result = run(*extra)
                self.assertEqual(result.returncode, code, result.stderr)
                self.assertFalse((self.work / "out").exists())

    def test_a_relock_records_each_binary_s_source_rpm_and_pins_the_source_trees(self):
        result, _ = self.relock()
        self.assertEqual(result.returncode, 0, result.stderr)
        lock = json.loads((self.work / "out/input-lock.json").read_text())
        repositories = json.loads((self.work / "out/fedora-repositories.json").read_text())
        for pinned in (lock["distribution"]["source_repositories"], repositories["source_repositories"]):
            self.assertEqual({a: r["repomd_sha256"] for a, r in pinned.items()}, self.source_pins)
        self.assertEqual(repositories["source_repositories"]["fedora-44-updates-source"]["baseurl"],
                         "https://dl.fedoraproject.org/pub/fedora/linux/updates/44/Everything/source/tree/")
        for section in (lock["distribution"]["system_packages"], lock["image_packages"]):
            for name, entry in section.items():
                file = "%s-%s-%s.src.rpm" % (name, entry["version"], entry["release"])
                epoch = "" if (entry["epoch"] or "0") == "0" else entry["epoch"] + ":"
                self.assertEqual(entry["srpm"], {
                    "nevra": "%s-%s%s-%s.src" % (name, epoch, entry["version"], entry["release"]),
                    "sha256": self.served[file]["sha256"], "href": "Packages/%s/%s" % (name[0].lower(), file),
                    "repo": entry["repo"] + "-source",
                    "koji_signed": "https://kojipkgs.fedoraproject.org/packages/%s/%s/%s/data/signed/6d9f90a6/src/%s"
                                   % (name, entry["version"], entry["release"], file)}, name)

    def test_a_source_rpm_that_is_missing_or_changed_its_bytes_is_refused(self):
        lock = self.lock
        less = lock["image_packages"]["less"]
        less_srpm = "less-%s-%s.src.rpm" % (less["version"], less["release"])
        for label, change in (("gone", lambda file, entry: None if file == less_srpm else entry),
                              ("other bytes", lambda file, entry: dict(entry, sha256="f" * 64) if file == less_srpm
                               else entry)):
            with self.subTest(label):
                shutil.rmtree(self.work / "metadata", ignore_errors=True)
                shutil.rmtree(self.work / "out", ignore_errors=True)
                # "other bytes": the branch lock pins less's source RPM, and this metadata serves it with other bytes.
                self.assertEqual(lock["image_packages"]["less"]["srpm"]["nevra"], less_srpm[:-4])
                result, _ = self.relock(source_change=change)
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn(less_srpm, result.stderr)
                self.assertFalse((self.work / "out").exists())
        self.lock = lock

    def test_the_branch_lock_names_the_source_rpm_of_every_locked_binary(self):
        lock = self.lock
        self.assertEqual(sorted(lock["distribution"]["source_repositories"]),
                         ["fedora-44-releases-source", "fedora-44-updates-source"])
        pins = json.loads((KIWI / "fedora-repositories.json").read_text())
        self.assertEqual(pins["source_repositories"], lock["distribution"]["source_repositories"])
        for section in (lock["distribution"]["system_packages"], lock["image_packages"]):
            for name, entry in section.items():
                srpm = entry["srpm"]
                self.assertEqual(srpm["nevra"].rsplit(".", 1)[1], "src", name)
                self.assertEqual(re.sub(r"-\d+:", "-", srpm["nevra"]) + ".rpm", entry["sourcerpm"], name)
                self.assertRegex(srpm["sha256"], r"^[0-9a-f]{64}$")
                self.assertTrue(srpm["koji_signed"].endswith("/data/signed/6d9f90a6/src/" + entry["sourcerpm"]), name)
        # Measured (r12c-fedora/sources/SRPM-MEASURE.txt): Koji's signed less-704-4.fc44.src.rpm is the updates source
        # tree's, sha256 df5f0f69, and its RSAHEADER verifies as the Fedora 44 key; Koji's unsigned copy is c8a74a60.
        self.assertEqual(lock["image_packages"]["less"]["srpm"]["sha256"],
                         "df5f0f6912e32530b075d0fdb089c10ddb427c0f6922417a347735a710aa63f3")


# A small closure for the release outputs (release_outputs.py): the image installed less (a locked root, from updates),
# ncurses-libs (a dependency no lock names, from releases), the two product RPMs of the delivery, the in-build dracut RPM
# and a gpg-pubkey entry, as KIWI 11.0.4 lists them from the image's rpm database (system/setup.py
# _export_rpm_package_list: NAME|EPOCH|VERSION|RELEASE|ARCH|DISTURL|LICENSE, sorted; "(none)" where a tag is unset).
RELEASE_VERSION = "26.10.0"
CLOSURE = [
    # name, epoch, version, release, arch, license, tree, source rpm
    ("less", "(none)", "704", "4.fc44", "x86_64", "GPL-3.0-only OR BSD-2-Clause", "fedora-44-updates", "less-704-4.fc44.src.rpm"),
    ("ncurses-libs", "(none)", "6.5", "7.20250614.fc44", "x86_64", "MIT AND X11", "fedora-44-releases",
     "ncurses-6.5-7.20250614.fc44.src.rpm"),
    ("tzdata", "(none)", "2026c", "2.fc44", "noarch", "LicenseRef-Fedora-Public-Domain AND BSD-3-Clause",
     "fedora-44-updates", "tzdata-2026c-2.fc44.src.rpm"),
    ("xz", "1", "5.8.2", "2.fc44", "x86_64", "GPLv2+ and Public Domain", "fedora-44-releases", "xz-5.8.2-2.fc44.src.rpm"),
]


def closure_sha256(name):
    return hashlib.sha256(("binary " + name).encode()).hexdigest()


def release_fixture(work, srpms=None, fedora_key=None):
    """Every input release_outputs.py reads, for CLOSURE, under WORK: the lock (less locked), the pins of the four trees
    and their metadata, the delivery's delivery.json, the build repository's dracut RPM, the source root (a git repository
    with one commit) and KIWI's target directory with its .packages list. SRPMS {file: bytes} are served by the source
    metadata with their own sha256 and kept in WORK/srpms; FEDORA_KEY (fingerprint, armored key) is the lock's key.
    Returns a dict of the paths and values."""
    import gzip as gz
    work = Path(work)
    srpms = srpms or {}
    metadata = work / "metadata"
    pins = {"repositories": {}, "source_repositories": {}}
    trees = {"fedora-44-releases": [], "fedora-44-updates": []}
    sources = {"fedora-44-releases": [], "fedora-44-updates": []}
    for name, epoch, version, release, arch, _, tree, srpm in CLOSURE:
        trees[tree].append('<package type="rpm"><name>%s</name><arch>%s</arch><version epoch="%s" ver="%s" rel="%s"/>'
                           '<checksum type="sha256" pkgid="YES">%s</checksum><location href="Packages/%s/%s-%s-%s.%s.rpm"/>'
                           '<format><rpm:sourcerpm xmlns:rpm="http://linux.duke.edu/metadata/rpm">%s</rpm:sourcerpm>'
                           '</format></package>' % (name, arch, "0" if epoch == "(none)" else epoch, version, release,
                                                     closure_sha256(name), name[0], name, version, release, arch, srpm))
        base, sversion, srelease = srpm[:-len(".src.rpm")].rsplit("-", 2)
        sources[tree].append('<package type="rpm"><name>%s</name><arch>src</arch><version epoch="%s" ver="%s" rel="%s"/>'
                             '<checksum type="sha256" pkgid="YES">%s</checksum><location href="Packages/%s/%s"/></package>'
                             % (base, "0" if epoch == "(none)" else epoch, sversion, srelease,
                                hashlib.sha256(srpms.get(srpm, ("source " + srpm).encode())).hexdigest(), base[0], srpm))
    for kind, rows_of, suffix in (("repositories", trees, ""), ("source_repositories", sources, "-source")):
        for alias, rows in rows_of.items():
            xml = ('<?xml version="1.0" encoding="UTF-8"?><metadata xmlns="http://linux.duke.edu/metadata/common" '
                   'packages="%d">%s</metadata>' % (len(rows), "".join(rows))).encode()
            packed = gz.compress(xml, mtime=0)
            short = alias.split("-")[-1] + suffix
            (metadata / short).mkdir(parents=True)
            (metadata / short / "primary.xml.gz").write_bytes(packed)
            repomd = ('<?xml version="1.0" encoding="UTF-8"?><repomd xmlns="http://linux.duke.edu/metadata/repo">'
                      '<data type="primary"><checksum type="sha256">%s</checksum><open-checksum type="sha256">%s'
                      '</open-checksum><location href="repodata/primary.xml.gz"/></data></repomd>\n'
                      % (hashlib.sha256(packed).hexdigest(), hashlib.sha256(xml).hexdigest()))
            (metadata / short / "repomd.xml").write_text(repomd)
            pins[kind][alias + suffix] = {"baseurl": "https://dl.fedoraproject.org/pub/fedora/linux/%s/44/Everything/%s/"
                                          % ("releases" if "releases" in alias else "updates",
                                             "source/tree" if suffix else "x86_64/os"),
                                          "repomd_sha256": hashlib.sha256(repomd.encode()).hexdigest(),
                                          "primary_sha256": hashlib.sha256(packed).hexdigest()}
    (work / "fedora-repositories.json").write_text(json.dumps(pins, indent=2))
    (work / "srpms").mkdir()
    for file, data in srpms.items():
        (work / "srpms" / file).write_bytes(data)
    key = {"fingerprint": FEDORA_KEY_FINGERPRINT}
    if fedora_key:
        (work / "fedora-key.asc").write_bytes(fedora_key[1])
        key = {"fingerprint": fedora_key[0], "sha256": hashlib.sha256(fedora_key[1]).hexdigest()}
    lock = {"schema": "olivares-appliance-image-toolchain-lock/v1",
            "distribution": {"key": key},
            "image_packages": {"less": {"nevra": "less-704-4.fc44.x86_64", "sha256": closure_sha256("less"),
                                        "sourcerpm": "less-704-4.fc44.src.rpm"}}}
    (work / "input-lock.json").write_text(json.dumps(lock, indent=2, sort_keys=True) + "\n")
    delivery = work / "packages"
    delivery.mkdir()
    products = [("olivares", RELEASE_VERSION, "1", "x86_64", "AGPL-3.0-only"),
                ("olivares-appliance-base", RELEASE_VERSION, "1", "x86_64", "AGPL-3.0-only")]
    rows = []
    for name, version, release, arch, _ in products:
        data = rpm_bytes(name, version, release, arch)
        file = "%s-%s-%s.%s.rpm" % (name, version, release, arch)
        (delivery / file).write_bytes(data)
        rows.append({"name": name, "nevra": file[:-4], "arch": arch, "file": file, "sha256": hashlib.sha256(data).hexdigest()})
    (delivery / "delivery.json").write_text(json.dumps({"schema": "olivares.ai/rpm-delivery/v1", "packages": rows}))
    build = work / "build-packages"
    build.mkdir()
    dracut = rpm_bytes("dracut-kiwi-oem-dump", "10.7.2", "1", "noarch")
    (build / "dracut-kiwi-oem-dump-10.7.2-1.noarch.rpm").write_bytes(dracut)
    source = work / "source"
    (source / "appliance/images/kiwi").mkdir(parents=True)
    (source / "appliance/images/kiwi/config.sh").write_text("# the recipe\n")
    (source / "README.md").write_text("the product\n")
    git = ["git", "-C", str(source), "-c", "user.name=Test", "-c", "user.email=test@invalid.olivares.ai"]
    subprocess.run(["git", "init", "-q", str(source)], check=True)
    subprocess.run(git + ["add", "-A"], check=True)
    subprocess.run(git + ["commit", "-q", "-m", "fixture"], check=True, env=dict(os.environ, GIT_COMMITTER_DATE="2026-09-27T00:00:00Z",
                                                                            GIT_AUTHOR_DATE="2026-09-27T00:00:00Z"))
    commit = subprocess.run(git + ["rev-parse", "HEAD"], check=True, capture_output=True, text=True).stdout.strip()
    tree = subprocess.run(git + ["rev-parse", "HEAD^{tree}"], check=True, capture_output=True, text=True).stdout.strip()
    target = work / "target"
    target.mkdir()
    listed = ["%s|%s|%s|%s|%s|(none)|%s" % (n, e, v, r, a, l) for n, e, v, r, a, l, _, _ in CLOSURE]
    listed += ["%s|(none)|%s|%s|%s|(none)|%s" % (n, v, r, a, l) for n, v, r, a, l in products]
    listed += ["dracut-kiwi-oem-dump|(none)|10.7.2|1|noarch|(none)|GPL-2.0-or-later",
               "gpg-pubkey|(none)|6d9f90a6|6823c31d|(none)|(none)|pubkey"]
    (target / "olivares-appliance.x86_64-1.0.0.packages").write_text("\n".join(sorted(listed)) + "\n")
    return {"work": work, "metadata": metadata, "repositories": work / "fedora-repositories.json",
            "lock": work / "input-lock.json", "delivery": delivery, "build": build, "source": source, "target": target,
            "commit": commit, "tree": tree, "products": rows, "srpms": work / "srpms", "fedora_key": work / "fedora-key.asc",
            "dracut_sha256": hashlib.sha256(dracut).hexdigest()}


def release_outputs(fixture, command, *extra):
    return subprocess.run([sys.executable, str(KIWI / "release_outputs.py"), command, "--target-dir", str(fixture["target"]),
                           "--version", RELEASE_VERSION, "--lock", str(fixture["lock"]), "--repositories",
                           str(fixture["repositories"]), "--metadata", str(fixture["metadata"]), "--delivery",
                           str(fixture["delivery"]), "--build-packages", str(fixture["build"]), "--source-root",
                           str(fixture["source"]), *extra], capture_output=True, text=True, timeout=120)


class ReleaseSbom(unittest.TestCase):
    """release_outputs.py sbom: the SPDX 2.3 SBOM of the image root, from the image's rpm database (KIWI's .packages list)
    and the lock's pinned metadata, the delivery and the build repository: each RPM's NEVRA, license and sha256, and the
    source pin (interface §2; addendum 2 rule 7)."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="a2-sbom-")
        self.addCleanup(self.temp.cleanup)
        self.fixture = release_fixture(self.temp.name)

    def sbom(self):
        name = "olivares-appliance-%s-x86_64.spdx.json" % RELEASE_VERSION
        return json.loads((self.fixture["target"] / name).read_text())

    def test_the_sbom_names_every_installed_rpm_with_its_nevra_license_and_sha256(self):
        result = release_outputs(self.fixture, "sbom")
        self.assertEqual(result.returncode, 0, result.stderr)
        target = self.fixture["target"]
        named = target / ("olivares-appliance-%s-x86_64.spdx.json" % RELEASE_VERSION)
        # The interface's T/sbom.spdx.json, which release.yml copies, is the same bytes.
        self.assertEqual((target / "sbom.spdx.json").read_bytes(), named.read_bytes())
        sbom = self.sbom()
        self.assertEqual((sbom["spdxVersion"], sbom["dataLicense"], sbom["SPDXID"], sbom["name"]),
                         ("SPDX-2.3", "CC0-1.0", "SPDXRef-DOCUMENT", "olivares-appliance-%s-x86_64" % RELEASE_VERSION))
        self.assertRegex(sbom["creationInfo"]["created"], r"^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$")
        self.assertTrue(sbom["documentNamespace"].startswith("https://"))
        lock_sha256 = hashlib.sha256(self.fixture["lock"].read_bytes()).hexdigest()
        for value in (self.fixture["commit"], self.fixture["tree"], lock_sha256):
            self.assertIn(value, sbom["comment"])
        packages = {p["SPDXID"]: p for p in sbom["packages"]}
        # SPDX 2.3 JSON spells the purpose with an underscore (schemas/spdx-schema.json at v2.3, primaryPackagePurpose).
        image = [p for p in sbom["packages"] if p.get("primaryPackagePurpose") == "OPERATING_SYSTEM"]
        self.assertEqual(len(image), 1)
        self.assertEqual((image[0]["name"], image[0]["versionInfo"], image[0]["downloadLocation"]),
                         ("olivares-appliance", RELEASE_VERSION,
                          "git+https://github.com/olivaresai/olivares@" + self.fixture["commit"]))
        rpms = {p["name"]: p for p in sbom["packages"] if p is not image[0]}
        expected = {n: ("%s%s-%s" % ("" if e == "(none)" else e + ":", v, r), closure_sha256(n)) for n, e, v, r, *_ in CLOSURE}
        expected.update({p["name"]: (RELEASE_VERSION + "-1", p["sha256"]) for p in self.fixture["products"]})
        expected["dracut-kiwi-oem-dump"] = ("10.7.2-1", self.fixture["dracut_sha256"])
        self.assertEqual({n: (p["versionInfo"], [(c["algorithm"], c["checksumValue"]) for c in p["checksums"]])
                          for n, p in rpms.items()}, {n: (v, [("SHA256", d)]) for n, (v, d) in expected.items()})
        self.assertEqual(rpms["less"]["licenseDeclared"], "GPL-3.0-only OR BSD-2-Clause")
        self.assertEqual(rpms["olivares"]["licenseDeclared"], "AGPL-3.0-only")
        self.assertEqual([r["referenceLocator"] for r in rpms["xz"]["externalRefs"]],
                         ["pkg:rpm/fedora/xz@5.8.2-2.fc44?arch=x86_64&epoch=1&distro=fedora-44"])
        self.assertIn("less-704-4.fc44.src.rpm", rpms["less"]["sourceInfo"])
        self.assertEqual(rpms["less"]["downloadLocation"], "https://kojipkgs.fedoraproject.org/packages/less/704/4.fc44/"
                                                           "data/signed/6d9f90a6/x86_64/less-704-4.fc44.x86_64.rpm")
        self.assertNotIn("gpg-pubkey", rpms, "a keyring entry is not a package")
        described = [(r["spdxElementId"], r["relationshipType"], r["relatedSpdxElement"]) for r in sbom["relationships"]]
        self.assertIn(("SPDXRef-DOCUMENT", "DESCRIBES", image[0]["SPDXID"]), described)
        self.assertEqual(sorted(t for s_, k, t in described if (s_, k) == (image[0]["SPDXID"], "CONTAINS")),
                         sorted(p["SPDXID"] for p in rpms.values()))
        for package in packages.values():
            self.assertRegex(package["SPDXID"], r"^SPDXRef-[A-Za-z0-9.-]+$")
            self.assertFalse(package["filesAnalyzed"])

    def test_a_license_that_is_no_spdx_expression_is_kept_verbatim_beside_noassertion(self):
        self.assertEqual(release_outputs(self.fixture, "sbom").returncode, 0)
        sbom = self.sbom()
        rpms = {p["name"]: p for p in sbom["packages"]}
        self.assertEqual(rpms["xz"]["licenseDeclared"], "NOASSERTION")
        self.assertIn("GPLv2+ and Public Domain", rpms["xz"]["licenseComments"])
        # A LicenseRef the document uses is defined in it (SPDX 2.3 §10).
        self.assertEqual(rpms["tzdata"]["licenseDeclared"], "LicenseRef-Fedora-Public-Domain AND BSD-3-Clause")
        self.assertEqual([i["licenseId"] for i in sbom["hasExtractedLicensingInfos"]], ["LicenseRef-Fedora-Public-Domain"])

    def test_an_installed_rpm_from_no_pinned_source_is_refused(self):
        listing = next(self.fixture["target"].glob("*.packages"))
        listing.write_text(listing.read_text() + "rogue|(none)|1|1|x86_64|(none)|MIT\n")
        result = release_outputs(self.fixture, "sbom")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("rogue-1-1.x86_64", result.stderr)
        self.assertEqual(list(self.fixture["target"].glob("*.spdx.json")), [])

    def test_a_locked_package_installed_at_another_build_is_refused(self):
        lock = json.loads(self.fixture["lock"].read_text())
        lock["image_packages"]["less"].update(nevra="less-704-5.fc44.x86_64")
        self.fixture["lock"].write_text(json.dumps(lock))
        result = release_outputs(self.fixture, "sbom")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("less", result.stderr)

    def test_metadata_other_than_the_pinned_metadata_is_refused(self):
        repomd = self.fixture["metadata"] / "updates/repomd.xml"
        repomd.write_text(repomd.read_text() + "<!-- moved -->\n")
        result = release_outputs(self.fixture, "sbom")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("fedora-44-updates", result.stderr)

    def test_the_outputs_are_named_from_x_y_z_or_0_0_0_dev_only(self):
        result = release_outputs(self.fixture, "sbom", "--version", "0.0.0-dev")
        self.assertEqual(result.returncode, 0, result.stderr)
        sbom = json.loads((self.fixture["target"] / "olivares-appliance-0.0.0-dev-x86_64.spdx.json").read_text())
        self.assertEqual(sbom["name"], "olivares-appliance-0.0.0-dev-x86_64")
        for version in VERSIONS_REFUSED:
            with self.subTest(refused=version):
                target = Path(tempfile.mkdtemp(dir=self.temp.name))
                shutil.copy2(next(self.fixture["target"].glob("*.packages")), target)
                result = release_outputs(dict(self.fixture, target=target), "sbom", "--version", version)
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertIn("VERSION is X.Y.Z or 0.0.0-dev, not %r" % version, result.stderr)
                self.assertEqual(sorted(p.name for p in target.iterdir()), ["olivares-appliance.x86_64-1.0.0.packages"])

    def test_the_source_pin_is_the_clean_commit_of_the_source_root(self):
        result = subprocess.run([sys.executable, str(KIWI / "release_outputs.py"), "pin", "--source-root",
                                 str(self.fixture["source"])], capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), {"repository": "https://github.com/olivaresai/olivares",
                                                     "commit": self.fixture["commit"], "tree": self.fixture["tree"]})
        (self.fixture["source"] / "README.md").write_text("changed after the commit\n")
        dirty = subprocess.run([sys.executable, str(KIWI / "release_outputs.py"), "pin", "--source-root",
                                str(self.fixture["source"])], capture_output=True, text=True, timeout=30)
        self.assertEqual(dirty.returncode, 2, "a build of uncommitted changes has no source pin")
        self.assertIn("README.md", dirty.stderr)


def signed_srpm(name, version, release, home, fingerprint):
    """A source RPM as Fedora signs one: the lead (type 1, source), a signature header whose RSAHEADER (268) is an OpenPGP
    signature, by FINGERPRINT's key in HOME, over the main header's bytes from its magic (rpm's header digest range), and
    the main header naming the package."""
    def header(entries):
        index, store = b"", b""
        for tag, kind, value in entries:
            index += struct.pack(">IIII", tag, kind, len(store), len(value) if kind == 7 else 1)  # BIN counts bytes
            store += value if kind == 7 else value.encode() + b"\0"
        return b"\x8e\xad\xe8\x01\0\0\0\0" + struct.pack(">II", len(entries), len(store)) + index + store
    main = header([(1000, 6, name), (1001, 6, version), (1002, 6, release), (1022, 6, "x86_64")])
    with tempfile.NamedTemporaryFile(dir=home) as body:
        body.write(main)
        body.flush()
        signature = subprocess.run(["gpg", "--homedir", str(home), "--batch", "--pinentry-mode", "loopback", "--passphrase",
                                    "", "--local-user", fingerprint + "!", "--detach-sign", "--output", "-", body.name],
                                   check=True, capture_output=True, timeout=60).stdout
    lead = b"\xed\xab\xee\xdb\x03\x00\x00\x01\x00\x01" + name.encode()[:65].ljust(66, b"\0") + b"\x00\x01\x00\x05" \
        + b"\0" * 16
    signed = header([(268, 7, signature)])
    signed += b"\0" * (-len(signed) % 8)
    return lead + signed + main + b"source payload of " + name.encode()


def zstd_bytes(data, compress=False):
    """zstd through the host's libzstd.so.1 (one-shot API of zstd.h), for the tests' own reading and tampering."""
    import ctypes
    lib = ctypes.CDLL("libzstd.so.1")
    lib.ZSTD_isError.argtypes = [ctypes.c_size_t]
    if compress:
        lib.ZSTD_compressBound.restype = ctypes.c_size_t
        lib.ZSTD_compressBound.argtypes = [ctypes.c_size_t]
        lib.ZSTD_compress.restype = ctypes.c_size_t
        lib.ZSTD_compress.argtypes = [ctypes.c_char_p, ctypes.c_size_t, ctypes.c_char_p, ctypes.c_size_t, ctypes.c_int]
        out = ctypes.create_string_buffer(lib.ZSTD_compressBound(len(data)))
        size = lib.ZSTD_compress(out, len(out), data, len(data), 3)
        assert not lib.ZSTD_isError(size)
        return out.raw[:size]
    lib.ZSTD_getFrameContentSize.restype = ctypes.c_ulonglong
    lib.ZSTD_getFrameContentSize.argtypes = [ctypes.c_char_p, ctypes.c_size_t]
    lib.ZSTD_decompress.restype = ctypes.c_size_t
    lib.ZSTD_decompress.argtypes = [ctypes.c_char_p, ctypes.c_size_t, ctypes.c_char_p, ctypes.c_size_t]
    capacity = lib.ZSTD_getFrameContentSize(data, len(data))
    if capacity >= 2 ** 63:  # unknown in the frame header (a streamed frame): decompress by the stream API
        spec = importlib.util.spec_from_file_location("relock_for_tests", FEDORA_TOOLCHAIN / "relock-fedora.py")
        relock = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(relock)
        return relock.zstd_ctypes("bundle.tar.zst", data)
    out = ctypes.create_string_buffer(capacity)
    size = lib.ZSTD_decompress(out, capacity, data, len(data))
    assert not lib.ZSTD_isError(size)
    return out.raw[:size]


RECIPE_SCHEMAS = ROOT / "appliance/images/schemas"


class ReleaseBundle(unittest.TestCase):
    """release_outputs.py bundle: the corresponding source bundle published beside the images (interface §2, addendum 2):
    olivares-appliance-VERSION-source.tar.zst, a zstd POSIX tar of regular files with SOURCES.json at its root, and
    source-bundle.json. Every SRPM enters verified by sha256 and the Fedora key; the product and recipe members are
    `git archive` of the pinned commit."""

    @classmethod
    def setUpClass(cls):
        cls.home = Path(tempfile.mkdtemp(prefix="g.", dir=os.environ.get("TMPDIR")))
        cls.home.chmod(0o700)
        cls.fingerprint, cls.public = throwaway_key(cls.home)
        cls.other_home = Path(tempfile.mkdtemp(prefix="g.", dir=os.environ.get("TMPDIR")))
        cls.other_home.chmod(0o700)
        cls.other_fingerprint, _ = throwaway_key(cls.other_home)

    @classmethod
    def tearDownClass(cls):
        for home in (cls.home, cls.other_home):
            subprocess.run(["gpgconf", "--homedir", str(home), "--kill", "all"], capture_output=True, timeout=30)
            shutil.rmtree(home, ignore_errors=True)

    def srpms(self, home=None, fingerprint=None):
        out = {}
        for _, _, _, _, _, _, _, srpm in CLOSURE:
            base, version, release = srpm[:-len(".src.rpm")].rsplit("-", 2)
            out[srpm] = signed_srpm(base, version, release, home or self.home, fingerprint or self.fingerprint)
        return out

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="a2-bundle-")
        self.addCleanup(self.temp.cleanup)

    def bundle(self, fixture, *extra):
        return release_outputs(fixture, "bundle", "--srpms", str(fixture["srpms"]), "--fedora-key", str(fixture["fedora_key"]),
                               *extra)

    def fixture(self, srpms=None):
        return release_fixture(self.temp.name, srpms or self.srpms(), (self.fingerprint, self.public))

    def read(self, fixture):
        import tarfile
        archive = fixture["target"] / ("olivares-appliance-%s-source.tar.zst" % RELEASE_VERSION)
        data = archive.read_bytes()
        members = {}
        with tarfile.open(fileobj=io.BytesIO(zstd_bytes(data)), mode="r:") as tar:
            infos = tar.getmembers()
            for info in infos:
                members[info.name] = (info, tar.extractfile(info).read() if info.isfile() else None)
        return data, infos, members, json.loads((fixture["target"] / "source-bundle.json").read_text())

    def validate(self, schema, document):
        try:
            import jsonschema  # draft 2020-12 support: jsonschema 4.x, on PYTHONPATH
        except ImportError:
            self.skipTest("jsonschema is not importable here; the composition rules above are checked regardless")
        loaded = json.loads((RECIPE_SCHEMAS / schema).read_text())
        jsonschema.Draft202012Validator.check_schema(loaded)
        errors = [e.message for e in jsonschema.Draft202012Validator(loaded).iter_errors(document)]
        self.assertEqual(errors, [], schema)

    def test_the_bundle_follows_the_schemas_and_the_composition_rules(self):
        fixture = self.fixture()
        result = self.bundle(fixture)
        self.assertEqual(result.returncode, 0, result.stderr)
        data, infos, members, descriptor = self.read(fixture)
        name = "olivares-appliance-%s-source.tar.zst" % RELEASE_VERSION
        # Rule 1: regular files only, exactly one root SOURCES.json, every other member listed at its path, nothing else.
        self.assertTrue(all(info.isfile() for info in infos))
        self.assertEqual([n for n in members if "/" not in n], ["SOURCES.json"])
        sources_bytes = members["SOURCES.json"][1]
        sources = json.loads(sources_bytes)
        self.assertEqual(sorted(members), sorted(["SOURCES.json"] + [m["path"] for m in sources["members"]]))
        self.assertNotIn("SOURCES.json", [m["path"] for m in sources["members"]])
        for member in sources["members"]:
            payload = members[member["path"]][1]
            self.assertEqual((len(payload), hashlib.sha256(payload).hexdigest()), (member["size"], member["sha256"]))
        # Rules 2 and 3: the descriptor.
        self.assertEqual(descriptor, {"schema": "olivares.ai/appliance-source-bundle/v1", "name": name, "size": len(data),
                                      "sha256": hashlib.sha256(data).hexdigest(), "members": len(sources["members"]) + 1,
                                      "sources_sha256": hashlib.sha256(sources_bytes).hexdigest()})
        # Rule 4: the product and recipe members are git archive of the pinned commit, with their origins.
        git = ["git", "-C", str(fixture["source"]), "archive", "--format=tar", fixture["commit"]]
        product = subprocess.run(git, check=True, capture_output=True).stdout
        recipe = subprocess.run(git + ["appliance/images"], check=True, capture_output=True).stdout
        self.assertEqual(members["product/olivares.tar"][1], product)
        self.assertEqual(members["recipe/appliance-images.tar"][1], recipe)
        origins = {m["path"]: m["origin"] for m in sources["members"]}
        self.assertEqual((origins["product/olivares.tar"], origins["recipe/appliance-images.tar"]),
                         ("git+https://github.com/olivaresai/olivares@" + fixture["commit"],
                          "git+https://github.com/olivaresai/olivares@%s#appliance/images" % fixture["commit"]))
        # Rule 5: each installed Fedora binary in exactly one srpm member's binaries; the product and build RPMs in none.
        srpms = [m for m in sources["members"] if m["kind"] == "srpm"]
        listed = [b for m in srpms for b in m["binaries"]]
        expected = ["%s-%s%s-%s.%s" % (n, "" if e == "(none)" else e + ":", v, r, a) for n, e, v, r, a, *_ in CLOSURE]
        self.assertEqual(sorted(listed), sorted(expected))
        self.assertEqual({m["path"] for m in srpms}, {"srpms/" + c[-1] for c in CLOSURE})
        self.assertTrue(all(m["signed_by"] == self.fingerprint for m in srpms))
        self.assertTrue(all(m["origin"].startswith("https://kojipkgs.fedoraproject.org/packages/") and
                            "/data/signed/%s/src/" % self.fingerprint[-8:].lower() in m["origin"] for m in srpms))
        # Rule 6: the source pin and the lock are the build's.
        self.assertEqual(sources["source"], {"repository": "https://github.com/olivaresai/olivares",
                                             "commit": fixture["commit"], "tree": fixture["tree"]})
        self.assertEqual(sources["lock_sha256"], hashlib.sha256(fixture["lock"].read_bytes()).hexdigest())
        self.assertEqual(sources["version"], RELEASE_VERSION)
        self.validate("sources.schema.json", sources)
        self.validate("source-bundle.schema.json", descriptor)

    def test_a_qualification_bundle_is_named_0_0_0_dev(self):
        fixture = self.fixture()
        result = self.bundle(fixture, "--version", "0.0.0-dev")
        self.assertEqual(result.returncode, 0, result.stderr)
        name = "olivares-appliance-0.0.0-dev-source.tar.zst"
        self.assertEqual(json.loads((fixture["target"] / "source-bundle.json").read_text())["name"], name)
        import tarfile
        with tarfile.open(fileobj=io.BytesIO(zstd_bytes((fixture["target"] / name).read_bytes())), mode="r:") as tar:
            self.assertEqual(json.loads(tar.extractfile("SOURCES.json").read())["version"], "0.0.0-dev")
        verified = subprocess.run([sys.executable, str(KIWI / "release_outputs.py"), "verify-bundle", "--target-dir",
                                   str(fixture["target"]), "--version", "0.0.0-dev"], capture_output=True, text=True,
                                  timeout=120)
        self.assertEqual(verified.returncode, 0, verified.stderr)

    def check_size(self, size):
        with tempfile.TemporaryDirectory(prefix="a2-size-", dir=os.environ.get("TMPDIR")) as temp:
            archive = Path(temp) / ("olivares-appliance-%s-source.tar.zst" % RELEASE_VERSION)
            with open(archive, "wb") as handle:
                handle.truncate(size)  # sparse: the size without the bytes
            return subprocess.run([sys.executable, str(KIWI / "release_outputs.py"), "check-size", "--file", str(archive)],
                                  capture_output=True, text=True, timeout=30)

    def test_a_bundle_over_2_gib_is_refused_by_name(self):
        # One file of a GitHub release may not pass 2 GiB (formats.json's release_asset_max_bytes, which the image formats
        # use as their ceiling); the Server release uploads the bundle as one.
        text = (ROOT / "appliance/images/formats/formats.json").read_text()
        limit = json.loads("\n".join(line for line in text.splitlines() if not line.lstrip().startswith("//")))[
            "release_asset_max_bytes"]
        self.assertEqual(limit, 2147483648)
        at_limit = self.check_size(2147483648)
        self.assertEqual(at_limit.returncode, 0, at_limit.stderr)
        self.assertIn("2147483648 bytes", at_limit.stdout)
        over = self.check_size(2147483649)
        self.assertEqual(over.returncode, 1, over.stderr)
        self.assertIn("2147483649 bytes, over the release asset limit of 2147483648 bytes (2 GiB)", over.stderr)

    def test_the_size_guard_refuses_before_source_bundle_json(self):
        spec = importlib.util.spec_from_file_location("release_outputs_size", KIWI / "release_outputs.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        fixture = self.fixture()
        argv = ["bundle", "--target-dir", str(fixture["target"]), "--version", RELEASE_VERSION, "--lock", str(fixture["lock"]),
                "--repositories", str(fixture["repositories"]), "--metadata", str(fixture["metadata"]), "--delivery",
                str(fixture["delivery"]), "--build-packages", str(fixture["build"]), "--source-root", str(fixture["source"]),
                "--srpms", str(fixture["srpms"]), "--fedora-key", str(fixture["fedora_key"])]
        name = "olivares-appliance-%s-source.tar.zst" % RELEASE_VERSION

        def run(limit, target):
            target.mkdir()
            shutil.copy2(next(fixture["target"].glob("*.packages")), target)
            stderr = io.StringIO()
            with patch.object(module, "RELEASE_ASSET_MAX_BYTES", limit), contextlib.redirect_stderr(stderr), \
                    contextlib.redirect_stdout(io.StringIO()):
                code = module.main([a if a != str(fixture["target"]) else str(target) for a in argv])
            return code, stderr.getvalue()
        code, _ = run(2147483648, Path(self.temp.name) / "measured")
        self.assertEqual(code, 0)
        size = (Path(self.temp.name) / "measured" / name).stat().st_size
        code, error = run(size - 1, Path(self.temp.name) / "over")
        self.assertEqual(code, 1, error)
        self.assertIn("over the release asset limit of %d bytes" % (size - 1), error)
        self.assertEqual(sorted(p.name for p in (Path(self.temp.name) / "over").iterdir()),
                         ["olivares-appliance.x86_64-1.0.0.packages"], "no bundle and no source-bundle.json stay")
        code, error = run(size, Path(self.temp.name) / "at")
        self.assertEqual(code, 0, error)
        self.assertEqual(json.loads((Path(self.temp.name) / "at/source-bundle.json").read_text())["size"], size)

    def test_the_recipe_schemas_are_the_interface_s_fields(self):
        bundle = json.loads((RECIPE_SCHEMAS / "source-bundle.schema.json").read_text())
        self.assertEqual((bundle["title"], bundle["additionalProperties"], sorted(bundle["required"])),
                         ("olivares.ai/appliance-source-bundle/v1", False,
                          ["members", "name", "schema", "sha256", "size", "sources_sha256"]))
        sources = json.loads((RECIPE_SCHEMAS / "sources.schema.json").read_text())
        self.assertEqual((sources["title"], sorted(sources["required"])),
                         ("olivares.ai/appliance-sources/v1", ["lock_sha256", "members", "schema", "source", "version"]))
        srpm, archive = sources["properties"]["members"]["items"]["oneOf"]
        self.assertEqual(sorted(srpm["required"]), ["binaries", "kind", "origin", "path", "sha256", "signed_by", "size"])
        self.assertEqual(sorted(archive["required"]), ["kind", "origin", "path", "sha256", "size"])

    def test_a_member_whose_sha256_differs_from_sources_json_fails_the_check(self):
        import tarfile
        fixture = self.fixture()
        self.assertEqual(self.bundle(fixture).returncode, 0)
        archive = fixture["target"] / ("olivares-appliance-%s-source.tar.zst" % RELEASE_VERSION)
        _, infos, members, descriptor = self.read(fixture)
        rebuilt = io.BytesIO()
        with tarfile.open(fileobj=rebuilt, mode="w:", format=tarfile.USTAR_FORMAT) as tar:
            for info in infos:
                payload = members[info.name][1]
                if info.name == "recipe/appliance-images.tar":
                    payload = payload + b"\0" * 512  # other bytes than SOURCES.json names
                info.size = len(payload)
                tar.addfile(info, io.BytesIO(payload))
        data = zstd_bytes(rebuilt.getvalue(), compress=True)
        archive.write_bytes(data)
        descriptor.update(size=len(data), sha256=hashlib.sha256(data).hexdigest())
        (fixture["target"] / "source-bundle.json").write_text(json.dumps(descriptor))
        result = subprocess.run([sys.executable, str(KIWI / "release_outputs.py"), "verify-bundle", "--target-dir",
                                 str(fixture["target"]), "--version", RELEASE_VERSION], capture_output=True, text=True,
                                timeout=120)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("recipe/appliance-images.tar", result.stderr)

    def test_an_srpm_whose_sha256_or_signature_does_not_verify_is_refused(self):
        srpms = self.srpms()
        fixture = self.fixture(srpms)
        cached = fixture["srpms"] / "less-704-4.fc44.src.rpm"
        cached.write_bytes(cached.read_bytes() + b"changed")
        result = self.bundle(fixture)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("less-704-4.fc44.src.rpm", result.stderr)
        self.assertEqual(list(fixture["target"].glob("*.tar.zst")), [])
        shutil.rmtree(self.temp.name)
        Path(self.temp.name).mkdir()
        other = dict(srpms, **{"xz-5.8.2-2.fc44.src.rpm": signed_srpm("xz", "5.8.2", "2.fc44", self.other_home,
                                                                      self.other_fingerprint)})
        fixture = self.fixture(other)
        result = self.bundle(fixture)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("xz-5.8.2-2.fc44.src.rpm", result.stderr)
        self.assertIn(self.fingerprint, result.stderr)

    def test_a_fedora_key_other_than_the_locked_one_cannot_verify(self):
        fixture = self.fixture()
        fixture["fedora_key"].write_bytes(self.public + b"\n")
        result = self.bundle(fixture)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("key", result.stderr)


class FedoraProductRepository(unittest.TestCase):
    """The product repository and the in-build dracut repository in config.xml, and the customize script that names their
    pinned keys (olivares-repo-pinned.sh)."""

    def test_both_local_repositories_are_read_with_both_checks_and_a_pinned_key(self):
        repos = {r.get("alias"): r for r in description_root().iter("repository") if in_profile(r, FEDORA)}
        for alias, key in (("olivares-appliance-rpms", "olivares-package-repository.asc"),
                           ("olivares-appliance-build", "olivares-build-key.asc")):
            with self.subTest(alias=alias):
                repo = repos[alias]
                self.assertEqual((repo.get("type"), repo.get("repository_gpgcheck"), repo.get("package_gpgcheck")),
                                 ("rpm-md", "true", "true"))
                self.assertEqual(repo.get("customize"), "olivares-repo-pinned.sh")
                self.assertEqual([k.get("key") for k in repo.iter("signing")], ["file:///description/" + key])
        self.assertEqual([s.get("path") for s in repos["olivares-appliance-build"].iter("source")], ["dir:///build-packages"])

    def test_the_release_key_is_pinned_in_the_recipe(self):
        pin = json.loads((KIWI / "package-repository-key.json").read_text())
        self.assertEqual(pin["release_fingerprint"], RELEASE_KEY_FINGERPRINT)
        self.assertIn("404", pin["basis"])

    def customize(self, work, section, fingerprint, key, extra=""):
        description = work / "description"
        description.mkdir(exist_ok=True)
        shutil.copy2(KIWI / "olivares-repo-pinned.sh", description)
        shutil.copy2(KIWI / "delivery_check.py", description)
        (description / key[0]).write_bytes(key[1])
        (description / "olivares-repositories.json").write_text(json.dumps(
            {section: {"key": key[0], "fingerprint": fingerprint}}))
        repo = work / (section + ".repo")
        repo.write_text("[%s]\nname = %s\nbaseurl = file:///packages\nrepo_gpgcheck = 1\ngpgcheck = 1\n%s\n"
                        % (section, section, extra))
        result = subprocess.run(["bash", "--norc", str(description / "olivares-repo-pinned.sh"), str(repo)],
                                capture_output=True, text=True, timeout=30,
                                env=dict(os.environ, OLIVARES_DESCRIPTION_DIR=str(description)))
        return result, repo.read_text()

    def test_the_customize_script_names_the_pinned_key_and_refuses_anything_else(self):
        home = Path(tempfile.mkdtemp(prefix="g.", dir=os.environ.get("TMPDIR")))
        home.chmod(0o700)
        self.addCleanup(shutil.rmtree, home, True)
        self.addCleanup(subprocess.run, ["gpgconf", "--homedir", str(home), "--kill", "all"], capture_output=True)
        fingerprint, public = throwaway_key(home)
        with tempfile.TemporaryDirectory(prefix="a2-repo-pin-") as temp:
            work = Path(temp)
            result, text = self.customize(work, "olivares-appliance-rpms", fingerprint,
                                          ("olivares-package-repository.asc", public))
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("gpgkey = file:///description/olivares-package-repository.asc\n", text)
            result, _ = self.customize(work, "olivares-appliance-rpms", "0" * 40, ("olivares-package-repository.asc", public))
            self.assertEqual(result.returncode, 1, "a key whose fingerprint is not the pinned one")
            for extra in ("gpgcheck = 0", "repo_gpgcheck = 0", "gpgkey = https://packages.olivares.ai/key.asc"):
                result, _ = self.customize(work, "olivares-appliance-rpms", fingerprint,
                                           ("olivares-package-repository.asc", public), extra=extra)
                self.assertEqual(result.returncode, 1, extra)
            result, _ = self.customize(work, "fedora-44-updates", fingerprint, ("olivares-package-repository.asc", public))
            self.assertEqual(result.returncode, 2, "a repository this script does not pin")


class DracutRpm(unittest.TestCase):
    """The stage step's dracut package on the Fedora builder: the same modules as an RPM at the locked KIWI version. Fedora 44
    packages dracut-kiwi-oem-dump 11.0.2 only (updates primary), not the builder's 11.0.4."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="a2-dracut-rpm-")
        self.addCleanup(self.temp.cleanup)
        self.work = Path(self.temp.name)
        self.root = self.work / "stage"
        self.modules = modules_fixture(self.root / "usr/lib/dracut/modules.d")
        self.out = self.work / "packages"
        self.out.mkdir()
        self.bin = self.work / "bin"
        self.bin.mkdir()
        (self.bin / "rpmbuild").write_text("#!" + sys.executable + "\n" + FAKE_RPMBUILD)
        (self.bin / "rpmbuild").chmod(0o755)
        self.version = json.loads(LOCK.read_text())["toolchain"]["kiwi"]["upstream_version"]
        self.env = dict(os.environ, PATH=str(self.bin) + os.pathsep + os.environ["PATH"],
                        FAKE_RPMBUILD_LOG=str(self.work / "rpmbuild.jsonl"), FAKE_RPMBUILD_SPEC=str(self.work / "spec"))

    def run_packer(self, *args, lock=LOCK):
        return subprocess.run([sys.executable, str(KIWI / "dracut_package.py"), "--lock", str(lock), *args],
                              capture_output=True, text=True, timeout=20, env=self.env)

    def test_the_rpm_carries_the_modules_unchanged_at_the_locked_version(self):
        result = self.run_packer("--format", "rpm", "--root", str(self.root), "--output", str(self.out))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual([p.name for p in self.out.iterdir()], ["dracut-kiwi-oem-dump-%s-1.noarch.rpm" % self.version])
        spec = (self.work / "spec").read_text()
        fields = dict(re.findall(r"(?m)^(Name|Version|Release|BuildArch|Requires|License):\s*(.+)$", spec))
        # KIWI's check after prepare compares the installed module package's version as dotted integers.
        self.assertEqual((fields["Name"], fields["Version"], fields["Release"], fields["BuildArch"], fields["Requires"]),
                         ("dracut-kiwi-oem-dump", self.version, "1", "noarch", "dracut"))
        listed = dict((path, mode) for mode, path in re.findall(r"(?m)^%attr\((0\d{3}),root,root\) (/\S+)$", spec))
        self.assertEqual(listed, {"/usr/lib/dracut/modules.d/" + rel: "0%o" % mode for rel, mode in MODULE_FILES.items()})
        self.assertIn("cp -a %s/usr/lib/dracut/modules.d/. %%{buildroot}/usr/lib/dracut/modules.d/" % self.root, spec)
        call = json.loads((self.work / "rpmbuild.jsonl").read_text().splitlines()[0])
        self.assertEqual(call[0], "-bb")
        defines = dict(call[i + 1].split(" ", 1) for i, a in enumerate(call) if a == "--define")
        self.assertEqual(defines["_rpmdir"], str(self.out))
        self.assertEqual(defines["_buildhost"], "olivares-appliance-builder")

    def test_an_rpm_without_the_installer_modules_is_refused(self):
        (self.modules / "55kiwi-dump/module-setup.sh").unlink()
        result = self.run_packer("--format", "rpm", "--root", str(self.root), "--output", str(self.out))
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(list(self.out.iterdir()), [])
        self.assertFalse((self.work / "rpmbuild.jsonl").exists(), "rpmbuild never runs for a refused tree")

    def test_the_rpm_index_check_reads_createrepo_metadata_at_the_locked_version(self):
        repodata = self.out / "repodata"
        repodata.mkdir()
        cases = ((("dracut-kiwi-oem-dump", self.version, "1", "dracut-kiwi-oem-dump-%s-1.noarch.rpm" % self.version),), 0), \
                ((("dracut-kiwi-oem-dump", "11.0.2", "1.fc44", "d.rpm"),), 1), ((("olivares", "0.0.0", "1", "o.rpm"),), 1)
        for entries, expected in cases:
            with self.subTest(entries=entries):
                repomd, primary = primary_gz(entries)
                (repodata / "repomd.xml").write_text(repomd)
                (repodata / "primary.xml.gz").write_bytes(primary)
                self.assertEqual(self.run_packer("--format", "rpm", "--indexed", str(self.out)).returncode, expected)
        shutil.rmtree(repodata)
        self.assertEqual(self.run_packer("--format", "rpm", "--indexed", str(self.out)).returncode, 2)


# An emulated xorriso for iso.sh: the El Torito report of a hybrid medium, -extract of a path from
# the directory named on the medium file's first line, and -extract_boot_images of the files in the
# directory named on its second line, as the real tool reads the ISO. The real xorriso 1.5.6 names
# the El Torito boot images eltorito_img<N>_bios.img and eltorito_img<N>_uefi.img.
FAKE_XORRISO = r'''
import os, shutil, sys
args = sys.argv[1:]
medium, boot_images = open(args[args.index("-indev") + 1]).read().split("\n")[:2]
if "-report_el_torito" in args:
    print("El Torito boot img :   1  BIOS  y   none  0x0000  0x00      4        2320")
    print("El Torito boot img :   2  UEFI  y   none  0x0000  0x00  25720          20")
    sys.exit(0)
if "-extract_boot_images" in args:
    target = args[args.index("-extract_boot_images") + 1]
    os.makedirs(target, exist_ok=True)
    for name in sorted(os.listdir(boot_images)):
        shutil.copyfile(os.path.join(boot_images, name), os.path.join(target, name))
    sys.exit(0)
if "-extract" in args:
    source, target = args[args.index("-extract") + 1:args.index("-extract") + 3]
    try:
        shutil.copyfile(medium + source, target)
    except OSError:
        sys.exit(5)
    sys.exit(0)
sys.exit(3)
'''


def newc(entries):
    """A newc cpio archive of ENTRIES [(name, bytes)], ended by its trailer, as dracut writes it."""
    out = b""
    for number, (name, data) in enumerate(list(entries) + [("TRAILER!!!", b"")]):
        encoded = name.encode() + b"\0"
        mode = 0o100644 if name != "TRAILER!!!" else 0
        fields = [number + 1, mode, 0, 0, 1, 0, len(data), 0, 0, 0, 0, len(encoded), 0]
        out += b"070701" + b"".join(b"%08X" % f for f in fields) + encoded
        out += b"\0" * (-len(out) % 4) + data
        out += b"\0" * (-len(out) % 4)
    return out


def initrd(modules, compress="gzip", early=False, where="usr/lib/dracut"):
    import gzip as gz
    import lzma
    body = newc([(".", b""), (where + "/modules.txt", "".join(m + "\n" for m in modules).encode()),
                 ("usr/bin/kiwi-dump-image", b"#!/bin/sh\n")])
    packed = {"gzip": lambda b: gz.compress(b, mtime=0), "xz": lzma.compress,
              "zstd": lambda b: b"\x28\xb5\x2f\xfd" + b}[compress](body)
    prefix = newc([("kernel/x86/microcode/AuthenticAMD.bin", b"microcode")]) + b"\0" * 512 if early else b""
    return prefix + packed


def fat12(files):
    """A 1440 KiB FAT12 file system holding FILES {path: bytes}, written as mkdosfs and mcopy write one: an 8.3
    name in upper case, with the flags that say its base or extension is lower case when it is (bootx64.efi), and
    long-name entries before it when its case is mixed (BOOTx64.EFI)."""
    sector, root_entries, fat_sectors, total = 512, 224, 9, 2880
    data_start = 1 + 2 * fat_sectors + root_entries * 32 // sector
    fat, data = {0: 0xFF0, 1: 0xFFF}, {}

    def reserve(size):
        clusters = list(range(max(fat) + 1, max(fat) + 1 + max(1, -(-size // sector))))
        for current, following in zip(clusters, clusters[1:] + [0xFFF]):
            fat[current] = following
        return clusters

    def fill(clusters, content):
        for index, number in enumerate(clusters):
            data[number] = content[index * sector:(index + 1) * sector]
        return clusters[0]

    def entries(name, attr, cluster, size):
        base, _, ext = name.partition(".")
        short = (base.upper().ljust(8) + ext.upper().ljust(3)).encode() if name not in (".", "..") else name.ljust(11).encode()
        mixed = any(part not in (part.upper(), part.lower()) for part in (base, ext))
        flags = 0 if mixed else (0x08 if base != base.upper() else 0) | (0x10 if ext != ext.upper() else 0)
        out = b""
        if mixed:
            checksum = 0
            for byte in short:
                checksum = (((checksum & 1) << 7) + (checksum >> 1) + byte) & 0xFF
            units = name.encode("utf-16-le") + b"\0\0"
            units += b"\xff" * (-len(units) % 26)
            pieces = [units[i:i + 26] for i in range(0, len(units), 26)]
            for number in range(len(pieces), 0, -1):
                piece = pieces[number - 1]
                out += bytes([number | (0x40 if number == len(pieces) else 0)]) + piece[:10] + bytes([0x0F, 0, checksum])
                out += piece[10:22] + b"\0\0" + piece[22:26]
        # attribute, case flags, 6 bytes of creation and access time, cluster high word, write time and date
        # (2026-09-26), cluster low word, size.
        return out + short + struct.pack("<BBB6xHHHHI", attr, flags, 0, cluster >> 16, 0, 0x5D3A, cluster & 0xFFFF, size)

    def directory(tree, parent):
        # A directory's clusters are reserved before its children's, so "." and ".." can name them; the
        # root directory (parent None) lives in its fixed region and its children name it as cluster 0.
        own = None if parent is None else reserve(64 + sum(len(entries(n, 0, 0, 0)) for n in tree))
        records = b"".join(
            entries(n, 0x10, directory(t, own[0] if own else 0), 0) if isinstance(t, dict) else
            entries(n, 0x20, fill(reserve(len(t)), t) if t else 0, len(t)) for n, t in sorted(tree.items()))
        if own is None:
            return records
        return fill(own, entries(".", 0x10, own[0], 0) + entries("..", 0x10, parent, 0) + records)

    tree = {}
    for path, content in files.items():
        node = tree
        for part in path.split("/")[:-1]:
            node = node.setdefault(part, {})
        node[path.split("/")[-1]] = content
    root = directory(tree, None)
    table = bytearray(fat_sectors * sector)
    for number, value in fat.items():
        offset = number * 3 // 2
        pair = int.from_bytes(table[offset:offset + 2], "little")
        pair = (pair & 0x000F) | (value << 4) if number % 2 else (pair & 0xF000) | value
        table[offset:offset + 2] = pair.to_bytes(2, "little")
    boot = bytearray(sector)
    boot[0:3], boot[3:11] = b"\xeb\x3c\x90", b"mkfs.fat"
    struct.pack_into("<HBHBHHBHHHII", boot, 11, sector, 1, 1, 2, root_entries, total, 0xF0, fat_sectors, 18, 2, 0, 0)
    boot[38], boot[43:54], boot[54:62], boot[510:512] = 0x29, b"NO NAME    ", b"FAT12   ", b"\x55\xaa"
    image = bytearray(total * sector)
    image[0:sector] = boot
    image[sector:sector + len(table)] = table
    image[sector + len(table):sector + 2 * len(table)] = table
    image[(1 + 2 * fat_sectors) * sector:(1 + 2 * fat_sectors) * sector + len(root)] = root
    for number, content in data.items():
        start = (data_start + number - 2) * sector
        image[start:start + len(content)] = content
    return bytes(image)


def grub_configuration_path(attributes, directory="grub"):
    """Where the signed Debian GRUB of the disk's UEFI path reads its configuration, for a KIWI 11.0.4 oem <type>.

    GRUB 2.12-9+deb13u2's signed image embeds the prefix /EFI/debian and no config, so it reads EFI/debian/grub.cfg on the
    ESP, where KIWI copies its early script (grub2.py:595-648). That script searches the file system of the boot
    partition when the disk has one, else of the root (builder/disk.py:1671-1686), and loads <boot path>/grub/grub.cfg,
    the boot path being / on a boot partition and /boot otherwise (grub2.py:1197-1232; bootloader/config/base.py:367-403).
    KIWI adds a boot partition only when the type asks for one on a btrfs root (storage/setup.py:212-235). On a btrfs
    root in a subvolume (named @, set as the default: volume_manager/btrfs.py:74-80, 140-152), GRUB 2.12 without the
    SUSE subvolume patches reads paths from the top level, where /boot does not exist: the file is at /@/boot/grub.
    Returns (file system, path as GRUB resolves it, where the file actually is). DIRECTORY is KIWI's grub directory name:
    grub, or grub2 when the root has grub2-install (defaults.py :649-669)."""
    config = "/%s/grub.cfg" % directory
    if attributes.get("bootpartition") == "true":
        return attributes.get("bootfilesystem") or "ext3", config, config
    in_subvolume = attributes.get("filesystem") == "btrfs" and attributes.get("btrfs_root_is_subvolume") == "true"
    return attributes.get("filesystem"), "/boot" + config, ("/@" if in_subvolume else "") + "/boot" + config


class UefiDisk(unittest.TestCase):
    """The disk's UEFI path (the uefi, uefi-secureboot and iso-install-boot guests) reaches its GRUB configuration."""

    def server_type(self):
        import xml.etree.ElementTree as ET
        image = ET.parse(KIWI / "config.xml").getroot()
        types = [t for p in image.iter("preferences") if "debian13-server-amd64" in (p.get("profiles") or "").split(",")
                 for t in p.iter("type")]
        self.assertEqual(len(types), 1)
        return dict(types[0].attrib)

    def test_the_signed_grub_finds_the_configuration_where_it_reads(self):
        # Hosted run 36259628735 stopped all three UEFI disk boots at the `grub>` prompt of GRUB 2.12-9+deb13u2.
        filesystem, reads, holds = grub_configuration_path(self.server_type())
        self.assertEqual(reads, holds, "GRUB reads %s on the %s file system; the configuration is at %s"
                         % (reads, filesystem, holds))
        self.assertIn(filesystem, ("ext2", "ext3", "ext4", "btrfs"))

    def test_a_root_subvolume_without_a_boot_partition_is_refused(self):
        # The control: the layout of 3aa1c30b, which the r10 ESP fixture shows GRUB cannot configure itself from.
        attributes = dict(self.server_type(), filesystem="btrfs", btrfs_root_is_subvolume="true")
        attributes.pop("bootpartition", None)
        _, reads, holds = grub_configuration_path(attributes)
        self.assertNotEqual(reads, holds)

    def test_the_boot_partition_holds_two_kernels_and_their_initrds(self):
        # Kernel updates arrive through unattended-upgrades: the old and the new kernel and initrd live in /boot
        # together until the old one is removed. KIWI's default boot partition is 300 MB (defaults.py:1417-1425).
        attributes = self.server_type()
        if attributes.get("bootpartition") == "true":
            self.assertGreaterEqual(int(attributes.get("bootpartsize", "300")), 1024)


# Debian 13's unit that makes the SSH host keys, as openssh-server 1:10.0p1-7+deb13u4 ships it
# (/usr/lib/systemd/system/sshd-keygen.service; its postinst enables it, WantedBy=ssh.service).
DEBIAN_SSHD_KEYGEN = """[Unit]
Description=Generate sshd host keys on first boot
ConditionFirstBoot=yes
ConditionPathIsReadWrite=/etc/ssh
ConditionPathIsSymbolicLink=!/etc/ssh
Before=ssh.service sshd.service sshd@.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=ssh-keygen -A
"""


def unit_conditions(*texts):
    """The [Unit] conditions and Before= of a unit and its drop-ins, merged as systemd.unit(5) says: settings append in
    file order, and a Condition...= set to the empty string resets the list of conditions of every kind."""
    conditions, before, section = [], [], None
    for text in texts:
        for line in text.splitlines():
            line = line.strip()
            if line.startswith("["):
                section = line
            elif section == "[Unit]" and "=" in line and not line.startswith("#"):
                key, value = line.split("=", 1)
                if key.startswith("Condition"):
                    conditions = [] if value == "" else conditions + [(key, value)]
                elif key == "Before":
                    before += value.split()
    return conditions, before


def condition_holds(key, value, boot):
    """One condition on an appliance boot described by BOOT {first_boot, etc_ssh_writable, host_keys}."""
    negate = value.startswith("!")
    value = value.lstrip("!")
    if key == "ConditionFirstBoot":
        return boot["first_boot"] == (value == "yes")
    if key == "ConditionPathIsReadWrite" and value == "/etc/ssh":
        result = boot["etc_ssh_writable"]
    elif key == "ConditionPathIsSymbolicLink" and value == "/etc/ssh":
        result = False
    elif key == "ConditionPathExistsGlob" and value.startswith("/etc/ssh/ssh_host_"):
        result = boot["host_keys"]
    elif key == "ConditionPathExists" and value.startswith("/etc/ssh/ssh_host_"):
        result = boot["host_keys"]
    else:
        raise AssertionError("a condition this model does not know: %s=%s" % (key, value))
    return result != negate


class SshHostKeys(unittest.TestCase):
    """ssh.service finds its host keys on the booted appliance."""

    DROP_IN = "/etc/systemd/system/sshd-keygen.service.d/"

    def config_sh(self):
        return (KIWI / "config.sh").read_text()

    def drop_ins(self):
        """The drop-ins config.sh writes for sshd-keygen.service: the text of each `cat > FILE <<'X'` heredoc."""
        found, text = [], self.config_sh()
        for m in re.finditer(r"cat > (\S+) <<'(\w+)'\n(.*?)\n\2\n", text, re.S):
            if m.group(1).startswith(self.DROP_IN):
                found.append(m.group(3))
        return found

    def appliance_boot(self, host_keys):
        # config.sh leaves /etc/machine-id empty, which systemd 257 does not count as a first boot (machine-id(5),
        # "First Boot Semantics", rule 4), and removes the image's host keys; cloud-init without a datasource makes none.
        text = self.config_sh()
        self.assertIn(": > /etc/machine-id", text)
        self.assertIn("rm -f /etc/ssh/ssh_host_*", text)
        return {"first_boot": False, "etc_ssh_writable": True, "host_keys": host_keys}

    def runs(self, boot):
        conditions, before = unit_conditions(DEBIAN_SSHD_KEYGEN, *self.drop_ins())
        return all(condition_holds(k, v, boot) for k, v in conditions), before

    def test_the_host_keys_are_made_before_ssh_on_an_appliance_boot(self):
        # Hosted run 36259628735: ssh.service failed in the bios and iso-install guests. Debian's sshd -t exits 1 with
        # "no hostkeys available" (r10/ssh/SSHD-PROBE.txt), and sshd-keygen.service was skipped: not a first boot.
        runs, before = self.runs(self.appliance_boot(host_keys=False))
        self.assertTrue(runs, "sshd-keygen.service is skipped on an appliance boot without host keys")
        self.assertIn("ssh.service", before)

    def test_a_boot_that_has_host_keys_keeps_them(self):
        # The control: keys that exist (made by cloud-init, which orders itself before sshd-keygen.service, or by an
        # earlier boot) are never made again, so the instance identity first boot records does not change.
        runs, _ = self.runs(self.appliance_boot(host_keys=True))
        self.assertFalse(runs)

    def test_the_template_still_counts_no_boot_as_a_first_boot(self):
        # Writing "uninitialized" to /etc/machine-id would make every ConditionFirstBoot unit run, among them Debian's
        # systemd-firstboot.service, which prompts on the console for a root password: an unattended appliance waits.
        self.assertNotIn("uninitialized", self.config_sh())


FEDORA = "fedora44-server-amd64"
DESKTOP = "fedora44-desktop-amd64"
DEBIAN = "debian13-server-amd64"
# The desktop edition's packages, every name measured in the Fedora 44 trees (the relock of
# 2026-09-29 pins each in the toolchain lock, with its tree and source RPM). gdm requires
# gnome-session, gnome-session-wayland-session, gnome-shell and gnome-settings-daemon, so the
# session arrives with it; no package of the GNOME line requires a font package (measured over
# the whole pinned releases primary), so Fedora's own default font set is named. The locale is
# the server profile's glibc-langpack-en, already in the shared set.
DESKTOP_PACKAGES = {
    "gdm": "the display manager; brings gnome-session and gnome-settings-daemon with it",
    "gnome-shell": "GNOME Shell, the desktop",
    "firefox": "the browser",
    "nautilus": "the file browser",
    "ptyxis": "the terminal, Fedora 44's default GNOME terminal (gnome-console is the other option)",
    "default-fonts-core": "Fedora's default font set: no GNOME package requires a font package",
}
# The product's boot and storage layout (platform report §3; the Debian type since F10a a5f0a04c): an expandable oem
# disk, UEFI with Secure Boot and the legacy BIOS path on the same disk, a btrfs root in the subvolume @ with the two data
# volumes beside it, a separate 1024 MB ext4 /boot, the hybrid installer medium and the serial console. A base change
# keeps it; it is never inferred from the distribution.
PRODUCT_LAYOUT = {
    "image": "oem", "filesystem": "btrfs", "firmware": "uefi", "eficsm": "true", "efipartsize": "512",
    "bootpartition": "true", "bootfilesystem": "ext4", "bootpartsize": "1024", "installiso": "true",
    "installboot": "install", "btrfs_root_is_subvolume": "true",
    "kernelcmdline": "console=tty0 console=ttyS0,115200n8 net.ifnames=0 systemd.show_status=1",
}
PRODUCT_VOLUMES = ["var/lib/olivares", "var/lib/olivares-appliance"]
PRODUCT_OEMCONFIG = {"oem-resize": "false", "oem-unattended": "true", "oem-swap": "false", "oem-skip-verify": "false"}


def description_root():
    import xml.etree.ElementTree as ET
    return ET.parse(KIWI / "config.xml").getroot()


def in_profile(element, profile):
    """KIWI's rule: an element without a profiles attribute applies to every profile."""
    return element.get("profiles") is None or profile in element.get("profiles").split(",")


def profile_preferences(image, profile):
    return [p for p in image.iter("preferences") if in_profile(p, profile)]


def profile_type(image, profile):
    types = [t for p in profile_preferences(image, profile) for t in p.iter("type")]
    assert len(types) == 1, "%s declares %d image types" % (profile, len(types))
    return types[0]


class FedoraProfile(unittest.TestCase):
    """Fedora 44 (server/minimal, RPM/DNF) is the shipping profile of the one description (Root c1efc2ee §1)."""

    def test_the_fedora_profile_is_the_shipping_default(self):
        image = description_root()
        profiles = {p.get("name"): p.get("import") for p in image.iter("profile")}
        self.assertIn(FEDORA, profiles)
        # KIWI builds the imported profile when none is named; build.sh names it (FedoraBuild).
        self.assertEqual([name for name, imported in profiles.items() if imported == "true"], [FEDORA])

    def test_the_fedora_profile_installs_with_dnf5_for_release_44(self):
        # KIWI 11.0.4 accepts apk, apt, zypper, dnf4, dnf5, microdnf and pacman (kiwi.rnc :999); Fedora 44's package
        # manager is dnf5 5.4.6.0 (updates primary, relocked 2026-09-29). Without <release-version> KIWI passes --releasever=0 to dnf5
        # (package_manager/base.py :48).
        preferences = profile_preferences(description_root(), FEDORA)
        self.assertEqual([p.get("arch") for p in preferences], ["x86_64"])
        self.assertEqual([e.text for p in preferences for e in p.iter("packagemanager")], ["dnf5"])
        self.assertEqual([e.text for p in preferences for e in p.iter("release-version")], ["44"])
        self.assertEqual([e.text for p in preferences for e in p.iter("rpm-check-signatures")], ["true"])

    def test_the_fedora_profile_keeps_the_product_boot_and_storage_layout(self):
        image = description_root()
        fedora = profile_type(image, FEDORA)
        self.assertEqual({k: fedora.get(k) for k in PRODUCT_LAYOUT}, PRODUCT_LAYOUT)
        self.assertEqual([(s.get("unit"), s.text) for s in fedora.iter("size")], [("G", "12")])
        self.assertEqual({e.tag: e.text for c in fedora.iter("oemconfig") for e in c}, PRODUCT_OEMCONFIG)
        self.assertEqual([v.get("name") for v in fedora.iter("volume")], PRODUCT_VOLUMES)
        # The same layout as the Debian profile's type, attribute for attribute.
        debian = profile_type(image, DEBIAN)
        self.assertEqual({k: debian.get(k) for k in PRODUCT_LAYOUT}, PRODUCT_LAYOUT)

    def test_the_description_states_the_fedora_base_and_no_product_string_names_a_distribution(self):
        # The product's name never contains Debian or Linux (design 10.3), nor Fedora, the Fedora Project's mark; the
        # description states the base as "based on Fedora 44" (recipe_test.go holds the same rule).
        image = description_root()
        self.assertIn("based on Fedora 44", image.find("description/specification").text)
        for value in (image.get("name"), image.get("displayname")):
            for mark in ("debian", "linux", "fedora"):
                self.assertNotIn(mark, value.lower())


# Fedora 44 as read on 2026-09-27 (r12-fedora/sources/GETS.tsv) and relocked on 2026-09-29 (the updates tree moved; the
# relock's --fetch read both trees again and the pins below are what it measured): the repomd.xml of the Everything x86_64
# releases and updates trees, and the Fedora 44 primary key, whose fingerprint fedoraproject.org/security lists and whose
# file fedora-gpg-keys-44-2 (updates primary) ships byte-equal to src.fedoraproject.org's copy.
FEDORA_RELEASES = "https://dl.fedoraproject.org/pub/fedora/linux/releases/44/Everything/x86_64/os/"
FEDORA_UPDATES = "https://dl.fedoraproject.org/pub/fedora/linux/updates/44/Everything/x86_64/"
FEDORA_REPOMD = {"fedora-44-releases": "da3845427d188097f6fd71b417a039bdfb8efefc4f38ca44b5cbb94f95a18991",
                 "fedora-44-updates": "440731252c9b7fe8d43126edc5ded1ad5776ce295ec95d419a2eff2139f67cb3"}
FEDORA_KEY = "/etc/pki/rpm-gpg/RPM-GPG-KEY-fedora-44-primary"
FEDORA_KEY_SHA256 = "93642aec521a1e5e96dd715f7ae0ec0850ebc9de09a94ce03cae5263f26cc18a"
FEDORA_KEY_FINGERPRINT = "36F612DCF27F7D1A48A835E4DBFCF71C6D9F90A6"


class FedoraRepositories(unittest.TestCase):
    """Fedora 44 releases and updates as rpm-md, pinned, and the product's own RPMs from the local repository."""

    def repositories(self, profile):
        return {r.get("alias"): r for r in description_root().iter("repository") if in_profile(r, profile)}

    def test_the_fedora_profile_reads_only_pinned_fedora_44_and_the_local_rpm_repository(self):
        repos = self.repositories(FEDORA)
        self.assertEqual(set(repos), {"fedora-44-releases", "fedora-44-updates", "olivares-appliance-rpms",
                                      "olivares-appliance-build"})
        self.assertEqual({a: r.get("type") for a, r in repos.items()}, dict.fromkeys(repos, "rpm-md"))
        sources = {a: [s.get("path") for s in r.iter("source")] for a, r in repos.items()}
        self.assertEqual(sources, {"fedora-44-releases": [FEDORA_RELEASES], "fedora-44-updates": [FEDORA_UPDATES],
                                   "olivares-appliance-rpms": ["dir:///packages"],
                                   "olivares-appliance-build": ["dir:///build-packages"]})
        for alias in ("fedora-44-releases", "fedora-44-updates"):
            with self.subTest(alias=alias):
                repo = repos[alias]
                # Every package is checked against the Fedora 44 key; the metadata has no signature to check
                # (repodata/repomd.xml.asc is 404 on both trees), so its pinned sha256 is what binds it.
                self.assertEqual((repo.get("package_gpgcheck"), repo.get("repository_gpgcheck")), ("true", "false"))
                self.assertEqual(repo.get("customize"), "fedora-repo-pinned.sh")
                self.assertEqual([k.get("key") for k in repo.iter("signing")], ["file://" + FEDORA_KEY])
                # The image's own update channel is the host slice's decision: the build repositories stay out of it.
                self.assertNotEqual(repo.get("imageinclude"), "true")
        # The local repositories keep both checks (D, 2026-09-27; FedoraProductRepository).
        for alias in ("olivares-appliance-rpms", "olivares-appliance-build"):
            local = repos[alias]
            self.assertEqual((local.get("package_gpgcheck"), local.get("repository_gpgcheck")), ("true", "true"), alias)

    def test_the_debian_repositories_stay_with_the_debian_profile(self):
        fedora, debian = self.repositories(FEDORA), self.repositories(DEBIAN)
        self.assertEqual([a for a, r in fedora.items() if r.get("type") in ("apt-deb", "deb-dir")], [])
        self.assertEqual({a: r.get("type") for a, r in debian.items()},
                         {"debian-trixie": "apt-deb", "debian-trixie-updates": "apt-deb",
                          "debian-trixie-security": "apt-deb", "olivares-appliance-packages": "deb-dir"})

    def test_the_pins_are_the_measured_metadata_and_the_fedora_44_key(self):
        pins = json.loads((KIWI / "fedora-repositories.json").read_text())
        self.assertEqual({a: p["repomd_sha256"] for a, p in pins["repositories"].items()}, FEDORA_REPOMD)
        self.assertEqual({a: p["baseurl"] for a, p in pins["repositories"].items()},
                         {"fedora-44-releases": FEDORA_RELEASES, "fedora-44-updates": FEDORA_UPDATES})
        self.assertEqual((pins["key"]["path"], pins["key"]["sha256"], pins["key"]["fingerprint"]),
                         (FEDORA_KEY, FEDORA_KEY_SHA256, FEDORA_KEY_FINGERPRINT))
        for alias, pin in pins["repositories"].items():
            self.assertRegex(pin["recorded_at"], r"^2026-09-29T\d\d:\d\d:\d\dZ$",
                             alias + ": the relock of 2026-09-29 read both trees again")

    def test_build_stages_the_pin_check_beside_the_description(self):
        self.assertIn('cp "$here/fedora-repo-pinned.sh" "$here/fedora-repositories.json" "$stage/"',
                      (KIWI / "build.sh").read_text())


class DesktopProfile(unittest.TestCase):
    """The desktop edition is a profile of the ONE description (A5): the server profile's type,
    layout, package manager, repositories and SELinux policy, the server package set plus GNOME,
    and its own overlay. The server profile's set and behavior are unchanged by it."""

    def test_the_desktop_profile_is_declared_and_not_the_default(self):
        profiles = {p.get("name"): p.get("import") for p in description_root().iter("profile")}
        self.assertIn(DESKTOP, profiles)
        self.assertIsNone(profiles[DESKTOP])
        self.assertEqual([name for name, imported in profiles.items() if imported == "true"], [FEDORA],
                         "the server profile stays the one a build takes when none is named")

    def test_the_desktop_profile_keeps_the_type_layout_and_repositories(self):
        image = description_root()
        desktop = profile_type(image, DESKTOP)
        self.assertEqual({k: desktop.get(k) for k in PRODUCT_LAYOUT}, PRODUCT_LAYOUT)
        self.assertEqual(desktop.get("selinux_policy"), "targeted")
        self.assertEqual([(s.get("unit"), s.text) for s in desktop.iter("size")], [("G", "12")])
        self.assertEqual({e.tag: e.text for c in desktop.iter("oemconfig") for e in c}, PRODUCT_OEMCONFIG)
        self.assertEqual([v.get("name") for v in desktop.iter("volume")], PRODUCT_VOLUMES)
        preferences = profile_preferences(image, DESKTOP)
        self.assertEqual([p.get("arch") for p in preferences], ["x86_64"])
        self.assertEqual([e.text for p in preferences for e in p.iter("packagemanager")], ["dnf5"])
        self.assertEqual([e.text for p in preferences for e in p.iter("release-version")], ["44"])
        self.assertEqual([e.text for p in preferences for e in p.iter("rpm-check-signatures")], ["true"])
        # The same repositories as the server profile reads, attribute for attribute.
        def repositories(profile):
            return {r.get("alias"): {"type": r.get("type"), "repository_gpgcheck": r.get("repository_gpgcheck"),
                                     "package_gpgcheck": r.get("package_gpgcheck"), "customize": r.get("customize"),
                                     "imageinclude": r.get("imageinclude"),
                                     "sources": [s.get("path") for s in r.iter("source")],
                                     "keys": [k.get("key") for k in r.iter("signing")]}
                    for r in description_root().iter("repository") if in_profile(r, profile)}
        self.assertEqual(repositories(DESKTOP), repositories(FEDORA))

    def test_the_server_package_set_is_unchanged_and_the_desktop_adds_its_own(self):
        image = description_root()
        server = profile_packages(image, "image", FEDORA)
        desktop = profile_packages(image, "image", DESKTOP)
        self.assertEqual(len(desktop), len(set(desktop)), "a package is named twice")
        self.assertEqual(set(desktop) - set(server), set(DESKTOP_PACKAGES))
        self.assertEqual(set(server) - set(desktop), set(), "the desktop profile removes nothing of the server's")
        self.assertEqual(set(profile_packages(image, "bootstrap", DESKTOP)),
                         set(profile_packages(image, "bootstrap", FEDORA)))
        self.assertEqual((set(desktop) | set(profile_packages(image, "bootstrap", DESKTOP))) & NOT_ON_FEDORA, set())

    def test_the_desktop_packages_are_pinned_in_the_lock(self):
        # The relock of 2026-09-29 added each desktop name to image_packages, with its NEVRA,
        # sha256, tree and source RPM; the shape test of the toolchain holds the common rule.
        lock = json.loads(FEDORA_LOCK.read_text())
        for name in DESKTOP_PACKAGES:
            with self.subTest(name=name):
                self.assertIn(name, lock["image_packages"])
                self.assertRegex(lock["image_packages"][name]["nevra"], r"\.(x86_64|noarch)$")
                self.assertRegex(lock["image_packages"][name]["sha256"], r"^[0-9a-f]{64}$")
                self.assertRegex(lock["image_packages"][name]["srpm"]["nevra"], r"\.src$")

    def test_the_readme_documents_both_variants(self):
        readme = (ROOT / "appliance/README.md").read_text()
        self.assertIn("`fedora44-server-amd64`", readme)
        self.assertIn("`fedora44-desktop-amd64`", readme)
        for what in ("| Server (", "| Desktop (", "Ptyxis", "Nautilus", "SFTP"):
            with self.subTest(what=what):
                self.assertIn(what, readme)


class DesktopEnablement(unittest.TestCase):
    """The desktop overlay: the server overlay's files plus the preset lines that declare the
    enablements the desktop exists for. The enablement itself is made by config.sh inside the
    image root - the graphical default target, gdm as the display manager, sshd for SSH/SFTP,
    each the link systemctl enable or set-default would create - because the overlay ships no
    symlinks (an absolute link's target lives on the installed system, and the public export
    copies the tree's files). config.sh reads its own links back, exactly, and images.sh holds
    the units' presence; every check fails the build."""

    PRESET = "etc/systemd/system-preset/10-olivares-appliance.preset"
    LINKS = (("etc/systemd/system/default.target", "/usr/lib/systemd/system/graphical.target"),
             ("etc/systemd/system/display-manager.service", "/usr/lib/systemd/system/gdm.service"),
             ("etc/systemd/system/multi-user.target.wants/sshd.service", "/usr/lib/systemd/system/sshd.service"))

    def preset_lines(self, profile):
        return [l.strip() for l in (KIWI / profile / self.PRESET).read_text().splitlines()
                if l.strip() and not l.startswith("#")]

    def test_the_shared_files_are_the_server_overlays_bytes(self):
        for rel in ("etc/yum.repos.d/olivares.repo", "etc/dnf/libdnf5.conf.d/20-olivares-appliance.conf"):
            with self.subTest(rel=rel):
                self.assertEqual((KIWI / DESKTOP / rel).read_bytes(), (KIWI / FEDORA / rel).read_bytes())

    def test_the_preset_is_the_servers_plus_the_two_enables(self):
        server = self.preset_lines(FEDORA)
        desktop = self.preset_lines(DESKTOP)
        self.assertEqual(desktop[:len(server)], server)
        self.assertEqual(desktop[len(server):], ["enable gdm.service", "enable sshd.service"])

    def test_no_profile_ships_a_symlink_anywhere_under_appliance(self):
        # The export copies files; a link's absolute target lives on the installed machine, not
        # in the tree, so the recipe creates every enablement link inside the image root.
        for path in ROOT.joinpath("appliance").rglob("*"):
            self.assertFalse(path.is_symlink(), str(path))

    def test_config_sh_makes_the_enablement_links_and_points_them_exactly(self):
        root = self.root()
        result = run_config_sh(root, [DESKTOP], programs=["setfiles"])
        self.assertEqual(result.returncode, 0, result.stderr[-2000:])
        for rel, target in self.LINKS:
            with self.subTest(rel=rel):
                link = root / rel
                self.assertTrue(link.is_symlink(), rel)
                self.assertEqual(os.readlink(link), target)

    def root(self):
        root = Path(tempfile.mkdtemp(prefix="a2-desktop-", dir=os.environ.get("TMPDIR")))
        contexts = root / "etc/selinux/targeted/contexts/files/file_contexts"
        contexts.parent.mkdir(parents=True)
        contexts.write_text("/.* system_u:object_r:default_t:s0\n")
        return root

    def rerun(self, root, profiles):
        """A second config.sh run on a root whose first run consumed the per-build key record:
        the record is what build.sh stages, so it is staged again before the run."""
        (root / BUILD_KEY_RECORD).parent.mkdir(parents=True, exist_ok=True)
        (root / BUILD_KEY_RECORD).write_text(PER_BUILD_FINGERPRINT + "\n")
        return run_config_sh(root, profiles, programs=["setfiles"])

    def test_config_sh_fails_when_it_no_longer_makes_an_enablement(self):
        # The fail-closed control of the new shape: config.sh both makes the links and reads
        # them back, so the mutant that matters is a config.sh that stopped making one - the
        # read-back must fail the build, not ship a desktop without its enablements.
        script = (KIWI / "config.sh").read_text()
        for rel, target in self.LINKS:
            with self.subTest(rel=rel):
                line = "ln -sfn %s %s" % (target, "/" + rel)
                assert line in script, line
                with tempfile.TemporaryDirectory(prefix="a2-desktop-mutant-", dir=os.environ.get("TMPDIR")) as temp:
                    shutil.copytree(KIWI, Path(temp) / "kiwi", dirs_exist_ok=True, symlinks=True)
                    mutant = Path(temp) / "kiwi" / "config.sh"
                    mutant.write_text(script.replace(line, ":"))
                    root = self.root()
                    result = run_config_sh(root, [DESKTOP], programs=["setfiles"], script=mutant)
                    self.assertNotEqual(result.returncode, 0, rel)
                    self.assertIn(rel, result.stderr)

    def test_config_sh_fails_a_preset_that_does_not_declare_the_units(self):
        for unit in ("gdm.service", "sshd.service"):
            with self.subTest(unit=unit):
                root = self.root()
                self.assertEqual(run_config_sh(root, [DESKTOP], programs=["setfiles"]).returncode, 0)
                preset = root / self.PRESET
                preset.write_text("".join(line + "\n" for line in preset.read_text().splitlines()
                                          if line != "enable " + unit))
                result = self.rerun(root, [DESKTOP])
                self.assertNotEqual(result.returncode, 0, unit)
                self.assertIn(unit, result.stderr)

    def test_images_sh_requires_the_enabled_units_in_the_image(self):
        def labeled_root(units):
            root = self.root()
            for path in LABELED_PATHS[:2] + tuple("/usr/lib/systemd/system/" + u for u in units):
                (root / path.lstrip("/")).parent.mkdir(parents=True, exist_ok=True)
                (root / path.lstrip("/")).write_text("")
            for path in LABELED_PATHS[2:]:
                (root / path.lstrip("/")).mkdir(parents=True)
            return root
        for units, expected in ((("gdm.service", "sshd.service"), 0), (("sshd.service",), 1), ((), 1)):
            with self.subTest(units=units):
                result = run_images_sh(labeled_root(units), [DESKTOP])
                self.assertEqual(result.returncode, expected,
                                 result.stderr[-1000:] if expected else result.stderr[-2000:])
                if expected:
                    self.assertIn("desktop overlay enables", result.stderr)
        # The server profile's image carries no such requirement.
        result = run_images_sh(labeled_root(()), [FEDORA])
        self.assertEqual(result.returncode, 0, result.stderr[-2000:])


# The image packages of the Debian profile mapped to Fedora 44 (each Fedora name resolves in the pinned releases or updates
# primary: r12-fedora/PACKAGE-MAP.tsv gives its NEVRA and sha256). None: nothing on Fedora, for the reason given.
PACKAGE_MAP = [
    # (Debian 13, Fedora 44, reason)
    ("linux-image-amd64", "kernel", "Fedora's kernel, which installs kernel-core and its modules"),
    ("systemd", "systemd", "systemd 259.9 as PID 1"),
    ("systemd-sysv", None, "Fedora's systemd itself provides /usr/sbin/init"),
    (None, "systemd-udev", "Fedora ships udev apart from systemd; Debian's systemd pulls udev"),
    ("systemd-timesyncd", "chrony", "Fedora 44 builds no systemd-timesyncd; chrony is its time service"),
    ("dbus", "dbus-broker", "Fedora's D-Bus message bus"),
    ("dracut", "dracut", "the initrd generator, dracut 108 on Fedora 44"),
    ("dracut-core", None, "Fedora's dracut is one package"),
    ("locales", "glibc-langpack-en", "the en_US locale the description names"),
    ("tzdata", "tzdata", "the same"),
    ("sudo", "sudo", "the same"),
    ("less", "less", "the same"),
    ("ca-certificates", "ca-certificates", "the same"),
    ("btrfs-progs", "btrfs-progs", "the root file system of the product layout"),
    ("dosfstools", "dosfstools", "the EFI system partition"),
    ("e2fsprogs", "e2fsprogs", "the ext4 /boot of the product layout"),
    ("gdisk", "gdisk", "the same"),
    ("parted", "parted", "the same"),
    ("kpartx", "kpartx", "the installer medium's partition mapper"),
    ("dialog", "dialog", "the installer's progress dialog"),
    ("xz-utils", "xz", "the compressor of the embedded image"),
    ("grub2-common", "grub2-common", "GRUB 2.12's shared files"),
    (None, "grub2-tools", "grub2-install and grub2-mkconfig, which Fedora ships apart"),
    ("grub-efi-amd64-bin", "grub2-efi-x64-modules", "the x86_64-efi modules"),
    ("grub-efi-amd64-signed", "grub2-efi-x64", "Fedora's signed GRUB, the second stage shim verifies"),
    ("shim-signed", "shim-x64", "shim signed by the Microsoft UEFI CA"),
    ("grub-pc-bin", "grub2-pc", "the legacy BIOS path of the same disk (eficsm)"),
    (None, "grub2-pc-modules", "the i386-pc modules grub2-pc installs from"),
    ("efibootmgr", "efibootmgr", "the same"),
    ("mokutil", "mokutil", "the same"),
    ("cloud-init", "cloud-init", "the owner of the host settings"),
    ("openssh-server", "openssh-server", "the host keys first boot records"),
    ("unattended-upgrades", None, "the update authority is the host slice's decision; nothing is installed for it"),
    ("nftables", "nftables", "the same"),
    ("cockpit-ws", "cockpit-ws", "system management"),
    ("cockpit-system", "cockpit-system", "system management"),
    (None, "NetworkManager", "Fedora's network owner, which cloud-init renders to on Fedora"),
    ("olivares", "olivares", "the product, from the local repository (D's RPM)"),
    ("olivares-appliance-base", "olivares-appliance-base", "the layer, from the local repository (D's RPM)"),
    ("dracut-kiwi-oem-dump", "dracut-kiwi-oem-dump", "KIWI 11.0.4's installer modules, built by the stage step"),
    ("dracut-network", "dracut-network", "kiwi-dump's network dependency"),
    ("iproute2", "iproute", "ip, for dracut's network"),
    ("cryptsetup-bin", "cryptsetup", "kiwi-lib's crypt dependency"),
    ("kexec-tools", "kexec-tools", "kiwi-dump-reboot's hand-over"),
    # The update interface F5 r3 §10 asks the recipe to install (A2 author, F1).
    (None, "dnf5", "the package manager of the managed DNF5 route (F5 r3 §1-§2)"),
    (None, "libdnf5-plugin-actions", "the actions plugin the package-phase bridge hooks (F5 r3 §2)"),
    (None, "dnf5-plugin-automatic", "installed with its timer disabled: one automatic route (F5 r3 §3)"),
    (None, "fedora-repos", "Fedora's own repository files and trust chain (F5 r3 §7)"),
]
# SELinux's own packages, which the Fedora profile adds to the map (FedoraSelinux).
SELINUX_PACKAGES = {"selinux-policy-targeted", "policycoreutils", "policycoreutils-python-utils", "olivares-selinux"}
FEDORA_BOOTSTRAP = {"filesystem", "fedora-release-server", "fedora-gpg-keys", "rpm", "ca-certificates"}
DEBIAN_BOOTSTRAP = {"apt", "dpkg", "gnupg", "gpgv", "ca-certificates", "debian-archive-keyring"}
# What the Fedora profile never installs: Debian's package tools and update channel, and any update authority the host
# slice has not named (dnf5, its automatic-update plugin, dnf-automatic, PackageKit).
NOT_ON_FEDORA = {"apt", "dpkg", "gpgv", "debian-archive-keyring", "unattended-upgrades", "python3-apt",
                 "apt-listchanges", "dnf-automatic", "PackageKit"}


def profile_packages(image, section_type, profile):
    return [p.get("name") for s in image.iter("packages") if s.get("type") == section_type and in_profile(s, profile)
            for p in s.iter("package")]


class FedoraPackages(unittest.TestCase):
    """The Fedora profile's packages are the mapped table, and nothing of Debian's package lifecycle."""

    def test_the_fedora_bootstrap_is_rpm_with_the_fedora_44_keyring(self):
        image = description_root()
        self.assertEqual(set(profile_packages(image, "bootstrap", FEDORA)), FEDORA_BOOTSTRAP)
        self.assertEqual(set(profile_packages(image, "bootstrap", DEBIAN)), DEBIAN_BOOTSTRAP)

    def test_the_fedora_image_installs_exactly_the_mapped_packages(self):
        image = description_root()
        fedora = profile_packages(image, "image", FEDORA)
        self.assertEqual(len(fedora), len(set(fedora)), "a package is named twice")
        self.assertEqual(set(fedora) - SELINUX_PACKAGES, {f for _, f, _ in PACKAGE_MAP if f})
        self.assertEqual(set(profile_packages(image, "image", DEBIAN)), {d for d, _, _ in PACKAGE_MAP if d})

    def test_no_debian_package_lifecycle_and_no_unnamed_update_authority_on_fedora(self):
        image = description_root()
        names = set(profile_packages(image, "bootstrap", FEDORA)) | set(profile_packages(image, "image", FEDORA))
        self.assertEqual(names & NOT_ON_FEDORA, set())

    def test_firmware_travels_in_its_own_profile_per_base(self):
        image = description_root()
        profiles = {p.get("name") for p in image.iter("profile")}
        self.assertIn("fedora44-firmware", profiles)
        self.assertEqual(profile_packages(image, "image", "fedora44-firmware"), ["linux-firmware"])
        self.assertNotIn("linux-firmware", profile_packages(image, "image", FEDORA))


# The image root's rpm keyring as dnf5 leaves it after installing from the three Fedora-profile repositories: Fedora 44's
# key, the product repository's key, and the per-build key of the in-build dracut RPM. rpmkeys --list prints
# "<fingerprint> <name> <userid> public key" (rpmkeys(8) at rpm 6.0.2, r12c-fedora/sources).
PER_BUILD_FINGERPRINT = "C0FFEE00" * 5
BUILD_KEY_RECORD = "var/lib/olivares-appliance-build/per-build-key.fingerprint"
IMAGE_KEYRING = {"36f612dcf27f7d1a48a835e4dbfcf71c6d9f90a6": "gpg-pubkey Fedora (44) <fedora-44-primary@fedoraproject.org>",
                 RELEASE_KEY_FINGERPRINT.lower(): "gpg-pubkey Olivares AI package repository",
                 PER_BUILD_FINGERPRINT.lower(): "gpg-pubkey Olivares appliance per-build key <per-build@invalid.olivares.ai>"}
FAKE_RPMKEYS = """#!/bin/sh
ring=@RING@
case "$1" in
  -l|--list) shift
    if [ $# -eq 0 ]; then cat "$ring"; exit 0; fi
    grep -i "^$1 " "$ring" || exit 1 ;;
  -e|--erase|-d|--delete) shift
    grep -qi "^$1 " "$ring" || exit 1
    @ERASE@ ;;
  *) exit 2 ;;
esac
"""


# The repository sections fedora-repos-44-2.noarch installs, with their enabled lines (its payload, read in r12b:
# FEDORA-REPOS.txt). fedora-cisco-openh264 is enabled by default: Fedora-signed builds that Cisco serves, outside the
# image's package set and trust chain.
FEDORA_REPO_FILES = {
    "fedora.repo": [("fedora", "1"), ("fedora-debuginfo", "0"), ("fedora-source", "0")],
    "fedora-updates.repo": [("updates", "1"), ("updates-debuginfo", "0"), ("updates-source", "0")],
    "fedora-updates-testing.repo": [("updates-testing", "0"), ("updates-testing-debuginfo", "0"),
                                    ("updates-testing-source", "0")],
    "fedora-cisco-openh264.repo": [("fedora-cisco-openh264", "1"), ("fedora-cisco-openh264-debuginfo", "0"),
                                   ("fedora-cisco-openh264-source", "0")],
}
ENABLED_ON_FEDORA = {"fedora", "updates", "olivares"}


def fedora_repo_files(root, files=None):
    """The /etc/yum.repos.d files of fedora-repos 44-2 below ROOT, as the package installs them before config.sh."""
    directory = Path(root) / "etc/yum.repos.d"
    directory.mkdir(parents=True, exist_ok=True)
    for name, sections in (FEDORA_REPO_FILES if files is None else files).items():
        (directory / name).write_text("".join(
            "[%s]\nname=Fedora $releasever %s\nmetalink=https://mirrors.fedoraproject.org/metalink?repo=%s\ntype=rpm\n%s"
            "repo_gpgcheck=0\ngpgcheck=1\ngpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-fedora-$releasever-$basearch\n\n"
            % (section, section, section, "" if enabled is None else "enabled=%s\n" % enabled)
            for section, enabled in sections))


def enabled_repositories(root):
    """The repository IDs dnf5 would enable from ROOT/etc/yum.repos.d: enabled is true unless a section says otherwise."""
    import configparser
    enabled = set()
    for path in sorted((Path(root) / "etc/yum.repos.d").glob("*.repo")):
        parser = configparser.ConfigParser(interpolation=None)
        parser.read_string(path.read_text())
        enabled |= {name for name in parser.sections() if parser[name].get("enabled", "1").strip().lower() in
                    ("1", "true", "yes", "on")}
    return enabled


# The policy store after olivares-selinux's install (its %post: %selinux_modules_install at priority 200, then its four
# port records), as semodule -lfull and semanage port -l -C list it.
POLICY_MODULES = "400 container pp\n200 olivares pp\n100 base pp\n"
PORT_RECORDS = ("olivares_console_port_t tcp 8443", "olivares_grpc_port_t tcp 8444", "olivares_portal_port_t tcp 9443",
                "olivares_model_runtime_port_t tcp 11434")


def port_listing(records):
    return "SELinux Port Type              Proto    Port Number\n\n" + "".join(
        "%-30s %-8s %s\n" % tuple(record.split()) for record in records)


def run_config_sh(root, profiles, programs=(), keyring=None, erase=True, record=PER_BUILD_FINGERPRINT,
                  modules=POLICY_MODULES, ports=PORT_RECORDS, script=None):
    """config.sh as KIWI runs it (system/setup.py call_config_script: bash in the image root after the packages), here on
    ROOT: its absolute /etc, /var, /.kconfig and /.profile paths are moved below ROOT, the layer's template gate is
    stubbed (it is the layer's, tested there), and PROGRAMS are the only extra commands on PATH, each exiting 0. On
    Fedora, rpmkeys works on ROOT/.rpm-keyring (KEYRING, by default IMAGE_KEYRING), and erases nothing unless ERASE; the
    overlay carries RECORD, the per-build key's fingerprint build.sh stages, unless it is None."""
    root = Path(root)
    (root / ".profile").write_text("kiwi_profiles='%s'\n" % ",".join(profiles))
    for profile in profiles:
        if (KIWI / profile).is_dir() and not (root / ".overlay-imported").exists():
            shutil.copytree(KIWI / profile, root, dirs_exist_ok=True, symlinks=True)
            key = root / "etc/pki/rpm-gpg/RPM-GPG-KEY-olivares-package-repository"
            key.parent.mkdir(parents=True, exist_ok=True)
            key.write_text("-----BEGIN PGP PUBLIC KEY BLOCK-----\nstaged by build.sh\n")
            if record is not None:
                (root / BUILD_KEY_RECORD).parent.mkdir(parents=True, exist_ok=True)
                (root / BUILD_KEY_RECORD).write_text(record + "\n")
            if profile in (FEDORA, DESKTOP) and not (root / "etc/yum.repos.d/fedora.repo").exists():
                fedora_repo_files(root)
            (root / ".overlay-imported").write_text(profile)
    text = (script if script is not None else KIWI / "config.sh").read_text() \
        .replace("/usr/bin/appliance-firstboot check-template /", "true")
    # A path after "//" is inside a URL (file:///etc/...), text config.sh compares, not a file of the root.
    text = re.sub(r"(?<![\w./])/(etc|var|\.kconfig|\.profile|\.autorelabel)(?=[/\s'\"]|$)",
                  lambda m: str(root) + m.group(0), text)
    script, stubs = root / "config-under-test.sh", root / "stub-bin"
    script.write_text(text)
    stubs.mkdir(exist_ok=True)
    for program in programs:
        (stubs / program).write_text("#!/bin/sh\nexit 0\n")
        (stubs / program).chmod(0o755)
    ring = root / ".rpm-keyring"
    if not ring.exists():
        ring.write_text("".join("%s %s public key\n" % item for item in (IMAGE_KEYRING if keyring is None else keyring).items()))
    removal = 'grep -vi "^$1 " "$ring" > "$ring.new"; mv "$ring.new" "$ring"' if erase else "exit 0"
    (stubs / "rpmkeys").write_text(FAKE_RPMKEYS.replace("@RING@", shlex_quote(str(ring))).replace("@ERASE@", removal))
    (stubs / "rpmkeys").chmod(0o755)
    listings = {"semodule": ("-lfull", modules), "semanage": ("port -l -C", port_listing(ports))}
    for program, (argv, listing) in listings.items():
        (root / (".%s.out" % program)).write_text(listing)
        (stubs / program).write_text('#!/bin/sh\n[ "$*" = %s ] || exit 2\ncat %s\n' % (
            shlex_quote(argv), shlex_quote(str(root / (".%s.out" % program)))))
        (stubs / program).chmod(0o755)
    env = dict(os.environ, PATH=str(stubs) + os.pathsep + "/usr/bin:/bin")
    return subprocess.run(["bash", str(script)], capture_output=True, text=True, timeout=20, env=env)


def shlex_quote(text):
    import shlex
    return shlex.quote(text)


class FedoraOpenh264(unittest.TestCase):
    """fedora-cisco-openh264, enabled by default by fedora-repos 44-2, is disabled in the image, and config.sh fails a root
    that would enable any repository but Fedora's two and ours (C3)."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="a2-openh264-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        contexts = self.root / "etc/selinux/targeted/contexts/files/file_contexts"
        contexts.parent.mkdir(parents=True)
        contexts.write_text("/.* system_u:object_r:default_t:s0\n")

    def openh264(self):
        return self.root / "etc/yum.repos.d/fedora-cisco-openh264.repo"

    def test_config_sh_disables_fedora_cisco_openh264_in_the_package_file(self):
        fedora_repo_files(self.root)
        before = self.openh264().read_text()
        self.assertIn("fedora-cisco-openh264", enabled_repositories(self.root))
        result = run_config_sh(self.root, [FEDORA], programs=["setfiles"])
        self.assertEqual(result.returncode, 0, result.stderr[-2000:])
        self.assertEqual(enabled_repositories(self.root), ENABLED_ON_FEDORA)
        # Only the one line changes, in the file the package owns (a %config(noreplace) file an update keeps).
        self.assertEqual(self.openh264().read_text(), before.replace("repo=fedora-cisco-openh264\ntype=rpm\nenabled=1\n",
                                                                     "repo=fedora-cisco-openh264\ntype=rpm\nenabled=0\n", 1))

    def test_an_openh264_section_that_stays_enabled_fails_the_build(self):
        # No enabled line: dnf5 enables a section by default, and no line is there to turn off.
        files = dict(FEDORA_REPO_FILES, **{"fedora-cisco-openh264.repo": [("fedora-cisco-openh264", None)]})
        fedora_repo_files(self.root, files)
        result = run_config_sh(self.root, [FEDORA], programs=["setfiles"])
        self.assertNotEqual(result.returncode, 0, "an enabled openh264 repository must fail the build")
        self.assertIn("fedora-cisco-openh264", result.stderr)

    def test_any_other_enabled_repository_fails_the_build(self):
        files = dict(FEDORA_REPO_FILES, **{"fedora-updates-testing.repo": [("updates-testing", "1")]})
        fedora_repo_files(self.root, files)
        result = run_config_sh(self.root, [FEDORA], programs=["setfiles"])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("updates-testing", result.stderr)

    def test_a_repository_override_fails_the_build(self):
        # dnf5.conf(5) at 5.4.5.0, "Drop-in repo directories": an override file can change enabled after the .repo files.
        fedora_repo_files(self.root)
        override = self.root / "etc/dnf/repos.override.d/50-openh264.repo"
        override.parent.mkdir(parents=True)
        override.write_text("[fedora-cisco-openh264]\nenabled=1\n")
        result = run_config_sh(self.root, [FEDORA], programs=["setfiles"])
        self.assertNotEqual(result.returncode, 0, "an override could enable openh264 again")
        self.assertIn("repos.override.d", result.stderr)


class FedoraBuildKey(unittest.TestCase):
    """The per-build key that signed the in-build dracut RPM stays out of the shipped image's rpm keyring: config.sh erases
    it by the fingerprint build.sh recorded, and fails a root whose keyring still lists it (C2)."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="a2-build-key-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        contexts = self.root / "etc/selinux/targeted/contexts/files/file_contexts"
        contexts.parent.mkdir(parents=True)
        contexts.write_text("/.* system_u:object_r:default_t:s0\n")

    def listed(self):
        return {line.split()[0] for line in (self.root / ".rpm-keyring").read_text().splitlines()}

    def test_config_sh_erases_the_per_build_key_by_its_recorded_fingerprint(self):
        result = run_config_sh(self.root, [FEDORA], programs=["setfiles"])
        self.assertEqual(result.returncode, 0, result.stderr[-2000:])
        self.assertEqual(self.listed(), set(IMAGE_KEYRING) - {PER_BUILD_FINGERPRINT.lower()})
        self.assertFalse((self.root / BUILD_KEY_RECORD).exists(), "the record leaves the image with the key")
        self.assertFalse((self.root / BUILD_KEY_RECORD).parent.exists())

    def test_a_keyring_that_keeps_the_per_build_key_fails_the_build(self):
        result = run_config_sh(self.root, [FEDORA], programs=["setfiles"], erase=False)
        self.assertNotEqual(result.returncode, 0, "a keyring that keeps the per-build key must fail the build")
        self.assertIn(PER_BUILD_FINGERPRINT, result.stderr.upper())

    def test_a_root_without_the_recorded_fingerprint_fails_the_build(self):
        result = run_config_sh(self.root, [FEDORA], programs=["setfiles"], record=None)
        self.assertNotEqual(result.returncode, 0, "the per-build key cannot be named, so it cannot be shown absent")
        malformed = Path(tempfile.mkdtemp(dir=self.temp.name))
        (malformed / "etc/selinux/targeted/contexts/files").mkdir(parents=True)
        (malformed / "etc/selinux/targeted/contexts/files/file_contexts").write_text("/.* x\n")
        self.assertNotEqual(run_config_sh(malformed, [FEDORA], programs=["setfiles"], record="not a fingerprint").returncode, 0)

    def test_a_keyring_without_the_per_build_key_passes(self):
        keyring = {k: v for k, v in IMAGE_KEYRING.items() if k != PER_BUILD_FINGERPRINT.lower()}
        result = run_config_sh(self.root, [FEDORA], programs=["setfiles"], keyring=keyring)
        self.assertEqual(result.returncode, 0, result.stderr[-2000:])
        self.assertEqual(self.listed(), set(keyring))


class FedoraSelinuxPackage(unittest.TestCase):
    """RECIPE-INTERFACE item 1: the policy module's package olivares-selinux in the Fedora profile, from the appliance's
    own repository, with policycoreutils-python-utils at the pinned policycoreutils' build; its build toolchain is in the
    recipe's builder, pinned by the lock like every builder package."""

    def lock(self):
        return json.loads(FEDORA_LOCK.read_text())

    def test_the_profile_installs_the_policy_package_and_semanage(self):
        installed = profile_packages(description_root(), "image", FEDORA)
        self.assertIn("olivares-selinux", installed)
        self.assertIn("policycoreutils-python-utils", installed)
        image = self.lock()["image_packages"]
        self.assertNotIn("olivares-selinux", image, "it comes from the appliance's own repository, like olivares")
        utils = image["policycoreutils-python-utils"]
        self.assertEqual(utils["nevra"], "policycoreutils-python-utils-3.11-2.fc44.noarch")
        self.assertEqual((utils["version"], utils["release"]),
                         (image["policycoreutils"]["version"], image["policycoreutils"]["release"]))

    def test_the_builder_holds_the_pinned_policy_toolchain(self):
        # F4's pins of selinux-policy 44.10-1.fc44 (the module's workflow, by signed Koji build and sha256), which are the
        # image's selinux-policy-targeted build.
        system = self.lock()["distribution"]["system_packages"]
        pinned = {"selinux-policy": "dd86ac4b3e3e82bf1881eb306af6f0a2fdb9c11e17933d56263e04a6504cdb8a",
                  "selinux-policy-devel": "42c3c0840cd5a4644edfedb54db5eabf8e0262c2b8de9002919694567c0fb4c9"}
        for name, sha256 in pinned.items():
            with self.subTest(name=name):
                self.assertEqual((system[name]["nevra"], system[name]["sha256"]), ("%s-44.10-1.fc44.noarch" % name, sha256))
                self.assertEqual(system[name]["koji_signed"], "https://kojipkgs.fedoraproject.org/packages/selinux-policy/"
                                                              "44.10/1.fc44/data/signed/6d9f90a6/noarch/%s-44.10-1.fc44.noarch.rpm"
                                                              % name)
        targeted = self.lock()["image_packages"]["selinux-policy-targeted"]
        self.assertEqual((targeted["version"], targeted["release"]), ("44.10", "1.fc44"), "one policy build")
        for name in ("make", "bzip2", "rpm-build"):
            with self.subTest(name=name):
                self.assertIn(name, system)
                self.assertTrue(system[name]["srpm"]["sha256"], name)
        installs = [line.rstrip(" \\") for line in (FEDORA_TOOLCHAIN / "bootstrap.sh").read_text().splitlines()]
        for name in ("selinux-policy", "selinux-policy-devel", "make", "bzip2"):
            self.assertIn("    " + system[name]["nevra"], installs)


def run_images_sh(root, profiles, relabel="", status=0):
    """images.sh as KIWI runs it (tasks/system_build.py: after config.sh and its own setfiles labeling, chrooted), here on
    ROOT: its absolute /etc, /usr, /var and /.autorelabel paths are moved below ROOT. setfiles is a stub that records its
    arguments in ROOT/.setfiles.args and prints RELABEL, exiting STATUS."""
    root = Path(root)
    (root / ".profile").write_text("kiwi_profiles='%s'\n" % ",".join(profiles))
    text = (KIWI / "images.sh").read_text()
    text = re.sub(r"(?<![\w./])/(etc|usr|var|\.kconfig|\.profile|\.autorelabel)(?=[/\s'\")]|$)",
                  lambda m: str(root) + m.group(0), text)
    script, stubs = root / "images-under-test.sh", root / "stub-bin"
    script.write_text(text)
    stubs.mkdir(exist_ok=True)
    (stubs / "setfiles").write_text("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %s\nprintf '%%s' %s\nexit %d\n" % (
        shlex_quote(str(root / ".setfiles.args")), shlex_quote(relabel), status))
    (stubs / "setfiles").chmod(0o755)
    env = dict(os.environ, PATH=str(stubs) + os.pathsep + "/usr/bin:/bin")
    return subprocess.run(["bash", str(script)], capture_output=True, text=True, timeout=20, env=env)


# RECIPE-INTERFACE item 4: the paths the policy module labels, as setfiles -n -v checks them after KIWI's labeling. The
# first five are the installed packages' (olivares, olivares-appliance-base); the last two are the Appliance Console
# package's, which the image does not install, and are checked when present.
LABELED_PATHS = ("/usr/bin/olivares", "/usr/bin/appliance-firstboot", "/etc/systemd/system/olivares.service.d",
                 "/var/lib/olivares", "/var/lib/olivares-appliance")
LABELED_WHEN_PRESENT = ("/usr/libexec/olivares", "/etc/olivares-portal")


class FedoraSelinuxBuildChecks(unittest.TestCase):
    """RECIPE-INTERFACE item 4, each failing the build: config.sh after the package set (the module at priority 200, its
    four port records) and images.sh after KIWI's own labeling (setfiles -n -v changes no label of the module's paths;
    no /.autorelabel)."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="a2-selinux-checks-")
        self.addCleanup(self.temp.cleanup)

    def root(self):
        root = Path(tempfile.mkdtemp(dir=self.temp.name))
        contexts = root / "etc/selinux/targeted/contexts/files/file_contexts"
        contexts.parent.mkdir(parents=True)
        contexts.write_text("/.* system_u:object_r:default_t:s0\n")
        return root

    def test_config_sh_passes_the_module_at_priority_200_and_its_four_ports(self):
        result = run_config_sh(self.root(), [FEDORA], programs=["setfiles"])
        self.assertEqual(result.returncode, 0, result.stderr[-2000:])

    def test_config_sh_fails_without_the_module_at_priority_200(self):
        for label, modules in (("absent", "100 base pp\n"), ("another priority", "400 olivares cil\n100 base pp\n"),
                               ("a longer name", "200 olivares-extra pp\n")):
            with self.subTest(label):
                result = run_config_sh(self.root(), [FEDORA], programs=["setfiles"], modules=modules)
                self.assertNotEqual(result.returncode, 0, label)
                self.assertIn("the olivares policy module is not installed at priority 200", result.stderr)

    def test_config_sh_fails_a_missing_port_record(self):
        for missing in PORT_RECORDS:
            with self.subTest(missing=missing):
                ports = [r for r in PORT_RECORDS if r != missing] + [missing.replace("tcp", "udp")]
                result = run_config_sh(self.root(), [FEDORA], programs=["setfiles"], ports=ports)
                self.assertNotEqual(result.returncode, 0, missing)
                self.assertIn("no port record %s" % missing, result.stderr)

    def labeled_root(self, *also):
        root = self.root()
        for path in LABELED_PATHS[:2] + also:
            (root / path.lstrip("/")).parent.mkdir(parents=True, exist_ok=True)
            (root / path.lstrip("/")).write_text("")
        for path in LABELED_PATHS[2:]:
            (root / path.lstrip("/")).mkdir(parents=True)
        return root

    def test_images_sh_passes_when_setfiles_would_change_no_label(self):
        root = self.labeled_root()
        result = run_images_sh(root, [FEDORA])
        self.assertEqual(result.returncode, 0, result.stderr[-2000:])
        args = (root / ".setfiles.args").read_text().splitlines()
        self.assertEqual(args, ["-n", "-v", str(root) + "/etc/selinux/targeted/contexts/files/file_contexts"]
                         + [str(root) + p for p in LABELED_PATHS])
        for path in LABELED_WHEN_PRESENT:
            self.assertIn("%s%s is not in this image" % (root, path), result.stdout)
        present = self.labeled_root(LABELED_WHEN_PRESENT[0])
        (present / LABELED_WHEN_PRESENT[1].lstrip("/")).mkdir(parents=True)
        self.assertEqual(run_images_sh(present, [FEDORA]).returncode, 0)
        self.assertEqual((present / ".setfiles.args").read_text().splitlines()[-2:],
                         [str(present) + p for p in LABELED_WHEN_PRESENT])

    def test_images_sh_fails_a_label_the_policy_would_change(self):
        line = "Would relabel /usr/bin/olivares from system_u:object_r:bin_t:s0 to system_u:object_r:olivares_exec_t:s0"
        for label, relabel, status in (("a relabel line", line, 0), ("setfiles fails", "", 255)):
            with self.subTest(label):
                result = run_images_sh(self.labeled_root(), [FEDORA], relabel=relabel, status=status)
                self.assertNotEqual(result.returncode, 0, label)
                self.assertIn("setfiles -n -v finds labels", result.stderr)
                self.assertIn(relabel, result.stderr)

    def test_images_sh_fails_a_missing_labeled_path_or_an_autorelabel(self):
        root = self.labeled_root()
        (root / LABELED_PATHS[0].lstrip("/")).unlink()
        result = run_images_sh(root, [FEDORA])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("%s%s, which the policy module labels, is not in the image" % (root, LABELED_PATHS[0]),
                      result.stderr)
        root = self.labeled_root()
        (root / ".autorelabel").write_text("")
        result = run_images_sh(root, [FEDORA])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("/.autorelabel", result.stderr)

    def test_images_sh_checks_nothing_on_debian(self):
        root = self.root()
        result = run_images_sh(root, [DEBIAN])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse((root / ".setfiles.args").exists())


class FedoraSelinux(unittest.TestCase):
    """SELinux stays enforcing with the targeted policy on the Fedora profile, and the root is labeled at build (Root
    c1efc2ee §2). There is no permissive or disabled path."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="a2-selinux-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        contexts = self.root / "etc/selinux/targeted/contexts/files/file_contexts"
        contexts.parent.mkdir(parents=True)
        contexts.write_text("/.* system_u:object_r:default_t:s0\n")

    def test_the_fedora_type_labels_the_root_with_the_targeted_policy(self):
        # KIWI 11.0.4 labels the root with setfiles after config.sh and again after the disk sync, when the policy's
        # file_contexts exists and setfiles is installed (system/setup.py :633-645, builder/disk.py :975-990), with the
        # type's selinux_policy (:608).
        image = description_root()
        self.assertEqual(profile_type(image, FEDORA).get("selinux_policy"), "targeted")
        self.assertLessEqual(SELINUX_PACKAGES, set(profile_packages(image, "image", FEDORA)))

    def test_nothing_turns_enforcement_off(self):
        fedora = profile_type(description_root(), FEDORA)
        tokens = fedora.get("kernelcmdline").split()
        self.assertEqual([t for t in tokens if t.split("=")[0] in ("selinux", "enforcing", "autorelabel")], [])
        text = (KIWI / "config.sh").read_text() + (KIWI / "post_bootstrap.sh").read_text()
        for forbidden in ("SELINUX=permissive", "SELINUX=disabled", "setenforce", "selinux=0", "enforcing=0"):
            self.assertNotIn(forbidden, text)

    def test_nothing_in_the_description_asks_for_an_autorelabel(self):
        # RECIPE-INTERFACE item 3: Fedora's relabel service lowers enforcement while it runs, so the image holds no
        # /.autorelabel and no kernel command line of any profile carries selinux=0, enforcing=0 or autorelabel.
        for image_type in description_root().iter("type"):
            tokens = (image_type.get("kernelcmdline") or "").split()
            self.assertEqual([t for t in tokens if t.split("=")[0] in ("selinux", "enforcing", "autorelabel")], [])
        writers = re.compile(r"(touch|>|install|cp|mv|ln)[^\n]*/\.autorelabel|fixfiles[^\n]*onboot")
        for path in sorted(KIWI.rglob("*")):
            if path.is_file():
                self.assertIsNone(writers.search(path.read_text(errors="replace")), path)
                self.assertNotEqual(path.name, ".autorelabel", path)

    def test_config_sh_fails_a_root_that_asks_for_an_autorelabel(self):
        clean = run_config_sh(self.root, [FEDORA], programs=["setfiles"])
        self.assertEqual(clean.returncode, 0, clean.stderr[-2000:])
        self.assertFalse((self.root / ".autorelabel").exists(), "config.sh never writes it")
        asking = Path(tempfile.mkdtemp(dir=self.temp.name))
        (asking / "etc/selinux/targeted/contexts/files").mkdir(parents=True)
        (asking / "etc/selinux/targeted/contexts/files/file_contexts").write_text("/.* system_u:object_r:default_t:s0\n")
        (asking / ".autorelabel").write_text("")
        result = run_config_sh(asking, [FEDORA], programs=["setfiles"])
        self.assertNotEqual(result.returncode, 0, "a root that asks for an autorelabel must fail the build")
        self.assertIn("/.autorelabel", result.stderr)

    def test_config_sh_makes_fedora_enforcing_with_the_targeted_policy(self):
        # selinux-policy 44.10-1 ships /etc/selinux/config as a file its scriptlet writes (it is in the header's file
        # list, not in the payload), so the recipe writes the mode it relies on and checks it.
        result = run_config_sh(self.root, [FEDORA], programs=["setfiles"])
        self.assertEqual(result.returncode, 0, result.stderr[-2000:])
        lines = [l for l in (self.root / "etc/selinux/config").read_text().splitlines() if l and not l.startswith("#")]
        self.assertEqual(lines, ["SELINUX=enforcing", "SELINUXTYPE=targeted"])

    def test_a_fedora_root_without_the_policy_or_setfiles_fails_the_build(self):
        # Without them KIWI only warns and leaves the root unlabeled (setup.py :640-645); the recipe refuses instead.
        result = run_config_sh(self.root, [FEDORA])
        self.assertNotEqual(result.returncode, 0, "a root KIWI cannot label must not pass config.sh")
        (self.root / "etc/selinux/targeted/contexts/files/file_contexts").unlink()
        result = run_config_sh(self.root, [FEDORA], programs=["setfiles"])
        self.assertNotEqual(result.returncode, 0, "a root without the targeted policy must not pass config.sh")

    def test_the_debian_root_gets_no_selinux_configuration(self):
        result = run_config_sh(self.root, [DEBIAN, "firmware-nonfree"])
        self.assertEqual(result.returncode, 0, result.stderr[-2000:])
        self.assertFalse((self.root / "etc/selinux/config").exists())


# Measured from the pinned Fedora 44 packages (r12-fedora/sources/rpm-reads): grub2-tools 2.12-64 ships /usr/bin/grub2-install,
# so KIWI names the grub directory grub2; its grub2-mkconfig reads /etc/default/grub only (no grub.d); grub2-efi-x64's
# signed grubx64.efi embeds the prefix /EFI/fedora and no config, and the package owns /boot/loader/entries; shim-x64's
# posttrans puts shimx64.efi in /boot/efi/EFI/fedora, the directory KIWI copies its early script to (grub2.py :595-640,
# defaults.py :1354-1357).
FEDORA_SERIAL_LINE = "serial --unit=0 --speed=115200 --word=8 --parity=no --stop=1"
FEDORA_BOOTLOADER = {"name": "grub2", "timeout": "3", "bls": "true", "output_console": "console serial",
                     "input_console": "console serial", "serial_line": FEDORA_SERIAL_LINE}
# The boot-entry form A4 consumes on Fedora: Boot Loader Specification type #1 entries on the ext4 boot partition,
# /boot/loader/entries/*.conf in the running system, read by GRUB's blscfg.
FEDORA_ENTRY_DIRECTORY = "/boot/loader/entries"


def grub_directory(image, profile):
    return "grub2" if "grub2-tools" in profile_packages(image, "image", profile) else "grub"


class FedoraBoot(unittest.TestCase):
    """GRUB 2.12 as Fedora 44 ships it, on the product's layout: BLS entries, the serial console, and the ESP's early script
    loading the boot partition's configuration."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="a2-fedora-boot-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        contexts = self.root / "etc/selinux/targeted/contexts/files/file_contexts"
        contexts.parent.mkdir(parents=True)
        contexts.write_text("/.* system_u:object_r:default_t:s0\n")

    def test_fedora_grub_writes_bls_entries_and_answers_on_the_serial_port(self):
        # KIWI writes GRUB_TERMINAL_INPUT/OUTPUT and GRUB_SERIAL_COMMAND into /etc/default/grub from these attributes, and
        # GRUB_ENABLE_BLSCFG=true when bls holds and grub2-mkconfig knows the variable (grub2.py :700-800). The serial
        # settings are the ones the Debian profile writes into grub.d.
        loaders = [dict(b.attrib) for b in profile_type(description_root(), FEDORA).iter("bootloader")]
        self.assertEqual(loaders, [FEDORA_BOOTLOADER])
        self.assertIn('GRUB_SERIAL_COMMAND="%s"' % FEDORA_SERIAL_LINE, (KIWI / "config.sh").read_text())

    def test_the_signed_fedora_grub_finds_the_configuration_where_it_reads(self):
        image = description_root()
        self.assertIn("shim-x64", profile_packages(image, "image", FEDORA))
        filesystem, reads, holds = grub_configuration_path(dict(profile_type(image, FEDORA).attrib),
                                                           grub_directory(image, FEDORA))
        self.assertEqual((filesystem, reads, holds), ("ext4", "/grub2/grub.cfg", "/grub2/grub.cfg"))

    def test_the_entries_a4_reads_are_bls_on_the_boot_partition(self):
        image = description_root()
        fedora = profile_type(image, FEDORA)
        self.assertEqual((fedora.get("bootpartition"), [b.get("bls") for b in fedora.iter("bootloader")]), ("true", ["true"]))
        self.assertIn("grub2-efi-x64", profile_packages(image, "image", FEDORA))
        self.assertTrue(FEDORA_ENTRY_DIRECTORY.startswith("/boot/"))

    def test_config_sh_keeps_debian_only_steps_off_fedora(self):
        # Fedora's grub2-mkconfig reads no grub.d; Fedora's sshd-keygen@.service already runs whenever a host key is
        # missing (ConditionFileNotEmpty=|!/etc/ssh/ssh_host_%i_key, openssh-server 10.2p1-14); there is no apt.
        result = run_config_sh(self.root, [FEDORA], programs=["setfiles"])
        self.assertEqual(result.returncode, 0, result.stderr[-2000:])
        self.assertFalse((self.root / "etc/default/grub.d").exists())
        self.assertFalse((self.root / "etc/systemd/system/sshd-keygen.service.d").exists())
        self.assertNotIn("apt-get", result.stderr)
        debian = Path(self.temp.name) / "debian"
        debian.mkdir()
        result = run_config_sh(debian, [DEBIAN])
        self.assertEqual(result.returncode, 0, result.stderr[-2000:])
        self.assertTrue((debian / "etc/default/grub.d/99-olivares-appliance.cfg").is_file())
        self.assertTrue((debian / "etc/systemd/system/sshd-keygen.service.d/olivares-appliance.conf").is_file())

    def test_post_bootstrap_selects_a_locale_only_on_debian(self):
        # /etc/locale.gen is Debian's locales mechanism; Fedora's locale comes from glibc-langpack-en.
        script = (KIWI / "post_bootstrap.sh").read_text()
        for release, expected in (("debian_version", ["en_US.UTF-8 UTF-8"]), ("fedora-release", None)):
            with self.subTest(release=release):
                root = Path(self.temp.name) / release
                (root / "etc").mkdir(parents=True)
                (root / "etc" / release).write_text("x\n")
                redirected = root / "post_bootstrap.sh"
                redirected.write_text(re.sub(r"(?<![\w.])/etc/", str(root) + "/etc/", script))
                result = subprocess.run(["bash", str(redirected)], capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)
                target = root / "etc/locale.gen"
                self.assertEqual(target.read_text().splitlines() if target.exists() else None, expected)


FEDORA_TOOLCHAIN = ROOT / "appliance/images/toolchain/fedora44"
# registry.fedoraproject.org/fedora:44, read 2026-09-27T10:04:40Z: the OCI index and its linux/amd64 manifest.
FEDORA_BASE_INDEX = "sha256:539cadb5d8a43564d8abefd6eafdfcbcd4809070efbb900ec248229903db5911"
FEDORA_BASE_AMD64 = "sha256:111c574c9647d837ae22edf3204b670d5a151bcbb1cf2d13928c215b8e6488c4"
# What the product builds in the stage step, not from Fedora's trees.
LOCAL_RPMS = {"olivares", "olivares-appliance-base", "olivares-selinux", "dracut-kiwi-oem-dump"}


def nevra_of(name, entry):
    epoch = "" if entry["epoch"] in ("0", "", None) else entry["epoch"] + ":"
    return "%s-%s%s-%s.%s" % (name, epoch, entry["version"], entry["release"], entry["arch"])


class FedoraToolchain(unittest.TestCase):
    """The Fedora 44 builder: a toolchain successor pinned by digest, KIWI 11.0.4 from its verified source on Fedora's own
    Python packages, and an input lock whose every RPM names its NEVRA and sha256 from the pinned metadata."""

    def lock(self):
        return json.loads((FEDORA_TOOLCHAIN / "input-lock.json").read_text())

    def test_the_fedora_builder_is_fedora_44_pinned_by_digest(self):
        container = (FEDORA_TOOLCHAIN / "Containerfile").read_text()
        self.assertEqual(re.findall(r"(?m)^FROM\s+(\S+)", container),
                         ["registry.fedoraproject.org/fedora:44@" + FEDORA_BASE_AMD64])
        base = self.lock()["base_image"]
        self.assertEqual((base["index_digest"], base["manifest_digest_amd64"]), (FEDORA_BASE_INDEX, FEDORA_BASE_AMD64))
        self.assertNotIn("DEBIAN_FRONTEND", container)

    def test_the_fedora_lock_pins_kiwi_11_0_4_from_the_same_verified_source(self):
        fedora, debian = self.lock()["toolchain"], json.loads(LOCK.read_text())["toolchain"]
        self.assertEqual(fedora["kiwi"], debian["kiwi"])
        self.assertEqual(fedora["kiwi"]["upstream_version"], "11.0.4")
        self.assertEqual(fedora["dracut_modules_dir"], debian["dracut_modules_dir"])
        # KIWI 11.0.4 needs typer>=0.19.0, lxml>=4.6.0, requests>=2.25.0, PyYAML>=5.4.0, simplejson>=3.17.0 and builds with
        # poetry-core>=1.2.0 (its pyproject.toml); Fedora 44 packages each at a newer version.
        self.assertEqual(set(fedora["python"]["rpm_dependencies"]),
                         {"python3-typer", "python3-lxml", "python3-requests", "python3-pyyaml", "python3-simplejson",
                          "python3-poetry-core"})
        self.assertLessEqual(set(fedora["python"]["rpm_dependencies"]), set(self.lock()["distribution"]["system_packages"]))

    def test_every_locked_rpm_names_its_nevra_and_sha256_from_a_pinned_tree(self):
        lock = self.lock()
        trees = lock["distribution"]["repositories"]
        pins = json.loads((KIWI / "fedora-repositories.json").read_text())["repositories"]
        self.assertEqual({a: t["repomd_sha256"] for a, t in trees.items()}, {a: p["repomd_sha256"] for a, p in pins.items()})
        entries = dict(lock["distribution"]["system_packages"], **lock["image_packages"])
        self.assertGreater(len(entries), 60)
        for name, entry in entries.items():
            with self.subTest(name=name):
                self.assertIn(entry["repo"], trees)
                self.assertRegex(entry["sha256"], r"^[0-9a-f]{64}$")
                self.assertEqual(entry["nevra"], nevra_of(name, entry))
                self.assertIn(entry["arch"], ("x86_64", "noarch"))

    def test_every_fedora_profile_package_is_locked(self):
        image = description_root()
        named = set()
        for profile in (FEDORA, DESKTOP, "fedora44-firmware"):
            named |= set(profile_packages(image, "bootstrap", profile)) | set(profile_packages(image, "image", profile))
        self.assertEqual(named - LOCAL_RPMS - set(self.lock()["image_packages"]), set())

    def test_bootstrap_installs_exactly_the_locked_rpms_from_the_verified_metadata(self):
        lock_bytes = (FEDORA_TOOLCHAIN / "input-lock.json").read_bytes()
        script = (FEDORA_TOOLCHAIN / "bootstrap.sh").read_text()
        self.assertIn("printf '%%s  %%s\\n' '%s' input-lock.json | sha256sum -c -" % hashlib.sha256(lock_bytes).hexdigest(),
                      script)
        self.assertIn(FEDORA_KEY_SHA256, script)
        for sha in FEDORA_REPOMD.values():
            self.assertIn(sha, script)
        # The metadata dnf5 installs from is the verified one: cached first, checked, then used offline (-C).
        self.assertLess(script.index("makecache"), script.index(FEDORA_REPOMD["fedora-44-updates"]))
        self.assertLess(script.index(FEDORA_REPOMD["fedora-44-updates"]), script.index(" -C install"))
        for name, entry in self.lock()["distribution"]["system_packages"].items():
            self.assertIn(" " + entry["nevra"], script, name)
        self.assertNotIn("apt-get", script)

    def test_the_fedora_lock_names_the_programs_and_files_its_recipe_uses(self):
        toolchain = self.lock()["toolchain"]
        self.assertLessEqual({"kiwi-ng", "dnf5", "rpmbuild", "createrepo_c", "grub2-mkimage", "qemu-img", "xorriso"},
                             set(toolchain["required_programs"]))
        self.assertNotIn("dpkg-scanpackages", toolchain["required_programs"])
        self.assertLessEqual({"/usr/lib/grub/x86_64-efi/modinfo.sh", "/usr/lib/grub/i386-pc/modinfo.sh",
                              "/usr/share/syslinux/isolinux.bin"}, set(toolchain["required_files"]))
        self.assertEqual((self.lock()["invocation"]["containerfile"]), "appliance/images/toolchain/fedora44/Containerfile")


# The actions plugin's own version, measured at the dnf5 source of the locked version: libdnf5-plugins/actions/actions.cpp
# :54 `PLUGIN_VERSION{1, 4, 1}` at tag 5.4.5.0 (GET 2026-09-27T11:17:59Z, r12b-fedora/sources) and the same line at tag
# 5.4.6.0 (GET 2026-09-29, this lane's relock). A locked dnf5 version without a measured row fails the check below.
ACTIONS_PLUGIN_VERSION = {"5.4.5.0": (1, 4, 1), "5.4.6.0": (1, 4, 1)}
# F5 r3 :62: the minimum plugin version for each feature the bridge uses, from libdnf5_plugins/actions.8 at tag 5.4.5.0
# (GET 11:17:58Z): mode=json 1.2.0 (:89), raise_error 1.4.0 (:99-103), the JSON stop operation 1.4.0 (:235),
# goal_resolved 1.3.0 (:61); pre_transaction and post_transaction (:62-63) carry no version note, the plugin's first.
F5_ACTIONS_MINIMUMS = {"mode=json": (1, 2, 0), "raise_error": (1, 4, 0), "stop operation": (1, 4, 0),
                       "goal_resolved": (1, 3, 0), "pre_transaction": (0, 0, 0), "post_transaction": (0, 0, 0)}
FEDORA_OVERLAY = KIWI / FEDORA
# The public product repository: D's layout (scripts/publish-package-repositories.sh :69-73 at ccf7ea20) on the reviewed
# origin (scripts/package-repository-client-ci.sh :54, :135). A single named constant, D's input.
PUBLIC_RPM_BASEURL = "https://packages.olivares.ai/stable/rpm/x86_64"
OLIVARES_KEY_PATH = "/etc/pki/rpm-gpg/RPM-GPG-KEY-olivares-package-repository"
# D's packaging/repositories/dnf-repo.template at S3's 0621ff27, its non-comment lines.
DNF_REPO_TEMPLATE = """[{{REPO_ID}}]
name={{REPO_NAME}}
baseurl={{BASEURL}}
enabled=1
pkg_gpgcheck=1
repo_gpgcheck=1
gpgkey={{GPGKEY}}
metadata_expire=0
type=rpm-md
"""


class FedoraUpdateInterface(unittest.TestCase):
    """What F5 r3 §10 asks the A2 recipe to install on Fedora: dnf5 and the actions plugin at the minimum versions, the
    automatic plugin disabled, our key and repository file, Fedora's repository files, and installonly_limit."""

    def lock(self):
        return json.loads(FEDORA_LOCK.read_text())

    def test_the_update_interface_packages_are_installed_from_the_lock(self):
        image = description_root()
        installed, locked = set(profile_packages(image, "image", FEDORA)), self.lock()["image_packages"]
        for name in ("dnf5", "libdnf5-plugin-actions", "dnf5-plugin-automatic", "fedora-repos"):
            with self.subTest(name=name):
                self.assertIn(name, installed)
                self.assertIn(name, locked)

    def test_the_locked_actions_plugin_meets_the_f5_minimums(self):
        locked = self.lock()["image_packages"]
        version = locked["libdnf5-plugin-actions"]["version"]
        self.assertEqual(version, locked["dnf5"]["version"], "the plugin is built from the dnf5 source it ships with")
        plugin = ACTIONS_PLUGIN_VERSION.get(version)
        self.assertIsNotNone(plugin, "no measured actions plugin version for dnf5 %s" % version)
        for feature, minimum in F5_ACTIONS_MINIMUMS.items():
            self.assertGreaterEqual(plugin, minimum, feature)

    def test_one_automatic_route_the_dnf5_automatic_timer_stays_disabled(self):
        preset = (FEDORA_OVERLAY / "etc/systemd/system-preset/10-olivares-appliance.preset").read_text().splitlines()
        self.assertLessEqual({"disable dnf5-automatic.timer", "disable dnf-automatic.timer"}, set(preset))
        for path in FEDORA_OVERLAY.rglob("*"):
            if path.is_file():
                self.assertNotIn("enable dnf5-automatic", path.read_text(errors="replace"), path)
        with tempfile.TemporaryDirectory(prefix="a2-automatic-") as temp:
            root = Path(temp)
            contexts = root / "etc/selinux/targeted/contexts/files/file_contexts"
            contexts.parent.mkdir(parents=True)
            contexts.write_text("/.* system_u:object_r:default_t:s0\n")
            result = run_config_sh(root, [FEDORA], programs=["setfiles"])
            self.assertEqual(result.returncode, 0, result.stderr[-2000:])
            wants = root / "etc/systemd/system/timers.target.wants"
            wants.mkdir(parents=True)
            (wants / "dnf5-automatic.timer").symlink_to("/usr/lib/systemd/system/dnf5-automatic.timer")
            result = run_config_sh(root, [FEDORA], programs=["setfiles"])
            self.assertNotEqual(result.returncode, 0, "an enabled dnf5-automatic timer must fail the build")

    def test_the_rest_of_the_debian_lifecycle_and_other_routes_stay_absent(self):
        image = description_root()
        names = set(profile_packages(image, "bootstrap", FEDORA)) | set(profile_packages(image, "image", FEDORA))
        self.assertEqual(names & {"apt", "dpkg", "unattended-upgrades", "python3-apt", "dnf-automatic", "PackageKit"}, set())

    def test_our_key_and_public_repository_file_are_pinned_with_both_checks(self):
        repo = (FEDORA_OVERLAY / "etc/yum.repos.d/olivares.repo").read_text()
        rendered = DNF_REPO_TEMPLATE.replace("{{REPO_ID}}", "olivares").replace("{{REPO_NAME}}", "Olivares Server stable") \
            .replace("{{BASEURL}}", PUBLIC_RPM_BASEURL).replace("{{GPGKEY}}", "file://" + OLIVARES_KEY_PATH)
        self.assertEqual([l for l in repo.splitlines() if l and not l.startswith("#")], rendered.splitlines())
        # The image ships no credential and no entitled repository: /dnf/v1/ is S7's runtime write (F5 r3 §7).
        self.assertEqual(sorted(p.name for p in (FEDORA_OVERLAY / "etc/yum.repos.d").iterdir()), ["olivares.repo"])
        for path in FEDORA_OVERLAY.rglob("*"):
            if path.is_file():
                text = path.read_text(errors="replace")
                for secret in ("/dnf/v1", "username", "password", "sslclientkey"):
                    self.assertNotIn(secret, text, path)
        self.assertEqual(json.loads((KIWI / "package-repository-key.json").read_text())["image_key_path"], OLIVARES_KEY_PATH)

    def test_installonly_limit_keeps_every_kernel_a4_references_plus_the_incoming_one(self):
        # A4: retained points boot their own store objects, so the only live kernels they reference are the current entry's
        # (DESIGN-A4-r1c :117-118) and, until the next witness, next_pair's (DESIGN-A4-r1h §3.1.4): two. A transaction that
        # installs a new kernel adds one: 3. dnf5.conf(5) at 5.4.5.0: minimum 2, default 3; /etc/dnf/dnf.conf loads last and
        # libdnf5 5.4.5.0-1's own dnf.conf sets no installonly_limit (sha256 bb898f90, r12b-fedora/sources/rpm-reads); the
        # locked 5.4.6.0-1 ships no /etc/dnf/dnf.conf at all (read from the pinned RPM, 2026-09-29).
        conf = (FEDORA_OVERLAY / "etc/dnf/libdnf5.conf.d/20-olivares-appliance.conf").read_text().splitlines()
        self.assertEqual([l for l in conf if l and not l.startswith("#")], ["[main]", "installonly_limit=3"])
        with tempfile.TemporaryDirectory(prefix="a2-installonly-") as temp:
            root = Path(temp)
            contexts = root / "etc/selinux/targeted/contexts/files/file_contexts"
            contexts.parent.mkdir(parents=True)
            contexts.write_text("/.* system_u:object_r:default_t:s0\n")
            (root / "etc/dnf").mkdir(parents=True)
            (root / "etc/dnf/dnf.conf").write_text("[main]\ninstallonly_limit=7\n")
            result = run_config_sh(root, [FEDORA], programs=["setfiles"])
            self.assertNotEqual(result.returncode, 0, "a dnf.conf that overrides installonly_limit must fail the build")


class ModuleHelperSockets(unittest.TestCase):
    """The Fedora 44 image enables the module helpers' sockets, which the Appliance Console connects to, by its preset,
    and the firewall owner's two services, its boot load and its guard; no root helper's socket is enabled by it."""

    PRESET = FEDORA_OVERLAY / "etc/systemd/system-preset/10-olivares-appliance.preset"
    MODULE_SOCKETS = {"olivares-helper-units.socket": "services", "olivares-helper-storage.socket": "storage",
                      "olivares-helper-firewall.socket": "firewall"}
    # The firewall owner's services, each with the target its [Install] section names: the boot load runs before the
    # network at every boot, and the guard reverts an unconfirmed change at its deadline.
    FIREWALL_SERVICES = {"olivares-firewall.service": "sysinit.target", "olivares-firewall-guard.service": "multi-user.target"}
    ROOT_SOCKETS = ("olivares-helper-power.socket", "olivares-helper-support-bundle.socket", "olivares-helper-cert.socket",
                    "olivares-helper-firewall-local.socket")
    # A root helper's units are the seam's, except the firewall's local entry point's, which the firewall module ships.
    ROOT_SOCKET_DIRS = {"olivares-helper-firewall-local.socket": "appliance/layer/firewall/units"}

    def preset(self):
        return [line.strip() for line in self.PRESET.read_text().splitlines() if line.strip() and not line.startswith("#")]

    def test_the_preset_enables_each_module_helper_socket_by_name(self):
        lines = self.preset()
        for socket in self.MODULE_SOCKETS:
            with self.subTest(socket=socket):
                self.assertIn("enable " + socket, lines)
        for line in lines:
            verb, _, unit = line.partition(" ")
            self.assertIn(verb, ("enable", "disable"), line)
            self.assertNotIn("*", unit, "a preset line names its unit, never a pattern: " + line)
            if verb == "enable":
                self.assertIn(unit, set(self.MODULE_SOCKETS) | set(self.FIREWALL_SERVICES),
                              "the preset enables only the module helpers' sockets and the firewall owner's two services: " + line)

    def test_the_preset_enables_the_firewall_owners_two_services_by_name(self):
        lines = self.preset()
        for service in self.FIREWALL_SERVICES:
            with self.subTest(service=service):
                self.assertEqual(lines.count("enable " + service), 1)
        enabled = [line.partition(" ")[2] for line in lines if line.startswith("enable ")]
        self.assertEqual(sorted(unit for unit in enabled if "firewall" in unit),
                         sorted(["olivares-helper-firewall.socket", *self.FIREWALL_SERVICES]),
                         "the firewall's socket and two services are enabled, and no other firewall unit")
        for other in ("nftables.service", "firewalld.service"):
            self.assertNotIn("enable " + other, lines, "a second owner of the host firewall is never enabled")

    def test_each_enabled_firewall_service_has_the_install_section_the_preset_needs(self):
        for service, target in self.FIREWALL_SERVICES.items():
            with self.subTest(service=service):
                text = (ROOT / "appliance/layer/firewall/units" / service).read_text()
                self.assertIn("\n[Install]\nWantedBy=" + target + "\n", text)
                self.assertIn("ExecStart=/usr/libexec/olivares/olivares-portal-firewall --", text)

    def test_no_root_helper_socket_or_template_is_enabled_by_the_preset(self):
        for line in self.preset():
            for socket in self.ROOT_SOCKETS:
                self.assertFalse(line.startswith("enable") and socket in line, line)
            self.assertFalse(line.startswith("enable") and "@.service" in line, line)

    def test_each_enabled_socket_has_the_install_section_the_preset_needs(self):
        for socket, module in self.MODULE_SOCKETS.items():
            with self.subTest(socket=socket):
                text = (ROOT / "appliance/layer" / module / "units" / socket).read_text()
                self.assertIn("\n[Install]\nWantedBy=sockets.target\n", text)
                self.assertIn("SocketGroup=olivares-portal\n", text)
                self.assertIn("SocketMode=0660\n", text)
        for socket in self.ROOT_SOCKETS:
            with self.subTest(socket=socket):
                text = (ROOT / self.ROOT_SOCKET_DIRS.get(socket, "appliance/layer/helpers/units") / socket).read_text()
                self.assertNotIn("[Install]", text.splitlines(), "a root helper's socket is packaged, not enabled")
                self.assertNotIn("WantedBy=sockets.target", text.splitlines())
                self.assertIn("SocketMode=0600\n", text)


class RepairConsoleHelpers(unittest.TestCase):
    """The local console starts its helpers without depending on the broken portal."""

    def unit(self):
        unit = configparser.ConfigParser(interpolation=None)
        unit.optionxform = str
        unit.read(ROOT / "appliance/layer/portal/units/olivares-repair-console.service")
        return unit

    def test_console_starts_and_orders_its_root_helper_sockets(self):
        unit = self.unit()["Unit"]
        sockets = {"olivares-helper-power.socket", "olivares-helper-cert.socket", "olivares-helper-firewall-local.socket"}
        self.assertEqual(set(unit.get("Wants", "").split()), sockets)
        self.assertEqual(set(unit.get("After", "").split()), sockets | {"getty@tty1.service"})
        for directive in ("Requires", "Requisite", "BindsTo", "PartOf", "Before"):
            self.assertFalse(unit.get(directive, ""), directive)

    def test_pam_can_audit_without_allowing_network_socket_families(self):
        service = self.unit()["Service"]
        self.assertEqual(set(service["RestrictAddressFamilies"].split()), {"AF_UNIX", "AF_NETLINK"})
        self.assertEqual(service["PrivateNetwork"], "yes")
        self.assertEqual(service["IPAddressDeny"], "any")


class VisibleName(unittest.TestCase):
    """The user-facing product name is Olivares Server (contract r3 §8). Only visible strings follow it: the image's display
    name and description, the product repository's display name and the formats' product text, which the OVA envelope
    shows. Identifiers and file names stay."""

    def test_the_image_shows_olivares_server(self):
        image = description_root()
        self.assertEqual((image.get("name"), image.get("displayname")), ("olivares-appliance", "Olivares Server"))
        self.assertEqual(image.find("description/specification").text,
                         "Olivares Server appliance, based on Fedora 44; the debian13-server-amd64 profile, not shipping, "
                         "is based on Debian 13 (trixie)")

    def test_the_product_repository_and_the_formats_show_olivares_server(self):
        repo = (FEDORA_OVERLAY / "etc/yum.repos.d/olivares.repo").read_text().splitlines()
        self.assertEqual([line for line in repo if line.startswith("name=")], ["name=Olivares Server stable"])
        self.assertIn("[olivares]", repo, "the repository id stays")
        text = (ROOT / "appliance/images/formats/formats.json").read_text()
        formats = json.loads("\n".join(line for line in text.splitlines() if not line.lstrip().startswith("//")))
        self.assertEqual(formats["product"], "Olivares Server")
        self.assertEqual(sorted(a["file"] for a in formats["artifacts"]),
                         ["olivares-appliance-desktop-amd64.iso", "olivares-appliance-desktop-amd64.ova",
                          "olivares-appliance-desktop-amd64.qcow2", "olivares-appliance-server-amd64.iso",
                          "olivares-appliance-server-amd64.ova", "olivares-appliance-server-amd64.qcow2"])
        self.assertEqual(sorted({a["edition"] for a in formats["artifacts"]}), ["desktop", "server"])
        envelope = (ROOT / "appliance/images/formats/ova/envelope-template.xml").read_text()
        self.assertIn("<Product>@PRODUCT@</Product>", envelope, "the OVA shows the formats' product text")

    def test_no_visible_olivares_ai_server_remains_in_the_image_tree(self):
        found = [str(path.relative_to(ROOT)) for path in sorted((ROOT / "appliance/images").rglob("*"))
                 if path.is_file() and "Olivares AI Server" in path.read_text(errors="replace")]
        self.assertEqual(found, [])


class FedoraRepoPin(unittest.TestCase):
    """fedora-repo-pinned.sh, the customize script KIWI runs on each Fedora .repo file it writes (repository/dnf5.py
    add_repo, then base.py run_repo_customize: `bash --norc SCRIPT FILE`)."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="a2-fedora-pin-")
        self.addCleanup(self.temp.cleanup)
        self.work = Path(self.temp.name)
        self.description = self.work / "description"
        self.description.mkdir()
        shutil.copy2(KIWI / "fedora-repo-pinned.sh", self.description / "fedora-repo-pinned.sh")
        self.tree = self.work / "mirror"
        (self.tree / "repodata").mkdir(parents=True)
        self.repomd = self.tree / "repodata/repomd.xml"
        self.repomd.write_bytes(b"<repomd>pinned</repomd>\n")
        self.key = self.work / "RPM-GPG-KEY-fedora-44-primary"
        self.key.write_bytes(b"-----BEGIN PGP PUBLIC KEY BLOCK-----\nkey\n")
        self.pin(hashlib.sha256(self.repomd.read_bytes()).hexdigest())
        self.repo = self.work / "fedora-44-updates.repo"
        self.written = ("[fedora-44-updates]\nname = fedora-44-updates\nbaseurl = %s\nrepo_gpgcheck = 0\ngpgcheck = 1\n\n"
                        % self.tree.as_uri())
        self.repo.write_text(self.written)

    def pin(self, repomd_sha256, key_sha256=None):
        lock = {"repositories": {"fedora-44-updates": {"baseurl": self.tree.as_uri() + "/", "repomd_sha256": repomd_sha256,
                                                       "recorded_at": "2026-09-27T09:57:57Z"}},
                "key": {"path": str(self.key), "sha256": key_sha256 or hashlib.sha256(self.key.read_bytes()).hexdigest(),
                        "fingerprint": FEDORA_KEY_FINGERPRINT}}
        (self.description / "fedora-repositories.json").write_text(json.dumps(lock))

    def customize(self):
        return subprocess.run(["bash", "--norc", str(self.description / "fedora-repo-pinned.sh"), str(self.repo)],
                              capture_output=True, text=True, timeout=30)

    def test_the_pinned_metadata_binds_the_repository_to_the_locked_key(self):
        result = self.customize()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.repo.read_text(), self.written.rstrip("\n") + "\ngpgkey = file://%s\n\n" % self.key)

    def test_metadata_that_moved_since_the_pin_is_refused_and_nothing_is_written(self):
        self.repomd.write_bytes(b"<repomd>a later push</repomd>\n")
        result = self.customize()
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("moved", result.stderr)
        self.assertEqual(self.repo.read_text(), self.written)

    def test_a_key_whose_digest_differs_or_that_is_missing_is_refused(self):
        for change in ("digest", "missing"):
            with self.subTest(change=change):
                if change == "digest":
                    self.pin(hashlib.sha256(self.repomd.read_bytes()).hexdigest(), key_sha256="0" * 64)
                else:
                    self.pin(hashlib.sha256(self.repomd.read_bytes()).hexdigest())
                    self.key.unlink()
                result = self.customize()
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertEqual(self.repo.read_text(), self.written)

    def test_a_repository_that_already_states_its_trust_or_turns_the_check_off_is_refused(self):
        for line in ("gpgkey = file:///etc/pki/rpm-gpg/other\n", "gpgcheck = 0\n", "sslverify = 0\n"):
            with self.subTest(line=line):
                text = self.written.replace("gpgcheck = 1\n", line if "gpgcheck" in line else "gpgcheck = 1\n" + line)
                self.repo.write_text(text)
                result = self.customize()
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertEqual(self.repo.read_text(), text)

    def test_an_unpinned_repository_or_a_file_that_is_not_a_repo_file_cannot_run(self):
        other = self.work / "fedora-44-testing.repo"
        other.write_text(self.written.replace("fedora-44-updates", "fedora-44-testing"))
        self.repo = other
        self.assertEqual(self.customize().returncode, 2)
        self.assertEqual(other.read_text(), self.written.replace("fedora-44-updates", "fedora-44-testing"))
        self.repo = self.work / "fedora-44-updates.pref"
        self.repo.write_text("x\n")
        self.assertEqual(self.customize().returncode, 2)


class InstallerMedium(unittest.TestCase):
    """iso.sh on the medium KIWI leaves: its installer initrd must carry KIWI's install modules, and the EFI boot
    image the firmware reads must carry the removable-media loader."""

    MODULES = ["systemd", "kiwi-lib", "kiwi-dump", "kiwi-dump-reboot", "rootfs-block"]
    SHIM = b"MZ shim " * 100
    # KIWI 11.0.4 writes the loaders under these names (defaults.py get_shim_loader, grub2.py
    # _setup_secure_boot_efi_image) into the medium's EFI/BOOT and into the EFI image it makes from it.
    KIWI_EFI = {"EFI/BOOT/bootx64.efi": SHIM, "EFI/BOOT/grubx64.efi": b"MZ grub", "EFI/BOOT/mmx64.efi": b"MZ mok",
                "EFI/BOOT/grub.cfg": b"search --file --set=root /boot/0x7517ab7f\n"}

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="a2-medium-")
        self.addCleanup(self.temp.cleanup)
        self.work = Path(self.temp.name)
        self.bin = self.work / "bin"
        self.bin.mkdir()
        (self.bin / "xorriso").write_text("#!" + sys.executable + "\n" + FAKE_XORRISO)
        (self.bin / "xorriso").chmod(0o755)
        self.iso_root = self.work / "iso-root"
        self.boot_images = self.work / "boot-images"
        self.boot_images.mkdir()
        self.target = self.work / "target"
        self.target.mkdir()
        (self.target / "olivares-appliance.x86_64-1.0.0.install.iso").write_text(
            str(self.iso_root) + "\n" + str(self.boot_images) + "\n")
        (self.iso_root / "EFI/BOOT").mkdir(parents=True)
        (self.iso_root / "EFI/BOOT/BOOTX64.EFI").write_bytes(b"MZ shim")
        (self.iso_root / "boot/x86_64/loader").mkdir(parents=True)
        (self.boot_images / "eltorito_img1_bios.img").write_bytes(b"\xeb\x63\x90" + b"\0" * 2045)
        self.efi_image({"EFI/BOOT/BOOTX64.EFI": b"MZ shim"})

    def efi_image(self, files):
        (self.boot_images / "eltorito_img2_uefi.img").write_bytes(fat12(files))

    def kiwi_medium(self, efi_files, tree_files=None):
        """The medium tree and the EFI image under the names given, the tree's EFI/BOOT replaced."""
        shutil.rmtree(self.iso_root / "EFI")
        for name, content in (efi_files if tree_files is None else tree_files).items():
            (self.iso_root / name).parent.mkdir(parents=True, exist_ok=True)
            (self.iso_root / name).write_bytes(content)
        self.efi_image(efi_files)

    def assemble(self, image=None, path=None, *extra):
        if image is not None:
            (self.iso_root / "boot/x86_64/loader/initrd").write_bytes(image)
        env = dict(os.environ, PATH=str(self.bin) + os.pathsep + (path or os.environ["PATH"]))
        return subprocess.run(["bash", str(ROOT / "appliance/images/formats/iso.sh"), "--target-dir", str(self.target),
                               "--output-dir", str(self.work / "out"), *extra], env=env, capture_output=True, text=True,
                              timeout=30)

    def reader(self, image, *args, path=None):
        (self.work / "initrd").write_bytes(image)
        env = dict(os.environ, PATH=path or os.environ["PATH"])
        return subprocess.run([sys.executable, str(ROOT / "appliance/images/formats/initrd_modules.py"),
                               str(self.work / "initrd"), *args], env=env, capture_output=True, text=True, timeout=20)

    def test_a_medium_whose_installer_initrd_carries_the_install_modules_is_delivered(self):
        result = self.assemble(initrd(self.MODULES))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("installer initrd carries kiwi-dump kiwi-dump-reboot", result.stdout)
        self.assertTrue((self.work / "out/olivares-appliance-server-amd64.iso").is_file())
        self.assertFalse(any(p.name.startswith(".install-initrd") for p in (self.work / "out").iterdir()))

    def test_the_assembly_names_the_editions_own_artifact_and_refuses_an_undeclared_edition(self):
        for edition, name in (("desktop", "olivares-appliance-desktop-amd64.iso"),
                              ("server", "olivares-appliance-server-amd64.iso")):
            with self.subTest(edition=edition):
                shutil.rmtree(self.work / "out", ignore_errors=True)
                result = self.assemble(initrd(self.MODULES), None, "--edition", edition)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertTrue((self.work / "out" / name).is_file())
                manifest = json.loads((self.work / "out" / (name + ".manifest.json")).read_text())
                self.assertEqual(manifest["edition"], edition)
                self.assertEqual(manifest["file"], name)
        shutil.rmtree(self.work / "out", ignore_errors=True)
        result = self.assemble(initrd(self.MODULES), None, "--edition", "workstation")
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertIn("workstation edition", result.stderr)
        self.assertEqual(list((self.work / "out").glob("olivares-appliance-*")), [])

    def test_a_medium_without_the_install_modules_is_refused(self):
        for missing in ("kiwi-dump", "kiwi-dump-reboot"):
            with self.subTest(missing=missing):
                result = self.assemble(initrd([m for m in self.MODULES if m != missing]))
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn(missing, result.stderr)
                self.assertFalse((self.work / "out/olivares-appliance-server-amd64.iso").exists())

    def test_a_medium_without_an_installer_initrd_is_refused(self):
        result = self.assemble()
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("/boot/x86_64/loader/initrd", result.stderr)

    def test_an_initrd_that_cannot_be_read_is_unmeasurable_never_a_pass(self):
        result = self.assemble(b"not an initrd at all")
        self.assertEqual(result.returncode, 2, result.stderr)

    def test_kiwi_s_loader_is_found_in_the_efi_image_in_any_case(self):
        # KIWI writes bootx64.efi. The firmware reads the El Torito EFI image, a FAT file system whose names match
        # in any case (UEFI 2.11, 13.3.1.2); the medium tree keeps the case KIWI wrote, and Rock Ridge names are exact.
        for loader in ("bootx64.efi", "BOOTx64.EFI", "BOOTX64.EFI"):
            with self.subTest(loader=loader):
                shutil.rmtree(self.work / "out", ignore_errors=True)
                files = dict(self.KIWI_EFI)
                files["EFI/BOOT/" + loader] = files.pop("EFI/BOOT/bootx64.efi")
                self.kiwi_medium(files)
                result = self.assemble(initrd(self.MODULES))
                self.assertEqual(result.returncode, 0, result.stderr)
                manifest = json.loads((self.work / "out/olivares-appliance-server-amd64.iso.manifest.json").read_text())
                self.assertEqual(manifest["boot"]["efi_loader_sha256"], hashlib.sha256(self.SHIM).hexdigest())
                self.assertFalse([p.name for p in (self.work / "out").iterdir() if p.name in (".boot-images", ".bootx64.efi")])

    def test_a_medium_whose_efi_image_lacks_the_loader_is_refused(self):
        # A loader that only the medium tree carries boots nothing, and neither does an empty one.
        without = {n: c for n, c in self.KIWI_EFI.items() if n != "EFI/BOOT/bootx64.efi"}
        cases = (("in the tree only", without, dict(without, **{"EFI/BOOT/BOOTX64.EFI": self.SHIM})),
                 ("nowhere", without, None),
                 ("empty", dict(without, **{"EFI/BOOT/BOOTX64.EFI": b""}), None))
        for case, efi_files, tree_files in cases:
            with self.subTest(case=case):
                shutil.rmtree(self.work / "out", ignore_errors=True)
                self.kiwi_medium(efi_files, tree_files)
                result = self.assemble(initrd(self.MODULES))
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn("El Torito EFI boot image", result.stderr)
                self.assertIn("BOOTX64.EFI", result.stderr)
                self.assertFalse((self.work / "out/olivares-appliance-server-amd64.iso").exists())

    def test_the_el_torito_report_and_the_efi_listing_are_logged_on_every_run(self):
        # The medium is not uploaded, so a refusal must be readable from the log alone.
        for loader, expected in ((True, 0), (False, 1)):
            with self.subTest(loader=loader):
                self.kiwi_medium({n: c for n, c in self.KIWI_EFI.items() if loader or n != "EFI/BOOT/bootx64.efi"})
                result = self.assemble(initrd(self.MODULES))
                self.assertEqual(result.returncode, expected, result.stderr)
                self.assertIn("El Torito boot img :   2  UEFI", result.stdout)
                self.assertIn("/EFI/BOOT/grubx64.efi", result.stdout)
                self.assertEqual("/EFI/BOOT/bootx64.efi" in result.stdout, loader, result.stdout)

    def test_an_efi_image_that_cannot_be_read_is_unmeasurable_never_a_pass(self):
        image = self.boot_images / "eltorito_img2_uefi.img"
        cases = (("not a FAT file system", lambda: image.write_bytes(b"\0" * 4096)),
                 ("truncated before its EFI/BOOT directory", lambda: image.write_bytes(fat12(self.KIWI_EFI)[:34 * 512])),
                 # Every file is in the first 60 sectors, but the image is shorter than its boot sector declares.
                 ("cut short after its files", lambda: image.write_bytes(fat12(self.KIWI_EFI)[:60 * 512])),
                 ("not extracted", image.unlink))
        for case, damage in cases:
            with self.subTest(case=case):
                self.kiwi_medium(self.KIWI_EFI)
                damage()
                result = self.assemble(initrd(self.MODULES))
                self.assertEqual(result.returncode, 2, result.stderr)

    def test_the_reader_takes_an_early_cpio_and_each_compression_dracut_may_choose(self):
        for compress in ("gzip", "xz"):
            for early in (False, True):
                with self.subTest(compress=compress, early=early):
                    result = self.reader(initrd(self.MODULES, compress, early), "--require", "kiwi-dump")
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(result.stdout.split(), self.MODULES)

    def test_the_reader_finds_the_list_where_lsinitrd_looks(self):
        # dracut-ng 106 writes $initdir/lib/dracut/modules.txt; lsinitrd reads lib64/dracut,
        # lib/dracut, usr/lib64/dracut and usr/lib/dracut, and so does the reader.
        for where in ("lib/dracut", "./lib/dracut", "lib64/dracut", "usr/lib64/dracut"):
            with self.subTest(where=where):
                result = self.reader(initrd(self.MODULES, where=where), "--require", "kiwi-dump-reboot")
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout.split(), self.MODULES)
        result = self.reader(initrd(self.MODULES, where="etc/dracut"), "--require", "kiwi-dump")
        self.assertEqual(result.returncode, 2, result.stderr)

    def test_a_truncated_main_archive_is_unreadable_never_a_short_list(self):
        # A main archive that ends before its compressed stream does is unreadable, even when the part that is
        # there already holds the module list: the reader says so and answers 2.
        for compress in ("gzip", "xz"):
            with self.subTest(compress=compress):
                whole = initrd(self.MODULES, compress)
                result = self.reader(whole[:len(whole) * 2 // 3], "--require", "kiwi-dump")
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertIn("truncated", result.stderr)

    def test_the_reader_answers_two_when_it_cannot_measure(self):
        # zstd with no zstd program on PATH, a cpio with no module list, and bytes that are neither.
        empty = self.work / "empty-path"
        empty.mkdir()
        cases = ((initrd(self.MODULES, "zstd"), str(empty), "zstd"),
                 (__import__("gzip").compress(newc([("usr/bin/sh", b"")]), mtime=0), None, "modules.txt"),
                 (b"\x00" * 16 + b"garbage", None, "offset"))
        for image, path, said in cases:
            with self.subTest(said=said):
                result = self.reader(image, "--require", "kiwi-dump", path=path)
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertIn(said, result.stderr)


# An emulated QEMU for boot-battery.sh: the guest is the directory of its -serial file, and FAKE_GUEST names a JSON
# file {guest: [[seconds, line], ...]} of what that guest prints, and when; the line __EXIT__ ends the guest by itself, as a
# guest reset under -no-reboot ends QEMU. With FAKE_INSTALL_WRITES=1 a guest with the installer medium attached (ide-cd)
# writes a GPT header at byte 512 of its virtio disk, as the installer writes the disk image. Like QEMU it holds a write
# lock on each writable disk while it runs, so a read of that disk fails until the guest has exited. It then idles until the
# battery stops it.
FAKE_QEMU = r'''
import fcntl, json, os, sys, time
args = sys.argv[1:]
log = next(a for a in args if a.startswith("file:") and a.endswith("serial.log"))[5:]
guest = os.path.basename(os.path.dirname(log))
held = []
for a in args:
    if a.startswith("if=virtio,format=qcow2,file=") and "snapshot=on" not in a:
        f = open(a.split("file=", 1)[1], "r+b")
        fcntl.lockf(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
        held.append(f)
if os.environ.get("FAKE_INSTALL_WRITES") == "1" and "ide-cd,drive=medium,bootindex=0" in args:
    held[0].seek(512)
    held[0].write(b"EFI PART" + b"\0" * 504)
    held[0].flush()
start = time.monotonic()
for at, line in json.load(open(os.environ["FAKE_GUEST"])).get(guest, []):
    time.sleep(max(0, start + at - time.monotonic()))
    if line == "__EXIT__":
        sys.exit(0)
    with open(log, "a") as f:
        f.write(line + "\n")
time.sleep(600)
'''

# An emulated qemu-img with the semantics of QEMU's img_dd (qemu-img.c, v8.2.2 :5078-5328, the same in v10.0.0; r10-act-9b/
# sources/): `count` bounds the copy as an absolute input offset (size = min(length, count * bs)), the output is created
# with size - skip * bs bytes (0 when skip * bs passes size, after "cannot skip to specified offset"), and the copy runs from
# skip * bs to size. So `bs=512 skip=1 count=1` writes an empty output with exit 0, as Debian's 10.0.13 and Ubuntu's 8.2.2
# do (r11/c4b/REAL-MATRIX.tsv). Opening an input another process holds a write lock on fails, as img_open without
# --force-share does. `create` writes a small blank file.
FAKE_QEMU_IMG = r'''
import fcntl, sys
args = sys.argv[1:]
if args[0] == "create":
    open(args[-2], "wb").truncate(1024 * 1024)
    sys.exit(0)
if args[0] == "dd":
    opts = dict(a.split("=", 1) for a in args if "=" in a)
    bs = int(opts.get("bs", 512))
    try:
        f = open(opts["if"], "rb")
        fcntl.lockf(f, fcntl.LOCK_SH | fcntl.LOCK_NB)
    except OSError:
        print("qemu-img: Could not open '%s': Failed to get shared \"write\" lock" % opts["if"], file=sys.stderr)
        sys.exit(1)
    data = f.read()
    size = len(data)
    if "count" in opts and int(opts["count"]) * bs < size:
        size = int(opts["count"]) * bs
    skip = int(opts.get("skip", 0))
    if "skip" in opts and size < skip * bs:
        print("qemu-img: %s: cannot skip to specified offset" % opts["if"], file=sys.stderr)
        start = size
    else:
        start = skip * bs
    open(opts["of"], "wb").write(data[start:size])
    sys.exit(0)
sys.exit(3)
'''

SERIAL_MULTI_USER = "[  OK  ] Reached target multi-user.target - Multi-User System."
# The label agetty prints on every console: the first line of the host layer's banner, without its hostname escape.
BANNER = ROOT / "appliance/layer/base/units/tty1-banner.txt"
BANNER_LABEL = re.sub(r"\s*\\n\s*$", "", BANNER.read_text().splitlines()[0])
SERIAL_LABEL = BANNER_LABEL + " localhost"
SERIAL_SECURE_BOOT = "[    0.000000] secureboot: Secure boot enabled"


class ConsoleLabel(unittest.TestCase):
    """The boot battery and the NoCloud probe wait for the console label the host layer delivers, read from its own
    banner (appliance/layer/base/units/tty1-banner.txt) and never kept as a second literal; battery-selftest.sh holds it."""

    def label_of(self, banner):
        return subprocess.run(["bash", "-c", '. "$1"; banner_label "$2"', "-", str(ROOT / "appliance/test/lib/console-label.sh"),
                               str(banner)], capture_output=True, text=True, timeout=10)

    def test_the_label_is_the_layer_s_banner_line(self):
        # The composed host line's banner reads "Olivares Server \n" (agetty puts the host name after the label).
        self.assertEqual(BANNER_LABEL, "Olivares Server")
        result = self.label_of(BANNER)
        self.assertEqual((result.returncode, result.stdout), (0, "Olivares Server\n"), result.stderr)
        with tempfile.TemporaryDirectory(prefix="a2-banner-") as temp:
            other = Path(temp) / "banner.txt"
            other.write_text("Another Name \\n\nsecond line\n")
            self.assertEqual(self.label_of(other).stdout, "Another Name\n")
            other.write_text("")
            self.assertEqual(self.label_of(other).returncode, 1, "an empty banner names no label")

    def test_the_batteries_read_the_banner_and_keep_no_literal(self):
        for script in ("boot-battery.sh", "nocloud-probe.sh"):
            with self.subTest(script=script):
                text = (ROOT / "appliance/test" / script).read_text()
                self.assertIn("lib/console-label.sh", text)
                self.assertIn("appliance/layer/base/units/tty1-banner.txt", text)
                for old in ("Olivares AI appliance", "'Olivares Server'", '"Olivares Server"'):
                    self.assertNotIn(old, text)

    def test_battery_selftest_holds_the_label(self):
        with tempfile.TemporaryDirectory(prefix="a2-selftest-", dir=os.environ.get("TMPDIR")) as temp:
            result = subprocess.run(["bash", str(ROOT / "appliance/test/battery-selftest.sh"), temp], capture_output=True,
                                    text=True, timeout=120)
        self.assertEqual(result.returncode, 0, result.stdout[-3000:])
        for name in ("battery_reads_the_console_label_of_the_delivered_banner",
                     "probe_reads_the_console_label_of_the_delivered_banner"):
            self.assertIn("PASS " + name, result.stdout)


class BootBattery(unittest.TestCase):
    """boot-battery.sh driven with an emulated QEMU: what each case waits for and what its assertions read."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="a2-battery-")
        self.addCleanup(self.temp.cleanup)
        self.work = Path(self.temp.name)
        self.bin = self.work / "bin"
        self.bin.mkdir()
        for name, body in (("qemu", FAKE_QEMU), ("qemu-img", FAKE_QEMU_IMG)):
            (self.bin / name).write_text("#!" + sys.executable + "\n" + body)
            (self.bin / name).chmod(0o755)
        self.ovmf = self.work / "ovmf"
        self.ovmf.mkdir()
        for name in ("OVMF_CODE_4M.fd", "OVMF_VARS_4M.fd", "OVMF_CODE_4M.secboot.fd", "OVMF_VARS_4M.ms.fd"):
            (self.ovmf / name).write_bytes(b"\0" * 64)
        self.artifacts = self.work / "artifacts"
        self.artifacts.mkdir()
        text = (ROOT / "appliance/images/formats/formats.json").read_text()
        for artifact in json.loads("\n".join(l for l in text.splitlines() if not l.lstrip().startswith("//")))["artifacts"]:
            (self.artifacts / artifact["file"]).write_bytes(b"\0" * 4096)

    def battery(self, guests, *cases, deadline=20, install_writes=True, script=None):
        (self.work / "guests.json").write_text(json.dumps(guests))
        evidence = self.work / "evidence"
        shutil.rmtree(evidence, ignore_errors=True)
        env = dict(os.environ, APPLIANCE_BOOT_ACCEL="tcg", APPLIANCE_QEMU=str(self.bin / "qemu"),
                   APPLIANCE_QEMU_IMG=str(self.bin / "qemu-img"), APPLIANCE_OVMF_DIR=str(self.ovmf),
                   APPLIANCE_BOOT_TIMEOUT=str(deadline), APPLIANCE_INSTALL_TIMEOUT=str(deadline),
                   FAKE_GUEST=str(self.work / "guests.json"), FAKE_INSTALL_WRITES="1" if install_writes else "0")
        result = subprocess.run(["bash", str(script or ROOT / "appliance/test/boot-battery.sh"), str(self.artifacts),
                                 str(evidence), *cases], env=env, capture_output=True, text=True, timeout=deadline * 4 + 60)
        return result, evidence

    def test_the_preflight_reads_the_named_editions_artifacts(self):
        desktop_only = self.work / "artifacts-desktop"
        desktop_only.mkdir()
        for artifact in (self.artifacts / name for name in ("olivares-appliance-desktop-amd64.qcow2",
                                                            "olivares-appliance-desktop-amd64.iso")):
            shutil.copy(artifact, desktop_only / artifact.name)
        battery = ["bash", str(ROOT / "appliance/test/boot-battery.sh"), "--check-preflight", str(desktop_only)]
        env = dict(os.environ, APPLIANCE_BOOT_ACCEL="tcg", APPLIANCE_QEMU=str(self.bin / "qemu"),
                   APPLIANCE_QEMU_IMG=str(self.bin / "qemu-img"), APPLIANCE_OVMF_DIR=str(self.ovmf))
        wrong = subprocess.run(battery, env=env, capture_output=True, text=True, timeout=30)
        self.assertEqual(wrong.returncode, 2, "the server edition's artifacts are not there")
        self.assertIn("olivares-appliance-server-amd64.qcow2", wrong.stdout + wrong.stderr)
        right = subprocess.run(battery, env=dict(env, APPLIANCE_EDITION="desktop"), capture_output=True,
                               text=True, timeout=30)
        self.assertEqual(right.returncode, 0, right.stdout + right.stderr)

    def test_a_boot_case_waits_for_the_console_label_it_asserts(self):
        # Hosted run 36259628735: the bios guest reached multi-user in 74 s and was stopped there, while first boot was
        # starting; the label prints when the serial getty runs, after first boot's jobs. The case must read the label.
        result, evidence = self.battery({"bios": [[0, SERIAL_MULTI_USER], [5, SERIAL_LABEL]]}, "bios")
        self.assertIn("PASS bios_reaches_multi_user_target", result.stdout)
        self.assertIn("PASS bios_shows_the_console_label", result.stdout, result.stdout)
        self.assertGreaterEqual(int((evidence / "bios/seconds").read_text()), 4)

    # What KIWI's installer guest printed in hosted runs 36259628735 and 36270893718 (V10 serial.log lines 1-66, 67 and
    # 975), in its order: the medium's dracut install with /run/install mounted, then no firmware and no GRUB but a second
    # kernel, the installed system kexec'd by kiwi-dump-reboot-system.sh (KIWI 11.0.4) once the dump and its check passed,
    # up to its login prompt. The kernel line is the installer's hand-over.
    MEDIUM = [[0, "Loading initrd..."],
              [0, "[   30.139873] dracut-pre-mount[718]: mount: /run/install: WARNING: source write-protected, "
                  "mounted read-only."]]
    HAND_OVER = [2, "[    0.000000] Linux version 6.12.107+deb13-amd64 (debian-kernel@lists.debian.org) "
                    "(x86_64-linux-gnu-gcc-14 (Debian 14.2.0-19) 14.2.0, GNU ld (GNU Binutils for Debian) 2.44) #1 SMP"]
    INSTALLER = MEDIUM + [HAND_OVER, [3, "[   23.023254] systemd[1]: Installed transient /etc/machine-id file."],
                          [4, "localhost login: "]]
    INSTALLED = [[0, SERIAL_SECURE_BOOT], [0, SERIAL_MULTI_USER], [1, SERIAL_LABEL]]

    def test_the_installer_assertion_reads_the_disk_it_wrote(self):
        # Hosted run 36259628735: "FAIL control:iso_medium_ran_the_installer accepted counterfeit evidence".
        result, _ = self.battery({"iso-install": self.INSTALLER, "iso-install-boot": self.INSTALLED}, "iso-install",
                                 deadline=6)
        self.assertIn("PASS iso_medium_ran_the_installer", result.stdout, result.stdout)
        self.assertIn("PASS control:iso_medium_ran_the_installer", result.stdout, result.stdout)

    def test_an_installer_that_wrote_nothing_is_refused(self):
        # The same serial log, and a target disk the installer never wrote: no partition table.
        result, _ = self.battery({"iso-install": self.INSTALLER, "iso-install-boot": self.INSTALLED}, "iso-install",
                                 deadline=6, install_writes=False)
        self.assertIn("FAIL iso_medium_ran_the_installer", result.stdout, result.stdout)

    def test_the_installer_case_ends_on_the_hand_over_to_the_installed_system(self):
        # Hosted run 36270893718: the installer guest sat at `localhost login:` until its 1,800 s deadline, since none of
        # the old markers appears when kiwi-dump kexecs into the installed system. The case ends on the hand-over.
        result, evidence = self.battery({"iso-install": self.INSTALLER, "iso-install-boot": self.INSTALLED}, "iso-install",
                                        deadline=30)
        self.assertIn("PASS iso_medium_ran_the_installer", result.stdout, result.stdout)
        self.assertLessEqual(int((evidence / "iso-install/seconds").read_text()), 10)
        self.assertRegex((evidence / "iso-install/qemu-exit.txt").read_text(), r"(?m)^ended: hand-over$")

    def test_a_guest_stopped_at_the_deadline_is_not_a_finished_installer(self):
        # A written GPT header without the hand-over is a partial installer: the dump writes LBA 0 and 1 first.
        result, evidence = self.battery({"iso-install": self.MEDIUM, "iso-install-boot": self.INSTALLED}, "iso-install",
                                        deadline=6)
        self.assertIn("FAIL iso_medium_ran_the_installer", result.stdout, result.stdout)
        self.assertRegex((evidence / "iso-install/qemu-exit.txt").read_text(), r"(?m)^ended: deadline$")

    def test_a_guest_that_exits_before_the_hand_over_is_not_a_finished_installer(self):
        # A failed dump or check ends in `reboot -f` (kiwi-dialog-lib.sh report_and_quit), which ends QEMU under
        # -no-reboot: an exit of the guest is no end of the installer.
        result, evidence = self.battery({"iso-install": self.MEDIUM + [[1, "__EXIT__"]], "iso-install-boot": self.INSTALLED},
                                        "iso-install", deadline=20)
        self.assertIn("FAIL iso_medium_ran_the_installer", result.stdout, result.stdout)
        self.assertRegex((evidence / "iso-install/qemu-exit.txt").read_text(), r"(?m)^ended: exited$")

    def test_a_kernel_line_before_the_installer_ran_is_no_hand_over(self):
        # Only a kernel that starts after the installer mounted its medium is the installed system.
        result, _ = self.battery({"iso-install": [[0, self.HAND_OVER[1]], [1, self.MEDIUM[1][1]]],
                                  "iso-install-boot": self.INSTALLED}, "iso-install", deadline=6)
        self.assertIn("FAIL iso_medium_ran_the_installer", result.stdout, result.stdout)

    def test_the_emulated_qemu_img_reads_as_img_dd_does(self):
        # The fixture stands for qemu-img, so it must answer as qemu-img answers (r11/c4b/REAL-MATRIX.tsv, both versions).
        disk, out = self.work / "gpt.qcow2", self.work / "out.raw"
        disk.write_bytes(b"\0" * 512 + b"EFI PART" + b"\0" * (2048 * 512 - 520))
        img = [str(self.bin / "qemu-img"), "dd", "-f", "qcow2", "-O", "raw"]
        old = subprocess.run(img + ["bs=512", "skip=1", "count=1", "if=%s" % disk, "of=%s" % out], capture_output=True)
        self.assertEqual((old.returncode, out.stat().st_size), (0, 0))
        new = subprocess.run(img + ["bs=512", "count=2", "if=%s" % disk, "of=%s" % out], capture_output=True)
        self.assertEqual((new.returncode, out.stat().st_size, out.read_bytes()[512:520]), (0, 1024, b"EFI PART"))

    def test_the_disk_read_is_recorded_with_its_exit_stderr_and_identity(self):
        # The read's exit and stderr, and the target's path, inode, size and sha256 before and after it, are evidence.
        result, evidence = self.battery({"iso-install": self.INSTALLER, "iso-install-boot": self.INSTALLED}, "iso-install",
                                        deadline=6)
        record = (evidence / "iso-install/installed.qcow2.read.txt").read_text()
        self.assertIn("bs=512 count=2", record)
        self.assertRegex(record, r"(?m)^exit: 0$")
        self.assertRegex(record, r"(?m)^stderr: ")
        before = re.search(r"(?m)^before: (.*)$", record).group(1)
        after = re.search(r"(?m)^after: (.*)$", record).group(1)
        self.assertRegex(before, r"inode \d+ bytes \d+ sha256 [0-9a-f]{64}")
        self.assertEqual(before, after)

    def test_a_predicate_that_trusts_the_serial_log_is_caught_by_its_control(self):
        # The mutant: the r9 predicate, reading the log again. The control must then refuse it.
        mutant = self.work / "repo/appliance/test/boot-battery.sh"
        mutant.parent.mkdir(parents=True)
        (self.work / "repo/appliance/images/formats").mkdir(parents=True)
        shutil.copy(ROOT / "appliance/images/formats/formats.json", self.work / "repo/appliance/images/formats/")
        # The battery reads its console label from the layer's banner through lib/console-label.sh.
        for path in ("appliance/test/lib/console-label.sh", "appliance/layer/base/units/tty1-banner.txt"):
            (self.work / "repo" / path).parent.mkdir(parents=True, exist_ok=True)
            shutil.copy(ROOT / path, self.work / "repo" / path)
        text = (ROOT / "appliance/test/boot-battery.sh").read_text()
        marker = "# ---- the fixture"
        self.assertEqual(text.count(marker), 1)
        mutant.write_text(text.replace(marker, "installed_the_disk() { grep -Eqi 'kiwi|install|dump' "
                                              "\"$evidence/iso-install/serial.log\"; }\n" + marker))
        result, _ = self.battery({"iso-install": self.INSTALLER, "iso-install-boot": self.INSTALLED}, "iso-install",
                                 deadline=6, script=mutant)
        self.assertIn("FAIL control:iso_medium_ran_the_installer accepted counterfeit evidence", result.stdout,
                      result.stdout)

    def test_a_guest_that_never_shows_the_label_fails_at_the_deadline(self):
        # The control of the wait: it is bounded, and a label that never comes is a failure, not a longer wait.
        result, evidence = self.battery({"bios": [[0, SERIAL_MULTI_USER]]}, "bios", deadline=8)
        self.assertIn("FAIL bios_shows_the_console_label", result.stdout)
        self.assertEqual(result.returncode, 1)
        self.assertGreaterEqual(int((evidence / "bios/seconds").read_text()), 8)


class FailureEvidence(unittest.TestCase):
    """What a failed job keeps: every upload runs on failure too, and the image upload stays under a byte budget."""

    BUDGET_CEILING = 3 * 1024 ** 3  # three formats of about 0.5 GB each were measured in hosted run 36259628735

    def steps(self):
        return workflow()["jobs"]["image"]["steps"]

    def test_every_upload_runs_when_the_job_fails(self):
        # Hosted run 36259628735: the boot phase failed, the image upload had no `if:` and was skipped, and the disk
        # images a UEFI failure is diagnosed from were gone with the runner.
        uploads = [s for s in self.steps() if "actions/upload-artifact" in s.get("uses", "")]
        self.assertEqual(len(uploads), 2)
        for step in uploads:
            with self.subTest(step=step.get("name")):
                self.assertIn("always()", str(step.get("if", "")))

    def budget_step(self):
        steps = self.steps()
        images = next(s for s in steps if "appliance-image-artifacts" in s.get("with", {}).get("name", ""))
        m = re.search(r"steps\.([\w-]+)\.outputs\.upload == 'true'", str(images.get("if", "")))
        self.assertIsNotNone(m, "the image upload is not gated by a byte budget")
        budget = next((s for s in steps if s.get("id") == m.group(1)), None)
        self.assertIsNotNone(budget)
        self.assertLess(steps.index(budget), steps.index(images))
        self.assertIn("always()", str(budget.get("if", "")))
        return budget, images

    def run_budget(self, budget, sizes, limit):
        work = Path(tempfile.mkdtemp(prefix="a2-budget-"))
        self.addCleanup(shutil.rmtree, work, True)
        (work / "dist/appliance").mkdir(parents=True)
        for name, size in sizes.items():
            with open(work / "dist/appliance" / name, "wb") as f:
                f.truncate(size)
        output = work / "github-output"
        output.write_text("")
        env = dict(os.environ, GITHUB_OUTPUT=str(output), **{k: str(v) for k, v in budget.get("env", {}).items()})
        env["BUDGET_BYTES"] = str(limit)
        subprocess.run(["bash", "-c", budget["run"]], cwd=work, env=env, check=True, capture_output=True, timeout=30)
        return dict(l.split("=", 1) for l in output.read_text().splitlines() if "=" in l).get("upload")

    def test_the_image_upload_is_held_to_a_byte_budget(self):
        budget, images = self.budget_step()
        self.assertLessEqual(int(budget["env"]["BUDGET_BYTES"]), self.BUDGET_CEILING)
        names = [Path(p.strip()).name for p in images["with"]["path"].splitlines() if p.strip()]
        self.assertEqual(self.run_budget(budget, {n: 100 for n in names}, 1000), "true")
        # The controls: over the budget, and a job that failed before any image existed, upload nothing.
        self.assertEqual(self.run_budget(budget, {n: 600 for n in names}, 1000), "false")
        self.assertEqual(self.run_budget(budget, {}, 1000), "false")


class PhaseEvidence(unittest.TestCase):
    """The attempt's phase runner and measurements, driven with small real processes."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="a2-phase-")
        self.addCleanup(self.temp.cleanup)
        self.work = Path(self.temp.name)
        self.module = load_attempt("recipe_phases")

    def test_interrupted_phase_keeps_its_streamed_output_and_record(self):
        pidfile = self.work / "pid"
        log = self.work / "boot.log"

        def interrupt(signum, frame):
            raise KeyboardInterrupt("recipe interrupted")
        previous = signal.signal(signal.SIGALRM, interrupt)
        self.addCleanup(signal.signal, signal.SIGALRM, previous)
        signal.setitimer(signal.ITIMER_REAL, 1.0)
        with self.assertRaises(KeyboardInterrupt):
            self.module.run_command(["bash", "-c", "echo first line; echo $$ > " + str(pidfile) + "; exec sleep 30"],
                                    log, 20)
        signal.setitimer(signal.ITIMER_REAL, 0)
        self.assertTrue(log.exists() and "first line" in log.read_text(), "partial output lost")
        record = json.loads(Path(str(log) + ".json").read_text())
        self.assertIs(record.get("interrupted"), True)
        self.assertLess(record.get("duration_seconds", 99), 10)
        self.assertLessEqual(record.get("started_at", "~"), record.get("finished_at", ""))
        with self.assertRaises(ProcessLookupError):
            os.kill(int(pidfile.read_text()), 0)

    def test_phase_log_is_bounded_and_keeps_its_end(self):
        log = self.work / "build.log"
        with patch.object(self.module, "LOG_HEAD", 1000, create=True), \
             patch.object(self.module, "LOG_TAIL", 100, create=True):
            code = self.module.run_command(
                ["bash", "-c", "head -c 5000 /dev/zero | tr '\\0' x; echo; echo END-OF-PHASE"], log, 20)
        self.assertEqual(code, 0)
        text = log.read_text()
        self.assertLessEqual(len(text.encode()), 1000 + 100 + 200)
        self.assertIn("END-OF-PHASE", text)
        self.assertGreater(json.loads(Path(str(log) + ".json").read_text()).get("dropped_bytes", 0), 0)

    def test_attempt_records_phase_times_and_sampled_disk(self):
        evidence = self.work / "attempt"

        def preflight(directory, argv):
            Path(directory).mkdir(parents=True)
            (Path(directory) / "receipt.json").write_text(json.dumps(receipt()))
            return 0

        def phase(argv, log, timeout):
            time.sleep(0.12)
            return 0
        output = io.StringIO()
        with patch.dict(os.environ, ENV), patch.object(self.module, "execute_preflight", preflight), \
             patch.object(self.module, "run_command", phase), \
             patch.object(self.module, "cleanup_containers", lambda *args: True), \
             patch.object(self.module, "SAMPLE_SECONDS", 0.05, create=True), contextlib.redirect_stdout(output):
            code = self.module.execute(evidence, "tcg", self.work / "debs", self.work / "target", "nonfree")
        self.assertEqual(code, 0)
        result = json.loads((evidence / "recipe-result.json").read_text())
        phases = result.get("phases", [])
        self.assertEqual([p["name"] for p in phases],
                         ["preflight", "build", "format-iso", "format-qcow2", "format-ova", "boot", "nocloud"])
        for phase_record in phases:
            self.assertGreaterEqual(phase_record["duration_seconds"], 0)
            self.assertLessEqual(phase_record["started_at"], phase_record["finished_at"])
        disk = result.get("disk", {})
        self.assertEqual(disk.get("interval_seconds"), 0.05)
        self.assertGreaterEqual(disk.get("samples", 0), 2)
        self.assertIn("lower bound", disk.get("limitation", ""))
        self.assertGreaterEqual(len((evidence / "disk-samples.jsonl").read_text().splitlines()), 2)
        self.assertIn("phase=build", output.getvalue())


class ReleaseCustody(unittest.TestCase):
    """The real release owner and child cleanup, against an inert daemon and the real admit.py."""

    CHILDREN = {"stage.cid": "5" * 64, "build.cid": "6" * 64}

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="a2-release-")
        self.addCleanup(self.temp.cleanup)
        self.evidence = Path(self.temp.name) / "attempt"
        self.module = load_attempt("recipe_release_custody")
        self.daemon = Daemon(images={IMAGE: ATTEMPT})
        self.interrupt = False

    def children(self):
        """What build.sh's two docker runs leave: exact cidfiles and labeled, stopped containers."""
        work = self.evidence / "recipe-work"
        work.mkdir(parents=True)
        for name, container in self.CHILDREN.items():
            (work / name).write_text(container + "\n")
            self.daemon.containers[container] = {"label": ATTEMPT, "running": name == "build.cid"}

    def command(self, argv, log, timeout):
        """run_command's inert stand-in: phases succeed; a release runs the real admit.py in-process."""
        argv = [str(part) for part in argv]
        if self.interrupt:
            raise KeyboardInterrupt("recipe interrupted during release")
        with open(log, "x") as handle:  # a log is evidence and is never overwritten
            owner = [i for i, a in enumerate(argv) if a.endswith("/runner/admit.py")]
            if not owner:
                if any(a.endswith("/kiwi/build.sh") for a in argv):
                    self.children()
                handle.write("inert phase\n")
                return 0
            output = io.StringIO()
            with patch.object(admit, "run", self.daemon), contextlib.redirect_stdout(output):
                code = admit.main(argv[owner[0] + 1:])
            handle.write(output.getvalue())
            return code

    def attempt(self):
        """The workflow's attempt step: the real execute() with an inert preflight and phases."""
        def preflight(directory, argv):
            Path(directory).mkdir(parents=True)
            (Path(directory) / "receipt.json").write_text(json.dumps(receipt()))
            return 0
        with patch.dict(os.environ, ENV), patch.object(self.module, "run", self.daemon), \
             patch.object(self.module, "run_command", self.command), \
             patch.object(self.module, "execute_preflight", preflight), contextlib.redirect_stdout(io.StringIO()):
            return self.module.execute(self.evidence, "tcg", self.temp.name, Path(self.temp.name) / "target", "nonfree")

    def always_step(self):
        """The workflow's always step: attempt.py --release-only through its real main()."""
        try:
            with patch.dict(os.environ, ENV), patch.object(self.module, "run", self.daemon), \
                 patch.object(self.module, "run_command", self.command), \
                 patch.object(sys, "argv", ["attempt.py", "--evidence", str(self.evidence), "--release-only"]), \
                 contextlib.redirect_stdout(io.StringIO()):
                return self.module.main()
        except KeyboardInterrupt:
            self.fail("an interruption escaped the release owner without a record")

    def cleanup(self):
        with patch.object(self.module, "run", self.daemon):
            return self.module.cleanup_containers(self.evidence, ATTEMPT)

    def test_cleanup_retires_only_recorded_children(self):
        self.children()
        self.assertTrue(self.cleanup())
        self.assertEqual(sorted(self.daemon.removed()), sorted(self.CHILDREN.values()))
        self.assertEqual(self.daemon.containers, {})
        # Repeated: the recorded children are already absent; nothing else is touched.
        self.daemon.calls.clear()
        self.assertTrue(self.cleanup())
        self.assertEqual(self.daemon.removed(), [])

    def test_unknown_labeled_child_is_refused_without_deletion(self):
        self.children()
        self.daemon.containers["7" * 64] = {"label": ATTEMPT, "running": False}
        self.assertFalse(self.cleanup())
        self.assertEqual(self.daemon.removed(), [])
        self.assertEqual(len(self.daemon.containers), 3)

    def test_stale_or_foreign_cidfile_is_refused_without_deletion(self):
        self.children()
        cases = {"stale": "c" * 10 + "\n", "foreign": "8" * 64 + "\n"}
        for case, content in cases.items():
            with self.subTest(case=case):
                # The stage container is gone; its cidfile is partial, or names another owner's.
                self.daemon = Daemon(containers={"8" * 64: {"label": "f" * 32, "running": False},
                                                 "6" * 64: {"label": ATTEMPT, "running": False}})
                (self.evidence / "recipe-work/stage.cid").write_text(content)
                self.assertFalse(self.cleanup())
                self.assertEqual(self.daemon.removed(), [])
                self.assertEqual(len(self.daemon.containers), 2)

    def test_first_release_then_duplicate_clean_release_keeps_every_record(self):
        self.assertEqual(self.attempt(), 0)
        first = snapshot(self.evidence)
        for name in ("cleanup.json", "release.log", "prerequisite/builder-release.json", "SHA256SUMS"):
            self.assertIn(name, first)
        self.assertEqual(json.loads(first["cleanup.json"])["state"], "released")
        code = self.always_step()
        self.assertEqual(rewritten(first, self.evidence), [])
        self.assertEqual(code, 0)
        recovery = json.loads((self.evidence / "recovery/001/cleanup.json").read_text())
        self.assertEqual(recovery["state"], "released")
        self.assertEqual(self.daemon.removed().count(IMAGE), 1, "the image is removed once")
        self.assertEqual(unverified(self.evidence), [])

    def test_failed_release_is_kept_byte_identical_beside_its_successful_recovery(self):
        self.daemon.fail.add(("image", "rm"))
        self.assertEqual(self.attempt(), 1)
        failed = snapshot(self.evidence)
        self.assertEqual(json.loads(failed["cleanup.json"])["state"], "defect")
        self.assertEqual(json.loads(failed["prerequisite/builder-release.json"])["state"], "defect")
        self.daemon.fail.clear()
        code = self.always_step()
        self.assertEqual(rewritten(failed, self.evidence), [])
        self.assertEqual(code, 0)
        record = self.evidence / "recovery/001"
        self.assertEqual(json.loads((record / "builder-release.json").read_text())["state"], "released")
        self.assertEqual(json.loads((record / "cleanup.json").read_text())["state"], "released")
        self.assertEqual((record / "receipt.json").read_bytes(), failed["prerequisite/receipt.json"])
        self.assertNotIn(IMAGE, self.daemon.images)
        self.assertEqual(unverified(self.evidence), [])
        self.assertTrue((record / "SHA256SUMS").is_file(), "a completed recovery record is sealed")
        self.assertIn("  SHA256SUMS", (record / "SHA256SUMS").read_text(), "a record chains to the attempt manifest")

    def test_interrupted_recovery_stays_and_the_next_one_is_numbered_after_it(self):
        self.daemon.fail.add(("image", "rm"))
        self.assertEqual(self.attempt(), 1)
        failed = snapshot(self.evidence)
        self.daemon.fail.clear()
        self.interrupt = True
        self.assertEqual(self.always_step(), 1)
        self.assertEqual(rewritten(failed, self.evidence), [])
        interrupted = json.loads((self.evidence / "recovery/001/cleanup.json").read_text())
        self.assertEqual(interrupted["state"], "defect")
        self.assertIn("interrupted", interrupted["reason"])
        # What a hard-killed recovery leaves behind: a claimed record with no outcome.
        killed = self.evidence / "recovery/002"
        killed.mkdir()
        (killed / "release.log").write_text("partial output of a killed release\n")
        kept = snapshot(self.evidence)
        self.interrupt = False
        code = self.always_step()
        self.assertEqual(rewritten(kept, self.evidence), [])
        self.assertEqual(code, 0)
        self.assertEqual(json.loads((self.evidence / "recovery/003/cleanup.json").read_text())["state"], "released")
        self.assertFalse((killed / "SHA256SUMS").exists())
        self.assertEqual(unverified(self.evidence), [])
        self.assertTrue((self.evidence / "recovery/003/SHA256SUMS").is_file(), "a completed recovery record is sealed")
        chain = (self.evidence / "recovery/003/SHA256SUMS").read_text()
        self.assertIn("  recovery/001/SHA256SUMS", chain, "the latest record commits to the one before it")

    def test_untyped_custody_proves_nothing_and_is_recorded(self):
        cases = {
            "integers": {"image_retained": 0, "image_removed": 1, "cleaned": True},
            "strings": {"image_retained": "false", "image_removed": "true", "cleaned": "true"},
            "none": {"image_retained": None, "image_removed": True, "cleaned": True},
            "missing": {"image_removed": True, "cleaned": True},
            "cleaned-integer": {"image_retained": False, "image_removed": True, "cleaned": 1},
            "list": [],
            "string": "released",
            "binding-list": None,
        }
        for case, custody in cases.items():
            with self.subTest(case=case):
                self.evidence = Path(self.temp.name) / case
                (self.evidence / "prerequisite").mkdir(parents=True)
                record = receipt()
                if case == "binding-list":
                    record["binding"] = []
                elif isinstance(custody, dict):
                    record["observations"]["container_toolchain"] = dict(custody, image_id="")
                else:
                    record["observations"]["container_toolchain"] = custody
                (self.evidence / "prerequisite/receipt.json").write_text(json.dumps(record))
                self.daemon = Daemon(images={IMAGE: ATTEMPT})
                # The always step twice: the first record and the attempt seal, then a recovery.
                for number, cleanup in ((1, "cleanup.json"), (2, "recovery/001/cleanup.json")):
                    try:
                        code = self.always_step()
                    except Exception as error:
                        self.fail("untyped custody escaped without a record: %r" % error)
                    self.assertEqual(code, 1, (number, case))
                    outcome = json.loads((self.evidence / cleanup).read_text())
                    self.assertEqual(outcome["state"], "defect")
                    self.assertIn("receipt.type", outcome["reason"])
                self.assertEqual(self.daemon.calls, [], "an untyped receipt proves no custody to act on")
                self.assertTrue((self.evidence / "recovery/001/SHA256SUMS").is_file())
                self.assertEqual(unverified(self.evidence), [])

    def test_a_retained_builder_is_released_without_reading_cleaned(self):
        # cleaned decides custody only for a builder the prerequisite did not retain; a
        # retained builder's release is the prerequisite's own exact-ID removal.
        (self.evidence / "prerequisite").mkdir(parents=True)
        record = receipt()
        del record["observations"]["container_toolchain"]["cleaned"]
        (self.evidence / "prerequisite/receipt.json").write_text(json.dumps(record))
        self.children()
        self.assertEqual(self.always_step(), 0)
        self.assertEqual(json.loads((self.evidence / "cleanup.json").read_text())["image"], "released")
        self.assertNotIn(IMAGE, self.daemon.images)

    def test_stopped_attempt_is_sealed_once_by_the_first_always_step(self):
        # What a hard-killed attempt leaves: a receipt, a partial phase log still "running",
        # the builder's children, and no release record or manifest.
        (self.evidence / "prerequisite").mkdir(parents=True)
        (self.evidence / "prerequisite/receipt.json").write_text(json.dumps(receipt()))
        (self.evidence / "build.log").write_text("partial output of a killed build\n")
        (self.evidence / "build.log.json").write_text(json.dumps({"state": "running"}))
        self.children()
        self.assertEqual(self.always_step(), 0)
        sealed = (self.evidence / "SHA256SUMS").read_text()
        for name in ("build.log", "build.log.json", "cleanup.json", "prerequisite/builder-release.json"):
            self.assertIn("  " + name + "\n", sealed)
        self.assertEqual(self.always_step(), 0)
        self.assertEqual((self.evidence / "SHA256SUMS").read_text(), sealed)
        self.assertTrue((self.evidence / "recovery/001/SHA256SUMS").is_file())
        self.assertEqual(unverified(self.evidence), [])


class ExitContract(unittest.TestCase):
    """0 complete, 1 observed defect, 2 missing prerequisite; the raw code stays in the record."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="a2-exit-")
        self.addCleanup(self.temp.cleanup)
        self.work = Path(self.temp.name)
        self.module = load_attempt("recipe_exit_contract")
        self.number = 0

    def probe(self, daemon):
        """The real collector's container probe, retaining for this job, over the inert daemon."""
        import collector
        self.number += 1
        work = self.work / ("probe-%d" % self.number)
        work.mkdir()
        return collector.container_probe(json.loads(LOCK.read_text()), work, ATTEMPT, retain=True, command=daemon)

    def attempt(self, observation, preflight_exit, phase_exit=0):
        evidence = self.work / ("attempt-%d" % self.number)
        commands = []

        def preflight(directory, argv):
            Path(directory).mkdir(parents=True)
            record = receipt()
            record["observations"]["container_toolchain"] = observation
            (Path(directory) / "receipt.json").write_text(json.dumps(record))
            return preflight_exit

        def command(argv, log, timeout):
            commands.append([str(part) for part in argv])
            return 0 if "--release" in argv else phase_exit
        with patch.dict(os.environ, ENV), patch.object(self.module, "run", Daemon()), \
             patch.object(self.module, "run_command", command), \
             patch.object(self.module, "execute_preflight", preflight), contextlib.redirect_stdout(io.StringIO()):
            code = self.module.execute(evidence, "tcg", self.work, self.work / "target", "nonfree")
        return (code, commands, json.loads((evidence / "cleanup.json").read_text()),
                json.loads((evidence / "recipe-result.json").read_text()))

    def test_image_released_after_a_failed_inspect_stays_unavailable(self):
        daemon = Daemon()
        daemon.once.append(("image", "inspect"))
        observation = self.probe(daemon)
        self.assertEqual((observation["image_id"], observation["image_removed"], observation["image_retained"]),
                         ("", True, False))
        code, commands, cleanup, _ = self.attempt(observation, 2)
        self.assertEqual(code, 2)
        self.assertEqual(commands, [], "no phase and no second image release")
        self.assertEqual((cleanup.get("state"), cleanup.get("image")), ("released", "released_by_prerequisite"))

    def test_unproved_builder_the_collector_released_keeps_the_classified_exit(self):
        daemon = Daemon()
        daemon.report = Result(0, "kiwi-ng 11.0.4\n{}")
        observation = self.probe(daemon)
        self.assertEqual((observation["image_id"], observation["image_removed"], observation["image_retained"]),
                         (IMAGE, True, False))
        code, commands, cleanup, _ = self.attempt(observation, 2)
        self.assertEqual(code, 2)
        self.assertEqual(commands, [])
        self.assertEqual((cleanup.get("state"), cleanup.get("image")), ("released", "released_by_prerequisite"))

    def test_unfinished_builder_build_leaves_cleanup_unconfirmed(self):
        daemon = Daemon()
        daemon.once.append(("build", "--iidfile"))
        observation = self.probe(daemon)
        self.assertEqual((observation["image_retained"], observation["image_removed"], observation["cleaned"]),
                         (False, False, False))
        code, commands, cleanup, _ = self.attempt(observation, 1)
        self.assertEqual(code, 1)
        self.assertEqual(commands, [])
        self.assertEqual((cleanup.get("state"), cleanup.get("image")), ("defect", "never_retained"))

    def test_phase_exits_normalize_to_three_answers_and_keep_the_raw_code(self):
        retained = dict(receipt()["observations"]["container_toolchain"], image_removed=False, cleaned=True)
        for raw, expected in ((1, 1), (2, 2), (7, 1), (124, 1), (127, 1), (-15, 1)):
            with self.subTest(raw=raw):
                self.number += 1
                code, _, _, result = self.attempt(retained, 0, raw)
                self.assertEqual(code, expected)
                self.assertEqual((result["exit"], result["recipe_exit"], result.get("phase_exit")), (expected, expected, raw))
                self.assertEqual((result["phase"], result["phases"][-1]["exit"]), ("build", raw))


class OwnerInterface(unittest.TestCase):
    """What an operator or the workflow can call, and what each call promises."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="a2-interface-")
        self.addCleanup(self.temp.cleanup)
        self.work = Path(self.temp.name)

    def test_workflow_requires_an_explicit_accelerator(self):
        spec = workflow()[True]["workflow_dispatch"]["inputs"]["accelerator"]
        self.assertIs(spec.get("required"), True)
        self.assertNotIn("default", spec)
        # The owner refuses an empty or unknown choice before it creates anything.
        for value in ("", "auto"):
            with self.subTest(value=value):
                result = subprocess.run([sys.executable, str(KIWI / "attempt.py"), "--evidence", str(self.work / "e"),
                                         "--accelerator", value], capture_output=True, text=True, timeout=20)
                self.assertEqual(result.returncode, 2)
                self.assertFalse((self.work / "e").exists())

    def test_workflow_shell_steps_are_strict(self):
        for step in workflow()["jobs"]["image"]["steps"]:
            if "run" in step:
                with self.subTest(step=step["name"]):
                    self.assertTrue(step["run"].startswith("set -euo pipefail\n"), step["run"][:60])

    def test_help_prints_the_whole_documented_header(self):
        lines = (KIWI / "build.sh").read_text().splitlines()
        header = []
        for line in lines[5:]:
            if not line.startswith("#"):
                break
            header.append(line)
        result = subprocess.run(["bash", str(KIWI / "build.sh"), "--help"], capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout.splitlines(), header)

    def test_the_taskfile_takes_the_desktop_edition_and_the_declaration_lists_both(self):
        import yaml
        task = yaml.safe_load((ROOT / "Taskfile.yml").read_text())["tasks"]["appliance:build"]
        guard = task["cmds"][0]
        for values, expected in ({"EDITION": "desktop", "FORMAT": "all"}, 0), ({"EDITION": "workstation"}, 2):
            with self.subTest(**values):
                variables = {"EDITION": "server", "ARCH": "amd64", "FORMAT": "all", **values}
                script = re.sub(r"\{\{\.(\w+)\}\}", lambda m: variables.get(m.group(1), ""), guard)
                result = subprocess.run(["bash", "-c", script], capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, expected, result.stderr)
        boot = yaml.safe_load((ROOT / "Taskfile.yml").read_text())["tasks"]["appliance:boot"]
        self.assertIn('APPLIANCE_EDITION="{{.EDITION}}"', boot["cmds"][-1])
        self.assertEqual(boot["vars"]["EDITION"], '{{.EDITION | default "server"}}')
        listed = subprocess.run(["bash", str(ROOT / "appliance/images/formats/assemble.sh"), "--list"],
                                capture_output=True, text=True, timeout=10)
        self.assertEqual(listed.stdout.split(), ["server", "iso", "server", "qcow2", "server", "ova",
                                                 "desktop", "iso", "desktop", "qcow2", "desktop", "ova"])

    def test_print_plan_is_a_pure_offline_plan(self):
        environment = {k: v for k, v in os.environ.items() if k != "WORK_DIR"}
        result = subprocess.run(["bash", str(KIWI / "build.sh"), "--print-plan", "--package-dir", str(self.work / "absent"),
                                 "--target-dir", str(self.work / "output")], env=environment,
                                capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("UNADMITTED PLAN", result.stdout)
        self.assertIn("kiwi-ng", result.stdout)
        self.assertEqual(sorted(p.name for p in self.work.iterdir()), [])
        # A value that is given is still held to its form: a plan never shows a mutable tag.
        refused = subprocess.run(["bash", str(KIWI / "build.sh"), "--print-plan", "--image-id", "mutable:tag"],
                                 env=environment, capture_output=True, text=True, timeout=10)
        self.assertEqual(refused.returncode, 2)

    def test_taskfile_build_is_the_single_all_format_transaction(self):
        import yaml
        task = yaml.safe_load((ROOT / "Taskfile.yml").read_text())["tasks"]["appliance:build"]
        guard = task["cmds"][0]
        for values, expected in (({"FORMAT": "qcow2"}, 2), ({"FORMAT": "iso"}, 2), ({"FORMAT": "all"}, 0)):
            with self.subTest(**values):
                variables = {"EDITION": "server", "ARCH": "amd64", **values}
                script = re.sub(r"\{\{\.(\w+)\}\}", lambda m: variables.get(m.group(1), ""), guard)
                result = subprocess.run(["bash", "-c", script], capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, expected, result.stderr)
                if expected:
                    self.assertIn("FORMAT=all", result.stderr)


if __name__ == '__main__':
    unittest.main()
