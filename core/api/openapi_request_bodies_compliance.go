// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

// complianceRequestBodyKind records the result of reading the compliance
// handler for one mutating route. An opaque body is deliberately distinct from
// a bodyless route: the former consumes raw JSON, but the open handler cannot
// prove a property-level schema for the separately wired resolver or packager.
type complianceRequestBodyKind uint8

const (
	complianceBodyless complianceRequestBodyKind = iota + 1
	complianceBodyful
	complianceBodyOpaque
)

type complianceRequestBodyDeclaration struct {
	kind     complianceRequestBodyKind
	required bool
	schema   map[string]any
}

// complianceRequestBody returns an OpenAPI requestBody for every compliance
// handler that reads one. Opaque raw-JSON handlers publish an empty schema: this
// records the real media type without inventing accepted properties.
func complianceRequestBody(r moduleRoute) (map[string]any, bool) {
	decl, ok := complianceRequestBodyDeclarationFor(r)
	if !ok || (decl.kind != complianceBodyful && decl.kind != complianceBodyOpaque) {
		return nil, false
	}
	return oaObj(
		"required", decl.required,
		"content", oaObj("application/json", oaObj("schema", decl.schema)),
	), true
}

// complianceRequestBodyDeclarationFor classifies all 33 mutating compliance
// routes registered by modules/compliance. Keep opaque cases explicit: a raw
// JSON document is not permission to guess the downstream parser's fields.
func complianceRequestBodyDeclarationFor(r moduleRoute) (complianceRequestBodyDeclaration, bool) {
	if r.ns != "compliance" {
		return complianceRequestBodyDeclaration{}, false
	}

	switch r.method + " " + r.pattern {
	case http.MethodPost + " /frameworks/{id}/evidence":
		return complianceBodyDeclaration(false, complianceObjectSchema(oaObj(
			"scope_note", oaObj("type", "string"),
		))), true
	case http.MethodPost + " /depth/ccm/snapshot":
		return complianceBodyDeclaration(false, complianceObjectSchema(oaObj(
			"frameworks", complianceStringArraySchema(),
			"scope_note", oaObj("type", "string"),
		))), true
	case http.MethodPost + " /residency":
		return complianceBodyDeclaration(true, complianceObjectSchema(oaObj(
			"region", oaObj("type", "string"),
			"perimeter", oaObj("type", "string"),
			"self_hosted", oaObj("type", "boolean"),
			"encryption_at_rest", oaObj("type", "boolean"),
			"data_classes", complianceStringArraySchema(),
			"note", oaObj("type", "string"),
		), "region")), true
	case http.MethodPost + " /risk/classify":
		return complianceBodyDeclaration(true, complianceObjectSchema(oaObj(
			"subject_kind", oaObj("type", "string"),
			"subject_ref", oaObj("type", "string"),
			"agent_id", oaObj("type", "string"),
		), "subject_ref")), true
	case http.MethodPost + " /risk/{id}/review":
		return complianceBodyDeclaration(true, complianceObjectSchema(oaObj(
			"tier", oaObj("type", "string", "enum", oaEnum("unacceptable", "high", "limited", "minimal")),
			"note", oaObj("type", "string"),
		), "tier")), true
	case http.MethodPut + " /nis2/incidents/{id}":
		return complianceBodyDeclaration(true, complianceObjectSchema(oaObj(
			"phase", oaObj("type", "string", "enum", oaEnum("early_warning", "notification", "intermediate", "final")),
			"note", oaObj("type", "string"),
		))), true
	case http.MethodPut + " /retention/policies/{class}":
		return complianceBodyDeclaration(true, complianceObjectSchema(oaObj(
			"retention_days", oaObj("type", "integer", "minimum", 1, "maximum", 36500),
			"disposition", oaObj("type", "string", "enum", oaEnum("retain", "purge")),
			"basis", oaObj("type", "string"),
			"enabled", oaObj("type", "boolean"),
		), "retention_days", "disposition")), true
	case http.MethodPost + " /holds":
		return complianceBodyDeclaration(true, complianceObjectSchema(oaObj(
			"matter_ref", oaObj("type", "string"),
			"title", oaObj("type", "string"),
			"scope_kind", oaObj("type", "string", "enum", oaEnum("tenant", "data_class", "subject")),
			"data_class", oaObj("type", "string"),
			"subject_kind", oaObj("type", "string"),
			"subject_ref", oaObj("type", "string"),
			"reason", oaObj("type", "string"),
			"on_behalf_of", oaObj("type", "string"),
		), "matter_ref", "scope_kind", "reason")), true
	case http.MethodPost + " /holds/{id}/release":
		return complianceBodyDeclaration(false, complianceObjectSchema(oaObj(
			"reason", oaObj("type", "string"),
			"on_behalf_of", oaObj("type", "string"),
		))), true
	case http.MethodPost + " /erasure":
		return complianceBodyDeclaration(true, complianceObjectSchema(oaObj(
			"subject_kind", oaObj("type", "string", "enum", oaEnum("user", "agent", "session", "document", "identity")),
			"subject_ref", oaObj("type", "string"),
			"aliases", complianceStringArraySchema(),
			"data_classes", complianceStringArraySchema(),
			"case_ref", oaObj("type", "string"),
			"reason", oaObj("type", "string"),
		), "subject_kind", "subject_ref", "case_ref")), true
	case http.MethodPost + " /erasure/{id}/execute":
		return complianceBodyDeclaration(false, complianceObjectSchema(oaObj(
			"reason", oaObj("type", "string"),
			"provider_user_ids", complianceStringArraySchema(),
		))), true
	case http.MethodPost + " /data-subjects/{id}/erase":
		return complianceBodyDeclaration(false, complianceObjectSchema(oaObj(
			"subject_kind", oaObj("type", "string", "enum", oaEnum("user", "agent", "session", "document", "identity")),
			"aliases", complianceStringArraySchema(),
			"data_classes", complianceStringArraySchema(),
			"case_ref", oaObj("type", "string"),
			"reason", oaObj("type", "string"),
			"provider_user_ids", complianceStringArraySchema(),
		))), true
	case http.MethodPost + " /claude-files/{id}/erase":
		return complianceBodyDeclaration(false, complianceObjectSchema(oaObj(
			"reason", oaObj("type", "string"),
		))), true

	case http.MethodDelete + " /aims/pack/{id}",
		http.MethodDelete + " /depth/fedramp/{id}",
		http.MethodDelete + " /depth/sector/{id}",
		http.MethodDelete + " /depth/us-law/{id}",
		http.MethodDelete + " /dora/incidents/{id}",
		http.MethodDelete + " /dora/register/{id}",
		http.MethodDelete + " /nis2/incidents/{id}",
		http.MethodDelete + " /oscal/profiles/{id}",
		http.MethodDelete + " /retention/policies/{class}",
		http.MethodPost + " /depth/ccm/drift",
		http.MethodPost + " /residency/scan",
		http.MethodPost + " /retention/sweep":
		return complianceRequestBodyDeclaration{kind: complianceBodyless}, true

	case http.MethodPost + " /oscal/profiles",
		http.MethodPost + " /dora/register",
		http.MethodPost + " /dora/incidents",
		http.MethodPost + " /aims/pack",
		http.MethodPost + " /depth/us-law",
		http.MethodPost + " /depth/sector",
		http.MethodPost + " /depth/fedramp",
		http.MethodPost + " /nis2/incidents/classify":
		return complianceOpaqueDeclaration(), true
	default:
		return complianceRequestBodyDeclaration{}, false
	}
}

func complianceOpaqueDeclaration() complianceRequestBodyDeclaration {
	return complianceRequestBodyDeclaration{
		kind:     complianceBodyOpaque,
		required: true,
		schema:   oaObj(),
	}
}

func complianceBodyDeclaration(required bool, schema map[string]any) complianceRequestBodyDeclaration {
	return complianceRequestBodyDeclaration{
		kind:     complianceBodyful,
		required: required,
		schema:   schema,
	}
}

func complianceObjectSchema(properties map[string]any, required ...string) map[string]any {
	schema := oaObj(
		"type", "object",
		"additionalProperties", false,
		"properties", properties,
	)
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func complianceStringArraySchema() map[string]any {
	return oaObj("type", "array", "items", oaObj("type", "string"))
}
