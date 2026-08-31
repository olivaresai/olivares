// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"

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

func eventingRequestBody(r moduleRoute) (map[string]any, bool) {
	decl, ok := eventingRequestBodyDeclarationFor(r)
	if !ok || decl.kind != eventingBodyful {
		return nil, false
	}
	return oaObj(
		"required", true,
		"content", oaObj("application/json", oaObj("schema", decl.schema)),
	), true
}

// eventingRequestBodyDeclarationFor classifies all ten eventing mutations. The
// six bodyful handlers use the module's bounded, unknown-field-rejecting decoder;
// the remaining four do not inspect the request body at all.
func eventingRequestBodyDeclarationFor(r moduleRoute) (eventingRequestBodyDeclaration, bool) {
	if r.ns != "eventing" {
		return eventingRequestBodyDeclaration{}, false
	}

	switch r.method + " " + r.pattern {
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

func eventingEgressCheckSchema() map[string]any {
	return eventingClosedObject(oaObj(
		"endpoint", oaObj(
			"type", "string",
			"minLength", 1,
			"maxLength", 2048,
			"description", "HTTPS destination to check; configured loopback development may also accept HTTP. The deployment policy and DNS/IP safety checks remain authoritative.",
		),
		"subscription_id", oaObj("type", "string"),
	), "endpoint")
}

func eventingSubscriptionSchema(create bool) map[string]any {
	set := siemwire.EventingSinkFormats()
	schema := eventingClosedObject(oaObj(
		"name", oaObj("type", "string", "minLength", 1, "maxLength", 200),
		"enabled", oaObj("anyOf", []any{
			oaObj("type", "boolean"),
			oaObj("type", "null"),
		}),
		"event_types", oaObj(
			"type", "array",
			"minItems", 1,
			"maxItems", 32,
			"items", oaObj(
				"type", "string",
				"minLength", 1,
				"description", "Cataloged event type returned by GET /event-types.",
			),
		),
		"match_sources", oaObj(
			"type", "array",
			"maxItems", 32,
			"items", oaObj("type", "string", "maxLength", 200),
		),
		"endpoint", oaObj(
			"type", "string",
			"minLength", 1,
			"maxLength", 2048,
			"description", "HTTPS delivery destination; configured loopback development may also accept HTTP. Deployment egress-policy and DNS/IP safety checks also apply.",
		),
		"role", oaObj(
			"type", "string",
			"enum", oaEnum("", auth.RoleViewer, auth.RoleEditor, auth.RoleAdmin, auth.RoleOwner),
			"description", "Delivery authorization role. Empty selects viewer; the caller cannot assign a role above its own.",
		),
		"description", oaObj("type", "string", "maxLength", 1024),
		"auth_type", oaObj(
			"type", "string",
			"enum", oaEnum("", "none", "bearer", "basic", "header"),
			"description", "Per-subscription authentication header type. Empty selects none.",
		),
		"auth_value", oaObj("type", "string", "maxLength", 2048),
		"auth_header_name", oaObj("type", "string", "maxLength", 200),
		"max_attempts", oaObj("type", "integer", "format", "int64", "minimum", 0, "maximum", 20),
		"initial_interval_seconds", oaObj("anyOf", []any{
			oaObj("type", "integer", "format", "int64", "const", 0),
			oaObj("type", "integer", "format", "int64", "minimum", 5, "maximum", 3600),
		}),
		"sink_kind", oaObj(
			"type", "string",
			"enum", oaEnum("", "https", "splunk_hec", "sentinel_dcr", "datadog", "newrelic"),
			"description", "SIEM sink kind. Empty selects the generic HMAC-signed webhook.",
		),
		"sink_format", oaObj(
			"type", "string",
			"enum", sinkFormatEnum(),
			"description", "SIEM wire dialect. Empty selects the eventing surface default ("+string(set.Default())+").",
		),
		"sink_cred", oaObj("type", "string", "maxLength", 2048),
		"sink_opts", oaObj(
			"type", "object",
			"maxProperties", 32,
			"propertyNames", oaObj("maxLength", 200),
			"additionalProperties", oaObj("type", "string", "maxLength", 2048),
		),
	), "name", "event_types", "endpoint")

	conditions := []any{
		oaObj(
			"if", oaObj(
				"required", []string{"auth_type"},
				"properties", oaObj("auth_type", oaObj("const", "header")),
			),
			"then", oaObj(
				"required", []string{"auth_header_name"},
				"properties", oaObj("auth_header_name", oaObj("type", "string", "minLength", 1)),
			),
		),
	}
	if create {
		conditions = append(conditions,
			oaObj(
				"if", oaObj(
					"required", []string{"auth_type"},
					"properties", oaObj("auth_type", oaObj("enum", oaEnum("bearer", "basic", "header"))),
				),
				"then", oaObj(
					"required", []string{"auth_value"},
					"properties", oaObj("auth_value", oaObj("type", "string", "minLength", 1)),
				),
			),
			oaObj(
				"if", oaObj(
					"required", []string{"sink_kind"},
					"properties", oaObj("sink_kind", oaObj(
						"enum", oaEnum("splunk_hec", "sentinel_dcr", "datadog", "newrelic"),
					)),
				),
				"then", oaObj(
					"required", []string{"sink_cred"},
					"properties", oaObj("sink_cred", oaObj("type", "string", "minLength", 1)),
				),
			),
		)
	}
	schema["allOf"] = conditions
	return schema
}

func eventingRestoreSchema() map[string]any {
	return eventingClosedObject(oaObj(
		"revision_id", oaObj("type", "string", "minLength", 1),
	), "revision_id")
}

func eventingRotateAuthSchema() map[string]any {
	return eventingClosedObject(oaObj(
		"auth_value", oaObj("type", "string", "minLength", 1, "maxLength", 2048),
	), "auth_value")
}

func eventingReplaySchema() map[string]any {
	return eventingClosedObject(oaObj(
		"from_seq", oaObj("type", "integer", "format", "int64", "minimum", 1),
		"to_seq", oaObj(
			"type", "integer",
			"format", "int64",
			"minimum", 0,
			"description", "Inclusive upper cursor. Zero means newest; a non-zero value must be at least from_seq.",
		),
	), "from_seq")
}
