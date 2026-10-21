// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package agenttoolsapi

import (
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/driverfacts"
)

func jsonObject(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
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
	case "DELETE /releases":
		declaration.BodyKind = api.ModuleOperationJSONBody
		declaration.RequestBody = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": jsonObject(map[string]any{"driver": text(), "version": text()}, "driver", "version")}}}
		declaration.SuccessResponses = map[string]any{"200": jsonResponse("Verified release removed after session admission and audit", jsonObject(map[string]any{"ok": map[string]any{"type": "boolean"}, "driver": text(), "version": text(), "sha256": text()}, "ok", "driver", "version", "sha256"))}
	case "GET /pins":
		declaration.Parameters = []map[string]any{{"name": "tenant_id", "in": "query", "required": true, "schema": map[string]any{"type": "string", "format": "uuid"}}}
		declaration.SuccessResponses = map[string]any{"200": jsonResponse("Stored workspace and agent version selections", jsonObject(map[string]any{"pins": map[string]any{"type": "array", "items": toolPinSchema()}}, "pins"))}
	case "PUT /pins":
		declaration.BodyKind = api.ModuleOperationJSONBody
		schema := toolPinSchema()
		schema["properties"].(map[string]any)["tenant_id"] = map[string]any{"type": "string", "format": "uuid"}
		schema["required"] = []string{"tenant_id", "driver", "version"}
		declaration.RequestBody = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schema}}}
		declaration.SuccessResponses = map[string]any{"200": jsonResponse("Version selection saved and audited; empty version clears it", jsonObject(map[string]any{"ok": map[string]any{"type": "boolean"}}, "ok"))}
	case "POST /plans":
		declaration.Parameters = []map[string]any{{"name": "X-Olivares-Tenant", "in": "header", "required": false, "description": "For from installs, select the organization whose Catalog admitted the declaration. Host authority and audit remain system-scoped.", "schema": map[string]any{"type": "string", "format": "uuid"}}}
		declaration.BodyKind = api.ModuleOperationJSONBody
		schema := jsonObject(map[string]any{"driver": map[string]any{"type": "string", "enum": driverfacts.InstallerKeys(), "minLength": 1}, "version": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "from": map[string]any{"type": "string", "format": "uri", "minLength": 1, "maxLength": 2048}}, "version")
		schema["oneOf"] = []map[string]any{{"required": []string{"driver"}, "not": map[string]any{"required": []string{"from"}}}, {"required": []string{"from"}, "not": map[string]any{"required": []string{"driver"}}}}
		declaration.RequestBody = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schema}}}
		declaration.SuccessResponses = map[string]any{"200": jsonResponse("Official or catalog-admitted release selected for administrator review", map[string]any{"type": "object", "properties": map[string]any{"driver": text(), "version": text(), "digest": text(), "verification": text(), "executable": text(), "document": map[string]any{"type": "object"}}, "required": []string{"driver", "version", "digest", "verification", "executable", "document"}})}
	case "POST /installs":
		declaration.Parameters = []map[string]any{{"name": "X-Olivares-Tenant", "in": "header", "required": false, "description": "For a catalog plan, select the same organization used in its preview.", "schema": map[string]any{"type": "string", "format": "uuid"}}}
		declaration.BodyKind = api.ModuleOperationJSONBody
		declaration.RequestBody = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": jsonObject(map[string]any{"plan_digest": text(), "request_id": map[string]any{"type": "string", "format": "uuid"}}, "plan_digest", "request_id")}}}
		declaration.SuccessResponses = map[string]any{"202": jsonResponse("Installation job accepted or identical request replayed", jobSchema())}
	case "GET /jobs/{id}":
		declaration.SuccessResponses = map[string]any{"200": jsonResponse("Persisted installation job", jobSchema())}
	case "GET /detect":
		declaration.Parameters = []map[string]any{{"name": "driver", "in": "query", "required": true, "schema": text()}, {"name": "probe_path", "in": "query", "required": false, "description": "Exact detected path selected for a bounded --version execution; requires the administrative step-up and a writable control plane.", "schema": text()}}
	case "GET /inventory":
	case "GET /providers":
		declaration.Parameters = []map[string]any{{"name": "tenant_id", "in": "query", "required": false, "description": "Also list the logins this organization made through Olivares; omitted, only each tool's own default login on this node.", "schema": map[string]any{"type": "string", "format": "uuid"}}}
		declaration.Parameters = append(declaration.Parameters, map[string]any{"name": "background", "in": "query", "required": false, "description": "Return completed snapshots immediately and refresh in the background. Cold instances are omitted until checked; refreshing=true means a probe is still running. Omitted or false preserves the synchronous read.", "schema": map[string]any{"type": "boolean", "default": false}})
		declaration.SuccessResponses = map[string]any{"200": jsonResponse("One snapshot per tool instance: what the tool itself reports about its login, plan, usage windows and models. A snapshot is refreshed at most once a minute; a failed refresh keeps the last one, marked stale.", jsonObject(map[string]any{"providers": map[string]any{"type": "array", "items": providerSchema()}, "refreshing": map[string]any{"type": "boolean", "description": "Present on background reads: a native probe is still running."}}, "providers"))}
	case "GET /sign-in":
		declaration.Parameters = []map[string]any{{"name": "driver", "in": "query", "required": true, "schema": map[string]any{"type": "string", "enum": driverfacts.SignInKeys()}},
			{"name": "tenant_id", "in": "query", "required": true, "description": "The organization whose own login is read: each keeps its own, in a home the product creates under the data directory.", "schema": map[string]any{"type": "string", "format": "uuid"}}}
		declaration.Parameters = append(declaration.Parameters, map[string]any{"name": "account_ref", "in": "query", "required": false, "description": "An existing provider account or profile in this organization; omitted, read the tenant's default login.", "schema": text()})
		declaration.SuccessResponses = map[string]any{"200": jsonResponse("The tool's native login status and any active sign-in for the same organization, driver and account", map[string]any{"type": "object", "properties": map[string]any{"driver": text(), "installed": map[string]any{"type": "boolean"}, "signed_in": map[string]any{"type": "boolean"}, "method": text(), "account": text(), "plan": text(), "account_ref": text(), "methods": map[string]any{"type": "array", "description": "The official login methods the tool offers, when it offers more than one.", "items": jsonObject(map[string]any{"id": text(), "label": text(), "default": map[string]any{"type": "boolean"}}, "id", "label")}, "pending": signInSchema()}, "required": []string{"driver", "installed", "signed_in"}})}
	case "POST /sign-in":
		declaration.BodyKind = api.ModuleOperationJSONBody
		declaration.RequestBody = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": jsonObject(map[string]any{"driver": map[string]any{"type": "string", "enum": driverfacts.SignInKeys()}, "tenant_id": map[string]any{"type": "string", "format": "uuid", "description": "The organization the login is for; it lands in that organization's own home."}, "account_ref": map[string]any{"type": "string", "description": "An existing provider account or profile in this organization; omitted, use the tenant's default login."}, "method": map[string]any{"type": "string", "description": "One of the login method ids the tool lists in the sign-in status; omitted, the tool's default method. An id the tool does not offer is refused with 400 and the valid ids."}}, "driver", "tenant_id")}}}
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
	case "GET /model-servers":
		declaration.SuccessResponses = map[string]any{"200": jsonResponse("Each local OpenAI-compatible model server (llama.cpp, vLLM): installed, running or not, its endpoint and the models it serves", jsonObject(map[string]any{"servers": map[string]any{"type": "array", "items": modelServerStatusSchema()}}, "servers"))}
	case "POST /model-servers/{kind}/start":
		declaration.BodyKind = api.ModuleOperationJSONBody
		declaration.RequestBody = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": jsonObject(map[string]any{
			"model":     map[string]any{"type": "string", "maxLength": 1024, "description": "llama-cpp: the absolute path of a .gguf file, or a Hugging Face model as user/repo[:quantization]. vllm: the absolute path of a model directory, or a Hugging Face model as user/repo."},
			"tenant_id": map[string]any{"type": "string", "format": "uuid", "description": "The organization whose Providers get this server's endpoint and the key the engine made for it."},
		}, "model", "tenant_id")}}}
		declaration.SuccessResponses = map[string]any{"202": jsonResponse("The server is starting, or already runs this model, on this node", modelServerStatusSchema())}
	case "POST /model-servers/{kind}/stop":
		declaration.BodyKind = api.ModuleOperationBodyless
		declaration.SuccessResponses = map[string]any{"200": jsonResponse("The server stopped; its models stay on disk", modelServerStatusSchema())}
	default:
		return declaration, false
	}
	return declaration, true
}

func providerSchema() map[string]any {
	text := func() map[string]any { return map[string]any{"type": "string"} }
	limit := jsonObject(map[string]any{"label": text(), "percent": map[string]any{"type": "number"}, "resets_at": map[string]any{"type": "string", "format": "date-time"}, "severity": text()}, "label", "percent")
	model := jsonObject(map[string]any{"id": text(), "name": text()}, "id")
	return jsonObject(map[string]any{
		"instance": text(), "driver": text(), "default": map[string]any{"type": "boolean"}, "config_dir": text(),
		"state":     map[string]any{"type": "string", "enum": []string{"ready", "not_signed_in", "not_installed", "unknown", "error"}},
		"installed": map[string]any{"type": "boolean"}, "version": text(), "auth_method": text(), "plan": text(),
		"email":  map[string]any{"type": "string", "description": "Masked: the first letter and the domain."},
		"limits": map[string]any{"type": "array", "items": limit}, "models": map[string]any{"type": "array", "items": model},
		"notes": map[string]any{"type": "array", "items": text()}, "next_command": text(),
		"checked_at": map[string]any{"type": "string", "format": "date-time"}, "source": text(),
		"stale": map[string]any{"type": "boolean"}, "error": text(),
	}, "instance", "driver", "default", "config_dir", "state", "installed", "limits", "models", "checked_at", "source")
}

func ollamaStatusSchema() map[string]any {
	text := func() map[string]any { return map[string]any{"type": "string"} }
	return map[string]any{"type": "object", "properties": map[string]any{"installed": map[string]any{"type": "boolean"}, "state": map[string]any{"type": "string", "enum": []string{"stopped", "starting", "running", "failed"}}, "message": text(), "endpoint": text(), "models": map[string]any{"type": "array", "items": text()}}, "required": []string{"installed", "state", "models"}}
}

func modelServerStatusSchema() map[string]any {
	text := func() map[string]any { return map[string]any{"type": "string"} }
	return map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"llama-cpp", "vllm"}}, "name": text(), "installed": map[string]any{"type": "boolean"}, "state": map[string]any{"type": "string", "enum": []string{"stopped", "starting", "running", "failed"}}, "message": text(), "endpoint": text(), "model": text(), "models": map[string]any{"type": "array", "items": text()}}, "required": []string{"kind", "name", "installed", "state", "models"}}
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

func toolPinSchema() map[string]any {
	return jsonObject(map[string]any{"driver": map[string]any{"type": "string"}, "version": map[string]any{"type": "string", "maxLength": 128}, "workspace_ref": map[string]any{"type": "string"}, "agent_ref": map[string]any{"type": "string", "format": "uuid"}}, "driver", "version")
}
