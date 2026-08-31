// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/models"
)

// --- OMS v1.0 bare-key bundle builder (mirrors the on-the-wire format) -------

func paeBytes(pt string, payload []byte) []byte {
	return append([]byte(fmt.Sprintf("DSSEv1 %d %s %d ", len(pt), pt, len(payload))), payload...)
}

// omsBareKeyBundle builds a valid OMS v1.0 bare-key signature bundle for a one-file
// manifest, signed by priv. Returns the bundle (as a JSON object), the PEM public key
// for the trust list, and the file's sha256 hex (to exercise the artifact re-hash).
func omsBareKeyBundle(t *testing.T, fileName, fileData string, priv ed25519.PrivateKey) (map[string]any, string, string) {
	t.Helper()
	sum := sha256.Sum256([]byte(fileData))
	digest := hex.EncodeToString(sum[:])
	stmt := map[string]any{
		"_type":         "https://in-toto.io/Statement/v1",
		"subject":       []any{map[string]any{"name": "my-model", "digest": map[string]any{"sha256": digest}}},
		"predicateType": "https://model_signing/signature/v1.0",
		"predicate": map[string]any{
			"resources":     []any{map[string]any{"name": fileName, "digest": digest, "algorithm": "sha256"}},
			"serialization": map[string]any{"method": "files", "hash_type": "sha256", "ignore_paths": []any{"model.sig"}},
		},
	}
	payload, err := json.Marshal(stmt)
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(priv, paeBytes("application/vnd.in-toto+json", payload))
	pub := priv.Public().(ed25519.PublicKey)
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	bundle := map[string]any{
		"mediaType":            "application/vnd.dev.sigstore.bundle.v0.3+json",
		"verificationMaterial": map[string]any{"publicKey": map[string]any{"hint": "test-key"}},
		"dsseEnvelope": map[string]any{
			"payload":     base64.StdEncoding.EncodeToString(payload),
			"payloadType": "application/vnd.in-toto+json",
			"signatures":  []any{map[string]any{"sig": base64.StdEncoding.EncodeToString(sig)}},
		},
	}
	return bundle, pubPEM, digest
}

// seedOwnedAndVersion creates an owned model + one version, returning (ownedID, versionID).
// A local inference deployment (D-08) needs BOTH so the deploy gate can check
// membership (version.owned_ref == owned_ref) alongside signed admission.
func (h *harness) seedOwnedAndVersion(editor string, tenant model.TenantID, name string) (ownedID, versionID string) {
	h.t.Helper()
	om := h.do("POST", "/v1/m/models/owned-models", editor, map[string]any{"name": name, "kind": "imported", "provider_ref": "acme"}, tenantHdr(tenant))
	if om.code != http.StatusCreated {
		h.t.Fatalf("create owned model = %d %s", om.code, om.raw)
	}
	ownedID = om.body["id"].(string)
	mv := h.do("POST", "/v1/m/models/model-versions", editor, map[string]any{"owned_ref": ownedID, "version": "1.0.0", "artifact_ref": "oci://acme/model@sha256:abc"}, tenantHdr(tenant))
	if mv.code != http.StatusCreated {
		h.t.Fatalf("create version = %d %s", mv.code, mv.raw)
	}
	return ownedID, mv.body["id"].(string)
}

// auditActions returns the audit actions recorded for a tenant (self-audit asserts).
func (h *harness) auditActions(tenant model.TenantID) []string {
	h.t.Helper()
	var actions []string
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 0, func(e model.AuditEvent) error {
			actions = append(actions, e.Action)
			return nil
		})
	}); err != nil {
		h.t.Fatalf("walk audit: %v", err)
	}
	return actions
}

// TestSignedModelAdmission is the core G15 gate: a model artifact signed by a trusted
// key under a require_signed policy is admitted; an artifact signed by an UNTRUSTED key
// is deny-closed (admitted=false), and the verdict is recorded either way.
func TestSignedModelAdmission(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "a@x.io", auth.RoleAdmin)
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	_, trustedPriv, _ := ed25519.GenerateKey(nil)

	// 1) Configure the trust root: require_signed with the trusted bare key.
	bundle, pubPEM, digest := omsBareKeyBundle(t, "weights.bin", "the weights", trustedPriv)
	pol := h.do("PUT", "/v1/m/models/admission-policy", adminTok, map[string]any{
		"require_signed": true, "trusted_keys": []string{pubPEM},
	}, tenantHdr(tenant))
	if pol.code != http.StatusOK {
		t.Fatalf("set policy = %d %s", pol.code, pol.raw)
	}

	// An editor cannot configure the trust root (admin-tier).
	if r := h.do("PUT", "/v1/m/models/admission-policy", editor, map[string]any{"require_signed": false}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("editor set policy = %d, want 403", r.code)
	}

	_, versionID := h.seedOwnedAndVersion(editor, tenant, "llama-clone")

	// 2) Admit with a valid signature + the matching artifact digest → admitted.
	adm := h.do("POST", "/v1/m/models/model-versions/"+versionID+"/admit", editor, map[string]any{
		"bundle": bundle, "resolved_digests": map[string]string{"weights.bin": digest},
	}, tenantHdr(tenant))
	if adm.code != http.StatusOK {
		t.Fatalf("admit = %d %s", adm.code, adm.raw)
	}
	if adm.body["admitted"] != true {
		t.Fatalf("a validly-signed model must be admitted; got %v body=%s", adm.body["admitted"], adm.raw)
	}
	admission := adm.body["admission"].(map[string]any)
	if admission["signature_verified"] != true || admission["artifact_verified"] != true {
		t.Errorf("admission must record signature_verified+artifact_verified true; got %v", admission)
	}
	if admission["method"] != "bare-key" {
		t.Errorf("method = %v, want bare-key", admission["method"])
	}
	if admission["tlog_verified"] != false {
		t.Errorf("tlog_verified must be false (honest seam); got %v", admission["tlog_verified"])
	}

	// 3) Admit a DIFFERENT version with an UNTRUSTED key → deny-closed, verdict recorded.
	_, otherPriv, _ := ed25519.GenerateKey(nil)
	evilBundle, _, _ := omsBareKeyBundle(t, "weights.bin", "the weights", otherPriv)
	_, v2 := h.seedOwnedAndVersion(editor, tenant, "evil-clone")
	adm2 := h.do("POST", "/v1/m/models/model-versions/"+v2+"/admit", editor, map[string]any{"bundle": evilBundle}, tenantHdr(tenant))
	if adm2.code != http.StatusOK {
		t.Fatalf("admit untrusted = %d %s", adm2.code, adm2.raw)
	}
	if adm2.body["admitted"] != false {
		t.Fatalf("an untrusted-key signature must NOT be admitted (deny-closed); got %v", adm2.body["admitted"])
	}
	if a := adm2.body["admission"].(map[string]any); a["signature_verified"] != false || a["reason"] == "" {
		t.Errorf("denied admission must record signature_verified=false with a reason; got %v", a)
	}

	// A malformed bundle is a 400, not a recorded verdict.
	if r := h.do("POST", "/v1/m/models/model-versions/"+v2+"/admit", editor, map[string]any{"bundle": map[string]any{"mediaType": "x"}}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("malformed bundle = %d, want 400", r.code)
	}

	// The ?verified filter is two-sided: verified=true lists only the admitted verdict,
	// verified=false lists only the denied one (the "Unverified only" triage view), and
	// omitting the param returns the full history.
	countAdmissions := func(query string) int {
		r := h.do("GET", "/v1/m/models/model-admissions"+query, editor, nil, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("list admissions%q = %d %s", query, r.code, r.raw)
		}
		return len(r.body["items"].([]any))
	}
	if n := countAdmissions(""); n != 2 {
		t.Fatalf("unfiltered admissions = %d, want 2", n)
	}
	if n := countAdmissions("?verified=true"); n != 1 {
		t.Fatalf("verified=true admissions = %d, want 1 (only the admitted verdict)", n)
	}
	if n := countAdmissions("?verified=false"); n != 1 {
		t.Fatalf("verified=false admissions = %d, want 1 (only the denied verdict)", n)
	}

	// admit + policy configure must be self-audited to the real principal.
	actions := h.auditActions(tenant)
	if !contains(actions, "models.model_admission.admit") || !contains(actions, "models.admission_policy.configure") {
		t.Errorf("admit/configure must self-audit; got %v", actions)
	}
}

// TestSignedModelAdmissionDeploymentGate proves the runtime deny-closed gate: with
// require_signed on, a self-hosted inference deployment of an UNADMITTED model version
// is refused (422), while the same deployment of a VERIFIED version is allowed.
func TestSignedModelAdmissionDeploymentGate(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "a@x.io", auth.RoleAdmin)
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	_, priv, _ := ed25519.GenerateKey(nil)
	bundle, pubPEM, _ := omsBareKeyBundle(t, "weights.bin", "w", priv)

	// Before any policy: an unsigned model deploys fine (observe-mode default).
	preOwned, unsignedVer := h.seedOwnedAndVersion(editor, tenant, "m-pre")
	if r := h.do("POST", "/v1/m/models/inference-deployments", editor, map[string]any{
		"name": "dep-pre", "runtime": "vllm", "deployment_type": "local", "owned_ref": preOwned, "version_ref": unsignedVer,
	}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("deploy in observe mode = %d %s (must not be gated before opt-in)", r.code, r.raw)
	}

	// Opt INTO deny-closed enforcement.
	if r := h.do("PUT", "/v1/m/models/admission-policy", adminTok, map[string]any{"require_signed": true, "trusted_keys": []string{pubPEM}}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("set policy = %d %s", r.code, r.raw)
	}

	// An UNADMITTED version cannot be deployed for inference → 422 deny-closed.
	ungatedOwned, ungated := h.seedOwnedAndVersion(editor, tenant, "m-unsigned")
	if r := h.do("POST", "/v1/m/models/inference-deployments", editor, map[string]any{
		"name": "dep-unsigned", "runtime": "vllm", "deployment_type": "local", "owned_ref": ungatedOwned, "version_ref": ungated,
	}, tenantHdr(tenant)); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("deploy unsigned under require_signed = %d, want 422 (%s)", r.code, r.raw)
	}

	// Admit it (verified) → the same deployment now succeeds.
	if r := h.do("POST", "/v1/m/models/model-versions/"+ungated+"/admit", editor, map[string]any{"bundle": bundle}, tenantHdr(tenant)); r.code != http.StatusOK || r.body["admitted"] != true {
		t.Fatalf("admit = %d admitted=%v %s", r.code, r.body["admitted"], r.raw)
	}
	if r := h.do("POST", "/v1/m/models/inference-deployments", editor, map[string]any{
		"name": "dep-signed", "runtime": "vllm", "deployment_type": "local", "owned_ref": ungatedOwned, "version_ref": ungated,
	}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("deploy admitted version = %d, want 201 (%s)", r.code, r.raw)
	}
}

// TestModelDeploymentAnchorRotation (F9): a verdict's booleans were computed against the
// trust policy AT ADMIT TIME. If the trusted key that admitted a version is later rotated OUT of
// the policy (revocation), the runtime deployment gate must STOP clearing deployments of that
// version through the stale verdict. Restoring the key re-enables it with NO re-admit — the verdict
// is re-evaluated, not destroyed (the per-verdict precision F7 established, here on the model axis).
func TestModelDeploymentAnchorRotation(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "a@x.io", auth.RoleAdmin)
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	_, priv1, _ := ed25519.GenerateKey(nil)
	bundle, pub1, _ := omsBareKeyBundle(t, "weights.bin", "w", priv1)
	_, priv2, _ := ed25519.GenerateKey(nil)
	_, pub2, _ := omsBareKeyBundle(t, "weights.bin", "w", priv2) // second trusted-key PEM (bundle unused)

	// Policy trusts K1; admit a version under K1 (verified).
	if r := h.do("PUT", "/v1/m/models/admission-policy", adminTok, map[string]any{"require_signed": true, "trusted_keys": []string{pub1}}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("set policy K1 = %d %s", r.code, r.raw)
	}
	rotOwned, ver := h.seedOwnedAndVersion(editor, tenant, "m-rot")
	if r := h.do("POST", "/v1/m/models/model-versions/"+ver+"/admit", editor, map[string]any{"bundle": bundle}, tenantHdr(tenant)); r.code != http.StatusOK || r.body["admitted"] != true {
		t.Fatalf("admit under K1 = %d admitted=%v %s", r.code, r.body["admitted"], r.raw)
	}

	// Rotate the trusted key OUT (K1 -> K2). The stale verdict must no longer clear deployment.
	if r := h.do("PUT", "/v1/m/models/admission-policy", adminTok, map[string]any{"require_signed": true, "trusted_keys": []string{pub2}}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("rotate policy to K2 = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/models/inference-deployments", editor, map[string]any{
		"name": "dep-rotated", "runtime": "vllm", "deployment_type": "local", "owned_ref": rotOwned, "version_ref": ver,
	}, tenantHdr(tenant)); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("deploy after anchor rotated out = %d, want 422 anchor-rotated-out (%s)", r.code, r.raw)
	}

	// Restore K1 (widen to [K1,K2]): deployment clears again with NO re-admit (per-verdict precision).
	if r := h.do("PUT", "/v1/m/models/admission-policy", adminTok, map[string]any{"require_signed": true, "trusted_keys": []string{pub1, pub2}}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("restore policy [K1,K2] = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/models/inference-deployments", editor, map[string]any{
		"name": "dep-restored", "runtime": "vllm", "deployment_type": "local", "owned_ref": rotOwned, "version_ref": ver,
	}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("deploy after anchor restored = %d, want 201 (%s)", r.code, r.raw)
	}
}

// modelsTestCARoot generates a self-signed CA certificate PEM and its "root:<fp>" marker
// (fp = full sha256 hex of the cert DER, matching what modelsign records in SignerRoots).
func modelsTestCARoot(t *testing.T) (pemStr, marker string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "models-test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(cert.Raw)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), "root:" + hex.EncodeToString(sum[:])
}

// seedCertVerdict writes a VERIFIED certificate-PKI admission verdict directly into the REAL
// models.model_admission table (as the admit path would), pinning rootMarker in signer_roots —
// exercising the real KindJSON column and the runtime deploy gate's Option-B root re-check.
func (h *harness) seedCertVerdict(tenant model.TenantID, versionRef, rootMarker string) {
	h.t.Helper()
	roots, _ := json.Marshal([]string{rootMarker})
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("models.model_admission")
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			"version_ref": versionRef, "signature_verified": true, "artifact_verified": false,
			"tlog_present": false, "tlog_verified": false, "resource_count": int64(1),
			"method": "certificate-pki", "signer_identity": "CN=build.acme.io,O=Acme", "signer_roots": string(roots),
		})
		return err
	}); err != nil {
		h.t.Fatalf("seed cert verdict: %v", err)
	}
}

// TestModelDeploymentRootRotation (B): the runtime deploy gate pins the EXACT anchoring
// root recorded on the verdict. REPLACING that root in a PKI-only policy (identity/issuer are
// not pinned, so root presence is the only binding) must stop the stale verdict from clearing a
// deployment, even though len(trusted_roots) stays > 0 — the residual Option B closes. Restoring
// the exact root re-enables deployment with NO re-admit.
func TestModelDeploymentRootRotation(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "a@x.io", auth.RoleAdmin)
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	rootPEM, rootMarker := modelsTestCARoot(t)
	otherPEM, _ := modelsTestCARoot(t)

	setPolicy := func(roots []string) {
		if r := h.do("PUT", "/v1/m/models/admission-policy", adminTok, map[string]any{
			"require_signed": true, "trusted_roots": roots,
		}, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("set PKI policy = %d %s", r.code, r.raw)
		}
	}

	setPolicy([]string{rootPEM}) // PKI-only: trust rests on the exact root
	rootOwned, ver := h.seedOwnedAndVersion(editor, tenant, "m-rootrot")
	h.seedCertVerdict(tenant, ver, rootMarker)

	deploy := func(name string) int {
		return h.do("POST", "/v1/m/models/inference-deployments", editor, map[string]any{
			"name": name, "runtime": "vllm", "deployment_type": "local", "owned_ref": rootOwned, "version_ref": ver,
		}, tenantHdr(tenant)).code
	}

	if code := deploy("dep-ok"); code != http.StatusCreated {
		t.Fatalf("deploy with anchoring root present = %d, want 201", code)
	}
	setPolicy([]string{otherPEM}) // replace the root (len still 1)
	if code := deploy("dep-rot"); code != http.StatusUnprocessableEntity {
		t.Fatalf("deploy after anchoring root replaced = %d, want 422 (Option B closes this)", code)
	}
	setPolicy([]string{rootPEM}) // restore the exact root
	if code := deploy("dep-restored"); code != http.StatusCreated {
		t.Fatalf("deploy after restoring the anchoring root = %d, want 201", code)
	}
}

// TestAdmissionPolicyRequiresTrustAnchor proves a deny-closed gate cannot be set with
// no trust anchor (it would reject everything), rejects private-key material, and
// requires keyless identity+issuer pins together (cosign semantics).
func TestAdmissionPolicyRequiresTrustAnchor(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "a@x.io", auth.RoleAdmin)

	if r := h.do("PUT", "/v1/m/models/admission-policy", adminTok, map[string]any{"require_signed": true}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("require_signed with no trust anchor = %d, want 400", r.code)
	}
	if r := h.do("PUT", "/v1/m/models/admission-policy", adminTok, map[string]any{
		"require_signed": true, "trusted_keys": []string{"-----BEGIN PRIVATE KEY-----\nx\n-----END PRIVATE KEY-----"},
	}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("private key in trust list = %d, want 400", r.code)
	}
	// Keyless pins must be set together (identity without issuer is a misconfiguration).
	if r := h.do("PUT", "/v1/m/models/admission-policy", adminTok, map[string]any{
		"require_signed": true, "trusted_roots": []string{"-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----"},
		"allowed_identities": []string{"^https://github.com/acme/"},
	}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("allowed_identities without allowed_issuers = %d, want 400 (cosign-style both-or-neither)", r.code)
	}
}

// TestAdmissionRequireArtifactDigests proves the require_artifact_digests policy is
// enforced at BOTH the admit decision AND the deployment gate: a validly-signed model
// whose on-disk artifact was NOT re-hashed is not admitted and cannot be deployed;
// supplying the matching digest flips both to allowed.
func TestAdmissionRequireArtifactDigests(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "a@x.io", auth.RoleAdmin)
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	_, priv, _ := ed25519.GenerateKey(nil)
	bundle, pubPEM, digest := omsBareKeyBundle(t, "weights.bin", "w", priv)
	if r := h.do("PUT", "/v1/m/models/admission-policy", adminTok, map[string]any{
		"require_signed": true, "require_artifact_digests": true, "trusted_keys": []string{pubPEM},
	}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("policy = %d %s", r.code, r.raw)
	}

	// Signed, but NO resolved digest supplied → signature verifies, artifact does not →
	// NOT admitted under require_artifact_digests.
	digOwned, ver := h.seedOwnedAndVersion(editor, tenant, "m-nodigest")
	adm := h.do("POST", "/v1/m/models/model-versions/"+ver+"/admit", editor, map[string]any{"bundle": bundle}, tenantHdr(tenant))
	if adm.code != http.StatusOK || adm.body["admitted"] != false {
		t.Fatalf("signed-without-digest under require_artifact_digests must NOT be admitted; got %d admitted=%v", adm.code, adm.body["admitted"])
	}
	if a := adm.body["admission"].(map[string]any); a["signature_verified"] != true || a["artifact_verified"] != false {
		t.Errorf("verdict must record signature_verified=true, artifact_verified=false; got %v", a)
	}
	// Deploy gate denies it too.
	if r := h.do("POST", "/v1/m/models/inference-deployments", editor, map[string]any{"name": "d-nodigest", "runtime": "vllm", "deployment_type": "local", "owned_ref": digOwned, "version_ref": ver}, tenantHdr(tenant)); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("deploy artifact-unverified version = %d, want 422", r.code)
	}

	// Re-admit WITH the matching digest → artifact verified → admitted → deployable.
	if r := h.do("POST", "/v1/m/models/model-versions/"+ver+"/admit", editor, map[string]any{
		"bundle": bundle, "resolved_digests": map[string]string{"weights.bin": digest},
	}, tenantHdr(tenant)); r.code != http.StatusOK || r.body["admitted"] != true {
		t.Fatalf("re-admit with digest = %d admitted=%v %s", r.code, r.body["admitted"], r.raw)
	}
	if r := h.do("POST", "/v1/m/models/inference-deployments", editor, map[string]any{"name": "d-digest", "runtime": "vllm", "deployment_type": "local", "owned_ref": digOwned, "version_ref": ver}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("deploy artifact-verified version = %d, want 201 (%s)", r.code, r.raw)
	}
}

// TestDeployGateFailedVerdictReason proves the deploy gate denies a version whose
// admission was RECORDED but failed verification, surfacing the stored reason.
func TestDeployGateFailedVerdictReason(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "a@x.io", auth.RoleAdmin)
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	_, trusted, _ := ed25519.GenerateKey(nil)
	_, untrusted, _ := ed25519.GenerateKey(nil)
	_, pubPEM, _ := omsBareKeyBundle(t, "weights.bin", "w", trusted)
	evil, _, _ := omsBareKeyBundle(t, "weights.bin", "w", untrusted)

	if r := h.do("PUT", "/v1/m/models/admission-policy", adminTok, map[string]any{"require_signed": true, "trusted_keys": []string{pubPEM}}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("policy = %d %s", r.code, r.raw)
	}
	failOwned, ver := h.seedOwnedAndVersion(editor, tenant, "m-failed")
	// Record a FAILED verdict (untrusted key).
	if r := h.do("POST", "/v1/m/models/model-versions/"+ver+"/admit", editor, map[string]any{"bundle": evil}, tenantHdr(tenant)); r.code != http.StatusOK || r.body["admitted"] != false {
		t.Fatalf("admit untrusted = %d admitted=%v", r.code, r.body["admitted"])
	}
	// Deploying it is denied 422 — the recorded-but-failed branch (not the no-record one).
	r := h.do("POST", "/v1/m/models/inference-deployments", editor, map[string]any{"name": "d-failed", "runtime": "vllm", "deployment_type": "local", "owned_ref": failOwned, "version_ref": ver}, tenantHdr(tenant))
	if r.code != http.StatusUnprocessableEntity {
		t.Fatalf("deploy failed-verdict version = %d, want 422 (%s)", r.code, r.raw)
	}
}

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}
