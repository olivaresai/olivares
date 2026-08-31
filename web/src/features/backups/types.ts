// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for the backup / restore / DR subsystem — mirror the Go DTOs in
// modules/dr/*.go 1:1 (snake_case JSON tags). Superadmin-only; the web is a thin
// client (ARCHITECTURE.md SS8): it renders the engine's state and dispatches privileged,
// audited operations — it never derives DR state on its own.

// --- backup list + detail -----------------------------------------------------

/** One row in GET /v1/console/dr/backups. */
export interface BackupListItem {
  id: string
  filename: string
  size_bytes: number
  created_at: string
  engine: string
  tenant_count: number
  notes: string
}

/** One tenant chain tip from core/dr.Manifest.Tenants. */
export interface ManifestTenant {
  tenant: string
  system?: boolean
  head_seq: number
  head_hash: string
  checkpoints: number
  verified_at_backup: boolean
  verify_reason?: string
}

/** One sealed key reference from core/dr.Manifest.Keys (never key material). */
export interface ManifestKey {
  file: string
  name: string
  role: string
  pub_sha256?: string
}

/** The manifest embedded in a backup bundle — surfaces in backup detail and
 *  after a restore upload so the operator can review what is inside. */
export interface Manifest {
  engine: string
  created_at: string
  tenants: ManifestTenant[]
  keys: ManifestKey[]
}

/** GET /v1/console/dr/backups/{id} — full detail with the decoded manifest. */
export interface BackupDetail {
  id: string
  filename: string
  size_bytes: number
  manifest: Manifest
}

// --- jobs (backup + restore) --------------------------------------------------

export type DRJobKind = 'backup' | 'restore' | (string & {})
export type DRJobStatus = 'running' | 'completed' | 'failed' | (string & {})

/** One job row from GET /v1/console/dr/jobs and the SSE job stream. */
export interface DRJob {
  id: string
  kind: DRJobKind
  status: DRJobStatus
  phase: string
  /** 0–100 integer percentage. */
  progress: number
  error?: string
  created_at: string
  finished_at?: string
}

// --- restore ------------------------------------------------------------------

/** POST /v1/console/dr/restore/upload response after the raw bundle is received. */
export interface RestoreUploadResponse {
  upload_id: string
  manifest: Manifest
  filename: string
}

// --- schedule -----------------------------------------------------------------

/** GET/PUT /v1/console/dr/schedule — automated backup cadence + DR policy. */
export interface DRSchedule {
  enabled: boolean
  cron: string
  retain_days: number
  /** When true, a CONSOLE restore must be approved by a second, distinct
   *  administrator (dual-control). Default false so a solo operator can
   *  still restore.
   *
   *  Two limits the copy has to state, because both were measured:
   *  it covers restores driven from THIS console and not `olivares dr restore`
   *  on the host — the path the console itself sends Postgres estates to, which
   *  replaces an estate outside this gate and records a declared operator
   *  instead; and this field is the gate's EFFECTIVE state, which is not always
   *  what was last PUT — see dual_control_disarm_effective_at. */
  require_dual_control_restore?: boolean
  /** Set while a requested DISARM of the gate has not yet taken effect (RFC3339).
   *  Arming is immediate; disarming waits, so the gate cannot be removed in the
   *  same sitting as the restore it guards. Until this instant the gate STILL
   *  holds and require_dual_control_restore stays true; re-arming cancels it. */
  dual_control_disarm_effective_at?: string
  /** The stable user who REQUESTED the disarm, present until someone re-arms —
   *  including after the disarm has taken effect and this field is the only trace
   *  of it left.
   *
   *  A delay on its own is a one-party control with patience, so the disarm never
   *  takes effect FOR THIS ACCOUNT: once the cool-down passes the gate reads off and
   *  any OTHER administrator restores unencumbered, while their own restore is
   *  still held for a second approver. So `require_dual_control_restore: false`
   *  together with this field set is not a contradiction — it is the gate being off
   *  for the estate and on for one account.
   *
   *  NOT YET SURFACED in this console: the operator learns it from their restore
   *  coming back `awaiting_approval`, not from the schedule screen. */
  dual_control_disarm_requested_by?: string
}

/** One restore awaiting a second approver (GET /v1/console/dr/restore/pending). */
export interface PendingRestore {
  request_id: string
  upload_id: string
  /** The requester's CREDENTIAL actor string ("user:<id>" or "token:<id>"). */
  initiator: string
  /** The stable user ACCOUNT behind that credential — what the distinct-approver rule
   *  compares. Absent only on a request registered before the server
   *  recorded it. */
  initiator_user?: string
  created_at: string
}

// --- request / response envelopes ---------------------------------------------

export interface CreateBackupRequest {
  notes: string
  passphrase: string
}

export interface CreateBackupResponse {
  job_id: string
}

export interface ApplyRestoreRequest {
  passphrase: string
}

/** Apply outcome: a job id (single-actor) OR an awaiting-approval request id
 *  (dual-control). Exactly one branch is populated. */
export interface ApplyRestoreResponse {
  job_id?: string
  awaiting_approval?: boolean
  request_id?: string
  initiator?: string
}

/** Second-approver confirmation under dual-control. */
export interface ApproveRestoreRequest {
  request_id: string
  passphrase: string
}
