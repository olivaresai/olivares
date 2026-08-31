// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

func mustInternal(t *testing.T, ns []string, entries []internalEntry) internalRegistry {
	t.Helper()
	ir, err := newInternalRegistry(ns, entries)
	if err != nil {
		t.Fatalf("newInternalRegistry: %v", err)
	}
	return ir
}

func TestInternalRegistryParsing(t *testing.T) {
	if got := parseNamespaceList(`["com.acme","io.github.acme"]`); len(got) != 2 || got[0] != "com.acme" {
		t.Errorf("JSON namespace parse = %v", got)
	}
	if got := parseNamespaceList("com.acme, io.github.acme ,"); len(got) != 2 || got[1] != "io.github.acme" {
		t.Errorf("comma namespace parse = %v", got)
	}
	entries, err := parseInternalEntries(`[{"name":"widgets","registry_name":"com.acme/widgets","version":"1.2.0"}]`)
	if err != nil || len(entries) != 1 || entries[0].Version != "1.2.0" {
		t.Errorf("entry parse = %+v err=%v", entries, err)
	}
}

func TestInternalRegistryDuplicateRejected(t *testing.T) {
	_, err := newInternalRegistry(nil, []internalEntry{
		{Name: "a", RegistryName: "com.acme/a"},
		{Name: "a", RegistryName: "com.acme/b"},
	})
	if err == nil {
		t.Error("a duplicate entry name must be rejected (ambiguous approval)")
	}
}

func TestInternalRegistryOwnsAndApproved(t *testing.T) {
	ir := mustInternal(t, []string{"COM.Acme"}, []internalEntry{
		{Name: "widgets", RegistryName: "io.github.acme/widgets", Version: "2.0.0"},
	})
	// Owned namespace match is case-insensitive on the namespace.
	if ns, ok := ir.owns("com.acme/secret"); !ok || ns != "com.acme" {
		t.Errorf("owns(com.acme/secret) = %q,%v", ns, ok)
	}
	if _, ok := ir.owns("io.github.other/x"); ok {
		t.Error("a foreign namespace must not be owned")
	}
	if _, ok := ir.owns(""); ok {
		t.Error("an empty registry name is never owned")
	}
	// Approved by registry name and by local name.
	if _, ok := ir.approved(serverSpec{Name: "x", RegistryName: "io.github.acme/widgets"}); !ok {
		t.Error("approved by registry_name failed")
	}
	if _, ok := ir.approved(serverSpec{Name: "widgets"}); !ok {
		t.Error("approved by local name failed")
	}
	if _, ok := ir.approved(serverSpec{Name: "unknown"}); ok {
		t.Error("an unknown server must not be approved")
	}
}

func TestInternalReconcileOwnedClearsShadow(t *testing.T) {
	s := &Source{internal: mustInternal(t, []string{"com.acme"}, nil)}
	fs, handled := s.internalReconcile(
		serverSpec{Name: "internal-srv", RegistryName: "com.acme/internal-srv"}, catalog{}, fixedTime())
	if !handled {
		t.Fatal("an owned-namespace server must be handled (cleared from shadow logic)")
	}
	if len(fs) != 1 || fs[0].Kind != findingProvenance || fs[0].Severity != model.SeverityInfo {
		t.Fatalf("want one Info provenance finding, got %+v", fs)
	}
	if !strings.Contains(fs[0].Title, "owned namespace com.acme") {
		t.Errorf("owned finding should name the namespace: %q", fs[0].Title)
	}
}

func TestInternalReconcileVersionDrift(t *testing.T) {
	s := &Source{internal: mustInternal(t, nil, []internalEntry{
		{Name: "widgets", Version: "1.2.0"},
	})}
	cat := catalog{server: InitializeResult{ServerInfo: serverInfo{Name: "widgets", Version: "1.9.9"}}}
	fs, handled := s.internalReconcile(serverSpec{Name: "widgets"}, cat, fixedTime())
	if !handled {
		t.Fatal("an approved server must be handled")
	}
	drift, ok := findFindingTagged(fs, "MCP04")
	if !ok {
		t.Fatalf("running!=pinned must raise an [MCP04] drift finding, got %+v", fs)
	}
	if drift.Severity != model.SeverityMedium {
		t.Errorf("drift severity = %q, want medium", drift.Severity)
	}
	if strings.Contains(drift.DetailHash, "1.2.0") || strings.Contains(drift.DetailHash, "1.9.9") {
		t.Error("drift detail must be hashed, not contain raw versions")
	}
	// Matching versions => no drift finding (only the approved provenance).
	catSame := catalog{server: InitializeResult{ServerInfo: serverInfo{Name: "widgets", Version: "1.2.0"}}}
	fs2, _ := s.internalReconcile(serverSpec{Name: "widgets"}, catSame, fixedTime())
	if _, ok := findFindingTagged(fs2, "MCP04"); ok {
		t.Errorf("matching versions must NOT raise drift: %+v", fs2)
	}
}

func TestInternalReconcileEmptyFallsThrough(t *testing.T) {
	s := &Source{internal: mustInternal(t, nil, nil)}
	fs, handled := s.internalReconcile(serverSpec{Name: "x", RegistryName: "io.github.acme/x"}, catalog{}, fixedTime())
	if handled || fs != nil {
		t.Errorf("an empty internal registry must fall through (nil,false), got %+v,%v", fs, handled)
	}
}

// findFindingTagged returns the first finding whose title carries the [tag].
func findFindingTagged(fs []model.FindingReport, tag string) (model.FindingReport, bool) {
	for _, f := range fs {
		if strings.Contains(f.Title, "["+tag+"]") {
			return f, true
		}
	}
	return model.FindingReport{}, false
}
