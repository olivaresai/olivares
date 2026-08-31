// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"context"

	"github.com/olivaresai/olivares/connectors/modelrouter"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"

	mp "github.com/olivaresai/olivares/connectors/modelprovider"
)

// policyKindRouting is the core Policy.Kind a routing policy is stored under. The
// module reuses the core Policy entity (ARCHITECTURE.md names "budget"/governance as
// Policy kinds) rather than registering a new table: routing/selection/fallback/
// version policy is exactly a governance policy.
const policyKindRouting = "routing"

// routingSpec is the typed view of a routing Policy's Spec. The module DEFINES
// and stores it; connectors/modelrouter EXECUTES the selection (the actual
// inference routing is the gateway's job, not the module's).
type routingSpec struct {
	Strategy             string   `json:"strategy"`
	RequiredCapabilities []string `json:"required_capabilities,omitempty"`
	PreferredProviders   []string `json:"preferred_providers,omitempty"`
	MinContextWindow     int64    `json:"min_context_window,omitempty"`
	PinnedModel          string   `json:"pinned_model,omitempty"`
	AllowDeprecated      bool     `json:"allow_deprecated,omitempty"`
	GatewayEndpoint      string   `json:"gateway_endpoint,omitempty"`

	// --- model-governance knobs (additive; absent = zero values = no
	// lifecycle/retention deny — opt-in like the budget gate). They act on the
	// declared reference data (reference.go) per lifecycle.go's governanceDeny.
	// DenyRetired denies routing to a model past its published retirement date
	// (requests to it FAIL at the provider). DenyDeprecated additionally denies
	// deprecated-not-yet-retired models (strictly stronger).
	DenyRetired    bool `json:"deny_retired,omitempty"`
	DenyDeprecated bool `json:"deny_deprecated,omitempty"`
	// RequireZDR restricts routing to verified ZDR-eligible models — Covered Models
	// (forced retention) and models with UNVERIFIED retention both deny
	// (deny-closed).
	RequireZDR bool `json:"require_zdr,omitempty"`
	// AccessTiers enrolls restricted access tiers (e.g. "glasswing") this policy
	// may route to. Unlike the knobs above this dimension is ALWAYS enforced: a
	// model with a non-empty AccessTier is denied unless its tier is listed here.
	AccessTiers []string `json:"access_tiers,omitempty"`
}

// normalize defaults an unset/invalid strategy to cost (the FinOps default) and
// returns the modelrouter.Policy it maps to.
func (s *routingSpec) normalize() modelrouter.Policy {
	p := modelrouter.Policy(s.Strategy)
	if !p.Valid() {
		p = modelrouter.PolicyCost
	}
	s.Strategy = string(p)
	return p
}

// toSpecMap renders the typed spec to the Policy.Spec map (the JSON column).
func (s routingSpec) toSpecMap() map[string]any {
	return map[string]any{
		"strategy":              s.Strategy,
		"required_capabilities": s.RequiredCapabilities,
		"preferred_providers":   s.PreferredProviders,
		"min_context_window":    s.MinContextWindow,
		"pinned_model":          s.PinnedModel,
		"allow_deprecated":      s.AllowDeprecated,
		"gateway_endpoint":      s.GatewayEndpoint,
		"deny_retired":          s.DenyRetired,
		"deny_deprecated":       s.DenyDeprecated,
		"require_zdr":           s.RequireZDR,
		"access_tiers":          s.AccessTiers,
	}
}

// parseRoutingSpec reads a routing spec from a Policy.Spec map (values arrive
// JSON-typed: numbers as float64, arrays as []any).
func parseRoutingSpec(spec map[string]any) routingSpec {
	return routingSpec{
		Strategy:             specString(spec, "strategy"),
		RequiredCapabilities: specStrings(spec, "required_capabilities"),
		PreferredProviders:   specStrings(spec, "preferred_providers"),
		MinContextWindow:     specInt64(spec, "min_context_window"),
		PinnedModel:          specString(spec, "pinned_model"),
		AllowDeprecated:      specBool(spec, "allow_deprecated"),
		GatewayEndpoint:      specString(spec, "gateway_endpoint"),
		DenyRetired:          specBool(spec, "deny_retired"),
		DenyDeprecated:       specBool(spec, "deny_deprecated"),
		RequireZDR:           specBool(spec, "require_zdr"),
		AccessTiers:          specStrings(spec, "access_tiers"),
	}
}

// requirement maps the spec to a modelrouter.Requirement.
func (s routingSpec) requirement() modelrouter.Requirement {
	caps := make([]mp.Capability, 0, len(s.RequiredCapabilities))
	for _, c := range s.RequiredCapabilities {
		caps = append(caps, mp.Capability(c))
	}
	return modelrouter.Requirement{
		RequiredCapabilities: caps,
		PreferredProviders:   s.PreferredProviders,
		MinContextWindow:     s.MinContextWindow,
		PinnedModel:          s.PinnedModel,
		AllowDeprecated:      s.AllowDeprecated,
	}
}

// resolve runs the policy against a catalog built from the governed estate and
// returns the routing decision (primary + fallback chain). It does not perform
// inference; it is a pure selection a caller (or the gateway) then executes.
func (s routingSpec) resolve(ctx context.Context, cat mp.Catalog) (modelrouter.Decision, error) {
	policy := s.normalize()
	var router modelrouter.Router
	if s.GatewayEndpoint != "" {
		router = modelrouter.NewGatewayRouter(cat, policy, s.GatewayEndpoint)
	} else {
		router = modelrouter.NewNativeRouter(cat, policy)
	}
	return router.Route(ctx, s.requirement())
}

// buildCatalog assembles a modelrouter catalog from the governed core Model/
// Provider entities, enriched from the reference table for capabilities and
// precise pricing. It is the live-estate catalog the router selects from: a model
// the estate actually has, priced and capability-tagged from the declared
// reference (or its operator-set core fields when no family matches).
func buildCatalog(ctx context.Context, sc store.Scope) (mp.Catalog, error) {
	provs, err := listAllPages(func(q model.Query) ([]model.Provider, model.Page, error) {
		return sc.Providers().List(ctx, q)
	})
	if err != nil {
		return mp.Catalog{}, err
	}
	pid2ref := make(map[string]string, len(provs))
	for _, p := range provs {
		pid2ref[p.ID.String()] = p.Name
	}
	mods, err := listAllPages(func(q model.Query) ([]model.Model, model.Page, error) {
		return sc.Models().List(ctx, q)
	})
	if err != nil {
		return mp.Catalog{}, err
	}
	cat := mp.Catalog{}
	for _, md := range mods {
		mm := mp.Model{
			ProviderRef:   pid2ref[md.ProviderID.String()],
			Ref:           md.Name,
			DisplayName:   md.Name,
			ContextWindow: md.ContextWindow,
			Deprecated:    md.Status == model.StatusInactive,
		}
		if ref, ok := lookupReference(md.Name); ok {
			mm.Capabilities = ref.Capabilities
			mm.Pricing = ref.Pricing
			if mm.ContextWindow == 0 {
				mm.ContextWindow = ref.ContextWindow
			}
			if mm.ProviderRef == "" {
				mm.ProviderRef = ref.ProviderRef
			}
		} else if md.InputCostMicroUSD > 0 || md.OutputCostMicroUSD > 0 {
			// No declared family: use the operator-set per-token cost as a coarse
			// blended price so the cost policy can still order it (Source=operator,
			// honest about its provenance).
			mm.Pricing = &mp.ModelPricing{
				InputPerMTokUSD:  float64(md.InputCostMicroUSD),
				OutputPerMTokUSD: float64(md.OutputCostMicroUSD),
				Currency:         "USD", Source: mp.PricingOperator,
			}
		}
		cat.Models = append(cat.Models, mm)
	}
	return cat, nil
}

// --- Policy.Spec scalar helpers (JSON-typed values) --------------------------

func specString(m map[string]any, k string) string {
	if s, ok := m[k].(string); ok {
		return s
	}
	return ""
}

func specBool(m map[string]any, k string) bool {
	if b, ok := m[k].(bool); ok {
		return b
	}
	return false
}

// specInt64 reads an integer that may be a float64 (JSON), an int or an int64.
func specInt64(m map[string]any, k string) int64 {
	switch v := m[k].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	}
	return 0
}

// specStrings reads a string slice that may be []any (JSON) or []string.
func specStrings(m map[string]any, k string) []string {
	switch v := m[k].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
