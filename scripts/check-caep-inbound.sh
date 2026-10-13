#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# The public CAEP contracts retain explicit Community/no-add-on refusals.
# Business Identity & Scale supplies the private receiver implementation.
# Three answers: 0 clean · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
cannot() { say "check-caep-inbound: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

command -v python3 >/dev/null || cannot "no python3"
python3 - <<'PY'
from pathlib import Path
import re
import sys


def fail(message: str) -> None:
    print(f"check-caep-inbound: FAIL — {message}", file=sys.stderr)
    raise SystemExit(1)


def read(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except OSError as exc:
        print(f"check-caep-inbound: COULD NOT LOOK — {path}: {exc}", file=sys.stderr)
        raise SystemExit(2)


def code(source: str) -> str:
    return re.sub(r"^\s*//[^\n]*|/\*.*?\*/", "", source, flags=re.M | re.S)


contracts = code(read(Path("core/auth/caep_events.go")))
stub = read(Path("core/auth/caep_noenterprise.go"))
handler = read(Path("core/api/handlers_identity_noenterprise.go"))
for symbol in ("ErrCAEPSetDisabled", "ErrCAEPRequiresBusiness"):
    if not re.search(r"\b" + symbol + r"\s*=\s*errors\.New\(", contracts):
        fail(f"shared receiver error missing: {symbol}")
if re.search(r"^func\s", contracts, re.M):
    fail("shared CAEP contract file contains executable receiver code")
for name in ("CAEPSetConfig", "CAEPEventEnvelope", "CAEPEventResult"):
    if not re.search(r"\btype\s+" + name + r"\s+struct\s*\{", contracts):
        fail(f"shared receiver contract missing: {name}")
for source in (stub, handler):
    if not source.startswith("//go:build !enterprise || !addon_ids\n"):
        fail("refusal must apply to Community and Business without addon_ids")
methods = {
    "ConfigureCAEPSet": r"error\s*\{\s*return ErrCAEPRequiresBusiness\s*\}",
    "CAEPReceiveEvent": r"\(CAEPEventResult, error\)\s*\{\s*return CAEPEventResult\{\}, ErrCAEPRequiresBusiness\s*\}",
}
for name, refusal in methods.items():
    if not re.search(r"func \(a \*Authenticator\) " + name + r"\([^{}]*\)\s*" + refusal, code(stub)):
        fail(f"{name} must return explicit Business refusal without side effects")
api = code(handler)
if not re.search(r"func \(s \*Server\) caepReceiveEvents\([^{}]*\)\s*\{\s*identityBusinessUnavailable\([^{}]*\)\s*\}", api):
    fail("HTTP CAEP receiver must use the Business refusal")
if "writeJSON(w, http.StatusNotImplemented, body)" not in api:
    fail("HTTP Business refusal must return 501")
# Assembly adds correctly edition-tagged private files at these same paths.
# This check examines the public seams; functional tests verify the paid leg.
for path in Path("core/auth").glob("*.go"):
    if path.name.endswith("_test.go") or path.name == "caep_noenterprise.go":
        continue
    source = read(path)
    constraint = source.splitlines()[0] if source else ""
    if re.fullmatch(r"//go:build enterprise && addon_ids(?: && [\w! ]+)*", constraint):
        continue
    if re.search(r"^func \([^\n]+\) (?:ConfigureCAEPSet|CAEPReceiveEvent|dispatchCAEPAction|resolveCAEPSubject|caepSetConfigFor)\(", code(source), re.M):
        fail(f"public executable CAEP receiver found in {path}")
print("check-caep-inbound: CLEAN — shared contracts and Community/no-add-on refusals; receiver implementation belongs to Business Identity & Scale.")
PY
