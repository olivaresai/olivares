// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

type eventingRequestBodyKind uint8

const (
	eventingBodyless eventingRequestBodyKind = iota + 1
	eventingBodyful
	eventingBodyNoDerivable
	eventingBodyPending
)

type eventingRequestBodyDeclaration struct {
	kind   eventingRequestBodyKind
	schema map[string]any
}

func eventingRequestBody(method, pattern string) (map[string]any, bool) {
	decl, ok := eventingRequestBodyDeclarationFor(method, pattern)
	if !ok || decl.kind != eventingBodyful {
		return nil, false
	}
	return oas.Obj(
		"required", true,
		"content", oas.Obj("application/json", oas.Obj("schema", decl.schema)),
	), true
}

// eventingRequestBodyDeclarationFor classifies all ten eventing mutations. The
// six bodyful handlers use the module's bounded, unknown-field-rejecting decoder;
// the remaining four do not inspect the request body at all.
func eventingRequestBodyDeclarationFor(method, pattern string) (eventingRequestBodyDeclaration, bool) {

	switch method + " " + pattern {
	case http.MethodPost + " /egress-policy/check":
		return eventingBodyDeclaration(eventingEgressCheckSchema()), true
	case http.MethodPost + " /subscriptions":
		return eventingBodyDeclaration(eventingSubscriptionSchema(true)), true
	case http.MethodPut + " /subscriptions/{id}":
		return eventingBodyDeclaration(eventingSubscriptionSchema(false)), true
	case http.MethodPost + " /subscriptions/{id}/restore":
		return eventingBodyDeclaration(eventingRestoreSchema()), true
	case http.MethodPost + " /subscriptions/{id}/rotate-auth":
		return eventingBodyDeclaration(eventingRotateAuthSchema()), true
	case http.MethodPost + " /subscriptions/{id}/replay":
		return eventingBodyDeclaration(eventingReplaySchema()), true

	case http.MethodDelete + " /subscriptions/{id}",
		http.MethodPost + " /subscriptions/{id}/rotate-secret",
		http.MethodPost + " /subscriptions/{id}/test",
		http.MethodPost + " /deliveries/{id}/redeliver":
		return eventingRequestBodyDeclaration{kind: eventingBodyless}, true
	default:
		return eventingRequestBodyDeclaration{}, false
	}
}

func eventingBodyDeclaration(schema map[string]any) eventingRequestBodyDeclaration {
	return eventingRequestBodyDeclaration{kind: eventingBodyful, schema: schema}
}

func eventingClosedObject(properties map[string]any, required ...string) map[string]any {
	schema := oas.Obj(
		"type", "object",
		"additionalProperties", false,
		"properties", properties,
	)
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func eventingEgressCheckSchema() map[string]any {
	return eventingClosedObject(oas.Obj(
		"endpoint", oas.Obj(
			"type", "string",
			"minLength", 1,
			"maxLength", 2048,
			"description", "HTTPS destination to check; configured loopback development may also accept HTTP. The deployment policy and DNS/IP safety checks remain authoritative.",
		),
		"subscription_id", oas.Obj("type", "string"),
	), "endpoint")
}

func eventingSubscriptionSchema(create bool) map[string]any {
	set := siemwire.EventingSinkFormats()
	schema := eventingClosedObject(oas.Obj(
		"name", oas.Obj("type", "string", "minLength", 1, "maxLength", 200),
		"enabled", oas.Obj("anyOf", []any{
			oas.Obj("type", "boolean"),
			oas.Obj("type", "null"),
		}),
		"event_types", oas.Obj(
			"type", "array",
			"minItems", 1,
			"maxItems", 32,
			"items", oas.Obj(
				"type", "string",
				"minLength", 1,
				"description", "Cataloged event type returned by GET /event-types.",
			),
		),
		"match_sources", oas.Obj(
			"type", "array",
			"maxItems", 32,
			"items", oas.Obj("type", "string", "maxLength", 200),
		),
		"endpoint", oas.Obj(
			"type", "string",
			"minLength", 1,
			"maxLength", 2048,
			"description", "HTTPS delivery destination; configured loopback development may also accept HTTP. Deployment egress-policy and DNS/IP safety checks also apply.",
		),
		"role", oas.Obj(
			"type", "string",
			"enum", oas.Enum("", auth.RoleViewer, auth.RoleEditor, auth.RoleAdmin, auth.RoleOwner),
			"description", "Delivery authorization role. Empty selects viewer; the caller cannot assign a role above its own.",
		),
		"description", oas.Obj("type", "string", "maxLength", 1024),
		"auth_type", oas.Obj(
			"type", "string",
			"enum", oas.Enum("", "none", "bearer", "basic", "header"),
			"description", "Per-subscription authentication header type. Empty selects none.",
		),
		"auth_value", oas.Obj("type", "string", "maxLength", 2048),
		"auth_header_name", oas.Obj("type", "string", "maxLength", 200),
		"max_attempts", oas.Obj("type", "integer", "format", "int64", "minimum", 0, "maximum", 20),
		"initial_interval_seconds", oas.Obj("anyOf", []any{
			oas.Obj("type", "integer", "format", "int64", "const", 0),
			oas.Obj("type", "integer", "format", "int64", "minimum", 5, "maximum", 3600),
		}),
		"sink_kind", oas.Obj(
			"type", "string",
			"enum", oas.Enum("", "https", "splunk_hec", "sentinel_dcr", "datadog", "newrelic"),
			"description", "SIEM sink kind. Empty selects the generic HMAC-signed webhook.",
		),
		"sink_format", oas.Obj(
			"type", "string",
			"enum", sinkFormatEnum(),
			"description", "SIEM wire dialect. Empty selects the eventing surface default ("+string(set.Default())+").",
		),
		"sink_cred", oas.Obj("type", "string", "maxLength", 2048),
		"sink_opts", oas.Obj(
			"type", "object",
			"maxProperties", 32,
			"propertyNames", oas.Obj("maxLength", 200),
			"additionalProperties", oas.Obj("type", "string", "maxLength", 2048),
		),
	), "name", "event_types", "endpoint")

	conditions := []any{
		oas.Obj(
			"if", oas.Obj(
				"required", []string{"auth_type"},
				"properties", oas.Obj("auth_type", oas.Obj("const", "header")),
			),
			"then", oas.Obj(
				"required", []string{"auth_header_name"},
				"properties", oas.Obj("auth_header_name", oas.Obj("type", "string", "minLength", 1)),
			),
		),
	}
	if create {
		conditions = append(conditions,
			oas.Obj(
				"if", oas.Obj(
					"required", []string{"auth_type"},
					"properties", oas.Obj("auth_type", oas.Obj("enum", oas.Enum("bearer", "basic", "header"))),
				),
				"then", oas.Obj(
					"required", []string{"auth_value"},
					"properties", oas.Obj("auth_value", oas.Obj("type", "string", "minLength", 1)),
				),
			),
			oas.Obj(
				"if", oas.Obj(
					"required", []string{"sink_kind"},
					"properties", oas.Obj("sink_kind", oas.Obj(
						"enum", oas.Enum("splunk_hec", "sentinel_dcr", "datadog", "newrelic"),
					)),
				),
				"then", oas.Obj(
					"required", []string{"sink_cred"},
					"properties", oas.Obj("sink_cred", oas.Obj("type", "string", "minLength", 1)),
				),
			),
		)
	}
	schema["allOf"] = conditions
	return schema
}

func eventingRestoreSchema() map[string]any {
	return eventingClosedObject(oas.Obj(
		"revision_id", oas.Obj("type", "string", "minLength", 1),
	), "revision_id")
}

func eventingRotateAuthSchema() map[string]any {
	return eventingClosedObject(oas.Obj(
		"auth_value", oas.Obj("type", "string", "minLength", 1, "maxLength", 2048),
	), "auth_value")
}

func eventingReplaySchema() map[string]any {
	return eventingClosedObject(oas.Obj(
		"from_seq", oas.Obj("type", "integer", "format", "int64", "minimum", 1),
		"to_seq", oas.Obj(
			"type", "integer",
			"format", "int64",
			"minimum", 0,
			"description", "Inclusive upper cursor. Zero means newest; a non-zero value must be at least from_seq.",
		),
	), "from_seq")
}

// OperationDocumentation publishes the request body each mutation's handler decodes.
// A route this module has not classified stays unclassified.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	decl, ok := eventingRequestBodyDeclarationFor(method, pattern)
	if !ok {
		return api.ModuleOperationDocumentation{}, false
	}
	var doc api.ModuleOperationDocumentation
	switch decl.kind {
	case eventingBodyful:
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody, _ = eventingRequestBody(method, pattern)
	case eventingBodyless:
		doc.BodyKind = api.ModuleOperationBodyless
	case eventingBodyNoDerivable, eventingBodyPending:
		// Undecided: published unclassified until the handler is read.
	}
	return doc, true
}

// sinkFormatEnum renders the eventing sink_format vocabulary for the OpenAPI
// enum from the sdk/siemwire catalog — auditFormatEnum's pattern applied to the
// beta surface. The empty spelling leads because an unset format is valid and
// selects the surface default.
func sinkFormatEnum() []any {
	toks := siemwire.EventingSinkFormats().Tokens()
	out := make([]any, 0, len(toks)+1)
	out = append(out, "")
	for _, t := range toks {
		out = append(out, string(t))
	}
	return out
}
