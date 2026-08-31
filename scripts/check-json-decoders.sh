#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-json-decoders.sh — every request-body JSON decoder must refuse a body that is not ONE
# JSON document.
#
# WHY (measured 2026-08-06, against a live engine). `json.Decoder.Decode` reads the FIRST value
# and stops. A helper that decodes once and returns success therefore accepts `{...}{...}`,
# silently discards everything after the first object, and performs a durable mutation. On the
# models routing-policy route that answered **201 Created**, and the row was read back with a
# separate GET to prove the effect was real, not just a hopeful status line. The same
# concatenation sent to a core route answered 400, which is what made it a drift rather than a
# property of encoding/json: core/api/render.go has called `dec.More()` since it was written,
# and **21 of the 22 copies of this helper had drifted from it**.
#
# The damage is not exotic. A concatenation bug in a caller becomes an apparently correct
# action; two layers can disagree about which of the two documents the request meant; and a
# proxy that reads the second value while the engine reads the first is a request-smuggling
# shape with a durable write on the end of it.
#
# WHY A GATE AND NOT ONE SHARED FUNCTION. Each module deliberately keeps its own small
# envelope helpers (its own errorBody wording, its own byte ceiling) and 21 packages already
# carry a near-identical copy. Collapsing them is a separate, larger change; what cannot wait
# is that the copies AGREE on the property that matters. This is the shared-shape method the
# repository already uses elsewhere: the copies stay, and one place asserts the invariant over
# all of them, so a twenty-third copy cannot arrive without it.
#
# THREE ANSWERS: clean / offenders named / CANNOT LOOK (no tree, no decoders found at all).
set -uo pipefail
export LC_ALL=C

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
SCAN="${OLIVARES_JSON_DECODER_SCAN:-$ROOT}"

if [ ! -d "$SCAN" ]; then
	echo "check-json-decoders: CANNOT LOOK — no tree at $SCAN." >&2
	echo "  Nothing was examined, so nothing is approved." >&2
	exit 2
fi

# THE ROSTER IS BEHAVIOURAL, NOT NOMINAL — and the first version of this gate got that
# wrong (corrected 2026-08-06, hours after it landed, by the adversarial contrast this
# session commissioned against its own work).
#
# It looked for declarations named exactly `func decodeJSON(` and then announced "23
# request-body decoders examined; every one refuses a body that is not a single JSON
# document". Measured: the tree holds THIRTY-ONE files that build a json.Decoder over a
# request body, and seven of them were invisible to that roster because they spell it
# differently or inline it — modules/sessions/runtime_dto.go declares `decodeJSONBody`,
# modules/sessions/templates.go decodes inline at three sites, core/api/handlers_scim.go is
# a one-line helper on the SCIM provisioning path. Every one of the seven had the defect.
#
# So the gate written to close "a green about a subject nobody examined" WAS one, in the same
# session, and its verdict line named a count that made the omission look like coverage. What
# a decoder is CALLED is a naming convention; what it DOES is the property. This keys on
# `json.NewDecoder` over `r.Body` (bare, wrapped in io.LimitReader, or wrapped in
# http.MaxBytesReader), which is what the defect actually needs to exist.
# TRACKED files only, via git: a filesystem walk of this repository crosses node_modules, the
# export scratch trees and every build cache, and measured here it takes minutes while a gate
# is running. `git ls-files` is both faster and more honest — an untracked decoder is not part
# of what we publish.
cd "$SCAN" || { echo "check-json-decoders: CANNOT LOOK — cannot enter $SCAN." >&2; exit 2; }
mapfile -t files < <(git ls-files -- '*.go' 2>/dev/null | grep -v '_test\.go$' |
	xargs -r grep -lE 'json\.NewDecoder\((io\.LimitReader\(|http\.MaxBytesReader\()?r\.Body' 2>/dev/null | sort)

if [ "${#files[@]}" -eq 0 ]; then
	echo "check-json-decoders: CANNOT LOOK — zero request-body json.Decoder sites found under $SCAN." >&2
	echo "  This repository has always had some (22 at the time of writing). Finding none means" >&2
	echo "  the scan stopped matching, not that the decoders stopped existing." >&2
	exit 2
fi

fail=0
for f in "${files[@]}"; do
	# FILE-SCOPED, deliberately. A decoder can be a named helper or three inline sites in one
	# handler file, so there is no single "body" to extract; asking whether the FILE that
	# builds a request-body decoder also contains the rejection is coarse but has no silent
	# direction — it can ask for a check that is already there, never approve a file with
	# none. `json.Unmarshal` is out of scope and that was measured, not assumed: on
	# `{"a":1}{"b":2}` it answers `invalid character '{' after top-level value`, while
	# NewDecoder().Decode returns nil with the first value and More() true.
	#
	# `dec.More()` is the rejection encoding/json gives us; io.EOF on a second Decode is the
	# other spelling. Either satisfies the property; neither being present does not.
	if ! grep -qE '\.More\(\)|io\.EOF' "$f"; then
		echo "$f: builds a request-body json.Decoder and never rejects a trailing value — it" >&2
		echo "    decodes the first and returns success, so {…}{…} mutates on the first and" >&2
		echo "    discards the rest. Reject it: if dec.More() { …400… }" >&2
		fail=1
	fi
done

if [ "$fail" -ne 0 ]; then
	echo "" >&2
	echo "check-json-decoders: FAIL — see the offenders above (${#files[@]} decoders examined)." >&2
	exit 1
fi

echo "check-json-decoders: OK — ${#files[@]} request-body decoders examined; every one refuses a body that is not a single JSON document"
