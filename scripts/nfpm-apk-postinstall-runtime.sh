#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Execute the APK-packaged post-install (extracted .post-install) inside a
# disposable, unprivileged, network-none container at the hook's real absolute
# paths. This is the runtime witness for OpenRC init selection. It is not apk
# add, not OpenRC pid 1, and not proof that a daemon started: rc-service and
# systemctl are recording stubs. Missing docker or the pinned image is exit 2
# (cannot examine), never a skip and never a green.
#
# IMAGE PIN — Alpine 3.22.5 as Docker Official Image library/alpine, digest from
# the publisher's registry tag API on 2026-09-07 (see IMAGE-PROVENANCE in the
# implementation assessment). Index digest is the run target so the engine
# selects the native platform; QEMU is not used.
set -euo pipefail

_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

LC_ALL=C
export LC_ALL
export GOMAXPROCS="${GOMAXPROCS:-2}"
export GOFLAGS="${GOFLAGS:--p=2}"

# docker.io/library/alpine:3.22.5 == alpine:3.22 (official-images library/alpine).
ALPINE_VERSION=3.22.5
ALPINE_INDEX_DIGEST='sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce'
ALPINE_AMD64_DIGEST='sha256:7c8cb692ae09657cbc4a3f3cbd0e8d5a2690ba38386aaaf252dbb060bf5eb2e6'
ALPINE_ARM64_DIGEST='sha256:2c9d26f410d032d5b1525aa8a873e238b05b90c4ae8618743d4311f0cc827e37'
ALPINE_IMAGE="docker.io/library/alpine@${ALPINE_INDEX_DIGEST}"
ALPINE_SOURCE_URL='https://hub.docker.com/v2/repositories/library/alpine/tags/3.22.5'
ALPINE_GITREPO='https://github.com/alpinelinux/docker-alpine.git'
ALPINE_GITCOMMIT='aff8a4cfde38012a59285f21c8820777acd6033b'
ALPINE_OFFICIAL_IMAGES='https://github.com/docker-library/official-images/blob/master/library/alpine'
ALPINE_RELEASES='https://alpinelinux.org/releases/'
CONTAINER_PREFIX='olivares-apk-pi'

me=nfpm-apk-postinstall-runtime

could_not_look() {
	printf '%s: NO HE PODIDO MIRAR — %s\n' "$me" "$*" >&2
	exit 2
}
fail() {
	printf 'not ok - %s\n' "$*" >&2
	exit 1
}

root="${OLIVARES_ROOT:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)}"

# --- static selftests (no package hook, no container) --------------------------------
# insert_assignment_mutant SRC DST: copy SRC to DST and insert the r16 assignment
# mutant on its own line immediately after the unique standalone `pkg_init` call
# (exact stripped line, not the `pkg_init() {` definition).
insert_assignment_mutant() {
	python3 - "$1" "$2" <<'PY' || return 1
from pathlib import Path
import sys
src, dst = Path(sys.argv[1]), Path(sys.argv[2])
text = src.read_text(encoding="utf-8")
lines = text.splitlines(keepends=True)
idxs = [i for i, line in enumerate(lines) if line.strip() == "pkg_init"]
if len(idxs) != 1:
    print(f"want exactly one standalone pkg_init call, found {len(idxs)}", file=sys.stderr)
    raise SystemExit(1)
i = idxs[0]
lines.insert(i + 1, "OLIVARES_PKG_INIT=systemd\n")
dst.write_text("".join(lines), encoding="utf-8")
PY
}

# stage_n1_mutant CASE_DIR HOOK: create the N1 case directory, then write its assignment
# mutant into it. The directory is created HERE, by the step that needs it, because
# insert_assignment_mutant writes its destination directly and Path.write_text does not
# create parents. run_n1 used to insert the mutant before prepare_witness -- the only other
# step that creates a descendant of the case directory -- so on a fresh scratch the write
# raised FileNotFoundError for cases/n1fresh/mutant.post-install and BOTH N1 mutants died in
# setup, after P1/P2 had already paid for a real APK build (mainline-ci run 34286700558, job
# 102263870572). P1/P2 never showed it: they call prepare_witness first. selftest_static
# stages into a path whose parents do not exist yet, so the same setup defect is caught
# without docker, without an image and without a package.
stage_n1_mutant() {
	local case_dir=$1 hook=$2
	mkdir -p "$case_dir" || return 1
	insert_assignment_mutant "$hook" "$case_dir/mutant.post-install"
}

emit_inner() {
	cat >"$1" <<'INNER'
#!/bin/sh
# Recording harness. Lives beside the SUT; does not rewrite pkg_init or paths.
set -eu

# export_evidence_copy SRC DST — publish one product file into the bind-mounted /out,
# where an unprivileged host judge reads it. This harness is root inside the container
# and the packaged hook leaves the manifest 0640 root:olivares, so a mode-preserving
# copy lands root-owned 0640 on the host and the judge gets EACCES (mainline run
# 34354335844, job 102475134713: "manifest json: [Errno 13] Permission denied"). Only
# the exported copy is widened, and only to 0644: SRC is never chmod-ed, chown-ed or
# rewritten, so the product manifest keeps exactly the access the hook gave it, and the
# copy is readable but not writable by anyone but its owner. The copy is staged under a
# .part name and renamed only once it is readable, and a failed export removes both
# names, so a partial or stale copy is never left behind as this run's evidence.
export_evidence_copy() {
  ec_src=$1
  ec_dst=$2
  ec_part="$ec_dst.part"
  rm -f "$ec_part"
  if cp "$ec_src" "$ec_part" && chmod 0644 "$ec_part" && mv "$ec_part" "$ec_dst"; then
    return 0
  fi
  rm -f "$ec_part" "$ec_dst"
  return 1
}

# Seam for the static selftests: run the export operation on its own, with no package,
# no container and no privilege. The runtime invocation passes no arguments.
if [ "${1:-}" = --export-evidence-copy ]; then
  if export_evidence_copy "$2" "$3"; then
    exit 0
  fi
  exit 1
fi

mkdir -p /usr/lib/olivares /etc/init.d /usr/share/olivares /var/lib/olivares /var/log /run /opt/fakebin /out

cp /witness/package-init /usr/lib/olivares/package-init
cp /witness/olivares.init /etc/init.d/olivares
chmod 0755 /etc/init.d/olivares
if [ -f /witness/olivares.env.example ]; then
  cp /witness/olivares.env.example /usr/share/olivares/olivares.env.example
fi
if [ -f /witness/plant-upgrade ]; then
  : >/run/olivares.pkg-upgrade-was-active
fi

# Independent image probe — not inferred from hook rc.
if stat -c '%u' / >/out/probe.stat-c 2>/out/probe.stat-c.err; then
  echo 0 >/out/probe.stat-c.rc
else
  echo 1 >/out/probe.stat-c.rc
  echo 'stat -c unavailable' >>/out/probe.fail
fi
if command -v adduser >/dev/null 2>&1; then
  command -v adduser >/out/probe.adduser
else
  echo 'adduser missing' >>/out/probe.fail
fi
if command -v addgroup >/dev/null 2>&1; then
  command -v addgroup >/out/probe.addgroup
else
  echo 'addgroup missing' >>/out/probe.fail
fi
command -v id >/out/probe.id 2>/dev/null || echo 'id missing' >>/out/probe.fail
command -v getent >/out/probe.getent 2>/dev/null || true
if [ ! -f /out/probe.fail ]; then
  echo ok >/out/probe.ok
fi
if [ -e /usr/lib/systemd/system/olivares.service ]; then
  echo present >/out/probe.systemd-unit
else
  echo absent >/out/probe.systemd-unit
fi

cat >/opt/fakebin/systemctl <<'STUB'
#!/bin/sh
printf '%s\n' "$*" >>/out/systemctl.log
case "$1" in
  daemon-reload) exit 0 ;;
  *) exit 1 ;;
esac
STUB
cat >/opt/fakebin/rc-service <<'STUB'
#!/bin/sh
printf '%s\n' "$*" >>/out/rc-service.log
if [ "$1" = olivares ] && [ "$2" = start ]; then
  exit 0
fi
if [ "$1" = olivares ] && [ "$2" = status ]; then
  if [ -f /out/rc-service.active-marker ]; then
    exit 0
  fi
  exit 1
fi
exit 1
STUB
cat >/opt/fakebin/rc-update <<'STUB'
#!/bin/sh
printf '%s\n' "$*" >>/out/rc-update.log
exit 0
STUB
chmod 0755 /opt/fakebin/systemctl /opt/fakebin/rc-service /opt/fakebin/rc-update
PATH="/opt/fakebin:${PATH}"
export PATH
unset OLIVARES_PKG_INIT || true

set +e
/bin/sh /witness/post-install >/out/hook.stdout 2>/out/hook.stderr
echo $? >/out/hook.rc
set -e

echo unmeasured >/out/daemon_started
if [ -f /var/lib/olivares/install-manifest.json ]; then
  # The recorded source permissions are read from the product path itself, before and
  # independently of the export: what the hook set is the measurement, never what the
  # evidence copy carries.
  stat -c '%a' /var/lib/olivares/install-manifest.json >/out/manifest.mode 2>/dev/null || true
  if export_evidence_copy /var/lib/olivares/install-manifest.json /out/install-manifest.json; then
    echo ok >/out/manifest.export
  else
    echo failed >/out/manifest.export
  fi
fi
if [ -f /etc/init.d/olivares ]; then
  echo /etc/init.d/olivares >/out/unit.path-on-disk
  stat -c '%a' /etc/init.d/olivares >/out/unit.mode-on-disk 2>/dev/null || true
fi
if [ -f /run/olivares.pkg-upgrade-was-active ]; then
  echo present >/out/upgrade-stamp.state
else
  echo removed-or-absent >/out/upgrade-stamp.state
fi
if id -u olivares >/out/olivares.uid 2>/dev/null; then
  :
else
  echo missing >/out/olivares.uid
fi
exit 0
INNER
}

# The manifest the host judge reads is an exported COPY: the harness is root inside the
# container, the host judge is not, and the packaged hook leaves the product manifest
# 0640 root:olivares. These controls run the real export operation out of the emitted
# inner harness -- no docker, no package, no privilege -- instead of asserting that some
# chmod literal appears in the source. The failure they exist for is the P1 judge read
# that returned EACCES in mainline run 34354335844, job 102475134713.
selftest_export_controls() {
	local base=$1 inner=$2 src dst exported_mode mode_before mode_after
	mkdir -p "$base/export/product" "$base/export/out"
	src="$base/export/product/install-manifest.json"
	dst="$base/export/out/install-manifest.json"
	write_valid_fresh_receipt "$base/export/receipt"
	cp "$base/export/receipt/install-manifest.json" "$src"

	# Stricter than the product's own 0640 on purpose: a mode-preserving copy of this
	# is unreadable to every reader that is not its owner, which is what the exported
	# copy is for the host judge.
	chmod 0600 "$src"
	mode_before="$(stat -c '%a' "$src")"
	[[ "$mode_before" == 600 ]] ||
		fail "export control could not set the restrictive source mode (got $mode_before)"

	sh "$inner" --export-evidence-copy "$src" "$dst" 2>"$base/export/ok.err" ||
		fail "export operation refused a readable product manifest: $(tr '\n' ' ' <"$base/export/ok.err")"
	[[ -f "$dst" ]] || fail "export operation returned success without writing the evidence copy"
	cmp -s "$src" "$dst" || fail "exported evidence copy is not byte-identical to the product manifest"
	exported_mode="$(stat -c '%a' "$dst")"
	# 0644: the host judge is neither the owner (container root) nor in its group, so it
	# reads the copy through the other class. Not 0666, and not a widened directory: the
	# export publishes readable evidence, it does not hand out write access.
	[[ "$exported_mode" == 644 ]] ||
		fail "exported evidence copy mode is $exported_mode, want 644 (the host judge reads it as other)"
	if ((8#$exported_mode & 8#0022)); then
		fail "exported evidence copy is group- or world-writable (mode $exported_mode)"
	fi
	mode_after="$(stat -c '%a' "$src")"
	[[ "$mode_after" == "$mode_before" ]] ||
		fail "export changed the product manifest mode from $mode_before to $mode_after"
	cmp -s "$base/export/receipt/install-manifest.json" "$src" ||
		fail "export rewrote the product manifest"
	[[ ! -e "$dst.part" ]] || fail "export left its staging copy behind"
	printf '%s\n' 'ok - export: 0600 product manifest -> 0644 evidence copy, exact bytes, source mode 0600 unchanged'

	# A failed export is not evidence: it must report failure and must not leave an
	# earlier copy in place for the judge to read as this run's measurement.
	printf '%s\n' 'stale evidence from an earlier attempt' >"$dst"
	if sh "$inner" --export-evidence-copy "$base/export/product/absent.json" "$dst" \
		2>"$base/export/failed.err"; then
		fail "export reported success for a product file it could not read"
	fi
	[[ ! -e "$dst" ]] ||
		fail "failed export left a stale evidence copy behind: $(tr '\n' ' ' <"$dst")"
	[[ ! -e "$dst.part" ]] || fail "failed export left its staging copy behind"
	[[ -s "$base/export/failed.err" ]] || fail "failed export produced no diagnostic"
	printf '%s\n' 'ok - export: a failed export reports failure and leaves no evidence copy or staging residue'
}

selftest_static() {
	local tmp call_line def_line host
	host="$root/scripts/nfpm-apk-postinstall-runtime.sh"
	tmp="$(mktemp -d "${TMPDIR:-/tmp}/nfpm-apk-pi-static.XXXXXX")"
	# shellcheck disable=SC2064
	trap 'case "$tmp" in "${TMPDIR:-/tmp}"/nfpm-apk-pi-static.*) rm -rf -- "$tmp" ;; esac' RETURN

	[[ -f "$root/packaging/nfpm/postinstall.sh" ]] ||
		could_not_look "missing packaging/nfpm/postinstall.sh"
	[[ -f "$host" ]] || could_not_look "missing $host"

	# Pin shape.
	[[ "$ALPINE_INDEX_DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]] ||
		fail "Alpine index digest is not sha256:hex64"
	[[ "$ALPINE_AMD64_DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]] ||
		fail "Alpine amd64 digest is not sha256:hex64"
	[[ "$ALPINE_ARM64_DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]] ||
		fail "Alpine arm64 digest is not sha256:hex64"
	[[ "$ALPINE_VERSION" == 3.22.5 ]] || fail "Alpine version pin drifted: $ALPINE_VERSION"
	[[ "$ALPINE_IMAGE" == "docker.io/library/alpine@${ALPINE_INDEX_DIGEST}" ]] ||
		fail "Alpine image ref is not the digest-pinned official library/alpine index"
	[[ "$ALPINE_IMAGE" != *latest* ]] || fail "image ref must not use :latest"

	# Host script must keep the isolation flags and must not grow forbidden ones.
	grep -Fq -- '--network none' "$host" || fail "host script lost --network none"
	# Only the docker-run invocation lines, not comments or this selftest's own strings.
	if grep -E '^[[:space:]]*docker run' "$host" | grep -E -- '--privileged' >/dev/null; then
		fail "docker run invocation contains --privileged"
	fi
	if grep -E '^[[:space:]]*docker run' "$host" | grep -F 'docker.sock' >/dev/null; then
		fail "docker run invocation names docker.sock"
	fi
	if grep -E '^[[:space:]]*sudo ' "$host"; then
		fail "host script invokes sudo"
	fi

	# Inner harness parses as POSIX sh.
	emit_inner "$tmp/inner.sh"
	sh -n "$tmp/inner.sh" || fail "inner.sh does not parse as POSIX sh"

	# ... and its export operation publishes a manifest the unprivileged host can read.
	selftest_export_controls "$tmp" "$tmp/inner.sh"

	# Standalone call vs function definition.
	# A grep failure must reach its diagnostic under set -e; -m avoids a head/SIGPIPE pipeline.
	def_line="$(grep -n -m 1 -F 'pkg_init() {' "$root/packaging/nfpm/postinstall.sh")" || fail "postinstall.sh lost pkg_init() {"
	call_line="$(grep -n -x 'pkg_init' "$root/packaging/nfpm/postinstall.sh")" || fail "postinstall.sh lost the standalone pkg_init call"
	[[ -n "$def_line" ]] || fail "postinstall.sh lost pkg_init() {"
	[[ -n "$call_line" ]] || fail "postinstall.sh lost the standalone pkg_init call"
	[[ "$(grep -cx 'pkg_init' "$root/packaging/nfpm/postinstall.sh")" -eq 1 ]] ||
		fail "postinstall.sh must have exactly one standalone pkg_init call line"
	python3 - "$root/packaging/nfpm/postinstall.sh" <<'PY' || fail "pkg_init call is not after the function definition"
from pathlib import Path
import sys
lines = Path(sys.argv[1]).read_text(encoding="utf-8").splitlines()
def_i = next(i for i, l in enumerate(lines) if l.strip().startswith("pkg_init()"))
call_i = next(i for i, l in enumerate(lines) if l.strip() == "pkg_init")
if call_i <= def_i:
    raise SystemExit(1)
PY

	# The first-substring inserter (the inspect_apk_hooks class of mistake) hits the
	# function name. The exact-line inserter must not.
	python3 - "$root/packaging/nfpm/postinstall.sh" "$tmp/wrong.txt" <<'PY' || fail "control: first-substring insert did not hit the definition"
from pathlib import Path
import sys
text = Path(sys.argv[1]).read_text(encoding="utf-8")
idx = text.find("pkg_init")
if idx < 0:
    raise SystemExit(1)
mut = text[: idx + len("pkg_init")] + "\nOLIVARES_PKG_INIT=systemd\n" + text[idx + len("pkg_init") :]
Path(sys.argv[2]).write_text(mut, encoding="utf-8")
if "pkg_init() {" in Path(sys.argv[1]).read_text(encoding="utf-8") and "pkg_init\nOLIVARES_PKG_INIT=systemd\n() {" in mut:
    raise SystemExit(0)
# busybox/function form with space: pkg_init() {
if mut.split("pkg_init", 1)[0].count("\n") < Path(sys.argv[1]).read_text(encoding="utf-8").split("pkg_init() {", 1)[0].count("\n"):
    raise SystemExit(0)
raise SystemExit(0)
PY

	insert_assignment_mutant "$root/packaging/nfpm/postinstall.sh" "$tmp/mutant.sh" ||
		fail "assignment-mutant inserter failed on tree postinstall.sh"
	python3 - "$root/packaging/nfpm/postinstall.sh" "$tmp/mutant.sh" <<'PY' || fail "assignment mutant did not land after the call"
from pathlib import Path
import sys
orig = Path(sys.argv[1]).read_text(encoding="utf-8").splitlines()
mut = Path(sys.argv[2]).read_text(encoding="utf-8").splitlines()
call_i = next(i for i, l in enumerate(orig) if l.strip() == "pkg_init")
if mut[call_i].strip() != "pkg_init":
    raise SystemExit("call line moved")
if mut[call_i + 1] != "OLIVARES_PKG_INIT=systemd":
    raise SystemExit("assignment is not the next line")
if orig[next(i for i, l in enumerate(orig) if l.strip().startswith("pkg_init()"))] != \
   mut[next(i for i, l in enumerate(mut) if l.strip().startswith("pkg_init()"))]:
    raise SystemExit("function definition changed")
if "pkg_init() {" not in "\n".join(mut):
    raise SystemExit("function definition lost")
PY
	if grep -Fxq 'OLIVARES_PKG_INIT=systemd' "$root/packaging/nfpm/postinstall.sh"; then
		fail "tree postinstall.sh already contains the assignment mutant line"
	fi

	# N1 setup, on the same footing the runtime path uses it: a case directory that does not
	# exist yet, three levels below a fresh scratch. This is what turns red when the staging
	# step stops creating its own directory -- the way both N1 mutants were lost in setup,
	# after the package had already been built. The staged bytes must also be the inserter's
	# exact mutant, so a "fix" that writes something else is not green either.
	[[ ! -e "$tmp/n1-staging" ]] || fail "N1 staging control is not starting from a fresh path"
	stage_n1_mutant "$tmp/n1-staging/cases/n1fresh" "$root/packaging/nfpm/postinstall.sh" ||
		fail "N1 staging did not create its case directory before writing mutant.post-install"
	[[ -f "$tmp/n1-staging/cases/n1fresh/mutant.post-install" ]] ||
		fail "N1 staging reported success without writing mutant.post-install"
	cmp -s "$tmp/mutant.sh" "$tmp/n1-staging/cases/n1fresh/mutant.post-install" ||
		fail "N1 staging wrote different bytes than the assignment-mutant inserter"

	selftest_judge_controls "$tmp"
	printf '%s\n' 'ok - static selftests (pin, isolation flags, exact-line mutant inserter, N1 case staging, inner.sh syntax, evidence export, judge controls)'
	trap - RETURN
}

# --- shared packaging helpers (copied in semantics from test-nfpm-openrc.sh) ------
resolve_tool() {
	local name=$1 var=$2 override=${!2:-} found
	if [[ -n "$override" ]]; then
		case "$override" in
		/*) ;;
		*) could_not_look "$var must be an absolute path, got: $override" ;;
		esac
		[[ -f "$override" ]] || could_not_look "$var is not a regular file: $override"
		[[ -x "$override" ]] || could_not_look "$var is not executable: $override"
		printf '%s\n' "$override"
		return 0
	fi
	found="$(command -v -- "$name" 2>/dev/null)" ||
		could_not_look "$name is not on PATH and $var is unset"
	printf '%s\n' "$found"
}

extract_nfpms() {
	python3 - "$1" "$2" <<'PY' || fail "could not extract the nfpms block from $1"
from pathlib import Path
import sys
src = Path(sys.argv[1]).read_text(encoding="utf-8")
start = src.find("\nnfpms:\n")
end = src.find("\nhomebrew_casks:\n")
if start < 0 or end < 0 or end <= start:
    raise SystemExit("nfpms block not found")
Path(sys.argv[2]).write_text(src[start + 1 : end], encoding="utf-8")
PY
}

make_pkgproj() {
	local proj=$1 nfpms=$2 f
	mkdir -p "$proj/cmd/olivares" "$proj/packaging"
	cp -a "$root/packaging/." "$proj/packaging/"
	for f in LICENSE NOTICE LICENSING.md DISCLAIMER.md; do
		cp "$root/$f" "$proj/"
	done
	cp -a "$root/LICENSES" "$proj/LICENSES"
	cat >"$proj/cmd/olivares/main.go" <<'GO'
package main

func main() {}
GO
	cat >"$proj/go.mod" <<'GOMOD'
module github.com/olivaresai/olivares

go 1.26
GOMOD
	{
		printf '%s\n' 'version: 2' 'project_name: olivares' 'builds:' \
			'  - id: olivares' '    main: ./cmd/olivares' '    binary: olivares' \
			'    goos: [linux]' '    goarch: [amd64]' '    env: [CGO_ENABLED=0]'
		cat "$nfpms"
	} >"$proj/.goreleaser.yaml"
	(
		cd "$proj"
		git init -q
		git config user.name "fixture"
		git config user.email "fixture@example.invalid"
		git add -- .goreleaser.yaml go.mod cmd packaging LICENSE NOTICE LICENSING.md DISCLAIMER.md LICENSES
		git -c core.hooksPath= -c commit.gpgsign=false commit -q --no-verify -s \
			-m "test: fixture packaging project"
		git remote add origin https://example.invalid/olivares/olivares.git
	)
	(
		cd "$proj"
		"$goreleaser_bin" check --config .goreleaser.yaml >/dev/null
	) || fail "goreleaser check rejected the $(basename "$proj") config"
}

build_packages() {
	local proj=$1 label=$2 grc
	set +e
	(
		cd "$proj"
		"$goreleaser_bin" release --snapshot --clean --config "$proj/.goreleaser.yaml" \
			--skip=before,publish,sign,sbom,docker,validate,archive,homebrew,ko,nix,scoop,snapcraft,winget,aur,announce,notarize,chocolatey,flatpak,makeself,mcp,srpm
	) >"$scratch/$label.goreleaser.out" 2>"$scratch/$label.goreleaser.err"
	grc=$?
	set -e
	printf '%s\n' "$grc" >"$scratch/$label.goreleaser.rc"
	if [[ "$grc" -ne 0 ]]; then
		sed -n '1,160p' "$scratch/$label.goreleaser.err" >&2
		fail "goreleaser snapshot package build failed for $label (rc=$grc)"
	fi
}

first_artifact() {
	find "$1/dist" -name "$2" -print | sed -n '1p'
}

apk_scripts() {
	python3 - "$1" "$2" <<'PY' || fail "apk control script extraction failed for $1"
import sys, tarfile
from pathlib import Path
apk, dest = sys.argv[1], Path(sys.argv[2])
dest.mkdir(parents=True, exist_ok=True)
names = {
    ".pre-install", ".post-install", ".pre-upgrade", ".post-upgrade",
    ".pre-deinstall", ".post-deinstall",
}
found = 0
with tarfile.open(apk, "r:gz") as tf:
    for member in tf.getmembers():
        base = member.name.split("/")[-1]
        if base in names:
            f = tf.extractfile(member)
            if f:
                (dest / base).write_bytes(f.read())
                found += 1
if not found:
    raise SystemExit("apk control scripts were not found")
PY
}

# Compare extracted .post-install to the tree hook. Byte equality is the
# expected case. An unexplained wrap is a defect; a named trailing-newline-only
# difference is recorded and accepted. pkg_init missing or rewritten is exit 1.
assert_extracted_hook() {
	python3 - "$root/packaging/nfpm/postinstall.sh" "$1" "$2" <<'PY' || return 1
from pathlib import Path
import hashlib, sys
src = Path(sys.argv[1]).read_bytes()
ext = Path(sys.argv[2]).read_bytes()
log = Path(sys.argv[3])
sh = hashlib.sha256(src).hexdigest()
eh = hashlib.sha256(ext).hexdigest()
lines = [
    f"tree_postinstall_sha256={sh}",
    f"extracted_post_install_sha256={eh}",
    f"tree_bytes={len(src)}",
    f"extracted_bytes={len(ext)}",
]
if src == ext:
    lines.append("relation=byte-identical")
    log.write_text("\n".join(lines) + "\n", encoding="utf-8")
    raise SystemExit(0)
# Trailing newline only.
if src + b"\n" == ext:
    lines.append("relation=extracted-has-extra-trailing-newline")
    log.write_text("\n".join(lines) + "\n", encoding="utf-8")
    raise SystemExit(0)
if ext + b"\n" == src:
    lines.append("relation=extracted-missing-trailing-newline")
    log.write_text("\n".join(lines) + "\n", encoding="utf-8")
    raise SystemExit(0)
text = ext.decode("utf-8", errors="replace")
if "pkg_init() {" not in text:
    lines.append("relation=pkg_init-function-missing")
    log.write_text("\n".join(lines) + "\n", encoding="utf-8")
    print("extracted .post-install lost the pkg_init function", file=sys.stderr)
    raise SystemExit(1)
if sum(1 for line in text.splitlines() if line.strip() == "pkg_init") != 1:
    lines.append("relation=pkg_init-call-missing-or-duplicated")
    log.write_text("\n".join(lines) + "\n", encoding="utf-8")
    print("extracted .post-install has no unique standalone pkg_init call", file=sys.stderr)
    raise SystemExit(1)
lines.append("relation=unexplained-difference")
log.write_text("\n".join(lines) + "\n", encoding="utf-8")
print("extracted .post-install differs from packaging/nfpm/postinstall.sh unexplained", file=sys.stderr)
print(f"  tree sha256={sh}", file=sys.stderr)
print(f"  extracted sha256={eh}", file=sys.stderr)
raise SystemExit(1)
PY
}

judge_case() {
	# stdout: PASS|FAIL|BLIND  stderr: named reasons. rc 0/1/2.
	python3 - "$1" "$2" <<'PY'
from pathlib import Path
import json, sys

out = Path(sys.argv[1])
mode = sys.argv[2]  # fresh_openrc | upgrade_openrc

def read(name, default=""):
    p = out / name
    if not p.is_file():
        return default
    return p.read_text(encoding="utf-8")

def nonempty(name):
    p = out / name
    return p.is_file() and p.stat().st_size > 0

reasons = []
blind = []

if not (out / "probe.ok").is_file():
    fail_txt = read("probe.fail").strip() or "image probe did not pass"
    blind.append(fail_txt)
if read("hook.rc").strip() == "":
    blind.append("hook.rc missing")

if blind:
    for b in blind:
        print(b, file=sys.stderr)
    print("BLIND")
    raise SystemExit(2)

hook_rc = read("hook.rc").strip()
init = ""
unit_path = ""
unit_mode = ""
manifest_ok = False
export_state = read("manifest.export").strip()
if (out / "install-manifest.json").is_file():
    # A copy is evidence only if the harness recorded that it published it: an
    # unrecorded or failed export must not be read as a successful measurement.
    if export_state != "ok":
        reasons.append(f"manifest export state={export_state or 'unrecorded'} want ok")
    try:
        manifest = json.loads((out / "install-manifest.json").read_text(encoding="utf-8"))
        init = str(manifest.get("init", ""))
        files = manifest.get("files") or []
        units = [f for f in files if f.get("role") == "unit"]
        if len(units) == 1:
            unit_path = str(units[0].get("path", ""))
            unit_mode = str(units[0].get("mode", ""))
            manifest_ok = True
        else:
            reasons.append(f"manifest unit role count={len(units)} want 1")
    except Exception as exc:  # noqa: BLE001 — record parse failure as a named defect
        reasons.append(f"manifest json: {exc}")
else:
    if export_state and export_state != "ok":
        reasons.append(f"manifest export state={export_state} want ok")
    reasons.append("manifest missing")

if hook_rc != "0":
    reasons.append(f"hook rc={hook_rc} want 0")

if init == "systemd":
    reasons.append("init=systemd")
elif init != "openrc":
    reasons.append(f"init={init or 'empty'} want openrc")

if unit_path == "/usr/lib/systemd/system/olivares.service":
    reasons.append("unit_path systemd")
elif unit_path != "/etc/init.d/olivares":
    reasons.append(f"unit_path={unit_path or 'empty'} want /etc/init.d/olivares")

if unit_mode != "0755":
    reasons.append(f"unit mode={unit_mode or 'empty'} want 0755")

on_disk_path = read("unit.path-on-disk").strip()
if on_disk_path != "/etc/init.d/olivares":
    reasons.append(f"on-disk unit path={on_disk_path or 'empty'} want /etc/init.d/olivares")

on_disk_mode = read("unit.mode-on-disk").strip()
if on_disk_mode not in ("755", "0755"):
    reasons.append(f"on-disk unit mode={on_disk_mode or 'empty'} want 0755")

if nonempty("systemctl.log"):
    reasons.append("systemctl invoked")
    log = read("systemctl.log")
    if "daemon-reload" in log:
        reasons.append("systemctl daemon-reload")

rc_log = read("rc-service.log")
has_start = any(line.strip() == "olivares start" for line in rc_log.splitlines())
if mode == "fresh_openrc" and has_start:
    reasons.append("rc-service olivares start logged")
if mode == "upgrade_openrc" and not has_start:
    reasons.append("rc-service olivares start missing")

if nonempty("rc-update.log"):
    reasons.append("rc-update invoked")

stamp = read("upgrade-stamp.state").strip()
if mode == "upgrade_openrc" and stamp != "removed-or-absent":
    reasons.append(f"upgrade stamp state={stamp} want removed")
if mode == "fresh_openrc" and stamp == "present":
    reasons.append("upgrade stamp present on fresh install")

# Recording stubs never prove a daemon started.
daemon = read("daemon_started").strip()
if daemon not in ("", "unmeasured"):
    reasons.append(f"daemon_started={daemon} (stubs must not claim start)")

if reasons:
    for r in reasons:
        print(r, file=sys.stderr)
    print("FAIL")
    raise SystemExit(1)

if not manifest_ok:
    print("manifest unit role unreadable", file=sys.stderr)
    print("FAIL")
    raise SystemExit(1)

print("PASS")
raise SystemExit(0)
PY
}

write_valid_fresh_receipt() {
	local dest=$1
	mkdir -p "$dest"
	printf '%s\n' ok >"$dest/probe.ok"
	printf '%s\n' 0 >"$dest/hook.rc"
	printf '%s\n' ok >"$dest/manifest.export"
	printf '%s\n' unmeasured >"$dest/daemon_started"
	printf '%s\n' removed-or-absent >"$dest/upgrade-stamp.state"
	printf '%s\n' /etc/init.d/olivares >"$dest/unit.path-on-disk"
	printf '%s\n' 755 >"$dest/unit.mode-on-disk"
	cat >"$dest/install-manifest.json" <<'JSON'
{
  "schema": "olivares.ai/local-install/v2",
  "mode": "system",
  "init": "openrc",
  "data_dir": "/var/lib/olivares",
  "config": "/etc/olivares/olivares.env",
  "files": [
    {"path": "/usr/bin/olivares", "role": "binary", "mode": "0755", "managed": false},
    {"path": "/etc/olivares/olivares.env", "role": "config", "mode": "0640", "managed": false},
    {"path": "/etc/init.d/olivares", "role": "unit", "mode": "0755", "managed": false}
  ],
  "account": {"user": "olivares", "group": "olivares", "user_created": true, "group_created": true},
  "manifest": "/var/lib/olivares/install-manifest.json"
}
JSON
}

# Finite synthetic receipts fed to the same judge_case used at runtime.
selftest_judge_controls() {
	local base=$1 valid c rc
	valid="$base/judge-controls/valid"
	write_valid_fresh_receipt "$valid"

	expect_judge() {
		local dir=$1 want_rc=$2 want_token=${3:-}
		set +e
		judge_case "$dir" fresh_openrc >"$dir/judge.out" 2>"$dir/judge.err"
		rc=$?
		set -e
		printf '%s\n' "$rc" >"$dir/judge.rc"
		if [[ "$rc" -ne "$want_rc" ]]; then
			fail "judge $(basename "$dir") rc=$rc want $want_rc: $(tr '\n' ' ' <"$dir/judge.err")"
		fi
		if [[ -n "$want_token" ]] && ! grep -Fq "$want_token" "$dir/judge.err"; then
			fail "judge $(basename "$dir") missing named cause '$want_token': $(tr '\n' ' ' <"$dir/judge.err")"
		fi
		printf 'ok - judge %s rc=%s%s\n' "$(basename "$dir")" "$rc" \
			"${want_token:+ cause=$want_token}"
	}

	expect_judge "$valid" 0

	c="$base/judge-controls/manifest-mode-absent"
	cp -a "$valid" "$c"
	python3 - "$c/install-manifest.json" <<'PY' || fail "could not omit manifest unit mode"
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
m = json.loads(p.read_text(encoding="utf-8"))
for f in m["files"]:
    if f.get("role") == "unit":
        f.pop("mode", None)
p.write_text(json.dumps(m, indent=2) + "\n", encoding="utf-8")
PY
	expect_judge "$c" 1 'unit mode=empty want 0755'

	c="$base/judge-controls/manifest-mode-empty"
	cp -a "$valid" "$c"
	python3 - "$c/install-manifest.json" <<'PY' || fail "could not empty manifest unit mode"
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
m = json.loads(p.read_text(encoding="utf-8"))
for f in m["files"]:
    if f.get("role") == "unit":
        f["mode"] = ""
p.write_text(json.dumps(m, indent=2) + "\n", encoding="utf-8")
PY
	expect_judge "$c" 1 'unit mode=empty want 0755'

	c="$base/judge-controls/export-state-failed"
	cp -a "$valid" "$c"
	printf '%s\n' failed >"$c/manifest.export"
	expect_judge "$c" 1 'manifest export state=failed want ok'

	c="$base/judge-controls/export-state-unrecorded"
	cp -a "$valid" "$c"
	rm -f "$c/manifest.export"
	expect_judge "$c" 1 'manifest export state=unrecorded want ok'

	c="$base/judge-controls/disk-path-absent"
	cp -a "$valid" "$c"
	rm -f "$c/unit.path-on-disk"
	expect_judge "$c" 1 'on-disk unit path=empty want /etc/init.d/olivares'

	c="$base/judge-controls/disk-path-empty"
	cp -a "$valid" "$c"
	: >"$c/unit.path-on-disk"
	expect_judge "$c" 1 'on-disk unit path=empty want /etc/init.d/olivares'

	c="$base/judge-controls/disk-path-altered"
	cp -a "$valid" "$c"
	printf '%s\n' /usr/lib/systemd/system/olivares.service >"$c/unit.path-on-disk"
	expect_judge "$c" 1 'on-disk unit path=/usr/lib/systemd/system/olivares.service want /etc/init.d/olivares'

	c="$base/judge-controls/disk-mode-absent"
	cp -a "$valid" "$c"
	rm -f "$c/unit.mode-on-disk"
	expect_judge "$c" 1 'on-disk unit mode=empty want 0755'

	c="$base/judge-controls/disk-mode-empty"
	cp -a "$valid" "$c"
	: >"$c/unit.mode-on-disk"
	expect_judge "$c" 1 'on-disk unit mode=empty want 0755'

	c="$base/judge-controls/disk-mode-altered"
	cp -a "$valid" "$c"
	printf '%s\n' 644 >"$c/unit.mode-on-disk"
	expect_judge "$c" 1 'on-disk unit mode=644 want 0755'
}

run_container() {
	local case_id=$1 witness=$2 out=$3 cname drc
	cname="${CONTAINER_PREFIX}-${case_id}-$$"
	container_names+=("$cname")
	mkdir -p "$out"
	# Only our named container. Leftovers from a killed prior run of THIS name.
	docker rm -f "$cname" >/dev/null 2>&1 || true
	set +e
	docker run --name "$cname" --rm --network none \
		--volume "$witness:/witness:ro" \
		--volume "$out:/out" \
		"$ALPINE_IMAGE" \
		/bin/sh /witness/inner.sh >"$out/docker.stdout" 2>"$out/docker.stderr"
	drc=$?
	set -e
	printf '%s\n' "$drc" >"$out/docker.rc"
	docker rm -f "$cname" >/dev/null 2>&1 || true
	if [[ "$drc" -ne 0 ]]; then
		sed -n '1,80p' "$out/docker.stderr" >&2
		could_not_look "container $cname exited $drc (image/runtime, not a hook verdict)"
	fi
}

has_named_systemd_cause() {
	grep -Fq 'init=systemd' "$1" ||
		grep -Fq 'unit_path systemd' "$1" ||
		grep -Fq 'systemctl invoked' "$1"
}

# --- argv ------------------------------------------------------------------
static_only=0
if [[ "${1:-}" == --selftest-static ]]; then
	static_only=1
	shift
fi
if [[ $# -gt 0 ]]; then
	could_not_look "unexpected argument: $*"
fi

for tool in bash chmod cp cut du find git grep mkdir mktemp python3 sed sha256sum stat tar tr; do
	command -v "$tool" >/dev/null 2>&1 || could_not_look "missing $tool"
done

scratch_parent="${TMPDIR:-}"
case "$scratch_parent" in /*) ;; *) could_not_look 'TMPDIR must be absolute' ;; esac
[[ -d "$scratch_parent" ]] || could_not_look "TMPDIR is absent: $scratch_parent"

selftest_static

if [[ "$static_only" -eq 1 ]]; then
	printf '%s\n' "$me: OK — static selftests only (runtime not measured)"
	exit 0
fi

command -v docker >/dev/null 2>&1 ||
	could_not_look "docker is not on PATH"

scratch="$(mktemp -d "$scratch_parent/nfpm-apk-pi.XXXXXX")"
container_names=()
cleanup() {
	local n
	if command -v docker >/dev/null 2>&1; then
		for n in "${container_names[@]+"${container_names[@]}"}"; do
			[[ -n "$n" ]] || continue
			case "$n" in
			"${CONTAINER_PREFIX}"-*) docker rm -f "$n" >/dev/null 2>&1 || true ;;
			esac
		done
	fi
	case "$scratch" in "$scratch_parent"/nfpm-apk-pi.*) rm -rf -- "$scratch" ;; esac
}
trap cleanup EXIT INT TERM

set +e
docker info >/dev/null 2>"$scratch/docker-info.err"
docker_info_rc=$?
set -e
printf '%s\n' "$docker_info_rc" >"$scratch/docker-info.rc"
if [[ "$docker_info_rc" -ne 0 ]]; then
	could_not_look "docker daemon is not running"
fi

goreleaser_bin="$(resolve_tool goreleaser OLIVARES_GORELEASER)" || exit 2
bsdtar_bin="$(resolve_tool bsdtar OLIVARES_BSDTAR)" || exit 2
bsdtar_lib="${OLIVARES_BSDTAR_LIB:-}"
if [[ -n "$bsdtar_lib" ]]; then
	IFS=: read -r -a bsdtar_lib_dirs <<<"$bsdtar_lib"
	for dir in "${bsdtar_lib_dirs[@]}"; do
		case "$dir" in
		/*) ;;
		*) could_not_look "OLIVARES_BSDTAR_LIB entries must be absolute directories, got: '$dir'" ;;
		esac
		[[ -d "$dir" ]] || could_not_look "OLIVARES_BSDTAR_LIB directory is absent: $dir"
	done
fi
bsdtar() {
	if [[ -n "$bsdtar_lib" ]]; then
		env LD_LIBRARY_PATH="$bsdtar_lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}" "$bsdtar_bin" "$@"
	else
		"$bsdtar_bin" "$@"
	fi
}
goreleaser_version="$("$goreleaser_bin" --version 2>/dev/null | sed -n 's/^GitVersion:[[:space:]]*//p')" || true
goreleaser_version="${goreleaser_version%%$'\n'*}"
[[ "$goreleaser_version" =~ ^2\.[0-9]+\.[0-9]+ ]] ||
	could_not_look "goreleaser at $goreleaser_bin did not report a v2 GitVersion (got '${goreleaser_version:-nothing}')"
bsdtar_version="$(bsdtar --version 2>/dev/null)" || true
bsdtar_version="${bsdtar_version%%$'\n'*}"
[[ "$bsdtar_version" == *libarchive* ]] ||
	could_not_look "bsdtar at $bsdtar_bin did not report a libarchive version (got '${bsdtar_version:-nothing}')"
python3 -c 'import yaml' 2>/dev/null || could_not_look 'python3 cannot import yaml (PyYAML)'
printf 'ok - tools: goreleaser %s at %s; %s at %s; docker present; image %s (%s)\n' \
	"$goreleaser_version" "$goreleaser_bin" "$bsdtar_version" "$bsdtar_bin" \
	"$ALPINE_IMAGE" "$ALPINE_VERSION"
printf 'ok - image provenance: %s git %s@%s amd64=%s arm64=%s\n' \
	"$ALPINE_SOURCE_URL" "$ALPINE_GITREPO" "$ALPINE_GITCOMMIT" \
	"$ALPINE_AMD64_DIGEST" "$ALPINE_ARM64_DIGEST"
printf 'ok - alpine releases: %s official-images: %s\n' \
	"$ALPINE_RELEASES" "$ALPINE_OFFICIAL_IMAGES"

mkdir -p "$scratch/gocache"
export GOCACHE="$scratch/gocache"

extract_nfpms "$root/.goreleaser.yaml" "$scratch/current.nfpms.yaml"
make_pkgproj "$scratch/pkgproj" "$scratch/current.nfpms.yaml"
build_packages "$scratch/pkgproj" current
apk="$(first_artifact "$scratch/pkgproj" '*.apk')"
[[ -n "$apk" && -f "$apk" ]] || fail "no apk produced under dist/"
sha256sum "$apk" | tee "$scratch/apk.sha256"
printf 'ok - goreleaser produced %s\n' "$(basename "$apk")"

list="$scratch/current.apk.list"
aroot="$scratch/current.apk-root"
mkdir -p "$aroot"
bsdtar -tf "$apk" >"$list"
grep -E 'etc/init.d/olivares' "$list" >/dev/null || fail "apk missing /etc/init.d/olivares"
if grep -E 'systemd/system/olivares.service' "$list"; then
	fail "apk still contains a systemd unit"
fi
grep -E 'usr/lib/olivares/package-init' "$list" >/dev/null || fail "apk missing package-init stamp"
bsdtar -x -f "$apk" -C "$aroot"
stamp="$aroot/usr/lib/olivares/package-init"
init_unit="$aroot/etc/init.d/olivares"
example="$aroot/usr/share/olivares/olivares.env.example"
[[ -f "$stamp" ]] || fail "apk package-init was not extracted"
[[ "$(tr -d '\n' <"$stamp")" == openrc ]] || fail "apk package-init is not openrc"
[[ -f "$init_unit" ]] || fail "apk OpenRC unit was not extracted"
mode="$(stat -c '%a' "$init_unit")"
[[ "$mode" == 755 ]] || fail "apk OpenRC unit mode is $mode, want 755"

apk_scripts "$apk" "$scratch/current.apk-scripts"
post="$scratch/current.apk-scripts/.post-install"
[[ -f "$post" ]] || fail "apk lacks .post-install"
assert_extracted_hook "$post" "$scratch/hook-equality.txt" ||
	fail "extracted .post-install is not the packaged postinstall (see $scratch/hook-equality.txt)"
printf '%s\n' 'ok - extracted .post-install matches tree postinstall.sh (or a named newline-only wrap)'
cat "$scratch/hook-equality.txt"

# Image: pull with network, then run with --network none.
set +e
docker image inspect "$ALPINE_IMAGE" >/dev/null 2>"$scratch/image-inspect.pre.err"
inspect_pre_rc=$?
set -e
printf '%s\n' "$inspect_pre_rc" >"$scratch/image-inspect.pre.rc"
if [[ "$inspect_pre_rc" -ne 0 ]]; then
	set +e
	docker pull "$ALPINE_IMAGE" >"$scratch/docker-pull.out" 2>"$scratch/docker-pull.err"
	pull_rc=$?
	set -e
	printf '%s\n' "$pull_rc" >"$scratch/docker-pull.rc"
	if [[ "$pull_rc" -ne 0 ]]; then
		sed -n '1,80p' "$scratch/docker-pull.err" >&2
		could_not_look "pinned Alpine image is unavailable: $ALPINE_IMAGE"
	fi
fi
docker image inspect "$ALPINE_IMAGE" >"$scratch/image-inspect.json"
printf 'ok - pinned image present: %s\n' "$ALPINE_IMAGE"

scratch_bytes="$(du -sb "$scratch" | tr '\t' ' ' | cut -d' ' -f1)"
printf '%s\n' "$scratch_bytes" >"$scratch/scratch.bytes"
if [[ "$scratch_bytes" -gt 1073741824 ]]; then
	printf 'note - scratch is %s bytes (>1GiB); package snapshot required the extra space\n' \
		"$scratch_bytes" >&2
fi

prepare_witness() {
	local dest=$1 hook=$2 plant_upgrade=$3
	mkdir -p "$dest"
	emit_inner "$dest/inner.sh"
	cp "$hook" "$dest/post-install"
	cp "$stamp" "$dest/package-init"
	cp "$init_unit" "$dest/olivares.init"
	if [[ -f "$example" ]]; then
		cp "$example" "$dest/olivares.env.example"
	fi
	if [[ "$plant_upgrade" == yes ]]; then
		: >"$dest/plant-upgrade"
	fi
}

# P1 fresh OpenRC
prepare_witness "$scratch/cases/p1/witness" "$post" no
run_container p1 "$scratch/cases/p1/witness" "$scratch/cases/p1/out"
set +e
judge_case "$scratch/cases/p1/out" fresh_openrc \
	>"$scratch/cases/p1/judge.out" 2>"$scratch/cases/p1/judge.err"
p1_rc=$?
set -e
printf '%s\n' "$p1_rc" >"$scratch/cases/p1/judge.rc"
if [[ "$p1_rc" -eq 2 ]]; then
	could_not_look "P1 image/runtime: $(tr '\n' ' ' <"$scratch/cases/p1/judge.err")"
fi
if [[ "$p1_rc" -ne 0 ]]; then
	fail "P1 fresh OpenRC: $(tr '\n' ' ' <"$scratch/cases/p1/judge.err")"
fi
printf '%s\n' 'ok - P1 fresh APK post-install records init=openrc, OpenRC unit 0755, no service start'

# P2 active upgrade
prepare_witness "$scratch/cases/p2/witness" "$post" yes
run_container p2 "$scratch/cases/p2/witness" "$scratch/cases/p2/out"
set +e
judge_case "$scratch/cases/p2/out" upgrade_openrc \
	>"$scratch/cases/p2/judge.out" 2>"$scratch/cases/p2/judge.err"
p2_rc=$?
set -e
printf '%s\n' "$p2_rc" >"$scratch/cases/p2/judge.rc"
if [[ "$p2_rc" -eq 2 ]]; then
	could_not_look "P2 image/runtime: $(tr '\n' ' ' <"$scratch/cases/p2/judge.err")"
fi
if [[ "$p2_rc" -ne 0 ]]; then
	fail "P2 active upgrade: $(tr '\n' ' ' <"$scratch/cases/p2/judge.err")"
fi
printf '%s\n' 'ok - P2 planted upgrade stamp: rc-service olivares start recorded, stamp removed, init=openrc'

run_n1() {
	local nid=$1 plant=$2 judge_mode=$3
	stage_n1_mutant "$scratch/cases/$nid" "$post" ||
		fail "N1 $nid: could not insert the assignment mutant into extracted .post-install"
	prepare_witness "$scratch/cases/$nid/witness" "$scratch/cases/$nid/mutant.post-install" "$plant"
	run_container "$nid" "$scratch/cases/$nid/witness" "$scratch/cases/$nid/out"
	set +e
	judge_case "$scratch/cases/$nid/out" "$judge_mode" \
		>"$scratch/cases/$nid/judge.out" 2>"$scratch/cases/$nid/judge.err"
	n_rc=$?
	set -e
	printf '%s\n' "$n_rc" >"$scratch/cases/$nid/judge.rc"
	if [[ "$n_rc" -eq 2 ]]; then
		could_not_look "N1 $nid image/runtime: $(tr '\n' ' ' <"$scratch/cases/$nid/judge.err")"
	fi
	if [[ "$n_rc" -eq 0 ]]; then
		fail "runtime witness missed the assignment mutant ($nid)"
	fi
	if ! has_named_systemd_cause "$scratch/cases/$nid/judge.err"; then
		fail "N1 $nid failed without named systemd cause: $(tr '\n' ' ' <"$scratch/cases/$nid/judge.err")"
	fi
	printf 'ok - N1 %s: witness rejected assignment mutant (%s)\n' \
		"$nid" "$(tr '\n' ',' <"$scratch/cases/$nid/judge.err")"
}

run_n1 n1fresh no fresh_openrc
run_n1 n1upgrade yes upgrade_openrc

printf '%s\n' "$me: OK — P1 fresh OpenRC, P2 active upgrade, N1 assignment mutant (extracted APK post-install)"
printf '%s\n' "$me: recording stubs do not prove a daemon started; apk add was not run"
