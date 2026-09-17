#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Mutation battery for the deterministic release distribution index.
set -uo pipefail
LC_ALL=C
export LC_ALL

ROOT="$(unset CDPATH; cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
RENDER="$ROOT/scripts/render-release-index.sh"
CHECK="$ROOT/scripts/check-release-index.sh"
SCHEMA="$ROOT/docs/contracts/release-index.schema.json"
blind() { printf 'test-release-index: NO HE PODIDO MIRAR — %s\n' "$*" >&2; exit 2; }

for f in "$RENDER" "$CHECK" "$SCHEMA"; do [ -r "$f" ] || blind "missing $f"; done
for tool in bash jq sha256sum cmp sed awk grep; do
	command -v "$tool" >/dev/null 2>&1 || blind "missing required tool: $tool"
done

W="$(mktemp -d "${TMPDIR:-/tmp}/test-release-index.XXXXXX")" || blind "cannot create scratch directory"
trap 'rm -rf "$W"' EXIT
DIST="$W/dist"
mkdir -p "$DIST"
VER=26.9.0
COMMIT=0123456789abcdef0123456789abcdef01234567
IMAGE_DIGEST=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
printf '%s\n' "$COMMIT" >"$W/release-commit.txt"

names=(
	"olivares_${VER}_linux_amd64.tar.gz"
	"olivares_${VER}_linux_amd64.deb"
	"olivares_${VER}_linux_amd64.rpm"
	"olivares_${VER}_linux_amd64.apk"
	"olivares_${VER}_linux_amd64.tar.gz.spdx.sbom.json"
)
for name in "${names[@]}"; do
	printf 'fixture bytes for %s\n' "$name" >"$DIST/$name"
done

write_checksums() {
	: >"$DIST/checksums.txt"
	# Deliberately not lexical order: deterministic output must not inherit producer order.
	for name in "${names[@]}"; do
		printf '%s  %s\n' "$(sha256sum "$DIST/$name" | awk '{print $1}')" "$name" >>"$DIST/checksums.txt"
	done
	printf '%s  release-commit.txt\n' \
		"$(sha256sum "$W/release-commit.txt" | awk '{print $1}')" >>"$DIST/checksums.txt"
}
write_checksums

ARGS=(
	--checksums "$DIST/checksums.txt"
	--artifact-dir "$DIST"
	--commit-file "$W/release-commit.txt"
	--repository olivaresai/olivares
	--version "$VER"
	--channel stable
	--state candidate
	--image "ghcr.io/olivaresai/olivares@sha256:$IMAGE_DIGEST"
	--image "docker.io/olivaresai/olivares@sha256:$IMAGE_DIGEST"
	--surface github-release=staged
	--surface ota-stable=staged
	--surface homebrew=staged
	--surface ghcr=published
	--surface docker-hub=staged
	--surface helm=not-published
)

pass=0
failed=0
ok() { printf '  ok  %-40s rc=%s\n' "$1" "$2"; pass=$((pass + 1)); }
bad() {
	printf '  ⛔ %-40s %s\n' "$1" "$2"
	sed -n '1,12p' "$W/out" | sed 's/^/       /'
	failed=$((failed + 1))
}
run_render() {
	local out="$1"
	set +e
	TMPDIR="$W/tmp" bash "$RENDER" "${ARGS[@]}" --out "$out" >"$W/out" 2>&1
	rc=$?
	set -e
}
run_check() {
	local index="$1"
	set +e
	TMPDIR="$W/tmp" bash "$CHECK" --index "$index" "${ARGS[@]}" >"$W/out" 2>&1
	rc=$?
	set -e
}
mkdir -p "$W/tmp"

run_render "$W/index.json"
artifacts="$(jq '.artifacts | length' "$W/index.json" 2>/dev/null || printf 0)"
packages="$(jq '[.artifacts[] | select(.kind == "package")] | length' "$W/index.json" 2>/dev/null || printf 0)"
if [ "$rc" = 0 ] && [ "$artifacts" = 6 ] && [ "$packages" = 3 ] &&
	jq -e '.commit == "0123456789abcdef0123456789abcdef01234567" and
	  .surfaces[0].name == "docker-hub" and
	  .install_layout.schema == "olivares.ai/install-layout/v1" and
	  (.install_layout.system.data | index("/var/lib/olivares")) != null and
	  (.install_layout.user.data_suffix | index("/.local/share/olivares")) != null' \
	  "$W/index.json" >/dev/null; then
	ok "positive: complete sorted index" "$rc"
else
	bad "positive: complete sorted index" "rc=$rc artifacts=$artifacts packages=$packages"
fi

run_check "$W/index.json"
if [ "$rc" = 0 ]; then
	ok "positive: re-derivation is exact" "$rc"
else
	bad "positive: re-derivation is exact" "rc=$rc"
fi

run_render "$W/index-second.json"
if [ "$rc" = 0 ] && cmp -s "$W/index.json" "$W/index-second.json"; then
	ok "deterministic second render" "$rc"
else
	bad "deterministic second render" "rc=$rc or bytes differ"
fi

# Drift mutant: the authenticated name stays the same but the release byte changes.
cp "$DIST/${names[0]}" "$W/archive.good"
printf 'mutant\n' >>"$DIST/${names[0]}"
run_check "$W/index.json"
if [ "$rc" = 1 ] && grep -q 'artifact digest differs' "$W/out"; then
	ok "mutant: artifact byte drift" "$rc"
else
	bad "mutant: artifact byte drift" "rc=$rc, expected named digest refusal"
fi
mv "$W/archive.good" "$DIST/${names[0]}"

# Claim mutant: no remote was measured again, but the index says Helm is published.
jq '(.surfaces[] | select(.name == "helm") | .status) = "published"' \
	"$W/index.json" >"$W/index-surface-mutant.json"
run_check "$W/index-surface-mutant.json"
if [ "$rc" = 1 ] && grep -q 'drifted' "$W/out"; then
	ok "mutant: surface state drift" "$rc"
else
	bad "mutant: surface state drift" "rc=$rc, expected exact-regeneration refusal"
fi

# Inventory mutant: a valid-looking artifact is silently omitted from the index.
jq 'del(.artifacts[0])' "$W/index.json" >"$W/index-omission-mutant.json"
run_check "$W/index-omission-mutant.json"
if [ "$rc" = 1 ] && grep -q 'drifted' "$W/out"; then
	ok "mutant: artifact omitted from index" "$rc"
else
	bad "mutant: artifact omitted from index" "rc=$rc, expected exact-regeneration refusal"
fi

cp "$DIST/checksums.txt" "$W/checksums.good"
sed -n '1p' "$W/checksums.good" >>"$DIST/checksums.txt"
run_render "$W/duplicate.json"
if [ "$rc" = 1 ] && grep -q 'duplicate artifact name' "$W/out"; then
	ok "negative: duplicate checksum name" "$rc"
else
	bad "negative: duplicate checksum name" "rc=$rc, expected duplicate refusal"
fi
cp "$W/checksums.good" "$DIST/checksums.txt"

printf '%064d  ../outside.tar.gz\n' 0 >>"$DIST/checksums.txt"
run_render "$W/traversal.json"
if [ "$rc" = 1 ] && grep -q 'malformed or unsafe' "$W/out"; then
	ok "negative: checksum path traversal" "$rc"
else
	bad "negative: checksum path traversal" "rc=$rc, expected unsafe-name refusal"
fi
cp "$W/checksums.good" "$DIST/checksums.txt"

awk '$2 != "release-commit.txt"' "$W/checksums.good" >"$DIST/checksums.txt"
run_render "$W/unbound.json"
if [ "$rc" = 1 ] && grep -q 'does not bind release-commit.txt' "$W/out"; then
	ok "negative: commit evidence unbound" "$rc"
else
	bad "negative: commit evidence unbound" "rc=$rc, expected commit binding refusal"
fi
cp "$W/checksums.good" "$DIST/checksums.txt"

printf 'test-release-index: %d ok, %d failed\n' "$pass" "$failed"
[ "$failed" -eq 0 ] || exit 1
