// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package codexmanagedconfig

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestValidateRequirementsTOML(t *testing.T) {
	// The rendered sample is valid.
	good, _ := RenderRequirements(sampleStrictPolicy())
	if issues := ValidateRequirementsTOML(good); len(issues) != 0 {
		t.Errorf("rendered sample requirements should validate clean, got %v", issues)
	}
	// Empty is an issue.
	if issues := ValidateRequirementsTOML([]byte("  ")); len(issues) == 0 {
		t.Error("empty requirements should be an issue")
	}
	// Unknown enum value.
	if issues := ValidateRequirementsTOML([]byte(`allowed_sandbox_modes = ["read-only", "yolo"]`)); len(issues) == 0 {
		t.Error("unknown sandbox mode should be an issue")
	}
	// MCP entry without identity.
	if issues := ValidateRequirementsTOML([]byte("[mcp_servers.docs]\n")); !hasIssue(issues, "must carry an identity") {
		t.Errorf("MCP without identity should be flagged, got %v", issues)
	}
	// MCP entry with BOTH command and url.
	bad := "[mcp_servers.docs]\nidentity = { command = \"x\", url = \"https://y\" }\n"
	if issues := ValidateRequirementsTOML([]byte(bad)); !hasIssue(issues, "exactly ONE identity selector") {
		t.Errorf("MCP with both selectors should be flagged, got %v", issues)
	}
	// A wrong-typed known key (bool as string) is caught as invalid TOML.
	if issues := ValidateRequirementsTOML([]byte(`allow_remote_control = "false"`)); len(issues) == 0 {
		t.Error("a wrong-typed known key should be flagged")
	}
	profileMismatch := "default_permissions = \":danger-full-access\"\n[allowed_permission_profiles]\n\":workspace\" = true\n"
	if issues := ValidateRequirementsTOML([]byte(profileMismatch)); !hasIssue(issues, "The profile must be allowed by allowed_permission_profiles") {
		t.Errorf("default_permissions must be allowed by allowed_permission_profiles, got %v", issues)
	}
	if _, err := RenderRequirements(Policy{Requirements: Requirements{
		DefaultPermissions:        ":danger-full-access",
		AllowedPermissionProfiles: &map[string]bool{":workspace": true},
	}}); err == nil {
		t.Error("RenderRequirements must reject a default_permissions profile denied by allowed_permission_profiles")
	}
	tolerated := `
[hooks]
managed = true

[apps]
terminal = true

[plugins.review.mcp_servers.docs]
command = "codex-mcp"

[auto_review]
enabled = true
`
	if issues := ValidateRequirementsTOML([]byte(tolerated)); len(issues) != 0 {
		t.Errorf("live-verified-but-unmodeled tables should be tolerated, got %v", issues)
	}
	if _, err := ParseRequirementsTOML([]byte(tolerated)); err != nil {
		t.Fatalf("ParseRequirementsTOML should tolerate unmodeled live tables: %v", err)
	}
}

func TestValidateManagedConfigTOML(t *testing.T) {
	good, _ := RenderManagedConfig(sampleStrictPolicy())
	if issues := ValidateManagedConfigTOML(good); len(issues) != 0 {
		t.Errorf("rendered sample managed_config should validate clean, got %v", issues)
	}
	if issues := ValidateManagedConfigTOML([]byte(`sandbox_mode = "yolo"`)); len(issues) == 0 {
		t.Error("unknown sandbox_mode should be an issue")
	}
	if issues := ValidateManagedConfigTOML([]byte("[otel]\nexporter = \"carrier-pigeon\"\n")); len(issues) == 0 {
		t.Error("unknown otel exporter should be an issue")
	}
	// statsig is valid for metrics_exporter specifically.
	if issues := ValidateManagedConfigTOML([]byte("[otel]\nmetrics_exporter = \"statsig\"\n")); len(issues) != 0 {
		t.Errorf("statsig metrics_exporter should be valid, got %v", issues)
	}
	// The granular approval_policy inline-table is accepted (not a scalar enum error).
	granular := "approval_policy = { granular = { sandbox_approval = true, rules = true } }\n"
	if issues := ValidateManagedConfigTOML([]byte(granular)); len(issues) != 0 {
		t.Errorf("granular approval_policy should be accepted, got %v", issues)
	}
}

// TestValidateOTELExporterRenderability pins the half the name check could not see: an
// exporter id can be perfectly KNOWN and still be unrenderable in the slot it sits in.
//
// codex-cli 0.147.0 treats the exporter slots as an externally-tagged enum with two kinds
// of variant. Fed real config.toml files on 2026-08-18:
//
//	exporter = "otlp-http"                        → invalid type: unit variant, expected struct variant
//	[otel.exporter.otlp-http] endpoint = "…"      → missing field `protocol`
//	[otel.exporter.otlp-http] protocol = "http"   → unknown variant `http`, expected `binary` or `json`
//	exporter = "none" / metrics_exporter = "statsig" → LOAD
//
// The consequence is why this validates instead of degrading: codex refuses the whole
// config.toml, so the agent does not start. An unrenderable pin has to fail in the console
// with a reason, not on an operator's host with a stack trace.
func TestValidateOTELExporterRenderability(t *testing.T) {
	casos := []struct {
		nombre string
		toml   string
		quiere bool // true = debe haber al menos un issue
	}{
		{"otlp-http sin endpoint", "[otel]\nexporter = \"otlp-http\"\n", true},
		{"otlp-grpc sin endpoint", "[otel]\nexporter = \"otlp-grpc\"\n", true},
		{"trace_exporter sin endpoint", "[otel]\ntrace_exporter = \"otlp-http\"\n", true},
		{"metrics_exporter sin endpoint", "[otel]\nmetrics_exporter = \"otlp-grpc\"\n", true},
		{"protocolo desconocido", "[otel]\n[otel.exporter.otlp-http]\nendpoint = \"http://c:4318\"\nprotocol = \"http\"\n", true},
		// Y los controles, que son los que impiden que el arreglo se pase de largo:
		{"otlp-http con endpoint y protocol", "[otel]\n[otel.exporter.otlp-http]\nendpoint = \"http://c:4318\"\nprotocol = \"binary\"\n", false},
		{"none pelado", "[otel]\nexporter = \"none\"\n", false},
		{"statsig pelado", "[otel]\nmetrics_exporter = \"statsig\"\n", false},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			issues := ValidateManagedConfigTOML([]byte(c.toml))
			if got := len(issues) > 0; got != c.quiere {
				t.Errorf("issues=%v, quería alguno=%v — %v", got, c.quiere, issues)
			}
		})
	}
}

func TestCanonicalRoundTrip(t *testing.T) {
	p := sampleStrictPolicy()
	rendered, _ := RenderRequirements(p)
	canon, err := CanonicalRequirementsTOML(rendered)
	if err != nil {
		t.Fatalf("canonical requirements: %v", err)
	}
	// Canonicalizing the canonical form is a fixed point.
	canon2, err := CanonicalRequirementsTOML(canon)
	if err != nil {
		t.Fatalf("canonical^2: %v", err)
	}
	if string(canon) != string(canon2) {
		t.Errorf("canonical requirements is not a fixed point:\n--- a ---\n%s\n--- b ---\n%s", canon, canon2)
	}
	// And the canonical form still drift-cleans against the authored intent.
	w, md, err := parseRequirements(canon)
	if err != nil {
		t.Fatal(err)
	}
	if d := requirementsDrift("h", p.Requirements, w, md, testNow()); len(d) != 0 {
		t.Errorf("canonical requirements must drift-clean, got %+v", d)
	}

	mc, _ := RenderManagedConfig(p)
	mcCanon, err := CanonicalManagedConfigTOML(mc)
	if err != nil {
		t.Fatalf("canonical managed_config: %v", err)
	}
	mw, mmd, err := parseManagedConfig(mcCanon)
	if err != nil {
		t.Fatal(err)
	}
	if d := managedConfigDrift("h", p.Defaults, mw, mmd, testNow()); len(d) != 0 {
		t.Errorf("canonical managed_config must drift-clean, got %+v", d)
	}
}

func TestVerifyDriftTOML(t *testing.T) {
	p := sampleStrictPolicy()
	reqTOML, _ := RenderRequirements(p)
	mcTOML, _ := RenderManagedConfig(p)

	// Matching host -> zero drift.
	if f := VerifyDriftTOML("h", p, reqTOML, mcTOML, testNow()); len(f) != 0 {
		t.Errorf("a matching host must not drift at publish time, got %+v", f)
	}
	// Absent observed requirements -> HIGH absence (authored).
	f := VerifyDriftTOML("h", p, nil, mcTOML, testNow())
	if !hasFinding(f, model.SeverityHigh, "requirements.toml (system tier) is absent") {
		t.Errorf("absent observed requirements should be a HIGH absence finding, got %+v", f)
	}
	// Invalid observed managed_config -> present-but-invalid finding.
	f = VerifyDriftTOML("h", p, reqTOML, []byte("approval_policy = "), testNow())
	if !hasFinding(f, model.SeverityMedium, "managed_config.toml (system tier) is present but invalid") {
		t.Errorf("invalid observed managed_config should be a present-but-invalid finding, got %+v", f)
	}
}

func TestPreviewHasPrecedenceAndCaveat(t *testing.T) {
	lines := Preview()
	joined := ""
	for _, l := range lines {
		joined += l.Scope + ": " + l.Note + "\n"
	}
	for _, want := range []string{
		"cloud-managed", "com.openai.codex", MDMRequirementsKey, MDMConfigKey,
		"FALLS BACK", "unverifiable-from-here", MinPermissionProfilesCodexVersion,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("precedence preview missing %q\n%s", want, joined)
		}
	}
}

func TestRequirementsPrecedenceOrderExact(t *testing.T) {
	got := RequirementsPrecedence()
	want := []RequirementsTier{TierCloudManaged, TierMDMRequirements, TierSystemRequirements}
	if len(got) != len(want) {
		t.Fatalf("RequirementsPrecedence length = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("RequirementsPrecedence[%d] = %q, want %q (full=%+v)", i, got[i], want[i], got)
		}
	}
	var joined string
	for _, l := range PrecedencePreview() {
		joined += l.Scope + ": " + l.Note + "\n"
	}
	for _, wantText := range []string{"cloud-managed > MDM > system-file", "MDM > managed_config.toml > config.toml"} {
		if !strings.Contains(joined, wantText) {
			t.Fatalf("PrecedencePreview missing %q\n%s", wantText, joined)
		}
	}
}

func hasIssue(issues []string, substr string) bool {
	for _, i := range issues {
		if strings.Contains(i, substr) {
			return true
		}
	}
	return false
}

func hasFinding(f []model.FindingReport, sev model.Severity, titleSubstr string) bool {
	for _, x := range f {
		if x.Severity == sev && strings.Contains(x.Title, titleSubstr) {
			return true
		}
	}
	return false
}
