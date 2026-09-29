#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# appliance-release-delivery.sh — the product delivery a RELEASE build of the Fedora appliance
# reads, assembled from the published release's own bytes.
#
# The qualification path (appliance/images/kiwi/qualification-delivery.sh) builds dev RPMs and
# signs them with a throwaway key, so the build records release false. A release build is the
# one whose delivery carries the package-repository key pinned in
# appliance/images/kiwi/package-repository-key.json: build.sh refuses any other key as a
# release. This script assembles that delivery from three inputs:
#
#   1. the PRODUCT rpm as published on the GitHub release named by --version — the file's
#      sha256 must equal --expected-sha256, which the caller took from the cosign-verified
#      checksums.txt, BEFORE anything signs or indexes it. The bytes that enter the image are
#      the bytes the release signed;
#   2. the BASE layer rpm (packaging/nfpm/olivares-appliance-base.yaml), built here from the
#      tagged checkout, stamped with the same VERSION;
#   3. the SELINUX rpm (appliance/selinux), built in the pinned builder like the
#      qualification path builds it.
#
# All three are header-signed with the production repository key (--key-dir, the directory
# scripts/materialize-package-repository-key.sh writes from the protected environment's
# secrets), the repodata is rendered and signed, write_delivery.py writes delivery.json in D's
# shape, and delivery_check.py accepts it under the PINNED fingerprint — which is what makes
# the consuming build a release. Nothing here copies key material out of --key-dir.
#
#   usage: appliance-release-delivery.sh --version X.Y.Z --release-rpm FILE --expected-sha256 HEX \
#          --key-dir DIR --out DIR
#   run from the tagged source root; OUT_DIR must not exist.
#   exit 0  written, and the release build's own delivery check accepts it
#   exit 1  refused: the rpm is not the release's bytes, the key is not the pinned release
#           key, or a produced artifact was refused by the checks that gate it
#   exit 2  could not run: missing tool or input, wrong version shape, existing OUT_DIR
set -euo pipefail
export LC_ALL=C

here="$(cd "$(dirname "$0")" && pwd)"
kiwi="$(cd "$here/../appliance/images/kiwi" && pwd)"
s3="$here"

die() { printf 'appliance-release-delivery.sh: %s\n' "$*" >&2; exit 2; }
refuse() { printf 'appliance-release-delivery.sh: REFUSED — %s\n' "$*" >&2; exit 1; }

version="" release_rpm="" expected_sha256="" key_dir="" out=""
while [ "$#" -gt 0 ]; do
	case "$1" in
	--version) version="${2:?}" ; shift 2 ;;
	--release-rpm) release_rpm="${2:?}" ; shift 2 ;;
	--expected-sha256) expected_sha256="${2:?}" ; shift 2 ;;
	--key-dir) key_dir="${2:?}" ; shift 2 ;;
	--out) out="${2:?}" ; shift 2 ;;
	*) die "unknown argument: $1" ;;
	esac
done

# The version is X.Y.Z — bare, the tag itself since the 2026-09-29 tag-name correction — and
# it stamps the base rpm and every release output beside the images. 0.0.0-dev is the
# qualification's, refused here on purpose: this script exists to make the OTHER kind of run.
case "$version" in
'' | 0.0.0-dev) die "VERSION must be the release's X.Y.Z, not '${version:-<empty>}'" ;;
esac
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "VERSION is X.Y.Z, not '$version'"
[[ "$expected_sha256" =~ ^[0-9a-f]{64}$ ]] || die "--expected-sha256 must be 64 lowercase hex"
for tool in go docker gpg gpgconf python3 jq sha256sum; do
	command -v "$tool" >/dev/null 2>&1 || die "$tool is not on PATH"
done
source_root="$(pwd)"
for path in "packaging/nfpm/olivares-appliance-base.yaml" \
	"appliance/selinux/olivares-selinux.spec" \
	"appliance/images/toolchain/fedora44/Containerfile" \
	"$kiwi/write_delivery.py" "$kiwi/delivery_check.py" "$kiwi/package-repository-key.json" \
	"$s3/rpm-payload-sign.py" "$s3/render-rpm-repodata.py"; do
	[ -f "$path" ] || die "run from the tagged source root: $source_root has no $path"
done
[ -f "$release_rpm" ] || die "--release-rpm '$release_rpm' is not a file"
case "$key_dir" in
/*) ;;
*) die "--key-dir must be an absolute path" ;;
esac
for input in descriptor.json openpgp-secret.asc passphrase olivares-packages.asc; do
	[ -s "$key_dir/$input" ] || die "--key-dir is missing $input (materialize-package-repository-key.sh writes it)"
done
[ -n "$out" ] || die "--out is required"
[ ! -e "$out" ] || die "OUT_DIR must not exist: $out"
case "$out" in
/*) ;;
*) out="$source_root/$out" ;;
esac

# THE PINNED RELEASE KEY, read from the recipe's own anchor. The key the delivery carries is
# what decides whether the consuming build records itself a release; a delivery under any
# other fingerprint is a qualification, and publishing one as the release route would be the
# silent downgrade this check exists to make impossible.
pinned_fingerprint="$(jq -r '.release_fingerprint // empty' "$kiwi/package-repository-key.json")"
[[ "$pinned_fingerprint" =~ ^[0-9A-F]{40}$ ]] || die "package-repository-key.json carries no release_fingerprint"
case "$(jq -r '.environment // empty' "$key_dir/descriptor.json")" in
production) ;;
*) refuse "the materialized key's descriptor is not the production environment" ;;
esac
named_fingerprint="$(jq -r '.openpgp_fingerprint // empty' "$key_dir/descriptor.json")"
[[ "$named_fingerprint" =~ ^[0-9A-F]{40}$ ]] || die "the key descriptor carries no openpgp_fingerprint"
[ "$named_fingerprint" = "$pinned_fingerprint" ] ||
	refuse "the materialized key $named_fingerprint is not the pinned release key $pinned_fingerprint"

# THE PRODUCT RPM IS THE RELEASE'S BYTES before anything signs or indexes it. The caller took
# this digest from the cosign-verified checksums.txt; re-deriving it here keeps the binding a
# property of the delivery, not of one workflow step's ordering.
product_rpm_name="olivares_${version}_linux_amd64.rpm"
case "$(basename "$release_rpm")" in
"$product_rpm_name") ;;
*) refuse "--release-rpm must be named $product_rpm_name, not '$(basename "$release_rpm")'" ;;
esac
observed="$(sha256sum "$release_rpm" | awk '{print $1}')"
[ "$observed" = "$expected_sha256" ] ||
	refuse "the product rpm's sha256 is $observed, not the release's $expected_sha256 — the bytes differ from what checksums.txt signed"

mkdir "$out" || die "cannot create $out"
out="$(cd "$out" && pwd)"
private="$(mktemp -d "${TMPDIR:-/tmp}/ardXXX")"
cleanup() {
	if [ -d "$private/gnupg" ]; then gpgconf --homedir "$private/gnupg" --kill all >/dev/null 2>&1 || true; fi
	rm -rf "$private"
}
trap cleanup EXIT

step() {
	local status=0
	"$@" || status=$?
	[ "$status" -eq 0 ] || die "$(basename "$1") ${2:-} exited $status"
}

export ARCH=amd64 VERSION="$version"
step env GOBIN="$private/gobin" go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.47.0
mkdir "$out/packages"
step "$private/gobin/nfpm" package --config packaging/nfpm/olivares-appliance-base.yaml --packager rpm --target "$out/packages/"
step docker build --iidfile "$private/builder.iid" --file appliance/images/toolchain/fedora44/Containerfile appliance/images/toolchain/fedora44
mkdir "$private/rpmbuild"
step docker run --rm --network none --user "$(id -u):$(id -g)" --env HOME=/rpmbuild \
	--volume "$source_root/appliance/selinux:/sources:ro" --volume "$private/rpmbuild:/rpmbuild" "$(cat "$private/builder.iid")" \
	rpmbuild -bb --define "_topdir /rpmbuild" --define "_sourcedir /sources" /sources/olivares-selinux.spec
built=("$private"/rpmbuild/RPMS/noarch/olivares-selinux-*.noarch.rpm)
[ "${#built[@]}" -eq 1 ] && [ -f "${built[0]}" ] || die "rpmbuild left no single olivares-selinux package"
cp "${built[0]}" "$out/packages/"
# The release's own product rpm, last, still exactly the verified bytes.
cp "$release_rpm" "$out/packages/$product_rpm_name"
observed2="$(sha256sum "$out/packages/$product_rpm_name" | awk '{print $1}')"
[ "$observed2" = "$expected_sha256" ] ||
	refuse "the product rpm changed while assembling the delivery (was $expected_sha256, now $observed2)"

# The production key signs every rpm header and the repodata. NO test-only latch: the
# descriptor says production and rpm-payload-sign.py refuses the latch beside it.
key=(env OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE="$key_dir/descriptor.json"
	OLIVARES_PACKAGE_REPO_OPENPGP_SECRET_KEY_FILE="$key_dir/openpgp-secret.asc"
	OLIVARES_PACKAGE_REPO_OPENPGP_PASSPHRASE_FILE="$key_dir/passphrase" TMPDIR="$private")
rpms=()
for rpm in "$out/packages"/*.rpm; do rpms+=(--rpm "$rpm"); done
step "${key[@]}" python3 "$s3/rpm-payload-sign.py" "${rpms[@]}"
step "${key[@]}" python3 "$s3/render-rpm-repodata.py" render --repo "$out/packages"

status=0
python3 "$kiwi/write_delivery.py" --package-dir "$out/packages" --key-file "$key_dir/olivares-packages.asc" >/dev/null || status=$?
[ "$status" -ne 1 ] || refuse "write_delivery.py refused the assembled tree"
[ "$status" -eq 0 ] || die "write_delivery.py exited $status"

# THE BUILD'S OWN CHECK, under the pinned fingerprint: this is the line that makes the
# consuming build record release true, so it is asserted here and not left to the build.
status=0
python3 "$kiwi/delivery_check.py" --package-dir "$out/packages" --expected-fingerprint "$pinned_fingerprint" >/dev/null || status=$?
if [ "$status" -eq 1 ]; then
	refuse "the release build would refuse this delivery (key $pinned_fingerprint)"
fi
[ "$status" -eq 0 ] || die "the delivery cannot be checked (delivery_check.py exited $status)"
printf 'appliance-release-delivery.sh: %s/packages under the pinned release key %s\n' "$out" "$pinned_fingerprint"
