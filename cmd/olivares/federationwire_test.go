// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"log/slog"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// TestNewFederationKeepsInvalidEnvCause covers the env fallback: an absent env
// SSO config is plain NoFederation, while a configured protocol that does not build
// keeps its cause and protocol for the SP metadata endpoint.
func TestNewFederationKeepsInvalidEnvCause(t *testing.T) {
	for _, protocol := range []string{"", auth.ProtocolSAML, auth.ProtocolOIDC} {
		t.Run(protocol, func(t *testing.T) {
			env := map[string]string{"OLIVARES_SSO_PROTOCOL": protocol} // no other part: never builds
			fed := newFederation(func(k string) string { return env[k] }, slog.New(slog.DiscardHandler))
			absent, ok := fed.(auth.NoFederation)
			if !ok || (absent.Cause != nil) != (protocol != "") || absent.ConfiguredProtocol != protocol {
				t.Fatalf("newFederation = %#v, want NoFederation with a cause only for a set protocol %q", fed, protocol)
			}
		})
	}
}
