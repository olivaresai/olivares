// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

// The published contract of the launch-readiness READ.
//
// It is declared with a TYPED response schema and a closed query rather than
// left to the generic module envelope, and that is the whole reason this file
// exists: a console that has to re-derive "which states exist", "which causes
// exist" or "which of these fields is a verdict and which is a pending check"
// from prose ends up re-implementing the server's rules in TypeScript. The
// enums below ARE the server's rules, so the generated client types them and
// the browser cannot invent a tenth state or promote an unknown to a ready.
//
// ⛔ THE ENUMS ARE CLOSED ON PURPOSE AND MUST TRACK modules/sessions. The
// module's own contract battery asserts that every state, check, code and
// remediation it can emit appears here; a new cause that skipped this file
// would be published as an unconstrained string and silently accepted by every
// generated client.

func sessionsLaunchReadinessRoute(r moduleRoute) bool {
	return r.ns == "sessions" && r.method == http.MethodGet &&
		r.pattern == "/provider-profiles/{ref}/launch-readiness"
}

// sessionsLaunchReadinessParameters publishes the CLOSED query. Both parameters
// are optional with a declared default, and an undeclared or repeated one is a
// 400 rather than something the server ignores.
func sessionsLaunchReadinessParameters() []any {
	return []any{
		oaParam("transport", "query",
			"Transport the requirements are evaluated for; defaults to stream-json. A known value the selected driver does not offer is answered 200 with an unsupported check, not 400.",
			false, oaObj("type", "string", "enum", oaEnum("stream-json", "remote-control"), "default", "stream-json")),
		oaParam("isolation", "query",
			"Containment posture the requirements are evaluated for; defaults to native. container and sandbox are accepted values that the native runner reports as unsupported.",
			false, oaObj("type", "string", "enum", oaEnum("native", "container", "sandbox"), "default", "native")),
	}
}

// sessionsLaunchReadinessResponses publishes every status this read can answer.
// The 200 is the observation; it is NOT a success verdict about a launch, and
// the schema is shaped so a client cannot read it as one.
func sessionsLaunchReadinessResponses() map[string]any {
	return oaObj(
		"200", oaJSONRespSchema(
			"The launch requirements observed for this profile and selection. It is a dated observation, never an authorization and never proof that the provider is authenticated: configuration_state describes LOCAL configuration only, while provider_authentication and launch_authorization are always unknown here and remaining_checks names what the POST still decides.",
			sessionsLaunchReadinessSchema()),
		"400", oaJSONResp("malformed profile reference, unknown or repeated query parameter, or an invalid transport/isolation value"),
		"401", oaJSONResp("unauthenticated"),
		"403", oaJSONResp("the caller lacks sessions:profile:read; no filesystem or configuration was inspected"),
		"404", oaJSONResp("no such profile in this tenant (a profile of another tenant is indistinguishable from an absent one)"),
		"409", oaJSONRespSchema(
			"The profile changed during the inspection, so the observation straddled two states; it was discarded and must be read again. The body carries the ratified stable identifier profile_changed, so a client detects this conflict by code and never by matching the message text.",
			sessionsLaunchReadinessConflictSchema()),
		"429", oaJSONResp("rate limited"),
		"503", oaJSONResp("the profile store is unavailable, so no requirements were inspected and no document certifies any"),
	)
}

// sessionsLaunchReadinessConflictSchema is the 409 body: the route's ordinary
// error envelope with the contract's closed identifier declared as a
// single-value enum, so a generated client can switch on it and cannot invent a
// second conflict code this route never emits.
func sessionsLaunchReadinessConflictSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"error", sessionsClosureClosedObject(oaObj(
			"code", oaObj("type", "string",
				"description", "Stable conflict identifier; detect the conflict by this value, never by the message text.",
				"enum", oaEnum("profile_changed")),
			"message", oaObj("type", "string",
				"description", "Human-readable explanation. It is localizable and may change; it is not an identifier."),
		), "code", "message"),
	), "error")
}

// sessionsReadinessStateSchema is the five-value state vocabulary ONE requirement
// dimension can carry. unknown and not_configured are DIFFERENT answers and the
// enum keeps them that way.
func sessionsReadinessStateSchema(description string) map[string]any {
	return oaObj("type", "string", "description", description,
		"enum", oaEnum("ready", "not_configured", "unsupported", "unknown", "not_applicable"))
}

// sessionsReadinessAggregateStateSchema is the strictly SMALLER set the aggregate
// can hold. not_applicable belongs to a single dimension — "this requirement does
// not apply to this combination" — and there is no whole configuration that does
// not apply; the aggregation cannot produce it. Publishing it invited a client to
// handle a state this API never emits.
func sessionsReadinessAggregateStateSchema(description string) map[string]any {
	return oaObj("type", "string", "description", description,
		"enum", oaEnum("ready", "not_configured", "unsupported", "unknown"))
}

func sessionsLaunchReadinessCheckSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"check", oaObj("type", "string",
			"description", "The requirement dimension. Every dimension is always reported, so a dominant known cause never hides an unknown one.",
			"enum", oaEnum("profile", "local_environment", "driver", "runner", "program",
				"homes", "auth_source", "credential_source", "runtime_credentials")),
		"state", sessionsReadinessStateSchema(
			"ready means this LOCAL requirement was checked and met — not that anything is authenticated or authorized. not_configured is a known absence or incompatibility; unsupported is a combination this runtime does not run; unknown is an observation that could not be made and is never rewritten as an absence; not_applicable means the requirement does not belong to this combination and never substitutes for unknown."),
		"code", oaObj("type", "string",
			"description", "Closed cause identifier. It carries no path, argument, environment value, credential or raw error: the console renders localized copy and a static documentation link from it.",
			"enum", oaEnum(sessionsLaunchReadinessCodes()...)),
		"remediation", oaObj("type", "string",
			"description", "Closed identifier of the operator action that would change this verdict; absent when there is nothing to do.",
			"enum", oaEnum(sessionsLaunchReadinessRemediations()...)),
	), "check", "state", "code")
}

// sessionsLaunchReadinessCodes is the closed cause vocabulary, in the module's
// own order (per dimension, then the two shared uncertainty codes).
func sessionsLaunchReadinessCodes() []string {
	return []string{
		"profile_active", "profile_disabled", "profile_retired",
		"environment_local", "other_environment", "environment_unavailable",
		"driver_operable", "driver_not_registered", "transport_unsupported",
		"runner_ready", "runner_not_configured", "isolation_unsupported", "runner_inspection_unavailable",
		"program_present", "program_missing", "program_not_executable",
		"homes_resolved", "home_unavailable", "home_changed",
		"auth_source_account_home", "auth_source_managed_injection", "auth_source_legacy_claude",
		"auth_source_required", "managed_injection_not_applied_for_transport",
		"claude_credential_source_configured", "claude_credential_source_not_configured",
		"provider_credential_adapter_configured", "provider_credential_adapter_not_configured",
		"credential_source_not_injected",
		"runtime_credentials_wired", "runtime_credentials_not_requested",
		"runtime_credential_wiring_incomplete", "runtime_readiness_unavailable",
		"inspection_unavailable", "not_checked_in_this_environment",
	}
}

func sessionsLaunchReadinessRemediations() []string {
	return []string{
		"review_profile", "select_local_profile", "configure_execution_environment",
		"configure_provider_driver", "configure_session_runner", "configure_provider_program",
		"review_profile_homes", "authorize_auth_source", "configure_claude_credential_source",
		"configure_provider_credential_adapter", "review_runtime_credential_composition",
		"select_supported_selection", "retry_inspection",
	}
}

func sessionsLaunchTransportCapabilitiesSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"protocol", oaObj("type", "string",
			"description", "The wire contract of the child this selection would own. unknown is a real answer: a registered driver that publishes no transport metadata is never assumed to speak another provider's protocol.",
			"enum", oaEnum("claude_stream_json", "claude_remote_control", "codex_app_server", "grok_acp", "opencode_acp", "unknown")),
		"io", oaObj("type", "string",
			"description", "Whether Olivares bridges the child's I/O in both directions or manages its lifecycle only. lifecycle_only means the provider relays the conversation: there is no bridged turn console to render.",
			"enum", oaEnum("bidirectional", "lifecycle_only", "unknown")),
		"input", oaObj("type", "string",
			"description", "Shape of one operator input: a raw NDJSON line for the Claude stream-json child, a turn of text for a driver that owns an RPC protocol, unavailable when the transport bridges no input.",
			"enum", oaEnum("line", "text", "unavailable", "unknown")),
	), "protocol", "io", "input")
}

// sessionsLaunchPendingStatementSchema is a dimension this read does not decide.
// Its state enum is a SINGLE value on purpose: neither statement can ever be
// ready here, so a client cannot be written that waits for one to become ready.
//
// ⛔ IT NO LONGER CARRIES required_permission, and the split is the point. One
// shared shape published that field as an optional free string on BOTH
// statements — so the contract allowed a permission on provider_authentication,
// where the server never emits one and where there is no permission to name,
// and it allowed any string on the one place it does belong while the document
// always carries the same value.
func sessionsLaunchPendingStatementSchema(description, code string) map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"state", oaObj("type", "string", "description", description, "enum", oaEnum("unknown")),
		"code", oaObj("type", "string", "enum", oaEnum(code)),
	), "state", "code")
}

// sessionsLaunchAuthorizationSchema is the one statement that names a permission,
// and it names EXACTLY the one the POST requires: required is required, and its
// enum has a single member. A client reading this cannot conclude that some other
// permission might appear, nor that the field might be missing.
func sessionsLaunchAuthorizationSchema(description, code string) map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"state", oaObj("type", "string", "description", description, "enum", oaEnum("unknown")),
		"code", oaObj("type", "string", "enum", oaEnum(code)),
		"required_permission", oaObj("type", "string",
			"description", "The permission creating the run requires. Holding it is not checked by this read and this read never grants it.",
			"enum", oaEnum("sessions:run:write")),
	), "state", "code", "required_permission")
}

func sessionsLaunchReadinessSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"profile_ref", oaObj("type", "string"),
		"profile_version", sessionsInt64Schema(),
		"driver", oaObj("type", "string", "description", "The profile's driver key. The set is open: a driver becomes operable by being registered with the runtime, never by appearing in a list."),
		"profile_state", oaObj("type", "string", "enum", oaEnum("active", "disabled", "retired")),
		"environment_ref", oaObj("type", "string", "description", "The execution environment that administers this profile."),
		"evaluated_environment_ref", oaObj("type", "string", "description", "The execution environment that answered. Absent when this node has no persistent environment identity, which keeps profiled launches deny-closed."),
		"observed_at", sessionsTimestampSchema(),
		"selection", sessionsClosureClosedObject(oaObj(
			"transport", oaObj("type", "string", "enum", oaEnum("stream-json", "remote-control")),
			"isolation", oaObj("type", "string", "enum", oaEnum("native", "container", "sandbox")),
		), "transport", "isolation"),
		"configuration_state", sessionsReadinessAggregateStateSchema(
			"Aggregate of the LOCAL configuration checks only, by explicit precedence: a known unsupported combination, then a known missing configuration, then uncertainty, and ready only when every applicable requirement is ready. not_applicable is neutral in that aggregation and is therefore not one of its values. provider_authentication and launch_authorization never contribute to it."),
		"checks", oaObj("type", "array", "description", "Every requirement dimension, always complete and always in the same order.",
			"items", sessionsLaunchReadinessCheckSchema()),
		"transport_capabilities", sessionsLaunchTransportCapabilitiesSchema(),
		"provider_authentication", sessionsLaunchPendingStatementSchema(
			"Always unknown: this read never contacts a provider, never reads a login file and never reuses another run's observed authentication state. A valid home is not an authenticated account.",
			"not_observed_for_this_launch"),
		"launch_authorization", sessionsLaunchAuthorizationSchema(
			"Always unknown: authorization is decided on the POST, with the budget, kill-switch, approval and claim gates that this read is forbidden to consult because consulting them has effects.",
			"evaluated_on_submit"),
		"remaining_checks", oaObj("type", "array",
			"description", "What this read deliberately did NOT decide, because deciding it needs the effects the launch is allowed to have.",
			"items", oaObj("type", "string", "enum", oaEnum(
				"workspace_and_template_validation", "current_launch_authorization", "claim_admission",
				"credential_mint_when_required", "process_start", "provider_authentication_and_protocol"))),
	),
		"profile_ref", "profile_version", "driver", "profile_state", "environment_ref", "observed_at",
		"selection", "configuration_state", "checks", "transport_capabilities",
		"provider_authentication", "launch_authorization", "remaining_checks",
	)
}
