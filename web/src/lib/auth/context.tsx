// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { authApi } from '@/lib/api/endpoints'
import { notifyUnauthorized } from '@/lib/api/client'
import { ApiError } from '@/lib/api/errors'
import { toast } from 'sonner'
import { useTranslation } from 'react-i18next'
import { queryKeys } from '@/lib/api/query'
import type {
  Grant,
  LoginRequest,
  LoginResponse,
  LoginResult,
  Whoami,
} from '@/lib/api/types'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { observeStepUpContext } from '@/stores/step-up'
import { can as rbacCan, confinedWorkspaceIn, roleInTenant } from './rbac'

export type AuthStatus = 'anonymous' | 'loading' | 'authenticated' | 'error'

export interface AuthContextValue {
  status: AuthStatus
  /** The authenticated principal (identity + grants), or null. */
  principal: Whoami | null
  /** Tenant grants of the principal. */
  grants: Grant[]
  /** The currently selected tenant id (X-Olivares-Tenant), or null. */
  activeTenant: string | null
  /** The principal's role in the active tenant, or null. */
  activeRole: string | null
  /**
   * The workspace the principal's membership in the ACTIVE tenant is confined to, or
   * null when the membership is tenant-wide.
   *
   * `can()` already accounts for the part of confinement that is a property of the
   * permission. This is here for the part that is NOT a permission at all: a handful of
   * server rules are `role AND NOT confined` (consoleviews' delete-any is the one that
   * bit us), and no permission set can express a role gate. A view that mirrors one of
   * those MUST consult this, or it offers a confined admin an action the server refuses.
   */
  confinedWorkspace: string | null
  isSuperadmin: boolean
  /** True once whoami has resolved a principal. */
  isAuthenticated: boolean
  /** Membership of the effective permission set the ENGINE computed for this
   *  principal in the target tenant. Hides/disables actions; it never grants. */
  can: (permission: string, opts?: { tenant?: string | null }) => boolean
  /** Exchange credentials for a session and load the principal — or return
   *  the pending second-factor challenge the login must complete . */
  login: (req: LoginRequest) => Promise<LoginResult>
  /** Adopt an already-verified session envelope (the second-factor completion
   *  paths): store it and load the principal, exactly like login's session leg. */
  adoptSession: (res: LoginResponse) => Promise<void>
  /** Confirm server revocation before clearing client state. */
  logout: () => Promise<void>
  /** Select the active tenant (org switcher). */
  setActiveTenant: (tenant: string | null) => void
}

const AuthContext = createContext<AuthContextValue | null>(null)

/** Cuánto antes del plazo se renueva, y el suelo entre dos intentos. */
const REFRESH_MARGIN_MS = 60_000
const REFRESH_FLOOR_MS = 5_000

export function AuthProvider({ children }: { children: ReactNode }) {
  const { t } = useTranslation('errors')
  const queryClient = useQueryClient()
  // Observe transitions synchronously, including movements React batches into A→A.
  // This retires only step-up intent; HTTP defaults and other caches stay unchanged.
  useLayoutEffect(() => observeStepUpContext(queryClient), [queryClient])
  const ready = useSessionStore((s) => s.ready)
  const token = useSessionStore((s) => s.csrfToken)
  const setSession = useSessionStore((s) => s.setSession)
  const clearSession = useSessionStore((s) => s.clear)
  const activeTenant = useTenantStore((s) => s.activeTenant)
  const setActiveTenantStore = useTenantStore((s) => s.setActiveTenant)
  const clearTenant = useTenantStore((s) => s.clear)
  // Held true while login() owns the first whoami. Otherwise `enabled: !!token`
  // starts a GET the moment setSession lands, and the login navigation (or a
  // competing fetchQuery) aborts it — console-walk records
  // GET /v1/auth/whoami net::ERR_ABORTED on the login step.
  const [loginInFlight, setLoginInFlight] = useState(false)
  const adoptionSequence = useRef(0)

  // Load the principal whenever a token is present. A 401 here means the token was
  // revoked/expired — the client's onUnauthorized hook clears the session, so the
  // query simply disables and the app returns to the anonymous state.
  const whoami = useQuery({
    queryKey: queryKeys.whoami,
    queryFn: () => authApi.whoami(),
    enabled: !!token && !loginInFlight,
    staleTime: 60_000,
  })

  // Drop the ended session's identity before a later login enables this query.
  const signedOut = !token
  useLayoutEffect(() => {
    if (!signedOut) return
    queryClient.removeQueries({ queryKey: queryKeys.whoami, exact: true })
  }, [signedOut, queryClient])

  const principal = token ? (whoami.data ?? null) : null
  // Memoize so the array identity is stable per principal (keeps the tenant effect
  // and the context value from recomputing every render).
  const grants = useMemo(() => principal?.grants ?? [], [principal])

  // Resolve a sensible active tenant: a single-membership principal defaults to it;
  // a stale/invalid selection (not in grants, non-superadmin) is dropped.
  useEffect(() => {
    if (!principal) return
    const tenantIds = grants.map((g) => g.tenant)
    if (
      activeTenant &&
      !principal.superadmin &&
      !tenantIds.includes(activeTenant)
    ) {
      setActiveTenantStore(null)
      return
    }
    if (!activeTenant && tenantIds.length >= 1) {
      setActiveTenantStore(tenantIds[0] ?? null)
    }
  }, [principal, grants, activeTenant, setActiveTenantStore])

  const expiresAt = useSessionStore((s) => s.expiresAt)

  /**
   * ⛔ PROACTIVO, NO REACTIVO — y quien lo decide es el MOTOR, no una preferencia.
   *
   * La forma obvia es «al 401, refrescar y reintentar». Aquí no funciona: `/v1/auth/refresh`
   * renueva LA SESIÓN QUE LLAMA (`core/api/openapi.go:184`), así que necesita una credencial
   * todavía válida. Cuando llega el 401 el token ya está muerto y el refresco sería rechazado
   * igual: se gastaría un viaje para fallar por segunda vez y echar al usuario de todos modos,
   * con la ruta de recuperación delante.
   *
   * Así que la consola renueva ANTES del plazo que ya conoce: `expires_at` se guarda desde el
   * login (abajo, en `login`) y no lo leía nadie.
   *
   * TRES GUARDAS, cada una por una forma de salir mal, no por una funcionalidad:
   *
   *  · Una caducidad ILEGIBLE no programa nada. `Date.parse` de un valor malformado da NaN, la
   *    aritmética sobre NaN sigue siendo NaN y `setTimeout` trata NaN como 0 — es decir, una
   *    tormenta de refrescos nacida de una cadena mala. Por eso se comprueba, no se coacciona.
   *  · Una sesión YA CADUCADA no refresca. No hay nada que renovar, el motor lo rechazaría, y el
   *    401 de la siguiente petición ya limpia la sesión — que es el comportamiento que existía
   *    antes de esto y se conserva.
   *  · Un refresco RECHAZADO no reprograma. Reintentar es como una sesión muerta se convierte en
   *    un bucle de peticiones contra un motor que ya dijo que no. Ese desenlace es del camino del
   *    401, que es el único sitio que limpia la sesión.
   *
   * Y el suelo: cuando la vida restante ya está dentro del margen el refresco es inmediato, pero
   * nunca antes de REFRESH_FLOOR_MS, de modo que un reloj desviado que produzca caducidades casi
   * pasadas cueste una llamada cada pocos segundos y no un giro.
   */
  useEffect(() => {
    if (!token || !expiresAt) return
    const deadline = Date.parse(expiresAt)
    if (!Number.isFinite(deadline)) return
    const remaining = deadline - Date.now()
    if (remaining <= 0) return
    const delay = Math.max(remaining - REFRESH_MARGIN_MS, REFRESH_FLOOR_MS)

    let cancelled = false
    let timer = 0

    // ⛔ CUARTA GUARDA, y la única que no se ve leyendo el código: `setTimeout` GUARDA EL RETRASO EN
    //    UN ENTERO DE 32 BITS CON SIGNO. Un valor por encima de 2 147 483 647 ms (24,9 días) NO
    //    espera más: **desborda y dispara de inmediato**. No es teórico — es cómo lo encontré.
    //
    //    Medido el 2026-08-18 en el arnés de accesibilidad: con una sesión sembrada a
    //    `expires_at: 2030-01-01` el retraso pedido son 106 444 800 000 ms, **50 veces el máximo**, y
    //    el refresco salía en el ARRANQUE. En el arnés eso vaciaba la sesión y **56 de 58 rutas
    //    renderizaban la pantalla de acceso con el gate en verde**.
    //
    //    Y en producción es peor que un arnés confundido: un refresco que triunfa devuelve otra
    //    caducidad lejana, que vuelve a desbordar, que vuelve a disparar ya. **Un bucle caliente de
    //    renovaciones contra el motor**, no un fallo visible. La probabilidad no es exótica: basta una
    //    sesión de más de 25 días, que es lo que pide cualquier «recuérdame».
    //
    //    ⇒ Se re-arma por tramos. Cada salto espera lo que quepa, y al vencer comprueba contra el
    //      RELOJ —no contra un contador acumulado— si ya toca; así un portátil suspendido tampoco
    //      desplaza el plazo.
    const MAX_TIMEOUT_MS = 2_147_483_647
    const armar = (ms: number) => {
      timer = window.setTimeout(
        () => {
          if (cancelled) return
          const restante = deadline - REFRESH_MARGIN_MS - Date.now()
          if (restante > 0) return armar(Math.max(restante, REFRESH_FLOOR_MS))
          void (async () => {
            try {
              const res = await authApi.refresh()
              if (cancelled) return
              setSession({
                csrfToken: res.csrf_token,
                sessionId: res.session_id,
                expiresAt: res.expires_at,
              })
            } catch {
              // Silencio deliberado y terminal: ver la tercera guarda de arriba.
            }
          })()
        },
        Math.min(ms, MAX_TIMEOUT_MS),
      )
    }
    armar(delay)

    return () => {
      cancelled = true
      window.clearTimeout(timer)
    }
  }, [token, expiresAt, setSession])

  // The session leg every credential path shares: password, and the two
  // second-factor completions (code / recovery code / first-factor activation)
  // that hand back the same envelope.
  const adoptSession = useCallback(
    async (res: LoginResponse) => {
      const sequence = ++adoptionSequence.current
      // Disable the observer first so setSession cannot start a second GET.
      setLoginInFlight(true)
      setSession({
        csrfToken: res.csrf_token,
        sessionId: res.session_id,
        expiresAt: res.expires_at,
      })
      const generation = useSessionStore.getState().credentialGeneration
      const isCurrent = () =>
        adoptionSequence.current === sequence &&
        useSessionStore.getState().credentialGeneration === generation
      try {
        // This response belongs to the installed credential. The shared
        // client's refresh and 401 effects cannot act on a later credential.
        const principal = await authApi.whoami({
          sessionEffects: 'none',
          dispatchGuard: () => {
            if (!isCurrent())
              throw new DOMException('Session adoption ended.', 'AbortError')
          },
        })
        if (!isCurrent()) return
        queryClient.setQueryData(queryKeys.whoami, principal)
      } catch (error) {
        if (!isCurrent()) return
        if (error instanceof ApiError && error.isUnauthenticated)
          notifyUnauthorized()
        throw error
      } finally {
        // An earlier adoption must not enable the observer while the newer
        // adoption still owns its first whoami request.
        if (adoptionSequence.current === sequence) setLoginInFlight(false)
      }
    },
    [setSession, queryClient],
  )

  const login = useCallback(
    async (req: LoginRequest): Promise<LoginResult> => {
      const res = await authApi.login(req)
      if ('csrf_token' in res && res.csrf_token) {
        await adoptSession(res)
        return res
      }
      return res
    },
    [adoptSession],
  )

  const logout = useCallback(async () => {
    try {
      await authApi.logout()
    } catch (error) {
      // A failed revocation must not look like logout: the cookie would sign the
      // operator back in on reload. A 401 confirms that the server session has ended.
      if (!(error instanceof ApiError && error.isUnauthenticated)) {
        toast.error(t('network.title'))
        return
      }
    }
    clearSession()
    clearTenant()
    queryClient.clear()
  }, [clearSession, clearTenant, queryClient, t])

  const can = useCallback(
    (permission: string, opts?: { tenant?: string | null }) =>
      rbacCan(permission, {
        principal,
        tenant: opts?.tenant !== undefined ? opts.tenant : activeTenant,
      }),
    [principal, activeTenant],
  )

  const status: AuthStatus = useMemo(() => {
    if (!ready) return 'loading'
    if (!token) return 'anonymous'
    if (whoami.isError) return 'error'
    if (principal) return 'authenticated'
    return 'loading'
  }, [ready, token, whoami.isError, principal])

  const value = useMemo<AuthContextValue>(
    () => ({
      status,
      principal,
      grants,
      activeTenant,
      activeRole: roleInTenant(principal, activeTenant),
      confinedWorkspace: confinedWorkspaceIn(principal, activeTenant),
      isSuperadmin: !!principal?.superadmin,
      isAuthenticated: !!principal,
      can,
      login,
      adoptSession,
      logout,
      setActiveTenant: setActiveTenantStore,
    }),
    [
      status,
      principal,
      grants,
      activeTenant,
      can,
      login,
      adoptSession,
      logout,
      setActiveTenantStore,
    ],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

// eslint-disable-next-line react-refresh/only-export-components
export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used within <AuthProvider>')
  return ctx
}
