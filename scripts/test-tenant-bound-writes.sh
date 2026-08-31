#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Bateria de check-tenant-bound-writes.sh. La bateria vive en el propio guion (--selftest); este
# fichero le da a la tarea de Taskfile el mismo nombre que a sus hermanas (test-<cosa>.sh).
set -uo pipefail
ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)" || { echo "test-tenant-bound-writes: 2 NO PUDE MIRAR — no resuelvo la raiz" >&2; exit 2; }
exec bash "$ROOT/scripts/check-tenant-bound-writes.sh" --selftest
