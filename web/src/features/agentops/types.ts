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

/** A lifecycle notice frame (event: `notice`), e.g. a remote-control "I/O not bridged"
 * sentinel or a state change. */
export interface AttachNotice {
  type: string
  state?: string
  detail?: string
}
