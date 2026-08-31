// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// el aviso de recorte del roster de tenants, ALCANZABLE.
//
// ⛔ POR QUE MONTANDO LA VISTA. Una sonda de fuente no distingue `<ListTruncationBadge …/>` de
//    `{false && <ListTruncationBadge …/>}`: las dos formas la satisfacen y solo una avisa. Es el
//    paso 4 de la receta de `scripts/check-list-truncation-witness.sh`.
//
// ⚠ Y AQUI EL SUJETO IMPORTA MAS QUE EN RESIDENCIA, aunque la lista sea la MISMA ruta: esta
//   pantalla decide a QUIEN se le retira el servicio. Un roster recortado leido como completo es
//   un tenant suspendido —o no suspendido— por una lista incompleta. El triaje del motor, con sus
//   sondas, esta en `./api.ts`.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import type { ReactElement } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import '@/features/_intel'
import './i18n'

const { api, authState } = vi.hoisted(() => ({
  api: { list: vi.fn(), setStatus: vi.fn(), remove: vi.fn() },
  authState: { can: (_p: string): boolean => true },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, tenantsApi: api }
})

import { TenantsView } from './tenants-view'

const ORGS = [
  {
    id: 'o1',
    tenant_id: 't-acme',
    name: 'Acme',
    slug: 'acme',
    status: 'active',
    created_at: '',
  },
  {
    id: 'o2',
    tenant_id: 't-globex',
    name: 'Globex',
    slug: 'globex',
    status: 'suspended',
    created_at: '',
  },
]

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = () => true
  api.list.mockResolvedValue({ items: ORGS, has_more: false })
})

describe('TenantsView · el aviso de recorte es ALCANZABLE', () => {
  it('lo pinta cuando el motor dice que hay mas', async () => {
    api.list.mockResolvedValue({ items: ORGS, has_more: true })
    wrap(<TenantsView />)
    expect(
      await screen.findByText('Loaded 2 tenants; there are more'),
    ).toBeInTheDocument()
  })

  it('y su hint dice que el total NO se puede inferir', async () => {
    api.list.mockResolvedValue({ items: ORGS, has_more: true })
    wrap(<TenantsView />)
    const aviso = await screen.findByText('Loaded 2 tenants; there are more')
    expect(aviso).toHaveAttribute(
      'title',
      expect.stringContaining('CANNOT be inferred'),
    )
  })

  it('CONTRAFACTUAL · sin recorte no hay aviso', async () => {
    api.list.mockResolvedValue({ items: ORGS, has_more: false })
    wrap(<TenantsView />)
    // COTA: la pantalla pinto de verdad.
    expect(await screen.findByText('Acme')).toBeInTheDocument()
    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })

  it('CONTRAFACTUAL · un `has_more` truthy que no es `true` no cuela', async () => {
    api.list.mockResolvedValue({ items: ORGS, has_more: 'false' })
    wrap(<TenantsView />)
    expect(await screen.findByText('Acme')).toBeInTheDocument()
    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })
})
