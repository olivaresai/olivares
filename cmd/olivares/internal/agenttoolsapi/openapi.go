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
		declaration.RequestBody = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": jsonObject(map[string]any{"plan_digest": text(), "request_id": map[string]any{"type": "string", "format": "uuid"}}, "plan_digest", "request_id")}}}
		declaration.SuccessResponses = map[string]any{"202": jsonResponse("Installation job accepted or identical request replayed", jobSchema())}
	case "GET /jobs/{id}":
		declaration.SuccessResponses = map[string]any{"200": jsonResponse("Persisted installation job", jobSchema())}
	case "GET /detect":
		declaration.Parameters = []map[string]any{{"name": "driver", "in": "query", "required": true, "schema": text()}, {"name": "probe_path", "in": "query", "required": false, "description": "Exact detected path selected for a bounded --version execution; requires the administrative step-up and a writable control plane.", "schema": text()}}
	case "GET /inventory":
	case "GET /sign-in":
		declaration.Parameters = []map[string]any{{"name": "driver", "in": "query", "required": true, "schema": map[string]any{"type": "string", "enum": []string{"claude", "codex", "grok"}}},
			{"name": "tenant_id", "in": "query", "required": true, "description": "The organization whose own login is read: each keeps its own, in a home the product creates under the data directory.", "schema": map[string]any{"type": "string", "format": "uuid"}}}
		declaration.Parameters = append(declaration.Parameters, map[string]any{"name": "account_ref", "in": "query", "required": false, "description": "An existing provider account or profile in this organization; omitted, read the tenant's default login.", "schema": text()})
		declaration.SuccessResponses = map[string]any{"200": jsonResponse("What the installed tool says about the organization's own login on this node", map[string]any{"type": "object", "properties": map[string]any{"driver": text(), "installed": map[string]any{"type": "boolean"}, "signed_in": map[string]any{"type": "boolean"}, "method": text(), "account": text(), "account_ref": text()}, "required": []string{"driver", "installed", "signed_in"}})}
	case "POST /sign-in":
		declaration.BodyKind = api.ModuleOperationJSONBody
		declaration.RequestBody = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": jsonObject(map[string]any{"driver": map[string]any{"type": "string", "enum": []string{"claude", "codex", "grok"}}, "tenant_id": map[string]any{"type": "string", "format": "uuid", "description": "The organization the login is for; it lands in that organization's own home."}, "account_ref": map[string]any{"type": "string", "description": "An existing provider account or profile in this organization; omitted, use the tenant's default login."}}, "driver", "tenant_id")}}}
		declaration.SuccessResponses = map[string]any{"202": jsonResponse("The tool's own login started on this node", signInSchema())}
	case "GET /sign-in/{id}":
		declaration.SuccessResponses = map[string]any{"200": jsonResponse("A login in progress", signInSchema())}
	case "POST /sign-in/{id}/code":
		declaration.BodyKind = api.ModuleOperationJSONBody
		declaration.RequestBody = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": jsonObject(map[string]any{"code": map[string]any{"type": "string", "maxLength": 2048}}, "code")}}}
		declaration.SuccessResponses = map[string]any{"202": jsonResponse("The pasted code was handed to the tool", signInSchema())}
	case "DELETE /sign-in/{id}":
		declaration.BodyKind = api.ModuleOperationBodyless
		declaration.SuccessResponses = map[string]any{"200": jsonResponse("The login was stopped", jsonObject(map[string]any{"ok": map[string]any{"type": "boolean"}}, "ok"))}
	case "GET /ollama":
		declaration.SuccessResponses = map[string]any{"200": jsonResponse("Whether Ollama is installed and runs on this node, its endpoint and its models", ollamaStatusSchema())}
	case "POST /ollama/start":
		declaration.BodyKind = api.ModuleOperationJSONBody
		declaration.RequestBody = map[string]any{"required": false, "content": map[string]any{"application/json": map[string]any{"schema": jsonObject(map[string]any{"tenant_id": map[string]any{"type": "string", "format": "uuid", "description": "The business tenant whose Providers get this endpoint once Ollama answers; omitted, none does."}})}}}
		declaration.SuccessResponses = map[string]any{"202": jsonResponse("Ollama is starting, or already runs, on this node", ollamaStatusSchema())}
	case "POST /ollama/stop":
		declaration.BodyKind = api.ModuleOperationBodyless
		declaration.SuccessResponses = map[string]any{"200": jsonResponse("Ollama stopped; its models stay on disk", ollamaStatusSchema())}
	case "POST /ollama/pulls":
		declaration.BodyKind = api.ModuleOperationJSONBody
		declaration.RequestBody = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": jsonObject(map[string]any{"model": map[string]any{"type": "string", "maxLength": 128}}, "model")}}}
		declaration.SuccessResponses = map[string]any{"202": jsonResponse("The model download started", ollamaPullSchema())}
	case "GET /ollama/pulls/{id}":
		declaration.SuccessResponses = map[string]any{"200": jsonResponse("A model download with Ollama's own progress", ollamaPullSchema())}
	default:
		return declaration, false
	}
	return declaration, true
}

func ollamaStatusSchema() map[string]any {
	text := func() map[string]any { return map[string]any{"type": "string"} }
	return map[string]any{"type": "object", "properties": map[string]any{"installed": map[string]any{"type": "boolean"}, "state": map[string]any{"type": "string", "enum": []string{"stopped", "starting", "running", "failed"}}, "message": text(), "endpoint": text(), "models": map[string]any{"type": "array", "items": text()}}, "required": []string{"installed", "state", "models"}}
}

func ollamaPullSchema() map[string]any {
	text := func() map[string]any { return map[string]any{"type": "string"} }
	count := func() map[string]any { return map[string]any{"type": "integer", "format": "int64"} }
	return map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string", "format": "uuid"}, "model": text(), "state": map[string]any{"type": "string", "enum": []string{"running", "succeeded", "failed"}}, "status": text(), "completed": count(), "total": count(), "error": text()}, "required": []string{"id", "model", "state", "completed", "total"}}
}
func jobSchema() map[string]any {
	text := func() map[string]any { return map[string]any{"type": "string"} }
	return map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string", "format": "uuid"}, "plan_digest": text(), "driver": text(), "version": text(), "state": map[string]any{"type": "string", "enum": []string{"running", "succeeded", "failed", "interrupted"}}, "progress": text(), "error": text(), "audit_error": text(), "receipt": map[string]any{}, "created_at": map[string]any{"type": "string", "format": "date-time"}, "updated_at": map[string]any{"type": "string", "format": "date-time"}}, "required": []string{"id", "plan_digest", "driver", "version", "state", "progress", "created_at", "updated_at"}}
}

func signInSchema() map[string]any {
	text := func() map[string]any { return map[string]any{"type": "string"} }
	return map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string", "format": "uuid"}, "driver": text(), "account_ref": text(), "state": map[string]any{"type": "string", "enum": []string{"starting", "needs_code", "waiting", "checking", "signed_in", "failed"}}, "url": text(), "user_code": text(), "message": text()}, "required": []string{"id", "driver", "state"}}
}
