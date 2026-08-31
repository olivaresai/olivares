// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"sort"
	"strings"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// This file is the MULTI-TAXONOMY substrate. Before it, a detector carried
// a single `owasp` slot that was multiplexed between the OWASP LLM Top 10 and the
// OWASP Agentic (ASI) Top 10 by value prefix, so a finding tagged `ASI02` was
// invisible to a query keyed by LLM ids and vice-versa. A finding is now described by
// THREE independent axes — OWASP LLM, OWASP Agentic (ASI), and MITRE ATLAS — each a
// SET, because one signal legitimately maps to several frameworks at once (an
// indirect prompt injection is OWASP LLM01:2025 AND OWASP-Agentic ASI01 AND MITRE
// ATLAS AML.T0051.001 simultaneously). A rule declares ids on a `shape` (its primary
// `owasp`/`atlas` plus an optional `also` set); scan() ROUTES each id onto the right
// axis by its canonical prefix, so existing rule literals never had to change. The
// id prefixes are unambiguous and stable: `LLM…` → LLM Top 10, `ASI…` → Agentic Top
// 10, `AML.…` → MITRE ATLAS.

// taxonomyAxes is a finding's three framework reference sets in one value, passed to
// emitFinding so the bus FindingReport (and thence the SIEM projection) carries every
// axis without widening the call signature each time a new axis appears.
type taxonomyAxes struct{ llm, asi, atlas []string }

// axesOf extracts a detection's taxonomy as a taxonomyAxes value.
func axesOf(d Detection) taxonomyAxes {
	return taxonomyAxes{llm: d.OWASPLLM, asi: d.OWASPASI, atlas: d.ATLAS}
}

// addTaxonomy routes one framework id onto the Detection's matching axis (de-duped,
// kept sorted). An unrecognized/empty id is dropped silently — the routing table is
// the single place that decides an id's axis, so a typo'd id never lands in the wrong
// set (it lands nowhere, an honest absence rather than a mis-tag).
func (d *Detection) addTaxonomy(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	switch {
	case strings.HasPrefix(id, "LLM"):
		d.OWASPLLM = insertSortedUnique(d.OWASPLLM, id)
	case strings.HasPrefix(id, "ASI"):
		d.OWASPASI = insertSortedUnique(d.OWASPASI, id)
	case strings.HasPrefix(id, "AML."):
		d.ATLAS = insertSortedUnique(d.ATLAS, id)
	default:
		// An id that matches no axis is NOT silently coerced — it is dropped. A rule
		// that needs a new axis must extend the routing table here, on purpose.
	}
}

// tagged returns d with each framework id routed onto its axis. It is the
// literal-construction counterpart of scan()'s shape routing, for the handful of
// detectors that build a Detection directly (a count threshold, a Luhn check, a model
// verdict) rather than from a shape catalog — so they tag the same multi-axis way.
func (d Detection) tagged(ids ...string) Detection {
	for _, id := range ids {
		d.addTaxonomy(id)
	}
	return d
}

// insertSortedUnique adds id to xs keeping it sorted and free of duplicates, so every
// axis is deterministic regardless of rule declaration order — the DetailHash, the
// persisted metadata and the SIEM projection are all byte-stable.
func insertSortedUnique(xs []string, id string) []string {
	i := sort.SearchStrings(xs, id)
	if i < len(xs) && xs[i] == id {
		return xs
	}
	xs = append(xs, "")
	copy(xs[i+1:], xs[i:])
	xs[i] = id
	return xs
}

// taxonomyMeta folds a detection's three framework axes into a finding's metadata
// map (keyed owasp_llm/owasp_asi/atlas), so a persisted finding is queryable
// by framework and an auditor sees every reference an alert carries. An empty axis is
// omitted (a finding with no framework reference carries no taxonomy key). The arrays
// are already sorted/de-duped by addTaxonomy; the keys mirror the SIEM Field keys.
func taxonomyMeta(d Detection, base map[string]any) map[string]any {
	if len(d.OWASPLLM) > 0 {
		base[sdkmodel.FieldOWASPLLM] = d.OWASPLLM
	}
	if len(d.OWASPASI) > 0 {
		base[sdkmodel.FieldOWASPASI] = d.OWASPASI
	}
	if len(d.ATLAS) > 0 {
		base[sdkmodel.FieldATLAS] = d.ATLAS
	}
	return base
}

// joinAxes renders one or more taxonomy axes as a single canonical, comma-joined,
// sorted id list. It backs the DetailHash fingerprint (so the hash of a single-id
// finding is byte-identical to the pre form) and the SIEM Field projection.
// Passing several axes (the OWASP LLM + ASI sets) folds them into one ordered list.
func joinAxes(axes ...[]string) string {
	var all []string
	for _, ax := range axes {
		all = append(all, ax...)
	}
	if len(all) == 0 {
		return ""
	}
	sort.Strings(all)
	// de-dup in place (sorted) so a value appearing on two passed axes is not repeated.
	// Compare against the last KEPT value (out), not all[i-1]: out aliases all, so a
	// prior append may have shifted all[i-1] — out[len-1] is always the real predecessor.
	out := all[:0]
	for _, v := range all {
		if len(out) == 0 || v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return strings.Join(out, ",")
}
