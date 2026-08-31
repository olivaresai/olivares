// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import (
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// TestTelemetryEnv_SanctionedObserveDefaults verifies the telemetry env turns
// observation ON, defaults both signals, and keeps CONTENT capture OFF (minimal-data)
// and never inlines a collector credential.
func TestTelemetryEnv_SanctionedObserveDefaults(t *testing.T) {
	env := TelemetryEnv(TelemetryConfig{Endpoint: "https://otel.corp.example:4317"})
	if env[EnvEnableTelemetry] != "1" {
		t.Fatalf("telemetry must be enabled: %v", env)
	}
	if env[EnvOTLPProtocol] != "grpc" {
		t.Errorf("protocol should default to grpc: %v", env)
	}
	if env[EnvOTLPEndpoint] != "https://otel.corp.example:4317" {
		t.Errorf("endpoint not set: %v", env)
	}
	if env[EnvMetricsExporter] != "otlp" || env[EnvLogsExporter] != "otlp" {
		t.Errorf("both signals should default ON: %v", env)
	}
	// Content capture OFF by default → keys absent (minimal-data), never inline a token.
	for _, k := range []string{EnvLogUserPrompts, EnvLogToolContent, EnvOTLPHeaders} {
		if _, ok := env[k]; ok {
			t.Errorf("key %q must be absent by default (minimal-data / no inline secret): %v", k, env)
		}
	}
}

// TestTelemetryEnv_ContentOptInIsExplicit verifies prompt/tool content capture is only
// present when explicitly requested.
func TestTelemetryEnv_ContentOptInIsExplicit(t *testing.T) {
	env := TelemetryEnv(TelemetryConfig{Endpoint: "x", Metrics: true, IncludePrompts: true, IncludeToolContent: true})
	if env[EnvLogUserPrompts] != "1" || env[EnvLogToolContent] != "1" {
		t.Fatalf("explicit content opt-in must set the keys: %v", env)
	}
	if env[EnvLogsExporter] != "" {
		t.Errorf("narrowing to metrics-only should not set logs exporter: %v", env)
	}
}

// TestPEPHook_BuildsPreToolUseAndErrors verifies the PEP hook builder produces a
// PreToolUse command hook (and PostToolUse when Redact), and is deny-closed on an
// empty command (a hookless PEP enforces nothing).
func TestPEPHook_BuildsPreToolUseAndErrors(t *testing.T) {
	if _, err := PEPHook(PEPHookConfig{Command: "  "}); err == nil {
		t.Fatal("empty command must error (a hookless PEP is a silent no-op)")
	}
	if _, err := PEPHook(PEPHookConfig{Command: "/opt/olivares/bin/pep", TimeoutSecs: -1}); err == nil {
		t.Fatal("negative timeout must error")
	}
	hooks, err := PEPHook(PEPHookConfig{Command: "/opt/olivares/bin/pep", Matcher: "Bash", TimeoutSecs: 20, Redact: true})
	if err != nil {
		t.Fatal(err)
	}
	pre := hooks[hookEventPreToolUse]
	if len(pre) != 1 || pre[0].Matcher != "Bash" || len(pre[0].Hooks) != 1 {
		t.Fatalf("PreToolUse PEP entry malformed: %+v", pre)
	}
	if h := pre[0].Hooks[0]; h.Type != hookTypeCommand || h.Command != "/opt/olivares/bin/pep" || h.Timeout != 20 {
		t.Fatalf("PEP command entry wrong: %+v", h)
	}
	if _, ok := hooks[hookEventPostToolUse]; !ok {
		t.Fatal("Redact=true must also install a PostToolUse hook")
	}
}

// TestManagedHooksEnvRoundTrip is the author→distribute→verify round-trip the DoD
// requires: a Policy carrying a PEP hook + telemetry env renders to the verified wire
// keys, and parses back preserving both — so drift diffs the same field set it emits.
func TestManagedHooksEnvRoundTrip(t *testing.T) {
	pepHooks, err := PEPHook(PEPHookConfig{Command: "/opt/olivares/bin/olivares-pep", TimeoutSecs: 15, Redact: true})
	if err != nil {
		t.Fatal(err)
	}
	orig := Policy{
		AllowManagedHooksOnly: true, // anti-tamper pairing
		Hooks:                 pepHooks,
		Env:                   TelemetryEnv(TelemetryConfig{Endpoint: "https://otel.corp.example:4317"}),
	}
	wire, err := Render(orig)
	if err != nil {
		t.Fatal(err)
	}
	s := string(wire)
	for _, want := range []string{
		`"hooks"`, `"PreToolUse"`, `"PostToolUse"`, `"command": "/opt/olivares/bin/olivares-pep"`,
		`"env"`, `"CLAUDE_CODE_ENABLE_TELEMETRY": "1"`, `"OTEL_EXPORTER_OTLP_ENDPOINT"`,
		`"allowManagedHooksOnly": true`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered managed-settings missing %q\n%s", want, s)
		}
	}
	got, err := ParsePolicyFromWire(wire)
	if err != nil {
		t.Fatal(err)
	}
	if !hasPreToolUseHook(got.Hooks) {
		t.Fatalf("round-trip lost the PreToolUse PEP hook: %+v", got.Hooks)
	}
	if !envHasTelemetry(got.Env) {
		t.Fatalf("round-trip lost the telemetry env: %+v", got.Env)
	}
}

// TestValidateJSON_EnvHooks verifies server-side validation rejects an inline credential
// in env and a hollow/malformed hook, but accepts a clean managed hooks+env document.
func TestValidateJSON_EnvHooks(t *testing.T) {
	// Inline collector token in OTEL_EXPORTER_OTLP_HEADERS → rejected.
	if issues := ValidateJSON([]byte(`{"env":{"OTEL_EXPORTER_OTLP_HEADERS":"Authorization=Bearer sk-secret-abc123"}}`)); len(issues) == 0 {
		t.Fatal("an inline collector credential in env must be rejected")
	}
	// Inline sk-ant- shaped secret in an arbitrary env value → rejected.
	if issues := ValidateJSON([]byte(`{"env":{"MY_TOKEN":"sk-ant-api01-AAAABBBBCCCCDDDD"}}`)); len(issues) == 0 {
		t.Fatal("an inline secret in an env value must be rejected")
	}
	// Hook with an empty command → rejected.
	if issues := ValidateJSON([]byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":""}]}]}}`)); len(issues) == 0 {
		t.Fatal("a hook with an empty command must be rejected")
	}
	// Hook with an unsupported type → rejected.
	if issues := ValidateJSON([]byte(`{"hooks":{"PreToolUse":[{"hooks":[{"type":"webhook","command":"x"}]}]}}`)); len(issues) == 0 {
		t.Fatal("an unsupported hook type must be rejected")
	}
	// A clean managed env + hook document validates.
	clean := `{"allowManagedHooksOnly":true,"env":{"CLAUDE_CODE_ENABLE_TELEMETRY":"1","OTEL_EXPORTER_OTLP_ENDPOINT":"https://otel.corp"},"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"/opt/olivares/bin/pep","timeout":15}]}]}}`
	if issues := ValidateJSON([]byte(clean)); len(issues) != 0 {
		t.Fatalf("a clean managed hooks+env document must validate: %v", issues)
	}
}

// TestDrift_PEPHookAndTelemetryAbsent verifies the host-drift findings: an authored PEP
// hook absent on the host is HIGH (observed, not governed); absent telemetry is MEDIUM.
func TestDrift_PEPHookAndTelemetryAbsent(t *testing.T) {
	authored := `{
      "allowManagedHooksOnly": true,
      "env": {"CLAUDE_CODE_ENABLE_TELEMETRY": "1"},
      "hooks": {"PreToolUse": [{"hooks": [{"type": "command", "command": "/opt/olivares/bin/pep"}]}]}
    }`
	observed := `{}` // host carries neither the PEP hook nor telemetry
	at := time.Unix(1_700_000_000, 0).UTC()
	findings, err := VerifyDriftJSON("host-pep", []byte(authored), []byte(observed), at)
	if err != nil {
		t.Fatal(err)
	}
	var pepHigh, telemMed bool
	for _, f := range findings {
		if strings.Contains(f.Title, "PreToolUse PEP hook is NOT distributed") && f.Severity == model.SeverityHigh {
			pepHigh = true
		}
		if strings.Contains(f.Title, "telemetry env is NOT distributed") && f.Severity == model.SeverityMedium {
			telemMed = true
		}
	}
	if !pepHigh {
		t.Errorf("absent PEP hook must drift HIGH; findings=%+v", findings)
	}
	if !telemMed {
		t.Errorf("absent telemetry env must drift MEDIUM; findings=%+v", findings)
	}
}

// TestDrift_PEPHookPresentNoDrift verifies a host that DOES carry the PEP hook and
// telemetry produces no PEP/telemetry drift (the governed, observed posture).
func TestDrift_PEPHookPresentNoDrift(t *testing.T) {
	doc := `{
      "allowManagedHooksOnly": true,
      "env": {"CLAUDE_CODE_ENABLE_TELEMETRY": "1"},
      "hooks": {"PreToolUse": [{"hooks": [{"type": "command", "command": "/opt/olivares/bin/pep"}]}]}
    }`
	at := time.Unix(1_700_000_000, 0).UTC()
	findings, err := VerifyDriftJSON("host-ok", []byte(doc), []byte(doc), at)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if strings.Contains(f.Title, "PreToolUse PEP hook") || strings.Contains(f.Title, "telemetry env") {
			t.Errorf("a host that carries both must not drift on them: %q", f.Title)
		}
	}
}

// TestAntiTamperReview verifies the authoring advisories: a PEP hook without
// allowManagedHooksOnly is flagged tamperable, and content-capture telemetry is flagged.
func TestAntiTamperReview(t *testing.T) {
	pep, _ := PEPHook(PEPHookConfig{Command: "/opt/olivares/bin/pep"})
	// Hooks without the lockdown → anti-tamper note.
	notes := AntiTamperReview(Policy{Hooks: pep})
	if len(notes) == 0 || !strings.Contains(strings.Join(notes, " "), "allowManagedHooksOnly") {
		t.Fatalf("PEP hook without lockdown must be flagged: %v", notes)
	}
	// Hooks WITH the lockdown and no content capture → no notes.
	if notes := AntiTamperReview(Policy{Hooks: pep, AllowManagedHooksOnly: true}); len(notes) != 0 {
		t.Fatalf("a locked-down PEP must produce no anti-tamper note: %v", notes)
	}
	// Content-capture telemetry → flagged.
	cc := AntiTamperReview(Policy{Env: map[string]string{EnvLogUserPrompts: "1"}})
	if len(cc) == 0 || !strings.Contains(strings.Join(cc, " "), EnvLogUserPrompts) {
		t.Fatalf("content-capture telemetry must be flagged: %v", cc)
	}
}
