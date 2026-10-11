// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/olivaresai/olivares/core/model"
)

// runDTO is the API shape of one operated Claude Code session. It carries NO
// secret, env value, prompt or transcript — only references and non-sensitive
// lifecycle facts (minimal-data, docs/SECURITY-HARDENING.md). `state` is the DERIVED state:
// stored `running` shows as `idle` when activity is stale (a read-time
// projection, never a stored flip-flop — mirrors the observe overlay's cc_state).
type runDTO struct {
	// Completion belongs only to an originating in-process launch call.
	Completion     RuntimeLaunchCompletion `json:"-"`
	RunRef         string                  `json:"run_ref"`
	Peers          []string                `json:"peers"`
	PeersRule      string                  `json:"peers_rule,omitempty"`
	Name           string                  `json:"name,omitempty"`
	Transport      string                  `json:"transport"`
	PermissionMode string                  `json:"permission_mode"`
	Effort         string                  `json:"effort,omitempty"`
	ModelRef       string                  `json:"model_ref,omitempty"`
	WorkspaceRef   string                  `json:"workspace_ref,omitempty"`
	// AuthzWorkspaceID is the stored core authorization scope, separate from the folder.
	AuthzWorkspaceID string `json:"authz_workspace_id,omitempty"`
	// WorkspacePath is the HOST directory this session's child was started in: the
	// registered workspace's canonical root, or the directory of its own this plane
	// created under the data directory. It is shown by NAME because the walk of
	// 2026-09-18 could only answer "where is this session working" by reading the
	// child's own init frame — and the answer then was the engine's own directory.
	// Empty on a run that predates the column, which reads as "not recorded".
	WorkspacePath string `json:"workspace_path,omitempty"`
	// WorktreeBranch is the branch of the session's own git worktree, named by the
	// plane that made it. Empty for a session that works in its workspace directly.
	WorktreeBranch  string `json:"worktree_branch,omitempty"`
	TemplateID      string `json:"template_id,omitempty"`
	TemplateVersion int64  `json:"template_version,omitempty"`
	// SecretEnv names the vault secrets this session receives as environment
	// variables. Names only: no read of a run ever returns a value.
	SecretEnv []SecretEnvRef `json:"secret_env,omitempty"`
	// GitRead names the repository binding this session gets a read credential
	// for. The binding only: no read of a run returns the token.
	GitRead         string           `json:"git_read,omitempty"`
	MaxDurationSecs int64            `json:"max_duration_secs,omitempty"`
	Isolation       string           `json:"isolation"`
	State           string           `json:"state"`
	ProcessState    string           `json:"process_state"`
	WorkScope       *runWorkScopeDTO `json:"work_scope,omitempty"`
	ClaudeSessionID string           `json:"claude_session_id,omitempty"`
	PID             *int64           `json:"pid,omitempty"`
	CredentialID    string           `json:"credential_id,omitempty"`
	ExitCode        *int64           `json:"exit_code,omitempty"`
	Reason          string           `json:"reason,omitempty"`
	LastEventSeq    int64            `json:"last_event_seq"`
	CreatedAt       string           `json:"created_at,omitempty"`
	StartedAt       string           `json:"started_at,omitempty"`
	LastActivityAt  string           `json:"last_activity_at,omitempty"`
	StoppedAt       string           `json:"stopped_at,omitempty"`

	// Governance posture, the non-sensitive launch-decision facts the portal
	// renders per session. AgentRef lets the client query the kill-switch/budget scoped on
	// this run; ApprovalRef lets it deep-link the HITL approval's live status. PEPProvisioned
	// reports whether the managed PreToolUse hook reaches the governed PEP (its tool-calls
	// are policed in line) vs deny-closed per-tool; RecordIO whether the bridged I/O is
	// anchored as ledger evidence; Critical whether it was a privileged launch.
	AgentRef       string `json:"agent_ref,omitempty"`
	PEPProvisioned bool   `json:"pep_provisioned"`
	RecordIO       bool   `json:"record_io"`
	ApprovalRef    string `json:"approval_ref,omitempty"`
	ApprovalURL    string `json:"approval_url,omitempty"`
	Critical       bool   `json:"critical"`

	// PendingApprovalRef is the existing human request this supervised run currently awaits.
	PendingApprovalRef string `json:"pending_approval_ref,omitempty"`

	// Work binding is references-only dispatch provenance. All fields are empty
	// for historical/ordinary runs; a work-launched run exposes the complete
	// generation stamp so operators can correlate it with its durable lease.
	WorkItemID      model.ID `json:"work_item_id,omitempty"`
	WorkLeaseFence  *int64   `json:"work_lease_fence,omitempty"`
	WorkDispatchKey string   `json:"work_dispatch_key,omitempty"`
	WorkOwnerEpoch  *int64   `json:"work_owner_epoch,omitempty"`
	// WorkLeaseState is the read-time control posture: active, ended or unknown.
	// It carries no lease details and never replaces effect-time fence checks.
	WorkLeaseState string `json:"work_lease_state,omitempty"`

	// Provider-profile facts (B1), persisted at launch: the profile, its driver and
	// its execution environment. References and labels only — the homes live on the
	// profile's authorized configuration read, never here. All empty for a legacy
	// run, which is never assigned a profile after the fact.
	ProviderProfileRef     string `json:"provider_profile_ref,omitempty"`
	ProviderDriver         string `json:"provider_driver,omitempty"`
	MCPGovernanceWarning   string `json:"mcp_governance_warning,omitempty"`
	ProviderEnvironmentRef string `json:"provider_environment_ref,omitempty"`
	// ProviderConversationID is the DRIVER-NEUTRAL name of the provider
	// conversation this run owns (Codex `thread.id`, Grok ACP `sessionId`, Claude's
	// init `session_id`). It is the same persisted value `claude_session_id`
	// carries; that older field stays for the clients already shipped against it,
	// and this one is what a non-Claude consumer should read.
	ProviderConversationID string `json:"provider_conversation_id,omitempty"`
	// ProviderAuthSource is the AUTHORIZED authentication source this run was
	// launched under; ProviderAuthState is the readiness the provider itself
	// reported. Neither is a credential, and the second is never derived from the
	// first: a home path and an injected value are not proof of an account.
	ProviderAuthSource string `json:"provider_auth_source,omitempty"`
	ProviderAuthState  string `json:"provider_auth_state,omitempty"`
	// ProviderInstance is the AI tools instance (GET /v1/m/agenttools/providers) whose
	// login this run uses, when it runs on the organization's own login of its tool;
	// absent for a run on a key or on homes of its own. A name, never a path.
	ProviderInstance string `json:"provider_instance,omitempty"`
	// What the governed turns of this session have cost, credited from the
	// provider's own result frames (runtime_usage.go). POINTERS, because the
	// difference between "the driver reported no usage" and "the turn was free" is
	// the whole point: an absent field is UNKNOWN and a zero would be a claim. A
	// session that has taken no turn yet carries none of them.
	//
	// InputTokens is the TOTAL input volume (uncached + cache-write + cache-read).
	// UsageModelRef is the model the provider reported using, which is not the
	// same fact as `model_ref` above — that one is what the launch ASKED for, and
	// on the golden path it was empty while the child answered on an explicit
	// model.
	InputTokens   *int64 `json:"input_tokens,omitempty"`
	OutputTokens  *int64 `json:"output_tokens,omitempty"`
	CostMicroUSD  *int64 `json:"cost_micro_usd,omitempty"`
	UsageModelRef string `json:"usage_model_ref,omitempty"`
	// ToolMode is the session's mode as the tool itself last reported it (Claude
	// Code's permission mode; Codex's approval policy and sandbox), beside the
	// permission_mode the launch asked for. Absent until the tool says.
	ToolMode string `json:"tool_mode,omitempty"`

	// LiveRef (B2) is the opaque id of the plane's managed live row for this run,
	// present only once the bridge proved the run owns its provider id. A console
	// navigates from a profiled run to its session by THIS, never by the bare
	// claude_session_id, which two homes may share.
	LiveRef string `json:"live_ref,omitempty"`

	// CanonicalSID is this session's canonical osn_ ID: what a peer send names and
	// what another run's `peers` holds (no read of a run showed it).
	CanonicalSID string `json:"canonical_sid,omitempty"`
	// CoreSessionID is the core Session of the current launch attempt, the row the
	// session cockpit lists. Empty before a first launch and on old runs.
	CoreSessionID string `json:"core_session_id,omitempty"`
}

// toRunDTO projects a run record, deriving the displayed state at read time.
func (m *Module) toRunDTO(rec model.Record) runDTO {
	out := runDTO{
		RunRef:                 rec.String(colRunRef),
		Peers:                  runPeers(rec),
		PeersRule:              rec.String(colRunPeersRule),
		Name:                   rec.String(colRunName),
		Transport:              rec.String(colTransport),
		PermissionMode:         rec.String(colPermissionMode),
		Effort:                 rec.String(colEffort),
		ModelRef:               rec.String(colRunModelRef),
		WorkspaceRef:           rec.String(colWorkspaceRef),
		AuthzWorkspaceID:       rec.String(colRunAuthzWorkspaceID),
		WorkspacePath:          rec.String(colRunWorkspacePath),
		WorktreeBranch:         rec.String(colRunWorktreeBranch),
		TemplateID:             rec.String(colTemplateID),
		TemplateVersion:        rec.Int(colTemplateVersion),
		SecretEnv:              storedSecretEnvNames(rec),
		GitRead:                rec.String(colRunGitRead),
		MaxDurationSecs:        rec.Int(colTemplateCeiling),
		Isolation:              rec.String(colIsolation),
		State:                  m.deriveRunState(rec),
		ProcessState:           rec.String(colState),
		WorkScope:              runWorkScope(rec),
		ClaudeSessionID:        rec.String(colClaudeSessionID),
		PID:                    intPtr(rec, colPID),
		CredentialID:           rec.String(colCredentialID),
		ExitCode:               intPtr(rec, colExitCode),
		Reason:                 rec.String(colReason),
		LastEventSeq:           rec.Int(colLastEventSeq),
		CreatedAt:              rec.String(model.ColCreatedAt),
		StartedAt:              rec.String(colStartedAt),
		LastActivityAt:         rec.String(colLastActivityAt),
		StoppedAt:              rec.String(colStoppedAt),
		AgentRef:               rec.String(colRunAgentRef),
		PEPProvisioned:         rec.Bool(colPEPProvisioned),
		RecordIO:               rec.Bool(colRecordIO),
		ApprovalURL:            approvalURL(rec.String(colApprovalRef)),
		ApprovalRef:            rec.String(colApprovalRef),
		PendingApprovalRef:     m.pendingRunApproval(rec),
		Critical:               rec.Bool(colCritical),
		WorkItemID:             model.ID(rec.String(colRunWorkItemID)),
		WorkLeaseFence:         intPtr(rec, colRunWorkLeaseFence),
		WorkDispatchKey:        hex.EncodeToString(rec.Bytes(colRunWorkDispatchKey)),
		WorkOwnerEpoch:         intPtr(rec, colRunWorkOwnerEpoch),
		ProviderProfileRef:     rec.String(colRunProfileID),
		ProviderDriver:         rec.String(colRunProfileDriver),
		ProviderEnvironmentRef: rec.String(colRunProfileEnvRef),
		ProviderConversationID: rec.String(colClaudeSessionID),
		ProviderAuthSource:     rec.String(colRunProviderAuthSource),
		ProviderAuthState:      rec.String(colRunProviderAuthState),
		LiveRef:                rec.String(colRunLiveRef),
		InputTokens:            intPtr(rec, colRunInputTokens),
		OutputTokens:           intPtr(rec, colRunOutputTokens),
		CostMicroUSD:           intPtr(rec, colRunCostMicroUSD),
		UsageModelRef:          rec.String(colRunUsageModelRef),
		ToolMode:               rec.String(colRunToolMode),
	}
	if sid := rec.String(colRunClaimSID); validCanonicalSID(sid) {
		out.CanonicalSID = sid
	}
	out.CoreSessionID = rec.String(colRunCoreSessionID)
	if m.usesOwnToolLogin(model.TenantID(rec.String(model.ColTenantID)), out.ProviderDriver, out.ProviderAuthSource, rec.String(colRunProfileConfigHome)) {
		out.ProviderInstance = driverfacts.OlivaresLoginInstance(out.ProviderDriver)
	}
	if runHasWorkBinding(rec) {
		out.WorkLeaseState = "unknown"
	}
	if out.ProviderDriver == providerDriverGrok || out.ProviderDriver == providerDriverOpenCode || out.ProviderDriver == providerDriverGemini {
		out.MCPGovernanceWarning = "MCP servers configured in this tool's own settings are not governed by Olivares."
	}
	return out
}

// deriveRunState derives the displayed state: a stored `running` session whose
// last activity is older than the idle window is shown as `idle` (the process is
// not killed). Every other stored state is shown verbatim.
func (m *Module) deriveRunState(rec model.Record) string {
	if rec.String(colState) != stateRunning {
		return rec.String(colState)
	}
	if t, err := model.ParseTimestamp(rec.String(colLastActivityAt)); err == nil {
		if m.now().Sub(t.Time()) > m.rt.idleWindow {
			return stateIdle
		}
	}
	return stateRunning
}

// runEventDTO is one lifecycle-ledger event (the queryable per-session chain). It
// exposes the PayloadHash anchor and the global-chain audit_seq so a consumer can
// cross-check the transition against the tamper-evident core audit ledger.
type runEventDTO struct {
	Seq            int64  `json:"seq"`
	At             string `json:"at"`
	Event          string `json:"event"`
	FromState      string `json:"from_state,omitempty"`
	ToState        string `json:"to_state,omitempty"`
	Detail         string `json:"detail,omitempty"`
	Actor          string `json:"actor,omitempty"`
	ActorKind      string `json:"actor_kind,omitempty"`
	PayloadHash    string `json:"payload_hash"`
	AuditSeq       int64  `json:"audit_seq"`
	WorkItemID     string `json:"work_item_id,omitempty"`
	WorkHolderSID  string `json:"work_holder_sid,omitempty"`
	WorkLeaseFence *int64 `json:"work_lease_fence,omitempty"`
	// P1: present only on a terminal event written after this change. The ID is the
	// generation that was retired; the observation says what was actually seen. An
	// unbound legacy recovery carries the observation and omits the ID.
	RetiredRuntimeLaunchID string `json:"retired_runtime_launch_id,omitempty"`
	TerminalObservation    string `json:"terminal_observation,omitempty"`
}

func toRunEventDTO(rec model.Record) runEventDTO {
	out := runEventDTO{
		Seq:           rec.Int(colEvSeq),
		At:            rec.String(colEvAt),
		Event:         rec.String(colEvEvent),
		FromState:     rec.String(colEvFromState),
		ToState:       rec.String(colEvToState),
		Detail:        rec.String(colEvDetail),
		Actor:         rec.String(colEvActor),
		ActorKind:     rec.String(colEvActorKind),
		PayloadHash:   rec.String(colEvPayloadHash),
		AuditSeq:      rec.Int(colEvAuditSeq),
		WorkItemID:    rec.String(colEvWorkItemID),
		WorkHolderSID: rec.String(colEvWorkSID),

		RetiredRuntimeLaunchID: rec.String(colEvRetiredLaunchID),
		TerminalObservation:    rec.String(colEvTerminalObservation),
	}
	if !rec.IsNull(colEvWorkFence) {
		fence := rec.Int(colEvWorkFence)
		out.WorkLeaseFence = &fence
	}
	return out
}

// createRunRequest is the POST /runs body.
//
// TemplateID is the ONLY template-shaped thing a caller may send. The restrictions
// themselves — the tool allowlist, the duration ceiling, the DLP floor — are read from
// the stored template by the server (templateapply.go), never accepted from the wire:
// a caller who could post an allowlist could post an empty one, and the body is
// exactly the surface the whole pack exists to stop being authoritative.
type createRunRequest struct {
	Name           string   `json:"name"`
	Transport      string   `json:"transport"`
	PermissionMode string   `json:"permission_mode"`
	Effort         string   `json:"effort"`
	Model          string   `json:"model"`
	WorkspaceRef   string   `json:"workspace_ref"`
	TemplateID     string   `json:"template_id"`
	Isolation      string   `json:"isolation"`
	EnvAllow       []string `json:"env_allow"`
	// Worktree opts the session into a git worktree and branch of its own, made from
	// the workspace's repository. Absent or false keeps today's default: the session
	// runs in the workspace folder itself (runtime_worktree.go).
	Worktree bool `json:"worktree"`
	// WorktreeFrom starts that worktree at a commit id or a local branch of the
	// workspace's repository instead of the workspace's HEAD (the work a handoff names).
	// Absent keeps today's start; it needs Worktree.
	WorktreeFrom string `json:"worktree_from"`
	// SecretEnv names vault secrets (env/…) the child receives as environment
	// variables. Names only; the server opens the values and needs tenant
	// administration from the caller.
	SecretEnv []SecretEnvRef `json:"secret_env"`
	// GitRead names one approved repository binding ("<binding id>:<owner>/<name>")
	// the session gets a short-lived GitHub read credential for. Needs tenant
	// administration, like SecretEnv.
	GitRead string `json:"git_read"`
	// ProviderProfileRef (B1) names the provider profile to launch under. Only the
	// reference: no home, environment, driver, binding, canonical sid, process
	// handle or authentication source is accepted from a client — the source is an
	// authorization held by the profile, and a request body cannot grant one.
	ProviderProfileRef string `json:"provider_profile_ref"`
}

// cleanupRunRequest is the optional POST /runs/{ref}/cleanup body; the endpoint has
// always accepted none, and it still takes any body: handleCleanupRun reads this
// leniently, so only an explicit true counts. DiscardWorktree confirms that the
// session's worktree and branch may be removed although the branch is not merged, the
// worktree holds uncommitted files or its HEAD is off the branch, and that a worktree
// this node cannot reach may be left in place; without it a release that would
// destroy or orphan that work is refused.
type cleanupRunRequest struct {
	DiscardWorktree bool `json:"discard_worktree"`
}

// inputRequest is the POST /runs/{ref}/input body (one NDJSON message to stdin).
type inputRequest struct {
	// Line is the raw NDJSON message to write to the process's stdin. Exactly one
	// of Line / Message is used; Message is JSON-encoded to a line for convenience.
	Line    string          `json:"line"`
	Message json.RawMessage `json:"message"`
	// Text is the input of a session driven by an OWNED provider protocol: the
	// driver turns it into that provider's own turn method. It is a separate field
	// because a raw frame and a turn are different things — writing a raw line onto
	// an RPC peer's stdin would be a method call, not an input. It may be sent WITH
	// work_lease_fence: a work-bound driver run is spoken to through the same fence
	// as its other controls, not through a second, unfenced door.
	Text           string `json:"text"`
	WorkLeaseFence *int64 `json:"work_lease_fence,omitempty"`
}

// stopRunRequest is optional for backward compatibility: the original stop
// endpoint accepted an empty body. A positive fence selects K2 work control;
// Reason is meaningful only on that fenced path.
type stopRunRequest struct {
	WorkLeaseFence *int64 `json:"work_lease_fence,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

// interruptRunRequest is optional for backward compatibility: the interrupt
// endpoint has always accepted an empty body, and for a non-work run that stays
// the whole contract. A positive fence selects the fenced control plane — the
// same plane the run's input and stop already use — so a work-bound run can have
// its TURN cancelled without ending its process.
type interruptRunRequest struct {
	WorkLeaseFence *int64 `json:"work_lease_fence,omitempty"`
}

// intPtr returns the column as *int64, or nil when the column is NULL (so the DTO
// distinguishes "no exit code yet" / "no pid" from a real 0).
func intPtr(rec model.Record, col string) *int64 {
	if rec.IsNull(col) {
		return nil
	}
	v := rec.Int(col)
	return &v
}

// decodeOptionalJSONBody decodes a request body that MAY be absent, leaving v at its
// zero value when it is. It is deliberately narrow: only a body that is entirely empty
// is tolerated, and anything present is held to decodeJSONBody's full strictness
// (bounded, unknown fields rejected, exactly one document).
//
// It exists because a route can gain a body without breaking the clients that were
// shipped before it had one — POST /templates/{id}/apply is called with no body by every
// generated SDK and by the console. The alternative, requiring the body, would turn a
// widening into a breaking change for a contract that is already published.
func decodeOptionalJSONBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Body == nil || r.ContentLength == 0 {
		return true
	}
	// Optional: a chunked/unknown-length body that turns out to be empty also
	// keeps v at its zero value.
	if err := api.DecodeRequestBody(w, r, v, api.RequestBodySpec{MaxBytes: 1 << 20, Optional: true}); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(api.RequestBodyErrorMessage(err, "invalid request body")))
		return false
	}
	return true
}

// decodeJSONBody decodes a bounded JSON request body with unknown-field rejection.
// It writes a 400 and returns false on any decode error.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := api.DecodeRequestBody(w, r, v, api.RequestBodySpec{MaxBytes: 1 << 20}); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(api.RequestBodyErrorMessage(err, "invalid request body")))
		return false
	}
	return true
}

// And K2 integration left two independent implementations of
// decodeOptionalJSONBody here. The retained version guarded a nil or zero-length
// body before decoding and used errors.Is(err, io.EOF) instead of direct equality
// to recognize wrapped EOF errors. The removed copy covered no additional case.
