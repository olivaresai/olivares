#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# publish-appliance-images.sh — publish appliance image files that do not fit a GitHub
# release asset to the public R2 route, object by object, discovery last.
#
# GitHub caps one release file at 2 GiB (docs.github.com, About releases), which the image
# formats declare as release_asset_max_bytes; a file over it cannot be a release asset, so it
# travels to R2 bucket `olivares-appliance` (account pinned by the caller workflow) under an
# IMMUTABLE versioned prefix:
#
#     appliance/<version>/<file>          the images and everything they ship with
#     appliance/<version>/index.json      written LAST, after every object reads back
#
# The discipline mirrors publish-package-repositories.sh with one honest difference, said
# here rather than papered over: the package route can point clean apt/rpm/apk CLIENTS at
# its staging prefix before promotion, and requires their evidence to promote. An appliance
# image has no installable client in CI, so the gate that stands where that evidence stands
# is the cosign-verified SHA256SUMS the caller already verified over these exact bytes, plus
# a byte-for-byte readback of every object and the index written only after all of them.
#
#   usage: publish-appliance-images.sh --version X.Y.Z --dir DIR --origin URL [--run-id ID]
#   DIR holds the public files (safe basenames, non-empty). Every object already present with
#   DIFFERENT bytes is refused — an immutable prefix is never overwritten. --origin is the
#   bucket's public custom domain; each file is fetched back through it and digest-compared
#   before the script reports success.
#
#   exit 0  every object reads back identical and the origin serves the same bytes
#   exit 1  refused: unsafe input, an immutable object that differs, a readback mismatch the
#           rollback could not undo, or the origin serving bytes other than the verified ones
#   exit 2  could not run: missing tool, env or input
set -Eeuo pipefail
LC_ALL=C
export LC_ALL=C

version="" dir="" origin="" run_id=""
while [[ "$#" -gt 0 ]]; do
	case "$1" in
	--version) version="${2:?}" ; shift 2 ;;
	--dir) dir="${2:?}" ; shift 2 ;;
	--origin) origin="${2:?}" ; shift 2 ;;
	--run-id) run_id="${2:?}" ; shift 2 ;;
	*) printf 'publish-appliance-images: unknown argument: %s\n' "$1" >&2; exit 2 ;;
	esac
done

blind() { printf 'publish-appliance-images: UNABLE TO LOOK — %s\n' "$*" >&2; exit 2; }
fail() { printf 'publish-appliance-images: REFUSED — %s\n' "$*" >&2; exit 1; }

[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || blind '--version must be X.Y.Z'
[[ "$dir" == /* && -d "$dir" && ! -L "$dir" ]] || blind '--dir must be an existing absolute directory'
[[ "$origin" == https://* ]] || blind '--origin must be an https:// URL (the public custom domain of the bucket)'
origin="${origin%/}"
for tool in awk cat cmp curl find jq mkdir mktemp rm sha256sum sort tac; do
	command -v "$tool" >/dev/null 2>&1 || blind "missing required tool: $tool"
done
tmp_root="${TMPDIR:-}"
[[ "$tmp_root" == /* && -d "$tmp_root" ]] || blind 'TMPDIR must be an existing absolute directory'
scratch="$(mktemp -d "$tmp_root/appliance-publish.XXXXXX")" || blind 'cannot allocate scratch'
cleanup() { rm -rf -- "$scratch"; }
trap cleanup EXIT
chmod 0700 "$scratch"

[[ "${OLIVARES_APPLIANCE_PUBLISH_APPROVED:-}" == 1 ]] ||
	blind 'apply requires OLIVARES_APPLIANCE_PUBLISH_APPROVED=1 from the reviewed environment job'
[[ -n "${CLOUDFLARE_API_TOKEN:-}" ]] || blind 'CLOUDFLARE_API_TOKEN is absent'
[[ "${CLOUDFLARE_ACCOUNT_ID:-}" =~ ^[0-9a-f]{32}$ ]] || blind 'CLOUDFLARE_ACCOUNT_ID is absent or malformed'
bucket=olivares-appliance
wrangler_bin="${OLIVARES_WRANGLER_BIN:-}"
if [[ -z "$wrangler_bin" ]]; then wrangler_bin="$(command -v wrangler || true)"; fi
[[ "$wrangler_bin" == /* && -x "$wrangler_bin" ]] || blind 'wrangler is not an absolute executable'
wrangler_version="$($wrangler_bin --version 2>/dev/null | awk 'NR==1{print $1}')"
[[ "$wrangler_version" == 4.100.0 ]] || blind "wrangler version is $wrangler_version, want reviewed 4.100.0"

# ONE REQUEST CANNOT CARRY ~5 GiB: R2 caps a single PUT at 5 GiB less 5 MiB (developers.
# cloudflare.com, R2 platform limits), and wrangler r2 object put performs one. A larger
# image needs the multipart follow-up; refusing here names it instead of truncating.
# shellcheck disable=SC2034
R2_SINGLE_PUT_MAX_BYTES=5363466240

# The public files: safe basenames, non-empty, no index of their own (this script writes it).
mapfile -d '' files < <(find "$dir" -maxdepth 1 -type f -print0 | sort -z)
[[ "${#files[@]}" -gt 0 ]] || blind 'the directory holds no files'
index_key="appliance/${version}/index.json"
names=()
for file in "${files[@]}"; do
	name="${file#"$dir"/}"
	[[ "$name" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]*$ ]] || fail "unsafe appliance file name: '$name'"
	[[ -s "$file" ]] || fail "empty appliance file: $name"
	[[ "$name" != "$(basename "$index_key")" ]] || fail "the directory already carries an index.json; this script writes it"
	size=$(wc -c <"$file" | tr -d ' ')
	[[ "$size" -lt "$R2_SINGLE_PUT_MAX_BYTES" ]] ||
		fail "$name is $size bytes, over the ${R2_SINGLE_PUT_MAX_BYTES}-byte single-PUT cap: it needs the multipart follow-up, not a truncated upload"
	names+=("$name")
done

content_type() {
	case "$1" in
	*.iso) printf '%s\n' application/x-iso9660-image ;;
	*.qcow2) printf '%s\n' application/octet-stream ;;
	*.ova) printf '%s\n' application/octet-stream ;;
	*.json) printf '%s\n' application/json ;;
	*.sig) printf '%s\n' application/pgp-signature ;;
	*.pem) printf '%s\n' application/x-pem-file ;;
	*.zst) printf '%s\n' application/zstd ;;
	*.txt | *SHA256SUMS) printf '%s\n' text/plain ;;
	*) printf '%s\n' application/octet-stream ;;
	esac
}

put_object() {
	local key="$1" source="$2"
	"$wrangler_bin" r2 object put "$bucket/$key" --remote --force --file "$source" \
		--content-type "$(content_type "$key")" --cache-control 'public, max-age=31536000, immutable' >/dev/null
}
get_object() {
	local key="$1" dest="$2"
	"$wrangler_bin" r2 object get "$bucket/$key" --remote --file "$dest" >/dev/null
}
delete_object() {
	local key="$1"
	"$wrangler_bin" r2 object delete "$bucket/$key" --remote --force >/dev/null
}

# Immutable-prefix discipline: an object that already exists with the SAME bytes is already
# delivered; one that exists with DIFFERENT bytes is a refused overwrite, never a clobber.
# Everything this run CREATES is recorded, and a later failure removes exactly that set.
created="$scratch/created.tsv"
: >"$created"
promoting=1
rollback() {
	local original_rc="$1" key
	[[ "$promoting" -eq 1 ]] || exit "$original_rc"
	set +e
	printf '%s\n' 'publish-appliance-images: partial publication failed; removing the objects this run created' >&2
	while IFS= read -r key; do
		[[ -n "$key" ]] || continue
		delete_object "$key" || printf 'publish-appliance-images: could not remove %s; inspect the bucket\n' "$key" >&2
	done < <(tac "$created")
	exit "$original_rc"
}
trap 'rollback $?' ERR

digest_of() { sha256sum "$1" | awk '{print $1}'; }

promote_one() { # promote_one <name>
	local name="$1" key="appliance/${version}/$1" source="$dir/$1" check="$scratch/readback" err_file="$scratch/get.err" rc state
	rm -f -- "$check" "$err_file"
	if get_object "$key" "$check" 2>"$err_file"; then
		rc=0
		state=present
	else
		rc=$?
		if grep -Eiq '10007|does not exist|not found' "$err_file"; then
			state=absent
		else
			printf 'publish-appliance-images: UNABLE TO LOOK — cannot distinguish an absent object from a failed read (rc=%d): %s\n' \
				"$rc" "$key" >&2
			return 2
		fi
	fi
	if [[ "$state" == present ]]; then
		if cmp -s "$source" "$check"; then
			printf 'publish-appliance-images: immutable object already matches: %s\n' "$key"
			return 0
		fi
		printf 'publish-appliance-images: REFUSED — immutable object differs; refusing overwrite: %s\n' "$key" >&2
		return 1
	fi
	printf '%s\n' "$key" >>"$created"
	put_object "$key" "$source"
	rm -f -- "$check"
	get_object "$key" "$check"
	cmp -s "$source" "$check" || {
		printf 'publish-appliance-images: REFUSED — remote digest differs after put: %s\n' "$key" >&2
		return 1
	}
}

for name in "${names[@]}"; do promote_one "$name"; done

# THE INDEX LAST, after every object it names reads back. It is the only mutable-shaped
# object of the prefix, and it is what a consumer reads to find everything else.
index="$scratch/index.json"
{
	printf '{"schema":"olivares.ai/appliance-index/v1","version":"%s","origin_run":"%s","files":[' "$version" "${run_id:-unknown}"
	first=1
	for name in "${names[@]}"; do
		[ "$first" -eq 1 ] || printf ','
		first=0
		printf '{"name":"%s","size":%s,"sha256":"%s"}' "$name" "$(wc -c <"$dir/$name" | tr -d ' ')" "$(digest_of "$dir/$name")"
	done
	printf ']}\n'
} >"$index"
jq -e '.schema == "olivares.ai/appliance-index/v1" and (.files | type == "array" and length > 0)' "$index" >/dev/null ||
	blind 'the assembled index does not parse as the schema it declares'
printf '%s\n' "$index_key" >>"$created"
put_object "$index_key" "$index"
check="$scratch/index-readback"
get_object "$index_key" "$check"
cmp -s "$index" "$check" || fail "the published index differs from the verified one: $index_key"
promoting=0
trap - ERR

# PUBLIC DELIVERY, the postcondition: the origin must serve the verified bytes.
for name in "${names[@]}" "$(basename "$index_key")"; do
	rm -f -- "$scratch/origin-$name"
	curl --fail --silent --show-error --max-time 600 --output "$scratch/origin-$name" "$origin/appliance/${version}/$name" ||
		fail "public delivery failed for appliance/${version}/$name (origin $origin)"
	cmp -s "$dir/$name" "$scratch/origin-$name" 2>/dev/null || cmp -s "$index" "$scratch/origin-$name" ||
		fail "the origin serves bytes other than the verified ones for $name"
done
printf 'publish-appliance-images: PUBLISHED — %d objects under appliance/%s and the index last, every byte read back and served\n' \
	"${#names[@]}" "$version"
