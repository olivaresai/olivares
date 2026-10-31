#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Run the demo dataset against a real engine, twice, and prove the second pass changes nothing.
# The offline battery (scripts/test-demo-dataset.py) pins the stand-ins and the seeder's decisions; this is
# the product contract: the routes accept what the seeder sends, the stand-ins launch as real session
# children, and the canonical estate reads back: seven live and nine finished, $6.84 today, one unpriced,
# three approvals waiting, and no Business request kind on Community.
# Needs a built binary and loopback ports; it is heavy enough to run through the heavy-job wrapper:
#
#   OLIVARES_BIN=/path/to/olivares bash scripts/test-demo-dataset-live.sh
#
# The engine starts as a fresh store and first-boot setup mints the same demo login. --seed-demo is not
# used because it also writes synthetic observed sessions into the same tenant, and the home would count
# them beside the canonical sixteen; the snapshot fails if the live list holds more than the
# seven canonical sessions. The communications
# plane needs the directory writer's one-time activation, which scripts/docs-captures.sh performs; this test
# runs without it and passes --no-communications. The channels and messages are covered by the offline
# battery (SeederLogic) and by the capture run itself. Budgets are Business: where the engine answers 501
# the snapshot shows them as n/a.
#
# Exit 0 when both passes succeed and the second creates nothing, 1 on a finding, 2 when it could not look.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
. "$ROOT/scripts/lib/redact-log.sh"
BIN="${OLIVARES_BIN:-$ROOT/bin/olivares}"
[ -x "$BIN" ] || { echo "test-demo-dataset-live: OLIVARES_BIN must name an executable engine" >&2; exit 2; }
. "$ROOT/scripts/lib/exec-tmpdir.sh"
EXEC_TMP="$(olivares_exec_tmpdir)" || { echo "test-demo-dataset-live: no temporary directory executes; set OLIVARES_EXEC_TMPDIR" >&2; exit 2; }
WORK="$(mktemp -d "$EXEC_TMP/dataset-live.XXXXXX")"
PIDS=()
cleanup() {
  status=$?
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
  # A failing pass names what the engine saw; the store and the log are removed below either way.
  if [ "$status" -ne 0 ] && [ -f "$WORK/engine.log" ]; then
    echo "--- engine.log (last 40 lines) ---" >&2
    log_tail "$WORK/engine.log" 40
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

free_port() { python3 -I -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'; }
PORT="$(free_port)"; GRPC="$(free_port)"
mkdir -p "$WORK/data" "$WORK/home"
printf 'demo-harness-inference-token-not-a-real-credential\n' >"$WORK/token"; chmod 600 "$WORK/token"
python3 -I "$ROOT/scripts/demo-tools/build-demo-tools.py" "$WORK/tools"

engine() { # engine [serve flags...]
  env -i PATH=/usr/local/bin:/usr/bin:/bin HOME="$WORK/home" TMPDIR="$WORK" \
    OLIVARES_SESSION_RUNTIME_TOKEN_FILE="$WORK/token" OLIVARES_SESSION_RUNTIME_WIF=0 \
    DEMO_SESSION_UNIQUE=1 \
    OLIVARES_SESSION_RUNTIME_CLAUDE_BIN="$WORK/tools/claude" OLIVARES_SESSION_RUNTIME_CODEX_BIN="$WORK/tools/codex" \
    OLIVARES_SESSION_RUNTIME_GEMINI_BIN="$WORK/tools/gemini" OLIVARES_SESSION_RUNTIME_OPENCODE_BIN="$WORK/tools/opencode" \
    "$BIN" serve --insecure "$@" --listen "127.0.0.1:$PORT" --grpc-listen "127.0.0.1:$GRPC" \
    --data-dir "$WORK/data" >>"$WORK/engine.log" 2>&1 &
  ENGINE_PID=$!
  PIDS+=("$ENGINE_PID")
  for _ in $(seq 1 240); do
    kill -0 "$ENGINE_PID" 2>/dev/null || { log_tail "$WORK/engine.log" 20; echo "test-demo-dataset-live: the engine exited" >&2; exit 2; }
    curl --noproxy '*' -sf "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && return 0
    sleep 0.5
  done
  echo "test-demo-dataset-live: the engine did not answer" >&2; exit 2
}

api() { # api METHOD PATH [JSON] -> body on stdout; the token never reaches argv
  printf 'Authorization: Bearer %s\nX-Olivares-Tenant: %s\nContent-Type: application/json\n' "$TOKEN" "$TENANT" |
    curl --noproxy '*' -sSf -X "$1" "http://127.0.0.1:$PORT$2" -H @- ${3:+--data-binary "$3"}
}

login() {
  TOKEN="$(printf '%s' '{"email":"demo@olivares.local","password":"olivares-demo-estate"}' |
    curl --noproxy '*' -sf -X POST "http://127.0.0.1:$PORT/v1/auth/login" -H 'Content-Type: application/json' --data-binary @- |
    python3 -I -c 'import sys,json; print(json.load(sys.stdin)["token"])')"
  TENANT="$(printf 'Authorization: Bearer %s\n' "$TOKEN" | curl --noproxy '*' -sf "http://127.0.0.1:$PORT/v1/auth/whoami" -H @- |
    python3 -I -c 'import sys,json; print(json.load(sys.stdin)["grants"][0]["tenant"])')"
}

# First-boot setup: the capture harness mints the same demo administrator on a fresh store, so no
# synthetic --seed-demo observations exist beside the canonical estate.
bootstrap() {
  local token body status
  token="$(awk '/^=== FIRST-BOOT SETUP ===$/ { inside = 1; next }
                inside && /^  Token: / { print $2; exit }' "$WORK/engine.log")"
  [ -n "$token" ] || { echo "test-demo-dataset-live: the fresh engine printed no first-boot token" >&2; return 1; }
  body="$(DEMO_SETUP_TOKEN="$token" python3 -I -c '
import json, os
print(json.dumps({"token": os.environ["DEMO_SETUP_TOKEN"], "email": "demo@olivares.local",
                  "password": "olivares-demo-estate", "organization": "Northwind Labs"}))')"
  status="$(printf '%s' "$body" | curl -sS --noproxy '*' -o "$WORK/setup.json" -w '%{http_code}' \
    -X POST "http://127.0.0.1:$PORT/v1/setup" -H 'Content-Type: application/json' --data-binary @-)" || status=000
  case "$status" in
  2??) return 0 ;;
  *) echo "test-demo-dataset-live: first-boot setup returned HTTP $status" >&2; return 1 ;;
  esac
}

# A fresh install with the modules selected, then restarted: how the harness hands the engine over.
engine
bootstrap || exit 2
login || { echo "test-demo-dataset-live: could not sign in to the demo engine" >&2; exit 2; }
SELECT="$(api GET /v1/console/modules | python3 -I -c '
import sys, json
m = json.load(sys.stdin)["modules"]
want = ["sessions", "skills", "governance", "inferenceproxy", "models", "accessmap"]
sel = [x["name"] for x in m if x["selected"]]
print(json.dumps({"selected": sel + [w for w in want if w not in sel and any(x["name"] == w for x in m)]}))')"
api PUT /v1/console/modules "$SELECT" >/dev/null || { echo "test-demo-dataset-live: the engine refused the module selection" >&2; exit 2; }
sleep 8
kill "$ENGINE_PID" 2>/dev/null || true; wait "$ENGINE_PID" 2>/dev/null || true
engine
login || { echo "test-demo-dataset-live: could not sign in after the restart" >&2; exit 2; }

seed() {
  OLIVARES_TOKEN="$TOKEN" python3 -I "$ROOT/scripts/seed-demo-dataset.py" "http://127.0.0.1:$PORT" - "$TENANT" \
    "$WORK/dataset" --no-communications
}

snapshot() { # one line per surface: what exists, by count and by state
  OLIVARES_TOKEN="$TOKEN" python3 -I - "$PORT" "$TENANT" "$ROOT/scripts/demo-tools/conversations.json" "$WORK/dataset" "$ROOT/scripts/lib" <<'PY'
import json, os, sys, urllib.error, urllib.request
port, tenant, conversations, dataset_root, scripts_lib = sys.argv[1:6]
sys.path.insert(0, scripts_lib)
# The shared opener keeps the bearer on loopback (an inherited http_proxy would carry it off the
# host) and refuses a redirect to another origin; the redactor covers a 4xx body the server writes.
from redaccion import Redactor, abre
token = os.environ["OLIVARES_TOKEN"]
redact = Redactor()
redact.recuerda(token)

class HttpError(Exception):
    def __init__(self, path, code, body):
        self.path, self.code, self.body = path, code, body
        super().__init__("%s returned HTTP %d: %s" % (path, code, body))

def get(path):
    req = urllib.request.Request("http://127.0.0.1:%s%s" % (port, path))
    req.add_header("Authorization", "Bearer " + token)
    req.add_header("X-Olivares-Tenant", tenant)
    try:
        with abre(req, timeout=30) as r:
            return json.load(r)
    except urllib.error.HTTPError as exc:
        raise HttpError(path, exc.code, redact(exc.read().decode("utf8", "replace"))[:200])
surfaces = {
    "providers": "/v1/m/sessions/providers?limit=100",
    "profiles": "/v1/m/sessions/provider-profiles?limit=100",
    "accounts": "/v1/m/sessions/provider-accounts?limit=100",
    "workspaces": "/v1/m/sessions/workspaces?limit=100",
    "templates": "/v1/m/sessions/templates?limit=100",
    "policies": "/v1/m/governance/policies?limit=100",
    "approvals": "/v1/m/governance/approvals?limit=100",
    "runs": "/v1/m/sessions/runs?limit=100",
    "packs": "/v1/m/skills/packs?limit=100",
    "members": "/v1/members?limit=100",
    "models": "/v1/m/models/owned-models?limit=100",
    "dlp": "/v1/m/inferenceproxy/dlp/rules",
    "mcp": "/v1/console/mcp-gateway",
    "budgets": "/v1/m/finops/budgets?limit=100",
}
problems = []
lists = {}
edition = get("/v1/server-info").get("edition")
for name, path in surfaces.items():
    try:
        body = get(path)
    except HttpError as exc:
        # FinOps is the one surface allowed to refuse, and only on Community (501); any other
        # refusal is a finding, and its body belongs in the message.
        if name == "budgets" and exc.code == 501 and edition == "community":
            print("%-10s n/a (%s)" % (name, exc))
            continue
        problems.append(str(exc))
        print("%-10s ?" % name)
        continue
    # Most surfaces use `items`; the MCP gateway is the one that names its list `servers`. A
    # renamed or dropped key must fail here, not print a reassuring `0 {}` that both passes agree on.
    key = {"mcp": "servers"}.get(name, "items")
    items = body.get(key)
    if not isinstance(items, list):
        problems.append("%s: the answer carries no %s list" % (name, key))
        print("%-10s ?" % name)
        continue
    lists[name] = items
    states = {}
    for it in items:
        st = it.get("state") or it.get("status") or it.get("effective_status") or "-"
        states[st] = states.get(st, 0) + 1
    print("%-10s %3d %s" % (name, len(items), json.dumps(states, sort_keys=True)))
# The roster, the sign-ins and the folders: the capture's people and launch-ready profiles come
# from these rows, so a count that agrees while a label differs is still a wrong estate.
people = {
    "ana.ruiz@olivares.local": ("Ana Ruiz", "admin"),
    "lena.fischer@olivares.local": ("Lena Fischer", "editor"),
    "marc.vidal@olivares.local": ("Marc Vidal", "editor"),
    "sam.okafor@olivares.local": ("Sam Okafor", "editor"),
    "priya.nair@olivares.local": ("Priya Nair", "editor"),
    "tomas.gil@olivares.local": ("Tomas Gil", "editor"),
}
by_email = {m.get("email"): m for m in lists.get("members", [])}
if len(lists.get("members", [])) != 7 or set(by_email) - set(people) != {"demo@olivares.local"}:
    problems.append("the roster is %d members, expected the six people and the demo administrator"
                    % len(lists.get("members", [])))
for email, (display, role) in people.items():
    row = by_email.get(email) or {}
    if (row.get("display_name"), row.get("role")) != (display, role):
        problems.append("%s reads back as %r (%r), expected %r (%r)"
                        % (email, row.get("display_name"), row.get("role"), display, role))

profiles = {
    "Lena's Claude account": "provider_account_home",
    "Sam's Claude account": "provider_account_home",
    "Tomas's Claude account": "provider_account_home",
    "Ana's Claude account": "provider_account_home",
    "Marc's ChatGPT account": "provider_account_home",
    "OpenAI team key": "managed_injection",
    "Anthropic team key": "managed_injection",
    "Gemini CLI": "provider_account_home",
}
by_display = {p.get("display_name"): p for p in lists.get("profiles", [])}
if set(by_display) != set(profiles):
    problems.append("the profiles are %s; expected the canonical eight" % sorted(by_display))
for display, source in profiles.items():
    if (by_display.get(display) or {}).get("auth_source") != source:
        problems.append("profile %r signs in via %r, expected %r"
                        % (display, (by_display.get(display) or {}).get("auth_source"), source))

accounts = {"Lena's Claude account", "Sam's Claude account", "Tomas's Claude account",
            "Ana's Claude account", "Marc's ChatGPT account", "OpenAI team key",
            "Anthropic team key"}
have_accounts = {a.get("display_name") for a in lists.get("accounts", [])}
if have_accounts != accounts:
    problems.append("the accounts are %s; expected the canonical seven" % sorted(have_accounts))

# Five repositories under the dataset root, read-write with DLP off: the canonical sessions launch here.
folders = {"storefront-web", "payments-api", "infra", "docs-site", "data-pipelines"}
expected_roots = {os.path.realpath(os.path.join(dataset_root, "repos", n)) for n in folders}
by_root = {w.get("root_path"): w for w in lists.get("workspaces", [])}
if {os.path.realpath(r or "") for r in by_root} != expected_roots:
    problems.append("the folders are %s; expected the five canonical repositories" % sorted(by_root))
for root, w in by_root.items():
    if (w.get("mount_mode"), w.get("dlp_mode")) != ("rw", "off"):
        problems.append("folder %s is %r/%r, expected rw/off"
                        % (root, w.get("mount_mode"), w.get("dlp_mode")))
# The canonical estate is the recorded sessions, told apart by name: a run outside it is a seeder bug.
with open(conversations, encoding="utf8") as fh:
    canonical = {s["name"]: s for s in json.load(fh)["sessions"] if not s.get("fixture")}
runs = get("/v1/m/sessions/runs?limit=100")["items"]
known = [r for r in runs if r.get("name") in canonical]
seed = [r for r in runs if r.get("name") not in canonical]
# The home merges every observed row with the runs: only the plane's own bridge rows of these
# sixteen runs may exist, or the captured figures count somebody else's sessions.
run_refs = {r.get("run_ref") for r in known}
live_rows = get("/v1/m/sessions/live?limit=100")["items"]
outside = [r for r in live_rows if r.get("attribution") != "managed" or r.get("run_ref") not in run_refs]
print("%-10s %3d (%d outside the canonical estate)" % ("live", len(live_rows), len(outside)))
if outside:
    problems.append("the live list carries %d observed row(s) outside the canonical estate: %s" % (
        len(outside), ", ".join(sorted({r.get("live_ref") or r.get("session_ref") or "-"
                                        for r in outside})[:5])))
missing = sorted(set(canonical) - {r["name"] for r in known})
if missing:
    problems.append("missing: " + ", ".join(missing))
for r in known:
    s = canonical[r["name"]]
    want, got = s.get("cost_micro_usd"), r.get("cost_micro_usd")
    if got != want:
        problems.append("%s reports cost %s, the canonical estate says %s" % (r["name"], got, want))
    ending = s.get("ending", "exit")
    if ending in ("hold", "linger"):
        # A hold turn never answers, so its run is in flight; a linger turn answered and the run
        # waits for the next input, which the engine reports as idle.
        want_state = "running" if ending == "hold" else "idle"
        ok = r.get("state") == want_state
    else:
        want_state = "stopped"
        ok = r.get("state") == "stopped" and r.get("exit_code") == 0
    if not ok:
        problems.append("%s is %s (exit %s), expected %s" % (
            r["name"], r.get("state"), r.get("exit_code"),
            want_state if want_state != "stopped" else "stopped cleanly"))
# The aggregates come from what the engine returned, not from the table the seeder read: the two
# agreeing today is exactly what can stop being true without anyone noticing.
total = sum(r.get("cost_micro_usd") for r in known if r.get("cost_micro_usd") is not None)
unpriced = sum(1 for r in known if r.get("cost_micro_usd") is None)
live = sum(1 for r in known if r.get("state") in ("running", "idle"))
print("%-10s %3d live=%d finished=%d cost=%d unpriced=%d"
      % ("canonical", len(known), live, len(known) - live, total, unpriced))
# `serve --seed-demo` writes observations, not runs: any other run on this list is a seeder bug.
print("%-10s %3d (runs outside the canonical estate; must be zero)" % ("seed-runs", len(seed)))
if seed:
    problems.append("the run list carries %d run(s) outside the canonical estate: %s"
                    % (len(seed), ", ".join(sorted({r.get("name") or "-" for r in seed}))))
if (len(known), live, total, unpriced) != (16, 7, 6840000, 1):
    problems.append("the canonical estate is %d runs, %d live, %d micro-USD today, %d unpriced"
                    % (len(known), live, total, unpriced))
# The other exit criteria, read back from the product: the exact queue (action, subject and
# state), three requests waiting, and no Business request kind on a Community estate.
COMMUNITY_QUEUE = {
    ("db.migrate", "payments-staging"): "pending",
    ("network.connect", "registry.terraform.io"): "pending",
    ("github.write", "storefront-web#482"): "pending",
    ("db.migrate", "staging-dev"): "approved",
    ("db.migrate", "staging-db"): "approved",
    ("network.connect", "pypi.org"): "approved",
    ("network.connect", "api.stripe.com"): "approved",
    ("github.write", "storefront-web#123"): "approved",
    ("network.connect", "registry.npmjs.org"): "approved",
    ("github.write", "storefront-web#456"): "rejected",
}
BUSINESS_QUEUE = {
    ("deploy.promote", "payments-api/production"): "approved",
    ("evidence.export", "q3-access-review"): "approved",
}
approvals = get("/v1/m/governance/approvals?limit=100")["items"]

def approval_state(a):
    return a.get("effective_status") or a.get("status") or a.get("state") or "-"

states = {}
for a in approvals:
    st = approval_state(a)
    states[st] = states.get(st, 0) + 1
want = dict(COMMUNITY_QUEUE)
if edition == "community":
    business_actions = {action for action, _ in BUSINESS_QUEUE}
    leaked = sorted({a.get("action") for a in approvals if a.get("action") in business_actions})
    if leaked:
        problems.append("a Community estate holds Business request kinds: " + ", ".join(leaked))
else:
    want.update(BUSINESS_QUEUE)
if states.get("pending", 0) != 3:
    problems.append("the approval queue holds %d waiting requests, the canonical estate has 3"
                    % states.get("pending", 0))
actual = {(a.get("action"), a.get("subject_ref")): a for a in approvals}
wrong = ["%s %s is %s, expected %s"
         % (action, subject, approval_state(actual[(action, subject)])
            if (action, subject) in actual else "missing", state)
         for (action, subject), state in want.items()
         if (action, subject) not in actual or approval_state(actual[(action, subject)]) != state]
extra = sorted(set(actual) - set(want))
if wrong or extra:
    problems.append("approval queue: " + "; ".join(
        wrong + ["unexpected %s %s" % key for key in extra]))
# A waiting request names the session that asked by its provider id; the home can only follow that
# name when the id belongs to a managed live row of one of these sixteen runs.
managed_sids = {r.get("session_ref"): r for r in live_rows if r.get("attribution") == "managed"}
for a in approvals:
    if approval_state(a) != "pending":
        continue
    sid = a.get("session_ref")
    row = managed_sids.get(sid)
    if not sid or row is None or row.get("run_ref") not in run_refs:
        problems.append("pending %s %s names session %r, which is not a managed live row of the "
                        "canonical estate" % (a.get("action"), a.get("subject_ref"), sid))
if problems:
    for p in problems:
        print("PROBLEM " + p, file=sys.stderr)
    sys.exit(1)
PY
}

echo "==> first pass"
seed
snapshot >"$WORK/first.txt" || { echo "test-demo-dataset-live: the first pass left sessions unfinished or the estate unreadable" >&2; exit 1; }
cat "$WORK/first.txt"
echo "==> second pass"
seed
snapshot >"$WORK/second.txt" || { echo "test-demo-dataset-live: the second pass left sessions unfinished or the estate unreadable" >&2; exit 1; }
if ! diff -u "$WORK/first.txt" "$WORK/second.txt"; then
  echo "test-demo-dataset-live: the second pass changed the estate" >&2
  exit 1
fi
runs="$(awk '$1 == "canonical" { print $2 }' "$WORK/first.txt")"
[ "$runs" = "16" ] || { echo "test-demo-dataset-live: expected the sixteen recorded conversations, found $runs canonical runs" >&2; exit 1; }
echo "test-demo-dataset-live: OK — two passes, the same estate, exactly seven observed sessions, 7 live and 9 finished canonical sessions, 3 approvals waiting"
