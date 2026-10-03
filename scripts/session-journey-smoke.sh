#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# session-journey-smoke.sh — the core journey on a given binary, the way a person
# does it: start the engine, create the administrator, add a Claude Code account,
# register a folder, start a session in it, send a prompt, read the reply, stop,
# resume, stop. On Linux it also asserts that the session process cannot read the
# engine's data directory (Landlock confinement).
#
#   REAL   — the engine binary, its API, the session runtime, the runner and the
#            confinement helper.
#   DOUBLE — the agent CLI: a protocol stub named `claude` on the engine's PATH that
#            speaks stream-json (an init frame, then one reply per prompt) and
#            reports whether it could read <data-dir>/secret-store.key.
#
# Usage:  scripts/session-journey-smoke.sh [--binary PATH] [--label NAME]
# Requires: bash, python3. Exit 0 = journey works; 1 = it does not (the step is
# named); 2 = could not look (no binary, no free port).
set -euo pipefail

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
bin="${OLIVARES_BIN:-$root/bin/olivares}"
label="$(uname -s | tr 'A-Z' 'a-z')"
while [[ "$#" -gt 0 ]]; do
	case "$1" in
		--binary) bin="${2:-}"; shift 2 ;;
		--label) label="${2:-}"; shift 2 ;;
		*) printf 'session-journey: unknown argument: %s\n' "$1" >&2; exit 2 ;;
	esac
done
[[ -x "$bin" ]] || { printf 'session-journey: COULD NOT LOOK — no executable binary at %s\n' "$bin" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { printf 'session-journey: COULD NOT LOOK — python3 is missing\n' >&2; exit 2; }

work="$(mktemp -d "${TMPDIR:-/tmp}/olivares-session-journey.XXXXXX")"
data="$work/data"; folder="$work/project"; stubdir="$work/stub"; home="$work/home"
mkdir -p "$data" "$folder" "$stubdir" "$home"; chmod 0700 "$data" "$home"
engine_pid=""
cleanup() {
	local rc=$?
	if [[ -n "$engine_pid" ]]; then
		kill "$engine_pid" 2>/dev/null || true
		wait "$engine_pid" 2>/dev/null || true
	fi
	if [[ "$rc" -ne 0 ]]; then
		printf 'session-journey: exit %s — last 40 lines of the engine log:\n' "$rc" >&2
		tail -n 40 "$work/engine.log" >&2 2>/dev/null || true
	fi
	rm -rf -- "$work"
}
trap cleanup EXIT

# The agent CLI double. Its folder is readable by a confined session (the
# program's own directory), so it finds the probe target there.
printf '%s\n' "$data" >"$stubdir/probe-target"
cat >"$stubdir/claude" <<'STUB'
#!/bin/sh
here=$(cd "$(dirname "$0")" && pwd)
target=$(cat "$here/probe-target" 2>/dev/null || true)
sid="journey-$$"
probe=none
if [ -n "$target" ]; then
	if cat "$target/secret-store.key" >/dev/null 2>&1; then probe=KEY-READ; else probe=key-denied; fi
fi
printf '{"type":"system","subtype":"init","session_id":"%s","cwd":"%s"}\n' "$sid" "$PWD"
printf '{"type":"system","subtype":"journey_probe","probe":"%s"}\n' "$probe"
while IFS= read -r line; do
	printf '{"type":"assistant","session_id":"%s","message":{"role":"assistant","content":[{"type":"text","text":"journey-reply"}]}}\n' "$sid"
	printf '{"type":"result","subtype":"success","is_error":false,"result":"journey-reply","session_id":"%s"}\n' "$sid"
done
STUB
chmod 0755 "$stubdir/claude"

read -r port grpc_port < <(python3 -c 'import socket
s=[socket.socket() for _ in range(2)]
[x.bind(("127.0.0.1",0)) for x in s]
print(*[x.getsockname()[1] for x in s])')
# The engine runs with a HOME of its own, so the tool's standard login directory
# (~/.claude) is the test's, never this machine user's.
HOME="$home" PATH="$stubdir:$PATH" "$bin" serve --insecure --data-dir "$data" \
	--listen "127.0.0.1:$port" --grpc-listen "127.0.0.1:$grpc_port" >"$work/engine.out" 2>"$work/engine.log" &
engine_pid=$!

expect_confined=0
[[ "$(uname -s)" == Linux ]] && expect_confined=1

python3 - "http://127.0.0.1:$port" "$work/engine.out" "$folder" "$data" "$expect_confined" "$label" <<'PY'
import json, re, sys, time, urllib.error, urllib.request
base, out_path, folder, data, expect_confined, label = sys.argv[1:7]
tenant, token = "", ""

def fail(step, detail):
    print(f"session-journey: FAIL at {step}: {detail}", file=sys.stderr)
    sys.exit(1)

def call(method, path, body=None):
    headers = {"content-type": "application/json"}
    if token:
        headers["authorization"] = "Bearer " + token
    if tenant:
        headers["X-Olivares-Tenant"] = tenant
    data_ = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + path, data=data_, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            raw = r.read()
            return r.status, json.loads(raw) if raw.strip()[:1] in (b"{", b"[") else {}
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode(errors="replace")[:400]

def ok(step, status, body, want=(200, 201)):
    if status not in want:
        fail(step, f"HTTP {status} {body}")
    return body

for _ in range(60):
    try:
        urllib.request.urlopen(base + "/healthz", timeout=2)
        break
    except Exception:
        time.sleep(1)
else:
    fail("engine start", "no /healthz answer within 60 s")
m = None
for _ in range(20):
    m = re.search(r"olst_[A-Za-z0-9]+", open(out_path).read())
    if m:
        break
    time.sleep(0.5)
if not m:
    fail("setup token", "the engine printed no setup token")

pw = "journey-password-1"
su = ok("setup", *call("POST", "/v1/setup", {"token": m.group(0), "email": "admin@journey.invalid", "password": pw}))
tenant = (su.get("organization") or {}).get("tenant_id", "")
token = ok("sign in", *call("POST", "/v1/auth/login", {"email": "admin@journey.invalid", "password": pw})).get("token", "")
# The console's way: a Claude Code profile that signs in with the tool's own login.
acct = ok("add Claude Code (the tool's own login)", *call("POST", "/v1/m/sessions/provider-profiles",
                                                        {"driver": "claude", "config_home": "", "user_home": "",
                                                         "display_name": "journey", "auth_source": "provider_account_home"}))
profile_ref = acct.get("ref") or acct.get("profile_ref") or acct.get("id")
ws = ok("register the folder", *call("POST", "/v1/m/sessions/workspaces",
                                    {"name": "project", "root_path": folder, "mount_mode": "rw", "dlp_mode": "off"}))
ws_ref = ws.get("ref") or ws.get("workspace_ref") or ws.get("id")
run = ok("start a session", *call("POST", "/v1/m/sessions/runs", {
    "provider_profile_ref": profile_ref, "workspace_ref": ws_ref,
    "transport": "stream-json", "permission_mode": "default", "name": "journey"}))
ref = run.get("run_ref")
if run.get("state") != "running":
    fail("start a session", f"state {run.get('state')!r}")

def frames(min_seq):
    """Read the session's output frames from the live attach stream."""
    got = []
    req = urllib.request.Request(f"{base}/v1/m/sessions/runs/{ref}/attach?from={min_seq}",
                                 headers={"authorization": "Bearer " + token, "X-Olivares-Tenant": tenant})
    try:
        with urllib.request.urlopen(req, timeout=3) as r:
            for raw in r:
                line = raw.decode(errors="replace").strip()
                if line.startswith("data:"):
                    try:
                        got.append(json.loads(line[5:]))
                    except ValueError:
                        pass
    except Exception:
        pass
    return got

user = {"type": "user", "message": {"role": "user", "content": "say hello"}}
ok("send a prompt", *call("POST", f"/v1/m/sessions/runs/{ref}/input", {"line": json.dumps(user)}), want=(200, 202, 204))
lines = []
for _ in range(10):
    lines = [f.get("line", "") for f in frames(0)]
    if any("journey-reply" in l for l in lines):
        break
    time.sleep(0.5)
else:
    fail("read the reply", f"no reply frame in {lines[-5:]}")
probe = next((json.loads(l).get("probe") for l in lines if "journey_probe" in l), None)

events = call("GET", f"/v1/m/sessions/runs/{ref}/events")[1]
confinement = re.search(r"confinement: [^\"]+", json.dumps(events))
if expect_confined == "1":
    if probe != "key-denied":
        fail("confinement", f"the session process read the engine key (probe={probe!r})")
    if not confinement or "landlock" not in confinement.group(0):
        fail("confinement", f"the launched event does not say landlock: {confinement and confinement.group(0)!r}")

ok("stop", *call("POST", f"/v1/m/sessions/runs/{ref}/stop", {}))
ok("resume", *call("POST", f"/v1/m/sessions/runs/{ref}/resume", {}))
ok("stop after resume", *call("POST", f"/v1/m/sessions/runs/{ref}/stop", {}))
state = ok("find it later", *call("GET", f"/v1/m/sessions/runs/{ref}")).get("state")
if state not in ("stopped", "completed"):
    fail("find it later", f"state {state!r}")
print(f"session-journey: OK — {label}: start, prompt, reply, stop, resume, stop; "
      f"probe={probe}; {confinement.group(0) if confinement else 'confinement: not reported'}")
PY
