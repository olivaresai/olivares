// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

type reportingRequestBodyKind uint8

const (
	reportingBodyless reportingRequestBodyKind = iota + 1
	reportingBodyful
	reportingBodyNoDerivable
	reportingBodyPending
)

type reportingRequestBodyDeclaration struct {
	kind      reportingRequestBodyKind
	mediaType string
	schema    map[string]any
}

func reportingRequestBody(r moduleRoute) (map[string]any, bool) {
	decl, ok := reportingRequestBodyDeclarationFor(r)
	if !ok || decl.kind != reportingBodyful {
		return nil, false
	}
	return oaObj(
		"required", true,
		"content", oaObj(decl.mediaType, oaObj("schema", decl.schema)),
	), true
}

func reportingRequestBodyDeclarationFor(r moduleRoute) (reportingRequestBodyDeclaration, bool) {
	if r.ns != "reporting" {
		return reportingRequestBodyDeclaration{}, false
	}
	switch r.method + " " + r.pattern {
	case http.MethodPost + " /schedules":
		return reportingBodyDeclaration("application/json", reportingScheduleSchema()), true
	case http.MethodPut + " /branding":
		return reportingBodyDeclaration("application/json", reportingBrandingSchema()), true
	case http.MethodPut + " /templates/{type}":
		return reportingBodyDeclaration("text/html", reportingTemplateSchema()), true
	case http.MethodDelete + " /schedules/{id}", http.MethodDelete + " /templates/{type}":
		return reportingRequestBodyDeclaration{kind: reportingBodyless}, true
	default:
		return reportingRequestBodyDeclaration{}, false
	}
}

func reportingBodyDeclaration(mediaType string, schema map[string]any) reportingRequestBodyDeclaration {
	return reportingRequestBodyDeclaration{kind: reportingBodyful, mediaType: mediaType, schema: schema}
}

func reportingNullable(schema map[string]any) map[string]any {
	return oaObj("anyOf", []any{schema, oaObj("type", "null")})
}

// Schedule and branding use json.Decoder directly without DisallowUnknownFields,
// so both object schemas intentionally remain open.
func reportingScheduleSchema() map[string]any {
	return oaObj(
		"type", "object",
		"additionalProperties", true,
		"properties", oaObj(
			"id", reportingNullable(oaObj("type", "string")),
			"report_type", oaObj("type", "string", "enum", oaEnum("compliance-evidence", "audit-summary", "finops-report", "access-review", "executive-summary")),
			"format", reportingNullable(oaObj("type", "string", "description", "pdf is preserved; every other value, including empty, is normalized to html.")),
			"cron", oaObj("type", "string", "description", "Must parse as the reporting module's five-field cron expression."),
			"framework", reportingNullable(oaObj("type", "string")),
			"team", reportingNullable(oaObj("type", "string")),
			"locale", reportingNullable(oaObj("type", "string")),
			"enabled", reportingNullable(oaObj("type", "boolean")),
		),
		"required", oaEnum("report_type", "cron"),
	)
}

func reportingBrandingSchema() map[string]any {
	return oaObj(
		"type", "object",
		"additionalProperties", true,
		"properties", oaObj(
			"logo_path", reportingNullable(oaObj("type", "string")),
			"primary_color", reportingNullable(oaObj("type", "string")),
			"secondary_color", reportingNullable(oaObj("type", "string")),
			"footer_text", reportingNullable(oaObj("type", "string")),
			"company_name", reportingNullable(oaObj("type", "string")),
		),
	)
}

func reportingTemplateSchema() map[string]any {
	return oaObj(
		"type", "string",
		"description", "Raw custom report template. After whitespace trimming it must be non-empty; the handler caps the original body at 524288 bytes.",
	)
}
