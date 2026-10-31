// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

type editionProbeGate struct{ called *bool }

func (g editionProbeGate) Authorize(context.Context, model.TenantID, sessions.LaunchIntent) (sessions.LaunchDecision, error) {
	*g.called = true
	return sessions.LaunchDecision{Allowed: false, Reason: "edition probe"}, nil
}

func TestEditionGateComposition(t *testing.T) {
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	h := newHarness(t)
	ctx := context.Background()
	tenant := model.TenantID(h.tenantA)
	sid, err := h.set.sessions.ResolveSession(ctx, tenant, sessions.SessionBinding{Provider: sessions.ProviderOperated, ExternalID: "run-edition", Origin: sessions.OriginOperated})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := h.set.sessions.Claim(ctx, tenant, sid, "user:holder", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	thisEdition.sessionLaunchGate = func(_ []api.Module, inner sessions.LaunchGate) sessions.LaunchGate {
		if inner == nil {
			t.Fatal("missing inner launch gate")
		}
		return editionProbeGate{&called}
	}
	gate := buildSessionLaunchGate(h.set, launchGateStub(func() {}))
	intent := sessions.LaunchIntent{RunRef: "run-edition", ClaimSID: sid, Holder: "user:holder", Fence: lease.Fence}
	decision, err := gate.Authorize(ctx, tenant, intent)
	if err != nil || decision.Allowed || decision.Reason != "edition probe" || !called {
		t.Fatalf("edition decoration lost: %+v %v", decision, err)
	}
	called = false
	intent.Holder = "user:intruder"
	decision, err = gate.Authorize(ctx, tenant, intent)
	if err != nil || decision.Allowed || called {
		t.Fatalf("edition decorator bypassed claim admission: %+v %v called=%v", decision, err, called)
	}
}
