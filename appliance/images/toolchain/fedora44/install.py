#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""install.py fetch|install|qualify|report — KIWI 11.0.4 into the Fedora 44 builder, from its verified source.

The Debian builder installs KIWI and every Python dependency from hashed wheels (../install.py). Fedora 44 packages
KIWI's dependencies at versions its pyproject accepts (typer, lxml, requests, PyYAML, simplejson, and the poetry-core
build backend), so this builder installs them as the locked RPMs bootstrap.sh installs, and builds KIWI 11.0.4 itself
from its sdist, verified by sha256, in a venv that sees the system packages. Fedora's own python3-kiwi is 11.0.2 and
is never installed.

  fetch    download the locked sdist and verify its sha256 (the only network step)
  install  offline: the venv, the KIWI wheel built without isolation or index, the dracut modules copied from the
           verified source to the lock's dracut_modules_dir, and pip check
  qualify  offline: kiwi-ng --version and --help, the imports, every required program and file, and the rpm and pip
           inventories, into /toolchain/qualification.json with the build provenance
  report   what the hosted preflight's collector reads (collector.py container_probe): the KIWI version line, then the
           qualification JSON, two lines
KIWI 11 exits 1 after --version and --help, whenever no command ran (../install.py kiwi_info), so those two are judged by
their output with exit 0 or 1 and nothing on stderr.
exit 0 done; exit 1 a check or command failed; exit 2 a lock this script cannot use."""
import hashlib
import json
import shutil
import subprocess
import sys
import tarfile
import urllib.request
from pathlib import Path

LOCK = Path("/toolchain/input-lock.json")
SDIST_DIR = Path("/toolchain/sdist")
VENV = Path("/opt/kiwi")
COMMAND_TIMEOUT = 600


def validate(lock):
    kiwi = lock["toolchain"]["kiwi"]
    python = lock["toolchain"]["python"]
    sdists = [p for p in python["packages"] if p["name"] == "kiwi"]
    if lock["distribution"]["id"] != "fedora" or lock["distribution"]["version"] != "44":
        raise SystemExit(2)
    if len(sdists) != 1 or sdists[0]["selected_artifact"]["sha256"] != kiwi["sha256"] or \
            sdists[0]["selected_artifact"]["filename"] != kiwi["filename"]:
        raise SystemExit(2)
    missing = [n for n in python["rpm_dependencies"] if n not in lock["distribution"]["system_packages"]]
    if missing:
        raise SystemExit(2)
    return sdists[0]["selected_artifact"]


def command(argv):
    result = subprocess.run(argv, capture_output=True, text=True, timeout=COMMAND_TIMEOUT)
    sys.stdout.write(result.stdout)
    sys.stderr.write(result.stderr[-2048:])
    if result.returncode != 0:
        print("install.py: command failed: %s (exit %d)" % (argv, result.returncode), file=sys.stderr)
        raise SystemExit(1)
    return result


def verify(path, expected):
    found = hashlib.sha256(path.read_bytes()).hexdigest()
    if found != expected:
        print("install.py: %s has sha256 %s, the lock says %s" % (path, found, expected), file=sys.stderr)
        raise SystemExit(1)


def fetch(lock):
    artifact = validate(lock)
    SDIST_DIR.mkdir()
    path = SDIST_DIR / artifact["filename"]
    with urllib.request.urlopen(artifact["url"], timeout=60) as response, path.open("xb") as output:
        output.write(response.read(64 * 1024 * 1024 + 1))
    verify(path, artifact["sha256"])


def install(lock):
    artifact = validate(lock)
    sdist = SDIST_DIR / artifact["filename"]
    verify(sdist, artifact["sha256"])
    command(["python3", "-m", "venv", "--system-site-packages", str(VENV)])
    pip = [str(VENV / "bin/python"), "-m", "pip"]
    source = Path("/toolchain/kiwi-source")
    with tarfile.open(sdist) as tar:
        tar.extractall(source, filter="data")
    project = source / ("kiwi-%s" % lock["toolchain"]["kiwi"]["upstream_version"])
    shutil.copytree(project / "dracut/modules.d", Path(lock["toolchain"]["dracut_modules_dir"]))
    wheels = Path("/toolchain/wheels")
    command(pip + ["wheel", "--no-index", "--no-deps", "--no-build-isolation", "--wheel-dir", str(wheels), str(project)])
    built = sorted(wheels.glob("kiwi-*.whl"))
    if len(built) != 1:
        print("install.py: expected one KIWI wheel, found %s" % built, file=sys.stderr)
        raise SystemExit(1)
    command(pip + ["install", "--no-index", "--no-deps", str(built[0])])
    command(pip + ["check"])
    provenance = {"lock_sha256": hashlib.sha256(LOCK.read_bytes()).hexdigest(),
                  "derived_kiwi_wheel_sha256": hashlib.sha256(built[0].read_bytes()).hexdigest(),
                  "source_sha256": artifact["sha256"]}
    Path("/toolchain/build-provenance.json").write_text(json.dumps(provenance, sort_keys=True) + "\n")


def kiwi_info(argv, expected):
    """One informational kiwi-ng run, judged by its output: exit 0 or 1, nothing on stderr, the expected text."""
    result = subprocess.run(argv, capture_output=True, text=True, timeout=COMMAND_TIMEOUT)
    if result.returncode not in (0, 1) or result.stderr or not expected(result.stdout):
        print("install.py: %s: exit %d, stderr %d bytes" % (argv, result.returncode, len(result.stderr)), file=sys.stderr)
        raise SystemExit(1)
    return result.stdout


def version_line(lock):
    return "KIWI (next generation) version %s\n" % lock["toolchain"]["kiwi"]["upstream_version"]


def qualify(lock):
    validate(lock)
    kiwi = str(VENV / "bin/kiwi-ng")
    kiwi_info([kiwi, "--version"], lambda text: text == version_line(lock))
    kiwi_info([kiwi, "--help"], lambda text: "Usage: kiwi-ng [OPTIONS] COMMAND [ARGS]..." in text)
    kiwi_info([kiwi, "system", "build", "--help"], lambda text: "Usage: kiwi-ng system build [OPTIONS]" in text)
    command([str(VENV / "bin/python"), "-c", "import kiwi,typer,click,lxml,yaml,requests,simplejson"])
    command([str(VENV / "bin/python"), "-m", "pip", "check"])
    for program in lock["toolchain"]["required_programs"]:
        if not shutil.which(program, path="%s:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin" % (VENV / "bin")):
            print("install.py: required program %s is missing" % program, file=sys.stderr)
            raise SystemExit(1)
    for filename in lock["toolchain"]["required_files"]:
        if not Path(filename).is_file():
            print("install.py: required file %s is missing" % filename, file=sys.stderr)
            raise SystemExit(1)
    inventory = sorted(command(["rpm", "-qa", "--qf", "%{NAME}-%{EPOCHNUM}:%{VERSION}-%{RELEASE}.%{ARCH}\\n"])
                       .stdout.splitlines())
    Path("/toolchain/rpm-inventory.txt").write_text("".join(line + "\n" for line in inventory))
    installed = command([str(VENV / "bin/python"), "-m", "pip", "freeze", "--all"]).stdout
    Path("/toolchain/python-inventory.txt").write_text(installed)
    qualification = {"schema": "olivares-toolchain-qualification/v1",
                     "kiwi_version": lock["toolchain"]["kiwi"]["upstream_version"], "python_version": sys.version,
                     "dependency_check": True, "cli_help_check": True, "imports_check": True,
                     "required_files": lock["toolchain"]["required_files"],
                     "required_programs": lock["toolchain"]["required_programs"],
                     "rpm_inventory": inventory, "python_inventory": installed.splitlines(),
                     "build_provenance": json.loads(Path("/toolchain/build-provenance.json").read_text())}
    Path("/toolchain/qualification.json").write_text(json.dumps(qualification, sort_keys=True) + "\n")


def report(lock):
    print(kiwi_info([str(VENV / "bin/kiwi-ng"), "--version"], lambda text: text == version_line(lock)).strip())
    print(Path("/toolchain/qualification.json").read_text().strip())


if __name__ == "__main__":
    actions = {"fetch": fetch, "install": install, "qualify": qualify, "report": report}
    if len(sys.argv) != 2 or sys.argv[1] not in actions:
        raise SystemExit(2)
    actions[sys.argv[1]](json.loads(LOCK.read_text()))
