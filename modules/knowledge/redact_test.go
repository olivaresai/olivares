// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"strings"
	"testing"
)

func TestScrubRemovesSecretsAndPII(t *testing.T) {
	body := strings.Join([]string{
		"Deploy with key AKIAIOSFODNN7EXAMPLE in prod.",
		"Token: ghp_abcdefghijklmnopqrstuvwxyz0123456789AB",
		"Auth header bearer abcdefghijklmnop.",
		"Slack xoxb" + "-12345678-abcdefghijkl token.",
		"Anthropic sk-ant-abcdefghijklmnopqrstuv key.",
		"password=hunter2secret in config.",
		"-----BEGIN RSA PRIVATE KEY-----",
		"Contact alice@acme.com for access.",
	}, "\n")
	clean, count := scrub(body)

	for _, secret := range []string{
		"AKIAIOSFODNN7EXAMPLE", "ghp_abcdefghijklmnopqrstuvwxyz0123456789AB",
		"xoxb" + "-12345678-abcdefghijkl", "sk-ant-abcdefghijklmnopqrstuv", "hunter2secret",
		"alice@acme.com",
	} {
		if strings.Contains(clean, secret) {
			t.Errorf("scrub left raw secret %q in: %q", secret, clean)
		}
	}
	if !strings.Contains(clean, redactPlaceholder) {
		t.Errorf("scrub should leave a redaction marker, got %q", clean)
	}
	if count < 7 {
		t.Errorf("expected at least 7 redactions, got %d", count)
	}
	if !containsSecret(body) {
		t.Error("containsSecret should detect the secrets")
	}
}

func TestScrubCleanTextUnchanged(t *testing.T) {
	body := "This is a perfectly ordinary paragraph about edge governance and retrieval."
	clean, count := scrub(body)
	if count != 0 {
		t.Errorf("clean text should yield 0 redactions, got %d", count)
	}
	if clean != body {
		t.Errorf("clean text should be unchanged, got %q", clean)
	}
	if containsSecret(body) {
		t.Error("containsSecret false positive on clean text")
	}
}

func TestScrubKeepsKeyDropsValue(t *testing.T) {
	clean, count := scrub("api_key=sk-supersecretvalue123456789")
	if count == 0 || strings.Contains(clean, "supersecretvalue") {
		t.Fatalf("key=value redaction failed: %q (count %d)", clean, count)
	}
	if !strings.Contains(strings.ToLower(clean), "api_key") {
		t.Errorf("redaction should keep the key, got %q", clean)
	}
}
