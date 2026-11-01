// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api/oas"
)

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

func sessionsClosureRequestBody(method, pattern string) (map[string]any, bool) {
	decl, ok := sessionsClosureRequestBodyDeclarationFor(method, pattern)
	if !ok || decl.kind != sessionsClosureBodyful {
		return nil, false
	}
	return oas.Obj(
		"required", decl.required,
		"content", oas.Obj("application/json", oas.Obj("schema", decl.schema)),
	), true
}

func sessionsClosureRequestBodyDeclarationFor(method, pattern string) (sessionsClosureRequestBodyDeclaration, bool) {
	switch method + " " + pattern {
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
	case http.MethodPost + " /decision-requests/{id}/responses":
		return sessionsClosureBodyDeclaration(true, sessionsDecisionRequestResponseSchema()), true
	case http.MethodPost + " /runs/{ref}/resume":
		return sessionsClosureBodyDeclaration(false, sessionsClosureClosedObject(oas.Obj("at_reset", oas.Obj("type", "boolean"), "cancel_reset", oas.Obj("type", "boolean")))), true
	case http.MethodPut + " /limit-resume-settings":
		return sessionsClosureBodyDeclaration(true, sessionsClosureClosedObject(oas.Obj("automatic", oas.Obj("type", "boolean")), "automatic")), true
	case http.MethodPost + " /runs":
		return sessionsClosureBodyDeclaration(true, sessionsCreateRunSchema()), true
	case http.MethodPost + " /push/subscriptions":
		return sessionsClosureBodyDeclaration(true, sessionsPushSubscribeSchema()), true
	case http.MethodPut + " /runs/{ref}/peers":
		return sessionsClosureBodyDeclaration(true, sessionsSetRunPeersSchema()), true
	case http.MethodPut + " /runs/{ref}/name":
		return sessionsClosureBodyDeclaration(true, sessionsClosureClosedObject(oas.Obj(
			"name", oas.Obj("type", "string", "maxLength", maxRunNameRunes,
				"description", "The session's new name, trimmed; at most 200 characters, no control or invisible (format, line or paragraph separator) characters. An empty name clears it."),
		), "name")), true
	case http.MethodPost + " /runs/{ref}/title":
		// An optional body: no body is the default style (run_title.go).
		return sessionsClosureBodyDeclaration(false, sessionsClosureClosedObject(oas.Obj(
			"style", oas.Obj("type", "string", "enum", oas.Enum(titleStyleShort, titleStyleSentence, titleStyleLong),
				"description", "How much of the first message the title keeps: short (28 characters), sentence (60, the default) or long (100)."),
			"if_untitled", oas.Obj("type", "boolean",
				"description", "True makes a title only for a session that has neither a name nor a title, and otherwise answers the session unchanged."),
		))), true
	case http.MethodPost + " /runs/{ref}/move":
		return sessionsClosureBodyDeclaration(true, sessionsMoveRunSchema()), true
	case http.MethodPost + " /runs/{ref}/handoff":
		return sessionsClosureBodyDeclaration(true, sessionsHandoffRunSchema()), true
	case http.MethodPost + " /runs/{ref}/attachments":
		return sessionsClosureBodyDeclaration(true, sessionsAttachmentUploadSchema()), true
	case http.MethodPost + " /runs/{ref}/preview":
		return sessionsClosureBodyDeclaration(true, sessionsClosureClosedObject(
			oas.Obj("port", oas.Obj("type", "integer", "minimum", 1, "maximum", 65535,
				"description", "A port the session's own processes listen on (GET /runs/{ref}/preview).")), "port")), true
	case http.MethodPost + " /templates":
		return sessionsClosureBodyDeclaration(true, sessionsCreateTemplateSchema()), true
	case http.MethodPut + " /templates/{id}":
		return sessionsClosureBodyDeclaration(true, sessionsUpdateTemplateSchema()), true
	case http.MethodPost + " /templates/{id}/duplicate":
		return sessionsClosureBodyDeclaration(true, sessionsDuplicateTemplateSchema()), true
	case http.MethodPost + " /templates/{id}/apply":
		return sessionsClosureBodyDeclaration(false, sessionsApplyTemplateSchema()), true
	case http.MethodPut + " /workspaces/{ref}/setup-command":
		return sessionsClosureBodyDeclaration(true, sessionsClosureClosedObject(oas.Obj("command", oas.Obj("type", "string", "maxLength", maxSetupCommandBytes, "description", "Exact setup command text, at most 8192 UTF-8 bytes without NUL; empty clears it. Stored only, never executed by this operation.")), "command")), true
	case http.MethodPost + " /workspaces/{ref}/setup-command/approve":
		return sessionsClosureBodyDeclaration(true, sessionsClosureClosedObject(oas.Obj("sha256", oas.Obj("type", "string", "pattern", "^[0-9a-f]{64}$", "description", "SHA-256 of the exact command text reviewed. Refused with 409 if the stored text has changed.")), "sha256")), true
	case http.MethodPost + " /workspaces":
		return sessionsClosureBodyDeclaration(true, sessionsCreateWorkspaceSchema()), true
	case http.MethodPatch + " /workspaces/{ref}":
		return sessionsClosureBodyDeclaration(true, sessionsPatchWorkspaceSchema()), true
	case http.MethodPost + " /workspaces/{ref}/project-config/approve":
		return sessionsClosureBodyDeclaration(true, sessionsProjectConfigApproveSchema()), true
	case http.MethodPost + " /workspaces/{ref}/files/move":
		return sessionsClosureBodyDeclaration(true, sessionsMoveFileSchema()), true
	case http.MethodPost + " /workspaces/{ref}/terminals":
		return sessionsClosureBodyDeclaration(false, sessionsTerminalSizeSchema()), true
	case http.MethodPost + " /workspaces/{ref}/terminals/{tid}/size":
		return sessionsClosureBodyDeclaration(true, sessionsTerminalSizeSchema()), true
	case http.MethodPost + " /workspaces/{ref}/terminals/{tid}/input":
		return sessionsClosureBodyDeclaration(true, sessionsClosureClosedObject(oas.Obj(
			"data", oas.Obj("type", "string", "maxLength", terminalMaxInputBytes,
				"description", "Text typed into the terminal, at most 65536 bytes. A newline ends a command line, which is recorded in the audit ledger before the shell receives it."),
		), "data")), true
	case http.MethodPost + " /provider-profiles":
		return sessionsClosureBodyDeclaration(true, sessionsCreateProviderProfileSchema()), true
	case http.MethodPatch + " /provider-profiles/{ref}":
		return sessionsClosureBodyDeclaration(true, sessionsPatchProviderProfileSchema()), true
	case http.MethodPost + " /provider-profiles/resolve":
		return sessionsClosureBodyDeclaration(true, sessionsResolveProviderProfileSchema()), true
	case http.MethodPost + " /provider-source-bindings":
		return sessionsClosureBodyDeclaration(true, sessionsCreateProviderBindingSchema()), true
	case http.MethodPost + " /providers":
		return sessionsClosureBodyDeclaration(true, sessionsCreateProviderSchema()), true
	case http.MethodPatch + " /providers/{ref}":
		return sessionsClosureBodyDeclaration(true, sessionsPatchProviderSchema()), true
	case http.MethodPost + " /provider-accounts":
		return sessionsClosureBodyDeclaration(true, sessionsCreateProviderAccountSchema()), true
	case http.MethodPost + " /provider-accounts/{ref}/adopt":
		return sessionsClosureBodyDeclaration(true, sessionsAdoptProviderAccountSchema()), true
	case http.MethodPatch + " /provider-accounts/{ref}":
		return sessionsClosureBodyDeclaration(true, sessionsPatchProviderAccountMetadataSchema()), true
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
	// run controls (/input, /stop) in openapi.go. Leaving it here would not
	// have failed — sessionsClosureRequestBody returns nothing for a bodyless
	// declaration and the sibling switch would still have published the schema — so
	// the census would have kept asserting "bodyless" about a route with a body,
	// which is the kind of true-looking contradiction this file exists to prevent.
	case http.MethodPut + " /tool-rings/{driver}":
		return sessionsClosureBodyDeclaration(true, sessionsClosureClosedObject(oas.Obj(
			"group_ref", oas.Obj("type", "string", "format", "uuid",
				"description", "The agent group whose members run the candidate version."),
			"version", oas.Obj("type", "string", "minLength", 1, "maxLength", 128,
				"description", "An installed version of the tool to try on that group."),
			"accept_promotion", oas.Obj("type", "boolean",
				"description", "Start over a promoted ring, giving up the pins its rollback would have restored."),
		), "group_ref", "version")), true
	case http.MethodPost + " /tool-rings/{driver}/promote",
		http.MethodPost + " /tool-rings/{driver}/rollback":
		// The tool names the ring and the ring holds the rest: a body would be a second
		// place to say which version and which group.
		return sessionsClosureRequestBodyDeclaration{kind: sessionsClosureBodyless}, true
	case http.MethodPost + " /runs/{ref}/snooze":
		// The one mark with something to say: the moment the snooze ends (run_mark.go).
		return sessionsClosureBodyDeclaration(true, sessionsClosureClosedObject(oas.Obj(
			"until", oas.Obj("type", "string", "format", "date-time",
				"description", "When the session comes back to the caller's list, an RFC 3339 time in the future. A second snooze moves it."),
		), "until")), true
	case http.MethodPut + " /connections/{name}":
		return sessionsClosureBodyDeclaration(true, sessionsClosureClosedObject(oas.Obj(
			"kind", oas.Obj("type", "string", "enum", oas.Enum("database", "ssh", "http", "tcp"),
				"description", "What the connection reaches."),
			"target", oas.Obj("type", "string", "minLength", 1, "maxLength", 512,
				"description", "host:port, or the base URL of an http service. Never a user name or a password."),
			"credential_ref", oas.Obj("type", "string", "maxLength", 256,
				"description", "A reference to a connection/ entry of the secret store: store:connection/<name>. Never the secret."),
			"options", oas.Obj("type", "object", "additionalProperties", oas.Obj("type", "string"),
				"description", "Non-secret settings of the kind: a database's engine, name and user; an ssh host's user and host key fingerprint."),
			"description", oas.Obj("type", "string", "maxLength", 512,
				"description", "What the connection is for, shown to the sessions that may use it."),
			"enabled", oas.Obj("type", "boolean", "description", "A disabled connection serves no session. A new connection is on; a change that omits this keeps the switch as it is."),
		), "kind", "target")), true
	case http.MethodDelete + " /connections/{name}",
		http.MethodPost + " /connections/{name}/test",
		http.MethodPut + " /connections/{name}/grants/{workspace}",
		http.MethodDelete + " /connections/{name}/grants/{workspace}":
		// The path names the connection and the department: a body would be a second place to say them.
		return sessionsClosureRequestBodyDeclaration{kind: sessionsClosureBodyless}, true
	case http.MethodPost + " /runs/{ref}/cleanup":
		// An OPTIONAL body since the worktree option: the confirmation to discard the
		// session's worktree and branch. It was bodyless, and a call with no body is
		// still the call it always was.
		return sessionsClosureBodyDeclaration(false, sessionsCleanupRunSchema()), true
	case http.MethodDelete + " /runs/{ref}",
		// A device is named by its id in the path (push.go).
		http.MethodDelete + " /push/subscriptions/{id}",
		// The pin toggle is bodyless for the reason the revoke is: the run
		// reference IS the request, and there is nothing else to say (run_pin.go).
		http.MethodPost + " /runs/{ref}/pin",
		http.MethodDelete + " /runs/{ref}/pin",
		// So are the caller's done mark and the end of a snooze (run_mark.go).
		http.MethodDelete + " /runs/{ref}/snooze",
		http.MethodPost + " /runs/{ref}/done",
		http.MethodDelete + " /runs/{ref}/done",
		// So is the archive toggle (run_name_archive.go).
		http.MethodPost + " /runs/{ref}/archive",
		http.MethodDelete + " /runs/{ref}/archive",
		http.MethodDelete + " /templates/{id}",
		http.MethodDelete + " /workspaces/{ref}",
		http.MethodDelete + " /workspaces/{ref}/files",
		// The terminal to end is named by its path: the reference IS the request.
		http.MethodDelete + " /workspaces/{ref}/terminals/{tid}",
		http.MethodPost + " /workspaces/{ref}/files/dir",
		http.MethodPost + " /workspaces/{ref}/wiki",
		http.MethodPost + " /channels/{id}/grants/{grant_id}/revoke",
		http.MethodPost + " /deliveries/{id}/ack":
		return sessionsClosureRequestBodyDeclaration{kind: sessionsClosureBodyless}, true
	default:
		// The message queue's routes (run_queue.go).
		return sessionsRunQueueBody(method, pattern)
	}
}

func sessionsClosureBodyDeclaration(required bool, schema map[string]any) sessionsClosureRequestBodyDeclaration {
	return sessionsClosureRequestBodyDeclaration{kind: sessionsClosureBodyful, required: required, schema: schema}
}

func sessionsClosureNullable(schema map[string]any) map[string]any {
	return oas.Obj("anyOf", []any{schema, oas.Obj("type", "null")})
}

func sessionsClosureClosedObject(properties map[string]any, required ...string) map[string]any {
	schema := oas.Obj("type", "object", "additionalProperties", false, "properties", properties)
	if len(required) > 0 {
		schema["required"] = oas.Enum(required...)
	}
	return schema
}

func sessionsClosureOptionalString(description string) map[string]any {
	return sessionsClosureNullable(oas.Obj("type", "string", "description", description))
}

// sessionsSecretEnvSchema is a launch's or a template's vault secrets: NAMES of
// env/… secrets and the variable each is read from (modules/sessions
// session_secret_env.go). No value is ever accepted or returned here.
func sessionsSecretEnvSchema() map[string]any {
	ref := sessionsClosureClosedObject(oas.Obj(
		"env", oas.Obj("type", "string", "description", "The environment variable the child reads the secret from. A shell name; variables the runtime, a provider or the host base owns are refused."),
		"secret", oas.Obj("type", "string", "description", "The vault secret's name in the tenant scope; must begin with env/."),
	), "env", "secret")
	return sessionsClosureNullable(oas.Obj("type", "array", "maxItems", 32, "items", ref))
}

func sessionsCreateRunSchema() map[string]any {
	envName := oas.Obj(
		"type", "string",
		"description", "After trimming, must be a 1..128-byte shell name ([A-Za-z_][A-Za-z0-9_]*). Names beginning OLIVARES_, ANTHROPIC_, or CLAUDE_CODE_, plus DISABLE_AUTOUPDATER, are reserved.",
	)
	return sessionsClosureNullable(sessionsClosureClosedObject(oas.Obj(
		"name", sessionsClosureOptionalString("Trimmed before persistence. At most 200 characters, with no control, invisible or separator characters; otherwise 400."),
		"transport", sessionsClosureNullable(oas.Obj("type", "string", "enum", oas.Enum("", "stream-json", "remote-control"), "description", "Empty/null defaults to stream-json.")),
		"permission_mode", sessionsClosureNullable(oas.Obj("type", "string", "enum", oas.Enum("", "default", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions"), "description", "Empty/null defaults to default.")),
		"effort", sessionsClosureNullable(oas.Obj("type", "string", "maxLength", 64, "description", "Claude Code accepts low, medium, high, xhigh and max; other non-empty values return 400 even when its catalog lists them. This is the final Claude launch contract. Other drivers pass a bounded printable term to the tool. Empty/null keeps the tool default.")),
		"model", sessionsClosureOptionalString("Trimmed before launch."),
		"fast_mode", sessionsClosureNullable(oas.Obj("type", "boolean", "description", "Claude Code only. Explicit false turns Fast mode off; omitted/null keeps the tool default. True requires a catalog model that supports it and an own-account profile; provider-bound launches refuse it to preserve endpoint isolation.")),
		"service_tier", sessionsClosureNullable(oas.Obj("type", "string", "maxLength", 200, "description", "Codex only. A non-empty, whitespace-free service tier is passed unchanged to thread/start and thread/resume; empty/null keeps the tool default. Other drivers refuse a non-empty choice.")),
		"workspace_ref", sessionsClosureOptionalString("Trimmed and, when non-empty, resolved to a registered workspace."),
		"run_on", sessionsClosureOptionalString("Business. The enrolled, online remote node that runs this session. Empty/null runs on this engine. Community answers 501 business_required; an unavailable or unauthorized node returns 409 node_unavailable; a node-check service failure returns 503."),
		"template_id", sessionsClosureOptionalString("When non-empty, identifies the stored template whose terms govern the launch."),
		"isolation", sessionsClosureNullable(oas.Obj("type", "string", "enum", oas.Enum("", "native", "container", "sandbox"), "description", "Empty/null defaults to native; unavailable runners refuse rather than downgrade.")),
		"env_allow", sessionsClosureNullable(oas.Obj("type", "array", "maxItems", 64, "uniqueItems", true, "items", envName)),
		"worktree", sessionsClosureNullable(oas.Obj("type", "boolean", "description", "Opt in to a git worktree and a new branch of the session's own, made from the named workspace's repository (which must be the repository's top folder, native isolation, with no read-only folders). Absent, null or false keeps the session in the workspace folder itself.")),
		"no_setup", sessionsClosureNullable(oas.Obj("type", "boolean", "description", "Skip the folder's setup command for this start only; leaves its stored command and approval unchanged.")),
		"repository_url", sessionsClosureOptionalString("HTTPS repository URL fetched into a new engine-owned session directory, without URL credentials, redirects or ambient authentication. Cannot be combined with workspace_ref or worktree."),
		"repository_ref", sessionsClosureOptionalString("Branch, tag or commit to fetch for repository_url; empty uses HEAD."),
		"cost_cap_micro_usd", sessionsClosureNullable(oas.Obj("type", "integer", "format", "int64", "minimum", 0, "description", "Per-run cost ceiling in millionths of a US dollar. Zero is unbounded. The engine stops the process on reported usage reaching the cap; provider reporting can lag spending.")),
		"timeout_seconds", sessionsClosureNullable(oas.Obj("type", "number", "format", "double", "minimum", 0, "maximum", 2592000, "description", "Per-run timeout, persisted as an absolute engine deadline. Zero is unbounded. Cannot extend a template ceiling.")),
		"worktree_from", sessionsClosureOptionalString("With worktree: start the new worktree at this commit id or local branch of the workspace's repository instead of its current commit (for example the branch and sha a handoff names). Absent keeps the current start. Refused without worktree, and when the commit or branch is not in the repository."),
		"source_url", sessionsClosureOptionalString("The page of a GitHub issue or pull request (https://github.com/<owner>/<repo>/issues/<number> or /pull/<number>) the session starts from. It implies worktree, names the session's branch after the item (olivares/issue-<number>-<suffix> or olivares/pr-<number>-<suffix>) and stays on the session as a link; the client reads the item's title and body with GET source-items and sends them as the first message. Cannot be combined with repository_url; any other page is refused with 400."),
		"secret_env", sessionsSecretEnvSchema(),
		"git_read", sessionsClosureOptionalString("When non-empty, one approved Git-host repository binding (<binding id>:<owner>/<name>) the session gets a short-lived GitHub read credential for: an installation token with contents:read for that repository, served to git by a credential helper (never in the environment) and revoked when the session ends. Needs tenant administration. A GitLab binding is refused (422)."),
		"credentials", sessionsClosureNullable(oas.Obj("type", "array", "maxItems", CredentialMaxNames, "uniqueItems", true, "items", oas.Obj("type", "string", "pattern", "^[a-z0-9][a-z0-9._-]{0,63}$"), "description", "Business. Names of managed credentials (an SSH key or certificate, a database login, an API account) the session gets a way to use: an SSH agent socket, a database proxy or a local reverse proxy that the engine holds, never the secret itself. Needs tenant administration. A Community engine answers 501 business_required.")),
		"provider_profile_ref", sessionsClosureOptionalString("When non-empty, names the provider profile the session launches under; the server resolves its homes and refuses a disabled, retired, foreign-environment or non-operable profile, and one whose driver requires an authorized authentication source it does not have. Only the reference is accepted: no home, environment, driver, binding, session id or authentication source."),
	)))
}

// sessionsCleanupRunSchema is the optional body of a session's release
// (modules/sessions runtime_dto.go, cleanupRunRequest). The endpoint has always
// accepted none, and the server still takes any body (only an explicit true counts);
// discard_worktree confirms that the session's worktree and branch may be removed
// although the branch is not merged, the worktree holds uncommitted files or its HEAD
// is off the branch, and that a worktree this node cannot reach may be left in place.
// Without it such a release is refused (409).
func sessionsCleanupRunSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"discard_worktree", sessionsClosureNullable(oas.Obj("type", "boolean", "description", "Confirms that the session's git worktree and branch may be removed although the branch is not merged, the worktree has uncommitted files or its HEAD is off the branch, and lets the release go on, leaving the worktree where it is, when this node cannot reach it. Absent, null or false removes them only when the branch is merged and the worktree is clean.")),
	))
}

// sessionsSetRunPeersSchema is the run peers choice (modules/sessions session_peers.go):
// exactly one of an explicit list of peer sessions or the same-template rule. The
// handler refuses {} and both fields together, so the schema says "exactly one"
// with two oneOf branches, each requiring its field.
func sessionsSetRunPeersSchema() map[string]any {
	schema := sessionsClosureClosedObject(oas.Obj(
		"peers", oas.Obj("type", "array", "maxItems", 128, "uniqueItems", true,
			"items", oas.Obj("type", "string", "description", "A canonical session ID of another live session in this run's folder."),
			"description", "The sessions this run may message or hand work to. Send this or peers_rule, not both."),
		"peers_rule", oas.Obj("type", "string", "enum", oas.Enum("same-template"),
			"description", "Every live session launched from the same template in this folder. Send this or peers, not both."),
	))
	schema["oneOf"] = []any{oas.Obj("required", oas.Enum("peers")), oas.Obj("required", oas.Enum("peers_rule"))}
	return schema
}

// sessionsMoveRunSchema is the body of POST /runs/{ref}/move. Only the reference of
// the target profile: homes, driver and authorization are the server's to resolve.
func sessionsMoveRunSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"provider_profile_ref", oas.Obj("type", "string",
			"description", "The provider profile to move the stopped session to. It must belong to the same tool as the session's current profile."),
	), "provider_profile_ref")
}

// sessionsHandoffRunSchema is the body of POST /runs/{ref}/handoff.
func sessionsHandoffRunSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"provider_profile_ref", oas.Obj("type", "string",
			"description", "The provider profile to continue in. It may belong to any tool, including the session's own."),
		"summary", oas.Obj("type", "string", "maxLength", handoffSummaryMax,
			"description", "The reviewed text the new session starts from. Omitted, the server generates it (GET /runs/{ref}/handoff returns the same text for review)."),
		"name", oas.Obj("type", "string",
			"description", "The new session's name. Defaults to a name that says it continues this one."),
	), "provider_profile_ref")
}

func sessionsTemplateBodySchema() map[string]any {
	hookEntry := sessionsClosureNullable(sessionsClosureClosedObject(oas.Obj(
		"command", sessionsClosureOptionalString("Hook command; the template authoring handler stores it without semantic validation."),
		"timeout_ms", sessionsClosureNullable(oas.Obj("type", "integer")),
	)))
	hookEntries := sessionsClosureNullable(oas.Obj("type", "array", "items", hookEntry))
	hooks := sessionsClosureNullable(sessionsClosureClosedObject(oas.Obj(
		"pre_tool", hookEntries,
		"post_tool", hookEntries,
		"pre_session", hookEntries,
		"post_session", hookEntries,
	)))
	settings := sessionsClosureNullable(sessionsClosureClosedObject(oas.Obj(
		"permission_mode", sessionsClosureOptionalString("Validated when the template is reduced for preview or launch."),
		"effort", sessionsClosureOptionalString("Validated when the template is reduced for preview or launch."),
		"model", sessionsClosureOptionalString("Stored template model selector."),
		"custom_instructions", sessionsClosureOptionalString("Stored custom instructions."),
		"secret_env", sessionsSecretEnvSchema(),
	)))
	stringList := sessionsClosureNullable(oas.Obj(
		"type", "array", "items", sessionsClosureNullable(oas.Obj("type", "string")),
	))
	policies := sessionsClosureNullable(sessionsClosureClosedObject(oas.Obj(
		"dlp_mode", sessionsClosureOptionalString("Validated when the template is reduced for preview or launch."),
		"max_session_duration_minutes", sessionsClosureNullable(oas.Obj("type", "integer")),
		"allowed_tools", stringList,
		"record_io", sessionsClosureNullable(oas.Obj("type", "boolean")),
		"require_truncate_protection", sessionsClosureNullable(oas.Obj("type", "boolean", "description", "Refuse read-only native launches on kernels that cannot block truncation of existing files. Omitted/null/false keeps the default launch and reports the kernel limit.")),
	)))
	return sessionsClosureNullable(sessionsClosureClosedObject(oas.Obj(
		"hooks", hooks,
		"settings", settings,
		"connectors", stringList,
		"policies", policies,
	)))
}

func sessionsCreateTemplateSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"name", oas.Obj("type", "string", "minLength", 1),
		"description", sessionsClosureOptionalString("Stored template description."),
		"body", sessionsTemplateBodySchema(),
	), "name")
}

func sessionsUpdateTemplateSchema() map[string]any {
	return sessionsClosureNullable(sessionsClosureClosedObject(oas.Obj(
		"name", sessionsClosureOptionalString("An explicit string, including empty, replaces the stored name; null/omission preserves it."),
		"description", sessionsClosureOptionalString("An explicit string replaces the stored description; null/omission preserves it."),
		"body", sessionsTemplateBodySchema(),
	)))
}

func sessionsDuplicateTemplateSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"name", oas.Obj("type", "string", "minLength", 1, "description", "Name for the duplicate; whitespace is accepted because the handler checks exact emptiness."),
	), "name")
}

func sessionsApplyTemplateSchema() map[string]any {
	stringList := sessionsClosureNullable(oas.Obj(
		"type", "array", "items", sessionsClosureNullable(oas.Obj("type", "string")),
	))
	target := sessionsClosureNullable(sessionsClosureClosedObject(oas.Obj(
		"transport", sessionsClosureOptionalString("Preview transport; unenforceable template terms are reported rather than silently dropped."),
		"permission_mode", sessionsClosureOptionalString("Proposed launch permission mode."),
		"effort", sessionsClosureOptionalString("Proposed launch effort."),
		"model", sessionsClosureOptionalString("Proposed launch model."),
		"allowed_tools", stringList,
		"custom_instructions", sessionsClosureOptionalString("Proposed custom instructions."),
		"record_io", sessionsClosureNullable(oas.Obj("type", "boolean")),
		"max_session_duration_minutes", sessionsClosureNullable(oas.Obj("type", "integer", "format", "int64")),
		"workspace_ref", sessionsClosureOptionalString("Proposed workspace reference."),
	)))
	return sessionsClosureNullable(sessionsClosureClosedObject(oas.Obj("target", target)))
}

func sessionsCreateWorkspaceSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"name", sessionsClosureOptionalString("Trimmed workspace display name."),
		"root_path", oas.Obj("type", "string", "description", "After trimming, must be an absolute, existing directory; the server stores its canonical real path."),
		"mount_mode", sessionsClosureNullable(oas.Obj("type", "string", "enum", oas.Enum("", "rw", "ro", "write_no_delete"), "description", "Empty/null defaults to rw. write_no_delete is read-write without the right to remove a name: a native session creates and changes files in the folder (existing files can still be overwritten or emptied) and cannot unlink, rmdir, rename or move one out, and the file API refuses delete and move. The kernel enforces it (Linux Landlock), so a session in such a folder does not start on a host that cannot confine. In a folder that is a Git repository, .git stays fully writable so the agent's own git works (it renames a lock file over its index), which also lets the session empty .git and destroy the repository's history; the working files stay protected. A tool that replaces a file by removing or renaming it (sed -i, an atomic-save formatter) is refused and can leave its temporary file. The session's Git panel acts for the person and is not restricted.")),
		"container_target", sessionsClosureOptionalString("Must be absolute after trimming; empty/null selects the default container target."),
		"allow_subpaths", sessionsClosureNullable(oas.Obj(
			"type", "array", "description", "Relative, NUL-free paths that cannot escape the workspace root.",
			"items", sessionsClosureNullable(oas.Obj("type", "string")),
		)),
		"backup", sessionsClosureNullable(oas.Obj("type", "boolean", "description", "Folder file-backup selection metadata; absent defaults to true.")),
		"data_class", sessionsDataClassSchema(),
		"read_only_folders", sessionsClosureNullable(sessionsWorkspaceReadOnlyFoldersSchema()),
		"max_read_bytes", sessionsClosureNullable(oas.Obj("type", "integer", "format", "int64", "minimum", 0)),
		"dlp_mode", sessionsClosureNullable(sessionsWorkspaceDLPModeSchema("Empty/null defaults to off.")),
	), "root_path")
}

// sessionsPatchWorkspaceSchema is the PATCH /workspaces/{ref} body: any of the
// settings, at least one; a setting left out or null is not changed.
func sessionsPatchWorkspaceSchema() map[string]any {
	schema := sessionsClosureClosedObject(oas.Obj(
		"backup", sessionsClosureNullable(oas.Obj("type", "boolean", "description", "Folder file-backup selection metadata; absent defaults to true.")),
		"data_class", sessionsDataClassSchema(),
		"read_only_folders", sessionsClosureNullable(sessionsWorkspaceReadOnlyFoldersSchema()),
		"dlp_mode", sessionsClosureNullable(sessionsWorkspaceDLPModeSchema("Empty selects off, as on registration.")),
		"default_permission", sessionsClosureNullable(sessionsWorkspaceDefaultPermissionSchema()),
	))
	schema["minProperties"] = 1
	schema["description"] = "Name at least one setting with a value. A setting left out or null is not changed."
	return schema
}

// sessionsWorkspaceDefaultPermissionSchema lists exactly what ResolvePermission
// accepts (permission_modes.go): names, labels and older names or launch phrases.
func sessionsWorkspaceDefaultPermissionSchema() map[string]any {
	values := []string{""}
	for _, n := range PermissionNames() {
		values = append(values, n.Name, n.Label)
		values = append(values, n.Aliases...)
	}
	return oas.Obj("type", "string", "enum", oas.Enum(values...),
		"description", "The permission a session started in this folder is offered by default, by name (an older name is stored as its name). Empty takes the default away. full and auto need run administration. Leaving it out or null changes nothing.")
}

func sessionsWorkspaceDLPModeSchema(description string) map[string]any {
	return oas.Obj("type", "string", "enum", oas.Enum("", "label", "deny", "off"), "description", description)
}

func sessionsWorkspaceReadOnlyFoldersSchema() map[string]any {
	return oas.Obj("type", "array", "description", "Additional read-only host folders for native sessions. Read-only folders protect file content and directory entries (no write, truncate, create, remove or rename); permissions, ownership, extended attributes and timestamps follow the host's normal permissions. Must be absolute, existing directories outside protected engine folders. The server stores canonical paths. Omitted on create or an empty array grants no extra access.",
		"items", oas.Obj("type", "string", "minLength", 1))
}

// sessionsTerminalSizeSchema mirrors the window size a terminal start or resize decodes
// (terminal_api.go): rows and columns, both optional, 0 meaning the default 24x80.
func sessionsTerminalSizeSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"rows", oas.Obj("type", "integer", "minimum", 0, "maximum", 65535,
			"description", "Window height in rows; absent or 0 means 24, and it is limited to 500."),
		"cols", oas.Obj("type", "integer", "minimum", 0, "maximum", 65535,
			"description", "Window width in columns; absent or 0 means 80, and it is limited to 1000."),
	))
}

func sessionsMoveFileSchema() map[string]any {
	path := oas.Obj(
		"type", "string", "pattern", ".*\\S.*",
		"description", "Relative, NUL-free path jailed inside the workspace; it cannot resolve to the workspace root.",
	)
	return sessionsClosureClosedObject(oas.Obj("from", path, "to", path), "from", "to")
}

// sessionsCreateProviderProfileSchema mirrors modules/sessions createProfileRequest
// (strict decoder: unknown keys are rejected). The homes are validated on the
// server — absolute, canonicalized, existing directories on this node's execution
// environment — and never created as a fallback. No credential value is accepted.
func sessionsCreateProviderProfileSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"max_data_class", sessionsDataClassSchema(),
		"session_work_grant", sessionsProfileWorkGrantSchema(),
		"driver", oas.Obj("type", "string", "minLength", 1, "description", "Provider driver key, lower-cased ([a-z0-9][a-z0-9._-]*). Open set; only drivers with an operated runner on this node are launchable."),
		"config_home", oas.Obj("type", "string", "minLength", 1, "description", "Absolute path of the provider configuration directory on this node; symlinks are resolved and the canonical path is stored."),
		"user_home", oas.Obj("type", "string", "minLength", 1, "description", "Absolute path used as HOME for the launched process; validated like config_home."),
		"display_name", sessionsClosureOptionalString("Editable label; trimmed, at most 200 bytes, no control characters."),
		"environment_ref", sessionsClosureOptionalString("Optional; when present must equal this node's execution environment (a foreign environment cannot have its homes validated here)."),
		"auth_source", sessionsClosureNullable(oas.Obj("type", "string", "enum", oas.Enum("", "provider_account_home", "managed_injection"), "description", "Authorized authentication source for this profile: provider_account_home (the child uses the saved login inside its own homes) or managed_injection (a provider-compatible credential minted by that driver's governed adapter). They are distinct authorizations with no fallback between them; empty authorizes neither and refuses every launch whose driver requires one.")),
		"provider_record_ref", sessionsClosureOptionalString("Optional: the registered provider whose credential this profile's managed launches use. Empty binds no registered provider; auth_source still governs how launches authenticate. The engine refuses a credential whose kind this driver cannot read."),
		"session_programs", sessionsProfileProgramsSchema(),
		"session_registries", sessionsProfileRegistriesSchema(),
		"session_egress_hosts", sessionsProfileEgressHostsSchema(),
	), "driver", "config_home", "user_home")
}

// sessionsPatchProviderProfileSchema mirrors patchProfileRequest: a label and/or an
// active↔disabled transition. Retirement has its own admin route; driver, environment
// and homes are immutable and are not keys of this body.
func sessionsPatchProviderProfileSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"max_data_class", sessionsDataClassSchema(),
		"session_work_grant", sessionsProfileWorkGrantSchema(),
		"display_name", sessionsClosureOptionalString("An explicit string replaces the stored label; null/omission preserves it."),
		"state", sessionsClosureNullable(oas.Obj("type", "string", "enum", oas.Enum("active", "disabled"), "description", "Reversible lifecycle transition; retired is refused here.")),
		"auth_source", sessionsClosureNullable(oas.Obj("type", "string", "enum", oas.Enum("", "provider_account_home", "managed_injection"), "description", "Re-authorizes (or, with the empty string, withdraws) the authentication source. It is not identity, so it does not create a new profile id; a live child keeps the source its own launch was authorized under.")),
		"provider_record_ref", sessionsClosureOptionalString("Binds (or, with the empty string, unbinds) the registered provider this profile's managed launches use. Like auth_source it is an authorization and not identity, and it does not reach a live child: a running session keeps the provider its own launch resolved."),
		"session_programs", sessionsProfileProgramsSchema(),
		"session_registries", sessionsProfileRegistriesSchema(),
		"session_egress_hosts", sessionsProfileEgressHostsSchema(),
	))
}

// sessionsProfileProgramsSchema, sessionsProfileRegistriesSchema and
// sessionsProfileEgressHostsSchema mirror provider_profile_packages.go: what every
// session under the profile gets.
func sessionsProfileProgramsSchema() map[string]any {
	return sessionsClosureNullable(oas.Obj("type", "array", "maxItems", maxSessionPrograms,
		"items", oas.Obj("type", "string", "pattern", "^[a-z0-9][a-z0-9_-]{0,63}$"),
		"description", "Installed third-party programs (the tool installer's program names) put first on each session's PATH. A launch whose program is not installed is refused 409. Declaring programs needs tenant administration. On PATCH, [] withdraws the declaration and null or omission keeps it."))
}

func sessionsProfileEgressHostsSchema() map[string]any {
	return sessionsClosureNullable(oas.Obj("type", "array", "maxItems", maxSessionEgressHosts,
		"items", oas.Obj("type", "string", "minLength", 1, "maxLength", 253, "description", "A DNS host name such as github.com: no scheme, port, path, wildcard or address."),
		"description", "HTTPS hosts a confined session may reach beside its bound provider (a Git host, a module mirror); none by default. Each is admitted only at public addresses, whatever its name resolves to. Declaring hosts needs tenant administration and is recorded in the audit ledger. On PATCH, [] withdraws the declaration and null or omission keeps it."))
}

func sessionsProfileRegistriesSchema() map[string]any {
	return sessionsClosureNullable(oas.Obj("type", "array", "maxItems", maxSessionRegistries,
		"items", sessionsClosureClosedObject(oas.Obj(
			"kind", oas.Obj("type", "string", "enum", oas.Enum(registryKindNPM)),
			"url", oas.Obj("type", "string", "minLength", 1, "description", "The registry's base URL: https, or http on a loopback host."),
			"scope", oas.Obj("type", "string", "description", "An npm scope (@name) routed to this registry; empty makes it the default registry, one per profile."),
			"secret", oas.Obj("type", "string", "description", "The tenant vault secret (env/...) holding the registry token. Empty sends none."),
		), "kind", "url"),
		"description", "npm registries each native session installs from. The token travels in a per-launch npm configuration file, never in the environment or argv, and a confined session's network reaches the registry hosts. Declaring registries needs tenant administration. On PATCH, [] withdraws the declaration and null or omission keeps it."))
}

// sessionsResolveProviderProfileSchema mirrors resolveProfileRequest: the driver
// only. Which profile a new session of that driver uses is the engine's one rule
// (its own login when signed in, otherwise a record in Providers it can use), so
// both clients ask instead of choosing.
func sessionsResolveProviderProfileSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"driver", oas.Obj("type", "string", "enum", oas.Enum("claude", "codex", "grok", "opencode", "gemini-cli", "cursor"), "description", "The coding tool the new session runs. The answer is a profile this node can launch it with, reused or created; a tool with nothing to run on is refused with the step that fixes it."),
		"account", oas.Obj("type", "string", "description", "Optional. An account name (as shown by the provider-accounts list) or a profile reference of the same tool: the answer is that account's profile, nothing is created and the engine's own choice does not run. Unknown: 404. Another tool's, foreign, disabled or retired: 409. Omit it and the rule above answers, exactly as before."),
	), "driver")
}

// sessionsResolvePreviewRoute is the read-only twin of the resolve: the same rule's
// answer with no profile made, for a page that shows what a session would run on.
func sessionsResolvePreviewRoute(method, pattern string) bool {
	return method == http.MethodGet && pattern == "/provider-profiles/resolve"
}

func sessionsResolvePreviewParameters() []map[string]any {
	return []map[string]any{oas.Param("driver", "query",
		"The coding tool a new session would run. Answered with the reason (own_login or api_key) and, for a key, the provider record; a tool with nothing to run on is refused 409 with the sentence the resolve gives.",
		true, oas.Obj("type", "string", "enum", oas.Enum("claude", "codex", "grok", "opencode", "gemini-cli", "cursor")))}
}

// sessionsCreateProviderSchema mirrors createProviderRecordRequest. api_key is
// the ONLY field that carries a credential; it travels in this body and appears in no
// response, no log line and no row — the record stores a locator and a four-character
// hint. base_url is required for openai_compatible, which has no official endpoint to
// assume; the engine enforces that and this description says so rather than trying to
// express a conditional requirement in the schema.
func sessionsCreateProviderSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"kind", oas.Obj("type", "string", "enum", oas.Enum("anthropic", "openai", "xai", "gemini", "openai_compatible"), "description", "What the credential IS, independent of which CLI reads it. The set is closed because a kind decides which environment variables a launched child receives."),
		"service", sessionsClosureOptionalString("Optional immutable vendor preset: a ref from GET /provider-presets. A preset fixes the kind and the vendor's documented endpoint, so base_url may be omitted or must equal it; an unknown ref is refused with the valid refs. deepseek registers the governed inference service, which no tool session uses. Empty/null keeps the provider's normal endpoint and discovery."),
		"display_name", oas.Obj("type", "string", "minLength", 1, "description", "Your own name for this credential; it is what a picker shows. Unique among active providers of the same kind, case-folded."),
		"base_url", sessionsClosureOptionalString("Absolute https endpoint override, or plain http at a loopback or private-network address for anthropic, xai and openai_compatible. Empty uses the provider's official endpoint; REQUIRED for openai_compatible. A URL carrying userinfo is refused: a credential must not travel in a field that is published in every read."),
		"default_model", sessionsClosureOptionalString("Model for fresh bound sessions unless another model is selected. Omission awaits the first usable successful connection test; an empty string clears the saved default and disables automatic selection."),
		"api_key", oas.Obj("type", "string", "minLength", 8, "description", "The credential. Sealed at rest by the engine and never returned by any read, including immediately after this write. Lose it and rotate; there is no read that recovers it."),
	), "kind", "display_name", "api_key")
}

// sessionsPatchProviderSchema mirrors patchProviderRecordRequest: rename,
// re-endpoint and/or ROTATE. `kind` is absent because a record's kind decides what is
// injected into a child, so changing it under an existing binding would redefine every
// launch that binding authorizes — registering another record is the honest way.
func sessionsPatchProviderSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"display_name", sessionsClosureOptionalString("An explicit string replaces the stored name; null/omission preserves it."),
		"base_url", sessionsClosureOptionalString("An explicit string replaces the stored endpoint; null/omission preserves it."),
		"default_model", sessionsClosureOptionalString("An explicit model replaces the saved default for fresh sessions; empty clears it, null/omission preserves it. Does not rotate credentials or change the last connection test."),
		"api_key", sessionsClosureNullable(oas.Obj("type", "string", "minLength", 8, "description", "Rotates the credential in place: it reseals under the SAME reference, so every profile bound to this provider keeps working and the NEXT launch uses the new value — a session already running keeps the one it started with. The previous connection test is cleared, because a verdict measured on a credential that no longer exists is not evidence about the one replacing it.")),
	))
}

// sessionsCreateProviderAccountSchema mirrors createProviderAccountRequest: the
// driver, optional account name and retry identity. The execution
// environment, the account reference, the home path, its mode and its isolation
// level are the server's to decide, so none of them is a key of this body.
func sessionsCreateProviderAccountSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"idempotency_key", sessionsClosureOptionalString("Stable account creation intention: 1 to 128 ASCII letters, digits, hyphens or underscores. Reuse it after an uncertain result with the same driver and name. Empty or null starts a new intention. This key does not grant authority."),
		"driver", oas.Obj("type", "string", "minLength", 1, "description", "Provider driver key, lower-cased ([a-z0-9][a-z0-9._-]*). The account's home is built for this driver on the node that serves this execution environment."),
		"name", sessionsClosureOptionalString("The account's name: lowercase ASCII, a letter first, then letters, digits or '-', at most 32 characters. It is unique in the execution environment across every driver, archived accounts included, and a taken name is refused rather than replaced by another. Empty or null asks the server to generate one: the bare driver name first, then -b, -c and so on."),
	), "driver")
}

// sessionsAdoptProviderAccountSchema mirrors adoptProviderAccountRequest: an
// optional name and nothing else. The isolation level is not a request field,
// because an adopted home is shared by what it is.
func sessionsAdoptProviderAccountSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"name", sessionsClosureOptionalString("The account's name: lowercase ASCII, a letter first, then letters, digits or '-', at most 32 characters. It is unique in the profile's execution environment across every driver, archived accounts included, and a taken name is refused rather than replaced by another. Empty or null asks the server to generate one: the bare driver name first, then -b, -c and so on."),
	))
}

// sessionsCreateProviderBindingSchema mirrors createBindingRequest: the source's
// persistent id, the EXACT revision this node has applied, and the profile.
func sessionsCreateProviderBindingSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"source_id", oas.Obj("type", "string", "minLength", 1, "description", "Persistent id of the durable source roster row (not its editable name)."),
		"source_revision", oas.Obj("type", "integer", "minimum", 1, "description", "The revision this node's runtime has successfully applied; any other value is refused as stale."),
		"profile_ref", oas.Obj("type", "string", "minLength", 1, "description", "The provider profile the source is dedicated to."),
	), "source_id", "source_revision", "profile_ref")
}

func sessionsCommunicationRefSchema(kinds ...string) map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"kind", oas.Obj("type", "string", "enum", oas.Enum(kinds...)),
		"ref", oas.Obj("type", "string", "minLength", 1, "maxLength", 1024),
	), "kind", "ref")
}

func sessionsContentReferenceSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"kind", oas.Obj("type", "string", "minLength", 1),
		"ref", oas.Obj("type", "string", "minLength", 1),
		"hash", oas.Obj("type", "string"),
	), "kind", "ref")
}

func sessionsMessageContentSchema() map[string]any {
	block := sessionsClosureClosedObject(oas.Obj(
		"type", oas.Obj("type", "string", "enum", oas.Enum("text", "reference", "status", "action_ref")),
		"format", oas.Obj("type", "string", "enum", oas.Enum("", "plain", "markdown")),
		"text", oas.Obj("type", "string"),
		"reference", sessionsClosureNullable(sessionsContentReferenceSchema()),
		"code", oas.Obj("type", "string"),
	), "type")
	return sessionsClosureClosedObject(oas.Obj(
		"subject", oas.Obj("type", "string"),
		"blocks", oas.Obj("type", "array", "minItems", 1, "maxItems", 64, "items", block),
	), "subject", "blocks")
}

func sessionsChannelGrantInputSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"subject", sessionsCommunicationRefSchema("user", "user_group", "agent", "agent_group", "session"),
		"can_read", oas.Obj("type", "boolean"),
		"can_write", oas.Obj("type", "boolean"),
		"can_admin", oas.Obj("type", "boolean"),
		"expires_at", sessionsClosureNullable(oas.Obj("type", "string", "format", "date-time")),
	), "subject", "can_read", "can_write", "can_admin")
}

func sessionsCreateChannelSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"workspace_id", sessionsProtocolBindingIDSchema(),
		"slug", oas.Obj("type", "string", "minLength", 1),
		"name", oas.Obj("type", "string", "minLength", 1),
		"description", oas.Obj("type", "string"),
		"kind", oas.Obj("type", "string", "enum", oas.Enum("coordination", "work", "incident", "announcement", "private")),
		"sensitivity", oas.Obj("type", "string", "enum", oas.Enum("internal", "restricted")),
		"content_protection", oas.Obj("type", "string", "enum", oas.Enum("storage", "application_sealed")),
		"default_ack_policy", oas.Obj("type", "string", "enum", oas.Enum("none", "each_required", "quorum")),
		"default_ack_timeout_ms", oas.Obj("type", "integer", "format", "int64", "minimum", 0),
		"default_wake", oas.Obj("type", "string", "enum", oas.Enum("none", "primary", "all", "inherit")),
		"retention_policy_ref", oas.Obj("type", "string"),
		"max_fanout", oas.Obj("type", "integer", "format", "int64", "minimum", 1),
		"max_automation_depth", oas.Obj("type", "integer", "format", "int64", "minimum", 0),
		"initial_grants", oas.Obj("type", "array", "minItems", 1, "maxItems", 64, "items", sessionsChannelGrantInputSchema()),
	), "workspace_id", "slug", "name", "initial_grants")
}

func sessionsUpdateChannelSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"channel_id", oas.Obj("type", "string", "format", "uuid"),
		"name", oas.Obj("type", "string"), "description", oas.Obj("type", "string"),
		"state", oas.Obj("type", "string", "enum", oas.Enum("active", "archived")),
		"sensitivity", oas.Obj("type", "string", "enum", oas.Enum("internal", "restricted")),
		"content_protection", oas.Obj("type", "string", "enum", oas.Enum("storage", "application_sealed")),
		"default_ack_policy", oas.Obj("type", "string", "enum", oas.Enum("none", "each_required", "quorum")),
		"default_ack_timeout_ms", oas.Obj("type", "integer", "format", "int64"),
		"default_wake", oas.Obj("type", "string", "enum", oas.Enum("none", "primary", "all", "inherit")),
		"retention_policy_ref", oas.Obj("type", "string"),
		"max_fanout", oas.Obj("type", "integer", "format", "int64"),
		"max_automation_depth", oas.Obj("type", "integer", "format", "int64"),
	), "channel_id")
}

func sessionsChannelGrantSchema() map[string]any { return sessionsChannelGrantInputSchema() }

func sessionsMessageSendSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"channel_id", oas.Obj("type", "string", "format", "uuid"),
		"recipient", sessionsCommunicationRefSchema("user", "agent", "session"),
		"content", sessionsMessageContentSchema(),
		"urgency", oas.Obj("type", "string", "enum", oas.Enum("", "normal", "high", "critical")),
		"available_at", sessionsTimestampSchema(),
	), "channel_id", "recipient", "content")
}

func sessionsCursorAdvanceSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"cursor", oas.Obj("type", "string", "minLength", 1, "maxLength", 8192),
		"delivery_id", oas.Obj("type", "string", "format", "uuid",
			"description", "Exact target Delivery named by the opaque cursor token; used for stored-workspace route authorization."),
	), "cursor", "delivery_id")
}

func sessionsHandoffContentSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"summary", oas.Obj("type", "string", "minLength", 1),
		"next_action", oas.Obj("type", "string", "minLength", 1),
		"risk", oas.Obj("type", "string"),
		"branch", oas.Obj("type", "string", "minLength", 1, "maxLength", 512, "description", "Optional. The git branch the handed-over work is on. A label the sender wrote; the receiver may open a session worktree from it."),
		"sha", oas.Obj("type", "string", "pattern", "^([0-9a-f]{40}|[0-9a-f]{64})$", "description", "Optional. The full git commit id the handed-over work is at (40 or 64 lowercase hex digits). The receiver may open a session worktree at it."),
		"artifact_refs", oas.Obj("type", "array", "maxItems", 64, "items", sessionsContentReferenceSchema()),
	), "summary", "next_action")
}

func sessionsHandoffOfferSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"channel_id", oas.Obj("type", "string", "format", "uuid"),
		"work_item_id", oas.Obj("type", "string", "format", "uuid"),
		"recipient", sessionsCommunicationRefSchema("user", "agent", "session"),
		"handoff", sessionsHandoffContentSchema(),
		"ack_deadline", oas.Obj("type", "string", "format", "date-time"),
		"expected_owner_epoch", oas.Obj("type", "integer", "format", "int64", "minimum", 0),
	), "channel_id", "work_item_id", "recipient", "handoff", "ack_deadline")
}

// sessionsReasonSchema is the single published shape of a communication reason.
// The handoff response body and the terminal reason a recipient reads back are
// the SAME content slot, so they share one schema instead of two literals that
// can drift.
func sessionsReasonSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"code", oas.Obj("type", "string", "minLength", 1),
		"text", oas.Obj("type", "string"),
		"references", oas.Obj("type", "array", "maxItems", 64, "items", sessionsContentReferenceSchema()),
	), "code")
}

func sessionsHandoffResponseSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"transition", oas.Obj("type", "string", "enum", oas.Enum("accept", "reject")),
		"reason", sessionsClosureNullable(sessionsReasonSchema()),
	), "transition")
}

func sessionsDecisionRequestResponseSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"transition", oas.Obj("type", "string", "enum", oas.Enum("accept", "block", "resolve", "reject", "cancel")),
		"response", sessionsClosureClosedObject(oas.Obj(
			"choice_key", oas.Obj("type", "string", "maxLength", 128,
				"description", "Required for resolve; omitted for every other transition."),
			"reason", sessionsReasonSchema(),
		), "reason"),
		"blocker_work_item_id", oas.Obj("type", "string", "format", "uuid",
			"description", "Optional blocker for the block transition; omitted for every other transition."),
	), "transition", "response")
}

// A grant is an operator-owned declaration; the server alone issues grant_id.
func sessionsProfileWorkGrantSchema() map[string]any {
	return sessionsClosureNullable(sessionsClosureClosedObject(oas.Obj(
		"role", oas.Obj("type", "string", "enum", oas.Enum("orchestrator")),
		"workspace_id", oas.Obj("type", "string", "format", "uuid", "description", "One active tenant workspace; UUIDv7."),
		"capabilities", oas.Obj("type", "array", "minItems", 1, "maxItems", 6, "uniqueItems", true, "items", oas.Obj("type", "string", "enum", oas.Enum("work.read", "work.create", "work.assign", "work.review", "decision.read", "decision.write"))),
	), "role", "workspace_id", "capabilities"))
}

func sessionsDataClassSchema() map[string]any {
	return sessionsClosureNullable(oas.Obj("type", "string", "enum", oas.Enum(dataClassValues...), "description", "Classification metadata; empty leaves it undeclared."))
}
