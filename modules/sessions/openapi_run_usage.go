// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

// sessionsRunUsageResponses publishes GET /usage (runtime_usage_read.go): the closed row a
// console totals from. The three sums are nullable on purpose, because null is "no run
// reported it" and 0 would be a claim.
func sessionsRunUsageResponses(method, pattern string) (map[string]any, bool) {
	if method != http.MethodGet || pattern != "/usage" {
		return nil, false
	}
	count := func(description string) map[string]any {
		return oas.Obj("type", "integer", "format", "int64", "description", description)
	}
	sum := func(description string) map[string]any { return sessionsClosureNullable(count(description)) }
	row := sessionsClosureClosedObject(oas.Obj(
		"key", oas.Obj("type", "string", "description", "The model, account reference, provider kind or tool of the runs in this row. Absent when those runs recorded none."),
		"day", oas.Obj("type", "string", "description", "The day (UTC, YYYY-MM-DD) the runs were created. Present only with bucket=day."),
		"runs", count("Runs in this row."),
		"input_tokens", sum("Input tokens the runs reported, summed. Null when no run reported any."),
		"output_tokens", sum("Output tokens the runs reported, summed. Null when no run reported any."),
		"cost_micro_usd", sum("Cost the tools reported, in millionths of a US dollar, summed. Null, never 0, when no run reported one."),
		"runs_without_tokens", count("Runs that reported neither input nor output tokens."),
		"runs_without_cost", count("Runs that reported no cost."),
	), "runs", "input_tokens", "output_tokens", "cost_micro_usd", "runs_without_tokens", "runs_without_cost")
	responses := api.GenericModuleResponses()
	responses["200"] = oas.JSONRespSchema("What the runs the caller may read used, totalled by the requested dimension", sessionsClosureClosedObject(oas.Obj(
		"group_by", oas.Obj("type", "string", "enum", oas.Enum(usageByModel, usageByAccount, usageByProvider, usageByTool)),
		"bucket", oas.Obj("type", "string", "enum", oas.Enum(usageBucketDay)),
		"since", sessionsTimestampSchema(),
		"until", sessionsTimestampSchema(),
		"items", oas.Obj("type", "array", "items", row),
	), "group_by", "items"))
	return responses, true
}

func sessionsRunUsageParameters(method, pattern string) []map[string]any {
	if method != http.MethodGet || pattern != "/usage" {
		return nil
	}
	return []map[string]any{
		oas.Param("group_by", "query", "What to total by: the model a run used, its provider account, the provider kind it ran on, or its tool.", true,
			oas.Obj("type", "string", "enum", oas.Enum(usageByModel, usageByAccount, usageByProvider, usageByTool))),
		oas.Param("since", "query", "Only runs created at or after this time.", false, sessionsTimestampSchema()),
		oas.Param("until", "query", "Only runs created before this time; it must be after since.", false, sessionsTimestampSchema()),
		oas.Param("bucket", "query", "day splits every row by the day (UTC) its runs were created.", false,
			oas.Obj("type", "string", "enum", oas.Enum(usageBucketDay))),
	}
}
