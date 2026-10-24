// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

// sessionsRunFactsResponses publishes the run list and the run detail with the six
// facts typed (session_facts.go). The run resource stays open (every field published
// before the facts is still sent), so the schema names what a console reads and adds
// nothing it must not.
func sessionsRunFactsResponses(method, pattern string) (map[string]any, bool) {
	if method != http.MethodGet || (pattern != "/runs" && pattern != "/runs/{ref}") {
		return nil, false
	}
	run := sessionsRunWithFactsSchema()
	responses := api.GenericModuleResponses()
	if pattern == "/runs/{ref}" {
		responses["200"] = oas.JSONRespSchema("the run, with the six facts a console shows about a session", run)
		return responses, true
	}
	responses["200"] = oas.JSONRespSchema("a page of runs, each with the six facts a console shows about a session", oas.Obj(
		"type", "object", "additionalProperties", true,
		"required", oas.Enum("items", "has_more"),
		"properties", oas.Obj(
			"items", oas.Obj("type", "array", "items", run),
			"has_more", oas.Obj("type", "boolean"),
			"cursor", oas.Obj("type", "string", "description", "Present in cursor mode when more rows follow."),
		),
	))
	return responses, true
}

func sessionsRunWithFactsSchema() map[string]any {
	str := func(description string) map[string]any { return oas.Obj("type", "string", "description", description) }
	reason := str("Why a value is null or missing; present only then.")
	closed := func(properties map[string]any, required ...string) map[string]any {
		schema := oas.Obj("type", "object", "additionalProperties", false, "properties", properties)
		if len(required) > 0 {
			schema["required"] = oas.Enum(required...)
		}
		return schema
	}
	nullable := sessionsClosureNullable
	return oas.Obj(
		"type", "object", "additionalProperties", true,
		"required", oas.Enum("run_ref", "state"),
		"properties", oas.Obj(
			"run_ref", str("The operated session's stable reference."),
			"state", str("The run's derived lifecycle state."),
			"acts_for", closed(oas.Obj(
				"kind", oas.Obj("type", "string", "enum", oas.Enum("user", "automation", "unknown"), "description", "A person launched it, a work item or agent dispatched it, or the session predates the launcher record."),
				"user_id", str("The account that launched the session."),
				"name", str("The account's display name."),
				"reason", reason,
			), "kind"),
			"runs_as", closed(oas.Obj(
				"tool", str("The session driver key, e.g. claude."),
				"tool_name", str("The tool's display name, e.g. Claude Code."),
				"profile_ref", str("The provider profile the session runs under."),
				"account", str("The provider account name of that profile."),
				"label", str("Tool and account, ready to show: Claude Code · team-a."),
				"reason", str("Why no account is shown: the session runs without a provider profile, or its profile could not be read or no longer exists."),
			), "tool", "label"),
			"reaches", closed(oas.Obj(
				"folders", oas.Obj("type", "array", "description", "The session folder and the read-only folders of its workspace.", "items", closed(oas.Obj(
					"path", str("A folder on the engine host."),
					"access", oas.Obj("type", "string", "enum", oas.Enum("write", "read", "unknown"), "description", "unknown when the folder's workspace could not be read or no longer exists."),
				), "path", "access")),
				"hosts", oas.Obj("type", "array", "description", "Hosts the session may connect to besides the engine: the bound provider's endpoint, then the profile's declared hosts.", "items", oas.Obj("type", "string")),
				"reason", reason,
			), "folders", "hosts"),
			"cost", closed(oas.Obj(
				"micro_usd", nullable(oas.Obj("type", "integer", "format", "int64", "description", "Cost so far in micro-USD, from the tokens the tool reported at list price. Null, never 0, when the tool reported nothing to price.")),
				"reason", reason,
				"limit_micro_usd", oas.Obj("type", "integer", "format", "int64", "description", "The limit, in micro-USD, of the most restrictive budget scoped to this session or to its agent identity; present only when one applies."),
				"limit_period", oas.Obj("type", "string", "enum", oas.Enum("daily", "weekly", "monthly", "total"), "description", "The period that budget counts over."),
			), "micro_usd"),
			"approvals", closed(oas.Obj(
				"approved", nullable(oas.Obj("type", "integer", "format", "int64", "description", "Actions of this session a person approved; null when no approval source could be read.")),
				"waiting", oas.Obj("type", "integer", "format", "int64", "description", "Actions of this session waiting for a person now."),
				"reason", reason,
			), "approved", "waiting"),
			"proof", closed(oas.Obj(
				"ledger_entries", nullable(oas.Obj("type", "integer", "format", "int64", "description", "Audit ledger entries that record this session: they target it, name its run or session in their metadata, or its session identity wrote them.")),
				"chain_verified", nullable(oas.Obj("type", "boolean", "description", "Whether the tenant's audit hash chain verifies up to its newest entry; null when the ledger could not be read or holds no entry. Each entry is checked when the engine first reads it; olivares audit verify re-walks the whole chain.")),
				"reason", reason,
			), "ledger_entries", "chain_verified"),
		),
	)
}
