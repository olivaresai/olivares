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

/** One operated Claude Code session (GET /runs, /runs/{ref}). */
export interface RunDTO {
  run_ref: string
  name?: string
  transport: Transport
  permission_mode: PermissionMode | string
  effort?: Effort | string
  model_ref?: string
  workspace_ref?: string
  /**
   * The HOST directory this session's child was started in: the registered
   * workspace's canonical root, or the directory of its own the engine created for
   * the run. It is shown by NAME because until v26.10 a session with no workspace
   * ran in the ENGINE's own working directory and nothing on any surface said so.
   */
  workspace_path?: string
  /** The workspace template this run was last launched under, if any.*/
  template_id?: string
  isolation: Isolation
  state: RunState
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

  // Optional K2 authority links; legacy and non-work runs omit all four.
  work_item_id?: string
  work_lease_fence?: number
  work_dispatch_key?: string
  work_owner_epoch?: number

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
  /** A privileged launch (drove the HITL + mandatory recording floor). */
  critical: boolean

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
  /** The opaque id of the plane's MANAGED live row for this run, present once the
   * bridge proved the run owns its provider id. A profiled run is joined to its
   * session by THIS, never by the bare `claude_session_id` two homes may share. */
  live_ref?: string
}

/** One provider profile (GET /provider-profiles): the durable identity of ONE
 * configured provider instance on ONE execution environment. Configuration and
 * storage identity — never an authenticated provider account. No path here: the
 * homes are on the admin-only configuration read. */
export interface ProviderProfileDTO {
  profile_ref: string
  driver: string
  environment_ref: string
  display_name?: string
  state: 'active' | 'disabled' | 'retired' | (string & {})
  /** Belongs to THIS node's execution environment; a foreign one is shown as such
   * and never launched here. */
  local_environment: boolean
  /** Legacy enablement flag, preserved with its exact meaning: local, active, a
   * driver this runtime has REGISTERED, and — for a driver that needs one — an
   * authorized authentication source. It is not a launch guarantee and is not
   * the source of the requirements panel; that is GET …/launch-readiness. */
  operable: boolean
  /** The AUTHORIZED authentication source: `provider_account_home` uses the saved
   * login inside the profile's own homes, `managed_injection` mints a
   * provider-compatible credential through that driver's governed adapter. They
   * are distinct authorizations with no fallback; absent authorizes neither. */
  auth_source?: 'provider_account_home' | 'managed_injection' | (string & {})
  /** The registered PROVIDER this profile's managed launches resolve their
   * credential from. Absent means none is named, and the host's own credential
   * variables decide — exactly the behaviour every profile had before v26.10. It is a
   * reference: no key, no hint, no endpoint travels with it. */
  provider_record_ref?: string
  created_at?: string
  updated_at?: string
  retired_at?: string
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

/** POST /provider-accounts/{profile_ref}/adopt body. An absent name asks the server to
 * generate one; a present name is checked exactly as typed. The isolation level is not
 * a field: an adopted home is shared by what it is. */
export interface AdoptAccountRequest {
  name?: string
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
  model: string
  workspace_ref: string
  isolation: Isolation
  env_allow: string[]
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
