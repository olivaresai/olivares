#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Copy the non-binary files of GoReleaser's dockers extra_files into DIR, so a scripted
# build of Dockerfile.release, .fips or .stig gets the context the release gets. The
# caller adds the binary. NOTICE_FILE becomes .license-notices/NOTICE-community, the
# image's /usr/share/doc/olivares/NOTICE: the generated notice after the GoReleaser
# license hook, or the project NOTICE for an image that is never published.
# Run from the repository root; check-docker-context-sufficiency.sh checks the result.
set -euo pipefail
[[ $# == 2 ]] || { echo 'usage: assemble-runtime-context.sh DIR NOTICE_FILE' >&2; exit 2; }
dir=$1
mkdir -p "$dir/packaging/container" "$dir/.license-notices"
cp LICENSE NOTICE LICENSING.md DISCLAIMER.md "$dir/"
cp -R LICENSES "$dir/LICENSES"
cp -R packaging/container/data-dir "$dir/packaging/container/data-dir"
cp packaging/container/uv-LICENSE-MIT.txt "$dir/packaging/container/"
cp "$2" "$dir/.license-notices/NOTICE-community"
