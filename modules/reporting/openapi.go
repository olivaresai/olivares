// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

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

func reportingRequestBody(method, pattern string) (map[string]any, bool) {
	decl, ok := reportingRequestBodyDeclarationFor(method, pattern)
	if !ok || decl.kind != reportingBodyful {
		return nil, false
	}
	return oas.Obj(
		"required", true,
		"content", oas.Obj(decl.mediaType, oas.Obj("schema", decl.schema)),
	), true
}

func reportingRequestBodyDeclarationFor(method, pattern string) (reportingRequestBodyDeclaration, bool) {
	switch method + " " + pattern {
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
	return oas.Obj("anyOf", []any{schema, oas.Obj("type", "null")})
}

// Schedule and branding use json.Decoder directly without DisallowUnknownFields,
// so both object schemas intentionally remain open.
func reportingScheduleSchema() map[string]any {
	return oas.Obj(
		"type", "object",
		"additionalProperties", true,
		"properties", oas.Obj(
			"id", reportingNullable(oas.Obj("type", "string")),
			"report_type", oas.Obj("type", "string", "enum", oas.Enum("compliance-evidence", "audit-summary", "finops-report", "access-review", "executive-summary")),
			"format", reportingNullable(oas.Obj("type", "string", "description", "pdf is preserved; every other value, including empty, is normalized to html.")),
			"cron", oas.Obj("type", "string", "description", "Must parse as the reporting module's five-field cron expression."),
			"framework", reportingNullable(oas.Obj("type", "string")),
			"team", reportingNullable(oas.Obj("type", "string")),
			"locale", reportingNullable(oas.Obj("type", "string")),
			"enabled", reportingNullable(oas.Obj("type", "boolean")),
		),
		"required", oas.Enum("report_type", "cron"),
	)
}

func reportingBrandingSchema() map[string]any {
	return oas.Obj(
		"type", "object",
		"additionalProperties", true,
		"properties", oas.Obj(
			"logo_path", reportingNullable(oas.Obj("type", "string")),
			"primary_color", reportingNullable(oas.Obj("type", "string")),
			"secondary_color", reportingNullable(oas.Obj("type", "string")),
			"footer_text", reportingNullable(oas.Obj("type", "string")),
			"company_name", reportingNullable(oas.Obj("type", "string")),
		),
	)
}

func reportingTemplateSchema() map[string]any {
	return oas.Obj(
		"type", "string",
		"description", "Raw custom report template. After whitespace trimming it must be non-empty; the handler caps the original body at 524288 bytes.",
	)
}

// OperationDocumentation publishes the request body each mutation's handler
// decodes, and the signing switch's contract. A route this module has not
// classified stays unclassified.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	if doc, ok := signingOperationDocumentation(method, pattern); ok {
		return doc, true
	}
	decl, ok := reportingRequestBodyDeclarationFor(method, pattern)
	if !ok {
		return api.ModuleOperationDocumentation{}, false
	}
	var doc api.ModuleOperationDocumentation
	switch decl.kind {
	case reportingBodyful:
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody, _ = reportingRequestBody(method, pattern)
	case reportingBodyless:
		doc.BodyKind = api.ModuleOperationBodyless
	case reportingBodyNoDerivable, reportingBodyPending:
		// Undecided: published unclassified until the handler is read.
	}
	return doc, true
}
