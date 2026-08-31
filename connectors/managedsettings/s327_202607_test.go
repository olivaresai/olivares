// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

// 2026-07 managed-settings currency (VERIFIED 2026-07-03 against
// code.claude.com/docs/en/{settings,sandboxing}). Per new key these tests prove
// the lockstep property: parseLive observes the wire shape, Render preserves it,
// and drift fires only under the accepted conditions.

const wireS327 = `{
  "disableSideloadFlags": true,
  "pluginTrustMessage": "Install only Olivares-approved plugins.",
  "disableSkillShellExecution": true,
  "sandbox": {
    "credentials": {
      "files": [{"path": "~/.aws/credentials", "mode": "deny"}],
      "envVars": [
        {"name": "GH_TOKEN", "mode": "mask", "injectHosts": ["api.github.com"]},
        {"name": "NPM_TOKEN", "mode": "deny"}
      ],
      "allowPlaintextInject": false
    }
  }
}`

func TestS327ParseLiveReadsWire(t *testing.T) {
	live, err := parseLive([]byte(wireS327))
	if err != nil {
		t.Fatalf("parseLive: %v", err)
	}
	if !live.DisableSideloadFlags || live.PluginTrustMessage == "" || !live.DisableSkillShellExecution {
		t.Fatalf("top-level keys not decoded: %+v", live)
	}
	if !live.credentialsProtectionSet() {
		t.Fatal("sandbox.credentials files/envVars must count as credential protection")
	}
	if !live.credentialMaskUsed() {
		t.Fatal("sandbox.credentials envVars mode=mask must be detected")
	}
	if live.Sandbox == nil || live.Sandbox.Credentials == nil || !rawPresent(live.Sandbox.Credentials.AllowPlaintextInject) {
		t.Fatalf("allowPlaintextInject presence not preserved: %+v", live.Sandbox)
	}
}

func TestS327RoundTripPreservesNewKeys(t *testing.T) {
	p := Policy{
		DisableSideloadFlags:       true,
		PluginTrustMessage:         "Install only Olivares-approved plugins.",
		DisableSkillShellExecution: true,
		SandboxCredentials: &msSandboxCredentials{
			Files: []msCredentialFileRule{{Path: "~/.aws/credentials", Mode: "deny"}},
			EnvVars: []msCredentialEnvRule{
				{Name: "GH_TOKEN", Mode: "mask", InjectHosts: []string{"api.github.com"}},
				{Name: "NPM_TOKEN", Mode: "deny"},
			},
			AllowPlaintextInject: []byte(`false`),
		},
	}
	rendered, err := Render(p)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	s := string(rendered)
	for _, want := range []string{
		`"disableSideloadFlags": true`,
		`"pluginTrustMessage": "Install only Olivares-approved plugins."`,
		`"disableSkillShellExecution": true`,
		`"credentials"`,
		`"path": "~/.aws/credentials"`,
		`"mode": "mask"`,
		`"injectHosts"`,
		`"allowPlaintextInject": false`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered policy missing %q\n%s", want, s)
		}
	}
	live, err := parseLive(rendered)
	if err != nil {
		t.Fatalf("parse rendered: %v", err)
	}
	if !live.DisableSideloadFlags || live.PluginTrustMessage != p.PluginTrustMessage ||
		!live.DisableSkillShellExecution || live.Sandbox == nil ||
		!sameSandboxCredentials(p.SandboxCredentials, live.Sandbox.Credentials) {
		t.Fatalf("Render→parseLive lost fields: live=%+v", live)
	}
	canon, err := CanonicalJSON([]byte(wireS327))
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	if !strings.Contains(string(canon), `"disableSkillShellExecution": true`) ||
		!strings.Contains(string(canon), `"allowPlaintextInject": false`) {
		t.Fatalf("CanonicalJSON lost fields:\n%s", canon)
	}
	if !HasAnyKeys([]byte(wireS327)) {
		t.Fatal("HasAnyKeys must report a keys-only document as governing")
	}
	if issues := ValidateJSON([]byte(wireS327)); len(issues) != 0 {
		t.Fatalf("fixture must validate clean, got %v", issues)
	}
}

func TestS327DriftChecks(t *testing.T) {
	strictLive := mustParseS327(t, `{"strictKnownMarketplaces":[{"source":"github","repo":"acme/plugins"}]}`)
	fs := driftFindings("h", Policy{}, strictLive, testNow())
	if sev, ok := s327Severity(fs, "plugin sideload flags are NOT rejected"); !ok || sev != model.SeverityHigh {
		t.Fatalf("sideload absent + strict marketplace = (%v, %v), want High; findings=%+v", sev, ok, fs)
	}

	// The `[]` complete-lockdown posture is the configuration MOST exposed to
	// the sideload bypass — a present-but-empty strictKnownMarketplaces must count.
	emptyLockdownLive := mustParseS327(t, `{"strictKnownMarketplaces":[]}`)
	fs = driftFindings("h", Policy{}, emptyLockdownLive, testNow())
	if sev, ok := s327Severity(fs, "plugin sideload flags are NOT rejected"); !ok || sev != model.SeverityHigh {
		t.Fatalf("sideload absent + empty-array lockdown = (%v, %v), want High; findings=%+v", sev, ok, fs)
	}

	fs = driftFindings("h", Policy{}, managedSettings{}, testNow())
	if s327Has(fs, "plugin sideload flags are NOT rejected") {
		t.Fatalf("sideload absent without marketplace lockdown must not drift: %+v", fs)
	}

	fs = driftFindings("h", Policy{DisableSkillShellExecution: true}, managedSettings{}, testNow())
	if sev, ok := s327Severity(fs, "inline shell execution in skills/custom commands"); !ok || sev != model.SeverityMedium {
		t.Fatalf("skill shell absent = (%v, %v), want Medium; findings=%+v", sev, ok, fs)
	}

	lockdownNoCreds := mustParseS327(t, `{"sandbox":{"network":{"allowManagedDomainsOnly":true}}}`)
	fs = driftFindings("h", Policy{}, lockdownNoCreds, testNow())
	if sev, ok := s327Severity(fs, "sandbox.credentials is empty"); !ok || sev != model.SeverityMedium {
		t.Fatalf("sandbox lockdown without credentials = (%v, %v), want Medium; findings=%+v", sev, ok, fs)
	}

	lockdownWithCreds := mustParseS327(t, `{
	  "sandbox": {
	    "network": {"allowManagedDomainsOnly": true},
	    "credentials": {"files": [{"path": "~/.aws/credentials", "mode": "deny"}]}
	  }
	}`)
	fs = driftFindings("h", Policy{}, lockdownWithCreds, testNow())
	if s327Has(fs, "sandbox.credentials is empty") {
		t.Fatalf("sandbox lockdown with credential entries must not drift: %+v", fs)
	}

	expectedCreds := Policy{SandboxCredentials: &msSandboxCredentials{
		EnvVars: []msCredentialEnvRule{{Name: "NPM_TOKEN", Mode: "deny"}},
	}}
	fs = driftFindings("h", expectedCreds, managedSettings{}, testNow())
	if sev, ok := s327Severity(fs, "sandbox.credentials on host drifts"); !ok || sev != model.SeverityMedium {
		t.Fatalf("credential mismatch = (%v, %v), want Medium; findings=%+v", sev, ok, fs)
	}
	rendered, err := Render(expectedCreds)
	if err != nil {
		t.Fatalf("render credentials: %v", err)
	}
	matching, err := parseLive(rendered)
	if err != nil {
		t.Fatalf("parse rendered credentials: %v", err)
	}
	if fs := driftFindings("h", expectedCreds, matching, testNow()); len(fs) != 0 {
		t.Fatalf("matching sandbox.credentials must not drift: %+v", fs)
	}

	if fs := driftFindings("h", Policy{PluginTrustMessage: "Read policy first."}, managedSettings{}, testNow()); s327Has(fs, "pluginTrustMessage") {
		t.Fatalf("pluginTrustMessage is UX-only and must not drift: %+v", fs)
	}
}

func TestS327PrecedencePreviewCarriesCredentialFacts(t *testing.T) {
	var saw bool
	for _, line := range PrecedencePreview() {
		if line.Scope == "sandbox-credentials-merge" &&
			strings.Contains(line.Note, "deny takes precedence over mask") &&
			strings.Contains(line.Note, "project/.local ignored") {
			saw = true
		}
	}
	if !saw {
		t.Fatal("precedence preview missing sandbox.credentials merge/mask facts")
	}
}

func mustParseS327(t *testing.T, doc string) managedSettings {
	t.Helper()
	live, err := parseLive([]byte(doc))
	if err != nil {
		t.Fatalf("parseLive: %v", err)
	}
	return live
}

func s327Severity(findings []model.FindingReport, sub string) (model.Severity, bool) {
	for _, f := range findings {
		if strings.Contains(f.Title, sub) {
			return f.Severity, true
		}
	}
	return "", false
}

func s327Has(findings []model.FindingReport, sub string) bool {
	_, ok := s327Severity(findings, sub)
	return ok
}
