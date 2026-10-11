// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/olivaresai/olivares/core/secret"
)

// The tools' children inherit the engine's proxy and CA variables by name, in
// any case, and nothing near them; a name the engine resolved as a secret stays
// behind in both of its cases (#547).
func TestHostNetworkEnvIsTheEnginesWayOutOnly(t *testing.T) {
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); name != "" {
			if proxy, trust := driverfacts.NetworkEnv(name); proxy || trust {
				t.Setenv(name, "")
				_ = os.Unsetenv(name)
			}
		}
	}
	for name, value := range map[string]string{
		"https_proxy": "http://corp:3128", "Http_Proxy": "http://corp:3128", "ssl_cert_file": "/etc/corp.pem",
		"HTTPS_PROXY_X": "x", "PROXY": "x", "SSL_CERT_DIR": "/etc/ssl/certs",
		"CURL_CA_BUNDLE": "/etc/engine.pem", "curl_ca_bundle": "/etc/engine.pem",
	} {
		t.Setenv(name, value)
	}
	// The record is process-wide: no other test names CURL_CA_BUNDLE.
	if _, err := (secret.EnvHandler{}).Resolve(context.Background(), "CURL_CA_BUNDLE"); err != nil {
		t.Fatal(err)
	}
	want := []string{"Http_Proxy=http://corp:3128", "https_proxy=http://corp:3128", "ssl_cert_file=/etc/corp.pem"}
	got := hostNetworkEnv()
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("hostNetworkEnv() names %q, want %q", envNames(got), want) // never values: a regression may carry secrets
	}
	// The probe of the engine user's own login reaches the vendor the same way.
	own, _, err := ownLogin("codex")
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range want {
		if !slices.Contains(own, kv) {
			t.Errorf("ownLogin env lacks %s; it has %q", kv, envNames(own))
		}
	}
}
