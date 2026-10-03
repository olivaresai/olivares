// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The OPERATE entities adds to module II. They live in the SAME "sessions"
// namespace as the observe overlay (chose to extend module II), so their
// dotted Kind keeps the namespace hyphen-free and SQL-safe.
const (
	// runKind is one operated Claude Code session (mutable lifecycle row).
	runKind model.Kind = "sessions.run"
	// runEventKind is one immutable lifecycle-ledger event (per-session,
	// queryable, anchored in PayloadHash via the core audit chain).
	runEventKind model.Kind = "sessions.run_event"
)

// Physical tables (namespace_snake, ≤40 chars).
const (
	runTable      = "sessions_run"
	runEventTable = "sessions_run_event"
)

// sessions.run columns. NO column holds a secret, an env value, a prompt, a
// transcript, or process arguments (minimal-data, docs/SECURITY-HARDENING.md). The inference
// token is injected in-memory and discarded; only its non-sensitive id lands.
const (
	colRunQueuedUserID            = "queued_user_id"
	colRunQueuedCredentialID      = "queued_credential_id"
	colRunQueuedCredentialKind    = "queued_credential_kind"
	colRunQueuedCredentialVersion = "queued_credential_version"
	colRunQueuedCredentialSeal    = "queued_credential_seal"
	colRunQueuedActor             = "queued_actor"
	colRunQueuedActorKind         = "queued_actor_kind"
	colRunEnvAllow                = "env_allow_names"
	colRunSecretEnv               = "secret_env_names"
	colRunQueuedIntent            = "queued_intent"
	colRunRef                     = "run_ref"
	colRunPeers                   = "peers"
	colRunPeersRule               = "peers_rule"
	colRunName                    = "name"
	colTransport                  = "transport"
	colPermissionMode             = "permission_mode"
	colEffort                     = "effort"
	colRunModelRef                = "model_ref"
	colWorkspaceRef               = "workspace_ref"
	// colRunWorkspacePath is the EFFECTIVE working directory this run's child was
	// started in: the canonical root of the registered workspace it named, or the
	// directory of its own this plane created under the data directory when it
	// named none (runtime_workspace_dir.go). It is a path and never a secret, and
	// it is what makes "where did this session run" answerable instead of
	// inferable: on 2026-09-18 that question could only be answered by reading the
	// child's own init frame. Nullable: a run that predates the column reads as
	// "not recorded", which is honest, and is never assigned today's directory
	// after the fact.
	colRunWorkspacePath = "workspace_path"
	// colRunWorkspaceDirOwned is the OWNERSHIP fact the release purge acts on: did
	// THIS module create the directory named by colRunWorkspacePath, for THIS run?
	//
	// ⛔ IT IS NOT DERIVABLE FROM THE PATH, and that is the whole reason it exists.
	// The same column carries the canonical root of a REGISTERED workspace, and an
	// operator may register one that sits directly under this node's own session
	// root — at which point every string predicate the purge can make on the path
	// alone ("its parent is my root") says "mine" about the operator's project.
	//
	// ⛔ AND IT IS A COLUMN RATHER THAN A MARKER INSIDE THE DIRECTORY, deliberately.
	// A marker file would live where the session's own child process has write
	// access, and the directory this guard must never remove IS one the child can
	// write to — so a marker is forgeable by exactly the thing being guarded
	// against. This column is written by this module alone, on a row no session
	// reaches, and it survives a restart because the run table is durable.
	//
	// Nullable, and read DENY-CLOSED: absent or false means "not proven mine", and
	// an unproven directory is never removed. A row that predates the column
	// therefore leaks its directory rather than risking someone else's.
	colRunWorkspaceDirOwned = "workspace_dir_owned"
	colTemplateID           = "template_id"       // the workspace template whose terms govern this run
	colTemplateVersion      = "template_version"  // the REVISION of it this run was launched from
	colTemplateCeiling      = "max_duration_secs" // the session-duration ceiling this run was launched under
	colIsolation            = "isolation"
	colState                = "state"
	colClaudeSessionID      = "claude_session_id"
	colPID                  = "pid"
	colCredentialID         = "credential_id"
	colExitCode             = "exit_code"
	colReason               = "reason"
	colLastEventSeq         = "last_event_seq"
	colStartedAt            = "started_at"
	colLastActivityAt       = "last_activity_at"
	colStoppedAt            = "stopped_at"
	// Governance facts: the non-sensitive launch-decision posture persisted
	// on the run so the portal renders the per-session governance panel without the
	// secrets that decided it. References and flags only — never a token, env value,
	// prompt or transcript (minimal-data, docs/SECURITY-HARDENING.md). The column name "agent_ref"
	// matches the observe entity's column but lives on a different table; the Go const is
	// distinct (colAgentRef is the observe one).
	colRunAgentRef    = "agent_ref"       // the agent NHI dimension the kill-switch/budget scope on
	colPEPProvisioned = "pep_provisioned" // the managed PreToolUse PEP env was injected (tool-calls governed in line)
	colRecordIO       = "record_io"       // the bridged I/O is anchored as governed ledger evidence
	colApprovalRef    = "approval_ref"    // the HITL approval opened for a CRITICAL launch (empty otherwise)
	colCritical       = "critical"        // a privileged launch (drove the HITL + mandatory recording floor)

	// SG-02-b admission stamp: the claim under which this run was last launched. A
	// reference and a counter, never a secret.
	colClaimHolder = "claim_holder"
	colClaimFence  = "claim_fence"
	colRunClaimSID = "claim_sid"

	// P2/W2: the CORE workspace whose authority governs this run, resolved from
	// the run's own session identity at creation. It is the run's authorization
	// lineage, and it is deliberately NOT the free-text workspace_ref above —
	// that one is a launch input, this one is a resolved authority fact.
	//
	// ⛔ ITS UNSET MEANING IS THE OPPOSITE OF sessions.identity's, ON PURPOSE.
	// The identity descriptor declares WorkspaceUnsetMeansDefault, so an identity
	// minted before that column existed reads as the tenant default. A run does
	// not get that reading: the producer always writes an EXPLICIT id, including
	// the default workspace's own id, so NULL here means "no lawful lineage was
	// established" and a confined reader must see nothing at all. Copying the
	// neighboring table's spelling would silently turn every unresolved legacy
	// run into a run the default workspace owns.
	colRunAuthzWorkspaceID = "authz_workspace_id"

	// G/K3 dual runtime credentials. The bearer values never enter this table:
	// only the two revocation handles, their expiries, and the exact server-owned
	// binding needed to revoke them after a restart are durable.
	colCommunicationWorkspaceID  = "communication_workspace_id"
	colWorkCredentialID          = "work_credential_id"
	colWorkCredentialExpiresAt   = "work_credential_expires_at"
	colCommunicationCredentialID = "communication_credential_id"
	colCommunicationExpiresAt    = "communication_credential_expires_at"
	colRuntimeLaunchID           = "runtime_launch_id"

	// K2 work-kernel binding. These four columns are one nullable stamp: legacy
	// runs have all four NULL, while a work-launched run records the exact
	// WorkItem generation and lease authority under which it was dispatched.
	// The Go names are run-specific because work_schema.go already owns the
	// entity-level work_item_id and owner_epoch constants.
	colRunWorkItemID      = "work_item_id"
	colRunWorkLeaseFence  = "work_lease_fence"
	colRunWorkDispatchKey = "work_dispatch_key"
	colRunWorkOwnerEpoch  = "work_owner_epoch"
	// colRunWorkLaunchSpecHash binds an idempotent K4 dispatch reservation to
	// the complete semantic launch request. The dispatch key deliberately names
	// the WorkItem generation, not every runtime choice; this digest is what
	// makes reusing the same key with a different profile a conflict.
	colRunWorkLaunchSpecHash = "work_launch_spec_hash"

	// B1 provider-profile snapshot. The profile a run was launched under and the
	// NON-SECRET home snapshot resolved server-side BEFORE the spawn. All five are
	// one nullable stamp: a legacy run has them all NULL and is never assigned the
	// current HOME after the fact. The paths are persisted so a resume can prove
	// it continues on the SAME location (rename of the profile is fine, a moved
	// home is not); they are exposed only on the authorized configuration read,
	// never in the run list/detail DTOs. A snapshot, never a credential value.
	colRunProfileID         = "provider_profile_id"
	colRunProfileDriver     = "provider_driver"
	colRunProfileEnvRef     = "provider_environment_ref"
	colRunProfileConfigHome = "provider_config_home"
	colRunProfileUserHome   = "provider_user_home"
	// The AUTHORIZED authentication source this run was launched under, and the
	// readiness the provider itself reported. Two different things: the first is a
	// decision somebody made before the launch (and it travels in the K4 dispatch
	// digest), the second is an observation the child answered afterwards. Neither
	// is a credential, and neither is derived from a home path. Both NULL on a
	// legacy run and on a profile that names no source.
	colRunProviderAuthSource = "provider_auth_source"
	colRunProviderAuthState  = "provider_auth_state"
	// colRunProviderRecordRef (v26.10) is the PROVIDER RECORD this run's credential
	// was resolved from. It is a reference and never a value, it is written with
	// the rest of the snapshot BEFORE the spawn, and it is what makes "which
	// credential authorised this session" answerable after a rotation, a rebinding
	// or a revocation. NULL on a legacy run and on a profile that names none.
	colRunProviderRecordRef = "provider_record_ref"
	// colRunLiveRef (B2) is the id of the plane's MANAGED live row for this run,
	// written by the bridge in the transaction that proved the run owns its
	// announced provider id (runtime_profile.go). It is the run→row half of the
	// join whose row→run half is sessions_live.run_ref; both are persisted facts,
	// never a lookup by bare external id. NULL for a legacy run and for a profiled
	// run whose id was never captured.
	colRunLiveRef = "live_ref"

	// The metering of the governed turns this run has taken, credited from the
	// official CLI's own result frames (runtime_usage.go). Counts and money, never
	// a prompt or a completion: a token count is not content (docs/SECURITY-HARDENING.md).
	//
	// All four are NULLABLE and that is load-bearing, not expand-contract hygiene:
	// NULL means the driver reported NO usage, which is UNKNOWN. Zero would be a
	// claim that the turn was free, and what was measured on 2026-09-18 was the
	// opposite defect — a turn whose cost was on the wire and nowhere else.
	colRunInputTokens   = "input_tokens"
	colRunOutputTokens  = "output_tokens"
	colRunCostMicroUSD  = "cost_micro_usd"
	colRunUsageModelRef = "usage_model_ref"
)

// sessions.run_event columns (append-only ledger; per-session hash anchor).
const (
	colEvRunRef      = "run_ref"
	colEvSeq         = "seq"
	colEvAt          = "at"
	colEvEvent       = "event"
	colEvFromState   = "from_state"
	colEvToState     = "to_state"
	colEvDetail      = "detail"
	colEvActor       = "actor"
	colEvActorKind   = "actor_kind"
	colEvPayloadHash = "payload_hash"
	colEvAuditSeq    = "audit_seq"
	colEvWorkItemID  = "work_item_id"
	colEvWorkSID     = "work_holder_sid"
	colEvWorkFence   = "work_lease_fence"
	// P1: the runtime generation a terminal transition retired, and what was
	// actually observed about the process. Nullable, so every historical and
	// non-terminal event stays exactly as it was written.
	colEvRetiredLaunchID     = "retired_runtime_launch_id"
	colEvTerminalObservation = "terminal_observation"
)

// registerRuntimeSchema declares the two operate entities. The engine creates
// the tables, injects the base columns (id/tenant_id/created_at/updated_at/
// version) and attaches the tenant + append-only guards. Called from
// RegisterSchema (schema.go) alongside the observe entities.
func (m *Module) registerRuntimeSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  runKind,
		Table: runTable,
		Fields: []model.FieldSpec{
			{Name: colRunRef, Kind: model.KindText, Principal: pdeclNoneRunRef},
			// Nullable expansion: historical runs have no peer authority.
			{Name: colRunPeers, Kind: model.KindText, Nullable: true, Principal: model.Nested([]string{}, model.ClassAuthority, model.Leaf("[]", pdeclNoneSID))},
			{Name: colRunPeersRule, Kind: model.KindText, Nullable: true, Principal: model.None("the closed same-template peer selection rule, not an identity: session_peers.go:21")},
			{Name: colRunQueuedUserID, Kind: model.KindUUID, Nullable: true, Principal: model.Ref(model.EncodeUserID, model.ClassAuthority)},
			{Name: colRunQueuedActor, Kind: model.KindText, Nullable: true, Principal: model.Ref(model.EncodeUserRef, model.ClassAuthority)},
			{Name: colRunQueuedCredentialID, Kind: model.KindUUID, Nullable: true, Principal: model.Ref(model.EncodeCredentialID, model.ClassAuthority)},
			{Name: colRunQueuedCredentialKind, Kind: model.KindText, Nullable: true, Principal: model.None("credential discriminator: user session or API token, no identity: runtime.go:1253")},
			{Name: colRunQueuedCredentialVersion, Kind: model.KindInt, Nullable: true},
			{Name: colRunQueuedCredentialSeal, Kind: model.KindText, Nullable: true, Principal: model.None("irreversible SHA-256 of a credential verifier; never bearer material or an identity: runtime.go:1253")},
			{Name: colRunQueuedActorKind, Kind: model.KindText, Nullable: true, Principal: model.None("actor kind of the queued launch, not a principal reference: runtime_approval_wait.go:57")},
			{Name: colRunEnvAllow, Kind: model.KindText, Nullable: true, Principal: model.None("JSON array of permitted environment variable names; never their values: runtime.go:1332-1333")},
			{Name: colRunSecretEnv, Kind: model.KindText, Nullable: true, Principal: model.None("JSON array of {env, secret} NAMES of the vault secrets a run is given; never their values: session_secret_env.go:151, session_secret_env.go:165, runtime.go:1499")},
			{Name: colRunQueuedIntent, Kind: model.KindText, Nullable: true, Principal: model.None("references-only launch question pinned to the original approval: runtime_approval_wait.go:53")},
			{Name: colRunName, Kind: model.KindText, Nullable: true, Principal: model.None("an operator-chosen run label, shown only: runtime_dto.go:119")},
			{Name: colTransport, Kind: model.KindText, Principal: model.None("a run transport, a closed set: runtime_ports.go:39-49, runtime_dto.go:120")},
			{Name: colPermissionMode, Kind: model.KindText, Principal: pdeclNonePermissionMode},
			{Name: colEffort, Kind: model.KindText, Nullable: true, Principal: model.None("an effort level, a closed set: runtime_ports.go:98-100, runtime_dto.go:122")},
			{Name: colRunModelRef, Kind: model.KindText, Nullable: true, Principal: model.None("a model id the launch passes to the child, shown only: runtime_dto.go:123")},
			{Name: colWorkspaceRef, Kind: model.KindText, Nullable: true, Principal: model.None("the id of a registered host workspace, never an account: workspace.go:82, runtime_dto.go:124")},
			// Nullable for the expand-contract reason every stamp below gives: an
			// existing sessions_run gains it on the next boot (reconcileColumns) and a
			// row that predates it carries no recorded directory.
			{Name: colRunWorkspacePath, Kind: model.KindText, Nullable: true, Principal: model.None("the effective working directory path of the run, shown only: runtime_dto.go:125")},
			// Nullable for the same expand-contract reason, and read deny-closed: a run
			// that predates it cannot PROVE the directory is this plane's, so release
			// leaves it alone instead of removing a path it cannot account for.
			{Name: colRunWorkspaceDirOwned, Kind: model.KindBool, Nullable: true},
			// the template this run was last launched under. A reference, never the
			// template's body — the terms are re-resolved from the template row on every
			// launch and resume, so a tightened template governs the next relaunch rather
			// than a snapshot nobody can see going stale. Nullable: an untemplated run.
			{Name: colTemplateID, Kind: model.KindText, Nullable: true, Principal: model.None("the id of a workspace template row: runtime.go:1239, runtime_dto.go:126")},
			// The template's store version at launch. A template is MUTABLE, so its id alone
			// does not say what a running child was started under; with the revision an
			// operator can tell an edited template from the terms actually applied, and the
			// launch gate can bind a human approval to the revision it approved.
			{Name: colTemplateVersion, Kind: model.KindInt, Nullable: true},
			// The duration ceiling this run was launched under, in seconds. The timer that
			// ENFORCES it is in-process and does not survive a restart (expireRun says so);
			// persisting the value is what lets a later boot reconcile a child that outlived
			// its ceiling, and lets the panel state the limit rather than imply one.
			{Name: colTemplateCeiling, Kind: model.KindInt, Nullable: true},
			{Name: colIsolation, Kind: model.KindText, Principal: model.None("an isolation posture, a closed set: runtime_ports.go:70-72, runtime_dto.go:129")},
			{Name: colState, Kind: model.KindText, Indexed: true, Principal: pdeclNoneRunState},
			{Name: colClaudeSessionID, Kind: model.KindText, Nullable: true, Principal: model.None("the provider's own conversation id captured from the child's stream, shown only: runtime_bridge.go:180, runtime_dto.go:131")},
			{Name: colPID, Kind: model.KindInt, Nullable: true},
			{Name: colCredentialID, Kind: model.KindText, Nullable: true, Principal: model.None("the id of the per-run credential, a revocation handle and never the bearer value: runtime.go:2412, runtime_dto.go:133")},
			{Name: colExitCode, Kind: model.KindInt, Nullable: true},
			{Name: colReason, Kind: model.KindText, Nullable: true, Principal: model.None("the detail text of the last transition, shown only: runtime.go:2743, runtime_dto.go:135")},
			{Name: colLastEventSeq, Kind: model.KindInt},
			{Name: colStartedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colLastActivityAt, Kind: model.KindTimestamp, Nullable: true, Indexed: true},
			{Name: colStoppedAt, Kind: model.KindTimestamp, Nullable: true},
			// Governance facts, all nullable so a row that predates them reads
			// as the safe default (Record.Bool ⇒ false; missing text ⇒ "").
			{Name: colRunAgentRef, Kind: model.KindText, Nullable: true, Principal: model.None("the run's agent identity string, compared as part of the launch snapshot and shown, never an account: runtime.go:2072, runtime_communication_credential.go:706, runtime_dto.go:141")},
			{Name: colPEPProvisioned, Kind: model.KindBool, Nullable: true},
			{Name: colRecordIO, Kind: model.KindBool, Nullable: true},
			{Name: colApprovalRef, Kind: model.KindText, Nullable: true, Principal: model.None("the id of the approval opened for a critical launch, shown only: runtime.go:2073, runtime_dto.go:144")},
			{Name: colCritical, Kind: model.KindBool, Nullable: true},
			// SG-02-b: the admission stamp. The claim this run is operating under, written
			// under that claim's own authority at launch, so a later governed write has a
			// DURABLE thing to compare the live claim against instead of re-reading the
			// current fence and comparing it with itself. Nullable: NULL means a run that
			// predates the control, which the next launch adopts and stamps once.
			{Name: colClaimHolder, Kind: model.KindText, Nullable: true, Principal: pdeclActorRef},
			{Name: colClaimFence, Kind: model.KindInt, Nullable: true},
			{Name: colRunClaimSID, Kind: model.KindText, Nullable: true, Principal: pdeclNoneSID},
			// Nullable for the same expand-contract reason as the stamp above: an
			// existing sessions_run gains it on the next boot (reconcileColumns),
			// and a row that predates it carries no lawful lineage.
			{Name: colRunWorkScope, Kind: model.KindText, Nullable: true, Principal: model.None("references-only work authority snapshot for the current launch; never a bearer or live authorization verdict: modules/sessions/runtime_work_scope.go:57")},
			{Name: colRunAuthzWorkspaceID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the core workspace id of the run's authorization lineage, never an account: runtime_schema.go:319-323, identity_read.go:158")},
			{Name: colCommunicationWorkspaceID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the core workspace id a communication credential was bound to: runtime_communication_credential.go:325, runtime_communication_credential.go:511")},
			{Name: colWorkCredentialID, Kind: model.KindUUID, Nullable: true, Principal: pdeclNoneRuntimeCredentialHandle},
			{Name: colWorkCredentialExpiresAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colCommunicationCredentialID, Kind: model.KindUUID, Nullable: true, Principal: pdeclNoneRuntimeCredentialHandle},
			{Name: colCommunicationExpiresAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colRuntimeLaunchID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the run's current launch generation: runtime_communication_credential.go:282, managed_stop_phases.go:497")},
			// K2: nullable is an expand-contract requirement for historical runs.
			{Name: colRunWorkItemID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the work item a work launch was dispatched for: runtime_work_lease.go:315, runtime_dto.go:146")},
			{Name: colRunWorkLeaseFence, Kind: model.KindInt, Nullable: true},
			{Name: colRunWorkDispatchKey, Kind: model.KindBytes, Nullable: true, Principal: model.None("the digest key of a work dispatch reservation: runtime_work_lease.go:333, runtime_dto.go:148")},
			{Name: colRunWorkOwnerEpoch, Kind: model.KindInt, Nullable: true},
			{Name: colRunWorkLaunchSpecHash, Kind: model.KindBytes, Nullable: true, Principal: model.None("a SHA-256 of the complete launch request of a work dispatch, compared only for replay: runtime.go:2435, runtime_work_launch.go:158")},
			// B1: nullable so an existing sessions_run gains them on the next boot
			// (reconcileColumns) and a pre-profile row reads as "no profile".
			{Name: colRunProfileID, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclNoneProfileRef},
			{Name: colRunProfileDriver, Kind: model.KindText, Nullable: true, Principal: pdeclNoneDriverKey},
			{Name: colRunProfileEnvRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneEnvRef},
			{Name: colRunProfileConfigHome, Kind: model.KindText, Nullable: true, Principal: pdeclNoneHomePath},
			{Name: colRunProfileUserHome, Kind: model.KindText, Nullable: true, Principal: pdeclNoneHomePath},
			// Nullable for the same expand-contract reason as the B1 stamp above: an
			// existing sessions_run gains them on the next boot (reconcileColumns) and a
			// row that predates them reads as "no authorized source, readiness unknown".
			{Name: colRunProviderAuthSource, Kind: model.KindText, Nullable: true, Principal: pdeclNoneAuthSource},
			{Name: colRunProviderAuthState, Kind: model.KindText, Nullable: true, Principal: model.None("the provider's reported authentication readiness, a closed set: runtime_provider_auth.go:55-57, runtime_driver.go:893")},
			{Name: colRunProviderRecordRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneProviderRecordRef},
			{Name: colRunLiveRef, Kind: model.KindText, Nullable: true, Principal: model.None("the id of the plane's managed live row for the run: runtime_profile.go:337, runtime_dto.go:156")},
			// Nullable = UNKNOWN, never zero: see the constants. A run that predates
			// these columns gains them on the next boot (reconcileColumns) and reads as
			// "the provider reported no usage", which is what was true.
			{Name: colRunInputTokens, Kind: model.KindInt, Nullable: true},
			{Name: colRunOutputTokens, Kind: model.KindInt, Nullable: true},
			{Name: colRunCostMicroUSD, Kind: model.KindInt, Nullable: true},
			{Name: colRunUsageModelRef, Kind: model.KindText, Nullable: true, Principal: model.None("the model id reported with the run's usage, shown only: runtime_usage.go:374, runtime_dto.go:160")},
		},
		// The run's authorization lineage. Unset is HIDDEN, never the tenant
		// default: see colRunAuthzWorkspaceID. Declaring it is also what turns a
		// confined Ext(runKind) from a refusal into a filter.
		WorkspaceLineage: model.WorkspaceLineageSpec{
			Column:   colRunAuthzWorkspaceID,
			Encoding: model.WorkspaceLineageID,
			Unset:    model.WorkspaceUnsetHidden,
		},
		Indexes: []model.IndexSpec{
			{
				Name:    "sessions_run_ref_uniq",
				Columns: []string{model.ColTenantID, colRunRef},
				Unique:  true,
			},
			{
				Name:    "sessions_run_dispatch_key_uniq",
				Columns: []string{model.ColTenantID, colRunWorkDispatchKey},
				Unique:  true,
			},
		},
	}); err != nil {
		return err
	}
	return reg.Register(model.EntityDescriptor{
		Kind:       runEventKind,
		Table:      runEventTable,
		AppendOnly: true, // immutability: no UPDATE/DELETE (engine triggers/grants)
		WorkspaceInheritedRead: model.WorkspaceInheritedReadSpec{
			ParentKind: runKind, ParentColumn: colRunRef, Column: colEvRunRef,
		},
		Fields: []model.FieldSpec{
			{Name: colEvRunRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneRunRef},
			{Name: colEvSeq, Kind: model.KindInt},
			{Name: colEvAt, Kind: model.KindTimestamp},
			{Name: colEvEvent, Kind: model.KindText, Principal: model.None("a lifecycle event name chosen by this module: runtime_ledger.go:189, runtime_dto.go:207")},
			{Name: colEvFromState, Kind: model.KindText, Nullable: true, Principal: pdeclNoneRunState},
			{Name: colEvToState, Kind: model.KindText, Nullable: true, Principal: pdeclNoneRunState},
			{Name: colEvDetail, Kind: model.KindText, Nullable: true, Principal: model.None("transition detail text, shown only: runtime_ledger.go:196, runtime_dto.go:210")},
			{Name: colEvActor, Kind: model.KindText, Nullable: true, Principal: pdeclActorRef},
			{Name: colEvActorKind, Kind: model.KindText, Nullable: true, Principal: model.None("the caller's audit actor kind label, never an account: runtime_api.go:525, runtime_ledger.go:198, runtime_dto.go:212")},
			{Name: colEvPayloadHash, Kind: model.KindText, Principal: model.None("the hex SHA-256 anchoring the event in the audit chain: runtime_ledger.go:151, runtime_ledger.go:190")},
			{Name: colEvAuditSeq, Kind: model.KindInt},
			// K2: complete generation under which a fenced runtime action was
			// settled. Nullable keeps historical/non-work events compatible.
			{Name: colEvWorkItemID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the work item whose lease generation settled the action: runtime_ledger.go:200, runtime_dto.go:215")},
			{Name: colEvWorkSID, Kind: model.KindText, Nullable: true, Principal: pdeclNoneSID},
			{Name: colEvWorkFence, Kind: model.KindInt, Nullable: true},
			// P1: which launch generation this terminal event retired, and what the
			// owner actually observed about that process. The engine's descriptor
			// reconciler adds both to an existing database before module SQL runs, so
			// no migration file declares them and no append-only row is rewritten.
			{Name: colEvRetiredLaunchID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the launch generation a terminal event retired: runtime_ledger.go:89-98, runtime_ledger.go:209, runtime_dto.go:218")},
			{Name: colEvTerminalObservation, Kind: model.KindText, Nullable: true, Principal: model.None("a terminal process observation, a closed set: runtime_ledger.go:63-69, runtime_ledger.go:99")},
		},
	})
}

// Principal declarations of the operated-run descriptors. The run row and its
// ledger name sessions, profiles, credentials and work by opaque ids; the only
// principal-bearing columns are the actor strings, kept as evidence.
var (
	pdeclNoneRunState                = model.None("a run lifecycle state, a closed set: runtime_ports.go:77-84, runtime_dto.go:168-169, runtime_dto.go:208-209")
	pdeclNoneRuntimeCredentialHandle = model.None("the id of a runtime credential, a revocation handle and never the bearer value or an account: runtime_communication_credential.go:513-515, runtime_communication_credential.go:560-565")
)
