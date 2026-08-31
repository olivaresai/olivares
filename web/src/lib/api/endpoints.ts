// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Typed endpoint helpers for the engine's CORE REST surface (/v1). Each function
// is a thin, named wrapper over the HTTP client — no business logic (ARCHITECTURE.md).
//
// PATTERN: a feature-module adds its own endpoints file under
// `features/<module>/api.ts` calling the same `http` helpers against its module
// routes (`/v1/m/<namespace>/…`), and its own query keys (see query.ts). This file
// owns only the engine-core endpoints (auth, identity, tenants, and the
// representative agents/access/audit surfaces) that the foundation itself uses.
import { http } from './client'
import type {
  AcceptInviteRequest,
  AcceptInviteResponse,
  AgentDTO,
  AgentInput,
  AuditEventDTO,
  AuditPubkeyResponse,
  AuditVerifyResponse,
  CreateOrgRequest,
  CreateUserRequest,
  GrantMembershipRequest,
  IssueTokenRequest,
  IssueTokenResponse,
  ListResponse,
  LoginRequest,
  LoginResponse,
  OrgDTO,
  ServerInfo,
  SetupRequest,
  SetupResponse,
  UserDTO,
  Whoami,
} from './types'

export interface ListParams {
  cursor?: string
  limit?: number
}

/** El techo de las listas del NUCLEO (`/v1`), y el numero no es una preferencia.
 *
 * El almacen generico acota cada pagina en `core/internal/store/sqlstore/generic.go:26-29`:
 * `defaultLimit = 100` y `maxLimit = 1000`. Sin `?limit` el motor sirve CIEN filas y publica
 * `has_more`; pedir mas de mil se recorta a mil EN SILENCIO. Asi que mil es el maximo que el
 * motor concede, y por eso es un techo de verdad y no un adorno.
 *
 * ⚠ `limit: 100` NO seria un techo: es el valor por omision del motor, asi que escribirlo no
 *   cambia nada de lo que ya pasa. La misma trampa esta documentada para la familia
 *   `/v1/m/sessions` en `features/work/api.ts` (alli el maximo es 200, no 1000: cada familia
 *   tiene el suyo y copiar el numero de al lado es como se rompe esto).
 *
 * ⛔ Y SUBIR EL SUELO NO COMPLETA LA LISTA. Con mil filas cargadas el motor sigue pudiendo
 *   decir `has_more`, y lo que cierra ese hueco es DECLARARLO en pantalla — el aviso
 *   compartido `<ListTruncationBadge>`, que es trabajo por vista y va aparte. */

/** Authentication + first-boot setup (core/api/handlers_auth.go). */
export const authApi = {
  /** Unauthenticated: version, engine, setup state, license status. */
  serverInfo: () =>
    http.get<ServerInfo>('/v1/server-info', { anonymous: true }),
  /** First-boot: create the first ORGANIZATION and the superadmin that owns it,
   * with the one-time setup token. The response carries the organization, whose
   * tenant id the console selects so the operator lands on a usable panel. */
  setup: (req: SetupRequest) =>
    http.post<SetupResponse>('/v1/setup', req, { anonymous: true }),
  /** Exchange email/password for an opaque session token. */
  login: (req: LoginRequest) =>
    http.post<LoginResponse>('/v1/auth/login', req, { anonymous: true }),
  /** Redeem a single-use onboarding invite: sets the password and activates the
   * account. Anonymous — the invitee has no session; the token is the gate.*/
  acceptInvite: (req: AcceptInviteRequest) =>
    http.post<AcceptInviteResponse>('/v1/invites/accept', req, {
      anonymous: true,
    }),
  /** Revoke the calling session (204). */
  /**
   * Renew the calling session's token. The engine ROTATES the credential and invalidates the
   * old one (core/api/api_test.go:296-320), so the response is a whole new session
   * envelope and the previous token stops working the moment this returns.
   */
  refresh: () => http.post<LoginResponse>('/v1/auth/refresh'),
  logout: () => http.post<void>('/v1/auth/logout'),
  /** The calling principal and its tenant grants. */
  whoami: () => http.get<Whoami>('/v1/auth/whoami'),
}

/** Identity & access management (core/api/handlers_core.go). */
// ⛔ EL TECHO DE LA CAPA COMPARTIDA. Estas llamadas las estrena cualquier feature, asi que un
//    recorte silencioso aqui se hereda sin que nadie lo decida. `maxLimit` del store es 1000
//    (`core/internal/store/sqlstore/generic.go`), asi que pedir mas no da mas: 1000 es el techo
//    REAL, y pedirlo explicitamente es lo que hace que el `has_more` que vuelve signifique algo.
// ⛔ UNA SOLA CONSTANTE. El merge dejo dos con el MISMO valor: `LIST_CEILING` (de `main`, 5 usos)
//    y `CORE_LIST_PAGE_MAX` (de esta rama, usada solo por su propio test). Dos nombres para un
//    techo son dos sitios donde cambiarlo y uno donde olvidarse. Se queda el de la punta y se
//    EXPORTA para que el test tenga a que atarse en vez de declarar su propio 1000.
export const LIST_CEILING = 1000

export const iamApi = {
  /** MOTOR MEDIDO el 2026-08-27: `handleListUsers` (core/api/handlers_core.go:233-254)
   *  construye su consulta con `parseListQuery(r)` (:782-793), que LEE `?limit` y `?cursor`, y
   *  publica `out.HasMore`. Es el caso (b) de la receta del testigo: el techo del llamante
   *  MANDA, asi que se pide.
   *
   *  ⛔ EL VALOR SE RESUELVE, NO SE ORDENA, y esa es la correccion del contraste `sol max`
   *  (hallazgo C). La primera version era `{ limit: LIST_CEILING, ...params }`, confiando
   *  en que la propiedad escrita mas tarde gana. Gana — pero `ListParams.limit` es OPCIONAL, asi
   *  que un llamante puede pasar `{ limit: undefined }` y el spread posterior **borra el techo**;
   *  el serializador omite los `undefined` (`lib/api/client.ts:253-257`) y el motor vuelve a su
   *  omision de CIEN. O sea: el techo desaparecia justo en la forma que mas se parece a «no me
   *  importa el limite». Con `??` el numero del llamante sigue mandando y la ausencia —explicita
   *  o implicita— cae en el techo. Ya no depende del orden de las claves. */
  listUsers: (params?: ListParams) =>
    http.get<ListResponse<UserDTO>>('/v1/users', {
      query: { ...params, limit: params?.limit ?? LIST_CEILING },
    }),
  createUser: (req: CreateUserRequest) => http.post<UserDTO>('/v1/users', req),
  issueToken: (req: IssueTokenRequest) =>
    http.post<IssueTokenResponse>('/v1/tokens', req),
  revokeToken: (id: string) =>
    http.delete<void>(`/v1/tokens/${encodeURIComponent(id)}`),
  grantMembership: (req: GrantMembershipRequest) =>
    http.post<{ id: string; user_id: string; tenant: string; role: string }>(
      '/v1/memberships',
      req,
    ),
}

/** Tenant/org provisioning (superadmin) — powers the org switcher. */
export const systemApi = {
  // ⛔ EL ORDEN DE ESTE COMENTARIO IMPORTA, y lo medi: el testigo busca la exencion en las
  //    SEIS lineas JUSTO ENCIMA de la llamada. Con la explicacion arriba y el aviso debajo,
  //    la linea de exencion queda fuera de esa ventana y el gate enrojece diciendo «pon
  //    techo, o exime» — con la exencion puesta, solo que una linea mas arriba. Por eso el
  //    razonamiento va primero y la exencion va PEGADA a la llamada.
  // ⛔ LA LINEA DE ABAJO VA PEGADA A LA LLAMADA Y NO ARRIBA, y no es estetica: el testigo lee la
  // seis, UNA frase mas en este comentario lo empuja fuera de la ventana y el gate enrojece
  // arriba. Medido: se comprobo anadiendo una frase y el gate paso a rc=1. Con la razon pegada al
  // sujeto, este comentario puede crecer todo lo que haga falta.

  // SIN TECHO A PROPOSITO: `handleListOrgs` (core/api/handlers_core.go:739) llama a
  // `sys.ListOrgs(ctx)`, que NO acepta consulta: drena la tabla entera y no fija `has_more`.
  // No hay recorte que declarar, y un techo aqui seria decorativo — mentiria diciendo que
  // manda sobre una consulta que el handler ni lee.
  listOrgs: () => http.get<ListResponse<OrgDTO>>('/v1/system/orgs'),
  createOrg: (req: CreateOrgRequest) =>
    http.post<OrgDTO>('/v1/system/orgs', req),
}

/** Agents — the representative tenant-scoped resource (the active tenant header is
 * attached automatically by the client). */
export const agentsApi = {
  /** MOTOR MEDIDO el 2026-08-27: `handleListAgents` (core/api/handlers_core.go:60-84) usa
   *  `parseFilteredListQuery(r, confinedWS)` (:797-830), que extiende `parseListQuery` — LEE
   *  `?limit` y `?cursor` — y publica `out.HasMore`. Caso (b): se pide techo.
   *
   *  Misma resolucion con `??` que en `listUsers`, y por la misma razon medida: el orden de las
   *  claves no protege de un `{ limit: undefined }`. Aqui el contraste encontro ademas que el
   *  ORDEN solo estaba probado para users, asi que invertir SOLO esta llamada dejaba la bateria
   *  entera en verde — hay celda propia desde entonces. */
  list: (params?: ListParams) =>
    http.get<ListResponse<AgentDTO>>('/v1/agents', {
      query: { ...params, limit: params?.limit ?? LIST_CEILING },
    }),
  get: (id: string) =>
    http.get<AgentDTO>(`/v1/agents/${encodeURIComponent(id)}`),
  create: (input: AgentInput) => http.post<AgentDTO>('/v1/agents', input),
  update: (id: string, input: AgentInput) =>
    http.patch<AgentDTO>(`/v1/agents/${encodeURIComponent(id)}`, input),
  remove: (id: string) =>
    http.delete<void>(`/v1/agents/${encodeURIComponent(id)}`),
}

// The access-graph (R/RW map) client was REMOVED in (C2): it was dead
// code with no importers, and its drift call pointed at the raw, UNRECONCILED core
// route (since removed). The live access-map view uses accessMapApi
// (src/features/access-map/api.ts → /v1/m/accessmap/*), which already consumes the
// reconciled diff — the single source of truth.

/** Page params for the evidence ledger. NOTE the ledger is keyset-paginated by
 * `from` (a 1-based sequence number), NOT the opaque `cursor` the generic ListParams
 * carries. Unfiltered continuation is `lastSeq + 1`; filtered continuation MUST use
 * the response's `next_from` (the last examined sequence may be far beyond the last
 * sparse match). Using `cursor` here would be silently ignored by the engine and pin
 * every request to page one. */
export interface AuditListParams {
  /** 1-based sequence to start the page at (default 1 = the genesis event). */
  from?: number
  limit?: number
  /** Inclusive RFC3339 lower timestamp bound. */
  since?: string
  /** Inclusive RFC3339 upper timestamp bound. */
  until?: string
  /** Exact audit actor. */
  actor?: string
  /** Audit action prefix. */
  action?: string
  /** Exact target kind. */
  target_kind?: string
  /** Exact target id. */
  target_id?: string
  /** Case-insensitive substring over action/actor/target fields. */
  q?: string
  /** Action prefixes to OMIT, using the same prefix rule as `action`. Repeatable —
   * pass an array and each entry becomes its own `exclude_action=` occurrence.
   *
   * It filters what is RETURNED and nothing else: the ledger still records every
   * event, including the `audit.read` this very request appends, and a request
   * without it still returns them all. That distinction is the reason it exists —
   * the alternative fix for a bell drowning in its own reads was to stop recording
   * them, which would have bought a quiet notification by destroying evidence. */
  exclude_action?: string[]
}

/** Filtered list requests expose scan progress because a sparse match page may
 * stop before the ledger head. Unfiltered responses keep the legacy shape and
 * leave these fields absent. */
export interface AuditListResponse extends ListResponse<AuditEventDTO> {
  next_from?: number
  scan_complete?: boolean
  /** The highest sequence this tenant's ledger has RECORDED, `0` when it has never
   * recorded one. Present on EVERY audit list response, filtered or not (the engine
   * marks it required in the published contract), and it is the only field that
   * addresses the END of the chain: `from` pages FORWARDS, so the newest activity is
   * `from = max(1, head_seq - N + 1)` with the page reversed for display.
   *
   * Two bounds on that, and they are not pedantry — each one is a real ledger state:
   * the window is N SEQUENCE POSITIONS, not N rows, so a chain carrying a declared
   * `audit.gap` returns a SHORTER page with older events still below it; and while
   * head_seq is read before the request's own self-audit event joins the chain and is
   * never behind the highest `seq` in `items`, it does not identify the exact snapshot
   * the page was read from, because a concurrent append can land between the two
   * reads. */
  head_seq: number
}

/** Evidence ledger (tamper-evident audit) — core surface, RBAC-gated `audit:read`.
 * The web renders the engine's events and verdicts; it never recomputes or repairs
 * the chain (ARCHITECTURE.md, docs/SECURITY-HARDENING.md).
 *
 * ⛔ ESTAS DOS ERAN INVISIBLES PARA EL CENSO DE RECORTE, y por eso llevan techo desde.
 *
 * `AuditListResponse` EXTIENDE `ListResponse<AuditEventDTO>` (arriba), asi que son listas
 * truncables — y el contrato lo dice: `web/openapi/openapi.json` declara el campo de recorte en el
 * 200 de `listAuditEvents` y `listSystemAuditEvents`. Pero la sonda de la capa compartida casaba
 * el envoltorio generico ESCRITO EN LA LLAMADA, y estas dos escriben el alias: **no las veia**. No
 * estaban exentas; no se miraban. Arreglado en este mismo lote (`alias_de_lista`), salieron a la
 * luz — y con ellas la pregunta que antes nadie hacia.
 *
 * ⚠ El literal que la sonda busca NO se reproduce en este comentario a proposito. La capa
 *   compartida no descontaba comentarios, asi que la frase que describia el patron se contaba como
 *   una llamada: «(sin nombre) endpoints.ts:199», sin techo, gate ROJO. **Documentar el punto
 *   ciego lo disparaba.** Tambien arreglado en este lote; la precaucion se queda porque cuesta
 *   nada y el habito es lo que sobrevive al arreglo.
 */
/** El techo del LEDGER, y NO es `LIST_CEILING` aunque el numero coincida.
 *
 * ⛔ LA REGLA ES OTRA, y por eso hay constante propia. `handleAuditList`
 *    (core/api/handlers_audit.go:91-94) hace:
 *
 *        limit := int(queryInt64(r, "limit", 100))
 *        if limit <= 0 || limit > 1000 { limit = 100 }
 *
 *    El almacen generico (`sqlstore/generic.go:280-286`) ACOTA a su maximo; esto **cae de vuelta a
 *    100**. Pedir 1001 devuelve CIEN filas, no mil. Compartir la constante del nucleo haria que
 *    subirla alli —un cambio legitimo en SU familia— convirtiera esta llamada en el minimo justo
 *    cuando pide el maximo. Misma leccion que `features/work/api.ts`, donde el maximo es 200 y
 *    pasarse es un 400 duro: cada familia tiene el suyo, y copiar el numero de al lado es como se
 *    rompe esto. Lo ata `core/internal/store/sqlstore` no: lo ata su propio test de paridad.
 *
 * ⛔ EL TECHO VA RESUELTO CON `??`, no por orden de claves, asi que **el llamante siempre gana**.
 *    Hoy es inerte: los dos llamantes traen el suyo a proposito —`notification-bell` pide 10 (un
 *    preview) y `audit-view` pagina de 100 con «cargar mas»—. Protege al tercero que lo omita, que
 *    sin esto se llevaria CIEN filas creyendo que pidio todo lo que hay.
 *
 * ⚠ Y no completa la lista: el ledger pagina por `from`, no por cursor (ver `AuditListParams`),
 *   asi que esto sube el suelo de UNA pagina. Lo que cierra el hueco es declarar el recorte en
 *   pantalla, y `audit-view` ya lo hace con el campo del envoltorio y `fetchNextPage`.
 */
export const AUDIT_PAGE_MAX = 1000

export const auditApi = {
  /** A page of the active tenant's ledger from `from` (default the genesis event). */
  list: (params?: AuditListParams) =>
    http.get<AuditListResponse>('/v1/audit', {
      query: { ...params, limit: params?.limit ?? AUDIT_PAGE_MAX },
    }),
  /** A page of the SYSTEM-tenant ledger (the superadmin auth-partition chain).
   * Superadmin only — the engine returns 403 to a tenant-bound principal. */
  systemList: (params?: AuditListParams) =>
    http.get<AuditListResponse>('/v1/audit/system', {
      query: { ...params, limit: params?.limit ?? AUDIT_PAGE_MAX },
    }),
  /** The engine's verdict on the chain + its signed checkpoints (from `from`). */
  verify: (params?: { from?: number }) =>
    http.get<AuditVerifyResponse>('/v1/audit/verify', { query: { ...params } }),
  /** The Ed25519 checkpoint key for offline verification of an export. */
  pubkey: () => http.get<AuditPubkeyResponse>('/v1/audit/pubkey'),
}
