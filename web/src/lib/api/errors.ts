// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ApiErrorCode, ErrorEnvelope } from './types'

/**
 * ApiError is a typed failure from the engine's REST API. It carries the HTTP
 * status, the stable non-leaking error code (core/api/errors.go), the human
 * message and the X-Request-ID (for support/correlation). The UI maps these to
 * consistent UX: 401 → re-login, 403 → a clear "not authorized" state (never a
 * generic red toast), 409 setup_required → the setup flow, etc.
 */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly requestId?: string
  /** Extra structured fields the handler attached to the error envelope beyond
   * code/message (e.g. the workflow graph validator's `step_ref`, which anchors
   * a validation failure to one node of the DAG). Never re-parse the human
   * message to recover machine-readable context: a message is prose and may be
   * reworded or translated, while these fields are the contract. */
  readonly details: Readonly<Record<string, unknown>>
  /** The parsed response body, whatever shape it had. Most failures carry the
   * error envelope (already projected into code/message/details), but some
   * endpoints answer a non-2xx with a DOMAIN document instead — a governed
   * actuation denied by its approval gate replies with the gate's own result
   * (status, approval_ref, detail) under 403/409/503. Without the raw body a
   * caller could only see "some request failed" and would report a human's
   * REJECTION as the operator lacking permission. */
  readonly body: unknown

  constructor(
    status: number,
    code: string,
    message: string,
    requestId?: string,
    details: Record<string, unknown> = {},
    body?: unknown,
  ) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.requestId = requestId
    this.details = details
    this.body = body
  }

  /** detailString reads one structured detail as a non-empty string, or
   * undefined when it is absent or not a string. */
  detailString(key: string): string | undefined {
    const v = this.details[key]
    return typeof v === 'string' && v !== '' ? v : undefined
  }

  get isUnauthenticated(): boolean {
    return this.status === 401
  }
  get isForbidden(): boolean {
    return this.status === 403
  }
  get isNotFound(): boolean {
    return this.status === 404
  }
  get isLockedOut(): boolean {
    return this.status === 429
  }
  get isSetupRequired(): boolean {
    return this.code === 'setup_required'
  }
  /** The action was refused for ASSURANCE, not for role: the operator may do
   * this, but the session must be elevated by a phishing-resistant ceremony
   * first (403 step_up_required — core/api/errors.go:224). Branch on THIS before
   * `isForbidden`, which is only a status: both are 403, and reporting a step-up
   * as "not authorized" tells the operator to go ask for a role they already
   * hold. The remedy is the step-up ceremony, never a generic toast. */
  get isStepUpRequired(): boolean {
    return this.code === 'step_up_required'
  }
  get isServerError(): boolean {
    return this.status >= 500
  }
}

/** NetworkError is a transport-level failure (the request never reached/returned
 * from the engine) — distinct from an ApiError, which is a structured response. */
export class NetworkError extends Error {
  readonly cause?: unknown
  constructor(message: string, cause?: unknown) {
    super(message)
    this.name = 'NetworkError'
    this.cause = cause
  }
}

/** ¿Es este fallo la FRONTERA open-core, y no una avería?
 *
 *  Se detecta por ESTADO, nunca casando el mensaje: 501 es el motor diciendo «esta capacidad
 *  vive en un add-on que no está enlazado» (`modules/compliance/nis2incident.go:121-126`,
 *  `modules/reporting/enterprise.go:104` y sus hermanas). El mensaje es prosa y se reescribe;
 *  el estado es el contrato. **Una consola que pinta esto en rojo enseña a los operadores a
 *  desconfiar de los errores de verdad.**
 *
 *  ⛔ VIVÍA DENTRO DE `features/compliance/api.ts` y se sube aquí porque `reporting` necesita la
 *  MISMA regla: sus tres informes enterprise contestan 501 con el motor sin cablear
 *  (`enterprise.go:104-106`). Copiarla habría sido duplicar una regla SEMÁNTICA, que es como se
 *  produce la deriva — el día que una de las dos aprenda otro estado, la otra no. Compliance la
 *  re-exporta, así que ningún importador cambia. */
export function isOpenCoreSeam(error: unknown): boolean {
  return error instanceof ApiError && error.status === 501
}

export function isApiError(e: unknown): e is ApiError {
  return e instanceof ApiError
}

export function isApiErrorCode(e: unknown, code: ApiErrorCode): boolean {
  return isApiError(e) && e.code === code
}

/** parseErrorEnvelope extracts {code,message} plus any EXTRA structured fields
 * the handler attached, tolerating a malformed/empty body (some 5xx may not
 * carry the envelope). The extras ride to ApiError.details so callers read a
 * machine-readable anchor rather than re-parsing the message. */
export function parseErrorEnvelope(
  body: unknown,
  fallbackMessage: string,
): { code: string; message: string; details: Record<string, unknown> } {
  const env = body as Partial<ErrorEnvelope> | null
  if (
    env &&
    typeof env === 'object' &&
    env.error &&
    typeof env.error === 'object'
  ) {
    const { code: _code, message: _message, ...details } = env.error
    return {
      code: env.error.code || 'internal',
      message: env.error.message || fallbackMessage,
      details,
    }
  }
  return { code: 'internal', message: fallbackMessage, details: {} }
}
