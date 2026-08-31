// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
)

func newAccessReviewSpoolHarness(
	t *testing.T,
) (*harness, string, model.TenantID, string) {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	dsn := filepath.Join(tmp, "access-review.db")
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	tok := secure.NewSetupToken(filepath.Join(tmp, "setup.token"))
	plaintext, _, err := tok.Ensure()
	if err != nil {
		t.Fatal(err)
	}

	// Provision the fixture before enabling the deliberately tiny degrade
	// budget. Setup, tenant creation and step-up are lifecycle mutations that
	// now correctly require durable audit evidence themselves; starving the
	// ledger before them would test setup refusal rather than export refusal.
	bootstrap, err := sqlstore.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: dsn, Debug: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.System(ctx, func(sys store.SystemScope) error {
		_, e := sys.EnsureSystemTenant(ctx)
		return e
	}); err != nil {
		_ = bootstrap.Close()
		t.Fatal(err)
	}
	bootstrapAuth := auth.NewAuthenticator(bootstrap, nil)
	bootstrapServer, err := api.New(api.Options{
		Store: bootstrap, Authenticator: bootstrapAuth,
		Authorizer: auth.NewAuthorizer(nil), Signer: signer,
		SetupToken: tok, Version: "test",
	})
	if err != nil {
		_ = bootstrap.Close()
		t.Fatal(err)
	}
	bootstrapHarness := &harness{
		t: t, srv: bootstrapServer, st: bootstrap, authr: bootstrapAuth,
		signer: signer, setupTok: plaintext, setupTokFile: tok,
	}
	admin := bootstrapHarness.adminLogin()
	tenant := bootstrapHarness.createOrg(admin, "acme")
	agentID := bootstrapHarness.mkAgent(admin, tenant, "bot")
	bootstrapHarness.elevate(admin)
	if err := bootstrap.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := sqlstore.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: dsn, Debug: true,
		AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolDegrade,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	authr := auth.NewAuthenticator(st, nil)
	srv, err := api.New(api.Options{
		Store: st, Authenticator: authr, Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: tok, Version: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{
		t: t, srv: srv, st: st, authr: authr, signer: signer,
		setupTok: plaintext, setupTokFile: tok,
	}, admin, tenant, agentID
}

// The access-review export is admin-tier + AAL3, produces a content-digested pack, and
// seals it in the audit ledger (fail-closed). It reports HOW each subject's access is
// conferred (rbac/scoped-grant/superadmin).
func TestAccessReviewExportSealed(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editorID, _ := h.authzMember(admin, "ed@acme.io", "editorpass1", auth.RoleEditor, tenant)
	h.authzMember(admin, "vw@acme.io", "viewerpass1", auth.RoleViewer, tenant)
	agentID := h.mkAgent(admin, tenant, "bot")

	body := map[string]any{"resource": map[string]any{"type": "agent", "id": agentID}}

	// AAL3 gate: a non-elevated superadmin (holds authz:admin) is asked to step up.
	if r := h.do("POST", "/access/v1/access-review/export", admin, body, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("non-elevated export = %d, want 403 step_up_required", r.code)
	}

	h.elevate(admin)
	r := h.do("POST", "/access/v1/access-review/export", admin, body, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("export = %d %s", r.code, r.raw)
	}
	integ, _ := r.body["integrity"].(map[string]any)
	if integ["sealed"] != true {
		t.Errorf("integrity.sealed = %v, want true", integ["sealed"])
	}
	if d, _ := integ["pack_sha256"].(string); d == "" {
		t.Error("integrity.pack_sha256 must be a non-empty digest")
	}
	if seq, _ := integ["audit_seq"].(float64); seq <= 0 {
		t.Errorf("integrity.audit_seq = %v, want > 0", integ["audit_seq"])
	}

	// The editor must appear with agent:write conferred by RBAC.
	entries, _ := r.body["entries"].([]any)
	foundEditorWrite := false
	for _, e := range entries {
		m, _ := e.(map[string]any)
		subj, _ := m["subject"].(map[string]any)
		if subj["id"] == editorID && m["permission"] == "agent:write" {
			foundEditorWrite = true
			if m["via"] != "rbac" {
				t.Errorf("editor agent:write via = %v, want rbac", m["via"])
			}
		}
	}
	if !foundEditorWrite {
		t.Errorf("export entries must include editor agent:write; entries = %v", entries)
	}

	// The export was sealed in the ledger: an access_review.export event exists.
	al := h.do("GET", "/v1/audit", admin, nil, tenantHdr(tenant))
	sealed := false
	for _, it := range al.body["items"].([]any) {
		if m, _ := it.(map[string]any); m["action"] == "access_review.export" {
			sealed = true
		}
	}
	if !sealed {
		t.Error("an access_review.export event must be recorded in the audit ledger")
	}
}

func TestAccessReviewExportRefusesDegradeDrop(t *testing.T) {
	h, admin, tenant, agentID := newAccessReviewSpoolHarness(t)

	r := h.do("POST", "/access/v1/access-review/export", admin,
		map[string]any{"resource": map[string]any{"type": "agent", "id": agentID}}, tenantHdr(tenant))
	if r.code != http.StatusServiceUnavailable {
		t.Fatalf("export = %d %s, want 503", r.code, r.raw)
	}
	errBody, _ := r.body["error"].(map[string]any)
	if errBody["code"] != "audit_spool_full" {
		t.Fatalf("error body = %v, want code audit_spool_full", errBody)
	}
	// The message used to be asserted as "internal error", which was pinning the
	// shape of the day rather than a requirement: this refusal is DELIBERATE and
	// there is nothing internal about it. A full audit spool refusing writes so it
	// cannot drop evidence is exactly the kind of thing this product exists to say
	// out loud, and an operator who is told "internal error" cannot act on it.
	msg, _ := errBody["message"].(string)
	if msg == "internal error" || !strings.Contains(msg, "audit spool") {
		t.Fatalf("a deliberate deny-closed refusal must name itself, got %q", msg)
	}
	if strings.Contains(r.raw, `"sealed":true`) {
		t.Fatalf("refused export falsely claims sealed: %s", r.raw)
	}
}

// The export is denied to a principal lacking authz:admin (a viewer), even elevated.
func TestAccessReviewExportRequiresAdmin(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, viewerTok := h.authzMember(admin, "vw@acme.io", "viewerpass1", auth.RoleViewer, tenant)
	agentID := h.mkAgent(admin, tenant, "bot")
	h.elevate(viewerTok)

	body := map[string]any{"resource": map[string]any{"type": "agent", "id": agentID}}
	if r := h.do("POST", "/access/v1/access-review/export", viewerTok, body, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("viewer export = %d, want 403 (authz:admin)", r.code)
	}

	// A missing resource is a 404 (the review target must exist).
	h.elevate(admin)
	miss := map[string]any{"resource": map[string]any{"type": "agent", "id": "00000000-0000-0000-0000-000000000000"}}
	if r := h.do("POST", "/access/v1/access-review/export", admin, miss, tenantHdr(tenant)); r.code != http.StatusBadRequest && r.code != http.StatusNotFound {
		t.Errorf("export of a missing/zero resource = %d, want 400/404", r.code)
	}
}
