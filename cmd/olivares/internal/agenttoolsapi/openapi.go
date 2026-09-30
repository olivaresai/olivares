// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package agenttoolsapi

import "github.com/olivaresai/olivares/core/api"

func jsonObject(properties map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}
func jsonResponse(description string, schema map[string]any) map[string]any {
	return map[string]any{"description": description, "content": map[string]any{"application/json": map[string]any{"schema": schema}}}
}

// OperationDocumentation owns the command envelopes beside the handlers. Core
// receives their schemas through the optional module port, not feature names.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	text := func() map[string]any { return map[string]any{"type": "string"} }
	declaration := api.ModuleOperationDocumentation{}
	switch method + " " + pattern {
	case "POST /plans":
		declaration.BodyKind = api.ModuleOperationJSONBody
		declaration.RequestBody = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": jsonObject(map[string]any{"driver": map[string]any{"type": "string", "enum": []string{"claude", "codex", "grok", "opencode", "ollama"}}, "version": map[string]any{"type": "string", "maxLength": 128}}, "driver", "version")}}}
		declaration.SuccessResponses = map[string]any{"200": jsonResponse("Official release selected for administrator review", map[string]any{"type": "object", "properties": map[string]any{"driver": text(), "version": text(), "digest": text(), "verification": text(), "executable": text(), "document": map[string]any{"type": "object"}}, "required": []string{"driver", "version", "digest", "verification", "executable", "document"}})}
	case "POST /installs":
		declaration.BodyKind = api.ModuleOperationJSONBody
		declaration.RequiredAssurance = 3
		declaration.RequestBody = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": jsonObject(map[string]any{"plan_digest": text(), "request_id": map[string]any{"type": "string", "format": "uuid"}}, "plan_digest", "request_id")}}}
		declaration.SuccessResponses = map[string]any{"202": jsonResponse("Installation job accepted or identical request replayed", jobSchema())}
	case "GET /jobs/{id}":
		declaration.SuccessResponses = map[string]any{"200": jsonResponse("Persisted installation job", jobSchema())}
	case "GET /detect":
		declaration.Parameters = []map[string]any{{"name": "driver", "in": "query", "required": true, "schema": text()}, {"name": "probe_path", "in": "query", "required": false, "description": "Exact detected path selected for a bounded --version execution; requires AAL3 and a writable control plane.", "schema": text()}}
	case "GET /inventory":
	default:
		return declaration, false
	}
	return declaration, true
}
func jobSchema() map[string]any {
	text := func() map[string]any { return map[string]any{"type": "string"} }
	return map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string", "format": "uuid"}, "plan_digest": text(), "driver": text(), "version": text(), "state": map[string]any{"type": "string", "enum": []string{"running", "succeeded", "failed", "interrupted"}}, "progress": text(), "error": text(), "audit_error": text(), "receipt": map[string]any{}, "created_at": map[string]any{"type": "string", "format": "date-time"}, "updated_at": map[string]any{"type": "string", "format": "date-time"}}, "required": []string{"id", "plan_digest", "driver", "version", "state", "progress", "created_at", "updated_at"}}
}
