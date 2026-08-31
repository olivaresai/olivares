#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Bateria de check-list-truncation-witness.sh. El guion trae su propia bateria en --selftest;
# este fichero existe para que la tarea de Taskfile tenga el mismo nombre que sus hermanas
# (test-<cosa>.sh) y para que el fallo salga con el nombre del gate delante.
set -uo pipefail
ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)" || { echo "test-list-truncation-witness: 2 NO PUDE MIRAR — no resuelvo la raiz" >&2; exit 2; }
exec bash "$ROOT/scripts/check-list-truncation-witness.sh" --selftest
