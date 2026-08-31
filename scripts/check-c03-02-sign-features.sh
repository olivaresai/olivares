#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C03-02: license sign exposes --features and writes Claims.Features.
# Three answers: 0 / 1 / 2.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c03-02-sign-features: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c03-02-sign-features: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"
SRC=cmd/olivares/cmd_license.go
[ -r "$SRC" ] || cannot "missing $SRC"

grep -q 'featuresCSV' "$SRC" || fail "sign command lost the --features variable"
grep -q 'StringVar(&featuresCSV, "features"' "$SRC" || fail "sign command has no --features flag"
grep -q 'parseLicenseFeatures(featuresCSV)' "$SRC" || fail "sign does not parse --features"
grep -q 'Features: feats' "$SRC" || fail "sign does not assign Features on Claims — Sign would mint an empty list"
grep -q 'func parseLicenseFeatures' "$SRC" || fail "parseLicenseFeatures missing"

say "check-c03-02-sign-features: CLEAN — license sign --features reaches Claims.Features."
exit 0
