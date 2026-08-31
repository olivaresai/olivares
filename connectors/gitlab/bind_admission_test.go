// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

// Same shape as the GitHub receiver and the same defect: gather.go serves the
// webhook mux over PLAINTEXT HTTP and the default address was the WILDCARD
// ":9801" (finding H-03 of the the model contrast of PR #565).

func validGitLabSettings(addr string) map[string]string {
	return map[string]string{
		"group":           "acme",
		"token":           "glpat-token",
		"webhook_secret":  "s3cret",
		"webhook_address": addr,
	}
}

func TestGitLabRefusesAPlaintextPublicWebhookBind(t *testing.T) {
	for _, addr := range []string{":9801", "0.0.0.0:9801", "[::]:9801", "192.0.2.7:9801"} {
		t.Run(addr, func(t *testing.T) {
			s := New()
			err := s.Open(context.Background(), sdk.Config{Settings: validGitLabSettings(addr)})
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

func TestGitLabAcceptsAPublicWebhookBindWhenDeclared(t *testing.T) {
	set := validGitLabSettings("0.0.0.0:9801")
	set["allow_public_bind"] = "true"
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: set}); err != nil {
		t.Fatalf("a declared public bind must be accepted: %v", err)
	}
	_ = s.Close(context.Background())
}

func TestGitLabAcceptsALoopbackWebhookBind(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:9801", "localhost:9801", "[::1]:9801"} {
		t.Run(addr, func(t *testing.T) {
			s := New()
			if err := s.Open(context.Background(), sdk.Config{Settings: validGitLabSettings(addr)}); err != nil {
				t.Fatalf("a loopback bind must need no opt-in: %v", err)
			}
			_ = s.Close(context.Background())
		})
	}
}

func TestGitLabDefaultWebhookAddressIsLoopback(t *testing.T) {
	set := validGitLabSettings("")
	delete(set, "webhook_address")
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: set}); err != nil {
		t.Fatalf("the default configuration must Open cleanly: %v", err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	if strings.HasPrefix(s.webhookAddr, ":") || strings.HasPrefix(s.webhookAddr, "0.0.0.0") {
		t.Errorf("default webhook address %q is a WILDCARD bind; a careless install must not become a public surface", s.webhookAddr)
	}

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
