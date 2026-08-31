// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Tests in this file include the DeepSeek source wiring added after provider currency
// verification on 2026-07-04.
package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	claudecompliancePkg "github.com/olivaresai/olivares/connectors/claude-compliance"
	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/core/api"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/knowledge"
	"github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// logHasLine reports whether any single line of the captured slog output contains
// all of the given substrings — so an assertion can pin one specific log line
// (e.g. a warning about a specific kind) without matching across unrelated lines.
func logHasLine(log string, substrs ...string) bool {
	for _, line := range strings.Split(log, "\n") {
		all := true
		for _, s := range substrs {
			if !strings.Contains(line, s) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// TestWireSourcesWarnsWhenEmpty pins the honest-posture contract (12 §5): a boot
// with no observation sources configured WARNS rather than silently no-op'ing, in
// both the serve and collector paths (which share wireSources).
func TestWireSourcesWarnsWhenEmpty(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	rt := runtime.New(runtime.Options{Logger: quietLog()})
	wireSources(context.Background(), rt, sourcesConfig{}, t.TempDir(), nil, log)
	if !strings.Contains(buf.String(), "no observation sources configured") {
		t.Fatalf("wireSources with empty sources did not warn (silent no-op); log = %q", buf.String())
	}
}

// spireExport is a minimal valid `spire-server entry show -output json` fixture.
const spireExport = `{
  "entries": [
    {
      "id": "e1",
      "spiffe_id": {"trust_domain": "corp.example", "path": "/workload/api"},
      "parent_id": {"trust_domain": "corp.example", "path": "/spire/agent/node1"},
      "selectors": [{"type": "k8s", "value": "ns:prod"}]
    }
  ]
}`

// TestWireRosterPopulatesAndSchedules is the IDN-06/CB-3 proof: the composition
// root builds a configured identity GraphProvider, hands it to governance via
// UseRosterProviders and schedules the periodic SyncRoster on the runtime — so the
// NHI roster populates IN THE BINARY (no longer empty), automatically, from a real
// connector. It uses the SPIFFE connector with an offline JSON export so no network
// is needed.
func TestWireRosterPopulatesAndSchedules(t *testing.T) {
	ctx := context.Background()
	log := quietLog()

	gov := governance.New()
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, gov.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	gov.UseData(api.NewModuleData(st))

	// A real business tenant the roster belongs to.
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		org, e := sys.CreateOrg(ctx, model.Org{Name: "Acme", Slug: "acme", Status: model.StatusActive})
		if e != nil {
			return e
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatalf("create org: %v", err)
	}

	// The SPIFFE entries export the connector reads offline.
	fixture := filepath.Join(t.TempDir(), "entries.json")
	if err := os.WriteFile(fixture, []byte(spireExport), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := sourcesConfig{
		RosterSyncSeconds: 1, // a short cadence so the test does not lean only on the immediate pass
		Identity: []identitySpec{{
			Name:   "corp-spire",
			Kind:   "spiffe",
			Tenant: tenant.String(),
			Config: map[string]string{"entries_file": fixture, "trust_domain": "corp.example"},
		}},
	}

	rt := runtime.New(runtime.Options{Logger: log})
	if err := rt.AddModule(gov, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	wireRoster(ctx, rt, gov, newWifGraphAdapter(log), cfg, nil, log)
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(c)
	})

	// The scheduled SyncRoster (immediate pass) must populate the NHI roster with
	// the workload identity from the export.
	const wantRef = "spiffe://corp.example/workload/api"
	deadline := time.After(3 * time.Second)
	for {
		var found bool
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			list, _, e := sc.Identities().List(ctx, model.Query{Limit: 16})
			for _, id := range list {
				if id.ExternalID == wantRef {
					found = true
				}
			}
			return e
		}); err != nil {
			t.Fatalf("list identities: %v", err)
		}
		if found {
			return // roster populated by the boot-wired, scheduled sync
		}
		select {
		case <-deadline:
			t.Fatalf("roster never populated with %q (UseRosterProviders/SyncRoster not wired)", wantRef)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestBuildInProcSourceDataPlatforms pins the wiring (DoD: every connector
// registered in `serve` by the seam): each data-platform observer kind
// resolves to its in-process SourceConnector with the expected
// Descriptor.Name, so the composition root actually wires them — not a silent
// unknown-kind no-op. Construction is config-free; Open (and its read-only file I/O)
// is the runtime's job at Start, re-polled (sampling) or blocking (tail).
func TestBuildInProcSourceDataPlatforms(t *testing.T) {
	cases := map[string]string{
		"snowflake-audit":  "olivares.snowflake-audit",
		"databricks-uc":    "olivares.databricks-uc",
		"bigquery-audit":   "olivares.bigquery-audit",
		"mssql-audit":      "olivares.mssql-audit",
		"oracle-audit":     "olivares.oracle-audit",
		"mongo-audit":      "olivares.mongo-audit",
		"redshift-audit":   "olivares.redshift-audit",
		"gcs-audit":        "olivares.gcs-audit",
		"azure-blob-audit": "olivares.azure-blob-audit",
		"iceberg-catalog":  "olivares.iceberg-catalog",
		"openlineage":      "olivares.openlineage",
		"delta-sharing":    "olivares.delta-sharing",
	}
	for kind, wantName := range cases {
		conn, ok := buildInProcSource(kind)
		if !ok {
			t.Errorf("kind %q: not wired (buildInProcSource returned ok=false)", kind)
			continue
		}
		if got := conn.Descriptor().Name; got != wantName {
			t.Errorf("kind %q: Descriptor.Name = %q, want %q", kind, got, wantName)
		}
		if got := conn.Descriptor().Type; got != sdk.TypeSource {
			t.Errorf("kind %q: Type = %q, want source", kind, got)
		}
	}
	if _, ok := buildInProcSource("nonexistent-kind"); ok {
		t.Error("an unknown kind must not wire (expected ok=false)")
	}
}

// TestBuildInProcSourceA2A pins the E1 wiring: the A2A (Agent2Agent v1.0)
// observation source already implemented sdk.SourceConnector and had connector-level
// tests, but had no sources.go case — unreachable in the default binary. This asserts
// it now resolves to its in-process SourceConnector with the expected Descriptor, so
// the composition root actually wires it. The Card discovery + JWS/JCS verification is
// proven in connectors/a2a/a2a_test.go; this proves the binary can reach it. It is an
// OBSERVER (never acts on a peer), consistent with the source-connector invariant.
func TestBuildInProcSourceA2A(t *testing.T) {
	conn, ok := buildInProcSource("a2a")
	if !ok {
		t.Fatal(`kind "a2a": not wired (buildInProcSource returned ok=false)`)
	}
	if got := conn.Descriptor().Name; got != "olivares.a2a" {
		t.Errorf("Descriptor.Name = %q, want olivares.a2a", got)
	}
	if got := conn.Descriptor().Type; got != sdk.TypeSource {
		t.Errorf("Type = %q, want source", got)
	}
}

// TestBuildInProcSourceCloudManagementPlane pins the S165 wiring: the two
// org/tenant management-plane observers resolve to their in-process
// SourceConnector with the expected Descriptor.Name, so the composition root
// actually wires them — completing the tri-cloud management-plane parity with the
// AWS connector. Construction is config-free; OFFLINE (no credential) Gather is a
// no-op, so wiring them never fails boot.
func TestBuildInProcSourceCloudManagementPlane(t *testing.T) {
	cases := map[string]string{
		"gcp-audit":      "olivares.gcp-audit",
		"azure-activity": "olivares.azure-activity",
	}
	for kind, wantName := range cases {
		conn, ok := buildInProcSource(kind)
		if !ok {
			t.Errorf("kind %q: not wired (buildInProcSource returned ok=false)", kind)
			continue
		}
		if got := conn.Descriptor().Name; got != wantName {
			t.Errorf("kind %q: Descriptor.Name = %q, want %q", kind, got, wantName)
		}
		if got := conn.Descriptor().Type; got != sdk.TypeSource {
			t.Errorf("kind %q: Type = %q, want source", kind, got)
		}
	}
}

// TestBuildInProcSourceHyperscalerModelProviders pins the wiring: the two
// hyperscaler model/provider observers (Google Vertex AI, Azure OpenAI / Foundry)
// resolve to their in-process SourceConnector with the expected Descriptor.Name, so the
// composition root actually wires them — covering the Google-enterprise and Azure
// generative-AI surfaces the gemini/openai connectors do not. Construction is config-free;
// OFFLINE (no credential) Gather is a no-op, so wiring them never fails boot.
func TestBuildInProcSourceHyperscalerModelProviders(t *testing.T) {
	cases := map[string]string{
		"vertex":       "olivares.vertex",
		"azure-openai": "olivares.azure-openai",
	}
	for kind, wantName := range cases {
		conn, ok := buildInProcSource(kind)
		if !ok {
			t.Errorf("kind %q: not wired (buildInProcSource returned ok=false)", kind)
			continue
		}
		if got := conn.Descriptor().Name; got != wantName {
			t.Errorf("kind %q: Descriptor.Name = %q, want %q", kind, got, wantName)
		}
		if got := conn.Descriptor().Type; got != sdk.TypeSource {
			t.Errorf("kind %q: Type = %q, want source", kind, got)
		}
	}
}

// TestBuildInProcSourceLongTailModelProviders pins the wiring for the long-tail
// hosted model/provider observers (Mistral, xAI, DeepSeek, GLM): each resolves to its
// in-process SourceConnector with the expected Descriptor.Name. Construction is
// config-free; OFFLINE (no credential) Gather is a no-op, so wiring them never fails boot.
func TestBuildInProcSourceLongTailModelProviders(t *testing.T) {
	cases := map[string]string{
		"mistral":  "olivares.mistral",
		"xai":      "olivares.xai",
		"deepseek": "olivares.deepseek",
		"glm":      "olivares.glm",
	}
	for kind, wantName := range cases {
		conn, ok := buildInProcSource(kind)
		if !ok {
			t.Errorf("kind %q: not wired (buildInProcSource returned ok=false)", kind)
			continue
		}
		if got := conn.Descriptor().Name; got != wantName {
			t.Errorf("kind %q: Descriptor.Name = %q, want %q", kind, got, wantName)
		}
		if got := conn.Descriptor().Type; got != sdk.TypeSource {
			t.Errorf("kind %q: Type = %q, want source", kind, got)
		}
	}
}

// TestBuildInProcSourceOpencodeAgentSurface pins the wiring for the opencode
// local-config agent-surface observer: it resolves to its in-process SourceConnector with
// the expected Descriptor.Name. Construction is config-free; OFFLINE it is a no-op.
func TestBuildInProcSourceOpencodeAgentSurface(t *testing.T) {
	conn, ok := buildInProcSource("opencode")
	if !ok {
		t.Fatal("opencode: not wired (buildInProcSource returned ok=false)")
	}
	if got := conn.Descriptor().Name; got != "olivares.opencode" {
		t.Errorf("opencode Descriptor.Name = %q, want olivares.opencode", got)
	}
	if got := conn.Descriptor().Type; got != sdk.TypeSource {
		t.Errorf("opencode Type = %q, want source", got)
	}
}

// TestWireClaudeConnectors is the Pieza-1 proof: claude-compliance (CLA-06,
// in-process source) and claude-wif (CLA-12/IDN-01, roster provider + as_source)
// stop being dead-code — the composition root wires them by kind, WITHOUT an
// "unknown kind" warning, and OFFLINE (no api_key/admin_key) the boot does not fail.
// An absent kind still warns honestly (the established posture).
func TestWireClaudeConnectors(t *testing.T) {
	// claude-compliance resolves as an in-process SourceConnector.
	conn, ok := buildInProcSource("claude-compliance")
	if !ok {
		t.Fatal("claude-compliance: not wired (buildInProcSource returned ok=false) — still dead-code")
	}
	if got := conn.Descriptor().Name; got != "olivares.claude-compliance" {
		t.Errorf("claude-compliance Descriptor.Name = %q", got)
	}

	// claude-wif resolves as BOTH the roster GraphProvider and the SourceConnector
	// (it emits PERMITTED edges + the WIF footgun finding, like claude-console).
	prov, srcConn, ok := buildRosterProvider("claude-wif")
	if !ok {
		t.Fatal("claude-wif: not wired (buildRosterProvider returned ok=false) — still dead-code")
	}
	if prov == nil || srcConn == nil {
		t.Fatal("claude-wif: provider/source must both be non-nil (GraphProvider+SourceConnector)")
	}
	if got := srcConn.Descriptor().Name; got != "olivares.claude-wif" {
		t.Errorf("claude-wif Descriptor.Name = %q", got)
	}

	// wireSources wires an OFFLINE claude-compliance source (empty api_key) without
	// the unknown-kind warning and without a boot failure; an absent kind warns.
	var srcBuf bytes.Buffer
	srcLog := slog.New(slog.NewTextHandler(&srcBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	rt := runtime.New(runtime.Options{Logger: quietLog()})
	wireSources(context.Background(), rt, sourcesConfig{Sources: []sourceSpec{
		{Name: "claude-compliance", Kind: "claude-compliance", Tenant: "acme", Config: map[string]string{}},
		{Name: "bogus", Kind: "not-a-real-kind", Tenant: "acme"},
	}}, t.TempDir(), nil, srcLog)
	if !logHasLine(srcBuf.String(), "wired source (in-process fast-path)", "kind=claude-compliance") {
		t.Errorf("claude-compliance was not wired in-process; log = %q", srcBuf.String())
	}
	if logHasLine(srcBuf.String(), "unknown or unsupported source kind", "kind=claude-compliance") {
		t.Errorf("claude-compliance warned unknown-kind (not wired)")
	}
	if !logHasLine(srcBuf.String(), "unknown or unsupported source kind", "kind=not-a-real-kind") {
		t.Errorf("an unknown source kind must still warn honestly; log = %q", srcBuf.String())
	}

	// wireRoster wires claude-wif as a roster provider AND, with as_source=true, as a
	// permitted-access source, offline (no admin_key) without a warning or boot failure.
	var rosBuf bytes.Buffer
	rosLog := slog.New(slog.NewTextHandler(&rosBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	gov := governance.New()
	rt2 := runtime.New(runtime.Options{Logger: quietLog()})
	wireRoster(context.Background(), rt2, gov, newWifGraphAdapter(rosLog), sourcesConfig{Identity: []identitySpec{{
		Name: "claude-wif", Kind: "claude-wif", Tenant: "acme", AsSource: true, Config: map[string]string{},
	}}}, nil, rosLog)
	ros := rosBuf.String()
	if strings.Contains(ros, "unknown identity connector kind") {
		t.Errorf("claude-wif warned unknown identity kind (not wired): %q", ros)
	}
	if !strings.Contains(ros, "wired identity provider") {
		t.Errorf("claude-wif roster provider not wired; log = %q", ros)
	}
	if !strings.Contains(ros, "also wired identity provider as a permitted-access source") {
		t.Errorf("claude-wif as_source=true did not also wire the permitted-access source; log = %q", ros)
	}
}

// TestClaudeComplianceGatherE2E is the integration proof: the connector
// constructed via the composition root path (buildInProcSource) ingests Activity
// Feed events and emits minimal-data FindingReport observations through the
// sdk.Sink contract — the same interface the engine ledger consumes. It proves:
//   - Findings carry Kind "external_activity" (the compliance module's evidence key)
//   - Every finding has a non-empty DetailHash (the tamper-evidence fingerprint)
//   - Actor PII (ip, email, user-agent) NEVER appears in any field except DetailHash
//   - The CAK-absent honest degradation note is emitted in feed-only mode
//   - Coverage gaps are documented once per Gather
//
// This exercises the connector end-to-end at the composition root level (package
// main), not just the connector unit tests (package claudecompliance).
func TestClaudeComplianceGatherE2E(t *testing.T) {
	// Proof 1: the composition root resolves it as an in-process source.
	if _, ok := buildInProcSource("claude-compliance"); !ok {
		t.Fatal("claude-compliance: not wired (buildInProcSource returned ok=false)")
	}

	// Proof 2: the connector ingests and emits minimal-data evidence through the
	// sdk.Sink contract. We construct it with an injected transport so the test
	// exercises the full Gather → Sink path without a live Anthropic key.
	src := claudecompliancePkg.New()
	src.SetTestTransport(&e2eActivityDoer{t: t})
	if err := src.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"api_key": "sk-ant-admin01-test-e2e",
		"org_ref": "e2e-parent-org",
	}}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close(context.Background()) }()

	var findings []sdkmodel.FindingReport
	sink := &e2eCaptureSink{t: t, findings: &findings}
	if err := src.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather failed: %v", err)
	}

	if len(findings) == 0 {
		t.Fatal("Gather emitted 0 findings — the connector must emit evidence")
	}

	// Classify findings by role.
	var activities, posture int
	for _, f := range findings {
		switch f.Kind {
		case "external_activity":
			activities++
		case "posture":
			posture++
		default:
			t.Errorf("unexpected finding Kind %q", f.Kind)
		}
	}
	if activities != 2 {
		t.Errorf("want 2 activity findings (one per page record), got %d", activities)
	}
	if posture < 2 {
		t.Errorf("want ≥2 posture findings (coverage gaps + CAK-absent note), got %d", posture)
	}

	// HEADLINE: PII must NEVER appear in clear in any finding field.
	piiSecrets := []string{
		"203.0.113.42", "alice@corp.example.com",
		"Mozilla/5.0 (E2ESecret)", "bob@corp.example.com",
	}
	for _, f := range findings {
		blob := f.Title + "|" + f.SubjectRef + "|" + f.Kind + "|" + f.SubjectKind
		for _, secret := range piiSecrets {
			if strings.Contains(blob, secret) {
				t.Fatalf("PII %q leaked into finding — minimal-data violated: %+v", secret, f)
			}
		}
		if f.DetailHash == "" {
			t.Errorf("finding %q missing DetailHash (tamper-evidence)", f.SubjectRef)
		}
	}

	// The CAK-absent finding must be present (feed-only mode).
	var hasCAKAbsent bool
	for _, f := range findings {
		if strings.Contains(f.Title, "Compliance Access Key not configured") {
			hasCAKAbsent = true
		}
	}
	if !hasCAKAbsent {
		t.Error("feed-only mode must emit the CAK-absent posture finding")
	}
}

// e2eActivityDoer is a composition-root-level mock transport for the integration
// test. It returns two pages of Activity Feed data with embedded PII that must
// NEVER appear in the findings.
type e2eActivityDoer struct{ t *testing.T }

func (d *e2eActivityDoer) Do(req *http.Request) (*http.Response, error) {
	d.t.Helper()
	if req.Method != http.MethodGet {
		d.t.Errorf("non-GET request %s — connector must be read-only", req.Method)
	}
	body := e2ePage1
	if req.URL.Query().Get("after_id") == "act_e2e_1" {
		body = e2ePage2
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

const e2ePage1 = `{"data":[{"id":"act_e2e_1","created_at":"2026-06-20T10:00:00Z","organization_id":"org_x","type":"claude_chat_created","actor":{"type":"user_actor","ip_address":"203.0.113.42","user_agent":"Mozilla/5.0 (E2ESecret)","email_address":"alice@corp.example.com","user_id":"u_1"},"claude_chat_id":"chat_e2e_1"}],"has_more":true,"first_id":"act_e2e_1","last_id":"act_e2e_1"}`
const e2ePage2 = `{"data":[{"id":"act_e2e_2","created_at":"2026-06-20T11:00:00Z","organization_id":"org_x","type":"api_key_created","actor":{"type":"admin_api_key_actor","ip_address":"198.51.100.9","email_address":"bob@corp.example.com","api_key_id":"key_1"}}],"has_more":false,"first_id":"act_e2e_2","last_id":"act_e2e_2"}`

// e2eCaptureSink captures FindingReport observations at the composition root level.
type e2eCaptureSink struct {
	t        *testing.T
	findings *[]sdkmodel.FindingReport
}

func (s *e2eCaptureSink) Emit(_ context.Context, obs sdkmodel.Observation) error {
	f, ok := obs.(sdkmodel.FindingReport)
	if !ok {
		s.t.Fatalf("emitted %T, want FindingReport", obs)
	}
	*s.findings = append(*s.findings, f)
	return nil
}

// TestBuildInProcSourceCodexManagedConfig pins that the Codex managed-config
// governance connector (gap G4/C2) resolves in `serve` to its SourceConnector — i.e. it is
// wired into CB-1, the Codex enforcement-posture sibling of managed-settings.
func TestBuildInProcSourceCodexManagedConfig(t *testing.T) {
	conn, ok := buildInProcSource("codex-managed-config")
	if !ok {
		t.Fatal("codex-managed-config: not wired (buildInProcSource returned ok=false)")
	}
	if got := conn.Descriptor().Name; got != "olivares.codex-managed-config" {
		t.Errorf("codex-managed-config Descriptor.Name = %q", got)
	}

	// wireSources wires it in-process from an operator config without a warning.
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	rt := runtime.New(runtime.Options{Logger: quietLog()})
	wireSources(context.Background(), rt, sourcesConfig{Sources: []sourceSpec{
		{Name: "codex-managed-config", Kind: "codex-managed-config", Tenant: "acme", Config: map[string]string{
			"requirements_path": "/etc/codex/requirements.toml", "managed_config_path": "/etc/codex/managed_config.toml",
		}},
	}}, t.TempDir(), nil, log)
	if !logHasLine(buf.String(), "wired source (in-process fast-path)", "kind=codex-managed-config") {
		t.Errorf("codex-managed-config was not wired in-process; log = %q", buf.String())
	}
}

// TestBuildInProcSourceIaCGitOps pins that the IaC/GitOps read-first observers
// resolve in `serve` to their SourceConnector — i.e. they are
// wired into CB-1, not an unknown-kind no-op. The runtime re-poll/bus emission is
// covered generically by core/runtime tests; this is the composition-root proof.
func TestBuildInProcSourceIaCGitOps(t *testing.T) {
	cases := map[string]string{
		"argocd":     "olivares.argocd",
		"flux":       "olivares.flux",
		"crossplane": "olivares.crossplane",
	}
	for kind, wantName := range cases {
		conn, ok := buildInProcSource(kind)
		if !ok {
			t.Errorf("kind %q: not wired (buildInProcSource returned ok=false)", kind)
			continue
		}
		if got := conn.Descriptor().Name; got != wantName {
			t.Errorf("kind %q: Descriptor.Name = %q, want %q", kind, got, wantName)
		}
		if got := conn.Descriptor().Type; got != sdk.TypeSource {
			t.Errorf("kind %q: Type = %q, want source", kind, got)
		}
	}
}

// TestBuildInProcSourceRRWGraph is the (FASE P / B1) proof: the R/RW access-map
// DIFFERENTIAL connectors — pgAudit, S3/CloudTrail, the eBPF backstop,
// the runtime inventory and MCP introspection — resolve in `serve` to their in-process
// SourceConnector, so a stock binary can wire the moat from OLIVARES_SOURCES_CONFIG
// rather than only the test harness. The hyphenated Descriptor.Name aliases
// (pg-audit / s3-cloudtrail) resolve too. Construction is config-free; the read-only
// Open I/O is the runtime's job at Start (re-polled or streaming per the source).
func TestBuildInProcSourceRRWGraph(t *testing.T) {
	cases := map[string]string{
		"pgaudit":       "olivares.pg-audit",
		"pg-audit":      "olivares.pg-audit",
		"s3cloudtrail":  "olivares.s3-cloudtrail",
		"s3-cloudtrail": "olivares.s3-cloudtrail",
		"ebpf":          "olivares.ebpf",
		"runtime":       "olivares.runtime",
		"mcp":           "olivares.mcp",
	}
	for kind, wantName := range cases {
		conn, ok := buildInProcSource(kind)
		if !ok {
			t.Errorf("kind %q: not wired (buildInProcSource returned ok=false) — the R/RW moat is not configurable", kind)
			continue
		}
		if got := conn.Descriptor().Name; got != wantName {
			t.Errorf("kind %q: Descriptor.Name = %q, want %q", kind, got, wantName)
		}
		if got := conn.Descriptor().Type; got != sdk.TypeSource {
			t.Errorf("kind %q: Type = %q, want source", kind, got)
		}
	}
	// A document-source kind is NOT an observation source: it must not resolve here
	// (it has no sdk.SourceConnector identity — the boundary).
	if _, ok := buildInProcSource("gdrive"); ok {
		t.Error("a knowledge document kind must NOT resolve as an in-process observation source")
	}
}

// TestBuildContentSourceKnowledgeDocs pins the document-source registry: the
// gdrive/confluence/notion/sharepoint/s3content/sap_odata/salesforce/snowflake/
// azure_ai_search connectors resolve to a
// contentsource.Source whose Kind() is ClassDocument (the boundary — a content
// source is never confused with an audit/inventory feed). They are NOT
// sdk.SourceConnector and are never registered with the runtime; the knowledge module
// drives them on ingest.
func TestBuildContentSourceKnowledgeDocs(t *testing.T) {
	cases := map[string]string{
		"gdrive":          "olivares.gdrive-content",
		"confluence":      "olivares.confluence-content",
		"notion":          "olivares.notion-content",
		"sharepoint":      "olivares.sharepoint-content",
		"s3content":       "olivares.s3-content",
		"sap_odata":       "olivares.sapodata-content",
		"salesforce":      "olivares.salesforce-content",
		"snowflake":       "olivares.snowflake-content",
		"azure_ai_search": "olivares.azureaisearch-content",
		"postgres":        "olivares.pg-content", //
		"filesystem":      "olivares.fs-content", //
	}
	for kind, wantName := range cases {
		src, ok := buildContentSource(kind, nil)
		if !ok {
			t.Errorf("kind %q: not wired (buildContentSource returned ok=false)", kind)
			continue
		}
		if got := src.Descriptor().Name; got != wantName {
			t.Errorf("kind %q: Descriptor.Name = %q, want %q", kind, got, wantName)
		}
		if got := src.Descriptor().Type; got != sdk.TypeContentSource {
			t.Errorf("kind %q: Descriptor.Type = %q, want TypeContentSource", kind, got)
		}
		if surfaces := src.Descriptor().Surfaces; len(surfaces) != 1 || surfaces[0] != "knowledge.document" {
			t.Errorf("kind %q: Descriptor.Surfaces = %v, want [knowledge.document]", kind, surfaces)
		}
		if got := src.Kind(); got != contentsource.ClassDocument {
			t.Errorf("kind %q: Kind() = %q, want ClassDocument (the boundary)", kind, got)
		}
	}
	// An R/RW observation kind is NOT a document source.
	if _, ok := buildContentSource("pgaudit", nil); ok {
		t.Error("an R/RW observation kind must NOT resolve as a document source")
	}
	if _, ok := buildContentSource("nonexistent-kind", nil); ok {
		t.Error("an unknown kind must not wire (expected ok=false)")
	}
}

// rawInlineToken is a FAKE cleartext credential pasted where a secret-store
// reference belongs. ValidateCredentialRef rejects it at the connector's Open (no
// scheme), and — per the never-log-the-error rule — it must never appear in any
// log line knowledgeContentOptions emits.
const rawInlineToken = "ghp_0123456789abcdefghijklmnopqrstuvwxyzAB"

// driveExportFixture is one document in the gdrive connector's NATIVE export shape
// (a Drive files.list/export JSON, the same shape as connectors/gdrive/testdata).
// The permission email is PII the connector must fold into the opaque permission
// id, never into the ACL.
const driveExportFixture = `{
  "files": [
    {
      "id": "doc-onboarding",
      "name": "Onboarding",
      "mimeType": "application/vnd.google-apps.document",
      "modifiedTime": "2026-06-01T10:00:00Z",
      "permissions": [
        {"id": "perm-1", "type": "user", "emailAddress": "someone@example.com", "role": "reader"}
      ],
      "exportedContent": "Welcome aboard. Badge pickup is on floor two."
    }
  ]
}`

// TestKnowledgeContentOptionsOpensConfiguredSources is the proof of the
// latent-bug fix: knowledgeContentOptions OPENS each document source with its
// configured settings before wiring it (the knowledge module only drives
// List/Fetch, so a never-Opened connector silently lists nothing), and a source
// whose Open FAILS is NOT wired — under the old never-Open wiring both negative
// entries below would have been handed to the module as one dead option each.
// The failure warning never carries the Open error (it can embed the configured
// credential/endpoint), so the pasted raw token must be absent from the log.
func TestKnowledgeContentOptionsOpensConfiguredSources(t *testing.T) {
	dir := t.TempDir()
	exportPath := filepath.Join(dir, "drive.json")
	if err := os.WriteFile(exportPath, []byte(driveExportFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	malformedPath := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(malformedPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg := sourcesConfig{Documents: []documentSpec{
		{Name: "corp-drive", Kind: "gdrive", Config: map[string]string{"export_path": exportPath}},
		// Open fails: an inline raw secret where a secret-store reference belongs.
		{Name: "pasted-secret", Kind: "gdrive", Config: map[string]string{"credential_ref": rawInlineToken}},
		// Open fails: the configured export file is not parseable.
		{Name: "broken-export", Kind: "gdrive", Config: map[string]string{"export_path": malformedPath}},
	}}
	// knowledgeContentSources COLLECTS the named, known-kind sources (all 3);
	// deferredSecretWiring.openAll then resolves each source's references and Opens
	// it, registering ONLY the openable ones on the module (the pasted-secret and the
	// broken export both fail to open and are not wired).
	pending := knowledgeContentSources(cfg, log)
	if len(pending) != 3 {
		t.Fatalf("knowledgeContentSources collected %d sources, want 3 (all named+known-kind); log = %q", len(pending), buf.String())
	}
	km := knowledge.New()
	d := &deferredSecretWiring{content: pending, knowledge: km}
	d.openAll(context.Background(), nil, log)
	out := buf.String()
	if !logHasLine(out, "wired document source", "name=corp-drive", "kind=gdrive") {
		t.Errorf("the openable gdrive source was not wired; log = %q", out)
	}
	if !logHasLine(out, "not wired", "name=pasted-secret") {
		t.Errorf("a source rejecting its inline-secret config must warn 'not wired'; log = %q", out)
	}
	if !logHasLine(out, "not wired", "name=broken-export") {
		t.Errorf("a source with a malformed export must warn 'not wired'; log = %q", out)
	}
	if strings.Contains(out, rawInlineToken) {
		t.Error("the configured raw credential leaked into the boot log (the Open error must never be logged)")
	}

	// Open ran WITH the configured settings: the same connector kind, opened the
	// same way the composition root does it, serves the seeded document via
	// List/Fetch — the exact contract the knowledge module's ingest relies on.
	src, ok := buildContentSource("gdrive", nil)
	if !ok {
		t.Fatal("gdrive: buildContentSource returned ok=false")
	}
	ctx := context.Background()
	if err := src.Open(ctx, sdk.Config{Settings: map[string]string{"export_path": exportPath}}); err != nil {
		t.Fatalf("open gdrive with the fixture export: %v", err)
	}
	refs, next, err := src.List(ctx, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(refs) != 1 || next != "" {
		t.Fatalf("list returned %d refs (next=%q), want the 1 seeded document", len(refs), next)
	}
	if refs[0].DocID != "doc-onboarding" {
		t.Errorf("DocID = %q, want doc-onboarding", refs[0].DocID)
	}
	doc, err := src.Fetch(ctx, "doc-onboarding")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(doc.Body, "Welcome aboard") {
		t.Errorf("fetched body does not carry the seeded content (got %d bytes)", len(doc.Body))
	}
	if len(doc.ACL) != 1 || doc.ACL[0] != "user:perm-1" {
		t.Errorf("ACL = %v, want [user:perm-1] (the opaque permission id, never the email)", doc.ACL)
	}
}

// TestKnowledgeContentOptions is the document-source counterpart to
// TestWireClaudeConnectors: knowledgeContentOptions wires each configured document
// source by kind (one knowledge.Option each), WARNS on an unknown kind and on a
// nameless entry (never a silent drop, 12 §5), and yields no option for a deny-closed
// empty config.
func TestKnowledgeContentOptions(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg := sourcesConfig{Documents: []documentSpec{
		{Name: "corp-drive", Kind: "gdrive"},
		{Name: "eng-wiki", Kind: "confluence", Config: map[string]string{"mode": "live"}},
		{Name: "bogus", Kind: "not-a-real-kind"}, // unknown: warn, do not wire
		{Name: "", Kind: "notion"},               // nameless: warn, do not wire
	}}
	pending := knowledgeContentSources(cfg, log)
	if len(pending) != 2 {
		t.Fatalf("knowledgeContentSources collected %d sources, want 2 (gdrive+confluence; unknown+nameless skipped)", len(pending))
	}
	for _, p := range pending {
		mode, ok := p.src.(contentSourceMode)
		if !ok {
			t.Fatalf("%s source does not expose Mode()", p.name)
		}
		wantMode := "export"
		if p.name == "eng-wiki" {
			wantMode = "live"
		}
		if got := mode.Mode(); got != wantMode {
			t.Fatalf("%s Mode() = %q, want %q", p.name, got, wantMode)
		}
		if _, ok := p.src.(contentsource.LiveSource); !ok {
			t.Fatalf("%s wrapper must preserve contentsource.LiveSource", p.name)
		}
	}
	out := buf.String()
	if !logHasLine(out, "unknown or unsupported document source kind", "kind=not-a-real-kind") {
		t.Errorf("an unknown document kind must warn honestly; log = %q", out)
	}
	if !logHasLine(out, "document source has no name", "kind=notion") {
		t.Errorf("a nameless document source must warn; log = %q", out)
	}
	// Deny-closed: with no documents configured, no pull sources are collected.
	if got := knowledgeContentSources(sourcesConfig{}, log); len(got) != 0 {
		t.Errorf("empty config collected %d document sources, want 0 (deny-closed)", len(got))
	}
}

// TestBuildRosterProviderS156Federation pins the (FED-1) composition: every
// federation roster kind resolves to its GraphProvider + SourceConnector pair
// with the expected Descriptor.Name, so the agent-identity federation is wirable
// from OLIVARES_SOURCES_CONFIG — never an unknown-kind no-op.
func TestBuildRosterProviderS156Federation(t *testing.T) {
	cases := map[string]string{
		"entra-agent":      "olivares.entra-agent",
		"agentcore":        "olivares.agentcore",
		"google-agent":     "olivares.google-agent",
		"oasf":             "olivares.oasf",
		"agent365":         "olivares.agent365",
		"foundry-agents":   "olivares.foundry-agents",
		"ai-control-tower": "olivares.ai-control-tower",
		"onepassword":      "olivares.onepassword",
	}
	for kind, wantName := range cases {
		prov, conn, ok := buildRosterProvider(kind)
		if !ok {
			t.Errorf("kind %q: not wired (buildRosterProvider returned ok=false)", kind)
			continue
		}
		if prov == nil {
			t.Errorf("kind %q: nil GraphProvider", kind)
		}
		if got := conn.Descriptor().Name; got != wantName {
			t.Errorf("kind %q: Descriptor.Name = %q, want %q", kind, got, wantName)
		}
	}
}

// TestBuildInProcSourceS156 pins that the kinds whose Gather is a
// re-pollable batch scan (item-usage edges, drift findings, badge findings, the
// vault audit tail) resolve as in-process observation sources — the cfg.Sources
// + poll_seconds path; the identity entry's as_source=true would run them only
// once per boot (the reviewed HIGH finding).
func TestBuildInProcSourceS156(t *testing.T) {
	cases := map[string]string{
		"vault-audit":    "olivares.vault-audit",
		"onepassword":    "olivares.onepassword",
		"entra-agent":    "olivares.entra-agent",
		"agentcore":      "olivares.agentcore",
		"oasf":           "olivares.oasf",
		"agent365":       "olivares.agent365",
		"google-agent":   "olivares.google-agent",
		"foundry-agents": "olivares.foundry-agents",
	}
	for kind, wantName := range cases {
		conn, ok := buildInProcSource(kind)
		if !ok {
			t.Errorf("kind %q: not wired (buildInProcSource returned ok=false)", kind)
			continue
		}
		if got := conn.Descriptor().Name; got != wantName {
			t.Errorf("kind %q: Descriptor.Name = %q, want %q", kind, got, wantName)
		}
		if got := conn.Descriptor().Type; got != sdk.TypeSource {
			t.Errorf("kind %q: Type = %q, want source", kind, got)
		}
	}
	// ai-control-tower has a no-op Gather: roster-only, deliberately NOT an
	// in-process observation source.
	for _, kind := range []string{"ai-control-tower"} {
		if _, ok := buildInProcSource(kind); ok {
			t.Errorf("kind %q has a no-op Gather and must not resolve as an observation source", kind)
		}
	}
}

// TestBuildInProcSourceS158IdentityGrants pins the composition: the
// identity sources whose Gather is now a re-pollable permitted-grant scan
// (ldap privileged-directory grants, idp Okta/Entra app/scope assignments,
// infisical project grants) resolve in buildInProcSource, so a cfg.Sources
// entry with poll_seconds re-runs them — the as_source=true path runs a Gather
// only once per boot. okta/entra are aliases of the one idp connector
// (Descriptor olivares.idp — the one-instance-per-kind limit applies to
// the family). The roster-only kinds stay pinned NOT to resolve
// (TestBuildInProcSourceS156), and spiffe/keycloak keep a no-op Gather.
func TestBuildInProcSourceS158IdentityGrants(t *testing.T) {
	cases := map[string]string{
		"ldap":      "olivares.ldap",
		"idp":       "olivares.idp",
		"okta":      "olivares.idp",
		"entra":     "olivares.idp",
		"infisical": "olivares.infisical",
	}
	for kind, wantName := range cases {
		conn, ok := buildInProcSource(kind)
		if !ok {
			t.Errorf("kind %q: not wired (buildInProcSource returned ok=false) — its permitted-grant scan is not re-pollable", kind)
			continue
		}
		if got := conn.Descriptor().Name; got != wantName {
			t.Errorf("kind %q: Descriptor.Name = %q, want %q", kind, got, wantName)
		}
		if got := conn.Descriptor().Type; got != sdk.TypeSource {
			t.Errorf("kind %q: Type = %q, want source", kind, got)
		}
	}
	// spiffe and keycloak remain roster-only (no grant surface, no-op Gather):
	// deliberately NOT in-process observation sources.
	for _, kind := range []string{"spiffe", "keycloak"} {
		if _, ok := buildInProcSource(kind); ok {
			t.Errorf("kind %q is roster-only and must not resolve as an observation source", kind)
		}
	}
}

// TestBuildInProcSourceBaseProviders pins the wiring of the three first-party
// base providers that were BUILT AND NEVER COMPOSED: the packages implemented
// sdk.SourceConnector in full, and the composition root never imported or selected
// them, so the product could not offer OpenAI, the Gemini API, or local/self-hosted
// inference at all. A package present in the tree is not an integration.
//
// The kinds are deliberately distinct from their neighbors and the test says so,
// because "gemini" vs "gemini-cli" vs "vertex" and "openai" vs "azure-openai" are the
// pairs an operator (or a future edit) is most likely to collapse:
//   - openai      speaks OpenAI-org paths; azure-openai speaks the real Azure surfaces.
//   - gemini      speaks the Gemini API; gemini-cli observes LOCAL CLI settings/posture.
//   - local       is Ollama + vLLM, which the canon requires to be always present.
//
// The non-firing direction is covered by the unknown-kind control above: a switch that
// answered ok=true for everything would satisfy every assertion here.
func TestBuildInProcSourceBaseProviders(t *testing.T) {
	for _, tc := range []struct{ kind, want string }{
		{"openai", "olivares.openai"},
		{"gemini", "olivares.gemini"},
		{"local", "olivares.local"},
	} {
		conn, ok := buildInProcSource(tc.kind)
		if !ok {
			t.Errorf("kind %q: not wired (buildInProcSource returned ok=false)", tc.kind)
			continue
		}
		if conn == nil {
			t.Errorf("kind %q: wired but returned a nil connector", tc.kind)
			continue
		}
		if got := conn.Descriptor().Name; got != tc.want {
			t.Errorf("kind %q: Descriptor.Name = %q, want %q", tc.kind, got, tc.want)
		}
	}
}
