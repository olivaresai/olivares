// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

func TestSessionMCPPeerSendKeysAreBoundedAndScopedToSender(t *testing.T) {
	a, tenant, _, launcher, _, _ := workSessionEdgePrincipals(t)
	issuer := auth.NewSessionCredentials(a, func(context.Context, auth.SessionScope) error { return nil })
	workspace, recipient := model.NewID(), "osn_"+model.NewID().String()
	principal := func() auth.Principal {
		t.Helper()
		bearer, err := issuer.Mint(t.Context(), launcher, auth.SessionScope{
			TenantID: tenant, WorkspaceID: workspace, FolderRef: "fixture", SessionRef: "osn_" + model.NewID().String(),
			RunRef: model.NewID().String(), Fence: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		p, err := issuer.Authenticate(t.Context(), bearer)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	sender, other := principal(), principal()
	keyFor := func(p auth.Principal, key string) string {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"to_sid": recipient, "title": "Report", "brief_md": "Reply", "idempotency_key": key})
		r, err := sessionToolRequest(t.Context(), p, "olivares_peer_send", raw)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		derived := r.Header.Get("Idempotency-Key")
		if parsed, err := model.ParseID(derived); err != nil || parsed.String() != derived {
			t.Fatal("agent key did not reach work apply as a canonical UUID")
		}
		return derived
	}
	retryID := "peer-retry-1"
	first := keyFor(sender, retryID)
	if keyFor(sender, retryID) != first || keyFor(other, retryID) == first || keyFor(sender, retryID+"-next") == first {
		t.Fatal("peer retries lost stability or different sender/key pairs shared a receipt")
	}
	keyFor(sender, strings.Repeat("界", 256))
	keyFor(sender, "key\nwith\x00controls")
	for _, invalid := range []string{"", strings.Repeat("k", 257)} {
		raw, _ := json.Marshal(map[string]any{"to_sid": recipient, "title": "Report", "brief_md": "Reply", "idempotency_key": invalid})
		if _, err := sessionToolRequest(t.Context(), sender, "olivares_peer_send", raw); err == nil {
			t.Fatal("empty or oversized peer key was admitted")
		}
	}
}
