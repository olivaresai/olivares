// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/license"
)

// A Business build links the renderer and asks its sink gate on every create or update that
// carries a SIEM sink: without a licence that covers SIEM push the answer is 403
// addon_requires_license and nothing is stored; with one the sink is authored. The generic
// webhook, and an update that drops a stored sink, never ask the gate.
func TestASIEMSinkIsAuthoredOnlyUnderALiveLicence(t *testing.T) {
	var refusal error
	h := newHarness(t, WithSinkRenderer(&fakeRenderer{}), WithSinkGate(func() error { return refusal }))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	rc := newReceiver(t)
	sink := func(name string) map[string]any {
		return map[string]any{"name": name, "event_types": []string{"finding.reported"}, "endpoint": rc.srv.URL,
			"role": "admin", "sink_kind": "splunk_hec", "sink_cred": "tok"}
	}
	stored, _ := h.createSubscription(admin, tenant, sink("licensed"))

	refusal = license.AddonRequired("siem-push", "author-sink")
	wantLicenceRefusal := func(what string, r resp) {
		t.Helper()
		e, _ := r.body["error"].(map[string]any)
		if r.code != http.StatusForbidden || e["code"] != "addon_requires_license" {
			t.Fatalf("%s = %d %s; want 403 addon_requires_license", what, r.code, r.raw)
		}
	}
	wantLicenceRefusal("unlicensed sink create", h.do("POST", "/v1/m/eventing/subscriptions", admin, sink("unlicensed"), tenantHdr(tenant)))
	update := sink("licensed")
	update["sink_cred"] = "other"
	wantLicenceRefusal("unlicensed sink update", h.do("PUT", "/v1/m/eventing/subscriptions/"+stored, admin, update, tenantHdr(tenant)))

	h.createSubscription(admin, tenant, map[string]any{
		"name": "chat", "event_types": []string{"finding.reported"}, "endpoint": rc.srv.URL, "role": "admin",
	})
	drop := map[string]any{"name": "licensed", "event_types": []string{"finding.reported"}, "endpoint": rc.srv.URL, "role": "admin", "enabled": false}
	if r := h.do("PUT", "/v1/m/eventing/subscriptions/"+stored, admin, drop, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("dropping a stored sink without a licence = %d %s; want 200", r.code, r.raw)
	}
}
