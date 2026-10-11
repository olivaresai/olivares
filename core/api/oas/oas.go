// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package oas builds the OpenAPI fragments the published documents are made of.
// The core builds its documents with it, and each module describes its own
// operations with it (api.ModuleOperationDocumenter), so both write one shape.
package oas

// Obj builds an object from alternating keys and values.
func Obj(kv ...any) map[string]any {
	m := make(map[string]any, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

// Enum lists string values as a JSON array.
func Enum(values ...string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// Param is one operation parameter.
func Param(name, in, description string, required bool, schema map[string]any) map[string]any {
	return Obj("name", name, "in", in, "required", required,
		"description", description, "schema", schema)
}

// JSONResp is a response whose JSON body is an unspecified object.
func JSONResp(description string) map[string]any {
	return JSONRespSchema(description, Obj("type", "object"))
}

// JSONRespSchema is a response whose JSON body follows schema.
func JSONRespSchema(description string, schema map[string]any) map[string]any {
	return Obj("description", description, "content",
		Obj("application/json", Obj("schema", schema)))
}
