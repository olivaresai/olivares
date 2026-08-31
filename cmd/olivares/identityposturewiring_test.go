// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"io"
	"log/slog"
	"testing"
)

// The read-only Admin credential is read from ONE place, so the rate-limit inventory and
// the identity posture can never drift onto different environment variables.
func TestClaudeAdminSettingsAreAbsentWithoutACredential(t *testing.T) {
	if _, ok := claudeAdminSettings(envFrom(map[string]string{})); ok {
		t.Error("settings reported present with no OLIVARES_CLAUDE_ADMIN_KEY")
	}
	// Whitespace is not a credential.
	if _, ok := claudeAdminSettings(envFrom(map[string]string{"OLIVARES_CLAUDE_ADMIN_KEY": "   "})); ok {
		t.Error("blank OLIVARES_CLAUDE_ADMIN_KEY accepted as a credential")
	}
}

func TestClaudeAdminSettingsCarryTheOptionalOverrides(t *testing.T) {
	s, ok := claudeAdminSettings(envFrom(map[string]string{
		"OLIVARES_CLAUDE_ADMIN_KEY":    "sk-ant-admin-x",
		"ANTHROPIC_BASE_URL":           "https://proxy.internal",
		"OLIVARES_CLAUDE_WORKSPACE_ID": "wrkspc_9",
	}))
	if !ok {
		t.Fatal("settings absent though a credential is set")
	}
	for k, want := range map[string]string{
		"admin_key": "sk-ant-admin-x", "base_url": "https://proxy.internal", "workspace_id": "wrkspc_9",
	} {
		if s[k] != want {
			t.Errorf("settings[%q] = %q, want %q", k, s[k], want)
		}
	}
}

func TestClaudeAdminSettingsOmitUnsetOverrides(t *testing.T) {
	s, ok := claudeAdminSettings(envFrom(map[string]string{"OLIVARES_CLAUDE_ADMIN_KEY": "sk-ant-admin-x"}))
	if !ok {
		t.Fatal("settings absent though a credential is set")
	}
	if _, present := s["base_url"]; present {
		t.Error("base_url present though ANTHROPIC_BASE_URL is unset")
	}
	if _, present := s["workspace_id"]; present {
		t.Error("workspace_id present though OLIVARES_CLAUDE_WORKSPACE_ID is unset")
	}
}

// NO CREDENTIAL ⇒ NO PROVIDER, so the identity console mounts the routes and answers
// available=false with a reason rather than an empty inventory that would read as
// "this organization has no customer-managed keys".
func TestIdentityPostureProviderIsNilWithoutACredential(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if p := newIdentityPostureProvider(envFrom(map[string]string{}), log); p != nil {
		t.Errorf("provider = %v with no Admin credential, want nil", p)
	}
}

func TestIdentityPostureProviderIsWiredWithACredential(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := newIdentityPostureProvider(envFrom(map[string]string{"OLIVARES_CLAUDE_ADMIN_KEY": "sk-ant-admin-x"}), log)
	if p == nil {
		t.Fatal("provider is nil though an Admin credential is configured")
	}
}
