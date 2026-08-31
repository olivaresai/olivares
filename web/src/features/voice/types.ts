// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for module XVI (voice / realtime), mirroring the UI data contract
// The wire carries ONLY references and
// aggregates — NEVER audio, transcription text, a prompt or PII. `transcript_ref_hash`
// is the fingerprint of an EXTERNAL locator; it proves a transcript exists, and is not
// linkable to content from here. Latency is honest avg+max (no p50/p95 without samples).

/** Derived at read-time (no stored state column): is the session live, idle or ended? */
export type SessionState = 'live' | 'idle' | 'ended'

/** A voice/realtime session. GET /sessions, GET /sessions/{ref}, SSE /sessions/{ref}/stream. */
export interface VoiceSession {
  id: string
  session_ref: string
  agent_ref: string
  model_ref: string
  provider_ref: string
  principal_ref: string
  policy_ref: string
  language_code: string
  state: SessionState
  user_turns: number
  agent_turns: number
  turn_count: number
  duration_ms: number
  /** Honest avg + max only — never a fabricated p50/p95 (no per-turn samples on the wire). */
  latency_avg_ms: number
  latency_max_ms: number
  /** Was the open? false = ungoverned (anti-evasion flag).*/
  governed: boolean
  /** 64-hex fingerprint of an EXTERNAL transcript locator — NEVER text/audio. */
  transcript_ref_hash: string
  first_event_at: string
  last_event_at: string
  closed_reason: string
  created_at: string
}

/** A voice policy. GET /policies, PUT /policies. Default-DENY: a policy is what
 *  PERMITS opening a session; `agent_ref: '*'` matches all agents. */
export interface VoicePolicy {
  id: string
  /** '*' = applies to every agent. */
  agent_ref: string
  /** '*' = any model permitted. */
  allowed_model_ref: string
  /** '*' = any provider permitted. */
  allowed_provider_ref: string
  max_session_minutes: number
  max_latency_ms: number
  set_by: string
  updated_at: string
}

/** PUT /policies body — the configuration write (default-DENY contract). */
export interface VoicePolicyInput {
  agent_ref: string
  allowed_model_ref: string
  allowed_provider_ref: string
  max_session_minutes: number
  max_latency_ms: number
}

// --- la superficie viva de una sesión (modules/voice) --------------------------

/** Una decisión registrada del plano de voz (`dto.go:137-155`). Los `?` son
 *  `omitempty` en Go: ausente y vacío son los mismos bytes, y ninguno de los dos
 *  afirma nada. `gate_status` y `op_status` NO lo son — llegan siempre. */
export interface VoiceDecision {
  id: string
  session_ref: string
  agent_ref: string
  requested_model_ref: string
  requested_provider_ref: string
  policy_ref?: string
  op: string
  policy_verdict: string
  plan_hash?: string
  approval_ref?: string
  gate_status: string
  op_status: string
  dispatch_ref?: string
  actor: string
  actor_kind: string
  result?: string
  occurred_at: string
}

/** El cuerpo del open gobernado. Los CUATRO refs son obligatorios: sin cualquiera de
 *  ellos el motor devuelve 400 (`policies.go:267-270`). `approval_ref` es la SEGUNDA
 *  fase — se re-envía el mismo cuerpo con él. */
export interface VoiceOpenInput {
  session_ref: string
  agent_ref: string
  model_ref: string
  provider_ref: string
  approval_ref?: string
}

/** La respuesta del open, y NO tiene dos desenlaces sino cinco (`policies.go:262-372`):
 *
 *    403 + policy_verdict=denied ...... una DECISIÓN de política, no un fallo
 *    202 + op_status=requested ........ fase 2: re-enviar con `approval_ref`
 *    gate_status = no_gate ............ NO hay puerta de aprobación cableada: un hueco
 *                                       del patrimonio, distinto de «denegado»
 *    502 «approval gate unavailable» .. NO SE PUDO MIRAR, distinto de denegar
 *    dispatch_ref presente ............ abierto de verdad
 *
 *  Dibujar esto como «funcionó / falló» borra tres de los cinco. */
export interface VoiceOpenResponse {
  op: string
  op_status: string
  policy_verdict: string
  plan_hash?: string
  approval_ref?: string
  gate_status?: string
  dispatch_ref?: string
  requires_approval?: boolean
  detail?: string
}
