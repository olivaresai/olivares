// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Model-documents tests — the seal is the only durable action, so it is the one under the
// most scrutiny. These pin: sealing is confirmed before it runs; a successful seal opens a
// receipt that offers the sealed CycloneDX document (the ledger keeps only the hash, so it
// is the operator's one chance to save it); the live documents and the sealed commitments
// are presented as distinct; and the copy states SPDX can never be sealed.
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import type { AibomSealReceipt } from '@/features/models/types'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
  info: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

const authState = vi.hoisted(() => ({
  activeTenant: 't1' as string | null,
  can: (_p: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({
  aibomSeals: vi.fn(),
  sealAibom: vi.fn(),
  generateAibom: vi.fn(),
  modelCard: vi.fn(),
}))
vi.mock('@/features/models/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/models/api')>()
  return { ...actual, modelsApi: api }
})

const exp = vi.hoisted(() => ({
  downloadBlob: vi.fn(),
  downloadJson: vi.fn(),
  fetchModelCardMarkdown: vi.fn(),
}))
vi.mock('./export', () => exp)

import { modelsKeys } from '@/features/models/api'
import { ModelDocuments } from './documents'
import './i18n'
import '@/features/_intel'

const receipt: AibomSealReceipt = {
  seal: {
    id: 's1',
    owned_ref: 'om1',
    serial_number: 'urn:uuid:abc',
    content_hash: 'a'.repeat(64),
    spec_version: '1.6',
    component_count: 3,
    ledger_seq: 0,
    generated_at: '2026-07-21T10:00:00Z',
  },
  aibom: { bomFormat: 'CycloneDX', specVersion: '1.6' },
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
  authState.can = () => true
  api.aibomSeals.mockReset()
  api.sealAibom.mockReset()
  exp.downloadJson.mockReset()
  toast.success.mockReset()
  api.aibomSeals.mockResolvedValue({ items: [] })
  api.sealAibom.mockResolvedValue(receipt)
})
afterEach(() => vi.clearAllMocks())

describe('ModelDocuments — live vs sealed', () => {
  it('separates generated (live) documents from sealed commitments', async () => {
    wrap(<ModelDocuments ownedRef="om1" ownedName="Acme" canWrite />)
    expect(
      await screen.findByText(/generated documents — live/i),
    ).toBeInTheDocument()
    expect(screen.getByText(/sealed aibom commitments/i)).toBeInTheDocument()
    // The seal hint states SPDX can never be sealed.
    expect(
      screen.getByText(/spdx export can never be sealed/i),
    ).toBeInTheDocument()
  })
})

describe('ModelDocuments — sealing', () => {
  it('confirms before sealing and never posts on cancel', async () => {
    const user = userEvent.setup()
    wrap(<ModelDocuments ownedRef="om1" ownedName="Acme" canWrite />)
    await user.click(
      await screen.findByRole('button', { name: /^seal aibom$/i }),
    )
    // The confirm dialog intercepts (its title is unique) — no POST yet.
    expect(await screen.findByText(/seal this aibom\?/i)).toBeInTheDocument()
    expect(api.sealAibom).not.toHaveBeenCalled()
  })

  it('after sealing, offers the sealed document for download (the ledger keeps only the hash)', async () => {
    const user = userEvent.setup()
    wrap(<ModelDocuments ownedRef="om1" ownedName="Acme" canWrite />)
    await user.click(
      await screen.findByRole('button', { name: /^seal aibom$/i }),
    )
    // Confirm.
    const dialog = await screen.findByRole('dialog')
    await user.click(
      within(dialog).getByRole('button', { name: /^seal aibom$/i }),
    )
    await waitFor(() => expect(api.sealAibom).toHaveBeenCalledWith('om1'))
    // The receipt dialog offers the sealed BOM.
    expect(
      await screen.findByRole('button', { name: /download sealed bom/i }),
    ).toBeInTheDocument()
    await user.click(
      screen.getByRole('button', { name: /download sealed bom/i }),
    )
    expect(exp.downloadJson).toHaveBeenCalledTimes(1)
    // It downloads the seal's OWN aibom (receipt.aibom), not a re-generated doc.
    expect(exp.downloadJson.mock.calls[0][0]).toBe(receipt.aibom)
  })

  it('does not offer a seal action to a read-only principal', async () => {
    wrap(<ModelDocuments ownedRef="om1" ownedName="Acme" canWrite={false} />)
    await screen.findByText(/generated documents — live/i)
    expect(
      screen.queryByRole('button', { name: /^seal aibom$/i }),
    ).not.toBeInTheDocument()
  })
})

describe('ModelDocuments — el historial de precintos declara su recorte', () => {
  /**
   * ⛔ EL TECHO LLEGA A LA LLAMADA, con el filtro del modelo. Precintar CREA fila
   * (`handleSealAIBOM` → `repo.Create`), así que el historial de un modelo crece sin tope y
   * sin `limit` salían los cien primeros por `id ASC`: el precinto MÁS RECIENTE es justo el
   * que se pierde. Este testigo mide el transporte, no la intención.
   */
  it('pide el techo real del motor junto al filtro del modelo', async () => {
    wrap(<ModelDocuments ownedRef="om1" ownedName="Acme" canWrite />)
    await waitFor(() => expect(api.aibomSeals).toHaveBeenCalled())
    expect(api.aibomSeals).toHaveBeenLastCalledWith({
      owned_ref: 'om1',
      limit: 1000,
    })
  })

  /**
   * ⛔ LA GUARDA DEL ERROR, montada con dato viejo en la caché. Con la consulta fallando desde
   * el primer intento, `data` es `undefined` y el aviso no sale **aunque se quite el
   * `&& !historyQ.error`**: la casilla pasaría sin llegar a la guarda. react-query CONSERVA el
   * último dato bueno mientras marca el error, y ése es el escenario real.
   */
  it('un refetch fallido retira el aviso aunque el dato viejo diga has_more', async () => {
    const clave = modelsKeys.aibomSeals('t1', 'om1', {
      owned_ref: 'om1',
      limit: 1000,
    })
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: 0 } },
    })
    // Se siembra una fila REAL, no una lista vacía con `has_more`: el caso que importa es el
    // que deja dato viejo en pantalla, y con `items: []` no se distinguiría del vacío.
    qc.setQueryData(clave, {
      items: [
        {
          id: 's9',
          owned_ref: 'om1',
          serial_number: 'urn:uuid:old',
          content_hash: 'b'.repeat(64),
          spec_version: '1.6',
          component_count: 1,
          ledger_seq: 3,
          generated_at: '2026-07-20T10:00:00Z',
        },
      ],
      has_more: true,
    })
    api.aibomSeals.mockRejectedValue(new Error('boom'))
    render(
      <QueryClientProvider client={qc}>
        <TooltipProvider delayDuration={0}>
          <ModelDocuments ownedRef="om1" ownedName="Acme" canWrite />
        </TooltipProvider>
      </QueryClientProvider>,
    )
    await waitFor(() => expect(api.aibomSeals).toHaveBeenCalled())
    await waitFor(() => expect(qc.getQueryState(clave)?.error).toBeTruthy())
    // El aviso no se queda flotando…
    expect(screen.queryByText(/Loaded \d+ seals; there are more/i)).toBeNull()
    // …y lo que ocupa el sitio es un ERROR, no la lista vieja ni «no hay precintos»: sin la
    // rama de error, esta pantalla dejaba una lista VIEJA y RECORTADA sin marca ninguna.
    expect(await screen.findByText(/could not be read/i)).toBeInTheDocument()
    expect(screen.queryByText(/no sealed/i)).toBeNull()
  })

  /**
   * ⛔ Y LA MITAD QUE MÁS CUESTA: un fallo en la PRIMERA lectura no puede leerse como «este
   * modelo no tiene precintos». Es una afirmación sobre el mundo hecha con un fallo de red, y
   * en un panel de evidencia es de las caras. `VersionEvidence` ya tenía esta rama por lo
   * mismo; a este panel se la devolvió el contraste externo.
   */
  it('un fallo de la primera lectura no se pinta como «no hay precintos»', async () => {
    api.aibomSeals.mockRejectedValue(new Error('boom'))
    wrap(<ModelDocuments ownedRef="om1" ownedName="Acme" canWrite />)
    expect(await screen.findByText(/could not be read/i)).toBeInTheDocument()
    expect(screen.queryByText(/no sealed/i)).toBeNull()
  })

  /** El aviso, en las dos direcciones: aparece con `has_more` y NO aparece sin él. */
  it('declara el recorte cuando el motor dice has_more, y no cuando no', async () => {
    api.aibomSeals.mockResolvedValue({ items: [], has_more: true })
    wrap(<ModelDocuments ownedRef="om1" ownedName="Acme" canWrite />)
    expect(
      await screen.findByText(/Loaded \d+ seals; there are more/i),
    ).toBeVisible()

    cleanup()
    api.aibomSeals.mockResolvedValue({ items: [], has_more: false })
    wrap(<ModelDocuments ownedRef="om1" ownedName="Acme" canWrite />)
    await waitFor(() => expect(api.aibomSeals).toHaveBeenCalled())
    expect(screen.queryByText(/Loaded \d+ seals; there are more/i)).toBeNull()
  })
})
