#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md.
#
# Trigger-coverage witness for mainline-ci (entry name kept for compatibility).
#
# CIG2 superseded trigger/classifier list equality. This witness requires an
# inspectable main push without `paths` or `paths-ignore`, an inspectable
# classify job, and a secrets job independent of classify. A grep for
# `paths-ignore:` is not sufficient: quoted keys, flow mappings, `paths:`,
# anchors and other unsupported shapes must not return 0.
#
# 0 observed coverage · 1 proven filter or secrets/classify coupling ·
# 2 missing, unreadable, or unsupported. Fixture data overrides
# (OLIVARES_CI_FILE, OLIVARES_ROOT) are for direct tests only. The helper
# is always the file beside this script; it is not selected by environment.
set -u
SELF="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)" || {
	echo "check-classify-paths-parity: NO HE PODIDO MIRAR: cannot resolve scripts/." >&2
	exit 2
}
HELPER="$SELF/lib/ci-trigger-coverage.py"
RAIZ="${OLIVARES_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || echo "")}"
[ -n "$RAIZ" ] || { echo "check-classify-paths-parity: NO HE PODIDO MIRAR: no estoy en un repositorio." >&2; exit 2; }
F="${OLIVARES_CI_FILE:-$RAIZ/.github/workflows/mainline-ci.yml}"
[ -r "$F" ] || { echo "check-classify-paths-parity: NO HE PODIDO MIRAR: no leo $F." >&2; exit 2; }
[ -r "$HELPER" ] || { echo "check-classify-paths-parity: NO HE PODIDO MIRAR: no leo $HELPER." >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || {
	echo "check-classify-paths-parity: NO HE PODIDO MIRAR: sin python3." >&2
	exit 2
}
python3 "$HELPER" check "$F"
exit $?
