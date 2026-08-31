// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// ⛔ ESCRIBÍ AQUÍ QUE ÉSTE ERA UN CAMINO VIVO Y ERA FALSO. Lo dejo escrito porque el error de
// método vale más que el arreglo: **hay DOS APIs de workspace y las emparejé por el NOMBRE de la
// función**.
//
//   · `agentOpsApi.createWorkspace` → `/v1/m/sessions/workspaces` → `modules/sessions/
//     workspace_api.go:52-72`. Autoriza con `sessions:workspace:admin` y **NO comprueba
//     assurance**; el wrapper común (`core/api/server.go:1089-1143`) tampoco añade gate AAL.
//   · La gateada es OTRA: `/v1/workspaces` → `core/api/handlers_scoping.go:139-186`, con
//     `requireAAL3`, y sus consumidores usan `consoleApi`, no `agentOpsApi`.
//
// El contraste Codex `sol max` no lo razonó por ausencia: **lo ejecutó** — una sesión fresca
// nace en AAL1 (`core/auth/assurance.go:22-28`) y la prueba HTTP de extremo a extremo crea este
// workspace con **201** (`modules/sessions/runtime_api_test.go:13-33`).
//
// Y de paso me corrigió otro detalle que canté de más: `agentops/index.tsx` **no es un ancestro
// React**, es un barrel que reexporta el panel. Hechos secundarios correctos sobre una premisa
// de ruta incorrecta.
//
// **Entonces, ¿qué es esto?** Defensa en profundidad, igual que `knowledge` y `governance`, más
// una mejora real e independiente del assurance: un 403 de ROL pasa de error ROJO a advertencia
// calmada, que es lo que un límite de permiso merece. La ceremonia queda cableada para el día
// que el gate llegue a esta ruta.
//
// Los caminos que SÍ están vivos son otros cuatro, y no son éste: connector test, SSO test,
// license install/remove + activation apply, y support bundle — todos con mutación a mano
// detrás de un pre-gate que puede estar CADUCADO. Van en su propia sesión.
import { QueryClientProvider, QueryClient } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/lib/api/errors'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: () => true,
    principal: { aal: 1 },
  }),
}))

// La ceremonia se pide al store, que es donde `useFailedActionReporter` la registra
// (lib/hooks/use-privileged-mutation.ts:37-46). Doblarlo aquí deja ver QUÉ se pidió, que es el
// invariante: no la mecánica de WebAuthn, sino que la petición llegue a existir.
const require_ = vi.hoisted(() => vi.fn(() => true))
vi.mock('@/stores/step-up', () => ({
  useStepUpStore: { getState: () => ({ require: require_ }) },
}))

const api = vi.hoisted(() => ({
  listWorkspaces: vi.fn(),
  createWorkspace: vi.fn(),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, agentOpsApi: { ...(real.agentOpsApi as object), ...api } }
})

import { WorkspacesPanel } from './workspaces-panel'

const ceremonia = () =>
  new ApiError(403, 'step_up_required', 'assurance level too low')
const rol = () =>
  new ApiError(403, 'forbidden', 'your role cannot create workspaces')

const wrap = (ui: ReactNode) =>
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      {ui}
    </QueryClientProvider>,
  )

/** Abre el diálogo y envía el formulario mínimo. */
async function crearWorkspace() {
  const user = userEvent.setup()
  wrap(<WorkspacesPanel />)
  // Los rótulos salen del i18n real (agentops/i18n/en.json), no de lo que yo suponga: el botón
  // que abre dice «Register workspace» y el que envía dice «Register» — dos nombres parecidos,
  // así que el segundo se ancla con `^…$` para no volver a pulsar el primero.
  await user.click(
    await screen.findByRole('button', { name: /register workspace/i }),
  )
  await user.type(await screen.findByLabelText(/^name$/i), 'w1')
  await user.type(await screen.findByLabelText(/^root path$/i), '/srv/w1')
  await user.click(screen.getByRole('button', { name: /^register$/i }))
  return user
}

describe('crear un workspace pide AAL3, y la consola tiene que ofrecerlo', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    require_.mockReturnValue(true)
    api.listWorkspaces.mockResolvedValue({ items: [], has_more: false })
  })

  it('con `step_up_required` abre la CEREMONIA y no un error rojo', async () => {
    api.createWorkspace.mockRejectedValue(ceremonia())
    await crearWorkspace()

    await waitFor(() => expect(require_).toHaveBeenCalled())
    // ⛔ Y LA MITAD QUE DE VERDAD FALLABA: ni un toast de error. Abrir la ceremonia y ADEMÁS
    //    pintar el rojo satisfaría la aserción de arriba, así que la exclusión no sobra.
    //
    //    ALCANCE DE ESTA EXCLUSIÓN, MEDIDO CON UN MUTANTE QUE SOBREVIVIÓ: intenté reproducir
    //    «las dos cosas a la vez» añadiendo un `opts.onError` que hiciera `toast.error`, y la
    //    celda siguió verde — no por hueco, sino porque el hook resuelve la frontera de
    //    autorización ANTES del callback de la feature, que nunca tiene primer rechazo sobre
    //    401/403 (use-privileged-mutation.ts:174-186, propiedad de #700 levantada por el
    //    contraste de). Esa forma es INALCANZABLE por esa puerta, y el hook la garantiza
    //    mejor de lo que puede hacerlo esta línea.
    //
    //    Lo que sí guarda esta exclusión es la vuelta atrás: rehacer la mutación a mano con su
    //    propio `toast.error`, que es exactamente como estaba. Ese mutante SÍ la mata.
    expect(toast.error).not.toHaveBeenCalled()
  })

  it('y un 403 de ROL se cuenta como frontera de permiso, no como fallo', async () => {
    // Control negativo: el otro 403 sigue teniendo su respuesta, y NO es la ceremonia.
    api.createWorkspace.mockRejectedValue(rol())
    await crearWorkspace()

    await waitFor(() => expect(toast.warning).toHaveBeenCalled())
    expect(require_).not.toHaveBeenCalled()
    expect(toast.error).not.toHaveBeenCalled()
  })

  it('y un error que no es 403 sigue siendo un error', async () => {
    // ⛔ El tercer camino, sin el cual «no pintes rojo nunca» sería un arreglo que rompe la
    //    señal de un fallo real: un 500 tiene que seguir viéndose.
    api.createWorkspace.mockRejectedValue(new ApiError(500, 'internal', 'boom'))
    await crearWorkspace()

    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    expect(require_).not.toHaveBeenCalled()
  })

  it('los dos 403 se distinguen por el CÓDIGO, no por el status', () => {
    expect(ceremonia().status).toBe(403)
    expect(rol().status).toBe(403)
    expect(ceremonia().isForbidden).toBe(true) // ⇦ la trampa entera
    expect(ceremonia().isStepUpRequired).toBe(true)
    expect(rol().isStepUpRequired).toBe(false)
  })
})
