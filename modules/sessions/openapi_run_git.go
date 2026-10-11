// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

func sessionsGitDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	const base = "/runs/{ref}/git"
	doc := api.ModuleOperationDocumentation{}
	status := method == http.MethodGet && pattern == base
	action := strings.TrimPrefix(pattern, base+"/")
	if !status && (method != http.MethodPost || (action != "stage" && action != "unstage" && action != "commit" && action != "branch")) {
		return doc, false
	}
	responses := api.GenericModuleResponses()
	responses["409"] = oas.JSONResp("Git refused the operation, the workspace changed, or the work lease is stale")
	responses["422"] = oas.JSONResp("unsupported isolation or Git output limit exceeded")
	responses["502"] = oas.JSONResp("session OS boundary could not be established; no unconfined fallback")
	responses["503"] = oas.JSONResp("session confinement, Git, policy or store is unavailable")
	if status {
		file := oas.Obj("type", "object", "required", []string{"path", "index", "worktree"}, "properties", oas.Obj("path", oas.Obj("type", "string"), "index", oas.Obj("type", "string"), "worktree", oas.Obj("type", "string")))
		schema := oas.Obj("type", "object", "required", []string{"branch", "branches", "files", "truncated", "writable"}, "properties", oas.Obj(
			"branch", oas.Obj("type", "string", "description", "Current local branch; empty for detached HEAD."),
			"branches", oas.Obj("type", "array", "maxItems", 200, "items", oas.Obj("type", "string")),
			"files", oas.Obj("type", "array", "maxItems", 200, "items", file),
			"truncated", oas.Obj("type", "boolean"), "writable", oas.Obj("type", "boolean")))
		responses["200"] = oas.JSONRespSchema("Confined local Git status and folder write policy", schema)
	} else {
		responses["200"] = oas.JSONRespSchema("Local Git operation completed", oas.Obj("type", "object", "required", []string{"ok"}, "properties", oas.Obj("ok", oas.Obj("type", "boolean"))))
		props := oas.Obj("paths", oas.Obj("type", "array", "minItems", 1, "maxItems", 200, "items", oas.Obj("type", "string", "maxLength", 4096)), "message", oas.Obj("type", "string", "maxLength", 8192, "description", "Nonblank commit message, at most 8192 UTF-8 bytes. Both Git identities come from the authenticated user profile."), "name", oas.Obj("type", "string", "maxLength", 512), "create", oas.Obj("type", "boolean"), "work_lease_fence", oas.Obj("type", "integer", "format", "int64", "minimum", 1))
		required := []string{"paths"}
		if action == "commit" {
			required = []string{"message"}
		}
		if action == "branch" {
			required = []string{"name"}
		}
		schema := oas.Obj("type", "object", "additionalProperties", false, "required", required, "properties", props)
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody = oas.Obj("required", true, "content", oas.Obj("application/json", oas.Obj("schema", schema)))
	}
	doc.Responses = responses
	return doc, true
}
