// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

// the claude-policy TRUTH LOOP: publish → signed artifact (deny-closed),
// agent pull, attested check-in → OBSERVED store, PERMITTED-vs-OBSERVED drift as
// REAL findings, honest unknowns everywhere a signal is absent, and live thread
// events (a wired-but-failing source is 502, never an empty list).

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/managedsettings"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

// policyDoc asserts two governed keys so drift against a host missing them is
// deterministic (defaultMode = Medium, forceRemoteSettingsRefresh off = High).
const policyDoc = `{"permissions":{"defaultMode":"plan"},"forceRemoteSettingsRefresh":true}`

// truthHarness wires the REAL distributor (test Ed25519 key) + the REAL
// store-backed observed provider into the policy console — the production wiring
// of wire.go/boot.go, minus the composition root.
func truthHarness(t *testing.T) (*harness, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	dist := governance.NewPolicyArtifactDistributor(priv)
	if dist == nil {
		t.Fatal("distributor must construct from a real key")
	}
	obs := governance.NewPolicyObservedStore()
	h := newHarnessWith(t, harnessOpts{
		policyOpts: []governance.PolicyConsoleOption{
			governance.WithManagedDistributor(dist),
			governance.WithObservedConfig(obs),
		},
		dataConsumers: []api.DataConsumer{dist, obs},
	})
	return h, pub
}

// publishDoc publishes a managed-settings document and returns the response.
func publishDoc(h *harness, tok string, tenant model.TenantID, doc string) resp {
	h.t.Helper()
	r := h.do("POST", "/v1/m/claude-policy/managed-settings/publish", tok, map[string]any{"content": doc}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		h.t.Fatalf("publish = %d %s", r.code, r.raw)
	}
	return r
}

func TestPolicyPublishDistributesSignedArtifact(t *testing.T) {
	h, pub := truthHarness(t)
	tenant, tok := h.tenantAdmin()

	r := publishDoc(h, tok, tenant, policyDoc)
	if got := r.body["distribution"]; got != "distributed" {
		t.Fatalf("distribution = %v, want distributed (%s)", got, r.raw)
	}
	art, _ := r.body["artifact"].(map[string]any)
	if art == nil || art["artifact_sha256"] == "" || art["key_fingerprint"] == "" {
		t.Fatalf("publish response must carry the artifact summary to pin, got %s", r.raw)
	}
	// With no check-in yet the drift response is an HONEST unknown, never "no drift".
	if r.body["drift_computed"] != false {
		t.Fatalf("drift_computed must be false without observations: %s", r.raw)
	}
	notes := r.raw
	if !strings.Contains(notes, "no agent check-in yet") {
		t.Fatalf("publish without observations must say drift was not computed, got %s", notes)
	}

	// The pull serves the EXACT canonical bytes plus a verifiable detached signature.
	ar := h.do("GET", "/v1/m/claude-policy/managed-settings/artifact", tok, nil, tenantHdr(tenant))
	if ar.code != http.StatusOK {
		t.Fatalf("artifact = %d %s", ar.code, ar.raw)
	}
	canon, err := managedsettings.CanonicalJSON([]byte(policyDoc))
	if err != nil {
		t.Fatal(err)
	}
	if ar.body["content"] != string(canon) {
		t.Fatalf("artifact content must be the exact canonical distributed bytes")
	}
	sum := sha256.Sum256(canon)
	wantSHA := hex.EncodeToString(sum[:])
	if ar.body["artifact_sha256"] != wantSHA {
		t.Fatalf("artifact_sha256 = %v, want %s", ar.body["artifact_sha256"], wantSHA)
	}
	// Verify the detached Ed25519 signature over the documented domain-separated preimage.
	servedPub, err := base64.StdEncoding.DecodeString(ar.body["pubkey"].(string))
	if err != nil || !ed25519.PublicKey(servedPub).Equal(pub) {
		t.Fatalf("served pubkey must be the distributor key")
	}
	sig, err := base64.StdEncoding.DecodeString(ar.body["signature"].(string))
	if err != nil {
		t.Fatal(err)
	}
	rev := int64(ar.body["revision"].(float64))
	preimage := "olivares.policy.artifact.v1|" + tenant.String() + "|managed-settings|" + strconv.FormatInt(rev, 10) + "|" + wantSHA
	hash := sha256.Sum256([]byte(preimage))
	if !ed25519.Verify(pub, hash[:], sig) {
		t.Fatal("artifact signature must verify against the documented preimage")
	}

	// The truth view: published + signed, but host state honestly UNKNOWN.
	dv := h.do("GET", "/v1/m/claude-policy/managed-settings/distribution", tok, nil, tenantHdr(tenant))
	if dv.code != http.StatusOK {
		t.Fatalf("distribution view = %d %s", dv.code, dv.raw)
	}
	if int64(dv.body["latest_revision"].(float64)) != rev {
		t.Fatalf("latest_revision mismatch: %s", dv.raw)
	}
	if !strings.Contains(dv.raw, "no agent has checked in") || !strings.Contains(dv.raw, "UNKNOWN") {
		t.Fatalf("truth view without check-ins must say host state is unknown, got %s", dv.raw)
	}
}

func TestPolicyPublishDenyClosedOnDistributionFailure(t *testing.T) {
	// The distributor exists but its store handle is NOT bound (boot interrupted):
	// the publish must say enqueue-failed — NEVER "distributed" — and the pull
	// must have nothing to serve.
	_, priv, _ := ed25519.GenerateKey(nil)
	dist := governance.NewPolicyArtifactDistributor(priv)
	h := newHarnessWith(t, harnessOpts{
		policyOpts: []governance.PolicyConsoleOption{governance.WithManagedDistributor(dist)},
		// dist deliberately NOT in dataConsumers.
	})
	tenant, tok := h.tenantAdmin()

	r := publishDoc(h, tok, tenant, policyDoc)
	if got := r.body["distribution"]; got != "enqueue-failed" {
		t.Fatalf("distribution = %v, want enqueue-failed (%s)", got, r.raw)
	}
	if _, hasArtifact := r.body["artifact"]; hasArtifact {
		t.Fatalf("a failed distribution must not report an artifact: %s", r.raw)
	}
	// The revision IS persisted (failure does not roll back the publish)…
	vr := h.do("GET", "/v1/m/claude-policy/managed-settings/versions", tok, nil, tenantHdr(tenant))
	if len(items(vr)) != 1 {
		t.Fatalf("revision must persist on distribution failure: %s", vr.raw)
	}
	// …but there is nothing signed to pull (deny-closed).
	ar := h.do("GET", "/v1/m/claude-policy/managed-settings/artifact", tok, nil, tenantHdr(tenant))
	if ar.code != http.StatusNotFound {
		t.Fatalf("artifact after failed distribution = %d, want 404 (%s)", ar.code, ar.raw)
	}
}

func TestPolicyCheckinTruthLoop(t *testing.T) {
	h, _ := truthHarness(t)
	root := h.adminLogin()
	tenant := h.createOrg(root, "acme")
	_, tok := h.roleUser(root, tenant, "boss@acme.io", "admin")
	_, editor := h.roleUser(root, tenant, "agent@acme.io", "editor")

	r := publishDoc(h, tok, tenant, policyDoc)
	art := r.body["artifact"].(map[string]any)
	sha := art["artifact_sha256"].(string)
	fp := art["key_fingerprint"].(string)
	canon, _ := managedsettings.CanonicalJSON([]byte(policyDoc))

	// host-a applied the artifact faithfully: verified, zero drift.
	ck := h.do("POST", "/v1/m/claude-policy/managed-settings/checkin", editor, map[string]any{
		"scope": "host-a", "revision": 1, "artifact_sha256": sha, "key_fingerprint": fp,
		"observed_content": string(canon),
	}, tenantHdr(tenant))
	if ck.code != http.StatusOK {
		t.Fatalf("checkin = %d %s", ck.code, ck.raw)
	}
	if ck.body["verified"] != true {
		t.Fatalf("a faithful check-in must verify: %s", ck.raw)
	}
	if drift, _ := ck.body["drift"].([]any); len(drift) != 0 {
		t.Fatalf("no drift expected for a faithful apply: %s", ck.raw)
	}
	if !strings.Contains(ck.raw, "no drift on this scope") {
		t.Fatalf("a clean scope must be reported as such: %s", ck.raw)
	}

	// host-b runs an ungoverned config: drift findings are computed, PERSISTED as
	// real core findings and emitted on the bus.
	ck = h.do("POST", "/v1/m/claude-policy/managed-settings/checkin", editor, map[string]any{
		"scope": "host-b", "observed_content": `{"permissions":{"defaultMode":"default"}}`,
	}, tenantHdr(tenant))
	if ck.code != http.StatusOK {
		t.Fatalf("checkin host-b = %d %s", ck.code, ck.raw)
	}
	drift, _ := ck.body["drift"].([]any)
	if len(drift) == 0 {
		t.Fatalf("a drifted host must produce drift findings: %s", ck.raw)
	}
	persisted := openDriftFindings(t, h, tenant)
	if len(persisted) != len(drift) {
		t.Fatalf("drift must persist as core findings: response %d vs stored %d", len(drift), len(persisted))
	}
	busDrift := 0
	for _, f := range h.host.findings() {
		if f.Kind == "policy_drift" {
			busDrift++
		}
	}
	if busDrift != len(drift) {
		t.Fatalf("drift must ride finding.reported: bus %d vs response %d", busDrift, len(drift))
	}

	// Re-checking in the SAME state must not duplicate the standing findings.
	ck2 := h.do("POST", "/v1/m/claude-policy/managed-settings/checkin", editor, map[string]any{
		"scope": "host-b", "observed_content": `{"permissions":{"defaultMode":"default"}}`,
	}, tenantHdr(tenant))
	if ck2.code != http.StatusOK {
		t.Fatalf("re-checkin = %d %s", ck2.code, ck2.raw)
	}
	if again := openDriftFindings(t, h, tenant); len(again) != len(persisted) {
		t.Fatalf("re-check-in duplicated findings: %d -> %d", len(persisted), len(again))
	}

	// The truth view now reflects both scopes' REAL state.
	dv := h.do("GET", "/v1/m/claude-policy/managed-settings/distribution", tok, nil, tenantHdr(tenant))
	scopes, _ := dv.body["scopes"].([]any)
	if len(scopes) != 2 {
		t.Fatalf("truth view must list both scopes: %s", dv.raw)
	}
	byScope := map[string]map[string]any{}
	for _, s := range scopes {
		m := s.(map[string]any)
		byScope[m["scope"].(string)] = m
	}
	if byScope["host-a"]["current"] != true || byScope["host-a"]["verified"] != true {
		t.Fatalf("host-a must be current+verified: %s", dv.raw)
	}
	if byScope["host-b"]["verified"] == true || byScope["host-b"]["drift_count"].(float64) == 0 {
		t.Fatalf("host-b must be unverified with drift: %s", dv.raw)
	}

	// PUBLISH-TIME drift over the store-backed observed provider: a new revision
	// is verified fleet-wide against BOTH recorded scopes.
	r2 := publishDoc(h, tok, tenant, policyDoc)
	if !strings.Contains(r2.raw, "2 observed scope(s)") {
		t.Fatalf("publish must compute drift across every observed scope: %s", r2.raw)
	}
	if drift2, _ := r2.body["drift"].([]any); len(drift2) == 0 {
		t.Fatalf("publish-time drift must surface host-b's findings: %s", r2.raw)
	}
	if r2.body["drift_computed"] != true {
		t.Fatalf("drift_computed must be true when scopes were diffed: %s", r2.raw)
	}

	// The check-in is self-audited.
	if !contains(h.auditActions(tenant), "governance.claude_policy.checkin") {
		t.Fatal("check-in must be audited")
	}
}

func TestPolicyCheckinAttestationMismatch(t *testing.T) {
	h, _ := truthHarness(t)
	root := h.adminLogin()
	tenant := h.createOrg(root, "acme")
	_, tok := h.roleUser(root, tenant, "boss@acme.io", "admin")
	_, editor := h.roleUser(root, tenant, "agent@acme.io", "editor")
	publishDoc(h, tok, tenant, policyDoc)

	ck := h.do("POST", "/v1/m/claude-policy/managed-settings/checkin", editor, map[string]any{
		"scope": "host-x", "revision": 1, "artifact_sha256": strings.Repeat("ab", 32),
	}, tenantHdr(tenant))
	if ck.code != http.StatusOK {
		t.Fatalf("checkin = %d %s", ck.code, ck.raw)
	}
	if ck.body["verified"] == true {
		t.Fatalf("a mismatched attestation must NOT verify: %s", ck.raw)
	}
	if !strings.Contains(ck.raw, "does NOT match") {
		t.Fatalf("the mismatch must be named: %s", ck.raw)
	}
	found := false
	for _, f := range openDriftFindings(t, h, tenant) {
		if strings.Contains(f.Title, "does not match the signed distribution record") {
			found = true
			if f.Severity != model.SeverityHigh {
				t.Fatalf("attestation mismatch must be High, got %s", f.Severity)
			}
		}
	}
	if !found {
		t.Fatal("attestation mismatch must persist as a finding")
	}
}

func TestPolicyCheckinRedactsInlineCredential(t *testing.T) {
	h, _ := truthHarness(t)
	root := h.adminLogin()
	tenant := h.createOrg(root, "acme")
	_, tok := h.roleUser(root, tenant, "boss@acme.io", "admin")
	_, editor := h.roleUser(root, tenant, "agent@acme.io", "editor")
	publishDoc(h, tok, tenant, policyDoc)

	ck := h.do("POST", "/v1/m/claude-policy/managed-settings/checkin", editor, map[string]any{
		"scope": "host-c", "observed_content": `{"env":{"KEY":"sk-ant-api03-SECRETSECRET"},"forceRemoteSettingsRefresh":true}`,
	}, tenantHdr(tenant))
	if ck.code != http.StatusOK {
		t.Fatalf("checkin = %d %s", ck.code, ck.raw)
	}
	// The stored OBSERVED row must never carry the secret material.
	var stored string
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("governance.policy_observed"))
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Limit: 100})
		if err != nil {
			return err
		}
		for _, rec := range recs {
			if rec.String("scope") == "host-c" {
				stored = rec.String("content")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if stored == "" || strings.Contains(stored, "SECRETSECRET") {
		t.Fatalf("the inline credential must be redacted before storage, stored=%q", stored)
	}
	if !strings.Contains(stored, "[REDACTED]") {
		t.Fatalf("redaction marker missing: %q", stored)
	}
	// And the credential itself is a HIGH finding.
	found := false
	for _, f := range openDriftFindings(t, h, tenant) {
		if strings.Contains(f.Title, "inline credential") && f.Severity == model.SeverityHigh {
			found = true
		}
	}
	if !found {
		t.Fatal("an observed inline credential must raise a High finding")
	}
}

func TestPolicyAttestationOnlyCheckinPreservesObservation(t *testing.T) {
	// Review finding: an attestation-only check-in must NOT wipe the
	// stored observation, must NOT stamp drift counters (no computation ran),
	// and the NEXT publish must not fabricate a HIGH "host is ungoverned"
	// absence finding off the wiped row.
	h, _ := truthHarness(t)
	root := h.adminLogin()
	tenant := h.createOrg(root, "acme")
	_, tok := h.roleUser(root, tenant, "boss@acme.io", "admin")
	_, editor := h.roleUser(root, tenant, "agent@acme.io", "editor")

	r := publishDoc(h, tok, tenant, policyDoc)
	art := r.body["artifact"].(map[string]any)
	canon, _ := managedsettings.CanonicalJSON([]byte(policyDoc))

	// 1. Content check-in: faithful host, zero drift.
	ck := h.do("POST", "/v1/m/claude-policy/managed-settings/checkin", editor, map[string]any{
		"scope": "host-a", "revision": 1, "artifact_sha256": art["artifact_sha256"],
		"observed_content": string(canon),
	}, tenantHdr(tenant))
	if ck.code != http.StatusOK {
		t.Fatalf("content checkin = %d %s", ck.code, ck.raw)
	}

	// 2. Attestation-only ping (no observed_content).
	ck = h.do("POST", "/v1/m/claude-policy/managed-settings/checkin", editor, map[string]any{
		"scope": "host-a", "revision": 1, "artifact_sha256": art["artifact_sha256"],
	}, tenantHdr(tenant))
	if ck.code != http.StatusOK {
		t.Fatalf("attestation-only checkin = %d %s", ck.code, ck.raw)
	}
	if !strings.Contains(ck.raw, "no observed_content reported") {
		t.Fatalf("the not-computed state must be named: %s", ck.raw)
	}
	if strings.Contains(ck.raw, "no drift on this scope") {
		t.Fatalf("an uncomputed check-in must NEVER claim no-drift: %s", ck.raw)
	}

	// The stored observation survives the contentless ping.
	var stored string
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("governance.policy_observed"))
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Limit: 100})
		for _, rec := range recs {
			if rec.String("scope") == "host-a" {
				stored = rec.String("content")
			}
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if stored != string(canon) {
		t.Fatalf("attestation-only check-in wiped the stored observation: %q", stored)
	}

	// 3. The next publish computes drift off the PRESERVED content: no absence
	// finding, no fabricated "host is ungoverned".
	r2 := publishDoc(h, tok, tenant, policyDoc)
	if drift, _ := r2.body["drift"].([]any); len(drift) != 0 {
		t.Fatalf("publish after attestation-only ping fabricated drift: %s", r2.raw)
	}
	for _, f := range openDriftFindings(t, h, tenant) {
		if strings.Contains(f.Title, "ungoverned") {
			t.Fatalf("fabricated absence finding: %s", f.Title)
		}
	}
}

func TestPolicyCheckinFirstContactWithoutContentIsUnknown(t *testing.T) {
	// A scope whose ONLY contact is an attestation ping must read as content-
	// UNKNOWN in the truth view, never as clean — and must not feed publish-time
	// drift at all (no fabricated absence finding).
	h, _ := truthHarness(t)
	root := h.adminLogin()
	tenant := h.createOrg(root, "acme")
	_, tok := h.roleUser(root, tenant, "boss@acme.io", "admin")
	_, editor := h.roleUser(root, tenant, "agent@acme.io", "editor")
	publishDoc(h, tok, tenant, policyDoc)

	ck := h.do("POST", "/v1/m/claude-policy/managed-settings/checkin", editor, map[string]any{
		"scope": "host-ping",
	}, tenantHdr(tenant))
	if ck.code != http.StatusOK {
		t.Fatalf("checkin = %d %s", ck.code, ck.raw)
	}
	dv := h.do("GET", "/v1/m/claude-policy/managed-settings/distribution", tok, nil, tenantHdr(tenant))
	scopes, _ := dv.body["scopes"].([]any)
	if len(scopes) != 1 {
		t.Fatalf("scope must appear: %s", dv.raw)
	}
	sc0 := scopes[0].(map[string]any)
	if sc0["content_reported"] != false {
		t.Fatalf("attestation-only scope must be content-unknown: %s", dv.raw)
	}
	if at, hasStamp := sc0["last_drift_at"].(string); hasStamp && at != "" {
		t.Fatalf("no drift computation ran — the stamp must be absent: %s", dv.raw)
	}
	if !strings.Contains(dv.raw, "content drift for it is UNKNOWN") {
		t.Fatalf("the unknown must be named in the truth view: %s", dv.raw)
	}
	// Publish-time drift skips the contentless scope honestly.
	r2 := publishDoc(h, tok, tenant, policyDoc)
	if !strings.Contains(r2.raw, "no observed host config available") {
		t.Fatalf("a contentless scope must not count as an observation: %s", r2.raw)
	}
}

func TestPolicyCheckinNoDriftNoteNeverBesideFindings(t *testing.T) {
	// "(no drift on this scope)" must never appear when the SAME response
	// carries attestation/credential findings.
	h, _ := truthHarness(t)
	root := h.adminLogin()
	tenant := h.createOrg(root, "acme")
	_, tok := h.roleUser(root, tenant, "boss@acme.io", "admin")
	_, editor := h.roleUser(root, tenant, "agent@acme.io", "editor")
	publishDoc(h, tok, tenant, policyDoc)
	canon, _ := managedsettings.CanonicalJSON([]byte(policyDoc))

	// Content matches (content-clean) but the attestation hash is wrong.
	ck := h.do("POST", "/v1/m/claude-policy/managed-settings/checkin", editor, map[string]any{
		"scope": "host-m", "revision": 1, "artifact_sha256": strings.Repeat("cd", 32),
		"observed_content": string(canon),
	}, tenantHdr(tenant))
	if ck.code != http.StatusOK {
		t.Fatalf("checkin = %d %s", ck.code, ck.raw)
	}
	if drift, _ := ck.body["drift"].([]any); len(drift) == 0 {
		t.Fatalf("the attestation mismatch must surface: %s", ck.raw)
	}
	if strings.Contains(ck.raw, "no drift on this scope") {
		t.Fatalf("no-drift claimed beside a HIGH finding: %s", ck.raw)
	}
	// The truth view's live open-findings count covers the mismatch.
	dv := h.do("GET", "/v1/m/claude-policy/managed-settings/distribution", tok, nil, tenantHdr(tenant))
	if !strings.Contains(dv.raw, `"open_findings":1`) {
		t.Fatalf("live open-findings count missing: %s", dv.raw)
	}
}

func TestPolicyArtifactExplicitOldRevisionNotes(t *testing.T) {
	// Pulling an explicitly older revision must say "a newer signed artifact
	// exists" — never fabricate "the newest revision failed to sign".
	h, _ := truthHarness(t)
	tenant, tok := h.tenantAdmin()
	publishDoc(h, tok, tenant, policyDoc)
	publishDoc(h, tok, tenant, policyDoc)

	ar := h.do("GET", "/v1/m/claude-policy/managed-settings/artifact?revision=1", tok, nil, tenantHdr(tenant))
	if ar.code != http.StatusOK {
		t.Fatalf("artifact r1 = %d %s", ar.code, ar.raw)
	}
	if strings.Contains(ar.raw, "enqueue failed") {
		t.Fatalf("must not fabricate an enqueue failure: %s", ar.raw)
	}
	if !strings.Contains(ar.raw, "a newer signed artifact exists (revision 2)") {
		t.Fatalf("the newer artifact must be named: %s", ar.raw)
	}
}

func TestPolicyCheckinRBAC(t *testing.T) {
	h, _ := truthHarness(t)
	root := h.adminLogin()
	tenant := h.createOrg(root, "acme")
	_, tok := h.roleUser(root, tenant, "boss@acme.io", "admin")
	_, viewer := h.roleUser(root, tenant, "viewer@acme.io", "viewer")
	publishDoc(h, tok, tenant, policyDoc)

	// A viewer can READ the artifact (pull is read-tier)…
	if r := h.do("GET", "/v1/m/claude-policy/managed-settings/artifact", viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("viewer artifact pull = %d, want 200", r.code)
	}
	// …but cannot CHECK IN (write-tier).
	r := h.do("POST", "/v1/m/claude-policy/managed-settings/checkin", viewer, map[string]any{"scope": "host-a"}, tenantHdr(tenant))
	if r.code != http.StatusForbidden {
		t.Fatalf("viewer checkin = %d, want 403 (%s)", r.code, r.raw)
	}
}

func TestPolicyObservedSourceFailureIsHonest(t *testing.T) {
	// A WIRED observed source that cannot answer must be reported as unreadable —
	// never as "no drift" and never as "no observation".
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	h.observed.err = context.DeadlineExceeded
	r := publishDoc(h, tok, tenant, policyDoc)
	if !strings.Contains(r.raw, "could not be read") {
		t.Fatalf("a failing observed source must be named: %s", r.raw)
	}
	if !strings.Contains(r.raw, "absence of signal, not absence of drift") {
		t.Fatalf("the honest-unknown wording must survive: %s", r.raw)
	}
}

func TestThreadEventsSourceErrorIs502(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	h.threads.err = context.DeadlineExceeded
	r := h.do("GET", "/v1/m/claude-agents/sessions/sesn_1/events", tok, nil, tenantHdr(tenant))
	if r.code != http.StatusBadGateway {
		t.Fatalf("a wired-but-failing thread source = %d, want 502 (%s)", r.code, r.raw)
	}
	if !strings.Contains(r.raw, "UNKNOWN") {
		t.Fatalf("the outage must be named as unknown, not absent: %s", r.raw)
	}
}

// openDriftFindings reads the tenant's OPEN policy_drift findings from the core
// findings repo (what the SOC/web views serve).
func openDriftFindings(t *testing.T, h *harness, tenant model.TenantID) []model.Finding {
	t.Helper()
	var out []model.Finding
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		finds, _, err := sc.Findings().List(context.Background(), model.Query{Filters: []model.Filter{
			{Column: "kind", Op: model.OpEq, Value: "policy_drift"},
			{Column: "status", Op: model.OpEq, Value: string(model.FindingOpen)},
		}, Limit: 200})
		out = finds
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}
