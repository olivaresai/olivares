// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import './i18n'

const { api, fetchReportMock, downloadBlobMock, authState } = vi.hoisted(
  () => ({
    api: {
      listReports: vi.fn(),
      listSchedules: vi.fn(),
      createSchedule: vi.fn(),
      deleteSchedule: vi.fn(),
      scheduleRuns: vi.fn(),
    },
    fetchReportMock: vi.fn(),
    downloadBlobMock: vi.fn(),
    authState: {
      activeTenant: 'tenant-A' as string | null,
      can: (_p: string): boolean => true,
    },
  }),
)

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return {
    ...actual,
    reportingApi: api,
    fetchReport: fetchReportMock,
    downloadBlob: downloadBlobMock,
  }
})

import { ReportingView } from './reporting-view'

const reports = [
  {
    type: 'compliance-evidence',
    title: 'Compliance Evidence Report',
    description: 'Compliance posture by framework.',
    formats: ['html', 'pdf'],
  },
  {
    type: 'finops-report',
    title: 'FinOps Report',
    description: 'AI spend breakdown.',
    formats: ['html', 'pdf'],
  },
]

const schedule = {
  id: 'schedule-1',
  report_type: 'compliance-evidence',
  format: 'html',
  cron: '0 8 * * 1',
  enabled: true,
}

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = () => true
  api.listReports.mockResolvedValue({ items: reports })
  api.listSchedules.mockResolvedValue({ items: [] })
  api.deleteSchedule.mockResolvedValue({ deleted: true })
  api.scheduleRuns.mockResolvedValue({ items: [] })
  fetchReportMock.mockResolvedValue({
    blob: new Blob(['<html></html>'], { type: 'text/html' }),
    contentType: 'text/html',
    filename: 'olivares-compliance-evidence-2026-07-09.html',
  })
})

describe('ReportingView', () => {
  it('forbids a reader without the permission', () => {
    authState.can = () => false
    wrap(<ReportingView />)
    expect(
      screen.queryByText('Compliance Evidence Report'),
    ).not.toBeInTheDocument()
    expect(api.listReports).not.toHaveBeenCalled()
  })

  it('lists the report catalog', async () => {
    wrap(<ReportingView />)
    expect(
      await screen.findByText('Compliance Evidence Report'),
    ).toBeInTheDocument()
    expect(screen.getByText('FinOps Report')).toBeInTheDocument()
  })

  it('generates + downloads a report through the real endpoint', async () => {
    const user = userEvent.setup()
    wrap(<ReportingView />)

    const row = (await screen.findByText('Compliance Evidence Report')).closest(
      'tr',
    )!
    await user.click(within(row).getByRole('button', { name: /generate/i }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: /download/i }))

    await waitFor(() =>
      expect(fetchReportMock).toHaveBeenCalledWith(
        'compliance-evidence',
        expect.objectContaining({ format: 'html' }),
      ),
    )
    expect(downloadBlobMock).toHaveBeenCalled()
  })

  it('shows the enterprise notice when scheduling is not wired (501)', async () => {
    api.listSchedules.mockRejectedValue(
      new ApiError(501, 'not_implemented', 'not wired'),
    )
    wrap(<ReportingView />)
    expect(
      await screen.findByText(/enterprise capability/i),
    ).toBeInTheDocument()
  })

  it('binds schedule reads and deletes to the active tenant', async () => {
    api.listSchedules.mockResolvedValue({ items: [schedule] })
    const user = userEvent.setup()
    wrap(<ReportingView />)

    const cron = await screen.findByText('0 8 * * 1')
    const row = cron.closest('tr')!
    expect(api.listSchedules).toHaveBeenCalledWith({ tenant: 'tenant-A' })

    await user.click(
      within(row).getByRole('button', { name: /delete schedule/i }),
    )
    await waitFor(() =>
      expect(api.deleteSchedule).toHaveBeenCalledWith('schedule-1', {
        tenant: 'tenant-A',
      }),
    )

    const refreshedRow = (await screen.findByText('0 8 * * 1')).closest('tr')!
    await user.click(
      within(refreshedRow).getByRole('button', { name: /history/i }),
    )
    await waitFor(() =>
      expect(api.scheduleRuns).toHaveBeenCalledWith('schedule-1', {
        tenant: 'tenant-A',
      }),
    )
  })

  /**
   * ⛔ LA MISMA PROPIEDAD, PERO CON UN 501 DE VERDAD, y existe por un hallazgo del recorrido con
   * navegador de the integrator: contra un motor community real, `GET /schedules` contestó 501 y
   * el informe decía que el operador no veía nada.
   *
   * La casilla de arriba inyecta un `ApiError` CONSTRUIDO A MANO, así que sólo prueba que el
   * componente reacciona a un objeto con `status: 501` — no que el cliente HTTP real produzca ese
   * objeto a partir de una respuesta 501 del cable. Esa distancia es la trampa de «verde porque el
   * doble no puede lo que producción sí», y es justo donde una discrepancia entre lo medido en
   * jsdom y lo visto en Chromium se escondería.
   *
   * Aquí el cliente REAL recorre su camino entero —`fetch` sustituido, respuesta 501 con envoltura
   * de error del motor, `parseErrorEnvelope`, `ApiError`— y la pantalla tiene que decirlo igual.
   */
  it('un 501 REAL del cable, por el cliente de verdad, también se dice', async () => {
    const { reportingApi: real } =
      await vi.importActual<typeof import('./api')>('./api')
    vi.stubGlobal('fetch', () =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            error: {
              code: 'not_implemented',
              message: 'scheduling is an add-on',
            },
          }),
          { status: 501, headers: { 'Content-Type': 'application/json' } },
        ),
      ),
    )
    // El cliente real, no el doble: si su 501 no llegara como ApiError(501), esto rompe.
    api.listSchedules.mockImplementation(() =>
      real.listSchedules({ tenant: 'tenant-A' }),
    )
    wrap(<ReportingView />)
    expect(
      await screen.findByText(/enterprise capability/i),
    ).toBeInTheDocument()
    vi.unstubAllGlobals()
  })

  //E6: the schedule selector is server-driven — it offers exactly what
  // GET /reports publishes instead of a re-coded list, so a catalog change can
  // never drift from the console.
  it('builds the schedule form from the server catalog', async () => {
    api.listReports.mockResolvedValue({
      items: [
        {
          type: 'custom-posture',
          title: 'Custom Posture Report',
          description: 'A report type the console has never heard of.',
          formats: ['html'],
        },
      ],
    })
    api.createSchedule.mockResolvedValue({ items: [] })

    const user = userEvent.setup()
    wrap(<ReportingView />)

    await user.click(
      await screen.findByRole('button', { name: /new schedule/i }),
    )
    const dialog = await screen.findByRole('dialog')

    // The server-published title is on offer (not a hardcoded slug list) and
    // the format defaults to the report's only offered format.
    expect(
      within(dialog).getByRole('combobox', { name: /report/i }),
    ).toHaveTextContent('Custom Posture Report')

    await user.click(within(dialog).getByRole('button', { name: /^create$/i }))
    await waitFor(() =>
      expect(api.createSchedule).toHaveBeenCalledWith(
        expect.objectContaining({
          report_type: 'custom-posture',
          format: 'html',
        }),
        { tenant: 'tenant-A' },
      ),
    )
  })

  it('falls back honestly when the catalog fetch fails', async () => {
    api.listReports.mockRejectedValue(
      new ApiError(500, 'internal', 'catalog down'),
    )
    api.listSchedules.mockResolvedValue({ items: [] })

    const user = userEvent.setup()
    wrap(<ReportingView />)

    await user.click(
      await screen.findByRole('button', { name: /new schedule/i }),
    )
    const dialog = await screen.findByRole('dialog')

    // No invented report types: the form shows an error with retry and the
    // submit stays disabled.
    expect(
      within(dialog).queryByRole('combobox', { name: /report/i }),
    ).not.toBeInTheDocument()
    expect(
      within(dialog).getByRole('button', { name: /^create$/i }),
    ).toBeDisabled()
    expect(api.createSchedule).not.toHaveBeenCalled()
  })
})
