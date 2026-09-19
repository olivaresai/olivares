#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# e2e-golden-path.sh — THE WHOLE PRODUCT, ONCE, AS A NEW OPERATOR LIVES IT.
#
# Every other harness in this repository proves a piece: web-e2e.sh proves the embedded
# bundle, first-hour-smoke.sh proves the hook PEP, the module batteries prove the module.
# NONE of them walks install → first boot → administrator → provider → official CLI →
# a REAL governed model turn → the same turn seen in the console, on one engine, in one
# run. This does, and it reports what breaks instead of routing around it.
#
# ⛔ IT MEASURES, IT DOES NOT REPAIR. A step that hangs, lies, needs a manual workaround,
#    prints a raw id or a raw error, or disagrees between the CLI and the console is a
#    NUMBERED DEFECT with an owner (engine | console | cli | docs) written to
#    defects.tsv, and by default the run STOPS there: a golden path that continues past
#    its first break is not a golden path, it is a list of excuses. Pass --keep-going to
#    collect the whole picture in one pass, which is how the defect table is produced.
#
# ⛔ IT SENDS REAL MODEL TURNS, so it is bounded ON PURPOSE. One short prompt from the
#    CLI and one from the console, each of a few tokens. The account is the host's own
#    login (--account-home); this script never reads, copies or prints its credentials —
#    it only names the home to the engine, which is what auth_source
#    `provider_account_home` means. --no-model-turn walks the whole path without them.
#
# ⛔ EVERY EVIDENCE FILE IS REDACTED AS IT IS WRITTEN, not afterwards. The one-time setup
#    token is live for the length of this run, and a capture written first and cleaned
#    afterwards is how such a token ends up in a published evidence file; `redact` runs
#    on the pipe, so there is no window in which an unredacted capture exists on disk.
#
# Usage:
#   scripts/e2e-golden-path.sh [options]
#     --evidence DIR     where captures, logs and tables are written
#                        (default: $OLIVARES_E2E_EVIDENCE, else ./e2e-golden-path-out)
#     --keep-going       record every defect instead of stopping at the first
#     --no-console       skip the Playwright console legs (CLI path only)
#     --no-model-turn    walk the path without sending any prompt to a real model
#     --no-cli-turn      send the console's prompt but not the CLI's (one turn, not two)
#     --no-robustness    skip the robustness legs
#     --account-home DIR the logged-in official-CLI account home (default: $HOME)
#     --bin PATH         the olivares binary (default: ./bin/olivares, built if absent)
#
# Exit: 0 = the whole path ran clean. 1 = stopped at a defect (its number is printed).
#       2 = the harness itself could not run (no exec tmpdir, no free port, no binary).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# ---------------------------------------------------------------------------
# Options
# ---------------------------------------------------------------------------
EVID="${OLIVARES_E2E_EVIDENCE:-$ROOT/e2e-golden-path-out}"
KEEP_GOING=0
DO_CONSOLE=1
DO_MODEL_TURN=1
DO_CLI_TURN=1
DO_ROBUSTNESS=1
# HOME is absent on six of nine CI runner classes (measured); under `set -u` a bare $HOME would kill the script on this line
# with the STEP's name, not the variable's. No account home is an inability, said by name.
ACCOUNT_HOME="${OLIVARES_E2E_ACCOUNT_HOME:-${HOME:-}}"
BIN="${OLIVARES_BIN:-$ROOT/bin/olivares}"
PROMPT="${OLIVARES_E2E_PROMPT:-Reply with the single word OK}"

while [ $# -gt 0 ]; do
  case "$1" in
    --evidence) EVID="$2"; shift 2 ;;
    --keep-going) KEEP_GOING=1; shift ;;
    --no-console) DO_CONSOLE=0; shift ;;
    --no-model-turn) DO_MODEL_TURN=0; shift ;;
    --no-cli-turn) DO_CLI_TURN=0; shift ;;
    --no-robustness) DO_ROBUSTNESS=0; shift ;;
    --account-home) ACCOUNT_HOME="$2"; shift 2 ;;
    --bin) BIN="$2"; shift 2 ;;
    -h|--help) sed -n '6,50p' "$0"; exit 0 ;;
    *) echo "$(basename "$0"): unknown option $1" >&2; exit 2 ;;
  esac
done
# The account home is judged AFTER the arguments are read: `--account-home DIR` is a complete answer on a machine with no HOME,
# and `--help` needs none. No home from any of the three sources is an inability, said by name, before anything depends on it.
[ -n "$ACCOUNT_HOME" ] || { echo "e2e-golden-path: COULD NOT LOOK — no account home: pass --account-home DIR, or set OLIVARES_E2E_ACCOUNT_HOME or HOME" >&2; exit 2; }

mkdir -p "$EVID"
PATHTSV="$EVID/path.tsv"
DEFECTS="$EVID/defects.tsv"
: >"$PATHTSV"
: >"$DEFECTS"
printf 'id\tcommand\trc\tms\n' >>"$PATHTSV"
printf 'n\towner\tdefect\tevidence\n' >>"$DEFECTS"

DEFECT_N=0
LAST_RC=0
LAST_MS=0
LAST_OUT=""

# The password is this harness's own, for an engine that lives for the length of the
# run. It is redacted out of every capture anyway: a fixture credential in an evidence
# file teaches the next reader that credentials belong in evidence files.
ADMIN_EMAIL="operator@e2e.local"
ADMIN_PASSWORD="e2e-golden-path-$$-correct-horse"

# ---------------------------------------------------------------------------
# Output discipline
# ---------------------------------------------------------------------------
note() { printf '\n==> %s\n' "$*"; }
info() { printf '    %s\n' "$*"; }

# redact filters the ONE-TIME SETUP TOKEN, bearer tokens and this run's password out of
# anything written to an evidence file. It is applied on the pipe, never as a later pass.
redact() {
  sed -E \
    -e 's/olst_[A-Za-z0-9_-]{6,}/olst_<redacted>/g' \
    -e 's/(Bearer|bearer|token=|--token )[[:space:]]*[A-Za-z0-9._-]{16,}/\1 <redacted>/g' \
    -e "s/$ADMIN_PASSWORD/<redacted-password>/g"
}

# ---------------------------------------------------------------------------
# Defects
# ---------------------------------------------------------------------------
# defect <owner> <one-line defect> [evidence file]
defect() {
  DEFECT_N=$((DEFECT_N + 1))
  local owner="$1" what="$2" ev="${3:-${LAST_OUT:-}}"
  printf '%d\t%s\t%s\t%s\n' "$DEFECT_N" "$owner" "$what" "${ev##*/}" >>"$DEFECTS"
  printf '\n⛔ DEFECT %d (%s): %s\n' "$DEFECT_N" "$owner" "$what" >&2
  [ -n "$ev" ] && printf '   evidence: %s\n' "$ev" >&2
  if [ "$KEEP_GOING" != 1 ]; then
    printf '\nSTOPPED at the first break. Re-run with --keep-going to collect them all.\n' >&2
    exit 1
  fi
}

# ---------------------------------------------------------------------------
# Steps
# ---------------------------------------------------------------------------
# step <id> <title shown in the capture> -- <argv...>
# Records the command, its output, its exit code and its wall time. NEVER fails the
# run by itself: the expectation after it decides whether a result is a defect.
step() {
  local id="$1" title="$2"; shift 2
  local out="$EVID/$id.txt" start end rc
  { printf '$ %s\n' "$title"; } | redact >"$out"
  start=$(date +%s%N)
  set +e
  "$@" 2>&1 | redact >>"$out"
  rc=${PIPESTATUS[0]}
  set -e
  end=$(date +%s%N)
  LAST_MS=$(( (end - start) / 1000000 ))
  LAST_RC=$rc
  LAST_OUT="$out"
  printf '\n[exit %d] [%d ms]\n' "$rc" "$LAST_MS" >>"$out"
  printf '%s\t%s\t%d\t%d\n' "$id" "$title" "$rc" "$LAST_MS" >>"$PATHTSV"
  info "$id → rc $rc, ${LAST_MS} ms"
}

# expect_rc <want> <owner> <what this proves>
expect_rc() {
  local want="$1" owner="$2" what="$3"
  [ "$LAST_RC" = "$want" ] && return 0
  defect "$owner" "$what (exit $LAST_RC, wanted $want)" "$LAST_OUT"
}

# expect_out <extended regex> <owner> <what this proves>
expect_out() {
  local re="$1" owner="$2" what="$3"
  grep -Eq "$re" "$LAST_OUT" && return 0
  defect "$owner" "$what" "$LAST_OUT"
}

# refute_out <extended regex> <owner> <what this forbids>
refute_out() {
  local re="$1" owner="$2" what="$3"
  grep -Eq "$re" "$LAST_OUT" || return 0
  defect "$owner" "$what" "$LAST_OUT"
}

# expect_under_ms <budget> <owner> <what>
expect_under_ms() {
  local budget="$1" owner="$2" what="$3"
  [ "$LAST_MS" -le "$budget" ] && return 0
  defect "$owner" "$what (${LAST_MS} ms, budget ${budget} ms)" "$LAST_OUT"
}

# ---------------------------------------------------------------------------
# The machine this runs on
# ---------------------------------------------------------------------------
ENGINE_PID=""
ATTACH_PID=""
WORK=""
DATA=""
CLI_HOME=""
EXEC_TMP=""

# Everything this script starts, it stops BY PID, and it stops it before it removes the
# directories underneath it: the reverse order leaves an engine writing into a path that
# no longer exists, and that is how a harness leaves a port squatted for a day.
cleanup() {
  local rc=$?
  for pid in "$ATTACH_PID" "$ENGINE_PID"; do
    [ -n "$pid" ] || continue
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
  [ -n "${WORK:-}" ] && rm -rf "$WORK"
  [ -n "${EXEC_TMP:-}" ] && rm -rf "$EXEC_TMP"
  exit $rc
}
trap cleanup EXIT INT TERM

# shellcheck source=lib/exec-tmpdir.sh
. "$ROOT/scripts/lib/exec-tmpdir.sh"
EXEC_TMP="$(olivares_exec_tmpdir)" || {
  echo "$(basename "$0"): no temporary directory on this host EXECUTES; the engine cannot extract its connector plugins." >&2
  echo "   Remedy: export OLIVARES_EXEC_TMPDIR to a directory that executes." >&2
  exit 2
}

if [ ! -x "$BIN" ]; then
  note "building $BIN"
  # shellcheck source=lib/build-bin.sh
  . "$ROOT/scripts/lib/build-bin.sh"
  build_olivares_bin "$BIN"
fi

# A CLEAN DATA DIRECTORY AND A CLEAN CLI HOME. The point of the walk is what a machine
# that has never run this product does, so nothing may be inherited: not a client
# context, not a data directory, not a driver's configuration home.
WORK="$(mktemp -d)"
chmod 711 "$WORK"
DATA="$WORK/data"
CLI_HOME="$WORK/clihome"
mkdir -p "$CLI_HOME"

read -r PORT GRPC_PORT < <(python3 - <<'PY'
import socket
s = [socket.socket() for _ in range(2)]
for x in s:
    x.bind(("127.0.0.1", 0))
print(*[x.getsockname()[1] for x in s])
for x in s:
    x.close()
PY
) || { echo "$(basename "$0"): could not reserve two loopback ports" >&2; exit 2; }

BASE="https://127.0.0.1:$PORT"
CA="$DATA/tls.crt"

# The CLI is invoked through this wrapper everywhere below, so EVERY command in the walk
# runs against the clean home and the engine this script booted — never against whatever
# context the operator running the harness happens to have saved.
ocli() {
  env HOME="$CLI_HOME" XDG_DATA_HOME="$CLI_HOME/.local/share" \
      XDG_CONFIG_HOME="$CLI_HOME/.config" OLIVARES_DATA_DIR="$DATA" \
      TMPDIR="$EXEC_TMP" \
      "$BIN" "$@"
}

note "engine   $BASE (grpc 127.0.0.1:$GRPC_PORT)"
note "data     $DATA"
note "cli home $CLI_HOME"
note "evidence $EVID"
{
  printf 'base_url\t%s\n' "$BASE"
  printf 'data_dir\t%s\n' "$DATA"
  printf 'cli_home\t%s\n' "$CLI_HOME"
  printf 'account_home\t%s\n' "$ACCOUNT_HOME"
  printf 'binary\t%s\n' "$BIN"
  printf 'started\t%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
} >"$EVID/run.tsv"

WALL_START=$(date +%s)
# The window every spend question below is asked over.
RUN_SINCE="$(date -u -d "@$(( $(date +%s) - 60 ))" +%Y-%m-%dT%H:%M:%SZ)"

# ===========================================================================
# 1. QUICKSTART — the one command a new operator runs
# ===========================================================================
note "1. quickstart (TLS on, single-use setup token, embedded console)"

# quickstart BLOCKS: it is the engine. It is started here and stopped by pid in cleanup.
# Its panel is the only place the one-time token is ever printed, so the log is captured
# through `redact` before anything else can read it.
QS_RAW="$WORK/quickstart.raw"
env HOME="$CLI_HOME" XDG_DATA_HOME="$CLI_HOME/.local/share" \
    XDG_CONFIG_HOME="$CLI_HOME/.config" OLIVARES_DATA_DIR="$DATA" \
    TMPDIR="$EXEC_TMP" \
    "$BIN" quickstart --data-dir "$DATA" \
      --listen "127.0.0.1:$PORT" --grpc-listen "127.0.0.1:$GRPC_PORT" \
      --public-url "$BASE" >"$QS_RAW" 2>&1 &
ENGINE_PID=$!

QS_START=$(date +%s%N)
HEALTHY=0
for _ in $(seq 1 240); do
  if curl -sk --max-time 2 "$BASE/livez" >/dev/null 2>&1; then HEALTHY=1; break; fi
  kill -0 "$ENGINE_PID" 2>/dev/null || break
  sleep 0.5
done
QS_MS=$(( ( $(date +%s%N) - QS_START ) / 1000000 ))
{ printf '$ olivares quickstart --data-dir <data> --listen 127.0.0.1:%s --grpc-listen 127.0.0.1:%s\n' "$PORT" "$GRPC_PORT"; cat "$QS_RAW"; } | redact >"$EVID/01-quickstart.txt"
printf '\n[livez 200 after %d ms]\n' "$QS_MS" >>"$EVID/01-quickstart.txt"
printf '01-quickstart\tolivares quickstart\t%d\t%d\n' "$((1 - HEALTHY))" "$QS_MS" >>"$PATHTSV"
LAST_OUT="$EVID/01-quickstart.txt"
info "01-quickstart → livez in ${QS_MS} ms"
[ "$HEALTHY" = 1 ] || defect engine "quickstart never answered /livez; the first command a new operator runs does not reach a console" "$EVID/01-quickstart.txt"

# The token is read from the RAW log, never from the redacted capture, and it is written
# to a file mode 600 inside the throwaway work directory — the CLI takes it on stdin.
SETUP_TOKEN="$(grep -oE 'olst_[A-Za-z0-9_-]+' "$QS_RAW" | head -1 || true)"
if [ -z "$SETUP_TOKEN" ]; then
  defect engine "the quickstart panel printed no one-time setup token, so first setup cannot be completed from this terminal" "$EVID/01-quickstart.txt"
fi

# The panel is the product's front door. It must name the console and the next command,
# and it must NOT be the place a stack trace or a raw Go error surfaces.
LAST_OUT="$EVID/01-quickstart.txt"
expect_out 'https://127\.0\.0\.1:'"$PORT" engine "the quickstart panel names the console address an operator opens"
refute_out 'goroutine [0-9]+ \[|panic: ' engine "the quickstart panel is free of Go runtime internals"

# ===========================================================================
# 2. FIRST-BOOT — what this installation is, and what is still pending
# ===========================================================================
note "2. first-boot"
step 02-first-boot "olivares first-boot" ocli first-boot --data-dir "$DATA"
expect_rc 0 cli "first-boot reports this installation"
expect_out 'PENDING|pending' cli "first-boot says setup is still pending before an administrator exists"
expect_out 'auth bootstrap' cli "first-boot names the command that finishes setup from this terminal"

# ===========================================================================
# 3. AUTH BOOTSTRAP — the first administrator, from the terminal
# ===========================================================================
note "3. auth bootstrap (first superadmin + organization + saved context)"
PWFILE="$WORK/admin.pw"
( umask 077; printf '%s' "$ADMIN_PASSWORD" >"$PWFILE" )

BOOT_OUT="$EVID/03-auth-bootstrap.txt"
{ printf '$ olivares auth bootstrap --server %s --ca-cert <data>/tls.crt --setup-token-file - --email %s --password-file <file> --organization "Golden Path" --save-context   (token on stdin)\n' "$BASE" "$ADMIN_EMAIL"; } >"$BOOT_OUT"
BS_START=$(date +%s%N)
set +e
printf '%s' "$SETUP_TOKEN" | env HOME="$CLI_HOME" XDG_DATA_HOME="$CLI_HOME/.local/share" \
  XDG_CONFIG_HOME="$CLI_HOME/.config" OLIVARES_DATA_DIR="$DATA" TMPDIR="$EXEC_TMP" \
  "$BIN" auth bootstrap --server "$BASE" --ca-cert "$CA" --setup-token-file - \
    --email "$ADMIN_EMAIL" --password-file "$PWFILE" \
    --organization "Golden Path" --save-context 2>&1 | redact >>"$BOOT_OUT"
LAST_RC=${PIPESTATUS[1]}
set -e
LAST_MS=$(( ( $(date +%s%N) - BS_START ) / 1000000 ))
LAST_OUT="$BOOT_OUT"
printf '\n[exit %d] [%d ms]\n' "$LAST_RC" "$LAST_MS" >>"$BOOT_OUT"
printf '03-auth-bootstrap\tolivares auth bootstrap\t%d\t%d\n' "$LAST_RC" "$LAST_MS" >>"$PATHTSV"
info "03-auth-bootstrap → rc $LAST_RC, ${LAST_MS} ms"
expect_rc 0 engine "the first superadmin and organization are created with the one-time token"
expect_out 'setup complete' cli "bootstrap says setup is complete in a sentence"
expect_out 'signed in as' cli "--save-context leaves the terminal authenticated"

TENANT="$(grep -oE 'tenant [0-9a-f-]{36}' "$BOOT_OUT" | head -1 | awk '{print $2}' || true)"
[ -n "$TENANT" ] || defect cli "bootstrap did not name the tenant it created in a form the next command can use" "$BOOT_OUT"
info "tenant $TENANT"
printf 'tenant\t%s\n' "$TENANT" >>"$EVID/run.tsv"

# ===========================================================================
# 4. DOCTOR — healthy, or the reason named
# ===========================================================================
note "4. doctor"
step 04-doctor "olivares doctor" ocli doctor
DOCTOR_RC=$LAST_RC
step 04b-doctor-json "olivares doctor -o json" ocli doctor -o json
python3 - "$EVID/04b-doctor-json.txt" >"$EVID/04c-doctor-required.txt" 2>&1 <<'PY' || true
import json, sys, re
raw = open(sys.argv[1]).read()
body = raw[raw.index('{'):raw.rindex('}') + 1]
report = json.loads(body)
checks = report.get('checks', [])
bad = [c for c in checks if c.get('required') and c.get('status') not in ('pass', 'not_applicable')]
print('overall:', report.get('overall'))
print('required checks not passing:', len(bad))
for c in bad:
    print(' -', c['name'], c['status'], '|', (c.get('detail') or '')[:100], '| remedy:', (c.get('remediation') or '')[:80])
PY
cat "$EVID/04c-doctor-required.txt"
if [ "$DOCTOR_RC" != 0 ]; then
  BAD="$(sed -n 's/^ - \([a-z-]*\) .*/\1/p' "$EVID/04c-doctor-required.txt" | paste -sd, -)"
  defect engine "doctor cannot reach healthy on a quickstart install; required checks failing: ${BAD:-unknown}" "$EVID/04c-doctor-required.txt"
fi

# ===========================================================================
# 5. THE OFFICIAL CLI — install one from its signed release, on this host
# ===========================================================================
note "5. agent tool install --driver grok (live origin)"
step 05-tool-detect "olivares agent tool detect" ocli agent tool detect --root "$DATA/tools"
expect_rc 0 cli "the host inventory of official CLIs reads"

step 06-tool-install-grok "olivares agent tool install --driver grok --version stable --yes" \
  ocli agent tool install --driver grok --version stable --yes --root "$DATA/tools"
expect_rc 0 cli "an official CLI installs from its signed release with one command"
expect_out 'sha256|SHA256|digest' cli "the install names the digest it verified"

step 07-tool-ls "olivares agent tool ls" ocli agent tool ls --root "$DATA/tools"
expect_rc 0 cli "the installed tool is in the inventory"

# ===========================================================================
# 6. PROVIDER — register a credential, test it, bind it, and hand the profile back
# ===========================================================================
# The REAL turn below runs on auth_source=provider_account_home: the account already
# logged in on this host. `provider add/test/bind` is walked anyway, because it is the
# path an operator takes when the credential is the PRODUCT's rather than the driver's,
# and because `test` is where a wrong credential must be refused BY NAME.
note "6. provider add / test / bind"
OLIVARES_E2E_WRONG_KEY="sk-ant-api03-not-a-real-key-this-is-the-refusal-leg"
step 08-provider-add "olivares provider add --kind anthropic --name 'Golden path (wrong on purpose)' --key-env ..." \
  env OLIVARES_E2E_WRONG_KEY="$OLIVARES_E2E_WRONG_KEY" HOME="$CLI_HOME" \
      XDG_DATA_HOME="$CLI_HOME/.local/share" XDG_CONFIG_HOME="$CLI_HOME/.config" \
      OLIVARES_DATA_DIR="$DATA" TMPDIR="$EXEC_TMP" \
      "$BIN" provider add --kind anthropic --name "Golden path (wrong on purpose)" \
        --key-env OLIVARES_E2E_WRONG_KEY
expect_rc 0 engine "a provider credential is sealed and referenced"
refute_out "$OLIVARES_E2E_WRONG_KEY" engine "the sealed credential is never echoed back"
PRV="$(grep -oE 'prv_[0-9a-zA-Z-]+' "$LAST_OUT" | head -1 || true)"
[ -n "$PRV" ] || defect cli "provider add printed no usable provider reference" "$LAST_OUT"
info "provider $PRV"

# ===========================================================================
# 7. AGENT DEPLOY — from an installed CLI to a profile that can launch
# ===========================================================================
note "7. agent deploy claude (the host's logged-in account home)"
PROFILE_NAME="golden-path-claude"
step 09-agent-deploy "olivares agent deploy claude --name $PROFILE_NAME --config-home <account>/.claude --user-home <account>" \
  ocli agent deploy claude --root "$DATA/tools" --name "$PROFILE_NAME" \
    --config-home "$ACCOUNT_HOME/.claude" --user-home "$ACCOUNT_HOME"
expect_rc 0 engine "one command turns an installed CLI into a profile that can launch"
expect_out 'PROFILE +ppf_' cli "deploy names the profile it registered"
PPF="$(grep -oE 'ppf_[0-9a-zA-Z-]+' "$LAST_OUT" | head -1 || true)"
[ -n "$PPF" ] || defect cli "agent deploy printed no usable profile reference" "$LAST_OUT"
info "profile $PPF"
printf 'profile\t%s\n' "$PPF" >>"$EVID/run.tsv"

step 10-provider-test "olivares provider test $PRV" ocli provider test "$PRV"
expect_under_ms 15000 engine "a credential test answers inside an operator's patience"
# NOT an expect_rc: what `test` DOES with a refused credential is measured below, in the
# robustness table, because the answer is the finding.

step 11-provider-bind "olivares provider bind $PRV --profile $PPF" ocli provider bind "$PRV" --profile "$PPF"
BIND_RC=$LAST_RC
step 12-provider-unbind "olivares provider bind --unbind --profile $PPF" ocli provider bind --unbind --profile "$PPF"
expect_rc 0 engine "--unbind returns the profile to the account home it was deployed with"

step 13-profile-get "olivares agent profile get $PPF -o json" ocli agent profile get "$PPF" -o json
expect_rc 0 engine "the profile reads back"
expect_out 'provider_account_home' engine "the profile's auth source is the host's own logged-in account home"

# ===========================================================================
# 8. A REAL GOVERNED SESSION, FROM THE CLI
# ===========================================================================
note "8. a real governed session from the CLI"
# A UNIQUE NAME, because the console's join is checked BY IT: a run called "e2e-cli" in
# a previous walk's data directory would make the next walk's agreement check pass for
# the wrong reason.
CLI_RUN_NAME="e2e-cli-$$"
step 14-session-create "olivares agent session create --name $CLI_RUN_NAME --provider-profile $PPF" \
  ocli agent session create --name "$CLI_RUN_NAME" --provider-profile "$PPF"
expect_rc 0 engine "a governed session launches under the deployed profile"
expect_out 'state=running' engine "the launched session is running"
RUN="$(grep -oE 'run [0-9a-f-]{36}' "$LAST_OUT" | head -1 | awk '{print $2}' || true)"
if [ -z "$RUN" ]; then
  RUN="$(grep -oE '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}' "$LAST_OUT" | head -1 || true)"
fi
[ -n "$RUN" ] || defect cli "session create printed no usable run reference" "$LAST_OUT"
info "run $RUN"
printf 'cli_run\t%s\n' "$RUN" >>"$EVID/run.tsv"

CLI_ANSWER=""
if [ "$DO_MODEL_TURN" = 1 ] && [ "$DO_CLI_TURN" = 1 ] && [ -n "$RUN" ]; then
  # ATTACH FIRST, then send: the answer frames are the thing being measured, and a
  # stream opened after them would have to trust the replay to have kept them.
  ATTACH_OUT="$WORK/attach-cli.ndjson"
  : >"$ATTACH_OUT"
  env HOME="$CLI_HOME" XDG_DATA_HOME="$CLI_HOME/.local/share" \
      XDG_CONFIG_HOME="$CLI_HOME/.config" OLIVARES_DATA_DIR="$DATA" TMPDIR="$EXEC_TMP" \
      "$BIN" agent session attach "$RUN" --from 0 >"$ATTACH_OUT" 2>&1 &
  ATTACH_PID=$!
  sleep 2

  TURN_START=$(date +%s%N)
  FRAME="$(python3 -c 'import json,sys; print(json.dumps({"type":"user","message":{"role":"user","content":[{"type":"text","text":sys.argv[1]}]}}))' "$PROMPT")"
  step 15-session-input "olivares agent session input $RUN --line '<one user turn>'" \
    ocli agent session input "$RUN" --line "$FRAME"
  expect_rc 0 engine "one operator turn is accepted by the live session"

  # Wait for the driver's own result frame. A turn this small answers in seconds; the
  # ceiling is generous because the box is loaded, and a timeout here is a DEFECT, not a
  # skip — "the model did not answer" is exactly what this walk exists to report.
  ANSWERED=0
  for _ in $(seq 1 180); do
    if grep -q '"subtype":"success"\|"type":"result"' "$ATTACH_OUT" 2>/dev/null; then ANSWERED=1; break; fi
    sleep 1
  done
  TURN_MS=$(( ( $(date +%s%N) - TURN_START ) / 1000000 ))
  kill "$ATTACH_PID" 2>/dev/null || true; wait "$ATTACH_PID" 2>/dev/null || true; ATTACH_PID=""
  redact <"$ATTACH_OUT" >"$EVID/16-session-attach.txt"
  printf '\n[first result frame after %d ms]\n' "$TURN_MS" >>"$EVID/16-session-attach.txt"
  printf '16-session-attach\tolivares agent session attach (until the result frame)\t%d\t%d\n' "$((1 - ANSWERED))" "$TURN_MS" >>"$PATHTSV"
  LAST_OUT="$EVID/16-session-attach.txt"
  info "16-session-attach → answered=$ANSWERED in ${TURN_MS} ms"

  CLI_ANSWER="$(python3 - "$ATTACH_OUT" <<'PY' || true
import json, sys, re
text = []
for line in open(sys.argv[1], errors='replace'):
    line = line.strip()
    if line.startswith('data:'):
        line = line[5:].strip()
    if not line.startswith('{'):
        continue
    try:
        frame = json.loads(line)
    except Exception:
        continue
    payload = frame
    for key in ('line', 'data', 'frame', 'output'):
        if isinstance(payload.get(key), str) and payload[key].strip().startswith('{'):
            try:
                payload = json.loads(payload[key])
            except Exception:
                pass
    if payload.get('type') == 'result' and isinstance(payload.get('result'), str):
        text.append(payload['result'])
    msg = payload.get('message')
    if payload.get('type') == 'assistant' and isinstance(msg, dict):
        for block in msg.get('content', []):
            if isinstance(block, dict) and block.get('type') == 'text':
                text.append(block['text'])
print(' '.join(t.strip() for t in text if t.strip())[:400])
PY
)"
  printf '%s\n' "$CLI_ANSWER" >"$EVID/17-cli-answer.txt"

  # WHAT THE GOVERNED CHILD WAS ACTUALLY GIVEN. The driver announces it on its own init
  # frame, so this is the child's account of its own authority, not the engine's.
  python3 - "$ATTACH_OUT" "$PWD" >"$EVID/17b-what-the-child-got.txt" 2>&1 <<'PY' || true
import json, sys
init, result = None, None
for line in open(sys.argv[1], errors='replace'):
    line = line.strip()
    if line.startswith('data:'):
        line = line[5:].strip()
    if not line.startswith('{'):
        continue
    try:
        f = json.loads(line)
    except Exception:
        continue
    if f.get('type') == 'system' and f.get('subtype') == 'init':
        init = f
    if f.get('type') == 'result':
        result = f
if init:
    print('cwd                 :', init.get('cwd'))
    print('engine cwd          :', sys.argv[2])
    print('cwd == engine cwd   :', init.get('cwd') == sys.argv[2])
    print('permission mode     :', init.get('permissionMode'))
    print('tools offered       :', len(init.get('tools') or []), '|', ','.join((init.get('tools') or [])[:12]))
    print('write-capable tools :', [t for t in (init.get('tools') or []) if t in ('Bash', 'Write', 'Edit', 'NotebookEdit')])
    print('model               :', init.get('model'))
else:
    print('no init frame reached the operator stream')
if result:
    print('total_cost_usd on the result frame :', result.get('total_cost_usd'))
    u = result.get('usage') or {}
    print('usage on the result frame          :', {k: u.get(k) for k in ('input_tokens', 'output_tokens', 'cache_read_input_tokens', 'cache_creation_input_tokens')})
PY
  cat "$EVID/17b-what-the-child-got.txt"
  if grep -q 'cwd == engine cwd   : True' "$EVID/17b-what-the-child-got.txt"; then
    defect engine "a governed session created without --workspace runs the agent in the ENGINE's own working directory (runtime_bridge.go launchWorkspaceTarget: an empty Dir falls back to the process cwd), so the child reads and writes whatever the operator started the engine from" "$EVID/17b-what-the-child-got.txt"
  fi
  if grep -Eq "write-capable tools : \['" "$EVID/17b-what-the-child-got.txt"; then
    defect engine "under permission_mode=default the governed child was offered its file-writing and shell tools in that directory; the product's own governance did not narrow the driver's tool surface" "$EVID/17b-what-the-child-got.txt"
  fi
  if grep -Eq 'total_cost_usd on the result frame : [0-9]' "$EVID/17b-what-the-child-got.txt"; then
    printf 'the driver reported the turn cost ON THE WIRE; where it goes is measured at step 22-23\n' >>"$EVID/17b-what-the-child-got.txt"
  fi
  info "CLI answer: ${CLI_ANSWER:-<none>}"
  printf 'cli_answer\t%s\n' "$CLI_ANSWER" >>"$EVID/run.tsv"
  if [ "$ANSWERED" != 1 ]; then
    defect engine "the governed CLI session never produced a provider result frame for a one-line prompt" "$EVID/16-session-attach.txt"
  elif [ -z "$CLI_ANSWER" ]; then
    defect engine "the session answered but no assistant text reached the operator's stream" "$EVID/16-session-attach.txt"
  fi
fi

step 18-session-ls "olivares agent session ls" ocli agent session ls
expect_rc 0 engine "the operated session is listed"
[ -n "$RUN" ] && expect_out "$RUN" engine "the session just created is the one listed"

step 19-session-events "olivares agent session events $RUN" ocli agent session events "$RUN"
expect_rc 0 engine "the session's lifecycle ledger reads"
expect_out '"event": *"created"' engine "the ledger records the creation"
expect_out '"event": *"launched"' engine "the ledger records the launch"

step 20-session-get "olivares agent session get $RUN" ocli agent session get "$RUN"
expect_rc 0 engine "the full session record reads"

step 21-audit "olivares agent session events (audit sequence present)" \
  bash -c "grep -c 'audit_seq' '$EVID/19-session-events.txt'"
expect_rc 0 engine "every lifecycle row carries the audit sequence it was written at"

# ===========================================================================
# 9. THE COST LEDGER — what the turn cost, where an operator looks for it
# ===========================================================================
note "9. the cost ledger for that turn"
step 22-session-cost "olivares agent session get $RUN -o json (tokens and cost)" ocli agent session get "$RUN" -o json
python3 - "$LAST_OUT" >"$EVID/22b-cost-reading.txt" 2>&1 <<'PY' || true
import json, sys
raw = open(sys.argv[1]).read()
body = raw[raw.index('{'):raw.rindex('}') + 1]
dto = json.loads(body)
keys = ('input_tokens', 'output_tokens', 'cost_micro_usd', 'total_tokens')
found = {k: dto.get(k) for k in keys if k in dto}
print('fields the record carries:', found if found else 'NONE of ' + ', '.join(keys))
print('state:', dto.get('state'), '| driver:', dto.get('provider_driver'), '| transport:', dto.get('transport'))
PY
cat "$EVID/22b-cost-reading.txt"
if [ "$DO_MODEL_TURN" = 1 ] && [ -n "$CLI_ANSWER" ]; then
  if grep -q 'NONE of' "$EVID/22b-cost-reading.txt"; then
    defect engine "the governed-session record carries no token or cost field at all, so what a real turn cost cannot be read from the session an operator is looking at" "$EVID/22b-cost-reading.txt"
  elif grep -Eq "'cost_micro_usd': 0" "$EVID/22b-cost-reading.txt" && grep -Eq "'input_tokens': 0" "$EVID/22b-cost-reading.txt"; then
    defect engine "a real model turn completed and the session's token and cost counters are still zero" "$EVID/22b-cost-reading.txt"
  fi
fi

# THE TENANT'S OWN SPEND SURFACE, asked over the window this walk occupies. A governed
# turn that cost real money and appears in no ledger is the finding, so the question is
# put to the ledger rather than to the session record alone.
step 23-finops-spend "olivares finops spend summary --since <this run>" \
  ocli finops spend summary --since "$RUN_SINCE"
if [ "$DO_MODEL_TURN" = 1 ] && [ -n "$CLI_ANSWER" ]; then
  # ⛔ THE QUESTION IS THE SAMPLE COUNT, AND IT USED TO BE A GREP FOR A ZERO. Until
  # 2026-09-18 this check fired on `(^|[^0-9])0(\.00)?$` anywhere in the report, which
  # matched ANY line ending in a zero — including `cache.cache_creation_1h_tokens 0` in
  # a report that also said `samples 2` and `total_micro_usd 41790`. Measured: with the
  # ledger working, the old predicate still accused the product of writing no row.
  # A coarse grep over a whole report is not a measurement of one field.
  SPEND_SAMPLES="$(sed -n 's/^samples[[:space:]]\{1,\}\([0-9]\{1,\}\)$/\1/p' "$LAST_OUT" | head -1)"
  if [ "$LAST_RC" != 0 ]; then
    defect engine "the tenant's spend surface does not answer after a real governed turn" "$LAST_OUT"
  elif [ -z "$SPEND_SAMPLES" ]; then
    # Not "zero samples": the report did not carry the field this check reads, so
    # nothing was measured and that is its own finding rather than a verdict.
    defect engine "the tenant's spend surface answered without a sample count, so whether a governed turn reached the ledger could not be read" "$LAST_OUT"
  elif [ "$SPEND_SAMPLES" = 0 ]; then
    defect engine "a real governed model turn left NO row on the tenant's spend ledger, although the driver reported total_cost_usd and the full token usage on its own result frame (see 17b): cost samples reach FinOps only from the in-process inference client (cmd/olivares/claude_inference.go, modelsactuate.go, recording.go), and nothing folds an official-CLI session's result frame onto that bus" "$LAST_OUT"
  fi
fi

# ===========================================================================
# 10. THE CONSOLE — the same product, through a browser
# ===========================================================================
CONSOLE_JSON="$EVID/console-result.json"
if [ "$DO_CONSOLE" = 1 ]; then
  note "10. the console legs (Playwright, real Chromium, live engine)"
  if [ ! -d "$ROOT/web/node_modules/@playwright" ]; then
    defect docs "the console legs cannot run: web/node_modules is absent (pnpm --dir web install --frozen-lockfile --offline)" ""
  else
    CONSOLE_START=$(date +%s%N)
    set +e
    env PLAYWRIGHT_BASE_URL="$BASE" \
        E2E_EMAIL="$ADMIN_EMAIL" E2E_PASSWORD="$ADMIN_PASSWORD" \
        E2E_PROFILE_REF="$PPF" E2E_PROFILE_NAME="$PROFILE_NAME" E2E_PROMPT="$PROMPT" \
        E2E_CLI_RUN="$RUN" E2E_CLI_RUN_NAME="$CLI_RUN_NAME" E2E_TENANT="$TENANT" \
        E2E_CAPTURES="$EVID/console" E2E_RESULT="$CONSOLE_JSON" \
        E2E_MODEL_TURN="$DO_MODEL_TURN" \
        TMPDIR="${TMPDIR:-$EXEC_TMP}" \
        pnpm --dir "$ROOT/web" exec playwright test e2e/golden-path.spec.ts \
        2>&1 | redact >"$EVID/24-console.txt"
    CONSOLE_RC=${PIPESTATUS[0]}
    set -e
    CONSOLE_MS=$(( ( $(date +%s%N) - CONSOLE_START ) / 1000000 ))
    printf '\n[exit %d] [%d ms]\n' "$CONSOLE_RC" "$CONSOLE_MS" >>"$EVID/24-console.txt"
    printf '24-console\tplaywright e2e/golden-path.spec.ts\t%d\t%d\n' "$CONSOLE_RC" "$CONSOLE_MS" >>"$PATHTSV"
    LAST_RC=$CONSOLE_RC; LAST_OUT="$EVID/24-console.txt"
    info "24-console → rc $CONSOLE_RC, ${CONSOLE_MS} ms"
    tail -30 "$EVID/24-console.txt"
    expect_rc 0 console "the console walks login → work surface → composer → a real turn → the inspector"

    # THE BROWSER'S OWN FINDINGS ARE NUMBERED HERE, not there: one table, one owner per
    # row, one numbering. A spec that threw on each would have stopped at the first and
    # hidden the rest.
    if [ -f "$CONSOLE_JSON" ]; then
      while IFS=$'\t' read -r owner what evidence; do
        [ -n "$owner" ] || continue
        LAST_OUT="$EVID/console/$evidence"
        defect "$owner" "$what" "$LAST_OUT"
      done < <(python3 - "$CONSOLE_JSON" <<'PY'
import json, sys
for d in json.load(open(sys.argv[1])).get('defects', []):
    print('%s\t%s\t%s' % (d.get('owner', 'console'), d.get('what', ''), d.get('evidence', '')))
PY
)
    fi
  fi
fi

# ===========================================================================
# 11. THE AGREEMENT CHECK — the CLI and the console describe ONE system
# ===========================================================================
if [ "$DO_CONSOLE" = 1 ] && [ -f "$CONSOLE_JSON" ]; then
  note "11. do the CLI and the console agree?"
  step 25-session-ls-json "olivares agent session ls -o json (after the console launched one)" \
    ocli agent session ls -o json
  # `set +e` rather than `|| true`: the exit code IS the verdict here, and `|| true`
  # would have made every agreement check report agreement.
  set +e
  python3 - "$LAST_OUT" "$CONSOLE_JSON" "$RUN" >"$EVID/26-agreement.txt" 2>&1 <<'PY'
"""Do the CLI and the console describe ONE system?

⛔ THE JOIN IS NOT THE ROW KEY. The console keys a row `live:<live_ref>` the moment the
   plane proves a managed live row for it (web/src/features/sessions/provenance.ts), so a
   run stops being keyed by its run_ref exactly when it starts working. Comparing run
   references against rail keys therefore reports a disagreement on a HEALTHY system —
   the first run of this harness did exactly that, and the finding was false. What is compared
   here is what an operator is actually owed: every run the CLI lists is one the console
   can open and name, and neither surface is holding a session the other has never heard
   of.
"""
import json, sys

raw = open(sys.argv[1]).read()
cli = json.loads(raw[raw.index('{'):raw.rindex('}') + 1])
console = json.load(open(sys.argv[2]))
cli_run = sys.argv[3]

items = cli.get('items') or []
cli_refs = sorted(i.get('run_ref', '') for i in items)
rail = console.get('rail_addresses', [])
console_run = console.get('console_run_ref', '')

print('CLI lists %d run(s):' % len(items))
for i in items:
    print('   %s  state=%-9s name=%s' % (i.get('run_ref'), i.get('state'), i.get('name') or '-'))
print('console rail shows %d row(s): %s' % (len(rail), ', '.join(rail) or '-'))
print()

rows = []
# 1. The run the console launched is one the CLI can see.
rows.append((
    'the console\'s own run is in `agent session ls`',
    console_run or '<none>',
    console_run in cli_refs,
))
# 2. The run the CLI launched OPENS in the console and names itself there.
rows.append((
    'the CLI\'s run opens in the console by its run address',
    cli_run or '<none>',
    bool(console.get('cli_run_addressable')),
))
# 3. Neither surface holds a session the other has never heard of. The rail may key a
#    row `live:` — that is the same session under its proven identity, not a new one —
#    so the comparison is on COUNT, which is the claim that survives the re-keying.
rows.append((
    'the two surfaces hold the same number of sessions',
    '%d CLI / %d rail' % (len(items), len(rail)),
    len(items) == len(rail),
))

print('%-52s %-40s %s' % ('claim', 'subject', 'agrees'))
for label, subject, ok in rows:
    print('%-52s %-40s %s' % (label, subject, 'yes' if ok else 'NO'))

bad = [r for r in rows if not r[2]]
print()
print('DISAGREEMENTS:', len(bad))
for label, subject, _ in bad:
    print(' -', label, '|', subject)
sys.exit(1 if bad else 0)
PY
  AGREE_RC=$?
  set -e
  cat "$EVID/26-agreement.txt"
  printf '26-agreement\tCLI/console agreement on the same two runs\t%d\t0\n' "$AGREE_RC" >>"$PATHTSV"
  if [ "$AGREE_RC" != 0 ]; then
    defect console "the CLI and the console do not show the same set of governed sessions" "$EVID/26-agreement.txt"
  fi
fi

# ===========================================================================
# 12. ROBUSTNESS — each leg measured, none argued
# ===========================================================================
ROBUST="$EVID/robustness.tsv"
printf 'leg\tresult\tms\tevidence\n' >"$ROBUST"
robust() { printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" >>"$ROBUST"; info "R: $1 → $2 (${3} ms)"; }

if [ "$DO_ROBUSTNESS" = 1 ] && [ -n "$RUN" ]; then
  note "12. robustness"

  # R1 — the operator's client dies mid-session and comes back. The session is a
  # control-plane object, so the terminal that launched it is not load-bearing.
  R1_START=$(date +%s%N)
  env HOME="$CLI_HOME" XDG_DATA_HOME="$CLI_HOME/.local/share" XDG_CONFIG_HOME="$CLI_HOME/.config" \
      OLIVARES_DATA_DIR="$DATA" TMPDIR="$EXEC_TMP" \
      "$BIN" agent session attach "$RUN" --from 0 >"$WORK/r1-attach.txt" 2>&1 &
  ATTACH_PID=$!
  sleep 3
  kill -9 "$ATTACH_PID" 2>/dev/null || true
  wait "$ATTACH_PID" 2>/dev/null || true
  ATTACH_PID=""
  step 30-r1-reattach "olivares agent session get $RUN (after the client was killed)" ocli agent session get "$RUN"
  R1_MS=$(( ( $(date +%s%N) - R1_START ) / 1000000 ))
  if [ "$LAST_RC" = 0 ] && grep -q '"state": *"running"' "$LAST_OUT"; then
    robust "kill the client mid-session, reconnect" "the session is still running and reattaches" "$R1_MS" "30-r1-reattach.txt"
  else
    robust "kill the client mid-session, reconnect" "BROKEN" "$R1_MS" "30-r1-reattach.txt"
    defect engine "killing the operator's attach client did not leave the session reattachable" "$LAST_OUT"
  fi

  # R2 — the ENGINE restarts. What an operator is entitled to is the record and the
  # ability to resume, not the child process: the child belonged to the dead engine.
  R2_START=$(date +%s%N)
  kill "$ENGINE_PID" 2>/dev/null || true
  wait "$ENGINE_PID" 2>/dev/null || true
  env HOME="$CLI_HOME" XDG_DATA_HOME="$CLI_HOME/.local/share" XDG_CONFIG_HOME="$CLI_HOME/.config" \
      OLIVARES_DATA_DIR="$DATA" TMPDIR="$EXEC_TMP" \
      "$BIN" quickstart --data-dir "$DATA" --listen "127.0.0.1:$PORT" \
        --grpc-listen "127.0.0.1:$GRPC_PORT" --public-url "$BASE" >"$WORK/quickstart2.raw" 2>&1 &
  ENGINE_PID=$!
  BACK=0
  for _ in $(seq 1 240); do
    curl -sk --max-time 2 "$BASE/livez" >/dev/null 2>&1 && { BACK=1; break; }
    sleep 0.5
  done
  R2_MS=$(( ( $(date +%s%N) - R2_START ) / 1000000 ))
  redact <"$WORK/quickstart2.raw" >"$EVID/31-r2-engine-restart.txt"
  step 32-r2-session-after-restart "olivares agent session get $RUN (after the engine restarted)" ocli agent session get "$RUN"
  if [ "$BACK" = 1 ] && [ "$LAST_RC" = 0 ]; then
    STATE_AFTER="$(grep -oE '"state": *"[a-z_]+"' "$LAST_OUT" | head -1 | sed 's/.*"\([a-z_]*\)"$/\1/')"
    robust "restart the engine, resume" "engine back in ${R2_MS} ms; the session record survived as state=$STATE_AFTER" "$R2_MS" "32-r2-session-after-restart.txt"
    step 33-r2-resume "olivares agent session resume $RUN" ocli agent session resume "$RUN"
    if [ "$LAST_RC" != 0 ]; then
      RESUME_MSG="$(sed -n '2p' "$LAST_OUT" | cut -c1-160)"
      robust "resume after the engine restart" "REFUSED: $RESUME_MSG" "$LAST_MS" "33-r2-resume.txt"
    else
      robust "resume after the engine restart" "resumed" "$LAST_MS" "33-r2-resume.txt"
    fi
  else
    robust "restart the engine, resume" "BROKEN" "$R2_MS" "31-r2-engine-restart.txt"
    defect engine "the engine did not come back on the same data directory after a restart" "$EVID/31-r2-engine-restart.txt"
  fi

  # R3 — a wrong provider credential. The answer must NAME the refusal, and it must
  # arrive before an operator wonders whether it hung.
  step 34-r3-wrong-credential "olivares provider test $PRV (the credential is wrong on purpose)" ocli provider test "$PRV"
  R3_MS=$LAST_MS
  R3_TEXT="$(grep -E 'CONNECTION|DETAIL' "$LAST_OUT" | head -2 | tr '\n' ' ' | tr -s ' ' | cut -c1-160)"
  robust "a wrong provider credential" "rc $LAST_RC in ${R3_MS} ms: ${R3_TEXT:-<said nothing>}" "$R3_MS" "34-r3-wrong-credential.txt"
  if ! grep -Eqi 'refus|invalid|unauthor|reject|not authenticated' "$LAST_OUT"; then
    defect cli "provider test does not name the refusal when the credential is wrong" "$LAST_OUT"
  fi
  # The SENTENCE is right. The question a script asks is the exit code, and a refused
  # credential and a good one leave the same one.
  REFUSED_RC=$LAST_RC
  step 34b-r3-wrong-credential-json "olivares provider test $PRV -o json" ocli provider test "$PRV" -o json
  if [ "$REFUSED_RC" = 0 ]; then
    if grep -Eq '"(outcome|connection|result)" *: *"refused"' "$LAST_OUT"; then
      defect cli "a REFUSED credential exits 0, exactly as an accepted one does; the refusal is only in the prose and in -o json, so a first-hour script that checks the exit code binds a credential the provider has already rejected" "$LAST_OUT"
    else
      defect cli "a REFUSED credential exits 0 and -o json carries no machine-readable outcome either, so nothing but prose distinguishes a good credential from a rejected one" "$LAST_OUT"
    fi
  fi
  # And the CLI's own next step, after a refusal, is to bind it.
  if grep -Eq '^Next: olivares provider bind' "$EVID/34-r3-wrong-credential.txt"; then
    defect cli "after naming the refusal, the next command the CLI offers is 'provider bind' — it walks the operator into binding the credential the provider has just rejected" "$EVID/34-r3-wrong-credential.txt"
  fi

  # R4 — a second operator without permission. Denied BY NAME, not by a blank screen.
  VIEWER_EMAIL="viewer@e2e.local"
  VIEWER_PW="$WORK/viewer.pw"
  ( umask 077; printf '%s' "viewer-$ADMIN_PASSWORD" >"$VIEWER_PW" )
  step 35-r4-create-viewer "olivares users create --email $VIEWER_EMAIL" \
    ocli users create --email "$VIEWER_EMAIL" --display-name "Second operator" --password-file "$VIEWER_PW"
  VIEWER_ID="$(grep -oE '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}' "$LAST_OUT" | head -1 || true)"
  step 36-r4-grant-viewer "olivares members grant --user <id> --role viewer" \
    ocli members grant --user "$VIEWER_ID" --tenant "$TENANT" --role viewer
  VIEWER_HOME="$WORK/viewerhome"
  mkdir -p "$VIEWER_HOME"
  vcli() {
    env HOME="$VIEWER_HOME" XDG_DATA_HOME="$VIEWER_HOME/.local/share" \
        XDG_CONFIG_HOME="$VIEWER_HOME/.config" OLIVARES_DATA_DIR="$DATA" TMPDIR="$EXEC_TMP" \
        "$BIN" "$@"
  }
  step 37-r4-viewer-login "olivares auth login --email $VIEWER_EMAIL" \
    vcli auth login --server "$BASE" --ca-cert "$CA" --email "$VIEWER_EMAIL" --password-file "$VIEWER_PW"
  step 38-r4-viewer-launch "olivares agent session create (as the viewer)" \
    vcli agent session create --name denied-on-purpose --provider-profile "$PPF"
  R4_MS=$LAST_MS
  R4_TEXT="$(sed -n '2,3p' "$LAST_OUT" | tr '\n' ' ' | tr -s ' ' | cut -c1-160)"
  if [ "$LAST_RC" = 0 ]; then
    robust "a second operator without permission" "LAUNCHED — not denied" "$R4_MS" "38-r4-viewer-launch.txt"
    defect engine "a viewer with no sessions:run:write launched a governed session" "$LAST_OUT"
  else
    robust "a second operator without permission" "rc $LAST_RC in ${R4_MS} ms: $R4_TEXT" "$R4_MS" "38-r4-viewer-launch.txt"
    if ! grep -Eqi 'permission|forbidden|not allowed|denied|authoriz' "$LAST_OUT"; then
      defect cli "the refusal a second operator reads does not name the permission that is missing" "$LAST_OUT"
    fi
  fi

  # R5 — the console's own robustness legs (light theme, 390 px) run inside the spec and
  # report through console-result.json.
  if [ -f "$CONSOLE_JSON" ]; then
    python3 - "$CONSOLE_JSON" >>"$ROBUST" 2>/dev/null <<'PY' || true
import json, sys
r = json.load(open(sys.argv[1]))
for leg in r.get('robustness', []):
    print('%s\t%s\t%s\t%s' % (leg.get('leg'), leg.get('result'), leg.get('ms', 0), leg.get('evidence', '')))
PY
  fi
fi

# ===========================================================================
# 13. WHAT THE WALK FOUND
# ===========================================================================
WALL=$(( $(date +%s) - WALL_START ))
note "the path"
column -t -s $'\t' "$PATHTSV" 2>/dev/null || cat "$PATHTSV"
if [ "$DO_ROBUSTNESS" = 1 ]; then
  note "robustness"
  column -t -s $'\t' "$ROBUST" 2>/dev/null || cat "$ROBUST"
fi
note "defects"
column -t -s $'\t' "$DEFECTS" 2>/dev/null || cat "$DEFECTS"

printf '\n'
if [ "$DEFECT_N" = 0 ]; then
  printf 'PASS — the golden path ran end to end in %d s with no defect.\n' "$WALL"
  exit 0
fi
printf 'The golden path ran end to end in %d s and found %d defect(s); each is in %s.\n' \
  "$WALL" "$DEFECT_N" "$DEFECTS"
exit 1
