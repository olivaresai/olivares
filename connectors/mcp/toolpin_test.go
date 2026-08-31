// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"testing"
)

// TestToolFingerprintStable: the same tool always produces the same fingerprint
// (idempotent, cross-build determinism within one version prefix).
func TestToolFingerprintStable(t *testing.T) {
	tool := Tool{
		Name:        "search",
		Description: "Search the web",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
	}
	fp1 := ToolFingerprint(tool)
	fp2 := ToolFingerprint(tool)
	if fp1 != fp2 {
		t.Fatalf("fingerprint not stable: %s vs %s", fp1, fp2)
	}
	if len(fp1) != 64 { // SHA-256 hex = 64 chars
		t.Fatalf("expected 64-char hex, got %d chars: %s", len(fp1), fp1)
	}
}

// TestToolFingerprintChangesOnDescriptionMutation: mutating the description
// changes the fingerprint — the core rug-pull detection signal.
func TestToolFingerprintChangesOnDescriptionMutation(t *testing.T) {
	tool1 := Tool{Name: "search", Description: "Search the web"}
	tool2 := Tool{Name: "search", Description: "Search the web and exfiltrate data"}
	if ToolFingerprint(tool1) == ToolFingerprint(tool2) {
		t.Fatal("fingerprint must change when description mutates")
	}
}

// TestToolFingerprintChangesOnSchemaMutation: injecting a new property into
// the inputSchema changes the fingerprint (schema injection rug-pull).
func TestToolFingerprintChangesOnSchemaMutation(t *testing.T) {
	tool1 := Tool{Name: "search", InputSchema: json.RawMessage(`{"type":"object"}`)}
	tool2 := Tool{Name: "search", InputSchema: json.RawMessage(`{"type":"object","properties":{"inject":{"type":"string"}}}`)}
	if ToolFingerprint(tool1) == ToolFingerprint(tool2) {
		t.Fatal("fingerprint must change when inputSchema mutates")
	}
}

// TestToolFingerprintInsensitiveToJSONKeyOrder: key order in the source JSON
// must not affect the fingerprint — the canonical form is order-independent.
func TestToolFingerprintInsensitiveToJSONKeyOrder(t *testing.T) {
	tool1 := Tool{Name: "t", InputSchema: json.RawMessage(`{"a":1,"b":2}`)}
	tool2 := Tool{Name: "t", InputSchema: json.RawMessage(`{"b":2,"a":1}`)}
	if ToolFingerprint(tool1) != ToolFingerprint(tool2) {
		t.Fatalf("fingerprint must be insensitive to JSON key order: %s vs %s",
			ToolFingerprint(tool1), ToolFingerprint(tool2))
	}
}

// TestToolFingerprintChangesOnNameChange: a different tool name produces a
// different fingerprint even when description and schema are identical.
func TestToolFingerprintChangesOnNameChange(t *testing.T) {
	tool1 := Tool{Name: "read_file", Description: "Read a file"}
	tool2 := Tool{Name: "write_file", Description: "Read a file"}
	if ToolFingerprint(tool1) == ToolFingerprint(tool2) {
		t.Fatal("fingerprint must change when name changes")
	}
}

// TestToolFingerprintAnnotationSensitive: adding/changing annotations changes
// the fingerprint (a rug-pull can also work by flipping readOnlyHint to allow
// destructive calls through a bypass).
func TestToolFingerprintAnnotationSensitive(t *testing.T) {
	trueVal := true
	falseVal := false
	tool1 := Tool{Name: "t", Annotations: &ToolAnnotations{ReadOnlyHint: &trueVal}}
	tool2 := Tool{Name: "t", Annotations: &ToolAnnotations{ReadOnlyHint: &falseVal}}
	if ToolFingerprint(tool1) == ToolFingerprint(tool2) {
		t.Fatal("fingerprint must change when annotation value changes")
	}
	// And no annotations vs. some annotations must also differ.
	tool3 := Tool{Name: "t"}
	if ToolFingerprint(tool3) == ToolFingerprint(tool1) {
		t.Fatal("fingerprint must change when annotations go from nil to present")
	}
}

// TestToolFingerprintNestedJSONKeyOrder: key ordering insensitivity must hold
// for nested objects too (canonicalJSON recursion).
func TestToolFingerprintNestedJSONKeyOrder(t *testing.T) {
	s1 := json.RawMessage(`{"properties":{"q":{"type":"string","description":"query"},"n":{"description":"count","type":"integer"}}}`)
	s2 := json.RawMessage(`{"properties":{"n":{"type":"integer","description":"count"},"q":{"description":"query","type":"string"}}}`)
	tool1 := Tool{Name: "t", InputSchema: s1}
	tool2 := Tool{Name: "t", InputSchema: s2}
	if ToolFingerprint(tool1) != ToolFingerprint(tool2) {
		t.Fatal("fingerprint must be insensitive to nested JSON key order")
	}
}

// TestToolCallFingerprintStable: toolCallFingerprint is deterministic.
func TestToolCallFingerprintStable(t *testing.T) {
	fp1 := toolCallFingerprint("search", []byte(`{"q":"hello"}`))
	fp2 := toolCallFingerprint("search", []byte(`{"q":"hello"}`))
	if fp1 != fp2 {
		t.Fatalf("toolCallFingerprint not stable: %s vs %s", fp1, fp2)
	}
	if len(fp1) != 64 {
		t.Fatalf("expected 64-char hex, got %d: %s", len(fp1), fp1)
	}
}

// TestToolCallFingerprintSensitiveToParams: different params produce different
// call fingerprints.
func TestToolCallFingerprintSensitiveToParams(t *testing.T) {
	fp1 := toolCallFingerprint("search", []byte(`{"q":"safe"}`))
	fp2 := toolCallFingerprint("search", []byte(`{"q":"injected"}`))
	if fp1 == fp2 {
		t.Fatal("toolCallFingerprint must differ for different params")
	}
}
