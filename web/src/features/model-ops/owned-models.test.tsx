// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// El parque de modelos propios y sus versiones: las DOS listas de este fichero pedían sin
// `limit`, así que el repositorio genérico paginaba a 100 y la consola tiraba el `has_more` que
// los dos handlers publican. El parque recortado se leía «éstos son nuestros modelos», y una
// versión que no se ve es también una versión cuya evidencia de admisión no se alcanza.
//
// Este fichero no existía: `owned-models.tsx` tenía 863 líneas y ninguna casilla.
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import type { OwnedModel } from '@/features/models/types'

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
  ownedModels: vi.fn(),
  modelVersions: vi.fn(),
  modelAdmissions: vi.fn(),
  createOwnedModel: vi.fn(),
  updateOwnedModel: vi.fn(),
  deleteOwnedModel: vi.fn(),
  createModelVersion: vi.fn(),
  deleteModelVersion: vi.fn(),
  aibomSeals: vi.fn(),
  generateAibom: vi.fn(),
  modelCard: vi.fn(),
  sealAibom: vi.fn(),
}))
vi.mock('@/features/models/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/models/api')>()
  return { ...actual, modelsApi: api }
})

import { modelsKeys } from '@/features/models/api'
import { OwnedModelsTab } from './owned-models'
import './i18n'
import '@/features/_intel'

const modelo: OwnedModel = {
  id: 'om1',
  name: 'Acme base',
  kind: 'foundation',
  visibility: 'private',
  status: 'active',
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
  for (const fn of Object.values(api)) fn.mockReset()
  api.ownedModels.mockResolvedValue({ items: [modelo], has_more: false })
  api.modelVersions.mockResolvedValue({ items: [], has_more: false })
  api.modelAdmissions.mockResolvedValue({ items: [], has_more: false })
  api.aibomSeals.mockResolvedValue({ items: [], has_more: false })
})
afterEach(() => vi.clearAllMocks())

describe('El parque de modelos propios', () => {
  it('pide el techo real del motor', async () => {
    wrap(<OwnedModelsTab />)
    await waitFor(() => expect(api.ownedModels).toHaveBeenCalled())
    expect(api.ownedModels).toHaveBeenLastCalledWith({ limit: 1000 })
  })

  /** El aviso en las dos direcciones, con la cifra REAL y no la constante pedida. */
  it('declara el recorte con has_more y no sin él', async () => {
    api.ownedModels.mockResolvedValue({ items: [modelo], has_more: true })
    wrap(<OwnedModelsTab />)
    expect(
      await screen.findByText('Loaded 1 models; there are more'),
    ).toBeVisible()

    cleanup()
    api.ownedModels.mockResolvedValue({ items: [modelo], has_more: false })
    wrap(<OwnedModelsTab />)
    await screen.findByText('Acme base')
    expect(screen.queryByText(/Loaded \d+ models; there are more/i)).toBeNull()
  })

  /**
   * ⛔ LA GUARDA DEL ERROR, con dato viejo sembrado: un rechazo desde el primer intento deja
   * `data` en `undefined` y la casilla pasaría SIN llegar a la guarda.
   */
  it('un refetch fallido retira el aviso aunque el dato viejo diga has_more', async () => {
    const clave = modelsKeys.ownedModels('t1', { limit: 1000 })
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: 0 } },
    })
    qc.setQueryData(clave, { items: [modelo], has_more: true })
    api.ownedModels.mockRejectedValue(new Error('boom'))
    render(
      <QueryClientProvider client={qc}>
        <TooltipProvider delayDuration={0}>
          <OwnedModelsTab />
        </TooltipProvider>
      </QueryClientProvider>,
    )
    await waitFor(() => expect(api.ownedModels).toHaveBeenCalled())
    await waitFor(() => expect(qc.getQueryState(clave)?.error).toBeTruthy())
    expect(screen.queryByText(/Loaded \d+ models; there are more/i)).toBeNull()
  })
})

describe('Las versiones de un modelo', () => {
  async function abrirCajon() {
    const user = userEvent.setup()
    wrap(<OwnedModelsTab />)
    await user.click(await screen.findByText('Acme base'))
    return user
  }

  it('pide el techo junto al filtro del modelo', async () => {
    await abrirCajon()
    await waitFor(() => expect(api.modelVersions).toHaveBeenCalled())
    expect(api.modelVersions).toHaveBeenLastCalledWith({
      owned_ref: 'om1',
      limit: 1000,
    })
  })

  it('declara el recorte con has_more y no sin él', async () => {
    api.modelVersions.mockResolvedValue({
      items: [{ id: 'v1', owned_ref: 'om1', version: '1.0', status: 'draft' }],
      has_more: true,
    })
    await abrirCajon()
    expect(
      await screen.findByText('Loaded 1 versions; there are more'),
    ).toBeVisible()

    // ⛔ Y LA MITAD QUE EL NOMBRE PROMETÍA Y NO ESTABA: sin `has_more` el aviso NO sale. Sin
    //    esta casilla, un mutante que dejara la condición en sólo `!query.error` pasaría, y la
    //    pantalla declararía un recorte permanente que no existe. Lo señaló el contraste, y el
    //    defecto era del nombre del test tanto como del test.
    cleanup()
    api.modelVersions.mockResolvedValue({
      items: [{ id: 'v1', owned_ref: 'om1', version: '1.0', status: 'draft' }],
      has_more: false,
    })
    await abrirCajon()
    await waitFor(() => expect(api.modelVersions).toHaveBeenCalled())
    expect(
      screen.queryByText(/Loaded \d+ versions; there are more/i),
    ).toBeNull()
  })

  /**
   * ⛔ LA GUARDA DEL ERROR DEL AVISO, con dato viejo sembrado. La casilla de abajo rechaza desde
   * el primer intento y por eso NO llega a la guarda: `data` es `undefined` y el aviso no sale
   * aunque se quite el `&& !query.error`. Lo cazó un mutante que la quitaba y ESCAPABA — el
   * único de los cinco de esta unidad que quedaba sin testigo.
   */
  it('un refetch fallido retira el aviso de versiones aunque el dato viejo diga has_more', async () => {
    const clave = modelsKeys.modelVersions('t1', 'om1', {
      owned_ref: 'om1',
      limit: 1000,
    })
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: 0 } },
    })
    qc.setQueryData(clave, {
      items: [{ id: 'v1', owned_ref: 'om1', version: '1.0', status: 'draft' }],
      has_more: true,
    })
    api.modelVersions.mockRejectedValue(new Error('boom'))
    const user = userEvent.setup()
    render(
      <QueryClientProvider client={qc}>
        <TooltipProvider delayDuration={0}>
          <OwnedModelsTab />
        </TooltipProvider>
      </QueryClientProvider>,
    )
    await user.click(await screen.findByText('Acme base'))
    await waitFor(() => expect(api.modelVersions).toHaveBeenCalled())
    await waitFor(() => expect(qc.getQueryState(clave)?.error).toBeTruthy())
    expect(
      screen.queryByText(/Loaded \d+ versions; there are more/i),
    ).toBeNull()
  })

  /**
   * ⛔ UNA LECTURA FALLIDA NO ES «NO HAY VERSIONES». Sin rama de error, un 500 caía al estado
   * vacío y el panel afirmaba una ausencia que nadie comprobó — y una versión invisible es una
   * versión cuya evidencia de admisión no se alcanza desde esta pantalla. Es el mismo defecto
   * que el contraste externo devolvió en el panel de documentos, en la misma feature.
   */
  it('un fallo de lectura no se pinta como «no hay versiones»', async () => {
    api.modelVersions.mockRejectedValue(new Error('boom'))
    await abrirCajon()
    expect(await screen.findByText(/could not be read/i)).toBeInTheDocument()
    // El texto exacto del estado vacío, no una subcadena: el propio aviso de error contiene
    // «no versions» entre comillas, así que un `/no versions/i` casaría consigo mismo.
    expect(screen.queryByText('No versions yet')).toBeNull()
  })
})
