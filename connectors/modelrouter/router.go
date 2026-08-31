// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelrouter

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// Policy is how the router orders candidate models. The set is small and explicit;
// module X exposes it as the routing strategy an operator picks per workload.
type Policy string

const (
	// PolicyCost orders candidates cheapest-first by blended list price (a model
	// with unknown pricing sorts last). This is the FinOps default — route to the
	// cheapest model that meets the requirement (README.md, module XI).
	PolicyCost Policy = "cost"
	// PolicyLatency orders candidates lowest-observed-latency first (unknown last).
	PolicyLatency Policy = "latency"
	// PolicyCapability preserves catalog order among models that satisfy the
	// required capabilities — "first capable model wins", deterministic.
	PolicyCapability Policy = "capability"
	// PolicyPinned puts the requirement's PinnedModel first, then applies the
	// secondary policy (cost) to order the remaining candidates as fallbacks.
	PolicyPinned Policy = "pinned"
)

// Valid reports whether p is a known policy.
func (p Policy) Valid() bool {
	switch p {
	case PolicyCost, PolicyLatency, PolicyCapability, PolicyPinned:
		return true
	default:
		return false
	}
}

// Requirement is what a workload needs from a model. The router filters the
// catalog by these constraints, then orders the survivors by policy.
type Requirement struct {
	// RequiredCapabilities every candidate must declare (empty = no constraint).
	RequiredCapabilities []modelprovider.Capability
	// PreferredProviders, when non-empty, restricts candidates to these provider
	// refs (e.g. keep traffic on anthropic + local).
	PreferredProviders []string
	// MinContextWindow excludes models whose context window is known and smaller
	// (0 = no constraint; a model with unknown window is not excluded).
	MinContextWindow int64
	// PinnedModel is the model ref to prefer first under PolicyPinned (ignored by
	// other policies). A pinned model that fails the capability/provider filter is
	// not forced — correctness wins over the pin.
	PinnedModel string
	// AllowDeprecated keeps deprecated models as candidates (default false).
	AllowDeprecated bool
}

// Target is one routable destination: a provider+model, optionally via a gateway.
type Target struct {
	// ProviderRef and ModelRef identify the model.
	ProviderRef string
	// ModelRef is the model identifier.
	ModelRef string
	// ViaGateway is true when the call should be sent through an external gateway
	// rather than directly to the provider.
	ViaGateway bool
	// Endpoint is the gateway endpoint to route through when ViaGateway is true
	// (empty for a direct call).
	Endpoint string
}

// Decision is the routing result: the primary target plus an ordered fallback
// chain to try on failure, with the policy applied and a human-readable reason.
type Decision struct {
	// Primary is the first model to try.
	Primary Target
	// Fallbacks are the remaining candidates in try order (may be empty).
	Fallbacks []Target
	// Policy is the policy that produced this ordering.
	Policy Policy
	// Reason is a short human explanation (for the model-X UI / audit).
	Reason string
}

// Chain returns the primary followed by the fallbacks, in try order. It is the
// convenience a caller iterates to attempt models until one succeeds.
func (d Decision) Chain() []Target {
	return append([]Target{d.Primary}, d.Fallbacks...)
}

// ErrNoCandidate is returned when no model in the catalog satisfies a requirement.
var ErrNoCandidate = errors.New("modelrouter: no model satisfies the requirement")

// Router selects a model/provider and fallback chain for a workload. It is the
// interface module X consumes; the native and gateway implementations both satisfy
// it, so an operator switches strategy without the caller changing.
type Router interface {
	// Route returns the routing decision for req, or ErrNoCandidate if nothing in
	// the catalog qualifies. It reads only the catalog and performs no inference.
	Route(ctx context.Context, req Requirement) (Decision, error)
}

// nativeRouter is the default, dependency-free Router: it selects directly from a
// catalog snapshot in Go. Targets are direct (ViaGateway=false).
type nativeRouter struct {
	catalog modelprovider.Catalog
	policy  Policy
}

// NewNativeRouter builds the native router over a catalog snapshot and policy. An
// invalid policy falls back to PolicyCost so the router is always usable.
func NewNativeRouter(catalog modelprovider.Catalog, policy Policy) Router {
	if !policy.Valid() {
		policy = PolicyCost
	}
	return &nativeRouter{catalog: catalog, policy: policy}
}

// gatewayRouter wraps a native selection and rewrites every target to route
// through an external gateway endpoint, behind the same Router interface. The
// SELECTION is still native Go (no embedded gateway); only the destination of the
// eventual call changes. This is the "delegate to LiteLLM/OpenRouter" path.
type gatewayRouter struct {
	inner    Router
	endpoint string
}

// NewGatewayRouter builds a router that selects natively from the catalog but
// marks every target as routed through endpoint (an external LiteLLM/OpenRouter-
// style gateway). policy controls the native ordering.
func NewGatewayRouter(catalog modelprovider.Catalog, policy Policy, endpoint string) Router {
	return &gatewayRouter{inner: NewNativeRouter(catalog, policy), endpoint: endpoint}
}

// Route selects natively, then rewrites the targets to go via the gateway.
func (g *gatewayRouter) Route(ctx context.Context, req Requirement) (Decision, error) {
	d, err := g.inner.Route(ctx, req)
	if err != nil {
		return Decision{}, err
	}
	d.Primary = g.viaGateway(d.Primary)
	for i := range d.Fallbacks {
		d.Fallbacks[i] = g.viaGateway(d.Fallbacks[i])
	}
	d.Reason = "via gateway " + g.endpoint + ": " + d.Reason
	return d, nil
}

// viaGateway marks t as routed through the configured gateway endpoint.
func (g *gatewayRouter) viaGateway(t Target) Target {
	t.ViaGateway = true
	t.Endpoint = g.endpoint
	return t
}

// Route filters the catalog by the requirement, orders the survivors by policy,
// and returns the primary + fallback chain.
func (r *nativeRouter) Route(_ context.Context, req Requirement) (Decision, error) {
	candidates := r.filter(req)
	if len(candidates) == 0 {
		return Decision{}, ErrNoCandidate
	}
	ordered := r.order(candidates, req)

	targets := make([]Target, len(ordered))
	for i, m := range ordered {
		targets[i] = Target{ProviderRef: m.ProviderRef, ModelRef: m.Ref}
	}
	return Decision{
		Primary:   targets[0],
		Fallbacks: targets[1:],
		Policy:    r.policy,
		Reason:    fmt.Sprintf("%s: %d candidate(s), primary %s/%s", r.policy, len(targets), targets[0].ProviderRef, targets[0].ModelRef),
	}, nil
}

// filter returns the catalog models that satisfy req's hard constraints
// (capabilities, providers, context window, deprecation).
func (r *nativeRouter) filter(req Requirement) []modelprovider.Model {
	var out []modelprovider.Model
	for _, m := range r.catalog.Models {
		if m.Deprecated && !req.AllowDeprecated {
			continue
		}
		if !providerAllowed(m.ProviderRef, req.PreferredProviders) {
			continue
		}
		if req.MinContextWindow > 0 && m.ContextWindow > 0 && m.ContextWindow < req.MinContextWindow {
			continue
		}
		if !hasAll(m, req.RequiredCapabilities) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// order sorts candidates by the router's policy. The sort is stable so catalog
// order is the deterministic tie-breaker (and the whole ordering for
// PolicyCapability).
func (r *nativeRouter) order(candidates []modelprovider.Model, req Requirement) []modelprovider.Model {
	out := make([]modelprovider.Model, len(candidates))
	copy(out, candidates)

	switch r.policy {
	case PolicyCost:
		sort.SliceStable(out, func(i, j int) bool {
			return blendedPrice(out[i]) < blendedPrice(out[j])
		})
	case PolicyPinned:
		sort.SliceStable(out, func(i, j int) bool {
			pi, pj := out[i].Ref == req.PinnedModel, out[j].Ref == req.PinnedModel
			if pi != pj {
				return pi // the pinned model sorts first
			}
			return blendedPrice(out[i]) < blendedPrice(out[j])
		})
	case PolicyLatency:
		sort.SliceStable(out, func(i, j int) bool {
			return latencyRank(out[i]) < latencyRank(out[j])
		})
	case PolicyCapability:
		// Keep catalog order: first capable model wins, deterministically.
	}
	return out
}

// latencyRank is a comparable latency for ordering: lower is better, and an
// unknown latency (0) sorts last so measured models are preferred.
func latencyRank(m modelprovider.Model) int64 {
	if m.ObservedLatencyMillis <= 0 {
		return 1<<62 - 1
	}
	return m.ObservedLatencyMillis
}

// blendedPrice is a single comparable price for cost ordering: input + output per
// MTok. A model with no pricing sorts last (returns +Inf-like large value).
func blendedPrice(m modelprovider.Model) float64 {
	if m.Pricing == nil {
		return 1e18
	}
	return m.Pricing.InputPerMTokUSD + m.Pricing.OutputPerMTokUSD
}

// providerAllowed reports whether ref is permitted given an allow-list (empty
// allow-list permits everything).
func providerAllowed(ref string, allow []string) bool {
	if len(allow) == 0 {
		return true
	}
	for _, a := range allow {
		if a == ref {
			return true
		}
	}
	return false
}

// hasAll reports whether m declares every required capability.
func hasAll(m modelprovider.Model, required []modelprovider.Capability) bool {
	for _, c := range required {
		if !m.HasCapability(c) {
			return false
		}
	}
	return true
}
