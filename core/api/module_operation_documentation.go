// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package api

// ModuleOperationDocumenter lets a composition-root module publish its owned
// wire schemas without moving feature contracts into the API package. It adds
// documentation only; route authorization and tenant/system scope stay fixed.
type ModuleOperationDocumenter interface {
	OperationDocumentation(method, pattern string) (ModuleOperationDocumentation, bool)
}

type ModuleOperationBodyKind string

const (
	ModuleOperationJSONBody ModuleOperationBodyKind = "schema-published"
	ModuleOperationBodyless ModuleOperationBodyKind = "bodyless"
)

type ModuleOperationDocumentation struct {
	BodyKind          ModuleOperationBodyKind
	RequestBody       map[string]any
	SuccessResponses  map[string]any
	Parameters        []map[string]any
	RequiredAssurance int
}
