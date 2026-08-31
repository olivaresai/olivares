// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

func governanceNullable(schema map[string]any) map[string]any {
	return oaObj("anyOf", []any{schema, oaObj("type", "null")})
}

func governanceOptionalString(description string) map[string]any {
	return governanceNullable(oaObj("type", "string", "description", description))
}

func governanceNonBlankString(description string) map[string]any {
	return oaObj("type", "string", "pattern", ".*\\S.*", "description", description)
}

func governancePDPEngineSchema() map[string]any {
	return oaObj("type", "string", "description", "Case-insensitive after trimming: cedar or opa.")
}

func governancePDPEngineSourceSchema(publish bool) map[string]any {
	properties := oaObj(
		"engine", governancePDPEngineSchema(),
		"source", governanceOptionalString("Cedar policy or Rego source."),
		"note", governanceOptionalString("Publish byte-caps this operator note at 4096; validate accepts but ignores it."),
	)
	required := []string{"engine"}
	if publish {
		properties["source"] = governanceNonBlankString("Must compile for the selected engine and is capped at 262144 bytes.")
		required = append(required, "source")
	}
	return governanceClosedObject(properties, required...)
}

func governancePDPStringMapSchema() map[string]any {
	return governanceNullable(oaObj(
		"type", "object",
		"additionalProperties", governanceNullable(oaObj("type", "string")),
	))
}

func governancePDPCandidateSchema() map[string]any {
	principal := governanceClosedObject(oaObj(
		"kind", governanceOptionalString("After trimming, user remains user; every other value is evaluated as token."),
		"id", governanceOptionalString("Optional credential identifier for the example principal."),
	))
	resource := governanceClosedObject(oaObj(
		"kind", governanceOptionalString("Example resource kind."),
		"id", governanceOptionalString("Example resource identifier."),
		"sensitivity", governanceOptionalString("Example sensitivity attribute."),
		"extra", governancePDPStringMapSchema(),
	))
	example := governanceClosedObject(oaObj(
		"principal", governanceNullable(principal),
		"permission", governanceOptionalString("Whitespace is trimmed before evaluation."),
		"tenant", governanceOptionalString("A valid non-zero tenant overrides the authenticated tenant; invalid input falls back to it."),
		"resource", governanceNullable(resource),
	))
	return governanceClosedObject(oaObj(
		"engine", governancePDPEngineSchema(),
		"source", governanceOptionalString("Cedar must compile; OPA candidates are not evaluated in-process."),
		"request", governanceNullable(example),
	), "engine")
}

func governancePDPRollbackSchema() map[string]any {
	return governanceClosedObject(oaObj(
		"engine", governancePDPEngineSchema(),
		"revision", oaObj("type", "integer", "format", "int64", "minimum", 1),
	), "engine", "revision")
}

func governanceBreakGlassActivateSchema() map[string]any {
	return governanceClosedObject(oaObj(
		"match_action", governanceOptionalString("Empty or * covers every action; otherwise exact match or one trailing * prefix wildcard; capped at 128 bytes."),
		"reason", governanceNonBlankString("Mandatory justification, trimmed and capped at 4096 bytes."),
		"expires_in_seconds", governanceNullable(oaObj(
			"type", "integer", "format", "int64", "maximum", 86400,
			"description", "Values at or below zero, null, and omission select the 3600-second default.",
		)),
	), "reason")
}

func governanceBreakGlassConsumeSchema() map[string]any {
	return governanceClosedObject(oaObj(
		"action", governanceNonBlankString("Trimmed, credential-free, and capped at 128 bytes."),
		"subject_kind", governanceOptionalString("Trimmed, credential-free, and capped at 128 bytes."),
		"subject_ref", governanceOptionalString("Credential-free and capped at 4096 bytes."),
	), "action")
}

func governanceRequiredNoteSchema() map[string]any {
	return governanceClosedObject(oaObj(
		"note", governanceNonBlankString("Mandatory post-review note, trimmed and capped at 4096 bytes."),
	), "note")
}

func governanceNHIOwnershipSchema() map[string]any {
	schema := governanceClosedObject(oaObj(
		"owner_ref", governanceOptionalString("When non-blank, must resolve to a human roster identity."),
		"sponsor_ref", governanceOptionalString("When non-blank, must resolve to a human roster identity; agent identities cannot clear their sponsor."),
	))
	schema["anyOf"] = []any{
		oaObj("required", oaEnum("owner_ref"), "properties", oaObj("owner_ref", governanceNonBlankString("Human roster identity."))),
		oaObj("required", oaEnum("sponsor_ref"), "properties", oaObj("sponsor_ref", governanceNonBlankString("Human roster identity."))),
	}
	return schema
}

func governanceNHIPolicySchema() map[string]any {
	return governanceNullable(governanceClosedObject(oaObj(
		"criticality", governanceOptionalString("Case-insensitive after trimming: low, medium, high, or critical; empty leaves the stored value unchanged."),
		"max_age_seconds", governanceNullable(oaObj(
			"type", "integer", "format", "int64", "minimum", 0, "maximum", 31536000,
			"description", "A value above zero updates the policy; zero leaves it unchanged.",
		)),
		"rotation_target", governanceOptionalString("When non-blank, a credential-free actuator target capped at 128 bytes; empty leaves it unchanged."),
		"rotated_at", governanceOptionalString("When non-blank, must parse as an RFC3339 timestamp; empty leaves it unchanged."),
	)))
}

func governanceNHIActionSchema() map[string]any {
	return governanceNullable(governanceClosedObject(oaObj(
		"target_ref", governanceOptionalString("Optional rotation target. The shared strict DTO also accepts it on offboard/finalize, where it is ignored."),
		"reason", governanceOptionalString("Optional approval justification; a handler-specific default is used when empty."),
	)))
}

func governanceKillSwitchEngageSchema() map[string]any {
	return governanceClosedObject(oaObj(
		"scope_kind", oaObj("type", "string", "description", "Case-insensitive after trimming: estate or agent."),
		"scope_ref", governanceOptionalString("Must be empty for estate and non-blank for agent; credential-free and capped at 4096 bytes."),
		"reason", governanceNonBlankString("Mandatory incident reason, trimmed and capped at 4096 bytes."),
	), "scope_kind", "reason")
}

func governanceKillSwitchReenableSchema() map[string]any {
	return governanceNullable(governanceClosedObject(oaObj(
		"reason", governanceOptionalString("Optional dual-control request note, trimmed and capped at 4096 bytes."),
	)))
}

func governanceGuardianCreateSchema() map[string]any {
	return governanceClosedObject(oaObj(
		"name", governanceNonBlankString("Trimmed rule name capped at 128 bytes."),
		"enabled", governanceNullable(oaObj("type", "boolean", "description", "Defaults to true when absent or null.")),
		"match_kinds", governanceOptionalString("Comma-separated finding kinds, capped at 4096 bytes; guardian self-finding prefixes are forbidden."),
		"min_severity", governanceOptionalString("Case-insensitive after trimming: info, low, medium, high, or critical; empty defaults to high."),
		"action", oaObj("type", "string", "description", "Case-insensitive after trimming: stop_agent, quarantine_nhi, or stop_estate."),
		"mode", oaObj("type", "string", "description", "Case-insensitive after trimming: auto or approval."),
		"agent_tier", governanceOptionalString("Case-insensitive after trimming: low, medium, high, critical, or empty for any."),
		"note", governanceOptionalString("Capped at 4096 bytes."),
	), "name", "action", "mode")
}

func governanceGuardianUpdateSchema() map[string]any {
	return governanceNullable(governanceClosedObject(oaObj(
		"enabled", governanceNullable(oaObj("type", "boolean")),
		"match_kinds", governanceOptionalString("Comma-separated finding kinds, capped at 4096 bytes; guardian self-finding prefixes are forbidden."),
		"min_severity", governanceOptionalString("Case-insensitive after trimming: info, low, medium, high, or critical."),
		"action", governanceOptionalString("Case-insensitive after trimming: stop_agent, quarantine_nhi, or stop_estate."),
		"mode", governanceOptionalString("Case-insensitive after trimming: auto or approval."),
		"agent_tier", governanceOptionalString("Case-insensitive after trimming: low, medium, high, critical, or empty to clear."),
		"note", governanceOptionalString("Capped at 4096 bytes."),
	)))
}

func governanceStringArraySchema(nullableItems bool, description string) map[string]any {
	item := oaObj("type", "string")
	if nullableItems {
		item = governanceNullable(item)
	}
	return governanceNullable(oaObj("type", "array", "items", item, "description", description))
}

func governanceRBACNameSchema(description string) map[string]any {
	return oaObj("type", "string", "pattern", "^[A-Za-z0-9._-]{1,64}$", "description", description)
}

func governanceCustomRoleSchema(create bool) map[string]any {
	properties := oaObj(
		"name", governanceOptionalString("On update this DTO field is accepted but ignored; the path names the role."),
		"display_name", governanceOptionalString("Capped at 4096 bytes."),
		"description", governanceOptionalString("Capped at 4096 bytes."),
		"base_role", governanceNullable(oaObj("type", "string", "enum", oaEnum("", "viewer", "editor", "admin", "owner"))),
		"permissions", governanceStringArraySchema(true, "Catalog permissions are trimmed, empty entries skipped, deduplicated, and sorted."),
		"groups", governanceStringArraySchema(false, "Every name must identify an existing permission-group."),
		"excludes", governanceStringArraySchema(true, "Catalog permissions subtracted after base, direct permissions, and groups."),
		"created_by", governanceOptionalString("Output field accepted by the DTO but ignored on input."),
	)
	if create {
		properties["name"] = governanceRBACNameSchema("Must not collide with a built-in role name.")
		return governanceClosedObject(properties, "name")
	}
	return governanceNullable(governanceClosedObject(properties))
}

func governancePermissionGroupSchema(create bool) map[string]any {
	properties := oaObj(
		"name", governanceOptionalString("On update this DTO field is accepted but ignored; the path names the group."),
		"display_name", governanceOptionalString("Capped at 4096 bytes."),
		"description", governanceOptionalString("Capped at 4096 bytes."),
		"permissions", governanceStringArraySchema(true, "Catalog permissions are trimmed, empty entries skipped, deduplicated, and sorted."),
		"created_by", governanceOptionalString("Output field accepted by the DTO but ignored on input."),
	)
	if create {
		properties["name"] = governanceRBACNameSchema("Tenant-unique permission-group name.")
		return governanceClosedObject(properties, "name")
	}
	return governanceNullable(governanceClosedObject(properties))
}

func governanceScopedGrantSchema() map[string]any {
	return governanceClosedObject(oaObj(
		"id", governanceOptionalString("Output field accepted by the DTO but ignored on create."),
		"subject_kind", oaObj("type", "string", "enum", oaEnum("user", "role", "group")),
		"subject_ref", governanceNonBlankString("User/group identifier, or a built-in role name when subject_kind is role."),
		"role", governanceNonBlankString("Must resolve to a built-in role unless role_custom is true, then to a custom role."),
		"role_custom", governanceNullable(oaObj("type", "boolean")),
		"scope_tree", oaObj("type", "string", "enum", oaEnum("tenant", "workspace", "agent_group", "folder")),
		"scope_ref", governanceOptionalString("Must be empty for tenant and identify an existing anchor for every other scope tree."),
		"scope_class", governanceOptionalString("When non-empty, must be a scopeable resource kind compatible with the selected tree."),
		"note", governanceOptionalString("Capped at 4096 bytes."),
		"created_by", governanceOptionalString("Output field accepted by the DTO but ignored on create."),
	), "subject_kind", "subject_ref", "role", "scope_tree")
}

func governanceAgentRiskClassifySchema() map[string]any {
	return governanceClosedObject(oaObj(
		"agent_id", governanceNonBlankString("Must parse as a non-zero agent identifier."),
	), "agent_id")
}

func governanceAgentRiskTierSchema() map[string]any {
	return governanceNullable(governanceClosedObject(oaObj(
		"tier", governanceOptionalString("Case-insensitive after trimming: low, medium, high, critical, or empty/null to clear the operator override."),
	)))
}

func governanceRoutineCadenceSchema() map[string]any {
	return governanceNullable(oaObj("anyOf", []any{
		oaObj("type", "integer", "format", "int64", "const", 0),
		oaObj("type", "integer", "format", "int64", "minimum", 60, "maximum", 31622400),
	}))
}

func governanceRoutineListSchema(description string) map[string]any {
	return governanceNullable(oaObj(
		"type", "array", "description", description,
		"items", governanceNullable(oaObj("type", "string")),
	))
}

func governanceRoutinePolicyCreateSchema() map[string]any {
	return governanceClosedObject(oaObj(
		"name", governanceNonBlankString("Trimmed, credential-free policy name capped at 4096 bytes."),
		"scope_kind", oaObj("type", "string", "description", "Case-insensitive after trimming: tenant, workspace, or user."),
		"scope_ref", governanceOptionalString("Must be empty for tenant and non-blank for workspace/user; credential-free and capped at 4096 bytes."),
		"enabled", governanceNullable(oaObj("type", "boolean", "description", "Defaults to true when absent or null.")),
		"max_cadence_seconds", governanceRoutineCadenceSchema(),
		"max_active_routines", governanceNullable(oaObj("type", "integer", "format", "int64", "minimum", 0)),
		"require_approval", governanceNullable(oaObj("type", "boolean")),
		"allowed_cron_patterns", governanceRoutineListSchema("Null/omission means any; an authored empty array deliberately denies all cron patterns."),
		"blocked_environments", governanceRoutineListSchema("Null/omission means none; an authored empty array is distinct and preserved."),
	), "name", "scope_kind")
}

func governanceRoutinePolicyUpdateSchema() map[string]any {
	return governanceNullable(governanceClosedObject(oaObj(
		"enabled", governanceNullable(oaObj("type", "boolean")),
		"max_cadence_seconds", governanceRoutineCadenceSchema(),
		"max_active_routines", governanceNullable(oaObj("type", "integer", "format", "int64", "minimum", 0)),
		"require_approval", governanceNullable(oaObj("type", "boolean")),
		"allowed_cron_patterns", governanceRoutineListSchema("Absent leaves stored state; null clears to any; [] is an authored deny-all."),
		"blocked_environments", governanceRoutineListSchema("Absent leaves stored state; null clears to none; [] is an authored empty list."),
	)))
}

func governanceAgentCorePlanSchema() map[string]any {
	return governanceNullable(governanceClosedObject(oaObj(
		"enforcement_mode", governanceOptionalString("Optional renderer mode override; when blank, the tenant binding's configured mode is used."),
	)))
}

func governanceAgentCoreApplySchema() map[string]any {
	return governanceClosedObject(oaObj(
		"plan_hash", governanceNonBlankString("Must match a freshly recomputed export plan."),
		"enforcement_mode", governanceOptionalString("Optional renderer mode override; it participates in the recomputed plan."),
	), "plan_hash")
}
