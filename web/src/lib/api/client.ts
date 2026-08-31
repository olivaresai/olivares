// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ApiError, NetworkError, parseErrorEnvelope } from './errors'

/**
 * The HTTP client is a THIN layer over fetch. It reimplements no server logic
 * (ARCHITECTURE.md) — it consumes the engine's REST API on the SAME origin (the web is
 * embedded). Responsibilities: relative base URL, bearer-token + tenant-header
 * injection, the error envelope → ApiError mapping, and the 401 hook.
 *
 * Auth/tenant are read through injected getters (configureApiClient) rather than
 * importing the stores, so this module stays dependency-free and trivially
 * testable, and the app wires the real session/tenant/router at bootstrap.
 */
interface ClientConfig {
  /** Current opaque session/API bearer token, or null when anonymous. */
  getToken: () => string | null
  /** Active tenant id for the X-Olivares-Tenant header, or null. */
  getTenant: () => string | null
  /** Called when an authenticated request gets 401 (session expired/revoked). */
  onUnauthorized: () => void
  /** Rotate the session credential and install the new one. Resolves true when a fresh
   * token is in place, false when the session is genuinely over.
   *
   * ⛔ WHY THIS EXISTS. Until now an authenticated 401 went straight to onUnauthorized:
   * the session was cleared, the router sent the operator to /login, and **whatever they
   * had half-filled was gone**. The engine has had `POST /v1/auth/refresh` all along
   * (core/api/handlers_auth.go:269, tested at core/api/api_test.go:304) and the console
   * never called it, so a token that merely EXPIRED was treated exactly like one that was
   * REVOKED. Those are different facts and only one of them should cost the operator work.
   *
   * Left undefined the client behaves exactly as before, which is what every existing test
   * relies on. */
  refreshSession?: () => Promise<boolean>
  /** ISO instant at which the current session credential dies, or null when unknown.
   *
   * ⛔ THIS IS THE FIELD THAT MAKES RENEWAL WORK AT ALL, and the 401 path alone does not.
   * Measured in the engine: the authenticator rejects an expired session outright
   * (core/auth/authenticator.go:140) and `RefreshSession` refuses one explicitly —
   * "refresh extends a live session, it never resurrects a dead one"
   * (core/auth/authenticator.go:572). So by the time a request 401s BECAUSE the credential
   * expired, refreshing with that same credential also 401s. Retrying after the fact
   * cannot fix expiry; only renewing BEFORE it can. */
  getExpiresAt?: () => string | null
}

/** How close to expiry a request renews first. Wide enough that a slow round trip cannot
 * land after the deadline, narrow enough that an idle console still times out. */
const MARGEN_RENOVACION_MS = 120_000

/**
 * ⛔ LA RUTA DE RENOVACIÓN NO SE RENUEVA A SÍ MISMA, Y ESTO ES UNA SOLA FUNCIÓN A PROPÓSITO.
 *
 * La exclusión existía —`puedeReintentar` la tenía desde el principio, con su razón escrita: pedir
 * un refresco en respuesta al 401 del propio refresco es recursión sin fondo—. Pero estaba escrita
 * en UNO de los dos caminos. El preventivo, que llegó después, no la copió.
 *
 * Y en producción `refreshSession` es otra `apiFetch('/v1/auth/refresh')` (`app/providers.tsx`), o
 * sea que sin esta guarda: petición → «caduca pronto» → renovar → apiFetch del refresco → «caduca
 * pronto» otra vez → renovar… **Medido con el cableado real: 1242 envíos para una sola petición.**
 * No se veía en los tests porque sustituían la renovación por una función de juguete que no vuelve
 * a entrar en el cliente.
 *
 * Dos copias del mismo control envejecen aparte. Ahora es una.
 */
function esLaRutaDeRenovacion(path: string): boolean {
  // Exacta, no `startsWith`: los parámetros van aparte por `opts.query`, así que no hace falta
  // aceptar sufijos — y aceptarlos excluiría de la renovación a rutas hermanas que hoy no existen
  // pero que mañana no tendrían por qué heredar esta excepción. Medido en el motor: la única ruta
  // registrada bajo ese prefijo es `POST /auth/refresh` (`core/api/server.go`).
  return path === '/v1/auth/refresh'
}

function caducaPronto(): boolean {
  const iso = config.getExpiresAt?.()
  if (!iso) return false
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return false
  return t - Date.now() <= MARGEN_RENOVACION_MS
}

let config: ClientConfig = {
  getToken: () => null,
  getTenant: () => null,
  onUnauthorized: () => {},
}

/** ⛔ ONE REFRESH SERVES EVERY CONCURRENT 401, AND THIS IS NOT AN OPTIMISATION — without it
 * the retry is WORSE than the bug. `RefreshSession` ROTATES the credential: the old token
 * stops working the instant the new one is issued (core/api/api_test.go:316 asserts the old
 * one then answers 401). A console screen fires five queries at once, all five get 401 on an
 * expired token, and five independent refreshes would each rotate — the first four tokens
 * die immediately and the operator is logged out anyway, having burned five rotations.
 *
 * So the FIRST 401 starts the refresh and the rest await the same promise. The slot is
 * cleared in `finally`, so a later expiry starts a fresh single flight. */
let refreshInFlight: Promise<boolean> | null = null
/** Cierto sólo durante el prefijo SÍNCRONO de `refreshSession`. Ver `refreshOnce`. */
let arrancandoRenovacion = false

function refreshOnce(): Promise<boolean> {
  const fn = config.refreshSession
  if (!fn) return Promise.resolve(false)
  // Reentrada desde el prefijo síncrono de la renovación en curso: no es otra petición.
  if (arrancandoRenovacion) return Promise.resolve(false)
  if (!refreshInFlight) {
    // ⛔ LA VENTANA SÍNCRONA, Y LAS DOS FORMAS OBVIAS DE CERRARLA SON PEORES QUE ÉSTA.
    //
    //    `refreshInFlight = fn()` evalúa la derecha primero: durante todo el prefijo SÍNCRONO de
    //    `fn()` la variable sigue valiendo `null`, así que una reentrada desde ahí dentro cree que
    //    no hay vuelo y arranca otro. Medidas las tres formas contra una reentrada efectiva:
    //
    //      tal cual .............. 50 renovaciones, 49 envíos   (recursión)
    //      Promise.resolve().then  pendiente para siempre       (espera circular)
    //      bandera de fase ....... 1 renovación, 1 envío        ← ésta
    //
    //    Diferir con un `then` instala la guarda antes de tiempo: la reentrada recibe el vuelo del
    //    que ella misma forma parte y se espera a sí misma. Cambia una recursión que revienta y se
    //    ve por un interbloqueo silencioso. La bandera distingue lo que hay que distinguir —una
    //    reentrada del prefijo síncrono NO es otra petición concurrente— y le contesta «no hay
    //    renovación» en vez de encadenarla. JavaScript no intercala ese prefijo, así que la
    //    bandera no puede confundir a un tercero.
    //
    //    No sustituye a la guarda de ruta: una reentrada DESPUÉS de un `await` dentro de `fn`
    //    seguiría viendo el vuelo instalado. Eso lo cierra `esLaRutaDeRenovacion`.
    arrancandoRenovacion = true
    try {
      refreshInFlight = fn()
        .catch(() => false)
        .finally(() => {
          refreshInFlight = null
        })
    } finally {
      arrancandoRenovacion = false
    }
  }
  return refreshInFlight
}

/** Exposed for tests only: drop any in-flight refresh so cases do not leak into each other. */
export function __resetRefreshState(): void {
  refreshInFlight = null
  arrancandoRenovacion = false
}

/** Wire the client to the real session/tenant/router. Called once at bootstrap;
 * tests call it with mocks. Partial — only override what you need. */
export function configureApiClient(c: Partial<ClientConfig>): void {
  config = { ...config, ...c }
}

/** ensureFreshSession renews the credential if it is about to expire. RAW-fetch paths —
 * the CSV/NDJSON/PDF downloads the JSON client cannot consume — call this before fetching,
 * because they bypass `apiFetch` and would otherwise be the only requests in the console
 * that still die of expiry. It shares the single-flight slot, so a download starting
 * alongside five queries still renews once.
 *
 * Silent on purpose: a renewal that cannot happen leaves the caller exactly where it was —
 * the request goes out with the current credential and its 401 is handled as before. */
export async function ensureFreshSession(): Promise<void> {
  if (caducaPronto()) await refreshOnce()
}

/** Trigger the configured 401 hook from a RAW-fetch path that bypasses `apiFetch`
 * (a streaming download the JSON client cannot consume — e.g. the audit ledger
 * export). Lets session expiry mid-download behave like every other authenticated
 * call (clear the session, route to login) instead of a generic failure. */
export function notifyUnauthorized(): void {
  config.onUnauthorized()
}

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE'
  /** JSON request body (serialized automatically). */
  body?: unknown
  /** Verbatim request body (NOT JSON-serialized) for endpoints that take raw bytes —
   * e.g. the workspace file write `PUT .../files/raw`. Mutually exclusive with `body`;
   * pair with `contentType`. */
  rawBody?: BodyInit
  /** Content-Type for a `rawBody` request (default application/octet-stream). */
  contentType?: string
  signal?: AbortSignal
  /** Anonymous requests (login, setup, server-info, health) attach no
   * Authorization / tenant headers and never trigger the 401 hook. */
  anonymous?: boolean
  /** Explicit tenant override for this request (defaults to the active tenant). */
  tenant?: string | null
  /** Query params; undefined values are omitted. */
  query?: Record<string, string | number | boolean | string[] | undefined>
  /**
   * Extra REQUEST headers. Added for the work kernel, whose contract is carried
   * in headers rather than in the body: `Idempotency-Key` (mandatory on every apply)
   * and `If-Match: "vN"` (mandatory on every apply but create). Neither can be
   * expressed as a query param or a field, so without this the console could not
   * speak the protocol at all — and a feature-local `fetch` would have had to
   * re-implement bearer injection, the tenant header and the 401 hook.
   *
   * Applied BEFORE the Authorization/tenant headers, so a caller cannot override
   * them: whose token this is stays a decision of this module.
   */
  headers?: Record<string, string>
}

/**
 * The request scope captured by a production query or mutation when that operation
 * is created. Unlike the optional fallback in RequestOptions, this shape makes the
 * caller name the tenant explicitly while still flowing through the same client seam.
 */
export type TenantRequestOptions = {
  tenant: Exclude<RequestOptions['tenant'], undefined>
}

/**
 * Opciones de una LECTURA DE LISTA atada al inquilino: `TenantRequestOptions` AMPLIADO,
 * nunca relajado.
 *
 * ⛔ POR QUE ES UNA LISTA BLANCA (`Pick`) Y NO UN `Omit`. La ampliacion obvia
 *    —`Omit<RequestOptions,'tenant'> & TenantRequestOptions`— compila esto:
 *
 *        listModelGroups({ tenant: 'acme', anonymous: true })
 *
 *    y `anonymous` hace `headers.delete('X-Olivares-Tenant')` doce lineas mas abajo
 *    (client.ts, bloque `if (opts.anonymous)`). Es decir: un tipo cuyo proposito es que el
 *    inquilino sea OBLIGATORIO permitiria DESATARLO en el mismo literal. Lo escribi asi
 *    primero, paso mis tres pruebas —inquilino+techo, solo inquilino, techo sin inquilino— y
 *    era inseguro igual: probe las direcciones que se me ocurrieron, no la que importaba.
 *
 *    Con `Pick` sólo entra lo que una lectura necesita: el techo (`query`) y la cancelacion
 *    (`signal`). `anonymous`, `headers`, `body` y `method` quedan fuera del tipo.
 *
 * ⛔ Y HASTA AQUI LLEGA EL TIPO, QUE ES MENOS DE LO QUE ESTA LINEA DECIA. Aqui ponia que esas
 *    cuatro «quedan fuera por construccion», y eso afirma mas de lo que un tipo puede cumplir:
 *    la comprobacion de propiedades en exceso SOLO mira literales frescos. Una variable de tipo
 *    ancho, un `as` o un `...spread` las cuela sin ruido y compila:
 *
 *        const ancho = { tenant: 't1', anonymous: true } as RequestOptions & { tenant: string }
 *        modelsApi.models(ancho)   // compila
 *
 *    Lo que de verdad cierra esa via NO es el tipo: es que la capa de API **reconstruye** el
 *    objeto que entrega al cliente —`{ tenant, signal, query: { ...query, limit: TECHO } }`— en
 *    vez de expandir el recibido, asi que las claves de mas se pierden en EJECUCION. El tipo
 *    documenta la intencion y caza el descuido; la reconstruccion es la frontera. Lo enseño el
 *    contraste `sol max` sobre `e03317146`, y lo prueba en vivo
 *    `models/tenant-list-options.test.ts` («una variable ANCHA con `anonymous`…»).
 */
export type TenantListOptions = TenantRequestOptions &
  Pick<RequestOptions, 'query' | 'signal'>

function buildUrl(path: string, query?: RequestOptions['query']): string {
  if (!query) return path
  const params = new URLSearchParams()
  for (const [k, v] of Object.entries(query)) {
    if (v === undefined) continue
    // An array is a REPEATED parameter, one occurrence per entry — not one
    // occurrence holding a comma-joined string. `String(['a','b'])` is `"a,b"`, which
    // an engine reading a repeatable filter takes as a single value `a,b` and matches
    // nothing: wrong, silently, and only for the second entry onwards. Arrays could
    // not reach here before (the type above did not admit them), so widening it is
    // additive by construction — no existing call can change shape.
    if (Array.isArray(v)) {
      for (const entry of v) params.append(k, entry)
      continue
    }
    params.set(k, String(v))
  }
  const qs = params.toString()
  return qs ? `${path}?${qs}` : path
}

/**
 * apiFetch performs one typed request. On a non-2xx response it throws an
 * ApiError built from the engine's error envelope; on a transport failure it
 * throws a NetworkError. A 204/empty body resolves to undefined.
 */
export async function apiFetch<T>(
  path: string,
  opts: RequestOptions = {},
): Promise<T> {
  const { data } = await apiFetchWithMeta<T>(path, opts)
  return data
}

/** apiFetchWithMeta is the same authenticated JSON client as apiFetch, but preserves
 * the HTTP status for endpoints whose success body is intentionally polymorphic
 * (for example 200 = applied now, 202 = queued for dual-control approval).
 *
 * It also returns the RESPONSE headers, because for some endpoints the header IS the
 * answer and the body cannot carry it. Two from the work kernel:
 * `Idempotency-Replayed: true` means "this was already applied" — the persisted body
 * is byte-identical to the original apply, deliberately (CommandResult.Replayed is
 * `json:"-"`), so a console reading only the body reports a replay as a fresh success;
 * and `ETag: "vN"` is the concurrency token the next apply must echo in `If-Match`. */
export async function apiFetchWithMeta<T>(
  path: string,
  opts: RequestOptions = {},
  reintentoTrasRefresco = false,
): Promise<{ status: number; data: T; headers: Headers }> {
  // ⛔ EL INQUILINO SE FIJA AL ENTRAR, ANTES DE CUALQUIER `await`.
  //
  //    Hasta aquí se leía abajo, junto al token, y eso es TARDE. Una misma petición lógica tiene
  //    DOS puntos donde se componen cabeceras, y los dos pueden caer después de una suspensión:
  //    el primer envío, si antes hubo `await refreshOnce()` preventivo; y el envío del replay
  //    tras un 401, que ocurre después de `await fetch`, `await res.text()` y otro refresco. (No
  //    son «dos esperas» entre entrar y enviar: antes del PRIMER envío sólo hay una.) Si el
  //    operador cambia de inquilino en cualquiera de los dos huecos, la petición sale con la
  //    cabecera del inquilino NUEVO.
  //
  //    En una LECTURA eso ensucia la caché (la clave se calculó con el viejo). En una ESCRITURA es
  //    peor y no tiene vuelta: **un borrado pedido en A se aplica en B**. El selector de inquilino
  //    sigue habilitado mientras una mutación vuela, y NO HAY NADA AGUAS ABAJO QUE LO PARE.
  //
  //    ⛔ Esta última frase decía antes «…y `cancelQueries` no alcanza a las mutaciones —sólo
  //    recorre la `QueryCache`—». Es cierto de la API de react-query y FALSO como descripción de
  //    esta consola: se lee como que las CONSULTAS en vuelo sí se cancelan. No se cancelan,
  //    porque esa función no se invoca en ninguna parte. Medido el 2026-08-23, antes de escribir
  //    esto: UNA sola aparición en todo `web/src`, y era la frase vieja de aquí. Cero llamadas.
  //    (Lo que encuentres hoy en este fichero es esta nota hablando de una función que nadie
  //    llama. Control positivo de que la consola sí usa el `queryClient`: `invalidateQueries`
  //    tiene 138 invocaciones.)
  //
  //    Un comentario que describe una defensa inexistente es una afirmación sin testigo, y ésta
  //    llevaba a concluir que aguas abajo había media red puesta. No la hay: fijarlo al entrar es
  //    TODA la defensa, y por eso importa que sea correcta.
  //
  //    Fijarlo al entrar es lo que ya significaba el acto: si el operador pidió algo ESTANDO en A,
  //    la petición dice A aunque tarde en salir. Un `opts.tenant` explícito manda sobre esto, y
  //    `null` es una elección válida (no mandar cabecera), por eso se compara con `undefined`.
  //    (El `opts.anonymous` de la condición es un ATAJO, no la propiedad de seguridad: una
  //    petición anónima no pone cabecera de inquilino porque el bloque de abajo entero está
  //    detrás de `if (!opts.anonymous)`. Medido con un mutante que quita el atajo: inerte EN
  //    CUANTO A LA CABECERA y con el getter de producción, que es una lectura de Zustand sin
  //    efectos. No es equivalencia total: sin el atajo se llamaría a `getTenant()` donde antes
  //    no. Se deja porque ahorra copiar el objeto, no porque proteja nada.)
  const opciones: RequestOptions =
    opts.anonymous || opts.tenant !== undefined
      ? opts
      : { ...opts, tenant: config.getTenant() }
  opts = opciones
  const headers = new Headers({ Accept: 'application/json' })
  // Caller headers first: the auth/tenant block below overwrites them, so a feature
  // cannot forge an Authorization or X-Olivares-Tenant through this seam.
  for (const [k, v] of Object.entries(opts.headers ?? {})) headers.set(k, v)
  // ⛔ …SALVO EN LAS ANÓNIMAS, donde ese bloque NO CORRE y la promesa de arriba era falsa.
  //    Una petición anónima que trajera `Authorization` o `X-Olivares-Tenant` en `opts.headers`
  //    las mandaba tal cual. No encontré ningún llamante que lo haga; la guarda existe para que la
  //    frase de arriba sea cierta por construcción y no por que nadie lo haya intentado todavía.
  if (opts.anonymous) {
    headers.delete('Authorization')
    headers.delete('X-Olivares-Tenant')
  }
  if (opts.rawBody !== undefined)
    headers.set('Content-Type', opts.contentType ?? 'application/octet-stream')
  else if (opts.body !== undefined)
    headers.set('Content-Type', 'application/json')

  // ⛔ RENOVAR ANTES, NO DESPUÉS. Un token caducado no se puede refrescar —lo rechazan el
  //    autenticador y el propio `RefreshSession`—, así que la renovación tiene que ocurrir
  //    mientras la credencial sigue VIVA. Se comprueba en cada petición autenticada: el
  //    operador que está trabajando genera tráfico, y ese tráfico es el reloj. Comparte el
  //    vuelo único con el camino del 401, así que cinco peticiones a la vez renuevan una vez.
  if (
    !opts.anonymous &&
    !reintentoTrasRefresco &&
    !esLaRutaDeRenovacion(path) &&
    caducaPronto()
  ) {
    await refreshOnce()
  }

  if (!opts.anonymous) {
    const token = config.getToken()
    if (token) headers.set('Authorization', `Bearer ${token}`)
    const tenant = opts.tenant !== undefined ? opts.tenant : config.getTenant()
    if (tenant) headers.set('X-Olivares-Tenant', tenant)
  }

  let res: Response
  try {
    res = await fetch(buildUrl(path, opts.query), {
      method: opts.method ?? 'GET',
      headers,
      body:
        opts.rawBody !== undefined
          ? opts.rawBody
          : opts.body !== undefined
            ? JSON.stringify(opts.body)
            : undefined,
      signal: opts.signal,
      // Same-origin embedded SPA; no cookies are used (bearer auth), but keep
      // same-origin credentials semantics explicit.
      credentials: 'same-origin',
    })
  } catch (cause) {
    if (cause instanceof DOMException && cause.name === 'AbortError')
      throw cause
    throw new NetworkError('The control plane is unreachable.', cause)
  }

  const requestId = res.headers.get('X-Request-ID') ?? undefined

  // Parse the body once (JSON when present); tolerate empty/non-JSON bodies.
  let parsed: unknown = undefined
  if (res.status !== 204) {
    const text = await res.text()
    if (text) {
      try {
        parsed = JSON.parse(text)
      } catch {
        parsed = undefined
      }
    }
  }

  if (!res.ok) {
    const { code, message, details } = parseErrorEnvelope(
      parsed,
      res.statusText || 'Request failed',
    )
    const err = new ApiError(
      res.status,
      code,
      message,
      requestId,
      details,
      parsed,
    )
    // An authenticated 401 means the session expired or was revoked → let the
    // app clear it and route to login. Anonymous 401s (e.g. bad login) surface
    // to the caller for inline handling.
    // EXPIRED is recoverable and REVOKED is not, so the credential is rotated once and
    // the request replayed; only if that fails does the app clear the session and route
    // to login. Anonymous 401s (e.g. bad login) still surface for inline handling.
    if (err.isUnauthenticated && !opts.anonymous) {
      if (
        puedeReintentar(path, opts, reintentoTrasRefresco) &&
        (await refreshOnce())
      )
        // `opts` lleva ya el inquilino fijado al entrar: el replay no vuelve a leerlo.
        return apiFetchWithMeta<T>(path, opts, true)
      config.onUnauthorized()
    }
    throw err
  }

  assertListEnvelope(path, res.status, parsed)
  return { status: res.status, data: parsed as T, headers: res.headers }
}

/** puedeReintentar decides whether ONE replay after a credential rotation is safe.
 *
 * Three refusals, and each is a way the retry would be worse than the 401 it fixes:
 *
 *  · ALREADY REPLAYED — one retry, never a loop. A REVOKED credential answers 401 again,
 *    and a client that kept refreshing would hammer the rotation endpoint instead of
 *    sending the operator to /login.
 *  · THE REFRESH CALL ITSELF — refreshing in response to the refresh's own 401 is
 *    unbounded recursion, and that 401 is precisely the answer meaning "the session is
 *    genuinely over".
 *  · A BODY THAT CANNOT BE SENT TWICE — `rawBody` may be a stream or another one-shot
 *    source, and `fetch` consumes it. Replaying with a drained body would turn a
 *    recoverable 401 into a corrupt request, so those keep the old behaviour. Strings and
 *    buffers are re-sendable and do replay; the JSON `body` is re-serialised each time, so
 *    it always is.
 */
function puedeReintentar(
  path: string,
  opts: RequestOptions,
  yaReintentado: boolean,
): boolean {
  if (yaReintentado) return false
  if (esLaRutaDeRenovacion(path)) return false
  const raw = opts.rawBody
  if (raw === undefined) return true
  return (
    typeof raw === 'string' ||
    raw instanceof ArrayBuffer ||
    ArrayBuffer.isView(raw) ||
    (typeof Blob !== 'undefined' && raw instanceof Blob)
  )
}

/**
 * assertListEnvelope rejects a 2xx whose list envelope carries an `items` that is
 * not an array.
 *
 * The engine cannot produce it any more — the envelope is a type whose MarshalJSON
 * renders an empty page as [] (core/api/listresponse.go), with a static invariant
 * and a clean-install sweep guarding the class. This is the belt to that pair of
 * braces, and it is deliberately NOT a `?? []` in the 47 files that consume
 * `ListResponse<T>`:
 *
 *   - Defending per call site would make every caller carry the cost of a broken
 *     contract, and `items` is typed `T[]` (non-nullable) precisely because the
 *     server promises an array. A promise each caller re-checks is not a contract.
 *   - Substituting [] would be WORSE than crashing here: this console renders
 *     evidence packages, legal holds and residency attestations. "No legal holds"
 *     and "the server did not answer properly" must never look the same.
 *
 * So it throws, once, where every response already passes: the view's AsyncSection
 * turns it into a retryable error card scoped to that section instead of the
 * whole-page "This view crashed" a `.map()` over null produced, and the thrown
 * ApiError names the endpoint and the violation for the operator's report.
 *
 * The check is a single property read on the top-level object — the shape every
 * list route returns — not a deep walk of every response.
 */
function assertListEnvelope(
  path: string,
  status: number,
  parsed: unknown,
): void {
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return
  if (!('items' in parsed)) return
  const items = (parsed as { items: unknown }).items
  // Widened from `items !== null`: null was the shape the engine could once produce,
  // but ANY non-array `items` breaks the same promise, and a caller writing
  // `data?.items ?? []` turns every one of them into "you have no rows". The residue
  // this layer cannot close is stated rather than papered over: a 2xx with NO `items`
  // key is indistinguishable here from a legitimate non-list endpoint, so it still
  // passes — closing that needs per-endpoint typing, not a shared guard.
  if (Array.isArray(items)) return
  const shape =
    items === null ? 'null' : Array.isArray(items) ? 'array' : typeof items
  throw new ApiError(
    status,
    'invalid_response',
    `The control plane returned an invalid list response for ${path}: "items" was ${shape}. ` +
      `An empty collection is [] — this response cannot be rendered, and showing it as ` +
      `empty would misreport the data.`,
    undefined,
    { path },
    parsed,
  )
}

/** Convenience verbs over apiFetch. */
export const http = {
  get: <T>(path: string, opts?: Omit<RequestOptions, 'method' | 'body'>) =>
    apiFetch<T>(path, { ...opts, method: 'GET' }),
  /** GET preserving status and response headers — for a read whose ETag the caller
   * must echo back on the next write (re-read after a 409).*/
  getWithMeta: <T>(
    path: string,
    opts?: Omit<RequestOptions, 'method' | 'body'>,
  ) => apiFetchWithMeta<T>(path, { ...opts, method: 'GET' }),
  post: <T>(
    path: string,
    body?: unknown,
    opts?: Omit<RequestOptions, 'method' | 'body'>,
  ) => apiFetch<T>(path, { ...opts, method: 'POST', body }),
  postWithMeta: <T>(
    path: string,
    body?: unknown,
    opts?: Omit<RequestOptions, 'method' | 'body'>,
  ) => apiFetchWithMeta<T>(path, { ...opts, method: 'POST', body }),
  patch: <T>(
    path: string,
    body?: unknown,
    opts?: Omit<RequestOptions, 'method' | 'body'>,
  ) => apiFetch<T>(path, { ...opts, method: 'PATCH', body }),
  patchWithMeta: <T>(
    path: string,
    body?: unknown,
    opts?: Omit<RequestOptions, 'method' | 'body'>,
  ) => apiFetchWithMeta<T>(path, { ...opts, method: 'PATCH', body }),
  put: <T>(
    path: string,
    body?: unknown,
    opts?: Omit<RequestOptions, 'method' | 'body'>,
  ) => apiFetch<T>(path, { ...opts, method: 'PUT', body }),
  putWithMeta: <T>(
    path: string,
    body?: unknown,
    opts?: Omit<RequestOptions, 'method' | 'body'>,
  ) => apiFetchWithMeta<T>(path, { ...opts, method: 'PUT', body }),
  /** PUT with a verbatim (non-JSON) body — for raw-bytes endpoints like the
   * workspace file write. */
  putRaw: <T>(
    path: string,
    rawBody: BodyInit,
    opts?: Omit<RequestOptions, 'method' | 'body' | 'rawBody'>,
  ) => apiFetch<T>(path, { ...opts, method: 'PUT', rawBody }),
  delete: <T>(
    path: string,
    body?: unknown,
    opts?: Omit<RequestOptions, 'method' | 'body'>,
  ) => apiFetch<T>(path, { ...opts, method: 'DELETE', body }),
  deleteWithMeta: <T>(
    path: string,
    body?: unknown,
    opts?: Omit<RequestOptions, 'method' | 'body'>,
  ) => apiFetchWithMeta<T>(path, { ...opts, method: 'DELETE', body }),
}
