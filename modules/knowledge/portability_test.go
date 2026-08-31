// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/sigbundle"
)

// ---- portability test helpers ---------------------------------------------------

// portKeys mints a dedicated portability keypair.
func portKeys(t *testing.T) (ed25519.PrivateKey, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	return priv, pub
}

// doRawImport POSTs a raw JSONL bundle (h.do would JSON-wrap it).
func (h *harness) doRawImport(token string, tenant model.TenantID, body []byte) resp {
	h.t.Helper()
	req := httptest.NewRequest("POST", "/v1/m/knowledge/memory/import", bytes.NewReader(body))
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Olivares-Tenant", tenant.String())
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	out := resp{code: rec.Code, raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

// parseExport splits an export body into its manifest and entry rows.
func parseExport(t *testing.T, raw string) (memPortManifest, []memPortRow) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatalf("empty export body: %q", raw)
	}
	var mani memPortManifest
	if err := json.Unmarshal([]byte(lines[0]), &mani); err != nil {
		t.Fatalf("manifest line: %v (%q)", err, lines[0])
	}
	var rows []memPortRow
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var row memPortRow
		if err := json.Unmarshal([]byte(l), &row); err != nil {
			t.Fatalf("entry line: %v (%q)", err, l)
		}
		rows = append(rows, row)
	}
	return mani, rows
}

// buildSignedBundle mints a VALIDLY-signed bundle from arbitrary rows (so a test can
// inject an unknown-label row into an otherwise-authentic bundle). It mirrors the
// exporter's canonical marshaling exactly, so signature + digest verify.
func buildSignedBundle(t *testing.T, priv ed25519.PrivateKey, tenant, agentRef string, scope memPortScope, rows []memPortRow) []byte {
	t.Helper()
	sum := sha256.New()
	var lines [][]byte
	for _, row := range rows {
		b, _ := json.Marshal(row)
		sum.Write(b)
		lines = append(lines, b)
	}
	mani := memPortManifest{
		Schema: memPortSchema, Tenant: tenant, AgentRef: agentRef, Scope: scope,
		Count: len(rows), EntriesSHA256: hex.EncodeToString(sum.Sum(nil)),
	}
	toSign, err := mani.signingBytes()
	if err != nil {
		t.Fatalf("signingBytes: %v", err)
	}
	mani.Signature = base64.StdEncoding.EncodeToString(sigbundle.Sign(sigbundle.TagMemoryPortability, toSign, priv))
	ml, _ := json.Marshal(mani)
	var buf bytes.Buffer
	buf.Write(ml)
	buf.WriteByte('\n')
	for _, l := range lines {
		buf.Write(l)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func ptr(s string) *string { return &s }

// seedPortMemory writes a classified corpus for agent-1 and returns tokens.
func seedPortMemory(t *testing.T, h *harness) (model.TenantID, string) {
	t.Helper()
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	for _, c := range []string{classPublic, classInternal, classConfidential, classSecret} {
		h.putMemoryScoped(editor, tenant, map[string]any{
			"agent_ref": "agent-1", "key": c, "content": c + " memory", "classification": c,
		})
	}
	return tenant, editor
}

// ---- fail-closed on unwired keys ------------------------------------------------

func TestMemoryExport_FailsClosedWithoutSigningKey(t *testing.T) {
	h := newHarnessWith(t, WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Clearance: classSecret}}))
	tenant, editor := seedPortMemory(t, h)
	r := h.do("GET", "/v1/m/knowledge/memory/export?agent_ref=agent-1", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusNotImplemented {
		t.Fatalf("export without a signing key must 501 (never emit an unsigned bundle), got %d %s", r.code, r.raw)
	}
}

func TestMemoryImport_FailsClosedWithoutVerifyKey(t *testing.T) {
	h := newHarnessWith(t, WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Clearance: classSecret}}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	r := h.doRawImport(editor, tenant, []byte(`{"schema":"olivares.memory-portability.v1"}`+"\n"))
	if r.code != http.StatusNotImplemented {
		t.Fatalf("import without a verify key must 501 (never persist an unverifiable bundle), got %d %s", r.code, r.raw)
	}
}

// ---- the security invariant: export never surfaces above clearance --------------

func TestMemoryExport_OmitsEntriesAboveClearance(t *testing.T) {
	priv, pub := portKeys(t)
	// Reader clearance is INTERNAL: confidential + secret must NOT appear in the export.
	h := newHarnessWith(t,
		WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Clearance: classInternal}}),
		WithMemoryPortabilityKeys(priv, pub))
	tenant, editor := seedPortMemory(t, h)

	r := h.do("GET", "/v1/m/knowledge/memory/export?agent_ref=agent-1", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("export = %d %s", r.code, r.raw)
	}
	mani, rows := parseExport(t, r.raw)
	got := map[string]bool{}
	for _, row := range rows {
		got[row.Classification] = true
	}
	if !got[classPublic] || !got[classInternal] {
		t.Fatalf("export must include public+internal, got %v", got)
	}
	if got[classConfidential] || got[classSecret] {
		t.Fatalf("SECURITY: export leaked an entry ABOVE the reader's clearance: %v", got)
	}
	if mani.Count != len(rows) || mani.Count != 2 {
		t.Fatalf("manifest count = %d, rows = %d, want 2", mani.Count, len(rows))
	}
}

func TestMemoryExport_DenyClosedGuardExportsPublicOnly(t *testing.T) {
	priv, pub := portKeys(t)
	// A resolve error must fall back to public-only clearance (deny closed), and the
	// export must honor that fallback exactly like the list path.
	h := newHarnessWith(t, WithRetrievalGuard(errorGuard{}), WithMemoryPortabilityKeys(priv, pub))
	tenant, editor := seedPortMemory(t, h)

	r := h.do("GET", "/v1/m/knowledge/memory/export?agent_ref=agent-1", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("export = %d %s", r.code, r.raw)
	}
	_, rows := parseExport(t, r.raw)
	if len(rows) != 1 || rows[0].Classification != classPublic {
		t.Fatalf("deny-closed export must be public-only, got %d rows %v", len(rows), rows)
	}
}

// ---- round trip: signature + digest + governed re-write -------------------------

func TestMemoryPortability_RoundTripPreservesClassificationAndScope(t *testing.T) {
	priv, pub := portKeys(t)
	// Source deployment: secret clearance so the full corpus + a user-scoped entry export.
	src := newHarnessWith(t,
		WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Clearance: classSecret}}),
		WithMemoryPortabilityKeys(priv, pub))
	admin := src.adminLogin()
	tenant := src.createOrg(admin, "acme")
	editor := src.roleToken(admin, tenant, "ed@acme.com", "editor")
	for _, c := range []string{classPublic, classInternal, classConfidential} {
		src.putMemoryScoped(editor, tenant, map[string]any{
			"agent_ref": "agent-1", "key": c, "content": c + " memory", "classification": c,
		})
	}
	src.putMemoryScoped(editor, tenant, map[string]any{
		"agent_ref": "agent-1", "key": "scoped", "content": "u1 note", "classification": classInternal, "user_ref": "u1",
	})

	// Export the u1 context (agent-global entries + u1-scoped).
	exp := src.do("GET", "/v1/m/knowledge/memory/export?agent_ref=agent-1&user_ref=u1", editor, nil, tenantHdr(tenant))
	if exp.code != http.StatusOK {
		t.Fatalf("export = %d %s", exp.code, exp.raw)
	}
	_, rows := parseExport(t, exp.raw)
	if len(rows) != 4 {
		t.Fatalf("want 4 exported rows (3 global + 1 scoped), got %d", len(rows))
	}

	// Destination deployment: a FRESH store + tenant, same keypair.
	dst := newHarnessWith(t,
		WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Clearance: classSecret}}),
		WithMemoryPortabilityKeys(priv, pub))
	dadmin := dst.adminLogin()
	dtenant := dst.createOrg(dadmin, "beta")
	deditor := dst.roleToken(dadmin, dtenant, "ed@beta.com", "editor")

	imp := dst.doRawImport(deditor, dtenant, []byte(exp.raw))
	if imp.code != http.StatusOK {
		t.Fatalf("import = %d %s", imp.code, imp.raw)
	}
	if got := imp.body["imported"].(float64); int(got) != 4 {
		t.Fatalf("imported = %v, want 4; rejected=%v", got, imp.body["rejected"])
	}

	// Verify via the admin view that classifications survived and the scope round-tripped.
	dadminTok := dst.roleToken(dadmin, dtenant, "adm@beta.com", "admin")
	all := dst.do("GET", "/v1/m/knowledge/memory/all?agent_ref=agent-1", dadminTok, nil, tenantHdr(dtenant))
	classes, scopedSeen := map[string]bool{}, false
	for _, it := range listItems(all) {
		classes[it["classification"].(string)] = true
		if it["key"].(string) == "scoped" && it["user_ref"] == "u1" {
			scopedSeen = true
		}
	}
	if !classes[classPublic] || !classes[classInternal] || !classes[classConfidential] {
		t.Fatalf("round-trip lost a classification: %v", classes)
	}
	if !scopedSeen {
		t.Fatal("round-trip lost the user-scoped entry's namespace")
	}
}

// ---- integrity + authenticity are checked BEFORE any write ----------------------

func TestMemoryImport_TamperedEntryRejectedWholeBundle(t *testing.T) {
	priv, pub := portKeys(t)
	h := newHarnessWith(t,
		WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Clearance: classSecret}}),
		WithMemoryPortabilityKeys(priv, pub))
	tenant, editor := seedPortMemory(t, h)
	exp := h.do("GET", "/v1/m/knowledge/memory/export?agent_ref=agent-1", editor, nil, tenantHdr(tenant))

	// Flip a byte in an entry line: the digest no longer matches the signed manifest.
	tampered := strings.Replace(exp.raw, "public memory", "public MEMORY", 1)
	if tampered == exp.raw {
		t.Fatal("setup: content to tamper not found")
	}
	dst := newHarnessWith(t, WithMemoryPortabilityKeys(priv, pub))
	dadmin := dst.adminLogin()
	dtenant := dst.createOrg(dadmin, "beta")
	deditor := dst.roleToken(dadmin, dtenant, "ed@beta.com", "editor")

	r := dst.doRawImport(deditor, dtenant, []byte(tampered))
	if r.code != http.StatusBadRequest {
		t.Fatalf("a tampered bundle must be rejected (digest mismatch), got %d %s", r.code, r.raw)
	}
	// Nothing must have been written.
	dadminTok := dst.roleToken(dadmin, dtenant, "adm@beta.com", "admin")
	all := dst.do("GET", "/v1/m/knowledge/memory/all?agent_ref=agent-1", dadminTok, nil, tenantHdr(dtenant))
	if items := listItems(all); len(items) != 0 {
		t.Fatalf("a rejected import must persist nothing, found %d entries", len(items))
	}
}

func TestMemoryImport_ForgedSignatureRejected(t *testing.T) {
	priv, pub := portKeys(t)
	// Source signs with its key; destination trusts a DIFFERENT key.
	src := newHarnessWith(t,
		WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Clearance: classSecret}}),
		WithMemoryPortabilityKeys(priv, pub))
	tenant, editor := seedPortMemory(t, src)
	exp := src.do("GET", "/v1/m/knowledge/memory/export?agent_ref=agent-1", editor, nil, tenantHdr(tenant))

	_, otherPub := portKeys(t)
	dst := newHarnessWith(t, WithMemoryPortabilityKeys(nil, otherPub))
	dadmin := dst.adminLogin()
	dtenant := dst.createOrg(dadmin, "beta")
	deditor := dst.roleToken(dadmin, dtenant, "ed@beta.com", "editor")

	r := dst.doRawImport(deditor, dtenant, []byte(exp.raw))
	if r.code != http.StatusBadRequest {
		t.Fatalf("a bundle signed under a different key must not verify, got %d %s", r.code, r.raw)
	}
}

// ---- import re-runs write-path governance: unknown label rejected per row -------

func TestMemoryImport_RejectsUnknownLabelPerRow(t *testing.T) {
	priv, pub := portKeys(t)
	h := newHarnessWith(t, WithMemoryPortabilityKeys(priv, pub))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")

	// A validly-signed bundle whose middle row carries a garbage classification.
	bundle := buildSignedBundle(t, priv, tenant.String(), "agent-9", memPortScope{}, []memPortRow{
		{AgentRef: "agent-9", Key: "ok1", Content: "fine", Classification: classInternal},
		{AgentRef: "agent-9", Key: "bad", Content: "nope", Classification: "ultra-top-secret"},
		{AgentRef: "agent-9", Key: "ok2", Content: "fine too", Classification: classPublic},
	})
	r := h.doRawImport(editor, tenant, bundle)
	if r.code != http.StatusOK {
		t.Fatalf("import = %d %s", r.code, r.raw)
	}
	if imported := int(r.body["imported"].(float64)); imported != 2 {
		t.Fatalf("imported = %d, want 2 (the unknown-label row rejected)", imported)
	}
	rej, _ := r.body["rejected"].([]any)
	if len(rej) != 1 {
		t.Fatalf("want exactly 1 rejected row (unknown label), got %v", r.body["rejected"])
	}
	// The unknown label must NOT have persisted as public (fail closed, never demote).
	adminTok := h.roleToken(admin, tenant, "adm@acme.com", "admin")
	all := h.do("GET", "/v1/m/knowledge/memory/all?agent_ref=agent-9", adminTok, nil, tenantHdr(tenant))
	for _, it := range listItems(all) {
		if it["key"].(string) == "bad" {
			t.Fatal("SECURITY: an unknown-label row must never persist (fail closed)")
		}
	}
}

// ---- import re-scrubs content (redact-before-store re-runs) ----------------------

func TestMemoryImport_ReScrubsContent(t *testing.T) {
	priv, pub := portKeys(t)
	h := newHarnessWith(t, WithMemoryPortabilityKeys(priv, pub))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")

	// Craft a bundle whose content carries a secret-shaped token the redactor scrubs.
	raw := "anthropic key sk-ant-abcdefghijklmnopqrstuv leaked here"
	bundle := buildSignedBundle(t, priv, tenant.String(), "agent-3", memPortScope{}, []memPortRow{
		{AgentRef: "agent-3", Key: "k", Content: raw, Classification: classInternal},
	})
	if r := h.doRawImport(editor, tenant, bundle); r.code != http.StatusOK {
		t.Fatalf("import = %d %s", r.code, r.raw)
	}
	adminTok := h.roleToken(admin, tenant, "adm@acme.com", "admin")
	all := h.do("GET", "/v1/m/knowledge/memory/all?agent_ref=agent-3", adminTok, nil, tenantHdr(tenant))
	items := listItems(all)
	if len(items) != 1 {
		t.Fatalf("want 1 imported entry, got %d", len(items))
	}
	if got := items[0]["content"].(string); strings.Contains(got, "sk-ant-abcdefghijklmnopqrstuv") {
		t.Fatalf("import did not re-scrub content: %q", got)
	}
}
