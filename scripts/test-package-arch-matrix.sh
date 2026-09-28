#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Arch Linux container leg of the package matrix. HOSTED ONLY: it builds real Arch
# packages with goreleaser from the nfpms block and runs the real pacman in disposable,
# network-none containers of the pinned archlinux:base image.
#   v1, v2   the tree's Arch package at 1.0.0 and 2.0.0. No Arch package was ever
#            published, so v1 is a synthetic N-1 built from this tree, not a release.
#   repo     a signed pacman repository rendered from v2 by
#            `publish-package-repositories.sh --mode pacman-render`; repo-add is the
#            image's own, run through a wrapper in the same pinned image. The signing
#            key is a THROWAWAY key generated in job scratch; pacman-key --init and
#            --lsign-key run only inside the containers.
# Cells: install (pacman -U v1), upgrade (pacman -U v1 then v2), repo (pacman -Sy
# olivares under SigLevel = Required), remove (pacman -R after an operator edit), three
# refusals under SigLevel = Required (an altered database, an unsigned database and a
# package signed by another key, each required to fail on the olivares signature), and
# aur: packaging/aur/olivares-bin built for real by makepkg as a non-root user in the
# pinned archlinux:base-devel image, from the release tarball the host fetched and bound
# to the cosign-verified checksums.txt (SRCDEST, read-only), then installed with pacman -U.
# Every cell runs with a pacman.conf holding only [options] (the image's NoExtract lines
# kept) and [olivares]: [core] and [extra] cannot sync without network. The olivares binary is a test double that
# records its argv and reproduces only `uninstall --preserve` (systemctl disable --now);
# the containers have no systemd PID 1, so "not enabled" is read from the unit links
# and `systemctl is-enabled`, and nothing here starts the service.
# The leg records the image digest, the image's pacman version and the UTC date: Arch
# is rolling, and a digest ages.
#
# Exit 0: every cell holds. Exit 1: a measured cell failed. Exit 2: a tool, image, build
# or container could not be observed (never a pass).
#   --selftest-static   no docker and no build: cell syntax and judge controls.
#   OLIVARES_ARCH_IMAGE        archlinux:base by index digest (default below).
#   OLIVARES_ARCH_DEVEL_IMAGE  archlinux:base-devel by index digest, for the AUR cell.
# The hosted run needs OLIVARES_COSIGN_BIN (scripts/assert-cosign-binary.sh) to verify the
# release checksums the AUR cell builds from.
set -euo pipefail
LC_ALL=C
export LC_ALL
export GOMAXPROCS="${GOMAXPROCS:-2}"
me=test-package-arch-matrix
CONTAINER_PREFIX=olivares-pkg-arch

could_not_look() {
	printf '%s: NO HE PODIDO MIRAR — %s\n' "$me" "$*" >&2
	exit 2
}
root="${OLIVARES_ROOT:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)}"
# archlinux:base, index digest read 2026-09-27T09:44:35Z from registry-1.docker.io
# (image version 20260920.0.596911, created 2026-09-20).
arch_image="${OLIVARES_ARCH_IMAGE:-docker.io/library/archlinux@sha256:f3691b4dde62ba4c4b6f0ae2c1fbf28e8c0c8c4b9a35c7e06dc1f70e21aa29f6}"
[[ "$arch_image" =~ ^docker\.io/library/archlinux@sha256:[0-9a-f]{64}$ ]] || could_not_look "the Arch image is not pinned by digest: $arch_image"
# archlinux:base-devel (fakeroot and the makepkg toolchain), index digest read
# 2026-09-27T11:44:05Z from registry-1.docker.io (amd64 manifest created 2026-09-21).
aur_image="${OLIVARES_ARCH_DEVEL_IMAGE:-docker.io/library/archlinux@sha256:8745817f349ed24373341ddb92776209eeec3f0364ea48f7f645ac5800d30a50}"
[[ "$aur_image" =~ ^docker\.io/library/archlinux@sha256:[0-9a-f]{64}$ ]] || could_not_look "the AUR image is not pinned by digest: $aur_image"

# --- the cell: bash, runs as root inside the container ----------------------------------
emit_cell() {
	cat >"$1" <<'CELL'
#!/bin/bash
# Usage: cell.sh SCENARIO FINGERPRINT. /work holds the packages, the repositories, the
# public key and the AUR definition (read-only); /srcdest the verified release tarball
# (read-only); /out receives the observations. pacman and makepkg are real.
set -u
scen=$1 fpr=$2
exec >/out/log 2>&1
obs() { printf '%s=%s\n' "$1" "$2" >>/out/obs; }
: >/out/obs
obs pacman "$(pacman -Q pacman 2>/dev/null | tr ' ' '_')"
# The image's NoExtract rules (docs, man pages, locales) stay: the licence texts are read
# where Arch keeps them, /usr/share/licenses. The count is recorded.
obs noextract-lines "$(grep -c '^NoExtract' /etc/pacman.conf || true)"
pacman-key --init >/dev/null 2>&1 || obs keyring init-failed
pacman-key --add /work/key.gpg >/dev/null 2>&1 || obs keyring add-failed
pacman-key --lsign-key "$fpr" >/dev/null 2>&1 || obs keyring lsign-failed
# write_conf REPO [CONF]: keep only the [options] section of CONF and add [olivares] at
# SigLevel = Required, served from /work/REPO. [core] and [extra] cannot sync here.
write_conf() {
	local conf="${2:-/etc/pacman.conf}"
	awk '/^\[/ {keep = ($0 == "[options]")} keep' "$conf" >"$conf.new" || return 1
	printf '\n[olivares]\nSigLevel = Required\nServer = file:///work/%s/stable/pacman/$arch\n' "$1" >>"$conf.new"
	mv -f "$conf.new" "$conf"
}
write_conf repo
observe() { # PREFIX
	local p=$1 v
	v="$(pacman -Q olivares 2>/dev/null | awk '{print $2}')"; obs "$p.version" "${v:-absent}"
	obs "$p.binary" "$([ -x /usr/bin/olivares ] && echo present || echo absent)"
	obs "$p.unit" "$([ -f /usr/lib/systemd/system/olivares.service ] && echo present || echo absent)"
	obs "$p.stamp" "$(cat /usr/lib/olivares/package-init 2>/dev/null || echo absent)"
	obs "$p.user" "$(getent passwd olivares | awk -F: '{print $1 ":" $6 ":" $7}' || true)"
	obs "$p.uid" "$(id -u olivares 2>/dev/null || echo absent)"
	obs "$p.datadir" "$(stat -c '%U:%G:%a' /var/lib/olivares 2>/dev/null || echo absent)"
	obs "$p.manifest" "$(grep -o '"init": "[a-z]*"' /var/lib/olivares/install-manifest.json 2>/dev/null || echo absent)"
	obs "$p.env" "$([ -f /etc/olivares/olivares.env ] && echo present || echo absent)"
	obs "$p.pacsave" "$([ -f /etc/olivares/olivares.env.pacsave ] && echo present || echo absent)"
	obs "$p.licenses" "$(cd /usr/share/licenses/olivares 2>/dev/null && ls -d LICENSE NOTICE LICENSING.md DISCLAIMER.md LICENSES 2>/dev/null | tr '\n' ',' || echo absent)"
	obs "$p.enabled" "$(systemctl is-enabled olivares 2>/dev/null || true)"
	obs "$p.wants" "$(find /etc/systemd/system -name 'olivares.service' 2>/dev/null | wc -l)"
}
run() { # NAME CMD...
	local name=$1 rc
	shift
	"$@"
	rc=$?
	obs "$name.rc" "$rc"
}
case "$scen" in
install)
	run install pacman -U --noconfirm /work/v1.pkg.tar.zst
	observe install
	;;
upgrade)
	run install pacman -U --noconfirm /work/v1.pkg.tar.zst
	observe before
	run upgrade pacman -U --noconfirm /work/v2.pkg.tar.zst
	observe after
	grep -q 'Olivares AI upgraded from 1.0.0-1 to 2.0.0-1.' /out/log && obs upgrade.notice present || obs upgrade.notice absent
	;;
repo)
	run sync pacman -Sy --noconfirm
	run install pacman -S --noconfirm olivares
	observe repo
	;;
remove)
	run sync pacman -Sy --noconfirm
	run install pacman -S --noconfirm olivares
	printf '# operator edit\n' >>/etc/olivares/olivares.env
	run remove pacman -R --noconfirm olivares
	observe removed
	;;
neg-db-altered | neg-db-unsigned | neg-pkg-wrong-key)
	write_conf "$scen"
	run sync pacman -Sy --noconfirm
	# The refusal must be the olivares signature, not any other failure of the sync.
	if grep -Eq 'invalid or corrupted database \(PGP signature\)|olivares\.db\.sig' /out/log; then obs neg.sigerr yes; else obs neg.sigerr no; fi
	if [ -e /var/lib/pacman/sync/olivares.db ]; then obs neg.synced present; else obs neg.synced absent; fi
	run install pacman -S --noconfirm olivares
	if grep -Eq 'invalid or corrupted package \(PGP signature\)|olivares.*signature' /out/log; then obs neg.pkgsigerr yes; else obs neg.pkgsigerr no; fi
	observe neg
	;;
aur)
	useradd -m builder
	cp -r /work/aur /home/builder/aur
	cp /work/check-aur-olivares-bin.sh /home/builder/check-aur-olivares-bin.sh
	chown -R builder:builder /home/builder
	run aurcheck runuser -u builder -- env SRCDEST=/srcdest TMPDIR=/home/builder \
		bash /home/builder/check-aur-olivares-bin.sh --dir /home/builder/aur --makepkg
	run makepkg runuser -u builder -- env SRCDEST=/srcdest bash -c 'cd /home/builder/aur && makepkg -f --noconfirm'
	aurpkg="$(find /home/builder/aur -maxdepth 1 -name 'olivares-bin-*.pkg.tar.zst' | head -1)"
	run install pacman -U --noconfirm "${aurpkg:-/nonexistent}"
	v="$(pacman -Q olivares-bin 2>/dev/null | awk '{print $2}')"; obs aur.version "${v:-absent}"
	obs aur.binary "$([ -x /usr/bin/olivares ] && echo present || echo absent)"
	obs aur.unit "$([ -f /usr/lib/systemd/system/olivares.service ] && echo present || echo absent)"
	obs aur.user "$(getent passwd olivares | awk -F: '{print $1 ":" $6 ":" $7}' || true)"
	obs aur.datadir "$(stat -c '%U:%G:%a' /var/lib/olivares 2>/dev/null || echo absent)"
	obs aur.licenses "$(cd /usr/share/licenses/olivares-bin 2>/dev/null && ls -d LICENSE NOTICE LICENSING.md DISCLAIMER.md LICENSES 2>/dev/null | tr '\n' ',' || echo absent)"
	obs aur.enabled "$(systemctl is-enabled olivares 2>/dev/null || true)"
	obs aur.wants "$(find /etc/systemd/system -name 'olivares.service' 2>/dev/null | wc -l)"
	;;
*) obs cell unknown-scenario ;;
esac
exit 0
CELL
}

# judge_cell OBS SCENARIO: prints the failed expectations; exit 0 when none.
judge_cell() {
	python3 - "$1" "$2" <<'PY'
import sys

obs = {}
for line in open(sys.argv[1], encoding="utf-8"):
    key, _, value = line.rstrip("\n").partition("=")
    obs[key] = value
scen = sys.argv[2]
bad = []
def want(key, value):
    if obs.get(key) != value:
        bad.append(f"{key}={obs.get(key)!r}, want {value!r}")
def installed(p, version):
    want(f"{p}.version", version)
    for key, value in (("binary", "present"), ("unit", "present"), ("stamp", "systemd"),
                       ("user", "olivares:/var/lib/olivares:/usr/bin/nologin"),
                       ("datadir", "olivares:olivares:750"), ("manifest", '"init": "systemd"'),
                       ("env", "present"), ("licenses", "DISCLAIMER.md,LICENSE,LICENSES,LICENSING.md,NOTICE,"),
                       ("enabled", "disabled"), ("wants", "0")):
        want(f"{p}.{key}", value)
for key in ("keyring",):
    if key in obs:
        bad.append(f"keyring step failed: {obs[key]}")
if scen == "install":
    want("install.rc", "0"); installed("install", "1.0.0-1")
elif scen == "upgrade":
    want("install.rc", "0"); want("upgrade.rc", "0")
    installed("before", "1.0.0-1"); installed("after", "2.0.0-1")
    want("upgrade.notice", "present")
    if obs.get("before.uid") != obs.get("after.uid"):
        bad.append("the service account changed across the upgrade")
elif scen == "repo":
    want("sync.rc", "0"); want("install.rc", "0"); installed("repo", "2.0.0-1")
elif scen == "remove":
    want("install.rc", "0"); want("remove.rc", "0")
    for key, value in (("version", "absent"), ("binary", "absent"), ("unit", "absent"),
                       ("user", "olivares:/var/lib/olivares:/usr/bin/nologin"),
                       ("datadir", "olivares:olivares:750"), ("pacsave", "present"), ("wants", "0")):
        want(f"removed.{key}", value)
elif scen in ("neg-db-altered", "neg-db-unsigned"):
    if obs.get("sync.rc") == "0":
        bad.append("pacman -Sy accepted the database under SigLevel = Required")
    want("neg.sigerr", "yes")
    want("neg.synced", "absent")
    want("neg.version", "absent")
elif scen == "neg-pkg-wrong-key":
    want("sync.rc", "0")
    if obs.get("install.rc") == "0":
        bad.append("pacman -S installed a package signed by another key")
    want("neg.pkgsigerr", "yes")
    want("neg.version", "absent")
elif scen == "aur":
    for key, value in (("aurcheck.rc", "0"), ("makepkg.rc", "0"), ("install.rc", "0"),
                       ("aur.binary", "present"), ("aur.unit", "present"),
                       ("aur.user", "olivares:/var/lib/olivares:/usr/bin/nologin"),
                       ("aur.datadir", "olivares:olivares:750"),
                       ("aur.licenses", "DISCLAIMER.md,LICENSE,LICENSES,LICENSING.md,NOTICE,"),
                       ("aur.enabled", "disabled"), ("aur.wants", "0")):
        want(key, value)
    if obs.get("aur.version", "absent") == "absent":
        bad.append("olivares-bin is not installed")
else:
    bad.append(f"unknown scenario {scen}")
print("\n".join(bad))
sys.exit(1 if bad else 0)
PY
}
scenarios=(install upgrade repo remove neg-db-altered neg-db-unsigned neg-pkg-wrong-key aur)

selftest_static() {
	local t
	t="$(mktemp -d "${TMPDIR:?}/pkg-arch-selftest.XXXXXX")"
	st_fail() { rm -rf -- "$t"; printf 'not ok - %s\n' "$*" >&2; exit 1; }
	emit_cell "$t/cell.sh"
	bash -n "$t/cell.sh" || st_fail 'the cell does not parse'
	if grep -E '^[[:space:]]*(exec )?docker run' "$0" | grep -E -- '--privileged|docker.sock' >/dev/null; then
		st_fail 'a docker run invocation is privileged or names docker.sock'
	fi
	if grep -E '^[[:space:]]*(exec )?docker run' "$0" | grep -v -- '--network none' >/dev/null; then
		st_fail 'a docker run invocation has network'
	fi
	[[ "${#scenarios[@]}" -eq 8 && " ${scenarios[*]} " == *" aur "* ]] || st_fail "the leg has ${#scenarios[@]} cells, want 8 with aur"
	[[ "$aur_image" =~ ^docker\.io/library/archlinux@sha256:[0-9a-f]{64}$ ]] || st_fail 'the AUR image is not pinned by digest'
	# RR-03: the cell's own pacman.conf writer on a sample of the image's file. Only
	# [options] (NoExtract kept) and [olivares] may remain: [core] and [extra] cannot
	# sync without network, and their failure must not decide a cell.
	awk '/^write_conf\(\) \{/,/^\}/' "$t/cell.sh" >"$t/write_conf.sh"
	grep -q '^write_conf()' "$t/write_conf.sh" || st_fail 'the cell has no write_conf'
	cat >"$t/pacman.conf" <<'CONF'
[options]
HoldPkg     = pacman glibc
Architecture = auto
NoExtract  = usr/share/help/* !usr/share/help/en*
NoExtract  = usr/share/gtk-doc/html/* usr/share/doc/*
SigLevel    = Required DatabaseOptional
LocalFileSigLevel = Optional

[core]
Include = /etc/pacman.d/mirrorlist

[extra]
Include = /etc/pacman.d/mirrorlist
CONF
	bash -c '. "$1"; write_conf repo "$2"' _ "$t/write_conf.sh" "$t/pacman.conf" || st_fail 'write_conf failed'
	[[ "$(grep -E '^\[' "$t/pacman.conf" | tr '\n' ' ')" == '[options] [olivares] ' ]] ||
		st_fail "pacman.conf sections after write_conf: $(grep -E '^\[' "$t/pacman.conf" | tr '\n' ' ')"
	[[ "$(grep -c '^NoExtract' "$t/pacman.conf")" -eq 2 ]] || st_fail 'write_conf dropped the image NoExtract lines'
	grep -qx 'SigLevel = Required' "$t/pacman.conf" || st_fail 'the olivares repository is not SigLevel = Required'
	grep -qx 'Server = file:///work/repo/stable/pacman/$arch' "$t/pacman.conf" || st_fail 'the olivares Server line'
	good_install() {
		printf '%s\n' pacman=pacman_7.1.0-2 install.rc=0 install.version=1.0.0-1 install.binary=present \
			install.unit=present install.stamp=systemd install.user=olivares:/var/lib/olivares:/usr/bin/nologin \
			install.uid=970 install.datadir=olivares:olivares:750 'install.manifest="init": "systemd"' \
			install.env=present install.pacsave=absent \
			install.licenses=DISCLAIMER.md,LICENSE,LICENSES,LICENSING.md,NOTICE, install.enabled=disabled install.wants=0
	}
	good_install >"$t/good"
	judge_cell "$t/good" install >/dev/null || st_fail 'the judge refuses a good install'
	sed 's/^install.enabled=.*/install.enabled=enabled/' "$t/good" >"$t/enabled"
	if judge_cell "$t/enabled" install >/dev/null; then st_fail 'the judge accepts an enabled unit'; fi
	sed 's/^install.licenses=.*/install.licenses=LICENSE,/' "$t/good" >"$t/nolicence"
	if judge_cell "$t/nolicence" install >/dev/null; then st_fail 'the judge accepts licence texts missing from /usr/share/licenses/olivares'; fi
	printf '%s\n' sync.rc=0 install.rc=1 neg.version=absent neg.sigerr=yes neg.synced=absent >"$t/negdb"
	if judge_cell "$t/negdb" neg-db-altered >/dev/null; then st_fail 'the judge accepts a synced altered database'; fi
	printf '%s\n' sync.rc=1 install.rc=1 neg.version=absent neg.sigerr=no neg.synced=absent >"$t/negdb-vacuous"
	if judge_cell "$t/negdb-vacuous" neg-db-altered >/dev/null; then st_fail 'the judge accepts a failed sync without the olivares signature error'; fi
	printf '%s\n' sync.rc=1 install.rc=1 neg.version=absent neg.sigerr=yes neg.synced=present >"$t/negdb-synced"
	if judge_cell "$t/negdb-synced" neg-db-unsigned >/dev/null; then st_fail 'the judge accepts a refused database left in the sync directory'; fi
	printf '%s\n' sync.rc=1 install.rc=1 neg.version=absent neg.sigerr=yes neg.synced=absent >"$t/negdb-ok"
	judge_cell "$t/negdb-ok" neg-db-unsigned >/dev/null || st_fail 'the judge refuses a refused database'
	printf '%s\n' sync.rc=0 install.rc=0 neg.version=2.0.0-1 neg.pkgsigerr=no >"$t/negpkg"
	if judge_cell "$t/negpkg" neg-pkg-wrong-key >/dev/null; then st_fail 'the judge accepts a wrongly signed package'; fi
	good_aur() {
		printf '%s\n' pacman=pacman_7.1.0-2 aurcheck.rc=0 makepkg.rc=0 install.rc=0 aur.version=26.9.0-1 \
			aur.binary=present aur.unit=present aur.user=olivares:/var/lib/olivares:/usr/bin/nologin \
			aur.datadir=olivares:olivares:750 aur.licenses=DISCLAIMER.md,LICENSE,LICENSES,LICENSING.md,NOTICE, \
			aur.enabled=disabled aur.wants=0
	}
	good_aur >"$t/aur"
	judge_cell "$t/aur" aur >/dev/null || st_fail 'the judge refuses a good AUR cell'
	sed 's/^makepkg.rc=.*/makepkg.rc=1/' "$t/aur" >"$t/aur-nomakepkg"
	if judge_cell "$t/aur-nomakepkg" aur >/dev/null; then st_fail 'the judge accepts an AUR cell where makepkg failed'; fi
	sed 's/^aurcheck.rc=.*/aurcheck.rc=2/' "$t/aur" >"$t/aur-nocheck"
	if judge_cell "$t/aur-nocheck" aur >/dev/null; then st_fail 'the judge accepts an AUR cell whose --makepkg check could not look'; fi
	rm -rf -- "$t"
	printf '%s: selftest-static OK — cell parses, pacman.conf holds [options] and [olivares] only, 8 cells, no network or privilege, judge controls hold\n' "$me"
}
if [[ "${1:-}" == --selftest-static ]]; then
	selftest_static
	exit 0
fi

# --- hosted run ----------------------------------------------------------------------------
scratch_parent="${TMPDIR:-}"
[[ "$scratch_parent" == /* && -d "$scratch_parent" ]] || could_not_look 'TMPDIR must be an existing absolute directory'
for tool in bash cp curl docker find git go gpg gpgconf python3 sed sha256sum; do
	command -v "$tool" >/dev/null 2>&1 || could_not_look "missing $tool"
done
goreleaser_bin="${OLIVARES_GORELEASER:-$(command -v goreleaser 2>/dev/null || true)}"
[[ -n "$goreleaser_bin" && -x "$goreleaser_bin" ]] || could_not_look 'goreleaser is not on PATH and OLIVARES_GORELEASER is unset'
scratch="$(mktemp -d "$scratch_parent/pkg-arch.XXXXXX")"
keys="$(mktemp -d "$scratch_parent/pak.XXXXXX")"
container_names=()
cleanup() {
	local c
	for c in "${container_names[@]}"; do docker rm -f "$c" >/dev/null 2>&1 || true; done
	gpgconf --homedir "$keys/good" --kill all >/dev/null 2>&1 || true
	gpgconf --homedir "$keys/wrong" --kill all >/dev/null 2>&1 || true
	case "$scratch" in "$scratch_parent"/pkg-arch.*) rm -rf -- "$scratch" ;; esac
	case "$keys" in "$scratch_parent"/pak.*) rm -rf -- "$keys" ;; esac
}
trap cleanup EXIT INT TERM
chmod 0700 "$keys"
mkdir -p "$scratch/gocache" "$scratch/work" "$scratch/cells"
export GOCACHE="$scratch/gocache"
printf 'date %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"

docker image inspect "$arch_image" >/dev/null 2>&1 || docker pull "$arch_image" >/dev/null 2>&1 ||
	could_not_look "image unavailable: $arch_image"
printf 'image %s = %s\n' "$arch_image" "$(docker image inspect --format '{{index .RepoDigests 0}}' "$arch_image" 2>/dev/null || echo unknown)"

# Two package sets from the real nfpms block and a test-double binary.
python3 - "$root/.goreleaser.yaml" "$scratch/nfpms.yaml" <<'PY' || could_not_look 'could not extract the nfpms block'
from pathlib import Path
import sys
src = Path(sys.argv[1]).read_text(encoding="utf-8")
start, end = src.find("\nnfpms:\n"), src.find("\nhomebrew_casks:\n")
if start < 0 or end <= start:
    raise SystemExit("nfpms block not found")
Path(sys.argv[2]).write_text(src[start + 1 : end], encoding="utf-8")
PY
build_set() { # LABEL VERSION
	local label=$1 version=$2 proj="$scratch/proj-$1" f a
	mkdir -p "$proj/cmd/olivares" "$proj/packaging"
	cp -a "$root/packaging/." "$proj/packaging/"
	for f in LICENSE NOTICE LICENSING.md DISCLAIMER.md; do cp "$root/$f" "$proj/"; done
	cp -a "$root/LICENSES" "$proj/LICENSES"
	cat >"$proj/cmd/olivares/main.go" <<'GO'
// Test double for the Arch leg: records argv; reproduces only the service effect of
// `olivares uninstall --preserve` for a systemd install.
package main

import (
	"os"
	"os/exec"
	"strings"
)

func main() {
	if f, err := os.OpenFile("/out/calls", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		_, _ = f.WriteString("olivares " + strings.Join(os.Args[1:], " ") + "\n")
		_ = f.Close()
	}
	if len(os.Args) >= 3 && os.Args[1] == "uninstall" && os.Args[2] == "--preserve" {
		_ = exec.Command("systemctl", "disable", "--now", "olivares").Run()
	}
}
GO
	printf 'module github.com/olivaresai/olivares\n\ngo 1.26\n' >"$proj/go.mod"
	{
		printf '%s\n' 'version: 2' 'project_name: olivares' 'builds:' \
			'  - id: olivares' '    main: ./cmd/olivares' '    binary: olivares' \
			'    goos: [linux]' '    goarch: [amd64]' '    env: [CGO_ENABLED=0]' \
			'snapshot:' "  version_template: \"$version\""
		cat "$scratch/nfpms.yaml"
	} >"$proj/.goreleaser.yaml"
	(
		cd "$proj"
		git init -q
		git config user.name fixture
		git config user.email fixture@example.invalid
		git add -- .goreleaser.yaml go.mod cmd packaging LICENSE NOTICE LICENSING.md DISCLAIMER.md LICENSES
		git -c core.hooksPath= -c commit.gpgsign=false commit -q --no-verify -s -m "test: Arch leg project"
		git remote add origin https://example.invalid/olivares/olivares.git
		"$goreleaser_bin" release --snapshot --clean --config .goreleaser.yaml \
			--skip=before,publish,sign,sbom,docker,validate,archive,homebrew,ko,nix,scoop,snapcraft,winget,aur,announce,notarize,chocolatey,flatpak,makeself,mcp,srpm
	) >"$scratch/build-$label.out" 2>&1 || {
		sed -n '1,120p' "$scratch/build-$label.out" >&2
		could_not_look "goreleaser could not build the $label set"
	}
	a="$(find "$proj/dist" -name "olivares_${version}_linux_amd64.pkg.tar.zst" -print | sed -n '1p')"
	[[ -n "$a" && -f "$a" ]] || could_not_look "the $label set has no olivares_${version}_linux_amd64.pkg.tar.zst"
	cp "$a" "$scratch/work/$label.pkg.tar.zst"
	mkdir -p "$scratch/release-$label"
	cp "$a" "$scratch/release-$label/"
}
build_set v1 1.0.0
build_set v2 2.0.0

# Throwaway keys, made here and nowhere else.
for name in good wrong; do
	mkdir -m 0700 "$keys/$name"
	gpg --homedir "$keys/$name" --batch --pinentry-mode loopback --passphrase '' \
		--quick-generate-key "Olivares pacman TEST ONLY $name <pacman-test-$name@invalid.olivares.ai>" rsa2048 sign 0 \
		>/dev/null 2>&1 || could_not_look "cannot generate throwaway key $name"
	gpg --homedir "$keys/$name" --batch --with-colons --list-secret-keys 2>/dev/null |
		awk -F: '$1=="fpr"{print $10; exit}' >"$keys/$name.fpr"
	gpg --homedir "$keys/$name" --batch --pinentry-mode loopback --passphrase '' --armor \
		--export-secret-keys "$(cat "$keys/$name.fpr")" >"$keys/$name.asc"
	chmod 0600 "$keys/$name.asc"
done
fpr="$(cat "$keys/good.fpr")"

# repo-add from the pinned image, run as this user on the current directory.
cat >"$scratch/repo-add" <<WRAP
#!/usr/bin/env bash
exec docker run --rm --network none --user "$(id -u):$(id -g)" --volume "\$PWD:/r" --workdir /r \
	--entrypoint repo-add "$arch_image" "\$@"
WRAP
chmod 0755 "$scratch/repo-add"
mkdir -p "$scratch/work/repo"
OLIVARES_REPO_ADD_BIN="$scratch/repo-add" OLIVARES_PACMAN_SIGNING_KEY_FILE="$keys/good.asc" \
	OLIVARES_PACMAN_SIGNING_FINGERPRINT="$fpr" OLIVARES_PACMAN_EXPECTED_FINGERPRINT="$fpr" TMPDIR="$scratch" \
	bash "$root/scripts/publish-package-repositories.sh" --mode pacman-render --tree "$scratch/work/repo" \
	--pacman-packages "$scratch/release-v2" --version 2.0.0 --source-date-epoch "$(date -u +%s)" ||
	could_not_look 'the publisher could not render the test repository'
cp "$scratch/work/repo/keys/olivares-pacman-repository.gpg" "$scratch/work/key.gpg"
# The refusal repositories: an altered database, an unsigned database, a package signed
# by the other throwaway key.
pkg=olivares_2.0.0_linux_amd64.pkg.tar.zst
for variant in neg-db-altered neg-db-unsigned neg-pkg-wrong-key; do
	cp -a "$scratch/work/repo" "$scratch/work/$variant"
done
printf 'x' >>"$scratch/work/neg-db-altered/stable/pacman/x86_64/olivares.db"
rm -f -- "$scratch/work/neg-db-unsigned/stable/pacman/x86_64/olivares.db.sig"
gpg --homedir "$keys/wrong" --batch --yes --pinentry-mode loopback --passphrase '' \
	--local-user "$(cat "$keys/wrong.fpr")!" --no-armor --detach-sign \
	--output "$scratch/work/neg-pkg-wrong-key/stable/pacman/x86_64/$pkg.sig" \
	"$scratch/work/neg-pkg-wrong-key/stable/pacman/x86_64/$pkg" >/dev/null 2>&1 ||
	could_not_look 'cannot sign with the wrong key'
# The AUR cell builds packaging/aur/olivares-bin from its published release tarball. The
# host fetches the tarball and checksums.txt with its cosign pair, and the AUR check binds
# the PKGBUILD's sha256 to the verified checksums before makepkg sees the tarball.
aur_ver="$(bash -c '. "$1"; printf %s "$pkgver"' _ "$root/packaging/aur/olivares-bin/PKGBUILD")" ||
	could_not_look 'cannot read the AUR pkgver'
aur_asset="olivares_${aur_ver}_linux_amd64.tar.gz"
mkdir -p "$scratch/release-aur" "$scratch/srcdest"
for f in checksums.txt checksums.txt.sig checksums.txt.pem "$aur_asset"; do
	curl --fail --silent --show-error --location --proto '=https' --max-time 300 \
		"https://github.com/olivaresai/olivares/releases/download/v${aur_ver}/$f" -o "$scratch/release-aur/$f" ||
		could_not_look "cannot fetch $f of v$aur_ver"
done
bash "$root/scripts/check-aur-olivares-bin.sh" --checksums "$scratch/release-aur/checksums.txt" ||
	could_not_look "the AUR definition is not bound to the verified v$aur_ver checksums (exit $?)"
want_sha="$(awk -v n="$aur_asset" '$2 == n {print $1}' "$scratch/release-aur/checksums.txt")"
[[ "$(sha256sum "$scratch/release-aur/$aur_asset" | cut -d' ' -f1)" == "$want_sha" ]] ||
	could_not_look "the fetched $aur_asset does not match the verified checksums"
cp "$scratch/release-aur/$aur_asset" "$scratch/srcdest/"
cp -r "$root/packaging/aur/olivares-bin" "$scratch/work/aur"
cp "$root/scripts/check-aur-olivares-bin.sh" "$scratch/work/check-aur-olivares-bin.sh"
emit_cell "$scratch/work/cell.sh"
(cd "$scratch/work" && find . -type f -name '*.pkg.tar.zst' -exec sha256sum {} +) | sort | tee "$scratch/artifacts.sha256"
sha256sum "$scratch/srcdest/$aur_asset" | sed "s|$scratch/||"

failed=0 cells=0 pacman_version=unknown
for scen in "${scenarios[@]}"; do
	out="$scratch/cells/$scen"
	mkdir -p "$out"
	cname="$CONTAINER_PREFIX-$scen-$$"
	container_names+=("$cname")
	image="$arch_image"
	[[ "$scen" == aur ]] && image="$aur_image"
	set +e
	docker run --name "$cname" --rm --network none \
		--volume "$scratch/work:/work:ro" --volume "$scratch/srcdest:/srcdest:ro" --volume "$out:/out" \
		"$image" /bin/bash /work/cell.sh "$scen" "$fpr" >"$out.docker.out" 2>&1
	drc=$?
	set -e
	[[ "$drc" -eq 0 && -s "$out/obs" ]] || { sed -n '1,40p' "$out.docker.out" "$out/log" >&2 2>/dev/null; could_not_look "container $cname exited $drc"; }
	pacman_version="$(sed -n 's/^pacman=//p' "$out/obs" | sed -n '1p')"
	cells=$((cells + 1))
	if verdict="$(judge_cell "$out/obs" "$scen")"; then
		printf 'ok - %s\n' "$scen"
	else
		failed=$((failed + 1))
		printf 'not ok - %s\n%s\n' "$scen" "$(sed 's/^/    /' <<<"$verdict")"
		sed -n '1,60s/^/    | /p' "$out/log"
	fi
done
printf 'recorded: image %s, AUR image %s, %s, date %s\n' "$arch_image" "$aur_image" "${pacman_version:-unknown}" \
	"$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf '%s: %d cells, %d failed\n' "$me" "$cells" "$failed"
[[ "$cells" -eq "${#scenarios[@]}" && "$failed" -eq 0 ]] || exit 1
