#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-quickstart-smoke-boot.sh — selftest for the engine-boot wait in scripts/quickstart-smoke.sh
#. The nightly drill died with "demo engine never became healthy" on 2026-10-21 and
# 2026-10-22 because the wait gave the engine a fixed 20 s and the CI runner stalled for 8-10 s at
# a random phase of the boot (key generation one day, store migrations the next), which the same
# binary does in about 7 s on one pinned CPU. The numbers the drill asserts were never wrong.
#
# Three stand-in engines, driven through OLIVARES_BIN, so no Go build or real engine is needed:
#   slow  accepts /healthz only after SLOW_BOOT_S (longer than the old 120 s budget): the drill
#         must go on past the wait instead of reporting an engine that never came up.
#   dead  exits at once: the drill must say the engine EXITED, and not burn the whole wait on it.
#   hang  stays up and never answers: the drill gives up by its budget (SMOKE_HEALTH_POLLS=4 here).
#   lag   answers the whole Path A API but serves an empty access graph for its first reads, as an
#         engine does while the demo source's observations are still on their way through the bus
#         after the module restart: the drill must wait for the published counts (nightly 2026-11-03:
#         "demo graph nodes (quickstart guide says 20): got '0'").
#
# Usage: scripts/test-quickstart-smoke-boot.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=lib/exec-workdir.sh
. "$ROOT/scripts/lib/exec-workdir.sh" || {
	echo "test-quickstart-smoke-boot: cannot source lib/exec-workdir.sh. NOT a pass." >&2
	exit 2
}
WORK="$(olivares_pick_exec_workdir quickstart-boot-tests)" || {
	echo "test-quickstart-smoke-boot: no scratch directory allows execution, so the stand-in" >&2
	echo "  engines could not run. NOTHING was measured. NOT a pass." >&2
	exit 2
}
trap 'rm -rf "$WORK"' EXIT

SLOW_BOOT_S=125 # above the 120 s budget exhausted during fresh SQLite migrations
DEAD_BOUND_S=15 # a dead engine must end the wait well before the old 20 s
PORT=$((21000 + ($$ % 900) * 2))
PASS=0
FAIL=0
ok() { PASS=$((PASS + 1)); printf '  ok   %s\n' "$1"; }
no() { FAIL=$((FAIL + 1)); printf '  FAIL %s\n       %s\n' "$1" "$2"; }

# stand_in <mode>: write $WORK/<mode>/olivares, a stand-in for `olivares serve`.
stand_in() {
	mkdir -p "$WORK/$1"
	cat >"$WORK/$1/olivares" <<EOF
#!/usr/bin/env bash
port=""
while [ \$# -gt 0 ]; do
  case "\$1" in --listen) port="\${2##*:}"; shift 2 ;; *) shift ;; esac
done
case "$1" in
dead) echo "listen tcp 127.0.0.1:\$port: bind: address already in use"; exit 1 ;;
slow) sleep $SLOW_BOOT_S ;;
hang) exec sleep 300 ;;
esac
exec python3 -c "
import http.server
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(s):
        if s.path == '/healthz':
            open('$WORK/$1/healthz-served', 'w').close()
            s.send_response(200)
        else:
            s.send_response(404)
        s.end_headers()
    def log_message(s, *a): pass
http.server.HTTPServer(('127.0.0.1', int('\$port')), H).serve_forever()"
EOF
	chmod +x "$WORK/$1/olivares"
}

# drive <mode> [VAR=value...]: run the real smoke against the stand-in; sets OUT, RC and SECS.
drive() {
	local t0 mode="$1"
	shift
	stand_in "$mode"
	t0="$(date +%s)"
	OUT="$(env "$@" OLIVARES_BIN="$WORK/$mode/olivares" SMOKE_PORT="$PORT" timeout 180 bash "$ROOT/scripts/quickstart-smoke.sh" 2>&1)"
	RC=$?
	SECS=$(($(date +%s) - t0))
}

echo "case 1: an engine that needs ${SLOW_BOOT_S}s to boot is waited for"
drive slow
# rc 124 is `timeout` killing the run: not "went on past the wait".
if [ -f "$WORK/slow/healthz-served" ] && [ "$RC" -ne 124 ]; then ok "the drill reached the engine's /healthz"; else no "the drill reached the engine's /healthz" "rc=$RC after ${SECS}s; output tail: $(printf '%s' "$OUT" | tail -3)"; fi
case "$OUT" in
*"never became healthy"*) no "no 'never became healthy' report for a slow boot" "gave up after ${SECS}s" ;;
*) ok "no 'never became healthy' report for a slow boot" ;;
esac

echo "case 2: an engine that exits during boot is reported as exited, at once"
drive dead
case "$OUT" in
*"exited during boot"*) ok "names the exit" ;;
*) no "names the exit" "rc=$RC; output tail: $(printf '%s' "$OUT" | tail -3)" ;;
esac
if [ "$RC" -ne 0 ] && [ "$SECS" -lt "$DEAD_BOUND_S" ]; then ok "fails within ${DEAD_BOUND_S}s (took ${SECS}s)"; else no "fails within ${DEAD_BOUND_S}s" "rc=$RC after ${SECS}s"; fi

echo "case 3: an engine that stays up but never answers is given up on, by the budget"
drive hang SMOKE_HEALTH_POLLS=4
case "$OUT" in
*"demo engine never became healthy"*) ok "reports 'never became healthy', not an exit (rc=$RC, ${SECS}s)" ;;
*) no "reports 'never became healthy', not an exit" "rc=$RC; output tail: $(printf '%s' "$OUT" | tail -3)" ;;
esac

echo "case 4: a graph that fills a moment after the modules are running is waited for"
# The published counts, from the guide the drill itself reads.
read -r GUIDE_NODES GUIDE_EDGES < <(sed -n 's/.*returns exactly \*\*\([0-9]*\) nodes and \([0-9]*\) edges.*/\1 \2/p' "$ROOT/docs-site/src/content/docs/start/quickstart.md")
mkdir -p "$WORK/lag"
cat >"$WORK/lag/server.py" <<'PYEOF'
import http.server, json, sys
nodes, edges, empty_reads = int(sys.argv[2]), int(sys.argv[3]), 4
reads = 0
mods = ["inventory", "observability", "security", "accessmap"]
class H(http.server.BaseHTTPRequestHandler):
    def send(s, body):
        s.send_response(200)
        s.send_header("Content-Type", "application/json")
        s.end_headers()
        s.wfile.write(json.dumps(body).encode())
    def do_GET(s):
        global reads
        p = s.path.split("?")[0]
        if p == "/healthz": s.send({})
        elif p == "/v1/auth/whoami": s.send({"grants": [{"tenant": "t1"}]})
        elif p == "/v1/console/modules": s.send({"modules": [{"name": m, "selected": True, "running": True} for m in mods]})
        elif p == "/v1/server-info": s.send({"edition": "community"})
        elif p == "/v1/m/security/findings": s.send({"items": []})
        elif p == "/v1/m/governance/identities": s.send({})
        elif p == "/v1/m/accessmap/graph":
            reads += 1
            n, e = (0, 0) if reads <= empty_reads else (nodes, edges)
            s.send({"nodes": [{}] * n, "edges": [{"attribution_tier": "firm" if i % 2 else "approximate"} for i in range(e)]})
        else:
            s.send_response(404); s.end_headers()
    def do_POST(s):
        s.rfile.read(int(s.headers.get("Content-Length", 0)))
        s.send({"token": "t"})
    do_PUT = do_POST
    def log_message(s, *a): pass
http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
PYEOF
cat >"$WORK/lag/olivares" <<EOF
#!/usr/bin/env bash
while [ \$# -gt 0 ]; do
  case "\$1" in --listen) port="\${2##*:}"; shift 2 ;; *) shift ;; esac
done
exec python3 "$WORK/lag/server.py" "\$port" "$GUIDE_NODES" "$GUIDE_EDGES"
EOF
chmod +x "$WORK/lag/olivares"
OUT="$(OLIVARES_BIN="$WORK/lag/olivares" SMOKE_PORT="$PORT" timeout 60 bash "$ROOT/scripts/quickstart-smoke.sh" 2>&1)"
case "$OUT" in
*"ok: demo graph nodes (quickstart guide says $GUIDE_NODES) = $GUIDE_NODES"*"ok: demo graph edges (quickstart guide says $GUIDE_EDGES) = $GUIDE_EDGES"*) ok "the drill asserts the published counts once the graph has them" ;;
*) no "the drill asserts the published counts once the graph has them" "output tail: $(printf '%s' "$OUT" | tail -3)" ;;
esac

printf '%s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
