// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// el aviso de recorte del roster de residencia, ALCANZABLE.
//
// ⛔ POR QUE MONTANDO LA VISTA Y NO LEYENDO LA FUENTE. `{false && <ListTruncationBadge …/>}`
//    satisface a CUALQUIER sonda de texto —el censo lo ve escrito y bien atado a su consulta— y
//    deja la lista recortada SIN aviso en pantalla. Ninguna lectura del fuente puede probar
//    alcanzabilidad: eso solo lo prueba montarlo. Es el paso 4 de la receta de
//    `scripts/check-list-truncation-witness.sh`, y el censo por si solo NO lo cubre.
//
// ⚠ Y LO QUE ESTA CELDA NO AFIRMA: que el motor recorte HOY. No lo hace —`handleListOrgs`
//   (core/api/handlers_core.go:739-761) drena, y el triaje completo esta en `./api.ts`—. Lo que
//   se fija aqui es que el dia que el campo de recorte llegue encendido, la pantalla lo DICE.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import '@/features/_intel'
import './i18n'

const { api, authState } = vi.hoisted(() => ({
  api: { getRegistry: vi.fn(), listOrgs: vi.fn(), setOrgRegion: vi.fn() },
  authState: { can: (_p: string): boolean => true },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, residencyApi: api }
})

import { ResidencyView } from './residency-view'

const ORGS = [
  {
    id: 'o1',
    tenant_id: 't-eu',
    name: 'Acme EU',
    slug: 'acme-eu',
    status: 'active',
    data_region: 'eu',
    created_at: '',
  },
  {
    id: 'o2',
    tenant_id: 't-us',
    name: 'Globex',
    slug: 'globex',
    status: 'active',
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
  api.getRegistry.mockResolvedValue({
    home_region: 'eu',
    regions: ['eu', 'us'],
    enforces: true,
  })
  api.listOrgs.mockResolvedValue({ items: ORGS, has_more: false })
})

describe('ResidencyView · el aviso de recorte es ALCANZABLE', () => {
  it('lo pinta cuando el motor dice que hay mas', async () => {
    api.listOrgs.mockResolvedValue({ items: ORGS, has_more: true })
    wrap(<ResidencyView />)
    expect(
      await screen.findByText('Loaded 2 orgs; there are more'),
    ).toBeInTheDocument()
  })

  it('y su hint dice que el total NO se puede inferir', async () => {
    api.listOrgs.mockResolvedValue({ items: ORGS, has_more: true })
    wrap(<ResidencyView />)
    const aviso = await screen.findByText('Loaded 2 orgs; there are more')
    // El hint viaja en `title`: es lo que lee quien se para encima, y es la mitad que impide
    // que el operador deduzca un total de las filas cargadas.
    expect(aviso).toHaveAttribute(
      'title',
      expect.stringContaining('CANNOT be inferred'),
    )
  })

  it('CONTRAFACTUAL · sin recorte no hay aviso', async () => {
    api.listOrgs.mockResolvedValue({ items: ORGS, has_more: false })
    wrap(<ResidencyView />)
    // COTA: la pantalla pinto de verdad. Sin esto, la ausencia del aviso no diria nada —
    // seria indistinguible de una vista que no llego a montarse.
    expect(await screen.findByText('Acme EU')).toBeInTheDocument()
    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })

  it('CONTRAFACTUAL · un `has_more` que no es el booleano verdadero no cuela', async () => {
    // El componente exige `=== true` porque el tipo es una ASERCION, no una comprobacion en
    // ejecucion: una cadena no vacia del transporte pintaria el aviso por ser truthy.
    api.listOrgs.mockResolvedValue({ items: ORGS, has_more: 'false' })
    wrap(<ResidencyView />)
    expect(await screen.findByText('Acme EU')).toBeInTheDocument()
    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })
})
