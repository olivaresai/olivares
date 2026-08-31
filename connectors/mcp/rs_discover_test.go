// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import "testing"

// TestMethodPolicyAdmitsServerDiscover pins the one RPC the specification makes
// mandatory. MCP 2026-07-28 (server/discover.mdx): "lets a client query a
// server's supported protocol versions, capabilities, and identity before
// sending any other requests. Servers MUST implement it."
//
// It was absent from the authorization matrix, so it fell through to
// default-deny and the gateway answered 403 to the first call a conforming
// client makes — the gateway advertised a revision whose mandatory entry point
// it refused. Admitting it exposes no server content (versions, capabilities,
// identity, cache hints); every listing and read behind it keeps its family
// scope, which the companion assertions below pin so this admission cannot be
// mistaken for a general relaxation.
func TestMethodPolicyAdmitsServerDiscover(t *testing.T) {
	t.Parallel()
	scope, known := methodPolicy(methodServerDiscover)
	if !known {
		t.Fatal("server/discover must be admitted: the spec says servers MUST implement it, and default-deny answers 403 to the first call a conforming client makes")
	}
	if scope != "" {
		t.Errorf("server/discover required scope = %q, want empty: it is the stateless model's protocol/identity surface, like initialize and ping", scope)
	}

	// The admission must not have widened anything else.
	for _, tc := range []struct {
		method string
		scope  string
	}{
		{"resources/read", scopeResourcesRead},
		{"resources/list", scopeResourcesRead},
		{"prompts/get", scopePromptsRead},
		{"prompts/list", scopePromptsRead},
	} {
		got, known := methodPolicy(tc.method)
		if !known || got != tc.scope {
			t.Errorf("methodPolicy(%q) = (%q, %v), want (%q, true)", tc.method, got, known, tc.scope)
		}
	}
	if _, known := methodPolicy("server/somethingElse"); known {
		t.Error("an unknown server/* method must stay default-denied")
	}
}
