// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// ⛔ LA CEREMONIA SOBREVIVE A QUIEN LA PIDIÓ, Y ESO NO PUEDE REANUDAR UNA ACCIÓN ABANDONADA.
//
// Entre pulsar y resolver hay tres esperas —opciones por red, `navigator.credentials.get` con el
// gesto del usuario, y la verificación más el `whoami`—. En ese hueco el componente que pidió la
// elevación puede desmontarse (se cierra el diálogo, se navega) y `StepUpPanel` llamaba
// `onElevated?.()` **incondicionalmente** al volver.
//
// Y no es inocuo por estar desmontado: el contraste Codex `sol max` lo reprodujo contra
// query-core 5.101.4 —`destroy()` sólo retira listeners y `refetch()` sigue llegando a
// `Query.fetch`— con el resultado `query_fn_calls_after_observer_destroy=1`. **La petición sale.**
//
// Se arregla en la RAÍZ (`assurance.tsx`) y no en los seis llamantes: este panel es el único
// punto por el que pasan todos, y parchear las hojas dejaría fuera a cualquier consumidor futuro.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const authState = vi.hoisted(() => ({
  principal: { aal: 1, amr: ['pwd'] } as { aal: number; amr: string[] },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

// La ceremonia se resuelve cuando ESTA promesa se resuelva: así el test controla exactamente el
// hueco en el que el operador cierra el diálogo.
const puerta = vi.hoisted(() => ({
  resolver: null as null | ((v: unknown) => void),
}))
const api = vi.hoisted(() => ({
  webauthnAuthOptions: vi.fn(),
  webauthnAuthenticate: vi.fn(),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, identityApi: { ...(real.identityApi as object), ...api } }
})

// ⛔ SE DOBLA EL MÓDULO DE WEBAUTHN, y se dice por qué: `isWebAuthnSupported()` exige
// `window.PublicKeyCredential`, que jsdom no tiene, así que sin esto el panel sale por
// «unsupported» y la celda mediría eso en vez del desmontaje. El sujeto aquí es QUIÉN reanuda
// tras la ceremonia, no la codificación de la credencial — eso lo cubren las celdas de
// `webauthn.ts`. La llamada al navegador se mantiene real (`credentials.get`), porque es una de
// las tres esperas que abren la ventana que estoy midiendo.
vi.mock('./webauthn', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return {
    ...real,
    isWebAuthnSupported: () => true,
    decodeRequestOptions: (pk: unknown) => pk,
    encodeAssertion: () => ({ id: 'c1' }),
  }
})

vi.stubGlobal('navigator', {
  ...globalThis.navigator,
  credentials: { get: vi.fn(async () => ({ id: 'c1' })) },
})

import { AAL, StepUpPanel } from './assurance'

function Anfitrion({ onElevated }: { onElevated: () => void }) {
  const [montado, setMontado] = useState(true)
  return (
    <>
      {montado && (
        <StepUpPanel
          minAal={AAL.HARDWARE}
          currentAal={AAL.PASSWORD}
          action="console"
          onElevated={onElevated}
        />
      )}
      <button type="button" onClick={() => setMontado(false)}>
        cerrar
      </button>
    </>
  )
}

const wrap = (ui: React.ReactElement) =>
  render(
    <QueryClientProvider
      client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
    >
      {ui}
    </QueryClientProvider>,
  )

describe('StepUpPanel no reanuda una acción cuyo dueño ya se fue', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    puerta.resolver = null
    api.webauthnAuthOptions.mockResolvedValue({ publicKey: { challenge: 'AA' } })
    api.webauthnAuthenticate.mockImplementation(
      () => new Promise((r) => (puerta.resolver = r)),
    )
  })

  it('si el dueño sigue MONTADO, la elevación reanuda su acción', async () => {
    const user = userEvent.setup()
    const onElevated = vi.fn()
    wrap(<Anfitrion onElevated={onElevated} />)

    await user.click(
      await screen.findByRole('button', { name: /authenticate/i }),
    )
    // Ancla positiva: la ceremonia llegó a pedir la verificación al motor. Sin esto, un fallo
    // temprano dejaría `onElevated` sin llamar por un motivo distinto del que mido.
    await waitFor(() => expect(api.webauthnAuthenticate).toHaveBeenCalled())
    puerta.resolver?.({ ok: true })

    await waitFor(() => expect(onElevated).toHaveBeenCalledTimes(1))
  })

  it('⛔ y si se DESMONTÓ mientras tanto, no reanuda nada', async () => {
    const user = userEvent.setup()
    const onElevated = vi.fn()
    wrap(<Anfitrion onElevated={onElevated} />)

    await user.click(
      await screen.findByRole('button', { name: /authenticate/i }),
    )
    await waitFor(() => expect(api.webauthnAuthenticate).toHaveBeenCalled())

    // El operador cierra el diálogo con la ceremonia a medias.
    await user.click(screen.getByRole('button', { name: 'cerrar' }))

    // …y ahora el motor responde. La elevación OCURRIÓ en el servidor —eso no se deshace— pero
    // la reanudación es del componente que la pidió, y ya no está.
    puerta.resolver?.({ ok: true })

    await new Promise((r) => setTimeout(r, 20))
    expect(onElevated).not.toHaveBeenCalled()
  })
})
