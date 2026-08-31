// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

// metric.sampled joins the public catalog. Connectors were already
// emitting it (claude, claude-api, claude-apps-gateway, vertex) but the type
// was uncataloged, so it was silently unsubscribable (deny-closed): the
// emission↔catalog gap the console audit surfaced. These tests pin the two
// halves of the closure: the privileged role ceiling and the actual capture.

import (
	"net/http"
	"strings"
	"testing"
)

// A viewer-role subscription may not author metric.sampled: the raw sample can
// name a developer, so it rides adoption:developer:read (editor+).
func TestS431_MetricSampledRoleCeiling(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	r := h.do("POST", "/v1/m/eventing/subscriptions", editor, map[string]any{
		"name": "metrics-viewer", "event_types": []string{"metric.sampled"},
		"role": "viewer", "endpoint": "https://consumer.example.com/hook",
	}, tenantHdr(tenant))
	if r.code != http.StatusBadRequest {
		t.Errorf("viewer-role metric.sampled subscription = %d, want 400 (%s)", r.code, r.raw)
	}

	r = h.do("POST", "/v1/m/eventing/subscriptions", editor, map[string]any{
		"name": "metrics-editor", "event_types": []string{"metric.sampled"},
		"role": "editor", "endpoint": "https://consumer.example.com/hook",
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Errorf("editor-role metric.sampled subscription = %d, want 201 (%s)", r.code, r.raw)
	}
}

// End-to-end: a published MetricSample is captured for a subscribed tenant and
// delivered to the consumer (the type is no longer dropped on the floor).
func TestS431_MetricSampledCaptured(t *testing.T) {
	h := newHarness(t)
	rc := newReceiver(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.createSubscription(editor, tenant, map[string]any{
		"name": "metrics", "event_types": []string{"metric.sampled"}, "role": "editor",
		"endpoint": rc.srv.URL,
	})
	h.publishMetric(tenant, "connector:claude")
	waitFor(t, "metric delivery", func() bool {
		rows := h.deliveryRows(tenant)
		return len(rows) == 1 && rows[0].String(colDelStatus) == statusDelivered
	})
	if rc.count() != 1 {
		t.Errorf("receiver saw %d deliveries, want 1", rc.count())
	}
}

// The revision ledger: every mutation snapshots the redacted DTO in-tx; restore
// re-applies an earlier delivery shape (never credentials) after re-validation;
// a foreign revision is a 404 (no cross-subscription existence leak); a deleted
// subscription keeps its history as evidence.
func TestS431_SubscriptionRevisions(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	id, secret := h.createSubscription(editor, tenant, map[string]any{
		"name": "orig", "event_types": []string{"finding.reported"}, "role": "viewer",
		"endpoint": "https://consumer.example.com/hook",
	})
	r := h.do("PUT", "/v1/m/eventing/subscriptions/"+id, editor, map[string]any{
		"name": "renamed", "event_types": []string{"finding.reported", "policy.changed"},
		"role": "viewer", "endpoint": "https://consumer.example.com/hook2",
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("update = %d %s", r.code, r.raw)
	}

	r = h.do("GET", "/v1/m/eventing/subscriptions/"+id+"/revisions", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("revisions = %d %s", r.code, r.raw)
	}
	items := r.body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("want 2 revisions (create,update), got %d (%s)", len(items), r.raw)
	}
	first := items[0].(map[string]any)
	second := items[1].(map[string]any)
	if first["op"] != "create" || second["op"] != "update" {
		t.Fatalf("ops = %v,%v want create,update", first["op"], second["op"])
	}
	if strings.Contains(r.raw, secret) {
		t.Fatal("a revision snapshot must NEVER contain the cleartext secret")
	}
	snap := first["snapshot"].(map[string]any)
	if snap["name"] != "orig" || snap["endpoint"] != "https://consumer.example.com/hook" {
		t.Fatalf("create snapshot mismatch: %v", snap)
	}

	// Restore the original shape.
	r = h.do("POST", "/v1/m/eventing/subscriptions/"+id+"/restore", editor, map[string]any{
		"revision_id": first["id"],
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("restore = %d %s", r.code, r.raw)
	}
	if r.body["name"] != "orig" || r.body["endpoint"] != "https://consumer.example.com/hook" {
		t.Fatalf("restore did not re-apply the shape: %s", r.raw)
	}
	r = h.do("GET", "/v1/m/eventing/subscriptions/"+id+"/revisions", editor, nil, tenantHdr(tenant))
	if items = r.body["items"].([]any); len(items) != 3 || items[2].(map[string]any)["op"] != "restore" {
		t.Fatalf("want 3rd revision op=restore, got %s", r.raw)
	}

	// A revision belonging to ANOTHER subscription: 404.
	otherID, _ := h.createSubscription(editor, tenant, map[string]any{
		"name": "other", "event_types": []string{"finding.reported"}, "role": "viewer",
		"endpoint": "https://consumer.example.com/other",
	})
	if r := h.do("POST", "/v1/m/eventing/subscriptions/"+otherID+"/restore", editor, map[string]any{
		"revision_id": first["id"],
	}, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("foreign revision restore = %d, want 404 (%s)", r.code, r.raw)
	}

	// Delete keeps the history as evidence, with a final delete snapshot.
	adminTok := h.roleToken(admin, tenant, "a@x.io", "admin")
	if r := h.do("DELETE", "/v1/m/eventing/subscriptions/"+otherID, adminTok, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/m/eventing/subscriptions/"+otherID+"/revisions", editor, nil, tenantHdr(tenant))
	items = r.body["items"].([]any)
	if len(items) != 2 || items[1].(map[string]any)["op"] != "delete" {
		t.Fatalf("deleted subscription must keep create+delete revisions, got %s", r.raw)
	}
}

// The deliveries list gains an ?origin filter: the console's replay flow
// links straight to the rows a replay produced.
func TestS431_DeliveriesOriginFilter(t *testing.T) {
	h := newHarness(t)
	rc := newReceiver(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.createSubscription(editor, tenant, map[string]any{
		"name": "metrics", "event_types": []string{"metric.sampled"}, "role": "editor",
		"endpoint": rc.srv.URL,
	})
	h.publishMetric(tenant, "connector:claude")
	waitFor(t, "live delivery", func() bool { return len(h.deliveryRows(tenant)) == 1 })

	if rows := h.deliveries(editor, tenant, "origin=live"); len(rows) != 1 {
		t.Fatalf("origin=live rows = %d, want 1", len(rows))
	}
	if rows := h.deliveries(editor, tenant, "origin=replay"); len(rows) != 0 {
		t.Fatalf("origin=replay rows = %d, want 0 before any replay", len(rows))
	}
}
