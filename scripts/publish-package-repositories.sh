#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# DIST-24-06 F2 publisher. Dry-run is the default. An applied publication first
# writes and rereads an immutable staging prefix. Promotion requires exact apt,
# rpm and apk evidence; content moves before signed discovery roots. Every
# changed canonical object is locally backed up and rolled back on a partial red.
set -Eeuo pipefail
LC_ALL=C
export LC_ALL

mode=""
bucket=""
tree=""
staging_id=""
evidence_dir=""
apply=0
while [[ "$#" -gt 0 ]]; do
	case "$1" in
	--mode) mode="${2:-}"; shift 2 ;;
	--bucket) bucket="${2:-}"; shift 2 ;;
	--tree) tree="${2:-}"; shift 2 ;;
	--staging-id) staging_id="${2:-}"; shift 2 ;;
	--evidence-dir) evidence_dir="${2:-}"; shift 2 ;;
	--apply) apply=1; shift ;;
	*) printf 'publish-package-repositories: unknown argument: %s\n' "$1" >&2; exit 2 ;;
	esac
done

blind() { printf 'publish-package-repositories: NO HE PODIDO MIRAR — %s\n' "$*" >&2; exit 2; }
fail() { printf 'publish-package-repositories: HALLAZGO — %s\n' "$*" >&2; exit 1; }

case "$mode" in stage | verify-stage | promote) ;; *) blind '--mode must be stage, verify-stage or promote' ;; esac
case "$bucket" in olivares-packages | olivares-packages-sandbox) ;; *) blind 'bucket is outside the reviewed production/sandbox pair' ;; esac
[[ "$tree" == /* && -d "$tree" && ! -L "$tree" ]] || blind '--tree must be an existing absolute directory'
[[ "$staging_id" =~ ^run-[0-9]+-attempt-[0-9]+$ ]] || blind '--staging-id must be run-N-attempt-N'
tmp_root="${TMPDIR:-}"
[[ "$tmp_root" == /* && -d "$tmp_root" ]] || blind 'TMPDIR must be an existing absolute directory'
for tool in awk cat chmod cmp dirname find grep mkdir mktemp rm sha256sum sort tac; do
	command -v "$tool" >/dev/null 2>&1 || blind "missing required tool: $tool"
done

non_regular="$(find "$tree" -mindepth 1 ! -type d ! -type f -print -quit)"
if [[ -n "$non_regular" ]]; then
	fail 'publish tree contains a link, device or other non-regular entry'
fi
mapfile -d '' files < <(find "$tree" -type f -print0 | sort -z)
[[ "${#files[@]}" -gt 0 ]] || blind 'publish tree contains zero files'
declare -a relatives=() content_files=() root_files=()
for file in "${files[@]}"; do
	rel="${file#"$tree"/}"
	[[ "$rel" != "$file" && "$rel" != /* && "$rel" != *..* && "$rel" =~ ^[A-Za-z0-9._/+~-]+$ ]] || \
		fail "unsafe publish-tree path: $rel"
	[[ -s "$file" ]] || fail "zero-byte repository object: $rel"
	relatives+=("$rel")
	case "$rel" in
	*/apt/dists/*/InRelease | */apt/dists/*/Release | */apt/dists/*/Release.gpg | \
		*/rpm/*/repodata/repomd.xml | */rpm/*/repodata/repomd.xml.asc | \
		*/apk/*/APKINDEX.tar.gz | */repository-manifest.json | */repository-manifest.json.asc)
		root_files+=("$rel") ;;
	*) content_files+=("$rel") ;;
	esac
done

required=(
	olivares-packages.gpg
	keys/olivares-package-repository.asc
	keys/olivares-packages-apk.rsa.pub
	stable/apt/dists/stable/InRelease
	stable/rpm/x86_64/repodata/repomd.xml
	stable/rpm/aarch64/repodata/repomd.xml
	stable/apk/x86_64/APKINDEX.tar.gz
	stable/apk/aarch64/APKINDEX.tar.gz
	stable/repository-manifest.json
	security/apt/dists/security/InRelease
	security/rpm/x86_64/repodata/repomd.xml
	security/rpm/aarch64/repodata/repomd.xml
	security/apk/x86_64/APKINDEX.tar.gz
	security/apk/aarch64/APKINDEX.tar.gz
	security/repository-manifest.json
)
for rel in "${required[@]}"; do
	[[ -f "$tree/$rel" && ! -L "$tree/$rel" ]] || fail "publish tree lacks required object: $rel"
done

cleanup() {
	case "$scratch" in "$tmp_root"/package-publish.*) rm -rf -- "$scratch" ;; *) ;; esac
}
scratch="$(mktemp -d "$tmp_root/package-publish.XXXXXX")" || blind 'cannot allocate scratch'; trap cleanup EXIT
chmod 0700 "$scratch"
inventory="$scratch/inventory.sha256"
for rel in "${relatives[@]}"; do
	printf '%s  %s\n' "$(sha256sum "$tree/$rel" | awk '{print $1}')" "$rel" >>"$inventory"
done

printf 'publish-package-repositories: plan mode=%s bucket=%s staging=%s files=%d roots=%d\n' \
	"$mode" "$bucket" "$staging_id" "${#relatives[@]}" "${#root_files[@]}"
if [[ "$apply" -eq 0 ]]; then
	printf '%s\n' 'publish-package-repositories: DRY-RUN — no R2 object was read, written or deleted'
	exit 0
fi

[[ "${OLIVARES_PACKAGE_PUBLISH_APPROVED:-}" == 1 ]] || blind 'apply requires OLIVARES_PACKAGE_PUBLISH_APPROVED=1 from the reviewed environment job'
[[ -n "${CLOUDFLARE_API_TOKEN:-}" ]] || blind 'CLOUDFLARE_API_TOKEN is absent'
[[ "${CLOUDFLARE_ACCOUNT_ID:-}" =~ ^[0-9a-f]{32}$ ]] || blind 'CLOUDFLARE_ACCOUNT_ID is absent or malformed'
wrangler_bin="${OLIVARES_WRANGLER_BIN:-}"
if [[ -z "$wrangler_bin" ]]; then wrangler_bin="$(command -v wrangler || true)"; fi
[[ "$wrangler_bin" == /* && -x "$wrangler_bin" ]] || blind 'wrangler is not an absolute executable'
wrangler_version="$($wrangler_bin --version 2>/dev/null | awk 'NR==1{print $1}')"
[[ "$wrangler_version" == 4.100.0 ]] || blind "wrangler version is $wrangler_version, want reviewed 4.100.0"

content_type() {
	case "$1" in
	*.json) printf '%s\n' application/json ;;
	*.gz | *.tgz) printf '%s\n' application/gzip ;;
	*.xml | *.repo) printf '%s\n' application/xml ;;
	*.asc | *.gpg | *.pub | */InRelease) printf '%s\n' application/pgp-keys ;;
	*.deb | *.rpm | *.apk) printf '%s\n' application/octet-stream ;;
	*) printf '%s\n' text/plain ;;
	esac
}

put_object() {
	local key="$1" source="$2" cache="$3"
	"$wrangler_bin" r2 object put "$bucket/$key" --remote --force --file "$source" \
		--content-type "$(content_type "$key")" --cache-control "$cache" >/dev/null
}
get_object() {
	local key="$1" dest="$2"
	"$wrangler_bin" r2 object get "$bucket/$key" --remote --file "$dest" >/dev/null
}
delete_object() {
	local key="$1"
	"$wrangler_bin" r2 object delete "$bucket/$key" --remote --force >/dev/null
}
is_immutable_key() {
	case "$1" in
	*.deb | *.rpm | *.apk | keys/* | olivares-packages.*) return 0 ;;
	*) return 1 ;;
	esac
}
cache_for_key() {
	if is_immutable_key "$1"; then
		printf '%s\n' 'public, max-age=31536000, immutable'
	else
		# Signed roots and every file they authenticate must be observed from R2
		# together. A CDN-cached old Packages/APKINDEX/repodata object would make
		# a correct roots-last promotion look corrupt to the first clean client.
		printf '%s\n' 'private, no-store'
	fi
}
put_and_verify() {
	local key="$1" source="$2" cache="$3" check="$scratch/readback"
	rm -f -- "$check"
	put_object "$key" "$source" "$cache" || return 1
	get_object "$key" "$check" || return 1
	cmp -s "$source" "$check" || {
		printf 'publish-package-repositories: HALLAZGO — remote digest differs after put: %s\n' "$key" >&2
		return 1
	}
}

stage_prefix="staging/$staging_id"
verify_stage() {
	local rel check="$scratch/stage-readback"
	for rel in "${relatives[@]}"; do
		rm -f -- "$check"
		get_object "$stage_prefix/$rel" "$check" || return 1
		cmp -s "$tree/$rel" "$check" || {
			printf 'publish-package-repositories: HALLAZGO — staged digest differs: %s\n' "$rel" >&2
			return 1
		}
	done
	rm -f -- "$check"
	get_object "$stage_prefix/.inventory.sha256" "$check" || return 1
	cmp -s "$inventory" "$check" || return 1
}

if [[ "$mode" == stage ]]; then
	for rel in "${relatives[@]}"; do
		put_and_verify "$stage_prefix/$rel" "$tree/$rel" 'private, no-store' || \
			fail "staging put/readback failed: $rel"
	done
	put_and_verify "$stage_prefix/.inventory.sha256" "$inventory" 'private, no-store' || \
		fail 'staging inventory put/readback failed'
	printf 'publish-package-repositories: STAGED — %d objects under %s and every byte reread\n' \
		"${#relatives[@]}" "$stage_prefix"
	exit 0
fi

if ! verify_stage; then fail 'remote staging prefix is absent or differs from the reviewed tree'; fi
if [[ "$mode" == verify-stage ]]; then
	printf 'publish-package-repositories: VERIFIED — %d staged objects and exact inventory\n' "${#relatives[@]}"
	exit 0
fi

[[ "$evidence_dir" == /* && -d "$evidence_dir" && ! -L "$evidence_dir" ]] || blind 'promote requires an absolute evidence directory'
for family in apt rpm apk; do
	evidence="$evidence_dir/$family.ok"
	[[ -f "$evidence" && ! -L "$evidence" ]] || fail "missing clean-client evidence: $family.ok"
	[[ "$(cat "$evidence")" == "$staging_id" ]] || fail "clean-client evidence is not bound to $staging_id: $family.ok"
done

mkdir -m 0700 "$scratch/backups"
changed="$scratch/changed.tsv"
: >"$changed"
promoting=1
rollback() {
	local original_rc="$1" state rel backup rollback_failed=0
	[[ "$promoting" -eq 1 ]] || exit "$original_rc"
	set +e
	printf '%s\n' 'publish-package-repositories: partial promotion failed; rolling canonical objects back' >&2
	while IFS=$'\t' read -r state rel; do
		[[ -n "$rel" ]] || continue
		backup="$scratch/backups/$rel"
		if [[ "$state" == present ]]; then
			put_object "$rel" "$backup" "$(cache_for_key "$rel")" || rollback_failed=1
		else
			delete_object "$rel" || rollback_failed=1
		fi
	done < <(tac "$changed")
	if [[ "$rollback_failed" -ne 0 ]]; then
		printf 'publish-package-repositories: HALLAZGO — rollback incomplete; staging source remains at %s\n' "$stage_prefix" >&2
	fi
	exit "$original_rc"
}
trap 'rollback $?' ERR

promote_one() {
	local rel="$1" source="${2:-$tree/$1}" backup="$scratch/backups/$1" err_file="$scratch/get.err" rc state cache
	mkdir -p "$(dirname "$backup")"
	rm -f -- "$backup" "$err_file"
	if get_object "$rel" "$backup" 2>"$err_file"; then
		rc=0
		state=present
	else
		rc=$?
		if grep -Eiq '10007|does not exist|not found' "$err_file"; then
			state=absent
		else
			printf 'publish-package-repositories: NO HE PODIDO MIRAR — cannot distinguish absent canonical object from a failed read (rc=%d): %s\n' \
				"$rc" "$rel" >&2
			return 2
		fi
	fi
	if [[ "$state" == present ]] && is_immutable_key "$rel"; then
		if cmp -s "$backup" "$source"; then
			printf 'publish-package-repositories: immutable canonical object already matches: %s\n' "$rel"
			return 0
		fi
		printf 'publish-package-repositories: HALLAZGO — immutable canonical object differs; refusing overwrite: %s\n' \
			"$rel" >&2
		return 1
	fi
	printf '%s\t%s\n' "$state" "$rel" >>"$changed"
	cache="$(cache_for_key "$rel")"
	put_and_verify "$rel" "$source" "$cache"
}

for rel in "${content_files[@]}"; do promote_one "$rel"; done
for rel in "${root_files[@]}"; do promote_one "$rel"; done
promote_one .inventory.sha256 "$inventory"
promoting=0
trap - ERR
printf 'publish-package-repositories: PROMOTED — %d content objects then %d signed discovery roots; rollback set retained at %s\n' \
	"${#content_files[@]}" "${#root_files[@]}" "$stage_prefix"
