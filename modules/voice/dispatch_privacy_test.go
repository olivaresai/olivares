// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package voice

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestOpenConnectionCredentialIsNotLedgerEvidence(t *testing.T) {
	const secret = "fixture-ephemeral-credential"
	const bundle = `{"session_id":"provider-session","credential":"fixture-ephemeral-credential","transport":"webrtc"}`
	expected := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(bundle)))
	h, _ := newHarness(t, WithApprovalGate(fakeGate{status: StatusApproved}), WithDispatcher(fakeDispatcher{ref: bundle}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "voice-privacy")
	operator := h.roleToken(admin, tenant, "operator@privacy.test", "admin")
	viewer := h.roleToken(admin, tenant, "viewer@privacy.test", "viewer")
	h.setPolicy(operator, tenant, "voice-agent", "realtime-model", "openai", 0)
	r := h.open(operator, tenant, "private-session", "voice-agent", "realtime-model", "openai", "appr-1")
	if r.code != http.StatusOK || r.body["dispatch_ref"] != bundle {
		t.Fatal("opening caller did not receive the unchanged connection bundle")
	}
	var historical model.Record
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(decisionKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Limit: 10})
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].String(colDispatchRef) != expected {
			t.Fatal("retained decision must contain credential-free dispatch evidence")
		}
		historical = rows[0]
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Replay the row format retained by older binaries without rewriting history.
	delete(historical, model.ColID)
	historical[colDispatchRef] = bundle
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(decisionKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), historical)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/m/voice/decisions", "/v1/m/voice/sessions/private-session/decisions"} {
		r := h.do("GET", path, viewer, nil, tenantHdr(tenant))
		if r.code != http.StatusOK || strings.Contains(r.raw, secret) {
			t.Fatalf("viewer ledger %s must return credential-free evidence, HTTP %d", path, r.code)
		}
		items, ok := r.body["items"].([]any)
		if !ok || len(items) != 2 {
			t.Fatalf("viewer ledger %s must retain both decisions", path)
		}
		for _, item := range items {
			if item.(map[string]any)["dispatch_ref"] != expected {
				t.Fatalf("viewer ledger %s must retain the bundle fingerprint", path)
			}
		}
	}
}
