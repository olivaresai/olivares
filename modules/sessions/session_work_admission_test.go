// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// askedWorkAuthorizer allows every permission and records what it was asked.
type askedWorkAuthorizer struct {
	mu    sync.Mutex
	asked []auth.Request
}

func (a *askedWorkAuthorizer) Authorize(_ context.Context, req auth.Request) auth.Decision {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked = append(a.asked, req)
	return auth.Decision{Allow: true}
}

// The session MCP's work port has no API server, so it hands the work handlers its own
// admission door: the composed authorizer. A work command through it therefore asks for
// sessions:work:admin on behalf of the session's principal, as the REST route does,
// instead of reading the rank of the launcher's role.
func TestSessionWorkPortAsksTheAuthorizerForWorkAdmin(t *testing.T) {
	f := newOrchestrationFixtureInWorkspace(t, []string{"work.read"}, true)
	a := auth.NewAuthenticator(f.h.st, nil)
	launcher, err := a.Authenticate(t.Context(), f.admin)
	if err != nil {
		t.Fatal(err)
	}
	live, _ := f.h.m.rt.getLive(f.tenant, f.run)
	issuer := auth.NewSessionCredentials(a, func(context.Context, auth.SessionScope) error { return nil })
	bearer, err := issuer.Mint(t.Context(), launcher, auth.SessionScope{
		TenantID: f.tenant, WorkspaceID: f.workspace, FolderRef: "test-folder", FolderPath: live.companion.Dir,
		SessionRef: f.sid, RunRef: f.run, AgentRef: live.agentRef, Fence: live.claim.Fence,
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := issuer.Authenticate(t.Context(), bearer)
	if err != nil {
		t.Fatal(err)
	}

	asked := &askedWorkAuthorizer{}
	WithWorkAuthorizer(asked)(f.h.m)
	w := httptest.NewRecorder()
	f.h.m.CallSessionWork(w, httptest.NewRequest("POST", "/v1/m/sessions/work-items?mode=validate", strings.NewReader(`{"command":"item.create"}`)), p, f.tenant)

	asked.mu.Lock()
	defer asked.mu.Unlock()
	for _, req := range asked.asked {
		if req.Permission == permWorkAdmin {
			if req.Principal.SessionIdentity != p.SessionIdentity || req.Tenant != f.tenant {
				t.Fatalf("work admin was asked for %+v in %s, want the session principal in its tenant", req.Principal, req.Tenant)
			}
			if req.Resource.WorkspaceID != f.workspace {
				t.Fatalf("work admin was asked in workspace %q, want the session's confining workspace %q", req.Resource.WorkspaceID, f.workspace)
			}
			return
		}
	}
	t.Fatalf("the work port never asked for %s (asked %d permissions, status %d %s)", permWorkAdmin, len(asked.asked), w.Code, w.Body.String())
}

// The port's door answers with the composed authorizer's decision and refuses without one:
// an allow is true, a deny is false, and a module with no authorizer yet is false rather
// than a panic.
func TestWorkAdmissionIsTheAuthorizersDecision(t *testing.T) {
	req := auth.Request{Permission: permWorkAdmin, Resource: auth.ResourceFor(permWorkAdmin)}
	for _, tc := range []struct {
		name string
		az   WorkAuthorizer
		want bool
	}{
		{"allowed", permissionSetWorkAuthorizer{permWorkAdmin: true}, true},
		{"denied", permissionSetWorkAuthorizer{permWorkAdmin: false}, false},
		{"another permission only", permissionSetWorkAuthorizer{permWorkRead: true}, false},
		{"no authorizer yet", nil, false},
	} {
		m := &Module{Dependencies: &Dependencies{WorkAuthorizer: tc.az}}
		if got := m.workAdmission(t.Context(), req); got != tc.want {
			t.Errorf("%s: workAdmission = %v, want %v", tc.name, got, tc.want)
		}
	}
}
