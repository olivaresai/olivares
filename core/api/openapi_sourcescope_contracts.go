// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

type sourceScopeRequestBodyKind uint8

const (
	sourceScopeBodyless sourceScopeRequestBodyKind = iota + 1
	sourceScopeBodyful
	sourceScopeBodyNoDerivable
	sourceScopeBodyPending
)

type sourceScopeRequestBodyDeclaration struct {
	kind   sourceScopeRequestBodyKind
	schema map[string]any
}

func sourceScopeRequestBody(r moduleRoute) (map[string]any, bool) {
	decl, ok := sourceScopeRequestBodyDeclarationFor(r)
	if !ok || decl.kind != sourceScopeBodyful {
		return nil, false
	}
	return oaObj(
		"required", true,
		"content", oaObj("application/json", oaObj("schema", decl.schema)),
	), true
}

func sourceScopeRequestBodyDeclarationFor(r moduleRoute) (sourceScopeRequestBodyDeclaration, bool) {
	if r.ns != "sourcescope" {
		return sourceScopeRequestBodyDeclaration{}, false
	}
	switch r.method + " " + r.pattern {
	case http.MethodPost + " /bindings":
		return sourceScopeBodyDeclaration(sourceScopeBindingSchema(true)), true
	case http.MethodPut + " /bindings/{id}":
		return sourceScopeBodyDeclaration(sourceScopeBindingSchema(false)), true
	case http.MethodPost + " /sources/disable-scoping":
		return sourceScopeBodyDeclaration(sourceScopeDisableScopingSchema()), true
	case http.MethodPut + " /guard-postures":
		return sourceScopeBodyDeclaration(sourceScopeGuardPostureSchema()), true
	case http.MethodPost + " /assignments":
		return sourceScopeBodyDeclaration(sourceScopeAssignmentSchema(true)), true
	case http.MethodPut + " /assignments/{id}":
		return sourceScopeBodyDeclaration(sourceScopeAssignmentSchema(false)), true
	case http.MethodPost + " /workspace-connectors":
		return sourceScopeBodyDeclaration(sourceScopeWorkspaceConnectorSchema(true)), true
	case http.MethodPut + " /workspace-connectors/{id}":
		return sourceScopeBodyDeclaration(sourceScopeWorkspaceConnectorSchema(false)), true
	case http.MethodDelete + " /bindings/{id}",
		http.MethodPost + " /posture-requests/{id}/approve",
		http.MethodPost + " /posture-requests/{id}/reject",
		http.MethodDelete + " /assignments/{id}",
		http.MethodDelete + " /workspace-connectors/{id}":
		return sourceScopeRequestBodyDeclaration{kind: sourceScopeBodyless}, true
	default:
		return sourceScopeRequestBodyDeclaration{}, false
	}
}

func sourceScopeBodyDeclaration(schema map[string]any) sourceScopeRequestBodyDeclaration {
	return sourceScopeRequestBodyDeclaration{kind: sourceScopeBodyful, schema: schema}
}

func sourceScopeNullable(schema map[string]any) map[string]any {
	return oaObj("anyOf", []any{schema, oaObj("type", "null")})
}

func sourceScopeClosedObject(properties map[string]any, required ...string) map[string]any {
	schema := oaObj("type", "object", "additionalProperties", false, "properties", properties)
	if len(required) > 0 {
		schema["required"] = oaEnum(required...)
	}
	return schema
}

func sourceScopeOptionalString(description string) map[string]any {
	return sourceScopeNullable(oaObj("type", "string", "description", description))
}

func sourceScopeBindingSchema(create bool) map[string]any {
	properties := oaObj(
		"id", sourceScopeOptionalString("Accepted by the DTO but ignored on input; identity comes from the store."),
		"source_type", oaObj(
			"type", "string",
			"description", "Case-insensitive after trimming: mcp, model, provider, knowledge, or data. On update the stored value wins.",
		),
		"source_ref", oaObj("type", "string", "description", "After trimming it must be non-empty. On update the stored value wins."),
		"scope_tree", oaObj(
			"type", "string",
			"description", "Case-insensitive after trimming: workspace, agent_group, folder, session, agent, user, user_group, or role.",
		),
		"scope_ref", sourceScopeOptionalString("Required for every scope_tree except workspace; a blank workspace ref selects the default workspace."),
		"effect", sourceScopeOptionalString("Case-insensitive after trimming. Empty defaults to allow; otherwise allow or forbid."),
		"folder_path", sourceScopeOptionalString("Output-only projection accepted by the DTO but ignored; the store resolves it from scope_ref."),
		"cred_name", sourceScopeOptionalString("Credential-reference tuple. When any tuple field is non-empty, name, ref_kind, and ref are all required."),
		"cred_ref_kind", sourceScopeOptionalString("Case-insensitive after trimming: env, vault, secret_manager, file, or other."),
		"cred_ref", sourceScopeOptionalString("A credential locator, never an inline credential value."),
		"cred_hint", sourceScopeOptionalString("Masked partial only; capped at 64 bytes."),
		"enabled", sourceScopeNullable(oaObj("type", "boolean")),
		"note", sourceScopeOptionalString("Capped at 512 bytes."),
	)
	if create {
		return sourceScopeClosedObject(properties, "source_type", "source_ref", "scope_tree")
	}
	properties["source_type"] = sourceScopeOptionalString("Accepted on update but ignored; the stored source type wins.")
	properties["source_ref"] = sourceScopeOptionalString("Accepted on update but ignored; the stored source reference wins.")
	return sourceScopeClosedObject(properties, "scope_tree")
}

func sourceScopeDisableScopingSchema() map[string]any {
	return sourceScopeClosedObject(oaObj(
		"source_type", oaObj(
			"type", "string",
			"description", "Case-insensitive after trimming: mcp, model, provider, knowledge, or data.",
		),
		"source_ref", oaObj("type", "string", "description", "After trimming it must be non-empty."),
	), "source_type", "source_ref")
}

func sourceScopeGuardPostureSchema() map[string]any {
	return sourceScopeClosedObject(oaObj(
		"source_type", sourceScopeOptionalString("Defaults to knowledge when empty; any non-empty value must normalize to knowledge."),
		"source_ref", oaObj("type", "string", "description", "Knowledge-base name; after trimming it must be non-empty."),
		"profile", oaObj(
			"type", "string",
			"description", "Case-insensitive after trimming: acl_aware or public_only; disable_acl is accepted as an alias for public_only.",
		),
		"reason", sourceScopeOptionalString("Trimmed and capped at 512 bytes."),
	), "source_ref", "profile")
}

func sourceScopeAssignmentSchema(create bool) map[string]any {
	properties := oaObj(
		"id", sourceScopeOptionalString("Accepted by the DTO but ignored on input; identity comes from the store."),
		"connector_name", sourceScopeOptionalString("After trimming it must be non-empty on create. On update the stored value wins."),
		"workspace_ref", sourceScopeOptionalString("After trimming it must resolve to a workspace on create. On update the stored value wins."),
		"mode", sourceScopeOptionalString("After trimming and lowercasing, r remains r; every other value normalizes to rw. Blank update input preserves the stored mode."),
		"enabled", sourceScopeNullable(oaObj("type", "boolean")),
		"note", sourceScopeOptionalString("Capped at 512 bytes."),
	)
	if create {
		properties["connector_name"] = oaObj("type", "string", "description", "After trimming it must be non-empty.")
		properties["workspace_ref"] = oaObj("type", "string", "description", "After trimming it must resolve to a workspace.")
		return sourceScopeClosedObject(properties, "connector_name", "workspace_ref")
	}
	return sourceScopeNullable(sourceScopeClosedObject(properties))
}

func sourceScopeStringMapSchema(description string) map[string]any {
	return sourceScopeNullable(oaObj(
		"type", "object", "description", description,
		"additionalProperties", sourceScopeNullable(oaObj("type", "string")),
	))
}

func sourceScopeWorkspaceConnectorSchema(create bool) map[string]any {
	properties := oaObj(
		"id", sourceScopeOptionalString("Accepted by the DTO but ignored on input; identity comes from the store."),
		"name", sourceScopeOptionalString("After trimming it must be non-empty and no longer than 128 bytes. On update the stored value wins."),
		"kind", sourceScopeOptionalString("After trimming it must be non-empty. On update the stored value wins."),
		"workspace_ref", sourceScopeOptionalString("After trimming it must resolve to a workspace. On update the stored value wins."),
		"config", sourceScopeStringMapSchema("Non-secret settings. Credential-bearing keys and inline credential values are rejected."),
		"secrets", sourceScopeStringMapSchema("Secret references or inline literals for sealing; URLs with embedded credentials are rejected."),
		"poll_seconds", sourceScopeNullable(oaObj("type", "integer", "minimum", 0)),
		"enabled", sourceScopeNullable(oaObj("type", "boolean")),
		"note", sourceScopeOptionalString("Trimmed and capped at 512 bytes."),
		"status", sourceScopeOptionalString("Create always overwrites this with pending; update persists the supplied value."),
	)
	if create {
		properties["name"] = oaObj("type", "string", "description", "After trimming it must be non-empty and no longer than 128 bytes.")
		properties["kind"] = oaObj("type", "string", "description", "After trimming it must be non-empty.")
		properties["workspace_ref"] = oaObj("type", "string", "description", "After trimming it must resolve to a workspace.")
		return sourceScopeClosedObject(properties, "name", "kind", "workspace_ref")
	}
	return sourceScopeNullable(sourceScopeClosedObject(properties))
}
