// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { WorkDecision } from './types'
import './i18n'

/**
 * C-13 — «cargar más» ACUMULA, no reemplaza.
 *
 * ⛔ EL DEFECTO QUE FIJA, con su línea de origen. `decisions-panel.tsx` usaba `useQuery` con el
 *    cursor DENTRO de la `queryKey`: al pulsar «cargar más», la consulta traía la página
 *    siguiente y el render pintaba sólo `page.items`, así que **las decisiones ya leídas
 *    desaparecían de la pantalla**. En una consola de gobierno eso es peor que una lista
 *    recortada sin avisar: el recorte no miente sobre lo que ya se vio, y esto sí — un revisor
 *    que pagina para buscar una decisión pierde de vista las que acaba de descartar.
 *
 * ⛔ POR QUÉ ESTE FICHERO EXISTE Y NO BASTA EL TYPECHECK. `useInfiniteQuery` compila igual de
 *    bien mal usado: si el render se quedara en la última página en vez de aplanar todas, `tsc`
 *    no diría nada y la pantalla seguiría perdiendo filas. Estos casos leen la PANTALLA.
 *
 * ⛔ Y EL SEGUNDO CASO NO ES DECORATIVO. Las dos vistas —`effective` e `history`— paginan tablas
 *    distintas: acumular a través de un cambio de vista mezclaría filas de dos consultas y sería
 *    PEOR que el defecto que arreglamos. El arreglo lo resuelve por construcción (`view` está en
 *    la `queryKey`), pero «por construcción» es justo lo que hay que atar: la construcción cambia.
 */

let authState = { can: (_: string) => false, activeTenant: 't1' }
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

/**
 * ⛔ SIN `as WorkDecision`, y esto me costo los cuatro casos en la primera pasada. Con el cast
 *    escribi un fixture con `verdict`, `reason` y `decided_at` — TRES CAMPOS QUE NO EXISTEN en el
 *    tipo — y compilaba: el cast apaga justo la comprobacion que lo habria dicho. La pantalla
 *    renderizaba filas vacias y los casos fallaban en `findByText` como si el componente
 *    estuviera roto. El propio comentario de `WorkDecision` (types.ts:196-203) cuenta ESA MISMA
 *    historia con otros dos campos: «llegaron undefined sin error, warning ni test que fallara».
 *    Sin cast, el compilador nombra el campo que falta.
 */
const decision = (
  id: string,
  over: Partial<WorkDecision> = {},
): WorkDecision => ({
  id,
  workspace_id: 'ws1',
  work_item_id: 'w1',
  decision_key: `k-${id}`,
  decision_seq: 1,
  subject_kind: 'agent',
  subject_ref: 'a1',
  operation: 'deploy',
  statement_md: `motivo ${id}`,
  rationale_md: 'porque si',
  decided_by_kind: 'user',
  decided_by_ref: 'u1',
  authority_ref: 'auth1',
  effective_at: '2026-08-29T00:00:00Z',
  decision_hash: `h-${id}`,
  state: 'effective',
  ...over,
})

const listDecisionsMock = vi.fn()
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return {
    ...actual,
    listDecisions: (...a: unknown[]) => listDecisionsMock(...a),
  }
})

const { DecisionsPanel } = await import('./decisions-panel')

function show() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <DecisionsPanel workItemId="w1" />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  listDecisionsMock.mockReset()
  authState = { can: () => false, activeTenant: 't1' }
})

describe('C-13 · decisiones: la página siguiente ACUMULA', () => {
  it('cargar mas CONSERVA las filas ya vistas', async () => {
    listDecisionsMock
      .mockResolvedValueOnce({
        items: [decision('d1'), decision('d2')],
        has_more: true,
        next_cursor: 'c2',
      })
      .mockResolvedValueOnce({
        items: [decision('d3')],
        has_more: false,
        next_cursor: undefined,
      })

    show()
    await screen.findByText('motivo d1')
    expect(screen.getByText('motivo d2')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: /más|more/i }))

    // La de la SEGUNDA pagina aparece...
    await screen.findByText('motivo d3')
    // ...y las de la PRIMERA siguen ahi. Este es el defecto que se arreglo.
    expect(screen.getByText('motivo d1')).toBeInTheDocument()
    expect(screen.getByText('motivo d2')).toBeInTheDocument()
  })

  it('el cursor de la segunda llamada es el `next_cursor` de la primera', async () => {
    // Sin esto, «acumula» podria pasar pidiendo DOS VECES la misma pagina.
    listDecisionsMock
      .mockResolvedValueOnce({
        items: [decision('d1')],
        has_more: true,
        next_cursor: 'CURSOR-2',
      })
      .mockResolvedValueOnce({
        items: [decision('d2')],
        has_more: false,
        next_cursor: undefined,
      })

    show()
    await screen.findByText('motivo d1')
    await userEvent.click(screen.getByRole('button', { name: /más|more/i }))
    await screen.findByText('motivo d2')

    expect(listDecisionsMock).toHaveBeenCalledTimes(2)
    const primera = listDecisionsMock.mock.calls[0][0] as { cursor?: string }
    const segunda = listDecisionsMock.mock.calls[1][0] as { cursor?: string }
    expect(primera.cursor).toBeUndefined()
    expect(segunda.cursor).toBe('CURSOR-2')
  })

  it('el boton desaparece tras paginar hasta el final', async () => {
    // ⛔ ESTE CASO LO PIDIO UN MUTANTE SUPERVIVIENTE, y por eso esta escrito asi. Tomar
    //    `has_more` de la PRIMERA pagina en vez de la ULTIMA pasaba los otros cuatro: el caso de
    //    abajo sólo prueba UNA pagina, donde primera y ultima son la misma, y los de acumulacion
    //    no miran el boton. Con `paginas[0]` el boton seguiria ofreciendo una pagina que ya no
    //    existe. Un mutante que sobrevive no siempre acusa al codigo: aqui acusaba al testigo.
    listDecisionsMock
      .mockResolvedValueOnce({
        items: [decision('p1')],
        has_more: true,
        next_cursor: 'c2',
      })
      .mockResolvedValueOnce({
        items: [decision('p2')],
        has_more: false,
        next_cursor: undefined,
      })

    show()
    await screen.findByText('motivo p1')
    await userEvent.click(screen.getByRole('button', { name: /más|more/i }))
    await screen.findByText('motivo p2')

    await waitFor(() => {
      expect(
        screen.queryByRole('button', { name: /más|more/i }),
      ).not.toBeInTheDocument()
    })
  })

  it('CONTROL: el boton desaparece cuando no hay mas paginas', async () => {
    // Calibra el caso de arriba: si el boton estuviera SIEMPRE, el primer caso pasaria por
    // accidente en cualquier implementacion que lo pintara.
    listDecisionsMock.mockResolvedValueOnce({
      items: [decision('d1')],
      has_more: false,
      next_cursor: undefined,
    })
    show()
    await screen.findByText('motivo d1')
    expect(
      screen.queryByRole('button', { name: /más|more/i }),
    ).not.toBeInTheDocument()
  })

  it('cambiar de vista TIRA lo acumulado: las dos vistas paginan tablas distintas', async () => {
    listDecisionsMock
      .mockResolvedValueOnce({
        items: [decision('efectiva-1')],
        has_more: true,
        next_cursor: 'c2',
      })
      .mockResolvedValueOnce({
        items: [decision('efectiva-2')],
        has_more: false,
        next_cursor: undefined,
      })
      .mockResolvedValueOnce({
        items: [decision('historica-1')],
        has_more: false,
        next_cursor: undefined,
      })

    show()
    await screen.findByText('motivo efectiva-1')
    await userEvent.click(screen.getByRole('button', { name: /más|more/i }))
    await screen.findByText('motivo efectiva-2')

    // Cambio de vista: lo acumulado de `effective` NO puede sobrevivir en `history`.
    const tabs = screen.getAllByRole('tab')
    await userEvent.click(tabs[tabs.length - 1])

    await screen.findByText('motivo historica-1')
    await waitFor(() => {
      expect(screen.queryByText('motivo efectiva-1')).not.toBeInTheDocument()
    })
    expect(screen.queryByText('motivo efectiva-2')).not.toBeInTheDocument()
  })
})
