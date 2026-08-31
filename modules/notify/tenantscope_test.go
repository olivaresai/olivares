// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package notify

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// scopedDispatcher provisions destinations with a per-destination tenant
// restriction, mirroring the composition root's connectorDispatcher: a destination
// absent from scope is addressable by every tenant.
type scopedDispatcher struct {
	dests     []string
	scope     map[string]map[model.TenantID]struct{}
	delivered map[string]int
}

func newScopedDispatcher(dests ...string) *scopedDispatcher {
	return &scopedDispatcher{
		dests:     dests,
		scope:     map[string]map[model.TenantID]struct{}{},
		delivered: map[string]int{},
	}
}

func (d *scopedDispatcher) restrict(dest string, tenants ...model.TenantID) {
	set := map[model.TenantID]struct{}{}
	for _, t := range tenants {
		set[t] = struct{}{}
	}
	d.scope[dest] = set
}

func (d *scopedDispatcher) addressable(t model.TenantID, dest string) bool {
	s, scoped := d.scope[dest]
	if !scoped {
		return true
	}
	_, ok := s[t]
	return ok
}

func (d *scopedDispatcher) Destinations() []string { return append([]string(nil), d.dests...) }

func (d *scopedDispatcher) DestinationsFor(t model.TenantID) []string {
	var out []string
	for _, x := range d.dests {
		if d.addressable(t, x) {
			out = append(out, x)
		}
	}
	return out
}

func (d *scopedDispatcher) Deliver(_ context.Context, t model.TenantID, dest string, _ sdk.Notification) error {
	if !d.addressable(t, dest) {
		return ErrUnknownDestination
	}
	d.delivered[dest]++
	return nil
}

func (d *scopedDispatcher) ConnectorFingerprint(string) (string, bool) { return "", false }

// TestATenantCannotAddressAnotherTenantsDestination is the property the whole
// scoping exists for.
//
// Destinations were resolved from one flat map with no tenant in the lookup, while
// routes are tenant-scoped. So a tenant's EDITOR could name any destination the
// operator had provisioned for anyone — and the notification that went there
// carried the naming tenant's own identity, which makes it a disclosure rather than
// a misroute. The list endpoint returned the global set, which is the discovery step
// that made finding the names trivial.
func TestATenantCannotAddressAnotherTenantsDestination(t *testing.T) {
	disp := newScopedDispatcher("soc-a", "soc-b", "shared")
	h := newHarness(t, WithDispatcher(disp))
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "acme")
	tenantB := h.createOrg(admin, "beta")
	disp.restrict("soc-a", tenantA)
	disp.restrict("soc-b", tenantB)

	editorA := h.roleToken(admin, tenantA, "e@acme.test", "editor")

	// A's own destination, and the unscoped one, are both fine.
	for _, dest := range []string{"soc-a", "shared"} {
		r := h.do("POST", "/v1/m/notify/routes", editorA,
			map[string]any{"name": "r-" + dest, "destination": dest}, tenantHdr(tenantA))
		if r.code != http.StatusCreated {
			t.Fatalf("tenant A may not address %q: %d %s", dest, r.code, r.raw)
		}
	}

	// B's destination is refused, and the refusal must not confirm that the name
	// exists — a route author must not be able to enumerate another tenant's
	// destinations by watching which error comes back.
	r := h.do("POST", "/v1/m/notify/routes", editorA,
		map[string]any{"name": "r-cross", "destination": "soc-b"}, tenantHdr(tenantA))
	if r.code != http.StatusBadRequest {
		t.Fatalf("tenant A created a route to tenant B's destination: %d %s", r.code, r.raw)
	}

	// And the discovery step is closed too: the list is the caller's, not the estate's.
	r = h.do("GET", "/v1/m/notify/destinations", editorA, nil, tenantHdr(tenantA))
	if r.code != http.StatusOK {
		t.Fatalf("destinations = %d: %s", r.code, r.raw)
	}
	if got := r.raw; strings.Contains(got, "soc-b") {
		t.Errorf("the destinations endpoint disclosed another tenant's destination: %s", got)
	}
	if got := r.raw; !strings.Contains(got, "soc-a") || !strings.Contains(got, "shared") {
		t.Errorf("the destinations endpoint hid the caller's own destinations: %s", got)
	}
}

// TestDeliveryRefusesACrossTenantDestinationEvenWhenTheRouteExists. Authoring is not
// the authoritative seam: a route can predate the scoping, and an operator can
// narrow a destination after a route names it. The transport must refuse regardless
// of how the row got there.
func TestDeliveryRefusesACrossTenantDestinationEvenWhenTheRouteExists(t *testing.T) {
	disp := newScopedDispatcher("soc-b")
	h := newHarness(t, WithDispatcher(disp))
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "acme")
	tenantB := h.createOrg(admin, "beta")
	editorA := h.roleToken(admin, tenantA, "e@acme.test", "editor")

	// Authored while nothing constrains it — which is how every existing row got there.
	h.mustCreateRoute(editorA, tenantA, map[string]any{"name": "cross", "destination": "soc-b"})
	// The operator now scopes the destination to tenant B.
	disp.restrict("soc-b", tenantB)

	h.publishFinding(tenantA, securitySource,
		finding("security_guardrail", sdkmodel.SeverityHigh, "agent", "a1", "t"))

	if n := disp.delivered["soc-b"]; n != 0 {
		t.Fatalf("tenant A delivered %d notifications to tenant B's destination", n)
	}
	dels := h.terminalDeliveries(editorA, tenantA, "")
	if len(dels) != 1 || dels[0]["status"] != statusUnknownDest {
		t.Fatalf("want one terminal row status=unknown_destination, got %v", dels)
	}
}
