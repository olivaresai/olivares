// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package notify

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

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

func notifyRequestBody(method, pattern string) (map[string]any, bool) {
	decl, ok := notifyRequestBodyDeclarationFor(method, pattern)
	if !ok || decl.kind != notifyBodyful {
		return nil, false
	}
	return oas.Obj(
		"required", true,
		"content", oas.Obj("application/json", oas.Obj("schema", decl.schema)),
	), true
}

func notifyRequestBodyDeclarationFor(method, pattern string) (notifyRequestBodyDeclaration, bool) {
	switch method + " " + pattern {
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
	return oas.Obj("anyOf", []any{schema, oas.Obj("type", "null")})
}

func notifyClosedObject(properties map[string]any, required ...string) map[string]any {
	schema := oas.Obj("type", "object", "additionalProperties", false, "properties", properties)
	if len(required) > 0 {
		schema["required"] = oas.Enum(required...)
	}
	return schema
}

func notifySeveritySchema() map[string]any {
	return notifyNullable(oas.Obj("type", "string", "enum", oas.Enum("", "info", "low", "medium", "high", "critical")))
}

func notifyStringSet(description string) map[string]any {
	return notifyNullable(oas.Obj(
		"type", "array",
		"items", notifyNullable(oas.Obj("type", "string")),
		"description", description,
	))
}

func notifyRouteSchema(create bool) map[string]any {
	required := []string{"destination"}
	if create {
		required = []string{"name", "destination"}
	}
	return notifyClosedObject(oas.Obj(
		"name", notifyNullable(oas.Obj("type", "string", "description", "Required and non-empty on create; accepted but ignored on update. Stored with the server byte cap.")),
		"enabled", notifyNullable(oas.Obj("type", "boolean", "description", "Defaults true on create; omitted or null keeps the stored value on update.")),
		"match_types", notifyStringSet("Each non-blank trimmed member must be present in GET /match-types; blank members are discarded."),
		"match_kinds", notifyStringSet("Empty means any kind."),
		"min_severity", notifySeveritySchema(),
		"match_sources", notifyStringSet("Empty means any source."),
		"match_subject_kinds", notifyStringSet("Empty means any subject kind."),
		"destination", oas.Obj("type", "string", "minLength", 1, "description", "Must name a destination provisioned for this tenant when a dispatcher is wired; stored with the server byte cap."),
		"dedup_window_seconds", notifyNullable(oas.Obj("type", "integer", "description", "Negative values are stored as zero.")),
		"throttle_window_seconds", notifyNullable(oas.Obj("type", "integer", "description", "Negative values are stored as zero.")),
		"priority", notifyNullable(oas.Obj("type", "integer")),
	), required...)
}

func notifyEvaluateSchema() map[string]any {
	return notifyClosedObject(oas.Obj(
		"event_type", oas.Obj("type", "string", "enum", oas.Enum("finding.reported", "approval.requested", "approval.resolved")),
		"kind", notifyNullable(oas.Obj("type", "string")),
		"severity", notifySeveritySchema(),
		"source", notifyNullable(oas.Obj("type", "string")),
		"subject_kind", notifyNullable(oas.Obj("type", "string")),
	), "event_type")
}

func notifyRestoreSchema() map[string]any {
	return notifyClosedObject(oas.Obj(
		"revision_id", oas.Obj("type", "string", "minLength", 1),
	), "revision_id")
}

// OperationDocumentation publishes the request body each mutation's handler decodes.
// A route this module has not classified stays unclassified.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	decl, ok := notifyRequestBodyDeclarationFor(method, pattern)
	if !ok {
		return api.ModuleOperationDocumentation{}, false
	}
	var doc api.ModuleOperationDocumentation
	switch decl.kind {
	case notifyBodyful:
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody, _ = notifyRequestBody(method, pattern)
	case notifyBodyless:
		doc.BodyKind = api.ModuleOperationBodyless
	case notifyBodyNoDerivable, notifyBodyPending:
		// Undecided: published unclassified until the handler is read.
	}
	return doc, true
}
