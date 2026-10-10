# shellcheck shell=bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Shared setup for the two live browser runners. ROOT/DATA belong to the caller;
# seed after its demo login has resolved PORT, TOKEN and TENANT.

prepare_conversation_fixture() {
  mkdir -p "$DATA/claude-config" "$DATA/claude-home" "$DATA/ws"
  printf 'fixture-dummy-bearer-not-a-secret' >"$DATA/session-token"
  export OLIVARES_SESSION_RUNTIME_CLAUDE_BIN="$ROOT/web/e2e/fixtures/conversation-driver.py"
  export OLIVARES_SESSION_RUNTIME_TOKEN_FILE="$DATA/session-token"
  export OLIVARES_SESSION_RUNTIME_TOKEN_TTL=15m
  export CONVERSATION_FIXTURE=1
}

seed_conversation_fixture() {
  local fixture
  fixture="$(FIXTURE_TOKEN="$TOKEN" FIXTURE_TENANT="$TENANT" \
    FIXTURE_DATA="$DATA" FIXTURE_BASE="http://127.0.0.1:$PORT" python3 - <<'PY'
import json
import os
import urllib.request

base = os.environ["FIXTURE_BASE"]
data = os.environ["FIXTURE_DATA"]
headers = {
    "Authorization": "Bearer " + os.environ["FIXTURE_TOKEN"],
    "X-Olivares-Tenant": os.environ["FIXTURE_TENANT"],
    "Content-Type": "application/json",
}

def post(path, body):
    request = urllib.request.Request(
        base + "/v1/m/sessions/" + path,
        data=json.dumps(body).encode(), headers=headers, method="POST",
    )
    with urllib.request.urlopen(request, timeout=20) as response:
        return json.load(response)

profile = post("provider-profiles", {
    "driver": "claude",
    "config_home": os.path.join(data, "claude-config"),
    "user_home": os.path.join(data, "claude-home"),
    "display_name": "Fixture Claude",
})
workspace = post("workspaces", {
    "name": "Conversation fixture", "root_path": os.path.join(data, "ws"),
    "mount_mode": "rw", "dlp_mode": "off",
})
run = post("runs", {
    "name": "conversation-fixture", "transport": "stream-json",
    "permission_mode": "default", "isolation": "native",
    "provider_profile_ref": profile["profile_ref"],
    "workspace_ref": workspace["workspace_ref"],
})
if run.get("state") != "running" or not run.get("run_ref") or not run.get("name"):
    raise SystemExit("conversation fixture did not return a named running session")
print(json.dumps({"run_ref": run["run_ref"], "name": run["name"]}))
PY
  )"
  E2E_CAPTURE_RUN_REF="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["run_ref"])' <<<"$fixture")"
  E2E_CLI_RUN_NAME="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["name"])' <<<"$fixture")"
  export E2E_CAPTURE_RUN_REF E2E_CLI_RUN_NAME
}
