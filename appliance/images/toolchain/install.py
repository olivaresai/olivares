#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Consume the fixed artifact closure; only KIWI has an explicit offline source build."""
import email.parser
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import tomllib
import urllib.parse
import urllib.request
import zipfile

SNAPSHOT = "https://snapshot.debian.org/archive/debian/20260922T000000Z/"
PYTHON = (3, 13, 5)


def canonical(name):
    return re.sub(r"[-_.]+", "-", name).lower()


def version(value):
    if not re.fullmatch(r"[0-9]+(?:\.[0-9]+)*", value):
        raise ValueError("unsupported version: " + value)
    fields = tuple(int(x) for x in value.split("."))
    return fields + (0,) * (4-len(fields))


def satisfies(actual, expression):
    for item in expression.split(","):
        item = item.strip()
        if not item:
            continue
        match = re.fullmatch(r"(>=|<=|==|!=|<|>|~=)([0-9.]+)(\.\*)?", item)
        if not match:
            raise ValueError("unsupported version constraint: " + item)
        op, required, wildcard = match.groups()
        a, b = version(actual), version(required.rstrip("."))
        if wildcard:
            if op != "!=":
                raise ValueError("unsupported wildcard")
            count = len(required.rstrip(".").split("."))
            okay = a[:count] != b[:count]
        else:
            okay = {">=":a>=b,"<=":a<=b,"==":a==b,"!=":a!=b,"<":a<b,">":a>b,
                    "~=":a>=b and a[0]==b[0]}[op]
        if not okay:
            return False
    return True


EXTRA = re.compile(r"""extra\s*==\s*(?:"[a-zA-Z0-9_.-]+"|'[a-zA-Z0-9_.-]+')""")


def extra_only(marker):
    """True when the marker selects its dependency only for a requested extra.

    That is a conjunction with no `or` anywhere, one of whose clauses, stripped of its outer
    parentheses, is `extra == NAME` in either quote style. Metadata writers differ here: the
    lock records `a == "x" and extra == "y"`, a wheel may write `(a == 'x') and extra == 'y'`.
    This installer requests no extra, so such a dependency is never selected. Anything else,
    including `!=`, a reversed comparison or an `or`, is not classified."""
    if re.search(r"\bor\b", marker):
        return False
    for clause in re.split(r"\band\b", marker):
        clause = clause.strip()
        while clause.startswith("(") and clause.endswith(")"):
            clause = clause[1:-1].strip()
        if EXTRA.fullmatch(clause):
            return True
    return False


def dependency(value):
    requirement, _, marker = value.partition(";")
    if marker:
        # These exact locked releases have only optional-extra dependencies.
        # An unfamiliar marker is rejected, not silently evaluated as absent.
        if extra_only(marker):
            return None
        raise ValueError("unclassified dependency marker: " + marker)
    match = re.fullmatch(r"\s*([a-zA-Z0-9_.-]+)\s*(.*)", requirement)
    if not match:
        raise ValueError("invalid dependency")
    return canonical(match[1]), match[2].strip().strip("()")


def validate(lock):
    if lock["distribution"]["snapshot_basis"] != SNAPSHOT:
        raise ValueError("snapshot moved")
    if lock["distribution"]["system_packages"]["python3"]["Version"] != "3.13.5-1":
        raise ValueError("interpreter bootstrap moved")
    python = lock["toolchain"]["python"]
    if python["version"] != "3.13" or python["extras"] != []:
        raise ValueError("unsupported interpreter or extras")
    packages = python["packages"]
    indexed = {canonical(row["name"]): row for row in packages}
    if len(indexed) != len(packages) or not {"kiwi", "poetry-core"} <= indexed.keys():
        raise ValueError("duplicate or missing builder package")
    for name, row in indexed.items():
        artifact = row["selected_artifact"]
        filename = artifact["filename"]
        if not re.fullmatch(r"[0-9a-f]{64}", artifact["sha256"]) or artifact["yanked"] is not False:
            raise ValueError("artifact has no accepted hash")
        url = urllib.parse.urlparse(artifact["url"])
        if url.scheme != "https" or url.netloc != "files.pythonhosted.org" or Path(url.path).name != filename:
            raise ValueError("artifact URL differs")
        if name == "kiwi":
            if filename != "kiwi-11.0.4.tar.gz" or row["version"] != "11.0.4":
                raise ValueError("unlisted KIWI source build")
        elif not (filename.endswith(("-py3-none-any.whl", "-py2.py3-none-any.whl")) or
                  re.search(r"-cp313-cp313-manylinux.*x86_64\.whl$", filename)):
            raise ValueError("unlisted source build or incompatible wheel ABI")
        if not satisfies("3.13.5", row["requires_python"]) or not satisfies("3.13.5", artifact["requires_python"]):
            raise ValueError("Python version refused by " + name)
        for declared in row["requires_dist"]:
            required = dependency(declared)
            if required:
                target, constraint = required
                if target not in indexed or not satisfies(indexed[target]["version"], constraint):
                    raise ValueError("dependency missing or incompatible: " + declared)
    return indexed


def verify(path, expected):
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(65536), b""):
            digest.update(chunk)
    if digest.hexdigest() != expected:
        raise ValueError("artifact hash differs: " + path.name)


def fetch(lock, directory):
    indexed = validate(lock)
    directory.mkdir()
    for row in indexed.values():
        artifact = row["selected_artifact"]
        path = directory / artifact["filename"]
        request = urllib.request.urlopen(artifact["url"], timeout=30)
        size = 0
        with request, path.open("xb") as output:
            while chunk := request.read(65536):
                size += len(chunk)
                if size > 64 * 1024 * 1024:
                    raise ValueError("artifact exceeds bounded download")
                output.write(chunk)
        verify(path, artifact["sha256"])
    # The next Containerfile stage has no network, regardless of pip configuration.


# Bytes of each stream a failed command shows. The collector keeps only the tail of the build log,
# so a failure prints its diagnosis last, bounded, and ends without a traceback.
FAILURE_TAIL_BYTES = 2048
COMMAND_TIMEOUT = 240


def run(argv):
    """Run one builder command offline with its output captured."""
    return subprocess.run(argv, capture_output=True, timeout=COMMAND_TIMEOUT, env={"PATH":"/opt/kiwi/bin:/usr/bin:/bin",
        "HOME":"/tmp", "PIP_NO_INDEX":"1", "PIP_DISABLE_PIP_VERSION_CHECK":"1",
        "PYTHONNOUSERSITE":"1", "PYTHONHASHSEED":"0", "SOURCE_DATE_EPOCH":"1790036065"})


def emit(result):
    """Show a finished command's whole output in the build log, as an uncaptured command would."""
    for stream, data in ((sys.stdout, result.stdout), (sys.stderr, result.stderr)):
        stream.flush()
        stream.buffer.write(data or b"")
        stream.flush()


def fail(argv, stdout, stderr, why):
    """Print the command, why it failed and the bounded tail of each stream, then end with one line."""
    summary = "install.py: command failed: %s (%s)" % (argv, why)
    sys.stdout.flush()
    sys.stderr.write(summary + "\n")
    for name, data in (("stdout", stdout or b""), ("stderr", stderr or b"")):
        shown = data[-FAILURE_TAIL_BYTES:]
        sys.stderr.write("install.py: %s (%d bytes, last %d shown):\n" % (name, len(data), len(shown)))
        sys.stderr.write(shown.decode("utf-8", "replace") + ("" if shown.endswith(b"\n") or not shown else "\n"))
    sys.stderr.flush()
    raise SystemExit(summary)


def command(argv):
    try:
        result = run(argv)
    except subprocess.TimeoutExpired as expired:
        fail(argv, expired.stdout, expired.stderr, "timed out after %d s" % COMMAND_TIMEOUT)
    if result.returncode != 0:
        fail(argv, result.stdout, result.stderr, "exit %d" % result.returncode)
    emit(result)
    return result


def install(lock, directory):
    indexed = validate(lock)
    if sys.version_info[:2] != (3, 13):
        raise ValueError("builder interpreter differs")
    declared = {row["selected_artifact"]["filename"] for row in indexed.values()}
    if {path.name for path in directory.iterdir()} != declared:
        raise ValueError("artifact directory has missing or unlisted inputs")
    for row in indexed.values():
        path = directory / row["selected_artifact"]["filename"]
        verify(path, row["selected_artifact"]["sha256"])
        if path.suffix == ".whl":
            with zipfile.ZipFile(path) as wheel:
                names = [name for name in wheel.namelist() if name.endswith(".dist-info/METADATA")]
                if len(names) != 1:
                    raise ValueError("wheel metadata is ambiguous")
                meta = email.parser.BytesParser().parsebytes(wheel.read(names[0]))
            if canonical(meta["Name"]) != canonical(row["name"]) or meta["Version"] != row["version"]:
                raise ValueError("wheel distribution differs")
            for requirement in meta.get_all("Requires-Dist", []):
                required = dependency(requirement)
                if required and (required[0] not in indexed or not satisfies(indexed[required[0]]["version"], required[1])):
                    raise ValueError("wheel metadata has an unresolved dependency")
    command(["python3", "-m", "venv", "/opt/kiwi"])
    pip = ["/opt/kiwi/bin/python", "-m", "pip"]
    backend = indexed["poetry-core"]["selected_artifact"]
    command(pip + ["install", "--no-index", "--no-deps", str(directory/backend["filename"])])
    source = Path("/tmp/kiwi-source")
    source.mkdir()
    with tarfile.open(directory/indexed["kiwi"]["selected_artifact"]["filename"], "r:gz") as archive:
        archive.extractall(source, filter="data")
    project = source / "kiwi-11.0.4"
    declaration = tomllib.loads((project/"pyproject.toml").read_text())
    build = declaration["build-system"]
    if build["build-backend"] != "poetry.core.masonry.api" or build.get("backend-path"):
        raise ValueError("unlisted source build backend")
    for requirement in build["requires"]:
        required = dependency(requirement)
        if not required or required[0] != "poetry-core" or not satisfies(indexed["poetry-core"]["version"], required[1]):
            raise ValueError("unlisted source build dependency")
    # Upstream Makefile installs these non-Python examples and legacy schema helpers.
    shared = Path("/usr/share/kiwi")
    shared.mkdir(exist_ok=True)
    (shared/"kiwi.yml.d").mkdir(exist_ok=True)
    Path("/etc/kiwi.yml.d").mkdir(exist_ok=True)
    shutil.copyfile(project/"kiwi.yml", shared/"kiwi.yml.example")
    shutil.copytree(project/"helper/xsl_to_v74", shared/"xsl_to_v74")
    # The guest's dracut modules from this same verified source. The builder does not use them;
    # the recipe stages them from the lock-declared path, which qualify checks by required_files.
    shutil.copytree(project/"dracut/modules.d", Path(lock["toolchain"]["dracut_modules_dir"]))
    output = Path("/tmp/kiwi-wheel")
    output.mkdir()
    command(pip + ["wheel", "--no-index", "--no-deps", "--no-build-isolation", "--wheel-dir", str(output), str(project)])
    wheels = list(output.glob("kiwi-11.0.4-*.whl"))
    if len(wheels) != 1:
        raise ValueError("source build produced an unexpected artifact set")
    runtime = [str(directory/row["selected_artifact"]["filename"]) for name,row in indexed.items() if name not in ("kiwi","poetry-core")]
    command(pip + ["install", "--no-index", "--no-deps", *runtime, str(wheels[0])])
    command(pip + ["check"])
    manifest = {"lock_sha256":hashlib.sha256(Path("/toolchain/input-lock.json").read_bytes()).hexdigest(),
        "derived_kiwi_wheel_sha256":hashlib.sha256(wheels[0].read_bytes()).hexdigest(),
        "source_sha256":indexed["kiwi"]["selected_artifact"]["sha256"]}
    Path("/toolchain/build-provenance.json").write_text(json.dumps(manifest,sort_keys=True)+"\n")


KIWI_NG = "/opt/kiwi/bin/kiwi-ng"


def kiwi_info(argv, expected, show=True):
    """Run one of kiwi-ng's informational commands and return what it printed.

    KIWI 11 exits 1 after --version and --help: its command line exits 1 whenever no command ran,
    even when the option printed what was asked. Such a run is judged by its output instead: it is
    accepted when it exited 0 or 1, wrote nothing to stderr and printed the expected text."""
    try:
        result = run(argv)
    except subprocess.TimeoutExpired as expired:
        fail(argv, expired.stdout, expired.stderr, "timed out after %d s" % COMMAND_TIMEOUT)
    text = result.stdout.decode("utf-8", "replace")
    if result.returncode not in (0, 1) or result.stderr or not expected(text):
        fail(argv, result.stdout, result.stderr,
             "exit %d, stderr %d bytes, expected output %s" % (result.returncode, len(result.stderr),
                                                                "present" if expected(text) else "absent"))
    if show:
        emit(result)
    return text


def version_line(lock):
    return "KIWI (next generation) version %s\n" % lock["toolchain"]["kiwi"]["upstream_version"]


def qualify(lock):
    validate(lock)
    kiwi_info([KIWI_NG, "--version"], lambda text: text == version_line(lock))
    kiwi_info([KIWI_NG, "--help"], lambda text: "Usage: kiwi-ng [OPTIONS] COMMAND [ARGS]..." in text)
    kiwi_info([KIWI_NG, "system", "build", "--help"], lambda text: "Usage: kiwi-ng system build [OPTIONS]" in text)
    command(["/opt/kiwi/bin/python", "-c", "import kiwi,typer,click,lxml,yaml,requests,simplejson"])
    command(["/opt/kiwi/bin/python", "-m", "pip", "check"])
    for program in lock["toolchain"]["required_programs"]:
        if not shutil.which(program, path="/opt/kiwi/bin:/usr/sbin:/usr/bin:/sbin:/bin"):
            raise ValueError("required tool missing: " + program)
    for filename in lock["toolchain"]["required_files"]:
        if not Path(filename).is_file():
            raise ValueError("required boot data missing: " + filename)
    # This tests installed KIWI data without assuming the distribution-package layout.
    command(["/opt/kiwi/bin/python", "-c", "from importlib.metadata import distribution; d=distribution('kiwi'); assert any(str(p).endswith('kiwi.rng') and d.locate_file(p).is_file() for p in d.files), 'KIWI schema data absent'"])
    inventory = subprocess.check_output(["dpkg-query", "--show", "--showformat=${Package}=${Version}\\n"], timeout=30)
    Path("/toolchain/dpkg-inventory.txt").write_bytes(inventory)
    installed = subprocess.check_output(["/opt/kiwi/bin/python", "-m", "pip", "freeze", "--all"], timeout=30)
    Path("/toolchain/python-inventory.txt").write_bytes(installed)
    qualification = {"schema":"olivares-toolchain-qualification/v1", "kiwi_version":"11.0.4",
        "python_version":sys.version, "dependency_check":True, "cli_help_check":True,
        "imports_check":True, "required_files":lock["toolchain"]["required_files"],
        "required_programs":lock["toolchain"]["required_programs"],
        "dpkg_inventory":inventory.decode().splitlines(), "python_inventory":installed.decode().splitlines(),
        "build_provenance":json.loads(Path("/toolchain/build-provenance.json").read_text())}
    Path("/toolchain/qualification.json").write_text(json.dumps(qualification,sort_keys=True)+"\n")


def report(lock, qualification):
    # The first line remains the explicit executable version, not an inventory match; the collector
    # reads exactly two lines, so the version run is judged without being echoed.
    print(kiwi_info([KIWI_NG, "--version"], lambda text: text == version_line(lock), show=False).strip())
    print(qualification.read_text().strip())


if __name__ == "__main__":
    lock = json.loads(Path("/toolchain/input-lock.json").read_bytes())
    action = sys.argv[1]
    if action == "fetch":
        fetch(lock, Path("/artifacts"))
    elif action == "install":
        install(lock, Path("/artifacts"))
    elif action == "qualify":
        qualify(lock)
    elif action == "report":
        report(lock, Path("/toolchain/qualification.json"))
    else:
        raise SystemExit("unsupported installer action")
