#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
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

repo_root="$(git rev-parse --show-toplevel)"
if [[ "${TMPDIR:-}" != /* || ! -d "$TMPDIR" ]]; then
	printf '%s\n' 'test-package-repositories: COULD NOT CHECK — TMPDIR must be an existing absolute directory' >&2
	exit 2
fi
for tool in python3 gpg openssl jq sha256sum diff cp mv; do
	command -v "$tool" >/dev/null 2>&1 || {
		printf 'test-package-repositories: COULD NOT CHECK — missing %s\n' "$tool" >&2
		exit 2
	}
done

scratch="$(mktemp -d "${TMPDIR:-}/dist24-06.XXXXXX")"
runner_like_tmp=""
unicode_tmp=""
# The renderer and the verifier place a gpg home in TMPDIR, and gpg-agent's socket path must
# fit sun_path (108 bytes). A caller's TMPDIR may be long, so both run with a short one.
short_tmp="$(mktemp -d /tmp/g.XXXXXX)" || {
	printf '%s\n' 'test-package-repositories: COULD NOT CHECK — cannot create a short TMPDIR under /tmp' >&2
	exit 2
}
cleanup() {
	case "$short_tmp" in
	/tmp/g.*) gpgconf --kill all >/dev/null 2>&1 || true; rm -rf -- "$short_tmp" ;;
	*) printf 'test-package-repositories: refusing unsafe cleanup: %s\n' "$short_tmp" >&2 ;;
	esac
	for external_tmp in "$runner_like_tmp" "$unicode_tmp"; do
		[[ -n "$external_tmp" ]] || continue
		case "$external_tmp" in
		/dev/shm/package-repo-runner.*|/dev/shm/é*) rm -rf -- "$external_tmp" ;;
		*) printf 'test-package-repositories: refusing unsafe cleanup: %s\n' "$external_tmp" >&2 ;;
		esac
	done
	case "$scratch" in
	"${TMPDIR:-}"/dist24-06.*) rm -rf -- "$scratch" ;;
	*) printf 'test-package-repositories: refusing unsafe cleanup: %s\n' "$scratch" >&2 ;;
	esac
}
trap cleanup EXIT INT TERM
epoch=1704067200
valid_until=1735689600
version=26.900
checks=0

expect_rc() {
	local label="$1"
	local expected="$2"
	shift 2
	local actual
	set +e
	"$@" >"$scratch/$label.stdout" 2>"$scratch/$label.stderr"
	actual=$?
	set -e
	if [[ "$actual" -ne "$expected" ]]; then
		# A step that could not look (rc=2) keeps the battery at rc=2; it is never
		# reported as a finding.
		if [[ "$actual" -eq 2 ]]; then
			printf 'test-package-repositories: COULD NOT CHECK — %s could not look (rc=2, expected rc=%s)\n' \
				"$label" "$expected" >&2
			sed -n '1,20p' "$scratch/$label.stderr" >&2
			exit 2
		fi
		printf 'test-package-repositories: HALLAZGO — %s expected rc=%s, got rc=%s\n' \
			"$label" "$expected" "$actual" >&2
		sed -n '1,20p' "$scratch/$label.stderr" >&2
		exit 1
	fi
	checks=$((checks + 1))
	printf 'ok %02d - %s (rc=%s)\n' "$checks" "$label" "$actual"
}

render() {
	local key_dir="$1"
	local assets="$2"
	local channel="$3"
	local out="$4"
	local rpm_tree=()
	[[ -z "${5:-}" ]] || rpm_tree=(--rpm-tree "$5")
	# The renderer renames its output out of TMPDIR, so it writes inside the short TMPDIR and
	# the result then moves to OUT.
	local staged="$short_tmp/render-out"
	rm -rf -- "$staged"
	TMPDIR="$short_tmp" \
	OLIVARES_PACKAGE_REPO_TEST_ONLY=1 \
	OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE="$key_dir/descriptor.json" \
	OLIVARES_PACKAGE_REPO_OPENPGP_SECRET_KEY_FILE="$key_dir/openpgp-secret.asc" \
	OLIVARES_PACKAGE_REPO_APK_PRIVATE_KEY_FILE="$key_dir/apk-private.pem" \
	python3 "$repo_root/scripts/render-package-repositories.py" \
		--checksums "$assets/checksums.txt" \
		--artifact-dir "$assets" \
		--version "$version" \
		--channel "$channel" \
		--source-date-epoch "$epoch" \
		--valid-until-epoch "$valid_until" \
		--out "$staged" "${rpm_tree[@]}" || return $?
	mv -- "$staged" "$out"
}

verify() {
	local key_dir="$1"
	local assets="$2"
	local channel="$3"
	local repository="$4"
	TMPDIR="$short_tmp" python3 "$repo_root/scripts/verify-package-repositories.py" \
		--repo-root "$repository" \
		--checksums "$assets/checksums.txt" \
		--artifact-dir "$assets" \
		--version "$version" \
		--channel "$channel" \
		--openpgp-key "$key_dir/openpgp-public.asc" \
		--apk-key "$key_dir/apk-public.pem" "${@:5}"
}

verify_s3() {
	verify "$@" --rpm-s3
}

expect_rc fixture-stable 0 python3 "$repo_root/scripts/package_repository_test_fixtures.py" \
	--out "$scratch/assets-stable" --variant stable
expect_rc fixture-security 0 python3 "$repo_root/scripts/package_repository_test_fixtures.py" \
	--out "$scratch/assets-security" --variant security
expect_rc key-one 0 env OLIVARES_PACKAGE_REPO_TEST_ONLY=1 \
	"$repo_root/scripts/generate-package-repository-test-key.sh" "$scratch/key-one"
expect_rc key-two 0 env OLIVARES_PACKAGE_REPO_TEST_ONLY=1 \
	"$repo_root/scripts/generate-package-repository-test-key.sh" "$scratch/key-two"

expect_rc render-stable 0 render "$scratch/key-one" "$scratch/assets-stable" stable "$scratch/repo-stable"
expect_rc verify-stable 0 verify "$scratch/key-one" "$scratch/assets-stable" stable "$scratch/repo-stable"
# The failing self-hosted runner had a 41-character TMPDIR. Recreate that length exactly under
# /dev/shm so this control is independent of the caller's path. With the old verifier prefix the
# gpg-agent socket exceeded sun_path; the shorter prefix plus --no-autostart must verify normally.
[[ -d /dev/shm ]] || {
	printf '%s\n' 'test-package-repositories: COULD NOT CHECK — /dev/shm is required for the runner-path control' >&2
	exit 2
}
runner_prefix='/dev/shm/package-repo-runner.'
runner_pad=$((41 - ${#runner_prefix} - 7))
[[ "$runner_pad" -ge 1 ]] || {
	printf '%s\n' 'test-package-repositories: HALLAZGO — runner-path fixture no longer fits 41 characters' >&2
	exit 1
}
runner_like_tmp="$(mktemp -d "${runner_prefix}$(printf 'x%.0s' $(seq 1 "$runner_pad")).XXXXXX")"
[[ "${#runner_like_tmp}" -eq 41 ]] || {
	printf 'test-package-repositories: HALLAZGO — runner-path fixture has %s chars, expected 41\n' \
		"${#runner_like_tmp}" >&2
	exit 1
}
expect_rc verify-stable-runner-tmpdir 0 env TMPDIR="$runner_like_tmp" \
	python3 "$repo_root/scripts/verify-package-repositories.py" \
		--repo-root "$scratch/repo-stable" \
		--checksums "$scratch/assets-stable/checksums.txt" \
		--artifact-dir "$scratch/assets-stable" \
		--version "$version" \
		--channel stable \
		--openpgp-key "$scratch/key-one/openpgp-public.asc" \
		--apk-key "$scratch/key-one/apk-public.pem"

# Even with --no-autostart, some gpg builds connect to an existing agent after import. A path that
# cannot represent that socket is the third answer, with the length named instead of blaming a key
# that gpg imported successfully.
too_long_tmp="$scratch/$(printf 'l%.0s' $(seq 1 90))"
mkdir -p "$too_long_tmp"
expect_rc verify-too-long-tmpdir 2 env TMPDIR="$too_long_tmp" \
	python3 "$repo_root/scripts/verify-package-repositories.py" \
		--repo-root "$scratch/repo-stable" \
		--checksums "$scratch/assets-stable/checksums.txt" \
		--artifact-dir "$scratch/assets-stable" \
		--version "$version" \
		--channel stable \
		--openpgp-key "$scratch/key-one/openpgp-public.asc" \
		--apk-key "$scratch/key-one/apk-public.pem"
grep -q 'too long for gpg-agent' "$scratch/verify-too-long-tmpdir.stderr" || {
	printf '%s\n' 'test-package-repositories: HALLAZGO — long TMPDIR did not name gpg-agent length' >&2
	exit 1
}
# sun_path counts bytes, not Unicode code points. This homedir is short by character count but too
# long in UTF-8; a len(str(home)) guard would accept it and let gpg emit the opaque rc 2 again.
unicode_prefix='éééééééééééééééééééé'  # language-data: UTF-8 path-length fixture
unicode_tmp="$(mktemp -d "/dev/shm/$unicode_prefix.XXXXXX")"
expect_rc verify-nonascii-long-tmpdir 2 env TMPDIR="$unicode_tmp" \
	python3 "$repo_root/scripts/verify-package-repositories.py" \
		--repo-root "$scratch/repo-stable" \
		--checksums "$scratch/assets-stable/checksums.txt" \
		--artifact-dir "$scratch/assets-stable" \
		--version "$version" \
		--channel stable \
		--openpgp-key "$scratch/key-one/openpgp-public.asc" \
		--apk-key "$scratch/key-one/apk-public.pem"
grep -q 'bytes.*too long\|too long.*bytes' "$scratch/verify-nonascii-long-tmpdir.stderr" || {
	printf '%s\n' 'test-package-repositories: HALLAZGO — non-ASCII TMPDIR did not report a byte length' >&2
	exit 1
}
expect_rc render-stable-repeat 0 render \
	"$scratch/key-one" "$scratch/assets-stable" stable "$scratch/repo-stable-repeat"
expect_rc deterministic-tree 0 diff -qr "$scratch/repo-stable" "$scratch/repo-stable-repeat"

expect_rc render-security 0 render \
	"$scratch/key-one" "$scratch/assets-security" security "$scratch/repo-security"
expect_rc verify-security 0 verify \
	"$scratch/key-one" "$scratch/assets-security" security "$scratch/repo-security"

# The publisher's rpm trees come from S3's signed path (publish-packages). The
# fixture trees are S3's own inside publish step, with stub Fedora tools and
# key one; render --rpm-tree adopts them, and verify --rpm-s3 checks them with
# S3's verifier and binds each rpm to its release asset's payload. The stub
# tools execute from TMPDIR; a host whose short TMPDIR is mounted noexec may
# name an exec-capable OLIVARES_EXEC_TMPDIR for that step alone.
s3_tmpdir="${OLIVARES_EXEC_TMPDIR:-$TMPDIR}"
[[ "$s3_tmpdir" == /* && -d "$s3_tmpdir" ]] || {
	printf '%s\n' 'test-package-repositories: COULD NOT CHECK — OLIVARES_EXEC_TMPDIR must be an existing absolute directory' >&2
	exit 2
}
for channel in stable security; do
	expect_rc "s3-rpm-$channel" 0 env OLIVARES_PACKAGE_REPO_TEST_ONLY=1 TMPDIR="$s3_tmpdir" \
		bash "$repo_root/scripts/test-rpm-repository.sh" --fixture-publish-tree \
		--assets "$scratch/assets-$channel" --version "$version" --key-dir "$scratch/key-one" \
		--out "$scratch/rpm-s3-$channel"
	expect_rc "render-$channel-s3" 0 render \
		"$scratch/key-one" "$scratch/assets-$channel" "$channel" "$scratch/repo-$channel-s3" "$scratch/rpm-s3-$channel"
	# The apt and apk trees are the same bytes whichever rpm path is used.
	for family in apt apk; do
		expect_rc "same-$family-$channel" 0 diff -r \
			"$scratch/repo-$channel/$channel/$family" "$scratch/repo-$channel-s3/$channel/$family"
	done
	expect_rc "verify-$channel-s3" 0 verify_s3 \
		"$scratch/key-one" "$scratch/assets-$channel" "$channel" "$scratch/repo-$channel-s3"
done

# Mutant 5: the unsigned rpm render is not an S3 tree.
expect_rc mutant-unsigned-rpm-as-s3 1 verify_s3 \
	"$scratch/key-one" "$scratch/assets-stable" stable "$scratch/repo-stable"
# Mutant 6: an S3 rpm changed after its metadata was signed.
cp -a "$scratch/repo-stable-s3" "$scratch/repo-s3-changed"
printf 'x' >>"$scratch/repo-s3-changed/stable/rpm/x86_64/olivares_${version}_linux_amd64.rpm"
expect_rc mutant-s3-rpm-changed 1 verify_s3 \
	"$scratch/key-one" "$scratch/assets-stable" stable "$scratch/repo-s3-changed"
# Mutant 9: a file outside S3's declared delivery set in an rpm arch tree.
cp -a "$scratch/repo-stable-s3" "$scratch/repo-s3-extra"
printf 'not delivered\n' >"$scratch/repo-s3-extra/stable/rpm/x86_64/extra-unsigned.txt"
expect_rc mutant-s3-rpm-extra-file 1 verify_s3 \
	"$scratch/key-one" "$scratch/assets-stable" stable "$scratch/repo-s3-extra"
grep -q 'unexpected=\[.extra-unsigned.txt.\]' "$scratch/mutant-s3-rpm-extra-file.stderr" || {
	printf '%s\n' 'test-package-repositories: HALLAZGO — the extra-file mutant was refused for another reason' >&2
	exit 1
}
# Mutant 7: a consistently signed S3 tree whose rpm payload is not the release
# asset's. Its own metadata agrees; only the payload binding can refuse it.
cp -a "$scratch/assets-stable" "$scratch/assets-other"
printf 'x' >>"$scratch/assets-other/olivares_${version}_linux_amd64.rpm"
expect_rc s3-rpm-other-payload 0 env OLIVARES_PACKAGE_REPO_TEST_ONLY=1 TMPDIR="$s3_tmpdir" \
	bash "$repo_root/scripts/test-rpm-repository.sh" --fixture-publish-tree \
	--assets "$scratch/assets-other" --version "$version" --key-dir "$scratch/key-one" \
	--out "$scratch/rpm-s3-other"
expect_rc render-stable-other-payload 0 render \
	"$scratch/key-one" "$scratch/assets-stable" stable "$scratch/repo-s3-other" "$scratch/rpm-s3-other"
expect_rc mutant-s3-rpm-other-payload 1 verify_s3 \
	"$scratch/key-one" "$scratch/assets-stable" stable "$scratch/repo-s3-other"
grep -q 'payload of its authenticated release asset' "$scratch/mutant-s3-rpm-other-payload.stderr" || {
	printf '%s\n' 'test-package-repositories: HALLAZGO — the payload mutant was refused for another reason' >&2
	exit 1
}

# The assembled publish tree holds S3's rpm trees, with repomd.xml.asc and
# delivery.json for stable and security, and never the unsigned rpm render.
key_view="$scratch/key-view"
mkdir -m 0755 "$key_view"
apk_name="$(jq -r .apk_public_key_name "$scratch/key-one/descriptor.json")"
cp "$scratch/repo-stable/keys/olivares-package-repository.asc" "$key_view/olivares-packages.asc"
gpg --homedir "$scratch/key-one/gnupg" --batch --dearmor --output "$key_view/olivares-packages.gpg" \
	"$key_view/olivares-packages.asc"
cp "$scratch/repo-stable/keys/$apk_name" "$key_view/$apk_name"
chmod 0644 "$key_view"/*
expect_rc assemble-s3 0 python3 "$repo_root/scripts/assemble-package-repository-publish-tree.py" \
	--stable "$scratch/repo-stable-s3" --security "$scratch/repo-security-s3" \
	--key-dir "$key_view" --out "$scratch/publish-s3"
for channel in stable security; do
	for arch in x86_64 aarch64; do
		for object in delivery.json repodata/repomd.xml.asc; do
			[[ -s "$scratch/publish-s3/$channel/rpm/$arch/$object" ]] || {
				printf 'test-package-repositories: HALLAZGO — publish tree lacks %s/rpm/%s/%s\n' \
					"$channel" "$arch" "$object" >&2
				exit 1
			}
		done
		[[ ! -e "$scratch/publish-s3/$channel/rpm/$arch/Packages" ]] || {
			printf 'test-package-repositories: HALLAZGO — publish tree holds an unsigned rpm tree\n' >&2
			exit 1
		}
	done
done
checks=$((checks + 1))
printf 'ok %02d - publish tree holds S3 rpm-md, repomd.xml.asc and delivery.json for both channels\n' "$checks"
# Mutant 8: the unsigned rpm render cannot be assembled for publication.
expect_rc mutant-assemble-unsigned-rpm 1 python3 "$repo_root/scripts/assemble-package-repository-publish-tree.py" \
	--stable "$scratch/repo-stable" --security "$scratch/repo-security" \
	--key-dir "$key_view" --out "$scratch/publish-unsigned"

# Mutant 1: a valid repository signed by a different key must be rejected by the
# externally provisioned client anchors. Rendering and verifying with key two are
# its positive control before the same tree is checked against key one.
expect_rc render-wrong-key-control 0 render \
	"$scratch/key-two" "$scratch/assets-stable" stable "$scratch/repo-wrong-key"
expect_rc verify-wrong-key-control 0 verify \
	"$scratch/key-two" "$scratch/assets-stable" stable "$scratch/repo-wrong-key"
expect_rc mutant-wrong-signing-key 1 verify \
	"$scratch/key-one" "$scratch/assets-stable" stable "$scratch/repo-wrong-key"

# Mutant 2: package bytes changed after index generation must not pass, while the
# untouched repository already passed verify-stable above.
cp -a "$scratch/repo-stable" "$scratch/repo-swapped"
printf 'swapped\n' >>"$scratch/repo-swapped/stable/apt/pool/main/o/olivares/olivares_${version}_linux_amd64.deb"
expect_rc mutant-package-without-index 1 verify \
	"$scratch/key-one" "$scratch/assets-stable" stable "$scratch/repo-swapped"

# Mutant 3: serving a correctly signed security tree at the stable route must fail
# on the signed channel identity. verify-security above is its positive control.
cp -a "$scratch/repo-security" "$scratch/repo-channel"
mv "$scratch/repo-channel/security" "$scratch/repo-channel/stable"
expect_rc mutant-stable-serves-security 1 verify \
	"$scratch/key-one" "$scratch/assets-security" stable "$scratch/repo-channel"

# Mutant 4: no real signing inputs is explicitly unmeasurable (rc=2), never an
# unsigned repository. render-stable above is the key-present positive control.
expect_rc mutant-real-key-absent 2 env \
	-u OLIVARES_PACKAGE_REPO_TEST_ONLY \
	-u OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE \
	-u OLIVARES_PACKAGE_REPO_OPENPGP_SECRET_KEY_FILE \
	-u OLIVARES_PACKAGE_REPO_APK_PRIVATE_KEY_FILE \
	python3 "$repo_root/scripts/render-package-repositories.py" \
		--checksums "$scratch/assets-stable/checksums.txt" \
		--artifact-dir "$scratch/assets-stable" \
		--version "$version" \
		--channel stable \
		--source-date-epoch "$epoch" \
		--valid-until-epoch "$valid_until" \
		--out "$scratch/repo-no-key"
[[ ! -e "$scratch/repo-no-key" ]] || {
	printf '%s\n' 'test-package-repositories: HALLAZGO — missing-key mutant left output behind' >&2
	exit 1
}
checks=$((checks + 1))
printf 'ok %02d - missing-key failure left no unsigned output\n' "$checks"

# A host adds the repository with the two files the publish tree carries, then takes releases
# from it with the package manager. The tree is assembled as the publisher assembles it (the
# canonical origin), the publisher's user-file check must accept it, and a real apt (rootless, a private
# state tree) then reads the deb822 file for a local copy of the same tree. The rpm file is checked
# as text, because dnf runs in the clean-client legs.
for tool in apt-get apt-cache gpgv; do
	command -v "$tool" >/dev/null 2>&1 || {
		printf 'test-package-repositories: COULD NOT CHECK — %s is required for the apt host checks\n' "$tool" >&2
		exit 2
	}
done
served="$scratch/served"
# apt refuses metadata that is not yet valid or already expired, so this block signs for now.
epoch=$(($(date -u +%s) - 3600))
valid_until=$((epoch + 31536000))

# release_tree VERSION: stable and security for VERSION, assembled as the publisher would.
release_tree() {
	local release="$1" channel
	for channel in stable security; do
		python3 "$repo_root/scripts/package_repository_test_fixtures.py" \
			--out "$scratch/rel-$release-$channel" --variant "$channel" --version "$release" || return 1
		env OLIVARES_PACKAGE_REPO_TEST_ONLY=1 TMPDIR="$s3_tmpdir" \
			bash "$repo_root/scripts/test-rpm-repository.sh" --fixture-publish-tree \
			--assets "$scratch/rel-$release-$channel" --version "$release" --key-dir "$scratch/key-one" \
			--out "$scratch/rel-$release-$channel-rpm" || return 1
		version="$release" render "$scratch/key-one" "$scratch/rel-$release-$channel" "$channel" \
			"$scratch/rel-$release-$channel-repo" "$scratch/rel-$release-$channel-rpm" || return 1
	done
	python3 "$repo_root/scripts/assemble-package-repository-publish-tree.py" \
		--stable "$scratch/rel-$release-stable-repo" --security "$scratch/rel-$release-security-repo" \
		--key-dir "$key_view" --out "$served"
}
# user_files_check TREE: the check the publisher runs on a tree before any R2 access (its own
# tests cover that call; this tree is signed with a test key, so the full plan does not apply).
user_files_check() {
	python3 "$repo_root/scripts/assemble-package-repository-publish-tree.py" --check-user-files "$1"
}
# canonical_user_files_ok TREE: the files name the one production origin and its key.
canonical_user_files_ok() {
	grep -qxF 'URIs: https://packages.olivares.ai/stable/apt' "$1/olivares.sources" &&
		grep -qxF "baseurl=https://packages.olivares.ai/stable/rpm/\$basearch" "$1/olivares.repo" &&
		grep -qxF 'gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-olivares-package-repository' "$1/olivares.repo"
}
# localize_user_files TREE: the same two files for a copy of the tree that is served from TREE
# itself, made with the assembler's own renderers, so a real apt can read it without a network.
localize_user_files() {
	python3 - "$repo_root/scripts/assemble-package-repository-publish-tree.py" "$1" <<'PY'
import importlib.util
import pathlib
import sys

spec = importlib.util.spec_from_file_location("assemble", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
tree = pathlib.Path(sys.argv[2])
origin = "file://" + str(tree)
key = (tree / "olivares-packages.asc").read_text(encoding="utf-8")
(tree / "olivares.sources").write_text(module.sources_file(origin, key), encoding="utf-8")
template = module.REPO_TEMPLATE.read_text(encoding="utf-8")
(tree / "olivares.repo").write_text(module.repo_file(origin, template), encoding="utf-8")
PY
}

# apt_host DIR SOURCES [INSTALLED]: a private apt state that reads SOURCES and, when INSTALLED
# is given, holds olivares at that version as installed.
apt_host() {
	local dir="$1" sources="$2" installed="${3:-}"
	mkdir -p "$dir/apt.conf.d" "$dir/sources.list.d" "$dir/trusted.gpg.d" "$dir/lists/partial" "$dir/archives/partial"
	cp -- "$sources" "$dir/sources.list.d/olivares.sources"
	: >"$dir/status"
	if [[ -n "$installed" ]]; then
		printf 'Package: olivares\nStatus: install ok installed\nPriority: optional\nSection: admin\nInstalled-Size: 1\nMaintainer: Olivares AI <release@olivares.ai>\nArchitecture: amd64\nVersion: %s\nDescription: installed fixture\n\n' \
			"$installed" >"$dir/status"
	fi
	cat >"$dir/apt.conf" <<CONF
Dir::Etc::parts "$dir/apt.conf.d";
Dir::Etc::sourcelist "/dev/null";
Dir::Etc::sourceparts "$dir/sources.list.d";
Dir::Etc::trusted "/dev/null";
Dir::Etc::trustedparts "$dir/trusted.gpg.d";
Dir::State::lists "$dir/lists";
Dir::State::status "$dir/status";
Dir::Cache::archives "$dir/archives";
Dir::Cache::pkgcache "";
Dir::Cache::srcpkgcache "";
Dir::Log "$dir/log";
Debug::NoLocking "true";
APT::Architecture "amd64";
APT::Architectures "amd64";
Acquire::Languages "none";
APT::Sandbox::User "";
CONF
}
apt_in() {
	local dir="$1"
	shift
	env APT_CONFIG="$dir/apt.conf" "$@"
}
apt_candidate_is() {
	[[ "$(apt_in "$1" apt-cache policy olivares | awk '$1 == "Candidate:" {print $2}')" == "$2" ]]
}
# apt_download_is DIR ASSET: the package apt downloads is the release asset, byte for byte.
apt_download_is() {
	local dir="$1" asset="$2" got="$1.download"
	mkdir -p "$got"
	(cd "$got" && apt_in "$dir" apt-get download olivares) || return 1
	cmp -s "$got"/olivares_*.deb "$asset"
}
# user_repo_file_ok FILE ORIGIN: the dnf file sets exactly the three signature checks, once each
# (DNF 4 reads gpgcheck, DNF5 pkg_gpgcheck; a later line would win), keeps $basearch for dnf to
# expand, and names the key file the host stores (never an address the repository answers) and
# both architectures' metadata this tree serves.
user_repo_file_ok() {
	local file="$1" origin="$2" arch
	[[ "$(grep -E '^[[:space:]]*(pkg_|repo_)?gpgcheck[[:space:]]*=' "$file" | LC_ALL=C sort)" == \
		$'gpgcheck=1\npkg_gpgcheck=1\nrepo_gpgcheck=1' ]] || return 1
	grep -qxF "baseurl=$origin/stable/rpm/\$basearch" "$file" || return 1
	[[ "$(grep -Ec '^[[:space:]]*gpgkey[[:space:]]*=' "$file")" -eq 1 ]] || return 1
	! grep -Eq '^[[:space:]]+[^[:space:]#]' "$file" || return 1
	grep -qxF 'gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-olivares-package-repository' "$file" || return 1
	! grep -q '{{' "$file" || return 1
	cmp -s "$served/olivares-packages.asc" "$served/keys/olivares-package-repository.asc" || return 1
	for arch in x86_64 aarch64; do
		[[ -s "$served/stable/rpm/$arch/repodata/repomd.xml" && -s "$served/stable/rpm/$arch/repodata/repomd.xml.asc" ]] || return 1
	done
}
# wrong_key_sources OUT: the same deb822 file carrying key two instead of the repository key.
wrong_key_sources() {
	python3 - "$repo_root/scripts/assemble-package-repository-publish-tree.py" \
		"$scratch/key-two/openpgp-public.asc" "file://$served" "$1" <<'PY'
import importlib.util
import pathlib
import sys

spec = importlib.util.spec_from_file_location("assemble", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
text = module.sources_file(sys.argv[3], pathlib.Path(sys.argv[2]).read_text(encoding="utf-8"))
pathlib.Path(sys.argv[4]).write_text(text, encoding="utf-8")
PY
}
same_user_files() {
	cmp -s "$1/olivares.sources" "$2/olivares.sources" && cmp -s "$1/olivares.repo" "$2/olivares.repo"
}
# upgrade_is_offered FILE FROM TO: FILE is an apt upgrade simulation (no dpkg) with one upgrade,
# FROM to TO.
upgrade_is_offered() {
	grep -qF "Inst olivares [$2] ($3 " "$1" && grep -q '^1 upgraded' "$1"
}

expect_rc release-1-tree 0 release_tree 26.900
expect_rc user-files-are-published 0 test -s "$served/olivares.sources" -a -s "$served/olivares.repo"
# The tree exactly as the production assembler writes it: the one canonical origin, the
# check accepts it, and a file that differs from what the assembler writes is refused.
expect_rc canonical-user-files-name-the-origin 0 canonical_user_files_ok "$served"
expect_rc user-files-check-accepts-assembled-tree 0 user_files_check "$served"
# Mutant 10: the dnf file gains a line that reopens a package check.
cp -a "$served" "$scratch/served-reopened"
printf 'gpgcheck = 0\n' >>"$scratch/served-reopened/olivares.repo"
expect_rc mutant-check-repo-file-reopens-a-check 1 user_files_check "$scratch/served-reopened"
grep -q 'is not what the assembler writes for https://packages.olivares.ai' "$scratch/mutant-check-repo-file-reopens-a-check.stderr" || {
	printf '%s\n' 'test-package-repositories: HALLAZGO — the reopened repo file was refused for another reason' >&2
	exit 1
}
# Mutant 11: the apt file names a local origin, as a tree assembled for a test would.
cp -a "$served" "$scratch/served-local-origin"
localize_user_files "$scratch/served-local-origin"
expect_rc mutant-check-sources-file-other-origin 1 user_files_check "$scratch/served-local-origin"

# From here the tree is served from its own path, as a host with a local mirror would see it.
expect_rc localize-release-1 0 localize_user_files "$served"
expect_rc user-repo-file-is-closed 0 user_repo_file_ok "$served/olivares.repo" "file://$served"
# Positive control for the next three: a host with the published file updates and sees release 1.
apt_host "$scratch/host-new" "$served/olivares.sources"
expect_rc apt-update-verifies-release-1 0 apt_in "$scratch/host-new" apt-get update
expect_rc apt-candidate-is-release-1 0 apt_candidate_is "$scratch/host-new" 26.900
expect_rc apt-package-is-release-1-asset 0 apt_download_is "$scratch/host-new" "$scratch/rel-26.900-stable/olivares_26.900_linux_amd64.deb"

# Mutant 12: the dnf file loses the package check DNF 4 reads, or has another one appended.
sed '/^gpgcheck=1$/d' "$served/olivares.repo" >"$scratch/repo-no-gpgcheck"
expect_rc mutant-repo-file-without-gpgcheck 1 user_repo_file_ok "$scratch/repo-no-gpgcheck" "file://$served"
{ cat "$served/olivares.repo"; printf 'repo_gpgcheck = 0\n'; } >"$scratch/repo-appended"
expect_rc mutant-repo-file-appended-override 1 user_repo_file_ok "$scratch/repo-appended" "file://$served"
# Mutant 12b: the dnf file fetches its key from the repository address, as it did before #2576.
sed "s#^gpgkey=.*#gpgkey=file://$served/olivares-packages.asc#" "$served/olivares.repo" >"$scratch/repo-key-from-address"
expect_rc mutant-repo-file-key-from-repository-address 1 user_repo_file_ok "$scratch/repo-key-from-address" "file://$served"
# Mutants 12c, 12d: dnf reads gpgkey as a list, so a second line, spaced or continued, adds a key address.
{ cat "$served/olivares.repo"; printf 'gpgkey = file://%s/olivares-packages.asc\n' "$served"; } >"$scratch/repo-second-key"
expect_rc mutant-repo-file-second-gpgkey-line 1 user_repo_file_ok "$scratch/repo-second-key" "file://$served"
{ cat "$served/olivares.repo"; printf '\tfile://%s/olivares-packages.asc\n' "$served"; } >"$scratch/repo-key-continued"
expect_rc mutant-repo-file-gpgkey-continuation 1 user_repo_file_ok "$scratch/repo-key-continued" "file://$served"
# Mutant 13: the apt file carries a key that did not sign the repository.
expect_rc wrong-key-sources 0 wrong_key_sources "$scratch/wrong-key.sources"
apt_host "$scratch/host-wrong-key" "$scratch/wrong-key.sources"
expect_rc mutant-apt-wrong-key 100 apt_in "$scratch/host-wrong-key" apt-get update
grep -q 'NO_PUBKEY\|not signed' "$scratch/mutant-apt-wrong-key.stderr" || {
	printf '%s\n' 'test-package-repositories: HALLAZGO — the wrong-key host was refused for another reason' >&2
	exit 1
}

# Release 2 lands in the same place. A host that holds release 1 sees one upgrade, with the
# files it already has.
apt_host "$scratch/host-old" "$served/olivares.sources" 26.900
expect_rc apt-old-host-updates-release-1 0 apt_in "$scratch/host-old" apt-get update
expect_rc apt-old-host-simulates-upgrade-1 0 apt_in "$scratch/host-old" apt-get -s upgrade
grep -q '^0 upgraded' "$scratch/apt-old-host-simulates-upgrade-1.stdout" || {
	printf '%s\n' 'test-package-repositories: HALLAZGO — a host at the latest release was offered an upgrade' >&2
	exit 1
}
mv -- "$served" "$scratch/served-release-1"
epoch=$((epoch + 60))
valid_until=$((epoch + 31536000))
expect_rc release-2-tree 0 release_tree 26.901
expect_rc user-files-check-accepts-release-2-tree 0 user_files_check "$served"
expect_rc localize-release-2 0 localize_user_files "$served"
expect_rc user-files-same-across-releases 0 same_user_files "$served" "$scratch/served-release-1"
expect_rc apt-old-host-updates-release-2 0 apt_in "$scratch/host-old" apt-get update
expect_rc apt-old-host-simulates-upgrade-2 0 apt_in "$scratch/host-old" apt-get -s upgrade
expect_rc apt-offers-release-1-to-2 0 upgrade_is_offered "$scratch/apt-old-host-simulates-upgrade-2.stdout" 26.900 26.901
expect_rc apt-package-is-release-2-asset 0 apt_download_is "$scratch/host-old" "$scratch/rel-26.901-stable/olivares_26.901_linux_amd64.deb"

# Mutant 14: the signed Packages index changes after signing; apt must refuse the update.
printf 'x' >>"$served/stable/apt/dists/stable/main/binary-amd64/Packages.gz"
apt_host "$scratch/host-tampered" "$served/olivares.sources"
expect_rc mutant-apt-tampered-index 100 apt_in "$scratch/host-tampered" apt-get update
grep -q 'Hash Sum mismatch\|unexpected size' "$scratch/mutant-apt-tampered-index.stderr" || {
	printf '%s\n' 'test-package-repositories: HALLAZGO — the tampered index was refused for another reason' >&2
	exit 1
}

printf 'test-package-repositories: hosts — the assembled tree passes the user-file check; a real apt verified it, was offered release 2 and refused a wrong key and a tampered index\n'
printf 'test-package-repositories: OK — %d checks; 9/9 mutants red with positive controls\n' "$checks"
