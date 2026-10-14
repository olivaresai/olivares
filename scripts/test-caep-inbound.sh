#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-caep-inbound.sh"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/caep-in.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

stage() {
  rm -rf "$TMP/tree"
  for path in core/auth/caep_events.go core/auth/caep_noenterprise.go core/api/handlers_identity_noenterprise.go cmd/olivares/caeptransmitgate.go; do
    mkdir -p "$TMP/tree/$(dirname "$path")"
    cp "$ROOT/$path" "$TMP/tree/$path"
  done
  mkdir -p "$TMP/tree/scripts"
  cp "$CHECK" "$TMP/tree/scripts/"
}
expect() {
  local name="$1" wanted="$2" rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-caep-inbound.sh" >"$TMP/out" 2>"$TMP/err" || rc=$?
  if [ "$rc" -eq "$wanted" ]; then ok "$name"; else bad "$name: exit $rc, want $wanted ($(cat "$TMP/err"))"; fi
}

stage
expect "public edition contracts and refusals are clean" 0
stage
sed -i '/ErrCAEPSetDisabled/d' "$TMP/tree/core/auth/caep_events.go"
expect "shared disabled receiver error remains required" 1
stage
sed -i 's/return ErrCAEPRequiresBusiness/return nil/' "$TMP/tree/core/auth/caep_noenterprise.go"
expect "configuration cannot silently succeed" 1
stage
sed -i 's/return CAEPEventResult{}, ErrCAEPRequiresBusiness/return CAEPEventResult{}, nil/' "$TMP/tree/core/auth/caep_noenterprise.go"
expect "received events cannot silently succeed" 1
stage
sed -i 's/!enterprise || !addon_ids/!enterprise/' "$TMP/tree/core/auth/caep_noenterprise.go"
expect "Business without identity add-on retains the refusal" 1
stage
sed -i 's/http.StatusNotImplemented/http.StatusOK/' "$TMP/tree/core/api/handlers_identity_noenterprise.go"
expect "HTTP refusal cannot become success" 1
stage
printf '\nfunc (a *Authenticator) dispatchCAEPAction() {}\n' >> "$TMP/tree/core/auth/caep_events.go"
expect "contract file cannot carry receiver dispatch" 1
stage
printf 'package auth\nfunc (a *Authenticator) CAEPReceiveEvent() {}\n' > "$TMP/tree/core/auth/leaked_receiver.go"
expect "another public file cannot carry receiver implementation" 1
stage
printf '//go:build enterprise && addon_ids\n\npackage auth\nfunc (a *Authenticator) CAEPReceiveEvent() {}\n' > "$TMP/tree/core/auth/caep_events_enterprise.go"
expect "assembled Business implementation stays outside the public guard" 0
stage
printf '//go:build enterprise || debug\n\npackage auth\nfunc (a *Authenticator) CAEPReceiveEvent() {}\n' > "$TMP/tree/core/auth/leaked_receiver.go"
expect "an unsafe enterprise alternative cannot hide a public receiver" 1
stage
rm "$TMP/tree/core/auth/caep_noenterprise.go"
expect "missing refusal source is unverified" 2

printf 'check-caep-inbound selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
