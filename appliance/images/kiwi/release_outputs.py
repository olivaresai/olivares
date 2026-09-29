#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""release_outputs.py — what the Fedora build writes beside the images for the public repository's release: the SPDX 2.3
SBOM of the image root and the corresponding source bundle. Run by build.sh on the host after KIWI; every input is an
argument.

  release_outputs.py pin --source-root R
      prints the source pin {repository, commit, tree} of the git checkout R, whose tracked files must be unchanged from
      its HEAD commit (a build of uncommitted changes has no pin).
  release_outputs.py sbom --target-dir T --version VERSION --lock L --repositories P --metadata DIR [--fetch]
                          --delivery D --build-packages B --source-root R
      writes T/olivares-appliance-VERSION-x86_64.spdx.json and the same bytes as T/sbom.spdx.json. VERSION is X.Y.Z
      (a release) or 0.0.0-dev (a qualification), the build's environment VERSION; any other value is refused
      before any output.
  release_outputs.py bundle (the sbom options) [--srpms DIR] [--fedora-key FILE]
      writes T/olivares-appliance-VERSION-source.tar.zst and T/source-bundle.json (schemas/sources.schema.json and
      schemas/source-bundle.schema.json beside this directory).
  release_outputs.py verify-bundle --target-dir T --version VERSION
      checks the bundle against source-bundle.json and its own SOURCES.json, as the public repository's consumer does.
  release_outputs.py check-size --file F
      the release asset limit alone: F may not pass 2147483648 bytes (2 GiB), as one file of a GitHub release may not.
      A bundle over it is refused by name while or after it is written, is removed, and gets no source-bundle.json.

The installed packages are the image's rpm database as KIWI 11.0.4 exports it into T (<name>.<arch>-<version>.packages:
NAME|EPOCH|VERSION|RELEASE|ARCH|DISTURL|LICENSE per package, system/setup.py _export_rpm_package_list); gpg-pubkey entries
are keys, not packages. Each package is resolved to exactly one pinned source, which gives its sha256:
  * the delivery D (delivery.json, already checked by delivery_check.py): the two product RPMs;
  * the build repository B: the in-build dracut RPM, named by its own header;
  * the Fedora 44 metadata the lock pins (P, fedora-repositories.json: each tree's repomd.xml by sha256 and its primary),
    read from DIR (releases/, updates/; with --fetch first read by GET from the pinned base URLs), which gives the sha256,
    the source RPM and Koji's signed copy of each Fedora package.
A package of no pinned source, a package the lock (L) pins installed at another NEVRA or sha256, and metadata other than
the pinned metadata are refused. The license is the RPM header's License tag: an SPDX expression as it stands, anything
else as NOASSERTION with the tag kept in licenseComments; each LicenseRef used is defined in the document.
The source bundle is a zstd-compressed POSIX (ustar) tar of regular files: SOURCES.json alone at its root, then
  * srpms/<file>: the source RPM of every installed Fedora package, found by its SOURCERPM in the pinned Fedora 44 source
    trees (P's source_repositories, read like the binary ones), fetched from Koji's copy signed with the Fedora key (or
    taken from --srpms), and admitted only when its sha256 is the source metadata's (and the lock's, for a locked binary)
    and its header signature verifies with gpg as the locked Fedora key; its member names the image's binaries built from
    it and that key's fingerprint;
  * product/olivares.tar and recipe/appliance-images.tar: `git archive` of the pinned commit, whole and appliance/images,
    the sources of the product RPMs and of the in-build dracut RPM.
The Fedora key is --fedora-key or Fedora's published file (the lock's key basis), either one checked against the lock's
sha256 and fingerprint. SOURCES.json lists every other member with its path, kind, size, sha256 and origin, never itself;
source-bundle.json names the archive, its size and sha256, the member count with SOURCES.json, and SOURCES.json's sha256.
exit 0 written (or verified); exit 1 refused; exit 2 could not run (usage, unreadable inputs, no clean source pin)."""
import argparse
import datetime
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import urllib.request

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from delivery_check import fingerprint  # noqa: E402
from write_delivery import ARCH, EPOCH, NAME, RELEASE, VERSION, header_fields  # noqa: E402

REPOSITORY = "https://github.com/olivaresai/olivares"
KOJI = "https://kojipkgs.fedoraproject.org/packages"
TREES = {"fedora-44-releases": "releases", "fedora-44-updates": "updates"}
SOURCE_TREES = {"fedora-44-releases-source": "releases-source", "fedora-44-updates-source": "updates-source"}
FEDORA_KEY_URL = "https://src.fedoraproject.org/rpms/fedora-repos/raw/f44/f/RPM-GPG-KEY-fedora-44-primary"
SOURCES_SCHEMA = "olivares.ai/appliance-sources/v1"
BUNDLE_SCHEMA = "olivares.ai/appliance-source-bundle/v1"
BUNDLE_FIELDS = ("schema", "name", "size", "sha256", "members", "sources_sha256")
# Signature header tags holding an OpenPGP signature over the main header: DSAHEADER 267 and RSAHEADER 268 (binary), and
# OPENPGP 278 (rpm 6: base64 strings).
HEADER_SIGNATURES = (268, 267, 278)
CHUNK = 1024 * 1024
# One file of a GitHub release may not pass 2 GiB; formats.json's release_asset_max_bytes is the same ceiling for the images.
RELEASE_ASSET_MAX_BYTES = 2147483648
VERSION_RULE = re.compile(r"[0-9]+\.[0-9]+\.[0-9]+|0\.0\.0-dev")
LICENSE_ID = re.compile(r"^(LicenseRef-[A-Za-z0-9.-]+|[A-Za-z0-9][A-Za-z0-9.-]*\+?)$")


class Refused(Exception):
    pass


class Unable(Exception):
    pass


def relock_module():
    spec = importlib.util.spec_from_file_location("relock_fedora", HERE.parent / "toolchain/fedora44/relock-fedora.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def source_pin(root):
    def git(*args):
        result = subprocess.run(["git", "-C", str(root)] + list(args), capture_output=True, text=True, timeout=60)
        if result.returncode != 0:
            raise Unable("git %s in %s: %s" % (" ".join(args), root, result.stderr.strip()))
        return result.stdout
    changed = git("status", "--porcelain", "--untracked-files=no").splitlines()
    if changed:
        raise Unable("the source root's tracked files differ from its commit, so the build has no source pin: %s"
                     % ", ".join(line[3:] for line in changed))
    return {"repository": REPOSITORY, "commit": git("rev-parse", "HEAD").strip(),
            "tree": git("rev-parse", "HEAD^{tree}").strip()}


def nevra(name, epoch, version, release, arch):
    epoch = "" if str(epoch or "0") in ("0", "(none)") else "%s:" % epoch
    return "%s-%s%s-%s.%s" % (name, epoch, version, release, arch)


def spdx_expression(text):
    """True when TEXT parses as an SPDX license expression (SPDX 2.3 Annex D: identifiers, AND, OR, WITH, parentheses).
    The identifiers are checked for form, not against the license list."""
    tokens = re.findall(r"\(|\)|[^\s()]+", text)
    position = 0

    def term():
        nonlocal position
        if position < len(tokens) and tokens[position] == "(":
            position += 1
            expression()
            if position >= len(tokens) or tokens[position] != ")":
                raise ValueError(text)
            position += 1
            return
        if position >= len(tokens) or tokens[position] in ("AND", "OR", "WITH") or not LICENSE_ID.match(tokens[position]):
            raise ValueError(text)
        position += 1
        if position < len(tokens) and tokens[position] == "WITH":
            position += 1
            if position >= len(tokens) or not LICENSE_ID.match(tokens[position]):
                raise ValueError(text)
            position += 1

    def expression():
        nonlocal position
        term()
        while position < len(tokens) and tokens[position] in ("AND", "OR"):
            position += 1
            term()
    try:
        expression()
    except ValueError:
        return False
    return bool(tokens) and position == len(tokens)


def pinned_metadata(options, trees, section):
    """The packages of the pinned trees (SECTION of the pins file), each tree's repomd.xml checked against its pin."""
    relock = relock_module()
    pins = json.loads(Path(options.repositories).read_text())[section]
    metadata = Path(options.metadata)
    if options.fetch and not (metadata / next(iter(trees.values()))).exists():
        relock.fetch(pins, metadata, trees)
    packages = []
    for alias, short in trees.items():
        repomd = (metadata / short / "repomd.xml").read_bytes()
        digest = hashlib.sha256(repomd).hexdigest()
        if digest != pins[alias]["repomd_sha256"]:
            raise Refused("%s: repomd.xml sha256 %s is not the pinned %s" % (alias, digest, pins[alias]["repomd_sha256"]))
        try:
            _, primary_sha256, found = relock.read_tree(metadata, short, ("src",) if section == "source_repositories"
                                                        else ("x86_64", "noarch"))
        except relock.Refused as issue:
            raise Refused("%s: %s" % (alias, issue))
        if primary_sha256 != pins[alias]["primary_sha256"]:
            raise Refused("%s: the primary's sha256 %s is not the pinned %s" % (alias, primary_sha256,
                                                                              pins[alias]["primary_sha256"]))
        packages += [dict(entry, repo=alias) for entries in found.values() for entry in entries]
    return packages


def installed(target):
    listed = sorted(Path(target).glob("*.packages"))
    if len(listed) != 1:
        raise Unable("%s holds %d KIWI package lists, not one" % (target, len(listed)))
    rows = []
    for line in listed[0].read_text().splitlines():
        if not line.strip():
            continue
        name, epoch, version, release, arch, _, license = line.split("|", 6)
        if name != "gpg-pubkey":
            rows.append({"name": name, "epoch": epoch, "version": version, "release": release, "arch": arch,
                         "license": license})
    return listed[0].name, rows


def resolve(options):
    """Every installed package with its origin (fedora, product or build), sha256 and, for Fedora, source RPM."""
    lock = json.loads(Path(options.lock).read_text())
    key_id = lock["distribution"]["key"]["fingerprint"][-8:].lower()
    fedora = {}
    for entry in pinned_metadata(options, TREES, "repositories"):
        fedora[nevra(entry["name"], entry["epoch"], entry["version"], entry["release"], entry["arch"])] = entry
    delivery = json.loads((Path(options.delivery) / "delivery.json").read_text())
    products = {row["nevra"]: row for row in delivery["packages"]}
    built = {}
    for rpm in sorted(Path(options.build_packages).glob("*.rpm")):
        data = rpm.read_bytes()
        fields = header_fields(data)
        built[nevra(fields[NAME], fields.get(EPOCH), fields[VERSION], fields[RELEASE], fields[ARCH])] = \
            hashlib.sha256(data).hexdigest()
    listing, rows = installed(options.target_dir)
    resolved, refused = [], []
    for row in rows:
        name = nevra(row["name"], row["epoch"], row["version"], row["release"], row["arch"])
        record = dict(row, nevra=name)
        if name in products:
            record.update(origin="product", sha256=products[name]["sha256"])
        elif name in built:
            record.update(origin="build", sha256=built[name])
        elif name in fedora:
            entry = fedora[name]
            source = (entry["sourcerpm"] or "").rsplit(".src.rpm", 1)[0].rsplit("-", 2)
            record.update(origin="fedora", sha256=entry["sha256"], sourcerpm=entry["sourcerpm"], repo=entry["repo"],
                          koji_signed="%s/%s/%s/%s/data/signed/%s/%s/%s" % (
                              KOJI, source[0], source[1], source[2], key_id, entry["arch"], Path(entry["href"]).name)
                          if len(source) == 3 else None)
        else:
            refused.append("%s comes from no pinned repository, the delivery or the build repository" % name)
            continue
        locked = lock["image_packages"].get(row["name"])
        if locked and (locked["nevra"] != name or locked["sha256"] != record["sha256"]):
            refused.append("%s is installed as %s (sha256 %s); the lock pins %s (sha256 %s)" % (
                row["name"], name, record["sha256"], locked["nevra"], locked["sha256"]))
        resolved.append(record)
    if refused:
        raise Refused("; ".join(refused))
    return listing, lock, sorted(resolved, key=lambda r: r["nevra"])


def spdx_id(text):
    return re.sub(r"[^A-Za-z0-9.-]", "-", text)


def sbom(options, pin, listing, lock_sha256, packages):
    version = options.version
    image_id = "SPDXRef-Image"
    source = "%s commit %s, tree %s" % (REPOSITORY, pin["commit"], pin["tree"])
    documents, relationships, references = [], [], set()
    for record in packages:
        epoch = "" if record["epoch"] in ("0", "(none)", "") else record["epoch"] + ":"
        evr = "%s%s-%s" % (epoch, record["version"], record["release"])
        package = {"SPDXID": "SPDXRef-RPM-" + spdx_id(record["nevra"]), "name": record["name"], "versionInfo": evr,
                   "supplier": "Organization: Fedora Project" if record["origin"] == "fedora" else "Organization: Olivares.AI",
                   "downloadLocation": record.get("koji_signed") or "NOASSERTION", "filesAnalyzed": False,
                   "checksums": [{"algorithm": "SHA256", "checksumValue": record["sha256"]}],
                   "licenseConcluded": "NOASSERTION", "copyrightText": "NOASSERTION"}
        if spdx_expression(record["license"]):
            package["licenseDeclared"] = record["license"]
            references |= set(re.findall(r"LicenseRef-[A-Za-z0-9.-]+", record["license"]))
        else:
            package["licenseDeclared"] = "NOASSERTION"
            package["licenseComments"] = "The RPM License tag reads %r, which is not an SPDX license expression." \
                % record["license"]
        qualifiers = "arch=%s%s" % (record["arch"], "&epoch=" + record["epoch"] if epoch else "")
        if record["origin"] == "fedora":
            locator = "pkg:rpm/fedora/%s@%s-%s?%s&distro=fedora-44" % (record["name"], record["version"],
                                                                        record["release"], qualifiers)
            package["sourceInfo"] = "built from the source RPM %s (Fedora 44, %s)" % (record["sourcerpm"], record["repo"])
        else:
            locator = "pkg:rpm/olivares/%s@%s-%s?%s" % (record["name"], record["version"], record["release"], qualifiers)
            package["sourceInfo"] = "built from %s: the product and recipe members of the source bundle" % source
        package["externalRefs"] = [{"referenceCategory": "PACKAGE-MANAGER", "referenceType": "purl",
                                    "referenceLocator": locator}]
        documents.append(package)
        relationships.append({"spdxElementId": image_id, "relationshipType": "CONTAINS",
                              "relatedSpdxElement": package["SPDXID"]})
    image = {"SPDXID": image_id, "name": "olivares-appliance", "versionInfo": version, "supplier": "Organization: Olivares.AI",
             "downloadLocation": "git+%s@%s" % (REPOSITORY, pin["commit"]), "filesAnalyzed": False,
             "licenseConcluded": "NOASSERTION", "licenseDeclared": "NOASSERTION", "copyrightText": "NOASSERTION",
             "primaryPackagePurpose": "OPERATING_SYSTEM",
             "sourceInfo": "the recipe and product at %s; the Fedora 44 lock sha256 %s" % (source, lock_sha256)}
    document = {
        "spdxVersion": "SPDX-2.3", "dataLicense": "CC0-1.0", "SPDXID": "SPDXRef-DOCUMENT",
        "name": "olivares-appliance-%s-x86_64" % version,
        "documentNamespace": "https://images.olivares.ai/spdx/olivares-appliance-%s-x86_64/%s" % (version, pin["commit"]),
        "creationInfo": {"created": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
                         "creators": ["Organization: Olivares.AI", "Tool: olivares-appliance-release-outputs"]},
        "comment": "The image root of olivares-appliance %s (x86_64), built from %s with the Fedora 44 lock sha256 %s. "
                   "The packages are the image's rpm database as KIWI exported it (%s)." % (version, source, lock_sha256,
                                                                                           listing),
        "packages": [image] + documents,
        "relationships": [{"spdxElementId": "SPDXRef-DOCUMENT", "relationshipType": "DESCRIBES",
                           "relatedSpdxElement": image_id}] + relationships,
    }
    if references:
        document["hasExtractedLicensingInfos"] = [
            {"licenseId": reference, "name": reference,
             "extractedText": "%s is a license identifier Fedora uses outside the SPDX License List, as an RPM License tag "
                              "of this image names it; the text is in the package that names it." % reference}
            for reference in sorted(references)]
    return document


def write_new(path, data):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o644)
    with os.fdopen(fd, "wb") as out:
        out.write(data)


def sha256_file(path):
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(CHUNK), b""):
            digest.update(chunk)
    return digest.hexdigest()


def libzstd():
    import ctypes
    try:
        lib = ctypes.CDLL("libzstd.so.1")
    except OSError:
        raise Unable("neither Python's compression.zstd nor libzstd.so.1 is here")

    class Buffer(ctypes.Structure):
        _fields_ = [("data", ctypes.c_void_p), ("size", ctypes.c_size_t), ("pos", ctypes.c_size_t)]
    lib.Buffer = Buffer
    for name in ("ZSTD_createCCtx", "ZSTD_createDStream"):
        getattr(lib, name).restype = ctypes.c_void_p
    for name in ("ZSTD_freeCCtx", "ZSTD_freeDStream", "ZSTD_initDStream"):
        getattr(lib, name).argtypes = [ctypes.c_void_p]
    lib.ZSTD_compressStream2.argtypes = [ctypes.c_void_p, ctypes.POINTER(Buffer), ctypes.POINTER(Buffer), ctypes.c_int]
    lib.ZSTD_compressStream2.restype = ctypes.c_size_t
    lib.ZSTD_decompressStream.argtypes = [ctypes.c_void_p, ctypes.POINTER(Buffer), ctypes.POINTER(Buffer)]
    lib.ZSTD_decompressStream.restype = ctypes.c_size_t
    lib.ZSTD_isError.argtypes = [ctypes.c_size_t]
    lib.ZSTD_CStreamOutSize.restype = ctypes.c_size_t
    lib.ZSTD_DStreamOutSize.restype = ctypes.c_size_t
    return lib


def size_guard(path, size=None):
    """The size of PATH (or SIZE, already measured), refused by name over the release asset limit."""
    size = Path(path).stat().st_size if size is None else size
    if size > RELEASE_ASSET_MAX_BYTES:
        raise Refused("%s is %d bytes, over the release asset limit of %d bytes (2 GiB)" % (
            Path(path).name, size, RELEASE_ASSET_MAX_BYTES))
    return size


class ZstdWriter:
    """A write-only zstd file (level 3, zstd's default): Python's compression.zstd where it exists (3.14), else libzstd's
    streaming API (zstd.h ZSTD_compressStream2). With GUARD, it stops as soon as what it wrote passes the release asset
    limit, so no file over it is ever completed."""

    def __init__(self, path, guard=False):
        self.path, self.guard, self.written = Path(path), guard, 0
        self.out = open(path, "xb")
        try:
            from compression import zstd
            self.native = zstd.ZstdCompressor(level=3)
        except ImportError:
            import ctypes
            self.native, self.ctypes, self.lib = None, ctypes, libzstd()
            self.ctx = self.lib.ZSTD_createCCtx()
            self.size = self.lib.ZSTD_CStreamOutSize()
            self.target = ctypes.create_string_buffer(self.size)

    def _stream(self, data, end):
        Buffer = self.lib.Buffer
        source = self.ctypes.create_string_buffer(data, len(data))
        in_buf = Buffer(self.ctypes.cast(source, self.ctypes.c_void_p), len(data), 0)
        while True:
            out_buf = Buffer(self.ctypes.cast(self.target, self.ctypes.c_void_p), self.size, 0)
            left = self.lib.ZSTD_compressStream2(self.ctx, self.ctypes.byref(out_buf), self.ctypes.byref(in_buf),
                                                 2 if end else 0)
            if self.lib.ZSTD_isError(left):
                raise Unable("libzstd could not compress the bundle")
            self._emit(self.target.raw[:out_buf.pos])
            if (end and left == 0) or (not end and in_buf.pos == in_buf.size):
                return

    def _emit(self, data):
        self.out.write(data)
        self.written += len(data)
        if self.guard:
            size_guard(self.path, self.written)

    def write(self, data):
        if self.native:
            self._emit(self.native.compress(bytes(data)))
        elif data:
            self._stream(bytes(data), False)
        return len(data)

    def close(self):
        try:
            if self.native:
                self._emit(self.native.flush())
            else:
                self._stream(b"", True)
        finally:
            if not self.native:
                self.lib.ZSTD_freeCCtx(self.ctx)
            self.out.close()


class ZstdReader:
    """A read-only zstd stream, for tarfile's stream mode: compression.zstd where it exists, else libzstd."""

    def __init__(self, path):
        self.source = open(path, "rb")
        self.pending = b""
        try:
            from compression import zstd
            self.native = zstd.ZstdDecompressor()
        except ImportError:
            import ctypes
            self.native, self.ctypes, self.lib = None, ctypes, libzstd()
            self.stream = self.lib.ZSTD_createDStream()
            self.lib.ZSTD_initDStream(self.stream)
            self.size = self.lib.ZSTD_DStreamOutSize()
            self.target = ctypes.create_string_buffer(self.size)

    def _more(self):
        data = self.source.read(CHUNK)
        if not data:
            return False
        if self.native:
            self.pending += self.native.decompress(data)
            return True
        Buffer = self.lib.Buffer
        source = self.ctypes.create_string_buffer(data, len(data))
        in_buf = Buffer(self.ctypes.cast(source, self.ctypes.c_void_p), len(data), 0)
        while in_buf.pos < in_buf.size:
            out_buf = Buffer(self.ctypes.cast(self.target, self.ctypes.c_void_p), self.size, 0)
            if self.lib.ZSTD_isError(self.lib.ZSTD_decompressStream(self.stream, self.ctypes.byref(out_buf),
                                                                    self.ctypes.byref(in_buf))):
                raise Refused("the bundle does not decompress as zstd")
            self.pending += self.target.raw[:out_buf.pos]
        return True

    def read(self, size=-1):
        while (size < 0 or len(self.pending) < size) and self._more():
            pass
        if size < 0:
            size = len(self.pending)
        data, self.pending = self.pending[:size], self.pending[size:]
        return data

    def close(self):
        self.source.close()
        if not self.native:
            self.lib.ZSTD_freeDStream(self.stream)


def header_signature(data):
    """(signature packet bytes, main header bytes) of an RPM file: the signature header's OpenPGP entry and the main header
    from its magic through its data store, the range rpm's header signature covers."""
    if data[:4] != b"\xed\xab\xee\xdb" or data[96:99] != b"\x8e\xad\xe8":
        raise Refused("not an RPM file with a signature header")
    count, size = int.from_bytes(data[104:108], "big"), int.from_bytes(data[108:112], "big")
    store, entries = 112 + 16 * count, {}
    for i in range(count):
        tag, kind, where, n = (int.from_bytes(data[112 + 16 * i + 4 * j:116 + 16 * i + 4 * j], "big") for j in range(4))
        entries[tag] = (kind, where, n)
    main = store + size + (-(store + size) % 8)
    if data[main:main + 3] != b"\x8e\xad\xe8":
        raise Refused("no main header")
    mcount, msize = int.from_bytes(data[main + 8:main + 12], "big"), int.from_bytes(data[main + 12:main + 16], "big")
    body = data[main:main + 16 + 16 * mcount + msize]
    for tag in HEADER_SIGNATURES:
        if tag in entries:
            kind, where, n = entries[tag]
            if kind == 7:
                return data[store + where:store + where + n], body
            import base64
            end = data.index(b"\0", store + where)
            return base64.b64decode(data[store + where:end]), body
    raise Refused("its signature header carries no OpenPGP signature")


class Verifier:
    """gpg with only the locked Fedora key, in a private home."""

    def __init__(self, key, expected):
        self.expected = expected
        self.home = tempfile.mkdtemp(prefix="g.")
        os.chmod(self.home, 0o700)
        self.gpg = ["gpg", "--homedir", self.home, "--batch", "--no-tty"]
        with tempfile.NamedTemporaryFile(dir=self.home) as handle:
            handle.write(key)
            handle.flush()
            if subprocess.run(self.gpg + ["--import", handle.name], capture_output=True, timeout=60).returncode != 0:
                raise Refused("the Fedora key does not import")

    def verify(self, data):
        signature, body = header_signature(data)
        with tempfile.NamedTemporaryFile(dir=self.home) as sig, tempfile.NamedTemporaryFile(dir=self.home) as signed:
            sig.write(signature)
            sig.flush()
            signed.write(body)
            signed.flush()
            result = subprocess.run(self.gpg + ["--status-fd", "1", "--verify", sig.name, signed.name], capture_output=True,
                                    text=True, timeout=60)
        valid = [line.split() for line in result.stdout.splitlines() if line.startswith("[GNUPG:] VALIDSIG ")]
        return result.returncode == 0 and len(valid) == 1 and valid[0][-1].upper() == self.expected

    def close(self):
        subprocess.run(["gpgconf", "--homedir", self.home, "--kill", "all"], capture_output=True, timeout=30)
        shutil.rmtree(self.home, ignore_errors=True)


def fedora_key(options, lock):
    pinned = lock["distribution"]["key"]
    if options.fedora_key:
        key = Path(options.fedora_key).read_bytes()
    else:
        with urllib.request.urlopen(FEDORA_KEY_URL, timeout=60) as response:
            key = response.read()
    if pinned.get("sha256") and hashlib.sha256(key).hexdigest() != pinned["sha256"]:
        raise Refused("the Fedora key has sha256 %s; the lock pins %s" % (hashlib.sha256(key).hexdigest(), pinned["sha256"]))
    if fingerprint(key) != pinned["fingerprint"].upper():
        raise Refused("the Fedora key is %s; the lock pins %s" % (fingerprint(key), pinned["fingerprint"]))
    return key, pinned["fingerprint"].upper()


def git_archive(root, commit, output, *paths):
    with open(output, "xb") as out:
        result = subprocess.run(["git", "-C", str(root), "archive", "--format=tar", commit] + list(paths), stdout=out,
                                stderr=subprocess.PIPE, timeout=1800)
    if result.returncode != 0:
        raise Unable("git archive %s: %s" % (commit, result.stderr.decode(errors="replace").strip()))


def bundle(options, pin):
    listing, lock, packages = resolve(options)
    lock_sha256 = hashlib.sha256(Path(options.lock).read_bytes()).hexdigest()
    key, key_fingerprint = fedora_key(options, lock)
    key_id = key_fingerprint[-8:].lower()
    sources = {Path(e["href"]).name: e for e in pinned_metadata(options, SOURCE_TREES, "source_repositories")}
    built_from = {}
    for record in packages:
        if record["origin"] == "fedora":
            built_from.setdefault(record["sourcerpm"], []).append(record)
    staging = Path(options.metadata) / "bundle"
    (staging / "srpms").mkdir(parents=True)
    try:
        return write_bundle(options, pin, lock, lock_sha256, key, key_fingerprint, key_id, sources, built_from, staging)
    finally:
        shutil.rmtree(staging, ignore_errors=True)


def write_bundle(options, pin, lock, lock_sha256, key, key_fingerprint, key_id, sources, built_from, staging):
    members, files, refused = [], {}, []
    verifier = Verifier(key, key_fingerprint)
    try:
        for file, binaries in sorted(built_from.items()):
            entry = sources.get(file)
            if entry is None:
                refused.append("%s, the source of %s, is in neither pinned source tree" % (
                    file, ", ".join(r["nevra"] for r in binaries)))
                continue
            origin = "%s/%s/%s/%s/data/signed/%s/src/%s" % (KOJI, entry["name"], entry["version"], entry["release"], key_id,
                                                            file)
            cached = Path(options.srpms) / file if options.srpms else None
            path = staging / "srpms" / file
            if cached and cached.is_file():
                shutil.copyfile(cached, path)
            else:
                with urllib.request.urlopen(origin, timeout=600) as response, open(path, "xb") as out:
                    shutil.copyfileobj(response, out, CHUNK)
            digest = sha256_file(path)
            locked = [lock["image_packages"][r["name"]]["srpm"]["sha256"] for r in binaries
                      if r["name"] in lock["image_packages"] and "srpm" in lock["image_packages"][r["name"]]]
            if digest != entry["sha256"] or any(d != digest for d in locked):
                refused.append("%s: sha256 %s is not the pinned source metadata's %s" % (file, digest, entry["sha256"]))
                continue
            if not verifier.verify(path.read_bytes()):
                refused.append("%s: its header signature does not verify as the Fedora key %s" % (file, key_fingerprint))
                continue
            member = "srpms/" + file
            files[member] = path
            members.append({"path": member, "kind": "srpm", "size": path.stat().st_size, "sha256": digest,
                            "origin": origin, "binaries": sorted(r["nevra"] for r in binaries),
                            "signed_by": key_fingerprint})
    finally:
        verifier.close()
    if refused:
        raise Refused("; ".join(refused))
    for member, kind, paths, origin in (
            ("product/olivares.tar", "product", (), "git+%s@%s" % (REPOSITORY, pin["commit"])),
            ("recipe/appliance-images.tar", "recipe", ("appliance/images",),
             "git+%s@%s#appliance/images" % (REPOSITORY, pin["commit"]))):
        path = staging / Path(member).name
        git_archive(options.source_root, pin["commit"], path, *paths)
        files[member] = path
        members.append({"path": member, "kind": kind, "size": path.stat().st_size, "sha256": sha256_file(path),
                        "origin": origin})
    index = (json.dumps({"schema": SOURCES_SCHEMA, "version": options.version, "source": pin, "lock_sha256": lock_sha256,
                         "members": members}, indent=2) + "\n").encode()
    committed = int(subprocess.run(["git", "-C", str(options.source_root), "show", "-s", "--format=%ct", pin["commit"]],
                                   capture_output=True, text=True, check=True, timeout=60).stdout.strip())
    name = "olivares-appliance-%s-source.tar.zst" % options.version
    archive = Path(options.target_dir) / name
    # The release asset limit, before source-bundle.json: the writer stops once it passes the limit, and the finished
    # archive is measured again; a refused archive does not stay.
    writer = ZstdWriter(archive, guard=True)
    try:
        try:
            with tarfile.open(fileobj=writer, mode="w|", format=tarfile.USTAR_FORMAT) as tar:
                for member, size, stream in [("SOURCES.json", len(index), None)] + [
                        (m["path"], m["size"], files[m["path"]]) for m in members]:
                    info = tarfile.TarInfo(member)
                    info.size, info.mode, info.mtime, info.type = size, 0o644, committed, tarfile.REGTYPE
                    info.uid = info.gid = 0
                    info.uname = info.gname = "root"
                    if stream is None:
                        import io
                        tar.addfile(info, io.BytesIO(index))
                    else:
                        with open(stream, "rb") as handle:
                            tar.addfile(info, handle)
        finally:
            writer.close()
        size = size_guard(archive)
    except Refused:
        archive.unlink()
        raise
    descriptor = {"schema": BUNDLE_SCHEMA, "name": name, "size": size, "sha256": sha256_file(archive),
                  "members": len(members) + 1, "sources_sha256": hashlib.sha256(index).hexdigest()}
    write_new(Path(options.target_dir) / "source-bundle.json", (json.dumps(descriptor, indent=2) + "\n").encode())
    return descriptor, len([m for m in members if m["kind"] == "srpm"])


def verify_bundle(target, version):
    """The consumer's checks (addendum 2, rules 1-3): the descriptor's fields, the archive's size and sha256, regular files
    only, SOURCES.json alone at the root with the descriptor's digest, and every other member exactly as SOURCES.json
    lists it, nothing more or less."""
    target = Path(target)
    descriptor = json.loads((target / "source-bundle.json").read_text())
    name = "olivares-appliance-%s-source.tar.zst" % version
    if sorted(descriptor) != sorted(BUNDLE_FIELDS) or descriptor["schema"] != BUNDLE_SCHEMA or descriptor["name"] != name:
        raise Refused("source-bundle.json is not %s for %s: %s" % (BUNDLE_SCHEMA, name, sorted(descriptor)))
    archive = target / name
    if (archive.stat().st_size, sha256_file(archive)) != (descriptor["size"], descriptor["sha256"]):
        raise Refused("%s is not the size and sha256 source-bundle.json names" % name)
    seen, index = {}, None
    reader = ZstdReader(archive)
    try:
        with tarfile.open(fileobj=reader, mode="r|") as tar:
            for info in tar:
                if not info.isreg():
                    raise Refused("%s is not a regular file" % info.name)
                if info.name in seen:
                    raise Refused("%s is in the archive twice" % info.name)
                digest, handle = hashlib.sha256(), tar.extractfile(info)
                data = b""
                for chunk in iter(lambda: handle.read(CHUNK), b""):
                    digest.update(chunk)
                    if info.name == "SOURCES.json":
                        data += chunk
                seen[info.name] = (info.size, digest.hexdigest())
                if info.name == "SOURCES.json":
                    index = data
    finally:
        reader.close()
    roots = sorted(n for n in seen if "/" not in n)
    if roots != ["SOURCES.json"] or index is None:
        raise Refused("the archive's root holds %s; it holds exactly SOURCES.json" % roots)
    if hashlib.sha256(index).hexdigest() != descriptor["sources_sha256"]:
        raise Refused("SOURCES.json is not the bytes source-bundle.json's sources_sha256 names")
    listed = {m["path"]: (m["size"], m["sha256"]) for m in json.loads(index)["members"]}
    if "SOURCES.json" in listed:
        raise Refused("SOURCES.json lists itself")
    problems = ["%s: %s in the archive, %s in SOURCES.json" % (path, seen.get(path), listed.get(path))
                for path in sorted(set(seen) - {"SOURCES.json"} | set(listed)) if seen.get(path) != listed.get(path)]
    if problems:
        raise Refused("; ".join(problems))
    if descriptor["members"] != len(listed) + 1:
        raise Refused("source-bundle.json counts %d members; the archive holds %d" % (descriptor["members"], len(seen)))
    return len(seen)


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("pin").add_argument("--source-root", required=True)
    for command in ("sbom", "bundle"):
        build = commands.add_parser(command)
        for name in ("--target-dir", "--version", "--lock", "--repositories", "--metadata", "--delivery",
                     "--build-packages", "--source-root"):
            build.add_argument(name, required=True)
        build.add_argument("--fetch", action="store_true")
        if command == "bundle":
            build.add_argument("--srpms", default="")
            build.add_argument("--fedora-key", default="")
    check = commands.add_parser("verify-bundle")
    check.add_argument("--target-dir", required=True)
    check.add_argument("--version", required=True)
    commands.add_parser("check-size").add_argument("--file", required=True)
    try:
        options = parser.parse_args(argv)
    except SystemExit:
        return 2
    try:
        if options.command == "check-size":
            size = size_guard(options.file)
            print("release_outputs.py: %s is %d bytes, within the release asset limit of %d bytes" % (
                Path(options.file).name, size, RELEASE_ASSET_MAX_BYTES))
            return 0
        if options.command != "pin" and not VERSION_RULE.fullmatch(options.version):
            raise Unable("VERSION is X.Y.Z or 0.0.0-dev, not %r" % options.version)
        if options.command == "verify-bundle":
            count = verify_bundle(options.target_dir, options.version)
            print("release_outputs.py: the source bundle holds %d regular files as SOURCES.json lists them" % count)
            return 0
        pin = source_pin(options.source_root)
        if options.command == "pin":
            print(json.dumps(pin, sort_keys=True))
            return 0
        if options.command == "bundle":
            descriptor, srpms = bundle(options, pin)
            print("release_outputs.py: %s, %d bytes, %d members (%d source RPMs)" % (
                descriptor["name"], descriptor["size"], descriptor["members"], srpms))
            return 0
        listing, lock, packages = resolve(options)
        lock_sha256 = hashlib.sha256(Path(options.lock).read_bytes()).hexdigest()
        data = (json.dumps(sbom(options, pin, listing, lock_sha256, packages), indent=2) + "\n").encode()
        target = Path(options.target_dir)
        write_new(target / ("olivares-appliance-%s-x86_64.spdx.json" % options.version), data)
        write_new(target / "sbom.spdx.json", data)
        print("release_outputs.py: SBOM of %d packages, source %s, lock %s" % (len(packages), pin["commit"], lock_sha256))
        return 0
    except Refused as issue:
        print("release_outputs.py: refused: %s" % issue, file=sys.stderr)
        return 1
    except (Unable, OSError, ValueError, KeyError, IndexError, tarfile.TarError, subprocess.SubprocessError) as issue:
        print("release_outputs.py: cannot write: %s" % issue, file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
