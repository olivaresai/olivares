// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package catalog_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure/modelsign"
)

// connectorEntry returns a valid draft catalog entry body for a third-party
// connector plugin (S142, EXT-3). specDigest is the artifact_digest the entry
// claims to curate ("" omits it, for an entry that does not pin its artifact).
func connectorEntry(slug, version, specDigest string) map[string]any {
	spec := map[string]any{
		"release_ref":     "ghcr.io/acme/siem-forwarder:" + version,
		"publisher":       "acme-integrations",
		"descriptor_name": "siem-forwarder",
	}
	if specDigest != "" {
		spec["artifact_digest"] = specDigest
	}
	return map[string]any{
		"kind":      "connector",
		"name":      "Acme SIEM Forwarder",
		"slug":      slug,
		"version":   version,
		"summary":   "Verified third-party SIEM forwarder connector",
		"owner_ref": "acme-integrations",
		"spec":      spec,
	}
}

func (h *harness) createConnectorEntry(editor string, tenant model.TenantID, slug, specDigest string) string {
	h.t.Helper()
	r := h.do("POST", "/v1/m/catalog/entries", editor, connectorEntry(slug, "1.0.0", specDigest), tenantHdr(tenant))
	if r.code != http.StatusCreated {
		h.t.Fatalf("create connector entry = %d %s", r.code, r.raw)
	}
	return r.body["id"].(string)
}

// TestCatalogConnectorAdmissionGate: the S142 deny-closed gate — with the
// tenant's connector-entry admission policy requiring signing, a connector entry
// approves into the catalog (and thus lists as a verified connector) ONLY with a
// verified attestation verdict. Observe mode keeps the existing estate working.
func TestCatalogConnectorAdmissionGate(t *testing.T) {
	h := newHarness(t, false)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	att := newAttestation(t, modelsign.PredicateTypeSLSAProvenanceV1, fixtureDigest)

	// Kind 'connector' creates fine as a draft registry artifact.
	r := h.do("POST", "/v1/m/catalog/entries", editor, connectorEntry("create-check", "1.0.0", ""), tenantHdr(tenant))
	if r.code != http.StatusCreated || r.body["kind"] != "connector" || r.body["status"] != "draft" {
		t.Fatalf("create connector entry = %d %s", r.code, r.raw)
	}

	// Observe mode (no policy): a connector entry approves ungated.
	id := h.createConnectorEntry(editor, tenant, "observe", "")
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("approve in observe mode = %d %s", r.code, r.raw)
	}

	// The default policy endpoint is honest about observe mode.
	r = h.do("GET", "/v1/m/catalog/connector-admission/policy", admin, nil, tenantHdr(tenant))
	if r.body["configured"] != false || !strings.Contains(r.raw, "OBSERVE") {
		t.Fatalf("unconfigured policy must say so, got %s", r.raw)
	}

	// Opt INTO deny-closed enforcement with the attestation key as trust root.
	policy := map[string]any{"require_signed": true, "trusted_keys": []string{att.pubPEM}}
	if r := h.do("PUT", "/v1/m/catalog/connector-admission/policy", admin, policy, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("put policy = %d %s", r.code, r.raw)
	}

	// No verdict → approval refused deny-closed.
	gated := h.createConnectorEntry(editor, tenant, "gated", "")
	if r := h.do("POST", "/v1/m/catalog/entries/"+gated+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("approve without verdict = %d, want 409 (%s)", r.code, r.raw)
	}

	// Admit with a verified SLSA attestation bound to the expected digest.
	body := map[string]any{"bundle": json.RawMessage(att.bundle), "expected_digest": "sha256:" + fixtureDigest}
	r = h.do("POST", "/v1/m/catalog/entries/"+gated+"/admit", admin, body, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("admit = %d %s", r.code, r.raw)
	}
	if r.body["admitted"] != true || r.body["enforced"] != true {
		t.Fatalf("a verified attestation under an enforcing policy must admit, got %s", r.raw)
	}
	adm := r.body["admission"].(map[string]any)
	if adm["signature_verified"] != true || adm["artifact_verified"] != true || adm["predicate_type"] != modelsign.PredicateTypeSLSAProvenanceV1 {
		t.Fatalf("verdict fields wrong: %s", r.raw)
	}

	// Now the entry approves — frozen + hash-pinned (the certification record).
	r = h.do("POST", "/v1/m/catalog/entries/"+gated+"/approve", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("approve with verified verdict = %d %s", r.code, r.raw)
	}
	if r.body["status"] != "approved" || r.body["content_hash"] == nil || r.body["content_hash"] == "" {
		t.Fatalf("approved connector entry must be frozen + hashed, got %s", r.raw)
	}

	// Verdicts list and filter.
	if r := h.do("GET", "/v1/m/catalog/connector-admissions?verified=true", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK || !strings.Contains(r.raw, gated) {
		t.Errorf("the verified verdict must list, got %d %s", r.code, r.raw)
	}
}

// TestCatalogConnectorAdmissionRefusals: the deny paths — untrusted key,
// malformed bundle, non-existent entry, the admit-route kind dispatch, and the
// policy input guards.
func TestCatalogConnectorAdmissionRefusals(t *testing.T) {
	h := newHarness(t, false)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	att := newAttestation(t, modelsign.PredicateTypeSLSAProvenanceV1, fixtureDigest)
	policy := map[string]any{"require_signed": true, "trusted_keys": []string{att.pubPEM}}
	if r := h.do("PUT", "/v1/m/catalog/connector-admission/policy", admin, policy, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("put policy = %d %s", r.code, r.raw)
	}

	// A bundle signed by a key OUTSIDE the trust root: recorded, not admitted,
	// reason set, approval stays refused (a well-formed failure is a verdict, not
	// an error).
	untrusted := newAttestation(t, modelsign.PredicateTypeSLSAProvenanceV1, fixtureDigest)
	id := h.createConnectorEntry(editor, tenant, "untrusted-key", "")
	r := h.do("POST", "/v1/m/catalog/entries/"+id+"/admit", admin, map[string]any{"bundle": json.RawMessage(untrusted.bundle)}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["admitted"] != false {
		t.Fatalf("an untrusted-key attestation must record a non-admitted verdict, got %d %s", r.code, r.raw)
	}
	adm := r.body["admission"].(map[string]any)
	if adm["signature_verified"] != false || adm["reason"] == nil || adm["reason"] == "" {
		t.Fatalf("verdict must be unverified with a reason, got %s", r.raw)
	}
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("approve after failed admission = %d, want 409 (%s)", r.code, r.raw)
	}

	// A malformed bundle is a 400, never a recorded verdict.
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/admit", admin, map[string]any{"bundle": json.RawMessage(`{"not":"a bundle"}`)}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Errorf("malformed bundle = %d, want 400 (%s)", r.code, r.raw)
	}

	// A non-existent entry is a 404 from the dispatch.
	if r := h.do("POST", "/v1/m/catalog/entries/0f0e0d0c-0b0a-4090-8070-605040302010/admit", admin, map[string]any{"bundle": json.RawMessage(att.bundle)}, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Errorf("admit on non-existent entry = %d, want 404 (%s)", r.code, r.raw)
	}

	// Kind dispatch: an 'agent' entry has no attestation admission flow → 400.
	agent := map[string]any{"kind": "agent", "name": "A", "slug": "ag", "version": "1.0.0"}
	ra := h.do("POST", "/v1/m/catalog/entries", editor, agent, tenantHdr(tenant))
	if ra.code != http.StatusCreated {
		t.Fatalf("create agent entry = %d %s", ra.code, ra.raw)
	}
	if r := h.do("POST", "/v1/m/catalog/entries/"+ra.body["id"].(string)+"/admit", admin, map[string]any{"bundle": json.RawMessage(att.bundle)}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Errorf("admitting an agent entry = %d, want 400 (%s)", r.code, r.raw)
	}

	// Kind dispatch regression: the SAME route still serves the MCP flow
	// (with the MCP policy's own trust root — the two policies are independent).
	mcpPolicy := map[string]any{"require_signed": true, "trusted_keys": []string{att.pubPEM}}
	if r := h.do("PUT", "/v1/m/catalog/mcp-admission/policy", admin, mcpPolicy, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("put mcp policy = %d %s", r.code, r.raw)
	}
	mcpID := h.createMCPEntry(editor, tenant, "dispatch-mcp")
	r = h.do("POST", "/v1/m/catalog/entries/"+mcpID+"/admit", admin, map[string]any{"bundle": json.RawMessage(att.bundle), "expected_digest": fixtureDigest}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["admitted"] != true {
		t.Fatalf("mcp admit via the shared route = %d %s, want admitted", r.code, r.raw)
	}

	// Policy guards: a private key is never trust material; enforcing with no
	// anchor, or one-sided keyless pins, are rejected.
	privKey := map[string]any{"require_signed": false, "trusted_keys": []string{"-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----"}}
	if r := h.do("PUT", "/v1/m/catalog/connector-admission/policy", admin, privKey, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Errorf("a private key in trusted_keys = %d, want 400", r.code)
	}
	if r := h.do("PUT", "/v1/m/catalog/connector-admission/policy", admin, map[string]any{"require_signed": true}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Errorf("an enforcing policy without anchors = %d, want 400", r.code)
	}
	oneSided := map[string]any{"require_signed": false, "allowed_identities": []string{".*"}}
	if r := h.do("PUT", "/v1/m/catalog/connector-admission/policy", admin, oneSided, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Errorf("one-sided keyless pins = %d, want 400", r.code)
	}
}

// TestCatalogConnectorAdmissionDigestDefaulting: the S142 delta vs the MCP flow —
// when the admit request omits expected_digest, the binding defaults from the
// entry's spec.artifact_digest (the entry names the artifact it curates), an
// explicit request value overrides, and under require_subject_digest an unbound
// or mismatched binding neither admits nor approves.
func TestCatalogConnectorAdmissionDigestDefaulting(t *testing.T) {
	h := newHarness(t, false)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	att := newAttestation(t, modelsign.PredicateTypeSLSAProvenanceV1, fixtureDigest)
	policy := map[string]any{
		"require_signed": true, "require_subject_digest": true,
		"trusted_keys": []string{att.pubPEM},
	}
	if r := h.do("PUT", "/v1/m/catalog/connector-admission/policy", admin, policy, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("put policy = %d %s", r.code, r.raw)
	}

	// Spec digest matches the bundle subject: admit WITHOUT a request digest →
	// the binding defaults from spec.artifact_digest → ArtifactVerified, admitted.
	bound := h.createConnectorEntry(editor, tenant, "spec-bound", "sha256:"+fixtureDigest)
	r := h.do("POST", "/v1/m/catalog/entries/"+bound+"/admit", admin, map[string]any{"bundle": json.RawMessage(att.bundle)}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["admitted"] != true {
		t.Fatalf("spec-defaulted binding must admit, got %d %s", r.code, r.raw)
	}
	adm := r.body["admission"].(map[string]any)
	if adm["artifact_verified"] != true || adm["subject_digest"] != fixtureDigest {
		t.Fatalf("verdict must be artifact-bound via the spec digest, got %s", r.raw)
	}
	if r := h.do("POST", "/v1/m/catalog/entries/"+bound+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("approve spec-bound entry = %d %s", r.code, r.raw)
	}

	// Spec digest names a DIFFERENT artifact than the bundle subject: the
	// defaulted binding fails — a valid signature over the wrong build must not
	// certify the entry. Recorded, not admitted, approval refused.
	mismatch := h.createConnectorEntry(editor, tenant, "spec-mismatch", strings.Repeat("1", 64))
	r = h.do("POST", "/v1/m/catalog/entries/"+mismatch+"/admit", admin, map[string]any{"bundle": json.RawMessage(att.bundle)}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["admitted"] != false {
		t.Fatalf("a spec-digest mismatch must not admit, got %d %s", r.code, r.raw)
	}
	adm = r.body["admission"].(map[string]any)
	if adm["artifact_verified"] != false {
		t.Fatalf("mismatched binding must not be artifact-verified, got %s", r.raw)
	}
	if r := h.do("POST", "/v1/m/catalog/entries/"+mismatch+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("approve after mismatched binding = %d, want 409 (%s)", r.code, r.raw)
	}
	// An EXPLICIT request digest overrides the (wrong) spec digest.
	r = h.do("POST", "/v1/m/catalog/entries/"+mismatch+"/admit", admin, map[string]any{"bundle": json.RawMessage(att.bundle), "expected_digest": fixtureDigest}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["admitted"] != true {
		t.Fatalf("an explicit digest must override the spec default, got %d %s", r.code, r.raw)
	}

	// No spec digest and no request digest: signature verifies but the artifact
	// binding stays unconfirmed → not admitted under require_subject_digest, and
	// approval refuses on the unconfirmed binding.
	unbound := h.createConnectorEntry(editor, tenant, "unbound", "")
	r = h.do("POST", "/v1/m/catalog/entries/"+unbound+"/admit", admin, map[string]any{"bundle": json.RawMessage(att.bundle)}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["admitted"] != false {
		t.Fatalf("an unbound verdict must not admit under require_subject_digest, got %d %s", r.code, r.raw)
	}
	adm = r.body["admission"].(map[string]any)
	if adm["signature_verified"] != true || adm["artifact_verified"] != false {
		t.Fatalf("verdict must be verified-but-unbound, got %s", r.raw)
	}
	if r := h.do("POST", "/v1/m/catalog/entries/"+unbound+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusConflict || !strings.Contains(r.raw, "unconfirmed") {
		t.Fatalf("approve with unbound digest = %d, want 409 'unconfirmed' (%s)", r.code, r.raw)
	}
}

// TestCatalogConnectorAdmissionAnchorRotation: rotating the trust anchor that admitted a
// connector entry OUT of the policy must deny-close approval — a revoked key must not keep
// certifying via a stale verdict. Mirrors the MCP anchor-rotation test.
func TestCatalogConnectorAdmissionAnchorRotation(t *testing.T) {
	h := newHarness(t, false)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	k1 := newAttestation(t, modelsign.PredicateTypeSLSAProvenanceV1, fixtureDigest)
	k2 := newAttestation(t, modelsign.PredicateTypeSLSAProvenanceV1, fixtureDigest)

	putPolicy := func(keys ...string) {
		t.Helper()
		body := map[string]any{"require_signed": true, "require_subject_digest": true, "trusted_keys": keys}
		if r := h.do("PUT", "/v1/m/catalog/connector-admission/policy", admin, body, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("put policy = %d %s", r.code, r.raw)
		}
	}
	putPolicy(k1.pubPEM)
	id := h.createConnectorEntry(editor, tenant, "anchor-rot", "sha256:"+fixtureDigest)
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/admit", admin, map[string]any{"bundle": json.RawMessage(k1.bundle)}, tenantHdr(tenant)); r.code != http.StatusOK || r.body["admitted"] != true {
		t.Fatalf("admit under K1 = %d %s", r.code, r.raw)
	}
	// Rotate to K2: K1 revoked → approve must deny-close on the removed anchor.
	putPolicy(k2.pubPEM)
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusConflict ||
		!strings.Contains(r.raw, "no longer in the tenant's admission policy") {
		t.Fatalf("approve after anchor rotation = %d, want 409 anchor-rotated-out (%s)", r.code, r.raw)
	}
	// Re-add K1 (widen): trusted again → approve restored, no re-admit.
	putPolicy(k1.pubPEM, k2.pubPEM)
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("approve after re-adding the anchor = %d %s (widening must not invalidate)", r.code, r.raw)
	}
}

// TestCatalogConnectorAdmissionEditAfterAdmit: the certification-invariant hole is
// CLOSED — the approve gate binds the recorded verdict to the digest the entry
// CURRENTLY curates (spec.artifact_digest), not just to the verdict booleans. A
// draft's curated digest is editable (handleUpdateEntry) and the verdict survives
// the edit, so a valid attestation over digest X must NOT certify an entry that
// has since been edited to curate digest Y (a different, unverified build).
func TestCatalogConnectorAdmissionEditAfterAdmit(t *testing.T) {
	h := newHarness(t, false)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	att := newAttestation(t, modelsign.PredicateTypeSLSAProvenanceV1, fixtureDigest)
	policy := map[string]any{
		"require_signed": true, "require_subject_digest": true,
		"trusted_keys": []string{att.pubPEM},
	}
	if r := h.do("PUT", "/v1/m/catalog/connector-admission/policy", admin, policy, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("put policy = %d %s", r.code, r.raw)
	}

	// Create a draft curating digest X, admit a valid attestation bound to X (the
	// default binding from spec.artifact_digest) → verdict verified + artifact-bound.
	id := h.createConnectorEntry(editor, tenant, "edit-after-admit", "sha256:"+fixtureDigest)
	r := h.do("POST", "/v1/m/catalog/entries/"+id+"/admit", admin, map[string]any{"bundle": json.RawMessage(att.bundle)}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["admitted"] != true {
		t.Fatalf("admit over the curated digest must verify, got %d %s", r.code, r.raw)
	}
	adm := r.body["admission"].(map[string]any)
	if adm["signature_verified"] != true || adm["artifact_verified"] != true || adm["subject_digest"] != fixtureDigest {
		t.Fatalf("verdict must be verified + bound to X, got %s", r.raw)
	}

	// Now EDIT the draft's spec.artifact_digest to Y (a different build). The draft
	// is still editable and the X-verdict survives the edit unchanged.
	editBody := connectorEntry("edit-after-admit", "1.0.0", strings.Repeat("a", 64))
	if r := h.do("PUT", "/v1/m/catalog/entries/"+id, editor, editBody, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("edit draft spec.artifact_digest to Y = %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/m/catalog/entries/"+id, admin, nil, tenantHdr(tenant)); r.code != http.StatusOK ||
		r.body["spec"].(map[string]any)["artifact_digest"] != strings.Repeat("a", 64) {
		t.Fatalf("the edit must have changed the curated digest to Y, got %s", r.raw)
	}

	// Approve must now FAIL: the recorded verdict was for X, the entry curates Y. A
	// stale verdict cannot certify the swapped-in artifact — re-admit is required.
	r = h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusConflict || !strings.Contains(r.raw, "re-admit") || !strings.Contains(r.raw, "different artifact") {
		t.Fatalf("approve after curated-digest edit = %d, want 409 re-admit/different-artifact (%s)", r.code, r.raw)
	}

	// Re-admit an attestation for the NEW curated digest Y, then approval succeeds —
	// the invariant is satisfiable, it just demands a verdict bound to what is curated.
	attY := newAttestation(t, modelsign.PredicateTypeSLSAProvenanceV1, strings.Repeat("a", 64))
	policyY := map[string]any{
		"require_signed": true, "require_subject_digest": true,
		"trusted_keys": []string{att.pubPEM, attY.pubPEM},
	}
	if r := h.do("PUT", "/v1/m/catalog/connector-admission/policy", admin, policyY, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("widen trust root to admit Y = %d %s", r.code, r.raw)
	}
	r = h.do("POST", "/v1/m/catalog/entries/"+id+"/admit", admin, map[string]any{"bundle": json.RawMessage(attY.bundle)}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["admitted"] != true {
		t.Fatalf("re-admit over Y must verify, got %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("approve after re-admitting for the current curated digest = %d %s", r.code, r.raw)
	}
}

// TestCatalogConnectorAdmissionNonStringSpecDigest: the admit-flow defaulting must
// treat a PRESENT-but-unusable spec.artifact_digest (here a JSON number) as a
// supplied-but-unusable pin and REFUSE it — never silently degrade to an
// unbound-but-"verified" verdict, which would contradict the deny-closed posture
// (and, under require_subject_digest, would not admit at all but for the wrong
// reason — "unbound" instead of "the pin was unusable").
func TestCatalogConnectorAdmissionNonStringSpecDigest(t *testing.T) {
	h := newHarness(t, false)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	att := newAttestation(t, modelsign.PredicateTypeSLSAProvenanceV1, fixtureDigest)
	policy := map[string]any{"require_signed": true, "trusted_keys": []string{att.pubPEM}}
	if r := h.do("PUT", "/v1/m/catalog/connector-admission/policy", admin, policy, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("put policy = %d %s", r.code, r.raw)
	}

	// Build a connector entry whose spec.artifact_digest is a JSON NUMBER, not a
	// string. The old `.(string)` assertion would yield "" and silently UNBIND.
	body := connectorEntry("nonstring-digest", "1.0.0", "")
	body["spec"].(map[string]any)["artifact_digest"] = 1234567890
	cr := h.do("POST", "/v1/m/catalog/entries", editor, body, tenantHdr(tenant))
	if cr.code != http.StatusCreated {
		t.Fatalf("create entry with numeric artifact_digest = %d %s", cr.code, cr.raw)
	}
	id := cr.body["id"].(string)

	// Admit WITHOUT a request digest: the present-but-unusable spec pin must make the
	// verifier REFUSE (supplied-but-unusable), not produce a verified-but-unbound
	// verdict. The bundle's own subject is valid, so a silent unbind would have set
	// signature_verified=true; deny-closed requires the verdict to be unverified.
	r := h.do("POST", "/v1/m/catalog/entries/"+id+"/admit", admin, map[string]any{"bundle": json.RawMessage(att.bundle)}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["admitted"] != false {
		t.Fatalf("a present-but-unusable spec digest must not admit, got %d %s", r.code, r.raw)
	}
	adm := r.body["admission"].(map[string]any)
	if adm["signature_verified"] != false || adm["artifact_verified"] != false {
		t.Fatalf("a supplied-but-unusable pin must refuse (unverified), not silently unbind: %s", r.raw)
	}
	if adm["reason"] == nil || !strings.Contains(adm["reason"].(string), "sha256") {
		t.Fatalf("the refusal reason must cite the unusable digest, got %s", r.raw)
	}

	// And approval stays refused (no verified verdict exists).
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("approve with a refused verdict = %d, want 409 (%s)", r.code, r.raw)
	}
}
