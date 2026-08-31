// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestContextPolicyApply(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, h *harness, tenant model.TenantID, admin string) (ContextPolicyQuery, EffectivePolicy)
	}{
		{
			// MaxContextTokens is a ceiling: the tenant's 8000 binds even though the
			// more-specific agent policy asks for 32000 (a techo is not loosenable).
			name: "ceiling-min-binds-agent-cannot-loosen",
			setup: func(t *testing.T, h *harness, tenant model.TenantID, _ string) (ContextPolicyQuery, EffectivePolicy) {
				seedContextPolicies(t, h, tenant,
					contextPolicySeed{kind: scopeTenant, ref: tenant.String(), maxTokens: 8000},
					contextPolicySeed{kind: scopeAgent, ref: "a1", maxTokens: 32000},
				)
				return ContextPolicyQuery{Principal: h.scopedPrincipal(tenant), AgentRef: "a1"}, EffectivePolicy{
					MaxContextTokens: 8000,
					Strategy:         strategyTruncate,
					WinningScope:     "tenant:" + tenant.String(),
				}
			},
		},
		{
			// The enforced group ceiling: a user_group cap binds over a more-specific
			// agent policy that asks for more (the brief's "techo por grupo enforced").
			name: "group-ceiling-binds-over-agent",
			setup: func(t *testing.T, h *harness, tenant model.TenantID, admin string) (ContextPolicyQuery, EffectivePolicy) {
				principal, groups := contextPrincipalWithGroups(t, h, admin, tenant, "cap@acme.io", auth.RoleViewer, "Capped")
				seedContextPolicies(t, h, tenant,
					contextPolicySeed{kind: scopeUserGroup, ref: groups[0], maxTokens: 16000},
					contextPolicySeed{kind: scopeAgent, ref: "a1", maxTokens: 64000},
				)
				return ContextPolicyQuery{Principal: principal, AgentRef: "a1"}, EffectivePolicy{
					MaxContextTokens: 16000,
					Strategy:         strategyTruncate,
					WinningScope:     "user_group:" + groups[0],
				}
			},
		},
		{
			name: "forbid-absolute",
			setup: func(t *testing.T, h *harness, tenant model.TenantID, admin string) (ContextPolicyQuery, EffectivePolicy) {
				principal, groups := contextPrincipalWithGroups(t, h, admin, tenant, "deny@acme.io", auth.RoleViewer, "Engineering")
				seedContextPolicies(t, h, tenant,
					contextPolicySeed{kind: scopeAgent, ref: "a1", maxTokens: 32000},
					contextPolicySeed{kind: scopeUserGroup, ref: groups[0], effect: effectForbid},
				)
				return ContextPolicyQuery{Principal: principal, AgentRef: "a1"}, EffectivePolicy{
					Deny:       true,
					DenyReason: "context denied by a forbid context-policy (deny-closed)",
				}
			},
		},
		{
			name: "user-in-two-groups-forbid-in-one",
			setup: func(t *testing.T, h *harness, tenant model.TenantID, admin string) (ContextPolicyQuery, EffectivePolicy) {
				principal, groups := contextPrincipalWithGroups(t, h, admin, tenant, "twogroups@acme.io", auth.RoleViewer, "G1", "G2")
				seedContextPolicies(t, h, tenant,
					contextPolicySeed{kind: scopeUserGroup, ref: groups[0], maxTokens: 12000},
					contextPolicySeed{kind: scopeUserGroup, ref: groups[1], effect: effectForbid},
				)
				return ContextPolicyQuery{Principal: principal}, EffectivePolicy{
					Deny:       true,
					DenyReason: "context denied by a forbid context-policy (deny-closed)",
				}
			},
		},
		{
			name: "redaction-or",
			setup: func(t *testing.T, h *harness, tenant model.TenantID, _ string) (ContextPolicyQuery, EffectivePolicy) {
				seedContextPolicies(t, h, tenant,
					contextPolicySeed{kind: scopeTenant, ref: tenant.String(), redactionRequired: true},
					contextPolicySeed{kind: scopeAgent, ref: "a1", redactionRequired: false},
				)
				return ContextPolicyQuery{Principal: h.scopedPrincipal(tenant), AgentRef: "a1"}, EffectivePolicy{
					Strategy:          strategyTruncate,
					RedactionRequired: true,
				}
			},
		},
		{
			name: "back-compat-no-effect-is-allow",
			setup: func(t *testing.T, h *harness, tenant model.TenantID, _ string) (ContextPolicyQuery, EffectivePolicy) {
				seedContextPolicies(t, h, tenant,
					contextPolicySeed{kind: scopeTenant, ref: tenant.String(), maxTokens: 8000, omitEffect: true},
					contextPolicySeed{kind: scopeKB, ref: "kb-1", strategy: strategySummarize, omitEffect: true},
					contextPolicySeed{kind: scopeAgent, ref: "a1", maxTokens: 32000, omitEffect: true},
				)
				return ContextPolicyQuery{Principal: h.scopedPrincipal(tenant), AgentRef: "a1", KBRef: "kb-1"}, EffectivePolicy{
					MaxContextTokens: 8000,
					Strategy:         strategySummarize,
					WinningScope:     "tenant:" + tenant.String(),
				}
			},
		},
		{
			name: "session-scope-ignored-when-unavailable",
			setup: func(t *testing.T, h *harness, tenant model.TenantID, _ string) (ContextPolicyQuery, EffectivePolicy) {
				seedContextPolicies(t, h, tenant,
					contextPolicySeed{kind: scopeSession, ref: "sess-1", effect: effectForbid},
				)
				return ContextPolicyQuery{Principal: h.scopedPrincipal(tenant)}, EffectivePolicy{
					Strategy: strategyTruncate,
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme")

			query, want := tc.setup(t, h, tenant, admin)
			got, err := h.module().Apply(context.Background(), tenant, query)
			if err != nil {
				t.Fatalf("Apply returned error: %v", err)
			}
			assertEffectivePolicy(t, got, want)
		})
	}
}

type contextPolicySeed struct {
	kind              string
	ref               string
	effect            string
	maxTokens         int64
	strategy          string
	redactionRequired bool
	spec              map[string]any
	omitEffect        bool
}

func seedContextPolicies(t *testing.T, h *harness, tenant model.TenantID, seeds ...contextPolicySeed) {
	t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(ctxPolicyKind)
		if err != nil {
			return err
		}
		for _, seed := range seeds {
			spec := "null"
			if seed.spec != nil {
				spec = marshalJSON(seed.spec)
			}
			rec := model.Record{
				colScopeKind: seed.kind,
				colScopeRef:  seed.ref,
				colMaxTokens: seed.maxTokens,
				colStrategy:  seed.strategy,
				colRedactReq: seed.redactionRequired,
				colSpec:      spec,
			}
			if !seed.omitEffect {
				effect := seed.effect
				if effect == "" {
					effect = effectAllow
				}
				rec[colEffect] = effect
			}
			if _, err := repo.Create(context.Background(), rec); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed context policies: %v", err)
	}
}

func contextPrincipalWithGroups(t *testing.T, h *harness, admin string, tenant model.TenantID, email, role string, groupNames ...string) (auth.Principal, []string) {
	t.Helper()
	ur := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": "memberpass1"}, nil)
	if ur.code != http.StatusCreated {
		t.Fatalf("create user %s = %d %s", email, ur.code, ur.raw)
	}
	uid := ur.body["id"].(string)
	if role != "" {
		if r := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": role}, nil); r.code != http.StatusCreated {
			t.Fatalf("grant membership = %d %s", r.code, r.raw)
		}
	}

	var groupIDs []string
	if len(groupNames) > 0 {
		if err := h.st.AuthMutate(context.Background(), func(as store.AuthScope) error {
			for _, name := range groupNames {
				g, err := as.Groups().Create(context.Background(), model.UserGroup{TargetTenantID: tenant, DisplayName: name})
				if err != nil {
					return err
				}
				if _, err := as.GroupMembers().Create(context.Background(), model.UserGroupMember{GroupID: g.ID, UserID: model.ID(uid)}); err != nil {
					return err
				}
				groupIDs = append(groupIDs, g.ID.String())
			}
			return nil
		}); err != nil {
			t.Fatalf("seed directory groups: %v", err)
		}
	}

	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"}, nil)
	if lr.code != http.StatusOK {
		t.Fatalf("login %s = %d %s", email, lr.code, lr.raw)
	}
	principal, err := auth.NewAuthenticator(h.st, nil).Authenticate(context.Background(), lr.body["token"].(string))
	if err != nil {
		t.Fatalf("authenticate %s: %v", email, err)
	}
	return principal, groupIDs
}

func assertEffectivePolicy(t *testing.T, got, want EffectivePolicy) {
	t.Helper()
	if got.Deny != want.Deny ||
		got.DenyReason != want.DenyReason ||
		got.MaxContextTokens != want.MaxContextTokens ||
		got.Strategy != want.Strategy ||
		got.RedactionRequired != want.RedactionRequired ||
		got.WinningScope != want.WinningScope ||
		!reflect.DeepEqual(got.ExcludedSources, want.ExcludedSources) {
		t.Fatalf("Apply = %+v, want %+v", got, want)
	}
}
