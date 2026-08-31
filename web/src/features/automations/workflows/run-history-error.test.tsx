// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import './i18n'

/**
 * C-10 — un fallo del historial NO es «no hay ejecuciones».
 *
 * ⛔ EL DEFECTO, con su cadena exacta. `run-panel.tsx` decidía así:
 *
 *      history.isPending ? <Spinner/>
 *      : (history.data?.items.length ?? 0) === 0 ? <EmptyState "no hay ejecuciones"/>
 *      : <ul>…</ul>
 *
 *    Cuando la consulta FALLA, `history.data` queda `undefined`, así que
 *    `(undefined?.items.length ?? 0) === 0` da **true** y la pantalla afirma que no hay
 *    ejecuciones. Es una afirmación sobre el MUNDO cuando lo único cierto es que no se pudo
 *    mirar — y este panel se abre precisamente para averiguar por qué falló algo anoche.
 *
 * ⛔ EL ARREGLO ES UN ORDEN: la rama de error va ANTES que la del vacío.
 *
 * ⛔ Y ESTE FICHERO ESTÁ REESCRITO POR UN MUTANTE. Mi primera versión probaba `RunError` suelto
 *    —el componente— y pasaba los cinco casos; al devolver el defecto entero (que la rama de
 *    error no se tome nunca) **seguía pasando los cinco**. Probaba el componente y no la
 *    DECISIÓN, que es donde vivía el fallo. Montar la pantalla es lo que lo mide.
 */

const { api, authState } = vi.hoisted(() => ({
  api: {
    run: vi.fn(),
    runs: vi.fn(),
    runDetail: vi.fn(),
    approve: vi.fn(),
  },
  authState: { activeTenant: 'tenant-a' as string | null },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  workflowsApi: api,
}))

const { RunPanel } = await import('./run-panel')

function renderPanel() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <RunPanel workflowId="workflow-1" canAdmin={false} />
    </QueryClientProvider>,
  )
}

/** Abre el diálogo del historial (el botón secundario, para un rol sin admin). */
async function abrirHistorial() {
  const user = userEvent.setup()
  const botones = screen.getAllByRole('button')
  await user.click(botones[0])
  return user
}

beforeEach(() => {
  vi.clearAllMocks()
  api.runDetail.mockResolvedValue(undefined)
})

describe('C-10 · el historial distingue FALLO de VACÍO', () => {
  it('un historial que FALLA no dice «no hay ejecuciones»', async () => {
    api.runs.mockRejectedValue(new ApiError(500, 'boom', 'server exploded'))
    renderPanel()
    await abrirHistorial()

    // Lo que NO puede aparecer: el rótulo del vacío. Es la afirmación falsa sobre el mundo.
    expect(await screen.findByRole('alert')).toBeInTheDocument()
    expect(
      screen.queryByText(/no runs have been recorded/i),
    ).not.toBeInTheDocument()
  })

  it('un historial VACÍO sigue diciendo que está vacío', async () => {
    // El par del anterior. Sin este, una implementación que pintara SIEMPRE el error pasaría el
    // primero y habría cambiado un estado falso por otro.
    api.runs.mockResolvedValue({ items: [], has_more: false })
    renderPanel()
    await abrirHistorial()

    expect(
      await screen.findByText(/no runs have been recorded/i),
    ).toBeInTheDocument()
  })

  it('el fallo del historial OFRECE reintentar, y reintentar vuelve a llamar al motor', async () => {
    api.runs.mockRejectedValueOnce(new ApiError(500, 'boom', 'server exploded'))
    api.runs.mockResolvedValue({ items: [], has_more: false })
    renderPanel()
    const user = await abrirHistorial()

    await screen.findByRole('alert')
    const llamadasAntes = api.runs.mock.calls.length
    const reintentar = screen
      .getAllByRole('button')
      .find((b) => /retry|reintent/i.test(b.textContent ?? ''))
    expect(
      reintentar,
      'el fallo debe ofrecer una salida, no sólo anunciarse',
    ).toBeTruthy()
    await user.click(reintentar!)

    expect(api.runs.mock.calls.length).toBeGreaterThan(llamadasAntes)
  })

  it('CONTROL: con ejecuciones, ni error ni vacío', async () => {
    // Calibra los dos primeros: si la pantalla no pintara la lista, «no aparece el vacío» pasaría
    // por la razón equivocada.
    api.runs.mockResolvedValue({
      items: [
        {
          id: 'run-1',
          workflow_ref: 'workflow-1',
          status: 'completed',
          plan_hash: 'h',
          approval_ref: 'a',
          actor: 'admin',
          started_at: '2026-07-01T00:00:00Z',
          finished_at: '2026-07-01T00:00:01Z',
          steps: [],
        },
      ],
      has_more: false,
    })
    renderPanel()
    await abrirHistorial()

    // findAllByText: el id sale en la fila del historial Y en el detalle del run seleccionado.
    expect((await screen.findAllByText(/run-1/)).length).toBeGreaterThan(0)
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})
