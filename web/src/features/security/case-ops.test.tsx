// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ForensicCase } from './types'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
  info: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

const api = vi.hoisted(() => ({
  createCase: vi.fn(),
  updateCase: vi.fn(),
  caseLinks: vi.fn(),
  linkCase: vi.fn(),
  exportCase: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, securityApi: { ...actual.securityApi, ...api } }
})

import { CaseActions, CaseLinksPanel, NewCaseButton } from './case-ops'
import './i18n'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const caso: ForensicCase = {
  id: 'case-1',
  title: 'Exfiltración sospechosa',
  status: 'open',
  severity: 'high',
  subject_kind: 'agent',
  subject_ref: 'agent-9',
  summary: 'Resumen',
  opened_by: 'user:1',
  integrity_ok: true,
  attested_seq: 42,
  opened_at: '2026-08-01T10:00:00Z',
}

beforeEach(() => {
  vi.clearAllMocks()
  api.createCase.mockResolvedValue(caso)
  api.updateCase.mockResolvedValue(caso)
  api.caseLinks.mockResolvedValue({ items: [] })
  api.linkCase.mockResolvedValue({ link_kind: 'finding', link_ref: 'f-1' })
  api.exportCase.mockResolvedValue({})
})

describe('NewCaseButton', () => {
  it('no se ofrece sin permiso de escritura', () => {
    wrap(<NewCaseButton canWrite={false} />)
    expect(screen.queryByRole('button', { name: /new case/i })).toBeNull()
  })

  // El motor abre el caso en `open` y no acepta `status` al crear: ofrecerlo mentiría.
  it('no ofrece estado al crear, y manda sólo título y severidad', async () => {
    const user = userEvent.setup()
    wrap(<NewCaseButton canWrite />)
    await user.click(screen.getByRole('button', { name: /new case/i }))
    expect(screen.queryByLabelText(/^status$/i)).toBeNull()

    fireEvent.change(await screen.findByLabelText(/^title$/i), {
      target: { value: '  Fuga de datos  ' },
    })
    await user.click(screen.getByRole('button', { name: /^create$/i }))
    await waitFor(() => expect(api.createCase).toHaveBeenCalledTimes(1))
    expect(api.createCase.mock.calls[0][0]).toEqual({
      title: 'Fuga de datos',
      severity: 'medium',
    })
  })

  it('no deja crear sin título', async () => {
    const user = userEvent.setup()
    wrap(<NewCaseButton canWrite />)
    await user.click(screen.getByRole('button', { name: /new case/i }))
    expect(
      await screen.findByRole('button', { name: /^create$/i }),
    ).toBeDisabled()
  })
})

describe('CaseActions — el PATCH manda SÓLO lo que cambia', () => {
  it('sin cambios, no hay nada que guardar', async () => {
    wrap(<CaseActions forensicCase={caso} canWrite />)
    expect(
      await screen.findByRole('button', { name: /save changes/i }),
    ).toBeDisabled()
  })

  // La celda que da sentido al tipo: cambiar el estado NO debe reenviar la severidad.
  it('cambiar el estado manda status y NADA más', async () => {
    const user = userEvent.setup()
    wrap(<CaseActions forensicCase={caso} canWrite />)
    await user.click(screen.getByLabelText(/^status$/i))
    await user.click(await screen.findByRole('option', { name: /contained/i }))
    await user.click(screen.getByRole('button', { name: /save changes/i }))

    await waitFor(() => expect(api.updateCase).toHaveBeenCalledTimes(1))
    expect(api.updateCase.mock.calls[0][1]).toEqual({ status: 'contained' })
  })

  it('sin permiso de escritura no ofrece guardar', () => {
    wrap(<CaseActions forensicCase={caso} canWrite={false} />)
    expect(screen.queryByRole('button', { name: /save changes/i })).toBeNull()
  })

  // Los formatos salen del catálogo compartido, no de una lista escrita aquí.
  it('exporta con un formato del catálogo del ledger', async () => {
    const user = userEvent.setup()
    wrap(<CaseActions forensicCase={caso} canWrite />)
    await user.click(screen.getByRole('button', { name: /^export$/i }))
    await waitFor(() => expect(api.exportCase).toHaveBeenCalledTimes(1))
    expect(api.exportCase.mock.calls[0]).toEqual(['case-1', 'cef'])
  })
})

describe('CaseLinksPanel', () => {
  it('sin permiso de escritura no ofrece enlazar', async () => {
    wrap(<CaseLinksPanel caseId="case-1" canWrite={false} />)
    await screen.findByText(/nothing linked yet/i)
    expect(screen.queryByRole('button', { name: /^link$/i })).toBeNull()
  })

  it('manda la clase y la referencia recortada', async () => {
    const user = userEvent.setup()
    wrap(<CaseLinksPanel caseId="case-1" canWrite />)
    fireEvent.change(await screen.findByLabelText(/reference/i), {
      target: { value: '  finding-77  ' },
    })
    await user.click(screen.getByRole('button', { name: /^link$/i }))
    await waitFor(() => expect(api.linkCase).toHaveBeenCalledTimes(1))
    expect(api.linkCase.mock.calls[0][1]).toEqual({
      link_kind: 'finding',
      link_ref: 'finding-77',
    })
  })

  it('pinta los enlaces existentes con su clase', async () => {
    api.caseLinks.mockResolvedValue({
      items: [{ link_kind: 'audit_seq', link_ref: '99', linked_by: 'user:2' }],
    })
    wrap(<CaseLinksPanel caseId="case-1" canWrite />)
    expect(await screen.findByText('99')).toBeInTheDocument()
    expect(screen.getByText(/ledger sequence/i)).toBeInTheDocument()
  })
})
