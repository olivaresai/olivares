// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

func TestSessionMCPPeerToolsPinSenderAndInboxToSharedPrincipal(t *testing.T) {
	a, tenant, _, viewer, _, _ := workSessionEdgePrincipals(t)
	admin := auth.ScopedPrincipal(model.NewID(), "peer launcher", tenant, auth.RoleAdmin)
	launcherToken, _, err := a.IssueToken(t.Context(), admin, auth.TokenSpec{Name: "peer editor", BoundTenant: tenant, Role: auth.RoleEditor})
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := a.Authenticate(t.Context(), launcherToken)
	if err != nil {
		t.Fatal(err)
	}
	issuer := auth.NewSessionCredentials(a, func(context.Context, auth.SessionScope) error { return nil })
	workspace, sender, recipient := model.NewID(), "osn_"+model.NewID().String(), "osn_"+model.NewID().String()
	mint := func(p auth.Principal) string {
		t.Helper()
		bearer, err := issuer.Mint(t.Context(), p, auth.SessionScope{
			TenantID: tenant, WorkspaceID: workspace, FolderRef: "fixture", SessionRef: sender,
			RunRef: model.NewID().String(), Fence: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		return bearer
	}
	bearer, readonly := mint(launcher), mint(viewer)
	calls := 0
	h := &sessionMCPHandler{authr: issuer, issuedSessionOnly: true, work: func(w http.ResponseWriter, r *http.Request, p auth.Principal, got model.TenantID) {
		calls++
		bounded, ok := p.ConfinedWorkspaceIn(got)
		if got != tenant || !ok || bounded != workspace || p.SessionIdentity != sender || r.Header.Get("Authorization") != "" {
			t.Error("peer work port lost the shared session boundary or received a bearer")
		}
		switch r.Method {
		case http.MethodPost:
			key, keyErr := model.ParseID(r.Header.Get("Idempotency-Key"))
			var cmd sessions.WorkCommand
			if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
				t.Fatal(err)
			}
			if r.URL.Path != "/v1/m/sessions/work-items" || r.URL.Query().Get("mode") != "apply" || keyErr != nil || key.String() != r.Header.Get("Idempotency-Key") ||
				cmd.Command != "item.create" || cmd.WorkspaceID != workspace || cmd.WorkKind != "message" || cmd.OwnerKind != "session" || cmd.OwnerRef != recipient ||
				cmd.ProvenanceKind != "mcp" || cmd.ProvenanceRef != sender || cmd.Title != "Review reply" || cmd.BriefMD != "Please review the change." || cmd.Priority != "p2" ||
				len(cmd.Acceptance) != 1 || !cmd.Acceptance[0].Required {
				t.Error("peer send did not map to the pinned work create envelope")
			}
			_, _ = w.Write([]byte(`{"result_id":"fixture-message","state":"draft"}`))
		case http.MethodGet:
			q := r.URL.Query()
			if r.URL.Path != "/v1/m/sessions/work-items" || q.Get("owner_kind") != "session" || q.Get("owner_ref") != sender || q.Get("limit") != "17" || q.Get("cursor") != "next" {
				t.Error("peer inbox lost the server-owned recipient or pagination")
			}
			_, _ = w.Write([]byte(`{"items":[]}`))
		default:
			t.Error("peer tool reached an unrelated work method")
		}
	}}
	request := func(token, method string, params any) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "peer-proof", "method": method, "params": params})
		r := httptest.NewRequest(http.MethodPost, "/session/mcp", strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	catalog := request(bearer, "tools/list", map[string]any{})
	if catalog.Code != http.StatusOK || !strings.Contains(catalog.Body.String(), "olivares_peer_send") || !strings.Contains(catalog.Body.String(), "olivares_peer_inbox") || strings.Contains(catalog.Body.String(), "olivares_session_") {
		t.Fatalf("peer tools are absent or K3 is exposed: %d %s", catalog.Code, catalog.Body.String())
	}
	send := map[string]any{"to_sid": recipient, "title": "Review reply", "brief_md": "Please review the change.", "idempotency_key": "reply-1"}
	if w := request(bearer, "tools/call", map[string]any{"name": "olivares_peer_send", "arguments": send}); w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"isError":true`) || calls != 1 {
		t.Fatalf("peer send = %d %s calls=%d", w.Code, w.Body.String(), calls)
	}
	inbox := map[string]any{"limit": 17, "cursor": "next"}
	if w := request(bearer, "tools/call", map[string]any{"name": "olivares_peer_inbox", "arguments": inbox}); w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"isError":true`) || calls != 2 {
		t.Fatalf("peer inbox = %d %s calls=%d", w.Code, w.Body.String(), calls)
	}
	for _, forged := range []map[string]any{{"owner_ref": recipient}, {"filters": map[string]string{"owner_ref": recipient}}} {
		if w := request(bearer, "tools/call", map[string]any{"name": "olivares_peer_inbox", "arguments": forged}); !strings.Contains(w.Body.String(), `"error"`) || calls != 2 {
			t.Fatal("caller could override the inbox recipient")
		}
	}
	send["provenance_ref"] = recipient
	if w := request(bearer, "tools/call", map[string]any{"name": "olivares_peer_send", "arguments": send}); !strings.Contains(w.Body.String(), `"error"`) || calls != 2 {
		t.Fatal("caller could forge the sender")
	}
	delete(send, "provenance_ref")
	send["to_sid"] = model.NewID().String()
	if w := request(bearer, "tools/call", map[string]any{"name": "olivares_peer_send", "arguments": send}); !strings.Contains(w.Body.String(), `"error"`) || calls != 2 {
		t.Fatal("bare UUID was accepted as a canonical session identity")
	}
	send["to_sid"] = recipient // Viewer refusal must be independent of validation.
	view := request(readonly, "tools/list", map[string]any{})
	if !strings.Contains(view.Body.String(), "olivares_peer_inbox") || strings.Contains(view.Body.String(), "olivares_peer_send") {
		t.Fatal("viewer peer catalogue lost its read-only ceiling")
	}
	if w := request(readonly, "tools/call", map[string]any{"name": "olivares_peer_send", "arguments": send}); !strings.Contains(w.Body.String(), `"error"`) || calls != 2 {
		t.Fatal("viewer reached peer mutation")
	}
}
