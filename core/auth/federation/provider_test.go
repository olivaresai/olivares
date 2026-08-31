// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package federation

import (
	"errors"
	"testing"
)

// envFrom returns a getenv backed by a map (the same injection point production uses).
func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestFromEnv_NotConfigured(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"empty protocol", map[string]string{}},
		{"unknown protocol", map[string]string{envProtocol: "kerberos"}},
		{"oidc missing issuer", map[string]string{envProtocol: "oidc", envOIDCClientID: "client-1"}},
		{"oidc missing client id", map[string]string{envProtocol: "oidc", envOIDCIssuer: "https://idp.example.com"}},
		{"oidc relative issuer", map[string]string{envProtocol: "oidc", envOIDCIssuer: "/not-absolute", envOIDCClientID: "client-1"}},
		{"saml missing fields", map[string]string{envProtocol: "saml", envSAMLEntityID: "sp"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := FromEnv(envFrom(tc.env))
			if !errors.Is(err, ErrNotConfigured) {
				t.Fatalf("err = %v, want wraps ErrNotConfigured", err)
			}
			if p != nil {
				t.Errorf("a partial/absent configuration must yield no provider, never a half-wired one; got %#v", p)
			}
		})
	}
}

func TestMustAbsURL(t *testing.T) {
	if _, err := mustAbsURL("https://idp.example.com/sso"); err != nil {
		t.Errorf("an absolute URL must be accepted; got %v", err)
	}
	for _, bad := range []string{"", "/relative/path", "idp.example.com", "://nope"} {
		if _, err := mustAbsURL(bad); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("mustAbsURL(%q) err = %v, want wraps ErrNotConfigured", bad, err)
		}
	}
}
