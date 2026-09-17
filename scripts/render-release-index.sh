#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Render the deterministic v1 distribution index from authenticated release inputs.
# The caller supplies remote-surface states because only the caller can have measured
# them; this producer validates their vocabulary and completeness, never invents them.
set -uo pipefail
LC_ALL=C
export LC_ALL

ROOT="$(unset CDPATH; cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
SCHEMA="$ROOT/docs/contracts/release-index.schema.json"

fail() { printf 'render-release-index: HALLAZGO — %s\n' "$*" >&2; exit 1; }
blind() { printf 'render-release-index: NO HE PODIDO MIRAR — %s\n' "$*" >&2; exit 2; }
usage() {
	cat >&2 <<'USAGE'
usage: render-release-index.sh --checksums FILE --artifact-dir DIR --commit-file FILE
  --repository OWNER/REPO --version X.Y.Z --channel stable|security|lts
  --state candidate|published --image REPOSITORY@sha256:DIGEST [--image ...]
  --surface NAME=STATUS [--surface ...] --out FILE

Required surfaces: github-release, ota-stable, homebrew, ghcr, docker-hub, helm.
Statuses: staged, published, not-published, not-applicable.
USAGE
	exit 2
}

CHECKSUMS=""
ARTIFACT_DIR=""
COMMIT_FILE=""
REPOSITORY=""
VERSION=""
CHANNEL=""
STATE=""
OUT=""
images=()
surfaces=()

while [ "$#" -gt 0 ]; do
	case "$1" in
	--checksums | --artifact-dir | --commit-file | --repository | --version | --channel | --state | \
		--image | --surface | --out)
		[ "$#" -ge 2 ] || usage
		case "$1" in
		--checksums) CHECKSUMS="$2" ;;
		--artifact-dir) ARTIFACT_DIR="$2" ;;
		--commit-file) COMMIT_FILE="$2" ;;
		--repository) REPOSITORY="$2" ;;
		--version) VERSION="$2" ;;
		--channel) CHANNEL="$2" ;;
		--state) STATE="$2" ;;
		--image) images+=("$2") ;;
		--surface) surfaces+=("$2") ;;
		--out) OUT="$2" ;;
		esac
		shift 2
		;;
	-h | --help) usage ;;
	*) blind "unknown argument: $1" ;;
	esac
done

for tool in jq sha256sum awk sed sort cmp wc grep mktemp; do
	command -v "$tool" >/dev/null 2>&1 || blind "missing required tool: $tool"
done
[ -r "$SCHEMA" ] || blind "release-index schema is missing: $SCHEMA"
jq -e '."$id" == "https://olivares.ai/schemas/release-index-v1.json"' "$SCHEMA" >/dev/null 2>&1 ||
	blind "release-index schema is unreadable or has the wrong identity"
[ -r "$CHECKSUMS" ] || blind "cannot read checksums file: ${CHECKSUMS:-<empty>}"
[ "$(basename -- "$CHECKSUMS")" = checksums.txt ] || blind "checksums input must be named checksums.txt"
[ -d "$ARTIFACT_DIR" ] || blind "artifact directory is missing: ${ARTIFACT_DIR:-<empty>}"
[ -r "$COMMIT_FILE" ] || blind "cannot read commit evidence: ${COMMIT_FILE:-<empty>}"
[ "$(basename -- "$COMMIT_FILE")" = release-commit.txt ] || blind "commit evidence must be named release-commit.txt"
[ -n "$OUT" ] || blind "--out is required"
[ -d "$(dirname -- "$OUT")" ] || blind "output directory does not exist: $(dirname -- "$OUT")"
[[ "$REPOSITORY" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || blind "repository must be OWNER/REPO"
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || blind "version must be MAJOR.MINOR.PATCH"
case "$CHANNEL" in stable | security | lts) ;; *) blind "unsupported channel: ${CHANNEL:-<empty>}" ;; esac
case "$STATE" in candidate | published) ;; *) blind "unsupported index state: ${STATE:-<empty>}" ;; esac
[ "${#images[@]}" -gt 0 ] || blind "at least one immutable image reference is required"

commit_bytes="$(wc -c <"$COMMIT_FILE" | tr -d ' ')" || blind "could not size release-commit.txt"
commit_lines="$(wc -l <"$COMMIT_FILE" | tr -d ' ')" || blind "could not count release-commit.txt lines"
commit="$(sed -n '1p' "$COMMIT_FILE")" || blind "could not read release-commit.txt"
if [ "$commit_bytes" != 41 ] || [ "$commit_lines" != 1 ] || [[ ! "$commit" =~ ^[0-9a-f]{40}$ ]]; then
	fail "release-commit.txt must contain exactly one lowercase 40-hex commit and LF"
fi

W="$(mktemp -d "${TMPDIR:-/tmp}/release-index.XXXXXX")" || blind "cannot create scratch directory"
trap 'rm -rf "$W"' EXIT
: >"$W/names"
: >"$W/artifacts.ndjson"
: >"$W/image-names"
: >"$W/images.ndjson"
: >"$W/surface-names"
: >"$W/surfaces.ndjson"

rows=0
commit_bound=0
while IFS= read -r row || [ -n "$row" ]; do
	[ -n "$row" ] || fail "checksums.txt contains an empty row"
	if [[ ! "$row" =~ ^([0-9a-f]{64})\ \ ([A-Za-z0-9][A-Za-z0-9._+-]*)$ ]]; then
		fail "malformed or unsafe checksum row: $row"
	fi
	digest="${BASH_REMATCH[1]}"
	name="${BASH_REMATCH[2]}"
	if grep -Fxq -- "$name" "$W/names"; then
		fail "checksums.txt contains duplicate artifact name: $name"
	fi
	printf '%s\n' "$name" >>"$W/names"

	case "$name" in
	release-commit.txt)
		path="$COMMIT_FILE"
		commit_bound=1
		kind=evidence
		;;
	olivares_"$VERSION"_*)
		path="$ARTIFACT_DIR/$name"
		case "$name" in
		*.spdx.sbom.json | *.cdx.sbom.json) kind=sbom ;;
		*.tar.gz) kind=archive ;;
		*.deb | *.rpm | *.apk) kind=package ;;
		*) kind=other ;;
		esac
		;;
	*) fail "artifact '$name' does not belong to release $VERSION" ;;
	esac
	if [ ! -f "$path" ] || [ -L "$path" ]; then
		fail "checksummed artifact is missing or is a link: $name"
	fi
	have="$(sha256sum "$path")" || blind "could not hash artifact: $name"
	have="${have%% *}"
	[ "$have" = "$digest" ] || fail "artifact digest differs from checksums.txt: $name"
	size="$(wc -c <"$path" | tr -d ' ')" || blind "could not size artifact: $name"
	case "$size" in '' | *[!0-9]*) blind "artifact size is not numeric: $name" ;; esac
	[ "$size" -gt 0 ] || fail "checksummed artifact is empty: $name"
	url="https://github.com/$REPOSITORY/releases/download/v$VERSION/$name"
	jq -nc --arg kind "$kind" --arg name "$name" --arg sha256 "$digest" \
		--argjson size "$size" --arg url "$url" \
		'{kind:$kind,name:$name,sha256:$sha256,size:$size,url:$url}' >>"$W/artifacts.ndjson" ||
		blind "could not encode artifact: $name"
	rows=$((rows + 1))
done <"$CHECKSUMS"
[ "$rows" -gt 0 ] || fail "checksums.txt contains zero artifacts"
[ "$commit_bound" = 1 ] || fail "checksums.txt does not bind release-commit.txt"

for ref in "${images[@]}"; do
	if [[ ! "$ref" =~ ^[A-Za-z0-9.-]+(/[A-Za-z0-9._-]+)+@sha256:[0-9a-f]{64}$ ]]; then
		blind "malformed immutable image reference: $ref"
	fi
	if grep -Fxq -- "$ref" "$W/image-names"; then
		fail "duplicate image reference: $ref"
	fi
	printf '%s\n' "$ref" >>"$W/image-names"
	digest="${ref##*@}"
	jq -nc --arg reference "$ref" --arg digest "$digest" \
		'{reference:$reference,digest:$digest}' >>"$W/images.ndjson" ||
		blind "could not encode image reference: $ref"
done

required_surfaces=$'docker-hub\nghcr\ngithub-release\nhelm\nhomebrew\nota-stable'
for item in "${surfaces[@]}"; do
	name="${item%%=*}"
	status="${item#*=}"
	if [ "$name" = "$item" ] || [ -z "$status" ]; then
		blind "surface must be NAME=STATUS: $item"
	fi
	case "$name" in
	docker-hub | ghcr | github-release | helm | homebrew | ota-stable) ;;
	*) blind "unknown surface: $name" ;;
	esac
	case "$status" in
	staged | published | not-published | not-applicable) ;;
	*) blind "unsupported status for $name: $status" ;;
	esac
	if grep -Fxq -- "$name" "$W/surface-names"; then
		fail "duplicate surface: $name"
	fi
	printf '%s\n' "$name" >>"$W/surface-names"
	jq -nc --arg name "$name" --arg status "$status" \
		'{name:$name,status:$status}' >>"$W/surfaces.ndjson" ||
		blind "could not encode surface: $name"
done
printf '%s\n' "$required_surfaces" >"$W/required-surfaces"
sort "$W/surface-names" >"$W/surface-names.sorted"
if ! cmp -s "$W/required-surfaces" "$W/surface-names.sorted"; then
	echo "required surfaces:" >&2; sed 's/^/  /' "$W/required-surfaces" >&2
	echo "provided surfaces:" >&2; sed 's/^/  /' "$W/surface-names.sorted" >&2
	fail "surface inventory is incomplete or contains an unexpected name"
fi

checksums_sum="$(sha256sum "$CHECKSUMS")" || blind "could not hash checksums.txt"
checksums_sum="${checksums_sum%% *}"
if ! jq -S -n \
	--arg schema 'olivares.ai/release-index/v1' \
	--arg state "$STATE" \
	--arg version "$VERSION" \
	--arg tag "v$VERSION" \
	--arg commit "$commit" \
	--arg channel "$CHANNEL" \
	--arg repository "$REPOSITORY" \
	--arg checksums_sha256 "$checksums_sum" \
	--slurpfile artifacts "$W/artifacts.ndjson" \
	--slurpfile images "$W/images.ndjson" \
	--slurpfile surfaces "$W/surfaces.ndjson" \
	'{
	  schema:$schema,
	  schema_version:1,
	  state:$state,
	  version:$version,
	  tag:$tag,
	  commit:$commit,
	  channel:$channel,
	  repository:$repository,
	  generated_from:{checksums:"checksums.txt",checksums_sha256:$checksums_sha256},
	  install_layout:{
	    schema:"olivares.ai/install-layout/v1",
	    system:{
	      binary:["/opt/olivares","/opt/olivares/bin/olivares","/usr/bin/olivares","/usr/local/bin/olivares"],
	      config:["/Library/Preferences/dev.olivares.olivares.env","/etc/olivares/olivares.env"],
	      data:["/Library/Application Support/Olivares","/var/lib/olivares"],
	      unit:["/Library/LaunchDaemons/dev.olivares.olivares.plist","/etc/init.d/olivares","/etc/systemd/system/olivares.service","/usr/lib/systemd/system/olivares.service"]
	    },
	    user:{
	      binary_suffix:["/.local/bin/olivares"],
	      config_suffix:["/.config/olivares/olivares.env","/Library/Preferences/dev.olivares.olivares.env"],
	      data_suffix:["/.local/share/olivares","/Library/Application Support/Olivares"],
	      unit_suffix:["/.config/systemd/user/olivares.service","/Library/LaunchAgents/dev.olivares.olivares.plist"]
	    }
	  },
	  artifacts:($artifacts|sort_by(.name)),
	  images:($images|sort_by(.reference)),
	  surfaces:($surfaces|sort_by(.name))
	}' >"$W/index.json"; then
	blind "jq could not render the release index"
fi
mv "$W/index.json" "$OUT" || blind "could not write release index: $OUT"
printf 'render-release-index: OK — %d artifact(s), %d image(s), 6 surface(s) -> %s\n' \
	"$rows" "${#images[@]}" "$OUT"
