#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Generate console screenshots through the real engine with synthetic data and login.
# Output: web/playwright-report/docs/<view>-<theme>.png.
# Usage: scripts/docs-captures.sh [Playwright args, e.g. --grep sessions].
# Requires Go, pnpm and Chromium (pnpm --dir web exec playwright install chromium).
# This rebuilds core/internal/webui/dist; run only in an owned disposable worktree.
# PUBLICAR=1 copies verified output to docs-site/public/console: the manifest and only the captures
# that a docs page, a README or a launch post names. The other takes stay in web/playwright-report/docs.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# Every request this harness makes goes to its own loopback engines. The exclusion is exported as
# well as passed per curl, so the shared helpers it sources or runs (select-console-modules.sh,
# seed-guide-connectors.sh) cannot send a bearer token to an inherited proxy. The caller's own
# exclusions are kept: replacing them would make an unrelated host the environment exempted unreachable.
export no_proxy="127.0.0.1,localhost,::1${no_proxy:+,$no_proxy}"
export NO_PROXY="127.0.0.1,localhost,::1${NO_PROXY:+,$NO_PROXY}"

# Load the shared Git-environment isolation library before any build or probe.
# Its recognized entry point prevents isolation probes from rebuilding the web bundle.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=lib/git-env.sh
. "$_olivares_git_env" || {
  echo "ERROR: cannot source $_olivares_git_env — refusing to run git beside a mktemp sandbox" >&2
  exit 1
}
unset _olivares_git_env
# Use the shared build flags from scripts/lib/build-bin.sh.
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/build-bin.sh"
. "$ROOT/scripts/lib/select-console-modules.sh"
. "$ROOT/scripts/lib/redact-log.sh"
# A view whose capture cannot be published yet keeps its published image and is listed in
# scripts/docs-captures-held.txt (`id reason`, one per line). Every run leaves those views out, so the one
# command never overwrites a held image; delete the line once the view passes the shutter checks.
_held="$(sed -E 's/[[:space:]]*#.*$//; s/^[[:space:]]+//; /^$/d' "$ROOT/scripts/docs-captures-held.txt" | awk '{ print $1 }' | paste -sd'|' -)"
[ -z "$_held" ] || DOCS_CAPTURE_SKIP="$_held${DOCS_CAPTURE_SKIP:+|$DOCS_CAPTURE_SKIP}"
# Fail before any engine starts when a view to leave out does not exist: a typo would otherwise be reported
# as "left out" while the view is still captured.
if [ -n "${DOCS_CAPTURE_SKIP:-}" ]; then
  [[ "$DOCS_CAPTURE_SKIP" =~ ^[A-Za-z0-9-]+(\|[A-Za-z0-9-]+)*$ ]] || { echo "ERROR: DOCS_CAPTURE_SKIP must be ids joined with |" >&2; exit 2; }
  IFS='|' read -ra _skip_ids <<<"$DOCS_CAPTURE_SKIP"
  for _id in "${_skip_ids[@]}"; do
    grep -qE "(^|[^_A-Za-z])id: '$_id'," "$ROOT/web/e2e/docs-captures.spec.ts" || { echo "ERROR: DOCS_CAPTURE_SKIP names an unknown view: $_id" >&2; exit 2; }
  done
fi
PORT="${E2E_PORT:-8466}"
GRPC_PORT="$((PORT + 1))"
# A folder the capture shows is named demo- and six lowercase letters: a mktemp suffix carries digits and capitals,
# which the shutter check reads as a name only this harness knows.
demo_dir() {
  local d
  for _ in $(seq 1 20); do
    # head closes the pipe once it has six letters; under pipefail tr's SIGPIPE must not end the script.
    d="${TMPDIR:-/tmp}/demo-$(LC_ALL=C tr -dc 'a-z' </dev/urandom | head -c 6 || true)"
    mkdir -m 700 "$d" 2>/dev/null && { printf '%s\n' "$d"; return 0; }
  done
  echo "docs-captures: cannot create a demo directory under ${TMPDIR:-/tmp}" >&2
  return 1
}
# The trap is installed before the first directory exists: a later demo_dir that fails must not leak the earlier ones.
DATA="" WS_PARENT="" DATASET_ROOT=""
cleanup() {
  [ -n "${PID:-}" ] && kill "$PID" 2>/dev/null || true
  for d in "$DATA" "$WS_PARENT" "$DATASET_ROOT" ${EXEC_TMP:+"$EXEC_TMP"}; do
    [ -z "$d" ] || rm -rf "$d"
  done
}
trap cleanup EXIT
DATA="$(demo_dir)"
BIN="${OLIVARES_BIN:-$ROOT/bin/olivares}"
# Disposable host tree for the governed sessions.workspace shown by video scene 10. The harness
# owns both its creation and cleanup; the API seeder only registers it. Session cards show the folder's
# own name, and the workspace sheet its whole path, so neither is a bare mktemp name.
WS_PARENT="$(demo_dir)"
WS_ROOT="$WS_PARENT/acme-platform"

# The demo dataset's repositories and tool homes live outside the engine's data directory.
DATASET_ROOT="$(demo_dir)"

# The observed-session/run join is a three-file contract. Check it before paying for the web and Go
# builds so a renamed demo session fails at its source rather than much later as a missing tab.
bash "$ROOT/scripts/test-demo-agent.sh"

if [ -n "${OLIVARES_BIN:-}" ]; then
  [ -x "$BIN" ] || { echo 'ERROR: OLIVARES_BIN must name an executable release candidate' >&2; exit 1; }
  echo '==> Using the supplied release candidate'
else
  echo "==> Building web bundle + olivares binary"
  pnpm --dir "$ROOT/web" install --frozen-lockfile >/dev/null 2>&1 || true
  pnpm --dir "$ROOT/web" run build
  # Build connector plugins before the engine binary; go:embed requires their artifacts.
  bash "$ROOT/scripts/build-connectors.sh"
  build_olivares_bin "$BIN"
fi

# A browsable workspace needs a real tree. These are synthetic service files in the disposable
# scratch directory, not paths or content borrowed from the repository being photographed.
mkdir -p "$WS_ROOT/src" "$WS_ROOT/deploy"
printf 'module acme-platform\n\ngo 1.26\n' >"$WS_ROOT/go.mod"
printf '# acme-platform\n\nBilling and entitlement services.\n' >"$WS_ROOT/README.md"
printf 'package main\n\nfunc main() {}\n' >"$WS_ROOT/src/main.go"
printf 'package billing\n' >"$WS_ROOT/src/billing.go"
printf 'replicas: 3\n' >"$WS_ROOT/deploy/values.yaml"

# Connector plugins execute from TMPDIR. Use the shared execution probe;
# an unavailable executable directory must refuse startup.
. "$ROOT/scripts/lib/exec-tmpdir.sh"
if EXEC_TMP="$(olivares_exec_tmpdir)"; then
  echo "==> Plugin scratch dir (exec-capable, probed): $EXEC_TMP"
else
  # Exit 2 when no temporary directory executes. OLIVARES_EXEC_TMPDIR can select one.
  echo "docs-captures: ⛔ CANNOT START: no temporary directory permits EXECUTION (/tmp noexec?)." >&2
  echo "   The engine extracts connector plugins to \$TMPDIR and LAUNCHES them; this harness" >&2
  echo "   builds them explicitly before the binary. Starting anyway would silently leave the" >&2
  echo "   connector plane unavailable, and captures would show zero adoption." >&2
  echo "   (the library already reported WHICH paths it tried on stderr)" >&2
  echo "   Remedy: export OLIVARES_EXEC_TMPDIR pointing to a directory that permits execution." >&2
  exit 2
fi

# This synthetic token is valid only with the demo tools, which make no provider calls.
# A real runtime needs a real credential; this fixture cannot qualify vendor
# authentication.
printf 'demo-harness-inference-token-not-a-real-credential\n' >"$DATA/demo-inference-token"
chmod 0600 "$DATA/demo-inference-token"

# Activate the directory writer on the stopped store, then reopen the same data.
# Both boots use the same custody files and activation settings.
# Disposable keyrings are mode 0600 inside a mode 0700 directory and never printed.
# A routing probe cannot prove readiness: each handoffs capture needs an authorized
# HTTP 200 collection read for its selected workspace.
K3_CUSTODY="$DATA/custody"
mkdir -p "$K3_CUSTODY"
chmod 0700 "$K3_CUSTODY"
python3 -I - "$K3_CUSTODY" <<'PYCUSTODIA'
import base64, json, os, secrets, sys

d = sys.argv[1]


def raiz():
    return base64.b64encode(secrets.token_bytes(32)).decode()


def escribe(nombre, doc):
    fd = os.open(os.path.join(d, nombre), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as fh:
        json.dump(doc, fh)


escribe("content-keyring.json", {
    "format": "olivares.communication-content-keyring.v1",
    "current_seal_version": "seal-v1",
    "current_digest_version": "digest-v1",
    "keys": [{"version": "seal-v1", "root_key_base64": raiz()},
             {"version": "digest-v1", "root_key_base64": raiz()}],
})
escribe("cursor-keyring.json", {
    "format": "olivares.communication-cursor-keyring.v1",
    "current_kid": "cursor-k1",
    "keys": [{"kid": "cursor-k1", "key_base64": raiz()}],
})
PYCUSTODIA

# Use only the deterministic fixture runtime; admission, lifecycle and workspace confinement stay active.
# The four AI tools of the demo estate: self-contained executables (a confined session child can read only
# its own program) that speak each tool's wire protocol and answer from recorded transcripts. No vendor
# account and no network.
# Built where connector plugins already run: a temporary directory may be mounted noexec.
DEMO_TOOLS="${EXEC_TMP:-$DATA}/demo-tools"
python3 -I "$ROOT/scripts/demo-tools/build-demo-tools.py" "$DEMO_TOOLS"

# Disable WIF for this engine so inherited configuration cannot replace the synthetic token issuer.
# Both boots use the same activation and file-based custody settings.
# Keep & on the engine command so $! identifies the engine.
# The engine runs with a minimal PATH and an empty HOME: a capture must not show the tools or the paths of the
# machine that took it, and a clean install is what the guides describe.
mkdir -p "$DATA/home"
arranca_motor() {
  PATH=/usr/local/bin:/usr/bin:/bin \
  HOME="$DATA/home" \
  OLIVARES_SESSION_RUNTIME_CLAUDE_BIN="$DEMO_TOOLS/claude" \
  OLIVARES_SESSION_RUNTIME_CODEX_BIN="$DEMO_TOOLS/codex" \
  OLIVARES_SESSION_RUNTIME_GEMINI_BIN="$DEMO_TOOLS/gemini" \
  OLIVARES_SESSION_RUNTIME_OPENCODE_BIN="$DEMO_TOOLS/opencode" \
  OLIVARES_SESSION_RUNTIME_TOKEN_FILE="$DATA/demo-inference-token" \
  OLIVARES_SESSION_RUNTIME_WIF=0 \
  OLIVARES_COMMUNICATION_ACTIVATION=on \
  OLIVARES_COMMUNICATION_CONTENT_KEYRING_FILE="$K3_CUSTODY/content-keyring.json" \
  OLIVARES_COMMUNICATION_CURSOR_KEYRING_FILE="$K3_CUSTODY/cursor-keyring.json" \
  OLIVARES_KEY_WRAP= \
  DEMO_SESSION_UNIQUE=1 \
  TMPDIR="${EXEC_TMP:-${TMPDIR:-/tmp}}" \
  "$BIN" serve --insecure "$@" --listen "127.0.0.1:$PORT" \
    --grpc-listen "127.0.0.1:$GRPC_PORT" --data-dir "$DATA" >>"$DATA/engine.log" 2>&1 &
  PID=$!
}

echo "==> Booting engine (insecure, fresh store, K3 activation requested) on 127.0.0.1:$PORT"
arranca_motor

echo "==> Waiting for the engine to accept connections"
# Wait up to 240 seconds for health readiness; refusal includes the engine log.
ARRIBA=0
for _ in $(seq 1 480); do
  if curl --noproxy '*' -sf "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then ARRIBA=1; break; fi
  sleep 0.5
done
if [ "$ARRIBA" -ne 1 ]; then
  echo "docs-captures: ⛔ THE ENGINE DID NOT ACCEPT CONNECTIONS within 240 s (load1 $(cut -d' ' -f1 /proc/loadavg))." >&2
  echo "  Captures are unverified because no engine is available. Tail of its log:" >&2
  log_tail "$DATA/engine.log" 20
  exit 2
fi

# First-boot setup creates the demo administrator and the Northwind organization. The engine's
# --seed-demo is deliberately NOT used: it also writes ten synthetic observed sessions into the same
# tenant, and the home then counts them beside the canonical sixteen. The banner prints
# the one-time token once; it is read here and never echoed. The setup reply is the user and its
# organization, with no secret in it.
SETUP_TOKEN="$(awk '/^=== FIRST-BOOT SETUP ===$/ { inside = 1; next }
                    inside && /^  Token: / { print $2; exit }' "$DATA/engine.log")"
if [ -z "$SETUP_TOKEN" ]; then
  echo "docs-captures: ⛔ the fresh engine printed no first-boot token; its store was not empty." >&2
  log_tail "$DATA/engine.log" 20
  exit 2
fi
SETUP_BODY="$(DEMO_SETUP_TOKEN="$SETUP_TOKEN" python3 -I -c '
import json, os
print(json.dumps({"token": os.environ["DEMO_SETUP_TOKEN"], "email": "demo@olivares.local",
                  "password": "olivares-demo-estate", "organization": "Northwind Labs"}))')"
SETUP_HTTP="$(printf '%s' "$SETUP_BODY" | curl -sS --noproxy '*' -o "$DATA/setup.json" -w '%{http_code}' \
  -X POST "http://127.0.0.1:$PORT/v1/setup" -H 'Content-Type: application/json' --data-binary @-)" || SETUP_HTTP=000
case "$SETUP_HTTP" in
2??) ;;
*)
  echo "docs-captures: ⛔ first-boot setup returned HTTP $SETUP_HTTP; without the demo administrator the canonical dataset refuses to run." >&2
  exit 2
  ;;
esac
unset SETUP_TOKEN SETUP_BODY
echo "==> First-boot setup created the Northwind organization and the demo administrator"

# Select the console modules while the first engine is up. The ceremony below stops it anyway, so the engine
# that serves the captures starts with them selected. A module switched on in a running engine is not wired to
# the sessions module: a skills import answers 503 (measured on the released 0.1 image) and
# the communications evidence is unavailable until a restart. This order needs no restart of its own.
TOKEN="$(printf '%s' '{"email":"demo@olivares.local","password":"olivares-demo-estate"}' | curl --noproxy '*' -sf -X POST "http://127.0.0.1:$PORT/v1/auth/login" \
  -H 'Content-Type: application/json' --data-binary @- \
  | python3 -I -c 'import sys,json;print(json.load(sys.stdin)["token"])')"
TENANT="$(printf 'Authorization: Bearer %s\n' "$TOKEN" | curl --noproxy '*' -sf "http://127.0.0.1:$PORT/v1/auth/whoami" -H @- \
  | python3 -I -c 'import sys,json;print(json.load(sys.stdin)["grants"][0]["tenant"])')"
[ -n "$TENANT" ] || { echo "ERROR: could not resolve the demo tenant on the first engine" >&2; exit 1; }
enable_console_modules "http://127.0.0.1:$PORT" accessmap adoption catalog claude-agents compliance deploy evals finops gitpublish health inferenceproxy inventory knowledge models notify observability orchestration posture recording redteam reporting sandbox security sessions skills voice

echo "==> K3 activation ceremony on the STOPPED store"
kill "$PID" 2>/dev/null || true
PARADO=0
for _ in $(seq 1 200); do
  # An exited child stays a zombie until it is reaped, so `kill -0` alone would wait the budget out.
  ESTADO="$(cut -d' ' -f3 "/proc/$PID/stat" 2>/dev/null || true)"
  if [ -z "$ESTADO" ] || [ "$ESTADO" = "Z" ]; then
    PARADO=1
    break
  fi
  sleep 0.1
done
if [ "$PARADO" -ne 1 ]; then
  echo "docs-captures: ⛔ the seeded engine (pid $PID) did not stop in 20 s; the ceremony needs the store STOPPED." >&2
  exit 2
fi
wait "$PID" 2>/dev/null || true
PID=""
if ! OLIVARES_COMMUNICATION_ACTIVATION=on \
  OLIVARES_COMMUNICATION_CONTENT_KEYRING_FILE="$K3_CUSTODY/content-keyring.json" \
  OLIVARES_COMMUNICATION_CURSOR_KEYRING_FILE="$K3_CUSTODY/cursor-keyring.json" \
  OLIVARES_KEY_WRAP= \
  PATH=/usr/local/bin:/usr/bin:/bin \
  HOME="$DATA/home" \
  TMPDIR="${EXEC_TMP:-${TMPDIR:-/tmp}}" \
  "$BIN" db activate-directory-writer --data-dir "$DATA" --expected-generation 1 \
  --writers-upgraded --writers-drained --actor docs-captures \
  --reason "disposable documentation-capture estate, serve stopped" >"$DATA/activation.txt" 2>&1; then
  echo "docs-captures: ⛔ the K3 activation ceremony failed; without it every communications door answers 503:" >&2
  tail -20 "$DATA/activation.txt" >&2 || true
  exit 2
fi

echo "==> Reopening the same store, so readiness observes the enforced writer"
arranca_motor
ARRIBA=0
for _ in $(seq 1 480); do
  kill -0 "$PID" 2>/dev/null || break
  if curl --noproxy '*' -sf "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then
    ARRIBA=1
    break
  fi
  sleep 0.5
done
if [ "$ARRIBA" -ne 1 ]; then
  echo "docs-captures: ⛔ the reopened engine did not accept connections in 240 s, or it died. Tail of its log:" >&2
  log_tail "$DATA/engine.log" 20
  exit 2
fi
# The executable that really serves the captures, read from the running process and not from the path that was asked for.
DOCS_CAPTURE_BINARY_SHA256="$(sha256sum "/proc/$PID/exe" 2>/dev/null | cut -d' ' -f1)" || true
[[ "$DOCS_CAPTURE_BINARY_SHA256" =~ ^[0-9a-f]{64}$ ]] || { echo "docs-captures: cannot read the sha256 of the running engine" >&2; exit 2; }
export DOCS_CAPTURE_BINARY_SHA256
echo "==> Engine executable sha256 $DOCS_CAPTURE_BINARY_SHA256"

TOKEN="$(printf '%s' '{"email":"demo@olivares.local","password":"olivares-demo-estate"}' | curl --noproxy '*' -sf -X POST "http://127.0.0.1:$PORT/v1/auth/login" \
  -H 'Content-Type: application/json' \
  --data-binary @- \
  | python3 -I -c 'import sys,json;print(json.load(sys.stdin)["token"])')"
TENANT="$(printf 'Authorization: Bearer %s\n' "$TOKEN" | curl --noproxy '*' -sf "http://127.0.0.1:$PORT/v1/auth/whoami" -H @- \
  | python3 -I -c 'import sys,json;print(json.load(sys.stdin)["grants"][0]["tenant"])')"
if [ -z "$TENANT" ]; then
  echo "ERROR: could not resolve the demo tenant" >&2
  redact_log "$DATA/engine.log" <"$DATA/engine.log" >&2
  exit 1
fi

# Preliminary routing probe only. A 4xx can precede the module's readiness check and therefore
# does not prove effective K3 readiness. Each captured handoffs view must independently obtain
# an authorized HTTP 200 collection read for its selected workspace.
K3_PROBE="$(printf 'Authorization: Bearer %s\n' "$TOKEN" | curl --noproxy '*' -s -o /dev/null -w '%{http_code}' \
  -H @- -H "X-Olivares-Tenant: $TENANT" \
  "http://127.0.0.1:$PORT/v1/m/sessions/inbox?workspace_id=00000000-0000-7000-8000-000000000000" || true)"
case "$K3_PROBE" in
2?? | 4??) echo "==> K3 preliminary routing probe HTTP $K3_PROBE; authorized collection proof remains required" ;;
*)
  echo "docs-captures: ⛔ K3 preliminary routing probe failed after the ceremony (HTTP ${K3_PROBE:-none}). Tail of the engine log:" >&2
  log_tail "$DATA/engine.log" 20
  exit 2
  ;;
esac


# Seed the three guide connectors after resolving the tenant and before capturing their
# views.
OLIVARES_TOKEN="$TOKEN" OTLP_BASE="$((PORT + 30))" OLIVARES_ENGINE_PID="$PID" \
  bash "$ROOT/scripts/seed-guide-connectors.sh" \
  "$BIN" "$DATA" "$TENANT" "$PORT" || {
  echo "docs-captures: ⛔ could not seed the guide connectors; the three guide captures" >&2
  echo "   would photograph an empty Connectors tab and look like the product has none." >&2
  exit 1
}
echo "==> Demo tenant: $TENANT"

# Only authenticated 200 and the published 501 refusal describe a capability.
capability_status() {
  local status
  status="$(printf 'Authorization: Bearer %s\n' "$TOKEN" | curl --noproxy '*' -sS -o "$DATA/capability.json" -w '%{http_code}' \
    "http://127.0.0.1:$PORT$1" -H @- -H "X-Olivares-Tenant: $TENANT")" || return 1
  case "$status" in 200|501) printf '%s' "$status" ;; *) echo "ERROR: $1 returned HTTP $status; expected 200 or 501" >&2; return 1 ;; esac
}
GRAPH_STATUS="$(capability_status '/v1/m/accessmap/graph')"
IDENTITY_STATUS="$(capability_status '/v1/m/governance/identities')"
BUILD_EDITION="$(printf 'Authorization: Bearer %s\n' "$TOKEN" | curl --noproxy '*' -sf "http://127.0.0.1:$PORT/v1/server-info" -H @- \
  | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["edition"])')"
case "$BUILD_EDITION" in community|enterprise|business) ;; *) echo "ERROR: unknown build edition: $BUILD_EDITION" >&2; exit 1 ;; esac
if [ "$BUILD_EDITION" = community ] && { [ "$GRAPH_STATUS" != 200 ] || [ "$IDENTITY_STATUS" != 200 ]; }; then
  echo 'ERROR: Community must serve both the access graph and the identity inventory' >&2; exit 1
fi
echo "==> Build: $BUILD_EDITION; graph HTTP $GRAPH_STATUS; identity inventory HTTP $IDENTITY_STATUS"

# The demo dataset first: it is the one source of the canonical Northwind estate — the six
# people, the three waiting approvals, the five folders and the recorded sessions. The seeders below
# fill the surfaces it does not own and must not add a second administrator, approval or run.
echo "==> Seeding the canonical Northwind estate through the product API"
if ! OLIVARES_TOKEN="$TOKEN" python3 -I "$ROOT/scripts/seed-demo-dataset.py" \
    "http://127.0.0.1:$PORT" - "$TENANT" "$DATASET_ROOT"; then
  echo "docs-captures: ⛔ the demo dataset could not be seeded; refusing the capture phase" >&2
  exit 1
fi

# Seed work items through the product API after authentication, retaining lease and
# acceptance-criteria validation.
echo "==> Seeding work items through the product API"
OLIVARES_TOKEN="$TOKEN" python3 -I "$ROOT/scripts/seed-demo-work.py" \
  "http://127.0.0.1:$PORT" - "$TENANT" "$WS_ROOT" --canonical-estate

# Fill each surface to its target with the idempotent seeder.
# Below-target results warn and continue so the captures show the remaining gaps.
# export-closure: absent-by-design docs/launch/objetivos-sembrado.json — es el catalogo de
# objetivos de sembrado del LANZAMIENTO, y esta curacion retira `docs/launch` ENTERO
# (the export curation script). Aqui es DATO, no una llamada: en el arbol publicado la ruta
# simplemente nunca casa, nada la ejecuta y por tanto no hay llamada que guardar. Declararlo
# `hub-only` seria mentir sobre su clase — `hub-only` exige una guarda de presencia en el sitio
# de la llamada, y aqui NO hay sitio de llamada.
#
echo "==> Filling the estate to the per-surface target"
# --prefijo: the rows are on screen in the lists; a name only this harness knows would date and mark the image.
VOLUME_RC=0
OLIVARES_TOKEN="$TOKEN" python3 -I "$ROOT/scripts/seed-estate-volume.py" \
  "http://127.0.0.1:$PORT" - "$TENANT" --prefijo acme || VOLUME_RC=$?
case "$VOLUME_RC" in
0) ;;
1) echo "    (some surfaces remain below their targets; the distribution above identifies them)" ;;
*)
  # 1 is an answer: the surfaces and their counts were read and are below target. Anything else
  # means the seeder could not look (target file missing, an unreadable agent list): the capture
  # must not publish empty Deploy and Health views as if the estate were complete.
  echo "    ⛔ the estate volume seeder COULD NOT LOOK (rc $VOLUME_RC); refusing the capture phase" >&2
  exit 2
  ;;
esac

# Report synthetic health states through the product API; unreported checks stay
# unknown.
echo "==> Reporting a probe against each declared health check"
OLIVARES_TOKEN="$TOKEN" python3 -I - "$PORT" "$TENANT" "$ROOT" <<'PYHEALTH' || echo "   (health probes skipped; the panel keeps its Unknown rows)" >&2
import json, os, sys, urllib.request

port, tenant, root = sys.argv[1:4]
sys.path.insert(0, os.path.join(root, "scripts", "lib"))
from redaccion import abre

token = os.environ["OLIVARES_TOKEN"]
base = f"http://127.0.0.1:{port}"

# Module requests require X-Olivares-Tenant as well as authentication.
def call(method, path, payload=None):
    data = json.dumps(payload).encode() if payload is not None else None
    req = urllib.request.Request(base + path, data=data, method=method,
                                 headers={"Authorization": f"Bearer {token}",
                                          "X-Olivares-Tenant": tenant,
                                          "Content-Type": "application/json"})
    # The shared opener uses no proxy for loopback and refuses a cross-origin redirect, which would
    # carry the Authorization header to another origin.
    with abre(req, timeout=10) as r:
        return json.loads(r.read() or b"{}")

checks = call("GET", "/v1/m/health/checks").get("items", [])
# Include degraded states so the capture shows the health distinctions.
ok = 0
for n, c in enumerate(checks):
    cid = c.get("id") or c.get("check_id")
    if not cid:
        continue
    estado = "degraded" if n % 5 == 4 else "healthy"
    try:
        call("POST", f"/v1/m/health/checks/{cid}/report",
             {"state": estado, "latency_ms": 40 + (n * 7) % 160})
        ok += 1
    except Exception:
        pass
print(f"   {ok}/{len(checks)} checks reported")
PYHEALTH

# Adoption data comes through OTLP. Register the source before reloading with SIGHUP;
# the runtime-reload API requires AAL3. A receiver already holding the port cannot be
# reconfigured there with the same address.
OTLP_PORT="$((PORT + 30))"
echo "==> Registering the claude connector for the OTLP plane on 127.0.0.1:$OTLP_PORT"
# Use the guide's claude-code-prod row for OTLP too; only one source may own
# olivares.claude.
if PATH=/usr/local/bin:/usr/bin:/bin HOME="$DATA/home" "$BIN" sources set --name claude-code-prod --kind claude --tenant "$TENANT" \
  --actor "docs-captures" --reason "seed the adoption plane for documentation captures" \
  --data-dir "$DATA" \
  --config "http_addr=127.0.0.1:$OTLP_PORT" --config enable_grpc=false \
  --config resource_labels=team,project,cost_center >/dev/null 2>&1; then
  # Reload to start the connector receiver before posting metrics.
  kill -HUP "$PID" 2>/dev/null || true
  OTLP_UP=0
  for _ in $(seq 1 20); do
    if curl --noproxy '*' -sf --connect-timeout 1 --max-time 2 -o /dev/null \
      -X POST "http://127.0.0.1:$OTLP_PORT/v1/metrics" \
      -H 'Content-Type: application/json' -d '{"resourceMetrics":[]}'; then
      OTLP_UP=1
      break
    fi
    sleep 0.5
  done
  # Report the engine's rejection reason when the receiver fails to start.
  if [ "$OTLP_UP" != "1" ] && command grep -q 'source rejected on reload' "$DATA/engine.log" 2>/dev/null; then
    echo "    ⛔ reload REJECTED the source; the engine explains why:"
    command grep 'source rejected on reload' "$DATA/engine.log" | tail -1 | sed 's/^/       /'
  fi
  if [ "$OTLP_UP" = "1" ]; then
    echo "==> Seeding the adoption plane through the connector receiver"
    # resource_labels must be set at registration so team labels reach adoption data.
    # Every session id the receiver sees is also a row of the Sessions list, with one event
    # and no name, so the volume is a team's, not a fleet's: four teams of two sessions
    # (the default of six teams of eight put 48 identical rows on the Sessions and
    # AgentOps views, #2268).
    OLIVARES_TOKEN="$TOKEN" python3 -I "$ROOT/scripts/seed-adoption-otlp.py" \
      "http://127.0.0.1:$PORT" - "$TENANT" \
      --otlp "http://127.0.0.1:$OTLP_PORT/v1/metrics" --equipos 4 --por-equipo 2 || {
      echo "    (adoption seeding failed; captures will show zero adoption)"
    }
  else
    # Warn about missing adoption data and continue capturing the other views.
    echo "    ⛔ the OTLP receiver did not start on 127.0.0.1:$OTLP_PORT — captures will show ZERO adoption"
  fi
else
  echo "    ⛔ could not register the claude connector — captures will show ZERO adoption"
fi

# The home counts every observed row, including the adoption rows no run launched. The dataset's
# own check ran before adoption; read the plane back now so a row that landed on today (or still
# reads as live) refuses the capture instead of publishing a figure nobody seeded.
echo "==> Reading back the live plane after adoption"
if ! OLIVARES_TOKEN="$TOKEN" python3 -I "$ROOT/scripts/seed-demo-dataset.py" \
    "http://127.0.0.1:$PORT" - "$TENANT" "$DATASET_ROOT" --verify-live-plane; then
  echo "docs-captures: ⛔ an observed row outside the canonical runs would move the captured home; refusing the capture phase" >&2
  exit 2
fi

# Capture /setup on a separate unseeded engine; the seeded engine redirects to login.
# Only setup_required=true makes this route eligible for capture.
SETUP_PORT="$((PORT + 20))"
SETUP_DATA="$(mktemp -d)"
cleanup_setup() {
  [ -n "${SETUP_PID:-}" ] && kill "$SETUP_PID" 2>/dev/null || true
  rm -rf "$SETUP_DATA"
}
trap 'cleanup; cleanup_setup' EXIT

echo "==> Booting a SECOND engine, UNSEEDED, on 127.0.0.1:$SETUP_PORT (first-boot wizard)"
mkdir -p "$SETUP_DATA/home"
PATH=/usr/local/bin:/usr/bin:/bin HOME="$SETUP_DATA/home" \
  "$BIN" serve --insecure --listen "127.0.0.1:$SETUP_PORT" \
  --grpc-listen "127.0.0.1:$((SETUP_PORT + 1))" --data-dir "$SETUP_DATA" >"$SETUP_DATA/engine.log" 2>&1 &
SETUP_PID=$!
# Check that the owned engine remains alive so another service cannot satisfy readiness.
SETUP_UP=0
for _ in $(seq 1 40); do
  kill -0 "$SETUP_PID" 2>/dev/null || break
  if curl --noproxy '*' -sf --connect-timeout 2 --max-time 5 "http://127.0.0.1:$SETUP_PORT/healthz" >/dev/null 2>&1; then
    SETUP_UP=1
    break
  fi
  sleep 0.5
done
# False or unknown setup readiness warns and omits /setup; other captures continue.
# Known limit: a server-side user-read failure can also report setup_required=true;
# this client cannot distinguish that result from a fresh installation.
SETUP_REQUIRED=unknown
if [ "$SETUP_UP" = "1" ]; then
  SETUP_REQUIRED="$(python3 -I -c 'import sys,json;v=json.load(sys.stdin).get("setup_required");print("true" if v is True else ("false" if v is False else "unknown"))' \
    <<<"$(curl --noproxy '*' -sf --connect-timeout 2 --max-time 10 "http://127.0.0.1:$SETUP_PORT/v1/server-info")" 2>/dev/null || echo unknown)"
else
  echo "==> ⛔ the SECOND engine did not accept connections (or stopped): COULD NOT LOOK" >&2
fi
if [ "$SETUP_REQUIRED" = "true" ]; then
  echo "==> Second engine reports setup_required=true — the wizard is photographable"
  export SETUP_BASE_URL="http://127.0.0.1:$SETUP_PORT"
else
  echo "==> ⚠ Second engine reports setup_required=$SETUP_REQUIRED — skipping /setup capture (could not look)" >&2
  redact_log "$SETUP_DATA/engine.log" <"$SETUP_DATA/engine.log" >&2 || true
fi

# Resolve the recording-session ID from the API and omit its viewer when none exists.
DEMO_SESSION_ID="$(printf 'Authorization: Bearer %s\n' "$TOKEN" | curl --noproxy '*' -sf "http://127.0.0.1:$PORT/v1/m/recording/sessions" \
  -H @- -H "X-Olivares-Tenant: $TENANT" \
  | python3 -I -c 'import sys,json;d=json.load(sys.stdin);i=(d.get("items") or []);print(i[0]["id"] if i else "")' 2>/dev/null || echo "")"
if [ -n "$DEMO_SESSION_ID" ]; then
  echo "==> Demo recording session: $DEMO_SESSION_ID"
  export DEMO_SESSION_ID
else
  echo "==> ⚠ the seeded estate exposes no recording sessions — skipping /session-viewer capture" >&2
fi

# Clear the output directory so filtered runs cannot publish captures left by an earlier
# run.
rm -rf "$ROOT/web/playwright-report/docs"
mkdir -p "$ROOT/web/playwright-report/docs"

echo "==> Running the docs-captures Playwright spec against live seeded data"
cd "$ROOT/web"
RC=0
SPECS=(e2e/docs-captures.spec.ts)
# The Community spec above captures the access map itself. A non-Community build that
# serves the graph must bring its private capture suite; an installed one is always run.
if [ "$BUILD_EDITION" != community ] && [ "$GRAPH_STATUS" = 200 ]; then
  [ -f e2e/docs-captures-business.spec.ts ] || { echo 'ERROR: available Business graph requires its private capture suite' >&2; exit 1; }
fi
SKIP_IDS="${DOCS_CAPTURE_SKIP:-}"
export DOCS_CAPTURE_SKIP_IDS="$SKIP_IDS" DOCS_CAPTURE_EDITION="$BUILD_EDITION"
SKIP_ARGS=()
if [ -n "$SKIP_IDS" ]; then
  SKIP_ARGS=(--grep-invert "capture ($SKIP_IDS)( —| \\(|\$)")
  echo "==> Leaving out: $SKIP_IDS"
fi
if [ -f e2e/docs-captures-business.spec.ts ]; then
  SPECS+=(e2e/docs-captures-business.spec.ts)
fi
# Playwright's request client sends Secure browser-session cookies on localhost.
PLAYWRIGHT_BASE_URL="http://localhost:$PORT" DEMO_TENANT="$TENANT" \
  pnpm exec playwright test "${SPECS[@]}" "${SKIP_ARGS[@]}" "$@" || RC=$?

# Merge per-capture evidence even after a failed run so missing captures remain visible.
echo "==> Merging evidence from each capture into manifest.json"
DOCS_DIR="$ROOT/web/playwright-report/docs" python3 -I - <<'PY' || true
import glob, json, os, subprocess, time

d = os.environ["DOCS_DIR"]
tomas = []
skipped = {i: "Excluded by DOCS_CAPTURE_SKIP"
           for i in os.environ.get("DOCS_CAPTURE_SKIP_IDS", "").split("|") if i}
for f in sorted(glob.glob(os.path.join(d, "*.evidence.json"))):
    with open(f, encoding="utf8") as fh:
        evidence = json.load(fh)
    if "skipped" in evidence:
        skipped[evidence["id"]] = evidence["skipped"]
    else:
        tomas.append(evidence)
    os.remove(f)  # Per-capture files are merged into the manifest.


def git(*a):
    try:
        return subprocess.run(["git", *a], capture_output=True, text=True, check=True).stdout.strip()
    except Exception:
        return None


# Record dirty state with the commit; a commit alone cannot reproduce a modified tree.
sucio = git("status", "--porcelain")
por_vista = {}
for t in tomas:
    por_vista.setdefault(t["id"], set()).add(t["theme"])

# List views missing either theme in the same run.
descabalados = sorted(k for k, v in por_vista.items() if v != {"light", "dark"})

man = {
    "taken_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    "commit": git("rev-parse", "HEAD"),
    "release_image": os.environ.get("DOCS_CAPTURE_IMAGE"),
    "binary_sha256": os.environ.get("DOCS_CAPTURE_BINARY_SHA256"),
    "edition": os.environ.get("DOCS_CAPTURE_EDITION"),
    "tree_dirty": bool(sucio),
    "views": len(por_vista),
    "captures": len(tomas),
    "unpaired": descabalados,
    "skipped": skipped,
    # List panel IDs and measurements, ordered by text length and empty-panel count.
    # An empty panel does not prove that the view is empty or defective.
    "empty_panels": [
        {"id": i, "paneles_vacios": v, "texto_main": m}
        for i, v, m in sorted(
            {
                (t["id"], t.get("vacios", 0), t.get("texto_main", 0))
                for t in tomas
                if t.get("vacios", 0) > 0 and t["theme"] == "light"
            },
            key=lambda x: (x[2], -x[1]),
        )
    ],
    # Retain the declared reasons for captured panels without rows; do not fabricate
    # data.
    "empty_by_control": {
        "rate-limits": "the Anthropic Admin-API governance ingest is not wired; the view shows"
                       " the reason rather than a fabricated inventory",
        "console": "AAL3 step-up required to issue an invitation, so the pending-invitations"
                   " panel stays empty; a password-token seeder must not mint accounts",
    },
    # Keep uncaptured routes separate from captured panels without rows.
    "not_captured_by_control": {
        "accept-invite": "issuing an invitation requires AAL3 step-up (hardware,"
                         " phishing-resistant) and the harness session is AAL1, so a live"
                         " invitation cannot be seeded; the tokenless screen is an ERROR state"
                         " and publishing it as the view would be false authority. Retired"
                         " 2026-08-29 rather than published stale-by-control: a PNG that can"
                         " never be refreshed is a future trap. Restoring it needs AAL3 in the"
                         " harness, not a new selector.",
    },
    # Raw i18n keys must be zero; the usage check does not cover dynamic keys.
    "raw_i18n_keys": sum(t.get("claves_crudas", 0) for t in tomas),
    "raw_i18n_keys_by_capture": [
        {"id": t["id"], "theme": t["theme"], "n": t.get("claves_crudas", 0),
         "vistas": t.get("claves_crudas_vistas", [])}
        for t in sorted(tomas, key=lambda t: (t["id"], t["theme"]))
        if t.get("claves_crudas", 0) > 0
    ],
    "take": sorted(tomas, key=lambda t: (t["id"], t["theme"])),
}
with open(os.path.join(d, "manifest.json"), "w", encoding="utf8") as fh:
    json.dump(man, fh, indent=2, ensure_ascii=False)
    fh.write("\n")

if man["raw_i18n_keys"]:
    print("    RAW i18n KEYS: %d" % man["raw_i18n_keys"])
    for e in man["raw_i18n_keys_by_capture"][:8]:
        print("        %-26s %-6s %d  %s" % (e["id"], e["theme"], e["n"], ", ".join(e["vistas"][:4])))
else:
    print("    raw i18n keys: 0")

if man["empty_panels"]:
    print(f"    ⚠ {len(man['empty_panels'])} view(s) with an empty panel — C10-02 candidates, NOT empty views:")
    for e in man["empty_panels"][:12]:
        print(f"      {e['id']:24} empty panels={e['paneles_vacios']:2}  text in main={e['texto_main']}")
print(f"    {man['captures']} captures across {man['views']} views · commit {man['commit']}"
      + (" · ⚠ DIRTY TREE: the SHA does NOT reproduce these images" if man["tree_dirty"] else ""))
if descabalados:
    print(f"    ⚠ missing light/dark pair: {', '.join(descabalados)}")
for view, reason in skipped.items():
    print(f"    SKIPPED {view}: {reason}")
PY

# PUBLICAR=1 requires a successful run, a clean tree and a manifest alongside the
# images.
if [ "${PUBLICAR:-0}" = "1" ]; then
  SRC="$ROOT/web/playwright-report/docs"
  if [ "$RC" -ne 0 ]; then
    echo "==> ⛔ NOT publishing: the run exited $RC. A failing batch has unverified captures." >&2
    exit "$RC"
  fi
  # Read the whole status output; an early-exiting consumer can trigger SIGPIPE under
  # pipefail.
  DIRTY="$(cd "$ROOT" && git status --porcelain)"
  if [ -n "$DIRTY" ]; then
    echo "==> ⛔ NOT publishing: the tree is DIRTY, so the manifest SHA does not reproduce these" >&2
    echo "       images. Commit or clean the changes, then run it again." >&2
    exit 2
  fi
  # Without the manifest that records where the captures come from, nothing is copied over the published images.
  manifiesto=0
  for art in "$SRC"/*; do
    if [ -f "$art" ] && [ "${art##*/}" = "manifest.json" ]; then manifiesto=1; fi
  done
  if [ "$manifiesto" -eq 0 ]; then
    echo "==> ⛔ NOT publishing: no manifest.json next to the captures, so their provenance is missing. Rerun the harness." >&2
    exit 2
  fi
  DEST="$ROOT/docs-site/public/console"
  mkdir -p "$DEST"
  # Publish current output by directory glob, excluding intermediate evidence files.
  # The exclusion list is empty because accept-invite is not captured; its missing-route
  # reason stays in the manifest. Keep capture eligibility distinct from publication.
  no_publicar=""
  n=0
  retenidas=0
  sin_pagina=0
  # The site serves only captures that a docs page, a README or a launch post names. Every take stays in $SRC and
  # in the manifest, which is what the route coverage check counts.
  # shellcheck source=lib/capture-refs.sh
  . "$ROOT/scripts/lib/capture-refs.sh"
  referenciadas="$(referenced_captures "$ROOT")" || exit 2
  for art in "$SRC"/*; do
    [ -f "$art" ] || continue
    base="$(basename "$art")"
    case "$base" in
    *.evidence.json) continue ;;
    *.png)
      # Here-string, not a pipe: an early `grep -q` exit would SIGPIPE the writer under pipefail.
      if ! grep -qxF "$base" <<<"$referenciadas"; then
        sin_pagina=$((sin_pagina + 1))
        continue
      fi
      ;;
    esac
    id_toma="${base%-light.png}"; id_toma="${id_toma%-dark.png}"
    case " $no_publicar " in
    *" $id_toma "*)
      retenidas=$((retenidas + 1))
      continue
      ;;
    esac
    cp -f "$art" "$DEST/$base"
    n=$((n + 1))
  done
  echo "==> PUBLISHED $n artifact(s) in docs-site/public/console/ (including the manifest)"
  [ "$retenidas" -gt 0 ] && echo "==> WITHHELD $retenidas capture(s) because their declared state is not representative: $no_publicar" >&2
  [ "$sin_pagina" -gt 0 ] && echo "==> NOT PUBLISHED $sin_pagina capture(s) that no docs page, README or launch post names; they stay in $SRC and in the manifest" >&2
  echo "==> Check the limit on the SAME commit:  task lint:screenshot-coverage"
fi

exit $RC
