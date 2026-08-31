// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-04 — la mitad de PANTALLA de los tres informes enterprise.
//
// Uno de los tres es el paquete de evidencia de auditoría FIRMADO: lo que se le entrega a un
// auditor externo. Lo servía `modules/reporting/enterprise.go:89-91` y no había forma de pedirlo
// desde la consola.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
// ⛔ EL ORDEN DE LOS ARGUMENTOS IMPORTA Y ME MORDIÓ: `ApiError` es `(status, code, message)`
// (`web/src/lib/api/errors.ts:32-38`). Mi primera versión escribía `new ApiError('not wired', 501)`
// y construía un error con `status: 'not wired'` — o sea, **un doble que no podía producir el 501
// que produce el motor**. La casilla fallaba por la fixture, no por la vista.
import { ApiError } from '@/lib/api/errors'
import './i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

const toastSpy = {
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
  info: vi.fn(),
}
vi.mock('@/components/ui/toaster', () => ({
  toast: toastSpy,
  Toaster: () => null,
}))

const bundleMock = vi.fn()
const postureMock = vi.fn()
const riskMock = vi.fn()
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  const lista = () => Promise.resolve({ items: [] })
  return {
    ...actual,
    reportingApi: {
      ...actual.reportingApi,
      enterpriseBundle: (...a: unknown[]) => bundleMock(...a),
      enterprisePosture: (...a: unknown[]) => postureMock(...a),
      enterpriseRisk: (...a: unknown[]) => riskMock(...a),
      listReports: lista,
      listSchedules: lista,
    },
  }
})

const { ReportingView } = await import('./reporting-view')

function montar() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <ReportingView />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  toastSpy.success.mockReset()
  toastSpy.error.mockReset()
  bundleMock.mockReset().mockResolvedValue({ signed: true })
  postureMock.mockReset().mockResolvedValue({})
  riskMock.mockReset().mockResolvedValue({})
  // jsdom no implementa createObjectURL: la descarga es un efecto, no lo que se mide aquí.
  URL.createObjectURL = vi.fn(() => 'blob:x')
  URL.revokeObjectURL = vi.fn()
})

describe('los tres informes enterprise', () => {
  /**
   * EL CONTROL: el paquete de evidencia se puede PEDIR desde la consola. Antes de esto el
   * cliente existía, tenía prueba de contrato, y ningún botón lo llamaba.
   */
  it('el paquete de evidencia firmado se pide desde la pantalla', async () => {
    const user = userEvent.setup()
    montar()
    // Exacto y sensible a mayúsculas: la descripción del panel también dice «evidence bundle»,
    // y un regex laxo la encontraría ahí — pulsando el botón de otra fila.
    const fila = (await screen.findByText('Evidence bundle')).closest('div')
      ?.parentElement as HTMLElement
    await user.click(
      await within(fila).findByRole('button', { name: /Request/i }),
    )
    await waitFor(() => expect(bundleMock).toHaveBeenCalled())
  })

  /**
   * ⛔ EL CONTROL QUE DE VERDAD IMPORTA: un **501** es la frontera comercial, no una avería, y se
   * dice con su nombre. El motor lo explica en `enterprise.go:141-148`: un add-on caducado
   * llegaba como 500 «failed to build the report», así que a un cliente que había pagado todo
   * menos ese add-on **se le decía que el servidor estaba roto**.
   *
   * EL MUTANTE: tratar el 501 como cualquier otro error. Sale el toast rojo genérico en vez del
   * texto que explica que eso vive en un complemento — y la consola enseña a desconfiar de los
   * errores de verdad.
   */
  it('un 501 se explica como frontera, sin toast de error', async () => {
    bundleMock.mockRejectedValue(
      new ApiError(501, 'not_implemented', 'not wired'),
    )
    const user = userEvent.setup()
    montar()
    // Exacto y sensible a mayúsculas: la descripción del panel también dice «evidence bundle»,
    // y un regex laxo la encontraría ahí — pulsando el botón de otra fila.
    const fila = (await screen.findByText('Evidence bundle')).closest('div')
      ?.parentElement as HTMLElement
    await user.click(
      await within(fila).findByRole('button', { name: /Request/i }),
    )
    expect(
      await screen.findByText(/lives in an add-on that is not linked/i),
    ).toBeInTheDocument()
    expect(toastSpy.error).not.toHaveBeenCalled()
  })

  /**
   * LA DIRECCIÓN QUE NO DEBE DISPARAR: un fallo de VERDAD sí se pinta como error. Sin esta
   * afirmación, tratar TODO como frontera pasaría la casilla de arriba y la consola se tragaría
   * las averías en silencio.
   */
  it('un 500 de verdad sí sale como error', async () => {
    bundleMock.mockRejectedValue(new ApiError(500, 'internal', 'boom'))
    const user = userEvent.setup()
    montar()
    // Exacto y sensible a mayúsculas: la descripción del panel también dice «evidence bundle»,
    // y un regex laxo la encontraría ahí — pulsando el botón de otra fila.
    const fila = (await screen.findByText('Evidence bundle')).closest('div')
      ?.parentElement as HTMLElement
    await user.click(
      await within(fila).findByRole('button', { name: /Request/i }),
    )
    await waitFor(() => expect(toastSpy.error).toHaveBeenCalled())
    expect(
      screen.queryByText(/lives in an add-on that is not linked/i),
    ).toBeNull()
  })
})
