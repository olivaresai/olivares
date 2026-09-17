#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Native clean-client leg for DIST-24-06. It deliberately imports the public key
# from the repository's published keys/ surface, then asks the distribution's own
# package manager to refresh and fetch. The final cmp binds those fetched bytes to
# the authenticated GitHub Release asset supplied separately by the caller.
set -euo pipefail
LC_ALL=C
export LC_ALL

family=""
image=""
repository=""
repository_url=""
assets=""
version=""
evidence_file=""
staging_id=""
inside=0

usage() {
	printf '%s\n' 'usage: package-repository-client-ci.sh --family apt|rpm|apk --image IMAGE (--repository ABS|--repository-url HTTPS) --assets ABS --version X.Y.Z [--evidence-file ABS --staging-id run-N-attempt-N]'
}

while [[ "$#" -gt 0 ]]; do
	case "$1" in
	--family) family="${2:-}"; shift 2 ;;
	--image) image="${2:-}"; shift 2 ;;
	--repository) repository="${2:-}"; shift 2 ;;
	--repository-url) repository_url="${2:-}"; shift 2 ;;
	--assets) assets="${2:-}"; shift 2 ;;
	--version) version="${2:-}"; shift 2 ;;
	--evidence-file) evidence_file="${2:-}"; shift 2 ;;
	--staging-id) staging_id="${2:-}"; shift 2 ;;
	--inside) inside=1; shift ;;
	-h | --help) usage; exit 0 ;;
	*) printf 'package-repository-client: unknown argument: %s\n' "$1" >&2; exit 2 ;;
	esac
done

case "$family" in apt | rpm | apk) ;; *) usage >&2; exit 2 ;; esac
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { usage >&2; exit 2; }

fail() { printf 'package-repository-client: HALLAZGO — %s\n' "$*" >&2; exit 1; }
blind() { printf 'package-repository-client: NO HE PODIDO MIRAR — %s\n' "$*" >&2; exit 2; }

if [[ "$inside" -eq 0 ]]; then
	[[ -n "$image" ]] || blind '--image is required'
	if [[ -n "$repository_url" ]]; then
		[[ -z "$repository" ]] || blind 'choose one of --repository or --repository-url'
		[[ "$repository_url" =~ ^https://packages[.]olivares[.]ai(/staging/run-[0-9]+-attempt-[0-9]+)?$ ]] || \
			blind '--repository-url must be the reviewed canonical or staging HTTPS origin'
	else
		[[ "$repository" == /* && -d "$repository" ]] || blind '--repository must be an existing absolute directory'
	fi
	[[ "$assets" == /* && -d "$assets" ]] || blind '--assets must be an existing absolute directory'
	if [[ -n "$evidence_file" || -n "$staging_id" ]]; then
		[[ -n "$repository_url" ]] || blind 'evidence is valid only for a remote repository'
		[[ "$evidence_file" == /* && ! -e "$evidence_file" ]] || blind '--evidence-file must be a new absolute path'
		[[ "$staging_id" =~ ^run-[0-9]+-attempt-[0-9]+$ ]] || blind '--staging-id must be run-N-attempt-N'
		[[ "$repository_url" == "https://packages.olivares.ai/staging/$staging_id" ]] || \
			blind 'evidence staging-id differs from the repository URL'
	fi
	command -v docker >/dev/null 2>&1 || blind 'docker is unavailable; native clean client did not run'
	docker_args=(run --rm --pull=always \
		--env CLIENT_FAMILY="$family" \
		--env CLIENT_VERSION="$version" \
		--volume "$assets:/release:ro" \
		--volume "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd):/tracked-scripts:ro")
	if [[ -n "$repository_url" ]]; then
		docker_args+=(--env CLIENT_REPOSITORY_URL="$repository_url")
	else
		docker_args+=(--volume "$repository:/repository:ro")
	fi
	docker "${docker_args[@]}" "$image" /bin/sh -eu -c \
		'if ! command -v bash >/dev/null 2>&1; then apk add --no-cache bash; fi
		exec bash /tracked-scripts/package-repository-client-ci.sh --inside \
			--family "$CLIENT_FAMILY" --version "$CLIENT_VERSION"'
	if [[ -n "$evidence_file" ]]; then
		printf '%s\n' "$staging_id" >"$evidence_file"
		chmod 0600 "$evidence_file"
	fi
	exit 0
fi

[[ "$repository" == "" && "$assets" == "" && "$image" == "" && "$evidence_file" == "" && "$staging_id" == "" ]] || blind 'internal client received host paths'
repository_url="${CLIENT_REPOSITORY_URL:-}"
if [[ -n "$repository_url" ]]; then
	[[ "$repository_url" =~ ^https://packages[.]olivares[.]ai(/staging/run-[0-9]+-attempt-[0-9]+)?$ ]] || blind 'internal remote origin is outside the reviewed domain'
else
	[[ -d /repository/stable ]] || blind 'clean client repository mount is absent'
fi
[[ -d /release ]] || blind 'clean client release mount is absent'
work=/client-download
mkdir -p "$work"

one_file() {
	local pattern="$1"
	local found=()
	mapfile -t found < <(find "$work" -maxdepth 1 -type f -name "$pattern" -print)
	[[ "${#found[@]}" -eq 1 ]] || fail "client fetched ${#found[@]} files matching $pattern, expected 1"
	printf '%s\n' "${found[0]}"
}

case "$family" in
apt)
	command -v apt-get >/dev/null 2>&1 || blind 'apt-get is absent in the apt client'
	if [[ -n "$repository_url" ]]; then
		apt-get -o Acquire::Languages=none update
		apt-get -y install ca-certificates curl
		curl --fail --silent --show-error --proto '=https' \
			"$repository_url/keys/olivares-package-repository.asc" \
			-o /usr/share/keyrings/olivares-package-repository.asc
		apt_origin="$repository_url/stable/apt"
	else
		cp /repository/keys/olivares-package-repository.asc /usr/share/keyrings/olivares-package-repository.asc
		apt_origin=file:/repository/stable/apt
	fi
	find /etc/apt/sources.list.d -mindepth 1 -maxdepth 1 -type f -delete 2>/dev/null || true
	printf 'deb [signed-by=/usr/share/keyrings/olivares-package-repository.asc] %s stable main\n' "$apt_origin" \
		>/etc/apt/sources.list
	apt-get -o Acquire::Languages=none update
	(cd "$work" && apt-get download "olivares=$version")
	apt-get -y install "olivares=$version"
	downloaded="$(one_file 'olivares_*.deb')"
	release_asset="/release/olivares_${version}_linux_amd64.deb"
	;;
rpm)
	command -v dnf >/dev/null 2>&1 || blind 'dnf is absent in the rpm client'
	if [[ -n "$repository_url" ]]; then
		dnf -y install ca-certificates curl
		rpm_origin="$repository_url/stable/rpm/x86_64"
		rpm_key="$repository_url/keys/olivares-package-repository.asc"
	else
		rpm_origin=file:///repository/stable/rpm/x86_64
		rpm_key=file:///repository/keys/olivares-package-repository.asc
	fi
	cat >/etc/yum.repos.d/olivares.repo <<REPO
[olivares]
name=Olivares AI stable
baseurl=$rpm_origin
enabled=1
gpgcheck=0
repo_gpgcheck=1
gpgkey=$rpm_key
metadata_expire=0
REPO
	dnf -y makecache --disablerepo='*' --enablerepo=olivares
	dnf -y download --disablerepo='*' --enablerepo=olivares --destdir="$work" "olivares-$version-1"
	dnf -y install --disablerepo='*' --enablerepo=olivares "olivares-$version-1"
	downloaded="$(one_file '*.rpm')"
	release_asset="/release/olivares_${version}_linux_amd64.rpm"
	;;
apk)
	command -v apk >/dev/null 2>&1 || blind 'apk is absent in the APK client'
	if [[ -n "$repository_url" ]]; then
		apk add --no-cache ca-certificates curl
		key=/etc/apk/keys/olivares-packages-apk.rsa.pub
		curl --fail --silent --show-error --proto '=https' \
			"$repository_url/keys/olivares-packages-apk.rsa.pub" -o "$key"
		apk_origin="$repository_url/stable/apk/x86_64"
	else
		key="$(find /repository/keys -maxdepth 1 -type f -name '*.rsa.pub' -print)"
		[[ -n "$key" && "$(printf '%s\n' "$key" | wc -l)" -eq 1 ]] || fail 'published APK key inventory is not exact'
		cp "$key" "/etc/apk/keys/${key##*/}"
		apk_origin=file:///repository/stable/apk/x86_64
	fi
	printf '%s\n' "$apk_origin" >/etc/apk/repositories
	apk update
	apk fetch --output "$work" "olivares=$version"
	apk add "olivares=$version"
	downloaded="$(one_file '*.apk')"
	release_asset="/release/olivares_${version}_linux_amd64.apk"
	;;
esac

[[ -f "$release_asset" ]] || blind "authenticated release asset is absent: $release_asset"
cmp -s "$downloaded" "$release_asset" || fail 'native client package is not byte-identical to release asset'
[[ -x /usr/bin/olivares ]] || fail 'native package manager did not install /usr/bin/olivares executable'
printf 'package-repository-client: LIVE OK — %s verified signed stable metadata and byte-identical %s\n' \
	"$family" "${release_asset##*/}"
