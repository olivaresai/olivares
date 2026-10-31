// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

func sessionsProjectConfigApproveSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj("sha256", oas.Obj("type", "string", "pattern", "^[0-9a-f]{64}$", "description", "SHA-256 of the flagged project agent configuration reviewed, as the report or the refused start shows it.")), "sha256")
}

// sessionsProjectConfigResponses publishes the two answers of the project
// agent configuration of a folder.
func sessionsProjectConfigResponses(method, pattern string) (map[string]any, bool) {
	hash := oas.Obj("type", "string", "pattern", "^[0-9a-f]{64}$")
	approval := sessionsClosureClosedObject(oas.Obj("sha256", hash, "actor", oas.Obj("type", "string"), "at", sessionsTimestampSchema()), "sha256", "actor", "at")
	switch method + " " + pattern {
	case http.MethodGet + " /workspaces/{ref}/project-config":
		responses := api.GenericModuleResponses()
		file := oas.Obj("type", "object", "properties", oas.Obj(
			"file", oas.Obj("type", "string"),
			"flagged", oas.Obj("type", "object", "description", "Flagged keys by path with their values; URL credentials are masked. Read, never executed."),
			"unreadable", oas.Obj("type", "string", "description", "Why the file could not be read as the tool's format; it counts as flagged."),
			"refused", oas.Obj("type", "string", "description", "Why no approval can let this file be used: what the tool would load cannot be bound by a hash. A start is refused while it is there.")))
		responses["200"] = oas.JSONRespSchema("Flagged repo-resident agent configuration of the folder, the SHA-256 that covers it and its approval", oas.Obj("type", "object", "properties", oas.Obj(
			"workspace_ref", oas.Obj("type", "string"),
			"files", oas.Obj("type", "array", "items", file),
			"sha256", oas.Obj("type", "string", "description", "Empty when nothing is flagged."),
			"approved", oas.Obj("type", "boolean"),
			"approval", approval)))
		return responses, true
	case http.MethodPost + " /workspaces/{ref}/project-config/approve":
		responses := api.GenericModuleResponses()
		responses["200"] = oas.JSONRespSchema("Who approved which SHA-256, and when", approval)
		return responses, true
	}
	return nil, false
}
