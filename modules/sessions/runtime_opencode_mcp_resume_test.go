// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

type openCodeCompanionMCPFixture struct {
	issuer        *auth.SessionCredentials
	launcher      auth.Principal
	workspace     model.ID
	dir, endpoint string
}

func (f *openCodeCompanionMCPFixture) Authorize(ctx context.Context, tenant model.TenantID, in LaunchIntent) (LaunchDecision, error) {
	token, err := f.issuer.Mint(ctx, f.launcher, auth.SessionScope{
		TenantID: tenant, WorkspaceID: f.workspace, FolderRef: in.RunRef,
		SessionRef: in.ClaimSID, RunRef: in.RunRef, AgentRef: in.AgentRef, Holder: in.Holder, Fence: in.Fence,
	})
	return LaunchDecision{Allowed: err == nil, InjectEnv: []EnvVar{{Name: "OLIVARES_HOOK_PEP_TOKEN", Value: token}}}, err
}

func (f *openCodeCompanionMCPFixture) ConfigureSessionMCP(_ context.Context, _ model.TenantID, runRef, driver string, spec *LaunchSpec) (func(), error) {
	return ConfigureSessionMCP(spec, driver, f.dir, runRef, f.endpoint, "OLIVARES_HOOK_PEP_TOKEN")
}

// The native peer lists MCP tools before answering session/new or session/resume.
// Listing managed tools resolves RuntimeCompanion even with no configured servers.
// A wire-only fixture cannot detect a runtime lock held across that callback.
func TestOpenCodeResumeMCPRegistersBeforeHandshakeReturns(t *testing.T) {
	f := &openCodeCompanionMCPFixture{dir: t.TempDir()}
	m := New(openCodeRuntimeOptions(WithLaunchGate(f), WithSessionWorkspaceRoot(t.TempDir()))...)
	m.UseExecutionEnvironmentRef(testEnvRef)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "opencode-mcp-resume")
	f.workspace = workAPIWorkspace(t, h, tenant)
	a := auth.NewAuthenticator(h.st, nil)
	var err error
	f.launcher, err = a.Authenticate(t.Context(), admin)
	if err != nil {
		t.Fatal(err)
	}
	f.issuer = auth.NewSessionCredentials(a, func(ctx context.Context, scope auth.SessionScope) error {
		return m.Authority(ctx, scope.TenantID, scope.SessionRef, scope.Holder, scope.Fence)
	})
	type registration struct {
		p         auth.Principal
		companion RuntimeCompanion
		err       error
	}
	registrations := make(chan registration, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		p, err := f.issuer.Authenticate(ctx, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		var companion RuntimeCompanion
		if err == nil {
			companion, err = m.RuntimeCompanion(ctx, tenant, p)
		}
		registrations <- registration{p, companion, err}
		if err != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	f.endpoint = server.URL + "/session/mcp"
	m.SessionMCP = f
	profile := newOpenCodeProfile(t, m, tenant, "resume-mcp", AuthSourceAccountHome)
	setOpenCodeFixture(t, profile, openCodeFixture{SessionID: "ses-mcp-register", RequireMCPURL: f.endpoint, ConnectMCP: true})
	run, err := m.createRun(t.Context(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: f.launcher.Actor(), ActorKind: model.ActorUser, ProviderProfileRef: profile.Ref,
	})
	if err != nil {
		t.Fatalf("new session MCP registration: %v", err)
	}
	first := <-registrations
	if first.err != nil {
		t.Fatal(first.err)
	}
	if _, err := m.stopRun(t.Context(), tenant, run.RunRef, f.launcher.Actor(), model.ActorUser); err != nil {
		t.Fatal(err)
	}
	if first.companion.Context.Err() == nil {
		t.Fatal("stopped generation kept its companion lifetime")
	}
	resumed, err := m.resumeRun(t.Context(), tenant, run.RunRef, f.launcher.Actor(), model.ActorUser, "")
	second := <-registrations
	if err != nil || second.err != nil {
		t.Fatalf("resumed MCP tools/list before handshake completed: launch=%v, registration=%v", err, second.err)
	}
	if resumed.ProviderConversationID != run.ProviderConversationID || second.companion.LaunchID == first.companion.LaunchID {
		t.Fatal("resume lost its conversation or reused the stopped generation")
	}
	for name, p := range map[string]auth.Principal{"stopped generation": first.p, "ordinary login": f.launcher} {
		if _, err := m.RuntimeCompanion(t.Context(), tenant, p); err == nil {
			t.Fatalf("%s retained companion authority", name)
		}
	}
	wrongFence := second.p
	wrongFence.SessionFence++
	if _, err := m.RuntimeCompanion(t.Context(), tenant, wrongFence); err == nil {
		t.Fatal("wrong fence admitted")
	}
	if _, err := m.RuntimeCompanion(t.Context(), model.TenantID(model.NewID()), second.p); err == nil {
		t.Fatal("wrong tenant admitted")
	}
	if _, err := m.stopRun(t.Context(), tenant, run.RunRef, f.launcher.Actor(), model.ActorUser); err != nil {
		t.Fatal(err)
	}
	if second.companion.Context.Err() == nil {
		t.Fatal("resumed generation kept its companion lifetime after stop")
	}
	if _, err := m.RuntimeCompanion(t.Context(), tenant, second.p); err == nil {
		t.Fatal("stopped resumed generation admitted")
	}
}
