// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-05 — LA CEREMONIA OFRECE PIV/CAC, Y SÓLO CUANDO PUEDE FUNCIONAR.
//
// ⛔ EL HUECO QUE CIERRA. `StepUpPanel` ofrecía ÚNICAMENTE WebAuthn, y PIV/CAC vivía «como ruta
//    paralela en la pestaña de login privilegiado». A quien el motor le exigía elevar y sólo tenía
//    PIV no se le ofrecía ahí: tenía que abandonar la acción, irse a otra pestaña y volver. El
//    motor sirve `POST /v1/auth/piv/elevate` desde (`core/api/server.go:626`) y la consola
//    sólo exponía `piv/status`.
//
// ⭐ Y LA CELDA QUE MÁS IMPORTA ES LA NEGATIVA. El motor eleva con el certificado del apretón de
//    manos TLS (`handlePIVElevate` → `peerCertificates(r)`), y un navegador sólo lo adjunta si el
//    servidor lo pidió al ABRIR la conexión — no se puede adjuntar después. Así que la puerta es
//    `PivStatus.presented`, no «PIV está configurado»: en un despliegue con PIV configurado y sin
//    certificado en ESTA conexión, el botón sería uno que falla siempre. Un botón que no puede
//    funcionar es peor que ningún botón, porque el operador gasta el intento y la confianza.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const authState = vi.hoisted(() => ({ principal: { aal: 1, amr: ['pwd'] } }))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({
  pivStatus: vi.fn(),
  pivElevate: vi.fn(),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, identityApi: { ...(real.identityApi as object), ...api } }
})

import { AAL, StepUpPanel } from './assurance'

const wrap = (onElevated?: () => void) =>
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <StepUpPanel
        minAal={AAL.HARDWARE}
        currentAal={AAL.PASSWORD}
        action="console"
        onElevated={onElevated}
      />
    </QueryClientProvider>,
  )

const boton = () => screen.queryByRole('button', { name: /PIV\/CAC/i })

describe('StepUpPanel ofrece PIV/CAC', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.pivElevate.mockResolvedValue({ ok: true, aal: 3 })
  })

  it('con certificado PRESENTADO ofrece la ruta y eleva por ella', async () => {
    api.pivStatus.mockResolvedValue({ presented: true, subject: 'CN=Ada' })
    const user = userEvent.setup()
    const onElevated = vi.fn()
    wrap(onElevated)

    const b = await screen.findByRole('button', { name: /PIV\/CAC/i })
    await user.click(b)
    await waitFor(() => expect(api.pivElevate).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(onElevated).toHaveBeenCalledTimes(1))
  })

  it('⭐ SIN certificado presentado NO ofrece la ruta', async () => {
    // La afirmación de AUSENCIA se hace después de que la pantalla haya llegado a su estado
    // estable —el botón de WebAuthn ya está—, no en el primer tick: una aserción negativa dentro
    // de un `waitFor` se cumple en cuanto se evalúa y no prueba nada.
    api.pivStatus.mockResolvedValue({ presented: false })
    wrap()
    await screen.findByRole('button', { name: /security key/i })
    await waitFor(() => expect(api.pivStatus).toHaveBeenCalled())
    expect(boton()).toBeNull()
  })

  it('con PIV NO CONFIGURADO (501) tampoco ofrece la ruta', async () => {
    api.pivStatus.mockRejectedValue(
      Object.assign(new Error('piv_not_configured'), {
        status: 501,
        code: 'piv_not_configured',
      }),
    )
    wrap()
    await screen.findByRole('button', { name: /security key/i })
    await waitFor(() => expect(api.pivStatus).toHaveBeenCalled())
    expect(boton()).toBeNull()
  })
})
