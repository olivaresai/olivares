// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// ⛔ EL ÚNICO SITIO DE LECTURA DE ESTE GRUPO, y por eso tiene celda propia: los otros trece
// son escrituras y los cubre la guarda de clase (`reporting-policy.test.ts`) más la celda de
// comportamiento de `datasets`. Aquí el defecto se ve distinto: `VersionEvidence` existe para
// ENSEÑAR el veredicto de admisión de una versión, y leyendo `isForbidden` primero —que es
// SÓLO el status 403 (lib/api/errors.ts:59)— un `step_up_required` lo tapaba con un «no
// autorizado» falso, sobre un permiso que el operador SÍ tiene y sin ofrecerle salida.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'

const api = vi.hoisted(() => ({ modelAdmissions: vi.fn() }))
vi.mock('@/features/models/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/models/api')>()
  return { ...actual, modelsApi: { ...actual.modelsApi, ...api } }
})
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

// ⛔ SE DOBLA EL PANEL INTERNO, NO EL ENVOLTORIO. Doblando `StepUpRequiredState` la celda no
// vería que el envoltorio real deja de anunciarse ni que `onElevated` llega a alguna parte;
// el contraste `sol max` demostró esa ceguera sobre cuatro celdas mías de esta campaña.
vi.mock('@/features/identity/assurance', () => ({
  StepUpPanel: ({
    action,
    onElevated,
  }: {
    minAal: number
    currentAal: number
    action: string
    className?: string
    onElevated?: () => void
  }) => (
    <div>
      <span>step-up ceremony</span>
      <span>{`action:${action}`}</span>
      <button type="button" onClick={() => onElevated?.()}>
        elevar
      </button>
    </div>
  ),
}))

import { VersionEvidence } from './shared'
import './i18n'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  api.modelAdmissions.mockReset()
})

describe('VersionEvidence — los dos 403 no son el mismo', () => {
  it('un step_up_required ofrece la CEREMONIA en vez de esconder el veredicto', async () => {
    api.modelAdmissions.mockRejectedValue(
      new ApiError(403, 'step_up_required', 'assurance level too low'),
    )
    wrap(<VersionEvidence versionRef="v1" />)

    expect(await screen.findByText('step-up ceremony')).toBeInTheDocument()
    expect(screen.getByText('action:generic')).toBeInTheDocument()
    // Y la acusación de rol NO acompaña a la ceremonia: «Not authorized» es la copy exacta de
    // ForbiddenState (lib/i18n/locales/en/errors.json:8), no un rol genérico que cualquier
    // estado satisface. Anclado a lo positivo, que ya está en pantalla.
    expect(screen.queryByText('Not authorized')).not.toBeInTheDocument()
  })

  it('y elevar REINTENTA la lectura refusada, que es la única salida', async () => {
    api.modelAdmissions.mockRejectedValue(
      new ApiError(403, 'step_up_required', 'assurance level too low'),
    )
    wrap(<VersionEvidence versionRef="v1" />)
    await screen.findByText('step-up ceremony')

    api.modelAdmissions.mockResolvedValue({ items: [] })
    await userEvent.click(screen.getByRole('button', { name: /elevar/i }))

    // Dos llamadas: la refusada y el reintento. Sin esto, `onElevated={undefined}` en el
    // sitio productivo dejaría la celda verde con una ceremonia que no lleva a ninguna parte.
    await vi.waitFor(() => expect(api.modelAdmissions).toHaveBeenCalledTimes(2))
  })

  it('una lectura FALLIDA no se pinta como «no hay evidencia»', async () => {
    // ⛔ Preexistente y cazado por el contraste: un 500 o una caída de red caían al estado
    // vacío y el panel afirmaba «no admission attempt recorded», que es una afirmación sobre
    // el MUNDO. Lo único cierto es que no se pudo mirar. En un panel de evidencia de admisión
    // esa confusión dice que no hubo intento donde igual lo hubo y fue DENEGADO.
    api.modelAdmissions.mockRejectedValue(new ApiError(500, 'internal', 'boom'))
    wrap(<VersionEvidence versionRef="v1" />)

    expect(await screen.findByRole('alert')).toBeInTheDocument()
    expect(
      screen.queryByText(/no admission attempt recorded/i),
    ).not.toBeInTheDocument()
  })

  it('un 403 SIN código de ceremonia conserva la negativa de ROL', async () => {
    // Control negativo, y es la mitad que protege el cambio: la negativa de rol es CIERTA y
    // se queda. Sin esta celda, mandar los DOS 403 a la ceremonia también pasaría.
    api.modelAdmissions.mockRejectedValue(new ApiError(403, 'forbidden', 'no'))
    wrap(<VersionEvidence versionRef="v1" />)

    expect(await screen.findByText('Not authorized')).toBeInTheDocument()
    expect(screen.queryByText('step-up ceremony')).not.toBeInTheDocument()
  })
})
