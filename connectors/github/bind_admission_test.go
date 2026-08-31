// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

// The webhook receiver serves PLAINTEXT HTTP: gather.go hands the mux to
// http.Server and never wraps it in TLS. A non-loopback bind therefore puts the
// webhook body — and the HMAC signature header that authenticates it — on the
// wire in clear, and exposes an unauthenticated-by-default port to anything that
// can route to the host. Every sibling receiver in this tree already refuses
// that shape unless the operator declares it (connectors/{ssf,claude,cowork,
// aaa,envoy,claude-managed-agents}); this connector did not, and the default
// address was the WILDCARD ":9800" (from the the model contrast of
// PR #565, finding H-03).

func validGitHubSettings(addr string) map[string]string {
	return map[string]string{
		"org":             "acme",
		"webhook_secret":  "s3cret",
		"pat":             "ghp_token",
		"webhook_address": addr,
	}
}

func TestGitHubRefusesAPlaintextPublicWebhookBind(t *testing.T) {
	for _, addr := range []string{":9800", "0.0.0.0:9800", "[::]:9800", "192.0.2.7:9800"} {
		t.Run(addr, func(t *testing.T) {
			s := New()
			err := s.Open(context.Background(), sdk.Config{Settings: validGitHubSettings(addr)})
			if err == nil {
				_ = s.Close(context.Background())
				t.Fatalf("Open accepted a plaintext non-loopback webhook bind %q without allow_public_bind", addr)
			}
			if !strings.Contains(err.Error(), "allow_public_bind") {
				t.Errorf("refusal must name the opt-in that unblocks it; got: %v", err)
			}
		})
	}
}

func TestGitHubAcceptsAPublicWebhookBindWhenDeclared(t *testing.T) {
	set := validGitHubSettings("0.0.0.0:9800")
	set["allow_public_bind"] = "true"
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: set}); err != nil {
		t.Fatalf("a declared public bind must be accepted: %v", err)
	}
	_ = s.Close(context.Background())
}

func TestGitHubAcceptsALoopbackWebhookBind(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:9800", "localhost:9800", "[::1]:9800"} {
		t.Run(addr, func(t *testing.T) {
			s := New()
			if err := s.Open(context.Background(), sdk.Config{Settings: validGitHubSettings(addr)}); err != nil {
				t.Fatalf("a loopback bind must need no opt-in: %v", err)
			}
			_ = s.Close(context.Background())
		})
	}
}

// The secure default is the other half of the fix: an operator who configures
// nothing must not get a wildcard listener.
func TestGitHubDefaultWebhookAddressIsLoopback(t *testing.T) {
	set := validGitHubSettings("")
	delete(set, "webhook_address")
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: set}); err != nil {
		t.Fatalf("the default configuration must Open cleanly: %v", err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	if strings.HasPrefix(s.webhookAddr, ":") || strings.HasPrefix(s.webhookAddr, "0.0.0.0") {
		t.Errorf("default webhook address %q is a WILDCARD bind; a careless install must not become a public surface", s.webhookAddr)
	}

	// The advertised default must match the applied one, or the descriptor lies
	// to every operator reading `olivares connectors describe`.
	var advertised string
	for _, f := range s.Descriptor().ConfigFields {
		if f.Key == "webhook_address" {
			advertised = f.Default
		}
	}
	if advertised != s.webhookAddr {
		t.Errorf("descriptor advertises default %q but Open applies %q", advertised, s.webhookAddr)
	}
}
