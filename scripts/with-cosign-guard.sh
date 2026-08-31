#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# with-cosign-guard.sh — run a command with the cosign containment shim ACTIVE, and prove
# it was active before running anything.
#
# WHY A WRAPPER AND NOT A SETTING. An adversarial review of was right that a guard
# which is installed but not activated is not a control, and proposed provisioning the
# guard directory first in the Taskfile's declarative `env:`. That remedy does not work,
# and the measurement is worth keeping because it is the kind of thing a settings file
# silently lies about:
#
#     go-task 3.51.1, Taskfile with
#         env:
#           FOO:  'bar-{{.PATH}}'
#           PATH: '/tmp/guarddir:{{.PATH}}'
#     -> FOO=bar-/tmp/guarddir:/home/... (template resolved, entry applied)
#     -> PATH unchanged, /tmp/guarddir NOT prepended
#     The same at per-task scope. Only an in-command prefix takes effect.
#
# So activation is done here, explicitly, and then ASSERTED with `--status` — which reports
# on PATH resolution, not on an inherited variable. If the assertion fails the command does
# not run: a gate that proceeds after failing to arm its own containment is theatre.
#
# WHAT THIS DOES NOT DO. It cannot cover an interactive shell, an absolute path, or
# `command -p`. That residual is declared by `cosign-guard.sh --status` itself and is
# covered only by an egress rule, which this container cannot create (`unshare -rn` is
# refused here).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GUARD="$ROOT/scripts/cosign-guard.sh"
GUARD_DIR="${OLIVARES_GUARD_DIR:-/workspace/.tools/bin-guard}"

[ "$#" -gt 0 ] || {
	echo "usage: $0 <command> [args...]" >&2
	exit 2
}

# Install on demand, so a fresh clone or a rebuilt container is covered without anyone
# remembering a bootstrap step. Installation is idempotent and refuses to overwrite a
# `cosign` that is not one of our shims.
if [ ! -e "$GUARD_DIR/cosign" ]; then
	bash "$GUARD" --install "$GUARD_DIR" >&2
fi

export PATH="$GUARD_DIR:$PATH"

if ! bash "$GUARD" --status >/dev/null 2>&1; then
	echo "::error::with-cosign-guard: the containment shim is NOT active after prepending $GUARD_DIR." >&2
	bash "$GUARD" --status >&2 || true
	echo "Refusing to run '$1' unguarded: on 2026-07-25 an uncontained cosign probe created two" >&2
	echo "permanent public Rekor records that cannot be removed." >&2
	exit 1
fi

exec "$@"
