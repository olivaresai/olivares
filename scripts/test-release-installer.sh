#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Hermetic DIST-24-03 battery. Named mutants prove that the release wiring, route
# contract, pin and verify-before-exec invariant are all observed by the gate.
set -euo pipefail

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# Keep executable test doubles off a commonly noexec /tmp. The default is a
# sibling of the checkout, never a path inside the repository under test.
scratch_parent="${OLIVARES_TEST_SCRATCH_ROOT:-$(dirname -- "$root")}"
scratch="$(mktemp -d "$scratch_parent/.olivares-release-installer-test.XXXXXX")"
trap 'rm -rf -- "$scratch"' EXIT
passes=0
case_ok() { printf 'ok %d - %s\n' "$((passes += 1))" "$1"; }
expect_rc() {
  local want="$1" label="$2"
  shift 2
  set +e
  "$@" >"$scratch/out" 2>"$scratch/err"
  local got=$?
  set -e
  [[ "$got" -eq "$want" ]] || {
    printf 'not ok - %s (wanted rc=%s, got rc=%s)\n' "$label" "$want" "$got" >&2
    sed -n '1,120p' "$scratch/out" >&2
    sed -n '1,120p' "$scratch/err" >&2
    exit 1
  }
  case_ok "$label"
}

expect_rc 0 "canonical installer contract" bash "$root/scripts/check-release-installer.sh"

rendered="$scratch/olivares-install-26.9.0.sh"
expect_rc 0 "renderer produces a pinned asset" \
  bash "$root/scripts/render-release-installer.sh" 26.9.0 "$rendered"
[[ "$(stat -c '%a' "$rendered")" = 755 ]] || {
  printf 'rendered asset mode is not 0755\n' >&2
  exit 1
}
grep -Fq "EMBEDDED_VERSION='26.9.0'" "$rendered"
if grep -Fq '@OLIVARES_INSTALLER_VERSION@' "$rendered"; then
  printf 'rendered asset retains the source marker\n' >&2
  exit 1
fi
case_ok "rendered asset has the exact pin and no marker"

# Resolve real installer inputs against a closed release fixture. A wrong tag is
# a missing download, even if its asset basename happens to match another era.
expect_rc 0 "latest, requested and rendered versions download the tag of their era" \
  python3 - "$root" "$scratch" <<'PYERA'
import hashlib
import json
import os
import pathlib
import pty
import subprocess
import sys
import tarfile

root, scratch = map(pathlib.Path, sys.argv[1:])
era = scratch / "era"
fixture = era / "release"
fakebin = era / "bin"
fixture.mkdir(parents=True)
fakebin.mkdir()
for version in ("26.9.0", "26.10.0", "26.11", "26.11.1"):
    payload = era / version
    payload.mkdir()
    binary = payload / "olivares"
    binary.write_text(f"#!/bin/sh\nprintf 'Olivares AI {version}\\n'\n")
    binary.chmod(0o755)
    archive = fixture / f"olivares_{version}_linux_amd64.tar.gz"
    with tarfile.open(archive, "w:gz") as handle:
        handle.add(binary, arcname="olivares")
    stage = fixture / f"olivares-install-{version}.sh"
    subprocess.run(["bash", str(root / "scripts/render-release-installer.sh"),
                    version, str(stage)], check=True, capture_output=True, timeout=20)
rows = []
for asset in fixture.iterdir():
    rows.append(f"{hashlib.sha256(asset.read_bytes()).hexdigest()}  {asset.name}")
(fixture / "checksums.txt").write_text("\n".join(rows) + "\n")
(fixture / "checksums.txt.sig").write_text("test signature\n")
(fixture / "checksums.txt.pem").write_text("test certificate\n")
(fakebin / "curl").write_text(r"""#!/usr/bin/env python3
import json, os, pathlib, sys
args = sys.argv[1:]
url = next(arg for arg in args if arg.startswith('https://'))
with open(os.environ['ERA_URL_LOG'], 'a') as log:
    log.write(url + '\n')
if url == 'https://api.fixture.invalid/repos/olivaresai/olivares/releases/latest':
    print(json.dumps({'tag_name': os.environ['ERA_LATEST']}))
else:
    prefix = 'https://fixture.invalid/olivaresai/olivares/releases/download/' + os.environ['ERA_EXPECT_TAG'] + '/'
    if not url.startswith(prefix) or '/' in url[len(prefix):]:
        sys.exit('unexpected release download: ' + url)
    data = (pathlib.Path(os.environ['ERA_FIXTURE']) / url[len(prefix):]).read_bytes()
    pathlib.Path(args[args.index('-o') + 1]).write_bytes(data)
""")
(fakebin / "cosign").write_text('#!/bin/sh\nprintf "%s\\n" "$*" >>"$ERA_COSIGN_LOG"\nexit 0\n')
for tool in fakebin.iterdir():
    tool.chmod(0o755)

install = root / "scripts/install.sh"
bootstrap = root / "scripts/install-bootstrap.sh"
rendered = fixture / "olivares-install-26.10.0.sh"
# These expectations are literal; neither the fixture nor the oracle derives tags.
monthly = fixture / "olivares-install-26.11.sh"
cases = [
    ("latest monthly", install, "", "26.11", "26.11"),
    ("install monthly bare", install, "26.11", "", "26.11"),
    ("install monthly prefixed", install, "v26.11", "", "26.11"),
    ("bootstrap monthly", bootstrap, "26.11", "", "26.11"),
    ("bootstrap monthly prefixed", bootstrap, "v26.11", "", "26.11"),
    ("rendered monthly pin", monthly, "", "", "26.11"),
    ("patch monthly", install, "26.11.1", "", "26.11.1"),
    ("latest bare 26.10.0", install, "", "26.10.0", "26.10.0"),
    ("latest historical v26.9.0", install, "", "v26.9.0", "v26.9.0"),
    ("latest tag is verbatim even when noncanonical", install, "", "v26.10.0", "v26.10.0"),
    ("interactive bootstrap latest bare 26.10.0", bootstrap, "", "26.10.0", "26.10.0"),
    ("piped bootstrap without --version installs latest", bootstrap, "", "26.11", "26.11"),
    ("install --version 26.10.0", install, "26.10.0", "", "26.10.0"),
    ("install --version v26.10.0", install, "v26.10.0", "", "26.10.0"),
    ("install --version 26.9.0", install, "26.9.0", "", "v26.9.0"),
    ("install --version v26.9.0", install, "v26.9.0", "", "v26.9.0"),
    ("bootstrap --version 26.10.0", bootstrap, "26.10.0", "", "26.10.0"),
    ("bootstrap --version v26.10.0", bootstrap, "v26.10.0", "", "26.10.0"),
    ("bootstrap --version 26.9.0", bootstrap, "26.9.0", "", "v26.9.0"),
    ("bootstrap --version v26.9.0", bootstrap, "v26.9.0", "", "v26.9.0"),
    ("rendered 26.10.0 default pin", rendered, "", "", "26.10.0"),
    ("rendered 26.10.0 bare input", rendered, "26.10.0", "", "26.10.0"),
    ("rendered 26.10.0 prefixed input", rendered, "v26.10.0", "", "26.10.0"),
]
failures = []
for index, (label, script, requested, latest, tag) in enumerate(cases):
    url_log = era / f"urls-{index}"
    destination = era / f"installed-{index}"
    cosign_log = era / f"cosign-{index}"
    env = dict(os.environ, PATH=f"{fakebin}:/usr/bin:/bin", CI="0",
               OLIVARES_NONINTERACTIVE="0", OLIVARES_VERSION="", OLIVARES_COSIGN="",
               OLIVARES_OS="linux", OLIVARES_ARCH="amd64",
               OLIVARES_GITHUB_URL="https://fixture.invalid",
               OLIVARES_GITHUB_API_URL="https://api.fixture.invalid",
               ERA_FIXTURE=str(fixture), ERA_URL_LOG=str(url_log),
               ERA_LATEST=latest, ERA_EXPECT_TAG=tag, ERA_COSIGN_LOG=str(cosign_log))
    args = ["/bin/sh", str(script), "--bindir", str(destination)]
    if requested:
        args += ["--version", requested]
    if label == "piped bootstrap without --version installs latest":
        result = subprocess.run(["/bin/sh", "-s", "--", *args[2:]], env=env,
                                input=script.read_text(), capture_output=True,
                                text=True, timeout=20)
    else:
        master, slave = pty.openpty()
        try:
            result = subprocess.run(args, env=env, stdin=slave, capture_output=True,
                                    text=True, timeout=20)
        finally:
            os.close(slave)
            os.close(master)
    urls = url_log.read_text().splitlines() if url_log.exists() else []
    version = tag.removeprefix("v")
    archive_url = f"https://fixture.invalid/olivaresai/olivares/releases/download/{tag}/olivares_{version}_linux_amd64.tar.gz"
    installed = destination / "olivares"
    if result.returncode != 0 or archive_url not in urls or not os.access(installed, os.X_OK):
        failures.append(label)
        print(f"not ok - {label}: rc={result.returncode}; URLs={urls}", file=sys.stderr)
        print(result.stderr, file=sys.stderr)
    else:
        assert installed.read_bytes() == (era / version / "olivares").read_bytes(), label
        if script == rendered:
            assert not any('/releases/latest' in url for url in urls), label
        if label == "piped bootstrap without --version installs latest":
            assert urls[0] == 'https://api.fixture.invalid/repos/olivaresai/olivares/releases/latest', urls
            calls = cosign_log.read_text().splitlines()
            assert len(calls) == 2 and all(call.startswith('verify-blob ') for call in calls), calls
        print(f"ok - {label}")

# A pin is required only when automation requests it explicitly, not because the
# person's one-line installer arrives on stdin through a pipe.
for variable, value in (("CI", "1"), ("CI", "true"), ("OLIVARES_NONINTERACTIVE", "1")):
    url_log = era / f"refused-{variable}-{value}"
    destination = era / f"refused-install-{variable}-{value}"
    refusal_env = dict(env, **{variable: value}, ERA_URL_LOG=str(url_log))
    result = subprocess.run(["/bin/sh", "-s", "--", "--bindir", str(destination)],
                            env=refusal_env, input=bootstrap.read_text(),
                            capture_output=True, text=True, timeout=20)
    assert result.returncode == 1 and 'must pin --version' in result.stderr, result.stderr
    assert not url_log.exists() and not destination.exists(), 'unpinned automation reached the release API or install destination'
    print(f"ok - explicit {variable}={value} without --version refuses before downloads")
assert not failures, failures

# Copying the printed export and next command must work for an off-PATH prefix,
# including shell metacharacters in its literal directory name.
destination = era / "person's install $literal `text`"
env = dict(env, PATH=f'{fakebin}:/usr/bin:/bin', ERA_EXPECT_TAG='26.10.0')
result = subprocess.run(['/bin/sh', str(install), '--version', '26.10.0',
                         '--bindir', str(destination)],
                        env=env, capture_output=True, text=True, timeout=20)
assert result.returncode == 0, result.stderr
lines = result.stdout.splitlines()
exports = [line.strip() for line in lines if line.strip().startswith('export PATH=')]
assert len(exports) == 1, result.stdout
next_command = next(line.removeprefix('Next: ') for line in lines if line.startswith('Next: '))
assert lines.index(next(line for line in lines if line.strip() == exports[0])) < lines.index('Next: ' + next_command)
shell = subprocess.run(['/bin/sh', '-c', exports[0] + '\ncommand -v olivares\n' + next_command],
                       env=env, capture_output=True, text=True, timeout=20)
assert shell.returncode == 0, shell.stderr
assert str(destination / 'olivares') in shell.stdout, shell.stdout
assert next_command != 'olivares quickstart', next_command
print('ok - off-PATH install prints a usable export and absolute next command')

# A real tar write failure must not be reported as an archive-layout defect.
# /dev/full supplies ENOSPC without filling a filesystem shared with other tests.
if pathlib.Path('/dev/full').exists():
    faultbin = era / 'write-failure-bin'
    faultbin.mkdir()
    (faultbin / 'mktemp').write_text('''#!/bin/sh
dir=$(/usr/bin/mktemp "$@") || exit
ln -s /dev/full "$dir/olivares"
printf '%s\\n' "$dir"
''')
    (faultbin / 'mktemp').chmod(0o755)
    env = dict(env, PATH=f'{faultbin}:{fakebin}:/usr/bin:/bin',
               TMPDIR=str(era), ERA_EXPECT_TAG='26.10.0')
    result = subprocess.run(['/bin/sh', str(install), '--version', '26.10.0',
                             '--bindir', str(era / 'write-failure-prefix')],
                            env=env, capture_output=True, text=True, timeout=20)
    assert result.returncode == 1, result.stderr
    assert 'does not contain a top-level' not in result.stderr, result.stderr
    assert str(era) in result.stderr and 'TMPDIR' in result.stderr, result.stderr
    assert not (era / 'write-failure-prefix' / 'olivares').exists()
    print('ok - tar write failure preserves the cause and names temporary-space recovery')
PYERA

expect_rc 0 "concurrent renders publish identical complete bytes independently of TMPDIR" \
  python3 - "$root" "$scratch" "$rendered" <<'PY'
import concurrent.futures
import os
import pathlib
import subprocess
import sys

root, scratch, reference = map(pathlib.Path, sys.argv[1:])
destination = scratch / "concurrent-installer.sh"
# A caller's unusable global temp directory must not affect publication into a
# writable output directory. This also detects the old cross-filesystem design
# deterministically on hosts where /tmp and the checkout share a filesystem.
unusable = scratch / "not-a-temp-directory"
unusable.write_text("ordinary file\n")
env = dict(os.environ, TMPDIR=str(unusable))

def render(_):
    return subprocess.run(
        ["bash", str(root / "scripts/render-release-installer.sh"),
         "26.9.0", str(destination)],
        env=env, capture_output=True, text=True, timeout=30,
    )

with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
    results = list(pool.map(render, range(8)))
assert all(result.returncode == 0 for result in results), [
    (result.returncode, result.stderr) for result in results
]
assert destination.read_bytes() == reference.read_bytes(), "partial or changed installer"
assert destination.stat().st_mode & 0o777 == 0o755, "installer mode changed"
assert not list(scratch.glob(".olivares-render-installer.*")), "render temporaries leaked"
PY

expect_rc 1 "mutant: versioned installer refuses a different requested version" \
  env OLIVARES_OS=linux OLIVARES_ARCH=amd64 /bin/sh "$rendered" \
    --version v26.9.1 --dry-run
grep -Fq 'pinned to v26.9.0' "$scratch/err"

snapshot="$scratch/olivares-install-0.0.0-SNAPSHOT-none.sh"
expect_rc 0 "snapshot hook still produces the checksum input" \
  bash "$root/scripts/render-release-installer.sh" 0.0.0-SNAPSHOT-none "$snapshot" true
grep -Fq "EMBEDDED_VERSION='SNAPSHOT'" "$snapshot"
expect_rc 1 "snapshot installer is explicitly non-installable" \
  /bin/sh "$snapshot" --dry-run
grep -Fq 'snapshot installers are not installable' "$scratch/err"

expect_rc 0 "bootstrap dry-run is network- and mutation-free" \
  env CI=1 OLIVARES_GITHUB_URL=http://must-not-be-read.invalid \
    /bin/sh "$root/scripts/install-bootstrap.sh" --version v26.9.0 --dry-run
grep -Fq 'bootstrap trust: this response is trusted through HTTPS' "$scratch/out"
expect_rc 1 "mutant: non-interactive bootstrap without a pin is refused" \
  env CI=1 /bin/sh "$root/scripts/install-bootstrap.sh" --dry-run
grep -Fq 'must pin --version' "$scratch/err"
expect_rc 0 "without cosign on PATH the plan discloses the pinned temporary copy" \
  env CI=1 PATH=/usr/bin:/bin OLIVARES_OS=linux OLIVARES_ARCH=amd64 \
    /bin/sh "$root/scripts/install-bootstrap.sh" --version v26.9.0 --dry-run
grep -Fq 'cosign: not on PATH; a temporary copy of cosign v2.6.4' "$scratch/out"
grep -Fq 'pass --install-cosign to keep it' "$scratch/out"
expect_rc 0 "second stage without cosign on PATH discloses the same, and --install-cosign" \
  env PATH=/usr/bin:/bin OLIVARES_OS=linux OLIVARES_ARCH=amd64 \
    /bin/sh "$rendered" --bindir /opt/olivares/bin --install-cosign --dry-run
grep -Fq 'cosign: not on PATH; a temporary copy of cosign v2.6.4' "$scratch/out"
grep -Fq -- '--install-cosign: that verified copy is kept at /opt/olivares/bin/cosign' "$scratch/out"
expect_rc 1 "mutant: --install-cosign is not an uninstall option" \
  env PATH=/usr/bin:/bin /bin/sh "$rendered" --uninstall --plan --install-cosign

fixture="$scratch/fixture"
fakebin="$scratch/fakebin"
mkdir -p "$fixture" "$fakebin"
cat >"$fixture/olivares-install-26.9.0.sh" <<'STAGE'
#!/bin/sh
printf 'executed:%s\n' "$*" >"$EXEC_MARKER"
STAGE
chmod 0755 "$fixture/olivares-install-26.9.0.sh"
digest="$(sha256sum "$fixture/olivares-install-26.9.0.sh" | awk '{print $1}')"
printf '%s  %s\n' "$digest" olivares-install-26.9.0.sh >"$fixture/checksums.txt"
printf 'test-signature\n' >"$fixture/checksums.txt.sig"
printf 'test-certificate\n' >"$fixture/checksums.txt.pem"
cat >"$fakebin/curl" <<'FAKECURL'
#!/bin/sh
set -eu
url=""
dest=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) dest="$2"; shift 2 ;;
    http://*|https://*) url="$1"; shift ;;
    *) shift ;;
  esac
done
[ -n "$url" ] && [ -n "$dest" ]
cp "$FIXTURE/${url##*/}" "$dest"
FAKECURL
cat >"$fakebin/cosign" <<'FAKECOSIGN'
#!/bin/sh
exit "${COSIGN_RC:-0}"
FAKECOSIGN
chmod 0755 "$fakebin/curl" "$fakebin/cosign"

marker="$scratch/executed"
common_env=(
  CI=1
  EXEC_MARKER="$marker"
  FIXTURE="$fixture"
  OLIVARES_GITHUB_URL=https://fixture.invalid
  PATH="$fakebin:/usr/bin:/bin"
)
expect_rc 1 "mutant: a bad cosign result prevents second-stage execution" \
  env "${common_env[@]}" COSIGN_RC=1 /bin/sh "$root/scripts/install-bootstrap.sh" --version v26.9.0
[[ ! -e "$marker" ]] || { printf 'second stage ran after cosign failure\n' >&2; exit 1; }

cp "$fixture/olivares-install-26.9.0.sh" "$scratch/good-stage"
printf '# checksum mutant\n' >>"$fixture/olivares-install-26.9.0.sh"
expect_rc 1 "mutant: installer bytes outside signed checksum are refused" \
  env "${common_env[@]}" COSIGN_RC=0 /bin/sh "$root/scripts/install-bootstrap.sh" --version v26.9.0
grep -Fq 'checksum mismatch' "$scratch/err"
[[ ! -e "$marker" ]] || { printf 'second stage ran after checksum mismatch\n' >&2; exit 1; }
mv "$scratch/good-stage" "$fixture/olivares-install-26.9.0.sh"

expect_rc 0 "verified second stage executes only after both checks" \
  env "${common_env[@]}" COSIGN_RC=0 /bin/sh "$root/scripts/install-bootstrap.sh" \
    --version v26.9.0 --bindir /opt/olivares/bin
grep -Fq 'executed:--version v26.9.0 --bindir /opt/olivares/bin' "$marker"

rm -f "$marker"
# The same fake curl without a cosign on PATH: the scripts must then fetch the
# pinned cosign through it and accept it only on the pinned SHA-256.
nocosign="$scratch/fakebin-nocosign"
mkdir -p "$nocosign"
cp "$fakebin/curl" "$nocosign/curl"
cat >"$fixture/cosign-linux-amd64" <<'ROGUE'
#!/bin/sh
printf 'rogue cosign executed\n' >"$EXEC_MARKER.cosign"
exit 0
ROGUE
chmod 0755 "$fixture/cosign-linux-amd64"
nocosign_env=(
  CI=1
  EXEC_MARKER="$marker"
  FIXTURE="$fixture"
  OLIVARES_GITHUB_URL=https://fixture.invalid
  OLIVARES_COSIGN_RELEASE_URL=https://fixture.invalid/cosign
  OLIVARES_OS=linux
  OLIVARES_ARCH=amd64
  PATH="$nocosign:/usr/bin:/bin"
)
expect_rc 1 "mutant: without cosign, a fetched cosign off its pinned SHA-256 is refused unexecuted (bootstrap)" \
  env "${nocosign_env[@]}" /bin/sh "$root/scripts/install-bootstrap.sh" --version v26.9.0
grep -Fq 'does not match its pinned SHA-256' "$scratch/err"
[[ ! -e "$marker.cosign" ]] || { printf 'rogue cosign was executed by the bootstrap\n' >&2; exit 1; }
[[ ! -e "$marker" ]] || { printf 'second stage ran after a rogue cosign\n' >&2; exit 1; }
expect_rc 1 "mutant: without cosign, a fetched cosign off its pinned SHA-256 is refused unexecuted (second stage)" \
  env "${nocosign_env[@]}" /bin/sh "$rendered" --bindir "$scratch/never-installed"
grep -Fq 'does not match its pinned SHA-256' "$scratch/err"
[[ ! -e "$marker.cosign" ]] || { printf 'rogue cosign was executed by the second stage\n' >&2; exit 1; }
[[ ! -e "$scratch/never-installed" ]] || { printf 'second stage installed after a rogue cosign\n' >&2; exit 1; }
# With the real pinned cosign (opt-in: OLIVARES_TEST_COSIGN_BINARY, digest checked here first)
# the temporary copy becomes the verifier: it rejects the fixture's fake signature, so the
# second stage never runs, and the refusal is cosign's, not a digest mismatch.
real_cosign="${OLIVARES_TEST_COSIGN_BINARY:-}"
if [[ -n "$real_cosign" && -f "$real_cosign" ]] &&
  [[ "$(sha256sum "$real_cosign" | awk '{print $1}')" = 309779b0c4e409186b0a80daba99041fe2cf65a920ce645013901df6211895a9 ]]; then
  cp "$real_cosign" "$fixture/cosign-linux-amd64"
  expect_rc 1 "pinned temporary cosign is the verifier: a fake signature is refused by cosign itself" \
    env "${nocosign_env[@]}" /bin/sh "$root/scripts/install-bootstrap.sh" --version v26.9.0
  grep -Fq 'cosign is not on PATH: fetching the pinned cosign v2.6.4 for linux/amd64' "$scratch/out"
  if grep -Fq 'does not match its pinned SHA-256' "$scratch/err"; then
    printf 'the real pinned cosign was reported as a digest mismatch\n' >&2
    exit 1
  fi
  [[ ! -e "$marker" ]] || { printf 'second stage ran after cosign refused the signature\n' >&2; exit 1; }
else
  printf '# skip: OLIVARES_TEST_COSIGN_BINARY is not the pinned cosign v2.6.4 linux/amd64; the real-verifier case did not run\n'
fi
rm -f "$fixture/cosign-linux-amd64"

make_mutant() {
  local name="$1"
  local dest="$scratch/$name"
  mkdir -p "$dest/scripts" "$dest/deploy/distribution" "$dest/docs"
  cp "$root/.goreleaser.yaml" "$dest/.goreleaser.yaml"
  cp "$root/README.md" "$root/INSTALL.md" "$dest/"
  cp "$root/scripts/install.sh" "$root/scripts/install-bootstrap.sh" \
    "$root/scripts/render-release-installer.sh" "$root/scripts/assert-cosign-binary.sh" "$dest/scripts/"
  cp "$root/deploy/distribution/install-endpoints.json" "$dest/deploy/distribution/"
  cp "$root/docs/RELEASE-INSTALLER.md" "$dest/docs/"
  printf '%s\n' "$dest"
}

mutant="$(make_mutant marker-mutant)"
sed -i 's/@OLIVARES_INSTALLER_VERSION@/26.9.0/' "$mutant/scripts/install.sh"
expect_rc 1 "mutant: source without the release marker is red" \
  env OLIVARES_ROOT="$mutant" bash "$root/scripts/check-release-installer.sh"

mutant="$(make_mutant cosign-pin-mutant)"
sed -i 's/^    linux-arm64) printf .%s. df408e5418/    linux-arm64) printf '"'"'%s'"'"' 00408e5418/' "$mutant/scripts/install.sh"
grep -Fq '00408e5418' "$mutant/scripts/install.sh"
expect_rc 1 "mutant: an installer cosign digest that drifts from the approved table is red" \
  env OLIVARES_ROOT="$mutant" bash "$root/scripts/check-release-installer.sh"
grep -Fq 'linux-arm64 digest differs from the approved table' "$scratch/err"

mutant="$(make_mutant cosign-refusal-mutant)"
sed -i 's/^resolve_cosign$/have cosign || err "cosign is required"/' "$mutant/scripts/install.sh"
expect_rc 1 "mutant: an installer that refuses without cosign instead of fetching the pinned copy is red" \
  env OLIVARES_ROOT="$mutant" bash "$root/scripts/check-release-installer.sh"

mutant="$(make_mutant route-mutant)"
sed -i 's#"/get"#"/fetch"#' "$mutant/deploy/distribution/install-endpoints.json"
expect_rc 1 "mutant: endpoint alias drift is red" \
  env OLIVARES_ROOT="$mutant" bash "$root/scripts/check-release-installer.sh"

mutant="$(make_mutant checksum-wiring-mutant)"
python3 - "$mutant/.goreleaser.yaml" <<'PY'
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
text = path.read_text(encoding="utf-8")
needle = "    - glob: dist/olivares-install-*.sh\n"
assert text.count(needle) == 2
path.write_text(text.replace(needle, "", 1), encoding="utf-8")
PY
expect_rc 1 "mutant: upload-only installer absent from signed checksums is red" \
  env OLIVARES_ROOT="$mutant" bash "$root/scripts/check-release-installer.sh"

printf '1..%d\n' "$passes"
