// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/webhook"
)

// fakeRenderer is a deterministic SinkRenderer for the engine tests: it records the
// event + profile it was asked to shape (so a test can assert the OPENED credential
// and the routing reached it) and returns a fixed shaped request POSTed to the
// subscription endpoint, so the harness receiver records it.
type fakeRenderer struct {
	gotEv      SinkEvent
	gotProfile SinkProfile
	calls      int
}

func (f *fakeRenderer) Render(ev SinkEvent, p SinkProfile) (SinkRequest, error) {
	f.gotEv, f.gotProfile = ev, p
	f.calls++
	return SinkRequest{
		URL:     p.Endpoint, // POST straight to the receiver
		Headers: map[string]string{"Content-Type": "application/json", "X-Sink-Auth": p.Cred},
		Body:    []byte(`{"shaped":true,"kind":"` + p.Kind + `"}`),
	}, nil
}

// A SIEM-sink subscription delivers the RENDERER's body (not the wireEvent) to the
// sink, with the sink auth header AND the HMAC over the rendered body (so a SIEM
// sink is signed too). The renderer receives the OPENED credential, never the sealed
// form.
func TestSinkDeliverySignedAndAuthed(t *testing.T) {
	fr := &fakeRenderer{}
	h := newHarness(t, WithSinkRenderer(fr))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	rc := newReceiver(t)

	_, secret := h.createSubscription(admin, tenant, map[string]any{
		"name": "splunk", "event_types": []string{"finding.reported"},
		"endpoint": rc.srv.URL, "role": "admin",
		"sink_kind": "splunk_hec", "sink_format": "ocsf", "sink_cred": "hec-token-xyz",
		"sink_opts": map[string]string{"index": "main", "sourcetype": "olivares"},
	})

	h.publishFinding(tenant, "module:security", "guardrail", "prompt injection")
	h.dispatch(tenant)
	waitFor(t, "sink delivery", func() bool { return rc.count() == 1 })

	if fr.calls != 1 {
		t.Fatalf("renderer calls = %d, want 1", fr.calls)
	}
	if fr.gotProfile.Kind != "splunk_hec" || fr.gotProfile.Format != "ocsf" {
		t.Fatalf("profile = %+v", fr.gotProfile)
	}
	if fr.gotProfile.Cred != "hec-token-xyz" {
		t.Fatalf("renderer must receive the OPENED credential, got %q", fr.gotProfile.Cred)
	}
	if fr.gotProfile.Opts["index"] != "main" {
		t.Fatalf("routing opts not delivered: %+v", fr.gotProfile.Opts)
	}
	if fr.gotEv.Type != "finding.reported" {
		t.Fatalf("event type = %q", fr.gotEv.Type)
	}

	req := rc.all()[0]
	if string(req.body) != `{"shaped":true,"kind":"splunk_hec"}` {
		t.Fatalf("sink got %q, want the rendered body", req.body)
	}
	if req.header.Get("X-Sink-Auth") != "hec-token-xyz" {
		t.Fatalf("sink auth header = %q", req.header.Get("X-Sink-Auth"))
	}
	// HMAC over the rendered body, verifiable with the subscription secret.
	ts := req.header.Get(headerTimestamp)
	sig := req.header.Get(headerSignature)
	if !webhook.Verify(secret, ts, sig, req.body) {
		t.Fatalf("sink delivery must be HMAC-signed over the rendered body")
	}
	if req.header.Get(headerEvent) == "" {
		t.Fatal("idempotency key header must ride on a sink delivery")
	}
}

// A generic-webhook subscription (no sink profile) is BYTE-IDENTICAL to before: the
// renderer is never consulted.
func TestWebhookPathUnchangedWithRendererWired(t *testing.T) {
	fr := &fakeRenderer{}
	h := newHarness(t, WithSinkRenderer(fr))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	rc := newReceiver(t)

	h.createSubscription(admin, tenant, map[string]any{
		"name": "plain", "event_types": []string{"finding.reported"},
		"endpoint": rc.srv.URL, "role": "admin",
	})
	h.publishFinding(tenant, "module:security", "guardrail", "x")
	h.dispatch(tenant)
	waitFor(t, "webhook delivery", func() bool { return rc.count() == 1 })

	if fr.calls != 0 {
		t.Fatalf("renderer must NOT be consulted for a generic webhook (calls=%d)", fr.calls)
	}
	if body := string(rc.all()[0].body); !strings.Contains(body, `"Type":"finding.reported"`) {
		t.Fatalf("generic webhook body must be the wireEvent JSON, got %q", body)
	}
}

// Deny-closed: a SIEM-sink subscription with NO renderer wired never sends — the
// delivery records the un-wired outcome and the sink receives nothing.
func TestSinkDenyClosedWithoutRenderer(t *testing.T) {
	h := newHarness(t) // no WithSinkRenderer
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	rc := newReceiver(t)

	h.createSubscription(admin, tenant, map[string]any{
		"name": "splunk", "event_types": []string{"finding.reported"},
		"endpoint": rc.srv.URL, "role": "admin",
		"sink_kind": "datadog", "sink_format": "ocsf", "sink_cred": "ddkey",
	})
	h.publishFinding(tenant, "module:security", "guardrail", "x")
	h.dispatch(tenant)

	waitFor(t, "un-wired outcome", func() bool {
		rows := h.deliveryRows(tenant)
		return len(rows) == 1 && rows[0].String(colDelLastStatus) == outcomeNoRenderer
	})
	if rc.count() != 0 {
		t.Fatalf("nothing must be sent without a renderer, sink saw %d", rc.count())
	}
}

// A cred-requiring sink without a credential is rejected at authoring time.
func TestSinkRequiresCredential(t *testing.T) {
	h := newHarness(t, WithSinkRenderer(&fakeRenderer{}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("POST", "/v1/m/eventing/subscriptions", admin, map[string]any{
		"name": "nocred", "event_types": []string{"finding.reported"},
		"endpoint": "https://splunk.example:8088", "role": "admin",
		"sink_kind": "splunk_hec", "sink_format": "ocsf", // no sink_cred
	}, tenantHdr(tenant))
	if r.code != 400 {
		t.Fatalf("missing sink_cred must be 400, got %d %s", r.code, r.raw)
	}
	if !strings.Contains(r.raw, "sink_cred is required") {
		t.Fatalf("unexpected error: %s", r.raw)
	}
}

// The sink profile round-trips through create/get/list — kind/format/opts/hint are
// shown, the credential NEVER is.
func TestSinkProfileDTORoundTrip(t *testing.T) {
	h := newHarness(t, WithSinkRenderer(&fakeRenderer{}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	id, _ := h.createSubscription(admin, tenant, map[string]any{
		"name": "dd", "event_types": []string{"finding.reported"},
		"endpoint": "https://http-intake.logs.datadoghq.com", "role": "admin",
		"sink_kind": "datadog", "sink_format": "cef", "sink_cred": "super-secret-key",
		"sink_opts": map[string]string{"service": "cp"},
	})

	r := h.do("GET", "/v1/m/eventing/subscriptions/"+id, admin, nil, tenantHdr(tenant))
	if r.code != 200 {
		t.Fatalf("get = %d %s", r.code, r.raw)
	}
	if r.body["sink_kind"] != "datadog" || r.body["sink_format"] != "cef" {
		t.Fatalf("sink fields not returned: %v", r.body)
	}
	if hint, _ := r.body["sink_cred_hint"].(string); hint == "" {
		t.Fatal("sink_cred_hint must be present")
	}
	if strings.Contains(r.raw, "super-secret-key") {
		t.Fatalf("the credential must NEVER be returned: %s", r.raw)
	}
	if _, leaked := r.body["sink_cred"]; leaked {
		t.Fatal("sink_cred must not be in the DTO")
	}
}
