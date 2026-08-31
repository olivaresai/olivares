// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// tenantreaders_test.go is the per-site half: one test per reader of a
// configured or decision-carried tenant, demonstrating that each one REFUSES a present
// but invalid tenant. The inference proxy (the sixth reader, and the one that used to
// widen) has its own file, inferenceproxytenant_test.go.
//
// The system-tenant leg is asserted at every site because model.ParseTenantID returns
// it with a NIL error (core/model/ids.go:56-58) and it is non-zero by design, so it
// slips through any check built only from `err` and `IsZero`. Two of the six sites were
// built exactly that way.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// ---- site 1: codexhookpepserver.go — the Codex hook PEP mount ----

// TestCodexPEPRefusesTheSystemTenant pins the reserved-tenant leg of site 1. The mount
// already refused it; this test exists so a future rewrite of that predicate through the
// shared helper cannot quietly drop the leg.
func TestCodexPEPRefusesTheSystemTenant(t *testing.T) {
	// CONTROL: a business tenant with the same inputs mounts. Asserting only "no error"
	// would leave the control green on a (nil, nil) return, which is NOT a mount — an
	// external contrast caught exactly that gap in the first version of this test.
	writeCodexPEPConfig(t, `{"tenant":"`+model.NewTenantID().String()+`"}`)
	srv, err := buildCodexHookPEPServer(&engine{}, sessions.New(), discardLog())
	if err != nil {
		t.Fatalf("control: a valid business tenant must mount; got %v", err)
	}
	if srv == nil {
		t.Fatal("control: a valid business tenant must produce a SERVER; a nil server is not a mount")
	}

	writeCodexPEPConfig(t, `{"tenant":"`+model.SystemTenantID.String()+`"}`)
	if _, err := buildCodexHookPEPServer(&engine{}, sessions.New(), discardLog()); err == nil {
		t.Error("the reserved system tenant must not back a governed Codex PEP surface")
	}
}

// ---- site 2: mcpgateway.go:271 — the MCP resource server's single tenant ----

// namesTheOffendingValue is how these tests tell a TENANT-VALIDITY refusal apart from
// every other mount-time refusal. Matching the word "tenant" is NOT enough: the
// kill-switch refusal ("a kill-switch mount with no tenant") contains it too, and an
// earlier draft of this file went green on exactly that. The configured value itself
// appears only in the message the shared helper builds.
func namesTheOffendingValue(err error, value string) bool {
	return err != nil && strings.Contains(err.Error(), value)
}

// TestMCPGatewayRefusesTheConfiguredSystemTenant is the second D2 closer, at a site the
// Brief listed but did not accuse: mcpgateway.go:271 checked ONLY the parse error,
// so the reserved system tenant was accepted as the resource server's enforcement
// anchor — the tenant the kill switch and the approval gate key on.
func TestMCPGatewayRefusesTheConfiguredSystemTenant(t *testing.T) {
	eng := &engine{killSwitch: noStopGuard{}}
	base := func() *mcpGatewayConfig {
		return &mcpGatewayConfig{
			Resource:             "https://mcp.example.com/mcp",
			AuthorizationServers: []string{"https://as.example.com"},
			UpstreamURL:          "https://upstream.example.com/mcp",
			Tools:                []mcpc.ToolPolicy{{Name: "search", RequiredScope: "tools:read"}},
		}
	}

	// Premise: the system tenant parses cleanly, so a parse-error-only check admits it.
	if _, err := model.ParseTenantID(model.SystemTenantID.String()); err != nil {
		t.Fatalf("premise: the system tenant must parse without error; got %v", err)
	}

	// CONTROL: a business tenant must not be refused FOR THE TENANT (other mount-time
	// refusals are allowed and are not what this test measures).
	ctrl := base()
	ctrl.Tenant = model.NewTenantID().String()
	if _, _, err := buildMCPResourceServer(eng, ctrl, discardLogger()); namesTheOffendingValue(err, ctrl.Tenant) {
		t.Fatalf("control: a valid business tenant must not be refused for its tenant; got %v", err)
	}

	sys := base()
	sys.Tenant = model.SystemTenantID.String()
	_, _, err := buildMCPResourceServer(eng, sys, discardLogger())
	if err == nil {
		t.Fatal("the reserved system tenant must not become the MCP resource server's enforcement anchor")
	}
	if !namesTheOffendingValue(err, sys.Tenant) {
		t.Errorf("the refusal must be the TENANT refusal naming %q; got %q", sys.Tenant, err)
	}
}

// TestMCPGatewayRefusesTheConfiguredNilUUID: the all-zero UUID parses cleanly too, and
// left rsTenant "zero", which silently disabled the approval gate instead of refusing.
func TestMCPGatewayRefusesTheConfiguredNilUUID(t *testing.T) {
	eng := &engine{killSwitch: noStopGuard{}}
	cfg := &mcpGatewayConfig{
		Resource:             "https://mcp.example.com/mcp",
		AuthorizationServers: []string{"https://as.example.com"},
		UpstreamURL:          "https://upstream.example.com/mcp",
		Tools:                []mcpc.ToolPolicy{{Name: "search", RequiredScope: "tools:read"}},
		Tenant:               "00000000-0000-0000-0000-000000000000",
	}
	_, _, err := buildMCPResourceServer(eng, cfg, discardLogger())
	if err == nil {
		t.Fatal("a present but unset tenant must be refused, not treated as 'no tenant configured'")
	}
	// The message assertion is what keeps this honest: without it the test would be
	// green on the kill-switch refusal, which also says "tenant".
	if !namesTheOffendingValue(err, cfg.Tenant) {
		t.Errorf("the refusal must be the TENANT refusal naming %q; got %q", cfg.Tenant, err)
	}
}

// ---- site 3: mcpgateway.go:663 enforcedTenant — the decision tenant of an ENFORCED op ----

// TestEnforcedTenantRefusesAnInvalidDecisionTenant pins: evidence is never
// re-attributed to the configured tenant because the decision named something broken.
func TestEnforcedTenantRefusesAnInvalidDecisionTenant(t *testing.T) {
	anchor := model.NewTenantID()
	a := mcpGateAuditor{log: discardLogger(), tenant: anchor}

	// CONTROL 1: an EMPTY decision tenant legitimately inherits the anchor.
	if got, ok := a.enforcedTenant(""); !ok || got != anchor {
		t.Fatalf("control: an empty decision tenant must inherit the anchor; got (%q,%v)", got, ok)
	}
	// CONTROL 2: the matching tenant resolves.
	if got, ok := a.enforcedTenant(anchor.String()); !ok || got != anchor {
		t.Fatalf("control: the matching decision tenant must resolve; got (%q,%v)", got, ok)
	}

	for _, bad := range []string{"not-a-uuid", model.SystemTenantID.String(), "00000000-0000-0000-0000-000000000000", model.NewTenantID().String()} {
		if got, ok := a.enforcedTenant(bad); ok {
			t.Errorf("enforcedTenant(%q) resolved to %q; a non-matching or invalid decision tenant must refuse", bad, got)
		}
	}
}

// ---- site 4: mcpgateway.go:821 bestEffortAnchor — the decision tenant of a denial ----

// TestBestEffortAnchorRefusesTheSystemDecisionTenant closes the leg site 4 was missing.
// bestEffortAnchor is reached on a branch that does NOT go through enforcedTenant
// (mcpgateway.go:689 vs :695), so its own check is the only one on that path. It looked
// at the parse error alone, so a decision naming the reserved system tenant would have
// been anchored under it — evidence for a business action filed outside every business
// boundary.
//
// The observable is the anchor's tenant argument, so the test drives a recording store
// double: with a nil store the function returns early and would be green for free.
func TestBestEffortAnchorRefusesTheSystemDecisionTenant(t *testing.T) {
	anchor := model.NewTenantID()

	// CONTROL: a decision naming the anchor itself does reach the store.
	ctrl := &recordingAnchorStore{}
	ac := mcpGateAuditor{log: discardLogger(), store: ctrl, tenant: anchor}
	ac.bestEffortAnchor(context.Background(), mcpc.ToolDecision{Tool: "search", Tenant: anchor.String()})
	if len(ctrl.tenants) == 0 {
		t.Fatal("control: a valid decision tenant must reach the ledger; the assertion below would be vacuous")
	}

	// An UNUSABLE decision tenant must produce NO anchor at all — the loud gap.
	// Asserting only "not under the system tenant" would be too weak: falling back to
	// the configured anchor is precisely the silent re-attribution forbade, and a
	// mutant that dropped the check would survive such an assertion.
	for _, bad := range []string{model.SystemTenantID.String(), "not-a-uuid", "00000000-0000-0000-0000-000000000000"} {
		sink := &recordingAnchorStore{}
		as := mcpGateAuditor{log: discardLogger(), store: sink, tenant: anchor}
		as.bestEffortAnchor(context.Background(), mcpc.ToolDecision{Tool: "search", Tenant: bad})
		if len(sink.tenants) != 0 {
			t.Errorf("decision tenant %q: anchored under %v; an unusable decision tenant must be a loud evidence GAP, never re-attributed to the configured anchor", bad, sink.tenants)
		}
	}
}

// recordingAnchorStore records the tenant every Mutate is pinned to and goes no
// further. The tenant argument IS the observable under test (which tenant the decision
// was filed under), so the double never needs to open a Scope; returning an error keeps
// it from pretending to have written anything.
type recordingAnchorStore struct{ tenants []model.TenantID }

var _ store.Store = (*recordingAnchorStore)(nil)

func (s *recordingAnchorStore) Mutate(_ context.Context, tenant model.TenantID, _ func(store.Scope) error) error {
	s.tenants = append(s.tenants, tenant)
	return errAnchorStoreStub
}

func (s *recordingAnchorStore) View(_ context.Context, tenant model.TenantID, _ func(store.Scope) error) error {
	s.tenants = append(s.tenants, tenant)
	return errAnchorStoreStub
}

// Custody records the tenant too. The anchor path uses Mutate today, but a double
// that observed only the doors it currently expects would go blind the moment the
// work moved — and evidence anchoring is exactly the kind of work that belongs on
// the custody door (core/audit checkpointing already moved there).
func (s *recordingAnchorStore) Custody(_ context.Context, tenant model.TenantID, _ func(store.CustodyScope) error) error {
	s.tenants = append(s.tenants, tenant)
	return errAnchorStoreStub
}

// Export records the tenant too, for the same reason Custody does.
func (s *recordingAnchorStore) Export(_ context.Context, tenant model.TenantID, _ func(store.ExportScope) error) error {
	s.tenants = append(s.tenants, tenant)
	return errAnchorStoreStub
}

func (s *recordingAnchorStore) System(context.Context, func(store.SystemScope) error) error {
	return errAnchorStoreStub
}
func (s *recordingAnchorStore) AuthView(context.Context, func(store.AuthScope) error) error {
	return errAnchorStoreStub
}
func (s *recordingAnchorStore) AuthMutate(context.Context, func(store.AuthScope) error) error {
	return errAnchorStoreStub
}
func (s *recordingAnchorStore) Engine() store.Engine       { return store.Engine("") }
func (s *recordingAnchorStore) Ping(context.Context) error { return nil }
func (s *recordingAnchorStore) Leader() store.LeaderElector {
	return nil
}
func (s *recordingAnchorStore) Close() error { return nil }

var errAnchorStoreStub = errors.New("recordingAnchorStore: observation only")

// ---- site 5: voicewebhook.go:53 — the voice call receiver ----

// TestVoiceCallModuleConfigRefusesInvalidTenants pins site 5 across all three invalid
// shapes, with the absent case as the control that must keep behaving as before.
func TestVoiceCallModuleConfigRefusesInvalidTenants(t *testing.T) {
	// CONTROL: a business tenant mounts.
	good := model.NewTenantID()
	if cc, ok := voiceCallModuleConfig(voiceCallConfig{Tenant: good.String()}, discardLogger()); !ok || cc.Tenant != good {
		t.Fatalf("control: a valid tenant must mount the receiver; got (%+v,%v)", cc, ok)
	}

	for _, bad := range []string{"not-a-uuid", model.SystemTenantID.String(), "00000000-0000-0000-0000-000000000000", "", "   "} {
		if _, ok := voiceCallModuleConfig(voiceCallConfig{Tenant: bad}, discardLogger()); ok {
			t.Errorf("voiceCallModuleConfig(%q) mounted; a receiver whose events cannot be attributed must not mount", bad)
		}
	}
}

// TestVoiceCallInvalidTenantIsLoudButAbsentIsNot pins the only observable that tells
// site 5's two refusal legs apart. Not mounting is the SAME return value for an absent
// tenant and for a typo, so the return value alone cannot prove the invalid leg exists —
// a mutant that deleted it survived this file's first version. The log is the difference
// that matters operationally: a typo must name the offending value so the operator can
// find it, while a deliberately unconfigured receiver must not shout.
func TestVoiceCallInvalidTenantIsLoudButAbsentIsNot(t *testing.T) {
	logOf := func(cfg voiceCallConfig) string {
		var buf bytes.Buffer
		if _, ok := voiceCallModuleConfig(cfg, slog.New(slog.NewTextHandler(&buf, nil))); ok {
			t.Fatalf("premise: %+v must not mount", cfg)
		}
		return buf.String()
	}

	const typo = "not-a-uuid"
	got := logOf(voiceCallConfig{Tenant: typo, WebhookSecret: "whsec_x"})
	if !strings.Contains(got, typo) {
		t.Errorf("a present but invalid tenant must be logged naming the offending value %q; got %q", typo, got)
	}
	if !strings.Contains(got, "ERROR") {
		t.Errorf("an operator typo is an ERROR, not a warning; got %q", got)
	}

	// The system tenant is the leg no `err`/`IsZero` check would have caught. It must be
	// logged at the SAME severity as any other operator typo: pinning only the value
	// would stay green if the reserved tenant were downgraded to a WARN.
	sys := logOf(voiceCallConfig{Tenant: model.SystemTenantID.String(), WebhookSecret: "whsec_x"})
	if !strings.Contains(sys, model.SystemTenantID.String()) {
		t.Errorf("a configured system tenant must be logged naming it; got %q", sys)
	}
	if !strings.Contains(sys, "ERROR") {
		t.Errorf("the reserved system tenant is an operator typo like any other and must be an ERROR; got %q", sys)
	}

	// CONTROL: an ABSENT tenant is not an operator typo and must not be logged as one.
	if absent := logOf(voiceCallConfig{Tenant: "", WebhookSecret: "whsec_x"}); strings.Contains(absent, "ERROR") {
		t.Errorf("an absent tenant is a legitimate configuration and must not log an ERROR; got %q", absent)
	}
}
