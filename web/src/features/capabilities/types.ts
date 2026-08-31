// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for the capabilities module (V) — mirror the Go DTOs in
// modules/capabilities/dto.go 1:1 (snake_case JSON tags). The web is a thin client
// (ARCHITECTURE.md): these are the exact shapes the engine returns at /v1/m/capabilities.
// CRITICAL invariants encoded here: secret_refs carry REFERENCES, never values; the
// MCP annotation hints are UNTRUSTED (annotation_trust is always 'untrusted').

/** Transport an MCP server speaks. */
export type Transport =
  'stdio' | 'http' | 'sse' | 'ws' | 'unknown' | (string & {})

/** Derived connection state — never fabricated. `unknown` = no signal yet. */
export type Connection =
  'connected' | 'degraded' | 'down' | 'unknown' | (string & {})

/** Where a referenced credential lives — NOT the credential itself. */
export type RefKind =
  'env' | 'vault' | 'secret_manager' | 'file' | 'other' | (string & {})

export const REF_KINDS: RefKind[] = [
  'env',
  'vault',
  'secret_manager',
  'file',
  'other',
]
export const TRANSPORTS: Transport[] = ['stdio', 'http', 'sse', 'ws']

/** A credential REFERENCE — by construction there is no value field. */
export interface SecretRefDTO {
  name: string
  ref_kind: RefKind
  /** A locator (e.g. "$GITHUB_TOKEN"), never the credential. */
  ref: string
  /** Optional short masked partial (e.g. "ghp_…aB12"); max 64 chars. */
  hint?: string
}

/** A live MCP-server catalog entry. */
export interface ServerDTO {
  id: string
  name: string
  transport: Transport
  endpoint?: string
  version?: string
  status: string
  connection: Connection
  tool_count: number
  has_config: boolean
  config_revision?: number
}

/** An UNTRUSTED, server-declared MCP tool. */
export interface ToolDTO {
  id: string
  name: string
  kind?: string
  mcp_server_id?: string
  /** UNTRUSTED declared annotation — informational only, never enforced. */
  read_only_hint: boolean
  /** UNTRUSTED declared annotation — informational only, never enforced. */
  destructive_hint: boolean
  /** Always the literal 'untrusted'. */
  annotation_trust: string
  schema_hash?: string
}

export interface SkillDTO {
  id: string
  name: string
  source?: string
  version?: string
  mcp_server_id?: string
  status: string
  description?: string
}

/** Last connection-health signal — basic state, NOT a formal SLA. */
export interface HealthDTO {
  status: string
  severity?: string
  last_title?: string
  detail_hash?: string
  status_at: string
  occurrence_count: number
}

/** Managed MCP config — no plaintext secrets, only references. */
export interface ConfigDTO {
  id?: string
  /** Natural key, immutable after create. */
  server_ref: string
  transport: Transport
  /** Reference only; the backend rejects inline credentials. */
  endpoint?: string
  scope?: string
  secret_refs: SecretRefDTO[]
  enabled: boolean
  note?: string
  /** Per-server monotonic revision counter. */
  revision?: number
}

/** The write payload for create/update — exactly the fields the backend accepts
 * (it rejects unknown fields). id/revision are server-assigned. */
export interface ConfigInput {
  server_ref: string
  transport: Transport
  endpoint?: string
  scope?: string
  secret_refs: SecretRefDTO[]
  enabled: boolean
  note?: string
}

/** One tenant-scoped MCP tool authorization pin, including a live drift signal. */
export interface ToolPinDTO {
  tool: string
  fingerprint: string
  pinned_at: string
  updated_at: string
  pin_count: number
  /** The CAS base version of the durable pin row. This GET is the ONLY place a client
   * can learn it, and approve/unpin refuse without it — see ToolPinWriteBase. */
  version: number
  drift_fingerprint?: string
  drift_at?: string
}

/**
 * The precondition EVERY pin write carries (modules/capabilities/toolpins.go:105-108,
 * 146-149). `expected_version` is the version read from this tool's ToolPinDTO: the
 * engine applies the change only if the durable row is still there, so a racing operator
 * write loses the compare-and-swap instead of being silently overwritten.
 */
export interface ToolPinWriteBase {
  tool: string
  expected_version: number
}

/**
 * Approve either an explicit fingerprint or the drift the operator just reviewed.
 *
 * `expected_drift_fingerprint` is REQUIRED on the from_drift branch and is not a
 * duplicate of the pin's `drift_fingerprint`: it is the exact drifted fingerprint that
 * was on screen when the operator decided, checked against the durable row at apply time
 * (toolpins.go:115-119). A tool that rug-pulls again between the read and the write gets
 * a 409 rather than an approval of a definition nobody ever saw.
 */
export type ToolPinApproveInput =
  | (ToolPinWriteBase & {
      fingerprint: string
      from_drift?: false
      expected_drift_fingerprint?: never
    })
  | (ToolPinWriteBase & {
      from_drift: true
      expected_drift_fingerprint: string
      fingerprint?: never
    })

export type ToolPinUnpinInput = ToolPinWriteBase

/**
 * The 202 body both verbs return (toolpins.go:224-228). It is 202 and not 200 on
 * purpose: the durable apply/settle is authoritative and asynchronous, so `apply_state`
 * may still be 'pending' — the verifier denies the tool until it settles. `version` is
 * the NEW base version, and the console refetches rather than trusting it as a local
 * cache: another writer may have moved it again already.
 */
export interface ToolPinActionResultDTO {
  tool: string
  operation_id: string
  apply_state: string
  version: number
  evidence_ref: string
}

/** An immutable config-version snapshot (history survives deletion). */
export interface RevisionDTO {
  server_ref: string
  revision: number
  transport: Transport
  endpoint?: string
  scope?: string
  secret_refs: SecretRefDTO[]
  enabled: boolean
  note?: string
  change_actor: string
  change_action: 'create' | 'update' | 'delete' | (string & {})
  changed_at: string
}

/** Full management view of one server (serverDTO fields are inline/flat). */
export interface ServerDetailDTO extends ServerDTO {
  config?: ConfigDTO | null
  health?: HealthDTO | null
  tools: ToolDTO[]
  skills: SkillDTO[]
  resources: string[]
  consumers: WiringPeerDTO[]
  notes?: Record<string, unknown>
}

/** A node / one end of a wiring edge. */
export interface WiringPeerDTO {
  kind: string
  ref: string
}

/** One capability-connection edge. signal_sources distinguishes OBSERVED
 * (e.g. 'otel') from DECLARED (e.g. 'mcp_annotation'). */
export interface WiringEdgeDTO {
  origin_kind: string
  origin_ref: string
  capability_kind: string
  capability_ref: string
  tool_ref?: string
  signal_sources: string[]
  first_seen: string
  last_seen: string
  occurrence_count: number
}

/** The /wiring response — NOT a ListResponse (bounded; uses `truncated`). */
export interface WiringGraphDTO {
  nodes: WiringPeerDTO[]
  edges: WiringEdgeDTO[]
  truncated?: boolean
  /** Fixed disclaimer distinguishing this from the R/RW access graph.*/
  note: string
}
