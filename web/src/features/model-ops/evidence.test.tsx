// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// AIBOM-seal ledger tests. These pin the honesty of the seal detail: ledger_seq == 0 is
// rendered as "no prior audit head" (never a sequence-zero proof), and the drawer states a
// seal is a content-hash commitment, not an archive of the BOM.
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import type { AibomSeal } from '@/features/models/types'

const authState = vi.hoisted(() => ({
  activeTenant: 't1' as string | null,
  can: (_p: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({ aibomSeals: vi.fn() }))
vi.mock('@/features/models/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/models/api')>()
  return { ...actual, modelsApi: api }
})

import { modelsKeys } from '@/features/models/api'
import { ModelEvidenceTab } from './evidence'
import './i18n'
import '@/features/_intel'

const seal: AibomSeal = {
  id: 's1',
  owned_ref: 'om1',
  serial_number: 'urn:uuid:abc',
  content_hash: 'a'.repeat(64),
  spec_version: '1.6',
  component_count: 3,
  ledger_seq: 0,
  generated_at: '2026-07-21T10:00:00Z',
}

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider delayDuration={0}>{ui}</TooltipProvider>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  api.aibomSeals.mockReset()
  api.aibomSeals.mockResolvedValue({ items: [seal] })
})
afterEach(() => vi.clearAllMocks())

describe('ModelEvidenceTab — honest seal detail', () => {
  it('renders a ledger_seq of 0 as "no prior audit head", not a zero sequence', async () => {
    const user = userEvent.setup()
    wrap(<ModelEvidenceTab />)
    // Open the seal row's detail drawer.
    await user.click(await screen.findByText('om1'))
    expect(await screen.findByText(/no prior audit head/i)).toBeInTheDocument()
    // And it explains a seal is a content-hash commitment, not the BOM.
    expect(screen.getByText(/not an archive of the bom/i)).toBeInTheDocument()
  })

  it('lists the sealed AIBOM commitment', async () => {
    wrap(<ModelEvidenceTab />)
    await waitFor(() => expect(api.aibomSeals).toHaveBeenCalled())
    expect(await screen.findByText('om1')).toBeInTheDocument()
  })

  /**
   * ⛔ EL TECHO LLEGA A LA LLAMADA. Sin `limit` el repositorio genérico pagina a 100, y este
   * ledger es APPEND-ONLY (`handleSealAIBOM` hace `repo.Create` por precinto), así que la
   * pestaña enseñaba los CIEN PRIMEROS por `id ASC` — orden de creación— y callaba. Este
   * testigo mide el TRANSPORTE, no la intención: el techo tiene que ir en la llamada.
   */
  it('pide el techo real del motor, no la página por defecto', async () => {
    wrap(<ModelEvidenceTab />)
    await waitFor(() => expect(api.aibomSeals).toHaveBeenCalled())
    expect(api.aibomSeals).toHaveBeenLastCalledWith({ limit: 1000 })
  })

  /**
   * ⛔ Y EL AVISO, EN LAS DOS DIRECCIONES. La casilla negativa es la que impide un aviso que
   * salga siempre: uno que aparece con la lista completa no informa de nada y enseña a
   * ignorarlo. Además el aviso dice QUÉ mil —por id, o sea lo más reciente es lo que falta—,
   * que es lo único que el dato sostiene.
   */
  it('declara el recorte cuando el motor dice has_more, y no cuando no', async () => {
    api.aibomSeals.mockResolvedValue({ items: [seal], has_more: true })
    wrap(<ModelEvidenceTab />)
    // ⛔ LA CIFRA ES LA QUE LLEGÓ, no el techo que se pidió. Con la constante interpolada el
    //    aviso diría «1000» aunque el motor hubiera devuelto una sola fila —un motor viejo con
    //    otro `maxLimit`, o una respuesta acotada por otra razón— y eso es una medida
    //    inventada en la pantalla que existe para no inventar medidas. Lo pidió el contraste
    //    externo tras ver que un mutante que volvía a la constante ESCAPABA.
    expect(
      await screen.findByText('Loaded 1 seals; there are more'),
    ).toBeVisible()

    cleanup()
    api.aibomSeals.mockResolvedValue({ items: [seal], has_more: false })
    wrap(<ModelEvidenceTab />)
    expect(await screen.findByText('om1')).toBeInTheDocument()
    expect(screen.queryByText(/Loaded \d+ seals; there are more/i)).toBeNull()
  })

  /**
   * LA DIRECCIÓN QUE TAMPOCO DEBE DISPARAR: si la consulta falla, el aviso no puede quedarse
   * flotando sobre una tabla que ya sólo enseña el error. «No pude mirar» no es «hay más».
   *
   * ⛔ Y HAY QUE MONTARLO CON DATO VIEJO EN LA CACHÉ, no con un simple rechazo: con la consulta
   * fallando desde el primer intento, `data` es `undefined` y el aviso no sale **aunque se
   * quite la guarda**. La casilla pasaría sin llegar a ella — lo cazó un mutante que quitaba
   * el `&& !query.error` y ESCAPABA. El caso real es el que importa: react-query CONSERVA el
   * último dato bueno mientras marca el error, así que sin la guarda el aviso se quedaría
   * flotando sobre una tabla que ya sólo enseña el fallo.
   */
  it('un refetch fallido retira el aviso aunque el dato viejo diga has_more', async () => {
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: 0 } },
    })
    qc.setQueryData(modelsKeys.aibomSeals('t1', undefined, { limit: 1000 }), {
      items: [seal],
      has_more: true,
    })
    api.aibomSeals.mockRejectedValue(new Error('boom'))
    render(
      <QueryClientProvider client={qc}>
        <TooltipProvider delayDuration={0}>
          <ModelEvidenceTab />
        </TooltipProvider>
      </QueryClientProvider>,
    )
    // El dato viejo se pinta (has_more: true) y el refetch falla…
    await waitFor(() => expect(api.aibomSeals).toHaveBeenCalled())
    await waitFor(() =>
      expect(
        qc.getQueryState(
          modelsKeys.aibomSeals('t1', undefined, { limit: 1000 }),
        )?.error,
      ).toBeTruthy(),
    )
    // …y el aviso NO se queda flotando.
    expect(screen.queryByText(/Loaded \d+ seals; there are more/i)).toBeNull()
  })
})
