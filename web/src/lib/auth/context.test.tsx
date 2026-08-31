// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-02 — THE SESSION RENEWS ITSELF BEFORE IT DIES, and this is the first test the
// AuthProvider has ever had: `web/src/lib/auth/` carried only rbac.test.ts, so login, logout,
// tenant resolution and now renewal were all covered by nothing.
//
// The property: with an expiry in hand the console renews BEFORE the deadline. It is not "on
// 401, refresh and retry" — `/v1/auth/refresh` renews THE CALLING SESSION
// (core/api/openapi.go:184), so by the time a 401 arrives the credential is already dead and a
// reactive refresh would be refused too. Every case below names the guard it measures and the
// direction that must NOT fire it.
import { render, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'

const refreshMock = vi.fn()
const whoamiMock = vi.fn()
vi.mock('@/lib/api/endpoints', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api/endpoints')>()
  return {
    ...actual,
    authApi: {
      ...actual.authApi,
      refresh: (...a: unknown[]) => refreshMock(...a),
      whoami: (...a: unknown[]) => whoamiMock(...a),
      logout: () => Promise.resolve(),
    },
  }
})

const { AuthProvider } = await import('./context')

const IN = (ms: number) => new Date(Date.now() + ms).toISOString()

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <AuthProvider>
        <div />
      </AuthProvider>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  refreshMock.mockReset()
  whoamiMock.mockReset().mockResolvedValue({ grants: [], superadmin: false })
  useTenantStore.getState().clear()
})

afterEach(() => {
  vi.useRealTimers()
  useSessionStore.getState().clear()
})

describe('session renewal', () => {
  /**
   * THE CONTROL: the console renews inside the margin, before the deadline.
   * THE MUTATION: delete the effect, or schedule it AT the deadline instead of before it —
   * either way the call never happens in this window and this fires.
   */
  it('renews before the deadline, not after it', async () => {
    refreshMock.mockResolvedValue({
      token: 'olvs_new',
      session_id: 's2',
      expires_at: IN(600_000),
    })
    useSessionStore.setState({
      token: 'olvs_old',
      sessionId: 's1',
      expiresAt: IN(90_000), // 90s left, margin is 60s → renew in ~30s
    })
    mount()
    await vi.advanceTimersByTimeAsync(35_000)
    await waitFor(() => expect(refreshMock).toHaveBeenCalledTimes(1))
    expect(useSessionStore.getState().token).toBe('olvs_new')
  })

  /**
   * ⛔ EL DESBORDE DE 32 BITS DE `setTimeout`, que es cómo una sesión LARGA se convierte en un bucle.
   *
   * `setTimeout` guarda el retraso en un entero de 32 bits con signo: por encima de 2 147 483 647 ms
   * (24,9 días) **no espera más — dispara de inmediato**. Con una caducidad a cuatro años el retraso
   * pedido son ~1,06·10¹¹ ms, cincuenta veces el máximo, así que el refresco salía en el ARRANQUE; y
   * como el refresco devuelve otra caducidad lejana, vuelve a desbordar. Bucle caliente contra el
   * motor. Basta un «recuérdame» de más de 25 días.
   *
   * ⚠ POR QUÉ ESTA CELDA MIRA EL ARGUMENTO Y NO LA CONDUCTA, que es lo que la hace válida: los
   *   timers falsos de vitest **no pueden desbordar**, porque guardan el retraso como un número JS
   *   normal. Un caso que «avance el reloj y compruebe que no refresca» pasaría **con el defecto
   *   puesto** — el doble no puede reproducir lo que hace producción. Lo único observable y fiel es
   *   la DECISIÓN: qué retraso se le entrega a `setTimeout`.
   *
   * LA MUTACIÓN: quitar el `Math.min(ms, MAX_TIMEOUT_MS)` y esto se pone rojo con la cifra exacta.
   */
  it('nunca programa un retraso por encima del máximo de 32 bits de setTimeout', async () => {
    const MAX = 2_147_483_647
    const espia = vi.spyOn(window, 'setTimeout')
    useSessionStore.setState({
      token: 'olvs_old',
      sessionId: 's1',
      expiresAt: new Date(Date.now() + 4 * 365 * 24 * 3_600_000).toISOString(),
    })
    mount()
    await vi.advanceTimersByTimeAsync(0)
    const retrasos = espia.mock.calls
      .map((c) => Number(c[1]))
      .filter((n) => Number.isFinite(n))
    expect(retrasos.length).toBeGreaterThan(0)
    const excedidos = retrasos.filter((n) => n > MAX)
    expect(
      excedidos,
      `Se programó un retraso mayor que 2^31-1 ms: setTimeout lo desborda y dispara YA, ` +
        `con lo que la renovación entra en bucle. Retrasos vistos: ${retrasos.join(', ')}`,
    ).toEqual([])
    espia.mockRestore()
  })

  /**
   * THE NON-FIRING DIRECTION, and without it the case above is satisfied by a console that
   * renews constantly: a session with plenty of life left must be left alone.
   */
  it('does not renew a session that is nowhere near its deadline', async () => {
    useSessionStore.setState({
      token: 'olvs_old',
      sessionId: 's1',
      expiresAt: IN(3_600_000),
    })
    mount()
    await vi.advanceTimersByTimeAsync(60_000)
    expect(refreshMock).not.toHaveBeenCalled()
  })

  /**
   * GUARD: an ALREADY EXPIRED session does not renew. There is nothing to renew — the engine
   * would refuse it — and the next request's 401 already clears the session. Without this the
   * console would spend a round trip to be told what it already knows.
   */
  it('does not renew a session that has already expired', async () => {
    useSessionStore.setState({
      token: 'olvs_old',
      sessionId: 's1',
      expiresAt: IN(-1_000),
    })
    mount()
    await vi.advanceTimersByTimeAsync(120_000)
    expect(refreshMock).not.toHaveBeenCalled()
  })

  /**
   * GUARD: an UNREADABLE expiry schedules nothing. `Date.parse` returns NaN, arithmetic on NaN
   * stays NaN, and `setTimeout` treats NaN as 0 — so without the check a malformed string is a
   * refresh storm rather than a no-op.
   */
  it('does not schedule anything on an unreadable expiry', async () => {
    useSessionStore.setState({
      token: 'olvs_old',
      sessionId: 's1',
      expiresAt: 'not-a-date',
    })
    mount()
    await vi.advanceTimersByTimeAsync(120_000)
    expect(refreshMock).not.toHaveBeenCalled()
  })

  /**
   * GUARD: a FAILED renewal does not reschedule. Retrying is how a dead session becomes a loop
   * of requests against an engine that has already said no; the 401 path owns that outcome and
   * is the only place that clears the session — which is why the token is still there.
   */
  it('does not retry a renewal the engine refused', async () => {
    refreshMock.mockRejectedValue(new Error('401'))
    useSessionStore.setState({
      token: 'olvs_old',
      sessionId: 's1',
      expiresAt: IN(90_000),
    })
    mount()
    await vi.advanceTimersByTimeAsync(35_000)
    await waitFor(() => expect(refreshMock).toHaveBeenCalledTimes(1))
    await vi.advanceTimersByTimeAsync(600_000)
    expect(refreshMock).toHaveBeenCalledTimes(1)
    expect(useSessionStore.getState().token).toBe('olvs_old')
  })
})
