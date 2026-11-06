#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Normal optional-module selection for authenticated smoke/capture journeys.
# TOKEN and TENANT remain shell variables; the bearer is read by curl on stdin.
enable_console_modules() (
  set -euo pipefail
  umask 077
  local base="$1" scratch status deadline ready
  shift
  scratch="$(mktemp -d)" || return 1
  trap 'rm -rf "$scratch"' EXIT
  # The first read lands while the freshly booted engine is still busy: 0.38 s on a CI
  # runner at load 13-23 on 8 threads, where later reads take 0.02-0.03 s. At load ~120 it
  # passed the old 5 s, so each request gets 30 s; a hang still ends at the 120 s deadlines.
  local request_s=30
  module_state() {
    printf 'Authorization: Bearer %s\n' "$TOKEN" | curl -sS --max-time "$request_s" \
      "$base/v1/console/modules" -H @- -H "X-Olivares-Tenant: $TENANT" \
      -o "$scratch/state.json" -w '%{http_code}'
  }
  # read_state WHAT: 0 when the state read answered 200, 1 when the engine is busy or
  # restarting (refused, timed out, 503); any other answer ends the helper.
  read_state() {
    local transport=0
    status="$(module_state)" || transport=$?
    case "$transport:$status" in
      0:200) return 0 ;;
      0:503 | 7:* | 28:*) return 1 ;;
      0:*) echo "ERROR: $1 returned HTTP $status" >&2; exit 1 ;;
      *) echo "ERROR: $1 transport failed (curl $transport)" >&2; exit 1 ;;
    esac
  }
  engine_alive() {
    if [ -n "${PID:-}" ] && ! kill -0 "$PID" 2>/dev/null; then
      echo "ERROR: owned engine exited while $1" >&2; exit 1
    fi
  }
  deadline=$((SECONDS + 120))
  until read_state 'module selection'; do
    engine_alive 'reading the module selection'
    [ "$SECONDS" -lt "$deadline" ] || { echo 'ERROR: the module selection never answered' >&2; return 1; }
    sleep 0.5
  done
  python3 - "$scratch/state.json" "$@" > "$scratch/selection.json" <<'PYSELECT' || { echo 'ERROR: invalid module selection or unavailable requested module' >&2; return 1; }
import json,sys
modules=json.load(open(sys.argv[1]))["modules"]
known={m["name"] for m in modules}
requested=sys.argv[2:]
if not set(requested).issubset(known):
    sys.exit("requested smoke modules are missing from the catalog")
selected=[m["name"] for m in modules if m["selected"]]
for name in requested:
    if name not in selected: selected.append(name)
json.dump({"selected":selected},sys.stdout)
PYSELECT
  status="$(printf 'Authorization: Bearer %s\n' "$TOKEN" | curl -sS --max-time "$request_s" \
    -X PUT "$base/v1/console/modules" -H @- -H "X-Olivares-Tenant: $TENANT" \
    -H 'Content-Type: application/json' --data-binary @"$scratch/selection.json" \
    -o "$scratch/update.json" -w '%{http_code}')" \
    || { echo 'ERROR: module selection update transport failed' >&2; return 1; }
  [ "$status" = 200 ] || { echo "ERROR: module selection update returned HTTP $status" >&2; return 1; }
  deadline=$((SECONDS + 120))
  while [ "$SECONDS" -lt "$deadline" ]; do
    engine_alive 'applying module selection'
    if read_state 'module readiness'; then
      ready=0
      python3 - "$scratch/state.json" "$@" <<'PYRUNNING' || ready=$?
import json,sys
try:
    running={m["name"] for m in json.load(open(sys.argv[1]))["modules"] if m["running"]}
except (ValueError, KeyError, TypeError):
    sys.exit(2)
sys.exit(not set(sys.argv[2:]).issubset(running))
PYRUNNING
      case "$ready" in
        0) return 0 ;;
        1) ;;
        *) echo 'ERROR: invalid module readiness response' >&2; return 1 ;;
      esac
    fi
    sleep 0.5
  done
  echo 'ERROR: requested smoke modules never became running after restart' >&2
  return 1
)
