// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// fakeConnections is the brokered surface a Business build supplies: it takes only the
// two names it declares and leaves every other name to the tools after it.
type fakeConnections struct {
	listErr error
	callErr error
	// foreignErr is returned with handled=false for a name the broker does not own: a
	// broker that does that must not be able to swallow another tool's call.
	foreignErr error
	outcome    sessions.ConnectionOutcome
	called     []string
	// principal and tenant are what the last ConnectionTools or CallConnection received.
	principal auth.Principal
	tenant    model.TenantID
}

func (f *fakeConnections) ConnectionTools(_ context.Context, p auth.Principal, tenant model.TenantID) ([]sessions.ConnectionTool, error) {
	f.principal, f.tenant = p, tenant
	if f.listErr != nil {
		return nil, f.listErr
	}
	return []sessions.ConnectionTool{
		{Name: "connections_list", Description: "List the connections this session may use.", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), ReadOnly: true},
		{Name: "connection_exec", Description: "Run one command on an SSH connection.", InputSchema: json.RawMessage(`{"type":"object","properties":{"connection":{"type":"string"},"command":{"type":"string"}},"required":["connection","command"],"additionalProperties":false}`)},
	}, nil
}

func (f *fakeConnections) CallConnection(_ context.Context, p auth.Principal, tenant model.TenantID, name string, args json.RawMessage) (sessions.ConnectionOutcome, bool, error) {
	f.principal, f.tenant = p, tenant
	if name != "connections_list" && name != "connection_exec" {
		return sessions.ConnectionOutcome{}, false, f.foreignErr
	}
	f.called = append(f.called, name+" "+string(args))
	return f.outcome, true, f.callErr
}

func connectionsRequest(h *sessionMCPHandler, token, method, params string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/session/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":`+params+`}`))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// connectionsSession is an issued session credential and the two authenticators around
// it: the issuer a session endpoint uses, and the plain one a non-issued edge uses.
type connectionsSession struct {
	issuer    *auth.SessionCredentials
	plain     *auth.Authenticator
	bearer    string
	workToken string
	tenant    model.TenantID
}

func mintConnectionsSession(t *testing.T) connectionsSession {
	t.Helper()
	a, tenant, _, launcher, workToken, _ := workSessionEdgePrincipals(t)
	issuer := auth.NewSessionCredentials(a, func(context.Context, auth.SessionScope) error { return nil })
	bearer, err := issuer.Mint(t.Context(), launcher, auth.SessionScope{
		TenantID: tenant, WorkspaceID: model.NewID(), FolderRef: "fixture", SessionRef: "osn_" + model.NewID().String(),
		RunRef: model.NewID().String(), Fence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return connectionsSession{issuer: issuer, plain: a, bearer: bearer, workToken: workToken, tenant: tenant}
}

func TestSessionMCPOffersAndServesBrokeredConnections(t *testing.T) {
	s := mintConnectionsSession(t)
	bearer := s.bearer
	fake := &fakeConnections{outcome: sessions.ConnectionOutcome{Text: `{"stdout":"ok"}`}}
	h := &sessionMCPHandler{authr: s.issuer, issuedSessionOnly: true, connections: fake}

	list := connectionsRequest(h, bearer, "tools/list", `{}`)
	if list.Code != 200 {
		t.Fatalf("tools/list = %d %s", list.Code, list.Body.String())
	}
	var catalogue struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Annotations struct {
					ReadOnly    bool `json:"readOnlyHint"`
					Destructive bool `json:"destructiveHint"`
				} `json:"annotations"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &catalogue); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tool := range catalogue.Result.Tools {
		seen[tool.Name] = true
		switch tool.Name {
		case "connections_list":
			if !tool.Annotations.ReadOnly || tool.Annotations.Destructive {
				t.Errorf("connections_list annotations = %+v, want read-only", tool.Annotations)
			}
		case "connection_exec":
			if tool.Annotations.ReadOnly || !tool.Annotations.Destructive {
				t.Errorf("connection_exec annotations = %+v, want a tool that acts", tool.Annotations)
			}
		}
	}
	if !seen["connections_list"] || !seen["connection_exec"] {
		t.Fatalf("brokered tools missing from tools/list: %s", list.Body.String())
	}

	call := connectionsRequest(h, bearer, "tools/call", `{"name":"connection_exec","arguments":{"connection":"build-host","command":"uptime"}}`)
	if call.Code != 200 || !strings.Contains(call.Body.String(), `"isError":false`) || !strings.Contains(call.Body.String(), `ok`) {
		t.Fatalf("brokered call = %d %s", call.Code, call.Body.String())
	}
	if len(fake.called) != 1 || !strings.HasPrefix(fake.called[0], "connection_exec ") || !strings.Contains(fake.called[0], `"command":"uptime"`) {
		t.Fatalf("broker saw %v, want the call and its arguments", fake.called)
	}

	fake.outcome = sessions.ConnectionOutcome{Text: `{"error":"not granted"}`, IsError: true}
	if refused := connectionsRequest(h, bearer, "tools/call", `{"name":"connections_list","arguments":{}}`); !strings.Contains(refused.Body.String(), `"isError":true`) {
		t.Fatalf("a refusal must reach the session as an error result: %s", refused.Body.String())
	}

	// A name outside the brokered surface is left to the tools after it.
	before := len(fake.called)
	other := connectionsRequest(h, bearer, "tools/call", `{"name":"connection_unknown","arguments":{}}`)
	if len(fake.called) != before || !strings.Contains(other.Body.String(), `"error"`) {
		t.Fatalf("a foreign name was taken by the broker or answered as success: %s", other.Body.String())
	}
}

// A failing broker never lends its cause to the session: the error string of a dial
// can carry the target, a user name or a password.
func TestSessionMCPBrokeredFailureLeaksNoCause(t *testing.T) {
	s := mintConnectionsSession(t)
	bearer := s.bearer
	cause := errors.New("dial db.internal:5432 password=hunter2")
	h := &sessionMCPHandler{authr: s.issuer, issuedSessionOnly: true, connections: &fakeConnections{callErr: cause, listErr: cause}}
	for _, c := range []struct{ method, params string }{
		{"tools/list", `{}`},
		{"tools/call", `{"name":"connection_exec","arguments":{"connection":"x","command":"id"}}`},
	} {
		w := connectionsRequest(h, bearer, c.method, c.params)
		if !strings.Contains(w.Body.String(), `"error"`) || strings.Contains(w.Body.String(), "hunter2") || strings.Contains(w.Body.String(), "db.internal") {
			t.Fatalf("%s leaked or hid the failure: %s", c.method, w.Body.String())
		}
	}
}

// Without a broker, and for a credential that is not an issued session's, the endpoint
// offers and takes nothing: Community answers exactly as it did before connections.
func TestSessionMCPWithoutBrokerOffersNoConnections(t *testing.T) {
	s := mintConnectionsSession(t)
	fake := &fakeConnections{}
	for name, c := range map[string]struct {
		handler *sessionMCPHandler
		token   string
	}{
		"no broker":             {&sessionMCPHandler{authr: s.issuer, issuedSessionOnly: true}, s.bearer},
		"not an issued session": {&sessionMCPHandler{authr: s.plain, connections: fake}, s.workToken},
	} {
		list := connectionsRequest(c.handler, c.token, "tools/list", `{}`)
		if list.Code != http.StatusOK || strings.Contains(list.Body.String(), "connection_exec") || strings.Contains(list.Body.String(), "connections_list") {
			t.Fatalf("%s: tools/list = %d %s", name, list.Code, list.Body.String())
		}
		call := connectionsRequest(c.handler, c.token, "tools/call", `{"name":"connection_exec","arguments":{"connection":"x","command":"id"}}`)
		if !strings.Contains(call.Body.String(), `"error"`) || len(fake.called) != 0 {
			t.Fatalf("%s: tools/call = %s, broker saw %v", name, call.Body.String(), fake.called)
		}
	}
}

// A turn that has ended is answered with the sentence the other session tools use, and an
// error a broker attaches to a name it does not own never turns that call into a failure.
func TestSessionMCPBrokeredTurnEndedAndForeignNames(t *testing.T) {
	s := mintConnectionsSession(t)
	fake := &fakeConnections{callErr: auth.ErrUnauthenticated, foreignErr: errors.New("the broker failed on a name that is not its own")}
	h := &sessionMCPHandler{authr: s.issuer, issuedSessionOnly: true, connections: fake}

	ended := connectionsRequest(h, s.bearer, "tools/call", `{"name":"connection_exec","arguments":{"connection":"x","command":"id"}}`)
	if !strings.Contains(ended.Body.String(), "-31001") || !strings.Contains(ended.Body.String(), "The session turn has ended") {
		t.Fatalf("an ended turn was answered %s", ended.Body.String())
	}
	foreign := connectionsRequest(h, s.bearer, "tools/call", `{"name":"connection_unknown","arguments":{}}`)
	if strings.Contains(foreign.Body.String(), "Connection tool unavailable") || !strings.Contains(foreign.Body.String(), "Tool unavailable for this session purpose or grant") {
		t.Fatalf("a foreign name was taken over by the broker: %s", foreign.Body.String())
	}
}

// The broker decides a grant from the principal and tenant it is handed, so the endpoint
// must hand it the session's own, on a list and on a call alike, and keep the session's
// native tools in the list beside the brokered ones.
func TestSessionMCPBrokerGetsTheSessionsTenantAndKeepsNativeTools(t *testing.T) {
	s := mintConnectionsSession(t)
	fake := &fakeConnections{}
	withBroker := &sessionMCPHandler{authr: s.issuer, issuedSessionOnly: true, connections: fake}
	plain := &sessionMCPHandler{authr: s.issuer, issuedSessionOnly: true}

	names := func(h *sessionMCPHandler) map[string]bool {
		var out struct {
			Result struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"result"`
		}
		if err := json.Unmarshal(connectionsRequest(h, s.bearer, "tools/list", `{}`).Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		set := map[string]bool{}
		for _, tool := range out.Result.Tools {
			set[tool.Name] = true
		}
		return set
	}
	native, all := names(plain), names(withBroker)
	if len(native) == 0 {
		t.Fatal("the session offers no native tool to compare against")
	}
	for name := range native {
		if !all[name] {
			t.Errorf("native tool %q left the list once a broker was attached", name)
		}
	}
	if !all["connection_exec"] {
		t.Fatalf("the brokered tools are missing: %v", all)
	}
	assertSessionTenant := func(what string) {
		t.Helper()
		if fake.tenant != s.tenant {
			t.Errorf("%s: the broker got tenant %q, want the session's %q", what, fake.tenant, s.tenant)
		}
		if got := fake.principal.Tenants(); len(got) != 1 || got[0] != s.tenant {
			t.Errorf("%s: the broker got a principal of tenants %v, want only the session's", what, got)
		}
	}
	assertSessionTenant("tools/list")
	fake.principal, fake.tenant = auth.Principal{}, ""
	connectionsRequest(withBroker, s.bearer, "tools/call", `{"name":"connections_list","arguments":{}}`)
	assertSessionTenant("tools/call")
}
