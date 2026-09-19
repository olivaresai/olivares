// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/olivaresai/olivares/core/model"
)

// runDTO is the API shape of one operated Claude Code session. It carries NO
// secret, env value, prompt or transcript — only references and non-sensitive
// lifecycle facts (minimal-data, docs/SECURITY-HARDENING.md). `state` is the DERIVED state:
// stored `running` shows as `idle` when activity is stale (a read-time
// projection, never a stored flip-flop — mirrors the observe overlay's cc_state).
type runDTO struct {
	RunRef          string `json:"run_ref"`
	Name            string `json:"name,omitempty"`
	Transport       string `json:"transport"`
	PermissionMode  string `json:"permission_mode"`
	Effort          string `json:"effort,omitempty"`
	ModelRef        string `json:"model_ref,omitempty"`
	WorkspaceRef    string `json:"workspace_ref,omitempty"`
	// WorkspacePath is the HOST directory this session's child was started in: the
	// registered workspace's canonical root, or the directory of its own this plane
	// created under the data directory. It is shown by NAME because the walk of
	// 2026-09-18 could only answer "where is this session working" by reading the
	// child's own init frame — and the answer then was the engine's own directory.
	// Empty on a run that predates the column, which reads as "not recorded".
	WorkspacePath   string `json:"workspace_path,omitempty"`
	TemplateID      string `json:"template_id,omitempty"`
	TemplateVersion int64  `json:"template_version,omitempty"`
	MaxDurationSecs int64  `json:"max_duration_secs,omitempty"`
	Isolation       string `json:"isolation"`
	State           string `json:"state"`
	ClaudeSessionID string `json:"claude_session_id,omitempty"`
	PID             *int64 `json:"pid,omitempty"`
	CredentialID    string `json:"credential_id,omitempty"`
	ExitCode        *int64 `json:"exit_code,omitempty"`
	Reason          string `json:"reason,omitempty"`
	LastEventSeq    int64  `json:"last_event_seq"`
	CreatedAt       string `json:"created_at,omitempty"`
	StartedAt       string `json:"started_at,omitempty"`
	LastActivityAt  string `json:"last_activity_at,omitempty"`
	StoppedAt       string `json:"stopped_at,omitempty"`

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
	Critical       bool   `json:"critical"`

	// Work binding is references-only dispatch provenance. All fields are empty
	// for historical/ordinary runs; a work-launched run exposes the complete
	// generation stamp so operators can correlate it with its durable lease.
	WorkItemID      model.ID `json:"work_item_id,omitempty"`
	WorkLeaseFence  *int64   `json:"work_lease_fence,omitempty"`
	WorkDispatchKey string   `json:"work_dispatch_key,omitempty"`
	WorkOwnerEpoch  *int64   `json:"work_owner_epoch,omitempty"`

	// Provider-profile facts (B1), persisted at launch: the profile, its driver and
	// its execution environment. References and labels only — the homes live on the
	// profile's authorized configuration read, never here. All empty for a legacy
	// run, which is never assigned a profile after the fact.
	ProviderProfileRef     string `json:"provider_profile_ref,omitempty"`
	ProviderDriver         string `json:"provider_driver,omitempty"`
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

	// LiveRef (B2) is the opaque id of the plane's managed live row for this run,
	// present only once the bridge proved the run owns its provider id. A console
	// navigates from a profiled run to its session by THIS, never by the bare
	// claude_session_id, which two homes may share.
	LiveRef string `json:"live_ref,omitempty"`
}

// toRunDTO projects a run record, deriving the displayed state at read time.
func (m *Module) toRunDTO(rec model.Record) runDTO {
	return runDTO{
		RunRef:                 rec.String(colRunRef),
		Name:                   rec.String(colRunName),
		Transport:              rec.String(colTransport),
		PermissionMode:         rec.String(colPermissionMode),
		Effort:                 rec.String(colEffort),
		ModelRef:               rec.String(colRunModelRef),
		WorkspaceRef:           rec.String(colWorkspaceRef),
		WorkspacePath:          rec.String(colRunWorkspacePath),
		TemplateID:             rec.String(colTemplateID),
		TemplateVersion:        rec.Int(colTemplateVersion),
		MaxDurationSecs:        rec.Int(colTemplateCeiling),
		Isolation:              rec.String(colIsolation),
		State:                  m.deriveRunState(rec),
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
		ApprovalRef:            rec.String(colApprovalRef),
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
	}
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
	// ProviderProfileRef (B1) names the provider profile to launch under. Only the
	// reference: no home, environment, driver, binding, canonical sid, process
	// handle or authentication source is accepted from a client — the source is an
	// authorization held by the profile, and a request body cannot grant one.
	ProviderProfileRef string `json:"provider_profile_ref"`
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
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return true // a chunked/unknown-length body that turned out to be empty
		}
		writeJSON(w, http.StatusBadRequest, errorBody("invalid request body"))
		return false
	}
	if dec.More() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid request body"))
		return false
	}
	return true
}

// decodeJSONBody decodes a bounded JSON request body with unknown-field rejection.
// It writes a 400 and returns false on any decode error.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid request body"))
		return false
	}
	// A body is ONE JSON document. This helper is named decodeJSONBody rather than
	// decodeJSON, which is why the first version of check-json-decoders.sh — keyed on the
	// NAME — reported OK while this route accepted two and mutated on the first.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid request body"))
		return false
	}
	return true
}

// decodeOptionalJSONBody vivía DOS VECES en este fichero tras componer con K2, y las dos
// compilaban por separado. Se queda la de arriba porque es estrictamente más fuerte, no por ser
// la primera: guarda `r.Body == nil || r.ContentLength == 0` ANTES de tocar el decodificador —un
// cuerpo nil hace panic en el otro— y usa `errors.Is(err, io.EOF)` en vez de `err == io.EOF`, que
// no desenvuelve. La eliminada no aportaba ningún caso que la superviviente no cubra.
