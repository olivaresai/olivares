// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"os"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

// helperSpec returns a serverSpec that re-executes this test binary as a canned
// MCP stdio server (see TestHelperMCPServer).
func helperSpec(name string) serverSpec {
	return serverSpec{
		Name:      name,
		Transport: transportStdio,
		Command:   os.Args[0],
		Args:      []string{"-test.run=^TestHelperMCPServer$"},
		Env:       map[string]string{helperEnv: "1"},
	}
}

func TestIntrospectOverStdio(t *testing.T) {
	cat, err := introspect(t.Context(), helperSpec("helper"))
	if err != nil {
		t.Fatalf("introspect over stdio: %v", err)
	}
	if len(cat.tools) != 2 {
		t.Fatalf("tools (with pagination) = %d, want 2", len(cat.tools))
	}
	if cat.tools[0].Name != "read_file" || cat.tools[1].Name != "delete_file" {
		t.Errorf("tool names = %+v", cat.tools)
	}
	if len(cat.resources) != 1 || len(cat.templates) != 1 || len(cat.prompts) != 1 {
		t.Errorf("catalog = %+v", cat)
	}

	// End-to-end through the mapper: the read-only tool is read, the destructive
	// tool is readwrite, and all carry UNTRUSTED provenance.
	edges := capabilityEdges("helper", cat, capTime)
	byRef := map[string]model.EdgeObservation{}
	for _, e := range edges {
		byRef[e.ResourceRef] = e
	}
	if byRef["helper/read_file"].Mode != model.ModeRead {
		t.Errorf("read_file = %+v", byRef["helper/read_file"])
	}
	if byRef["helper/delete_file"].Mode != model.ModeReadWrite {
		t.Errorf("delete_file = %+v", byRef["helper/delete_file"])
	}
}

func TestStdioMissingCommand(t *testing.T) {
	if _, err := newStdioTransport(t.Context(), serverSpec{Name: "x", Transport: transportStdio}); err == nil {
		t.Error("stdio transport with no command must error")
	}
}
