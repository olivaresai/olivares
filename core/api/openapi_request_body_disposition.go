// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

const moduleRequestBodyDispositionExtension = "x-olivares-request-body-disposition"

type moduleRequestBodyDisposition string

const (
	moduleRequestBodySchemaPublished moduleRequestBodyDisposition = "schema-published"
	moduleRequestBodyOpaque          moduleRequestBodyDisposition = "opaque-body"
	moduleRequestBodyBodyless        moduleRequestBodyDisposition = "bodyless"
	moduleRequestBodyUnclassified    moduleRequestBodyDisposition = "unclassified"
)

func moduleRouteIsMutation(r moduleRoute) bool {
	switch r.method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// moduleRequestBodyDispositionFor publishes the request-body kind the owning
// module declares (ModuleOperationDocumenter). Every kind is switched
// explicitly: a route the module does not document, or a kind without its
// body, stays unclassified. The adapter never infers a schema from requestBody
// presence.
func moduleRequestBodyDispositionFor(r moduleRoute) moduleRequestBodyDisposition {
	if r.documentation == nil {
		return moduleRequestBodyUnclassified
	}
	switch r.documentation.BodyKind {
	case ModuleOperationJSONBody:
		if r.documentation.RequestBody != nil {
			return moduleRequestBodySchemaPublished
		}
	case ModuleOperationOpaqueBody:
		if r.documentation.RequestBody != nil {
			return moduleRequestBodyOpaque
		}
	case ModuleOperationBodyless:
		return moduleRequestBodyBodyless
	}
	return moduleRequestBodyUnclassified
}
