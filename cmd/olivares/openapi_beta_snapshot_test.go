// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

// The committed snapshot is the Community document: an enterprise build registers more routes.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// maxDriftLines bounds the operations a failure names; the rest is a count.
const maxDriftLines = 10

// TestBetaOpenAPIMatchesCommittedSnapshot binds web/openapi/openapi.beta.json to the module
// routes the production module set registers, as core/api's
// TestStableOpenAPIMatchesUnchangedSnapshot does for the stable document.
// TestBetaOpenAPICoversEveryMountedModuleRoute binds the document to the mounted routes; this
// binds the committed file to the document. `task openapi:check` finds the same drift but needs
// the web toolchain, so a route added without its snapshot reached main and was found only by
// the public export (#1910). Dev runs no suite that includes this package before a merge, so
// AGENTS.md "Work and verification" has a route change run this test by name and paste the result.
func TestBetaOpenAPIMatchesCommittedSnapshot(t *testing.T) {
	committed, err := os.ReadFile("../../web/openapi/openapi.beta.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := moduleOpenAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	generated, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	// The command prints the document and one newline; `task openapi:check` diffs these bytes.
	generated = append(generated, '\n')
	if bytes.Equal(generated, committed) {
		return
	}
	t.Fatalf("web/openapi/openapi.beta.json is stale%s\nrun `task web:codegen` and commit the snapshots and the generated client",
		operationDrift(t, generated, committed))
}

// operationDrift names the operations only one side has, so the failure says which route
// moved. It is empty when the sets agree and only a schema or a description changed.
func operationDrift(t *testing.T, generated, committed []byte) string {
	t.Helper()
	operations := func(side string, raw []byte) map[string]bool {
		var doc struct {
			Paths map[string]map[string]json.RawMessage `json:"paths"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse the %s OpenAPI document: %v", side, err)
		}
		set := map[string]bool{}
		for path, item := range doc.Paths {
			for method := range item {
				set[method+" "+path] = true
			}
		}
		return set
	}
	have, want := operations("committed", committed), operations("generated", generated)
	var lines []string
	for op := range want {
		if !have[op] {
			lines = append(lines, "missing from the snapshot: "+op)
		}
	}
	for op := range have {
		if !want[op] {
			lines = append(lines, "only in the snapshot: "+op)
		}
	}
	sort.Strings(lines)
	if len(lines) > maxDriftLines {
		lines = append(lines[:maxDriftLines], fmt.Sprintf("... and %d more", len(lines)-maxDriftLines))
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n  " + strings.Join(lines, "\n  ")
}
