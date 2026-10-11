// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package oastest reads the published module OpenAPI document in module tests.
package oastest

import (
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
)

// Op builds the beta document for module and returns the operation at
// method (lower case) and the full /v1/m/... path.
func Op(t *testing.T, module api.Module, method, path string) map[string]any {
	t.Helper()
	return PathOp(t, api.ModuleOpenAPIDocument([]api.Module{module})["paths"].(map[string]any), method, path)
}

// PathOp returns the operation at method (lower case) and path of paths.
func PathOp(t *testing.T, paths map[string]any, method, path string) map[string]any {
	t.Helper()
	item, ok := paths[path].(map[string]any)
	if !ok {
		t.Fatalf("path %q missing from document", path)
	}
	op, ok := item[method].(map[string]any)
	if !ok {
		t.Fatalf("%s %s missing", strings.ToUpper(method), path)
	}
	return op
}

// Strings returns the strings of a JSON array value, sorted.
func Strings(value any) []string {
	values, _ := value.([]any)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if item, ok := value.(string); ok {
			out = append(out, item)
		}
	}
	sort.Strings(out)
	return out
}

// Map asserts value is a JSON object and returns it; label names it in the
// failure.
func Map(t *testing.T, value any, label string) map[string]any {
	t.Helper()
	got, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v, want map[string]any", label, value)
	}
	return got
}

// Keys returns the keys of m, sorted.
func Keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
