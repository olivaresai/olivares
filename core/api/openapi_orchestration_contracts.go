// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

type orchestrationRequestBodyKind uint8

const (
	orchestrationBodyless orchestrationRequestBodyKind = iota + 1
	orchestrationBodyful
	orchestrationBodyNoDerivable
	orchestrationBodyPending
)

type orchestrationRequestBodyDeclaration struct {
	kind     orchestrationRequestBodyKind
	required bool
	schema   map[string]any
}

func orchestrationRequestBody(r moduleRoute) (map[string]any, bool) {
	decl, ok := orchestrationRequestBodyDeclarationFor(r)
	if !ok || decl.kind != orchestrationBodyful {
		return nil, false
	}
	return oaObj(
		"required", decl.required,
		"content", oaObj("application/json", oaObj("schema", decl.schema)),
	), true
}

func orchestrationRequestBodyDeclarationFor(r moduleRoute) (orchestrationRequestBodyDeclaration, bool) {
	if r.ns != "orchestration" {
		return orchestrationRequestBodyDeclaration{}, false
	}
	switch r.method + " " + r.pattern {
	case http.MethodPost + " /schedules":
		return orchestrationBodyDeclaration(true, orchestrationCreateScheduleSchema()), true
	case http.MethodPatch + " /schedules/{id}":
		return orchestrationBodyDeclaration(true, orchestrationPatchScheduleSchema()), true
	case http.MethodPost + " /schedules/{id}/fire":
		return orchestrationBodyDeclaration(false, orchestrationApprovalSchema(false)), true
	case http.MethodPost + " /schedules/{id}/restore":
		return orchestrationBodyDeclaration(true, orchestrationApprovalSchema(true)), true
	case http.MethodPost + " /workflows":
		return orchestrationBodyDeclaration(true, orchestrationCreateWorkflowSchema()), true
	case http.MethodPatch + " /workflows/{id}":
		return orchestrationBodyDeclaration(true, orchestrationPatchWorkflowSchema()), true
	case http.MethodPut + " /workflows/{id}/steps":
		return orchestrationBodyDeclaration(true, orchestrationPutStepsSchema()), true
	case http.MethodPost + " /workflows/{id}/restore":
		return orchestrationBodyDeclaration(true, orchestrationApprovalSchema(true)), true
	case http.MethodPost + " /workflows/{id}/run":
		return orchestrationBodyDeclaration(false, orchestrationApprovalSchema(false)), true
	case http.MethodPost + " /workflows/{id}/dry-run":
		return orchestrationRequestBodyDeclaration{kind: orchestrationBodyless}, true
	default:
		return orchestrationRequestBodyDeclaration{}, false
	}
}

func orchestrationBodyDeclaration(required bool, schema map[string]any) orchestrationRequestBodyDeclaration {
	return orchestrationRequestBodyDeclaration{kind: orchestrationBodyful, required: required, schema: schema}
}

func orchestrationNullable(schema map[string]any) map[string]any {
	return oaObj("anyOf", []any{schema, oaObj("type", "null")})
}

func orchestrationClosedObject(properties map[string]any, required ...string) map[string]any {
	schema := oaObj("type", "object", "additionalProperties", false, "properties", properties)
	if len(required) > 0 {
		schema["required"] = oaEnum(required...)
	}
	return schema
}

func orchestrationCreateScheduleSchema() map[string]any {
	return orchestrationClosedObject(oaObj(
		"name", oaObj("type", "string", "minLength", 1, "description", "Must be non-empty; persistence byte-caps it."),
		"subject_kind", oaObj("type", "string", "enum", oaEnum("agent", "swarm")),
		"subject_ref", oaObj("type", "string", "minLength", 1),
		"trigger_kind", oaObj("type", "string", "enum", oaEnum("cron", "event", "manual")),
		"cadence_spec", orchestrationNullable(oaObj("type", "string")),
		"expected_interval_seconds", orchestrationNullable(oaObj(
			"type", "integer", "format", "int64",
			"description", "Zero disables cadence-miss checks; otherwise 60..31622400 and trigger_kind must be cron.",
		)),
		"grace_factor", orchestrationNullable(oaObj(
			"type", "integer", "format", "int64",
			"description", "Zero or omission selects the default of 2; otherwise the accepted range is 1..10.",
		)),
		"approval_ref", orchestrationNullable(oaObj("type", "string")),
	), "name", "subject_kind", "subject_ref", "trigger_kind")
}

func orchestrationPatchScheduleSchema() map[string]any {
	return orchestrationNullable(orchestrationClosedObject(oaObj(
		"desired_status", orchestrationNullable(oaObj("type", "string", "enum", oaEnum("active", "paused", "retired"))),
		"subject_ref", orchestrationNullable(oaObj("type", "string")),
		"cadence_spec", orchestrationNullable(oaObj("type", "string")),
		"expected_interval_seconds", orchestrationNullable(oaObj(
			"type", "integer", "format", "int64",
			"description", "Zero disables cadence-miss checks; otherwise the accepted range is 60..31622400.",
		)),
		"grace_factor", orchestrationNullable(oaObj("type", "integer", "format", "int64", "minimum", 1, "maximum", 10)),
		"approval_ref", orchestrationNullable(oaObj("type", "string")),
	)))
}

func orchestrationApprovalSchema(revisionRequired bool) map[string]any {
	properties := oaObj("approval_ref", orchestrationNullable(oaObj("type", "string")))
	if revisionRequired {
		properties["revision_id"] = oaObj("type", "string", "minLength", 1)
		return orchestrationClosedObject(properties, "revision_id")
	}
	return orchestrationNullable(orchestrationClosedObject(properties))
}

func orchestrationCreateWorkflowSchema() map[string]any {
	return orchestrationClosedObject(oaObj(
		"name", oaObj("type", "string", "minLength", 1, "description", "Must be non-empty; persistence byte-caps it."),
		"description", orchestrationNullable(oaObj("type", "string", "description", "Persistence byte-caps it to 2000 bytes.")),
		"enabled", orchestrationNullable(oaObj("type", "boolean", "description", "Defaults to true when omitted or null.")),
		"steps", orchestrationStepsSchema(),
	), "name")
}

func orchestrationPatchWorkflowSchema() map[string]any {
	return orchestrationNullable(orchestrationClosedObject(oaObj(
		"description", orchestrationNullable(oaObj("type", "string", "description", "An explicit empty string clears the stored description.")),
		"enabled", orchestrationNullable(oaObj("type", "boolean")),
	)))
}

func orchestrationPutStepsSchema() map[string]any {
	return orchestrationNullable(orchestrationClosedObject(oaObj("steps", orchestrationStepsSchema())))
}

func orchestrationStepsSchema() map[string]any {
	return orchestrationNullable(oaObj(
		"type", "array",
		"description", "The handler accepts an empty graph. Its configured step cap defaults to 50; refs must be unique, dependencies must exist, the graph must be acyclic, and fan-in/fan-out are capped at 8.",
		"items", oaObj("oneOf", orchestrationStepVariants()),
	))
}

func orchestrationStepVariants() []any {
	return []any{
		orchestrationStepVariant("schedule-fire", orchestrationSimpleRequiredString("schedule_id", "Must identify an existing non-retired schedule."), true),
		orchestrationStepVariant("eventing-emit", orchestrationSimpleRequiredString("label", "After trimming it must be non-empty and is byte-capped to 200."), true),
		orchestrationStepVariant("notify-test", orchestrationSimpleRequiredString("route_id", "Must identify an existing notification route."), true),
		orchestrationStepVariant("wait", orchestrationClosedObject(oaObj("seconds", oaObj("type", "integer", "format", "int64", "minimum", 1, "maximum", 86400)), "seconds"), true),
		orchestrationStepVariant("approval-gate", orchestrationNullable(orchestrationClosedObject(oaObj(
			"reason", orchestrationNullable(oaObj("type", "string", "description", "Byte-capped to 200.")),
		))), false),
		orchestrationStepVariant("work-create", orchestrationWorkCreateConfigSchema(), true),
		orchestrationStepVariant("work-assign", orchestrationWorkAssignConfigSchema(), true),
		orchestrationStepVariant("work-claim", orchestrationWorkClaimConfigSchema(), true),
		orchestrationStepVariant("session-launch", orchestrationSessionLaunchConfigSchema(), true),
		orchestrationStepVariant("work-message", orchestrationWorkMessageConfigSchema(), true),
		orchestrationStepVariant("work-wait-ack", orchestrationWorkWaitAckConfigSchema(), true),
		orchestrationStepVariant("work-handoff", orchestrationWorkHandoffConfigSchema(), true),
		orchestrationStepVariant("work-transition", orchestrationWorkTransitionConfigSchema(), true),
		orchestrationStepVariant("work-cancel", orchestrationWorkCancelConfigSchema(), true),
		orchestrationStepVariant("work-reconcile", orchestrationSimpleRequiredString("binding_id", "Must be a valid binding identifier."), true),
		orchestrationStepVariant("remote-plan", orchestrationRemotePlanConfigSchema(), true),
		orchestrationStepVariant("remote-test", orchestrationStepRefConfigSchema("plan_step_ref"), true),
		orchestrationStepVariant("remote-start", orchestrationStepRefConfigSchema("plan_step_ref"), true),
		orchestrationStepVariant("remote-observe", orchestrationRemoteBindingConfigSchema(), true),
		orchestrationStepVariant("remote-cancel", orchestrationRemoteCancelConfigSchema(), true),
	}
}

func orchestrationStepVariant(kind string, config map[string]any, configRequired bool) map[string]any {
	properties := oaObj(
		"ref", orchestrationStepRefSchema("Unique within the workflow."),
		"kind", oaObj("const", kind),
		"config", config,
		"depends_on", orchestrationNullable(oaObj(
			"type", "array", "maxItems", 8, "uniqueItems", true,
			"items", orchestrationStepRefSchema("Must name another step in this graph."),
		)),
	)
	required := []string{"ref", "kind"}
	if configRequired {
		required = append(required, "config")
	}
	schema := orchestrationClosedObject(properties, required...)
	schema["description"] = "config is strictly decoded, capped at 4096 JSON bytes, and canonicalized before storage."
	return schema
}

func orchestrationStepRefSchema(description string) map[string]any {
	return oaObj("type", "string", "pattern", "^[a-z0-9][a-z0-9_-]{0,63}$", "description", description)
}

func orchestrationSimpleRequiredString(name, description string) map[string]any {
	return orchestrationClosedObject(oaObj(name, oaObj("type", "string", "minLength", 1, "description", description)), name)
}

func orchestrationStepRefConfigSchema(name string) map[string]any {
	return orchestrationClosedObject(oaObj(name, orchestrationStepRefSchema("Must name the required upstream step.")), name)
}

func orchestrationIDSchema(description string) map[string]any {
	return oaObj("type", "string", "minLength", 1, "description", description)
}

func orchestrationOptionalString(description string) map[string]any {
	return orchestrationNullable(oaObj("type", "string", "description", description))
}

func orchestrationParticipantSchema() map[string]any {
	return orchestrationClosedObject(oaObj(
		"kind", oaObj("type", "string", "enum", oaEnum("user", "agent", "session")),
		"ref", orchestrationIDSchema("After trimming it must be non-empty and no longer than 512 bytes."),
	), "kind", "ref")
}

func orchestrationWorkSelectorProperties() map[string]any {
	return oaObj(
		"work_item_id", orchestrationOptionalString("A valid WorkItem identifier. Mutually exclusive with work_item_step_ref; omitting both selects the run root WorkItem."),
		"work_item_step_ref", orchestrationNullable(orchestrationStepRefSchema("A direct dependency that produces a WorkItem. Mutually exclusive with work_item_id.")),
	)
}

func orchestrationApplyWorkSelector(schema map[string]any) {
	nonBlank := oaObj("type", "string", "pattern", ".*\\S.*")
	schema["not"] = oaObj(
		"required", oaEnum("work_item_id", "work_item_step_ref"),
		"properties", oaObj("work_item_id", nonBlank, "work_item_step_ref", nonBlank),
	)
}

func orchestrationWorkCreateConfigSchema() map[string]any {
	criterion := orchestrationClosedObject(oaObj(
		"key", orchestrationStepRefSchema("Unique criterion key."),
		"ordinal", oaObj("type", "integer", "format", "int64", "minimum", 1, "description", "Must be unique within criteria."),
		"statement", oaObj("type", "string", "minLength", 1, "description", "After trimming it must be non-empty and no longer than 1024 bytes."),
		"required", orchestrationNullable(oaObj("type", "boolean")),
	), "key", "ordinal", "statement")
	provenance := orchestrationClosedObject(oaObj(
		"kind", oaObj("type", "string", "enum", oaEnum("human", "workflow", "a2a", "mcp", "migration", "system")),
		"ref", orchestrationIDSchema("After trimming it must be non-empty and no longer than 512 bytes."),
		"hash", orchestrationOptionalString("When non-empty, it is byte-capped to 128."),
	), "kind", "ref")
	schema := orchestrationClosedObject(oaObj(
		"workspace_id", orchestrationIDSchema("Must be a valid workspace identifier."),
		"work_kind", orchestrationStepRefSchema("Work kind slug."),
		"title", oaObj("type", "string", "minLength", 1, "description", "After trimming it must be non-empty and no longer than 256 bytes."),
		"brief_md", orchestrationOptionalString("Exactly one of brief_md or brief_ref must be non-empty; maximum 2048 bytes."),
		"brief_ref", orchestrationOptionalString("Exactly one of brief_md or brief_ref must be non-empty; maximum 512 bytes."),
		"priority", oaObj("type", "string", "enum", oaEnum("p0", "p1", "p2", "p3")),
		"owner", orchestrationParticipantSchema(),
		"criteria", oaObj(
			"type", "array", "minItems", 1, "maxItems", 16,
			"description", "Keys and ordinals must be unique and at least one criterion must set required=true.",
			"contains", oaObj("type", "object", "properties", oaObj("required", oaObj("const", true)), "required", oaEnum("required")),
			"items", criterion,
		),
		"provenance", provenance,
		"due_at", orchestrationOptionalString("When non-empty, must parse as a canonical timestamp."),
	), "workspace_id", "work_kind", "title", "priority", "owner", "criteria", "provenance")
	schema["oneOf"] = []any{
		oaObj("required", oaEnum("brief_md"), "properties", oaObj("brief_md", oaObj("type", "string", "minLength", 1))),
		oaObj("required", oaEnum("brief_ref"), "properties", oaObj("brief_ref", oaObj("type", "string", "minLength", 1))),
	}
	return schema
}

func orchestrationWorkAssignConfigSchema() map[string]any {
	properties := orchestrationWorkSelectorProperties()
	properties["expected_owner_epoch"] = oaObj("type", "integer", "format", "int64", "minimum", 1)
	properties["target"] = orchestrationParticipantSchema()
	properties["require_ack"] = orchestrationNullable(oaObj("type", "boolean"))
	properties["channel_id"] = orchestrationOptionalString("Required as a valid identifier when require_ack is true.")
	properties["context"] = orchestrationOptionalString("With require_ack=true, exactly one of context or context_ref is required; maximum 2048 bytes.")
	properties["context_ref"] = orchestrationOptionalString("With require_ack=true, exactly one of context or context_ref is required; maximum 512 bytes.")
	properties["ack_deadline"] = orchestrationOptionalString("Required as a canonical timestamp when require_ack is true.")
	schema := orchestrationClosedObject(properties, "expected_owner_epoch", "target")
	orchestrationApplyWorkSelector(schema)
	schema["description"] = "When require_ack is false or absent, channel_id, context, context_ref, and ack_deadline must all be empty."
	return schema
}

func orchestrationWorkClaimConfigSchema() map[string]any {
	properties := orchestrationWorkSelectorProperties()
	properties["sid"] = orchestrationIDSchema("After trimming it must be non-empty and no longer than 512 bytes.")
	properties["ttl_seconds"] = oaObj("type", "integer", "format", "int64", "minimum", 1, "maximum", 86400)
	schema := orchestrationClosedObject(properties, "sid", "ttl_seconds")
	orchestrationApplyWorkSelector(schema)
	return schema
}

func orchestrationSessionLaunchConfigSchema() map[string]any {
	properties := orchestrationWorkSelectorProperties()
	properties["owner_epoch"] = orchestrationNullable(oaObj("type", "integer", "format", "int64", "minimum", 0))
	properties["fence"] = orchestrationNullable(oaObj("type", "integer", "format", "int64", "minimum", 0, "description", "A positive literal fence is mutually exclusive with fence_step_ref."))
	properties["fence_step_ref"] = orchestrationNullable(orchestrationStepRefSchema("Mutually exclusive with a positive literal fence."))
	properties["runtime_profile_ref"] = orchestrationIDSchema("After trimming it must be non-empty and no longer than 512 bytes.")
	properties["attempt_kind"] = orchestrationNullable(orchestrationStepRefSchema("Defaults to lease-bind when omitted or empty."))
	schema := orchestrationClosedObject(properties, "runtime_profile_ref")
	orchestrationApplyWorkSelector(schema)
	return schema
}

func orchestrationWorkMessageConfigSchema() map[string]any {
	properties := orchestrationWorkSelectorProperties()
	properties["channel_id"] = orchestrationIDSchema("Must be a valid channel identifier.")
	properties["recipient"] = orchestrationParticipantSchema()
	properties["body"] = orchestrationOptionalString("Exactly one of body or body_ref must be non-empty; maximum 2048 bytes.")
	properties["body_ref"] = orchestrationOptionalString("Exactly one of body or body_ref must be non-empty; maximum 512 bytes.")
	properties["ack_due_at"] = orchestrationOptionalString("When non-empty, must parse as a canonical timestamp.")
	properties["urgency"] = orchestrationNullable(oaObj("type", "string", "enum", oaEnum("normal", "high", "critical")))
	schema := orchestrationClosedObject(properties, "channel_id", "recipient")
	orchestrationApplyWorkSelector(schema)
	schema["oneOf"] = []any{
		oaObj("required", oaEnum("body"), "properties", oaObj("body", oaObj("type", "string", "minLength", 1))),
		oaObj("required", oaEnum("body_ref"), "properties", oaObj("body_ref", oaObj("type", "string", "minLength", 1))),
	}
	return schema
}

func orchestrationWorkWaitAckConfigSchema() map[string]any {
	schema := orchestrationClosedObject(oaObj(
		"target_kind", oaObj("type", "string", "enum", oaEnum("message", "handoff")),
		"target_id", orchestrationOptionalString("Exactly one of target_id or target_step_ref; target_id must be a valid identifier."),
		"target_step_ref", orchestrationNullable(orchestrationStepRefSchema("Exactly one of target_id or target_step_ref.")),
		"deadline", orchestrationIDSchema("Must parse as a canonical timestamp."),
		"after_event_seq", orchestrationNullable(oaObj("type", "integer", "format", "int64", "minimum", 0)),
	), "target_kind", "deadline")
	schema["oneOf"] = orchestrationExactlyOneNonBlank("target_id", "target_step_ref")
	return schema
}

func orchestrationWorkHandoffConfigSchema() map[string]any {
	properties := orchestrationWorkSelectorProperties()
	properties["channel_id"] = orchestrationIDSchema("Must be a valid channel identifier.")
	properties["target"] = orchestrationParticipantSchema()
	properties["context"] = orchestrationOptionalString("Exactly one of context or context_ref must be non-empty; maximum 2048 bytes.")
	properties["context_ref"] = orchestrationOptionalString("Exactly one of context or context_ref must be non-empty; maximum 512 bytes.")
	properties["ack_deadline"] = orchestrationIDSchema("Must parse as a canonical timestamp.")
	schema := orchestrationClosedObject(properties, "channel_id", "target", "ack_deadline")
	orchestrationApplyWorkSelector(schema)
	schema["oneOf"] = []any{
		oaObj("required", oaEnum("context"), "properties", oaObj("context", oaObj("type", "string", "minLength", 1))),
		oaObj("required", oaEnum("context_ref"), "properties", oaObj("context_ref", oaObj("type", "string", "minLength", 1))),
	}
	return schema
}

func orchestrationWorkTransitionConfigSchema() map[string]any {
	properties := orchestrationWorkSelectorProperties()
	properties["target_state"] = oaObj("type", "string", "enum", oaEnum("ready", "blocked", "review", "completed", "failed", "canceled"))
	properties["evidence_ref"] = orchestrationOptionalString("When non-empty, maximum 512 bytes.")
	properties["reason"] = orchestrationOptionalString("Required for blocked, failed, or canceled; when non-empty, maximum 2048 bytes.")
	schema := orchestrationClosedObject(properties, "target_state")
	orchestrationApplyWorkSelector(schema)
	return schema
}

func orchestrationWorkCancelConfigSchema() map[string]any {
	properties := orchestrationWorkSelectorProperties()
	properties["binding_id"] = orchestrationOptionalString("When non-empty, must be a valid binding identifier.")
	properties["reason"] = orchestrationIDSchema("After trimming it must be non-empty and no longer than 2048 bytes.")
	schema := orchestrationClosedObject(properties, "reason")
	orchestrationApplyWorkSelector(schema)
	return schema
}

func orchestrationRemotePlanConfigSchema() map[string]any {
	properties := orchestrationWorkSelectorProperties()
	properties["workspace_id"] = orchestrationIDSchema("Must be a valid workspace identifier.")
	properties["binding_spec_id"] = orchestrationIDSchema("Must be a valid binding-spec identifier.")
	properties["binding_spec_generation"] = oaObj("type", "integer", "format", "int64", "minimum", 1)
	properties["protocol"] = oaObj("type", "string", "enum", oaEnum("a2a", "mcp"))
	properties["protocol_version"] = orchestrationIDSchema("After trimming it must be non-empty and no longer than 64 bytes.")
	properties["authority"] = orchestrationIDSchema("After trimming it must be non-empty and no longer than 512 bytes.")
	properties["agent_ref"] = orchestrationIDSchema("After trimming it must be non-empty and no longer than 512 bytes.")
	properties["skill"] = orchestrationIDSchema("After trimming it must be non-empty and no longer than 512 bytes.")
	properties["scope"] = orchestrationIDSchema("After trimming it must be non-empty and no longer than 512 bytes.")
	properties["owner_epoch"] = oaObj("type", "integer", "format", "int64", "minimum", 1)
	properties["lease_fence"] = oaObj("type", "integer", "format", "int64", "minimum", 1)
	properties["brief_hash"] = orchestrationIDSchema("After trimming it must be non-empty and no longer than 128 bytes.")
	properties["criteria_revision"] = oaObj("type", "integer", "format", "int64", "minimum", 1)
	schema := orchestrationClosedObject(properties,
		"workspace_id", "binding_spec_id", "binding_spec_generation", "protocol", "protocol_version",
		"authority", "agent_ref", "skill", "scope", "owner_epoch", "lease_fence", "brief_hash", "criteria_revision",
	)
	orchestrationApplyWorkSelector(schema)
	return schema
}

func orchestrationRemoteBindingConfigSchema() map[string]any {
	schema := orchestrationClosedObject(oaObj(
		"binding_id", orchestrationOptionalString("Exactly one of binding_id or binding_step_ref; binding_id must be a valid identifier."),
		"binding_step_ref", orchestrationNullable(orchestrationStepRefSchema("Exactly one of binding_id or binding_step_ref.")),
	))
	schema["oneOf"] = orchestrationExactlyOneNonBlank("binding_id", "binding_step_ref")
	return schema
}

func orchestrationRemoteCancelConfigSchema() map[string]any {
	properties := orchestrationWorkSelectorProperties()
	properties["binding_id"] = orchestrationOptionalString("Exactly one of binding_id or binding_step_ref; binding_id must be a valid identifier.")
	properties["binding_step_ref"] = orchestrationNullable(orchestrationStepRefSchema("Exactly one of binding_id or binding_step_ref."))
	properties["reason"] = orchestrationIDSchema("After trimming it must be non-empty and no longer than 2048 bytes.")
	schema := orchestrationClosedObject(properties, "reason")
	orchestrationApplyWorkSelector(schema)
	schema["oneOf"] = orchestrationExactlyOneNonBlank("binding_id", "binding_step_ref")
	return schema
}

func orchestrationExactlyOneNonBlank(first, second string) []any {
	nonBlank := func(name string) map[string]any {
		return oaObj(
			"required", oaEnum(name),
			"properties", oaObj(name, oaObj("type", "string", "pattern", ".*\\S.*")),
		)
	}
	return []any{nonBlank(first), nonBlank(second)}
}
