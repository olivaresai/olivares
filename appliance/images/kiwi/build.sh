#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# build.sh — one appliance root, built by the pinned KIWI NG inside the pinned container.
#
# It stages what the build may read (the description, the appliance's own packages, and the
# dracut modules of the same pinned KIWI release as the package dracut-kiwi-oem-dump, which
# KIWI's runtime check for an installer medium requires by name and neither Debian 13 nor Fedora
# 44 ships at that release, built in the builder by dracut_package.py), runs the builder in a
# privileged container
# with the host's /dev shared - which is what KIWI documents for container mode, because a
# disk image needs loop devices - and writes a manifest naming every input by digest. The
# formats (appliance/images/formats) are assembled from what it leaves in the target
# directory; this script formats nothing itself.
#
# usage: build.sh [--base fedora44|debian13] [--edition server|desktop] [--arch amd64] [--firmware nonfree|free]
#                 --receipt FILE --attempt ID --image-id sha256:ID --accelerator kvm|tcg
#                 [--package-dir DIR] [--qualification-key FINGERPRINT] [--target-dir DIR]
#        build.sh --print-plan [any option above]
#   --base fedora44, the default, is the shipping Fedora 44 base on the Fedora builder
#   (toolchain/fedora44); debian13 is the Debian 13 base, not shipping, on the Debian builder
#   with the product's .debs, which --deb-dir (the same option as --package-dir, for debian13 only)
#   names.
#   --edition server, the default, builds the fedora44-server-amd64 profile; desktop builds
#   fedora44-desktop-amd64, the server profile plus GNOME, on the fedora44 base only.
#   On Fedora, --package-dir is the signed rpm-md tree D's S3 delivers with its delivery.json and
#   the package-repository key; delivery_check.py refuses it before KIWI unless its key is the
#   release key pinned in package-repository-key.json, or the throwaway key --qualification-key
#   names, and the manifest then records the build as not a release. VERSION (from the environment:
#   X.Y.Z for a release, 0.0.0-dev for a qualification) names the release outputs
#   release_outputs.py writes beside the images after KIWI, from the source pin of this checkout,
#   which must be clean.
#   --print-plan prints the two builder commands offline and runs nothing. It reads no receipt,
#   package or WORK_DIR; an identity not given is shown as a placeholder, one given must be exact.
#   exit 0  the build finished and the manifest is written, or the plan was printed
#   exit 1  the build ran and failed, including any builder command after admission whatever
#           status it returned (logged raw), or current admission of the builder was refused, or
#           (Fedora) the delivered product repository was refused before any container
#   exit 2  it could not run: an unknown option, base, edition, architecture or firmware; a package
#           the image must install that nobody staged; no receipt; a malformed attempt, image
#           or accelerator; no WORK_DIR; a work or output directory that is not new; or (Fedora) no
#           VERSION of X.Y.Z or 0.0.0-dev, or no clean source pin.
#           An inability is never a pass.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../.." && pwd)

base=${BASE:-fedora44}
edition=server
arch=amd64
firmware=${FIRMWARE:-nonfree}
package_dir=${PACKAGE_DIR:-${DEB_DIR:-$repo/dist}}
target_dir=${TARGET_DIR:-$repo/dist/appliance}
print_plan=false
runtime=docker
receipt=""
attempt=""
image_id=""
accelerator=""
qualification_key=""
deb_dir_option=false

die() { printf 'build.sh: %s\n' "$*" >&2; exit 2; }

while [ $# -gt 0 ]; do
  case $1 in
    --base) base=${2:?}; shift 2 ;;
    --edition) edition=${2:?}; shift 2 ;;
    --arch) arch=${2:?}; shift 2 ;;
    --firmware) firmware=${2:?}; shift 2 ;;
    --package-dir) package_dir=${2:?}; shift 2 ;;
    --deb-dir) package_dir=${2:?}; deb_dir_option=true; shift 2 ;;
    --qualification-key) qualification_key=${2:?}; shift 2 ;;
    --target-dir) target_dir=${2:?}; shift 2 ;;
    --receipt) receipt=${2:?}; shift 2 ;;
    --attempt) attempt=${2:?}; shift 2 ;;
    --image-id) image_id=${2:?}; shift 2 ;;
    --accelerator) accelerator=${2:?}; shift 2 ;;
    --print-plan) print_plan=true; shift ;;
    -h|--help) awk 'NR >= 6 { if (!/^#/) exit; print }' "$0"; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

# One architecture; the desktop edition is a profile of the same description as the server's,
# the server's packages plus GNOME, and arm64 (A6) stays unclaimed here.
case $edition in
  server|desktop) ;;
  *) die "EDITION is server or desktop, not $edition" ;;
esac
[ "$arch" = amd64 ] || die "this recipe delivers amd64 only, not $arch"
case $firmware in
  nonfree|free) ;;
  *) die "FIRMWARE is nonfree or free, not $firmware" ;;
esac

# The base: its profile, its firmware profile, its builder (toolchain and lock) and the kind of
# package its local repository holds. Fedora 44 ships (Root c1efc2ee); Debian 13 stays buildable
# by name while its own tests hold.
case $base in
  fedora44)
    overlay="fedora44-$edition-amd64"; firmware_profile=fedora44-firmware; kind=rpm
    toolchain="$repo/appliance/images/toolchain/fedora44" ;;
  debian13)
    [ "$edition" = server ] || die "the Debian 13 base builds the server edition only, not $edition"
    overlay=debian13-server-amd64; firmware_profile=firmware-nonfree; kind=deb
    toolchain="$repo/appliance/images/toolchain" ;;
  *) die "BASE is fedora44 (shipping) or debian13, not $base" ;;
esac
profiles=("$overlay")
lock="$toolchain/input-lock.json"
# The release outputs are named from VERSION alone (olivares-appliance-VERSION-...): X.Y.Z for a release, 0.0.0-dev for
# a qualification, and nothing else. It is never derived from git, a date or a file; a build that cannot name its
# outputs does not start.
version=${VERSION:-}
version_rule='^([0-9]+\.[0-9]+\.[0-9]+|0\.0\.0-dev)$'
if [ "$kind" = rpm ] && [ "$print_plan" != true ] && ! [[ "$version" =~ $version_rule ]]; then
  die "VERSION is X.Y.Z or 0.0.0-dev, not '$version'"
fi
if [ "$deb_dir_option" = true ] && [ "$base" != debian13 ]; then
  die "--deb-dir names .debs, for --base debian13 only; the Fedora base reads --package-dir"
fi
# Redistributable firmware by default: it is what the official Debian installer has carried
# since bookworm, it is Fedora's linux-firmware, and it is the difference between booting and
# not booting on most server network and storage controllers. Whether the PUBLISHED image
# carries it is the owner's open decision (design 10.1, 11.4); FIRMWARE=free answers it the
# other way here.
if [ "$firmware" = nonfree ]; then
  profiles+=("$firmware_profile")
fi

# The two packages the image installs from the appliance's own repository. The product is the
# community edition the owner decided the image ships installed; the layer is first boot.
required_packages=(olivares olivares-appliance-base)

# The prerequisite is the only builder owner. A proposed identity is not admission, and an
# identity must be exact: only --print-plan may leave one open, and shows a placeholder.
exact() {  # VALUE PATTERN MESSAGE
  if [ -z "$1" ] && [ "$print_plan" = true ]; then
    return 0
  fi
  [[ "$1" =~ $2 ]] || die "$3"
}
exact "$attempt" '^[0-9a-f]{32}$' "an exact attempt ID is required"
exact "$image_id" '^sha256:[0-9a-f]{64}$' "an immutable builder image ID is required"
exact "$accelerator" '^(kvm|tcg)$' "an explicit kvm or tcg accelerator is required"

# The product repository's key (Fedora): the release key the recipe pins, or a throwaway key a
# qualification build names explicitly. D's S3 scripts sign the in-build dracut RPM with a per-build
# key; they are composed into scripts/ before this recipe (S3_SCRIPTS_DIR names another place).
expected_key=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["release_fingerprint"])' \
  "$here/package-repository-key.json")
release=true
if [ -n "$qualification_key" ]; then
  [[ "$qualification_key" =~ ^[0-9A-F]{40}$ ]] || die "--qualification-key is an upper-case 40-hex OpenPGP fingerprint"
  expected_key=$qualification_key
  release=false
fi
s3_scripts=${S3_SCRIPTS_DIR:-$repo/scripts}

# NAME_VERSION_ARCH.deb, or NAME-VERSION-RELEASE.ARCH.rpm, whose version starts with a digit.
staged_package() {
  if [ "$kind" = deb ]; then
    find "$package_dir" -maxdepth 1 \( -name "$1_*.deb" -o -name "$1.deb" \) 2>/dev/null | sort | head -n 1
  else
    find "$package_dir" -maxdepth 1 \( -name "$1-[0-9]*.rpm" -o -name "$1.rpm" \) 2>/dev/null | sort | head -n 1
  fi
}
delivery_record=""
if [ "$print_plan" != true ] && [ "$kind" = rpm ]; then
  status=0
  delivery_record=$(python3 "$here/delivery_check.py" --package-dir "$package_dir" --expected-fingerprint "$expected_key") \
    || status=$?
  if [ "$status" -eq 1 ]; then
    printf 'build.sh: the delivered product repository is refused (expected key %s)\n' "$expected_key" >&2
    exit 1
  fi
  [ "$status" -eq 0 ] || die "the delivered product repository cannot be checked (expected key $expected_key)"
  for script in rpm-payload-sign.py render-rpm-repodata.py; do
    [ -f "$s3_scripts/$script" ] || die "D's S3 script $script is not in $s3_scripts: compose S3 before this recipe"
  done
  source_pin=$(python3 "$here/release_outputs.py" pin --source-root "$repo") \
    || die "the release outputs need the clean source pin of $repo"
  printf 'source pin: %s\n' "$source_pin"
fi
if [ "$print_plan" != true ]; then
  if [ ! -d "$package_dir" ]; then
    die "no package directory at $package_dir: build the packages first (task appliance:package:base and the product package)"
  fi
  missing=()
  for pkg in "${required_packages[@]}"; do
    if [ "$kind" = deb ] && [ -z "$(staged_package "$pkg")" ]; then
      missing+=("$pkg")
    fi
  done
  if [ ${#missing[@]} -gt 0 ]; then
    die "the image must install ${missing[*]}, and no such $kind package is in $package_dir"
  fi
  [ -r "$receipt" ] || die "a same-job prerequisite receipt is required"
  [ -n "${WORK_DIR:-}" ] || die "the same-job owner must provide its exclusive WORK_DIR"
fi
kiwi_version=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["toolchain"]["kiwi"]["upstream_version"])' "$lock")
base_image=$(sed -n 's/^FROM //p' "$toolchain/Containerfile" | head -n 1)
work=${WORK_DIR:-<WORK_DIR>}
attempt=${attempt:-<attempt-id>}
image_id=${image_id:-<admitted-sha256-image-id>}
accelerator=${accelerator:-<kvm|tcg>}
stage="$work/description"
packages="$work/packages"
build_packages="$work/build-packages"

profile_args=()
for profile in "${profiles[@]}"; do profile_args+=(--profile "$profile"); done

# The two commands this build is. They are printed before they run - and printed INSTEAD of
# running with --print-plan - because a build nobody can read is a build nobody can repeat.
# The stage step: the dracut package from the admitted builder's modules, then the local
# repository's index (dpkg-scanpackages on Debian; createrepo_c with gzip metadata on Fedora, which
# the index check reads), and the check that the index lists the dracut package.
stage_modules='set -eux
    mkdir -p /work/stage/usr/lib/dracut/modules.d
    modules=$(python3 -c "import json; print(json.load(open(\"/toolchain/input-lock.json\"))[\"toolchain\"][\"dracut_modules_dir\"])")
    cp -a "$modules/." /work/stage/usr/lib/dracut/modules.d/'
if [ "$kind" = deb ]; then
  stage_index='
    python3 /work/dracut_package.py --lock /toolchain/input-lock.json --root /work/stage --output /packages
    cd /packages && dpkg-scanpackages --multiversion . > Packages && gzip -kf Packages
    python3 /work/dracut_package.py --lock /toolchain/input-lock.json --indexed /packages/Packages'
else
  # The product repository stays D's signed tree as delivered; the dracut RPM goes to a signed
  # repository of its own (stage_fedora.py).
  stage_index='
    python3 /work/stage_fedora.py --lock /toolchain/input-lock.json --root /work/stage --build-packages /build-packages --description /work/description --s3 /work/s3'
fi
local_volumes=(--volume "$packages:/packages")
if [ "$kind" = rpm ]; then
  local_volumes+=(--volume "$build_packages:/build-packages")
fi
plan_stage=("$runtime" run --cidfile "$work/stage.cid" --label "org.olivares.appliance.attempt=$attempt"
  --volume "$work:/work"
  "${local_volumes[@]}"
  "$image_id"
  bash -c "$stage_modules$stage_index")
plan_kiwi=("$runtime" run --cidfile "$work/build.cid" --label "org.olivares.appliance.attempt=$attempt" --privileged
  --volume /dev:/dev
  --volume "$stage:/description"
  "${local_volumes[@]}"
  --volume "$target_dir:/target"
  "$image_id"
  kiwi-ng "${profile_args[@]}" --color-output system build
  --description /description --target-dir /target)

print_plan() {
  printf 'base:          %s\n' "$base"
  printf 'edition:       %s\n' "$edition"
  printf 'architecture:  %s\n' "$arch"
  printf 'profiles:      %s\n' "${profiles[*]}"
  printf 'firmware:      %s\n' "$firmware"
  printf 'kiwi:          %s\n' "$kiwi_version"
  printf 'base image:    %s\n' "$base_image"
  printf 'runtime:       %s\n' "$runtime"
  printf 'builder image: %s\n' "$image_id"
  printf 'accelerator:   %s\n' "$accelerator"
  printf 'packages from: %s\n' "$package_dir"
  if [ "$kind" = rpm ]; then
    printf 'version:       %s\n' "${version:-<X.Y.Z>}"
  fi
  if [ "$kind" = rpm ] && [ "$release" = true ]; then
    printf 'release:       yes, package repository key %s\n' "$expected_key"
  elif [ "$kind" = rpm ]; then
    printf 'release:       no, qualification key %s\n' "$expected_key"
  fi
  printf 'target:        %s\n' "$target_dir"
  printf 'stage inputs:  %s\n' "${plan_stage[*]}"
  printf 'build image:   %s\n' "${plan_kiwi[*]}"
}

if [ "$print_plan" = true ]; then
  printf 'UNADMITTED PLAN: no builder or runtime qualification performed\n'
  print_plan
  exit 0
fi

# The actual owner rechecks current context, live image and capacity. Validate the reply
# before creating directories, and never substitute a tag or silently change accelerator.
python3 "$repo/appliance/test/runner/admit.py" --lock "$lock" --receipt "$receipt" \
  --attempt "$attempt" --image-id "$image_id" --accelerator "$accelerator" | \
  python3 "$here/consume.py" "$attempt" "$image_id" "$accelerator"
mkdir "$work" || die "the recipe work directory must be exclusively new"
mkdir "$stage" "$packages"
mkdir "$target_dir" || die "the output directory must be exclusively new"
cp "$here/config.xml" "$here/config.sh" "$here/images.sh" "$stage/"
cp "$here/archive-signed-by.sh" "$here/archive-keys.json" "$stage/"
cp "$here/fedora-repo-pinned.sh" "$here/fedora-repositories.json" "$stage/"
cp "$here/post_bootstrap.sh" "$stage/"
cp "$here/dracut_package.py" "$work/"
if [ "$kind" = deb ]; then
  for pkg in "${required_packages[@]}"; do
    cp "$(staged_package "$pkg")" "$packages/"
  done
else
  # D's tree as delivered and checked, never re-indexed; its key beside the description, pinned.
  cp -a "$package_dir/." "$packages/"
  mkdir "$build_packages" "$work/s3"
  cp "$package_dir/olivares-package-repository.asc" "$stage/"
  # The profile overlay KIWI copies into the root before config.sh (system/setup.py
  # import_overlay_files: a directory named exactly like each selected profile), with the
  # checked key at the path the repository file names.
  cp -a "$here/$overlay" "$stage/"
  mkdir -p "$stage/$overlay/etc/pki/rpm-gpg"
  cp "$package_dir/olivares-package-repository.asc" \
    "$stage/$overlay/etc/pki/rpm-gpg/RPM-GPG-KEY-olivares-package-repository"
  printf '{"olivares-appliance-rpms": {"key": "olivares-package-repository.asc", "fingerprint": "%s"}}\n' \
    "$expected_key" > "$stage/olivares-repositories.json"
  cp "$here/olivares-repo-pinned.sh" "$here/delivery_check.py" "$stage/"
  cp "$here/stage_fedora.py" "$work/"
  cp "$s3_scripts/rpm-payload-sign.py" "$s3_scripts/render-rpm-repodata.py" "$work/s3/"
  printf 'delivered product repository accepted: %s\n' "$delivery_record"
fi

# After current admission a failed builder command is an observed defect, whatever status the
# container or its tool returned (GNU tar's fatal status is 2): exit 1, with the raw status in
# the log. A signal is not trapped, so an interrupted build still ends by that signal.
builder() {  # NAME COMMAND...
  local name=$1 status=0
  shift
  "$@" || status=$?
  if [ "$status" -ne 0 ]; then
    printf 'build.sh: builder command %s exited with status %s after admission\n' "$name" "$status" >&2
    exit 1
  fi
}

print_plan
builder stage "${plan_stage[@]}"
build_key=""
if [ "$kind" = rpm ]; then
  # config.sh erases the per-build key from the image's rpm keyring by this record, then removes the record.
  build_key=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["olivares-appliance-build"]["fingerprint"])' \
    "$stage/olivares-build-repository.json")
  mkdir -p "$stage/$overlay/var/lib/olivares-appliance-build"
  printf '%s\n' "$build_key" > "$stage/$overlay/var/lib/olivares-appliance-build/per-build-key.fingerprint"
fi
# The local repositories' packages are named by digest here, after the stage step and before KIWI
# reads any of them: on Debian the one KIWI trusts without a signature, on Fedora the two it reads
# with both checks.
if [ "$kind" = deb ]; then
  printf 'local repository before KIWI trusts it (%s):\n' "$packages"
  (cd "$packages" && sha256sum -- *."$kind")
else
  printf 'local repositories before KIWI reads them (%s):\n' "$work"
  (cd "$work" && sha256sum -- packages/*.rpm build-packages/*.rpm)
fi
builder kiwi "${plan_kiwi[@]}"
if [ "$kind" = rpm ]; then
  # The release outputs beside the images, from the image's rpm database as KIWI exported it into the target directory
  # and the pinned metadata, which release_outputs.py reads again by GET and checks against the pins.
  # Then the corresponding source bundle from the same inputs, and the consumer's check of it.
  for step in sbom bundle; do
    status=0
    python3 "$here/release_outputs.py" "$step" --target-dir "$target_dir" --version "$version" --lock "$lock" \
      --repositories "$here/fedora-repositories.json" --metadata "$work/release-metadata" --fetch \
      --delivery "$packages" --build-packages "$build_packages" --source-root "$repo" || status=$?
    if [ "$status" -ne 0 ]; then
      printf 'build.sh: the %s was not written (release_outputs.py exited %s)\n' "$step" "$status" >&2
      exit 1
    fi
  done
  status=0
  python3 "$here/release_outputs.py" verify-bundle --target-dir "$target_dir" --version "$version" || status=$?
  if [ "$status" -ne 0 ]; then
    printf 'build.sh: the source bundle does not verify (release_outputs.py exited %s)\n' "$status" >&2
    exit 1
  fi
  # Its measured size, in the build log (and, with every output, in the manifest below).
  bundle_file="$target_dir/olivares-appliance-$version-source.tar.zst"
  printf 'source bundle: %s, %s bytes (a release asset may not pass 2147483648)\n' "$(basename "$bundle_file")" \
    "$(stat -c %s "$bundle_file")"
fi

# The manifest: what went in, by digest, beside what came out. A3 turns this into the release
# evidence of an image artifact; here it is what lets a second build be compared with a first.
manifest="$target_dir/build.manifest.json"
{
  printf '{\n'
  printf '  "schema": "olivares-appliance-build/v1",\n'
  printf '  "base": "%s",\n' "$base"
  printf '  "edition": "%s",\n' "$edition"
  printf '  "arch": "%s",\n' "$arch"
  printf '  "firmware": "%s",\n' "$firmware"
  printf '  "profiles": ['
  separator=""
  for profile in "${profiles[@]}"; do
    printf '%s"%s"' "$separator" "$profile"
    separator=", "
  done
  printf '],\n'
  printf '  "kiwi_version": "%s",\n' "$kiwi_version"
  printf '  "base_image": "%s",\n' "$base_image"
  printf '  "builder_image_id": "%s",\n' "$image_id"
  printf '  "attempt_id": "%s",\n' "$attempt"
  printf '  "accelerator": "%s",\n' "$accelerator"
  printf '  "lock_sha256": "%s",\n' "$(sha256sum "$lock" | cut -d' ' -f1)"
  if [ "$kind" = rpm ]; then
    printf '  "version": "%s",\n' "$version"
    printf '  "release": %s,\n' "$release"
    printf '  "package_repository_key": "%s",\n' "$expected_key"
    printf '  "build_key": "%s",\n' "$build_key"
  fi
  printf '  "built_at": "%s",\n' "$(date -u +%FT%TZ)"
  printf '  "inputs": [\n'
  first=true
  while IFS= read -r file; do
    [ "$first" = true ] || printf ',\n'
    first=false
    if [ "$kind" = deb ]; then name=$(basename "$file"); else name=${file#"$work"/}; fi
    printf '    {"file": "%s", "sha256": "%s"}' "$name" "$(sha256sum "$file" | cut -d' ' -f1)"
  done < <(if [ "$kind" = deb ]; then find "$packages" -maxdepth 1 -name '*.deb'; else
           find "$packages" "$build_packages" -maxdepth 1 -name '*.rpm'; fi | sort)
  printf '\n  ],\n'
  printf '  "outputs": [\n'
  first=true
  while IFS= read -r file; do
    [ "$first" = true ] || printf ',\n'
    first=false
    printf '    {"file": "%s", "bytes": %s, "sha256": "%s"}' \
      "$(basename "$file")" "$(stat -c %s "$file")" "$(sha256sum "$file" | cut -d' ' -f1)"
  done < <(find "$target_dir" -maxdepth 1 -type f ! -name '*.json' | sort)
  printf '\n  ]\n}\n'
} > "$manifest"

printf 'build.sh: wrote %s\n' "$manifest"
