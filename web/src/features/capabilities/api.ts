// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Endpoint helpers + query keys for the capabilities module (V). Thin wrappers over
// the core HTTP client against /v1/m/capabilities (ARCHITECTURE.md — no logic here). The
// active tenant header is attached automatically; tenant-scoped keys cache-isolate
// per tenant (query.ts contract).
import { z } from 'zod'
import { http } from '@/lib/api'
import { ApiError } from '@/lib/api/errors'
import type { ListResponse } from '@/lib/api/types'
import type {
  ConfigDTO,
  ConfigInput,
  RevisionDTO,
  ServerDTO,
  ServerDetailDTO,
  SkillDTO,
  ToolPinActionResultDTO,
  ToolPinApproveInput,
  ToolPinDTO,
  ToolPinUnpinInput,
  ToolDTO,
  WiringGraphDTO,
} from './types'
import { EVIDENCE_PAGE } from '@/features/models/api'
// Re-exportado porque `api-transport.test.ts` lo importa de AQUÍ (`./api`), no de models:
// quitar la declaración local sin esto rompe ese test con TS2459.
export { EVIDENCE_PAGE }

const BASE = '/v1/m/capabilities'

export interface ListParams {
  cursor?: string
  limit?: number
}

export interface WiringParams {
  origin_kind?: string
  origin_ref?: string
  capability_kind?: string
  capability_ref?: string
}

const toolPinSchema: z.ZodType<ToolPinDTO> = z.object({
  tool: z.string().min(1),
  fingerprint: z.string().min(1),
  pinned_at: z.string().min(1),
  updated_at: z.string().min(1),
  pin_count: z.number().int().nonnegative(),
  // NOT optional and NOT defaulted. An engine that stops sending `version` has taken the
  // CAS precondition away, and the honest outcome is a loud parse failure here rather
  // than a write that sends `undefined` and dies at the engine with an opaque 400.
  version: z.number().int().nonnegative(),
  drift_fingerprint: z.string().min(1).optional(),
  drift_at: z.string().min(1).optional(),
})

const toolPinsResponseSchema = z.object({ items: z.array(toolPinSchema) })

/** The 202 body of approve/unpin — the engine's real shape (toolpins.go:224-228), not
 * the `{tool, fingerprint}` this file used to assert, which Zod silently stripped it
 * down to and which hid `apply_state` from the operator. */
const toolPinActionSchema: z.ZodType<ToolPinActionResultDTO> = z.object({
  tool: z.string().min(1),
  operation_id: z.string().min(1),
  apply_state: z.string().min(1),
  version: z.number().int().nonnegative(),
  evidence_ref: z.string(),
})

/** The community build returns 501 because no enterprise verifier is wired. */
export function isEnterprisePending(error: unknown): boolean {
  return error instanceof ApiError && error.status === 501
}

/**
 * A 409 that means the durable state MOVED between the operator's read and their write:
 * the pin row advanced, or the tool drifted again. That is information, not a failure to
 * swallow — the caller refetches and shows the divergence.
 *
 * It must never trigger an automatic resend with the fresh value: that would overwrite
 * whatever the other writer just did while wearing the face of a success, which is the
 * exact hazard the CAS exists to prevent (same rule as work/api.ts applyWork).
 *
 * ⛔ IT IS KEYED ON THE CODE, NOT THE STATUS, AND THAT IS THE WHOLE POINT. The engine
 * also answers 409 for `idempotency_key_reused` — a key rebound to a DIFFERENT effect,
 * which is a replay or a client bug, not another operator. Matching on the status alone
 * (which this function did until the the model contrast) told the operator that
 * somebody else had moved the state, refetched, and hid the message. A replay must stay
 * loud, so it falls through to the ordinary error path.
 */
const STATE_MOVED_CODES = new Set(['pin_version_conflict', 'pin_drift_changed'])

export function isPreconditionConflict(error: unknown): boolean {
  return (
    error instanceof ApiError &&
    error.status === 409 &&
    STATE_MOVED_CODES.has(error.code)
  )
}

/** A pin the engine currently reports as drifted — the only kind that can be approved
 * from drift, because `expected_drift_fingerprint` is mandatory on that branch. */
export type DriftedToolPin = ToolPinDTO & { drift_fingerprint: string }

/**
 * ONE operator intention, owning ONE Idempotency-Key for its whole life — retries
 * included. Modelled on web/src/features/work/api.ts:315-347, and a type rather than a
 * convention for the same reason it is there: regenerating the key on retry turns a
 * network timeout into a DOUBLE APPLY, and the engine cannot protect you, because two
 * keys are two intentions as far as it can tell (the OperationID is minted from the key,
 * toolpins.go:273-281).
 *
 * There is no code path that mints a key inside the send functions. Getting a second key
 * requires building a second intent — see `intentFor`, which is what decides when that is
 * actually a second intention.
 */
export interface ToolPinIntent {
  readonly key: string
  readonly kind: 'approve' | 'unpin'
  readonly body: ToolPinApproveInput | ToolPinUnpinInput
}

/** crypto.randomUUID is the platform generator; every browser this console supports
 * provides it in a secure context (same call as work/api.ts:348-350). */
function newIdempotencyKey(): string {
  return crypto.randomUUID()
}

/**
 * The identity of an intention: the verb plus the exact preconditions. Two requests with
 * the same signature ask the engine for the SAME effect on the SAME reviewed state, so
 * they are one intention and must carry one key.
 */
function intentSignature(kind: string, body: Record<string, unknown>): string {
  return JSON.stringify([kind, Object.entries(body).sort()])
}

/**
 * ⛔ A CLOSED DIALOG IS NOT A RESOLVED INTENTION.
 *
 * Building the intent at click time was not enough, and the the model contrast
 * named the hole: after "the server committed the effect but the response was lost", the
 * outcome is INDETERMINATE. Closing the dialog and clicking again is the most natural
 * thing an operator does next — and with a key minted per click, that second attempt
 * arrives as a different OperationID, so the engine can no longer recognise it as the
 * same intention and dedup it to the original outcome.
 *
 * So the key is remembered by SIGNATURE until the intention actually resolves. Reopening
 * the same decision against the same reviewed state reuses its key; the retry is a retry
 * however the operator got back to it.
 *
 * KEYED BY THE PRECONDITIONS, NOT BY THE TOOL, and that is the load-bearing part: if the
 * row moved, the new attempt carries a different expected_version, so it is a different
 * signature and gets a NEW key. Reusing the old key with a new body is precisely what the
 * engine refuses as a replay (ErrPinReplay), and it would deserve to.
 *
 * `resolveToolPinIntent` is called when the answer is definitive — applied, or refused for
 * a reason that leaves no effect behind — because after that the next click really is a
 * new intention.
 */
const liveIntents = new Map<string, ToolPinIntent>()

function intentFor(
  kind: 'approve' | 'unpin',
  body: ToolPinApproveInput | ToolPinUnpinInput,
): ToolPinIntent {
  const sig = intentSignature(kind, body as unknown as Record<string, unknown>)
  const live = liveIntents.get(sig)
  if (live) return live
  const intent: ToolPinIntent = { key: newIdempotencyKey(), kind, body }
  liveIntents.set(sig, intent)
  return intent
}

/** Forget an intention that reached a definitive answer. Anything still in flight, or
 * whose outcome nobody knows, deliberately stays — that is the whole point. */
export function resolveToolPinIntent(intent: ToolPinIntent): void {
  liveIntents.delete(
    intentSignature(
      intent.kind,
      intent.body as unknown as Record<string, unknown>,
    ),
  )
}

/** Approve the drift currently on screen. Both preconditions come from the row the
 * operator actually read: its CAS version and the exact fingerprint they reviewed. */
export function buildApproveIntent(pin: DriftedToolPin): ToolPinIntent {
  return intentFor('approve', {
    tool: pin.tool,
    from_drift: true,
    expected_version: pin.version,
    expected_drift_fingerprint: pin.drift_fingerprint,
  })
}

/** Revoke the pin the operator read, at the version they read it at. */
export function buildUnpinIntent(pin: ToolPinDTO): ToolPinIntent {
  return intentFor('unpin', {
    tool: pin.tool,
    expected_version: pin.version,
  })
}

// El techo que pedimos al motor. CUATRO handlers de esta feature llaman `listQuery(r)` y publican
// `has_more` — `modules/capabilities/servers.go:127` (servers), `:258` (skills), `:284` (tools) y
// `modules/capabilities/config.go:186` (configs) —, así que sin `limit` el repositorio genérico
// pagina a 100 (`core/internal/store/sqlstore/generic.go:28`). Pedimos su máximo (`maxLimit`, :29).
//
// `{ ...params, limit: params?.limit ?? EVIDENCE_PAGE }`: el techo va siempre y el `limit` de un
// llamante que pida otra cosa GANA; con `??`, un `{ limit: undefined }` explícito tampoco lo pisa.
//
// ⚠ TRES rutas de esta misma API NO llevan techo, y cada una por su razón MEDIDA:
//   · `wiring`        no devuelve `ListResponse`: es un grafo, no una lista paginada.
//   · `listToolPins`  su handler escribe un mapa fijo (`toolpins.go:75`), sin `List` ni `has_more`.
//   · `listRevisions` su handler fija `Limit: listCap` (`config.go:401`) e IGNORA el `limit` del
//     llamante — y además DESCARTA la página (`recs, _, err`), así que ni siquiera publica
//     `has_more`. Un techo ahí sería decorativo y AFIRMARÍA un gobierno que no existe; y declarar
//     el recorte es imposible mientras el motor tire la señal. Eso último es de `core`, no mío.
// El valor vive en `@/features/models/api` y se IMPORTA arriba: la rama lo declaraba aquí
// también, y el merge dejó las dos —import y declaración local— lo que rompe el typecheck
// (TS2440/TS2395). Mismo valor en los dos sitios (1000) y nadie lo importaba desde aquí,
// así que se queda el import y esta explicación, que es lo que de verdad no se puede perder.

export const capabilitiesApi = {
  listServers: (params?: ListParams) =>
    http.get<ListResponse<ServerDTO>>(`${BASE}/servers`, {
      query: { ...params, limit: params?.limit ?? EVIDENCE_PAGE },
    }),
  getServer: (id: string) =>
    http.get<ServerDetailDTO>(`${BASE}/servers/${encodeURIComponent(id)}`),
  listSkills: (params?: ListParams & { server_id?: string }) =>
    http.get<ListResponse<SkillDTO>>(`${BASE}/skills`, {
      query: { ...params, limit: params?.limit ?? EVIDENCE_PAGE },
    }),
  listTools: (params?: ListParams & { server_id?: string }) =>
    http.get<ListResponse<ToolDTO>>(`${BASE}/tools`, {
      query: { ...params, limit: params?.limit ?? EVIDENCE_PAGE },
    }),
  wiring: (params?: WiringParams) =>
    http.get<WiringGraphDTO>(`${BASE}/wiring`, { query: { ...params } }),

  listToolPins: async () =>
    toolPinsResponseSchema.parse(await http.get<unknown>(`${BASE}/toolpins`)),

  /**
   * Send one pin intention. The Idempotency-Key rides in a header because that is where
   * the engine reads it (toolpins.go:100-104) — through the SHARED client's `headers`
   * seam (lib/api/client.ts:63-75), never a feature-local fetch, which would have to
   * re-implement bearer injection, the tenant header and the 401 hook.
   *
   * `intent` is passed whole, so a retry is `sendToolPinIntent(sameIntent)` and reuses
   * the same key by construction.
   */
  sendToolPinIntent: async (intent: ToolPinIntent) => {
    const opts = { headers: { 'Idempotency-Key': intent.key } }
    // TWO call sites with literal paths, not one with `${BASE}/toolpins/${kind}`. The
    // route census in cmd/olivares/consoleroutes_test.go resolves a template literal
    // built from a path constant, but not one carrying an expression — the whole call
    // would become "unresolvable": counted against the ratchet, never checked against a
    // registered route. Same reasoning as web/src/features/work/api.ts:45-52.
    const body =
      intent.kind === 'approve'
        ? await http.post<unknown>(
            `${BASE}/toolpins/approve`,
            intent.body,
            opts,
          )
        : await http.post<unknown>(`${BASE}/toolpins/unpin`, intent.body, opts)
    return toolPinActionSchema.parse(body)
  },

  listConfigs: (
    params?: ListParams & { server_ref?: string; transport?: string },
  ) =>
    http.get<ListResponse<ConfigDTO>>(`${BASE}/configs`, {
      query: { ...params, limit: params?.limit ?? EVIDENCE_PAGE },
    }),
  getConfig: (id: string) =>
    http.get<ConfigDTO>(`${BASE}/configs/${encodeURIComponent(id)}`),
  createConfig: (input: ConfigInput) =>
    http.post<ConfigDTO>(`${BASE}/configs`, input),
  updateConfig: (id: string, input: ConfigInput) =>
    http.put<ConfigDTO>(`${BASE}/configs/${encodeURIComponent(id)}`, input),
  deleteConfig: (id: string) =>
    http.delete<void>(`${BASE}/configs/${encodeURIComponent(id)}`),
  listRevisions: (id: string) =>
    http.get<ListResponse<RevisionDTO>>(
      `${BASE}/configs/${encodeURIComponent(id)}/revisions`,
    ),
}

/** Tenant-scoped query keys (query.ts contract: tenant id in every key). */
export const capabilitiesKeys = {
  all: (t: string | null) => ['capabilities', t] as const,
  servers: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['capabilities', t, 'servers'] as const)
      : (['capabilities', t, 'servers', params] as const),
  server: (t: string | null, id: string) =>
    ['capabilities', t, 'server', id] as const,
  tools: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['capabilities', t, 'tools'] as const)
      : (['capabilities', t, 'tools', params] as const),
  skills: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['capabilities', t, 'skills'] as const)
      : (['capabilities', t, 'skills', params] as const),
  wiring: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['capabilities', t, 'wiring'] as const)
      : (['capabilities', t, 'wiring', params] as const),
  toolPins: (t: string | null) => ['capabilities', t, 'toolpins'] as const,
  configs: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['capabilities', t, 'configs'] as const)
      : (['capabilities', t, 'configs', params] as const),
  config: (t: string | null, id: string) =>
    ['capabilities', t, 'config', id] as const,
  revisions: (t: string | null, id: string) =>
    ['capabilities', t, 'config', id, 'revisions'] as const,
}
