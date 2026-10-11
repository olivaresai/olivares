// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sourcescope"
)

func TestDepartmentCredentialPrecedence(t *testing.T) {
	for _, slugs := range [][3]string{{"eng", "eng-platform", "eng-sre"}, {"z-root", "m-parent", "a-child"}} {
		t.Run(slugs[0], func(t *testing.T) {
			h := newHarness(t)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "department-credentials")
			principal := h.principalFor(admin, tenant, "actor@acme.io", "")
			approver := h.tokenFor(admin, tenant, "approver@acme.io", auth.RoleAdmin)
			var workspace model.ID
			if err := h.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
				for _, slug := range slugs {
					ws, err := sc.Workspaces().Create(t.Context(), model.Workspace{
						Name: slug, Slug: slug, Status: model.StatusActive, ParentID: workspace,
					})
					if err != nil {
						return err
					}
					workspace = ws.ID
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			agent := h.createAgent(tenant, "child-agent", workspace)
			session := h.createSession(tenant, "child-session", agent.ID, workspace)
			h.addAgentToGroup(tenant, agent.ID, "bots", workspace)

			for _, tc := range []struct {
				name           string
				credentials    [3]string // root, parent, child; "absent" means no binding
				agentGroup     bool
				ancestorDeny   bool
				wantAllowed    bool
				wantCredential string
			}{
				{name: "child-over-parent", credentials: [3]string{"root-key", "parent-key", "child-key"}, wantAllowed: true, wantCredential: "child-key"},
				{name: "parent-without-credential", credentials: [3]string{"absent", "", "child-key"}, wantAllowed: true, wantCredential: "child-key"},
				{name: "child-without-credential", credentials: [3]string{"root-key", "parent-key", ""}, wantAllowed: true},
				{name: "nearest-ancestor", credentials: [3]string{"root-key", "parent-key", "absent"}, wantAllowed: true, wantCredential: "parent-key"},
				{name: "nearest-ancestor-without-credential", credentials: [3]string{"root-key", "", "absent"}, wantAllowed: true},
				{name: "root-inheritance", credentials: [3]string{"root-key", "absent", "absent"}, wantAllowed: true, wantCredential: "root-key"},
				{name: "agent-group-precedence", credentials: [3]string{"root-key", "parent-key", "child-key"}, agentGroup: true, wantAllowed: true, wantCredential: "group-key"},
				{name: "ancestor-forbid", credentials: [3]string{"root-key", "parent-key", "child-key"}, ancestorDeny: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					hasAllow := false
					for i, credential := range tc.credentials {
						if credential == "absent" {
							continue
						}
						body := map[string]any{
							"source_type": "model", "source_ref": tc.name, "scope_tree": "workspace", "scope_ref": slugs[i], "enabled": true,
						}
						if credential != "" {
							body["cred_name"], body["cred_ref_kind"], body["cred_ref"] = credential, "vault", "test/"+credential
						}
						if tc.ancestorDeny && i == 0 {
							body["effect"] = "forbid"
						}
						if hasAllow && body["effect"] != "forbid" {
							h.createBindingApproved(admin, approver, tenant, body)
						} else if r := h.createBinding(admin, tenant, body); r.code != http.StatusCreated {
							t.Fatalf("bind %s: %d %s", slugs[i], r.code, r.raw)
						}
						if body["effect"] != "forbid" {
							hasAllow = true
						}
					}
					if tc.agentGroup {
						h.createBindingApproved(admin, approver, tenant, map[string]any{
							"source_type": "model", "source_ref": tc.name, "scope_tree": "agent_group", "scope_ref": "bots", "enabled": true,
							"cred_name": "group-key", "cred_ref_kind": "vault", "cred_ref": "test/group-key",
						})
					}
					for _, entry := range []string{"agent", "session"} {
						t.Run(entry, func(t *testing.T) {
							var decision sourcescope.Decision
							var err error
							if entry == "agent" {
								decision, err = h.resolver.ResolveForAgent(t.Context(), tenant, principal, agent.ExternalID, sourcescope.SourceModel, tc.name)
							} else {
								decision, err = h.resolver.ResolveForSession(t.Context(), tenant, principal, session.ExternalID, sourcescope.SourceModel, tc.name)
							}
							if err != nil {
								t.Fatal(err)
							}
							if decision.Allowed != tc.wantAllowed || !decision.Bound {
								t.Fatalf("decision = %+v, want allowed=%v and bound", decision, tc.wantAllowed)
							}
							if tc.wantCredential == "" {
								if decision.Cred != nil {
									t.Fatalf("credential = %+v, want nil", decision.Cred)
								}
							} else if want := (sourcescope.CredRef{Name: tc.wantCredential, RefKind: "vault", Ref: "test/" + tc.wantCredential}); decision.Cred == nil || *decision.Cred != want {
								t.Fatalf("credential = %+v, want %+v", decision.Cred, want)
							}
							actorRef := agent.ExternalID
							if entry == "session" {
								actorRef = session.ExternalID
							}
							preview := h.do(http.MethodGet, "/v1/m/sourcescope/resolve?source_type=model&source_ref="+tc.name+"&actor_kind="+entry+"&actor_ref="+actorRef, admin, nil, tenantHdr(tenant))
							if preview.code != http.StatusOK || preview.body["allowed"] != tc.wantAllowed || preview.body["bound"] != true {
								t.Fatalf("preview = %d %s", preview.code, preview.raw)
							}
							credential, _ := preview.body["cred_name"].(string)
							if credential != tc.wantCredential {
								t.Fatalf("preview credential = %q, want %q", credential, tc.wantCredential)
							}
						})
					}
				})
			}
		})
	}
}
