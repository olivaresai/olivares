#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# assemble.sh — one format, named by the declaration and by nothing else.
#
# It is the single entry the Taskfile calls, so a format that is not declared in formats.json
# cannot be built by typing it: the declaration is the list, and the three scripts beside this
# one are what it maps to.
#
# usage: assemble.sh --format iso|qcow2|ova [--target-dir DIR] [--output-dir DIR]
#        assemble.sh --list
set -euo pipefail
assembly=assemble.sh
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=appliance/images/formats/common.sh
. "$here/common.sh"

format=""
passthrough=()
while [ $# -gt 0 ]; do
  case $1 in
    --format) format=${2:?}; shift 2 ;;
    --list)
      python3 - "$formats_json" <<'LIST'
import json, sys
body = "\n".join(l for l in open(sys.argv[1]).read().splitlines() if not l.strip().startswith("//"))
for artifact in json.loads(body)["artifacts"]:
    print(artifact["format"])
LIST
      exit 0 ;;
    --target-dir|--output-dir) passthrough+=("$1" "${2:?}"); shift 2 ;;
    *) unmeasurable "unknown option: $1" ;;
  esac
done

[ -n "$format" ] || unmeasurable "name a format: --format iso|qcow2|ova"
script=$(formats_query "$format" script) || unmeasurable "formats.json declares no format $format"
[ -x "$here/$script" ] || unmeasurable "the assembly of $format is not executable: $here/$script"
exec bash "$here/$script" "${passthrough[@]+"${passthrough[@]}"}"
