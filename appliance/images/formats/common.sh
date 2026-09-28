#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# common.sh — what the three format assemblies share: the declaration they all read, the
# ceiling every artifact is measured against, and the manifest each one writes beside itself.
#
# It is sourced, not run. Each assembly takes the disk KIWI built and produces ONE file and
# ONE manifest; no assembly invents a boot chain, and none of them is done by hand outside
# this directory, which is the whole reason the directory exists.

formats_here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
formats_json="$formats_here/formats.json"

fail() { printf '%s: %s\n' "${assembly:-formats}" "$*" >&2; exit 1; }
unmeasurable() { printf '%s: %s\n' "${assembly:-formats}" "$*" >&2; exit 2; }

require_tool() {
  local tool
  for tool in "$@"; do
    command -v "$tool" >/dev/null 2>&1 || unmeasurable "$tool is not available, so this artifact cannot be assembled"
  done
}

# formats_query FORMAT KEY — one field of the declaration. The document carries // comment
# lines, which the recipe's shape gate strips the same way.
formats_query() {
  python3 - "$formats_json" "$1" "$2" <<'PY'
import json, sys
path, fmt, key = sys.argv[1], sys.argv[2], sys.argv[3]
body = "\n".join(l for l in open(path).read().splitlines() if not l.strip().startswith("//"))
document = json.loads(body)
if fmt == "":
    print(document[key])
else:
    for artifact in document["artifacts"]:
        if artifact["format"] == fmt:
            print(artifact[key])
            break
    else:
        raise SystemExit("formats.json declares no format " + fmt)
PY
}

# enforce_release_ceiling FILE FORMAT — the artifact is at or under the ceiling its own
# declaration gives it. A file that passes it is a build failure here and not a surprise at
# upload time: while distribution is GitHub only, a release file may not pass 2 GiB, and an
# edition that cannot fit says so in formats.json by declaring itself split.
enforce_release_ceiling() {
  local file=$1 format=$2 bytes ceiling split
  bytes=$(stat -c %s "$file")
  split=$(formats_query "$format" split)
  if [ "$split" = "True" ] || [ "$split" = "true" ]; then
    ceiling=$(formats_query "$format" split_part_max_bytes)
  else
    ceiling=$(formats_query "$format" max_bytes)
  fi
  if [ "$bytes" -gt "$ceiling" ]; then
    fail "$(basename "$file") is $bytes bytes, over the $ceiling the declaration allows it: either the package set shrinks or formats.json declares this format split, with its parts"
  fi
  printf '%s: %s is %s bytes, under its ceiling of %s\n' "${assembly:-formats}" "$(basename "$file")" "$bytes" "$ceiling"
}

# find_disk TARGET_DIR — the one raw disk KIWI left. Two of them, or none, is not a build the
# formats may guess about.
find_disk() {
  local target=$1 disks=()
  while IFS= read -r file; do disks+=("$file"); done < <(find "$target" -maxdepth 1 -name '*.raw' | sort)
  [ ${#disks[@]} -eq 1 ] || unmeasurable "expected exactly one .raw disk in $target, found ${#disks[@]}"
  printf '%s\n' "${disks[0]}"
}

# find_install_iso TARGET_DIR — the one installer medium KIWI left.
find_install_iso() {
  local target=$1 media=()
  while IFS= read -r file; do media+=("$file"); done < <(find "$target" -maxdepth 1 -name '*.install.iso' | sort)
  [ ${#media[@]} -eq 1 ] || unmeasurable "expected exactly one .install.iso in $target, found ${#media[@]}"
  printf '%s\n' "${media[0]}"
}

# write_manifest FILE FORMAT SOURCE [EXTRA_JSON] — the artifact's own manifest, beside it.
# A3 signs these and puts them in the release; here they are what lets two builds be compared
# and what says which disk an artifact was made from.
write_manifest() {
  local file=$1 format=$2 source=$3 extra=${4:-}
  local manifest="$file.manifest.json"
  {
    printf '{\n'
    printf '  "schema": "olivares-appliance-artifact/v1",\n'
    printf '  "format": "%s",\n' "$format"
    printf '  "product": "%s",\n' "$(formats_query "" product)"
    printf '  "vendor": "%s",\n' "$(formats_query "" vendor)"
    printf '  "edition": "%s",\n' "$(formats_query "" edition)"
    printf '  "arch": "%s",\n' "$(formats_query "" arch)"
    printf '  "file": "%s",\n' "$(basename "$file")"
    printf '  "bytes": %s,\n' "$(stat -c %s "$file")"
    printf '  "sha256": "%s",\n' "$(sha256sum "$file" | cut -d' ' -f1)"
    printf '  "source": {"file": "%s", "sha256": "%s"},\n' \
      "$(basename "$source")" "$(sha256sum "$source" | cut -d' ' -f1)"
    printf '  "assembled_at": "%s",\n' "$(date -u +%FT%TZ)"
    [ -n "$extra" ] && printf '%s,\n' "$extra"
    printf '  "signed": false\n'
    printf '}\n'
  } > "$manifest"
  printf '%s: wrote %s\n' "${assembly:-formats}" "$(basename "$manifest")"
}

# deliver SOURCE DESTINATION — the artifact under the name the declaration gives it, without
# a second copy of a gigabyte where the filesystem can avoid one.
deliver() {
  ln -f "$1" "$2" 2>/dev/null || cp --reflink=auto "$1" "$2"
}
