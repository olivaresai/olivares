// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package catalog_test

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/catalog"
)

// newHarnessProvisioned wires the catalog with its signing key provisioned AT
// CONSTRUCTION (catalog.WithSigningKey — the production on-by-default boot path that
// boot.go/wire.go use), with NO runtime/config: the signer is set before Init, exactly
// as buildModules does. This is the end-to-end mirror of the shipped binary.
func newHarnessProvisioned(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	_, catPriv, _ := ed25519.GenerateKey(nil)
	cat := catalog.New(catalog.WithSigningKey(catPriv))
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, cat.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	cat.UseData(api.NewModuleData(st))

	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := tok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: tok, Version: "test", Modules: []api.Module{cat},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, srv: srv, st: st, cat: cat, setupTok: plaintext}
}

// TestProvisionedKeySignsAndPinsVerify proves the headline of part (a): with the key
// provisioned at boot, an approved entry is SIGNED and verifies in the PINNED branch
// (Verified=true), and a downgrade (the signature stripped at the store) is reported
// NOT verified — the posture a governed internal marketplace needs.
func TestProvisionedKeySignsAndPinsVerify(t *testing.T) {
	h := newHarnessProvisioned(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)

	// The boot-provisioned key reports signing enabled.
	if r := h.do("GET", "/v1/m/catalog/pubkey", editor, nil, tenantHdr(tenant)); r.code != http.StatusOK || r.body["signing_enabled"] != true {
		t.Fatalf("pubkey signing_enabled != true with a provisioned key: %d %v", r.code, r.body)
	}

	id := h.createApproved(editor, admin, tenant, "github", "1.0.0")

	// Pristine: signed + pinned verify.
	r := h.do("GET", "/v1/m/catalog/entries/"+id+"/verify", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["verified"] != true || r.body["signed"] != true || r.body["signature_ok"] != true {
		t.Fatalf("provisioned-key approved entry must verify PINNED (Verified=true): %v", r.body)
	}

	// Downgrade: strip the signature at the store (leaving a still-matching hash). With a
	// key configured this is NOT a legitimately-unsigned entry — it is a downgrade attack.
	ctx := context.Background()
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("catalog.entry")
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, model.ID(id))
		if err != nil {
			return err
		}
		rec["signature"] = ""
		rec["sig_alg"] = ""
		rec["signed_by"] = ""
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r = h.do("GET", "/v1/m/catalog/entries/"+id+"/verify", editor, nil, tenantHdr(tenant))
	if r.body["verified"] != false {
		t.Errorf("downgrade (stripped signature) not detected with a provisioned key: %v", r.body)
	}
}
