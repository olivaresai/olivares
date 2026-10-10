#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Hermetic battery for the pacman family of scripts/publish-package-repositories.sh.
# No network, no real key, no R2: two THROWAWAY OpenPGP keys are generated in this
# battery's own scratch, repo-add and wrangler are doubles, and every case is
# independent. The negative cases build their pacman trees with this battery's own
# signer, never with the publisher's render mode, so a defect in the render cannot
# hide a defect in the refusal.
#
# The repo-add double writes the repo-add(8) layout (<repo>.db.tar.gz with a
# <name>-<version>/desc entry, <repo>.files.tar.gz, and the two links) from the
# fixture's file name. The real repo-add and pacman read real packages in the
# Arch Linux container leg of the package matrix.
set -uo pipefail
LC_ALL=C
export LC_ALL

could_not_look() {
	printf 'test-package-publish-pacman: COULD NOT CHECK — %s\n' "$*" >&2
	exit 2
}
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
tmp_root="${TMPDIR:-}"
[[ "$tmp_root" == /* && -d "$tmp_root" ]] || could_not_look 'TMPDIR must be an existing absolute directory'
for tool in awk bash cat chmod cmp cp find gpg gpgv grep ln mkdir mktemp mv python3 rm sed sha256sum sort wc; do
	command -v "$tool" >/dev/null 2>&1 || could_not_look "missing $tool"
done
cleanup() {
	gpgconf --homedir "$keys/good" --kill all >/dev/null 2>&1 || true
	gpgconf --homedir "$keys/wrong" --kill all >/dev/null 2>&1 || true
	case "$scratch" in "$tmp_root"/package-publish-pacman-test.*) rm -rf -- "$scratch" ;; *) ;; esac
	case "$keys" in "$tmp_root"/ppk.*) rm -rf -- "$keys" ;; *) ;; esac
}
scratch="$(mktemp -d "$tmp_root/package-publish-pacman-test.XXXXXX")" || could_not_look 'cannot allocate scratch'
# The key homes get their own short directory: gpg-agent's socket path must stay
# under the 108-byte sun_path limit.
keys="$(mktemp -d "$tmp_root/ppk.XXXXXX")" || could_not_look 'cannot allocate key scratch'
trap cleanup EXIT INT TERM
chmod 0700 "$scratch" "$keys"
publisher="$root/scripts/publish-package-repositories.sh"
version=26.1000
pkg_name="olivares_${version}_linux_amd64.pkg.tar.zst"
pacman_key_rel=keys/olivares-pacman-repository.gpg
epoch=1790000000

# --- throwaway keys ------------------------------------------------------------------
# make_key NAME: a TEST ONLY rsa2048 signing key in $keys/NAME, created at a
# fixed past time; prints nothing. Writes NAME.fpr, NAME.secret.asc and NAME.gpg.
make_key() {
	local name="$1" home="$keys/$1"
	mkdir -p "$home"
	chmod 0700 "$home"
	gpg --homedir "$home" --batch --yes --pinentry-mode loopback --passphrase '' \
		--faked-system-time '1704067200!' --quick-generate-key \
		"Olivares pacman TEST ONLY $name <pacman-test-$name@invalid.olivares.ai>" rsa2048 sign 0 \
		>/dev/null 2>&1 || could_not_look "cannot generate throwaway key $name"
	gpg --homedir "$home" --batch --with-colons --list-secret-keys 2>/dev/null |
		awk -F: '$1=="fpr"{print $10; exit}' >"$keys/$name.fpr"
	grep -Eq '^[0-9A-F]{40}$' "$keys/$name.fpr" || could_not_look "no fingerprint for $name"
	gpg --homedir "$home" --batch --pinentry-mode loopback --passphrase '' --armor \
		--export-secret-keys "$(cat "$keys/$name.fpr")" >"$keys/$name.secret.asc"
	chmod 0600 "$keys/$name.secret.asc"
	gpg --homedir "$home" --batch --export-options export-minimal \
		--export "$(cat "$keys/$name.fpr")" >"$keys/$name.gpg"
	[[ -s "$keys/$name.gpg" ]] || could_not_look "no public key for $name"
}
make_key good
make_key wrong
good_fpr="$(cat "$keys/good.fpr")"

# sign KEY SUBJECT: binary detached signature SUBJECT.sig made by throwaway KEY.
sign() {
	gpg --homedir "$keys/$1" --batch --yes --pinentry-mode loopback --passphrase '' \
		--faked-system-time "${epoch}!" --local-user "$(cat "$keys/$1.fpr")!" \
		--digest-algo SHA256 --no-armor --detach-sign --output "$2.sig" "$2" >/dev/null 2>&1 ||
		could_not_look "throwaway signing failed for $2"
}

# --- doubles -------------------------------------------------------------------------
mkdir -p "$scratch/bin"
cat >"$scratch/bin/repo-add" <<'FAKE'
#!/usr/bin/env bash
# repo-add double: repo-add [-q] DB.db.tar.gz PKG
set -euo pipefail
[[ "${1:-}" == -q || "${1:-}" == --quiet ]] && shift
db="$1" pkg="$2"
[[ "$db" == *.db.tar.gz && -f "$pkg" ]] || exit 9
python3 - "$db" "$pkg" <<'PY'
import hashlib, io, os, re, sys, tarfile
db, pkg = sys.argv[1], sys.argv[2]
m = re.fullmatch(r"olivares_([0-9.]+)_linux_amd64\.pkg\.tar\.zst", os.path.basename(pkg))
if not m:
    sys.exit(9)
entry = f"olivares-{m.group(1)}-1"
digest = hashlib.sha256(open(pkg, "rb").read()).hexdigest()
desc = (f"%FILENAME%\n{os.path.basename(pkg)}\n\n%NAME%\nolivares\n\n%VERSION%\n{m.group(1)}-1\n\n"
        f"%SHA256SUM%\n{digest}\n\n%ARCH%\nx86_64\n\n").encode()
def write(path, members):
    with tarfile.open(path, "w:gz", format=tarfile.USTAR_FORMAT) as tar:
        for name, data in members:
            info = tarfile.TarInfo(name)
            info.size, info.mtime, info.mode = len(data), 1790000000, 0o644
            tar.addfile(info, io.BytesIO(data))
write(db, [(f"{entry}/desc", desc)])
write(db.replace(".db.tar.gz", ".files.tar.gz"), [(f"{entry}/desc", desc), (f"{entry}/files", b"%FILES%\nusr/bin/olivares\n\n")])
PY
ln -sf "$(basename "$db")" "${db%.tar.gz}"
files="${db%.db.tar.gz}.files.tar.gz"
ln -sf "$(basename "$files")" "${files%.tar.gz}"
printf 'repo-add %s %s\n' "$(basename "$db")" "$(basename "$pkg")" >>"$FAKE_REPO_ADD_LOG"
FAKE
cat >"$scratch/bin/wrangler" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == --version ]]; then printf '%s\n' 4.100.0; exit 0; fi
[[ "${1:-}" == r2 && "${2:-}" == object ]] || exit 9
verb="${3:-}"; ref="${4:-}"; shift 4
[[ "$ref" != /* && "$ref" != *..* ]] || exit 9
file="" type="" cache=""
while [[ "$#" -gt 0 ]]; do
  case "$1" in
  --file) file="${2:-}"; shift 2 ;;
  --content-type) type="${2:-}"; shift 2 ;;
  --cache-control) cache="${2:-}"; shift 2 ;;
  *) shift ;;
  esac
done
target="$FAKE_STORE/$ref"
mkdir -p "$(dirname "$target")"
case "$verb" in
put)
  printf 'put\t%s\t%s\t%s\n' "$ref" "$type" "$cache" >>"$FAKE_LOG"
  cp "$file" "$target"
  ;;
get)
  [[ -f "$target" ]] || { echo 'The specified key does not exist. [code: 10007]' >&2; exit 1; }
  cp "$target" "$file"
  ;;
delete) rm -f -- "$target" ;;
*) exit 9 ;;
esac
FAKE
chmod 0755 "$scratch/bin/repo-add" "$scratch/bin/wrangler"

mkdir -p "$scratch/release"
printf 'fixture Arch Linux package %s\n' "$version" >"$scratch/release/$pkg_name"

# base_tree DIR: the apt/rpm/apk objects the publisher requires, as in test-package-publish.sh.
base_tree() {
	local dir="$1" rel
	for rel in \
		olivares-packages.gpg keys/olivares-package-repository.asc keys/olivares-packages-apk.rsa.pub \
		stable/apt/dists/stable/InRelease stable/rpm/x86_64/repodata/repomd.xml \
		stable/rpm/aarch64/repodata/repomd.xml stable/apk/x86_64/APKINDEX.tar.gz \
		stable/apk/aarch64/APKINDEX.tar.gz stable/repository-manifest.json \
		security/apt/dists/security/InRelease security/rpm/x86_64/repodata/repomd.xml \
		security/rpm/aarch64/repodata/repomd.xml security/apk/x86_64/APKINDEX.tar.gz \
		security/apk/aarch64/APKINDEX.tar.gz security/repository-manifest.json \
		stable/apt/pool/main/o/olivares/olivares.deb; do
		mkdir -p "$dir/$(dirname "$rel")"
		printf 'fixture %s\n' "$rel" >"$dir/$rel"
	done
}
# pacman_fixture DIR [KEY]: this battery's own signed pacman repositories in DIR.
pacman_fixture() {
	local dir="$1" key="${2:-good}" channel repo
	for channel in stable security; do
		repo="$dir/$channel/pacman/x86_64"
		mkdir -p "$repo"
		cp "$scratch/release/$pkg_name" "$repo/"
		sign "$key" "$repo/$pkg_name"
		(cd "$repo" && FAKE_REPO_ADD_LOG=/dev/null "$scratch/bin/repo-add" -q olivares.db.tar.gz "$pkg_name") ||
			could_not_look 'repo-add double failed'
		rm -f "$repo/olivares.db" "$repo/olivares.files"
		mv "$repo/olivares.db.tar.gz" "$repo/olivares.db"
		mv "$repo/olivares.files.tar.gz" "$repo/olivares.files"
		sign "$key" "$repo/olivares.db"
		sign "$key" "$repo/olivares.files"
	done
	cp "$keys/$key.gpg" "$dir/keys/olivares-pacman-repository.gpg"
}
# new_case NAME: fresh tree, store and logs under $scratch/case-NAME.
new_case() {
	case_dir="$scratch/case-$1"
	mkdir -p "$case_dir/tree" "$case_dir/store" "$case_dir/evidence"
	export FAKE_STORE="$case_dir/store" FAKE_LOG="$case_dir/wrangler.log" FAKE_REPO_ADD_LOG="$case_dir/repo-add.log"
	: >"$FAKE_LOG"
	: >"$FAKE_REPO_ADD_LOG"
	base_tree "$case_dir/tree"
}
# The expected pacman key fingerprint comes from the workflow (a tracked descriptor once
# the key is decided); expected_fpr=none leaves it unset.
publish() {
	local expect="${expected_fpr:-$good_fpr}"
	[[ "$expect" == none ]] && expect=
	env CLOUDFLARE_API_TOKEN=test-only CLOUDFLARE_ACCOUNT_ID=0123456789abcdef0123456789abcdef \
		OLIVARES_PACKAGE_PUBLISH_APPROVED=1 OLIVARES_WRANGLER_BIN="$scratch/bin/wrangler" \
		${expect:+OLIVARES_PACMAN_EXPECTED_FINGERPRINT="$expect"} \
		bash "$publisher" --bucket olivares-packages --tree "$case_dir/tree" --staging-id run-7-attempt-1 "$@" \
		>"$case_dir/out" 2>&1
}
render() {
	env OLIVARES_REPO_ADD_BIN="$scratch/bin/repo-add" \
		OLIVARES_PACMAN_SIGNING_KEY_FILE="${render_key_file:-$keys/good.secret.asc}" \
		OLIVARES_PACMAN_SIGNING_FINGERPRINT="${render_fpr:-$good_fpr}" \
		OLIVARES_PACMAN_EXPECTED_FINGERPRINT="${expected_fpr:-$good_fpr}" \
		bash "$publisher" --mode pacman-render --tree "$case_dir/tree" \
		--pacman-packages "$scratch/release" --version "$version" --source-date-epoch "$epoch" \
		>"$case_dir/out" 2>&1
}
evidence_all() {
	local family
	for family in apt rpm apk pacman; do printf '%s\n' run-7-attempt-1 >"$case_dir/evidence/$family.ok"; done
}
no_canonical() {
	[[ -z "$(find "$case_dir/store" -type f ! -path '*/staging/*' -print -quit)" ]]
}

passed=0 failed=0
result() {
	if [[ "$1" -eq 0 ]]; then
		passed=$((passed + 1)); printf 'ok - %s\n' "$2"
	else
		failed=$((failed + 1)); printf 'not ok - %s\n' "$2"
		sed -n '1,12s/^/    /p' "$case_dir/out" 2>/dev/null
	fi
}
gpgv_ok() { gpgv --keyring "$1" "$2.sig" "$2" >/dev/null 2>&1; }

# --- cases ---------------------------------------------------------------------------
case_render_writes_signed_repositories() (
	new_case "${FUNCNAME[0]#case_}"
	render || exit 1
	repo="$case_dir/tree/stable/pacman/x86_64"
	got="$(cd "$case_dir/tree" && find stable/pacman security/pacman keys/olivares-pacman-repository.gpg -type f | sort)"
	want="$(for c in security stable; do for f in olivares.db olivares.db.sig olivares.files olivares.files.sig \
		"$pkg_name" "$pkg_name.sig"; do printf '%s/pacman/x86_64/%s\n' "$c" "$f"; done; done
		printf '%s\n' keys/olivares-pacman-repository.gpg)"
	[[ "$got" == "$(sort <<<"$want")" ]] || exit 1
	[[ -z "$(find "$case_dir/tree" -type l -print -quit)" ]] || exit 1
	for c in stable security; do
		for f in olivares.db olivares.files "$pkg_name"; do
			gpgv_ok "$keys/good.gpg" "$case_dir/tree/$c/pacman/x86_64/$f" || exit 1
		done
	done
	cmp -s "$scratch/release/$pkg_name" "$repo/$pkg_name" || exit 1
	[[ "$(grep -c '^repo-add olivares.db.tar.gz ' "$FAKE_REPO_ADD_LOG")" -eq 2 ]] || exit 1
	grep -F "$good_fpr" "$case_dir/out" >/dev/null
)
case_render_refuses_other_fingerprint() (
	new_case "${FUNCNAME[0]#case_}"
	render_fpr="$(cat "$keys/wrong.fpr")"
	expected_fpr="$render_fpr"   # the anchored key, which the key file does not hold
	render && exit 1
	grep -F 'does not hold the signing fingerprint' "$case_dir/out" >/dev/null &&
		[[ ! -e "$case_dir/tree/stable/pacman" ]]
)
case_render_refuses_shared_key_file() (
	new_case "${FUNCNAME[0]#case_}"
	cp "$keys/good.secret.asc" "$case_dir/secret.asc"
	chmod 0640 "$case_dir/secret.asc"
	render_key_file="$case_dir/secret.asc"
	render && exit 1
	grep -F 'group or other access' "$case_dir/out" >/dev/null && [[ ! -e "$case_dir/tree/stable/pacman" ]]
)
case_render_then_publish_roots_last() (
	new_case "${FUNCNAME[0]#case_}"
	render || exit 1
	publish --mode stage --apply || exit 1
	publish --mode verify-stage --apply || exit 1
	evidence_all
	publish --mode promote --apply --evidence-dir "$case_dir/evidence" || exit 1
	last_content="$(awk -F '\t' '$2 ~ /^olivares-packages\/(stable|security)\/pacman\/.*\.pkg\.tar\.zst(\.sig)?$/{n=NR} END{print n+0}' "$FAKE_LOG")"
	first_root="$(awk -F '\t' '$2 ~ /^olivares-packages\/(stable|security)\/pacman\/x86_64\/olivares\.(db|files)(\.sig)?$/{print NR; exit}' "$FAKE_LOG")"
	[[ "$last_content" -gt 0 && "${first_root:-0}" -gt "$last_content" ]] || exit 1
	grep -P "^put\tolivares-packages/stable/pacman/x86_64/$pkg_name\tapplication/octet-stream\tpublic, max-age=31536000, immutable$" "$FAKE_LOG" >/dev/null || exit 1
	grep -P "^put\tolivares-packages/stable/pacman/x86_64/$pkg_name.sig\tapplication/pgp-signature\tpublic, max-age=31536000, immutable$" "$FAKE_LOG" >/dev/null || exit 1
	grep -P "^put\tolivares-packages/stable/pacman/x86_64/olivares.db\tapplication/octet-stream\tprivate, no-store$" "$FAKE_LOG" >/dev/null || exit 1
	grep -P "^put\tolivares-packages/stable/pacman/x86_64/olivares.db.sig\tapplication/pgp-signature\tprivate, no-store$" "$FAKE_LOG" >/dev/null || exit 1
	grep -P "^put\tolivares-packages/keys/olivares-pacman-repository.gpg\tapplication/pgp-keys\tpublic, max-age=31536000, immutable$" "$FAKE_LOG" >/dev/null
)
# refused_before_any_write CASE MUTATION...: the mutated tree is refused by the dry run,
# by stage and by promote, with exit 1, and no canonical object is written.
refused_everywhere() {
	publish --mode stage && return 1
	[[ "$(tail -n 1 "$case_dir/out")" == *"$1"* ]] || return 1
	publish --mode stage --apply && return 1
	evidence_all
	publish --mode promote --apply --evidence-dir "$case_dir/evidence"
	[[ "$?" -eq 1 ]] || return 1
	[[ "$(tail -n 1 "$case_dir/out")" == *"$1"* ]] || return 1
	[[ -z "$(find "$case_dir/store" -type f -print -quit)" ]]
}
case_unsigned_database() (
	new_case "${FUNCNAME[0]#case_}"
	pacman_fixture "$case_dir/tree"
	rm -f "$case_dir/tree/stable/pacman/x86_64/olivares.db.sig"
	refused_everywhere 'pacman database is unsigned: stable/pacman/x86_64/olivares.db'
)
case_wrongly_signed_database() (
	new_case "${FUNCNAME[0]#case_}"
	pacman_fixture "$case_dir/tree"
	sign wrong "$case_dir/tree/security/pacman/x86_64/olivares.db"
	refused_everywhere 'signature does not verify with keys/olivares-pacman-repository.gpg: security/pacman/x86_64/olivares.db'
)
case_altered_database() (
	new_case "${FUNCNAME[0]#case_}"
	pacman_fixture "$case_dir/tree"
	printf 'x' >>"$case_dir/tree/stable/pacman/x86_64/olivares.db"
	refused_everywhere 'signature does not verify with keys/olivares-pacman-repository.gpg: stable/pacman/x86_64/olivares.db'
)
case_unsigned_package() (
	new_case "${FUNCNAME[0]#case_}"
	pacman_fixture "$case_dir/tree"
	rm -f "$case_dir/tree/stable/pacman/x86_64/$pkg_name.sig"
	refused_everywhere "pacman package is unsigned: stable/pacman/x86_64/$pkg_name"
)
case_database_names_other_bytes() (
	new_case "${FUNCNAME[0]#case_}"
	pacman_fixture "$case_dir/tree"
	repo="$case_dir/tree/stable/pacman/x86_64"
	printf 'other bytes\n' >"$repo/$pkg_name"
	sign good "$repo/$pkg_name"
	refused_everywhere "pacman database does not describe the package bytes beside it: stable/pacman/x86_64/olivares.db"
)
case_unexpected_pacman_object() (
	new_case "${FUNCNAME[0]#case_}"
	pacman_fixture "$case_dir/tree"
	mkdir -p "$case_dir/tree/stable/pacman/aarch64"
	cp "$case_dir/tree/stable/pacman/x86_64/olivares.db" "$case_dir/tree/stable/pacman/aarch64/olivares.db"
	refused_everywhere 'unexpected pacman object: stable/pacman/aarch64/olivares.db'
)
case_partial_pacman_family() (
	new_case "${FUNCNAME[0]#case_}"
	pacman_fixture "$case_dir/tree"
	rm -rf "$case_dir/tree/security/pacman"
	refused_everywhere 'publish tree lacks required object: security/pacman/x86_64/olivares.db'
)
case_promote_needs_pacman_evidence() (
	new_case "${FUNCNAME[0]#case_}"
	pacman_fixture "$case_dir/tree"
	publish --mode stage --apply || exit 1
	evidence_all
	rm -f "$case_dir/evidence/pacman.ok"
	publish --mode promote --apply --evidence-dir "$case_dir/evidence"
	[[ "$?" -eq 1 ]] || exit 1
	grep -F 'missing clean-client evidence: pacman.ok' "$case_dir/out" >/dev/null && no_canonical
)
case_immutable_package_signature() (
	new_case "${FUNCNAME[0]#case_}"
	pacman_fixture "$case_dir/tree"
	publish --mode stage --apply || exit 1
	evidence_all
	existing="$case_dir/store/olivares-packages/stable/pacman/x86_64/$pkg_name.sig"
	mkdir -p "$(dirname "$existing")"
	printf 'different signature bytes\n' >"$existing"
	publish --mode promote --apply --evidence-dir "$case_dir/evidence"
	[[ "$?" -eq 1 ]] || exit 1
	grep -F "immutable canonical object differs; refusing overwrite: stable/pacman/x86_64/$pkg_name.sig" "$case_dir/out" >/dev/null &&
		grep -Fx 'different signature bytes' "$existing" >/dev/null
)
case_tree_wholly_signed_by_another_key() (
	new_case "${FUNCNAME[0]#case_}"
	pacman_fixture "$case_dir/tree" wrong
	refused_everywhere "pacman key $pacman_key_rel is not the expected key $good_fpr"
)
case_pacman_without_expected_fingerprint() (
	new_case "${FUNCNAME[0]#case_}"
	pacman_fixture "$case_dir/tree"
	expected_fpr=none
	publish --mode stage
	[[ "$?" -eq 2 ]] && grep -F 'OLIVARES_PACMAN_EXPECTED_FINGERPRINT' "$case_dir/out" >/dev/null &&
		[[ -z "$(find "$case_dir/store" -type f -print -quit)" ]]
)
case_render_refuses_a_key_other_than_expected() (
	new_case "${FUNCNAME[0]#case_}"
	expected_fpr="$(cat "$keys/wrong.fpr")"
	render && exit 1
	grep -F 'is not the expected key' "$case_dir/out" >/dev/null && [[ ! -e "$case_dir/tree/stable/pacman" ]]
)
case_toplevel_pacman_tree_is_checked() (
	new_case "${FUNCNAME[0]#case_}"
	pacman_fixture "$case_dir/tree"
	mkdir -p "$case_dir/tree/pacman/x86_64"
	printf 'x\n' >"$case_dir/tree/pacman/x86_64/olivares.db"
	refused_everywhere 'unexpected pacman object: pacman/x86_64/olivares.db'
)
case_tree_without_pacman_is_unchanged() (
	new_case "${FUNCNAME[0]#case_}"
	publish --mode stage --apply || exit 1
	for family in apt rpm apk; do printf '%s\n' run-7-attempt-1 >"$case_dir/evidence/$family.ok"; done
	publish --mode promote --apply --evidence-dir "$case_dir/evidence"
)

for name in \
	case_render_writes_signed_repositories \
	case_render_refuses_other_fingerprint \
	case_render_refuses_shared_key_file \
	case_render_then_publish_roots_last \
	case_unsigned_database \
	case_wrongly_signed_database \
	case_altered_database \
	case_unsigned_package \
	case_database_names_other_bytes \
	case_unexpected_pacman_object \
	case_partial_pacman_family \
	case_promote_needs_pacman_evidence \
	case_immutable_package_signature \
	case_tree_wholly_signed_by_another_key \
	case_pacman_without_expected_fingerprint \
	case_render_refuses_a_key_other_than_expected \
	case_toplevel_pacman_tree_is_checked \
	case_tree_without_pacman_is_unchanged; do
	"$name"
	rc=$?
	case_dir="$scratch/case-${name#case_}"
	result "$rc" "${name#case_}"
done
printf 'test-package-publish-pacman: %d/%d cases green\n' "$passed" "$((passed + failed))"
[[ "$failed" -eq 0 ]]
