// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestReadRowBarrierRejectsAlteredBatchesAndRevokedReference(t *testing.T) {
	policy := &decisionHorizonPolicy{}
	var az *auth.Authorizer
	h := newHarnessOpts(t, func(o *api.Options) { policy.store = o.Store; az = auth.NewAuthorizer(policy); o.Authorizer = az })
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "read-receipts")
	token := witnessTenantPrincipal(t, h, admin, tenant)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := h.authr.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	port := api.NewReadRowAuthorizationPort(az, h.authr)
	ctx, p, err = port.RefreshReadPrincipal(ctx, p, tenant)
	if err != nil {
		t.Fatal(err)
	}
	attrs := []auth.ResourceAttrs{{Kind: "agent", ID: model.NewID().String()}}
	complete := port.(api.CompleteReadRowAuthorizationPort)
	set, err := complete.DecideReadRows(ctx, p, tenant, "agent:read", api.RouteMetadata{}, attrs)
	if err != nil {
		t.Fatal(err)
	}
	req := auth.Request{Principal: p, Tenant: tenant, Permission: "agent:read"}
	validate := func(batches []api.CompleteReadRowBatch) error {
		return h.st.View(ctx, tenant, func(sc store.Scope) error {
			now, e := sc.(store.TransactionClock).TransactionNow(ctx)
			if e != nil {
				return e
			}
			return api.ValidateCompleteReadRowBatches(ctx, sc, now.Time(), req, batches)
		})
	}
	baseline := []api.CompleteReadRowBatch{{Set: set, Resources: attrs}}
	if err := validate(baseline); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*api.CompleteReadRowBatch){
		"missing_receipt": func(b *api.CompleteReadRowBatch) { b.Set.Decisions = nil },
		"flipped_allow":   func(b *api.CompleteReadRowBatch) { b.Set.Allowed = []bool{!set.Allowed[0]} },
		"transplanted_resource": func(b *api.CompleteReadRowBatch) {
			b.Resources = []auth.ResourceAttrs{{Kind: "agent", ID: model.NewID().String()}}
		},
		"zero_decision": func(b *api.CompleteReadRowBatch) { b.Set.Decisions = []auth.RouteReadDecision{{}} },
	} {
		t.Run(name, func(t *testing.T) {
			b := baseline[0]
			change(&b)
			if e := validate([]api.CompleteReadRowBatch{b}); e == nil {
				t.Fatal("altered page accepted")
			}
		})
	}
	if err := validate(nil); err == nil {
		t.Fatal("empty page without collection receipt accepted")
	}
	// A changed directory fact invalidates the original receipt even while its
	// digest/window still verifies; refresh must keep the identical opaque ref.
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, e := sc.Agents().Create(ctx, model.Agent{Name: "causal-directory"})
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if err := validate(baseline); err == nil {
		t.Fatal("old principal fact accepted after directory write")
	}
	oldRef, _ := p.Ref()
	ctx, fresh, err := port.RefreshReadPrincipal(ctx, p, tenant)
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := fresh.Ref()
	if ref != oldRef {
		t.Fatal("refresh substituted credential")
	}
	if err := h.st.AuthMutate(ctx, func(sc store.AuthScope) error { return sc.Sessions().Delete(ctx, p.CredID) }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := port.RefreshReadPrincipal(ctx, p, tenant); err == nil {
		t.Fatal("revoked reference refreshed")
	}
}
