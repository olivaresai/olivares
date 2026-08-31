// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

type notifyRequestBodyKind uint8

const (
	notifyBodyless notifyRequestBodyKind = iota + 1
	notifyBodyful
	notifyBodyNoDerivable
	notifyBodyPending
)

type notifyRequestBodyDeclaration struct {
	kind   notifyRequestBodyKind
	schema map[string]any
}

func notifyRequestBody(r moduleRoute) (map[string]any, bool) {
	decl, ok := notifyRequestBodyDeclarationFor(r)
	if !ok || decl.kind != notifyBodyful {
		return nil, false
	}
	return oaObj(
		"required", true,
		"content", oaObj("application/json", oaObj("schema", decl.schema)),
	), true
}

func notifyRequestBodyDeclarationFor(r moduleRoute) (notifyRequestBodyDeclaration, bool) {
	if r.ns != "notify" {
		return notifyRequestBodyDeclaration{}, false
	}
	switch r.method + " " + r.pattern {
	case http.MethodPost + " /routes":
		return notifyBodyDeclaration(notifyRouteSchema(true)), true
	case http.MethodPut + " /routes/{id}":
		return notifyBodyDeclaration(notifyRouteSchema(false)), true
	case http.MethodPost + " /routes/evaluate":
		return notifyBodyDeclaration(notifyEvaluateSchema()), true
	case http.MethodPost + " /routes/{id}/restore":
		return notifyBodyDeclaration(notifyRestoreSchema()), true
	case http.MethodDelete + " /routes/{id}",
		http.MethodPost + " /routes/{id}/test",
		http.MethodPost + " /outbox/{id}/redeliver":
		return notifyRequestBodyDeclaration{kind: notifyBodyless}, true
	default:
		return notifyRequestBodyDeclaration{}, false
	}
}

func notifyBodyDeclaration(schema map[string]any) notifyRequestBodyDeclaration {
	return notifyRequestBodyDeclaration{kind: notifyBodyful, schema: schema}
}

func notifyNullable(schema map[string]any) map[string]any {
	return oaObj("anyOf", []any{schema, oaObj("type", "null")})
}

func notifyClosedObject(properties map[string]any, required ...string) map[string]any {
	schema := oaObj("type", "object", "additionalProperties", false, "properties", properties)
	if len(required) > 0 {
		schema["required"] = oaEnum(required...)
	}
	return schema
}

func notifySeveritySchema() map[string]any {
	return notifyNullable(oaObj("type", "string", "enum", oaEnum("", "info", "low", "medium", "high", "critical")))
}

func notifyStringSet(description string) map[string]any {
	return notifyNullable(oaObj(
		"type", "array",
		"items", notifyNullable(oaObj("type", "string")),
		"description", description,
	))
}

func notifyRouteSchema(create bool) map[string]any {
	required := []string{"destination"}
	if create {
		required = []string{"name", "destination"}
	}
	return notifyClosedObject(oaObj(
		"name", notifyNullable(oaObj("type", "string", "description", "Required and non-empty on create; accepted but ignored on update. Stored with the server byte cap.")),
		"enabled", notifyNullable(oaObj("type", "boolean", "description", "Defaults true on create; omitted or null keeps the stored value on update.")),
		"match_types", notifyStringSet("Each non-blank trimmed member must be present in GET /match-types; blank members are discarded."),
		"match_kinds", notifyStringSet("Empty means any kind."),
		"min_severity", notifySeveritySchema(),
		"match_sources", notifyStringSet("Empty means any source."),
		"match_subject_kinds", notifyStringSet("Empty means any subject kind."),
		"destination", oaObj("type", "string", "minLength", 1, "description", "Must name a destination provisioned for this tenant when a dispatcher is wired; stored with the server byte cap."),
		"dedup_window_seconds", notifyNullable(oaObj("type", "integer", "description", "Negative values are stored as zero.")),
		"throttle_window_seconds", notifyNullable(oaObj("type", "integer", "description", "Negative values are stored as zero.")),
		"priority", notifyNullable(oaObj("type", "integer")),
	), required...)
}

func notifyEvaluateSchema() map[string]any {
	return notifyClosedObject(oaObj(
		"event_type", oaObj("type", "string", "enum", oaEnum("finding.reported", "approval.requested", "approval.resolved")),
		"kind", notifyNullable(oaObj("type", "string")),
		"severity", notifySeveritySchema(),
		"source", notifyNullable(oaObj("type", "string")),
		"subject_kind", notifyNullable(oaObj("type", "string")),
	), "event_type")
}

func notifyRestoreSchema() map[string]any {
	return notifyClosedObject(oaObj(
		"revision_id", oaObj("type", "string", "minLength", 1),
	), "revision_id")
}
