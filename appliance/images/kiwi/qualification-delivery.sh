#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# qualification-delivery.sh OUT_DIR — the product delivery a qualification build reads, made the way D's S3 makes the
# release one, but with a throwaway key. Run from the pinned source root. The nfpm configurations read VERSION and ARCH
# from the environment: ARCH is amd64 here, and VERSION is X.Y.Z or 0.0.0-dev, the qualification's, which an unset
# VERSION means; any other value is refused before anything is written.
#
#   1. go builds bin/appliance-firstboot, bin/appliance-answers and bin/olivares (static), and installs nfpm v2.47.0 into
#      a private GOBIN;
#   2. nfpm packages packaging/nfpm/olivares-appliance-base.yaml and appliance/images/kiwi/product-dev.nfpm.yaml as RPMs
#      into OUT_DIR/packages, and rpmbuild builds olivares-selinux from appliance/selinux/olivares-selinux.spec in the
#      recipe's own builder (toolchain/fedora44: its base image by digest and its packages, the policy toolchain
#      among them, pinned by the lock), as the runner's user and without a network, into the same directory;
#   3. D's generate-package-repository-test-key.sh makes a throwaway key under its test-only latch, in a private
#      directory; D's rpm-payload-sign.py signs each RPM's header and render-rpm-repodata.py renders and signs the
#      repodata with it;
#   4. write_delivery.py writes delivery.json in D's shape and the public key beside the tree; the key is also
#      OUT_DIR/qualification-key.asc, with its fingerprint in OUT_DIR/qualification-key.fingerprint;
#   5. delivery_check.py checks the delivery as the build will, with that fingerprint; the private directory, and with
#      it the secret key, is removed whatever happened.
# D's scripts come from S3_SCRIPTS_DIR (default: scripts/ of the source root).
#   exit 0  written, and the build's own check accepts it
#   exit 1  written, and the build's own check (or write_delivery.py) refuses it
#   exit 2  it could not run: no new OUT_DIR, a VERSION of another form, not the source root, a missing tool or D script,
#           or a step
#           that failed
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
die() { printf 'qualification-delivery.sh: %s\n' "$*" >&2; exit 2; }

[ $# -eq 1 ] && [ -n "$1" ] || die "usage: qualification-delivery.sh OUT_DIR (a new directory)"
out=$1
VERSION=${VERSION-0.0.0-dev}
[[ "$VERSION" =~ ^([0-9]+\.[0-9]+\.[0-9]+|0\.0\.0-dev)$ ]] || die "VERSION is X.Y.Z or 0.0.0-dev, not '$VERSION'"
source_root=$(pwd)
builder_dir=appliance/images/toolchain/fedora44
for config in packaging/nfpm/olivares-appliance-base.yaml appliance/images/kiwi/product-dev.nfpm.yaml \
    appliance/selinux/olivares-selinux.spec "$builder_dir/Containerfile"; do
  [ -f "$source_root/$config" ] || die "run from the pinned source root: $source_root has no $config"
done
s3=${S3_SCRIPTS_DIR:-$source_root/scripts}
missing=()
for script in generate-package-repository-test-key.sh rpm-payload-sign.py render-rpm-repodata.py; do
  [ -f "$s3/$script" ] || missing+=("$script")
done
[ ${#missing[@]} -eq 0 ] || die "D's S3 scripts ${missing[*]} are not in $s3"
for tool in go docker gpg gpgconf python3; do
  command -v "$tool" >/dev/null || die "$tool is not on PATH"
done
mkdir "$out" 2>/dev/null || die "OUT_DIR must be new: $out"
out=$(cd "$out" && pwd)

# The secret key lives only here, in $private/k, with D's GnuPG home in $private/k/gnupg. The names are short because
# GnuPG's socket paths below them (S.gpg-agent.browser, the longest) must fit a Unix socket name under any usual TMPDIR.
private=$(mktemp -d "${TMPDIR:-/tmp}/qXXX")
cleanup() {
  if [ -d "$private/k/gnupg" ]; then
    gpgconf --homedir "$private/k/gnupg" --kill all >/dev/null 2>&1 || true
  fi
  rm -rf "$private"
}
trap cleanup EXIT

step() {  # a step that fails means the delivery could not be made
  local status=0
  "$@" || status=$?
  [ "$status" -eq 0 ] || die "$(basename "$1") ${2:-} exited $status"
}

export ARCH=amd64 VERSION
step env GOBIN="$private/gobin" go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.47.0
for command in appliance/cmd/appliance-firstboot appliance/cmd/appliance-answers cmd/olivares; do
  step env CGO_ENABLED=0 go build -trimpath -o "bin/$(basename "$command")" "./$command"
done
mkdir "$out/packages"
for config in packaging/nfpm/olivares-appliance-base.yaml appliance/images/kiwi/product-dev.nfpm.yaml; do
  step "$private/gobin/nfpm" package --config "$config" --packager rpm --target "$out/packages/"
done
step docker build --iidfile "$private/builder.iid" --file "$builder_dir/Containerfile" "$builder_dir"
mkdir "$private/rpmbuild"
step docker run --rm --network none --user "$(id -u):$(id -g)" --env HOME=/rpmbuild \
  --volume "$source_root/appliance/selinux:/sources:ro" --volume "$private/rpmbuild:/rpmbuild" "$(cat "$private/builder.iid")" \
  rpmbuild -bb --define "_topdir /rpmbuild" --define "_sourcedir /sources" /sources/olivares-selinux.spec
built=("$private"/rpmbuild/RPMS/noarch/olivares-selinux-*.noarch.rpm)
[ ${#built[@]} -eq 1 ] && [ -f "${built[0]}" ] || die "rpmbuild left no single olivares-selinux package"
cp "${built[0]}" "$out/packages/"

step env OLIVARES_PACKAGE_REPO_TEST_ONLY=1 bash "$s3/generate-package-repository-test-key.sh" "$private/k"
key=(env OLIVARES_PACKAGE_REPO_TEST_ONLY=1 OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE="$private/k/descriptor.json"
     OLIVARES_PACKAGE_REPO_OPENPGP_SECRET_KEY_FILE="$private/k/openpgp-secret.asc" TMPDIR="$private")
rpms=()
for rpm in "$out/packages"/*.rpm; do rpms+=(--rpm "$rpm"); done
step "${key[@]}" python3 "$s3/rpm-payload-sign.py" "${rpms[@]}"
step "${key[@]}" python3 "$s3/render-rpm-repodata.py" render --repo "$out/packages"

status=0
python3 "$here/write_delivery.py" --package-dir "$out/packages" --key-file "$private/k/openpgp-public.asc" || status=$?
[ "$status" -ne 1 ] || { printf 'qualification-delivery.sh: write_delivery.py refused the tree\n' >&2; exit 1; }
[ "$status" -eq 0 ] || die "write_delivery.py exited $status"
cp "$private/k/openpgp-public.asc" "$out/qualification-key.asc"
# The fingerprint is the key's own (delivery_check.fingerprint), and must be the one D's descriptor names.
fingerprint=$(python3 -c 'import json, sys
sys.path.insert(0, sys.argv[1])
from delivery_check import fingerprint
found = fingerprint(open(sys.argv[2], "rb").read())
named = json.load(open(sys.argv[3]))["openpgp_fingerprint"]
sys.exit("the key is %s and its descriptor names %s" % (found, named)) if found != named else print(found)' \
  "$here" "$out/qualification-key.asc" "$private/k/descriptor.json") || die "the throwaway key cannot be named"
printf '%s\n' "$fingerprint" > "$out/qualification-key.fingerprint"

status=0
python3 "$here/delivery_check.py" --package-dir "$out/packages" --expected-fingerprint "$fingerprint" >/dev/null || status=$?
if [ "$status" -eq 1 ]; then
  printf 'qualification-delivery.sh: the build would refuse this delivery (key %s)\n' "$fingerprint" >&2
  exit 1
fi
[ "$status" -eq 0 ] || die "the delivery cannot be checked (delivery_check.py exited $status)"
printf 'qualification-delivery.sh: %s/packages, throwaway key %s (not a release)\n' "$out" "$fingerprint"
