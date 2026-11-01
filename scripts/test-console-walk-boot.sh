#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/run"
awk '
  /^      - name: console walk against a live engine / { step = 1; next }
  step && /^      - / { exit }
  step && /^        run: \|/ { body = 1; next }
  step && body { sub(/^          /, ""); print }
' "$ROOT/.github/workflows/drills-nightly.yml" > "$TMP/workflow.sh"
cat > "$TMP/bin/go" <<'GO'
#!/usr/bin/env bash
set -eu
cat > "$RUNNER_TEMP/olivares" <<'ENGINE'
#!/usr/bin/env bash
# Synchronize virtual time with the workflow's polling sleep. No wall-clock race.
echo $$ > "$RUNNER_TEMP/engine.pid"
while [ ! -f "$RUNNER_TEMP/ready" ]; do /bin/sleep 0.01; done
if [ "$BOOT_CASE" = slow ]; then
  echo olst_TESTONLY
  touch "$RUNNER_TEMP/announced"
fi
exec /bin/sleep 30
ENGINE
chmod +x "$RUNNER_TEMP/olivares"
GO
cat > "$TMP/bin/sleep" <<'SLEEP'
#!/usr/bin/env bash
set -eu
[ "$1" = 2 ] || exit 72
n=0
[ ! -f "$RUNNER_TEMP/polls" ] || read -r n < "$RUNNER_TEMP/polls"
n=$((n + 1))
echo "$n" > "$RUNNER_TEMP/polls"
if [ "$n" = 65 ] && [ "$BOOT_CASE" = slow ]; then
  touch "$RUNNER_TEMP/ready"
  for _ in $(/usr/bin/seq 1 1000); do
    [ ! -f "$RUNNER_TEMP/announced" ] || exit 0
    /bin/sleep 0.01
  done
  exit 73
fi
SLEEP
cat > "$TMP/bin/task" <<'TASK'
#!/usr/bin/env bash
set -eu
[ "$*" != '-x console:walk' ] || touch "$RUNNER_TEMP/walk-ran"
TASK
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/pnpm"
chmod +x "$TMP/bin/"*
for BOOT_CASE in slow stuck; do
  export BOOT_CASE RUNNER_TEMP="$TMP/run/$BOOT_CASE"
  mkdir -p "$RUNNER_TEMP"
  export GITHUB_OUTPUT="$RUNNER_TEMP/output"
  : > "$GITHUB_OUTPUT"
  PATH="$TMP/bin:$PATH" RUNNER_ENVIRONMENT=self-hosted bash -e "$TMP/workflow.sh" > "$RUNNER_TEMP/log" 2>&1
  if [ -f "$RUNNER_TEMP/engine.pid" ] && kill -0 "$(cat "$RUNNER_TEMP/engine.pid")" 2>/dev/null; then
    kill "$(cat "$RUNNER_TEMP/engine.pid")"
    echo 'FAIL: workflow leaked its engine' >&2; exit 1
  fi
  if [ "$BOOT_CASE" = slow ]; then
    grep -qx failed=false "$GITHUB_OUTPUT" && [ -f "$RUNNER_TEMP/walk-ran" ] \
      || { echo 'FAIL: startup after 130 virtual seconds never reached the walk' >&2; exit 1; }
  else
    grep -qx failed=true "$GITHUB_OUTPUT" && [ ! -f "$RUNNER_TEMP/walk-ran" ] \
      || { echo 'FAIL: stuck startup was not refused' >&2; exit 1; }
    [ "$(cat "$RUNNER_TEMP/polls")" -le 150 ] \
      || { echo 'FAIL: startup wait exceeded five minutes' >&2; exit 1; }
  fi
done
echo 'PASS: slow startup reaches the walk; stuck startup is bounded; both engines are reaped.'
