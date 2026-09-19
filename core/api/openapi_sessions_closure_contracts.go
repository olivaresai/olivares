// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

type sessionsClosureRequestBodyKind uint8

const (
	sessionsClosureBodyless sessionsClosureRequestBodyKind = iota + 1
	sessionsClosureBodyful
	sessionsClosureBodyNoDerivable
	sessionsClosureBodyPending
)

type sessionsClosureRequestBodyDeclaration struct {
	kind     sessionsClosureRequestBodyKind
	required bool
	schema   map[string]any
}

func sessionsClosureRequestBody(r moduleRoute) (map[string]any, bool) {
	decl, ok := sessionsClosureRequestBodyDeclarationFor(r)
	if !ok || decl.kind != sessionsClosureBodyful {
		return nil, false
	}
	return oaObj(
		"required", decl.required,
		"content", oaObj("application/json", oaObj("schema", decl.schema)),
	), true
}

func sessionsClosureRequestBodyDeclarationFor(r moduleRoute) (sessionsClosureRequestBodyDeclaration, bool) {
	if r.ns != "sessions" {
		return sessionsClosureRequestBodyDeclaration{}, false
	}
	switch r.method + " " + r.pattern {
	case http.MethodPost + " /channels":
		return sessionsClosureBodyDeclaration(true, sessionsCreateChannelSchema()), true
	case http.MethodPatch + " /channels":
		return sessionsClosureBodyDeclaration(true, sessionsUpdateChannelSchema()), true
	case http.MethodPost + " /channels/{id}/grants":
		return sessionsClosureBodyDeclaration(true, sessionsChannelGrantSchema()), true
	case http.MethodPost + " /messages/send":
		return sessionsClosureBodyDeclaration(true, sessionsMessageSendSchema()), true
	case http.MethodPut + " /inbox/cursors/personal/{recipient}":
		return sessionsClosureBodyDeclaration(true, sessionsCursorAdvanceSchema()), true
	case http.MethodPost + " /handoffs":
		return sessionsClosureBodyDeclaration(true, sessionsHandoffOfferSchema()), true
	case http.MethodPost + " /handoffs/{id}/responses":
		return sessionsClosureBodyDeclaration(true, sessionsHandoffResponseSchema()), true
	case http.MethodPost + " /runs":
		return sessionsClosureBodyDeclaration(true, sessionsCreateRunSchema()), true
	case http.MethodPost + " /templates":
		return sessionsClosureBodyDeclaration(true, sessionsCreateTemplateSchema()), true
	case http.MethodPut + " /templates/{id}":
		return sessionsClosureBodyDeclaration(true, sessionsUpdateTemplateSchema()), true
	case http.MethodPost + " /templates/{id}/duplicate":
		return sessionsClosureBodyDeclaration(true, sessionsDuplicateTemplateSchema()), true
	case http.MethodPost + " /templates/{id}/apply":
		return sessionsClosureBodyDeclaration(false, sessionsApplyTemplateSchema()), true
	case http.MethodPost + " /workspaces":
		return sessionsClosureBodyDeclaration(true, sessionsCreateWorkspaceSchema()), true
	case http.MethodPost + " /workspaces/{ref}/files/move":
		return sessionsClosureBodyDeclaration(true, sessionsMoveFileSchema()), true
	case http.MethodPost + " /provider-profiles":
		return sessionsClosureBodyDeclaration(true, sessionsCreateProviderProfileSchema()), true
	case http.MethodPatch + " /provider-profiles/{ref}":
		return sessionsClosureBodyDeclaration(true, sessionsPatchProviderProfileSchema()), true
	case http.MethodPost + " /provider-source-bindings":
		return sessionsClosureBodyDeclaration(true, sessionsCreateProviderBindingSchema()), true
	case http.MethodPost + " /providers":
		return sessionsClosureBodyDeclaration(true, sessionsCreateProviderSchema()), true
	case http.MethodPatch + " /providers/{ref}":
		return sessionsClosureBodyDeclaration(true, sessionsPatchProviderSchema()), true
	case http.MethodPost + " /provider-profiles/{ref}/retire",
		http.MethodPost + " /provider-source-bindings/{ref}/revoke",
		// The connection TEST is bodyless on purpose: everything it needs —
		// which provider, which endpoint, which credential — is already the stored
		// record, and a body would be a second place to say it. The revoke is
		// bodyless for the reason its siblings are: the reference IS the request.
		http.MethodPost + " /providers/{ref}/test",
		http.MethodPost + " /providers/{ref}/revoke":
		return sessionsClosureRequestBodyDeclaration{kind: sessionsClosureBodyless}, true
	// ⛔ /runs/{ref}/interrupt IS NOT IN THIS LIST ANY MORE, and the omission is the
	// point: it now accepts an OPTIONAL fenced body, declared beside its two sibling
	// run controls (/input, /stop) in openapi_modules.go. Leaving it here would not
	// have failed — sessionsClosureRequestBody returns nothing for a bodyless
	// declaration and the sibling switch would still have published the schema — so
	// the census would have kept asserting "bodyless" about a route with a body,
	// which is the kind of true-looking contradiction this file exists to prevent.
	case http.MethodDelete + " /runs/{ref}",
		http.MethodPost + " /runs/{ref}/cleanup",
		http.MethodPost + " /runs/{ref}/resume",
		http.MethodDelete + " /templates/{id}",
		http.MethodDelete + " /workspaces/{ref}",
		http.MethodDelete + " /workspaces/{ref}/files",
		http.MethodPost + " /workspaces/{ref}/files/dir",
		http.MethodPost + " /channels/{id}/grants/{grant_id}/revoke",
		http.MethodPost + " /deliveries/{id}/ack":
		return sessionsClosureRequestBodyDeclaration{kind: sessionsClosureBodyless}, true
	default:
		return sessionsClosureRequestBodyDeclaration{}, false
	}
}

func sessionsClosureBodyDeclaration(required bool, schema map[string]any) sessionsClosureRequestBodyDeclaration {
	return sessionsClosureRequestBodyDeclaration{kind: sessionsClosureBodyful, required: required, schema: schema}
}

func sessionsClosureNullable(schema map[string]any) map[string]any {
	return oaObj("anyOf", []any{schema, oaObj("type", "null")})
}

func sessionsClosureClosedObject(properties map[string]any, required ...string) map[string]any {
	schema := oaObj("type", "object", "additionalProperties", false, "properties", properties)
	if len(required) > 0 {
		schema["required"] = oaEnum(required...)
	}
	return schema
}

func sessionsClosureOptionalString(description string) map[string]any {
	return sessionsClosureNullable(oaObj("type", "string", "description", description))
}

func sessionsCreateRunSchema() map[string]any {
	envName := oaObj(
		"type", "string",
		"description", "After trimming, must be a 1..128-byte shell name ([A-Za-z_][A-Za-z0-9_]*). Names beginning OLIVARES_, ANTHROPIC_, or CLAUDE_CODE_, plus DISABLE_AUTOUPDATER, are reserved.",
	)
	return sessionsClosureNullable(sessionsClosureClosedObject(oaObj(
		"name", sessionsClosureOptionalString("Trimmed before persistence."),
		"transport", sessionsClosureNullable(oaObj("type", "string", "enum", oaEnum("", "stream-json", "remote-control"), "description", "Empty/null defaults to stream-json.")),
		"permission_mode", sessionsClosureNullable(oaObj("type", "string", "enum", oaEnum("", "default", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions"), "description", "Empty/null defaults to default.")),
		"effort", sessionsClosureNullable(oaObj("type", "string", "enum", oaEnum("", "low", "medium", "high", "xhigh", "max"))),
		"model", sessionsClosureOptionalString("Trimmed before launch."),
		"workspace_ref", sessionsClosureOptionalString("Trimmed and, when non-empty, resolved to a registered workspace."),
		"template_id", sessionsClosureOptionalString("When non-empty, identifies the stored template whose terms govern the launch."),
		"isolation", sessionsClosureNullable(oaObj("type", "string", "enum", oaEnum("", "native", "container", "sandbox"), "description", "Empty/null defaults to native; unavailable runners refuse rather than downgrade.")),
		"env_allow", sessionsClosureNullable(oaObj("type", "array", "maxItems", 64, "uniqueItems", true, "items", envName)),
		"provider_profile_ref", sessionsClosureOptionalString("When non-empty, names the provider profile the session launches under; the server resolves its homes and refuses a disabled, retired, foreign-environment or non-operable profile, and one whose driver requires an authorized authentication source it does not have. Only the reference is accepted: no home, environment, driver, binding, session id or authentication source. Refused (422) while profiled launches are not enabled on the deployment."),
	)))
}

func sessionsTemplateBodySchema() map[string]any {
	hookEntry := sessionsClosureNullable(sessionsClosureClosedObject(oaObj(
		"command", sessionsClosureOptionalString("Hook command; the template authoring handler stores it without semantic validation."),
		"timeout_ms", sessionsClosureNullable(oaObj("type", "integer")),
	)))
	hookEntries := sessionsClosureNullable(oaObj("type", "array", "items", hookEntry))
	hooks := sessionsClosureNullable(sessionsClosureClosedObject(oaObj(
		"pre_tool", hookEntries,
		"post_tool", hookEntries,
		"pre_session", hookEntries,
		"post_session", hookEntries,
	)))
	settings := sessionsClosureNullable(sessionsClosureClosedObject(oaObj(
		"permission_mode", sessionsClosureOptionalString("Validated when the template is reduced for preview or launch."),
		"effort", sessionsClosureOptionalString("Validated when the template is reduced for preview or launch."),
		"model", sessionsClosureOptionalString("Stored template model selector."),
		"custom_instructions", sessionsClosureOptionalString("Stored custom instructions."),
	)))
	stringList := sessionsClosureNullable(oaObj(
		"type", "array", "items", sessionsClosureNullable(oaObj("type", "string")),
	))
	policies := sessionsClosureNullable(sessionsClosureClosedObject(oaObj(
		"dlp_mode", sessionsClosureOptionalString("Validated when the template is reduced for preview or launch."),
		"max_session_duration_minutes", sessionsClosureNullable(oaObj("type", "integer")),
		"allowed_tools", stringList,
		"record_io", sessionsClosureNullable(oaObj("type", "boolean")),
	)))
	return sessionsClosureNullable(sessionsClosureClosedObject(oaObj(
		"hooks", hooks,
		"settings", settings,
		"connectors", stringList,
		"policies", policies,
	)))
}

func sessionsCreateTemplateSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"name", oaObj("type", "string", "minLength", 1),
		"description", sessionsClosureOptionalString("Stored template description."),
		"body", sessionsTemplateBodySchema(),
	), "name")
}

func sessionsUpdateTemplateSchema() map[string]any {
	return sessionsClosureNullable(sessionsClosureClosedObject(oaObj(
		"name", sessionsClosureOptionalString("An explicit string, including empty, replaces the stored name; null/omission preserves it."),
		"description", sessionsClosureOptionalString("An explicit string replaces the stored description; null/omission preserves it."),
		"body", sessionsTemplateBodySchema(),
	)))
}

func sessionsDuplicateTemplateSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"name", oaObj("type", "string", "minLength", 1, "description", "Name for the duplicate; whitespace is accepted because the handler checks exact emptiness."),
	), "name")
}

func sessionsApplyTemplateSchema() map[string]any {
	stringList := sessionsClosureNullable(oaObj(
		"type", "array", "items", sessionsClosureNullable(oaObj("type", "string")),
	))
	target := sessionsClosureNullable(sessionsClosureClosedObject(oaObj(
		"transport", sessionsClosureOptionalString("Preview transport; unenforceable template terms are reported rather than silently dropped."),
		"permission_mode", sessionsClosureOptionalString("Proposed launch permission mode."),
		"effort", sessionsClosureOptionalString("Proposed launch effort."),
		"model", sessionsClosureOptionalString("Proposed launch model."),
		"allowed_tools", stringList,
		"custom_instructions", sessionsClosureOptionalString("Proposed custom instructions."),
		"record_io", sessionsClosureNullable(oaObj("type", "boolean")),
		"max_session_duration_minutes", sessionsClosureNullable(oaObj("type", "integer", "format", "int64")),
		"workspace_ref", sessionsClosureOptionalString("Proposed workspace reference."),
	)))
	return sessionsClosureNullable(sessionsClosureClosedObject(oaObj("target", target)))
}

func sessionsCreateWorkspaceSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"name", sessionsClosureOptionalString("Trimmed workspace display name."),
		"root_path", oaObj("type", "string", "description", "After trimming, must be an absolute, existing directory; the server stores its canonical real path."),
		"mount_mode", sessionsClosureNullable(oaObj("type", "string", "enum", oaEnum("", "rw", "ro"), "description", "Empty/null defaults to rw.")),
		"container_target", sessionsClosureOptionalString("Must be absolute after trimming; empty/null selects the default container target."),
		"allow_subpaths", sessionsClosureNullable(oaObj(
			"type", "array", "description", "Relative, NUL-free paths that cannot escape the workspace root.",
			"items", sessionsClosureNullable(oaObj("type", "string")),
		)),
		"max_read_bytes", sessionsClosureNullable(oaObj("type", "integer", "format", "int64", "minimum", 0)),
		"dlp_mode", sessionsClosureNullable(oaObj("type", "string", "enum", oaEnum("", "label", "deny", "off"), "description", "Empty/null defaults to label.")),
	), "root_path")
}

func sessionsMoveFileSchema() map[string]any {
	path := oaObj(
		"type", "string", "pattern", ".*\\S.*",
		"description", "Relative, NUL-free path jailed inside the workspace; it cannot resolve to the workspace root.",
	)
	return sessionsClosureClosedObject(oaObj("from", path, "to", path), "from", "to")
}

// sessionsCreateProviderProfileSchema mirrors modules/sessions createProfileRequest
// (strict decoder: unknown keys are rejected). The homes are validated on the
// server — absolute, canonicalized, existing directories on this node's execution
// environment — and never created as a fallback. No credential value is accepted.
func sessionsCreateProviderProfileSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"driver", oaObj("type", "string", "minLength", 1, "description", "Provider driver key, lower-cased ([a-z0-9][a-z0-9._-]*). Open set; only drivers with an operated runner on this node are launchable."),
		"config_home", oaObj("type", "string", "minLength", 1, "description", "Absolute path of the provider configuration directory on this node; symlinks are resolved and the canonical path is stored."),
		"user_home", oaObj("type", "string", "minLength", 1, "description", "Absolute path used as HOME for the launched process; validated like config_home."),
		"display_name", sessionsClosureOptionalString("Editable label; trimmed, at most 200 bytes, no control characters."),
		"environment_ref", sessionsClosureOptionalString("Optional; when present must equal this node's execution environment (a foreign environment cannot have its homes validated here)."),
		"auth_source", sessionsClosureNullable(oaObj("type", "string", "enum", oaEnum("", "provider_account_home", "managed_injection"), "description", "Authorized authentication source for this profile: provider_account_home (the child uses the saved login inside its own homes) or managed_injection (a provider-compatible credential minted by that driver's governed adapter). They are distinct authorizations with no fallback between them; empty authorizes neither and refuses every launch whose driver requires one.")),
		"provider_record_ref", sessionsClosureOptionalString("Optional: the registered provider whose credential this profile's managed launches use. Empty leaves the host's own credential variables in charge, which is every profile's behaviour before v26.10. The engine refuses a credential whose kind this driver cannot read."),
	), "driver", "config_home", "user_home")
}

// sessionsPatchProviderProfileSchema mirrors patchProfileRequest: a label and/or an
// active↔disabled transition. Retirement has its own admin route; driver, environment
// and homes are immutable and are not keys of this body.
func sessionsPatchProviderProfileSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"display_name", sessionsClosureOptionalString("An explicit string replaces the stored label; null/omission preserves it."),
		"state", sessionsClosureNullable(oaObj("type", "string", "enum", oaEnum("active", "disabled"), "description", "Reversible lifecycle transition; retired is refused here.")),
		"auth_source", sessionsClosureNullable(oaObj("type", "string", "enum", oaEnum("", "provider_account_home", "managed_injection"), "description", "Re-authorizes (or, with the empty string, withdraws) the authentication source. It is not identity, so it does not create a new profile id; a live child keeps the source its own launch was authorized under.")),
		"provider_record_ref", sessionsClosureOptionalString("Binds (or, with the empty string, unbinds) the registered provider this profile's managed launches use. Like auth_source it is an authorization and not identity, and it does not reach a live child: a running session keeps the provider its own launch resolved."),
	))
}

// sessionsCreateProviderSchema mirrors createProviderRecordRequest. api_key is
// the ONLY field that carries a credential; it travels in this body and appears in no
// response, no log line and no row — the record stores a locator and a four-character
// hint. base_url is required for openai_compatible, which has no official endpoint to
// assume; the engine enforces that and this description says so rather than trying to
// express a conditional requirement in the schema.
func sessionsCreateProviderSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"kind", oaObj("type", "string", "enum", oaEnum("anthropic", "openai", "xai", "openai_compatible"), "description", "What the credential IS, independent of which CLI reads it. The set is closed because a kind decides which environment variables a launched child receives."),
		"display_name", oaObj("type", "string", "minLength", 1, "description", "Your own name for this credential; it is what a picker shows. Unique among active providers of the same kind, case-folded."),
		"base_url", sessionsClosureOptionalString("Absolute https endpoint override. Empty uses the provider's official endpoint; REQUIRED for openai_compatible. A URL carrying userinfo is refused: a credential must not travel in a field that is published in every read."),
		"api_key", oaObj("type", "string", "minLength", 8, "description", "The credential. Sealed at rest by the engine and never returned by any read, including immediately after this write. Lose it and rotate; there is no read that recovers it."),
	), "kind", "display_name", "api_key")
}

// sessionsPatchProviderSchema mirrors patchProviderRecordRequest: rename,
// re-endpoint and/or ROTATE. `kind` is absent because a record's kind decides what is
// injected into a child, so changing it under an existing binding would redefine every
// launch that binding authorizes — registering another record is the honest way.
func sessionsPatchProviderSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"display_name", sessionsClosureOptionalString("An explicit string replaces the stored name; null/omission preserves it."),
		"base_url", sessionsClosureOptionalString("An explicit string replaces the stored endpoint; null/omission preserves it."),
		"api_key", sessionsClosureNullable(oaObj("type", "string", "minLength", 8, "description", "Rotates the credential in place: it reseals under the SAME reference, so every profile bound to this provider keeps working and the NEXT launch uses the new value — a session already running keeps the one it started with. The previous connection test is cleared, because a verdict measured on a credential that no longer exists is not evidence about the one replacing it.")),
	))
}

// sessionsCreateProviderBindingSchema mirrors createBindingRequest: the source's
// persistent id, the EXACT revision this node has applied, and the profile.
func sessionsCreateProviderBindingSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"source_id", oaObj("type", "string", "minLength", 1, "description", "Persistent id of the durable source roster row (not its editable name)."),
		"source_revision", oaObj("type", "integer", "minimum", 1, "description", "The revision this node's runtime has successfully applied; any other value is refused as stale."),
		"profile_ref", oaObj("type", "string", "minLength", 1, "description", "The provider profile the source is dedicated to."),
	), "source_id", "source_revision", "profile_ref")
}

func sessionsCommunicationRefSchema(kinds ...string) map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"kind", oaObj("type", "string", "enum", oaEnum(kinds...)),
		"ref", oaObj("type", "string", "minLength", 1, "maxLength", 1024),
	), "kind", "ref")
}

func sessionsContentReferenceSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"kind", oaObj("type", "string", "minLength", 1),
		"ref", oaObj("type", "string", "minLength", 1),
		"hash", oaObj("type", "string"),
	), "kind", "ref")
}

func sessionsMessageContentSchema() map[string]any {
	block := sessionsClosureClosedObject(oaObj(
		"type", oaObj("type", "string", "enum", oaEnum("text", "reference", "status", "action_ref")),
		"format", oaObj("type", "string", "enum", oaEnum("", "plain", "markdown")),
		"text", oaObj("type", "string"),
		"reference", sessionsClosureNullable(sessionsContentReferenceSchema()),
		"code", oaObj("type", "string"),
	), "type")
	return sessionsClosureClosedObject(oaObj(
		"subject", oaObj("type", "string"),
		"blocks", oaObj("type", "array", "minItems", 1, "maxItems", 64, "items", block),
	), "subject", "blocks")
}

func sessionsChannelGrantInputSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"subject", sessionsCommunicationRefSchema("user", "user_group", "agent", "agent_group", "session"),
		"can_read", oaObj("type", "boolean"),
		"can_write", oaObj("type", "boolean"),
		"can_admin", oaObj("type", "boolean"),
		"expires_at", sessionsClosureNullable(oaObj("type", "string", "format", "date-time")),
	), "subject", "can_read", "can_write", "can_admin")
}

func sessionsCreateChannelSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"workspace_id", oaProtocolBindingIDSchema(),
		"slug", oaObj("type", "string", "minLength", 1),
		"name", oaObj("type", "string", "minLength", 1),
		"description", oaObj("type", "string"),
		"kind", oaObj("type", "string", "enum", oaEnum("coordination", "work", "incident", "announcement", "private")),
		"sensitivity", oaObj("type", "string", "enum", oaEnum("internal", "restricted")),
		"content_protection", oaObj("type", "string", "enum", oaEnum("storage", "application_sealed")),
		"default_ack_policy", oaObj("type", "string", "enum", oaEnum("none", "each_required", "quorum")),
		"default_ack_timeout_ms", oaObj("type", "integer", "format", "int64", "minimum", 0),
		"default_wake", oaObj("type", "string", "enum", oaEnum("none", "primary", "all", "inherit")),
		"retention_policy_ref", oaObj("type", "string"),
		"max_fanout", oaObj("type", "integer", "format", "int64", "minimum", 1),
		"max_automation_depth", oaObj("type", "integer", "format", "int64", "minimum", 0),
		"initial_grants", oaObj("type", "array", "minItems", 1, "maxItems", 64, "items", sessionsChannelGrantInputSchema()),
	), "workspace_id", "slug", "name", "initial_grants")
}

func sessionsUpdateChannelSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"channel_id", oaObj("type", "string", "format", "uuid"),
		"name", oaObj("type", "string"), "description", oaObj("type", "string"),
		"state", oaObj("type", "string", "enum", oaEnum("active", "archived")),
		"sensitivity", oaObj("type", "string", "enum", oaEnum("internal", "restricted")),
		"content_protection", oaObj("type", "string", "enum", oaEnum("storage", "application_sealed")),
		"default_ack_policy", oaObj("type", "string", "enum", oaEnum("none", "each_required", "quorum")),
		"default_ack_timeout_ms", oaObj("type", "integer", "format", "int64"),
		"default_wake", oaObj("type", "string", "enum", oaEnum("none", "primary", "all", "inherit")),
		"retention_policy_ref", oaObj("type", "string"),
		"max_fanout", oaObj("type", "integer", "format", "int64"),
		"max_automation_depth", oaObj("type", "integer", "format", "int64"),
	), "channel_id")
}

func sessionsChannelGrantSchema() map[string]any { return sessionsChannelGrantInputSchema() }

func sessionsMessageSendSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"channel_id", oaObj("type", "string", "format", "uuid"),
		"recipient", sessionsCommunicationRefSchema("user", "agent", "session"),
		"content", sessionsMessageContentSchema(),
		"urgency", oaObj("type", "string", "enum", oaEnum("", "normal", "high", "critical")),
		"available_at", sessionsTimestampSchema(),
	), "channel_id", "recipient", "content")
}

func sessionsCursorAdvanceSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"cursor", oaObj("type", "string", "minLength", 1, "maxLength", 8192),
		"delivery_id", oaObj("type", "string", "format", "uuid",
			"description", "Exact target Delivery named by the opaque cursor token; used for stored-workspace route authorization."),
	), "cursor", "delivery_id")
}

func sessionsHandoffContentSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"summary", oaObj("type", "string", "minLength", 1),
		"next_action", oaObj("type", "string", "minLength", 1),
		"risk", oaObj("type", "string"),
		"artifact_refs", oaObj("type", "array", "maxItems", 64, "items", sessionsContentReferenceSchema()),
	), "summary", "next_action")
}

func sessionsHandoffOfferSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"channel_id", oaObj("type", "string", "format", "uuid"),
		"work_item_id", oaObj("type", "string", "format", "uuid"),
		"recipient", sessionsCommunicationRefSchema("user", "agent", "session"),
		"handoff", sessionsHandoffContentSchema(),
		"ack_deadline", oaObj("type", "string", "format", "date-time"),
		"expected_owner_epoch", oaObj("type", "integer", "format", "int64", "minimum", 0),
	), "channel_id", "work_item_id", "recipient", "handoff", "ack_deadline")
}

// sessionsReasonSchema is the single published shape of a communication reason.
// The handoff response body and the terminal reason a recipient reads back are
// the SAME content slot, so they share one schema instead of two literals that
// can drift.
func sessionsReasonSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"code", oaObj("type", "string", "minLength", 1),
		"text", oaObj("type", "string"),
		"references", oaObj("type", "array", "maxItems", 64, "items", sessionsContentReferenceSchema()),
	), "code")
}

func sessionsHandoffResponseSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"transition", oaObj("type", "string", "enum", oaEnum("accept", "reject")),
		"reason", sessionsClosureNullable(sessionsReasonSchema()),
	), "transition")
}
