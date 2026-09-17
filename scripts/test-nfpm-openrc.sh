#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Build fixture .deb/.rpm/.apk from the real nfpms contents with goreleaser v2,
# inspect payload/hooks, and prove the baseline APK contract is red — from a
# self-contained fixture that needs no git history.
#
# TOOLS — discovered on PATH, or taken from an explicit override that is validated
# before anything runs:
#   OLIVARES_GORELEASER   absolute path to a goreleaser v2 executable
#   OLIVARES_BSDTAR       absolute path to a bsdtar (libarchive) executable
#   OLIVARES_BSDTAR_LIB   colon-separated absolute directories placed on
#                         LD_LIBRARY_PATH for bsdtar only; honoured only when set
# Until 2026-09-07 the defaults were absolute paths inside ONE developer workspace,
# so mainline-ci run 34131797918 (job control-plane) died here with «goreleaser is
# not executable: /workspace/…» after five green mutants — a portability defect,
# not a missing token. CI provisions both tools in the step before this gate
# (scripts/ensure-goreleaser.sh pins goreleaser by digest; libarchive-tools brings
# bsdtar). Absence is exit 2 — «could not look» — never a skip and never a green.
#
# BASELINE WITNESS — the defect this gate keeps red (an APK that shipped a systemd
# unit and recorded init=systemd) used to be read with `git show <sha>:…`. The
# public export is a curated NEW history, so that object cannot exist there and the
# gate could only refuse to look. The witness now lives in
# scripts/fixtures/nfpm-openrc-baseline/ and is BUILT and INSPECTED like the real
# packages: what it proves is the causal failure (APK payload without OpenRC, a hook
# that hard-codes init=systemd), not a string match on history nobody else has.
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

could_not_look() {
	printf 'test-nfpm-openrc: NO HE PODIDO MIRAR — %s\n' "$*" >&2
	exit 2
}
fail() {
	printf 'not ok - %s\n' "$*" >&2
	exit 1
}

root="${OLIVARES_ROOT:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)}"
scratch_parent="${TMPDIR:-}"
case "$scratch_parent" in /*) ;; *) could_not_look 'TMPDIR must be absolute' ;; esac
[[ -d "$scratch_parent" ]] || could_not_look "TMPDIR is absent: $scratch_parent"

# --- tool discovery ------------------------------------------------------------
# resolve_tool NAME VAR: prints the executable to use. An override in $VAR must be
# an absolute path to an executable regular file; without one, NAME is looked up
# on PATH. Anything else is «could not look» — never a fallback to another binary.
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

for tool in bash chmod cp dpkg-deb find git go grep mkdir mktemp python3 sed sha256sum stat tar tr; do
	command -v "$tool" >/dev/null 2>&1 || could_not_look "missing $tool"
done
goreleaser_bin="$(resolve_tool goreleaser OLIVARES_GORELEASER)" || exit 2
bsdtar_bin="$(resolve_tool bsdtar OLIVARES_BSDTAR)" || exit 2

# The library override is applied ONLY when provided, and every entry must exist:
# a typo here would otherwise let the loader pick whatever libarchive it finds and
# call the result «bsdtar».
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

# Discovery is not enough: each tool must run and be what its name says.
goreleaser_version="$("$goreleaser_bin" --version 2>/dev/null | sed -n 's/^GitVersion:[[:space:]]*//p')" || true
goreleaser_version="${goreleaser_version%%$'\n'*}"
[[ "$goreleaser_version" =~ ^2\.[0-9]+\.[0-9]+ ]] ||
	could_not_look "goreleaser at $goreleaser_bin did not report a v2 GitVersion (got '${goreleaser_version:-nothing}')"
bsdtar_version="$(bsdtar --version 2>/dev/null)" || true
bsdtar_version="${bsdtar_version%%$'\n'*}"
[[ "$bsdtar_version" == *libarchive* ]] ||
	could_not_look "bsdtar at $bsdtar_bin did not report a libarchive version (got '${bsdtar_version:-nothing}')"
python3 -c 'import yaml' 2>/dev/null || could_not_look 'python3 cannot import yaml (PyYAML)'
printf 'ok - tools: goreleaser %s at %s; %s at %s\n' \
	"$goreleaser_version" "$goreleaser_bin" "$bsdtar_version" "$bsdtar_bin"

scratch="$(mktemp -d "$scratch_parent/nfpm-openrc.XXXXXX")"
cleanup() {
	case "$scratch" in "$scratch_parent"/nfpm-openrc.*) rm -rf -- "$scratch" ;; esac
}
trap cleanup EXIT INT TERM

# --- env loader: values are not executed as root --------------------------------
pwned="$scratch/pwned"
: >"$scratch/env-eval"
cat >"$scratch/env-eval" <<'EOT'
# comment
OLIVARES_EXTRA_ARGS=--listen=0.0.0.0:8443 --grpc-listen=0.0.0.0:8444
EVIL=$(touch PLACEHOLDER)
QUOTED="--listen=127.0.0.1:9443"
EOT
sed -i "s|PLACEHOLDER|$pwned|" "$scratch/env-eval"
# shellcheck disable=SC1091
set +e
# shellcheck source=packaging/openrc/load-env.sh
. "$root/packaging/openrc/load-env.sh"
olivares_load_env "$scratch/env-eval"
load_rc=$?
set -e
[[ "$load_rc" -eq 0 ]] || fail "olivares_load_env rejected a well-formed env file (rc=$load_rc)"
[[ ! -e "$pwned" ]] || fail "env loader executed command substitution from the env file"
[[ "$olivares_extra_args" == "--listen=0.0.0.0:8443 --grpc-listen=0.0.0.0:8444" ]] ||
	fail "OLIVARES_EXTRA_ARGS was not loaded as a literal string: $olivares_extra_args"
[[ "${EVIL-}" == '$(touch '"$pwned"')' ]] || fail "EVIL was not stored as a literal: ${EVIL-}"
[[ "${QUOTED-}" == "--listen=127.0.0.1:9443" ]] || fail "quoted value was not unwrapped: ${QUOTED-}"
printf '%s\n' 'ok - OpenRC env loader does not execute values'

# --- baseline witness, static: a fixture read from the tree, never from git ------
fixture="$root/scripts/fixtures/nfpm-openrc-baseline"
for f in README.md nfpms.yaml postinstall.sh preremove.sh; do
	[[ -f "$fixture/$f" ]] || could_not_look "baseline fixture is missing $fixture/$f"
done
# baseline_predicate NFPMS POSTINSTALL PREREMOVE: exit 0 when the three read as the
# defect (one unscoped systemd unit, no OpenRC unit, init=systemd recorded, an APK
# removal that stops nothing), exit 1 when they read as the correction.
baseline_predicate() {
	python3 - "$@" <<'PY'
import pathlib, sys
gr = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
post = pathlib.Path(sys.argv[2]).read_text(encoding="utf-8")
pre = pathlib.Path(sys.argv[3]).read_text(encoding="utf-8")
systemd_unscoped = "src: packaging/systemd/olivares.service" in gr and "packager: apk" not in gr
no_openrc_unit = "dst: /etc/init.d/olivares" not in gr
systemd_manifest = '"init": "systemd"' in post
apk_skips_stop = "Alpine has no" in pre or ("uninstall --plan" in pre and "rc-service" not in pre)
raise SystemExit(0 if (systemd_unscoped and no_openrc_unit and systemd_manifest and apk_skips_stop) else 1)
PY
}
# extract_nfpms SRC DST: the nfpms block of a goreleaser config, verbatim.
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
extract_nfpms "$root/.goreleaser.yaml" "$scratch/current.nfpms.yaml"

baseline_predicate "$fixture/nfpms.yaml" "$fixture/postinstall.sh" "$fixture/preremove.sh" ||
	fail "baseline fixture no longer reads as the defect it witnesses (it looks like the OpenRC correction)"
printf '%s\n' 'ok - baseline fixture ships systemd for every format and records init=systemd'
if baseline_predicate "$scratch/current.nfpms.yaml" "$root/packaging/nfpm/postinstall.sh" "$root/packaging/nfpm/preremove.sh"; then
	fail "baseline predicate mistakes the corrected packaging for the baseline"
fi
printf '%s\n' 'ok - corrected packaging is not read as the baseline (predicate control)'

# --- current contract (static) -------------------------------------------------
OLIVARES_ROOT="$root" bash "$root/scripts/check-uninstall-contract.sh" >/dev/null ||
	fail "check-uninstall-contract failed on the corrected tree"
printf '%s\n' 'ok - current uninstall/nfpm contract is green'

# --- mutant: drop APK OpenRC from current goreleaser ---------------------------
python3 - "$root/.goreleaser.yaml" "$scratch/mutant.goreleaser.yaml" <<'PY' || fail "could not write goreleaser mutant"
import pathlib, sys, yaml
src, dst = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
data = yaml.safe_load(src.read_text(encoding="utf-8"))
nfpm = data["nfpms"][0]
nfpm["contents"] = [
    c for c in nfpm["contents"]
    if c.get("packager") != "apk" and c.get("dst") != "/etc/init.d/olivares"
]
for c in nfpm["contents"]:
    if c.get("src") == "packaging/systemd/olivares.service":
        c.pop("packager", None)
nfpm["contents"].append({
    "src": "packaging/systemd/olivares.service",
    "dst": "/usr/lib/systemd/system/olivares.service",
})
nfpm.pop("apk", None)
dst.write_text(yaml.safe_dump(data, sort_keys=False), encoding="utf-8")
PY
set +e
python3 - "$scratch/mutant.goreleaser.yaml" <<'PY'
import pathlib, sys, yaml
nfpm = yaml.safe_load(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))["nfpms"][0]
apk_units = [c for c in nfpm["contents"] if c.get("dst") == "/etc/init.d/olivares"]
apk_scoped = [c for c in apk_units if c.get("packager") == "apk"]
unscoped = [
    c for c in nfpm["contents"]
    if c.get("src") == "packaging/systemd/olivares.service" and not c.get("packager")
]
# Current-tree predicate: APK OpenRC unit exists and systemd is packager-scoped.
good = bool(apk_scoped) and not unscoped
raise SystemExit(0 if good else 1)
PY
mutant_rc=$?
set -e
[[ "$mutant_rc" -eq 1 ]] || fail "mutant without APK OpenRC was accepted by the payload predicate"
printf '%s\n' 'ok - mutant without APK OpenRC is rejected by the payload predicate'

# --- package projects: the real packaging tree plus an nfpms block ---------------
# make_pkgproj DIR NFPMS [OVERLAY]: copies packaging/ and the licence texts, adds a
# stub main and go.mod, writes a goreleaser config whose nfpms block is NFPMS, and
# commits it. OVERLAY, when given, supplies the postinstall/preremove hooks (the
# baseline witness brings its own). Ends with `goreleaser check`.
make_pkgproj() {
	local proj=$1 nfpms=$2 overlay=${3:-} f
	mkdir -p "$proj/cmd/olivares" "$proj/packaging"
	cp -a "$root/packaging/." "$proj/packaging/"
	if [[ -n "$overlay" ]]; then
		cp "$overlay/postinstall.sh" "$proj/packaging/nfpm/postinstall.sh"
		cp "$overlay/preremove.sh" "$proj/packaging/nfpm/preremove.sh"
	fi
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
		# A SYNTHETIC identity, in the house `*@example.invalid` form: this repository is
		# built and destroyed inside this battery's own mktemp, nothing downstream asserts
		# on the author, and goreleaser only needs SOME committer to exist. The real
		# maintainer identity used to be written here as a STRING VALUE in a script that
		# SHIPS, so the export's identity leg reported it on 2026-09-06 and refused the
		# push. A throwaway fixture is the one place where that datum buys nothing at all.
		git config user.name "fixture"
		git config user.email "fixture@example.invalid"
		git add -- .goreleaser.yaml go.mod cmd packaging LICENSE NOTICE LICENSING.md DISCLAIMER.md LICENSES
		git -c core.hooksPath= -c commit.gpgsign=false commit -q --no-verify -s \
			-m "test: fixture packaging project"
		# A SYNTHETIC remote, for the same reason as the identity: `goreleaser check`
		# resolves the release owner/name from the remote of the directory it runs in
		# and refuses «no remote configured to list refs from» without one. Until
		# 2026-09-07 the check ran from the REPOSITORY's cwd and so leaned, unseen, on
		# that clone having an origin — true here and in CI, false in a curated export
		# or any remote-less checkout (measured: the gate went red there at this line).
		# Snapshot builds never contact the remote; the name only has to parse.
		git remote add origin https://example.invalid/olivares/olivares.git
	)
	# The check runs INSIDE the throwaway project so its verdict depends on that
	# project alone, never on the repository this script happens to be run from.
	(
		cd "$proj"
		"$goreleaser_bin" check --config .goreleaser.yaml >/dev/null
	) || fail "goreleaser check rejected the $(basename "$proj") config"
}
# build_packages DIR LABEL: snapshot release of DIR, packages only.
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
	if [[ "$grc" -ne 0 ]]; then
		sed -n '1,160p' "$scratch/$label.goreleaser.err" >&2
		fail "goreleaser snapshot package build failed for $label (rc=$grc)"
	fi
}
# first_artifact DIR GLOB: the one artifact of that kind under DIR/dist, or empty.
first_artifact() {
	find "$1/dist" -name "$2" -print | sed -n '1p'
}
mkdir -p "$scratch/gocache"
export GOCACHE="$scratch/gocache"

# --- inspection: the CURRENT contract --------------------------------------------
# apk_scripts APK DIR: writes each control script the APK carries to DIR/<name>.
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
# inspect_apk APK LABEL: payload contract. Lists into LABEL.apk.list, extracts into
# LABEL.apk-root, and `fail`s on the first miss, NAMING it — the baseline control
# below reads that name.
inspect_apk() {
	local apk=$1 label=$2 list aroot init mode stamp
	list="$scratch/$label.apk.list"
	aroot="$scratch/$label.apk-root"
	bsdtar -tf "$apk" >"$list"
	grep -E 'etc/init.d/olivares' "$list" >/dev/null || fail "apk missing /etc/init.d/olivares"
	if grep -E 'systemd/system/olivares.service' "$list"; then
		fail "apk still contains a systemd unit"
	fi
	grep -E 'usr/lib/olivares/package-init' "$list" >/dev/null || fail "apk missing package-init stamp"
	mkdir -p "$aroot"
	bsdtar -x -f "$apk" -C "$aroot"
	init="$aroot/etc/init.d/olivares"
	[[ -f "$init" ]] || fail "apk init script was not extracted"
	mode="$(stat -c '%a' "$init")"
	[[ "$mode" == 755 ]] || fail "apk OpenRC unit mode is $mode, want 755"
	[[ -x "$init" ]] || fail "apk OpenRC unit is not executable"
	grep -Fq 'command_user="olivares:olivares"' "$init" || fail "apk OpenRC unit lost service account"
	grep -Fq 'command="/usr/bin/olivares"' "$init" || fail "apk OpenRC unit lost the product binary"
	grep -Fq 'serve --data-dir=/var/lib/olivares' "$init" || fail "apk OpenRC unit lost product serve args"
	grep -Fq -- '--listen=127.0.0.1:8443' "$init" || fail "apk OpenRC unit lost loopback HTTP listen"
	grep -Fq 'output_log="/var/log/olivares.log"' "$init" || fail "apk OpenRC unit lost /var/log/olivares.log"
	if grep -Fq 'output_logger' "$init"; then
		fail "apk OpenRC unit pipes the product into logger (SIGPIPE on Alpine without syslogd)"
	fi
	grep -Fq 'chown olivares:olivares /var/lib/olivares' "$init" ||
		fail "apk OpenRC unit does not re-own the data dir before start"
	grep -Fq 'ip link set lo up' "$init" ||
		fail "apk OpenRC unit does not bring up loopback for loopback listeners"
	if grep -E 'var/lib/olivares/?$' "$list" >/dev/null; then
		fail "apk ships /var/lib/olivares (apk resets that dir to root:root)"
	fi
	if grep -Fq 'export "$line"' "$init"; then
		fail "apk OpenRC unit exports raw env lines"
	fi
	stamp="$aroot/usr/lib/olivares/package-init"
	[[ -f "$stamp" ]] || fail "apk package-init was not extracted"
	[[ "$(tr -d '\n' <"$stamp")" == openrc ]] || fail "apk package-init is not openrc"
	return 0
}
# inspect_apk_hooks DIR: hook contract over the control scripts apk_scripts wrote.
inspect_apk_hooks() {
	local dir=$1 post preup prede
	post="$dir/.post-install"
	preup="$dir/.pre-upgrade"
	prede="$dir/.pre-deinstall"
	[[ -f "$post" && -f "$preup" && -f "$prede" ]] ||
		fail "apk lacks a post-install, pre-upgrade or pre-deinstall script"
	grep -Fq 'rc-service olivares start' "$post" || fail "apk post-install lost OpenRC start instructions"
	grep -Fq 'OLIVARES_PKG_INIT' "$post" || fail "apk post-install does not branch on package-init"
	if grep -Fq '"init": "systemd"' "$post"; then
		fail "apk post-install hard-codes init=systemd"
	fi
	grep -Fq 'pkg-upgrade-was-active' "$preup" || fail "apk pre-upgrade lost the upgrade active stamp"
	grep -Fq 'pkg-upgrade-was-active' "$post" || fail "apk post-install lost the upgrade active stamp"
	grep -Fq 'rc-service olivares stop' "$preup" || fail "apk pre-upgrade does not stop an active OpenRC service"
	if grep -Fq 'rc-update add' "$preup"; then
		fail "apk pre-upgrade enables the service"
	fi
	grep -Fq 'rc-service olivares stop' "$prede" || fail "apk pre-deinstall does not stop OpenRC"
	python3 - "$post" <<'PY' || fail "apk post-install classifies the package from systemctl"
import pathlib, sys
post = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
raise SystemExit(1 if "command -v systemctl" in post.split("pkg_init", 1)[0] else 0)
PY
	return 0
}
inspect_deb_rpm() {
	local kind=$1 archive=$2 list
	list="$scratch/$kind.list"
	if [[ "$kind" == deb ]]; then
		dpkg-deb -c "$archive" >"$list"
		dpkg-deb --control "$archive" "$scratch/$kind-control"
	else
		bsdtar -tf "$archive" >"$list"
	fi
	if grep -E 'etc/init.d/olivares' "$list"; then
		fail "$kind contains an OpenRC unit"
	fi
	grep -E 'lib/systemd/system/olivares.service' "$list" >/dev/null ||
		fail "$kind missing systemd unit"
	if [[ "$kind" == deb ]]; then
		[[ -f "$scratch/$kind-control/postinst" ]] || fail "deb missing postinst"
		grep -Fq 'OLIVARES_PKG_INIT' "$scratch/$kind-control/postinst" ||
			fail "deb postinst does not use package-init"
		if grep -Fq '"init": "systemd"' "$scratch/$kind-control/postinst"; then
			fail "deb postinst hard-codes init=systemd"
		fi
	fi
	return 0
}

# --- real packages via the discovered goreleaser ---------------------------------
make_pkgproj "$scratch/pkgproj" "$scratch/current.nfpms.yaml"
build_packages "$scratch/pkgproj" current
apk="$(first_artifact "$scratch/pkgproj" '*.apk')"
deb="$(first_artifact "$scratch/pkgproj" '*.deb')"
rpm="$(first_artifact "$scratch/pkgproj" '*.rpm')"
[[ -n "$apk" && -f "$apk" ]] || fail "no apk produced under dist/"
[[ -n "$deb" && -f "$deb" ]] || fail "no deb produced under dist/"
[[ -n "$rpm" && -f "$rpm" ]] || fail "no rpm produced under dist/"
printf '%s\n' "ok - goreleaser produced $(basename "$apk") $(basename "$deb") $(basename "$rpm")"

inspect_apk "$apk" current
inspect_deb_rpm deb "$deb"
inspect_deb_rpm rpm "$rpm"
printf '%s\n' 'ok - apk carries executable OpenRC; deb/rpm retain systemd'

apk_scripts "$apk" "$scratch/current.apk-scripts"
inspect_apk_hooks "$scratch/current.apk-scripts"
printf '%s\n' 'ok - apk hooks are OpenRC-specific'

# --- baseline witness, built and inspected like the real thing --------------------
# The fixture's nfpms block and hooks are overlaid on the SAME packaging tree and
# built with the SAME goreleaser. The current contract must reject the result, and
# for its causal reason: the payload has no OpenRC unit. Then the shape of the
# defect is asserted on the artifact itself, not on prose about it.
make_pkgproj "$scratch/baseproj" "$fixture/nfpms.yaml" "$fixture"
build_packages "$scratch/baseproj" baseline
baseline_apk="$(first_artifact "$scratch/baseproj" '*.apk')"
[[ -n "$baseline_apk" && -f "$baseline_apk" ]] || fail "baseline fixture produced no apk under dist/"
set +e
(
	set -e
	inspect_apk "$baseline_apk" baseline
) 2>"$scratch/baseline.inspect.err"
baseline_rc=$?
set -e
[[ "$baseline_rc" -eq 1 ]] || fail "baseline apk was accepted by the current payload contract (rc=$baseline_rc)"
grep -Fq 'apk missing /etc/init.d/olivares' "$scratch/baseline.inspect.err" ||
	fail "baseline apk failed for a reason other than its payload: $(tr '\n' ' ' <"$scratch/baseline.inspect.err")"
grep -E 'usr/lib/systemd/system/olivares.service' "$scratch/baseline.apk.list" >/dev/null ||
	fail "baseline apk does not ship the systemd unit the witness is about"
if grep -E 'usr/lib/olivares/package-init' "$scratch/baseline.apk.list" >/dev/null; then
	fail "baseline apk carries a package-init stamp"
fi
apk_scripts "$baseline_apk" "$scratch/baseline.apk-scripts"
grep -Fq '"init": "systemd"' "$scratch/baseline.apk-scripts/.post-install" ||
	fail "baseline post-install does not record init=systemd"
if grep -Fq 'OLIVARES_PKG_INIT' "$scratch/baseline.apk-scripts/.post-install"; then
	fail "baseline post-install branches on package-init"
fi
if grep -Fq 'rc-service olivares stop' "$scratch/baseline.apk-scripts/.pre-deinstall"; then
	fail "baseline pre-deinstall stops an OpenRC service it never shipped"
fi
set +e
(
	set -e
	inspect_apk_hooks "$scratch/baseline.apk-scripts"
) 2>"$scratch/baseline.hooks.err"
baseline_hooks_rc=$?
set -e
[[ "$baseline_hooks_rc" -eq 1 ]] ||
	fail "baseline apk hooks were accepted by the current hook contract (rc=$baseline_hooks_rc)"
printf '%s\n' 'ok - baseline apk is red for its causal defect: systemd payload, init=systemd hook, no OpenRC'

sha256sum "$apk" "$deb" "$rpm" "$baseline_apk" | tee "$scratch/artifact-digests.txt"
printf '%s\n' "test-nfpm-openrc: OK — fixture packages, baseline witness and mutant predicates"
