// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

func TestAgentEligibleForWorkRequiresLiveLifecycleAndSponsor(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.nhiTenant()
	ctx := context.Background()

	register := func(agentRef, sponsorRef string) {
		t.Helper()
		h.seedIdentity(tenant, sponsorRef, "user", "okta", "human", false)
		registerWorkAgent(t, h, tenant, tok, agentRef, sponsorRef)
	}

	register("agent:work:eligible", "human:work:eligible")
	if eligible, err := h.gov.AgentEligibleForWork(ctx, tenant, "agent:work:eligible"); err != nil || !eligible {
		t.Fatalf("eligible agent = %v, %v; want true, nil", eligible, err)
	}

	// NO-FIRE: an unrelated roster identity has no agent lifecycle authority.
	h.seedIdentity(tenant, "agent:work:roster-only", "service_account", "spiffe", "nhi", false)
	if eligible, err := h.gov.AgentEligibleForWork(ctx, tenant, "agent:work:roster-only"); err != nil || eligible {
		t.Fatalf("roster-only NHI = %v, %v; want false, nil", eligible, err)
	}

	register("agent:work:orphaned", "human:work:orphaned")
	h.setIdentityDisabled(tenant, "human:work:orphaned", true)
	// The hot path checks live sponsor state: it does not wait for the sweep to
	// materialize orphaned=true before refusing new work.
	if eligible, err := h.gov.AgentEligibleForWork(ctx, tenant, "agent:work:orphaned"); err != nil || eligible {
		t.Fatalf("disabled sponsor = %v, %v; want false, nil", eligible, err)
	}
	materializeWorkOrphan(t, h, tenant, tok, "agent:work:orphaned")
	if eligible, err := h.gov.AgentEligibleForWork(ctx, tenant, "agent:work:orphaned"); err != nil || eligible {
		t.Fatalf("materialized orphan = %v, %v; want false, nil", eligible, err)
	}

	register("agent:work:invalid-sponsor", "human:work:retyped")
	setWorkSponsorPrincipalType(t, h, tenant, "human:work:retyped", "nhi")
	if eligible, err := h.gov.AgentEligibleForWork(ctx, tenant, "agent:work:invalid-sponsor"); err != nil || eligible {
		t.Fatalf("non-human sponsor = %v, %v; want false, nil", eligible, err)
	}

	register("agent:work:blocked", "human:work:blocked")
	blockWorkAgent(t, h, tenant, tok, "agent:work:blocked")
	if eligible, err := h.gov.AgentEligibleForWork(ctx, tenant, "agent:work:blocked"); err != nil || eligible {
		t.Fatalf("blocked agent = %v, %v; want false, nil", eligible, err)
	}

	register("agent:work:offboarded", "human:work:offboarded")
	offboardWorkAgent(t, h, tenant, tok, "agent:work:offboarded")
	if eligible, err := h.gov.AgentEligibleForWork(ctx, tenant, "agent:work:offboarded"); err != nil || eligible {
		t.Fatalf("offboarded agent = %v, %v; want false, nil", eligible, err)
	}

	register("agent:work:ambiguous-sponsor", "human:work:ambiguous")
	// FIRE: Identity.external_id is intentionally not unique at the core schema.
	// Authorization must refuse two possible accountable humans instead of
	// inheriting roster upsert's safe-for-upsert "first match" tolerance.
	h.seedIdentity(tenant, "human:work:ambiguous", "user", "second-directory", "human", false)
	if eligible, err := h.gov.AgentEligibleForWork(
		ctx, tenant, "agent:work:ambiguous-sponsor",
	); err != nil || eligible {
		t.Fatalf("ambiguous sponsor = %v, %v; want false, nil", eligible, err)
	}

	// The composition observer returns only opaque fact ids/versions. The store
	// locks those on the later WorkItem transaction; governance vocabulary and
	// sponsor refs never cross into sessions.
	for _, tc := range []struct {
		ref  string
		want bool
	}{
		{ref: "agent:work:eligible", want: true},
		{ref: "agent:work:orphaned"},
		{ref: "agent:work:invalid-sponsor"},
		{ref: "agent:work:blocked"},
		{ref: "agent:work:offboarded"},
		{ref: "agent:work:ambiguous-sponsor"},
	} {
		var got bool
		var facts []store.AuthorizationFactRef
		err := h.st.View(ctx, tenant, func(sc store.Scope) error {
			var err error
			got, facts, err = h.gov.AgentWorkAuthorityFactsInScope(ctx, sc, tc.ref)
			return err
		})
		if err != nil || got != tc.want {
			t.Errorf("authority facts %s = %v, %v; want %v, nil",
				tc.ref, got, err, tc.want)
		}
		if got && (len(facts) != 2 || facts[0].Version < 1 || facts[1].Version < 1) {
			t.Errorf("authority facts %s = %#v, want lifecycle+sponsor versions", tc.ref, facts)
		}
	}

	// A workspace-confined scope must not unwrap itself to reach the tenant-wide
	// lifecycle table. The caller therefore gets missing evidence; sessions maps
	// this error to NO_HE_PODIDO_MIRAR rather than running unconfined.
	if err := h.st.View(ctx, tenant, func(sc store.Scope) error {
		workspace, err := sc.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		confined, err := store.ConfineWorkspace(ctx, sc, workspace.ID)
		if err != nil {
			return err
		}
		_, _, err = h.gov.AgentWorkAuthorityFactsInScope(
			ctx, confined, "agent:work:eligible",
		)
		if !errors.Is(err, store.ErrWorkspaceLineageRequired) {
			t.Fatalf("confined lifecycle check err = %v, want ErrWorkspaceLineageRequired", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect confined lifecycle behavior: %v", err)
	}
}

func TestAgentEligibleForWorkFailsClosedWhenLifecycleUnavailable(t *testing.T) {
	eligible, err := governance.New().AgentEligibleForWork(
		context.Background(), model.TenantID(model.NewID().String()), "agent:work:unavailable",
	)
	if err == nil || eligible {
		t.Fatalf("unwired lifecycle = %v, %v; want false plus error", eligible, err)
	}
}

func TestAgentWorkSponsorRequiresReportedEnabledState(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.nhiTenant()
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		metadata map[string]any
		want     bool
	}{
		{"absent", map[string]any{"principal_type": "human"}, false},
		{"disabled", map[string]any{"principal_type": "human", "disabled": true}, false},
		{"enabled", map[string]any{"principal_type": "human", "disabled": false}, true},
		{"null", map[string]any{"principal_type": "human", "disabled": nil}, false},
		{"string_false", map[string]any{"principal_type": "human", "disabled": "false"}, false},
		{"number_zero", map[string]any{"principal_type": "human", "disabled": 0}, false},
		{"nil_metadata", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agentRef, sponsorRef := "agent:state:"+tc.name, "human:state:"+tc.name
			h.seedIdentity(tenant, sponsorRef, "user", "test", "human", false)
			registerWorkAgent(t, h, tenant, tok, agentRef, sponsorRef)
			sponsorID := h.identityID(tenant, sponsorRef)
			if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
				identity, err := sc.Identities().Get(ctx, sponsorID)
				if err != nil {
					return err
				}
				identity.Metadata = tc.metadata
				_, err = sc.Identities().Update(ctx, identity)
				return err
			}); err != nil {
				t.Fatalf("set sponsor account state: %v", err)
			}
			eligible, err := h.gov.AgentEligibleForWork(ctx, tenant, agentRef)
			if err != nil || eligible != tc.want {
				t.Errorf("work eligibility = %v, %v; want %v, nil", eligible, err, tc.want)
			}
			if err := h.st.View(ctx, tenant, func(sc store.Scope) error {
				eligible, facts, err := h.gov.AgentWorkAuthorityFactsInScope(ctx, sc, agentRef)
				if err != nil || eligible != tc.want {
					t.Errorf("authority eligibility = %v, %v; want %v, nil", eligible, err, tc.want)
				}
				if tc.want {
					if len(facts) != 2 || facts[0].Version < 1 || facts[1].Version < 1 {
						t.Errorf("enabled sponsor facts = %#v; want sponsor+lifecycle versions", facts)
					}
				} else if len(facts) != 0 {
					t.Errorf("ineligible sponsor returned authority facts: %#v", facts)
				}
				return err
			}); err != nil {
				t.Fatalf("read work authority facts: %v", err)
			}
		})
	}
}

func setWorkSponsorPrincipalType(t *testing.T, h *harness, tenant model.TenantID, ref, principalType string) {
	t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		ids, _, err := sc.Identities().List(context.Background(), model.Query{Filters: []model.Filter{{
			Column: "external_id", Op: model.OpEq, Value: ref,
		}}, Limit: 1})
		if err != nil {
			return err
		}
		if len(ids) != 1 {
			return errors.New("work sponsor identity not found")
		}
		identity := ids[0]
		if identity.Metadata == nil {
			identity.Metadata = map[string]any{}
		}
		identity.Metadata["principal_type"] = principalType
		_, err = sc.Identities().Update(context.Background(), identity)
		return err
	}); err != nil {
		t.Fatalf("set work sponsor principal type: %v", err)
	}
}
