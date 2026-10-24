// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for the Claude Code OPERATE portal (FASE V) — a 1:1 mirror of the
// modules/sessions OPERATE surface (runtime workspace governance).
// The web RENDERS these; it computes no product math (ARCHITECTURE.md). Minimal-data
// (docs/SECURITY-HARDENING.md): only references, flags, classifications and counters cross the wire —
// never a secret, token, env value, prompt, transcript or file bytes (file content is
// fetched hot through the governed read API and never persisted).

/** A run's stored lifecycle state (the runtime OWNS the process, so it knows). `idle`
 * is DERIVED at read time from activity recency — the process is not killed. */
export type RunState =
  | 'pending'
  | 'running'
  | 'idle'
  | 'stopped'
  | 'failed'
  | 'cleaned'
  | (string & {})

/** How Olivares drives the `claude` process. `stream-json` is the GOVERNED default
 * (bidirectional NDJSON bridged in full); `remote-control` is LIFECYCLE-ONLY — its I/O
 * is relayed to Anthropic's cloud, so Olivares manages the process but cannot show it. */
export type Transport = 'stream-json' | 'remote-control' | (string & {})

/** The launched process's containment posture. Only `native` is wired this release;
 * `container`/`sandbox` are forward-compat seam values the runtime refuses. */
export type Isolation = 'native' | 'container' | 'sandbox' | (string & {})

/** Claude Code `--permission-mode` values (verified against the deployed binary). */
export type PermissionMode =
  'default' | 'acceptEdits' | 'plan' | 'auto' | 'dontAsk' | 'bypassPermissions'

export const PERMISSION_MODES: PermissionMode[] = [
  'default',
  'acceptEdits',
  'plan',
  'auto',
  'dontAsk',
  'bypassPermissions',
]

/** `--effort` levels (GA set). `ultracode` is a session-orchestration mode, NOT an
 * effort value, so it is deliberately absent (matches the backend validation). */
export type Effort = 'low' | 'medium' | 'high' | 'xhigh' | 'max'

export const EFFORT_LEVELS: Effort[] = ['low', 'medium', 'high', 'xhigh', 'max']

/** A permission mode is CRITICAL (drives the HITL + recording floor) when it removes
 * the per-tool prompt. Used to warn before launch — the backend is the source of truth. */
export const CRITICAL_PERMISSION_MODES: PermissionMode[] = [
  'dontAsk',
  'bypassPermissions',
]

/** The answer of POST and DELETE /runs/{ref}/pin: the signed-in person's pin state. */
export interface RunPinDTO {
  run_ref: string
  pinned: boolean
  pinned_at?: string
}

/** The answer of the snooze and done routes: the signed-in person's two marks. */
export interface RunMarkDTO {
  run_ref: string
  snoozed_until?: string
  done: boolean
}

/** One operated Claude Code session (GET /runs, /runs/{ref}). */
export interface RunDTO {
  run_ref: string
  name?: string
  /**
   * THE CALLER'S OWN pin on this session, as the engine reads it for the signed-in
   * person: true when they pinned it, with `pinned_at` the moment. Another person's
   * pin never shows here. Absent on answers that do not look the pin up (a stop or a
   * resume), which is not "unpinned".
   */
  pinned?: boolean
  /** When the session was archived: out of the default list, under Archived. */
  archived_at?: string
  pinned_at?: string
  /**
   * THE CALLER'S OWN snooze and done marks, read like the pin. `snoozed_until` is
   * present only while the snooze is still to end: the engine stops sending it at that
   * moment, so the session comes back without a clock here.
   */
  snoozed_until?: string
  done?: boolean
  transport: Transport
  permission_mode: PermissionMode | string
  effort?: Effort | string
  model_ref?: string
  fast_mode?: boolean
  service_tier?: string
  workspace_ref?: string
  /** The workspace the run is authorized in (MC 850beb3d), separate from its folder;
   * omitted when the engine recorded none. */
  authz_workspace_id?: string
  /**
   * The HOST directory this session's child was started in: the registered
   * workspace's canonical root, or the directory of its own the engine created for
   * the run. It is shown by NAME because until v26.10 a session with no workspace
   * ran in the ENGINE's own working directory and nothing on any surface said so.
   */
  workspace_path?: string
  /** The branch of the session's own git worktree, when it was launched with the
   * "new worktree" option; absent for a session that works in its folder directly. */
  worktree_branch?: string
  /** The session this one was handed off from, when a handoff started it. */
  handoff_of?: string
  /** The workspace template this run was last launched under, if any.*/
  template_id?: string
  /** The vault secrets this session receives as environment variables, by name. */
  secret_env?: SecretEnvRef[]
  isolation: Isolation
  state: RunState
  /** Stored process lifecycle, separate from output-derived activity. Absent on older servers. */
  process_state?: string
  /** References-only authority snapshot of the last admitted launch; not a live verdict. */
  work_scope?: {
    role: 'worker' | 'orchestrator'
    workspace_id: string
    capabilities?: SessionWorkCapability[]
    grant_id?: string
  }
  claude_session_id?: string
  pid?: number
  credential_id?: string
  exit_code?: number
  reason?: string
  last_event_seq: number
  created_at?: string
  started_at?: string
  last_activity_at?: string
  stopped_at?: string

  /** MC: present only for a Grok Build or OpenCode run, whose own settings can name MCP
   * servers Olivares does not govern. The engine's sentence, shown as-is. */
  mcp_governance_warning?: string

  // Optional K2 authority links; legacy and non-work runs omit all four.
  work_item_id?: string
  work_lease_fence?: number
  work_dispatch_key?: string
  work_owner_epoch?: number
  /** Whether the lease the stamp names is held now (MC): `ended` once it is released,
   * expired or revoked; `unknown` when the engine could not read it. Omitted with no
   * binding. */
  work_lease_state?: 'active' | 'ended' | 'unknown'

  // Governance posture persisted on the run — the panel renders these.
  /** The agent NHI dimension the kill-switch / budget scope on (empty for a user actor). */
  agent_ref?: string
  /** The managed PreToolUse hook reaches the governed PEP (tool-calls policed in line)
   * vs deny-closed per-tool when false. */
  pep_provisioned: boolean
  /** The bridged I/O is anchored as governed ledger evidence. */
  record_io: boolean
  /** The HITL approval opened for a CRITICAL launch (deep-linkable).*/
  approval_ref?: string
  /** The approval a tool call of this run is waiting on right now (PEP), absent when
   * nothing waits. Requested from PEP for HU-R12: the run stays `running` while a hook
   * call waits, so without it the console cannot say the session needs a person. */
  pending_approval_ref?: string
  /** Set while the run's turn was interrupted because it spent its budget of policy
   * denials and no operator has answered yet; absent otherwise. */
  needs_person?: {
    code: 'repeated_policy_denials'
    consecutive_denials: number
    total_denials: number
    reasons: string[]
  }
  /** The canonical session ids this run may message or hand work to (COMMS-PATH, MC). */
  peers?: string[]
  /** "same-template": every live session of the run's template, resolved at each call. */
  peers_rule?: 'same-template'
  /** A privileged launch (drove the HITL + mandatory recording floor). */
  critical: boolean
  /** What the session reaches (#2545): its folders with their access, and the hosts it
   * may connect to besides the engine. `reason` says why a part could not be read. */
  reaches?: {
    folders: { path: string; access: 'write' | 'read' | 'unknown' }[]
    hosts: string[]
    reason?: string
  }

  // Provider-profile facts, persisted at launch. References only — the
  // homes live on the profile's authorized configuration read, never here. All
  // absent for a legacy run, which is never assigned a profile after the fact.
  provider_profile_ref?: string
  provider_driver?: string
  provider_environment_ref?: string
  /** The provider conversation this run owns, under a DRIVER-NEUTRAL name (Codex
   * `thread.id`, Grok ACP `sessionId`, Claude's init `session_id`). It is the same
   * value `claude_session_id` carries; that field stays for the clients already
   * shipped against it, and this is what a non-Claude consumer should read. */
  provider_conversation_id?: string
  /** The AUTHORIZED authentication source this run was launched under. An
   * authorization, never a credential. */
  provider_auth_source?:
    'provider_account_home' | 'managed_injection' | (string & {})
  /** The readiness the PROVIDER itself reported, separate from process,
   * conversation and turn state. A home path is not proof of an account. */
  provider_auth_state?: 'unknown' | 'required' | 'ready' | (string & {})
  /** The AI tools instance (GET /agenttools/providers) whose login this run uses,
   * when it runs on the organization's own login of its tool ("claude/olivares"). */
  provider_instance?: string
  /** The session's mode as the TOOL last reported it (Claude Code's permission mode;
   * Codex's approval policy and sandbox), beside the `permission_mode` the launch
   * asked for. Absent until the tool says. */
  tool_mode?: string
  /** The usage or rate limit the tool ended its last turn with, from the tool's own
   * result. `resets_at` (RFC 3339) is when the tool says it lifts; absent when it
   * names none. Absent altogether until a turn ends on a limit, and again after a
   * later turn succeeds. */
  limit?: RunLimit
  /** What this session's turns metered, summed over its launches. ABSENT means the
   * tool reported nothing, which is unknown, never zero; a cost is the tool's own
   * figure or the list price of a turn it did not price: an estimate. */
  input_tokens?: number
  output_tokens?: number
  cost_micro_usd?: number
  usage_model_ref?: string
  /** The opaque id of the plane's MANAGED live row for this run, present once the
   * bridge proved the run owns its provider id. A profiled run is joined to its
   * session by THIS, never by the bare `claude_session_id` two homes may share. */
  live_ref?: string
  /** The canonical session this run operates (`osn_…`): what an approval's
   * `session_ref` names. */
  canonical_sid?: string
  /** Who the session acts for (the run read's session facts, #2545). Absent on an
   * engine older than the facts. */
  acts_for?: RunActsFor
  /** The audit ledger's evidence for the session; `chain_verified` is the tenant's
   * whole chain, read incrementally by the engine. */
  proof?: RunProof
}

/** A person launched it, a work item or agent dispatched it, or it predates the record. */
export interface RunActsFor {
  kind: 'user' | 'automation' | 'unknown'
  user_id?: string
  name?: string
  reason?: string
}

/** Null values come with a `reason`; null is never "no" or 0. */
export interface RunProof {
  ledger_entries: number | null
  chain_verified: boolean | null
  reason?: string
}

/** A limit a tool ended a turn on: the account's plan ran out (`usage`), or the
 * provider is throttling requests for a moment (`rate`). */
export interface RunLimit {
  kind: 'usage' | 'rate'
  resets_at?: string
  account_ref?: string
}

/** One provider profile (GET /provider-profiles): the durable identity of ONE
 * configured provider instance on ONE execution environment. Configuration and
 * storage identity — never an authenticated provider account. No path here: the
 * homes are on the admin-only configuration read. */
export type ProviderAccentName = 'orange' | 'green' | 'amber' | 'red' | 'blue'
export interface ProviderAccountMetadataPatch {
  /** The account's name (lowercase letters, digits and dashes). Absent keeps it. An engine
   * that does not know the field refuses (400 or 422) or ignores it. */
  name?: string
  display_name?: string
  accent?: ProviderAccentName | ''
}

export interface ProviderProfileDTO {
  profile_ref: string
  driver: string
  environment_ref: string
  display_name?: string
  accent?: ProviderAccentName | (string & {})
  state: 'active' | 'disabled' | 'retired' | (string & {})
  /** Belongs to THIS node's execution environment; a foreign one is shown as such
   * and never launched here. */
  local_environment: boolean
  /** Legacy enablement flag, preserved with its exact meaning: local, active, a
   * driver this runtime has REGISTERED, and — for a driver that needs one — an
   * authorized authentication source. It is not a launch guarantee and is not
   * the source of the requirements panel; that is GET …/launch-readiness. */
  operable: boolean
  /** An own-login profile that points at this server's user login (the engine
   * user's own ~/.claude and the like, how profiles were made before FH 036). It is
   * not launched; sign in again to use a product login. */
  server_user_login?: boolean
  /** The AUTHORIZED authentication source: `provider_account_home` uses the saved
   * login inside the profile's own homes, `managed_injection` mints a
   * provider-compatible credential through that driver's governed adapter. They
   * are distinct authorizations with no fallback; absent authorizes neither. */
  auth_source?: 'provider_account_home' | 'managed_injection' | (string & {})
  /** The registered PROVIDER this profile's managed launches resolve their
   * credential from. Absent binds no registered provider; auth_source still governs
   * launch authentication. It is a reference: no key, no hint, no endpoint travels
   * with it. */
  provider_record_ref?: string
  /** The name of a profile that is an account (`claude`, `claude-b`). Absent on the default
   * login, which has no name, and on an engine that does not give names yet. */
  account_name?: string
  session_work_grant?: SessionWorkGrantDTO | null
  /** The programs every session under this profile gets first on PATH. */
  session_programs?: string[]
  /** The npm registries those sessions install from; `secret` names a vault entry,
   * never a token. */
  session_registries?: {
    kind: string
    url: string
    scope?: string
    secret?: string
  }[]
  /** The hosts a confined session may reach beside its provider. */
  session_egress_hosts?: string[]
  created_at?: string
  updated_at?: string
  retired_at?: string
}

/** What a profile is called: the label it was given, else the name of the account it is
 * (`claude-b`). Undefined for a profile nobody has named; the caller then falls back to
 * what suits its place (the reference, the tool). */
export function profileName(
  profile: Pick<ProviderProfileDTO, 'display_name' | 'account_name'>,
): string | undefined {
  return (
    profile.display_name?.trim() || profile.account_name?.trim() || undefined
  )
}

/** A profile as a person reads it: the name it was given, else its tool and the end of
 * its reference, so two unnamed accounts of one tool are still told apart. */
export function profileLabel(
  profile: Pick<
    ProviderProfileDTO,
    'display_name' | 'account_name' | 'driver' | 'profile_ref'
  >,
): string {
  return (
    profileName(profile) ?? `${profile.driver} ${profile.profile_ref.slice(-6)}`
  )
}

/** The ledger names a profile by its opaque reference, because the record is
 * append-only and an account name can be an email address. Showing it, the console
 * puts the name the person gave the account in its place; a reference it cannot
 * resolve (a profile retired or not readable) stays as it is. */
export function nameProfileRefs(
  detail: string,
  names: ReadonlyMap<string, string>,
): string {
  return detail.replace(/ppf_[0-9a-f-]{36}/g, (ref) => names.get(ref) ?? ref)
}

/** The ledger events whose detail names profiles. */
export function namesProfiles(event: string): boolean {
  return event === 'moved' || event === 'handed_off'
}

export type SessionWorkCapability =
  | 'work.read'
  | 'work.create'
  | 'work.assign'
  | 'work.review'
  | 'decision.read'
  | 'decision.write'

export interface SessionWorkGrantRequest {
  role: 'orchestrator'
  workspace_id: string
  capabilities: SessionWorkCapability[]
}

export interface SessionWorkGrantDTO extends SessionWorkGrantRequest {
  /** Output-only generation; clients must omit it when replacing the grant. */
  grant_id: string
}

/** The AUTHORIZED configuration read of one profile (GET
 * /provider-profiles/{ref}/configuration, `sessions:profile:admin`): the canonical
 * homes AS STORED, resolved by the execution environment that owns them. This is the
 * only read that carries a path, it is fetched on demand and never on render, and it
 * still carries no credential value. */
export interface ProviderProfileConfigurationDTO {
  profile_ref: string
  driver: string
  environment_ref: string
  config_home: string
  user_home: string
  state: 'active' | 'disabled' | 'retired' | (string & {})
}

/** POST /provider-profiles body. The homes are EXISTING directories on THIS node's
 * execution environment: the server resolves and validates them (absolute, symlinks
 * resolved, existing, a directory) and never creates, installs or logs anything in.
 * `environment_ref` may be omitted (this node) or must equal it; a profile for another
 * environment is created on that environment. No credential travels here. */
export interface CreateProfileRequest {
  driver: string
  config_home: string
  user_home: string
  display_name?: string
  environment_ref?: string
  /** The authorized authentication source. Absent authorizes neither, which
   * refuses every launch whose driver requires one. */
  auth_source?: 'provider_account_home' | 'managed_injection' | (string & {})
  /** Bind a registered provider in the same authorized call, so deploying an
   * agent is one step rather than a checklist. */
  provider_record_ref?: string
  session_work_grant?: SessionWorkGrantRequest | null
}

/** The provider record a resolved profile runs on, by reference (never a value). */
export interface ResolveProviderSource {
  provider_ref: string
  kind: string
  display_name?: string
  default_model?: string
  /** The models the key's last connection test listed: what a session may pick. */
  models?: string[]
}

/** GET /provider-profiles/resolve: what a new session of a tool would run on —
 * its own login, or the key or local model from Providers the engine's one rule
 * picks. Nothing is created; a tool with nothing to run on is refused (409) with
 * the sentence that says what to add. */
export interface ProfilePreviewDTO {
  reason: 'own_login' | 'api_key'
  provider?: ResolveProviderSource
  /** The tool cannot start on this key without a model, its own default included. Absent on
   * an engine that does not say. */
  model_required?: boolean
  /** The profile the session would reuse; absent when the launch would create one
   * (which declares no programs or registries). */
  profile?: ProviderProfileDTO
}

/** GET /provider-profiles/readiness: whether a new session can start on each tool the
 * engine drives. A tool that cannot carries a stable `code` and the one sentence that
 * says why (`message`); a key the provider refused at its last test is `key_refused`,
 * with the key in `provider`. */
export interface ToolReadinessDTO {
  driver: string
  ready: boolean
  reason?: 'own_login' | 'api_key'
  provider?: ResolveProviderSource
  model_required?: boolean
  code?: string
  message?: string
}

/** One model of a profile's live catalog (GET /provider-profiles/{ref}/models). Every
 * marker and capability is present only when the tool reports it: an absent one is
 * unknown, never false. */
export interface ProfileModelDTO {
  id: string
  display_name?: string
  effort_levels?: string[]
  default_effort?: string
  is_default?: boolean
  is_new?: boolean
  is_legacy?: boolean
  supports_fast_mode?: boolean
  service_tiers?: string[]
}

/** GET /provider-profiles/{ref}/models: the models and effort levels the installed tool
 * or the bound provider of one profile reports now. `fresh` is a real answer; `unsupported`
 * and `failed` leave free text and the tool's own default. An empty `default_model` is the
 * tool's own default. */
export interface ProfileModelCatalogDTO {
  profile_ref: string
  driver: string
  state: 'fresh' | 'unsupported' | 'failed'
  models: ProfileModelDTO[]
  default_model: string
  free_text: boolean
  message?: string
  checked_at: string
  refresh_interval_seconds: number
}

/** POST /provider-profiles/resolve: the same answer, as the profile the session
 * runs under (reused, or created with the server's own homes). */
export interface ResolvedProfileDTO extends ProfilePreviewDTO {
  profile: ProviderProfileDTO
  created: boolean
}

/** PATCH /provider-profiles/{ref} body — the ONLY post-creation mutation: a label
 * and/or an active↔disabled transition. Retirement is irreversible and has its own
 * admin route (POST …/retire); driver, environment and homes are identity and are not
 * here. */
export interface PatchProfileRequest {
  display_name?: string
  state?: 'active' | 'disabled'
  auth_source?: 'provider_account_home' | 'managed_injection' | (string & {})
  /** Bind (or unbind, with "") the registered provider this profile's managed
   * launches use. A LIVE child keeps the one its own launch resolved. */
  provider_record_ref?: string
  /** Omitted preserves the grant; explicit null revokes its session authority. */
  session_work_grant?: SessionWorkGrantRequest | null
}

/** One source→profile binding (GET /provider-source-bindings): ONE configured source,
 * at ONE applied revision, on ONE execution environment, dedicated to ONE profile.
 * Identity is the source's persistent id + applied revision — never its editable
 * name (`source_name` is an informational snapshot taken at bind time). Rows are
 * immutable except for revocation. */
export interface ProviderBindingDTO {
  binding_ref: string
  source_id: string
  source_revision: number
  source_name?: string
  environment_ref: string
  selector_key: string
  profile_ref: string
  driver: string
  state: 'active' | 'revoked' | (string & {})
  bound_at: string
  revoked_at?: string
}

/** POST /provider-source-bindings body. `source_id` is the roster row's persistent id
 * and `source_revision` the EXACT revision this node applied, both read from the
 * source roster (`/v1/console/sources`: `id`, `applied_revision`) — a client never
 * derives either from a name or a stored version. Creating a binding additionally
 * requires administering the source (deployment-wide `system:admin`), which the
 * engine checks through its composition port. */
export interface CreateBindingRequest {
  source_id: string
  source_revision: number
  profile_ref: string
}

/** One provider account (GET /provider-accounts, /provider-accounts/{ref}): a provider
 * profile that has been NAMED. An unnamed profile is not an account and is never listed
 * as one. The reference is the profile's own; there is no second id. No path travels
 * here — `home_relative` is a location under the server's accounts root, and the console
 * does not paint it — and no credential ever. */
export interface ProviderAccountDTO {
  account_ref: string
  name: string
  display_name?: string
  accent?: ProviderAccentName | (string & {})
  driver: string
  environment_ref: string
  state: 'active' | 'disabled' | 'retired' | (string & {})
  /** `adopted`: the operator's existing home, left where it is. */
  home_mode: 'adopted' | 'managed' | (string & {})
  home_generation: number
  home_relative: string
  /** Always stated by the server, never inferred: an adopted home is `shared`. */
  isolation_level: 'shared' | 'dedicated' | (string & {})
  os_user?: string
  release_ref?: string
  pending_release?: string
  auth_source: string
  provider_record_ref?: string
  /** Who the provider says is signed in, and where that came from. Source `none`
   * means nothing asked the provider: the identity is empty, not guessed. */
  identity: string
  identity_source: 'none' | (string & {})
  last_login_at?: string
  created_at: string
  updated_at: string
}

/** One stable creation request. Retries keep the same body and key. */
export interface CreateAccountRequest {
  driver: string
  name?: string
  idempotency_key: string
}

/** POST /provider-accounts/{profile_ref}/adopt body. An absent name asks the server to
 * generate one; a present name is checked exactly as typed. The isolation level is not
 * a field: an adopted home is shared by what it is. */
export interface AdoptAccountRequest {
  name?: string
}

/** GET …/activity: what a Claude Code session's tool is doing, read by the engine from the
 * transcript the tool keeps (modules/sessions/session_activity.go). */
export type ActivityState =
  'running' | 'completed' | 'failed' | 'stopped' | 'scheduled'

export interface ActivityAgentDTO {
  tool_use_id: string
  agent_id?: string
  type?: string
  description?: string
  state: ActivityState
  background?: boolean
  started_at?: string
  ended_at?: string
  duration_ms?: number
  tokens?: number
  tool_uses?: number
  model?: string
  summary?: string
}

export interface ActivityWorkflowDTO {
  run_id: string
  name?: string
  duration_ms?: number
  /** Agents the tool has observed so far, not an estimate of future work. */
  agents_total: number
  agents_done: number
  agents_failed?: number
  phases: {
    index: number
    title: string
    detail?: string
    /** Live state describes observed active/failed agents, not script-phase
     * completion. Only a completed workflow summary ends its phases. */
    state?: ActivityState
    agents_total: number
    agents_done: number
    agents_failed?: number
  }[]
}

export interface ActivityTaskDTO {
  tool_use_id: string
  task_id?: string
  /** shell, monitor, workflow or wakeup. */
  kind: string
  description?: string
  command?: string
  state: ActivityState
  started_at?: string
  ended_at?: string
  due_at?: string
  summary?: string
  workflow?: ActivityWorkflowDTO
}

export interface ActivityFileDTO {
  path: string
  added: number
  removed: number
  created?: boolean
  edits: number
  last_at?: string
}

export interface ActivityTodoDTO {
  content: string
  status: 'pending' | 'in_progress' | 'completed' | string
  active_form?: string
}

/** One message of a session's transcript, as GET /runs/{ref}/messages indexes it (#2129).
 * `id` is its position among the session's messages; `text` is only present when one
 * message is read whole by id. */
export interface RunMessageDTO {
  id: number
  role: 'user' | 'assistant'
  at?: string
  first_line: string
  text?: string
  /** With `text`: the text was cut at 64 KiB and ends in " [...]". */
  clipped?: boolean
  /** With `text`: how many later messages of the same sender repeat its first line. */
  later_same?: number
}

export interface RunMessagesDTO {
  driver: string
  /** read; pending (not written yet); missing (it should be there). */
  transcript: 'read' | 'pending' | 'missing'
  /** Every message that matches, not only this page. */
  total: number
  /** Oldest first, whichever end of the session the page was read from. */
  items: RunMessageDTO[]
  /** The cursor of the page that continues in the order asked; absent at the end. */
  next?: string
  unreadable?: number
}

/** What the messages read accepts: a phrase, a start time, every sender or the person's
 * own, the end to read from and a page cursor. */
export interface RunMessagesParams {
  q?: string
  at?: string
  role?: 'user' | 'all'
  order?: 'asc' | 'desc'
  cursor?: string
  limit?: number
  id?: number
  /** Every message of the page carries its whole `text` (Copy transcript). */
  text?: 1
}

export interface RunActivityDTO {
  driver: string
  /** read; pending (not reported or not written yet); missing (it should be there). */
  transcript: 'read' | 'pending' | 'missing'
  model?: string
  effort?: string
  context_tokens: number
  /** The tool's reported window; null/absent means unknown. */
  context_window?: number | null
  /** Already a percentage, calculated by the engine. */
  context_percent?: number | null
  compactions: {
    at?: string
    trigger?: string
    pre_tokens?: number
    post_tokens?: number
  }[]
  agents: ActivityAgentDTO[]
  tasks: ActivityTaskDTO[]
  files: ActivityFileDTO[]
  todos: ActivityTodoDTO[]
  skills: string[]
  mcp_servers: string[]
  unreadable?: number
  updated_at?: string
}

/** GET …/activity/agents/{agent}: one subagent's transcript in plain turns. */
export interface ActivityAgentTranscriptDTO {
  agent_id: string
  turns: {
    role: 'user' | 'assistant' | 'tool' | 'result' | string
    text: string
  }[]
  unreadable?: number
}

/** What GET …/handoff answers: the text a handoff would start from, for review. */
export interface HandoffPreviewDTO {
  summary: string
  source_driver: string
  /** The requests the conversation holds. */
  turns: number
  transcript_bytes: number
}

/** POST …/handoff. `summary` is the reviewed text; absent, the engine generates it. */
export interface HandoffRunRequest {
  provider_profile_ref: string
  summary?: string
  name?: string
}

/** What became of the first turn of a handoff. Only `delivered` is a certainty. */
export type HandoffSummaryState =
  'delivered' | 'not_delivered' | 'unknown' | 'waits_for_approval'

/** The new, linked session and what became of its first turn. `first_turn` is the
 * text the engine would have sent, present only when it did not arrive. */
export type HandoffRunResult = RunDTO & {
  summary_state: HandoffSummaryState
  first_turn?: string
}

/** One lifecycle-ledger event (GET /runs/{ref}/events), seq-ordered. The PayloadHash
 * + audit_seq cross-link the transition to the tamper-evident core audit ledger. */
export interface RunEventDTO {
  seq: number
  at: string
  event: string
  from_state?: string
  to_state?: string
  detail?: string
  actor?: string
  actor_kind?: string
  payload_hash: string
  audit_seq: number
}

/** POST /runs body. `env_allow` is the operator's allowlist of host env var NAMES to
 * forward to the child (values are never sent — only names). */
export interface CreateRunRequest {
  name: string
  transport: Transport
  permission_mode: PermissionMode | string
  effort: string
  model?: string
  fast_mode?: boolean
  service_tier?: string
  workspace_ref: string
  isolation: Isolation
  env_allow: string[]
  /** Opt in to a git worktree and branch of the session's own, made from the
   * workspace's repository. Sent only when chosen: absent keeps the session in the
   * workspace folder itself. */
  worktree?: boolean
  /** With `worktree`: start the worktree at this commit id or local branch of the
   * workspace's repository instead of its current commit (the work a handoff names).
   * Sent only when a handoff's start was chosen. */
  worktree_from?: string
  /** The workspace template whose terms GOVERN this launch. The server resolves
   * it, merges its terms over the fields above BEFORE the governance gates, and writes
   * the result into the child's argv — so the restriction holds for a caller that never
   * opens this console. The restrictions themselves are never sent from here: only the
   * template id is, which is why a client cannot post itself an empty allowlist. */
  template_id?: string
  /** The provider profile to launch under. Only the REFERENCE leaves the
   * browser: the server resolves and validates the profile's homes, persists the
   * snapshot on the run before the spawn and refuses a disabled, retired, foreign or
   * non-operable profile. Absent ⇒ the legacy launch under the runner's own home. */
  provider_profile_ref?: string
  /** Vault secrets (env/…) the child receives as environment variables, by NAME. The
   * server opens the values and needs tenant administration from the caller. */
  secret_env?: SecretEnvRef[]
}

/** One vault secret a session receives as an environment variable: names only. */
export interface SecretEnvRef {
  env: string
  secret: string
}

/** A registered workspace (host root a session works in). No file bytes or secrets. */
export interface WorkspaceDTO {
  workspace_ref: string
  name?: string
  root_path: string
  mount_mode: 'rw' | 'ro' | string
  container_target?: string
  allow_subpaths?: string[]
  max_read_bytes: number
  dlp_mode: 'label' | 'deny' | 'off' | string
  state: 'active' | 'disabled' | string
  /** Host folders a session in this workspace may read and never change. The engine canonicalizes
   * and checks them; an engine from before HU2-21 omits the field. */
  read_only_folders?: string[]
  created_at?: string
  updated_at?: string
}

/** POST /workspaces body. */
export interface CreateWorkspaceRequest {
  name: string
  root_path: string
  mount_mode: 'rw' | 'ro'
  container_target: string
  allow_subpaths: string[]
  max_read_bytes: number
  dlp_mode: 'label' | 'deny' | 'off'
}

/** The engine's folder default for an empty dlp_mode (modules/sessions/workspace.go). Every
 * console path that registers a folder starts from it, so the same folder gets the same approvals. */
export const DEFAULT_DLP_MODE: CreateWorkspaceRequest['dlp_mode'] = 'off'

/** One DLP sensitivity label on a read (the class/rule/severity — NEVER the matched value). */
export interface SensitivityHit {
  class: string
  rule?: string
  count?: number
  severity?: string
}

/** A directory entry (GET .../files, .../files/stat, mkdir result). */
export interface FileEntry {
  name: string
  path: string
  type: 'file' | 'dir' | 'symlink' | 'other' | string
  size: number
  mode: string
  mtime: string
  is_symlink?: boolean
}

/** A paginated directory listing (GET .../files). */
export interface FileListResponse {
  path: string
  entries: FileEntry[]
  cursor?: string
  has_more: boolean
}

/** A governed file read (GET .../files/raw). UTF-8 text or base64 binary + DLP labels. */
export interface FileReadResponse {
  path: string
  size: number
  encoding: 'utf-8' | 'base64' | string
  content: string
  truncated?: boolean
  sensitivity?: SensitivityHit[]
  sha256?: string
}

/** A confirmed write (PUT .../files/raw). Content is anchored by hash, never echoed. */
export interface WriteResponse {
  path: string
  size: number
  sha256: string
  created: boolean
}

// --- Attach stream frames (GET /runs/{ref}/attach, SSE) --------------------------

/** One bridged I/O frame (event: `output`). `line` is one NDJSON line from the process. */
export interface AttachFrame {
  seq: number
  stream: 'stdout' | 'stderr' | string
  line: string
  /** When the engine read the frame (RFC 3339); absent from an older engine. */
  at?: string
}

/** A backpressure/gap sentinel (event: `lag`): the ring evicted frames below the cursor
 * — N frames are gone for good and the stream resumed at `next_seq`. Surfaced honestly,
 * never a silent drop. */
export interface AttachLag {
  type: 'lag'
  dropped: number
  next_seq: number
}

/** Typed I/O-absence on an attach `notice`. Additive and stable; the English
 * `detail` is not the discriminator. Unknown values must not stop reconnect. */
export type AttachIOUnavailable = 'not_live_on_node' | 'remote_control'

export function isAttachIOUnavailable(
  value: unknown,
): value is AttachIOUnavailable {
  return value === 'not_live_on_node' || value === 'remote_control'
}

/** A lifecycle notice frame (event: `notice`), e.g. a remote-control "I/O not bridged"
 * sentinel or a state change. `io_unavailable` is absent on legacy notices. */
export interface AttachNotice {
  type: string
  state?: string
  detail?: string
  io_unavailable?: string
}

/**
 * One entry of the "/" menu: a command or skill (both are typed as /name) or an agent,
 * exactly as the session's tool reports it. `source` is empty when the tool does not say.
 */
export interface RunCommandDTO {
  name: string
  description: string
  argument_hint?: string
  /** Native invocation when the tool does not use /name. */
  insert_text?: string
  source: '' | 'built-in' | 'user' | 'project' | 'plugin'
  kind: 'command' | 'agent'
}

/**
 * What the session's tool reports it offers (GET /runs/{ref}/commands). `reported` is
 * false when the tool reported nothing: the session is not live, or its tool is not read yet.
 * `stale` is true when the last ask of the tool failed and `commands` is what it reported before.
 */
export interface RunCommandsDTO {
  tool: string
  reported: boolean
  stale?: boolean
  commands: RunCommandDTO[]
}

/** GET /v1/m/sessions/runs/{ref}/changes: files the session changed in its folder. */
export interface RunChangesDTO {
  /** The folder by name; empty when the run has no folder on this node. */
  folder: string
  since?: string
  files: { path: string; size: number; modified_at: string }[]
  truncated: boolean
}

/** GET /v1/m/sessions/runs/{ref}/changes/file?path=: one file's current text. */
export interface RunChangedFileDTO {
  path: string
  size: number
  text: string
  binary: boolean
  truncated: boolean
}

export type WorktreeDiffStatus = 'added' | 'modified' | 'deleted'

/** GET /v1/m/sessions/runs/{ref}/diff: the paths the session's worktree branch changed
 * since it left the workspace's current commit (committed work only). `base` and `head`
 * are full commit ids. */
export interface RunWorktreeDiffDTO {
  branch: string
  base: string
  head: string
  files: { path: string; status: WorktreeDiffStatus }[]
  truncated: boolean
}

/** GET /v1/m/sessions/runs/{ref}/diff/file?path=: one path at the base and at the
 * branch tip; `original` or `modified` is empty where the file does not exist. */
export interface RunWorktreeDiffFileDTO {
  path: string
  status: WorktreeDiffStatus
  original: string
  modified: string
  binary: boolean
  truncated: boolean
}
export interface RunGitStatusDTO {
  branch: string
  branches: string[]
  files: { path: string; index: string; worktree: string }[]
  truncated: boolean
  writable: boolean
}

export type RunGitAction = 'stage' | 'unstage' | 'commit' | 'branch'
export interface RunGitRequest {
  paths?: string[]
  message?: string
  name?: string
  create?: boolean
  work_lease_fence?: number
}
