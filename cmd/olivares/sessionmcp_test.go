// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

func TestSessionMCPDenyOrdinaryOriginAndWorkerEscalation(t *testing.T) {
	a, tenant, ordinary, _, worker, _ := workSessionEdgePrincipals(t)
	calls := 0
	h := &sessionMCPHandler{authr: a, work: func(w http.ResponseWriter, r *http.Request, p auth.Principal, got model.TenantID) {
		calls++
		if r.Header.Get("Authorization") != "" || got != tenant || p.SessionRunRef == "" {
			t.Error("session authority was substituted")
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"forbidden"}`))
	}}
	request := func(token, origin, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/session/mcp", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	list := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	for _, token := range []string{"", ordinary, "unknown"} {
		w := request(token, "", list)
		if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
			t.Fatalf("ordinary credential admitted: %d", w.Code)
		}
	}
	if w := request(worker, "https://untrusted.example", list); w.Code != http.StatusForbidden {
		t.Fatalf("origin = %d", w.Code)
	}
	w := request(worker, "", list)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "olivares_work_command") || strings.Contains(w.Body.String(), "olivares_work_list") || strings.Contains(w.Body.String(), "olivares_session_send") {
		t.Fatalf("worker catalog: %d %s", w.Code, w.Body.String())
	}
	if w := request(worker, "", `{"jsonrpc":"2.0","id":"deny","method":"tools/call","params":{"name":"olivares_work_list","arguments":{}}}`); !strings.Contains(w.Body.String(), `"error"`) || calls != 0 {
		t.Fatal("worker backlog tool escaped its catalog")
	}
	if w := request(worker, "", `{"jsonrpc":"2.0","id":"deny-handoff","method":"tools/call","params":{"name":"olivares_session_handoff_offer","arguments":{}}}`); !strings.Contains(w.Body.String(), `"error"`) || calls != 0 {
		t.Fatal("work bearer escaped into the communication purpose")
	}
	w = request(worker, "", `{"jsonrpc":"2.0","id":"bounded","method":"tools/call","params":{"name":"olivares_work_command","arguments":{"mode":"apply","idempotency_key":"fixture","command":{"command":"item.create"}}}}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"isError":true`) || calls != 1 {
		t.Fatalf("module denial lost: %d %s calls=%d", w.Code, w.Body.String(), calls)
	}
	if bytes.Contains(w.Body.Bytes(), []byte(worker)) {
		t.Fatal("bearer exposed")
	}
	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response["id"] != "bounded" {
		t.Fatal("string JSON-RPC id not preserved")
	}
}

func TestSessionMCPSharedCredentialUsesPrincipalWithoutRESTBearer(t *testing.T) {
	a, tenant, ordinary, launcher, _, _ := workSessionEdgePrincipals(t)
	issuer := auth.NewSessionCredentials(a, func(context.Context, auth.SessionScope) error { return nil })
	workspace := model.NewID()
	bearer, err := issuer.Mint(t.Context(), launcher, auth.SessionScope{
		TenantID: tenant, WorkspaceID: workspace, FolderRef: "fixture", SessionRef: "osn_" + model.NewID().String(),
		RunRef: model.NewID().String(), Fence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	h := &sessionMCPHandler{authr: issuer, issuedSessionOnly: true, work: func(w http.ResponseWriter, r *http.Request, p auth.Principal, got model.TenantID) {
		calls++
		bounded, ok := p.ConfinedWorkspaceIn(got)
		_, ordinary := p.Ref()
		if got != tenant || !ok || bounded != workspace || ordinary || p.SessionRunRef == "" || r.Header.Get("Authorization") != "" {
			t.Error("work port lost the shared session principal or received a bearer")
		}
		_, _ = w.Write([]byte(`{"items":[]}`))
	}}
	request := func(token, method, params string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/session/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":`+params+`}`))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request(ordinary, "tools/list", `{}`); w.Code != http.StatusUnauthorized {
		t.Fatal("shared service fell back to an ordinary credential")
	}
	if w := request(bearer, "tools/list", `{}`); w.Code != 200 || !strings.Contains(w.Body.String(), "olivares_work_list") || strings.Contains(w.Body.String(), "olivares_session_") || strings.Contains(w.Body.String(), "olivares_work_command") {
		t.Fatalf("viewer session catalog: %d %s", w.Code, w.Body.String())
	}
	if w := request(bearer, "tools/call", `{"name":"olivares_work_list","arguments":{}}`); w.Code != 200 || calls != 1 || strings.Contains(w.Body.String(), bearer) {
		t.Fatalf("shared session work call: %d %s", w.Code, w.Body.String())
	}
	for _, name := range []string{"olivares_session_send", "olivares_session_inbox", "olivares_session_handoff_offer", "olivares_work_command"} {
		if w := request(bearer, "tools/call", `{"name":"`+name+`","arguments":{}}`); !strings.Contains(w.Body.String(), `"error"`) || calls != 1 {
			t.Fatalf("unavailable %s reached the work port", name)
		}
	}
}

type sessionMCPObservationSink struct{ observations []sdkmodel.Observation }

func (s *sessionMCPObservationSink) Emit(_ context.Context, o sdkmodel.Observation) error {
	s.observations = append(s.observations, o)
	return nil
}

func TestSessionMCPCommunicationPlaneOff(t *testing.T) {
	a, tenant, _, _, _, worker := workSessionEdgePrincipals(t)
	actor, err := auth.NewSystemOperator("test:session-mcp", "exercise native session communication tools")
	if err != nil {
		t.Fatal(err)
	}
	workspace := model.NewID()
	c, err := a.IssueCommunicationSessionCredential(t.Context(), actor, auth.CommunicationSessionCredentialSpec{Tenant: tenant, WorkspaceID: workspace, SessionRef: worker.SessionIdentity, RunRef: worker.SessionRunRef, ClaimFence: 1})
	if err != nil {
		t.Fatal(err)
	}
	h := &sessionMCPHandler{authr: a, work: func(http.ResponseWriter, *http.Request, auth.Principal, model.TenantID) {
		t.Error("communication reached the work port")
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	spec, _ := json.Marshal([]any{map[string]any{"name": "session-messages", "url": srv.URL + "/session/mcp", "headers": map[string]string{"Authorization": "Bearer " + c.Token}, "next_revision": false}})
	connector := mcpc.New()
	defer func() { _ = connector.Close(t.Context()) }()
	if err := connector.Open(t.Context(), sdk.Config{Settings: map[string]string{"servers": string(spec), "posture_scan": "false", "timeout": "5s"}}); err != nil {
		t.Fatal(err)
	}
	sink := &sessionMCPObservationSink{}
	if err := connector.Gather(t.Context(), sink); err != nil {
		t.Fatal(err)
	}
	edges := 0
	for _, o := range sink.observations {
		if edge, ok := o.(sdkmodel.EdgeObservation); ok {
			edges++
			if !strings.HasPrefix(edge.ToolRef, "olivares_session_") {
				t.Error("communication token advertised work tools")
			}
		}
	}
	if edges != 0 {
		t.Fatalf("communication plane exposed %d tools, want 0", edges)
	}
	r := httptest.NewRequest(http.MethodPost, "/session/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"olivares_session_inbox","arguments":{"limit":5}}}`))
	r.Header.Set("Authorization", "Bearer "+c.Token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"error"`) || strings.Contains(w.Body.String(), c.Token) {
		t.Fatal("hidden inbox was callable")
	}
	if err := a.RevokeCommunicationSessionCredential(t.Context(), actor, c.ID, auth.CommunicationSessionCredentialSpec{Tenant: tenant, WorkspaceID: workspace, SessionRef: worker.SessionIdentity, RunRef: worker.SessionRunRef, ClaimFence: 1}); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r.Clone(t.Context()))
	if w.Code != 401 {
		t.Fatal("revoked bearer reused a connection's authority")
	}
}

func TestSessionMCPOrchestratorCatalogAndCurrentGrantDenial(t *testing.T) {
	a, tenant, _, _, _, worker := workSessionEdgePrincipals(t)
	actor, err := auth.NewSystemOperator("test:session-mcp", "exercise separate orchestration purpose")
	if err != nil {
		t.Fatal(err)
	}
	c, err := a.IssueOrchestrationSessionCredential(t.Context(), actor, auth.OrchestrationSessionCredentialSpec{Tenant: tenant, WorkspaceID: model.NewID(), SessionRef: worker.SessionIdentity, RunRef: worker.SessionRunRef, AgentRef: "orchestrator:" + model.NewID().String(), ClaimFence: 1, ProfileRef: "ppf_" + model.NewID().String(), GrantID: model.NewID(), Capabilities: []string{"work.read"}})
	if err != nil {
		t.Fatal(err)
	}
	live := true
	h := &sessionMCPHandler{authr: a, work: func(http.ResponseWriter, *http.Request, auth.Principal, model.TenantID) {
		t.Error("catalog called work API")
	}, checkOrchestration: func(context.Context, auth.Principal, model.TenantID) error {
		if !live {
			return auth.ErrUnauthenticated
		}
		return nil
	}}
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/session/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		r.Header.Set("Authorization", "Bearer "+c.Token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := request()
	if w.Code != 200 || !strings.Contains(w.Body.String(), "olivares_work_list") || strings.Contains(w.Body.String(), "olivares_work_command") {
		t.Fatalf("read grant catalog = %d %s", w.Code, w.Body.String())
	}
	live = false
	if w := request(); w.Code != 403 {
		t.Fatal("withdrawn profile grant advertised session authority")
	}
	h.checkOrchestration = nil
	if w := request(); w.Code != 403 {
		t.Fatal("unwired grant authority admitted orchestrator")
	}
}

func TestSessionMCPWorkLeaseEnvelope(t *testing.T) {
	workspace := model.NewID()
	p := auth.Principal{SessionWorkspaceID: workspace}
	item, sid := model.NewID(), "osn_"+model.NewID().String()
	command, _ := json.Marshal(map[string]any{"mode": "apply", "version": 3, "idempotency_key": "stable-command", "command": map[string]any{"command": "lease.acquire", "work_item_id": item, "holder_sid": sid, "holder_run_ref": model.NewID(), "ttl_seconds": 60}})
	r, err := sessionToolRequest(t.Context(), p, "olivares_work_command", command)
	if err != nil {
		t.Fatal(err)
	}
	if r.URL.Path != "/v1/m/sessions/work-items/"+item.String()+"/lease/acquire" || r.Header.Get("If-Match") != `"v3"` || r.Header.Get("Idempotency-Key") != "stable-command" {
		t.Fatal("lease lost route/version/idempotency")
	}
	body, _ := io.ReadAll(r.Body)
	if bytes.Contains(body, []byte("work_item_id")) {
		t.Fatal("lease body escaped closed REST projection")
	}
	bad, _ := json.Marshal(map[string]any{"mode": "apply", "idempotency_key": "stable-command", "command": map[string]any{"command": "lease.acquire", "work_item_id": item, "title": "silently ignored"}})
	if _, err := sessionToolRequest(t.Context(), p, "olivares_work_command", bad); err == nil {
		t.Fatal("lease unrelated field discarded silently")
	}
}

func TestSessionMCPCommunicationEnvelopesUnavailable(t *testing.T) {
	for _, name := range []string{"olivares_session_inbox", "olivares_session_delivery", "olivares_session_send", "olivares_session_ack", "olivares_session_handoff_inbox", "olivares_session_handoff_get", "olivares_session_handoff_offer", "olivares_session_handoff_respond"} {
		if _, err := sessionToolRequest(t.Context(), auth.Principal{}, name, []byte(`{}`)); err == nil {
			t.Fatalf("%s retained a callable envelope", name)
		}
	}
}

func TestSessionMCPManagedSwitchDefaultOffAndRevocation(t *testing.T) {
	a, tenant, _, _, worker, _ := workSessionEdgePrincipals(t)
	enabled := false
	unavailable := false
	checks := 0
	h := &sessionMCPHandler{authr: a, work: func(http.ResponseWriter, *http.Request, auth.Principal, model.TenantID) {
		t.Error("catalog called work API")
	}, enabled: func(ctx context.Context, got model.TenantID) (bool, error) {
		checks++
		if got != tenant {
			t.Error("switch read wrong tenant")
		}
		if unavailable {
			return false, auth.ErrMCPGatewayUnavailable
		}
		return enabled, nil
	}}
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/session/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
		r.Header.Set("Authorization", "Bearer "+worker)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request(); w.Code != 404 || strings.Contains(w.Body.String(), "olivares_work_command") {
		t.Fatal("default-off catalog exposed")
	}
	enabled = true
	if w := request(); w.Code != 200 || !strings.Contains(w.Body.String(), "olivares_work_command") {
		t.Fatalf("enabled catalog=%d %s", w.Code, w.Body.String())
	}
	enabled = false
	if w := request(); w.Code != 404 {
		t.Fatal("switch revocation ignored")
	}
	unavailable = true
	if w := request(); w.Code != 503 {
		t.Fatal("unreadable switch admitted")
	}
	if checks != 4 {
		t.Fatal("switch not checked per authenticated request")
	}
}
