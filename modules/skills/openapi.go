// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills

import (
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

// OperationDocumentation publishes the existing mutation envelopes without
// changing their handlers, authorization, or tenant scope.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	doc := api.ModuleOperationDocumentation{}
	var schema map[string]any
	switch method + " " + pattern {
	case "POST /assignments", "PUT /assignments/{id}":
		schema = map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"target_kind", "target_id", "pack_revision_id"},
			"properties": map[string]any{
				"target_kind":      map[string]any{"type": "string", "enum": []string{"workspace", "folder", "template", "agent_group", "agent", "session"}},
				"target_id":        map[string]any{"type": "string", "format": "uuid"},
				"pack_revision_id": map[string]any{"type": "string", "format": "uuid"},
				"members": map[string]any{"type": []string{"array", "null"}, "maxItems": MaxSelectedMembers, "uniqueItems": true,
					"items": map[string]any{"type": "string"}, "description": "Omitted, null, or empty selects all members of the pinned revision."},
				"approve_active_content": map[string]any{"type": "boolean", "description": "Required when a selected member carries hooks, shell interpolation, a tool pre-approval or a plugin, settings or hooks file. Revisions list these as active_content; the approval is recorded in the audit event."},
			},
		}
	case "POST /packs", "POST /packs/{id}/revisions":
		required := []string{"source"}
		if pattern == "/packs" {
			required = []string{"name", "source"}
		}
		schema = map[string]any{
			"type": "object", "additionalProperties": false, "required": required,
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "Required and nonblank for a new pack; at most 128 bytes. Revisions retain the recorded pack name."},
				"source": map[string]any{
					"type": "object", "additionalProperties": false, "required": []string{"kind"},
					"properties": map[string]any{
						"kind":            map[string]any{"type": "string", "enum": []string{"git", "workspace", "builtin"}},
						"url":             map[string]any{"type": "string", "description": "Git HTTPS URL without credentials, query, or fragment. Empty for workspace and built-in imports."},
						"ref":             map[string]any{"type": "string", "description": "Git branch, tag, or commit to resolve; for a built-in pack, its identifier in the pinned catalog. Empty for workspace imports."},
						"subdir":          map[string]any{"type": "string", "description": "Optional selected Git directory. Empty for workspace imports."},
						"expected_digest": map[string]any{"type": "string", "description": "Optional expected SHA-256 of the selected source snapshot."},
						"workspace_ref":   map[string]any{"type": "string", "description": "Registered workspace UUID. Empty for Git imports."},
						"directory":       map[string]any{"type": "string", "description": "Selected relative directory in the registered workspace. Empty for Git imports."},
					},
					"oneOf": []any{
						map[string]any{"properties": map[string]any{"kind": map[string]any{"const": "git"}}, "required": []string{"url", "ref"}},
						map[string]any{"properties": map[string]any{"kind": map[string]any{"const": "workspace"}}, "required": []string{"workspace_ref", "directory"}},
						map[string]any{"properties": map[string]any{"kind": map[string]any{"const": "builtin"}}, "required": []string{"ref"}},
					},
				},
			},
		}
		doc.Parameters = []map[string]any{{"name": "Idempotency-Key", "in": "header", "required": true,
			"schema": map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "pattern": "^[!-~]+$"}}}
	case "POST /packs/{id}/update":
		doc.Responses = api.GenericModuleResponses()
		doc.Responses["200"] = oas.JSONResp("Update check or idempotent replay of a published revision.")
		doc.Responses["201"] = oas.JSONResp("Reviewed update published as a new pack revision.")
		schema = map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"apply":           map[string]any{"type": "boolean", "description": "Publish the source's current content as the next revision. Requires expected_digest and an Idempotency-Key header."},
				"expected_digest": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$", "description": "The candidate manifest digest (candidate.manifest_digest of the check) the caller reviewed. Required with apply; refused otherwise."},
				"ref":             map[string]any{"type": "string", "description": "Git packs only: follow this branch, tag or commit from now on. Defaults to the recorded ref."},
				"subdir":          map[string]any{"type": "string", "description": "Git packs only: the selected directory, for a revision recorded before directories were kept."},
			},
		}
		doc.Parameters = []map[string]any{{"name": "Idempotency-Key", "in": "header", "required": false,
			"description": "Required with apply.", "schema": map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "pattern": "^[!-~]+$"}}}
	case "GET /packs/{id}/diff":
		doc.BodyKind = api.ModuleOperationBodyless
		doc.Parameters = []map[string]any{
			{"name": "from", "in": "query", "required": false, "description": "Revision to compare from. Defaults to the revision before to.", "schema": map[string]any{"type": "string", "format": "uuid"}},
			{"name": "to", "in": "query", "required": false, "description": "Revision to compare to. Defaults to the latest revision.", "schema": map[string]any{"type": "string", "format": "uuid"}},
		}
		return doc, true
	case "DELETE /assignments/{id}", "DELETE /packs/{id}":
		doc.BodyKind = api.ModuleOperationBodyless
	default:
		return doc, false
	}
	if schema != nil {
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody = map[string]any{"required": true, "content": map[string]any{
			"application/json": map[string]any{"schema": schema},
		}}
		if pattern == "/packs" || pattern == "/packs/{id}/revisions" {
			doc.RequestBody["description"] = "JSON imports a Git, registered-workspace or built-in catalog source. Archive uploads also accept multipart/form-data with archive (binary), format (zip or tar.gz), name (required for a new pack), and optional expected_digest. The same Idempotency-Key header is required."
		}
	}
	if method == "PUT" || method == "DELETE" {
		doc.Parameters = []map[string]any{{"name": "If-Match", "in": "header", "required": true,
			"description": "Positive recorded version, optionally enclosed in double quotes.", "schema": map[string]any{"type": "string"}}}
	}
	return doc, true
}
