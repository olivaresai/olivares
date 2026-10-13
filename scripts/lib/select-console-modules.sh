#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Normal optional-module selection for authenticated smoke/capture journeys.
# TOKEN and TENANT remain shell variables; the bearer is read by curl on stdin.
enable_console_modules() (
  set -euo pipefail
  umask 077
  local base="$1" scratch status deadline transport ready
  shift
  scratch="$(mktemp -d)" || return 1
  trap 'rm -rf "$scratch"' EXIT
  module_state() {
    printf 'Authorization: Bearer %s\n' "$TOKEN" | curl -sS --max-time 5 \
      "$base/v1/console/modules" -H @- -H "X-Olivares-Tenant: $TENANT" \
      -o "$scratch/state.json" -w '%{http_code}'
  }
  status="$(module_state)" || { echo 'ERROR: module selection transport failed' >&2; return 1; }
  [ "$status" = 200 ] || { echo "ERROR: module selection returned HTTP $status" >&2; return 1; }
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
  status="$(printf 'Authorization: Bearer %s\n' "$TOKEN" | curl -sS --max-time 10 \
    -X PUT "$base/v1/console/modules" -H @- -H "X-Olivares-Tenant: $TENANT" \
    -H 'Content-Type: application/json' --data-binary @"$scratch/selection.json" \
    -o "$scratch/update.json" -w '%{http_code}')" \
    || { echo 'ERROR: module selection update transport failed' >&2; return 1; }
  [ "$status" = 200 ] || { echo "ERROR: module selection update returned HTTP $status" >&2; return 1; }
  deadline=$((SECONDS + 120))
  while [ "$SECONDS" -lt "$deadline" ]; do
    if [ -n "${PID:-}" ] && ! kill -0 "$PID" 2>/dev/null; then
      echo 'ERROR: owned engine exited while applying module selection' >&2; return 1
    fi
    transport=0
    status="$(module_state)" || transport=$?
    case "$transport" in
      0)
        case "$status" in
          200)
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
            ;;
          503) ;; # The engine can be temporarily unavailable while restarting.
          *) echo "ERROR: module readiness returned HTTP $status" >&2; return 1 ;;
        esac
        ;;
      7|28) ;; # A refused connection or timeout can accompany the restart.
      *) echo "ERROR: module readiness transport failed (curl $transport)" >&2; return 1 ;;
    esac
    sleep 0.5
  done
  echo 'ERROR: requested smoke modules never became running after restart' >&2
  return 1
)
