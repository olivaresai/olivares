// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	claudewif "github.com/olivaresai/olivares/connectors/claude-wif"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

// --- fake seams ---------------------------------------------------------------

type fakeObserved struct {
	content map[string][]byte
	scope   string
	err     error
}

func (f *fakeObserved) Observed(_ context.Context, _ model.TenantID, surface string) ([]governance.ObservedConfig, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.content == nil {
		return nil, nil
	}
	c, ok := f.content[surface]
	if !ok {
		return nil, nil
	}
	sc := f.scope
	if sc == "" {
		sc = "host-test"
	}
	return []governance.ObservedConfig{{Scope: sc, Content: c}}, nil
}

type fakeThreads struct {
	events map[string][]governance.ThreadEvent
	err    error
}

func (f *fakeThreads) ThreadEvents(_ context.Context, _ model.TenantID, sessionID string) ([]governance.ThreadEvent, bool, error) {
	if f.err != nil {
		return nil, true, f.err
	}
	if f.events == nil {
		return nil, false, nil
	}
	e, ok := f.events[sessionID]
	return e, ok, nil
}

type fakeWif struct {
	graph *claudewif.WIFGraph
}

func (f *fakeWif) WifGraph(_ context.Context, _ model.TenantID) (claudewif.WIFGraph, bool) {
	if f.graph == nil {
		return claudewif.WIFGraph{}, false
	}
	return *f.graph, true
}

// tenantAdmin sets up a tenant and returns its id + an admin token for it.
func (h *harness) tenantAdmin() (model.TenantID, string) {
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, tok := h.roleUser(admin, tenant, "boss@acme.io", "admin")
	return tenant, tok
}

// --- B: managed-* authoring ---------------------------------------------------

func TestManagedSettingsValidate(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	hdr := tenantHdr(tenant)

	ok := h.do("POST", "/v1/m/claude-policy/managed-settings/validate", tok, map[string]any{
		"content": `{"permissions":{"defaultMode":"plan"},"forceRemoteSettingsRefresh":true}`,
	}, hdr)
	if ok.code != http.StatusOK || ok.body["ok"] != true {
		t.Fatalf("valid doc should validate ok: %d %s", ok.code, ok.raw)
	}
	bad := h.do("POST", "/v1/m/claude-policy/managed-settings/validate", tok, map[string]any{
		"content": `{"permissions":{"defaultMode":"yolo"}}`,
	}, hdr)
	if bad.code != http.StatusOK || bad.body["ok"] != false {
		t.Fatalf("invalid defaultMode should report ok=false: %d %s", bad.code, bad.raw)
	}
}

func TestManagedSettingsDryRunNoMerge(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	r := h.do("POST", "/v1/m/claude-policy/managed-settings/dry-run", tok, map[string]any{
		"content": `{"forceRemoteSettingsRefresh":true}`,
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("dry-run = %d %s", r.code, r.raw)
	}
	if !strings.Contains(r.raw, "do NOT merge") && !strings.Contains(r.raw, "no-merge") {
		t.Fatalf("dry-run must explain the no-merge precedence: %s", r.raw)
	}
	// Dry-run must NOT create any revision (no host/store write).
	v := h.do("GET", "/v1/m/claude-policy/managed-settings/versions", tok, nil, tenantHdr(tenant))
	if len(items(v)) != 0 {
		t.Fatalf("dry-run must not persist a revision; versions=%d", len(items(v)))
	}
}

func TestManagedSettingsPublishRevisionAndDrift(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	hdr := tenantHdr(tenant)
	// The host enforces NONE of the asserted lockdown → drift.
	h.observed.content = map[string][]byte{"managed-settings": []byte(`{}`)}
	h.observed.scope = "host-x"

	pub := h.do("POST", "/v1/m/claude-policy/managed-settings/publish", tok, map[string]any{
		"content": `{"forceRemoteSettingsRefresh":true}`,
	}, hdr)
	if pub.code != http.StatusOK {
		t.Fatalf("publish = %d %s", pub.code, pub.raw)
	}
	if rev, _ := pub.body["revision"].(float64); rev != 1 {
		t.Fatalf("first publish should be revision 1: %s", pub.raw)
	}
	// Distribution went to the seam (no host write), and drift was returned.
	if pub.body["distribution"] != "seam-pending" {
		t.Fatalf("distribution should be seam-pending (no host write): %s", pub.raw)
	}
	drift, _ := pub.body["drift"].([]any)
	if len(drift) == 0 {
		t.Fatalf("publish must return the PERMITTED-vs-OBSERVED drift finding: %s", pub.raw)
	}
	d0, _ := drift[0].(map[string]any)
	if d0["detail_hash"] == nil || d0["detail_hash"] == "" {
		t.Fatalf("drift finding must carry a redacted detail_hash, never a payload: %v", d0)
	}

	// A second publish is an immutable new revision.
	pub2 := h.do("POST", "/v1/m/claude-policy/managed-settings/publish", tok, map[string]any{
		"content": `{"allowManagedHooksOnly":true}`,
	}, hdr)
	if rev, _ := pub2.body["revision"].(float64); rev != 2 {
		t.Fatalf("second publish should be revision 2: %s", pub2.raw)
	}
	// versions lists both; getVersion returns the content.
	v := h.do("GET", "/v1/m/claude-policy/managed-settings/versions", tok, nil, hdr)
	if len(items(v)) != 2 {
		t.Fatalf("expected 2 revisions, got %d", len(items(v)))
	}
	g := h.do("GET", "/v1/m/claude-policy/managed-settings/versions/1", tok, nil, hdr)
	if g.code != http.StatusOK || !strings.Contains(g.raw, "forceRemoteSettingsRefresh") {
		t.Fatalf("getVersion(1) must return the stored content: %d %s", g.code, g.raw)
	}

	// The privileged publish is on the audit ledger.
	if !contains(h.auditActions(tenant), "governance.claude_policy.publish") {
		t.Fatal("publish must be audited")
	}
}

func TestManagedSettingsPublishRBAC(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, viewerTok := h.roleUser(admin, tenant, "viewer@acme.io", "viewer")
	hdr := tenantHdr(tenant)

	// A viewer can validate (read tier) but NOT publish (admin tier).
	val := h.do("POST", "/v1/m/claude-policy/managed-settings/validate", viewerTok, map[string]any{"content": `{}`}, hdr)
	if val.code != http.StatusOK {
		t.Fatalf("viewer validate = %d %s", val.code, val.raw)
	}
	pub := h.do("POST", "/v1/m/claude-policy/managed-settings/publish", viewerTok, map[string]any{"content": `{"forceRemoteSettingsRefresh":true}`}, hdr)
	if pub.code != http.StatusForbidden {
		t.Fatalf("viewer publish must be 403, got %d %s", pub.code, pub.raw)
	}
}

func TestManagedSettingsPublishDenyClosed(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	hdr := tenantHdr(tenant)

	// Invalid document → 400, NOTHING published.
	bad := h.do("POST", "/v1/m/claude-policy/managed-settings/publish", tok, map[string]any{"content": `{"permissions":{"defaultMode":"yolo"}}`}, hdr)
	if bad.code != http.StatusBadRequest {
		t.Fatalf("invalid publish must be 400, got %d %s", bad.code, bad.raw)
	}
	// Inline credential → 400 (minimal-data backstop).
	key := h.do("POST", "/v1/m/claude-policy/managed-settings/publish", tok, map[string]any{"content": `{"x":"sk-ant-leak"}`}, hdr)
	if key.code != http.StatusBadRequest {
		t.Fatalf("inline credential must be 400, got %d %s", key.code, key.raw)
	}
	v := h.do("GET", "/v1/m/claude-policy/managed-settings/versions", tok, nil, hdr)
	if len(items(v)) != 0 {
		t.Fatalf("deny-closed: a rejected publish must persist nothing; versions=%d", len(items(v)))
	}
}

func TestHooksValidatePermissionRequestCorrection(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	// applyPermissionRule under PreToolUse is the stale/incorrect schema.
	r := h.do("POST", "/v1/m/claude-policy/hooks/validate", tok, map[string]any{
		"content": `{"hooks":{"PreToolUse":[{"hooks":[{"applyPermissionRule":true}]}]}}`,
	}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["ok"] != false {
		t.Fatalf("applyPermissionRule under PreToolUse must be flagged: %d %s", r.code, r.raw)
	}
	if !strings.Contains(r.raw, "PermissionRequest-only") {
		t.Fatalf("diagnostic must name the verified correction: %s", r.raw)
	}
}

// --- C: Cedar/OPA PDP authoring ----------------------------------------------

func TestPdpValidateCedar(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	hdr := tenantHdr(tenant)
	good := h.do("POST", "/v1/m/governance/pdp/validate", tok, map[string]any{
		"engine": "cedar", "source": `forbid(principal, action, resource) when { context.permission == "agent:write" };`,
	}, hdr)
	if good.code != http.StatusOK || good.body["ok"] != true {
		t.Fatalf("valid cedar should validate ok: %d %s", good.code, good.raw)
	}
	bad := h.do("POST", "/v1/m/governance/pdp/validate", tok, map[string]any{
		"engine": "cedar", "source": `this is not cedar {{{`,
	}, hdr)
	if bad.code != http.StatusOK || bad.body["ok"] != false {
		t.Fatalf("invalid cedar must report ok=false: %d %s", bad.code, bad.raw)
	}
}

func TestPdpExplainGrantSemantics(t *testing.T) {
	// the authored Cedar policy is a SCOPED-GRANT policy, so the dry-run is
	// three-valued — a permit GRANTS, a forbid RESTRICTS, an empty policy ABSTAINS.
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	hdr := tenantHdr(tenant)
	req := map[string]any{
		"principal":  map[string]any{"kind": "token", "id": "tok1"},
		"permission": "agent:write",
		"resource":   map[string]any{"kind": "agent"},
	}
	// A forbid matches → denied (the policy RESTRICTS — overrides any grant).
	deny := h.do("POST", "/v1/m/governance/pdp/explain", tok, map[string]any{
		"engine": "cedar", "source": `forbid(principal, action, resource) when { context.permission == "agent:write" };`,
		"request": req,
	}, hdr)
	if deny.code != http.StatusOK || deny.body["allow"] != false {
		t.Fatalf("forbid should deny: %d %s", deny.code, deny.raw)
	}
	if reason, _ := deny.body["reason"].(string); !strings.Contains(reason, "RESTRICTS") {
		t.Fatalf("forbid reason must state the policy restricts: %q", reason)
	}
	// A matching permit → GRANT: the policy positively authorizes within scope (the
	// Capability the old deny-only overlay could not express).
	grant := h.do("POST", "/v1/m/governance/pdp/explain", tok, map[string]any{
		"engine": "cedar", "source": `permit(principal, action == Action::"agent:write", resource);`,
		"request": req,
	}, hdr)
	if grant.code != http.StatusOK || grant.body["allow"] != true {
		t.Fatalf("matching permit should grant: %d %s", grant.code, grant.raw)
	}
	if reason, _ := grant.body["reason"].(string); !strings.Contains(reason, "GRANTS") {
		t.Fatalf("permit reason must state the policy grants: %q", reason)
	}
	// An empty policy → abstain (no grant, no restriction): allow=true (RBAC governs).
	abstain := h.do("POST", "/v1/m/governance/pdp/dry-run", tok, map[string]any{
		"engine": "cedar", "source": ``, "request": req,
	}, hdr)
	if abstain.code != http.StatusOK || abstain.body["allow"] != true {
		t.Fatalf("empty policy → abstain (no restriction): %d %s", abstain.code, abstain.raw)
	}
	if reason, _ := abstain.body["reason"].(string); !strings.Contains(reason, "abstains") {
		t.Fatalf("empty-policy reason must state the policy abstains: %q", reason)
	}
}

func TestPdpPublishRecomposeDenyClosed(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	hdr := tenantHdr(tenant)
	ctx := context.Background()
	req := auth.Request{
		Principal:  auth.Principal{Kind: auth.KindToken, CredID: "tok1"},
		Permission: "agent:write",
		Tenant:     tenant,
		Resource:   auth.ResourceAttrs{Kind: "agent"},
	}

	// Before publish: no authored restriction.
	if dec, _ := h.gov.Evaluator().Evaluate(ctx, req); !dec.Allow {
		t.Fatalf("no authored policy → no restriction, got deny: %s", dec.Reason)
	}
	// Publish a compiling forbid → it recomposes the live evaluator and now RESTRICTS.
	pub := h.do("POST", "/v1/m/governance/pdp/publish", tok, map[string]any{
		"engine": "cedar", "source": `forbid(principal, action, resource) when { context.permission == "agent:write" };`,
	}, hdr)
	if pub.code != http.StatusOK || pub.body["active"] != true {
		t.Fatalf("cedar publish should activate: %d %s", pub.code, pub.raw)
	}
	if dec, _ := h.gov.Evaluator().Evaluate(ctx, req); dec.Allow {
		t.Fatal("after publish the authored forbid must restrict the request")
	}
	// Publish an INVALID cedar → 400, NOT activated; the prior policy still stands.
	bad := h.do("POST", "/v1/m/governance/pdp/publish", tok, map[string]any{
		"engine": "cedar", "source": `forbid(((( garbage`,
	}, hdr)
	if bad.code != http.StatusBadRequest {
		t.Fatalf("invalid cedar publish must be 400, got %d %s", bad.code, bad.raw)
	}
	if dec, _ := h.gov.Evaluator().Evaluate(ctx, req); dec.Allow {
		t.Fatal("deny-closed: a failed publish must leave the prior policy in force")
	}
	// The activation is audited and versioned.
	if !contains(h.auditActions(tenant), "governance.pdp.publish") {
		t.Fatal("pdp publish must be audited")
	}
	v := h.do("GET", "/v1/m/governance/pdp/versions", tok, nil, hdr)
	if len(items(v)) != 1 {
		t.Fatalf("only the compiling policy should be versioned, got %d", len(items(v)))
	}
}

// TestPdpBootReloadDurableActivation proves the persisted active=true is HONEST: a
// fresh module over the SAME store starts with an empty overlay (no restriction) and,
// after ReloadActivePDP (the boot-reload the composition root runs per tenant),
// enforces the stored active Cedar policy — so activation survives a restart.
func TestPdpBootReloadDurableActivation(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	ctx := context.Background()
	req := auth.Request{
		Principal:  auth.Principal{Kind: auth.KindToken, CredID: "tok1"},
		Permission: "agent:write", Tenant: tenant, Resource: auth.ResourceAttrs{Kind: "agent"},
	}
	pub := h.do("POST", "/v1/m/governance/pdp/publish", tok, map[string]any{
		"engine": "cedar", "source": `forbid(principal, action, resource) when { context.permission == "agent:write" };`,
	}, tenantHdr(tenant))
	if pub.code != http.StatusOK {
		t.Fatalf("publish: %d %s", pub.code, pub.raw)
	}
	// A fresh module over the SAME store = a restarted process: empty overlay.
	fresh := governance.New(governance.WithClock(h.clk))
	fresh.UseData(api.NewModuleData(h.st))
	if dec, _ := fresh.Evaluator().Evaluate(ctx, req); !dec.Allow {
		t.Fatal("a fresh module before boot-reload must impose no restriction")
	}
	if err := fresh.ReloadActivePDP(ctx, tenant); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if dec, _ := fresh.Evaluator().Evaluate(ctx, req); dec.Allow {
		t.Fatal("after boot-reload the stored active cedar policy must restrict (active=true is durable)")
	}
}

func TestPdpTestsReflectHonest(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	r := h.do("GET", "/v1/m/governance/pdp/tests?engine=cedar", tok, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["available"] != false {
		t.Fatalf("tests must reflect honestly (available=false, no fabricated pass): %d %s", r.code, r.raw)
	}
}

// --- D: tool confirmation -----------------------------------------------------

func TestToolConfirmationS24AndAudit(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	hdr := tenantHdr(tenant)

	conf := h.do("POST", "/v1/m/claude-agents/sessions/sess-1/tool-confirmation", tok, map[string]any{
		"tool_use_id": "tool-1", "result": "deny", "deny_message": "blocked: writes outside scope",
	}, hdr)
	if conf.code != http.StatusOK || conf.body["result"] != "deny" {
		t.Fatalf("tool-confirmation = %d %s", conf.code, conf.raw)
	}
	fp, _ := conf.body["tool_hash"].(string)
	if len(fp) != 64 { // sha256 hex
		t.Fatalf("confirmation must record a redacted fingerprint, got %q", fp)
	}
	// A managed-agent approval exists, keyed on the REDACTED fingerprint
	// (never the raw session|tool), and is terminally rejected.
	apps := h.do("GET", "/v1/m/governance/approvals", tok, nil, hdr)
	found := false
	for _, it := range items(apps) {
		m, _ := it.(map[string]any)
		if m["subject_kind"] == "anthropic.managed_agent" {
			found = true
			if m["subject_ref"] == "sess-1|tool-1" {
				t.Fatal("subject_ref must be a redacted hash, never the raw session|tool")
			}
			if m["status"] != "rejected" {
				t.Fatalf("deny → rejected, got %v", m["status"])
			}
		}
	}
	if !found {
		t.Fatalf("tool-confirmation must bind to an approval: %s", apps.raw)
	}
	if !contains(h.auditActions(tenant), "governance.agent.tool_confirmation") {
		t.Fatal("tool-confirmation must be audited")
	}
	// Re-confirming the same tool is a conflict (already decided).
	again := h.do("POST", "/v1/m/claude-agents/sessions/sess-1/tool-confirmation", tok, map[string]any{
		"tool_use_id": "tool-1", "result": "allow",
	}, hdr)
	if again.code != http.StatusConflict {
		t.Fatalf("re-confirmation must conflict, got %d %s", again.code, again.raw)
	}
}

func TestToolConfirmationValidation(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	hdr := tenantHdr(tenant)
	for _, body := range []map[string]any{
		{"tool_use_id": "", "result": "allow"},
		{"tool_use_id": "x", "result": "maybe"},
	} {
		r := h.do("POST", "/v1/m/claude-agents/sessions/s/tool-confirmation", tok, body, hdr)
		if r.code != http.StatusBadRequest {
			t.Fatalf("invalid confirmation must be 400, got %d for %v", r.code, body)
		}
	}
}

func TestThreadEvents(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	hdr := tenantHdr(tenant)
	// Unwired → honest empty list (200, not a pending seam).
	empty := h.do("GET", "/v1/m/claude-agents/sessions/none/events", tok, nil, hdr)
	if empty.code != http.StatusOK || len(items(empty)) != 0 {
		t.Fatalf("no ingest → empty 200, got %d %s", empty.code, empty.raw)
	}
	h.threads.events = map[string][]governance.ThreadEvent{
		"sess-1": {{Type: "agent.tool_use", ToolName: "Bash", ToolUseID: "tool-1"}},
	}
	got := h.do("GET", "/v1/m/claude-agents/sessions/sess-1/events", tok, nil, hdr)
	if got.code != http.StatusOK || len(items(got)) != 1 {
		t.Fatalf("wired events should be served: %d %s", got.code, got.raw)
	}
}

// --- E: WIF graph -------------------------------------------------------------

func TestWifGraphNoKeyMaterial(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	hdr := tenantHdr(tenant)
	h.wif.graph = &claudewif.WIFGraph{
		Issuers: []claudewif.WIFIssuer{{ID: "fdis_1", JWKSMode: "discovery", CACertConfigured: true}},
		Rules: []claudewif.WIFRule{{
			RuleID: "fdrl_1", ServiceAccountID: "svac_1", IssuerID: "fdis_1",
			OAuthScope: "workspace:developer", SubjectPrefix: "repo:acme/", CACertConfigured: true,
		}},
		ServiceAccounts: []claudewif.WIFServiceAccount{{ID: "svac_1", OAuthScope: "workspace:developer"}},
		KeyShadow:       &claudewif.WIFKeyShadow{Present: true, Var: "ANTHROPIC_API_KEY"},
	}
	r := h.do("GET", "/v1/m/identity/wif", tok, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("wif = %d %s", r.code, r.raw)
	}
	if !strings.Contains(r.raw, `"ca_cert_configured":true`) {
		t.Fatalf("ca_cert_configured boolean must be present: %s", r.raw)
	}
	for _, banned := range []string{"sk-ant-", "BEGIN CERTIFICATE", "PRIVATE KEY", "ca_cert_pem"} {
		if strings.Contains(r.raw, banned) {
			t.Fatalf("WIF response leaked %q: %s", banned, r.raw)
		}
	}
	// The privileged read self-audits.
	if !contains(h.auditActions(tenant), "governance.identity.wif.read") {
		t.Fatal("WIF read must self-audit")
	}
}

func TestWifGraphEmptyIsNotPendingSeam(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	// No WIF data wired → honest empty graph at 200 (NOT 404/501 → no pending seam).
	r := h.do("GET", "/v1/m/identity/wif", tok, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("empty WIF must be 200 (not a pending seam), got %d %s", r.code, r.raw)
	}
	if r.body["issuers"] == nil || r.body["rules"] == nil || r.body["service_accounts"] == nil {
		t.Fatalf("empty graph must still carry empty arrays: %s", r.raw)
	}
}
