#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# TEST-ONLY key-set generator for DIST-24-06 batteries. The production renderer
# never calls this helper and rejects its descriptor unless the same explicit
# test-only latch is present.
set -euo pipefail
LC_ALL=C
export LC_ALL

if [[ "${OLIVARES_PACKAGE_REPO_TEST_ONLY:-}" != 1 ]]; then
	printf '%s\n' 'generate-package-repository-test-key: HALLAZGO — requires OLIVARES_PACKAGE_REPO_TEST_ONLY=1' >&2
	exit 1
fi
if [[ "$#" -ne 1 || "$1" != /* ]]; then
	printf '%s\n' 'usage: generate-package-repository-test-key.sh /absolute/new/output-directory' >&2
	exit 2
fi
out="$1"
if [[ -e "$out" ]]; then
	printf 'generate-package-repository-test-key: HALLAZGO — output already exists: %s\n' "$out" >&2
	exit 1
fi
for tool in gpg openssl jq sha256sum awk chmod mkdir; do
	command -v "$tool" >/dev/null 2>&1 || {
		printf 'generate-package-repository-test-key: NO HE PODIDO MIRAR — missing %s\n' "$tool" >&2
		exit 2
	}
done
mkdir -p "$out/gnupg"
chmod 0700 "$out" "$out/gnupg"
export GNUPGHOME="$out/gnupg"
key_epoch="${OLIVARES_PACKAGE_REPO_TEST_KEY_EPOCH:-1704067200}"
[[ "$key_epoch" =~ ^[0-9]+$ ]] || {
	printf '%s\n' 'generate-package-repository-test-key: NO HE PODIDO MIRAR — test key epoch is not numeric' >&2
	exit 2
}
gpg --batch --yes --pinentry-mode loopback --passphrase '' \
	--faked-system-time "${key_epoch}!" --quick-generate-key \
	'Olivares DIST-24-06 TEST ONLY <dist24-06-test@invalid.olivares.ai>' rsa2048 sign 0 \
	>/dev/null 2>&1
fingerprint="$(gpg --batch --with-colons --list-secret-keys 2>/dev/null | awk -F: '$1=="fpr"{print $10; exit}')"
[[ "$fingerprint" =~ ^[0-9A-F]{40}$ ]] || {
	printf '%s\n' 'generate-package-repository-test-key: NO HE PODIDO MIRAR — GPG emitted no primary fingerprint' >&2
	exit 2
}
gpg --batch --yes --pinentry-mode loopback --passphrase '' --armor \
	--export-secret-keys "$fingerprint" >"$out/openpgp-secret.asc"
chmod 0600 "$out/openpgp-secret.asc"
gpg --batch --yes --armor --export-options export-minimal \
	--export "$fingerprint" >"$out/openpgp-public.asc"
chmod 0644 "$out/openpgp-public.asc"
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 \
	-out "$out/apk-private.pem" >/dev/null 2>&1
chmod 0600 "$out/apk-private.pem"
openssl pkey -in "$out/apk-private.pem" -pubout -out "$out/apk-public.pem" 2>/dev/null
chmod 0644 "$out/apk-public.pem"
apk_sha="$(sha256sum "$out/apk-public.pem" | awk '{print $1}')"
apk_name="olivares-package-repository-test-${fingerprint:0:16}.rsa.pub"
jq -nS \
	--arg schema 'olivares.ai/package-repository-key/v1' \
	--arg purpose 'apt-rpm-apk-repository-metadata' \
	--arg environment test \
	--arg openpgp_fingerprint "$fingerprint" \
	--arg apk_public_key_name "$apk_name" \
	--arg apk_public_key_sha256 "$apk_sha" \
	'{schema:$schema,purpose:$purpose,environment:$environment,
	  openpgp_fingerprint:$openpgp_fingerprint,
	  apk_public_key_name:$apk_public_key_name,
	  apk_public_key_sha256:$apk_public_key_sha256}' >"$out/descriptor.json"
chmod 0644 "$out/descriptor.json"
printf 'generate-package-repository-test-key: TEST ONLY — OpenPGP %s, APK SHA-256 %s\n' \
	"$fingerprint" "$apk_sha"
