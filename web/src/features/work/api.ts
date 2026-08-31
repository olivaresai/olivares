// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ApiError } from '@/lib/api/errors'
import { http, type TenantRequestOptions } from '@/lib/api/client'
import type {
  AcceptanceCriterion,
  Assessment,
  CommandResult,
  LeaseFilters,
  Plan,
  Verdict,
  WorkLease,
  WorkLeasePage,
  WorkDecision,
  WorkDependency,
  WorkEventRow,
  WorkErrorBody,
  WorkItem,
  WorkMode,
  WorkPage,
  WorkSnapshot,
} from './types'

/**
 * The K1 work kernel client — hand-written, because the generated client covers
 * no `/v1/m/` route (see types.ts). It follows features/agentops/api.ts, including its
 * habit of saying which filter narrows the PAGE and which narrows the STORE.
 *
 * WHICH FILTER NARROWS WHAT, measured against modules/sessions/work_api.go:
 *
 *  - EVERY work-item filter narrows the STORE. handleWorkList (:396-402) passes them
 *    into WorkQuery.Filters and the repository turns them into SQL predicates; none is
 *    applied to an already-read page. So "status=blocked" answers "…among ALL items",
 *    not "…among the N most recent". This is the opposite of the agentops `state`
 *    filter, and the difference is worth stating rather than assuming.
 *  - The decision SUBJECT and ACTOR filters are the exception, and only in the
 *    DecisionHead view: listCurrentWorkDecisions (:659) evaluates them on the
 *    referenced current Decision after reading each head. It keeps scanning until the
 *    page is full precisely so post-filtering cannot deform `has_more` — so the answer
 *    is still store-wide. Do not "help" by re-filtering client-side.
 *
 * THE QUERY STRING IS CLOSED. handleWorkList rejects an unknown key, and a repeated
 * key, with 400 invalid_command (:390-395). Sending a stray param is a broken screen,
 * not an ignored hint, so the builders below emit only what the engine allows.
 */

// Single-quoted literals, NOT a template off a shared BASE constant. The route census in
// cmd/olivares/consoleroutes_test.go resolves a template literal, a single-quoted
// literal or a path constant — but a constant whose VALUE is a template built from
// another constant is one level too indirect, and every call site derived from it
// becomes "unresolvable": counted, never checked against a registered route.
// Measured 2026-08-11: these two alone contributed 9 of the 40 unresolvable sites
// that broke the ratchet (budget 32) when the cockpit landed. Keep them literal.
//
// The BASE constant this file used to interpolate is GONE with them, and that is the
// second half of the fix rather than tidying: literalising the paths left it with no
// readers, `tsc -b` rejects an unread local (TS6133), and it broke main. eslint on the
// file did NOT catch it, so "the linter is green" is not evidence here. A sibling that
// took the same fix (features/agent-artifacts/api.ts) dropped its BASE in the same
// commit; this one did not, and the two changes landed hours apart.
const WORK_ITEMS = '/v1/m/sessions/work-items'
// Its own single-quoted literal, for the reason the comment above gives: a constant built by
// interpolating another constant is one level too indirect for the route census, and every call
// site derived from it becomes unresolvable — counted, never checked against a registered route.
const LEASES = '/v1/m/sessions/leases'
const DECISIONS = '/v1/m/sessions/decisions'

/** El techo de la familia `/v1/m/sessions`, y NO es 1000.
 *
 * ⛔ ESTA FAMILIA NO ES LA DE `/v1/m/models`. Pagina por KEYSET con cursor, y su motor
 *    (`modules/sessions/work_api.go:798-807`, `queryLimit`) responde **400 `invalid_command`** con
 *    `n < 1 || n > 200`. Copiar aqui el `EVIDENCE_PAGE = 1000` del store generico —que es lo que
 *    invita a hacer una campana de "poner techos"— cambiaria un recorte silencioso por un error
 *    duro, que ademas parece un fallo del producto.
 *
 * ⚠ Y `limit: 100` tampoco es un techo: es el valor POR OMISION del motor. Pasarlo explicitamente
 *    no protege de nada; solo 200 amplia lo que se ve.
 *
 * Esto sube el suelo al maximo que el motor acepta. NO completa la lista: el keyset sigue
 * truncando, y lo que cierra ese hueco es declarar el recorte en pantalla y ofrecer «cargar mas»,
 * que es trabajo por vista y va aparte.
 */
export const WORK_PAGE_MAX = 200
export const WORK_STREAM_PATH = '/v1/m/sessions/work-stream'

const id = (v: string) => encodeURIComponent(v)

/** React Query keys.
 *
 * ⛔ TODAS LLEVAN EL INQUILINO, y no lo llevaban. Estas rutas son de módulo
 * (`/v1/m/sessions/…`) y el motor las lee con el inquilino en el tipo:
 * `modules/sessions/work_read.go` recibe `tenant model.TenantID` y elige `m.workData(tenant)`.
 * Sin el hueco, cambiar de inquilino no cambiaba la clave y la consola seguía enseñando el
 * trabajo, las decisiones, las dependencias y los arriendos del anterior — que no son sólo
 * presentación: un arriendo expresa autoridad viva.
 *
 * Lo aplacé por «hay que leer el alcance de cada módulo» y el contraste externo lo refutó con
 * la firma del motor delante: aquí no había ambigüedad que resolver. */
export const workKeys = {
  all: (tenant: string | null) => ['work', tenant] as const,
  items: (tenant: string | null, params: ListWorkParams) =>
    ['work', tenant, 'items', params] as const,
  item: (tenant: string | null, itemId: string) =>
    ['work', tenant, 'item', itemId] as const,
  acceptance: (tenant: string | null, itemId: string) =>
    ['work', tenant, 'acceptance', itemId] as const,
  dependencies: (tenant: string | null, itemId: string) =>
    ['work', tenant, 'dependencies', itemId] as const,
  lease: (tenant: string | null, itemId: string) =>
    ['work', tenant, 'lease', itemId] as const,
  events: (tenant: string | null, itemId: string) =>
    ['work', tenant, 'events', itemId] as const,
  decisions: (tenant: string | null, params: ListDecisionsParams) =>
    ['work', tenant, 'decisions', params] as const,
}

// ---------------------------------------------------------------------------
// Verdict handling — the outcome that is neither success nor failure
// ---------------------------------------------------------------------------

/**
 * NO_HE_PODIDO_MIRAR REACHES THE CONSOLE BY TWO DIFFERENT DOORS, and only one of them
 * looks like an error. This is the single most important fact in this file.
 *
 *  1. As a FAILURE: unknown() builds a 503 whose body is
 *     {verdict, code, error:{...}} (work_api.go writeWorkError). ApiError carries it.
 *  2. As a 200 OK: in validate/plan, when the store cannot be read, the engine wraps
 *     the failure into an Assessment and writes it with http.StatusOK
 *     (work_api.go:199-205). The request SUCCEEDED; the observation did not.
 *
 * A console that branches on the HTTP status alone renders door 2 as a clean result —
 * which is exactly the defect this repository has spent a week closing: a check that
 * says "clean" when it means "I could not look". So every read below returns the
 * verdict alongside the data and the callers render on the VERDICT, never on the
 * absence of a thrown error.
 */
export function verdictOfError(err: unknown): Verdict | null {
  if (!(err instanceof ApiError)) return null
  const body = err.body as Partial<WorkErrorBody> | undefined
  const v = body?.verdict
  return v === 'LIMPIO' || v === 'ROTO' || v === 'NO_HE_PODIDO_MIRAR' ? v : null
}

/** True when the engine said it could not look — through EITHER door. */
export function isUnknownVerdict(x: unknown): boolean {
  if (verdictOfError(x) === 'NO_HE_PODIDO_MIRAR') return true
  const v = (x as { verdict?: unknown } | null | undefined)?.verdict
  return v === 'NO_HE_PODIDO_MIRAR'
}

/** The engine's error code, for the operator's report. */
export function workErrorCode(err: unknown): string | null {
  if (!(err instanceof ApiError)) return null
  const body = err.body as Partial<WorkErrorBody> | undefined
  return body?.code ?? err.code ?? null
}

/**
 * El MOTIVO en prosa que dio el motor, para enseñárselo al operador tal cual.
 *
 * ⛔ NO ES `workErrorCode` CON OTRO NOMBRE, y la diferencia decide qué se puede hacer con cada
 *    uno. El `code` es legible por máquina y estable: con él se clasifica el fallo y se elige la
 *    acción que se ofrece (re-leer tras un conflicto de versión NO es reintentar). El `message`
 *    es PROSA —`errors.ts:20` lo dice— y puede cambiar de una versión a otra: por eso se muestra
 *    y NUNCA se compara ni se parsea. Devolver los dos por separado es lo que impide que alguien
 *    acabe ramificando sobre el texto.
 */
export function workErrorReason(err: unknown): string | null {
  if (!(err instanceof ApiError)) return null
  const body = err.body as Partial<WorkErrorBody> | undefined
  // ⛔ ANIDADO: `WorkErrorBody` es `{ verdict, code, error: { code, message } }` (types.ts:269-273).
  //    Mi primera versión leyó `body.message` de la raíz — no existe, y `tsc -b` lo dijo. El
  //    `?? err.message` sigue siendo el respaldo cuando el cuerpo no trae sobre.
  const m = body?.error?.message ?? err.message
  return typeof m === 'string' && m.trim() !== '' ? m : null
}

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

export interface ListWorkParams {
  status?: string
  priority?: string
  work_kind?: string
  owner_kind?: string
  owner_ref?: string
  provenance_kind?: string
  provenance_ref?: string
  parent_id?: string
  /**
   * TRI-STATE, and it is not a boolean with a default. work_api.go maps
   * `archived=false` to `archived_at IS NULL` and `archived=true` to `IS NOT NULL`;
   * ABSENT selects neither, i.e. every item regardless of archival.
   *
   * `undefined` here therefore means "do not send the key at all" — the third state —
   * and NOT "send false". A default of `false` would quietly redefine the list the
   * operator believes they are reading, hiding archived work behind a filter nobody
   * chose. The UI exposes all three and starts at the engine's own answer: absent.
   */
  archived?: boolean
  due_before?: string
  updated_after?: string
  limit?: number
  cursor?: string
}

/** Build the closed query the engine accepts, omitting every absent key. */
function listWorkQuery(p: ListWorkParams): Record<string, string> {
  const q: Record<string, string> = {}
  const put = (k: string, v: string | undefined) => {
    if (v !== undefined && v !== '') q[k] = v
  }
  put('status', p.status)
  put('priority', p.priority)
  put('work_kind', p.work_kind)
  put('owner_kind', p.owner_kind)
  put('owner_ref', p.owner_ref)
  put('provenance_kind', p.provenance_kind)
  put('provenance_ref', p.provenance_ref)
  put('parent_id', p.parent_id)
  put('due_before', p.due_before)
  put('updated_after', p.updated_after)
  put('cursor', p.cursor)
  if (p.limit !== undefined) q.limit = String(p.limit)
  // Only when the operator chose one of the two narrowing states. Absent stays absent.
  if (p.archived !== undefined) q.archived = String(p.archived)
  return q
}

export async function listWorkItems(
  p: ListWorkParams,
  options: TenantRequestOptions,
  signal?: AbortSignal,
): Promise<WorkPage<WorkItem>> {
  return http.get<WorkPage<WorkItem>>(WORK_ITEMS, {
    ...options,
    query: listWorkQuery(p),
    signal,
  })
}

/** GET one item with its acceptance criteria and dependencies, plus the ETag the next
 * apply must echo in If-Match. The engine sets it from the item version
 * (work_api.go:374), so it is read here rather than reconstructed as `"v${version}"`:
 * formatting the concurrency token client-side is a second implementation of the
 * server's rule, and this console has paid for that class before. */
export async function getWorkItem(
  itemId: string,
  options: TenantRequestOptions,
  signal?: AbortSignal,
): Promise<{ snapshot: WorkSnapshot; etag: string | null }> {
  const { data, headers } = await http.getWithMeta<WorkSnapshot>(
    `${WORK_ITEMS}/${id(itemId)}`,
    { ...options, signal },
  )
  return { snapshot: data, etag: headers.get('ETag') }
}

export async function listAcceptance(
  itemId: string,
  options: TenantRequestOptions,
  signal?: AbortSignal,
): Promise<WorkPage<AcceptanceCriterion>> {
  return http.get<WorkPage<AcceptanceCriterion>>(
    `${WORK_ITEMS}/${id(itemId)}/acceptance`,
    { ...options, signal },
  )
}

export async function listDependencies(
  itemId: string,
  options: TenantRequestOptions,
  signal?: AbortSignal,
): Promise<WorkPage<WorkDependency>> {
  // ⛔ SOLO `limit` y `cursor`: `workChildrenQuery` (work_api.go:954-963) RECHAZA con 400
  //    `invalid_command` cualquier otro parametro de query, asi que aqui no se puede expandir nada
  //    del llamante. Sin `limit` el motor servia su pagina de 100 y la vista no decia que faltaban.
  return http.get<WorkPage<WorkDependency>>(
    `${WORK_ITEMS}/${id(itemId)}/dependencies`,
    { ...options, signal, query: { limit: WORK_PAGE_MAX } },
  )
}

export async function listWorkEvents(
  itemId: string,
  params: { limit?: number; cursor?: string },
  options: TenantRequestOptions,
  signal?: AbortSignal,
): Promise<WorkPage<WorkEventRow>> {
  const query: Record<string, string> = {}
  if (params.limit !== undefined) query.limit = String(params.limit)
  if (params.cursor) query.cursor = params.cursor
  return http.get<WorkPage<WorkEventRow>>(
    `${WORK_ITEMS}/${id(itemId)}/events`,
    { ...options, query, signal },
  )
}

/**
 * THE DECISION LIST IS TWO DIFFERENT ENDPOINTS WEARING ONE PATH, and confusing them is
 * how a console tells a lie the engine refused to tell.
 *
 *  - `view: 'history'` sends NEITHER boolean. The engine returns the append-only
 *    Decision history and, in the contract's words, "no atribuye estado actual a filas
 *    históricas". Those rows carry no `state`, and painting an "in force" badge on
 *    one asserts something the engine explicitly declines to assert.
 *  - `view: 'effective' | 'revoked'` sends exactly one boolean and selects the
 *    DecisionHead projection. Only these rows carry `state`, and only they may be
 *    labelled with current state.
 *
 * Contradictory pairs (effective=true&revoked=true) are `invalid_command`, so this
 * function makes them unrepresentable rather than relying on a caller not to build one.
 *
 * The opaque cursor orders HEADS, not decisions (work_api.go:607-612). It is therefore
 * not interchangeable between the two views and must never be carried across a view
 * switch — resetDecisionCursorOnViewChange in the panel is what enforces that.
 */
export type DecisionView = 'history' | 'effective' | 'revoked'

export interface ListDecisionsParams {
  view: DecisionView
  work_item_id?: string
  decision_key?: string
  subject_kind?: string
  subject_ref?: string
  decided_by_kind?: string
  decided_by_ref?: string
  limit?: number
  cursor?: string
}

export async function listDecisions(
  p: ListDecisionsParams,
  options: TenantRequestOptions,
  signal?: AbortSignal,
): Promise<WorkPage<WorkDecision>> {
  const query: Record<string, string> = {}
  const put = (k: string, v: string | undefined) => {
    if (v !== undefined && v !== '') query[k] = v
  }
  put('work_item_id', p.work_item_id)
  put('decision_key', p.decision_key)
  put('subject_kind', p.subject_kind)
  put('subject_ref', p.subject_ref)
  put('decided_by_kind', p.decided_by_kind)
  put('decided_by_ref', p.decided_by_ref)
  put('cursor', p.cursor)
  if (p.limit !== undefined) query.limit = String(p.limit)
  // Exactly one boolean, or none. Never both — that is invalid_command by contract.
  if (p.view === 'effective') query.effective = 'true'
  else if (p.view === 'revoked') query.revoked = 'true'
  return http.get<WorkPage<WorkDecision>>(DECISIONS, {
    ...options,
    query,
    signal,
  })
}

/** True when this row may carry a current-state badge. History rows may not, and the
 * test for that is the engine's own projection marker, not the caller's memory of which
 * request it sent. */
export function attributesCurrentState(d: WorkDecision): boolean {
  return d.state === 'effective' || d.state === 'revoked'
}

// ---------------------------------------------------------------------------
// Mutations: one intent, three phases, one key
// ---------------------------------------------------------------------------

/** The route + verb for one command, so a caller cannot aim a command at the wrong
 * path. Mirrors modules/sessions/work_api.go workRoutes. */
export type WorkCommandName =
  | 'item.create'
  | 'item.update'
  | 'item.assign'
  | 'dependency.add'
  | 'dependency.remove'
  | 'acceptance.add'
  | 'acceptance.update'
  | 'acceptance.evaluate'
  | 'decision.set'
  | 'decision.supersede'
  | 'decision.revoke'
  | 'item.ready'
  | 'item.block'
  | 'item.unblock'
  | 'item.submit'
  | 'item.complete'
  | 'item.fail'
  | 'item.cancel'
  | 'item.archive'
  | 'lease.acquire'
  | 'lease.renew'
  | 'lease.release'
  | 'lease.takeover'
  | 'lease.revoke'
  | 'lease.clock_rebase'

/**
 * A WorkIntent is ONE operator intention, and it owns ONE idempotency key for its whole
 * life — including every retry.
 *
 * This is a type, not a convention, because the contract cannot protect us here and the
 * failure is silent and expensive: "Idempotency-Key se genera UNA VEZ por intención y se
 * REUTILIZA en el reintento. Si la regeneras al reintentar, un timeout de red se
 * convierte en DOBLE APLICACIÓN — y el kernel no puede protegerte, porque para él son
 * dos intenciones distintas."
 *
 * A retry is `applyWork(sameIntent)`. There is no code path that mints a key inside
 * apply, so regenerating one requires deliberately constructing a second intent — which
 * reads, correctly, as declaring a second intention.
 *
 * The key is generated at CONSTRUCTION, which is what lets the dialog print it before
 * transmitting, exactly as the canonical CLI does
 * (cmd/olivares/cmd_work.go:303-306: "idempotency key: %s (reuse this key for an
 * ambiguous retry)"). An operator holding the key can resolve an ambiguous timeout
 * themselves.
 */
export interface WorkIntent {
  readonly key: string
  /** Tenant captured beside the idempotency key when the operator creates the
   * intention. Every phase and replay must remain in this request scope. */
  readonly tenant: TenantRequestOptions['tenant']
  readonly command: WorkCommandName
  readonly path: string
  readonly method: 'POST' | 'PATCH' | 'DELETE'
  readonly body: Record<string, unknown>
  /** The ETag read from the server. Required on every apply but create; never
   * synthesized from a version number. */
  readonly etag: string | null
}

/** model.ParseID accepts a UUID, and the engine rejects anything else
 * (work_service.go:822). crypto.randomUUID is the platform's v4 generator; the browsers
 * this console supports all provide it in a secure context. */
function newIdempotencyKey(): string {
  return crypto.randomUUID()
}

/**
 * WHAT THE ENGINE REQUIRES BEFORE A COMMAND CAN SUCCEED — the fields without which the
 * kernel answers 400/422 no matter what else is right.
 *
 * This table exists because the first pass shipped SIX BUTTONS THAT COULD NEVER WORK.
 * block/fail/cancel were sent with an empty body, and work_state.go:292-294 demands
 * `boundedToken(Code,64)` and `boundedText(Reason,1,2048)` — both reject empty, so every
 * press was a guaranteed 400. The three acceptance verdicts were sent as `{state}`,
 * while work_state.go:318-321 requires an `acceptance` ARRAY of exactly one element, and
 * :196-201 additionally requires an evidence ref for passed/failed and an evidence HASH
 * for passed (422 acceptance_incomplete).
 *
 * A button that always fails is worse than an absent one: it teaches the operator that
 * the product is broken, and it does so in the surface whose whole purpose is telling
 * them the truth. The answer is not to remove the actions — it is to ask for what the
 * engine needs, which is what CommandField drives.
 */
export interface CommandField {
  name: string
  /** `number` exists because the engine takes int64 for fence and ttl_seconds, and
   * foldFields wrote every value as a STRING — which decodes into int64 as invalid_command:
   * the operator fills the field correctly and the command is still refused. The kind is what
   * tells the fold to coerce; it is not a rendering hint. */
  kind: 'token' | 'text' | 'hash' | 'number'
  required: boolean
  /** WHERE the value folds into the command document. The acceptance verdict's fields
   * live inside `acceptance[0]`, not at the root (work_state.go:318-321 requires the
   * array), and a field folded to the wrong level is rejected exactly like a missing
   * one — so the level is part of the contract, not a rendering detail. */
  path: 'root' | 'acceptance'
}

export function requiredFieldsFor(
  command: WorkCommandName,
  acceptanceState?: 'passed' | 'failed' | 'waived',
): CommandField[] {
  switch (command) {
    // work_state.go:371-404, read one case at a time rather than assumed uniform: the six do
    // NOT ask for the same things. `ttl_seconds` is absent ON PURPOSE — validWorkLeaseTTL
    // accepts 0 (:424-426), so it is optional, and demanding it would invent a requirement the
    // engine does not have and refuse a command the engine would take.
    case 'lease.acquire':
      return [
        { name: 'holder_sid', kind: 'token', required: true, path: 'root' },
      ]
    case 'lease.renew':
    case 'lease.release':
    case 'lease.takeover':
      // fence >= 1 on all three (:383-396). Acquire is the exception: there is no prior fence
      // to match, which is the same shape as create being the one command without If-Match.
      return [
        { name: 'holder_sid', kind: 'token', required: true, path: 'root' },
        { name: 'fence', kind: 'number', required: true, path: 'root' },
      ]
    case 'lease.revoke':
      // Revoking takes somebody else's lease away, so the engine REQUIRES the reason
      // (:397-399), bounded 1..2048 — where release merely accepts one.
      return [
        { name: 'fence', kind: 'number', required: true, path: 'root' },
        { name: 'reason', kind: 'text', required: true, path: 'root' },
      ]
    case 'lease.clock_rebase':
      // Rebasing a clock is an evidence-bearing act: a decision to point at, and the evidence
      // itself (:401-404). Neither is optional and no holder or fence is asked for.
      return [
        { name: 'decision_id', kind: 'token', required: true, path: 'root' },
        { name: 'evidence_ref', kind: 'text', required: true, path: 'root' },
      ]
    case 'item.block':
    case 'item.fail':
    case 'item.cancel':
      // work_state.go:292-294 — both mandatory, and `code` must match ^[a-z0-9][a-z0-9._-]*$.
      return [
        { name: 'code', kind: 'token', required: true, path: 'root' },
        { name: 'reason', kind: 'text', required: true, path: 'root' },
      ]
    case 'acceptance.evaluate': {
      // work_state.go:196-201. passed and failed both need custody evidence; passed
      // additionally needs its hash, and the engine answers 422 acceptance_incomplete
      // rather than pretending the criterion was evaluated.
      if (acceptanceState === 'waived') {
        return [
          {
            name: 'waiver_decision_id',
            kind: 'text',
            required: false,
            path: 'acceptance',
          },
        ]
      }
      const fields: CommandField[] = [
        {
          name: 'evidence_ref',
          kind: 'text',
          required: true,
          path: 'acceptance',
        },
      ]
      if (acceptanceState === 'passed') {
        fields.push({
          name: 'evidence_hash',
          kind: 'hash',
          required: true,
          path: 'acceptance',
        })
      }
      return fields
    }
    default:
      return []
  }
}

/** Fold operator-supplied values into the command document at the level each one
 * belongs to, preserving whatever the caller already put there (e.g. the acceptance
 * state the button chose). */
export function foldFields(
  body: Record<string, unknown>,
  fields: CommandField[],
  values: Record<string, string>,
): Record<string, unknown> {
  const out: Record<string, unknown> = { ...body }
  const acceptance = Array.isArray(out.acceptance)
    ? { ...((out.acceptance as Record<string, unknown>[])[0] ?? {}) }
    : null
  for (const f of fields) {
    const v = (values[f.name] ?? '').trim()
    if (!v) continue
    const folded: unknown = f.kind === 'number' ? Number(v) : v
    if (f.path === 'acceptance' && acceptance) acceptance[f.name] = folded
    else if (f.path === 'root') out[f.name] = folded
  }
  if (acceptance) out.acceptance = [acceptance]
  return out
}

/** The acceptance verdict document the engine actually accepts (work_state.go:187-202).
 * Kept here, next to the command table, because the shape is a contract and not a
 * component's business. */
export function acceptanceEvaluateBody(
  state: 'passed' | 'failed' | 'waived',
  evidenceRef: string,
  evidenceHash: string,
  waiverDecisionID: string,
): Record<string, unknown> {
  const entry: Record<string, unknown> = { state }
  // Only for the states that carry custody evidence; sending them with `pending` is
  // itself invalid_command (:193), and this UI never offers pending.
  if (state === 'passed' || state === 'failed') entry.evidence_ref = evidenceRef
  if (state === 'passed') entry.evidence_hash = evidenceHash
  if (state === 'waived' && waiverDecisionID)
    entry.waiver_decision_id = waiverDecisionID
  return { acceptance: [entry] }
}

/** Complete an intent's body while KEEPING ITS KEY. This is how the operator fills in
 * what the engine requires without the change becoming a second intention: the key is
 * the identity of the intention, and collecting a reason does not create a new one. */
export function withBody(
  intent: WorkIntent,
  extra: Record<string, unknown>,
): WorkIntent {
  return { ...intent, body: { ...intent.body, ...extra } }
}

export interface BuildIntentArgs {
  tenant: TenantRequestOptions['tenant']
  command: WorkCommandName
  /** The work item / decision the command targets, when the route needs it in the path. */
  itemId?: string
  decisionId?: string
  dependencyId?: string
  criterionId?: string
  body?: Record<string, unknown>
  etag?: string | null
}

/** Resolve a command to its route, mirroring workRoutes. Transitions all share one
 * route and carry the verb in the body (validRouteBodyCommand, work_api.go:263-275). */
export function buildIntent(args: BuildIntentArgs): WorkIntent {
  const body: Record<string, unknown> = { ...(args.body ?? {}) }
  let path: string
  let method: 'POST' | 'PATCH' | 'DELETE' = 'POST'

  switch (args.command) {
    case 'item.create':
      path = WORK_ITEMS
      break
    // ⛔ LOS COMANDOS DE LEASE SON MUTACIONES DE TRABAJO, no un POST aparte. Medido contra un
    // motor vivo el 2026-08-16: un POST directo a .../lease/acquire contesta **400
    // mode_required** — pasan por handleWorkMutation (work_api.go:110-131) igual que todo lo
    // demás, así que exigen ?mode=, If-Plan-Hash e Idempotency-Key. Un cliente que los tratara
    // como una llamada suelta enseña seis botones que no hacen nada, y NINGUNA prueba con el
    // fetch mockeado puede verlo: sólo abrir la pantalla contra el motor (canon §1.10).
    // Y al ir por aquí heredan lo que la propia página promete — «every change is planned
    // before it is applied» — con el plan a la vista antes de aplicarse.
    case 'lease.acquire':
      path = `${WORK_ITEMS}/${id(args.itemId ?? '')}/lease/acquire`
      break
    case 'lease.renew':
      path = `${WORK_ITEMS}/${id(args.itemId ?? '')}/lease/renew`
      break
    case 'lease.release':
      path = `${WORK_ITEMS}/${id(args.itemId ?? '')}/lease/release`
      break
    case 'lease.takeover':
      path = `${WORK_ITEMS}/${id(args.itemId ?? '')}/lease/takeover`
      break
    case 'lease.revoke':
      path = `${WORK_ITEMS}/${id(args.itemId ?? '')}/lease/revoke`
      break
    case 'lease.clock_rebase':
      path = `${WORK_ITEMS}/${id(args.itemId ?? '')}/lease/clock-rebase`
      break
    case 'item.update':
      path = `${WORK_ITEMS}/${id(args.itemId ?? '')}`
      method = 'PATCH'
      break
    case 'item.assign':
      path = `${WORK_ITEMS}/${id(args.itemId ?? '')}/assignments`
      break
    case 'dependency.add':
      path = `${WORK_ITEMS}/${id(args.itemId ?? '')}/dependencies`
      break
    case 'dependency.remove':
      path = `${WORK_ITEMS}/${id(args.itemId ?? '')}/dependencies/${id(args.dependencyId ?? '')}`
      method = 'DELETE'
      break
    case 'acceptance.add':
      path = `${WORK_ITEMS}/${id(args.itemId ?? '')}/acceptance`
      break
    case 'acceptance.update':
    case 'acceptance.evaluate':
      path = `${WORK_ITEMS}/${id(args.itemId ?? '')}/acceptance/${id(args.criterionId ?? '')}`
      method = 'PATCH'
      break
    case 'decision.set':
    case 'decision.supersede':
      path = DECISIONS
      body.command = args.command
      break
    case 'decision.revoke':
      path = `${DECISIONS}/${id(args.decisionId ?? '')}/revoke`
      break
    default:
      // Every remaining name is an FSM transition: one shared route, verb in the body.
      path = `${WORK_ITEMS}/${id(args.itemId ?? '')}/transitions`
      body.command = args.command
      break
  }

  return {
    key: newIdempotencyKey(),
    tenant: args.tenant,
    command: args.command,
    path,
    method,
    body,
    etag: args.etag ?? null,
  }
}

async function send<T>(
  intent: WorkIntent,
  mode: WorkMode,
  headers: Record<string, string>,
  signal?: AbortSignal,
): Promise<{ data: T; headers: Headers }> {
  const opts = { tenant: intent.tenant, query: { mode }, headers, signal }
  if (intent.method === 'PATCH') {
    const r = await http.patchWithMeta<T>(intent.path, intent.body, opts)
    return { data: r.data, headers: r.headers }
  }
  if (intent.method === 'DELETE') {
    const r = await http.deleteWithMeta<T>(intent.path, intent.body, opts)
    return { data: r.data, headers: r.headers }
  }
  const r = await http.postWithMeta<T>(intent.path, intent.body, opts)
  return { data: r.data, headers: r.headers }
}

/** mode=validate — writes ZERO rows. Returns an Assessment whose verdict may be
 * NO_HE_PODIDO_MIRAR on a 200; see the note at the top of this file. */
export async function validateWork(
  intent: WorkIntent,
  signal?: AbortSignal,
): Promise<Assessment> {
  const { data } = await send<Assessment>(intent, 'validate', {}, signal)
  return data
}

/** mode=plan — writes ZERO rows and returns the canonical plan hash plus the row
 * effects the apply would have. Showing this to the operator before apply is the whole
 * point of the three-phase protocol; a console that jumps straight to apply throws away
 * the kernel's best property. */
export async function planWork(
  intent: WorkIntent,
  signal?: AbortSignal,
): Promise<Plan> {
  const { data } = await send<Plan>(intent, 'plan', {}, signal)
  return data
}

export interface ApplyOutcome {
  result: CommandResult
  /** The new concurrency token, straight from the response header. */
  etag: string | null
  /**
   * The engine answered from the idempotency ledger: this intention was ALREADY
   * applied. The body is byte-identical to the original apply's, so this header is the
   * only way to know — and it must be rendered as "already applied", never as a fresh
   * success. (Contract: replay returns the original status and body byte for byte,
   * plus Idempotency-Replayed: true.)
   */
  replayed: boolean
}

/**
 * mode=apply. Sends the intent's key — never a new one — and `If-Match` for every
 * command but create.
 *
 * WHY create IS THE EXCEPTION: there is no prior version to match against. The contract
 * says "apply exige Idempotency-Key UUID y, salvo create, If-Match: \"vN\"", and the
 * engine's parseWorkETag treats an absent If-Match as "no expectation" rather than as
 * an error (work_api.go:321-333). Sending one on a create would be asserting a version
 * for a row that does not exist yet.
 *
 * ON 409 THIS FUNCTION DOES NOTHING CLEVER. It throws. It does NOT re-read the item and
 * retry with the fresh ETag: that would overwrite whatever the other writer just did,
 * wearing the face of a success. The caller re-reads and SHOWS the divergence; the
 * human decides.
 */
export async function applyWork(
  intent: WorkIntent,
  planHash?: string,
  signal?: AbortSignal,
): Promise<ApplyOutcome> {
  const headers: Record<string, string> = { 'Idempotency-Key': intent.key }
  if (intent.command !== 'item.create' && intent.etag) {
    headers['If-Match'] = intent.etag
  }
  // THE PLAN→APPLY GUARANTEE, which the first pass displayed and then threw away.
  // The dialog showed the operator a canonical plan hash and applied WITHOUT binding to
  // it, so what was approved and what was applied were only related by luck. The engine
  // offers the binding (work_api.go:155-165: If-Plan-Hash sets ExpectedPlanHash), and
  // using it turns "the world changed between the plan and the apply" from an invisible
  // race into a refusal the operator can see and re-plan.
  if (planHash) headers['If-Plan-Hash'] = planHash
  const { data, headers: res } = await send<CommandResult>(
    intent,
    'apply',
    headers,
    signal,
  )
  return {
    result: data,
    etag: res.get('ETag'),
    replayed: res.get('Idempotency-Replayed') === 'true',
  }
}

/**
 * Classify an apply failure into what the operator must be told. These are genuinely
 * different actions, and collapsing any pair of them into "it failed" is what makes an
 * operator retry the one thing they must not.
 *
 *  - 'conflict-version': If-Match lost. Someone else moved the item. RE-READ and show
 *    the divergence. Never auto-retry.
 *  - 'version-required': we sent no If-Match on a command that demands one. This is OUR
 *    bug, not a race, and it is worth its own name so it cannot hide inside "conflict".
 *  - 'conflict-idempotency': the same key was reused with a DIFFERENT body, ETag or
 *    plan. This is information — the operator is about to apply something other than
 *    what they think — not an error to swallow.
 *  - 'conflict-domain': the kernel refused on its own rules (illegal_transition,
 *    dependency_cycle, target_closed…). Re-reading will not help; the command was wrong.
 *  - 'unknown': NO_HE_PODIDO_MIRAR. Whether the write happened is UNDETERMINED. The
 *    safe move is to retry with the SAME key, which is exactly what the intent holds.
 *
 * THE STATUS CODES ARE MEASURED, NOT ASSUMED, and the first version of this function
 * got them wrong in a way worth recording: it tested only for 409 and mapped
 * `state_conflict` to the version case. The engine's own suite says otherwise
 * (modules/sessions/work_api_test.go:292-319) — a stale If-Match is
 * 412 version_mismatch, a missing one is 428 version_required, and 409 is what a
 * DIVERGENT KEY REUSE returns. Under the original mapping the single most important
 * outcome here — "someone else moved this item" — fell through to 'other' and the
 * operator would have been shown a generic failure with no re-read offered.
 */
export type ApplyFailure =
  | 'conflict-version'
  | 'version-required'
  | 'plan-changed'
  | 'conflict-idempotency'
  | 'conflict-domain'
  | 'unknown'
  | 'other'

export function classifyApplyFailure(err: unknown): ApplyFailure {
  if (isUnknownVerdict(err)) return 'unknown'
  if (!(err instanceof ApiError)) return 'other'
  const code = workErrorCode(err) ?? ''
  // Classify on the CODE first: it is the contract. The status is checked only where
  // the code alone would be ambiguous, so a future code sharing a status cannot be
  // silently absorbed into the wrong bucket.
  // plan_changed shares 412 with version_mismatch and needs a DIFFERENT recovery:
  // re-PLAN and re-confirm, not re-read and compare. Classifying on the code first is
  // what keeps them apart (work_service.go:900-901).
  if (code === 'plan_changed') return 'plan-changed'
  if (code === 'version_mismatch') return 'conflict-version'
  if (code === 'version_required') return 'version-required'
  if (code === 'idempotency_key_reused') return 'conflict-idempotency'
  if (err.status === 409) return 'conflict-domain'
  if (err.status === 412) return 'conflict-version'
  if (err.status === 428) return 'version-required'
  return 'other'
}

/* ── K2 leases (C07-01) ───────────────────────────────────────────────────────────────────
 * The eight lease routes with their THREE permission levels, as the engine registers them
 * (`modules/sessions/work_api.go:42-49`):
 *
 *   permLeaseRead    GET  /leases                              list
 *   permLeaseRead    GET  /work-items/{id}/lease               one
 *   permLeaseWrite   POST /work-items/{id}/lease/acquire
 *   permLeaseWrite   POST /work-items/{id}/lease/renew
 *   permLeaseWrite   POST /work-items/{id}/lease/release
 *   permLeaseAdmin   POST /work-items/{id}/lease/takeover
 *   permLeaseAdmin   POST /work-items/{id}/lease/revoke
 *   permLeaseAdmin   POST /work-items/{id}/lease/clock-rebase
 *
 * Until this landed the console called NONE of them: it painted the derived `leased: boolean`
 * and offered no operation on the lease at all.
 */

/** The list. Its query is built from the closed `LeaseFilters` type and never from caller input:
 *  the engine's allowlist is strict and rejects the whole request, not the stray key. */
export async function listLeases(
  filters: LeaseFilters,
  options: TenantRequestOptions,
  signal?: AbortSignal,
): Promise<WorkLeasePage> {
  const query: Record<string, string> = {}
  for (const key of [
    'limit',
    'cursor',
    'work_item_id',
    'holder_sid',
    'state',
    'expires_before',
  ] as const) {
    const value = filters[key]
    if (value !== undefined && value !== '') query[key] = String(value)
  }
  // ⛔ La ruta va SOLA y la query por la opción del cliente. Concatenarla —`${LEASES}${qs}`—
  // deja el sitio como `/v1/m/sessions/leases${}` y el censo de rutas lo marca IRRESOLUBLE:
  // contado, nunca comprobado contra una ruta registrada. Medido aquí mismo el 2026-08-16, y
  // además reventaba el presupuesto (15 sobre 14).
  return http.get<WorkLeasePage>(LEASES, { ...options, query, signal })
}

/**
 * ⛔ THE ETag THIS RETURNS IS THE PARENT WORKITEM'S VERSION, NOT THE LEASE ROW'S, and that is
 * why it is handed back beside the body instead of leaving the caller to rebuild one.
 *
 * The engine says it in its own comment (`work_api.go:643-645`): lease mutations take the parent
 * WorkItem's If-Match, and returning the lease row version here made the natural
 * `GET lease → renew/release` sequence fail with **412** as soon as an unrelated write had
 * advanced only the parent.
 *
 * ⇒ `lease.version` is NOT an If-Match. Rebuilding `"v${lease.version}"` is the exact defect this
 * client exists not to commit, and it is the same rule `api.ts` already states above for the rest
 * of the kernel: **the ETag is READ from the response, never synthesized from a version number.**
 */
export async function getLease(
  itemId: string,
  options: TenantRequestOptions,
  signal?: AbortSignal,
): Promise<{ lease: WorkLease; etag: string | null }> {
  const { data, headers } = await http.getWithMeta<WorkLease>(
    `${WORK_ITEMS}/${id(itemId)}/lease`,
    { ...options, signal },
  )
  return { lease: data, etag: headers.get('ETag') }
}
