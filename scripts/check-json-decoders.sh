#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-json-decoders.sh — every request-body JSON decode goes through ONE shared helper,
# core/api.DecodeRequestBody, so a request body is exactly one JSON document everywhere.
#
# WHY ONE HELPER NOW (2026-10-01, supersedes the 2026-08-06 keep-the-copies decision).
# The fleet used to be ~32 per-package decodeJSON copies held in line by this gate's
# file-scoped check, and it drifted three ways the gate could not see:
#
#   1. THE SPELLING. The old roster matched `json.NewDecoder((io.LimitReader(|http.
#      MaxBytesReader()?)r.Body` literally, so it never saw http.MaxBytesReader(w, r.Body)
#      (the stdlib puts w first), the jsonDecoder(io.LimitReader(r.Body)) wrapper in
#      modules/sessions, or the bytes-from-Body sites in work_api.go and agenttoolsapi.
#   2. THE SCOPE. The check was per FILE — any `.More()` or `io.EOF` in the file approved
#      every decoder in it — so modules/orchestration's decodeOptionalJSON (schedules fire,
#      workflow run) and modules/governance's (NHI rotate/offboard/finalize) decoded the
#      first document and returned success with NO trailing-data rejection, in files the
#      gate reported clean. `{...}{...}` was applied as the first value on routes that
#      actuate production.
#   3. THE CHECK ITSELF. dec.More() peeks one byte and answers false for a tail opening
#      with ']' or '}', so the More()-based copies still accepted `{...}}`, `{...}]`,
#      `{...}\n]{}` and `{...}\n}null`. The only sound rejection is a second Decode that
#      must reach io.EOF — what modules/gitpublish landed first (a2b73194).
#
# One defect class, one home: core/api.DecodeRequestBody decodes strictly (exact size cap
# via http.MaxBytesReader, unknown fields rejected unless the route's contract says
# otherwise, second Decode must be io.EOF), and every handler calls it. The behavioral
# anchors live in core/api/decode_test.go (the four malformed tails and a second value,
# through the helper) and in the per-module route tests.
#
# WHAT THIS GATE ASSERTS, over TRACKED non-test .go files (git ls-files — a filesystem
# walk crosses node_modules and build caches, and an untracked decoder is not published):
#
#   A. No request-body json.NewDecoder outside the helper and the REVIEWED exemptions.
#      The roster keys on the construction the defect needs — `json.NewDecoder` over a
#      request .Body, any receiver spelling (r, req, request), bare or wrapped. An
#      exemption is a file carrying `check-json-decoders: exempt` with its reason, like
#      the signed JSONL memory-portability stream that is multi-document BY CONTRACT.
#   B. The helper itself is intact: DecodeRequestBody exists and still bounds the read
#      (http.MaxBytesReader), rejects unknown fields by default (DisallowUnknownFields)
#      and rejects a trailing value (io.EOF) — so the property cannot be edited away in
#      one place while this gate watches the copies.
#   C. The fleet uses it: api.DecodeRequestBody( appears at a floor of call sites, so a
#      mass reversion to hand-rolled decoders cannot pass as "no offenders found".
#
# THREE ANSWERS: clean / offenders named / CANNOT LOOK (no tree, no helper, roster empty
# in a repository that has always had request bodies).
set -uo pipefail
export LC_ALL=C

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
SCAN="${OLIVARES_JSON_DECODER_SCAN:-$ROOT}"
HELPER_REL="core/api/decode.go"
EXEMPT_MARK='check-json-decoders: exempt'
CALL_FLOOR=20

if [ ! -d "$SCAN" ]; then
	echo "check-json-decoders: CANNOT LOOK — no tree at $SCAN." >&2
	echo "  Nothing was examined, so nothing is approved." >&2
	exit 2
fi
cd "$SCAN" || { echo "check-json-decoders: CANNOT LOOK — cannot enter $SCAN." >&2; exit 2; }

# --- B. the helper is intact ---------------------------------------------------
if [ ! -f "$HELPER_REL" ] || ! git ls-files --error-unmatch "$HELPER_REL" >/dev/null 2>&1; then
	echo "check-json-decoders: CANNOT LOOK — $HELPER_REL is absent or untracked." >&2
	echo "  The single home of the property is missing; nothing else can be approved." >&2
	exit 2
fi
helper_fail=0
for token in 'func DecodeRequestBody(' 'http.MaxBytesReader' 'DisallowUnknownFields' 'io.EOF'; do
	if ! grep -qF "$token" "$HELPER_REL"; then
		echo "$HELPER_REL: lost '$token' — the strict single-document property lives in this" >&2
		echo "    one function; if it is edited away, every handler loses it at once." >&2
		helper_fail=1
	fi
done
if [ "$helper_fail" -ne 0 ]; then
	echo "" >&2
	echo "check-json-decoders: FAIL — the shared helper no longer decodes strictly." >&2
	exit 1
fi

# --- A. the roster -------------------------------------------------------------
mapfile -t files < <(git ls-files -- '*.go' 2>/dev/null | grep -v '_test\.go$' |
	xargs -r grep -lE 'json\.NewDecoder\(.*\b(r|req|request)\.Body' 2>/dev/null | sort)

if [ "${#files[@]}" -eq 0 ]; then
	echo "check-json-decoders: CANNOT LOOK — zero request-body json.NewDecoder sites found under $SCAN," >&2
	echo "  not even the helper. The scan stopped matching; the decoders did not stop existing." >&2
	exit 2
fi

fail=0
exempt=0
for f in "${files[@]}"; do
	if [ "$f" = "$HELPER_REL" ]; then
		continue
	fi
	if grep -qF "$EXEMPT_MARK" "$f"; then
		exempt=$((exempt + 1))
		continue
	fi
	echo "$f: builds a request-body json.Decoder outside core/api.DecodeRequestBody — the" >&2
	echo "    property drifts the day a second copy exists (it already did, three ways: see" >&2
	echo "    the header). Decode through the helper, or carry a reviewed '$EXEMPT_MARK'" >&2
	echo "    line saying why this body is not one JSON document by contract." >&2
	fail=1
done

# --- C. the fleet uses the helper ----------------------------------------------
callers=$(git ls-files -- '*.go' 2>/dev/null | grep -v '_test\.go$' |
	xargs -r grep -lE 'api\.DecodeRequestBody\(' 2>/dev/null | grep -v "^$HELPER_REL$" | wc -l)
if [ "$callers" -lt "$CALL_FLOOR" ]; then
	echo "check-json-decoders: FAIL — only $callers files call api.DecodeRequestBody (floor $CALL_FLOOR)." >&2
	echo "  The fleet reverted to decoders this roster cannot see, or the call was renamed." >&2
	exit 1
fi

if [ "$fail" -ne 0 ]; then
	echo "" >&2
	echo "check-json-decoders: FAIL — see the offenders above (${#files[@]} request-body decoder files examined, $exempt exempt)." >&2
	exit 1
fi

echo "check-json-decoders: OK — one shared strict helper ($HELPER_REL), $callers calling files, $exempt reviewed exemption(s); ${#files[@]} request-body decoder files examined"
