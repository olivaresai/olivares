#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# DIST-24-06 F2 publisher. Dry-run is the default. An applied publication first
# writes and rereads an immutable staging prefix. Promotion requires exact apt,
# rpm and apk evidence; content moves before signed discovery roots. Every
# changed canonical object is locally backed up and rolled back on a partial red.
#
# pacman (Arch Linux). `--mode pacman-render` builds <channel>/pacman/x86_64 for
# stable and security from the release's olivares_<version>_linux_amd64.pkg.tar.zst
# with repo-add(8), signs the package and both databases with detached binary
# OpenPGP signatures, and writes the verifying key to
# keys/olivares-pacman-repository.gpg. The signing input names the key:
#   OLIVARES_PACMAN_SIGNING_KEY_FILE         armored secret key, no group/other access
#   OLIVARES_PACMAN_SIGNING_FINGERPRINT      40-hex primary fingerprint to sign with
#   OLIVARES_PACMAN_SIGNING_PASSPHRASE_FILE  optional, no group/other access
#   OLIVARES_REPO_ADD_BIN                    optional absolute repo-add path
# The key is anchored outside the tree: every mode that meets a pacman object needs
#   OLIVARES_PACMAN_EXPECTED_FINGERPRINT     40-hex primary fingerprint the workflow reads
#                                            from a tracked descriptor
# and refuses a key file holding anything else (exit 1), or exits 2 without it. Render
# signs only with that key.
# pacman fetches <repo>.db and <repo>.files; repo-add writes them as links to
# .tar.gz files, and this tree admits no link, so the tree carries the link targets'
# bytes under the fetched names. A tree that carries any pacman object must carry
# the whole family, every signature must verify with the tree's pacman key, each
# database must describe the package bytes beside it, and promotion then also
# requires pacman.ok. These checks run before any R2 read or write, dry run included.
set -Eeuo pipefail
LC_ALL=C
export LC_ALL

mode=""
bucket=""
tree=""
staging_id=""
evidence_dir=""
pacman_packages=""
release_version=""
source_date_epoch=""
apply=0
while [[ "$#" -gt 0 ]]; do
	case "$1" in
	--mode) mode="${2:-}"; shift 2 ;;
	--bucket) bucket="${2:-}"; shift 2 ;;
	--tree) tree="${2:-}"; shift 2 ;;
	--staging-id) staging_id="${2:-}"; shift 2 ;;
	--evidence-dir) evidence_dir="${2:-}"; shift 2 ;;
	--pacman-packages) pacman_packages="${2:-}"; shift 2 ;;
	--version) release_version="${2:-}"; shift 2 ;;
	--source-date-epoch) source_date_epoch="${2:-}"; shift 2 ;;
	--apply) apply=1; shift ;;
	*) printf 'publish-package-repositories: unknown argument: %s\n' "$1" >&2; exit 2 ;;
	esac
done

blind() { printf 'publish-package-repositories: NO HE PODIDO MIRAR — %s\n' "$*" >&2; exit 2; }
fail() { printf 'publish-package-repositories: HALLAZGO — %s\n' "$*" >&2; exit 1; }

case "$mode" in
stage | verify-stage | promote | pacman-render) ;;
*) blind '--mode must be stage, verify-stage, promote or pacman-render' ;;
esac
[[ "$tree" == /* && -d "$tree" && ! -L "$tree" ]] || blind '--tree must be an existing absolute directory'
tmp_root="${TMPDIR:-}"
[[ "$tmp_root" == /* && -d "$tmp_root" ]] || blind 'TMPDIR must be an existing absolute directory'
for tool in awk cat chmod cmp dirname find grep mkdir mktemp rm sha256sum sort tac; do
	command -v "$tool" >/dev/null 2>&1 || blind "missing required tool: $tool"
done

cleanup() {
	if [[ -d "$scratch/g" ]]; then gpgconf --homedir "$scratch/g" --kill all >/dev/null 2>&1 || true; fi
	case "$scratch" in "$tmp_root"/package-publish.*) rm -rf -- "$scratch" ;; *) ;; esac
}
scratch="$(mktemp -d "$tmp_root/package-publish.XXXXXX")" || blind 'cannot allocate scratch'; trap cleanup EXIT
chmod 0700 "$scratch"

pacman_key_rel=keys/olivares-pacman-repository.gpg
pacman_channels=(stable security)
pacman_object_re='^(stable|security)/pacman/x86_64/(olivares\.(db|files)(\.sig)?|olivares_[0-9]+\.[0-9]+\.[0-9]+_linux_amd64\.pkg\.tar\.zst(\.sig)?)$'

# pacman_key_primaries KEYRING: the primary fingerprints of the keys in KEYRING, one per
# line, read without importing them anywhere.
pacman_key_primaries() {
	local home
	home="$(mktemp -d "$scratch/k.XXXXXX")" || blind 'cannot allocate a key listing directory'
	gpg --homedir "$home" --batch --with-colons --import-options show-only --import "$1" 2>/dev/null |
		awk -F: '$1 == "pub" {primary = 1; next} $1 == "fpr" && primary {print $10} {primary = 0}'
}

# pacman_verify_tree: every pacman object of $tree is expected, signed by the key at
# $pacman_key_rel, and each database describes exactly the package bytes beside it.
pacman_verify_tree() {
	local rel channel repo root file tool keyring="$tree/$pacman_key_rel" expect held
	local -a pkgs=()
	for tool in gpg gpgv python3; do
		command -v "$tool" >/dev/null 2>&1 || blind "missing required tool for pacman: $tool"
	done
	expect="${OLIVARES_PACMAN_EXPECTED_FINGERPRINT:-}"
	[[ "$expect" =~ ^[0-9A-F]{40}$ ]] ||
		blind 'OLIVARES_PACMAN_EXPECTED_FINGERPRINT is absent or not an uppercase 40-hex fingerprint; pacman objects cannot be checked without it'
	while IFS= read -r -d '' file; do
		rel="${file#"$tree"/}"
		[[ "$rel" =~ $pacman_object_re ]] || fail "unexpected pacman object: $rel"
	done < <(find "$tree" \( -path "$tree/pacman/*" -o -path "$tree/*/pacman/*" \) ! -type d -print0 | sort -z)
	[[ -f "$keyring" && ! -L "$keyring" && -s "$keyring" ]] || fail "publish tree lacks required object: $pacman_key_rel"
	# The key that verifies everything below must be the anchored key and nothing else:
	# a tree whose key file and signatures were all replaced together is refused here.
	held="$(pacman_key_primaries "$keyring" | tr '\n' ' ' | sed 's/ $//')"
	[[ "$held" == "$expect" ]] ||
		fail "pacman key $pacman_key_rel is not the expected key $expect (it holds: ${held:-no readable key})"
	for channel in "${pacman_channels[@]}"; do
		repo="$channel/pacman/x86_64"
		# The .sig roots are required by the signature loop below, which names them.
		for root in olivares.db olivares.files; do
			[[ -f "$tree/$repo/$root" ]] || fail "publish tree lacks required object: $repo/$root"
		done
		mapfile -t pkgs < <(find "$tree/$repo" -maxdepth 1 -type f -name '*.pkg.tar.zst' -printf '%f\n' | sort)
		[[ "${#pkgs[@]}" -eq 1 ]] || fail "pacman repository must hold exactly one package: $repo"
		for file in olivares.db olivares.files "${pkgs[0]}"; do
			if [[ ! -f "$tree/$repo/$file.sig" ]]; then
				case "$file" in
				*.pkg.tar.zst) fail "pacman package is unsigned: $repo/$file" ;;
				*) fail "pacman database is unsigned: $repo/$file" ;;
				esac
			fi
			gpgv --keyring "$keyring" "$tree/$repo/$file.sig" "$tree/$repo/$file" >/dev/null 2>&1 ||
				fail "signature does not verify with $pacman_key_rel: $repo/$file"
		done
		python3 - "$tree/$repo" "${pkgs[0]}" <<'PY' ||
import hashlib
import sys
import tarfile

repo, package = sys.argv[1], sys.argv[2]
digest = hashlib.sha256(open(f"{repo}/{package}", "rb").read()).hexdigest()
entries = {}
for db in ("olivares.db", "olivares.files"):
    try:
        with tarfile.open(f"{repo}/{db}", "r:*") as archive:
            descs = [m for m in archive.getmembers() if m.isfile() and m.name.endswith("/desc")]
            if len(descs) != 1:
                sys.exit(1)
            text = archive.extractfile(descs[0]).read().decode("utf-8")
    except (OSError, tarfile.TarError, UnicodeError):
        sys.exit(1)
    fields, key = {}, None
    for line in text.splitlines():
        if len(line) > 2 and line.startswith("%") and line.endswith("%"):
            key = line.strip("%")
            fields[key] = []
        elif line and key:
            fields[key].append(line)
    entries[db] = (descs[0].name, fields)
name, fields = entries["olivares.db"]
if entries["olivares.files"][0] != name:
    sys.exit(1)
want = ([package], ["olivares"], ["x86_64"], [digest])
got = (fields.get("FILENAME"), fields.get("NAME"), fields.get("ARCH"), fields.get("SHA256SUM"))
sys.exit(0 if got == want else 1)
PY
			fail "pacman database does not describe the package bytes beside it: $repo/olivares.db"
	done
}

# pacman_sign FILE: FILE.sig, a detached binary signature by the signing fingerprint,
# dated at the source date so that the same inputs and key give the same bytes.
pacman_sign() {
	gpg --homedir "$scratch/g" --batch --yes "${pacman_pass_args[@]}" \
		--faked-system-time "${source_date_epoch}!" --local-user "${pacman_fpr}!" \
		--digest-algo SHA256 --no-armor --detach-sign --output "$1.sig" "$1" >/dev/null 2>&1 ||
		blind "signing failed: ${1#"$tree"/}"
}

# pacman_render: build and sign the stable and security pacman repositories in $tree.
pacman_render() {
	local repo_add="${OLIVARES_REPO_ADD_BIN:-}" key_file="${OLIVARES_PACMAN_SIGNING_KEY_FILE:-}"
	local pass_file="${OLIVARES_PACMAN_SIGNING_PASSPHRASE_FILE:-}" package channel repo name input bits tool
	pacman_fpr="${OLIVARES_PACMAN_SIGNING_FINGERPRINT:-}"
	[[ "$pacman_packages" == /* && -d "$pacman_packages" && ! -L "$pacman_packages" ]] ||
		blind '--pacman-packages must be an existing absolute directory'
	[[ "$release_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || blind '--version must be MAJOR.MINOR.PATCH'
	[[ "$source_date_epoch" =~ ^[0-9]+$ ]] || blind '--source-date-epoch must be a Unix time'
	if [[ -z "$repo_add" ]]; then repo_add="$(command -v repo-add || true)"; fi
	[[ "$repo_add" == /* && -x "$repo_add" ]] || blind 'repo-add is not an absolute executable'
	for tool in cp gpg gpgconf mv stat; do
		command -v "$tool" >/dev/null 2>&1 || blind "missing required tool for pacman: $tool"
	done
	[[ "$pacman_fpr" =~ ^[0-9A-F]{40}$ ]] || blind 'OLIVARES_PACMAN_SIGNING_FINGERPRINT must be an uppercase 40-hex fingerprint'
	[[ "${OLIVARES_PACMAN_EXPECTED_FINGERPRINT:-}" =~ ^[0-9A-F]{40}$ ]] ||
		blind 'OLIVARES_PACMAN_EXPECTED_FINGERPRINT is absent or not an uppercase 40-hex fingerprint'
	[[ "$pacman_fpr" == "$OLIVARES_PACMAN_EXPECTED_FINGERPRINT" ]] ||
		fail "signing fingerprint $pacman_fpr is not the expected key $OLIVARES_PACMAN_EXPECTED_FINGERPRINT"
	[[ -n "$key_file" ]] || blind 'OLIVARES_PACMAN_SIGNING_KEY_FILE is absent'
	for input in "$key_file" "$pass_file"; do
		[[ -n "$input" ]] || continue
		[[ "$input" == /* && -f "$input" && ! -L "$input" ]] ||
			blind "signing input is missing, not a regular file, or a link: $input"
		bits="$(stat -c '%a' "$input")"
		(((8#$bits & 8#077) == 0)) || fail "signing input has group or other access (mode $bits): $input"
	done
	package="olivares_${release_version}_linux_amd64.pkg.tar.zst"
	[[ -f "$pacman_packages/$package" && ! -L "$pacman_packages/$package" && -s "$pacman_packages/$package" ]] ||
		fail "release package is absent: $package"
	for channel in "${pacman_channels[@]}"; do
		[[ ! -e "$tree/$channel/pacman" ]] || fail "refusing to overwrite an existing pacman tree: $channel/pacman"
	done

	mkdir -m 0700 "$scratch/g"
	gpg --homedir "$scratch/g" --batch --import "$key_file" >/dev/null 2>&1 || blind 'signing key import failed'
	gpg --homedir "$scratch/g" --batch --with-colons --list-secret-keys 2>/dev/null |
		awk -F: -v want="$pacman_fpr" '$1 == "fpr" && $10 == want {found = 1} END {exit !found}' ||
		fail "signing key file does not hold the signing fingerprint $pacman_fpr"
	pacman_pass_args=(--pinentry-mode loopback --passphrase '')
	if [[ -n "$pass_file" ]]; then pacman_pass_args=(--pinentry-mode loopback --passphrase-file "$pass_file"); fi
	gpg --homedir "$scratch/g" --batch --export-options export-minimal --export "$pacman_fpr" \
		>"$scratch/pacman-key.gpg" 2>/dev/null || blind 'public key export failed'
	[[ -s "$scratch/pacman-key.gpg" ]] || blind 'public key export is empty'
	if [[ -e "$tree/$pacman_key_rel" ]]; then
		cmp -s "$scratch/pacman-key.gpg" "$tree/$pacman_key_rel" || fail "a different key already sits at $pacman_key_rel"
	else
		mkdir -p "$tree/keys"
		cp "$scratch/pacman-key.gpg" "$tree/$pacman_key_rel"
	fi

	for channel in "${pacman_channels[@]}"; do
		repo="$tree/$channel/pacman/x86_64"
		mkdir -p "$repo"
		cp "$pacman_packages/$package" "$repo/$package"
		pacman_sign "$repo/$package"
		(cd "$repo" && "$repo_add" -q olivares.db.tar.gz "$package") >/dev/null || fail "repo-add failed for $channel"
		for name in olivares.db olivares.files; do
			[[ -L "$repo/$name" && -f "$repo/$name.tar.gz" && ! -L "$repo/$name.tar.gz" ]] ||
				fail "repo-add did not write $name as a link to $name.tar.gz in $channel"
			# Replace the link with its target's bytes under the name pacman fetches.
			mv -f -- "$repo/$name.tar.gz" "$repo/$name"
			pacman_sign "$repo/$name"
		done
	done
	pacman_verify_tree
	printf 'publish-package-repositories: PACMAN-RENDERED — %s for stable and security x86_64, signed by %s\n' \
		"$package" "$pacman_fpr"
}

if [[ "$mode" == pacman-render ]]; then
	pacman_render
	exit 0
fi

case "$bucket" in olivares-packages | olivares-packages-sandbox) ;; *) blind 'bucket is outside the reviewed production/sandbox pair' ;; esac
[[ "$staging_id" =~ ^run-[0-9]+-attempt-[0-9]+$ ]] || blind '--staging-id must be run-N-attempt-N'

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
		*/apk/*/APKINDEX.tar.gz | */repository-manifest.json | */repository-manifest.json.asc | \
		*/pacman/*/olivares.db | */pacman/*/olivares.db.sig | */pacman/*/olivares.files | */pacman/*/olivares.files.sig)
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

families=(apt rpm apk)
pacman_present=0
for rel in "${relatives[@]}"; do
	case "$rel" in pacman/* | */pacman/* | "$pacman_key_rel") pacman_present=1 ;; *) ;; esac
done
if [[ "$pacman_present" -eq 1 ]]; then
	pacman_verify_tree
	families+=(pacman)
fi

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
	*.sig) printf '%s\n' application/pgp-signature ;;
	*.gz | *.tgz) printf '%s\n' application/gzip ;;
	*.xml | *.repo) printf '%s\n' application/xml ;;
	*.asc | *.gpg | *.pub | */InRelease) printf '%s\n' application/pgp-keys ;;
	*.deb | *.rpm | *.apk | *.pkg.tar.zst | */pacman/*/olivares.db | */pacman/*/olivares.files)
		printf '%s\n' application/octet-stream ;;
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
	# A package signature is bound to immutable package bytes; pacman_sign dates it
	# at the source date, so a rerun with the same key writes the same bytes.
	*.deb | *.rpm | *.apk | *.pkg.tar.zst | *.pkg.tar.zst.sig | keys/* | olivares-packages.*) return 0 ;;
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
for family in "${families[@]}"; do
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
