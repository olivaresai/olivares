#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Build the real FIPS binary, then assemble/test GoReleaser-sized contexts.
set -euo pipefail
source_image=${1:?source-built image is required}
owner=${2:?repository_id-run_id-run_attempt is required}
[[ "$owner" =~ ^[0-9]+-[0-9]+-[0-9]+$ ]] || exit 2
context=$(mktemp -d "${RUNNER_TEMP:?}/runtime-release.XXXXXX")
prefix="olivares-runtime:$owner"
extractor="olivares-runtime-$owner-extract"
cleanup() {
	docker rm -f "$extractor" >/dev/null 2>&1 || true
	for variant in fips-source release fips stig; do
		if docker image inspect "$prefix-$variant" >/dev/null 2>&1; then
			docker image rm "$prefix-$variant" >/dev/null
		fi
	done
	rm -rf "$context"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

docker build -f Dockerfile.fips --build-arg VERSION=runtime-ci \
	--build-arg "COMMIT=${GITHUB_SHA:?}" \
	--build-arg "SOURCE_DATE_EPOCH=$(git show -s --format=%ct HEAD)" \
	--tag "$prefix-fips-source" .
for variant in release fips stig; do
	dir="$context/$variant"
	mkdir -p "$dir/packaging/container"
	cp LICENSE NOTICE LICENSING.md DISCLAIMER.md "$dir/"
	cp -R LICENSES "$dir/LICENSES"
	cp -R packaging/container/data-dir "$dir/packaging/container/data-dir"
	cp packaging/container/uv-LICENSE-MIT.txt "$dir/packaging/container/"
	cp "Dockerfile.$variant" "$dir/Dockerfile"
	from="$prefix-fips-source"
	binary=olivares-fips
	args=(--build-arg BIN_SOURCE=prebuilt)
	if [ "$variant" = release ]; then
		from="$source_image"
		binary=olivares
		args=()
	fi
	docker create --name "$extractor" "$from" version >/dev/null
	docker cp "$extractor:/usr/local/bin/olivares" "$dir/$binary"
	docker rm "$extractor" >/dev/null
	docker build "${args[@]}" --tag "$prefix-$variant" "$dir"
	bash scripts/qualify-container-agent-runtime.sh "$prefix-$variant" "$owner-$variant"
	if [ "$variant" != release ]; then
		fips_json=$(docker run --rm "$prefix-$variant" version -o json)
		grep -q '"fips"[[:space:]]*:[[:space:]]*"on"' <<<"$fips_json"
		grep -q '"module"[[:space:]]*:[[:space:]]*"v1.0.0"' <<<"$fips_json"
	fi
	if [ "$variant" = stig ]; then
		docker run --rm --entrypoint /bin/sh "$prefix-stig" -eu -c \
			'rpm -q glibc git-core python3; grep -q "^ID=\"rhel\"" /etc/os-release'
	fi
done
