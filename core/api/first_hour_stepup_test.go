// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// The first-hour actions share the policy gate even though their authorization
// and persistence paths differ: a workspace, an administrator, a source, a key in
// the secret store and a Settings change (the module selection). Source
// application and the module selection are stubs here; the other actions use the
// real store, sealer-backed secret store and authentication flow.
func TestFirstHourActionsFollowStepUpPolicy(t *testing.T) {
	for _, policy := range []string{auth.StepUpNone, auth.StepUpTOTP, auth.StepUpPasskey} {
		t.Run(policy, func(t *testing.T) {
			roster := &firstHourSourceRoster{}
			modules := &firstHourModules{}
			configure := func(o *api.Options) {
				o.SourceRoster = roster
				o.SecretStore = auth.NewSecretStore(o.Store, fakeSealer{})
				o.ModuleSelection = modules
			}
			h := newHarnessOpts(t, configure)
			admin := h.adminLogin()
			who := h.do("GET", "/v1/auth/whoami", admin, nil, nil)
			grants, _ := who.body["grants"].([]any)
			if who.code != http.StatusOK || len(grants) != 1 || who.body["aal"] != float64(auth.AAL1) {
				t.Fatalf("fresh password owner: %d %s", who.code, who.raw)
			}
			tenant := grants[0].(map[string]any)["tenant"].(string)
			hdr := map[string]string{"X-Olivares-Tenant": tenant}
			ws := h.do("GET", "/v1/workspaces", admin, nil, hdr)
			items, _ := ws.body["items"].([]any)
			if ws.code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["slug"] != "default" {
				t.Fatalf("fresh setup must already have its default workspace: %d %s", ws.code, ws.raw)
			}
			if policy != auth.StepUpNone {
				h.requireStepUp(policy)
			}
			// Reconstruct the server/authenticator over the same open store:
			// reloading must preserve the policy and the original AAL1 session.
			h = newHarnessOptsFromStoreSource(t, harnessStoreSource{borrowed: h.st}, configure)
			membersBefore := h.do("GET", "/v1/members", admin, nil, hdr)
			secretsBefore := h.do("GET", "/v1/console/secrets", admin, nil, nil)
			if membersBefore.code != http.StatusOK || secretsBefore.code != http.StatusOK {
				t.Fatalf("members/secrets before actions: %d/%d", membersBefore.code, secretsBefore.code)
			}
			for _, action := range []struct {
				method, path string
				body         map[string]any
				hdr          map[string]string
				want         int
			}{
				{"POST", "/v1/workspaces", map[string]any{"name": "Team", "slug": "team"}, hdr, http.StatusCreated},
				{"POST", "/v1/onboard", map[string]any{"email": "admin@team.invalid", "role": auth.RoleAdmin, "password": "first-hour-password"}, hdr, http.StatusCreated},
				{"PUT", "/v1/console/sources", map[string]any{"name": "claude", "tenant": tenant, "enabled": true}, hdr, http.StatusOK},
				{"PUT", "/v1/console/secrets", map[string]any{"name": "vault/FIRST_HOUR_KEY", "value": "first-hour-value"}, nil, http.StatusOK},
				{"PUT", "/v1/console/modules", map[string]any{"selected": []string{"sessions"}}, nil, http.StatusOK},
			} {
				r := h.do(action.method, action.path, admin, action.body, action.hdr)
				if policy != auth.StepUpNone {
					if r.code != http.StatusForbidden || errorCode(r) != "step_up_required" {
						t.Errorf("%s %s at AAL1 with %s policy = %d %s", action.method, action.path, policy, r.code, r.raw)
					}
				} else if r.code != action.want {
					t.Errorf("%s %s at AAL1 with default policy = %d %s, want %d", action.method, action.path, r.code, r.raw, action.want)
				}
			}
			who = h.do("GET", "/v1/auth/whoami", admin, nil, nil)
			if who.body["aal"] != float64(auth.AAL1) || who.body["admin_step_up"] != policy || who.body["step_up_satisfied"] != (policy == auth.StepUpNone) {
				t.Fatalf("setup actions changed assurance or policy: %s", who.raw)
			}
			secretsAfter := h.do("GET", "/v1/console/secrets", admin, nil, nil)
			stored, _ := secretsAfter.body["secrets"].([]any)
			if policy == auth.StepUpNone {
				if roster.writes != 1 || modules.writes != 1 || len(stored) != 1 {
					t.Fatalf("source/module writes = %d/%d and secrets %s, want 1/1 and one secret", roster.writes, modules.writes, secretsAfter.raw)
				}
			} else {
				membersAfter := h.do("GET", "/v1/members", admin, nil, hdr)
				workspacesAfter := h.do("GET", "/v1/workspaces", admin, nil, hdr)
				if roster.writes != 0 || modules.writes != 0 || membersAfter.raw != membersBefore.raw ||
					workspacesAfter.raw != ws.raw || secretsAfter.raw != secretsBefore.raw {
					t.Fatal("refused setup actions changed workspace, membership, source, secret or module state")
				}
			}
		})
	}
}

// firstHourModules counts module-selection writes (the Settings change).
type firstHourModules struct{ writes int }

func (*firstHourModules) ModuleSelection(context.Context) (api.ModuleSelectionDTO, error) {
	return api.ModuleSelectionDTO{}, nil
}

func (m *firstHourModules) SelectModules(context.Context, auth.Principal, []string) (api.ModuleSelectionDTO, error) {
	m.writes++
	return api.ModuleSelectionDTO{}, nil
}

type firstHourSourceRoster struct {
	stubSourceRoster
	writes int
}

func (s *firstHourSourceRoster) PutSource(context.Context, auth.Principal, api.SourceRosterInput) (api.SourceApplyResult, error) {
	s.writes++
	return api.SourceApplyResult{}, nil
}

// Removing an extra authentication ceremony does not grant tenant membership,
// administrative permissions, or human assurance to an API token.
func TestFirstHourDefaultPolicyDoesNotGrantAuthority(t *testing.T) {
	roster := &firstHourSourceRoster{}
	modules := &firstHourModules{}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.SourceRoster = roster
		o.SecretStore = auth.NewSecretStore(o.Store, fakeSealer{})
		o.ModuleSelection = modules
	})
	root := h.adminLogin()
	tenant := h.createOrg(root, "target")
	other := h.createOrg(root, "other")
	viewer := h.mkMember(root, "viewer@target.invalid", "viewer-password", auth.RoleViewer, tenant)
	outsider := h.mkMember(root, "owner@other.invalid", "owner-password", auth.RoleOwner, other)
	issued := h.do("POST", "/v1/tokens", root, map[string]any{"name": "automation", "superadmin": true}, nil)
	if issued.code != http.StatusCreated {
		t.Fatalf("issue API token: %d", issued.code)
	}
	hdr := tenantHdr(tenant)
	membersBefore := h.do("GET", "/v1/members", root, nil, hdr)
	workspacesBefore := h.do("GET", "/v1/workspaces", root, nil, hdr)
	secretsBefore := h.do("GET", "/v1/console/secrets", root, nil, nil)
	for name, token := range map[string]string{"viewer": viewer, "other tenant owner": outsider, "API token": issued.body["token"].(string)} {
		t.Run(name, func(t *testing.T) {
			for _, action := range []struct {
				method, path string
				body         map[string]any
			}{
				{"POST", "/v1/workspaces", map[string]any{"name": "Refused", "slug": "refused"}},
				{"POST", "/v1/onboard", map[string]any{"email": "refused@target.invalid", "role": auth.RoleAdmin, "password": "refused-password"}},
				{"PUT", "/v1/console/sources", map[string]any{"name": "refused", "tenant": tenant.String(), "enabled": true}},
				{"PUT", "/v1/console/secrets", map[string]any{"name": "vault/REFUSED", "value": "refused-value"}},
				{"PUT", "/v1/console/modules", map[string]any{"selected": []string{"sessions"}}},
			} {
				if r := h.do(action.method, action.path, token, action.body, hdr); r.code != http.StatusForbidden {
					t.Errorf("%s %s = %d, want 403", action.method, action.path, r.code)
				}
			}
		})
	}
	membersAfter := h.do("GET", "/v1/members", root, nil, hdr)
	workspacesAfter := h.do("GET", "/v1/workspaces", root, nil, hdr)
	secretsAfter := h.do("GET", "/v1/console/secrets", root, nil, nil)
	if membersBefore.code != http.StatusOK || workspacesBefore.code != http.StatusOK || secretsBefore.code != http.StatusOK ||
		membersAfter.code != http.StatusOK || workspacesAfter.code != http.StatusOK || secretsAfter.code != http.StatusOK ||
		membersBefore.raw != membersAfter.raw || workspacesBefore.raw != workspacesAfter.raw ||
		secretsBefore.raw != secretsAfter.raw || roster.writes != 0 || modules.writes != 0 {
		t.Fatal("refused callers changed membership, workspace, source, secret or module state")
	}
}
