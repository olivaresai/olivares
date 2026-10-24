// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"testing"
	"time"
)

// Each surface compares the published document with the host file through its own
// connector: a host that matches is clean, a weakened host is a finding of the tool's own kind, and the hooks, managed-mcp and sandbox surfaces have no content diff.
func TestSurfaceDriftUsesEachToolsConnector(t *testing.T) {
	at := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		surface, authored, weakened, origin string
	}{
		{surfaceManagedSettings, `{"forceRemoteSettingsRefresh":true}`, `{}`, "managed_policy"},
		{surfaceCodexRequirements, "allowed_approval_policies = [\"on-request\"]\n", "allowed_approval_policies = [\"on-request\", \"never\"]\n", "codex_managed_config"},
		{surfaceCodexManagedConfig, "approval_policy = \"on-request\"\n", "approval_policy = \"never\"\n", "codex_managed_config"},
		{surfaceGrokRequirements, "[sandbox]\nprofile = \"strict\"\n", "[sandbox]\nprofile = \"off\"\n", "grok_managed_config"},
		{surfaceOpenCodeConfig, `{"permission":{"edit":"deny"}}`, `{"permission":{"edit":"allow"}}`, "opencode_managed_config"},
		{surfaceGeminiCLISettings, `{"security":{"disableYoloMode":true}}`, `{"security":{"disableYoloMode":false}}`, "gemini_cli_managed_config"},
	}
	for _, tt := range tests {
		t.Run(tt.surface, func(t *testing.T) {
			if !surfaceHasContentDiff(tt.surface) {
				t.Fatalf("surfaceHasContentDiff(%q) = false", tt.surface)
			}
			// What publish accepts, the comparison can read: a published document never
			// makes a check-in answer "drift not computed".
			if issues, _ := validateSurfaceContent(tt.surface, tt.authored); len(issues) > 0 {
				t.Fatalf("the sample document is not valid: %v", issues)
			}
			clean, supported, err := surfaceDrift(tt.surface, "host-1", []byte(tt.authored), []byte(tt.authored), at)
			if err != nil || !supported || len(clean) != 0 {
				t.Fatalf("the published file on the host: supported=%t err=%v findings=%+v", supported, err, clean)
			}
			got, _, err := surfaceDrift(tt.surface, "host-1", []byte(tt.authored), []byte(tt.weakened), at)
			if err != nil || len(got) == 0 {
				t.Fatalf("a weakened host: err=%v findings=%+v", err, got)
			}
			if got[0].SubjectKind != tt.origin || got[0].SubjectRef != "host-1" || got[0].Kind != "policy_drift" {
				t.Fatalf("finding = %+v", got[0])
			}
			absent, _, err := surfaceDrift(tt.surface, "host-1", []byte(tt.authored), nil, at)
			if err != nil || len(absent) == 0 {
				t.Fatalf("no file on the host must be a finding, not a pass: err=%v findings=%+v", err, absent)
			}
		})
	}
	for _, surface := range []string{surfaceHooks, surfaceManagedMCP, surfaceSandbox} {
		if surfaceHasContentDiff(surface) {
			t.Errorf("surfaceHasContentDiff(%q) = true; its drift is the connector findings stream", surface)
		}
		if _, supported, err := surfaceDrift(surface, "host-1", []byte(`{}`), []byte(`{}`), at); supported || err != nil {
			t.Errorf("surfaceDrift(%q): supported=%t err=%v", surface, supported, err)
		}
	}
}

func TestSurfaceDriftRefusesAMalformedPublishedDocument(t *testing.T) {
	at := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	for _, surface := range []string{surfaceCodexRequirements, surfaceCodexManagedConfig, surfaceGrokRequirements, surfaceOpenCodeConfig, surfaceGeminiCLISettings} {
		if _, _, err := surfaceDrift(surface, "host-1", []byte("= not a document {"), []byte(""), at); err == nil {
			t.Errorf("%s: a malformed published document must be an error, not a clean comparison", surface)
		}
	}
}

// A Codex file that sets keys the connector's typed model does not hold must still be compared:
// the typed comparison alone reports nothing for it, even for a host with no file.
func TestCodexKeysTheTypedModelDoesNotHoldAreStillCompared(t *testing.T) {
	at := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	for _, surface := range []string{surfaceCodexManagedConfig, surfaceCodexRequirements} {
		doc := "model = \"gpt-5\"\n"
		for name, host := range map[string]string{"absent": "", "different": "model = \"o3\"\n", "unset": "other = 1\n"} {
			got, supported, err := surfaceDrift(surface, "host-1", []byte(doc), []byte(host), at)
			if err != nil || !supported || len(got) == 0 {
				t.Errorf("%s with a %s host: supported=%t err=%v findings=%d, want drift", surface, name, supported, err, len(got))
			}
		}
		if got, _, err := surfaceDrift(surface, "host-1", []byte(doc), []byte(doc+"extra = 1\n"), at); err != nil || len(got) != 0 {
			t.Errorf("%s: a host that sets the key and adds its own: err=%v findings=%d", surface, err, len(got))
		}
	}
}

// One drifting fact is one finding. Codex has two comparisons (the connector's typed model
// and the file-level one); the file-level one only speaks for what the typed model is silent
// about, so a widened allowlist or a missing file is not reported twice.
func TestCodexReportsOneFindingPerDriftingFact(t *testing.T) {
	at := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name, surface, authored, host string
	}{
		{"requirements widened", surfaceCodexRequirements, "allowed_approval_policies = [\"on-request\"]\n", "allowed_approval_policies = [\"on-request\", \"never\"]\n"},
		{"requirements absent", surfaceCodexRequirements, "allowed_approval_policies = [\"on-request\"]\n", ""},
		{"managed config changed", surfaceCodexManagedConfig, "approval_policy = \"on-request\"\n", "approval_policy = \"never\"\n"},
		{"managed config absent", surfaceCodexManagedConfig, "approval_policy = \"on-request\"\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := surfaceDrift(tt.surface, "host-1", []byte(tt.authored), []byte(tt.host), at)
			if err != nil || len(got) != 1 {
				t.Fatalf("err=%v findings=%d, want exactly 1: %+v", err, len(got), got)
			}
		})
	}
}
