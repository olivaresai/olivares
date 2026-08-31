// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for the eventing module (webhook event subscriptions). Mirrors the Go DTOs
// in modules/eventing/dto.go 1:1. The subscriptionDTO `secret` field is returned
// ONLY on creation (one-time); subsequent reads carry `secret_hint` only.

export type AuthType = 'none' | 'bearer' | 'basic' | 'header'

export type DeliveryStatus =
  'queued' | 'delivering' | 'delivered' | 'dead' | 'denied'

export type DeliveryOrigin = 'live' | 'replay'

export type EventTypeStability = 'stable' | 'preview' | 'deprecated'

/** An event type from the catalog (GET /event-types). */
export interface EventType {
  type: string
  stability: EventTypeStability
  permission: string
  description: string
}

/** GET /event-types response. */
export interface EventTypeCatalog {
  event_types: EventType[]
}

/** A webhook subscription (list/get response). */
export interface Subscription {
  id: string
  name: string
  enabled: boolean
  event_types: string[]
  match_sources: string[]
  endpoint: string
  secret_hint: string
  role: string
  description: string
  created_at: string
  updated_at: string
  auth_type: AuthType
  auth_value_hint: string
  auth_header_name: string
  max_attempts: number
  initial_interval_seconds: number
  sink_kind: string
  sink_format: string
  sink_opts: Record<string, unknown>
  sink_cred_hint: string
}

/** POST /subscriptions response — includes the one-time secret. */
export interface CreatedSubscription extends Subscription {
  secret: string
}

/** Input body for POST/PUT /subscriptions. */
export interface SubscriptionInput {
  name: string
  enabled?: boolean
  event_types: string[]
  match_sources?: string[]
  endpoint: string
  role?: string
  description?: string
  auth_type?: AuthType
  auth_value?: string
  auth_header_name?: string
  max_attempts?: number
  initial_interval_seconds?: number
  sink_kind?: string
  sink_format?: string
  sink_cred?: string
  sink_opts?: Record<string, unknown>
}

/** POST /subscriptions/{id}/test response. */
export interface TestResult {
  delivered: boolean
  outcome: string
}

/** POST /subscriptions/{id}/rotate-secret response. */
export interface RotateSecretResult {
  secret: string
}

/** POST /subscriptions/{id}/rotate-auth body — the endpoint rejects an empty
 * credential with 400 (the engine never invents one). The cleartext is sealed
 * at rest and never returned; the response carries `auth_value_hint` only. */
export interface RotateAuthInput {
  auth_value: string
}

/** POST /subscriptions/{id}/replay body. */
export interface ReplayInput {
  from_seq: number
  to_seq?: number
}

/** POST /subscriptions/{id}/replay response. */
export interface ReplayResult {
  replayed: number
  next_seq: number
  has_more: boolean
}

/** A captured event (GET /events). */
export interface CapturedEvent {
  id: string
  seq: number
  type: string
  occurred_at: string
  source: string
  payload: unknown
}

/** GET /events response. */
export interface EventsResponse {
  items: CapturedEvent[]
  next_seq: number
  has_more: boolean
}

/** A delivery record (GET /deliveries | /dead-letters). */
export interface Delivery {
  id: string
  subscription: string
  event_id: string
  event_seq: number
  event_type: string
  status: DeliveryStatus
  origin: DeliveryOrigin
  attempts: number
  next_attempt_at: string
  last_attempt_at: string
  last_status: string
}

// ── The egress destination control's own surface ─────────────────────────────
//
// ⛔ ESTOS DTO SON DE LECTURA Y EL SERVIDOR YA DECIDE QUÉ CABE EN ELLOS. El motor
// documenta que NO enseña las reglas —«the rules themselves … name an operator's
// internal collectors»— y que el resumen de compatibilidad sólo se sirve a quien
// además puede leer el informe detallado, porque un CONTEO es un oráculo de
// pertenencia. La consola pinta lo que llega y no deduce nada de lo que falta.
//
// Mirrors modules/eventing/egressapi.go 1:1.

/** The writer fence's posture (unit H). Capability levels, never who runs what.*/
export interface EgressWriterFence {
  armed: boolean
  mode?: string
  generation?: number
  required_capability: number
  binary_capability: number
  /** Neither armed nor dormant: UNKNOWN, and reported as such. */
  unavailable?: boolean
}

/** The tenant-safe half of the compatibility record. Served only to an admin caller. */
export interface EgressCompatSummary {
  seeded: boolean
  recorded: number
  still_needed: number
  /** False is the shape a partial restore produces: the counts describe less than the record claims. */
  intact: boolean
  unparsable: number
}

/** GET /egress-policy — whether a policy is in force, and where it came from. Never a rule. */
export interface EgressPolicyStatus {
  in_force: boolean
  source?: string
  /** A policy exists but could not be READ right now: deliveries requeue rather than refuse. */
  unavailable?: boolean
  /** enforced | legacy_compat | policy_optional — a fact about the DEPLOYMENT. */
  mode: string
  mode_unavailable?: boolean
  /** What the engine decided when it first met this deployment. Never changes. */
  classified_mode?: string
  enforcement_committed: boolean
  writer_fence: EgressWriterFence
  compat?: EgressCompatSummary
}

/** One preserved destination and how many subscriptions use it. */
export interface EgressLegacyAuthority {
  authority: string
  kind: string
  subscriptions: number
  /** The policy in force already permits it, so it is not part of the breaking change. */
  covered: boolean
}

/** GET /egress-policy/compat — ADMIN tier, because it names hosts. */
export interface EgressCompatReport {
  seeded: boolean
  intact: boolean
  integrity_note?: string
  seeded_at?: string
  seed_digest?: string
  subscriptions: number
  unparsed: number
  authorities: EgressLegacyAuthority[]
  still_needed: number
}
