// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { EmergingStandardsResponse } from './types'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

const api = vi.hoisted(() => ({ emergingStandards: vi.fn() }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, governanceApi: { ...actual.governanceApi, ...api } }
})

import { EmergingStandardsPanel } from './emerging-standards'
import '@/features/_intel'
import './i18n'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const DISCLAIMER =
  'Design-toward only (IDN-12): these emerging agent-identity standards are TRACKED, not implemented.'

const respuesta: EmergingStandardsResponse = {
  verified_at: '2026-06',
  disclaimer: DISCLAIMER,
  standards: [
    {
      key: 'oauth-id-assert',
      name: 'Identity Assertion Authorization Grant',
      body: 'IETF OAuth WG',
      spec: 'draft-ietf-oauth-identity-assertion-authz-grant',
      revision: '-04',
      status: 'draft',
      seam: 'core/auth: token exchange for agent-acting-as-user',
      caveat:
        'The grant type is not registered; wiring it now would freeze a draft.',
      verified_at: '2026-06',
      authority:
        'https://datatracker.ietf.org/doc/draft-ietf-oauth-identity-assertion-authz-grant/',
    },
  ],
}

beforeEach(() => {
  vi.clearAllMocks()
  api.emergingStandards.mockResolvedValue(respuesta)
})

describe('EmergingStandardsPanel — no prometer de más es el requisito', () => {
  it('pinta el aviso del motor, literal', async () => {
    wrap(<EmergingStandardsPanel canRead />)
    expect(
      await screen.findByText(/TRACKED, not implemented/i),
    ).toBeInTheDocument()
  })

  // La razón de existir de la fila: sin el caveat, un registro de seguimiento se lee como
  // catálogo de soporte.
  it('cada estándar enseña SIEMPRE su nota de honestidad', async () => {
    wrap(<EmergingStandardsPanel canRead />)
    expect(
      await screen.findByText(/wiring it now would freeze a draft/i),
    ).toBeInTheDocument()
  })

  it('nombra el seam como donde ENCAJARÍA, no como algo que exista', async () => {
    wrap(<EmergingStandardsPanel canRead />)
    expect(await screen.findByText(/would plug in at/i)).toBeInTheDocument()
    expect(screen.getByText(/core\/auth: token exchange/i)).toBeInTheDocument()
  })

  // El motor declara `verified_at` grueso a propósito: se muestra tal cual.
  it('muestra el mes verificado sin convertirlo en una fecha exacta', async () => {
    wrap(<EmergingStandardsPanel canRead />)
    expect(await screen.findByText(/2026-06/)).toBeInTheDocument()
    expect(screen.queryByText(/2026-06-01/)).toBeNull()
  })

  it('el enlace a la fuente primaria no filtra la referencia', async () => {
    wrap(<EmergingStandardsPanel canRead />)
    const a = await screen.findByRole('link', { name: /primary source/i })
    expect(a).toHaveAttribute('rel', expect.stringContaining('noopener'))
    expect(a).toHaveAttribute('href', respuesta.standards[0].authority)
  })

  it('sin permiso de lectura no llama al motor', async () => {
    wrap(<EmergingStandardsPanel canRead={false} />)
    expect(await screen.findByText(/do not have access/i)).toBeInTheDocument()
    expect(api.emergingStandards).not.toHaveBeenCalled()
  })

  it('una lista vacía se dice, no se calla', async () => {
    api.emergingStandards.mockResolvedValue({ ...respuesta, standards: [] })
    wrap(<EmergingStandardsPanel canRead />)
    expect(await screen.findByText(/nothing tracked/i)).toBeInTheDocument()
    // El aviso sigue estando: no depende de que haya filas.
    expect(screen.getByText(/TRACKED, not implemented/i)).toBeInTheDocument()
  })
})
