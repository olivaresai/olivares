#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Scan the repository's two-space Taskfile grammar, only within tasks:.
# Print unexecutable task names (default) or all keys; exit 9 for an empty scan.
sin_ejecutable() {
	awk -v mode="${2:-broken}" '
		function cerrar() { if (t != "") { vistas++; if (mode == "keys" || !ok) print t; t = "" } }
		/^[^ #]/ { cerrar(); in_tasks = ($0 ~ /^["\047]?tasks["\047]?[ \t]*:/); next }
		in_tasks && /^  [^ #-]/ {
			cerrar(); ok = 0
			if (/^  [^ #-].*:[ \t]*(#.*)?$/) { t = $0; sub(/^  /, "", t); sub(/:[ \t]*(#.*)?$/, "", t) }
			next
		}
		in_tasks && /^[ \t]*(cmds|deps|cmd)[ \t]*:/ { ok = 1 }
		END { cerrar(); if (vistas == 0) exit 9 }
	' "$1"
}
