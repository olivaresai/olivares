#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# End-to-end smoke for the embedded web UI. Builds the web bundle into the Go embed
# dir, compiles the olivares binary, boots it on localhost in --insecure mode
# with a fresh data dir, captures the one-time setup token, and runs the Playwright
# specs against the REAL embedded artifact.
#
# WHY THIS RUNS MORE THAN ONE ENGINE (2026-08-05). Until today it booted ONE engine
# and handed every spec to it. But the setup token is SINGLE-USE by design, so the
# first spec that walks /setup consumes it and every later first-boot spec finds a
# configured engine, no #token field, and dies on a 30 s locator timeout. Measured on
# this repo: agentops (first alphabetically) did the setup, and onboarding-first-hour
# and smoke both failed with `waiting for locator('#token')`. Neither was a real
# defect and neither had ever been seen, because the battery had never run here.
#
# So first-boot specs now get a VIRGIN ENGINE EACH, and the rest share one. Which
# specs those are is DERIVED, never listed: a spec that fills `#token` is doing a
# first boot. A hard-coded list would drift the first time somebody adds a spec.
#
# Usage: scripts/web-e2e.sh [extra playwright args]
#   Extra args are forwarded to the SHARED run only — passing a filter that selects a
#   first-boot spec is handled by matching it there too, so `scripts/web-e2e.sh
#   e2e/smoke.spec.ts` still does the right thing.
#
#   E2E_SPECS="e2e/<a>.spec.ts ..." runs ONLY those specs (a first-boot one still gets its
#   own virgin engine), after a STRICT frozen install, with Playwright's JSON report gated by
#   scripts/lib/e2e-selection.sh: a missing or empty report, or any skip, answers 2 (could not
#   look); a failure or a retried pass answers 1. A report left by an earlier run is removed
#   before each run, so it can never be read as this run's answer. E2E_SPECS that is SET but
#   selects nothing (empty or blank) answers 2 before anything is built; only an UNSET
#   E2E_SPECS takes the default run. That is the qualification form a CI job uses.
#   E2E_REPORT_DIR places the JSON reports (default web/playwright-report/e2e-json).
#   Every run exports E2E_WORK_DIR, the harness-owned work directory its cleanup removes,
#   for spec fixtures that must exist on the engine's host.
# Requires: go, pnpm, and Playwright's chromium (`pnpm --dir web exec playwright install chromium`).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PORT="${E2E_PORT:-8456}"
BIN="$ROOT/bin/olivares"
WORK="$(mktemp -d)"

# The pids live in a FILE, not in PIDS, because every boot happens inside a command
# substitution and an array append there dies with the subshell. Measured 2026-08-08: this
# trap had never killed anything, and the engines it leaked went on to squat the ports for
# more than a day. See scripts/lib/engine-boot.sh for the full account.
# Sourced HERE, before cleanup() is defined and before the trap is armed, and the order is
# load-bearing: cleanup calls engine_proc_starttime, so a failure between `trap` and the
# old source position would have run a cleanup whose helpers did not exist yet. That is a
# trap that reports success at doing nothing, which is the class this whole file removes.
# shellcheck source=lib/engine-boot.sh
. "$ROOT/scripts/lib/engine-boot.sh"

# THIS SCRIPT BUILDS THROWAWAY DIRECTORIES AND NOW RUNS GIT, SO IT SANITISES FIRST.
#
# git exports GIT_DIR to every hook invoked from a LINKED WORKTREE, and GIT_DIR OUTRANKS `-C`.
# A script that pairs `mktemp -d` with a git call can therefore drive the LIVE repository
# instead of its sandbox — measured on 2026-08-06, when it left the branch of PR #526 pointing
# at a fixture commit and stamped core.bare=true on the shared config.
#
# It became a member of that class in this repository's own history rather than by design:
# restoring `build_olivares_bin` here (the fix a merge had reverted) is what introduced the
# git call, and lint:git-env went red on main for every contributor. Sourcing IS the unset — the
# helper clears the inherited environment in the caller's shell, which is exactly what a
# script with its own working tree wants.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=lib/git-env.sh
. "$_olivares_git_env" || {
  echo "ERROR: cannot source $_olivares_git_env — refusing to run git beside a mktemp sandbox" >&2
  exit 1
}

# The engine binary is built ONE way outside a release: see scripts/lib/build-bin.sh.
# Writing the flags out longhand here is what let five scripts drift from build:bin — and
# RESTORING it is not decoration: the union of #625 and #626 was resolved with
# `git checkout --theirs`, which takes the WHOLE file and silently dropped this line and its
# call below. #625 had already fixed this script; the merge un-fixed it.
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/build-bin.sh"
# shellcheck source=lib/e2e-selection.sh
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/e2e-selection.sh"

PIDFILE="$WORK/engine.pids"
: >"$PIDFILE"

# EACH LINE IS `<pid> <starttime>`, NOT A BARE PID. Written by boot_engine_at; read here.
# The identity is what makes this trap safe on a host three containers share — see
# engine_proc_starttime in the library for the demonstration that a bare pid killed an
# innocent `sleep 300`.
cleanup() {
  # Kill BEFORE rm -rf: the old order removed the data directories out from under engines
  # that went on running, which is exactly the state found on this box.
  if [ -s "$PIDFILE" ]; then
    local p want now i
    while read -r p want; do
      [ -n "${p:-}" ] || continue
      now="$(engine_proc_starttime "$p")"
      # THREE ANSWERS, NOT TWO. The pid is GONE, it is PROVABLY ours, or we cannot tell —
      # and the third must never fall through to the second.
      if [ -z "$now" ]; then continue; fi          # gone: nothing to signal
      if [ -z "$want" ]; then
        # No identity was recorded at launch, so ownership cannot be established NOW
        # either: signalling would be the bare-pid behaviour this block exists to remove.
        # Refuse, and SAY it — silence here leaks an engine, which is the defect that
        # produced a squatter alive for a day and five hours.
        echo "cleanup: pid $p has NO recorded identity — refusing to signal it." >&2
        echo "         If an engine is still holding a port, it is this one; kill it BY PID." >&2
        continue
      fi
      if [ "$now" != "$want" ]; then
        echo "cleanup: pid $p was recycled (started $now, ours started $want) — NOT signalling it" >&2
        continue
      fi
      kill "$p" 2>/dev/null || true
    done <"$PIDFILE"
    while read -r p want; do
      [ -n "${p:-}" ] || continue
      for i in 1 2 3 4 5 6 7 8 9 10; do
        engine_proc_gone "$p" && break
        sleep 0.2
      done
      if ! engine_proc_gone "$p"; then
        now="$(engine_proc_starttime "$p")"
        # Same three answers: an unidentified pid never reaches SIGKILL.
        if [ -n "$want" ] && [ "$now" = "$want" ]; then
          kill -9 "$p" 2>/dev/null || true
          for i in 1 2 3 4 5; do engine_proc_gone "$p" && break; sleep 0.2; done
          # Say it. Deleting the data directory under a live engine is the state this
          # block exists to prevent, and a quiet failure to kill walks straight back in.
          engine_proc_gone "$p" || echo "cleanup: engine pid $p SURVIVED SIGKILL — $WORK is being removed under it" >&2
        fi
      fi
    done <"$PIDFILE"
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

# THE SELECTION IS READ BEFORE ANYTHING IS BUILT: a selection that names nothing runnable
# answers 2 at once instead of after ten minutes of builds. A SET but empty E2E_SPECS is such
# a selection, not a request for the default run: only an unset one means "every spec".
SELECTED=()
GATES=()
if [ -n "${E2E_SPECS+set}" ]; then
  sel="$(e2e_selection_parse "$ROOT/web" "$E2E_SPECS")" || exit $?
  while IFS= read -r s; do SELECTED+=("$s"); done <<<"$sel"
  REPORT_DIR="${E2E_REPORT_DIR:-$ROOT/web/playwright-report/e2e-json}"
  mkdir -p "$REPORT_DIR"
fi
export E2E_WORK_DIR="$WORK"

echo "==> Building web bundle + olivares binary"
if [ "${#SELECTED[@]}" -gt 0 ]; then
  # A qualification never runs on dependencies that failed to install.
  pnpm --dir "$ROOT/web" install --frozen-lockfile
else
  pnpm --dir "$ROOT/web" install --frozen-lockfile >/dev/null 2>&1 || true
fi
pnpm --dir "$ROOT/web" run build
build_olivares_bin "$BIN"

# boot_engine <port> <data-dir> — starts an engine, PROVES it is ours, prints its one-time
# setup token on stdout. Registers the PID for cleanup. Fails loudly: a boot we could not
# confirm is never reported as a boot that worked.
#
# The body moved to scripts/lib/engine-boot.sh so its FAILURE paths can be exercised without
# pnpm, a web bundle and a Go build — inline, the only way it ever ran was the slow way, and
# the ways it broke never ran at all. scripts/test-web-e2e-boot.sh sources the same file.
# Prove the squatter probe works in THIS bash before trusting a single negative from it.
# Built without /dev/tcp, the pre-flight would call every port free and wave through the
# exact leaked engine it exists to stop.
# `|| probe_rc=$?` and NOT a bare call: this script runs under `set -e` (line 28), so a bare
# `engine_probe_usable` that returns non-zero kills the shell BEFORE the case below and the two
# diagnostics never reach the operator. The refusal survived either way — errexit exits 1 — but
# the whole point of this block is to say WHICH of the two "could not look" cases happened, and
# a message that cannot be printed is not a message. Reproduced: `set -e; f(){ return 2; }; f;
# case "$?" ...` prints nothing at all.
probe_rc=0
engine_probe_usable || probe_rc=$?
case "$probe_rc" in
0) ;;
2)
  echo "ERROR: cannot build the control listener (no python3), so the port pre-flight is" >&2
  echo "       UNVERIFIED. That is 'could not look', and it is not a licence to boot anyway." >&2
  exit 1
  ;;
*)
  echo "ERROR: this bash cannot connect through /dev/tcp, so the port pre-flight cannot" >&2
  echo "       detect a squatter. Refusing rather than reporting every port free." >&2
  exit 1
  ;;
esac

boot_engine() {
  boot_engine_at "$BIN" "$1" "$2" "$PIDFILE"
}

# Derive the first-boot specs: the ones that fill the setup form's #token field.
FIRSTBOOT=()
while IFS= read -r spec; do
  FIRSTBOOT+=("$spec")
done < <(cd "$ROOT/web" && grep -lE "locator\('#token'\)" e2e/*.spec.ts 2>/dev/null | sort)

if [ "${#FIRSTBOOT[@]}" -eq 0 ]; then
  echo "ERROR: no first-boot spec found (none fills #token). The derivation above is" >&2
  echo "       stale, or e2e/ moved — refusing to run a battery whose shape I cannot see." >&2
  exit 1
fi

# ⛔ ESPECIFICACIONES QUE PERTENECEN A OTRO RUNNER. No se excluyen por gusto: se ejecutaban aqui y
# se saltaban enteras por falta de `DEMO_TENANT`, que este guion no siembra. Resultado medido el
# 2026-08-19: «7 passed, 124 skipped» con rc=0 — un verde sobre el 5 % de lo que el recuento
# sugiere, y el 95 % restante saltado por pertenecer a otro flujo, no por estar cubierto.
#
# El numero no era falso; era ILEGIBLE, que en un veredicto es igual de malo.
#
# Y la lista NO puede pudrirse en silencio: si una de estas deja de existir, este guion PARA. Una
# exclusion que nombra un fichero inexistente excluye nada y nadie se entera.
OTROS_RUNNERS=(
  "e2e/console-func-l4.spec.ts" # scripts/web-e2e-demo.sh — estate demo + lote funcional vivo
  "e2e/demo-graph.spec.ts"      # scripts/web-e2e-demo.sh — grafo y deriva sobre estate demo
  "e2e/docs-captures.spec.ts"   # scripts/docs-captures.sh — siembra el estate y captura
  "e2e/launch-states.spec.ts"   # scripts/launch-state-captures.sh — MUTA el estate, va al final
)
for _o in "${OTROS_RUNNERS[@]}"; do
  [ -f "$ROOT/web/$_o" ] || { echo "web-e2e: NO HE PODIDO MIRAR: la exclusion nombra $_o y no existe" >&2; exit 2; }
done

# Everything else shares one engine, as before.
SHARED=()
while IFS= read -r spec; do
  keep=1
  for fb in "${FIRSTBOOT[@]}"; do [ "$spec" = "$fb" ] && keep=0; done
  for ot in "${OTROS_RUNNERS[@]}"; do [ "$spec" = "$ot" ] && keep=0; done
  [ "$keep" -eq 1 ] && SHARED+=("$spec")
done < <(cd "$ROOT/web" && ls e2e/*.spec.ts | sort)
echo "==> ${#OTROS_RUNNERS[@]} spec(s) excluida(s) por tener runner propio: ${OTROS_RUNNERS[*]}"

if [ "${#SELECTED[@]}" -gt 0 ]; then
  keep_fb=()
  keep_sh=()
  for s in "${FIRSTBOOT[@]}"; do e2e_selected "$s" "${SELECTED[@]}" && keep_fb+=("$s"); done
  for s in "${SHARED[@]}"; do e2e_selected "$s" "${SELECTED[@]}" && keep_sh+=("$s"); done
  FIRSTBOOT=("${keep_fb[@]}")
  SHARED=("${keep_sh[@]}")
  if [ "$((${#FIRSTBOOT[@]} + ${#SHARED[@]}))" -ne "${#SELECTED[@]}" ]; then
    echo "web-e2e: COULD NOT LOOK: the selection (${SELECTED[*]}) names a spec another runner owns" >&2
    exit 2
  fi
  echo "==> selection: ${SELECTED[*]}"
fi

echo "==> ${#FIRSTBOOT[@]} first-boot spec(s), each on its own virgin engine; ${#SHARED[@]} shared"
cd "$ROOT/web"
rc=0
next_port="$PORT"

for spec in "${FIRSTBOOT[@]}"; do
  echo "==> [$spec] booting a virgin engine on 127.0.0.1:$next_port"
  token="$(boot_engine "$next_port" "$WORK/$(basename "$spec" .spec.ts)")"
  if [ "${#SELECTED[@]}" -gt 0 ]; then
    grc=0
    PLAYWRIGHT_BASE_URL="http://127.0.0.1:$next_port" PLAYWRIGHT_SETUP_TOKEN="$token" \
      e2e_gated_run "$REPORT_DIR/$(basename "$spec" .spec.ts).json" "$spec" \
      pnpm exec playwright test "$spec" --reporter=list,json || grc=$?
    GATES+=("$grc")
  else
    PLAYWRIGHT_BASE_URL="http://127.0.0.1:$next_port" PLAYWRIGHT_SETUP_TOKEN="$token" \
      pnpm exec playwright test "$spec" || rc=1
  fi
  next_port="$((next_port + 2))"
done

if [ "${#SHARED[@]}" -gt 0 ]; then
  echo "==> [shared] booting the engine for the remaining specs on 127.0.0.1:$next_port"
  token="$(boot_engine "$next_port" "$WORK/shared")"
  if [ "${#SELECTED[@]}" -gt 0 ]; then
    grc=0
    PLAYWRIGHT_BASE_URL="http://127.0.0.1:$next_port" PLAYWRIGHT_SETUP_TOKEN="$token" \
      e2e_gated_run "$REPORT_DIR/shared.json" shared \
      pnpm exec playwright test "${SHARED[@]}" "$@" --reporter=list,json || grc=$?
    GATES+=("$grc")
  else
    PLAYWRIGHT_BASE_URL="http://127.0.0.1:$next_port" PLAYWRIGHT_SETUP_TOKEN="$token" \
      pnpm exec playwright test "${SHARED[@]}" "$@" || rc=1
  fi
fi

if [ "${#SELECTED[@]}" -gt 0 ]; then
  rc="$(e2e_worst "$rc" "${GATES[@]}")"
fi
exit "$rc"
