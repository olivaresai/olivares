// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

func sessionsFileSearchRoute(method, pattern string) bool {
	return method == http.MethodGet && pattern == "/workspaces/{ref}/files/search"
}

func sessionsFileSearchParameters() []map[string]any {
	return []map[string]any{
		oas.Param("q", "query", "What to look for, 1 to 200 characters after trimming. Literal and case-insensitive; a regular expression is not interpreted.", true, oas.Obj("type", "string", "minLength", 1, "maxLength", searchMaxQuery)),
		oas.Param("in", "query", "name finds files by name or path; content finds lines by their text.", false, oas.Obj("type", "string", "enum", oas.Enum(searchInName, searchInContent), "default", searchInName)),
		oas.Param("limit", "query", "The most matches returned; a larger value is treated as the maximum.", false, oas.Obj("type", "integer", "minimum", 1, "maximum", searchMaxLimit, "default", searchDefaultLimit)),
	}
}

func sessionsFileSearchResponses() map[string]any {
	match := oas.Obj("type", "object", "additionalProperties", false, "required", []string{"path", "name"}, "properties", oas.Obj(
		"path", oas.Obj("type", "string", "description", "Workspace-relative path."),
		"name", oas.Obj("type", "string", "description", "The file's name."),
		"line", oas.Obj("type", "integer", "minimum", 1, "description", "Content search only: 1-based line of the match."),
		"column", oas.Obj("type", "integer", "minimum", 1, "description", "Content search only: 1-based position of the match within the line, in characters."),
		"text", oas.Obj("type", "string", "maxLength", searchMaxText, "description", "Content search only: the matching line, cut to 240 characters around the match."),
	))
	responses := api.GenericModuleResponses()
	responses["200"] = oas.JSONRespSchema("Matches, in rank order for a name search and in path then line order for a content search", oas.Obj("type", "object", "additionalProperties", false, "required", []string{"query", "in", "matches", "truncated"}, "properties", oas.Obj(
		"query", oas.Obj("type", "string"),
		"in", oas.Obj("type", "string", "enum", oas.Enum(searchInName, searchInContent)),
		"matches", oas.Obj("type", "array", "items", match),
		"truncated", oas.Obj("type", "boolean", "description", "True when the limit, or a bound on files, bytes read, matches per file or time, left something unsearched or unreturned."),
	)))
	return responses
}
