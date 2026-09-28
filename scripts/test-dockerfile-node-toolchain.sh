#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Battery for check-dockerfile-node-toolchain.sh, in both directions.
#
# The known-bad sample is the web stage as it stood at d8e1625b in Dockerfile, Dockerfile.fips
# and Dockerfile.stig: node:26 and a bare `RUN corepack enable`. If case 1 ever goes green, the
# gate has stopped seeing the defect it was written for. The known-good sample is the same stage
# with a pinned corepack installed first. The other cases are the mutations the acceptance oracle
# names (the install removed from one file of three, the pin replaced by `latest`), the reading
# rules (comments are not code, a `#` inside a word does not hide what follows, continuation,
# heredoc, `sh -c`, exec form, a leading byte order mark), stage inheritance, ARG defaults, the
# pnpm-from-npm alternative and its packageManager equality, the corepack forms that pick a
# version (`corepack pnpm@…`, `corepack use`, `corepack up`), ONBUILD triggers, yarn, and the
# COULD NOT LOOK answers (no root, no Dockerfile, no FROM, a lone escape line, a heredoc or a
# quote left open).
#
# Each case writes its Dockerfile(s) into its own scratch directory and runs the gate on it.
# No git, no docker, no network.
set -euo pipefail
export LC_ALL=C

GATE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/check-dockerfile-node-toolchain.sh"
EXPECTED_CASES=61
scratch="$(mktemp -d "${TMPDIR:-/tmp}/dockerfile-node-toolchain.XXXXXX")"
trap 'rm -rf -- "$scratch"' EXIT
pass=0
fail=0
n=0
dir=""

new_case() {
	n=$((n + 1))
	dir="$scratch/case-$n"
	mkdir -p "$dir/web"
}

# df [NAME] < text — writes a Dockerfile of this case.
df() { cat >"$dir/${1:-Dockerfile}"; }

# pm VALUE — web/package.json with that packageManager; pm - writes one without the field.
pm() {
	if [ "$1" = - ]; then
		printf '{"name": "web"}\n' >"$dir/web/package.json"
	else
		printf '{"name": "web", "packageManager": "%s"}\n' "$1" >"$dir/web/package.json"
	fi
}

# expect NAME RC TEXT [ROOT] — runs the gate on this case and grades its exit code and output.
expect() {
	local name="$1" want="$2" text="$3" target="${4:-$dir}" out rc
	if out="$(bash "$GATE" "$target" 2>&1)"; then rc=0; else rc=$?; fi
	case "$out" in
	*"$text"*) seen=1 ;;
	*) seen=0 ;;
	esac
	if [ "$rc" = "$want" ] && [ "$seen" = 1 ]; then
		pass=$((pass + 1))
		printf '  ok   %2d %-62s rc=%s\n' "$n" "$name" "$rc"
	else
		fail=$((fail + 1))
		printf '  FAIL %2d %-62s rc=%s (want %s, text %s)\n' "$n" "$name" "$rc" "$want" "$text"
		printf '%s\n' "$out" | sed 's/^/         /' | head -8
	fi
}

# The web stage of the three files at d8e1625b (Dockerfile:35-46), with its placeholder go stage.
bad_stage() {
	cat <<'EOF'
FROM node:26-bookworm-slim AS web
WORKDIR /src
# Enable corepack/pnpm without a network round-trip beyond the registry.
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./web/
RUN cd web && pnpm install --frozen-lockfile
COPY web/ ./web/
RUN cd web && pnpm run build

FROM golang:1.26.8-bookworm AS build
COPY --from=web /src/core/internal/webui/dist/ ./dist/
EOF
}

# The same stage after the fix: a pinned corepack before `corepack enable`.
good_stage() { bad_stage | sed 's/^RUN corepack enable$/RUN npm install -g corepack@0.34.6 \&\& corepack enable/'; }

echo "test-dockerfile-node-toolchain: known-bad and known-good samples"
new_case; pm pnpm@10.33.2; bad_stage | df
expect "KNOWN-BAD: d8e1625b web stage (bare corepack enable on node:26)" 1 'Dockerfile:4: stage "web"'
new_case; pm pnpm@10.33.2; bad_stage | df
expect "KNOWN-BAD reports the stage once, not each pnpm line after it" 1 "1 violation(s) in 1 Dockerfile(s)"
new_case; pm pnpm@10.33.2; good_stage | df
expect "KNOWN-GOOD: pinned corepack installed before corepack enable" 0 "CLEAN — 1 Dockerfile(s), 2 stage(s); 1 on node 25"
new_case; pm pnpm@10.33.2; good_stage | sed 's/ \&\& corepack enable$/ \&\& corepack enable pnpm/' | df
expect "KNOWN-GOOD, the narrower form: corepack enable pnpm" 0 "CLEAN — 1 Dockerfile(s), 2 stage(s); 1 on node 25"
new_case; pm pnpm@10.33.2; good_stage | sed 's/ \&\& corepack enable$/ \&\& corepack enable pnpx/' | df
expect "corepack enable pnpx: a binary, not a package manager" 1 "Dockerfile:4: corepack enable pnpx names no package manager"
new_case; pm pnpm@10.33.2; good_stage | sed 's/ \&\& corepack enable$/ \&\& corepack enable pnpm yarn/' | df
expect "corepack enable pnpm yarn: two manager names" 0 "CLEAN"
new_case; pm pnpm@10.33.2; good_stage | sed 's/ \&\& corepack enable$/ \&\& corepack enable npm pnpm/' | df
expect "corepack enable npm pnpm: npm is a manager name corepack accepts" 0 "CLEAN"

echo "test-dockerfile-node-toolchain: the oracle's mutations"
new_case; pm pnpm@10.33.2; good_stage | df; good_stage | df Dockerfile.fips; bad_stage | df Dockerfile.stig
expect "install line removed from one file of three" 1 "Dockerfile.stig:4:"
new_case; pm pnpm@10.33.2; good_stage | sed 's/corepack@0\.34\.6/corepack@latest/' | df
expect "pinned version replaced by latest" 1 "installs corepack without an exact version (corepack@latest)"
new_case; pm pnpm@10.33.2; good_stage | sed 's/corepack@0\.34\.6/corepack/' | df
expect "no version at all" 1 "installs corepack without an exact version (corepack@)"
new_case; pm pnpm@10.33.2; good_stage | sed 's/corepack@0\.34\.6/corepack@^0.34.6/' | df
expect "a range is not a pin" 1 "(corepack@^0.34.6)"
new_case; pm pnpm@10.33.2; good_stage | sed 's/corepack@0\.34\.6/corepack@0.34/' | df
expect "a partial version is not a pin" 1 "(corepack@0.34)"
new_case; pm pnpm@10.33.2; df <<'EOF'
FROM node:26-bookworm-slim AS web
RUN corepack enable
RUN npm install -g corepack@0.34.6
EOF
expect "installed after it is used" 1 "Dockerfile:2: stage"

echo "test-dockerfile-node-toolchain: which bases are judged"
new_case; pm pnpm@10.33.2; bad_stage | sed 's/node:26-bookworm-slim/node:24-bookworm-slim/' | df
expect "node 24 still bundles corepack: not judged" 0 "CLEAN"
new_case; pm pnpm@10.33.2; bad_stage | sed 's/node:26-bookworm-slim/node:lts-bookworm-slim/' | df
expect "no version in the tag: judged" 1 "node, version not in the tag"
new_case; pm pnpm@10.33.2; bad_stage | sed 's/node:26-bookworm-slim/node/' | df
expect "untagged node is latest: judged" 1 'Dockerfile:4: stage "web" (FROM node at line 1'
new_case; pm pnpm@10.33.2; bad_stage | sed 's/node:26-bookworm-slim/docker.io\/library\/node:26.1.0-slim@sha256:0000/' | df
expect "registry path, full version and digest" 1 "node 26)"
new_case; pm pnpm@10.33.2; { printf 'ARG NODE_IMAGE=node:26-bookworm-slim\n'; bad_stage | sed 's/^FROM node:26-bookworm-slim AS web$/FROM ${NODE_IMAGE} AS web/'; } | df
expect "base from a global ARG default" 1 "Dockerfile:5: stage"
new_case; pm pnpm@10.33.2; { printf 'ARG NODE_IMAGE\n'; bad_stage | sed 's/^FROM node:26-bookworm-slim AS web$/FROM ${NODE_IMAGE} AS web/'; } | df
expect "unresolved base: judged as node 25 or later" 1 "base not resolved"
new_case; pm pnpm@10.33.2; df <<'EOF'
FROM node:26-bookworm-slim AS base
RUN npm install -g corepack@0.34.6 && corepack enable
FROM base AS web
COPY web/package.json ./web/
RUN cd web && pnpm install
EOF
expect "a stage FROM a stage that installed it inherits the install" 0 "CLEAN"
new_case; df <<'EOF'
FROM golang:1.26.8-bookworm AS build
RUN go version
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/olivares /usr/local/bin/olivares
EOF
expect "no node stage at all" 0 "CLEAN — 1 Dockerfile(s), 2 stage(s); 0 on node 25"

echo "test-dockerfile-node-toolchain: it reads code, not prose"
new_case; pm pnpm@10.33.2; df <<'EOF'
FROM node:26-bookworm-slim AS web
# RUN corepack enable
RUN echo "no package manager here"
EOF
expect "a comment that quotes the old line is not code" 0 "CLEAN"
new_case; pm pnpm@10.33.2; df <<'EOF'
FROM node:26-bookworm-slim AS web
RUN curl -fsS http://example.invalid/#frag && corepack enable
EOF
expect "a # inside a word does not hide the rest of the line" 1 "Dockerfile:2: stage"
new_case; pm pnpm@10.33.2; df <<'EOF'
FROM node:26-bookworm-slim AS web
RUN apt-get update && \
    # corepack comes next
    corepack enable
EOF
expect "continuation with a comment line inside it" 1 "Dockerfile:2: stage"
new_case; pm pnpm@10.33.2; df <<'EOF'
FROM node:26-bookworm-slim AS web
RUN <<SCRIPT
set -e
corepack enable
SCRIPT
EOF
expect "heredoc body" 1 "Dockerfile:2: stage"
new_case; pm pnpm@10.33.2; df <<'EOF'
FROM node:26-bookworm-slim AS web
RUN sh -c "cd web && corepack enable"
EOF
expect "sh -c string" 1 "Dockerfile:2: stage"
new_case; pm pnpm@10.33.2; df <<'EOF'
FROM node:26-bookworm-slim AS web
RUN ["corepack", "enable"]
EOF
expect "exec form" 1 "Dockerfile:2: stage"
new_case; pm pnpm@10.33.2; df <<'EOF'
FROM node:26-bookworm-slim AS web
RUN if ! pnpm --version; then echo missing; fi
EOF
expect "pnpm behind a shell keyword, nothing installed" 1 "runs pnpm, which nothing installed"
new_case; pm pnpm@10.33.2; df <<'EOF'
FROM node:26-bookworm-slim AS web
RUN echo "$(pnpm --version)"
EOF
expect "pnpm inside a command substitution" 1 "runs pnpm, which nothing installed"

new_case; pm pnpm@10.33.2; { printf '\357\273\277'; bad_stage; } | df
expect "a byte order mark before the first FROM hides nothing" 1 'Dockerfile:4: stage "web"'

echo "test-dockerfile-node-toolchain: versions, ARG defaults and packageManager"
new_case; pm pnpm@10.33.2; good_stage | sed 's/^RUN npm install -g corepack@0\.34\.6/ARG COREPACK_VERSION=0.34.6\nRUN npm install -g corepack@${COREPACK_VERSION}/' | df
expect "version from a stage ARG default" 0 "CLEAN"
new_case; pm pnpm@10.33.2; good_stage | sed 's/^RUN npm install -g corepack@0\.34\.6/ARG COREPACK_VERSION\nRUN npm install -g corepack@${COREPACK_VERSION}/' | df
expect "ARG without a default is not a pin" 1 'corepack@${COREPACK_VERSION}'
new_case; pm pnpm@10.33.2; bad_stage | sed 's/^RUN corepack enable$/RUN npm install -g pnpm@10.33.2/' | df
expect "pnpm from npm, equal to packageManager" 0 "CLEAN"
new_case; pm pnpm@10.33.2; bad_stage | sed 's/^RUN corepack enable$/RUN npm install -g pnpm@10.30.0/' | df
expect "pnpm from npm, not the packageManager version" 1 "declares packageManager pnpm@10.33.2"
new_case; pm pnpm@10.33.2; bad_stage | sed 's/^RUN corepack enable$/RUN npm i --global pnpm/' | df
expect "pnpm from npm without a version" 1 "installs pnpm without an exact version (pnpm@)"
new_case; pm -; good_stage | df
expect "corepack route with no packageManager field" 1 "declares packageManager pnpm@<exact version>"
new_case; pm pnpm@10; good_stage | df
expect "corepack route with packageManager pnpm@10" 1 "(found pnpm@10)"
new_case; pm "pnpm@10.33.2+sha512.a90faf6feeab71ad"; good_stage | df
expect "packageManager with its integrity suffix is exact" 0 "CLEAN"
new_case; pm pnpm@10.33.2; bad_stage | sed 's/^RUN corepack enable$/RUN npm install -g corepack@0.34.6 \&\& corepack enable \&\& corepack prepare pnpm@latest --activate/' | df
expect "corepack prepare pnpm@latest" 1 "installs pnpm without an exact version (pnpm@latest)"
new_case; pm pnpm@10.33.2; df <<'EOF'
FROM golang:1.26.8-bookworm AS tools
RUN npm install -g pnpm@latest
EOF
expect "an unpinned install in a non-node stage" 1 "Dockerfile:2: installs pnpm"
new_case; pm pnpm@10.33.2; bad_stage | sed 's/^RUN corepack enable$//; s/pnpm install --frozen-lockfile/npx pnpm@10.33.2 install --frozen-lockfile/; s/pnpm run build/npx pnpm@10.33.2 run build/' | df
expect "npx pnpm with the packageManager version" 0 "CLEAN"
new_case; pm pnpm@10.33.2; df <<'EOF'
FROM node:26-bookworm-slim AS web
RUN npx pnpm install
EOF
expect "npx pnpm without a version" 1 "installs pnpm without an exact version"

echo "test-dockerfile-node-toolchain: corepack forms that pick a pnpm version"
corepack_stage() { # corepack_stage COMMAND — a node:26 stage with a pinned corepack, then COMMAND in web/
	printf 'FROM node:26-bookworm-slim AS web\nRUN npm install -g corepack@0.34.6\nCOPY web/package.json ./web/\nRUN cd web && %s\n' "$1"
}
new_case; pm pnpm@10.33.2; corepack_stage 'corepack pnpm@latest install' | df
expect "corepack pnpm@latest runs an unpinned pnpm" 1 "Dockerfile:4: installs pnpm without an exact version (pnpm@latest)"
new_case; pm pnpm@10.33.2; corepack_stage 'corepack use pnpm@latest && corepack pnpm install' | df
expect "corepack use pnpm@latest writes an unpinned pnpm" 1 "Dockerfile:4: installs pnpm without an exact version (pnpm@latest)"
new_case; pm pnpm@10.33.2; corepack_stage 'corepack up && corepack pnpm install' | df
expect "corepack up is never a pin" 1 "Dockerfile:4: installs pnpm through corepack up"
new_case; pm pnpm@10.33.2; corepack_stage 'corepack use pnpm@10.33.2 && corepack pnpm install' | df
expect "corepack use with an exact version" 0 "CLEAN"
new_case; pm pnpm@10.33.2; corepack_stage 'corepack pnpm@10.33.2 install' | df
expect "corepack pnpm@<exact> install" 0 "CLEAN"
new_case; pm -; corepack_stage 'corepack pnpm install' | df
expect "corepack pnpm with no packageManager to read" 1 "declares packageManager pnpm@<exact version>"
new_case; pm pnpm@10.33.2; corepack_stage 'corepack pnpm install' | df
expect "corepack pnpm reads the exact packageManager" 0 "CLEAN"

echo "test-dockerfile-node-toolchain: ONBUILD triggers and yarn"
new_case; pm pnpm@10.33.2; df <<'EOF'
FROM node:26-bookworm-slim AS web
ONBUILD RUN corepack enable
EOF
expect "ONBUILD RUN corepack on node 26, nothing installed" 1 "Dockerfile:2: stage \"web\""
new_case; pm pnpm@10.33.2; df <<'EOF'
FROM node:26-bookworm-slim AS web
ONBUILD RUN cd web && pnpm install
RUN npm install -g corepack@0.34.6 && corepack enable pnpm
COPY web/package.json ./web/
EOF
expect "ONBUILD runs after the stage, so a later install counts" 0 "CLEAN"
new_case; pm pnpm@10.33.2; df <<'EOF'
FROM node:26-bookworm-slim AS web
COPY web/package.json ./web/
RUN cd web && yarn install
EOF
expect "yarn on node 26, nothing installed" 1 "runs yarn, which nothing installed"
new_case; pm pnpm@10.33.2; good_stage | sed 's/ \&\& corepack enable$/ \&\& corepack enable pnpm/; s/pnpm run build/yarn run build/' | df
expect "corepack enable pnpm creates no yarn" 1 "runs yarn, which nothing installed"
new_case; pm yarn@4.5.0; df <<'EOF'
FROM node:26-bookworm-slim AS web
RUN npm install -g corepack@0.34.6 && corepack enable
COPY web/package.json ./web/
RUN cd web && yarn install
EOF
expect "yarn through corepack with an exact packageManager" 0 "CLEAN"
new_case; pm pnpm@10.33.2; df <<'EOF'
FROM node:26-bookworm-slim AS web
RUN npm install -g yarn@latest
EOF
expect "npm install -g yarn@latest" 1 "installs yarn without an exact version (yarn@latest)"

echo "test-dockerfile-node-toolchain: COULD NOT LOOK is exit 2, never 0"
new_case
expect "no Dockerfile at the root" 2 "COULD NOT LOOK — no Dockerfile* at"
new_case
expect "the root is not a directory" 2 "COULD NOT LOOK" "$dir/absent"
new_case; df <<'EOF'
FROM node:26-bookworm-slim AS web
RUN <<SCRIPT
corepack enable
EOF
expect "an unterminated heredoc" 2 "heredoc <<SCRIPT is never terminated"
new_case; df <<'EOF'
FROM node:26-bookworm-slim AS web
RUN echo "unbalanced && corepack enable
EOF
expect "an unbalanced quote" 2 "unbalanced quote"
new_case; df <<'EOF'
# syntax notes only
ARG NODE_IMAGE=node:26-bookworm-slim
EOF
expect "a Dockerfile with no FROM" 2 "COULD NOT LOOK — Dockerfile: no FROM"
new_case; printf 'FROM node:26-bookworm-slim AS web\n\\\n' | df
expect "a lone escape line" 2 "COULD NOT LOOK — Dockerfile:2:"

echo "test-dockerfile-node-toolchain: $pass ok, $fail FAIL, $n case(s) (expected $EXPECTED_CASES)"
if [ "$n" -ne "$EXPECTED_CASES" ]; then
	echo "test-dockerfile-node-toolchain: the battery ran $n case(s), not $EXPECTED_CASES" >&2
	exit 1
fi
[ "$fail" -eq 0 ]
