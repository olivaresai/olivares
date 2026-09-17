#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Decode the encrypted production repository-key bundle without trusting its TAR
# paths or its self-declared identity. Secret values are read from files, never
# argv. The tracked descriptor is the independent identity anchor.
set -euo pipefail
LC_ALL=C
export LC_ALL

bundle_file=""
passphrase_file=""
descriptor_file=""
out=""
github_output=""
while [[ "$#" -gt 0 ]]; do
	case "$1" in
	--bundle-base64-file) bundle_file="${2:-}"; shift 2 ;;
	--passphrase-file) passphrase_file="${2:-}"; shift 2 ;;
	--descriptor) descriptor_file="${2:-}"; shift 2 ;;
	--out) out="${2:-}"; shift 2 ;;
	--github-output) github_output="${2:-}"; shift 2 ;;
	*) printf 'materialize-package-repository-key: unknown argument: %s\n' "$1" >&2; exit 2 ;;
	esac
done

blind() { printf 'materialize-package-repository-key: NO HE PODIDO MIRAR — %s\n' "$*" >&2; exit 2; }
fail() { printf 'materialize-package-repository-key: HALLAZGO — %s\n' "$*" >&2; exit 1; }

[[ "$bundle_file" == /* && -f "$bundle_file" && ! -L "$bundle_file" ]] || blind 'the base64 bundle file is not a regular absolute path'
[[ "$passphrase_file" == /* && -f "$passphrase_file" && ! -L "$passphrase_file" ]] || blind 'the passphrase file is not a regular absolute path'
[[ "$descriptor_file" == /* && -f "$descriptor_file" && ! -L "$descriptor_file" ]] || blind 'the tracked descriptor is not a regular absolute path'
[[ "$out" == /* && ! -e "$out" ]] || blind '--out must be a new absolute path'
[[ -z "$github_output" || "$github_output" == /* ]] || blind '--github-output must be absolute when present'
tmp_root="${TMPDIR:-}"
[[ "$tmp_root" == /* && -d "$tmp_root" ]] || blind 'TMPDIR must be an existing absolute directory'
for tool in awk base64 chmod cmp find gpg install jq mkdir mktemp openssl python3 rm sha256sum shred; do
	command -v "$tool" >/dev/null 2>&1 || blind "missing required tool: $tool"
done

cleanup() {
	case "$scratch" in
	"$tmp_root"/package-key.*)
		find "$scratch" -type f -exec shred -u -- {} + 2>/dev/null || true
		rm -rf -- "$scratch"
		;;
	*) printf 'materialize-package-repository-key: refusing unsafe cleanup %s\n' "$scratch" >&2 ;;
	esac
}
scratch="$(mktemp -d "$tmp_root/package-key.XXXXXX")" || blind 'cannot allocate scratch'; trap cleanup EXIT INT TERM
chmod 0700 "$scratch"

base64 --decode "$bundle_file" >"$scratch/bundle.gpg" 2>/dev/null || \
	blind 'OLIVARES_REPO_SIGNING_KEY is not canonical base64'
gpg --batch --quiet --pinentry-mode loopback --passphrase-file "$passphrase_file" \
	--output "$scratch/payload.tar" --decrypt "$scratch/bundle.gpg" 2>/dev/null || \
	fail 'encrypted custody bundle does not decrypt with the provisioned passphrase'

mkdir -m 0700 "$scratch/extracted"
python3 - "$scratch/payload.tar" "$scratch/extracted" <<'PY'
import os
import pathlib
import sys
import tarfile

archive = pathlib.Path(sys.argv[1])
out = pathlib.Path(sys.argv[2])
expected = {"openpgp-secret.asc", "apk-private.pem", "descriptor.json"}
with tarfile.open(archive, mode="r:") as bundle:
    members = bundle.getmembers()
    names = [member.name for member in members]
    if len(names) != len(expected) or set(names) != expected:
        raise SystemExit("custody TAR inventory is not exact")
    for member in members:
        if not member.isfile() or member.name.startswith("/") or pathlib.PurePosixPath(member.name).parts != (member.name,):
            raise SystemExit(f"custody TAR member is unsafe: {member.name!r}")
        if member.size <= 0 or member.size > 1024 * 1024:
            raise SystemExit(f"custody TAR member size is unsafe: {member.name!r}")
        stream = bundle.extractfile(member)
        if stream is None:
            raise SystemExit(f"custody TAR member cannot be read: {member.name!r}")
        body = stream.read(member.size + 1)
        if len(body) != member.size:
            raise SystemExit(f"custody TAR member size changed: {member.name!r}")
        target = out / member.name
        fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, "wb") as handle:
            handle.write(body)
PY

cmp -s "$descriptor_file" "$scratch/extracted/descriptor.json" || \
	fail 'encrypted descriptor differs from the tracked production anchor'
jq -e '
  .schema == "olivares.ai/package-repository-key/v1" and
  .purpose == "apt-rpm-apk-repository-metadata" and
  .environment == "production" and
  (.openpgp_fingerprint | test("^[0-9A-F]{40}$")) and
  (.apk_public_key_sha256 | test("^[0-9a-f]{64}$")) and
  (.apk_public_key_name | test("^[A-Za-z0-9._-]+[.]rsa[.]pub$"))
' "$descriptor_file" >/dev/null || fail 'tracked production descriptor is malformed'

mkdir -m 0700 "$scratch/gnupg"
gpg --homedir "$scratch/gnupg" --batch --quiet --import \
	"$scratch/extracted/openpgp-secret.asc" 2>/dev/null || fail 'OpenPGP secret import failed'
fingerprint="$(gpg --homedir "$scratch/gnupg" --batch --with-colons --list-secret-keys 2>/dev/null | awk -F: '$1=="fpr"{print $10; exit}')"
expected_fingerprint="$(jq -r .openpgp_fingerprint "$descriptor_file")"
[[ "$fingerprint" == "$expected_fingerprint" ]] || fail 'OpenPGP secret does not match the tracked fingerprint'

apk_name="$(jq -r .apk_public_key_name "$descriptor_file")"
expected_apk_sha="$(jq -r .apk_public_key_sha256 "$descriptor_file")"
openssl pkey -in "$scratch/extracted/apk-private.pem" -passin "file:$passphrase_file" \
	-pubout -out "$scratch/$apk_name" 2>/dev/null || fail 'APK private key cannot yield a public key'
actual_apk_sha="$(sha256sum "$scratch/$apk_name" | awk '{print $1}')"
[[ "$actual_apk_sha" == "$expected_apk_sha" ]] || fail 'APK private key does not match the tracked public-key digest'

mkdir -m 0700 "$out"
install -m 0600 "$scratch/extracted/openpgp-secret.asc" "$out/openpgp-secret.asc"
install -m 0600 "$scratch/extracted/apk-private.pem" "$out/apk-private.pem"
install -m 0600 "$passphrase_file" "$out/passphrase"
install -m 0644 "$descriptor_file" "$out/descriptor.json"
gpg --homedir "$scratch/gnupg" --batch --yes --armor --export-options export-minimal \
	--output "$out/olivares-packages.asc" --export "$fingerprint"
gpg --homedir "$scratch/gnupg" --batch --yes --export-options export-minimal \
	--output "$out/olivares-packages.gpg" --export "$fingerprint"
install -m 0644 "$scratch/$apk_name" "$out/$apk_name"
chmod 0600 "$out/openpgp-secret.asc" "$out/apk-private.pem" "$out/passphrase"
chmod 0644 "$out/descriptor.json" "$out/olivares-packages.asc" "$out/olivares-packages.gpg" "$out/$apk_name"

if [[ -n "$github_output" ]]; then
	{
		printf 'key_dir=%s\n' "$out"
		printf 'openpgp_fingerprint=%s\n' "$fingerprint"
		printf 'apk_public_key_name=%s\n' "$apk_name"
		printf 'apk_public_key_sha256=%s\n' "$actual_apk_sha"
	} >>"$github_output"
fi
printf 'materialize-package-repository-key: OK — production anchors match OpenPGP %s and APK SHA-256 %s\n' \
	"$fingerprint" "$actual_apk_sha"
