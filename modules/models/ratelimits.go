// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"context"
	"net/http"

	mp "github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/api"
)

// This file surfaces the read-only Anthropic Rate Limits inventory (ANT2-05) as a
// CONSULTABLE inventory (the connector already reads it, but module X
// did not expose it). It is strictly read-only: the inventory is what a gateway/proxy
// must keep IN SYNC, never a control the module mutates. With no Admin connector wired
// the route degrades to an empty inventory WITH a reason — honest, never a 500.

// RateLimitProvider is the read seam over the claude-api Admin connector's rate-limit
// inventory. nil (the default) means the connector is not wired; the real adapter
// lives in the composition root (it holds the Admin credential). The provider is
// read-only by contract.
type RateLimitProvider interface {
	// RateLimits returns the org and per-workspace override rate-limit inventory. An
	// error is a TRANSIENT fetch failure — the route degrades to empty-with-reason,
	// never a 500. No credential yields an empty slice, not an error.
	RateLimits(ctx context.Context) ([]mp.RateLimitRef, error)
}

// rateLimitValueDTO is one limiter inside a rate-limit group. org_limit is the
// workspace endpoint's org-level echo; omitted means not reported/not applicable, never
// a hard zero.
type rateLimitValueDTO struct {
	Type     string `json:"type"`
	Value    int64  `json:"value"`
	OrgLimit int64  `json:"org_limit,omitempty"`
}

// rateLimitDTO is one rate-limit inventory group. Minimal-data: provider vocabulary,
// model ids/aliases and numeric ceilings only — no secret, no key material. Workspace
// rows are OVERRIDES ONLY; absence means inherit the org value, not unlimited.
type rateLimitDTO struct {
	WorkspaceRef string              `json:"workspace_ref,omitempty"`
	GroupType    string              `json:"group_type"`
	Models       []string            `json:"models,omitempty"`
	Limits       []rateLimitValueDTO `json:"limits"`
}

// rateLimitsResponseDTO is the inventory response. available distinguishes a wired
// provider from the degraded default; caveat carries the documented coverage limit a
// mirroring gateway MUST honor.
type rateLimitsResponseDTO struct {
	Available  bool           `json:"available"`
	Reason     string         `json:"reason,omitempty"`
	RateLimits []rateLimitDTO `json:"rate_limits"`
	Caveat     string         `json:"caveat"`
}

// rateLimitsCaveat is the verbatim ANT2-05 coverage caveat (verified vs the Rate
// Limits API doc): Managed Agents are NOT covered, and a gateway/proxy must mirror.
const rateLimitsCaveat = "Managed Agents are NOT covered by the Anthropic Rate Limits API (ANT2-05); gateways and proxies must keep these limits in sync."

// handleRateLimits returns the read-only rate-limit inventory. It NEVER mutates and
// NEVER 500s: with no provider wired, or on a transient fetch error, it returns
// available=false with a reason and an empty inventory.
func (m *Module) handleRateLimits(w http.ResponseWriter, r *http.Request, _ api.ModuleContext) {
	out := rateLimitsResponseDTO{RateLimits: []rateLimitDTO{}, Caveat: rateLimitsCaveat}
	if m.rateLimits == nil {
		out.Reason = "the Claude Admin-API connector is not wired; the rate-limit inventory is unavailable (provision the read-only Admin credential to enable it)"
		writeJSON(w, http.StatusOK, out)
		return
	}
	refs, err := m.rateLimits.RateLimits(r.Context())
	if err != nil {
		// A transient fetch failure: degrade honestly, never a 500, and never leak the
		// endpoint/credential the error may embed (log it server-side instead).
		if m.log != nil {
			m.log.Warn("models: rate-limit inventory fetch failed; degrading to empty-with-reason", "err", err)
		}
		out.Reason = "the rate-limit inventory is temporarily unavailable"
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.Available = true
	for _, rl := range refs {
		limits := make([]rateLimitValueDTO, 0, len(rl.Limits))
		for _, lim := range rl.Limits {
			limits = append(limits, rateLimitValueDTO{
				Type:     lim.Type,
				Value:    lim.Value,
				OrgLimit: lim.OrgLimit,
			})
		}
		out.RateLimits = append(out.RateLimits, rateLimitDTO{
			WorkspaceRef: rl.WorkspaceRef,
			GroupType:    rl.GroupType,
			Models:       rl.Models,
			Limits:       limits,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
