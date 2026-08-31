// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package codexmanagedconfig

import (
	"strings"
	"testing"
)

func TestRenderRequirementsRoundTrip(t *testing.T) {
	p := sampleStrictPolicy()
	out, err := RenderRequirements(p)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		`allowed_approval_policies = ["untrusted", "on-request"]`,
		`allowed_sandbox_modes = ["read-only", "workspace-write"]`,
		`allowed_web_search_modes = ["cached"]`,
		`enforce_residency = "us"`,
		"allow_remote_control = false",
		"allow_managed_hooks_only = true",
		`default_permissions = ":workspace"`,
		"[allowed_permission_profiles]",
		`":workspace" = true`,
		"[computer_use]",
		"allow_locked_computer_use = false",
		"[[remote_sandbox_config]]",
		`hostname_patterns = ["*.corp.example"]`,
		"[windows]",
		`allowed_sandbox_implementations = ["unelevated"]`,
		"[marketplaces]",
		"restrict_to_allowed_sources = true",
		"[marketplaces.allowed_sources.approved]",
		`source = "git"`,
		"[rules]",
		"[[rules.prefix_rules]]",
		`decision = "forbidden"`,
		"[experimental_network]",
		"enabled = false",
		"http_port = 8080",
		"[mcp_servers.docs.identity]",
		`command = "codex-mcp"`,
		"[mcp_servers.remote.identity]",
		`url = "https://example.com/mcp"`,
		"[permissions.filesystem]",
		`deny_read = ["/**/*.env", "~/.ssh"]`,
		"[features]",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered requirements.toml missing %q\n%s", want, s)
		}
	}
	// A host carrying exactly the rendered file must NOT drift against the authored intent.
	w, md, err := parseRequirements(out)
	if err != nil {
		t.Fatalf("rendered requirements does not parse: %v", err)
	}
	if d := requirementsDrift("h", p.Requirements, w, md, testNow()); len(d) != 0 {
		t.Errorf("a host matching the authored requirements must not drift, got %+v", d)
	}
}

func TestRenderManagedConfigRoundTrip(t *testing.T) {
	p := sampleStrictPolicy()
	out, err := RenderManagedConfig(p)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		`approval_policy = "on-request"`,
		`sandbox_mode = "workspace-write"`,
		"[sandbox_workspace_write]",
		"network_access = false",
		"[experimental_network]",
		"managed_allowed_domains_only = true",
		"[otel]",
		"log_user_prompt = false",
		"https://otel.olivares.example/v1/logs", // endpoint nested under the exporter
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered managed_config.toml missing %q\n%s", want, s)
		}
	}
	w, md, err := parseManagedConfig(out)
	if err != nil {
		t.Fatalf("rendered managed_config does not parse: %v", err)
	}
	if d := managedConfigDrift("h", p.Defaults, w, md, testNow()); len(d) != 0 {
		t.Errorf("a host matching the authored defaults must not drift, got %+v", d)
	}
}

func TestRenderLockdowns(t *testing.T) {
	// The three-state EMPTY-present LOCKDOWN forms must render (never be dropped as empty).
	p := Policy{Requirements: Requirements{
		AllowedWebSearchModes: &[]string{},    // [] => only "disabled" permitted
		AllowedMCPServers:     &[]MCPServer{}, // empty [mcp_servers] => all MCP off
	}}
	out, err := RenderRequirements(p)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "allowed_web_search_modes = []") {
		t.Errorf("web-search [] lockdown not rendered:\n%s", s)
	}
	if !strings.Contains(s, "[mcp_servers]") {
		t.Errorf("MCP empty-table lockdown not rendered:\n%s", s)
	}
	// And the lockdown round-trips: parse must see both as PRESENT (not absent).
	w, md, err := parseRequirements(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !isDefined(md, "allowed_web_search_modes") || !isDefined(md, "mcp_servers") {
		t.Errorf("lockdown forms must round-trip as PRESENT; web=%v mcp=%v", isDefined(md, "allowed_web_search_modes"), isDefined(md, "mcp_servers"))
	}
	_ = w
}

func TestRenderEmptyPolicy(t *testing.T) {
	for _, b := range [][]byte{mustRender(t, RenderRequirements), mustRender(t, RenderManagedConfig)} {
		if strings.TrimSpace(string(b)) != "" {
			t.Errorf("an empty policy must render empty TOML, got %q", b)
		}
	}
}

func mustRender(t *testing.T, fn func(Policy) ([]byte, error)) []byte {
	t.Helper()
	b, err := fn(Policy{})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestRenderOTELExporterShapePerVariant replaces TestRenderOTELScalarExporterWhenNoEndpoint,
// which asserted `exporter = "otlp-http"` — and that is the one shape codex-cli 0.147.0
// REFUSES TO LOAD. A green test was holding the defect in place, which is worse than no
// test: it made the wrong shape look decided.
//
// Measured 2026-08-18 by feeding real config.toml files to the binary with CODEX_HOME on a
// scratch dir, one shape per run (see the table over the exporter constants in policy.go):
// none and statsig are UNIT variants and render bare; otlp-http and otlp-grpc are STRUCT
// variants and need a table, with endpoint required for both and protocol required by
// otlp-http. A refused config.toml does not degrade telemetry — the agent does not start.
func TestRenderOTELExporterShapePerVariant(t *testing.T) {
	t.Run("struct variant renders endpoint AND protocol", func(t *testing.T) {
		p := Policy{Defaults: ManagedConfig{OTEL: &OTELConfig{
			Exporter: OTELExporterOTLPHTTP,
			Endpoint: "http://collector.example:4318",
		}}}
		out, err := RenderManagedConfig(p)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		s := string(out)
		for _, want := range []string{"http://collector.example:4318", `protocol = "binary"`} {
			if !strings.Contains(s, want) {
				t.Errorf("missing %q — codex rejects an otlp-http slot without it:\n%s", want, s)
			}
		}
		if strings.Contains(s, `exporter = "otlp-http"`) {
			t.Errorf("the bare id is the shape codex refuses to load:\n%s", s)
		}
	})

	t.Run("an authored protocol wins over the default", func(t *testing.T) {
		p := Policy{Defaults: ManagedConfig{OTEL: &OTELConfig{
			Exporter: OTELExporterOTLPHTTP,
			Endpoint: "http://collector.example:4318",
			Protocol: OTELProtocolJSON,
		}}}
		out, err := RenderManagedConfig(p)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		if !strings.Contains(string(out), `protocol = "json"`) {
			t.Errorf("authored protocol dropped:\n%s", out)
		}
	})

	t.Run("unit variants still render bare", func(t *testing.T) {
		// The control that keeps the fix from over-reaching: none and statsig LOAD as bare
		// strings, so turning them into tables would break the other direction.
		p := Policy{Defaults: ManagedConfig{OTEL: &OTELConfig{
			Exporter:        OTELExporterNone,
			MetricsExporter: OTELExporterStatsig,
		}}}
		out, err := RenderManagedConfig(p)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		s := string(out)
		for _, want := range []string{`exporter = "none"`, `metrics_exporter = "statsig"`} {
			if !strings.Contains(s, want) {
				t.Errorf("missing %q — unit variants must stay bare:\n%s", want, s)
			}
		}
	})

	t.Run("metrics_exporter takes the same shape", func(t *testing.T) {
		// It rendered as a bare string unconditionally, so an operator pinning
		// metrics_exporter to otlp-http produced the config the binary refuses.
		p := Policy{Defaults: ManagedConfig{OTEL: &OTELConfig{
			MetricsExporter: OTELExporterOTLPHTTP,
			Endpoint:        "http://collector.example:4318",
		}}}
		out, err := RenderManagedConfig(p)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		if strings.Contains(string(out), `metrics_exporter = "otlp-http"`) {
			t.Errorf("metrics_exporter rendered the refused bare form:\n%s", out)
		}
		if !strings.Contains(string(out), `protocol = "binary"`) {
			t.Errorf("metrics_exporter table without protocol:\n%s", out)
		}
	})

	t.Run("a struct variant with no endpoint emits no exporter key", func(t *testing.T) {
		// Authoring refuses this combination; if it ever reaches the renderer, emitting
		// NOTHING is right and emitting the bare id is not — the second one stops the agent.
		p := Policy{Defaults: ManagedConfig{OTEL: &OTELConfig{Exporter: OTELExporterOTLPHTTP}}}
		out, err := RenderManagedConfig(p)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		if strings.Contains(string(out), `exporter = "otlp-http"`) {
			t.Errorf("emitted the unloadable bare id:\n%s", out)
		}
	})
}
