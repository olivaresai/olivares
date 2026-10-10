// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package api

import "github.com/olivaresai/olivares/core/api/oas"

// ModuleOperationDocumenter lets a composition-root module publish its owned
// wire schemas without moving feature contracts into the API package. It adds
// documentation only; route authorization and tenant/system scope stay fixed.
// A module is asked only about the routes it registered, by method and
// module-relative pattern.
type ModuleOperationDocumenter interface {
	OperationDocumentation(method, pattern string) (ModuleOperationDocumentation, bool)
}

type ModuleOperationBodyKind string

const (
	ModuleOperationJSONBody ModuleOperationBodyKind = "schema-published"
	// ModuleOperationOpaqueBody publishes RequestBody for a handler that reads
	// the body without a schema of its own (raw JSON, NDJSON).
	ModuleOperationOpaqueBody ModuleOperationBodyKind = "opaque-body"
	ModuleOperationBodyless   ModuleOperationBodyKind = "bodyless"
)

type ModuleOperationDocumentation struct {
	BodyKind    ModuleOperationBodyKind
	RequestBody map[string]any
	// SuccessResponses are added to a fixed error set (moduleResponses).
	SuccessResponses map[string]any
	// Responses, when set, is the operation's whole responses object. Start
	// from GenericModuleResponses to change only some statuses.
	Responses map[string]any
	// Parameters precede the tenant header.
	Parameters []map[string]any
	// PathParameters replace the generated path parameter of the same name.
	PathParameters map[string]map[string]any
	// TrailingParameters follow the path parameters.
	TrailingParameters []map[string]any
	// Extensions are x- fields copied onto the operation; a field the core
	// already set (permission, assurance, scope, disposition) is not replaced.
	Extensions        map[string]any
	RequiredAssurance int
}

// GenericModuleResponses is the responses object of a module operation that
// declares none: the generic JSON envelope for success and for every error.
// Each call returns a new map.
func GenericModuleResponses() map[string]any {
	return oas.Obj(
		"400", oas.JSONResp("bad request"),
		"401", oas.JSONResp("unauthenticated"),
		"403", oas.JSONResp("forbidden"),
		"404", oas.JSONResp("not found"),
		"409", oas.JSONResp("conflict / setup required"),
		"429", oas.JSONResp("rate limited"),
		"200", oas.JSONResp("OK"),
	)
}
