#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Re-derive a release index from trusted expectations and compare every byte.
# The index is never allowed to supply its own expected version, images or states.
set -uo pipefail
LC_ALL=C
export LC_ALL

ROOT="$(unset CDPATH; cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
RENDER="$ROOT/scripts/render-release-index.sh"
INDEX=""
render_args=()

fail() { printf 'check-release-index: HALLAZGO — %s\n' "$*" >&2; exit 1; }
blind() { printf 'check-release-index: NO HE PODIDO MIRAR — %s\n' "$*" >&2; exit 2; }
usage() {
	cat >&2 <<'USAGE'
usage: check-release-index.sh --index FILE <all render-release-index inputs except --out>
USAGE
	exit 2
}

while [ "$#" -gt 0 ]; do
	case "$1" in
	--index)
		[ "$#" -ge 2 ] || usage
		INDEX="$2"
		shift 2
		;;
	--checksums | --artifact-dir | --commit-file | --repository | --version | --channel | --state | --image | --surface)
		[ "$#" -ge 2 ] || usage
		render_args+=("$1" "$2")
		shift 2
		;;
	-h | --help) usage ;;
	*) blind "unknown argument: $1" ;;
	esac
done

[ -x "$RENDER" ] || blind "release-index generator is missing or not executable: $RENDER"
if [ ! -f "$INDEX" ] || [ -L "$INDEX" ]; then
	blind "index is missing or is a link: ${INDEX:-<empty>}"
fi
for tool in jq cmp diff mktemp; do
	command -v "$tool" >/dev/null 2>&1 || blind "missing required tool: $tool"
done

# This is the executable subset of docs/contracts/release-index.schema.json. Exact
# regeneration below closes values and order; this block keeps malformed documents
# attributable even if a future renderer exits before producing a comparison file.
if ! jq -e '
  type == "object" and
  ((keys | sort) == ([
    "artifacts","channel","commit","generated_from","images","install_layout","repository",
    "schema","schema_version","state","surfaces","tag","version"
  ] | sort)) and
  .schema == "olivares.ai/release-index/v1" and .schema_version == 1 and
  (.state == "candidate" or .state == "published") and
  (.version | type == "string" and test("^[0-9]+\\.[0-9]+\\.[0-9]+$")) and
  .tag == ("v" + .version) and
  (.commit | type == "string" and test("^[0-9a-f]{40}$")) and
  (.channel == "stable" or .channel == "security" or .channel == "lts") and
  (.repository | type == "string" and test("^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$")) and
  .generated_from.checksums == "checksums.txt" and
  (.generated_from.checksums_sha256 | test("^[0-9a-f]{64}$")) and
  ((.install_layout | keys | sort) == ["schema","system","user"]) and
  .install_layout.schema == "olivares.ai/install-layout/v1" and
  ((.install_layout.system | keys | sort) == ["binary","config","data","unit"]) and
  .install_layout.system.binary == ["/opt/olivares","/opt/olivares/bin/olivares","/usr/bin/olivares","/usr/local/bin/olivares"] and
  .install_layout.system.config == ["/Library/Preferences/dev.olivares.olivares.env","/etc/olivares/olivares.env"] and
  .install_layout.system.data == ["/Library/Application Support/Olivares","/var/lib/olivares"] and
  .install_layout.system.unit == ["/Library/LaunchDaemons/dev.olivares.olivares.plist","/etc/init.d/olivares","/etc/systemd/system/olivares.service","/usr/lib/systemd/system/olivares.service"] and
  ((.install_layout.user | keys | sort) == ["binary_suffix","config_suffix","data_suffix","unit_suffix"]) and
  .install_layout.user.binary_suffix == ["/.local/bin/olivares"] and
  .install_layout.user.config_suffix == ["/.config/olivares/olivares.env","/Library/Preferences/dev.olivares.olivares.env"] and
  .install_layout.user.data_suffix == ["/.local/share/olivares","/Library/Application Support/Olivares"] and
  .install_layout.user.unit_suffix == ["/.config/systemd/user/olivares.service","/Library/LaunchAgents/dev.olivares.olivares.plist"] and
  (.artifacts | type == "array" and length > 0) and
  ([.artifacts[].name] | length == (unique | length)) and
  (all(.artifacts[];
    ((keys | sort) == ["kind","name","sha256","size","url"]) and
    (.kind == "archive" or .kind == "package" or .kind == "sbom" or .kind == "evidence" or .kind == "other") and
    (.name | test("^[A-Za-z0-9][A-Za-z0-9._+-]*$")) and
    (.sha256 | test("^[0-9a-f]{64}$")) and
    (.size | type == "number" and . > 0 and floor == .) and
    (.url | type == "string"))) and
  (.images | type == "array" and length > 0) and
  ([.images[].reference] | length == (unique | length)) and
  (all(.images[];
    ((keys | sort) == ["digest","reference"]) and
    (.reference | test("^[A-Za-z0-9.-]+(/[A-Za-z0-9._-]+)+@sha256:[0-9a-f]{64}$")) and
    (.digest | test("^sha256:[0-9a-f]{64}$")) and
    (.digest as $digest | .reference | endswith("@" + $digest)))) and
  (.surfaces | type == "array" and length == 6) and
  ([.surfaces[].name] | sort == ["docker-hub","ghcr","github-release","helm","homebrew","ota-stable"]) and
  (all(.surfaces[];
    ((keys | sort) == ["name","status"]) and
    (.status == "staged" or .status == "published" or .status == "not-published" or .status == "not-applicable")))
' "$INDEX" >/dev/null 2>&1; then
	fail "index does not satisfy the executable v1 schema contract: $INDEX"
fi

W="$(mktemp -d "${TMPDIR:-/tmp}/check-release-index.XXXXXX")" || blind "cannot create scratch directory"
trap 'rm -rf "$W"' EXIT
set +e
bash "$RENDER" "${render_args[@]}" --out "$W/expected.json" >"$W/render.out" 2>"$W/render.err"
render_rc=$?
set -e
case "$render_rc" in
0) ;;
1)
	sed 's/^/  /' "$W/render.err" >&2
	fail "release inputs no longer form the expected index"
	;;
2)
	sed 's/^/  /' "$W/render.err" >&2
	blind "could not re-derive the expected index"
	;;
*)
	sed 's/^/  /' "$W/render.err" >&2
	blind "generator returned unexpected rc=$render_rc"
	;;
esac

if ! cmp -s "$INDEX" "$W/expected.json"; then
	echo "check-release-index: expected index differs (first 80 diff lines):" >&2
	diff -u "$INDEX" "$W/expected.json" | sed -n '1,80p' >&2 || true
	fail "release-index.json drifted from authenticated inputs or explicit surface expectations"
fi
printf 'check-release-index: OK — %s is byte-exactly derived from the release inputs\n' "$INDEX"
