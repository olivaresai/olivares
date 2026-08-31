// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package catalog_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure/modelsign"
)

// attestationFixture builds a signed in-toto attestation as a Sigstore bundle
// (bare-key) plus the PEM public key for the tenant trust root — the shape
// `cosign download attestation` exports for a Docker-built MCP Catalog image.
type attestationFixture struct {
	bundle json.RawMessage
	pubPEM string
}

const fixtureDigest = "b8938122495f7857c4cb81b77662f4737367665350700856d61724ce61109fac"

func newAttestation(t *testing.T, predicateType, subjectDigest string) attestationFixture {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	statement := map[string]any{
		"_type":         "https://in-toto.io/Statement/v1",
		"subject":       []map[string]any{{"name": "mcp/brave-search", "digest": map[string]string{"sha256": subjectDigest}}},
		"predicateType": predicateType,
		"predicate":     map[string]any{"buildType": "test"},
	}
	payload, err := json.Marshal(statement)
	if err != nil {
		t.Fatal(err)
	}
	const payloadType = "application/vnd.in-toto+json"
	pae := fmt.Sprintf("DSSEv1 %d %s %d %s", len(payloadType), payloadType, len(payload), payload)
	sig := ed25519.Sign(priv, []byte(pae))

	bundle := map[string]any{
		"mediaType":            "application/vnd.dev.sigstore.bundle.v0.3+json",
		"verificationMaterial": map[string]any{"publicKey": map[string]string{"hint": "test-key"}},
		"dsseEnvelope": map[string]any{
			"payload":     base64.StdEncoding.EncodeToString(payload),
			"payloadType": payloadType,
			"signatures":  []map[string]string{{"sig": base64.StdEncoding.EncodeToString(sig)}},
		},
	}
	bj, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return attestationFixture{
		bundle: bj,
		pubPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})),
	}
}

func (h *harness) createMCPEntry(editor string, tenant model.TenantID, slug string) string {
	h.t.Helper()
	r := h.do("POST", "/v1/m/catalog/entries", editor, mcpEntry(slug, "1.0.0"), tenantHdr(tenant))
	if r.code != http.StatusCreated {
		h.t.Fatalf("create mcp entry = %d %s", r.code, r.raw)
	}
	return r.body["id"].(string)
}

// TestCatalogMCPAdmissionGate: the deny-closed gate — with the tenant's
// MCP-entry admission policy requiring signing, an MCP entry approves into the
// catalog (and thus the served sub-registry) ONLY with a verified attestation
// verdict. Observe mode keeps the existing estate working.
func TestCatalogMCPAdmissionGate(t *testing.T) {
	h := newHarness(t, false)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	att := newAttestation(t, modelsign.PredicateTypeSLSAProvenanceV1, fixtureDigest)

	// Observe mode (no policy): an MCP entry approves ungated.
	id := h.createMCPEntry(editor, tenant, "observe")
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("approve in observe mode = %d %s", r.code, r.raw)
	}

	// The default policy endpoint is honest about observe mode.
	if r := h.do("GET", "/v1/m/catalog/mcp-admission/policy", admin, nil, tenantHdr(tenant)); r.body["configured"] != false {
		t.Fatalf("unconfigured policy must say so, got %s", r.raw)
	}

	// Opt INTO deny-closed enforcement with the attestation key as trust root.
	policy := map[string]any{"require_signed": true, "trusted_keys": []string{att.pubPEM}}
	if r := h.do("PUT", "/v1/m/catalog/mcp-admission/policy", admin, policy, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("put policy = %d %s", r.code, r.raw)
	}

	// No verdict → approval refused deny-closed.
	gated := h.createMCPEntry(editor, tenant, "gated")
	if r := h.do("POST", "/v1/m/catalog/entries/"+gated+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("approve without verdict = %d, want 409 (%s)", r.code, r.raw)
	}

	// Admit with a verified SLSA attestation bound to the expected digest.
	body := map[string]any{"bundle": json.RawMessage(att.bundle), "expected_digest": "sha256:" + fixtureDigest}
	r := h.do("POST", "/v1/m/catalog/entries/"+gated+"/admit", admin, body, tenantHdr(tenant))
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

	// Now the entry approves (and would be servable: lo servido = lo aprobado).
	if r := h.do("POST", "/v1/m/catalog/entries/"+gated+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("approve with verified verdict = %d %s", r.code, r.raw)
	}

	// Verdicts list and filter.
	if r := h.do("GET", "/v1/m/catalog/mcp-admissions?verified=true", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK || !strings.Contains(r.raw, gated) {
		t.Errorf("the verified verdict must list, got %d %s", r.code, r.raw)
	}
}

// TestCatalogMCPAdmissionRefusals: the deny paths — wrong artifact digest,
// predicate outside the allow-list, malformed bundle, non-MCP entry kind.
func TestCatalogMCPAdmissionRefusals(t *testing.T) {
	h := newHarness(t, false)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	att := newAttestation(t, modelsign.PredicateTypeSLSAProvenanceV1, fixtureDigest)
	policy := map[string]any{
		"require_signed": true, "trusted_keys": []string{att.pubPEM},
		"allowed_predicates": []string{modelsign.PredicateTypeSLSAProvenanceV1},
	}
	if r := h.do("PUT", "/v1/m/catalog/mcp-admission/policy", admin, policy, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("put policy = %d %s", r.code, r.raw)
	}

	// A valid signature over the WRONG artifact: recorded, not admitted, approval stays refused.
	id := h.createMCPEntry(editor, tenant, "wrong-digest")
	body := map[string]any{"bundle": json.RawMessage(att.bundle), "expected_digest": strings.Repeat("0", 64)}
	r := h.do("POST", "/v1/m/catalog/entries/"+id+"/admit", admin, body, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["admitted"] != false {
		t.Fatalf("a wrong-artifact attestation must record a non-admitted verdict, got %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("approve after failed admission = %d, want 409 (%s)", r.code, r.raw)
	}

	// A predicate the policy does not allow (SPDX vs SLSA-only policy): refused —
	// and a request cannot WIDEN the policy's set.
	spdx := newAttestation(t, modelsign.PredicateTypeSPDX, fixtureDigest)
	policyWithBoth := map[string]any{
		"require_signed":     true,
		"trusted_keys":       []string{att.pubPEM, spdx.pubPEM},
		"allowed_predicates": []string{modelsign.PredicateTypeSLSAProvenanceV1},
	}
	if r := h.do("PUT", "/v1/m/catalog/mcp-admission/policy", admin, policyWithBoth, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("put policy = %d %s", r.code, r.raw)
	}
	id2 := h.createMCPEntry(editor, tenant, "bad-predicate")
	widen := map[string]any{
		"bundle":          json.RawMessage(spdx.bundle),
		"predicate_types": []string{modelsign.PredicateTypeSPDX}, // outside the policy → dropped
	}
	r = h.do("POST", "/v1/m/catalog/entries/"+id2+"/admit", admin, widen, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["admitted"] != false {
		t.Fatalf("an out-of-policy predicate must not admit, got %d %s", r.code, r.raw)
	}

	// A malformed bundle is a 400, never a recorded verdict.
	if r := h.do("POST", "/v1/m/catalog/entries/"+id2+"/admit", admin, map[string]any{"bundle": json.RawMessage(`{"not":"a bundle"}`)}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Errorf("malformed bundle = %d, want 400 (%s)", r.code, r.raw)
	}

	// Admission applies to MCP entries only.
	skill := map[string]any{"kind": "skill", "name": "S", "slug": "sk", "version": "1.0.0"}
	rs := h.do("POST", "/v1/m/catalog/entries", editor, skill, tenantHdr(tenant))
	if r := h.do("POST", "/v1/m/catalog/entries/"+rs.body["id"].(string)+"/admit", admin, map[string]any{"bundle": json.RawMessage(att.bundle)}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Errorf("admitting a non-MCP entry = %d, want 400 (%s)", r.code, r.raw)
	}

	// Policy guards: enforcing with no anchor, or one-sided keyless pins, are rejected.
	if r := h.do("PUT", "/v1/m/catalog/mcp-admission/policy", admin, map[string]any{"require_signed": true}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Errorf("an enforcing policy without anchors = %d, want 400", r.code)
	}
	oneSided := map[string]any{"require_signed": false, "allowed_identities": []string{".*"}}
	if r := h.do("PUT", "/v1/m/catalog/mcp-admission/policy", admin, oneSided, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Errorf("one-sided keyless pins = %d, want 400", r.code)
	}
}

// TestCatalogMCPAdmissionSpecEditInvalidatesVerdict: an MCP entry's spec IS what
// gets served (transport/endpoint/secret_refs), and the MCP approve gate — unlike
// the model/connector gates — does not re-bind the recorded verdict to the current
// spec. So a served-spec edit between admit and approve must invalidate the verdict
// (deny-closed until re-admit); a non-spec edit (name/summary) must NOT.
func TestCatalogMCPAdmissionSpecEditInvalidatesVerdict(t *testing.T) {
	h := newHarness(t, false)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	att := newAttestation(t, modelsign.PredicateTypeSLSAProvenanceV1, fixtureDigest)
	policy := map[string]any{"require_signed": true, "trusted_keys": []string{att.pubPEM}}
	if r := h.do("PUT", "/v1/m/catalog/mcp-admission/policy", admin, policy, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("put policy = %d %s", r.code, r.raw)
	}
	admit := func(id string) {
		t.Helper()
		body := map[string]any{"bundle": json.RawMessage(att.bundle), "expected_digest": "sha256:" + fixtureDigest}
		r := h.do("POST", "/v1/m/catalog/entries/"+id+"/admit", admin, body, tenantHdr(tenant))
		if r.code != http.StatusOK || r.body["admitted"] != true {
			t.Fatalf("admit = %d %s", r.code, r.raw)
		}
	}
	listed := func(id string) bool {
		r := h.do("GET", "/v1/m/catalog/mcp-admissions?entry_ref="+id, admin, nil, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("list admissions = %d %s", r.code, r.raw)
		}
		return strings.Contains(r.raw, id)
	}

	// Entry A: admit a verified verdict, then EDIT the served endpoint. The verdict
	// certified the old launch template; the edit must drop it, so approve deny-closes.
	a := h.createMCPEntry(editor, tenant, "spec-edit")
	admit(a)
	if !listed(a) {
		t.Fatalf("the verified verdict for A must list before the edit")
	}
	editA := mcpEntry("spec-edit", "1.0.0")
	editA["spec"].(map[string]any)["endpoint"] = "npx -y @modelcontextprotocol/server-swapped"
	if r := h.do("PUT", "/v1/m/catalog/entries/"+a, editor, editA, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("edit A served spec = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/catalog/entries/"+a+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusConflict ||
		!strings.Contains(r.raw, "no attestation-admission verdict") {
		t.Fatalf("approve after a served-spec edit = %d, want 409 no-verdict (%s)", r.code, r.raw)
	}
	if listed(a) {
		t.Fatalf("the stale verdict for A must be invalidated (deleted) by the spec edit, still present: %s", a)
	}

	// Entry B: a NAME-only edit leaves the served spec byte-identical, so the verdict
	// survives and approval still succeeds — cosmetic edits must not force a re-admit.
	b := h.createMCPEntry(editor, tenant, "name-edit")
	admit(b)
	editB := mcpEntry("name-edit", "1.0.0")
	editB["name"] = "Renamed GitHub MCP"
	editB["summary"] = "Same template, new label"
	if r := h.do("PUT", "/v1/m/catalog/entries/"+b, editor, editB, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("edit B name/summary = %d %s", r.code, r.raw)
	}
	if !listed(b) {
		t.Fatalf("a name-only edit must NOT invalidate B's verdict")
	}
	if r := h.do("POST", "/v1/m/catalog/entries/"+b+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("approve B after a name-only edit = %d %s", r.code, r.raw)
	}
}

// TestCatalogMCPAdmissionKindFlipInvalidatesVerdict: the verdict is keyed by entry
// id, not kind, so a "kind-flip" must not smuggle a stale verdict past the gate —
// admit as mcp, flip mcp→agent (verdict survives the id), then flip agent→mcp with an
// attacker spec, and approve must still deny-close. The invalidation triggers on the
// kind change, deleting the verdict before it can be reused.
func TestCatalogMCPAdmissionKindFlipInvalidatesVerdict(t *testing.T) {
	h := newHarness(t, false)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	att := newAttestation(t, modelsign.PredicateTypeSLSAProvenanceV1, fixtureDigest)
	policy := map[string]any{"require_signed": true, "trusted_keys": []string{att.pubPEM}}
	if r := h.do("PUT", "/v1/m/catalog/mcp-admission/policy", admin, policy, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("put policy = %d %s", r.code, r.raw)
	}

	id := h.createMCPEntry(editor, tenant, "kindflip")
	body := map[string]any{"bundle": json.RawMessage(att.bundle), "expected_digest": "sha256:" + fixtureDigest}
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/admit", admin, body, tenantHdr(tenant)); r.code != http.StatusOK || r.body["admitted"] != true {
		t.Fatalf("admit = %d %s", r.code, r.raw)
	}

	// Flip mcp→agent WITHOUT touching the spec: the kind change alone must invalidate
	// the verdict (it is keyed by entry id and would otherwise survive the round trip).
	flipOut := map[string]any{
		"kind": "agent", "name": "GitHub MCP", "slug": "kindflip", "version": "1.0.0",
		"spec": map[string]any{"transport": "stdio", "endpoint": "npx -y @modelcontextprotocol/server-github"},
	}
	if r := h.do("PUT", "/v1/m/catalog/entries/"+id, editor, flipOut, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("flip mcp→agent = %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/m/catalog/mcp-admissions?entry_ref="+id, admin, nil, tenantHdr(tenant)); strings.Contains(r.raw, id) {
		t.Fatalf("the kind flip must have invalidated the verdict, still present: %s", r.raw)
	}

	// Flip agent→mcp with an ATTACKER spec, then approve: no verdict remains, so the
	// gate deny-closes — the served template can never ride a stale attestation.
	flipBack := map[string]any{
		"kind": "mcp", "name": "GitHub MCP", "slug": "kindflip", "version": "1.0.0",
		"spec": map[string]any{"transport": "stdio", "endpoint": "npx -y @attacker/evil-server"},
	}
	if r := h.do("PUT", "/v1/m/catalog/entries/"+id, editor, flipBack, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("flip agent→mcp = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusConflict ||
		!strings.Contains(r.raw, "no attestation-admission verdict") {
		t.Fatalf("approve after kind-flip = %d, want 409 no-verdict (%s)", r.code, r.raw)
	}
}

// TestCatalogMCPAdmissionAnchorRotation: a verdict's booleans were computed against the
// trust policy AT ADMIT TIME. If the anchor that verified it is later rotated OUT of the
// policy (e.g. a compromised key revoked), the stale verdict must NOT keep certifying the
// entry at approve. And re-adding the anchor (widening) must restore approval WITHOUT a
// re-admit — the per-verdict re-check is precise, it does not destroy the verdict.
func TestCatalogMCPAdmissionAnchorRotation(t *testing.T) {
	h := newHarness(t, false)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	k1 := newAttestation(t, modelsign.PredicateTypeSLSAProvenanceV1, fixtureDigest)
	k2 := newAttestation(t, modelsign.PredicateTypeSLSAProvenanceV1, fixtureDigest)

	// Trust K1, admit under it → verified.
	if r := h.do("PUT", "/v1/m/catalog/mcp-admission/policy", admin, map[string]any{"require_signed": true, "trusted_keys": []string{k1.pubPEM}}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("put policy = %d %s", r.code, r.raw)
	}
	id := h.createMCPEntry(editor, tenant, "anchor-rot")
	admitBody := map[string]any{"bundle": json.RawMessage(k1.bundle), "expected_digest": "sha256:" + fixtureDigest}
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/admit", admin, admitBody, tenantHdr(tenant)); r.code != http.StatusOK || r.body["admitted"] != true {
		t.Fatalf("admit under K1 = %d %s", r.code, r.raw)
	}

	// Rotate the policy to trust K2 only — K1 (which admitted this entry) is revoked. The
	// verdict survives, but approve must now deny-close: its anchor is no longer trusted.
	if r := h.do("PUT", "/v1/m/catalog/mcp-admission/policy", admin, map[string]any{"require_signed": true, "trusted_keys": []string{k2.pubPEM}}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("rotate policy to K2 = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusConflict ||
		!strings.Contains(r.raw, "no longer in the tenant's admission policy") {
		t.Fatalf("approve after anchor rotation = %d, want 409 anchor-rotated-out (%s)", r.code, r.raw)
	}

	// Widen the policy to trust BOTH K1 and K2 — the anchor is trusted again, so approval
	// is restored with NO re-admit (per-verdict precision: widening never invalidates).
	if r := h.do("PUT", "/v1/m/catalog/mcp-admission/policy", admin, map[string]any{"require_signed": true, "trusted_keys": []string{k1.pubPEM, k2.pubPEM}}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("widen policy = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("approve after re-adding the anchor = %d %s (widening must not invalidate)", r.code, r.raw)
	}
}

// TestCatalogEntryApproveRefusedIsAudited: a refused approval is a durable, hash-
// chained ledger event (catalog.entry.approve_refused for the target entry), not
// just a transient 409 — the refusal survives the response. (The reason string is
// carried in the event Meta and, durably, in the admission verdict the console reads
// via /mcp-admissions; the audit list projection itself omits Meta.)
func TestCatalogEntryApproveRefusedIsAudited(t *testing.T) {
	h := newHarness(t, false)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	att := newAttestation(t, modelsign.PredicateTypeSLSAProvenanceV1, fixtureDigest)
	policy := map[string]any{"require_signed": true, "trusted_keys": []string{att.pubPEM}}
	if r := h.do("PUT", "/v1/m/catalog/mcp-admission/policy", admin, policy, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("put policy = %d %s", r.code, r.raw)
	}

	// An MCP entry with no verdict is refused deny-closed.
	id := h.createMCPEntry(editor, tenant, "refused")
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("approve without verdict = %d, want 409 (%s)", r.code, r.raw)
	}

	// The refusal left a durable audit event TARGETING this entry (assert the event's
	// own target_id, not a bare substring — the create event also carries the id).
	r := h.do("GET", "/v1/audit?limit=100", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("list audit = %d %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	var refused map[string]any
	for _, it := range items {
		if m, ok := it.(map[string]any); ok && m["action"] == "catalog.entry.approve_refused" {
			refused = m
			break
		}
	}
	if refused == nil {
		t.Fatalf("no catalog.entry.approve_refused audit event, got %s", r.raw)
	}
	if refused["target_id"] != id {
		t.Fatalf("approve_refused must target entry %s, got target_id=%v", id, refused["target_id"])
	}
}

// TestCatalogMCPAdmissionDigestBinding: the require_subject_digest enforcement —
// a signature-verified but digest-UNBOUND verdict must neither admit nor approve.
func TestCatalogMCPAdmissionDigestBinding(t *testing.T) {
	h := newHarness(t, false)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	att := newAttestation(t, modelsign.PredicateTypeSLSAProvenanceV1, fixtureDigest)
	policy := map[string]any{
		"require_signed": true, "require_subject_digest": true,
		"trusted_keys": []string{att.pubPEM},
	}
	if r := h.do("PUT", "/v1/m/catalog/mcp-admission/policy", admin, policy, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("put policy = %d %s", r.code, r.raw)
	}

	id := h.createMCPEntry(editor, tenant, "digest-bound")
	// Admit WITHOUT expected_digest: signature verifies, artifact stays unbound →
	// not admitted under require_subject_digest.
	r := h.do("POST", "/v1/m/catalog/entries/"+id+"/admit", admin, map[string]any{"bundle": json.RawMessage(att.bundle)}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["admitted"] != false {
		t.Fatalf("an unbound verdict must not admit under require_subject_digest, got %d %s", r.code, r.raw)
	}
	adm := r.body["admission"].(map[string]any)
	if adm["signature_verified"] != true || adm["artifact_verified"] != false {
		t.Fatalf("verdict must be verified-but-unbound, got %s", r.raw)
	}
	// Approval refuses on the unconfirmed binding (the :requireDigest deny branch).
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusConflict || !strings.Contains(r.raw, "unconfirmed") {
		t.Fatalf("approve with unbound digest = %d, want 409 'unconfirmed' (%s)", r.code, r.raw)
	}
	// Re-admit WITH the digest → bound → admitted and approvable.
	r = h.do("POST", "/v1/m/catalog/entries/"+id+"/admit", admin, map[string]any{"bundle": json.RawMessage(att.bundle), "expected_digest": "sha256:" + fixtureDigest}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["admitted"] != true {
		t.Fatalf("a bound verdict must admit, got %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("approve with bound digest = %d %s", r.code, r.raw)
	}
}
