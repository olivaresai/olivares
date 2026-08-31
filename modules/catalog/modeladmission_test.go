// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package catalog_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/catalog"
)

// newHarnessWithModelStandIns builds a catalog plane that ALSO registers minimal
// stand-ins for the models module's admission policy + verdict entities, so the
// catalog's deny-closed kindModel approve overlay can be exercised without importing
// the models module (the same KIND+column loose-coupling it uses at runtime).
func newHarnessWithModelStandIns(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	cat := catalog.New()
	register := func(reg store.ExtensionRegistry) error {
		if err := cat.RegisterSchema(reg); err != nil {
			return err
		}
		if err := reg.Register(model.EntityDescriptor{
			Kind: "models.admission_policy", Table: "models_admission_policy",
			Fields: []model.FieldSpec{
				{Name: "policy_scope", Kind: model.KindText, Indexed: true},
				{Name: "require_signed", Kind: model.KindBool, Indexed: true},
				{Name: "require_artifact_digests", Kind: model.KindBool},
				// Trust-anchor columns the catalog reconstructs into a modelsign.TrustPolicy for
				// the approve-time anchor re-check (F9). Stored as text holding a JSON []string
				// (what parseJSONStrings reads via rec.String — same as production's KindJSON).
				{Name: "allowed_identities", Kind: model.KindText, Nullable: true},
				{Name: "allowed_issuers", Kind: model.KindText, Nullable: true},
				{Name: "trusted_keys", Kind: model.KindText, Nullable: true},
				{Name: "trusted_roots", Kind: model.KindText, Nullable: true},
			},
		}); err != nil {
			return err
		}
		return reg.Register(model.EntityDescriptor{
			Kind: "models.model_admission", Table: "models_model_admission",
			Fields: []model.FieldSpec{
				{Name: "version_ref", Kind: model.KindText, Indexed: true},
				{Name: "signature_verified", Kind: model.KindBool, Indexed: true},
				{Name: "artifact_verified", Kind: model.KindBool},
				{Name: "reason", Kind: model.KindText, Nullable: true},
				// Recorded signer anchor, re-checked against the current policy (F9 + B).
				{Name: "method", Kind: model.KindText, Nullable: true},
				{Name: "signer_identity", Kind: model.KindText, Nullable: true},
				{Name: "signer_issuer", Kind: model.KindText, Nullable: true},
				{Name: "signer_roots", Kind: model.KindText, Nullable: true},
			},
		})
	}
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, register)
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

// Keyless trust anchor used by the model-admission stand-in tests: a verified verdict is
// anchored to this identity/issuer under a present root, so the F9 approve-time anchor re-check
// passes until the identity is rotated OUT of the policy.
const (
	testModelIdentity    = "https://ci.acme.io/build/1"
	testModelIdentityPat = `^https://ci\.acme\.io/.*$`
	testModelIssuer      = "https://accounts.acme.io"
)

// newTestCARoot generates a self-signed CA certificate PEM and its "root:<fp>" marker
// (fp = full sha256 hex of the cert DER — exactly what modelsign records in
// Verdict.SignerRoots and re-checks in AnchorStillTrusted). A real, parseable cert is
// required: the anchor re-check parses each trusted_roots entry with
// x509.ParseCertificate, so a fake PEM would yield no trusted-root markers at all.
func newTestCARoot(t *testing.T) (pemStr, marker string) {
	t.Helper()
	pemStr, marker, err := genCARoot()
	if err != nil {
		t.Fatal(err)
	}
	return pemStr, marker
}

func genCARoot() (pemStr, marker string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return "", "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "catalog-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return "", "", err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(cert.Raw)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), "root:" + hex.EncodeToString(sum[:]), nil
}

// The default anchoring root for the stand-in tests (one real CA, generated once).
var testModelRoot, testModelRootMarker = mustGenCARoot()

func mustGenCARoot() (string, string) {
	p, m, err := genCARoot()
	if err != nil {
		panic(err)
	}
	return p, m
}

func jsonArr(ss []string) string { b, _ := json.Marshal(ss); return string(b) }

func (h *harness) seedAdmissionPolicy(tenant model.TenantID, requireSigned bool) {
	h.t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("models.admission_policy")
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			"policy_scope": "default", "require_signed": requireSigned, "require_artifact_digests": false,
			"trusted_roots": jsonArr([]string{testModelRoot}), "allowed_identities": jsonArr([]string{testModelIdentityPat}),
			"allowed_issuers": jsonArr([]string{testModelIssuer}), "trusted_keys": jsonArr([]string{}),
		})
		return err
	}); err != nil {
		h.t.Fatalf("seed policy: %v", err)
	}
}

// rotateModelPolicyIdentities replaces allowed_identities on the singleton policy, simulating an
// operator rotating the anchoring identity out of (or back into) the tenant's trust anchors.
func (h *harness) rotateModelPolicyIdentities(tenant model.TenantID, identityPats []string) {
	h.t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("models.admission_policy")
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Filters: []model.Filter{{Column: "policy_scope", Op: model.OpEq, Value: "default"}}, Limit: 1})
		if err != nil {
			return err
		}
		if len(recs) == 0 {
			return fmt.Errorf("no admission policy to rotate")
		}
		rec := recs[0]
		rec["allowed_identities"] = jsonArr(identityPats)
		_, err = repo.Update(context.Background(), rec)
		return err
	}); err != nil {
		h.t.Fatalf("rotate policy: %v", err)
	}
}

func (h *harness) seedModelAdmission(tenant model.TenantID, versionRef string, verified bool) {
	h.t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("models.model_admission")
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			"version_ref": versionRef, "signature_verified": verified, "artifact_verified": false, "reason": "test",
			"method": "sigstore-keyless", "signer_identity": testModelIdentity, "signer_issuer": testModelIssuer,
			"signer_roots": jsonArr([]string{testModelRootMarker}),
		})
		return err
	}); err != nil {
		h.t.Fatalf("seed admission: %v", err)
	}
}

// seedModelAdmissionNoRoot seeds a VERIFIED keyless verdict that recorded NO anchoring root —
// a legacy row from before root pinning. It must be deny-closed at approve.
func (h *harness) seedModelAdmissionNoRoot(tenant model.TenantID, versionRef string) {
	h.t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("models.model_admission")
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			"version_ref": versionRef, "signature_verified": true, "artifact_verified": false, "reason": "test",
			"method": "sigstore-keyless", "signer_identity": testModelIdentity, "signer_issuer": testModelIssuer,
		})
		return err
	}); err != nil {
		h.t.Fatalf("seed no-root admission: %v", err)
	}
}

// rotateModelPolicyRoots replaces trusted_roots on the singleton policy, simulating an operator
// rotating/replacing the anchoring CA root (the residual: a replacement keeps len(Roots) > 0).
func (h *harness) rotateModelPolicyRoots(tenant model.TenantID, roots []string) {
	h.t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("models.admission_policy")
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Filters: []model.Filter{{Column: "policy_scope", Op: model.OpEq, Value: "default"}}, Limit: 1})
		if err != nil {
			return err
		}
		if len(recs) == 0 {
			return fmt.Errorf("no admission policy to rotate")
		}
		rec := recs[0]
		rec["trusted_roots"] = jsonArr(roots)
		_, err = repo.Update(context.Background(), rec)
		return err
	}); err != nil {
		h.t.Fatalf("rotate policy roots: %v", err)
	}
}

func modelEntry(slug, versionRef string) map[string]any {
	return map[string]any{
		"kind": "model", "name": "Llama clone", "slug": slug, "version": "1.0.0",
		"summary": "An admitted self-hosted model", "owner_ref": "ml-team",
		"spec": map[string]any{"version_ref": versionRef, "artifact_ref": "oci://acme/m@sha256:abc"},
	}
}

// TestCatalogModelAdmissionGate is the XIV "admission gate in the catalog": with the
// tenant's signed-model-admission policy requiring signing, a MODEL entry can only be
// APPROVED when the model version it curates has a verified admission. Default off ⇒
// model entries approve freely (observe mode) and non-model entries are unaffected.
func TestCatalogModelAdmissionGate(t *testing.T) {
	h := newHarnessWithModelStandIns(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	// Observe mode (no policy): a model entry approves like any other kind.
	r := h.do("POST", "/v1/m/catalog/entries", editor, modelEntry("m-observe", "ver-x"), tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create model entry = %d %s (kindModel must be a valid kind)", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/catalog/entries/"+r.body["id"].(string)+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("approve model entry in observe mode = %d %s (must not be gated)", r.code, r.raw)
	}

	// Opt INTO deny-closed enforcement.
	h.seedAdmissionPolicy(tenant, true)

	// A model entry whose version has NO verified admission is refused at approval.
	r = h.do("POST", "/v1/m/catalog/entries", editor, modelEntry("m-unsigned", "ver-unsigned"), tenantHdr(tenant))
	id := r.body["id"].(string)
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("approve unverified model entry = %d, want 409 deny-closed (%s)", r.code, r.raw)
	}

	// A model entry whose version has a RECORDED-but-FAILED admission is refused
	// (the recorded-but-unverified deny branch, not the no-record one).
	h.seedModelAdmission(tenant, "ver-failed", false)
	r = h.do("POST", "/v1/m/catalog/entries", editor, modelEntry("m-failed", "ver-failed"), tenantHdr(tenant))
	if r := h.do("POST", "/v1/m/catalog/entries/"+r.body["id"].(string)+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("approve failed-verdict model entry = %d, want 409 (%s)", r.code, r.raw)
	}

	// A model entry whose version has a VERIFIED admission approves.
	h.seedModelAdmission(tenant, "ver-signed", true)
	r = h.do("POST", "/v1/m/catalog/entries", editor, modelEntry("m-signed", "ver-signed"), tenantHdr(tenant))
	id = r.body["id"].(string)
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("approve verified model entry = %d, want 200 (%s)", r.code, r.raw)
	}

	// A non-model entry (mcp) is unaffected by the policy.
	if h.createApproved(editor, admin, tenant, "gh-mcp", "1.0.0") == "" {
		t.Fatal("non-model entry must still approve under the policy")
	}

	// A model entry with NO version_ref in spec is refused (cannot identify the artifact).
	noVer := modelEntry("m-noref", "")
	noVer["spec"] = map[string]any{"artifact_ref": "oci://x"}
	r = h.do("POST", "/v1/m/catalog/entries", editor, noVer, tenantHdr(tenant))
	if r2 := h.do("POST", "/v1/m/catalog/entries/"+r.body["id"].(string)+"/approve", admin, nil, tenantHdr(tenant)); r2.code != http.StatusConflict {
		t.Fatalf("approve model entry without version_ref = %d, want 409 (%s)", r2.code, r2.raw)
	}
}

// TestCatalogModelAdmissionAnchorRotation (F9): a model admission verdict's booleans were
// computed against the trust policy AT ADMIT TIME. If the anchor that verified it (the keyless
// identity here) is later rotated OUT of the tenant's models.admission_policy, approving the model
// entry into the catalog must be refused — a rotated-out anchor cannot keep certifying through a
// stale verdict, the SAME revocation guard F7 gave the MCP/connector gates, now on the model axis.
// Restoring the anchor re-enables approval with NO re-admit (per-verdict precision).
func TestCatalogModelAdmissionAnchorRotation(t *testing.T) {
	h := newHarnessWithModelStandIns(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	h.seedAdmissionPolicy(tenant, true)           // keyless policy anchored on testModelIdentity
	h.seedModelAdmission(tenant, "ver-rot", true) // verified verdict anchored to that identity

	r := h.do("POST", "/v1/m/catalog/entries", editor, modelEntry("m-rot", "ver-rot"), tenantHdr(tenant))
	id := r.body["id"].(string)

	// Rotate the anchoring identity OUT of the policy: the stale verdict must no longer certify.
	h.rotateModelPolicyIdentities(tenant, []string{`^https://other\.example/.*$`})
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("approve after anchor rotated out = %d, want 409 anchor-rotated-out (%s)", r.code, r.raw)
	}

	// Restore the anchor: approval succeeds again with NO re-admit — the verdict is re-evaluated,
	// not destroyed (per-verdict precision, the chosen #20 design).
	h.rotateModelPolicyIdentities(tenant, []string{testModelIdentityPat})
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("approve after anchor restored = %d, want 200 (%s)", r.code, r.raw)
	}
}

// TestCatalogModelAdmissionSchemaDriftDenyClosed (F9): the catalog reads the models trust
// policy by column NAME (decoupled). If that contract drifts so the reconstructed policy is
// anchor-less or has an inconsistent keyless allow-list, the approve gate must DENY-CLOSED rather
// than skip a pin — fail-closed under schema drift (defense-in-depth flagged by the Codex review).
func TestCatalogModelAdmissionSchemaDriftDenyClosed(t *testing.T) {
	h := newHarnessWithModelStandIns(t)
	admin := h.adminLogin()

	seedRawPolicy := func(tenant model.TenantID, rec model.Record) {
		if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
			repo, err := sc.Ext("models.admission_policy")
			if err != nil {
				return err
			}
			_, err = repo.Create(context.Background(), rec)
			return err
		}); err != nil {
			t.Fatalf("seed raw policy: %v", err)
		}
	}
	approveVerified := func(tenant model.TenantID, editor, slug, ver string) int {
		h.seedModelAdmission(tenant, ver, true)
		r := h.do("POST", "/v1/m/catalog/entries", editor, modelEntry(slug, ver), tenantHdr(tenant))
		return h.do("POST", "/v1/m/catalog/entries/"+r.body["id"].(string)+"/approve", admin, nil, tenantHdr(tenant)).code
	}

	// (a) enforcing policy with NO readable trust anchor (roots/keys columns unreadable) → deny-closed.
	t1 := h.createOrg(admin, "acme-noanchor")
	ed1 := h.roleToken(admin, t1, "e1@x.io", auth.RoleEditor)
	seedRawPolicy(t1, model.Record{"policy_scope": "default", "require_signed": true, "require_artifact_digests": false})
	if code := approveVerified(t1, ed1, "m-noanchor", "ver-na"); code != http.StatusConflict {
		t.Fatalf("approve under anchor-less policy = %d, want 409 deny-closed", code)
	}

	// (b) enforcing policy with an INCONSISTENT keyless allow-list (identities set, issuers empty) → deny.
	t2 := h.createOrg(admin, "acme-halfkeyless")
	ed2 := h.roleToken(admin, t2, "e2@x.io", auth.RoleEditor)
	seedRawPolicy(t2, model.Record{
		"policy_scope": "default", "require_signed": true, "require_artifact_digests": false,
		"trusted_roots": jsonArr([]string{testModelRoot}), "allowed_identities": jsonArr([]string{testModelIdentityPat}),
		"allowed_issuers": jsonArr([]string{}),
	})
	if code := approveVerified(t2, ed2, "m-halfkeyless", "ver-hk"); code != http.StatusConflict {
		t.Fatalf("approve under inconsistent keyless allow-list = %d, want 409 deny-closed", code)
	}
}

// TestCatalogModelAdmissionRootRotation (B): a keyless verdict pins the EXACT anchoring root.
// REPLACING that root in the policy (drop the old, add a new one in one edit — len(trusted_roots)
// stays > 0, and the identity/issuer still match) must deny approval, closing the residual the
// old "a trusted root is still present" check left open. Restoring the root re-enables approval
// with NO re-admit (per-verdict precision).
func TestCatalogModelAdmissionRootRotation(t *testing.T) {
	h := newHarnessWithModelStandIns(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	h.seedAdmissionPolicy(tenant, true)            // keyless policy anchored on testModelRoot
	h.seedModelAdmission(tenant, "ver-root", true) // verdict pins testModelRootMarker

	r := h.do("POST", "/v1/m/catalog/entries", editor, modelEntry("m-root", "ver-root"), tenantHdr(tenant))
	id := r.body["id"].(string)

	// Replace the anchoring root with a DIFFERENT one (identity/issuer unchanged, len(Roots) still 1).
	// A refused approval (409) does not transition entry state, so the same entry stays approvable.
	otherRoot, _ := newTestCARoot(t)
	h.rotateModelPolicyRoots(tenant, []string{otherRoot})
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("approve after anchoring root replaced = %d, want 409 (Option B closes this) (%s)", r.code, r.raw)
	}

	// Restore the original root: approval succeeds now that the exact anchoring root is present again.
	h.rotateModelPolicyRoots(tenant, []string{testModelRoot})
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("approve after restoring the anchoring root = %d, want 200 (%s)", r.code, r.raw)
	}
}

// TestCatalogModelAdmissionLegacyNoRootDenyClosed (B): a VERIFIED keyless verdict admitted
// before root pinning recorded no anchoring root. It is deny-closed — never grandfathered — so the
// residual cannot survive on legacy rows; re-admission under the current policy is required.
func TestCatalogModelAdmissionLegacyNoRootDenyClosed(t *testing.T) {
	h := newHarnessWithModelStandIns(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	h.seedAdmissionPolicy(tenant, true)
	h.seedModelAdmissionNoRoot(tenant, "ver-legacy") // verified, but NO signer_roots

	r := h.do("POST", "/v1/m/catalog/entries", editor, modelEntry("m-legacy", "ver-legacy"), tenantHdr(tenant))
	if r := h.do("POST", "/v1/m/catalog/entries/"+r.body["id"].(string)+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("approve legacy no-root verdict = %d, want 409 deny-closed (%s)", r.code, r.raw)
	}
}
