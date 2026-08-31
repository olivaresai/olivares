// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

type sessionsClosureRequestBodyKind uint8

const (
	sessionsClosureBodyless sessionsClosureRequestBodyKind = iota + 1
	sessionsClosureBodyful
	sessionsClosureBodyNoDerivable
	sessionsClosureBodyPending
)

type sessionsClosureRequestBodyDeclaration struct {
	kind     sessionsClosureRequestBodyKind
	required bool
	schema   map[string]any
}

func sessionsClosureRequestBody(r moduleRoute) (map[string]any, bool) {
	decl, ok := sessionsClosureRequestBodyDeclarationFor(r)
	if !ok || decl.kind != sessionsClosureBodyful {
		return nil, false
	}
	return oaObj(
		"required", decl.required,
		"content", oaObj("application/json", oaObj("schema", decl.schema)),
	), true
}

func sessionsClosureRequestBodyDeclarationFor(r moduleRoute) (sessionsClosureRequestBodyDeclaration, bool) {
	if r.ns != "sessions" {
		return sessionsClosureRequestBodyDeclaration{}, false
	}
	switch r.method + " " + r.pattern {
	case http.MethodPost + " /runs":
		return sessionsClosureBodyDeclaration(true, sessionsCreateRunSchema()), true
	case http.MethodPost + " /templates":
		return sessionsClosureBodyDeclaration(true, sessionsCreateTemplateSchema()), true
	case http.MethodPut + " /templates/{id}":
		return sessionsClosureBodyDeclaration(true, sessionsUpdateTemplateSchema()), true
	case http.MethodPost + " /templates/{id}/duplicate":
		return sessionsClosureBodyDeclaration(true, sessionsDuplicateTemplateSchema()), true
	case http.MethodPost + " /templates/{id}/apply":
		return sessionsClosureBodyDeclaration(false, sessionsApplyTemplateSchema()), true
	case http.MethodPost + " /workspaces":
		return sessionsClosureBodyDeclaration(true, sessionsCreateWorkspaceSchema()), true
	case http.MethodPost + " /workspaces/{ref}/files/move":
		return sessionsClosureBodyDeclaration(true, sessionsMoveFileSchema()), true
	case http.MethodDelete + " /runs/{ref}",
		http.MethodPost + " /runs/{ref}/cleanup",
		http.MethodPost + " /runs/{ref}/resume",
		http.MethodDelete + " /templates/{id}",
		http.MethodDelete + " /workspaces/{ref}",
		http.MethodDelete + " /workspaces/{ref}/files",
		http.MethodPost + " /workspaces/{ref}/files/dir":
		return sessionsClosureRequestBodyDeclaration{kind: sessionsClosureBodyless}, true
	default:
		return sessionsClosureRequestBodyDeclaration{}, false
	}
}

func sessionsClosureBodyDeclaration(required bool, schema map[string]any) sessionsClosureRequestBodyDeclaration {
	return sessionsClosureRequestBodyDeclaration{kind: sessionsClosureBodyful, required: required, schema: schema}
}

func sessionsClosureNullable(schema map[string]any) map[string]any {
	return oaObj("anyOf", []any{schema, oaObj("type", "null")})
}

func sessionsClosureClosedObject(properties map[string]any, required ...string) map[string]any {
	schema := oaObj("type", "object", "additionalProperties", false, "properties", properties)
	if len(required) > 0 {
		schema["required"] = oaEnum(required...)
	}
	return schema
}

func sessionsClosureOptionalString(description string) map[string]any {
	return sessionsClosureNullable(oaObj("type", "string", "description", description))
}

func sessionsCreateRunSchema() map[string]any {
	envName := oaObj(
		"type", "string",
		"description", "After trimming, must be a 1..128-byte shell name ([A-Za-z_][A-Za-z0-9_]*). Names beginning OLIVARES_, ANTHROPIC_, or CLAUDE_CODE_, plus DISABLE_AUTOUPDATER, are reserved.",
	)
	return sessionsClosureNullable(sessionsClosureClosedObject(oaObj(
		"name", sessionsClosureOptionalString("Trimmed before persistence."),
		"transport", sessionsClosureNullable(oaObj("type", "string", "enum", oaEnum("", "stream-json", "remote-control"), "description", "Empty/null defaults to stream-json.")),
		"permission_mode", sessionsClosureNullable(oaObj("type", "string", "enum", oaEnum("", "default", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions"), "description", "Empty/null defaults to default.")),
		"effort", sessionsClosureNullable(oaObj("type", "string", "enum", oaEnum("", "low", "medium", "high", "xhigh", "max"))),
		"model", sessionsClosureOptionalString("Trimmed before launch."),
		"workspace_ref", sessionsClosureOptionalString("Trimmed and, when non-empty, resolved to a registered workspace."),
		"template_id", sessionsClosureOptionalString("When non-empty, identifies the stored template whose terms govern the launch."),
		"isolation", sessionsClosureNullable(oaObj("type", "string", "enum", oaEnum("", "native", "container", "sandbox"), "description", "Empty/null defaults to native; unavailable runners refuse rather than downgrade.")),
		"env_allow", sessionsClosureNullable(oaObj("type", "array", "maxItems", 64, "uniqueItems", true, "items", envName)),
	)))
}

func sessionsTemplateBodySchema() map[string]any {
	hookEntry := sessionsClosureNullable(sessionsClosureClosedObject(oaObj(
		"command", sessionsClosureOptionalString("Hook command; the template authoring handler stores it without semantic validation."),
		"timeout_ms", sessionsClosureNullable(oaObj("type", "integer")),
	)))
	hookEntries := sessionsClosureNullable(oaObj("type", "array", "items", hookEntry))
	hooks := sessionsClosureNullable(sessionsClosureClosedObject(oaObj(
		"pre_tool", hookEntries,
		"post_tool", hookEntries,
		"pre_session", hookEntries,
		"post_session", hookEntries,
	)))
	settings := sessionsClosureNullable(sessionsClosureClosedObject(oaObj(
		"permission_mode", sessionsClosureOptionalString("Validated when the template is reduced for preview or launch."),
		"effort", sessionsClosureOptionalString("Validated when the template is reduced for preview or launch."),
		"model", sessionsClosureOptionalString("Stored template model selector."),
		"custom_instructions", sessionsClosureOptionalString("Stored custom instructions."),
	)))
	stringList := sessionsClosureNullable(oaObj(
		"type", "array", "items", sessionsClosureNullable(oaObj("type", "string")),
	))
	policies := sessionsClosureNullable(sessionsClosureClosedObject(oaObj(
		"dlp_mode", sessionsClosureOptionalString("Validated when the template is reduced for preview or launch."),
		"max_session_duration_minutes", sessionsClosureNullable(oaObj("type", "integer")),
		"allowed_tools", stringList,
		"record_io", sessionsClosureNullable(oaObj("type", "boolean")),
	)))
	return sessionsClosureNullable(sessionsClosureClosedObject(oaObj(
		"hooks", hooks,
		"settings", settings,
		"connectors", stringList,
		"policies", policies,
	)))
}

func sessionsCreateTemplateSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"name", oaObj("type", "string", "minLength", 1),
		"description", sessionsClosureOptionalString("Stored template description."),
		"body", sessionsTemplateBodySchema(),
	), "name")
}

func sessionsUpdateTemplateSchema() map[string]any {
	return sessionsClosureNullable(sessionsClosureClosedObject(oaObj(
		"name", sessionsClosureOptionalString("An explicit string, including empty, replaces the stored name; null/omission preserves it."),
		"description", sessionsClosureOptionalString("An explicit string replaces the stored description; null/omission preserves it."),
		"body", sessionsTemplateBodySchema(),
	)))
}

func sessionsDuplicateTemplateSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"name", oaObj("type", "string", "minLength", 1, "description", "Name for the duplicate; whitespace is accepted because the handler checks exact emptiness."),
	), "name")
}

func sessionsApplyTemplateSchema() map[string]any {
	stringList := sessionsClosureNullable(oaObj(
		"type", "array", "items", sessionsClosureNullable(oaObj("type", "string")),
	))
	target := sessionsClosureNullable(sessionsClosureClosedObject(oaObj(
		"transport", sessionsClosureOptionalString("Preview transport; unenforceable template terms are reported rather than silently dropped."),
		"permission_mode", sessionsClosureOptionalString("Proposed launch permission mode."),
		"effort", sessionsClosureOptionalString("Proposed launch effort."),
		"model", sessionsClosureOptionalString("Proposed launch model."),
		"allowed_tools", stringList,
		"custom_instructions", sessionsClosureOptionalString("Proposed custom instructions."),
		"record_io", sessionsClosureNullable(oaObj("type", "boolean")),
		"max_session_duration_minutes", sessionsClosureNullable(oaObj("type", "integer", "format", "int64")),
		"workspace_ref", sessionsClosureOptionalString("Proposed workspace reference."),
	)))
	return sessionsClosureNullable(sessionsClosureClosedObject(oaObj("target", target)))
}

func sessionsCreateWorkspaceSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"name", sessionsClosureOptionalString("Trimmed workspace display name."),
		"root_path", oaObj("type", "string", "description", "After trimming, must be an absolute, existing directory; the server stores its canonical real path."),
		"mount_mode", sessionsClosureNullable(oaObj("type", "string", "enum", oaEnum("", "rw", "ro"), "description", "Empty/null defaults to rw.")),
		"container_target", sessionsClosureOptionalString("Must be absolute after trimming; empty/null selects the default container target."),
		"allow_subpaths", sessionsClosureNullable(oaObj(
			"type", "array", "description", "Relative, NUL-free paths that cannot escape the workspace root.",
			"items", sessionsClosureNullable(oaObj("type", "string")),
		)),
		"max_read_bytes", sessionsClosureNullable(oaObj("type", "integer", "format", "int64", "minimum", 0)),
		"dlp_mode", sessionsClosureNullable(oaObj("type", "string", "enum", oaEnum("", "label", "deny", "off"), "description", "Empty/null defaults to label.")),
	), "root_path")
}

func sessionsMoveFileSchema() map[string]any {
	path := oaObj(
		"type", "string", "pattern", ".*\\S.*",
		"description", "Relative, NUL-free path jailed inside the workspace; it cannot resolve to the workspace root.",
	)
	return sessionsClosureClosedObject(oaObj("from", path, "to", path), "from", "to")
}
