// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// fakeDeliveryGate is a programmable DeliveryGate for the kill-switch
// park tests.
type fakeDeliveryGate struct {
	pause DeliveryPause
	err   error
}

func (g fakeDeliveryGate) Check(context.Context, model.TenantID) (DeliveryPause, error) {
	return g.pause, g.err
}

// TestKillSwitchParksNonExemptAndDeliversExempt proves the park semantics
// of a paused tenant: an exempt (governance-channel) event type still delivers,
// while a non-exempt delivery is PARKED pre-attempt — status queued, outcome
// killswitch_parked, next_attempt_at pushed into the future, the retry ladder
// never consumed and nothing on the wire.
func TestKillSwitchParksNonExemptAndDeliversExempt(t *testing.T) {
	h := newHarness(t, WithDeliveryGate(fakeDeliveryGate{pause: DeliveryPause{
		Paused: true, Exempt: map[string]struct{}{"finding.reported": {}},
	}}))
	rcFind := newReceiver(t)
	rcEdge := newReceiver(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.createSubscription(editor, tenant, map[string]any{
		"name": "findings", "event_types": []string{"finding.reported"},
		"endpoint": rcFind.srv.URL,
	})
	edgeSub, _ := h.createSubscription(editor, tenant, map[string]any{
		"name": "edges", "event_types": []string{"edge.observed"}, "role": "editor",
		"endpoint": rcEdge.srv.URL,
	})

	h.publishFinding(tenant, "module:security", "guardrail", "governance traffic")
	h.publishEdge(tenant, "connector:pg")

	// The exempt governance channel still delivers under the stop.
	waitFor(t, "exempt finding delivery", func() bool {
		return len(h.deliveries(editor, tenant, "status=delivered")) == 1
	})
	if rcFind.count() != 1 {
		t.Errorf("exempt receiver saw %d deliveries, want 1", rcFind.count())
	}
	// The non-exempt delivery parks pre-attempt.
	waitFor(t, "edge delivery to park", func() bool {
		ds := h.deliveries(editor, tenant, "subscription="+edgeSub)
		return len(ds) == 1 && ds[0]["last_status"] == outcomeKillSwitched
	})
	d := h.deliveries(editor, tenant, "subscription="+edgeSub)[0]
	if d["status"] != statusQueued {
		t.Errorf("parked delivery status = %v, want queued (parked, never consumed)", d["status"])
	}
	if d["attempts"].(float64) != 0 {
		t.Errorf("parked delivery attempts = %v, want 0 (the retry ladder is never consumed)", d["attempts"])
	}
	next, err := model.ParseTimestamp(d["next_attempt_at"].(string))
	if err != nil || !next.Time().After(h.clk.Now().Time()) {
		t.Errorf("parked next_attempt_at = %v (parse err %v), want pushed into the future", d["next_attempt_at"], err)
	}
	if rcEdge.count() != 0 {
		t.Errorf("non-exempt receiver saw %d deliveries, want 0", rcEdge.count())
	}
	// An explicit pass keeps it parked (it is not due until the recheck window).
	h.dispatch(tenant)
	if rcEdge.count() != 0 {
		t.Errorf("non-exempt receiver saw %d deliveries after an explicit pass, want 0", rcEdge.count())
	}
}

// TestKillSwitchGateErrorParksEverything proves the module's own deny-closed
// belt: a gate ERROR inside the module parks ALL deliveries this pass — even a
// type the (unreadable) exemption set might have spared — and nothing reaches
// the wire.
func TestKillSwitchGateErrorParksEverything(t *testing.T) {
	h := newHarness(t, WithDeliveryGate(fakeDeliveryGate{err: errors.New("synthetic stop-state outage")}))
	rc := newReceiver(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.createSubscription(editor, tenant, map[string]any{
		"name": "findings", "event_types": []string{"finding.reported"},
		"endpoint": rc.srv.URL,
	})
	h.publishFinding(tenant, "module:security", "guardrail", "t")

	waitFor(t, "delivery to park deny-closed", func() bool {
		ds := h.deliveries(editor, tenant, "")
		return len(ds) == 1 && ds[0]["last_status"] == outcomeKillSwitched
	})
	d := h.deliveries(editor, tenant, "")[0]
	if d["status"] != statusQueued || d["attempts"].(float64) != 0 {
		t.Errorf("parked delivery = %v, want queued with 0 attempts", d)
	}
	if rc.count() != 0 {
		t.Errorf("receiver saw %d deliveries, want 0 (an unreadable stop state never means deliver)", rc.count())
	}
	// An explicit pass parks again, never delivers.
	h.dispatch(tenant)
	if rc.count() != 0 {
		t.Errorf("receiver saw %d deliveries after an explicit pass, want 0", rc.count())
	}
}
