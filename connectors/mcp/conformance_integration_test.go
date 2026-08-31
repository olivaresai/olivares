// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Tier-1 interop QUALIFICATION job: runs the MCP introspection binding against a
// REFERENCE MCP server provided out of band, so a green run is real evidence of protocol
// compatibility (unlike the deterministic httptest fixtures, which prove the wire binding
// only). Behind the `integration` tag (out of the default gate; run per-release via
// `task test:integration`, Actions being OFF). Credential-safe: SKIPS when the endpoint
// is not configured; no endpoint or secret is ever committed.
//
//	OLIVARES_MCP_CONFORMANCE_URL  streamable-HTTP URL of the reference MCP server (required)

package mcp

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

func TestConformanceMCPIntrospection(t *testing.T) {
	url := os.Getenv("OLIVARES_MCP_CONFORMANCE_URL")
	if url == "" {
		t.Skip("set OLIVARES_MCP_CONFORMANCE_URL to a reference MCP server to run this qualification job")
	}
	server := struct {
		Name      string `json:"name"`
		Transport string `json:"transport"`
		URL       string `json:"url"`
	}{Name: "conformance", Transport: "http", URL: url}
	serversJSON, err := json.Marshal([]any{server})
	if err != nil {
		t.Fatalf("marshal servers config: %v", err)
	}

	s := New()
	if err := s.Open(t.Context(), sdk.Config{Settings: map[string]string{cfgServers: string(serversJSON)}}); err != nil {
		t.Fatalf("open against reference MCP server %q: %v", url, err)
	}
	sink := &fakeSink{}
	if err := s.Gather(t.Context(), sink); err != nil {
		t.Fatalf("gather against reference MCP server %q: %v", url, err)
	}
	// A POSITIVE introspection outcome is required. "Some observation" is not enough: an
	// unreachable/non-MCP endpoint still emits a HEALTH finding, so a health-only pass
	// would be a false green. Conformance means introspectOne actually succeeded, proven
	// by either a capability edge (a discovered tool/resource) or a protocol-revision
	// finding (emitted ONLY after a successful introspection).
	edges, findings := sink.edges(), sink.findings()
	introspected := len(edges) > 0
	for _, f := range findings {
		if f.Kind == findingRevision {
			introspected = true
			break
		}
	}
	if !introspected {
		t.Fatalf("MCP introspection did NOT succeed against %q: %d capability edges, %d findings, none a %q finding — a health-only result means the binding never reached a live MCP server: %+v", url, len(edges), len(findings), findingRevision, findings)
	}
	t.Logf("MCP conformance: %d capability edges, %d findings from %q", len(edges), len(findings), url)
}
