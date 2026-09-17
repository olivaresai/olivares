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
	printf '%s\n' 'test-package-repositories: NO HE PODIDO MIRAR — TMPDIR must be an existing absolute directory' >&2
	exit 2
fi
for tool in python3 gpg openssl jq sha256sum diff cp mv; do
	command -v "$tool" >/dev/null 2>&1 || {
		printf 'test-package-repositories: NO HE PODIDO MIRAR — missing %s\n' "$tool" >&2
		exit 2
	}
done

scratch="$(mktemp -d "${TMPDIR:-}/dist24-06.XXXXXX")"
runner_like_tmp=""
unicode_tmp=""
cleanup() {
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
version=26.9.0
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
		--out "$out"
}

verify() {
	local key_dir="$1"
	local assets="$2"
	local channel="$3"
	local repository="$4"
	python3 "$repo_root/scripts/verify-package-repositories.py" \
		--repo-root "$repository" \
		--checksums "$assets/checksums.txt" \
		--artifact-dir "$assets" \
		--version "$version" \
		--channel "$channel" \
		--openpgp-key "$key_dir/openpgp-public.asc" \
		--apk-key "$key_dir/apk-public.pem"
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
	printf '%s\n' 'test-package-repositories: NO HE PODIDO MIRAR — /dev/shm is required for the runner-path control' >&2
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
unicode_tmp="$(mktemp -d "/dev/shm/$(printf 'é%.0s' $(seq 1 20)).XXXXXX")"
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

printf 'test-package-repositories: OK — %d checks; 4/4 mutants red with positive controls\n' "$checks"
