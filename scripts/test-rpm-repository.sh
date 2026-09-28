#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Red cases for the signed rpm-md repository.
# --self-test runs without a container. --qualify runs the Fedora 44 cases.
set -euo pipefail
LC_ALL=C
export LC_ALL

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
signer="${root}/scripts/rpm-payload-sign.py"
renderer="${root}/scripts/render-rpm-repodata.py"
client="${root}/scripts/dnf-repository-client.sh"
template="${root}/packaging/repositories/dnf-repo.template"
workflow="${root}/.github/workflows/rpm-repository.yml"
publish_workflow="${root}/.github/workflows/publish-packages.yml"

fail() {
	printf 'test-rpm-repository: refused — %s\n' "$*" >&2
	exit 1
}
blind() {
	printf 'test-rpm-repository: could not check — %s\n' "$*" >&2
	exit 2
}

scratch_dir() {
	local base="${TMPDIR:-/tmp}"
	[[ "$base" == /* && -d "$base" ]] || blind "TMPDIR must be an existing absolute directory"
	mktemp -d "${base}/rpm-repository.XXXXXX"
}

assert_closed_text() {
	local file="$1"
	grep -qx 'pkg_gpgcheck=1' "$file" || return 1
	grep -qx 'repo_gpgcheck=1' "$file" || return 1
	if grep -E '^[[:space:]]*gpgcheck=' "$file" >/dev/null; then
		return 1
	fi
	return 0
}

write_fixture() {
	local dest="$1" mode="$2"
	python3 - "$dest" "$mode" <<'PY'
import bz2
import gzip
import hashlib
import lzma
import sys
from pathlib import Path

dest = Path(sys.argv[1])
mode = sys.argv[2]
repodata = dest / "repodata"
repodata.mkdir(parents=True)
payload = b"<metadata>primary</metadata>\n"
# open-KIND-VARIANT modes supply open-checksum and open-size for one compression.
kind = mode.split("-")[1] if mode.startswith("open-") else "gz"
compressors = {
    "gz": lambda data: gzip.compress(data, mtime=0),
    "bz2": bz2.compress,
    "xz": lzma.compress,
    "zst": lambda data: b"\x28\xb5\x2f\xfd" + data,
}
blob = compressors[kind](payload)
name = f"primary.xml.{kind}"
digest = hashlib.sha256(blob).hexdigest()
size = len(blob)
if mode == "wrong-hash":
    digest = "ab" * 32
(repodata / name).write_bytes(blob)
open_fields = ""
if mode.startswith("open-"):
    open_hash = hashlib.sha256(payload).hexdigest()
    open_size = str(len(payload))
    if mode.endswith("-wrong-hash"):
        open_hash = "00" * 32
    if mode.endswith("-garbage-size"):
        open_size = "garbage"
    if mode.endswith("-wrong-size"):
        open_size = str(len(payload) + 1)
    open_fields = f'<open-checksum type="sha256">{open_hash}</open-checksum><open-size>{open_size}</open-size>'
text = """<?xml version=\"1.0\" encoding=\"UTF-8\"?>
<repomd xmlns=\"http://linux.duke.edu/metadata/repo\">
  <data type=\"primary\">
    <checksum type=\"sha256\">{digest}</checksum>
    <size>{size}</size>
    <location href=\"repodata/{name}\"/>{open_fields}
  </data>
</repomd>
""".format(digest=digest, size=size, name=name, open_fields=open_fields)
(repodata / "repomd.xml").write_text(text, encoding="utf-8")
armor = "-----BEGIN PGP SIGNATURE-----\n\nZm9v\n-----END PGP SIGNATURE-----\n"
if mode == "bad-asc":
    armor = "not a signature\n"
if mode != "missing-asc":
    (repodata / "repomd.xml.asc").write_text(armor, encoding="utf-8")
if mode == "unnamed":
    (repodata / "not-named.xml.gz").write_bytes(blob)
if mode == "symlink-dir":
    (dest / "extra").mkdir()
    (dest / "extra" / "unnamed.xml.gz").write_bytes(blob)
    (repodata / "alias").symlink_to("../extra", target_is_directory=True)
if mode == "symlink-file":
    (repodata / "linked.xml.gz").symlink_to(name)
PY
}

# Parse each workflow with PyYAML, as the Actions runner will. A run block whose
# text leaves its literal indentation is invalid YAML and the workflow cannot be
# dispatched. Every step with uses: must name a 40-hex commit.
check_workflows() {
	python3 - "$@" <<'PY'
import re
import sys
from pathlib import Path

try:
    import yaml
except ImportError:
    print("test-rpm-repository: could not check — PyYAML is absent, so no workflow was parsed", file=sys.stderr)
    raise SystemExit(2)


def refuse(message):
    print(f"test-rpm-repository: refused — {message}", file=sys.stderr)
    raise SystemExit(1)


for name in sys.argv[1:]:
    path = Path(name)
    try:
        document = yaml.safe_load(path.read_text(encoding="utf-8"))
    except yaml.YAMLError as error:
        refuse(f"{path.name} is not valid YAML: " + " ".join(str(error).split()))
    jobs = document.get("jobs") if isinstance(document, dict) else None
    if not isinstance(jobs, dict) or not jobs:
        refuse(f"{path.name} has no jobs mapping")
    for job_name, job in jobs.items():
        steps = job.get("steps") if isinstance(job, dict) else None
        if not isinstance(steps, list) or not steps:
            refuse(f"{path.name} job {job_name} has no steps list")
        for step in steps:
            if not isinstance(step, dict):
                refuse(f"{path.name} job {job_name} has a step that is not a mapping")
            uses = step.get("uses")
            if uses is not None and not re.fullmatch(r"[^@\s]+@[0-9a-f]{40}", str(uses)):
                refuse(f"{path.name} step {step.get('name')} uses an action not pinned by commit")
            run = step.get("run")
            if run is not None and not isinstance(run, str):
                refuse(f"{path.name} step {step.get('name')} run is not a string")
    print(f"test-rpm-repository: workflow parses: {path.name}")
PY
}

# The reader's RPM-01 case: one line of a run block at column 1.
write_column_one_workflow() {
	python3 - "$1" "$2" <<'PY'
import sys
from pathlib import Path

lines = Path(sys.argv[1]).read_text(encoding="utf-8").split("\n")
for index, line in enumerate(lines):
    if line.rstrip().endswith("run: |"):
        lines.insert(index + 2, "import hashlib, os")
        break
else:
    raise SystemExit("workflow has no literal run block to break")
Path(sys.argv[2]).write_text("\n".join(lines), encoding="utf-8")
PY
}

RELEASE_BASE_URL=https://github.com/olivaresai/olivares/releases/download

# Download the published amd64 rpm and checksums.txt, then check the rpm against
# its own line. GitHub answers a release asset with a redirect to its storage
# host; without --location curl writes the empty 302 body and exits 0. curl(1):
# --location follows the redirect, --proto and --proto-redir keep every hop on
# HTTPS, --fail turns an HTTP error into a non-zero exit.
fetch_release() {
	local version="${1:-}" dest="${2:-}"
	[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || blind "release version must be MAJOR.MINOR.PATCH"
	[[ "$dest" == /* ]] || blind "release destination must be an absolute path"
	mkdir -p -- "$dest"
	local name="olivares_${version}_linux_amd64.rpm" asset
	local release_assets=("$name" checksums.txt)
	for asset in "${release_assets[@]}"; do
		rm -f -- "${dest:?}/$asset"
		curl --fail --silent --show-error --location --max-redirs 5 \
			--proto '=https' --proto-redir '=https' --tlsv1.2 \
			--output "$dest/$asset" "${RELEASE_BASE_URL}/v${version}/${asset}" ||
			fail "release asset download failed: ${asset}"
		[[ -f "$dest/$asset" && -s "$dest/$asset" ]] || fail "release asset is zero bytes: ${asset}"
	done
	python3 - "$dest/checksums.txt" "$dest/$name" "$name" <<'PY'
import hashlib
import sys
from pathlib import Path

checksums, rpm_file, name = sys.argv[1:]
expected = [
    parts[0]
    for parts in (line.split() for line in Path(checksums).read_text(encoding="utf-8").splitlines())
    if len(parts) == 2 and parts[1].lstrip("*") == name
]
if len(expected) != 1 or len(expected[0]) != 64:
    print(f"test-rpm-repository: refused — checksums.txt has no single sha256 line for {name}", file=sys.stderr)
    raise SystemExit(1)
if hashlib.sha256(Path(rpm_file).read_bytes()).hexdigest() != expected[0].lower():
    print(f"test-rpm-repository: refused — {name} does not match published checksums.txt", file=sys.stderr)
    raise SystemExit(1)
print(f"test-rpm-repository: {name} matches published checksums.txt")
PY
}

# A curl stub that answers as the release host does: without --location, the
# empty 302 body and exit 0. STUB_CURL_MODE=empty keeps the empty body with
# --location; empty-rpm serves a zero-byte rpm whose published sha256 is that of
# zero bytes; mismatch publishes a wrong sha256.
write_stub_curl() {
	local bin="$1"
	printf '#!%s\n' "$(command -v python3)" >"$bin/curl"
	cat >>"$bin/curl" <<'PY'
import hashlib
import json
import os
import sys
from pathlib import Path

args = sys.argv[1:]
with open(os.environ["STUB_LOG"], "a", encoding="utf-8") as log:
    log.write(json.dumps(["curl", *args]) + "\n")
url = args[-1]
output = Path(args[args.index("--output") + 1])
mode = os.environ.get("STUB_CURL_MODE", "ok")
payload = b"" if mode == "empty-rpm" else b"stub rpm payload\n"
if "--location" not in args or mode == "empty":
    output.write_bytes(b"")
    raise SystemExit(0)
version = url.rsplit("/", 2)[1].lstrip("v")
name = f"olivares_{version}_linux_amd64.rpm"
if url.endswith("/checksums.txt"):
    digest = "0" * 64 if mode == "mismatch" else hashlib.sha256(payload).hexdigest()
    output.write_text(f"{'1' * 64}  {name}.cdx.sbom.json\n{digest}  {name}\n", encoding="utf-8")
else:
    output.write_bytes(payload)
PY
	chmod 0755 "$bin/curl"
}

STUB_FINGERPRINT=0123456789ABCDEF0123456789ABCDEF01234567

# Stub gpg, rpm, rpmkeys, rpmsign and createrepo_c for the path tests. Every call
# is appended to $STUB_LOG as a JSON argv. The gpg stub's detached signature
# carries the signer fingerprint and the sha256 of the signed file, so a changed
# repomd.xml fails its --verify. The createrepo_c stub writes gzip rpm-md from
# NAME-VERSION-RELEASE.ARCH.rpm file names.
write_stub_tools() {
	local bin="$1" python
	python=$(command -v python3)
	printf '#!%s\n' "$python" >"$bin/stub-tool"
	cat >>"$bin/stub-tool" <<'PY'
import gzip
import hashlib
import json
import os
import re
import sys
from pathlib import Path

tool = Path(sys.argv[0]).name
args = sys.argv[1:]
with open(os.environ["STUB_LOG"], "a", encoding="utf-8") as log:
    log.write(json.dumps([tool, *args]) + "\n")


def value(flag):
    return args[args.index(flag) + 1] if flag in args else None


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def stub_nevra(file_name):
    """NAME-VERSION-RELEASE.ARCH.rpm, or a release asset NAME_VERSION_linux_ARCH.rpm."""
    match = re.fullmatch(r"(?P<name>.+)-(?P<ver>[^-]+)-(?P<rel>[^-]+)\.(?P<arch>[^.]+)\.rpm", file_name)
    if match is not None:
        return match["name"], match["ver"], match["rel"], match["arch"]
    match = re.fullmatch(r"(?P<name>[a-z-]+)_(?P<ver>[0-9.]+)_linux_(?P<asset>amd64|arm64)\.rpm", file_name)
    if match is None:
        raise SystemExit(f"stub cannot name {file_name}")
    return match["name"], match["ver"], "1", {"amd64": "x86_64", "arm64": "aarch64"}[match["asset"]]


if tool == "gpg":
    if "--import" in args and os.environ.get("STUB_GPG_IMPORT_FAIL") == "1":
        raise SystemExit(2)
    if "--export" in args:
        sys.stdout.write("-----BEGIN PGP PUBLIC KEY BLOCK-----\n\nstub\n-----END PGP PUBLIC KEY BLOCK-----\n")
    elif "--detach-sign" in args:
        signer = os.environ.get("STUB_GPG_SIGN_AS") or value("--local-user").rstrip("!")
        body = f"{signer} {sha256(Path(args[-1]).read_bytes())}"
        Path(value("--output")).write_text(f"-----BEGIN PGP SIGNATURE-----\n\n{body}\n-----END PGP SIGNATURE-----\n")
    elif "--verify" in args:
        lines = Path(args[-2]).read_text(errors="replace").splitlines()
        body = lines[2].split() if len(lines) == 4 and lines[0] == "-----BEGIN PGP SIGNATURE-----" else []
        if len(body) != 2 or body[1] != sha256(Path(args[-1]).read_bytes()):
            raise SystemExit(1)
        if value("--status-fd") == "1":
            sys.stdout.write(f"[GNUPG:] VALIDSIG {body[0]} 2026-09-27 1790000000 0 4 0 1 8 00 {body[0]}\n")
elif tool == "rpmsign" and os.environ.get("STUB_RPMSIGN_TAMPER") == "1":
    with open(args[-1], "ab") as handle:
        handle.write(b"x")
elif tool == "rpm" and any("%{NAME}" in arg for arg in args):
    name, ver, rel, arch = stub_nevra(Path(args[-1]).name)
    sys.stdout.write(f"{name} {ver} {arch}\n")
elif tool == "rpm":
    sys.stdout.write("RSA/SHA256, stub\n(none)\n(none)\n(none)\n(none)\n")
elif tool == "rpmkeys" and "-Kv" in args and os.environ.get("STUB_RPMKEYS_NOKEY_DB", "\0") in (value("--dbpath") or ""):
    sys.stdout.write(f"{args[-1]}:\n    Header V4 RSA/SHA256 Signature, key ID stub: NOKEY\n    Header SHA256 digest: OK\n")
    raise SystemExit(1)
elif tool == "rpmkeys" and "-Kv" in args:
    sys.stdout.write(f"{args[-1]}:\n    Header V4 RSA/SHA256 Signature, key ID stub: OK\n    Header SHA256 digest: OK\n")
elif tool == "createrepo_c":
    repo = Path(args[-1])
    packages = []
    for path in sorted(repo.glob("*.rpm")):
        name, ver, rel, arch = stub_nevra(path.name)
        packages.append(
            f'<package type="rpm"><name>{name}</name><arch>{arch}</arch>'
            f'<version epoch="0" ver="{ver}" rel="{rel}"/>'
            f'<checksum type="sha256" pkgid="YES">{sha256(path.read_bytes())}</checksum>'
            f'<location href="{path.name}"/></package>'
        )
    documents = {
        "primary": '<metadata xmlns="http://linux.duke.edu/metadata/common" '
        f'packages="{len(packages)}">' + "".join(packages) + "</metadata>\n",
        "filelists": '<filelists xmlns="http://linux.duke.edu/metadata/filelists"/>\n',
        "other": '<otherdata xmlns="http://linux.duke.edu/metadata/other"/>\n',
    }
    repodata = repo / "repodata"
    repodata.mkdir()
    entries = []
    for kind, text in documents.items():
        opened = ('<?xml version="1.0" encoding="UTF-8"?>\n' + text).encode()
        blob = gzip.compress(opened, mtime=0)
        name = f"{sha256(blob)}-{kind}.xml.gz"
        (repodata / name).write_bytes(blob)
        entries.append(
            f'<data type="{kind}"><checksum type="sha256">{sha256(blob)}</checksum>'
            f'<open-checksum type="sha256">{sha256(opened)}</open-checksum>'
            f'<location href="repodata/{name}"/><size>{len(blob)}</size><open-size>{len(opened)}</open-size></data>'
        )
    (repodata / "repomd.xml").write_text(
        '<?xml version="1.0" encoding="UTF-8"?>\n<repomd xmlns="http://linux.duke.edu/metadata/repo">'
        + "".join(entries) + "</repomd>\n"
    )
PY
	chmod 0755 "$bin/stub-tool"
	local tool
	for tool in gpg rpm rpmkeys rpmsign createrepo_c; do
		ln -s stub-tool "$bin/$tool"
	done
	ln -s "$python" "$bin/python3"
}

# An rpm-shaped file: a 96-byte lead, an empty signature header (no padding
# needed at 112 bytes), then stand-in header and payload bytes.
write_fixture_rpm() {
	python3 - "$1" <<'PY'
import sys
from pathlib import Path

lead = b"\xed\xab\xee\xdb\x03\x00" + bytes(90)
signature = b"\x8e\xad\xe8\x01" + bytes(4) + (0).to_bytes(4, "big") + (0).to_bytes(4, "big")
Path(sys.argv[1]).write_bytes(lead + signature + b"stand-in header and payload\n")
PY
}

# A test-environment key descriptor and a 0600 secret file for the stubs.
write_stub_key() {
	local dir="$1"
	python3 - "$dir/descriptor.json" "$STUB_FINGERPRINT" <<'PY'
import json
import sys
from pathlib import Path

Path(sys.argv[1]).write_text(json.dumps({
    "schema": "olivares.ai/package-repository-key/v1",
    "purpose": "apt-rpm-apk-repository-metadata",
    "environment": "test",
    "openpgp_fingerprint": sys.argv[2],
    "apk_public_key_name": "stub.rsa.pub",
    "apk_public_key_sha256": "0" * 64,
}) + "\n", encoding="utf-8")
PY
	printf 'stub secret\n' >"$dir/openpgp-secret.asc"
	chmod 0600 "$dir/openpgp-secret.asc"
}

# Run "$@" with only the stub tools on PATH and the stub key.
run_with_stubs() {
	local tools="$1" keys="$2"
	shift 2
	STUB_LOG="$tools/calls.jsonl" PATH="$tools" OLIVARES_PACKAGE_REPO_TEST_ONLY=1 \
		OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE="$keys/descriptor.json" \
		OLIVARES_PACKAGE_REPO_OPENPGP_SECRET_KEY_FILE="$keys/openpgp-secret.asc" \
		"$@"
}

# Exit 0 when $1 (a stub call log) holds a call whose argv starts with $2...
stub_called() {
	python3 - "$@" <<'PY'
import json
import sys
from pathlib import Path

want = sys.argv[2:]
for line in Path(sys.argv[1]).read_text(encoding="utf-8").splitlines():
    if json.loads(line)[: len(want)] == want:
        raise SystemExit(0)
raise SystemExit(1)
PY
}

# The exact case-results.tsv of a passing --inside-qualify run, in order.
QUALIFY_CASES=(
	$'unsigned\trefused'
	$'tampered\trefused'
	$'foreign-key-rpm\trefused'
	$'bad-repomd-asc\trefused'
	$'missing-repomd-asc\trefused'
	$'foreign-key-repomd\trefused'
	$'wrong-hash\trefused'
	$'unnamed-metadata\trefused'
	$'valid\tinstalled'
)

# A second disposable key, mounted at /foreign-keys. Only /keys is a trust anchor.
with_foreign_key() {
	OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE=/foreign-keys/descriptor.json \
		OLIVARES_PACKAGE_REPO_OPENPGP_SECRET_KEY_FILE=/foreign-keys/openpgp-secret.asc \
		"$@"
}

# gpg-agent's socket path must fit sun_path (108 bytes), which a long TMPDIR
# does not leave room for under DIR/NAME/gnupg/S.gpg-agent.browser.
key_scratch_dir() {
	local base="${TMPDIR:-/tmp}"
	((${#base} <= 40)) || base=/tmp
	mktemp -d "${base}/rk.XXXXXX"
}

# Stop the agents of the self-test keys and remove them, also after a refusal.
KEY_ANCHORS=""
cleanup_key_anchors() {
	[[ -n "$KEY_ANCHORS" && -d "$KEY_ANCHORS" ]] || return 0
	local home
	for home in "$KEY_ANCHORS"/*/gnupg; do
		[[ -d "$home" ]] || continue
		gpgconf --homedir "$home" --kill gpg-agent >/dev/null 2>&1 || true
	done
	rm -rf -- "$KEY_ANCHORS"
}
# publish_rpm's work directory holds the signer's GnuPG home while it signs.
# It is shredded and removed on every exit, including INT and TERM.
PUBLISH_WORK=""
cleanup_publish_work() {
	[[ -n "$PUBLISH_WORK" && -d "$PUBLISH_WORK" ]] || return 0
	find "$PUBLISH_WORK" -type f -exec shred -u -- {} + 2>/dev/null || true
	rm -rf -- "$PUBLISH_WORK"
	PUBLISH_WORK=""
}
trap 'cleanup_key_anchors; cleanup_publish_work' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Make a disposable key with the shared helper and print its fingerprint.
make_test_key() {
	local dir="$1"
	OLIVARES_PACKAGE_REPO_TEST_ONLY=1 bash "${root}/scripts/generate-package-repository-test-key.sh" "$dir" >/dev/null ||
		blind "generate-package-repository-test-key.sh failed for ${dir}"
	python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["openpgp_fingerprint"])' "$dir/descriptor.json"
}

# Detach-sign $3 into $4 with the key made in $1 whose fingerprint is $2.
sign_with_test_key() {
	gpg --homedir "$1/gnupg" --batch --yes --pinentry-mode loopback --passphrase '' \
		--local-user "$2!" --digest-algo SHA256 --armor --detach-sign --output "$4" "$3" 2>/dev/null
}

# check_rc LABEL WANT RC ERRFILE: exit 2 from a check means it could not look,
# and the self-test then exits 2 too; it is never reported as a refusal.
check_rc() {
	local label="$1" want="$2" rc="$3" err="$4"
	[[ "$rc" -eq "$want" ]] && return 0
	cat -- "$err" >&2 || true
	[[ "$rc" -eq 2 ]] && blind "${label} could not check (exit 2)"
	fail "${label} exited ${rc}, want ${want}"
}

# expect_delivery CASE EXIT REASON [ARG...]: copy the rendered appliance tree,
# apply CASE, and run verify-delivery with the stub key fingerprint and ARGs.
expect_delivery() {
	local case_name="$1" want="$2" reason="$3" source="$4" dest="$5"
	shift 5
	cp -a -- "$source" "$dest"
	python3 - "$dest" "$case_name" <<'PY'
import json
import sys
from pathlib import Path

repo = Path(sys.argv[1])
case = sys.argv[2]
manifest_path = repo / "delivery.json"
manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
if case == "wrong-package-digest":
    manifest["packages"][0]["sha256"] = "0" * 64
elif case == "wrong-repomd-digest":
    manifest["repomd_sha256"] = "0" * 64
elif case == "wrong-asc-digest":
    manifest["repomd_asc_sha256"] = "0" * 64
elif case == "wrong-fingerprint":
    manifest["key_fingerprint"] = "F" * 40
elif case == "dropped-package":
    manifest["packages"] = manifest["packages"][:1]
elif case == "missing-asc":
    (repo / "repodata" / "repomd.xml.asc").unlink()
elif case == "extra-rpm":
    (repo / "olivares-extra-1-1.x86_64.rpm").write_bytes(b"not delivered\n")
elif case == "tampered-rpm":
    target = repo / manifest["packages"][0]["file"]
    target.write_bytes(target.read_bytes() + b"x")
elif case not in ("good", "one-expected"):
    raise SystemExit(f"unknown delivery case {case}")
manifest_path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
PY
	local rc=0
	python3 "$renderer" verify-delivery --repo "$dest" --fingerprint "$STUB_FINGERPRINT" "$@" \
		>/dev/null 2>"$dest.err" || rc=$?
	if [[ "$rc" -ne "$want" ]]; then
		cat "$dest.err" >&2 || true
		fail "delivery ${case_name} exited ${rc}, want ${want}"
	fi
	if [[ -n "$reason" ]] && ! grep -qF -- "$reason" "$dest.err"; then
		cat "$dest.err" >&2 || true
		fail "delivery ${case_name} was refused for another reason than: ${reason}"
	fi
	printf 'test-rpm-repository: delivery %s exited %s\n' "$case_name" "$rc"
}

# expect_verify MODE EXIT [REASON]: REASON must appear in the refusal.
expect_verify() {
	local mode="$1" want="$2" reason="${3:-}"
	local fixture
	fixture=$(scratch_dir)
	write_fixture "$fixture" "$mode"
	local rc=0
	python3 "$renderer" verify --repo "$fixture" >/dev/null 2>"$fixture/verify.err" || rc=$?
	if [[ "$rc" -ne "$want" ]]; then
		printf 'test-rpm-repository: verify %s exited %s, want %s\n' "$mode" "$rc" "$want" >&2
		cat "$fixture/verify.err" >&2 || true
		exit 1
	fi
	if [[ -n "$reason" ]] && ! grep -qF -- "$reason" "$fixture/verify.err"; then
		cat "$fixture/verify.err" >&2 || true
		fail "verify ${mode} was refused for another reason than: ${reason}"
	fi
	rm -rf "$fixture"
	printf 'test-rpm-repository: verify %s exited %s\n' "$mode" "$rc"
}

self_test() {
	local force="${OLIVARES_RPM_SELF_TEST_FORCE_REPO_GPGCHECK:-}"
	bash -n "$client"
	bash -n "$0"
	python3 -m py_compile "$signer" "$renderer"
	# shellcheck disable=SC1090
	source "$client"
	local rendered_dir rendered rc
	rendered_dir=$(scratch_dir)
	rendered="$rendered_dir/olivares.repo"
	if [[ -n "$force" ]]; then
		OLIVARES_RPM_SELF_TEST_FORCE_REPO_GPGCHECK="$force" \
			render_repo_file olivares "Olivares AI" "file:///repository" "file:///pinned/key.asc" "$rendered" || true
		if assert_closed_text "$rendered"; then
			fail "negative control accepted a repository file whose metadata check was not 1"
		fi
		printf 'test-rpm-repository: negative control refused the open repository file\n'
		exit 1
	fi
	render_repo_file olivares "Olivares AI" "file:///repository" "file:///pinned/key.asc" "$rendered"
	assert_closed_text "$rendered" || fail "rendered repo file does not set both gpg checks"
	local forced=0
	local opened="$rendered_dir/opened.repo"
	local closed_text
	closed_text=$(cat -- "$rendered")
	printf '%s\n' "${closed_text/repo_gpgcheck=1/repo_gpgcheck=${forced}}" >"$opened"
	if assert_closed_text "$opened"; then
		fail "open repository file was accepted"
	fi
	printf 'test-rpm-repository: rendered template keeps both gpg checks\n'

	# The publisher's clean rpm client: DNF5 against the reviewed HTTPS origin
	# (staged or promoted), both gpg checks from the template, the pinned key
	# file, and staging evidence written only after the client passed.
	local https_repo="$rendered_dir/https.repo"
	render_repo_file olivares "Olivares AI" 'https://packages.olivares.ai/stable/rpm/$basearch' \
		"file:///pinned/key.asc" "$https_repo" || fail "the reviewed HTTPS baseurl was refused"
	grep -qx 'baseurl=https://packages.olivares.ai/stable/rpm/$basearch' "$https_repo" ||
		fail "the HTTPS repo file does not keep \$basearch for DNF5"
	assert_closed_text "$https_repo" || fail "the HTTPS repo file does not set both gpg checks"
	local bad_url
	for bad_url in 'http://packages.olivares.ai/stable/rpm/$basearch' 'https://example.org/stable/rpm/$basearch' \
		'https://packages.olivares.ai/stable/rpm/x86_64'; do
		rc=0
		render_repo_file olivares "Olivares AI" "$bad_url" "file:///pinned/key.asc" "$rendered_dir/bad.repo" \
			2>/dev/null || rc=$?
		[[ "$rc" -eq 1 ]] || fail "baseurl ${bad_url} exited ${rc}, want 1"
	done
	local client_bin staged_url
	client_bin=$(scratch_dir)
	cat >"$client_bin/docker" <<'SH'
#!/bin/bash
printf '%s\n' "$@" >>"$STUB_DOCKER_LOG"
previous=""
closed='[{"id":"olivares","pkg_gpgcheck":true,"repo_gpgcheck":true}]'
for arg in "$@"; do
	target=""
	[[ "$previous" == --volume && "$arg" == *:/client-out ]] && target="${arg%:/client-out}/repo-info.json"
	[[ "$previous" == --volume && "$arg" == *:/client-out/repo-info.json ]] && target="${arg%:/client-out/repo-info.json}"
	if [[ -n "$target" && "${STUB_DOCKER_RC:-0}" == 0 && -z "${STUB_NO_REPO_INFO:-}" ]]; then
		if [[ -n "${STUB_REPO_INFO_SYMLINK:-}" ]]; then
			printf '%s\n' "$closed" >"$STUB_REPO_INFO_SYMLINK"
			rm -f -- "$target"
			ln -s "$STUB_REPO_INFO_SYMLINK" "$target"
		else
			printf '%s\n' "${STUB_REPO_INFO:-$closed}" >"$target"
		fi
	fi
	previous="$arg"
done
exit "${STUB_DOCKER_RC:-0}"
SH
	chmod 0755 "$client_bin/docker"
	printf 'pinned key\n' >"$client_bin/key.asc"
	staged_url='https://packages.olivares.ai/staging/run-7-attempt-1/stable/rpm/$basearch'
	rc=0
	(
		export PATH="$client_bin:$PATH" STUB_DOCKER_LOG="$client_bin/https.args"
		bash "$client" --baseurl "$staged_url" --gpgkey "$client_bin/key.asc" --package olivares --version 26.9.0 \
			--evidence-file "$client_bin/rpm.ok" --staging-id run-7-attempt-1
	) >/dev/null 2>"$client_bin/https.err" || rc=$?
	check_rc "the staged HTTPS client" 0 "$rc" "$client_bin/https.err"
	[[ "$(cat "$client_bin/rpm.ok")" == run-7-attempt-1 ]] || fail "staged rpm evidence is not the staging id"
	grep -qx "CLIENT_BASEURL=${staged_url}" "$client_bin/https.args" || fail "the client did not pass the staged baseurl"
	grep -qx "${client_bin}/key.asc:/pinned/repository-key.asc:ro" "$client_bin/https.args" ||
		fail "the client did not mount the pinned key read-only"
	grep -qx 'CLIENT_VERSION=26.9.0' "$client_bin/https.args" || fail "the client did not pin the version"
	rc=0
	(
		export PATH="$client_bin:$PATH" STUB_DOCKER_LOG="$client_bin/failed.args" STUB_DOCKER_RC=1
		bash "$client" --baseurl "$staged_url" --gpgkey "$client_bin/key.asc" --package olivares --version 26.9.0 \
			--evidence-file "$client_bin/failed.ok" --staging-id run-7-attempt-1
	) >/dev/null 2>&1 || rc=$?
	[[ "$rc" -eq 1 && ! -e "$client_bin/failed.ok" ]] || fail "a failed client exited ${rc} or wrote evidence"
	# R3-B1: the effective gpg checks are parsed on the host, from the repo info
	# the container wrote; the container itself runs no python3.
	local info_case info want_rc
	for info_case in open-repo missing-info symlinked-info; do
		info='[{"id":"olivares","pkg_gpgcheck":true,"repo_gpgcheck":false}]'
		want_rc=1
		rc=0
		(
			export PATH="$client_bin:$PATH" STUB_DOCKER_LOG="$client_bin/info.args" STUB_REPO_INFO="$info"
			[[ "$info_case" == missing-info ]] && export STUB_NO_REPO_INFO=1
			# R4-n1: the output replaced by a symlink to a closed JSON elsewhere.
			[[ "$info_case" == symlinked-info ]] && export STUB_REPO_INFO_SYMLINK="$client_bin/decoy-repo-info.json"
			bash "$client" --baseurl "$staged_url" --gpgkey "$client_bin/key.asc" --package olivares --version 26.9.0 \
				--evidence-file "$client_bin/$info_case.ok" --staging-id run-7-attempt-1
		) >/dev/null 2>"$client_bin/$info_case.err" || rc=$?
		[[ "$info_case" == missing-info ]] && want_rc=2
		[[ "$rc" -eq "$want_rc" && ! -e "$client_bin/$info_case.ok" ]] ||
			fail "client with ${info_case} exited ${rc} (want ${want_rc}) or wrote evidence"
	done
	grep -q 'effective repo_gpgcheck is not true' "$client_bin/open-repo.err" || fail "an open repo_gpgcheck was not named"
	grep -q 'repo info is not a regular file' "$client_bin/symlinked-info.err" ||
		fail "a symlinked repo-info.json was not refused as such"
	ln -s "$client_bin/decoy-repo-info.json" "$client_bin/linked-repo-info.json"
	rc=0
	bash "$client" --check-repo-info "$client_bin/linked-repo-info.json" >/dev/null 2>&1 || rc=$?
	[[ "$rc" -eq 1 ]] || fail "check_repo_info on a symlink exited ${rc}, want 1"
	grep -A1 -x -- '--out-dir' "$client_bin/https.args" | grep -qx /client-out ||
		fail "the client did not give the container its out dir"
	printf 'test-rpm-repository: a symlinked repo-info.json is refused\n'
	container_commands "$0" "$client" "$signer" "$renderer" all >"$client_bin/commands.out" ||
		{ cat "$client_bin/commands.out" >&2; fail "a container runs a command its image does not ship"; }
	local mismatch
	for mismatch in run-8-attempt-1 ''; do
		rc=0
		(
			export PATH="$client_bin:$PATH" STUB_DOCKER_LOG="$client_bin/mismatch.args"
			bash "$client" --baseurl "$staged_url" --gpgkey "$client_bin/key.asc" --package olivares \
				--evidence-file "$client_bin/mismatch-${mismatch:-none}.ok" --staging-id "$mismatch"
		) >/dev/null 2>&1 || rc=$?
		[[ "$rc" -eq 2 ]] || fail "evidence with staging id '${mismatch}' exited ${rc}, want 2"
	done
	rc=0
	(
		export PATH="$client_bin:$PATH" STUB_DOCKER_LOG="$client_bin/local.args"
		bash "$client" --repo "$client_bin" --gpgkey "$client_bin/key.asc" --expect refuse
	) >/dev/null 2>&1 || rc=$?
	if [[ "$rc" -ne 0 ]] || ! grep -qx -- '--network' "$client_bin/local.args" || ! grep -qx none "$client_bin/local.args"; then
		fail "the local-directory client is not network-none"
	fi
	# R4-n1: the client container's only writable mount is its one output file.
	python3 - "$client_bin/https.args" "$client_bin/local.args" <<'PY' || fail "a client container mount other than repo-info.json is writable"
import re
import sys
from pathlib import Path

for name in sys.argv[1:]:
    args = Path(name).read_text(encoding="utf-8").splitlines()
    volumes = [args[i + 1] for i, arg in enumerate(args[:-1]) if arg == "--volume"]
    writable = [v for v in volumes if not v.endswith(":ro")]
    if len(writable) != 1 or not re.fullmatch(r"/.+/repo-info\.json:/client-out/repo-info\.json", writable[0]):
        print(f"{name}: writable mounts {writable}", file=sys.stderr)
        raise SystemExit(1)
PY
	printf 'test-rpm-repository: the client container writes only its repo-info.json\n'
	if grep -qx -- '--network' "$client_bin/https.args"; then
		fail "the HTTPS client passes a network flag it does not need"
	fi
	printf 'test-rpm-repository: HTTPS rpm client pins its key, binds evidence to the staging id\n'

	check_workflows "$workflow" "$publish_workflow"
	local broken_workflow="$rendered_dir/column-one.yml"
	write_column_one_workflow "$workflow" "$broken_workflow"
	rc=0
	check_workflows "$broken_workflow" >/dev/null 2>"$rendered_dir/column-one.err" || rc=$?
	[[ "$rc" -eq 1 ]] || fail "a workflow with a run line at column 1 exited ${rc}, want 1"
	grep -q 'is not valid YAML' "$rendered_dir/column-one.err" || fail "column-1 workflow refusal was not named"
	printf 'test-rpm-repository: column-1 run line refused\n'

	local stub bin rc
	stub=$(scratch_dir)
	bin=$(scratch_dir)
	ln -s "$(command -v python3)" "$bin/python3"
	rc=0
	PATH="$bin" python3 "$renderer" render --repo "$stub" >/dev/null 2>"$stub/render.err" || rc=$?
	[[ "$rc" -eq 2 ]] || fail "createrepo_c absent exited ${rc}, want 2"
	grep -q 'createrepo_c is absent' "$stub/render.err" || fail "createrepo_c absence was not named"
	printf 'test-rpm-repository: createrepo_c absent exited 2\n'
	rc=0
	PATH="$bin" python3 "$signer" --rpm "$stub/missing.rpm" >/dev/null 2>"$stub/sign.err" || rc=$?
	[[ "$rc" -eq 2 ]] || fail "rpmsign absent exited ${rc}, want 2"
	grep -q 'rpmsign is absent' "$stub/sign.err" || fail "rpmsign absence was not named"
	printf 'test-rpm-repository: rpmsign absent exited 2\n'

	# RPM-02: require_tool resolves a path. The guards must still let the
	# documented command reach that tool.
	local tools keys paths_repo
	tools=$(scratch_dir)
	keys=$(scratch_dir)
	paths_repo=$(scratch_dir)
	write_stub_tools "$tools"
	write_stub_key "$keys"
	write_fixture_rpm "$paths_repo/olivares-26.9.0-1.x86_64.rpm"
	rc=0
	run_with_stubs "$tools" "$keys" python3 "$signer" --rpm "$paths_repo/olivares-26.9.0-1.x86_64.rpm" \
		>/dev/null 2>"$tools/sign.err" || rc=$?
	[[ "$rc" -eq 0 ]] || { cat "$tools/sign.err" >&2; fail "signer with stub tools exited ${rc}, want 0"; }
	stub_called "$tools/calls.jsonl" rpmsign --addsign --key-id "$STUB_FINGERPRINT" ||
		fail "the signer did not reach rpmsign --addsign --key-id"
	printf 'test-rpm-repository: signer reached rpmsign by its resolved path\n'
	rc=0
	run_with_stubs "$tools" "$keys" python3 "$renderer" render --repo "$paths_repo" \
		>/dev/null 2>"$tools/render.err" || rc=$?
	[[ "$rc" -eq 0 ]] || { cat "$tools/render.err" >&2; fail "renderer with stub tools exited ${rc}, want 0"; }
	stub_called "$tools/calls.jsonl" createrepo_c --checksum sha256 --general-compress-type gz "$paths_repo" ||
		fail "the renderer did not reach createrepo_c with the documented argv"
	printf 'test-rpm-repository: renderer reached createrepo_c by its resolved path\n'

	# RPM-03: the release fetch follows the redirect over HTTPS only and refuses
	# a zero-byte asset before the checksum step.
	local curl_bin fetched
	curl_bin=$(scratch_dir)
	fetched=$(scratch_dir)
	write_stub_curl "$curl_bin"
	rc=0
	(
		export STUB_LOG="$curl_bin/calls.jsonl" PATH="$curl_bin:$PATH"
		fetch_release 26.9.0 "$fetched/ok"
	) >/dev/null 2>"$fetched/ok.err" || rc=$?
	[[ "$rc" -eq 0 ]] || { cat "$fetched/ok.err" >&2; fail "release fetch exited ${rc}, want 0"; }
	python3 - "$curl_bin/calls.jsonl" <<'PY' || fail "a release GET is not redirect-following, HTTPS-only and failing on HTTP errors"
import json
import sys
from pathlib import Path

calls = [json.loads(line) for line in Path(sys.argv[1]).read_text(encoding="utf-8").splitlines()]
if len(calls) != 2:
    raise SystemExit(1)
for call in calls:
    pairs = list(zip(call, call[1:]))
    if "--location" not in call or "--fail" not in call:
        raise SystemExit(1)
    if ("--proto", "=https") not in pairs or ("--proto-redir", "=https") not in pairs:
        raise SystemExit(1)
    if not call[-1].startswith("https://"):
        raise SystemExit(1)
PY
	local fetch_case
	for fetch_case in empty empty-rpm mismatch; do
		rc=0
		(
			export STUB_CURL_MODE="$fetch_case" STUB_LOG="$curl_bin/calls.jsonl" PATH="$curl_bin:$PATH"
			fetch_release 26.9.0 "$fetched/$fetch_case"
		) >/dev/null 2>"$fetched/$fetch_case.err" || rc=$?
		[[ "$rc" -eq 1 ]] || fail "release fetch ${fetch_case} exited ${rc}, want 1"
	done
	grep -q 'is zero bytes' "$fetched/empty.err" || fail "a zero-byte release asset was not named"
	grep -q 'is zero bytes: olivares_26.9.0_linux_amd64.rpm' "$fetched/empty-rpm.err" ||
		fail "a zero-byte rpm with a matching sha256 was not refused as zero bytes"
	grep -q 'does not match published checksums.txt' "$fetched/mismatch.err" || fail "checksum mismatch was not named"
	rc=0
	(fetch_release 26.9 "$fetched/bad-version") >/dev/null 2>&1 || rc=$?
	[[ "$rc" -eq 2 ]] || fail "release version 26.9 exited ${rc}, want 2"
	printf 'test-rpm-repository: release fetch follows HTTPS redirects and refuses zero bytes\n'

	expect_verify good 0
	expect_verify wrong-hash 1
	expect_verify unnamed 1
	expect_verify symlink-dir 1 'symlink under repodata is refused: repodata/alias'
	expect_verify symlink-file 1 'symlink under repodata is refused: repodata/linked.xml.gz'
	local open_case
	for open_case in open-gz-good open-bz2-good open-xz-good; do
		expect_verify "$open_case" 0
	done
	for open_case in open-gz-wrong-hash open-bz2-wrong-hash open-xz-wrong-hash; do
		expect_verify "$open_case" 1 'open checksum does not match'
	done
	for open_case in open-gz-garbage-size open-xz-garbage-size; do
		expect_verify "$open_case" 1 'open size is not a decimal byte count'
	done
	expect_verify open-gz-wrong-size 1 'open size does not match'
	expect_verify open-zst-good 1 'supplied for an unsupported compression'
	expect_verify bad-asc 1
	expect_verify missing-asc 1

	# RPM-04: two independent trust-anchor negatives in the Fedora run, and the
	# verifier's half of the foreign repomd case here with real OpenPGP keys.
	local listed
	listed=$(printf '%s\n' "${QUALIFY_CASES[@]}")
	grep -qx $'foreign-key-rpm\trefused' <<<"$listed" || fail "the Fedora cases omit foreign-key-rpm"
	grep -qx $'foreign-key-repomd\trefused' <<<"$listed" || fail "the Fedora cases omit foreign-key-repomd"
	declare -f inside_qualify | grep -q 'with_foreign_key python3 "$signer"' ||
		fail "foreign-key-rpm does not sign with the foreign key"
	declare -f inside_qualify | grep -q 'with_foreign_key python3 "$renderer" render' ||
		fail "foreign-key-repomd does not sign repomd with the foreign key"
	declare -f qualify | grep -q '/foreign-keys:ro' || fail "the foreign key is not mounted read-only"
	command -v gpg >/dev/null 2>&1 || blind "gpg is absent"
	local anchors approved_fpr foreign_fpr
	anchors=$(key_scratch_dir)
	KEY_ANCHORS="$anchors"
	approved_fpr=$(make_test_key "$anchors/approved")
	foreign_fpr=$(make_test_key "$anchors/foreign")
	[[ "$approved_fpr" != "$foreign_fpr" ]] || fail "two disposable keys share a fingerprint"
	write_fixture "$anchors/repo" good
	sign_with_test_key "$anchors/approved" "$approved_fpr" "$anchors/repo/repodata/repomd.xml" \
		"$anchors/repo/repodata/repomd.xml.asc" || blind "approved key could not sign"
	rc=0
	python3 "$renderer" verify --repo "$anchors/repo" --public-key "$anchors/approved/openpgp-public.asc" \
		>/dev/null 2>"$anchors/approved.err" || rc=$?
	check_rc "approved-key repomd verify" 0 "$rc" "$anchors/approved.err"
	# RPM-r2-m1: a verifier that could not look (exit 2) keeps the self-test at 2.
	rc=0
	(
		export STUB_GPG_IMPORT_FAIL=1
		inner=0
		run_with_stubs "$tools" "$keys" python3 "$renderer" verify --repo "$anchors/repo" \
			--public-key "$anchors/approved/openpgp-public.asc" >/dev/null 2>"$anchors/blind.err" || inner=$?
		check_rc "stub key import" 0 "$inner" "$anchors/blind.err"
	) 2>/dev/null || rc=$?
	[[ "$rc" -eq 2 ]] || fail "a verifier that could not import its key made the self-test exit ${rc}, want 2"
	sign_with_test_key "$anchors/foreign" "$foreign_fpr" "$anchors/repo/repodata/repomd.xml" \
		"$anchors/repo/repodata/repomd.xml.asc" || blind "foreign key could not sign"
	rc=0
	python3 "$renderer" verify --repo "$anchors/repo" --public-key "$anchors/approved/openpgp-public.asc" \
		>/dev/null 2>"$anchors/foreign.err" || rc=$?
	check_rc "foreign-key repomd with the approved key" 1 "$rc" "$anchors/foreign.err"
	printf 'test-rpm-repository: foreign-key repomd refused with the approved key\n'

	# The signer's own invariant: rpmsign may change only the signature header.
	local tampered_rpm
	tampered_rpm=$(scratch_dir)
	write_fixture_rpm "$tampered_rpm/olivares-26.9.0-1.x86_64.rpm"
	rc=0
	STUB_RPMSIGN_TAMPER=1 run_with_stubs "$tools" "$keys" python3 "$signer" \
		--rpm "$tampered_rpm/olivares-26.9.0-1.x86_64.rpm" >/dev/null 2>"$tampered_rpm/sign.err" || rc=$?
	check_rc "rpmsign that changes the payload" 1 "$rc" "$tampered_rpm/sign.err"
	grep -q 'outside its signature header' "$tampered_rpm/sign.err" || fail "the payload change was not named"
	printf 'test-rpm-repository: a payload changed by rpmsign is refused\n'

	# The production key has a passphrase and rpmsign takes none: the signer
	# seeds gpg-agent from the passphrase file. With real gpg and a locked
	# disposable key, the right file signs; a wrong or absent one could not (2).
	local locked lockedbin locked_fpr
	locked="$anchors/locked"
	lockedbin=$(scratch_dir)
	mkdir -m 0700 "$locked" "$locked/gnupg" "$locked-descriptor"
	printf 'correct horse battery\n' >"$locked/passphrase"
	printf 'wrong horse battery\n' >"$locked/wrong-passphrase"
	chmod 0600 "$locked/passphrase" "$locked/wrong-passphrase"
	gpg --homedir "$locked/gnupg" --batch --pinentry-mode loopback --passphrase-file "$locked/passphrase" \
		--quick-generate-key 'Olivares rpm S3 locked TEST ONLY <locked@invalid.olivares.ai>' rsa2048 sign 0 \
		>/dev/null 2>&1 || blind "gpg could not make the locked test key"
	locked_fpr=$(gpg --homedir "$locked/gnupg" --batch --with-colons --list-secret-keys | awk -F: '$1=="fpr"{print $10; exit}')
	gpg --homedir "$locked/gnupg" --batch --pinentry-mode loopback --passphrase-file "$locked/passphrase" \
		--armor --export-secret-keys "$locked_fpr" >"$locked/openpgp-secret.asc" 2>/dev/null ||
		blind "gpg could not export the locked test key"
	chmod 0600 "$locked/openpgp-secret.asc"
	(STUB_FINGERPRINT="$locked_fpr" write_stub_key "$locked-descriptor")
	local tool_name
	for tool_name in rpmsign rpm rpmkeys; do
		ln -s "$tools/stub-tool" "$lockedbin/$tool_name"
	done
	ln -s "$(command -v gpg)" "$lockedbin/gpg"
	ln -s "$(command -v gpgconf)" "$lockedbin/gpgconf"
	ln -s "$(command -v python3)" "$lockedbin/python3"
	local locked_case want reason
	for locked_case in passphrase wrong-passphrase none; do
		write_fixture_rpm "$locked/olivares-26.9.0-1.x86_64.rpm"
		want=2
		reason='cannot sign unattended'
		[[ "$locked_case" == passphrase ]] && want=0
		rc=0
		(
			export STUB_LOG="$tools/calls.jsonl" PATH="$lockedbin" OLIVARES_PACKAGE_REPO_TEST_ONLY=1 \
				OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE="$locked-descriptor/descriptor.json" \
				OLIVARES_PACKAGE_REPO_OPENPGP_SECRET_KEY_FILE="$locked/openpgp-secret.asc"
			[[ "$locked_case" == none ]] || export OLIVARES_PACKAGE_REPO_OPENPGP_PASSPHRASE_FILE="$locked/$locked_case"
			python3 "$signer" --rpm "$locked/olivares-26.9.0-1.x86_64.rpm"
		) >/dev/null 2>"$locked/$locked_case.err" || rc=$?
		[[ "$rc" -eq "$want" ]] || { cat "$locked/$locked_case.err" >&2; fail "locked key with ${locked_case} exited ${rc}, want ${want}"; }
		[[ "$want" -eq 0 ]] || grep -q "$reason" "$locked/$locked_case.err" || fail "locked key with ${locked_case} did not name: ${reason}"
	done
	printf 'test-rpm-repository: a locked key signs only with its passphrase file\n'

	# delivery.json: the producer side of each refusal the appliance consumer
	# makes (fingerprint, repomd and package digests, missing repomd.xml.asc,
	# a package set other than exactly olivares and olivares-appliance-base).
	local delivery good
	delivery=$(scratch_dir)
	good="$delivery/appliance"
	mkdir -p "$good"
	printf 'stub olivares\n' >"$good/olivares-26.9.0-1.x86_64.rpm"
	printf 'stub appliance base\n' >"$good/olivares-appliance-base-26.9.0-1.noarch.rpm"
	local appliance=(--expect-package olivares --expect-package olivares-appliance-base)
	rc=0
	run_with_stubs "$tools" "$keys" python3 "$renderer" render --repo "$good" "${appliance[@]}" \
		>/dev/null 2>"$delivery/render.err" || rc=$?
	[[ "$rc" -eq 0 ]] || { cat "$delivery/render.err" >&2; fail "appliance render exited ${rc}, want 0"; }
	python3 - "$good/delivery.json" "$STUB_FINGERPRINT" <<'PY' || fail "delivery.json does not have the v1 shape"
import json
import sys

document = json.load(open(sys.argv[1], encoding="utf-8"))
want_packages = [
    ["olivares", "olivares-26.9.0-1.x86_64", "x86_64", "olivares-26.9.0-1.x86_64.rpm"],
    ["olivares-appliance-base", "olivares-appliance-base-26.9.0-1.noarch", "noarch",
     "olivares-appliance-base-26.9.0-1.noarch.rpm"],
]
ok = (
    list(document) == ["schema", "key_fingerprint", "repomd_sha256", "repomd_asc_sha256", "packages"]
    and document["schema"] == "olivares.ai/rpm-delivery/v1"
    and document["key_fingerprint"] == sys.argv[2]
    and [[p["name"], p["nevra"], p["arch"], p["file"]] for p in document["packages"]] == want_packages
    and all(len(p["sha256"]) == 64 for p in document["packages"])
)
raise SystemExit(0 if ok else 1)
PY
	grep -q '  delivery.json$' "$good/checksums.txt" || fail "checksums.txt does not list delivery.json"
	expect_delivery good 0 '' "$good" "$delivery/good" "${appliance[@]}"
	expect_delivery wrong-package-digest 1 'does not match the tree' "$good" "$delivery/wpd" "${appliance[@]}"
	expect_delivery wrong-repomd-digest 1 'repomd_sha256 does not match' "$good" "$delivery/wrd" "${appliance[@]}"
	expect_delivery wrong-asc-digest 1 'repomd_asc_sha256 does not match' "$good" "$delivery/wad" "${appliance[@]}"
	expect_delivery wrong-fingerprint 1 'key_fingerprint is not' "$good" "$delivery/wfp" "${appliance[@]}"
	expect_delivery missing-asc 1 'repomd.xml.asc is missing' "$good" "$delivery/mas" "${appliance[@]}"
	expect_delivery dropped-package 1 'package set does not match' "$good" "$delivery/drp" "${appliance[@]}"
	expect_delivery extra-rpm 1 'rpm in the tree is not named' "$good" "$delivery/xrp" "${appliance[@]}"
	expect_delivery tampered-rpm 1 'does not match its primary checksum' "$good" "$delivery/trp" "${appliance[@]}"
	expect_delivery one-expected 1 'package set is not exactly' "$good" "$delivery/one" --expect-package olivares
	local solo="$delivery/solo"
	mkdir -p "$solo"
	printf 'stub olivares\n' >"$solo/olivares-26.9.0-1.x86_64.rpm"
	rc=0
	run_with_stubs "$tools" "$keys" python3 "$renderer" render --repo "$solo" "${appliance[@]}" \
		>/dev/null 2>"$delivery/solo.err" || rc=$?
	[[ "$rc" -eq 1 && ! -e "$solo/delivery.json" ]] || fail "render of one package for the appliance set exited ${rc}"
	grep -q 'package set is not exactly' "$delivery/solo.err" || fail "the appliance package set refusal was not named"
	local other="$delivery/other-signer"
	mkdir -p "$other"
	printf 'stub olivares\n' >"$other/olivares-26.9.0-1.x86_64.rpm"
	rc=0
	STUB_GPG_SIGN_AS="$(printf 'F%.0s' {1..40})" run_with_stubs "$tools" "$keys" \
		python3 "$renderer" render --repo "$other" >/dev/null 2>"$delivery/other.err" || rc=$?
	[[ "$rc" -eq 1 && ! -e "$other/delivery.json" ]] || fail "a repomd signed by another key exited ${rc} at render"
	grep -q 'not the descriptor key' "$delivery/other.err" || fail "the signer fingerprint refusal was not named"
	printf 'test-rpm-repository: render refuses the wrong package set and a repomd by another key\n'

	# The same manifest with real OpenPGP: the approved disposable key signs
	# repomd, and verify-delivery binds key_fingerprint to the signature.
	local realbin real="$delivery/real"
	realbin=$(scratch_dir)
	ln -s "$tools/stub-tool" "$realbin/createrepo_c"
	ln -s "$(command -v gpg)" "$realbin/gpg"
	ln -s "$(command -v python3)" "$realbin/python3"
	mkdir -p "$real"
	printf 'stub olivares\n' >"$real/olivares-26.9.0-1.x86_64.rpm"
	printf 'stub appliance base\n' >"$real/olivares-appliance-base-26.9.0-1.noarch.rpm"
	rc=0
	STUB_LOG="$tools/calls.jsonl" PATH="$realbin" OLIVARES_PACKAGE_REPO_TEST_ONLY=1 \
		OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE="$anchors/approved/descriptor.json" \
		OLIVARES_PACKAGE_REPO_OPENPGP_SECRET_KEY_FILE="$anchors/approved/openpgp-secret.asc" \
		python3 "$renderer" render --repo "$real" "${appliance[@]}" >/dev/null 2>"$delivery/real.err" || rc=$?
	check_rc "real-key appliance render" 0 "$rc" "$delivery/real.err"
	rc=0
	python3 "$renderer" verify-delivery --repo "$real" --fingerprint "$approved_fpr" \
		--public-key "$anchors/approved/openpgp-public.asc" "${appliance[@]}" >/dev/null 2>"$delivery/real-ok.err" || rc=$?
	check_rc "real-key delivery with the approved key" 0 "$rc" "$delivery/real-ok.err"
	rc=0
	python3 "$renderer" verify-delivery --repo "$real" \
		--public-key "$anchors/foreign/openpgp-public.asc" >/dev/null 2>"$delivery/real-foreign.err" || rc=$?
	check_rc "real-key delivery with a foreign public key" 1 "$rc" "$delivery/real-foreign.err"
	python3 - "$real/delivery.json" "$foreign_fpr" <<'PY'
import json
import sys
from pathlib import Path

path = Path(sys.argv[1])
document = json.loads(path.read_text(encoding="utf-8"))
document["key_fingerprint"] = sys.argv[2]
path.write_text(json.dumps(document, indent=2) + "\n", encoding="utf-8")
PY
	rc=0
	python3 "$renderer" verify-delivery --repo "$real" \
		--public-key "$anchors/approved/openpgp-public.asc" >/dev/null 2>"$delivery/real-fpr.err" || rc=$?
	check_rc "delivery naming the foreign fingerprint over an approved signature" 1 "$rc" "$delivery/real-fpr.err"
	grep -q 'not delivery.json key_fingerprint' "$delivery/real-fpr.err" || fail "the fingerprint mismatch was not named"
	printf 'test-rpm-repository: delivery.json binds its fingerprint to a real repomd signature\n'

	# The publisher hands S3 the tracked production descriptor with no test
	# latch. Both S3 key-policy checks must accept it without the latch, refuse
	# it with OLIVARES_PACKAGE_REPO_TEST_ONLY=1, and accept a test descriptor
	# only with the latch.
	local production="$root/packaging/repositories/production-key-descriptor.json"
	[[ -f "$production" ]] || blind "the tracked production descriptor is absent"
	python3 - "$signer" "$renderer" "$production" "$keys/descriptor.json" <<'PY' || fail "a key-policy case differs"
import contextlib
import importlib.util
import io
import os
import sys

sys.dont_write_bytecode = True


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    loaded = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(loaded)
    return loaded


signer = module("signer", sys.argv[1])
renderer = module("renderer", sys.argv[2])
production, test = sys.argv[3], sys.argv[4]
cases = [(production, None, 0), (production, "1", 1), (test, None, 1), (test, "1", 0)]
for path, latch, want in cases:
    for label, check in (("signer", signer.load_descriptor), ("renderer", renderer.load_fingerprint)):
        os.environ["OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE"] = path
        os.environ.pop("OLIVARES_PACKAGE_REPO_TEST_ONLY", None)
        if latch is not None:
            os.environ["OLIVARES_PACKAGE_REPO_TEST_ONLY"] = latch
        code = 0
        with contextlib.redirect_stderr(io.StringIO()):
            try:
                check()
            except SystemExit as exit_:
                code = exit_.code
        if code != want:
            print(f"key policy {label} {os.path.basename(path)} latch={latch}: exit {code}, want {want}", file=sys.stderr)
            raise SystemExit(1)
PY
	printf 'test-rpm-repository: key policy accepts production only without the latch, test only with it\n'

	# The publisher path: release checksums, then the inside publish step on
	# fixture rpms with stub Fedora tools and the approved real key.
	local publish_dir
	publish_dir=$(scratch_dir)
	mkdir -p "$publish_dir/assets"
	write_fixture_rpm "$publish_dir/assets/olivares_26.9.0_linux_amd64.rpm"
	write_fixture_rpm "$publish_dir/assets/olivares_26.9.0_linux_arm64.rpm"
	(cd "$publish_dir/assets" && sha256sum olivares_26.9.0_linux_amd64.rpm olivares_26.9.0_linux_arm64.rpm >checksums.txt)
	check_release_sha256 "$publish_dir/assets/checksums.txt" "$publish_dir/assets/olivares_26.9.0_linux_amd64.rpm" \
		olivares_26.9.0_linux_amd64.rpm || fail "an authenticated release rpm was refused"
	cp "$publish_dir/assets/olivares_26.9.0_linux_amd64.rpm" "$publish_dir/changed.rpm"
	printf 'x' >>"$publish_dir/changed.rpm"
	rc=0
	check_release_sha256 "$publish_dir/assets/checksums.txt" "$publish_dir/changed.rpm" \
		olivares_26.9.0_linux_amd64.rpm 2>/dev/null || rc=$?
	[[ "$rc" -eq 1 ]] || fail "a release rpm that differs from checksums.txt exited ${rc}, want 1"
	: >"$publish_dir/empty.rpm"
	rc=0
	check_release_sha256 "$publish_dir/assets/checksums.txt" "$publish_dir/empty.rpm" \
		olivares_26.9.0_linux_amd64.rpm 2>/dev/null || rc=$?
	[[ "$rc" -eq 1 ]] || fail "a zero-byte release rpm exited ${rc}, want 1"
	rc=0
	(
		export OLIVARES_PACKAGE_REPO_TEST_ONLY=1
		fixture_publish_tree --assets "$publish_dir/assets" --version 26.9.0 --key-dir "$anchors/approved" \
			--out "$publish_dir/tree"
	) >/dev/null 2>"$publish_dir/tree.err" || rc=$?
	check_rc "the fixture publish tree" 0 "$rc" "$publish_dir/tree.err"
	local channel arch_dir
	for channel in stable security; do
		for arch_dir in x86_64 aarch64; do
			[[ -s "$publish_dir/tree/$channel/rpm/$arch_dir/delivery.json" &&
				-s "$publish_dir/tree/$channel/rpm/$arch_dir/repodata/repomd.xml.asc" ]] ||
				fail "the publish tree lacks ${channel}/rpm/${arch_dir} delivery.json or repomd.xml.asc"
		done
	done
	declare -f publish_rpm | grep -q -- '--network none --security-opt no-new-privileges' ||
		fail "the publish container is not network-none"
	declare -f publish_rpm | grep -q -- '"$image_id" bash /src/scripts/test-rpm-repository.sh --inside-publish-rpm' ||
		fail "the publish container does not run the tools image by ID"
	declare -f publish_rpm | grep -q 'check_tool_identity "$image_id"' || fail "the publish path does not check tool NEVRAs"
	if declare -f publish_rpm | grep -q 'OLIVARES_PACKAGE_REPO_TEST_ONLY=1'; then
		fail "the publish path sets the test-only latch itself"
	fi
	printf 'test-rpm-repository: the publish path signs, renders and verifies both channels and arches\n'
	# R3-m1: a served rpm whose header signature does not verify with the pinned
	# key alone is refused, even when the signer's own check passed.
	rc=0
	(
		export OLIVARES_PACKAGE_REPO_TEST_ONLY=1 STUB_RPMKEYS_NOKEY_DB=pinned-rpmdb
		fixture_publish_tree --assets "$publish_dir/assets" --version 26.9.0 --key-dir "$anchors/approved" \
			--out "$publish_dir/nokey-tree"
	) >/dev/null 2>"$publish_dir/nokey.err" || rc=$?
	check_rc "a publish tree whose rpms do not verify with the pinned key" 1 "$rc" "$publish_dir/nokey.err"
	grep -q 'no header signature that verifies with the pinned repository key' "$publish_dir/nokey.err" ||
		fail "the pinned-key refusal was not named"
	printf 'test-rpm-repository: served rpms must verify with the pinned key alone\n'

	# R3-m2: publish_rpm through a docker stub. The publish container gets the
	# descriptor, the OpenPGP secret, the passphrase and the public key as four
	# read-only file mounts, and never the key directory (which holds the APK
	# private key).
	local publish_bin publish_keys
	publish_bin=$(scratch_dir)
	publish_keys=$(scratch_dir)
	cp "$anchors/approved/descriptor.json" "$anchors/approved/openpgp-secret.asc" "$anchors/approved/openpgp-public.asc" \
		"$anchors/approved/apk-private.pem" "$publish_keys/"
	printf 'stub passphrase\n' >"$publish_keys/passphrase"
	chmod 0600 "$publish_keys/openpgp-secret.asc" "$publish_keys/apk-private.pem" "$publish_keys/passphrase"
	cat >"$publish_bin/docker" <<'SH'
#!/bin/bash
command="$1"
shift
case "$command" in
pull | rmi) exit 0 ;;
build)
	while [[ "$#" -gt 0 ]]; do
		[[ "$1" == --iidfile ]] && printf 'sha256:%s\n' "$(printf 'b%.0s' {1..64})" >"$2"
		shift
	done
	;;
run)
	if [[ " $* " == *" rpm -q "* ]]; then
		printf '%s\n' $STUB_RPM_Q
	else
		printf '%s\n' "$@" >"$STUB_DOCKER_LOG"
		exit "${STUB_PUBLISH_RUN_RC:-0}"
	fi
	;;
*) exit 1 ;;
esac
SH
	chmod 0755 "$publish_bin/docker"
	local publish_case
	for publish_case in ok failed; do
		rc=0
		(
			export PATH="$publish_bin:$PATH" STUB_RPM_Q="${TOOL_NEVRAS[*]}" STUB_DOCKER_LOG="$publish_bin/run.args" \
				OLIVARES_PACKAGE_REPO_TEST_ONLY=1 \
				OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE="$publish_keys/descriptor.json" \
				OLIVARES_PACKAGE_REPO_OPENPGP_SECRET_KEY_FILE="$publish_keys/openpgp-secret.asc" \
				OLIVARES_PACKAGE_REPO_OPENPGP_PASSPHRASE_FILE="$publish_keys/passphrase"
			[[ "$publish_case" == failed ]] && export STUB_PUBLISH_RUN_RC=1
			unset GITHUB_STEP_SUMMARY
			bash "$0" --publish-rpm --assets "$publish_dir/assets" --version 26.9.0 \
				--public-key "$publish_keys/openpgp-public.asc" --out "$publish_dir/published-$publish_case" \
				--work "$publish_bin/work-$publish_case"
		) >/dev/null 2>"$publish_bin/publish-$publish_case.err" || rc=$?
		[[ "$publish_case" == ok ]] && check_rc "publish_rpm through a docker stub" 0 "$rc" "$publish_bin/publish-ok.err"
		[[ "$publish_case" == failed && "$rc" -eq 0 ]] && fail "a failed publish container exited 0"
		# R3-n2: the work directory (the signer's GnuPG home) is gone either way.
		[[ ! -e "$publish_bin/work-$publish_case" ]] || fail "publish_rpm left its work directory after ${publish_case}"
	done
	grep -qx -- '--init' "$publish_bin/run.args" || fail "the publish container does not run with --init"
	python3 - "$publish_bin/run.args" "$publish_keys" <<'PY' || fail "the publish container's key mounts are not the four read-only files"
import sys
from pathlib import Path

args = Path(sys.argv[1]).read_text(encoding="utf-8").splitlines()
keys = sys.argv[2]
volumes = [args[i + 1] for i, arg in enumerate(args[:-1]) if arg == "--volume"]
key_mounts = sorted(v for v in volumes if ":/keys" in v)
want = sorted(
    f"{keys}/{name}:/keys/{target}:ro"
    for name, target in (
        ("descriptor.json", "descriptor.json"),
        ("openpgp-secret.asc", "openpgp-secret.asc"),
        ("openpgp-public.asc", "public.asc"),
        ("passphrase", "passphrase"),
    )
)
if key_mounts != want or any(keys + ":" in v for v in volumes) or any("apk-private" in a for a in args):
    print("\n".join(volumes), file=sys.stderr)
    raise SystemExit(1)
PY
	printf 'test-rpm-repository: the publish container mounts four key files read-only, not the key directory\n'

	# RPM-07: exact tool NEVRAs, installed and checked, and the tools image run
	# by the ID docker build wrote, never by a tag.
	local nevra
	for nevra in "${TOOL_NEVRAS[@]}"; do
		[[ "$nevra" =~ ^[a-z0-9_+-]+-[0-9][A-Za-z0-9._+~^]*-[A-Za-z0-9._+~^]+\.fc44\.x86_64$ ]] ||
			fail "tool is not pinned to an exact Fedora 44 NEVRA: ${nevra}"
	done
	[[ "$(tool_names | tr '\n' ' ')" == "rpm rpm-sign createrepo_c dnf5 gnupg2 python3 python3-libs " ]] ||
		fail "TOOL_NEVRAS does not name rpm, rpm-sign, createrepo_c, dnf5, gnupg2, python3 and python3-libs"
	declare -f prepare_tools_image | grep -q 'install __TOOL_NEVRAS__' || fail "the tools image installs unpinned names"
	declare -f prepare_tools_image | grep -q -- '--iidfile' || fail "the tools image ID is not recorded"
	declare -f qualify | grep -q 'check_tool_identity "$image_id"' || fail "the tool identity is not checked"
	declare -f qualify | grep -q '"$image_id" bash /src/scripts/test-rpm-repository.sh --inside-qualify' ||
		fail "the transaction does not run the image by its ID"
	if declare -f qualify prepare_tools_image | grep -q -E -- '(^|[[:space:]])-t[[:space:]]|:local'; then
		fail "the tools image is tagged"
	fi
	local docker_bin identity
	docker_bin=$(scratch_dir)
	printf '#!/bin/bash\nprintf "%%s\\n" $STUB_RPM_Q\n' >"$docker_bin/docker"
	chmod 0755 "$docker_bin/docker"
	identity="$docker_bin/identity.txt"
	rc=0
	(
		export PATH="$docker_bin:$PATH" STUB_RPM_Q="${TOOL_NEVRAS[*]}"
		check_tool_identity "sha256:$(printf 'a%.0s' {1..64})" "$identity"
	) >/dev/null 2>&1 || rc=$?
	[[ "$rc" -eq 0 ]] || fail "matching tool identity exited ${rc}, want 0"
	rc=0
	(
		export PATH="$docker_bin:$PATH" STUB_RPM_Q="${TOOL_NEVRAS[*]/rpm-sign-6.0.2-1/rpm-sign-6.0.1-2}"
		check_tool_identity "sha256:$(printf 'a%.0s' {1..64})" "$identity"
	) >/dev/null 2>"$docker_bin/drift.err" || rc=$?
	[[ "$rc" -eq 2 ]] || fail "a drifted rpm-sign NEVRA exited ${rc}, want 2"
	grep -q 'rpm-sign-6.0.1-2.fc44.x86_64' "$docker_bin/drift.err" || fail "the drifted NEVRA was not printed"
	printf 'test-rpm-repository: tool NEVRAs are pinned and checked; the image runs by ID\n'

	python3 - "$signer" "$renderer" <<'PY'
import ast
import pathlib
import sys
signer = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
renderer = pathlib.Path(sys.argv[2]).read_text(encoding="utf-8")
module = ast.parse(signer)
found = False
for node in ast.walk(module):
    if isinstance(node, ast.Assign):
        for target in node.targets:
            if isinstance(target, ast.Name) and target.id == "DOCUMENTED_SIGN_ARGV":
                value = ast.literal_eval(node.value)
                if value != ("rpmsign", "--addsign", "--key-id"):
                    raise SystemExit("signer prefix drifted")
                found = True
if not found:
    raise SystemExit("documented signer prefix is missing")
if "DOCUMENTED_CREATEREPO_ARGV" not in renderer:
    raise SystemExit("documented createrepo argv is missing")
if 'createrepo_c", "--checksum", "sha256", "--general-compress-type", "gz"' not in renderer:
    raise SystemExit("createrepo argv drifted")
print("test-rpm-repository: documented commands are unchanged")
PY

	local owned digest client_digest
	owned=("$signer" "$renderer" "$client" "$template" "$workflow")
	for path in "${owned[@]}"; do
		[[ -f "$path" ]] || blind "owned file is absent: $path"
		if grep -n -E 'gpgcheck=0|fedora:latest|rpm -U --nodeps|--privileged' "$path" >/dev/null; then
			fail "forbidden marker in ${path#"$root"/}"
		fi
	done
	grep -q 'pkg_gpgcheck=1' "$template" || fail "template missing pkg_gpgcheck=1"
	grep -q 'repo_gpgcheck=1' "$template" || fail "template missing repo_gpgcheck=1"
	grep -q 'runs-on: ubuntu-latest' "$workflow" || fail "workflow is not pinned to ubuntu-latest"
	grep -q 'actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1' "$workflow" || fail "checkout action is not pinned"
	grep -q -- '--rm' "$client" || fail "client container is not removed"
	grep -q -- '--network none' "$client" || fail "client container is not network-none"
	grep -q 'OLIVARES_PACKAGE_REPO_TEST_ONLY' "$workflow" || fail "workflow does not set the test-only latch"
	grep -q 'scripts/test-rpm-repository.sh --fetch-release' "$workflow" || fail "workflow does not fetch through fetch_release"
	if grep -q -E '(^|[[:space:]])curl[[:space:]]' "$workflow"; then
		fail "workflow downloads outside fetch_release"
	fi
	digest=$(grep -o 'sha256:[0-9a-f]\{64\}' "$workflow" | head -n 1 || true)
	client_digest=$(grep -o 'sha256:[0-9a-f]\{64\}' "$client" | head -n 1 || true)
	[[ -n "$digest" && "$digest" == "$client_digest" ]] || fail "workflow image digest does not match the client"
	printf 'test-rpm-repository: static self-test passed\n'
	cleanup_key_anchors
	rm -rf "$rendered_dir" "$stub" "$bin" "$tools" "$keys" "$paths_repo" "$curl_bin" "$fetched" "$docker_bin" \
		"$delivery" "$realbin" "$tampered_rpm" "$lockedbin" "$publish_dir" "$client_bin" \
		"$publish_bin" "$publish_keys"
}

# The pinned Fedora 44 image, measured 2026-09-27T16:50Z by anonymous registry
# GETs: index sha256:539cadb5d8a43564d8abefd6eafdfcbcd4809070efbb900ec248229903db5911,
# amd64 manifest sha256:111c574c9647d837ae22edf3204b670d5a151bcbb1cf2d13928c215b8e6488c4,
# one layer sha256:99cda56b847bb13c23ee9f2b2207d89bc1fd961355de6cfaef5c65b7c994fc0b
# (file list and rpmdb NEVRAs in the r4 evidence). These are the commands the
# scripts run inside a container that the layer ships in /usr/bin or
# /usr/libexec. python3 and cmp (diffutils) are absent from it.
BASE_IMAGE_COMMANDS=(
	bash cat chmod cp cut dirname dnf5 find gpg gpg-agent gpg-preset-passphrase gpgconf grep head
	mkdir mktemp rm rpm rpmkeys sed sha256sum sort tee
)

# container_commands DRIVER CLIENT SIGNER RENDERER [SCOPE]: every external command run
# inside the client container (base image) and the tools container (base image
# plus what the Containerfile requires with command -v) must be one the image
# ships. SCOPE is client or all. Commands are read from the bash function bodies (declare -f) reached
# from the in-container entry points, and from the tools the Python scripts
# start. It exits 1 naming each command the image lacks.
container_commands() {
	local driver="$1" client_file="$2" signer_file="$3" renderer_file="$4" scope="${5:-all}" dump rc=0
	dump=$(scratch_dir)
	bash -c 'source <(sed "\$d" "$1") >/dev/null 2>&1; source "$2" >/dev/null 2>&1; declare -f' \
		_ "$driver" "$client_file" >"$dump/functions.sh" || blind "could not read the in-container functions"
	python3 - "$dump/functions.sh" "$signer_file" "$renderer_file" "${BASE_IMAGE_COMMANDS[*]}" "$scope" <<'PY' || rc=$?
import re
import sys
from pathlib import Path

functions_file, signer_file, renderer_file, base_words, scope = sys.argv[1:]
base = set(base_words.split())
BUILTINS = set(
    ": . [ alias bg bind break builtin caller cd command compgen complete compopt continue declare dirs "
    "disown echo enable eval exec exit export false fc fg getopts hash help history jobs kill let local "
    "logout mapfile popd printf pushd pwd read readarray readonly return set shift shopt source suspend "
    "test times trap true type typeset ulimit umask unalias unset wait".split()
)
KEYWORDS = set("if then else elif fi case esac for select while until do done in function time { } ! [[ ]]".split())
WRAPPERS = {"timeout", "env", "nice"}

functions = {}
raw = {}
name = None
lines = Path(functions_file).read_text(encoding="utf-8").splitlines()
index = 0
while index < len(lines):
    line = lines[index]
    header = re.fullmatch(r"(\S+) \(\) ", line)
    if header:
        name = header.group(1)
        functions[name] = []
        raw[name] = []
    elif name is not None:
        heredoc = re.search(r"<<-?\s*'?([A-Za-z_]+)'?", line)
        functions[name].append(line)
        raw[name].append(line)
        if heredoc:
            terminator = heredoc.group(1)
            index += 1
            while index < len(lines) and lines[index].strip() != terminator:
                raw[name].append(lines[index])
                index += 1
    index += 1


def unquote(line):
    """Drop quoted text but keep $(...) inside double quotes."""
    out, index, quote = [], 0, None
    while index < len(line):
        char = line[index]
        if quote == "'":
            quote = None if char == "'" else quote
        elif char == "\\":
            index += 1
        elif line.startswith("$(", index):
            depth, start = 1, index
            index += 2
            while index < len(line) and depth:
                depth += {"(": 1, ")": -1}.get(line[index], 0)
                index += 1
            out.append("\n" + unquote(line[start + 2 : index - 1]) + "\n")
            continue
        elif quote == '"':
            quote = None if char == '"' else quote
        elif char in "'\"":
            quote = char
            out.append(" % ")
        else:
            out.append(char)
        index += 1
    return "".join(out)


def commands(body):
    found = set()
    for line in body:
        text = unquote(line).replace("$(", "\n").replace("`", "\n")
        for segment in re.split(r"\n|;|&&|\|\||\||\(\(|\)\)|(?<![$<>])\(", text):
            words = segment.strip().split()
            while words and (words[0] in KEYWORDS or re.match(r"[A-Za-z_][A-Za-z0-9_]*(\[[^]]*\])?\+?=", words[0])):
                if words[0] in ("for", "case", "select", "in"):
                    words = []
                    break
                words = words[1:]
            if not words:
                continue
            word = words[0]
            if word in WRAPPERS:
                rest = [w for w in words[1:] if not w.startswith("-") and not re.fullmatch(r"[0-9]+[smh]?", w) and "=" not in w]
                if rest:
                    found.add(rest[0])
                found.add(word)
                continue
            if not re.fullmatch(r"[A-Za-z_./][A-Za-z0-9_.+/-]*", word) or word.endswith(")") or word.startswith(("-", '"', "'", "$", ">", "<", "*", "}", "]")) or word in BUILTINS:
                continue
            found.add(word)
    return found


def closure(roots):
    seen, external, stack = set(), set(), list(roots)
    while stack:
        current = stack.pop()
        if current in seen:
            continue
        seen.add(current)
        for word in commands(functions.get(current, [])):
            if word in functions:
                stack.append(word)
            else:
                external.add(word)
    return external


def containerfile_tools():
    body = "\n".join(raw.get("prepare_tools_image", []))
    return set(re.findall(r"command -v ([A-Za-z0-9_.+-]+)", body))


def python_tools():
    tools = set()
    for source in (signer_file, renderer_file):
        text = Path(source).read_text(encoding="utf-8")
        tools |= set(re.findall(r'(?:require_tool|shutil\.which|which)\("([^"]+)"\)', text))
        tools |= set(re.findall(r'/ "(gpg-preset-passphrase)"', text))
    return tools


failures = []
client = closure(["run_inside"])
for word in sorted(client - base):
    failures.append(f"the client container (base image) runs {word}, which the image does not ship")
if scope == "all":
    tools_image = base | containerfile_tools()
    tools = closure(["inside_publish_rpm", "inside_qualify", "run_inside"])
    if "python3" in tools:
        tools |= python_tools()
    for word in sorted(tools - tools_image):
        failures.append(f"the tools container runs {word}, which the tools image does not ship")
    print("test-rpm-repository: tools container commands: " + " ".join(sorted(tools)))
print("test-rpm-repository: client container commands: " + " ".join(sorted(client)))
for failure in failures:
    print(f"test-rpm-repository: refused — {failure}", file=sys.stderr)
raise SystemExit(1 if failures else 0)
PY
	rm -rf -- "$dump"
	return "$rc"
}

# The signing and client toolchain, by exact NEVRA. Fedora 44 stable on
# https://packages.fedoraproject.org/pkgs/<source>/<name>/ read 2026-09-27T15:16Z
# for rpm, rpm-sign, createrepo_c, dnf5 and gnupg2, and 2026-09-27T16:50Z for
# python3 and python3-libs (source python3.14). rpm, dnf5 and gnupg2 are the
# NEVRAs the pinned base image already holds (its rpmdb, measured 16:50Z).
# python3 is here because the signer and the renderer are python3 programs that
# drive rpmsign, rpm, rpmkeys, createrepo_c and gpg-preset-passphrase, which exist
# only in this image; the base image has no python3. When Fedora ships a newer
# build, the install or the identity check fails closed and names what it found;
# update these lines, never the check.
TOOL_NEVRAS=(
	rpm-6.0.2-1.fc44.x86_64
	rpm-sign-6.0.2-1.fc44.x86_64
	createrepo_c-1.2.1-5.fc44.x86_64
	dnf5-5.4.5.0-1.fc44.x86_64
	gnupg2-2.4.9-16.fc44.x86_64
	python3-3.14.7-1.fc44.x86_64
	python3-libs-3.14.7-1.fc44.x86_64
)

# Print the tool names of TOOL_NEVRAS, in order, one per line.
tool_names() {
	local nevra
	for nevra in "${TOOL_NEVRAS[@]}"; do
		printf '%s\n' "${nevra%-*-*}"
	done
}

# Build the tools image from the pinned base and print its image ID. The image
# is used by that ID, never by a tag (docker build --iidfile, docs.docker.com
# reference/cli/docker/buildx/build, read 2026-09-27).
prepare_tools_image() {
	local base="$1" iidfile="$2"
	local dockerfile
	dockerfile="$(scratch_dir)/Containerfile"
	cat >"$dockerfile" <<'EOF'
FROM __BASE_IMAGE__
RUN dnf -y --setopt=install_weak_deps=False install __TOOL_NEVRAS__ \
 && if ! command -v dnf5 >/dev/null 2>&1; then \
      if dnf --version 2>&1 | head -n 1 | grep -q dnf5; then ln -s "$(command -v dnf)" /usr/local/bin/dnf5; \
      else printf '%s\n' 'dnf5 is absent' >&2; exit 1; fi; \
    fi \
 && command -v createrepo_c \
 && command -v rpmsign \
 && command -v gpg \
 && command -v dnf5 \
 && command -v python3
EOF
	local quoted
	quoted=$(printf '%s\n' "$base" | sed 's/[\/&]/\\&/g')
	sed -i "s/__BASE_IMAGE__/${quoted}/" "$dockerfile"
	sed -i "s/__TOOL_NEVRAS__/${TOOL_NEVRAS[*]}/" "$dockerfile"
	timeout 600 docker build --network=default --iidfile "$iidfile" -f "$dockerfile" "$(dirname -- "$dockerfile")" >&2
	local image_id
	image_id=$(<"$iidfile")
	[[ "$image_id" =~ ^sha256:[0-9a-f]{64}$ ]] || blind "docker build wrote no image ID"
	printf '%s\n' "$image_id"
}

# Refuse a tools image whose installed NEVRAs are not exactly TOOL_NEVRAS.
check_tool_identity() {
	local image_id="$1" record="$2"
	local names=()
	mapfile -t names < <(tool_names)
	docker run --rm --network none "$image_id" rpm -q --qf '%{NEVRA}\n' "${names[@]}" >"$record" ||
		blind "rpm -q could not read the tools image"
	if ! printf '%s\n' "${TOOL_NEVRAS[@]}" | cmp -s - "$record"; then
		printf 'test-rpm-repository: installed tool NEVRAs:\n' >&2
		cat -- "$record" >&2
		blind "tools image identity differs from TOOL_NEVRAS"
	fi
	printf 'test-rpm-repository: tools image %s\n' "$image_id"
	sed 's/^/test-rpm-repository: tool /' "$record"
}

reset_package() {
	local rpm_file="$1" name
	name=$(rpm -qp --qf '%{NAME}' "$rpm_file")
	dnf5 remove -y "$name" >/dev/null 2>&1 || true
	dnf5 clean all >/dev/null 2>&1 || true
}

copy_tree() {
	local source="$1" dest="$2"
	rm -rf "$dest"
	mkdir -p "$dest"
	cp -a "$source"/. "$dest"/
}

inside_qualify() {
	local rpm_file=""
	while [[ "$#" -gt 0 ]]; do
		case "$1" in
		--rpm) rpm_file="${2:-}"; shift 2 ;;
		*) blind "unknown inside argument: $1" ;;
		esac
	done
	[[ "$rpm_file" == /* && -f "$rpm_file" ]] || blind "--rpm must be an absolute file"
	command -v createrepo_c >/dev/null 2>&1 || blind "createrepo_c is absent"
	command -v rpmsign >/dev/null 2>&1 || blind "rpmsign is absent"
	command -v dnf5 >/dev/null 2>&1 || blind "dnf5 is absent"
	command -v python3 >/dev/null 2>&1 || blind "python3 is absent from the tools image; TOOL_NEVRAS pins it"
	mkdir -p /work/tmp /work/cases
	export TMPDIR=/work/tmp
	local results=/work/case-results.tsv
	: >"$results"
	local unsigned=/work/cases/unsigned
	mkdir -p "$unsigned"
	cp -- "$rpm_file" "$unsigned/"
	rpmsign --delsign "$unsigned"/*.rpm || true
	if rpm -qp --qf '%{RSAHEADER:pgpsig}\n%{DSAHEADER:pgpsig}\n%{SIGPGP:pgpsig}\n%{SIGGPG:pgpsig}\n%{OPENPGP}\n' "$unsigned"/*.rpm |
		grep -v -E '^\(none\)$|^$' | grep -q .; then
		fail "unsigned case still has a signature header"
	fi
	python3 "$renderer" render --repo "$unsigned"
	reset_package "$rpm_file"
	local rc=0
	bash "$client" --inside --repo "$unsigned" --gpgkey /keys/openpgp-public.asc --expect refuse || rc=$?
	[[ "$rc" -eq 0 ]] || fail "unsigned rpm was not refused (client exit ${rc})"
	printf 'unsigned\trefused\n' | tee -a "$results"

	local signed=/work/cases/signed
	mkdir -p "$signed"
	cp -- "$rpm_file" "$signed/"
	python3 "$signer" --rpm "$signed"/*.rpm
	python3 "$renderer" render --repo "$signed"
	python3 "$renderer" verify --repo "$signed" --public-key /keys/openpgp-public.asc
	python3 "$renderer" verify-delivery --repo "$signed" --public-key /keys/openpgp-public.asc
	cat -- "$signed/delivery.json"
	# Record only: the published package's format (RPMFORMAT tag extension,
	# rpm 6.0 lib/tagexts.cc) and the signature rpmkeys reads with the approved key.
	rpm -qp --qf 'test-rpm-repository: input %{NEVRA} rpmformat=%{RPMFORMAT} sha256=' "$rpm_file" ||
		printf 'test-rpm-repository: input rpmformat query failed\n'
	sha256sum -- "$rpm_file" | cut -d' ' -f1
	mkdir -p /work/tmp/rpmdb-record
	rpmkeys --dbpath /work/tmp/rpmdb-record --import /keys/openpgp-public.asc
	rpmkeys --dbpath /work/tmp/rpmdb-record -Kv "$signed"/*.rpm ||
		printf 'test-rpm-repository: rpmkeys -Kv of the signed rpm exited non-zero\n'

	local tampered=/work/cases/tampered
	copy_tree "$signed" "$tampered"
	rm -rf "$tampered/repodata" "$tampered/checksums.txt"
	python3 - "$tampered" <<'PY'
import pathlib, sys
paths = sorted(pathlib.Path(sys.argv[1]).glob("*.rpm"))
if len(paths) != 1:
    raise SystemExit("expected one rpm to tamper")
data = bytearray(paths[0].read_bytes())
if not data:
    raise SystemExit("rpm is empty")
data[-1] ^= 0x01
paths[0].write_bytes(data)
PY
	python3 "$renderer" render --repo "$tampered"
	reset_package "$rpm_file"
	rc=0
	bash "$client" --inside --repo "$tampered" --gpgkey /keys/openpgp-public.asc --expect refuse || rc=$?
	[[ "$rc" -eq 0 ]] || fail "tampered rpm was not refused (client exit ${rc})"
	printf 'tampered\trefused\n' | tee -a "$results"

	# RPM-04: the rpm is signed by a second disposable key; repomd is signed by
	# the approved key. DNF5 holds only the approved key.
	local foreign_rpm=/work/cases/foreign-key-rpm
	mkdir -p "$foreign_rpm"
	cp -- "$rpm_file" "$foreign_rpm/"
	rpmsign --delsign "$foreign_rpm"/*.rpm || true
	with_foreign_key python3 "$signer" --rpm "$foreign_rpm"/*.rpm
	python3 "$renderer" render --repo "$foreign_rpm"
	python3 "$renderer" verify --repo "$foreign_rpm" --public-key /keys/openpgp-public.asc
	reset_package "$rpm_file"
	rc=0
	bash "$client" --inside --repo "$foreign_rpm" --gpgkey /keys/openpgp-public.asc --expect refuse || rc=$?
	[[ "$rc" -eq 0 ]] || fail "rpm signed by a foreign key was not refused (client exit ${rc})"
	printf 'foreign-key-rpm\trefused\n' | tee -a "$results"

	local bad=/work/cases/bad-asc
	copy_tree "$signed" "$bad"
	printf 'not a signature\n' >"$bad/repodata/repomd.xml.asc"
	reset_package "$rpm_file"
	rc=0
	bash "$client" --inside --repo "$bad" --gpgkey /keys/openpgp-public.asc --expect refuse || rc=$?
	[[ "$rc" -eq 0 ]] || fail "bad repomd.xml.asc was not refused (client exit ${rc})"
	printf 'bad-repomd-asc\trefused\n' | tee -a "$results"

	local missing=/work/cases/missing-asc
	copy_tree "$signed" "$missing"
	rm -f "$missing/repodata/repomd.xml.asc"
	reset_package "$rpm_file"
	rc=0
	bash "$client" --inside --repo "$missing" --gpgkey /keys/openpgp-public.asc --expect refuse || rc=$?
	[[ "$rc" -eq 0 ]] || fail "missing repomd.xml.asc was not refused (client exit ${rc})"
	printf 'missing-repomd-asc\trefused\n' | tee -a "$results"

	# RPM-04: the rpm is signed by the approved key; repomd.xml.asc is made by a
	# second disposable key. DNF5 holds only the approved key.
	local foreign_md=/work/cases/foreign-key-repomd
	copy_tree "$signed" "$foreign_md"
	with_foreign_key python3 "$renderer" render --repo "$foreign_md"
	rc=0
	python3 "$renderer" verify --repo "$foreign_md" --public-key /keys/openpgp-public.asc \
		>/dev/null 2>/tmp/foreign-md-verify.err || rc=$?
	[[ "$rc" -eq 1 ]] || fail "foreign-key repomd was not refused by the verifier with the approved key (exit ${rc})"
	reset_package "$rpm_file"
	rc=0
	bash "$client" --inside --repo "$foreign_md" --gpgkey /keys/openpgp-public.asc --expect refuse || rc=$?
	[[ "$rc" -eq 0 ]] || fail "repomd signed by a foreign key was not refused (client exit ${rc})"
	printf 'foreign-key-repomd\trefused\n' | tee -a "$results"

	local wrong=/work/cases/wrong-hash
	copy_tree "$signed" "$wrong"
	python3 - "$wrong" <<'PY'
import pathlib, sys
repo = pathlib.Path(sys.argv[1]) / "repodata"
matches = sorted(path for path in repo.iterdir() if path.name.endswith(".xml.gz"))
if not matches:
    raise SystemExit("no compressed metadata to alter")
data = bytearray(matches[0].read_bytes())
data[0] ^= 0x01
matches[0].write_bytes(data)
PY
	rc=0
	python3 "$renderer" verify --repo "$wrong" >/dev/null 2>/tmp/wrong-verify.err || rc=$?
	[[ "$rc" -eq 1 ]] || fail "wrong metadata hash was not refused by the verifier (exit ${rc})"
	reset_package "$rpm_file"
	rc=0
	bash "$client" --inside --repo "$wrong" --gpgkey /keys/openpgp-public.asc --expect refuse || rc=$?
	[[ "$rc" -eq 0 ]] || fail "wrong metadata hash was not refused by dnf5 (client exit ${rc})"
	printf 'wrong-hash\trefused\n' | tee -a "$results"

	local unnamed=/work/cases/unnamed
	copy_tree "$signed" "$unnamed"
	printf 'x\n' >"$unnamed/repodata/not-named.xml.gz"
	rc=0
	python3 "$renderer" verify --repo "$unnamed" >/dev/null 2>/tmp/unnamed-verify.err || rc=$?
	[[ "$rc" -eq 1 ]] || fail "unnamed metadata was not refused (exit ${rc})"
	printf 'unnamed-metadata\trefused\n' | tee -a "$results"

	reset_package "$rpm_file"
	rc=0
	mkdir -p /work/client-out
	bash "$client" --inside --repo "$signed" --gpgkey /keys/openpgp-public.asc --expect install \
		--out-dir /work/client-out || rc=$?
	[[ "$rc" -eq 0 ]] || fail "valid transaction did not install (client exit ${rc})"
	rc=0
	bash "$client" --check-repo-info /work/client-out/repo-info.json || rc=$?
	[[ "$rc" -eq 0 ]] || fail "the valid transaction's effective gpg checks are not both true (exit ${rc})"
	printf 'valid\tinstalled\n' | tee -a "$results"
	# The base image has no cmp (diffutils); compare in bash.
	local expected_results
	expected_results=$(printf '%s\n' "${QUALIFY_CASES[@]}")
	[[ "$(<"$results")" == "$expected_results" ]] ||
		fail "case-results.tsv is not exactly the ${#QUALIFY_CASES[@]} required lines"
	printf 'test-rpm-repository: Fedora 44 cases passed\n'
}

# The publisher's RPM path. The release rpms (checksums.txt already
# authenticated by the caller) are checked again here, signed once per arch
# with the repository key, and rendered into OUT/<channel>/rpm/<arch> for
# stable and security: rpm-md, repomd.xml.asc and delivery.json, each verified
# against the reviewed public key. Everything runs in the tools image, by ID,
# with TOOL_NEVRAS enforced, --network none, as the caller's uid. The key
# policy is S3's: a production descriptor needs no latch, and
# OLIVARES_PACKAGE_REPO_TEST_ONLY=1 is forwarded only when the caller set it.
RPM_ARCHES=(amd64:x86_64 arm64:aarch64)
RPM_CHANNELS=(stable security)

publish_rpm() {
	local assets="" version="" out="" public_key="" image="" work=""
	# shellcheck disable=SC1090
	source "$client"
	image="$FEDORA_44_IMAGE"
	while [[ "$#" -gt 0 ]]; do
		case "$1" in
		--assets) assets="${2:-}"; shift 2 ;;
		--version) version="${2:-}"; shift 2 ;;
		--out) out="${2:-}"; shift 2 ;;
		--public-key) public_key="${2:-}"; shift 2 ;;
		--image) image="${2:-}"; shift 2 ;;
		--work) work="${2:-}"; shift 2 ;;
		*) blind "unknown publish argument: $1" ;;
		esac
	done
	[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || blind "--version must be MAJOR.MINOR.PATCH"
	[[ "$assets" == /* && -d "$assets" && -f "$assets/checksums.txt" ]] || blind "--assets must hold checksums.txt"
	[[ "$out" == /* && ! -e "$out" && -d "$(dirname -- "$out")" ]] || blind "--out must be a new absolute path"
	local descriptor="${OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE:-}"
	local secret="${OLIVARES_PACKAGE_REPO_OPENPGP_SECRET_KEY_FILE:-}"
	local passphrase="${OLIVARES_PACKAGE_REPO_OPENPGP_PASSPHRASE_FILE:-}"
	[[ "$descriptor" == /* && -f "$descriptor" && "$secret" == /* && -f "$secret" ]] || blind "key descriptor or secret is absent"
	[[ "$public_key" == /* && -f "$public_key" ]] || blind "--public-key must be an absolute file"
	[[ -z "$passphrase" || ( "$passphrase" == /* && -f "$passphrase" ) ]] || blind "the passphrase file is absent"
	case "$image" in
	*@sha256:*) ;;
	*) blind "image is not pinned by digest" ;;
	esac
	command -v docker >/dev/null 2>&1 || blind "docker is absent"
	local pair asset
	if [[ -n "$work" ]]; then
		[[ "$work" == /* && ! -e "$work" && -d "$(dirname -- "$work")" ]] || blind "--work must be a new absolute path"
		mkdir -m 0700 -- "$work"
	else
		work=$(scratch_dir)
	fi
	PUBLISH_WORK="$work"
	mkdir -p "$work/input" "$work/out" "$work/tmp"
	for pair in "${RPM_ARCHES[@]}"; do
		asset="olivares_${version}_linux_${pair%%:*}.rpm"
		check_release_sha256 "$assets/checksums.txt" "$assets/$asset" "$asset"
		cp -- "$assets/$asset" "$work/input/"
	done
	timeout 300 docker pull "$image"
	local image_id
	image_id=$(prepare_tools_image "$image" "$work/tools.iid")
	check_tool_identity "$image_id" "$work/tool-identity.txt"
	if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
		{
			printf 'rpm tools image `%s` from `%s`\n\n' "$image_id" "$image"
			sed 's/^/- /' "$work/tool-identity.txt"
		} >>"$GITHUB_STEP_SUMMARY"
	fi
	# Only the four files the step needs, each read-only: never the key
	# directory, which also holds the APK private key.
	local key_env=(
		--env OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE=/keys/descriptor.json
		--env OLIVARES_PACKAGE_REPO_OPENPGP_SECRET_KEY_FILE=/keys/openpgp-secret.asc
		--volume "${descriptor}:/keys/descriptor.json:ro"
		--volume "${secret}:/keys/openpgp-secret.asc:ro"
		--volume "${public_key}:/keys/public.asc:ro"
	)
	[[ -z "$passphrase" ]] || key_env+=(
		--env OLIVARES_PACKAGE_REPO_OPENPGP_PASSPHRASE_FILE=/keys/passphrase
		--volume "${passphrase}:/keys/passphrase:ro"
	)
	[[ -z "${OLIVARES_PACKAGE_REPO_TEST_ONLY:-}" ]] || key_env+=(--env OLIVARES_PACKAGE_REPO_TEST_ONLY="$OLIVARES_PACKAGE_REPO_TEST_ONLY")
	# --init: bash is not PID 1, so the TERM that timeout forwards stops the step.
	timeout 1200 docker run --init --rm --network none --security-opt no-new-privileges \
		--user "$(id -u):$(id -g)" "${key_env[@]}" --env TMPDIR=/work/tmp \
		--volume "${root}:/src:ro" --volume "${work}:/work" \
		"$image_id" bash /src/scripts/test-rpm-repository.sh --inside-publish-rpm \
		--input /work/input --out /work/out --public-key /keys/public.asc --version "$version"
	docker rmi "$image_id" >/dev/null 2>&1 || true
	mv -- "$work/out" "$out"
	cleanup_publish_work
	printf 'test-rpm-repository: S3 rpm trees for %s at %s\n' "${RPM_CHANNELS[*]}" "$out"
}

# One authenticated checksums.txt line must name the asset, and match it.
check_release_sha256() {
	python3 - "$@" <<'PY'
import hashlib
import sys
from pathlib import Path

checksums, rpm_file, name = sys.argv[1:]
expected = [
    parts[0]
    for parts in (line.split() for line in Path(checksums).read_text(encoding="utf-8").splitlines())
    if len(parts) == 2 and parts[1].lstrip("*") == name
]
if len(expected) != 1 or len(expected[0]) != 64:
    print(f"test-rpm-repository: refused — checksums.txt has no single sha256 line for {name}", file=sys.stderr)
    raise SystemExit(1)
path = Path(rpm_file)
if not path.is_file() or path.stat().st_size == 0:
    print(f"test-rpm-repository: refused — {name} is missing or zero bytes", file=sys.stderr)
    raise SystemExit(1)
if hashlib.sha256(path.read_bytes()).hexdigest() != expected[0].lower():
    print(f"test-rpm-repository: refused — {name} does not match published checksums.txt", file=sys.stderr)
    raise SystemExit(1)
PY
}

# Inside the tools image (or under the fixture's stub tools): sign each release
# rpm once, then render and verify one rpm-md tree per channel and arch.
inside_publish_rpm() {
	local input="" out="" public_key="" version=""
	while [[ "$#" -gt 0 ]]; do
		case "$1" in
		--input) input="${2:-}"; shift 2 ;;
		--out) out="${2:-}"; shift 2 ;;
		--public-key) public_key="${2:-}"; shift 2 ;;
		--version) version="${2:-}"; shift 2 ;;
		*) blind "unknown inside publish argument: $1" ;;
		esac
	done
	[[ -d "$input" && -d "$out" && -f "$public_key" ]] || blind "inside publish mounts are absent"
	[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || blind "--version must be MAJOR.MINOR.PATCH"
	command -v python3 >/dev/null 2>&1 || blind "python3 is absent from the tools image; TOOL_NEVRAS pins it"
	local fingerprint
	fingerprint=$(python3 -c 'import json, os; print(json.load(open(os.environ["OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE"]))["openpgp_fingerprint"])') ||
		blind "the key descriptor has no fingerprint"
	local pair asset arch identity rc channel dir
	local signed="$input/signed" served=()
	mkdir -p "$signed"
	for pair in "${RPM_ARCHES[@]}"; do
		asset="olivares_${version}_linux_${pair%%:*}.rpm"
		arch="${pair##*:}"
		[[ -f "$input/$asset" ]] || blind "release rpm is absent: ${asset}"
		cp -- "$input/$asset" "$signed/$asset"
		identity=$(rpm -qp --qf '%{NAME} %{VERSION} %{ARCH}\n' "$signed/$asset") || blind "rpm cannot read ${asset}"
		[[ "$identity" == "olivares ${version} ${arch}" ]] || fail "${asset} is '${identity}', want 'olivares ${version} ${arch}'"
	done
	rc=0
	python3 "$signer" --rpm "$signed/olivares_${version}_linux_amd64.rpm" --rpm "$signed/olivares_${version}_linux_arm64.rpm" || rc=$?
	[[ "$rc" -eq 0 ]] || exit "$rc"
	for channel in "${RPM_CHANNELS[@]}"; do
		for pair in "${RPM_ARCHES[@]}"; do
			asset="olivares_${version}_linux_${pair%%:*}.rpm"
			dir="$out/$channel/rpm/${pair##*:}"
			mkdir -p "$dir"
			cp -- "$signed/$asset" "$dir/$asset"
			python3 "$renderer" render --repo "$dir" --expect-package olivares
			python3 "$renderer" verify-delivery --repo "$dir" --fingerprint "$fingerprint" \
				--public-key "$public_key" --expect-package olivares
			served+=("$dir/$asset")
		done
	done
	# R3-m1: every served rpm's header signature, checked with the reviewed
	# public key alone (not the key the signer exported from its secret).
	python3 "$signer" --verify-with "$public_key" "${served[@]/#/--rpm=}"
}

# Test support only: the inside publish path with stub Fedora tools and real
# gpg, so the publisher batteries can hold a genuine S3 tree without a
# container. It refuses to run without OLIVARES_PACKAGE_REPO_TEST_ONLY=1.
fixture_publish_tree() {
	local assets="" version="" key_dir="" out=""
	while [[ "$#" -gt 0 ]]; do
		case "$1" in
		--assets) assets="${2:-}"; shift 2 ;;
		--version) version="${2:-}"; shift 2 ;;
		--key-dir) key_dir="${2:-}"; shift 2 ;;
		--out) out="${2:-}"; shift 2 ;;
		*) blind "unknown fixture argument: $1" ;;
		esac
	done
	[[ "${OLIVARES_PACKAGE_REPO_TEST_ONLY:-}" == 1 ]] || blind "the fixture tree requires OLIVARES_PACKAGE_REPO_TEST_ONLY=1"
	[[ "$assets" == /* && "$key_dir" == /* && "$out" == /* && ! -e "$out" ]] || blind "fixture paths must be absolute and --out new"
	local bin work pair tool
	bin=$(scratch_dir)
	work=$(scratch_dir)
	write_stub_tools "$bin"
	rm -f -- "$bin/gpg"
	ln -s "$(command -v gpg)" "$bin/gpg"
	ln -s "$(command -v gpgconf)" "$bin/gpgconf"
	for tool in bash cp dirname mkdir; do
		ln -s "$(command -v "$tool")" "$bin/$tool"
	done
	mkdir -p "$work/input" "$out"
	for pair in "${RPM_ARCHES[@]}"; do
		cp -- "$assets/olivares_${version}_linux_${pair%%:*}.rpm" "$work/input/"
	done
	# A child process, so errexit holds inside it whatever the caller's context.
	local rc=0
	STUB_LOG="$bin/calls.jsonl" PATH="$bin" \
		OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE="$key_dir/descriptor.json" \
		OLIVARES_PACKAGE_REPO_OPENPGP_SECRET_KEY_FILE="$key_dir/openpgp-secret.asc" \
		bash "$root/scripts/test-rpm-repository.sh" --inside-publish-rpm --input "$work/input" --out "$out" \
		--public-key "$key_dir/openpgp-public.asc" --version "$version" || rc=$?
	rm -rf -- "$bin" "$work"
	return "$rc"
}

qualify() {
	local rpms=() image=""
	# shellcheck disable=SC1090
	source "$client"
	image="$FEDORA_44_IMAGE"
	while [[ "$#" -gt 0 ]]; do
		case "$1" in
		--rpm) rpms+=("${2:-}"); shift 2 ;;
		--image) image="${2:-}"; shift 2 ;;
		*) blind "unknown qualify argument: $1" ;;
		esac
	done
	[[ "${#rpms[@]}" -ge 1 ]] || blind "--qualify requires --rpm"
	[[ "${OLIVARES_PACKAGE_REPO_TEST_ONLY:-}" == "1" ]] || blind "OLIVARES_PACKAGE_REPO_TEST_ONLY=1 is required"
	local descriptor="${OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE:-}"
	local secret="${OLIVARES_PACKAGE_REPO_OPENPGP_SECRET_KEY_FILE:-}"
	[[ "$descriptor" == /* && -f "$descriptor" ]] || blind "descriptor file is absent"
	[[ "$secret" == /* && -f "$secret" ]] || blind "OpenPGP secret file is absent"
	[[ "$(dirname -- "$descriptor")" == "$(dirname -- "$secret")" ]] || blind "key files must share one directory"
	case "$image" in
	*@sha256:*) ;;
	*) blind "image is not pinned by digest" ;;
	esac
	command -v docker >/dev/null 2>&1 || blind "docker is absent"
	local work key_dir image_id rpm_file
	work=$(scratch_dir)
	mkdir -p "$work/input" "$work/tmp"
	local item
	for item in "${rpms[@]}"; do
		[[ "$item" == /* && -f "$item" ]] || blind "rpm is not an absolute file"
		cp -- "$item" "$work/input/"
	done
	rpm_file=$(find "$work/input" -maxdepth 1 -type f -name '*.rpm' | head -n 1)
	[[ -n "$rpm_file" ]] || blind "no rpm was copied"
	key_dir=$(dirname -- "$secret")
	local foreign_fpr approved_fpr
	approved_fpr=$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["openpgp_fingerprint"])' "$descriptor")
	foreign_fpr=$(make_test_key "$work/foreign-key")
	[[ "$foreign_fpr" != "$approved_fpr" ]] || blind "the foreign test key has the approved fingerprint"
	timeout 300 docker pull "$image"
	image_id=$(prepare_tools_image "$image" "$work/tools.iid")
	check_tool_identity "$image_id" "$work/tool-identity.txt"
	if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
		{
			printf 'tools image `%s` from `%s`\n\n' "$image_id" "$image"
			sed 's/^/- /' "$work/tool-identity.txt"
		} >>"$GITHUB_STEP_SUMMARY"
	fi
	timeout 900 docker run --rm --network none \
		--security-opt no-new-privileges \
		--env OLIVARES_PACKAGE_REPO_TEST_ONLY=1 \
		--env OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE=/keys/descriptor.json \
		--env OLIVARES_PACKAGE_REPO_OPENPGP_SECRET_KEY_FILE=/keys/openpgp-secret.asc \
		--env TMPDIR=/work/tmp \
		--volume "${root}:/src:ro" \
		--volume "${key_dir}:/keys:ro" \
		--volume "${work}/foreign-key:/foreign-keys:ro" \
		--volume "${work}:/work" \
		"$image_id" \
		bash /src/scripts/test-rpm-repository.sh --inside-qualify --rpm "/work/input/$(basename -- "$rpm_file")"
	docker rmi "$image_id" >/dev/null 2>&1 || true
}

main() {
	local command="${1:-}"
	if [[ "$#" -gt 0 ]]; then
		shift
	fi
	case "$command" in
	--self-test | "") self_test ;;
	--fetch-release) fetch_release "$@" ;;
	--qualify) qualify "$@" ;;
	--inside-qualify) inside_qualify "$@" ;;
	--publish-rpm) publish_rpm "$@" ;;
	--inside-publish-rpm) inside_publish_rpm "$@" ;;
	--fixture-publish-tree) fixture_publish_tree "$@" ;;
	--container-commands)
		container_commands "${1:-$0}" "${2:-$client}" "${3:-$signer}" "${4:-$renderer}" "${5:-all}"
		;;
	-h | --help)
		printf '%s\n' "usage: test-rpm-repository.sh --self-test | --fetch-release VERSION ABS_DIR | --qualify --rpm ABS" \
			"       | --publish-rpm --assets ABS --version X.Y.Z --public-key ABS --out ABS [--image DIGEST]" \
			"       | --fixture-publish-tree --assets ABS --version X.Y.Z --key-dir ABS --out ABS (test key only)"
		;;
	*) blind "unknown command: $command" ;;
	esac
}

main "$@"
