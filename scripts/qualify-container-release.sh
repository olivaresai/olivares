#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Assemble and test the Community release image context.
set -euo pipefail

_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env
source_image=${1:?source-built image is required}
owner=${2:?repository_id-run_id-run_attempt is required}
[[ "$owner" =~ ^[0-9]+-[0-9]+-[0-9]+$ ]] || exit 2
context=$(mktemp -d "${RUNNER_TEMP:?}/runtime-release.XXXXXX")
prefix="olivares-runtime:$owner"
extractor="olivares-runtime-$owner-extract"
cleanup() {
	docker rm -f "$extractor" >/dev/null 2>&1 || true
	for variant in release; do
		if docker image inspect "$prefix-$variant" >/dev/null 2>&1; then
			docker image rm "$prefix-$variant" >/dev/null
		fi
	done
	rm -rf "$context"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

for variant in release; do
	dir="$context/$variant"
	# Qualification images are never published: the project NOTICE stands in for the
	# third-party notice that only the GoReleaser license hook generates.
	bash scripts/assemble-runtime-context.sh "$dir" NOTICE
	cp "Dockerfile.$variant" "$dir/Dockerfile"
	from="$source_image"
	binary=olivares
	args=()
	docker create --name "$extractor" "$from" version >/dev/null
	docker cp "$extractor:/usr/local/bin/olivares" "$dir/$binary"
	docker rm "$extractor" >/dev/null
	docker build "${args[@]}" --tag "$prefix-$variant" "$dir"
	bash scripts/qualify-container-agent-runtime.sh "$prefix-$variant" "$owner-$variant"
done
