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

// moduleRequestBodyDispositionFor adapts each handler-derived producer to one
// public vocabulary. Every producer enum is switched explicitly: an added enum
// value therefore remains unclassified until it receives a deliberate mapping.
// In particular, the adapter never infers a schema from requestBody presence.
func moduleRequestBodyDispositionFor(r moduleRoute) moduleRequestBodyDisposition {
	if _, raw := moduleRouteRawRequestBody(r); raw {
		return moduleRequestBodySchemaPublished
	}

	if decl, ok := claudeAgentsRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case claudeAgentsBodyful:
			return moduleRequestBodySchemaPublished
		case claudeAgentsBodyless:
			return moduleRequestBodyBodyless
		case claudeAgentsBodyNoDerivable, claudeAgentsBodyPending:
			return moduleRequestBodyUnclassified
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if decl, ok := capabilitiesRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case capabilitiesBodyful:
			return moduleRequestBodySchemaPublished
		case capabilitiesBodyless:
			return moduleRequestBodyBodyless
		case capabilitiesBodyNoDerivable, capabilitiesBodyPending:
			return moduleRequestBodyUnclassified
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if decl, ok := consoleViewsRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case consoleViewsBodyful:
			return moduleRequestBodySchemaPublished
		case consoleViewsBodyless:
			return moduleRequestBodyBodyless
		case consoleViewsBodyNoDerivable, consoleViewsBodyPending:
			return moduleRequestBodyUnclassified
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if decl, ok := healthRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case healthBodyful:
			return moduleRequestBodySchemaPublished
		case healthBodyless:
			return moduleRequestBodyBodyless
		case healthBodyNoDerivable, healthBodyPending:
			return moduleRequestBodyUnclassified
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if decl, ok := inferenceProxyRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case inferenceProxyBodyful:
			return moduleRequestBodySchemaPublished
		case inferenceProxyBodyless:
			return moduleRequestBodyBodyless
		case inferenceProxyBodyNoDerivable, inferenceProxyBodyPending:
			return moduleRequestBodyUnclassified
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if decl, ok := notifyRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case notifyBodyful:
			return moduleRequestBodySchemaPublished
		case notifyBodyless:
			return moduleRequestBodyBodyless
		case notifyBodyNoDerivable, notifyBodyPending:
			return moduleRequestBodyUnclassified
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if decl, ok := claudePolicyRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case claudePolicyBodyful:
			return moduleRequestBodySchemaPublished
		case claudePolicyBodyless:
			return moduleRequestBodyBodyless
		case claudePolicyBodyNoDerivable, claudePolicyBodyPending:
			return moduleRequestBodyUnclassified
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if decl, ok := recordingRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case recordingBodyful:
			return moduleRequestBodySchemaPublished
		case recordingBodyless:
			return moduleRequestBodyBodyless
		case recordingBodyNoDerivable, recordingBodyPending:
			return moduleRequestBodyUnclassified
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if decl, ok := redTeamRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case redTeamBodyful:
			return moduleRequestBodySchemaPublished
		case redTeamBodyless:
			return moduleRequestBodyBodyless
		case redTeamBodyNoDerivable, redTeamBodyPending:
			return moduleRequestBodyUnclassified
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if decl, ok := voiceRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case voiceBodyful:
			return moduleRequestBodySchemaPublished
		case voiceBodyless:
			return moduleRequestBodyBodyless
		case voiceBodyNoDerivable, voiceBodyPending:
			return moduleRequestBodyUnclassified
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if decl, ok := sandboxRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case sandboxBodyful:
			return moduleRequestBodySchemaPublished
		case sandboxBodyless:
			return moduleRequestBodyBodyless
		case sandboxBodyNoDerivable, sandboxBodyPending:
			return moduleRequestBodyUnclassified
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if decl, ok := securityRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case securityBodyful:
			return moduleRequestBodySchemaPublished
		case securityBodyless:
			return moduleRequestBodyBodyless
		case securityBodyNoDerivable, securityBodyPending:
			return moduleRequestBodyUnclassified
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if decl, ok := reportingRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case reportingBodyful:
			return moduleRequestBodySchemaPublished
		case reportingBodyless:
			return moduleRequestBodyBodyless
		case reportingBodyNoDerivable, reportingBodyPending:
			return moduleRequestBodyUnclassified
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if decl, ok := orchestrationRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case orchestrationBodyful:
			return moduleRequestBodySchemaPublished
		case orchestrationBodyless:
			return moduleRequestBodyBodyless
		case orchestrationBodyNoDerivable, orchestrationBodyPending:
			return moduleRequestBodyUnclassified
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if decl, ok := sourceScopeRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case sourceScopeBodyful:
			return moduleRequestBodySchemaPublished
		case sourceScopeBodyless:
			return moduleRequestBodyBodyless
		case sourceScopeBodyNoDerivable, sourceScopeBodyPending:
			return moduleRequestBodyUnclassified
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if decl, ok := finopsRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case finopsBodyful:
			return moduleRequestBodySchemaPublished
		case finopsBodyless:
			return moduleRequestBodyBodyless
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if decl, ok := modelsRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case modelsBodyful:
			return moduleRequestBodySchemaPublished
		case modelsPostBodyless, modelsDeleteBodyless:
			return moduleRequestBodyBodyless
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if decl, ok := complianceRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case complianceBodyful:
			return moduleRequestBodySchemaPublished
		case complianceBodyOpaque:
			return moduleRequestBodyOpaque
		case complianceBodyless:
			return moduleRequestBodyBodyless
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if decl, ok := governanceRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case governanceBodyful:
			return moduleRequestBodySchemaPublished
		case governanceBodyless:
			return moduleRequestBodyBodyless
		case governanceBodyNoDerivable, governanceBodyPending:
			return moduleRequestBodyUnclassified
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if decl, ok := evalsRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case evalsBodyful:
			return moduleRequestBodySchemaPublished
		case evalsBodyless:
			return moduleRequestBodyBodyless
		case evalsBodyNoDerivable, evalsBodyPending:
			return moduleRequestBodyUnclassified
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if decl, ok := catalogRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case catalogBodyful:
			return moduleRequestBodySchemaPublished
		case catalogBodyless:
			return moduleRequestBodyBodyless
		case catalogBodyNoDerivable, catalogBodyPending:
			return moduleRequestBodyUnclassified
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if decl, ok := eventingRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case eventingBodyful:
			return moduleRequestBodySchemaPublished
		case eventingBodyless:
			return moduleRequestBodyBodyless
		case eventingBodyNoDerivable, eventingBodyPending:
			return moduleRequestBodyUnclassified
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if decl, ok := knowledgeRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case knowledgeBodyful:
			return moduleRequestBodySchemaPublished
		case knowledgeBodyOpaque:
			return moduleRequestBodyOpaque
		case knowledgeBodyless:
			return moduleRequestBodyBodyless
		case knowledgeBodyPending:
			return moduleRequestBodyUnclassified
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if decl, ok := deployRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case deployBodyful:
			return moduleRequestBodySchemaPublished
		case deployBodyless:
			return moduleRequestBodyBodyless
		case deployBodyNoDerivable, deployBodyPending:
			return moduleRequestBodyUnclassified
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if decl, ok := sessionsClosureRequestBodyDeclarationFor(r); ok {
		switch decl.kind {
		case sessionsClosureBodyful:
			return moduleRequestBodySchemaPublished
		case sessionsClosureBodyless:
			return moduleRequestBodyBodyless
		case sessionsClosureBodyNoDerivable, sessionsClosureBodyPending:
			return moduleRequestBodyUnclassified
		default:
			return moduleRequestBodyUnclassified
		}
	}
	if sessionsLegacyRequestBodyIsSchemaPublished(r) {
		return moduleRequestBodySchemaPublished
	}
	return moduleRequestBodyUnclassified
}

// sessionsLegacyRequestBodyIsSchemaPublished mirrors only the direct Sessions
// schemas in moduleRequestBody. It stays route-explicit so an unrelated POST in
// that namespace cannot be upgraded merely because it is a payload verb.
func sessionsLegacyRequestBodyIsSchemaPublished(r moduleRoute) bool {
	if r.ns != "sessions" {
		return false
	}
	if r.method == http.MethodPost {
		switch r.pattern {
		case "/protocol-binding-specs",
			"/protocol-binding-specs/{id}/activate",
			"/protocol-binding-specs/{id}/disable",
			"/protocol-bindings/{id}/reconcile",
			"/runs/{ref}/input",
			"/runs/{ref}/stop":
			return true
		}
	}
	return sessionsWorkMutation(r)
}
