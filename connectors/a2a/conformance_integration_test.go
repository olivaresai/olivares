// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// This is a Tier-1 interop QUALIFICATION job, not a unit test. Unit tests use
// deterministic httptest fixtures (discover_test.go), which prove the wire binding but
// NOT compatibility with a live A2A implementation. This job runs the real discovery
// binding against a REFERENCE A2A agent-card server provided out of band, so a green
// run is actual evidence for a "conformance-defined" / "continuously-verified" badge in
// connectors/interop/tier1-matrix.json. It is behind the `integration` build tag (absent
// from the default gate; run per-release via `task test:integration`) because Actions is
// OFF in this repo and it needs an external endpoint.
//
// Credential-safe: it SKIPS cleanly when the endpoint is not configured, so it never
// fails a developer's machine and never embeds an endpoint or key in the repo.
//
//	OLIVARES_A2A_CONFORMANCE_URL   base URL of the reference agent (required to run)
//	OLIVARES_A2A_CONFORMANCE_JWKS  the agent's trust JWKS (optional; if absent the card's
//	                               jku is allowed to resolve the signing keys)

package a2a

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func TestConformanceA2ADiscovery(t *testing.T) {
	url := os.Getenv("OLIVARES_A2A_CONFORMANCE_URL")
	if url == "" {
		t.Skip("set OLIVARES_A2A_CONFORMANCE_URL to a reference A2A agent to run this qualification job")
	}
	jwks := os.Getenv("OLIVARES_A2A_CONFORMANCE_JWKS")

	type agentSpecJSON struct {
		Name      string          `json:"name"`
		URL       string          `json:"url"`
		TrustJWKS json.RawMessage `json:"trust_jwks,omitempty"`
	}
	agent := agentSpecJSON{Name: "conformance", URL: url}
	settings := map[string]string{}
	if jwks != "" {
		agent.TrustJWKS = json.RawMessage(jwks)
	} else {
		// No pinned JWKS supplied: let the card's jku fetch its own signing keys so the
		// discovery binding can still complete against the reference server.
		settings[cfgAllowJKU] = "true"
	}
	agentsJSON, err := json.Marshal([]agentSpecJSON{agent})
	if err != nil {
		t.Fatalf("marshal agents config: %v", err)
	}
	settings[cfgAgents] = string(agentsJSON)

	s := New()
	if err := s.Open(t.Context(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("open against reference A2A agent %q: %v", url, err)
	}
	sink := &fakeSink{}
	if err := s.Gather(t.Context(), sink); err != nil {
		t.Fatalf("gather against reference A2A agent %q: %v", url, err)
	}
	// A POSITIVE outcome is required — "some finding" is not enough, because an
	// unreachable/404/garbage endpoint emits a discovery-FAILURE finding. Conformance
	// means the binding actually reached the reference server, fetched the well-known
	// card and ran it through signature evaluation:
	//   (1) NO discovery-failure finding, and
	//   (2) at least one trust finding (a card was fetched and its signature evaluated).
	if disc := sink.findingsOfKind(findingDiscovery); len(disc) > 0 {
		t.Fatalf("A2A discovery FAILED against %q (%d discovery-failure findings) — the binding did not fetch/parse a live agent card: %+v", url, len(disc), disc)
	}
	trust := sink.findingsOfKind(findingTrust)
	if len(trust) == 0 {
		t.Fatalf("no A2A trust finding from %q — a live discovery must fetch a card and evaluate its signature; the binding did not reach the reference server", url)
	}
	// With a pinned JWKS we assert the card actually VERIFIED (SeverityInfo == the
	// trustVerified outcome); an unsigned/unverifiable card against a pinned key is a
	// real conformance failure, not a pass.
	if jwks != "" {
		verified := false
		for _, f := range trust {
			if f.Severity == model.SeverityInfo {
				verified = true
				break
			}
		}
		if !verified {
			t.Fatalf("A2A card from %q did NOT verify against the supplied JWKS (no trustVerified finding): %+v", url, trust)
		}
	}
	for _, f := range trust {
		t.Logf("A2A conformance trust finding: severity=%s title=%q", f.Severity, f.Title)
	}
}
