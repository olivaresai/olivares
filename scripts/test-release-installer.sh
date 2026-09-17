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
