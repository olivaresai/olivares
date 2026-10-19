// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package eventing

import (
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// errorOf returns the code and message of a module error envelope.
func errorOf(t *testing.T, r resp) (code, message string) {
	t.Helper()
	e, ok := r.body["error"].(map[string]any)
	if !ok {
		t.Fatalf("response has no error envelope: %d %s", r.code, r.raw)
	}
	code, _ = e["code"].(string)
	message, _ = e["message"].(string)
	return code, message
}

// wantBusinessRefusal fails unless r is the one Business answer: 501 business_required naming Business.
func wantBusinessRefusal(t *testing.T, what string, r resp) {
	t.Helper()
	if r.code != http.StatusNotImplemented {
		t.Fatalf("%s = %d %s; want 501", what, r.code, r.raw)
	}
	if code, message := errorOf(t, r); code != "business_required" || !strings.Contains(message, "Business") {
		t.Fatalf("%s = %q %q; want business_required naming Business", what, code, message)
	}
}

// Community wires no SIEM renderer (siemforward.NewRenderer is nil there), so a SIEM sink
// could only ever be parked. Authoring one answers the Business refusal instead of
// accepting a subscription that silently never delivers; the generic webhook stays open.
func TestCommunityRefusesToAuthorASIEMSink(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	rc := newReceiver(t)

	for _, kind := range []string{"splunk_hec", "sentinel_dcr", "datadog", "newrelic", "https"} {
		r := h.do("POST", "/v1/m/eventing/subscriptions", admin, map[string]any{
			"name": "siem-" + kind, "event_types": []string{"finding.reported"},
			"endpoint": rc.srv.URL, "role": "admin", "sink_kind": kind, "sink_cred": "tok",
		}, tenantHdr(tenant))
		wantBusinessRefusal(t, kind+" sink create", r)
	}
	// The edition answer comes before input validation, as it does on update: a vendor sink
	// with no credential is a Business matter first, not a 400 about a field Community cannot use.
	wantBusinessRefusal(t, "sink create without credential", h.do("POST", "/v1/m/eventing/subscriptions", admin, map[string]any{
		"name": "siem-nocred", "event_types": []string{"finding.reported"},
		"endpoint": rc.srv.URL, "role": "admin", "sink_kind": "splunk_hec",
	}, tenantHdr(tenant)))
	if list := h.do("GET", "/v1/m/eventing/subscriptions", admin, nil, tenantHdr(tenant)); strings.Contains(list.raw, "siem-") {
		t.Fatalf("a refused sink left a subscription behind: %s", list.raw)
	}

	// The generic webhook is a Community capability and is not touched.
	h.createSubscription(admin, tenant, map[string]any{
		"name": "chat", "event_types": []string{"finding.reported"}, "endpoint": rc.srv.URL, "role": "admin",
	})
}

// An update that carries a sink is refused in Community, whether it adds one, changes the kind
// or re-sends the stored one (which would rewrite its credential, options and endpoint). A sink
// stored before is data: an update that omits it disables or clears it, and DELETE removes it.
func TestCommunityRefusesAnyUpdateCarryingASink(t *testing.T) {
	h := newHarness(t, WithSinkRenderer(&fakeRenderer{}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	rc := newReceiver(t)
	stored, _ := h.createSubscription(admin, tenant, map[string]any{
		"name": "splunk", "event_types": []string{"finding.reported"}, "endpoint": rc.srv.URL,
		"role": "admin", "sink_kind": "splunk_hec", "sink_cred": "tok",
	})
	plain, _ := h.createSubscription(admin, tenant, map[string]any{
		"name": "plain", "event_types": []string{"finding.reported"}, "endpoint": rc.srv.URL, "role": "admin",
	})
	h.mod.renderer = nil // the same data under a Community build

	put := func(id string, body map[string]any) resp {
		body["name"], body["event_types"], body["endpoint"], body["role"] = "x", []string{"finding.reported"}, rc.srv.URL, "admin"
		return h.do("PUT", "/v1/m/eventing/subscriptions/"+id, admin, body, tenantHdr(tenant))
	}
	for _, tc := range []struct {
		name string
		id   string
		body map[string]any
	}{
		{"the stored sink re-sent", stored, map[string]any{"sink_kind": "splunk_hec", "sink_cred": "other", "enabled": false}},
		{"another kind on the stored sink", stored, map[string]any{"sink_kind": "datadog", "sink_cred": "tok"}},
		{"a sink on a plain subscription", plain, map[string]any{"sink_kind": "splunk_hec", "sink_cred": "tok"}},
		{"an invalid sink on a plain subscription", plain, map[string]any{"sink_kind": "bogus"}},
	} {
		wantBusinessRefusal(t, tc.name, put(tc.id, tc.body))
	}
	if got := h.sinkOf(tenant, model.ID(stored)).String(colSinkKind); got != "splunk_hec" {
		t.Fatalf("a refused update changed the stored sink kind to %q", got)
	}
	if row := h.sinkOf(tenant, model.ID(plain)); row != nil {
		t.Fatalf("a refused update left a sink behind on a plain subscription: %v", row)
	}

	// Disabling goes through, and an update without a sink drops the stored profile.
	r := put(stored, map[string]any{"enabled": false})
	if r.code != http.StatusOK || r.body["enabled"] != false || r.body["sink_kind"] != nil {
		t.Fatalf("disabling a stored sink without re-sending it = %d %s; want 200, disabled, no sink", r.code, r.raw)
	}
	if got := h.sinkOf(tenant, model.ID(stored)).String(colSinkKind); got != "" {
		t.Fatalf("the stored sink kind %q survived an update that omitted it", got)
	}
}
