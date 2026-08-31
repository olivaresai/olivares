// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package residency

import (
	"fmt"
	"sort"
	"strings"
)

// Region is a data-residency region code (e.g. "eu", "us", "apac"). Per the
// Scope decision it is an operator-defined label validated against a
// configured registry, NOT a fixed enum baked into the binary: adding a region
// is configuration, not a code change and release.
type Region string

// maxRegionLen bounds a region code so a pin can never become a large blob.
const maxRegionLen = 32

func (r Region) String() string { return string(r) }

// Normalize lower-cases and trims a raw region string. Region codes are
// case-insensitive ("EU" and "eu" are the same region).
func Normalize(s string) Region {
	return Region(strings.ToLower(strings.TrimSpace(s)))
}

// valid reports whether a region code is well-formed: non-empty, within the
// length bound, and only [a-z0-9-]. Callers Normalize first.
func (r Region) valid() bool {
	if r == "" || len(r) > maxRegionLen {
		return false
	}
	for _, c := range r {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// Registry is the multi-region configuration of ONE control-plane instance: its
// HOME region (the single region whose tenants this instance serves — Model B)
// and the set of KNOWN region codes across the whole deployment, so a pin can be
// validated against the deployment's regions and an unknown region is rejected.
// A nil Registry, or one with no home, is single-region mode: residency is not
// enforced and everything is served (today's behavior, unchanged).
type Registry struct {
	home  Region
	known map[Region]struct{}
}

// NewRegistry builds the registry from the home region and the known-region set
// (home is added to known implicitly). Codes are normalized. An empty home means
// single-region mode and returns (nil, nil) — UNLESS known regions were given
// without a home, which is a misconfiguration (fail closed at boot). Every code
// must be well-formed or NewRegistry errors, so a typo fails the deployment at
// boot rather than silently at request time.
func NewRegistry(home string, known []string) (*Registry, error) {
	h := Normalize(home)
	if h == "" {
		if hasNonEmpty(known) {
			return nil, fmt.Errorf("residency: --known-regions set without a --region home (single-region mode takes neither)")
		}
		return nil, nil
	}
	if !h.valid() {
		return nil, fmt.Errorf("residency: invalid home region %q (use lowercase [a-z0-9-], <=%d chars)", home, maxRegionLen)
	}
	set := map[Region]struct{}{h: {}}
	for _, k := range known {
		r := Normalize(k)
		if r == "" {
			continue
		}
		if !r.valid() {
			return nil, fmt.Errorf("residency: invalid known region %q (use lowercase [a-z0-9-], <=%d chars)", k, maxRegionLen)
		}
		set[r] = struct{}{}
	}
	return &Registry{home: h, known: set}, nil
}

func hasNonEmpty(ss []string) bool {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return true
		}
	}
	return false
}

// Home is this instance's home region, or "" in single-region mode.
func (r *Registry) Home() Region {
	if r == nil {
		return ""
	}
	return r.home
}

// Enforces reports whether residency enforcement is active (a home region is
// configured). When false, the instance is single-region and serves all tenants.
func (r *Registry) Enforces() bool { return r != nil && r.home != "" }

// IsKnown reports whether region is in the configured registry.
func (r *Registry) IsKnown(region Region) bool {
	if r == nil {
		return false
	}
	_, ok := r.known[region]
	return ok
}

// Known returns the known region codes, sorted for stable display.
func (r *Registry) Known() []Region {
	if r == nil {
		return nil
	}
	out := make([]Region, 0, len(r.known))
	for k := range r.known {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Serves reports whether THIS instance may serve a tenant whose residency pin is
// `pin`. In single-region mode everything is served. Otherwise an empty pin (an
// unpinned tenant, no residency requirement) is served anywhere, and a non-empty
// pin must equal the home region. Deny-closed: anything else is not served.
func (r *Registry) Serves(pin string) bool {
	if !r.Enforces() {
		return true
	}
	p := Normalize(pin)
	return p == "" || p == r.home
}

// ValidatePin validates a requested residency pin for a tenant being provisioned
// or re-pinned on THIS instance. An empty pin is always allowed (unpinned). A
// non-empty pin requires a region-scoped instance, must be a KNOWN region, and —
// because each instance is region-scoped (Model B) — must equal the home region:
// an instance must never hold data it does not serve. Returns a descriptive
// error (mapped to 400 by the API) otherwise.
func (r *Registry) ValidatePin(pin string) error {
	p := Normalize(pin)
	if p == "" {
		return nil
	}
	if !r.Enforces() {
		return fmt.Errorf("residency: cannot pin a tenant to region %q on a non-region-scoped instance (start it with --region to enforce residency)", pin)
	}
	if !r.IsKnown(p) {
		return fmt.Errorf("residency: unknown region %q (known regions: %s)", pin, joinRegions(r.Known()))
	}
	if p != r.home {
		return fmt.Errorf("residency: cannot provision a tenant pinned to region %q on the %q instance — provision it on its own region's instance", p, r.home)
	}
	return nil
}

func joinRegions(rs []Region) string {
	ss := make([]string, len(rs))
	for i, r := range rs {
		ss[i] = string(r)
	}
	return strings.Join(ss, ", ")
}
