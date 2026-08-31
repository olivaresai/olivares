#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-c04-04-fly.sh — C04-04. Retired Fly descriptors must not return.
# The engine start path names --dsn; ENGINE_DSN as an env var is not a DSN.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-04-fly: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-04-fly: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

# export-closure: hub-only cloud/control-plane/README.md — el modulo cloud/ no viaja al export
# export-closure: hub-only cloud/control-plane/docs/RUNBOOK.md — el modulo cloud/ no viaja al export
# export-closure: hub-only cloud/staging/docker-compose.yml — el modulo cloud/ no viaja al export
# Sin el modulo no hay sujeto que comprobar. La respuesta correcta es la TERCERA del
# canon —«no he podido mirar»—, no un verde y no el error crudo con el que muere hoy.
if [ ! -d cloud/control-plane ]; then
	printf '%s\n' "check-c04-04-fly: COULD NOT LOOK — cloud/control-plane is not in this tree" >&2
	exit 2
fi
if [ ! -d cloud/staging ]; then
	printf '%s\n' "check-c04-04-fly: COULD NOT LOOK — cloud/staging is not in this tree" >&2
	exit 2
fi

for f in cloud/engine/fly.toml cloud/control-plane/fly.toml; do
  if [ -e "$f" ]; then
    fail "retired Fly descriptor still present: $f"
  fi
done

COMPOSE="cloud/staging/docker-compose.yml"
[ -r "$COMPOSE" ] || cannot "$COMPOSE missing"
grep -q -- '--dsn' "$COMPOSE" || fail "staging compose does not pass --dsn (retired Fly start)"

# A live deploy command (not the word "retired"). Matches `flyctl deploy --…`
# at the start of a line or after a prompt.
if grep -nE '(^|[[:space:]])flyctl deploy( --|$)' \
     cloud/control-plane/README.md cloud/control-plane/docs/RUNBOOK.md >/dev/null 2>&1; then
  fail "control-plane docs still instruct flyctl deploy"
fi

say "check-c04-04-fly: CLEAN — Fly descriptors gone; compose still passes --dsn."
exit 0
