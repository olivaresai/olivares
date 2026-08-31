// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// las invariantes honestas de la superficie de una sesión de voz.
//
// El open gobernado tiene CINCO desenlaces y los tres del medio son los que una consola
// normal borra: una decisión de política dibujada como fallo, un hueco de despliegue
// dibujado como denegación, y un «no se pudo mirar» dibujado como «no». Cada celda mata
// una mutación concreta.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import type { VoiceOpenInput } from './types'

const api = {
  decisions: vi.fn(),
  open: vi.fn(),
  sessions: vi.fn(),
  policies: vi.fn(),
}
const perm = { admin: true }
const stream = {
  status: 'closed' as string,
  enabled: undefined as boolean | undefined,
}

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (p: string) => (p === 'voice:session:admin' ? perm.admin : true),
  }),
}))
vi.mock('@/features/shared/sse', () => ({
  useLiveStream: (o: { enabled?: boolean }) => {
    stream.enabled = o.enabled
    return { status: stream.status }
  },
}))
vi.mock('./api', async () => {
  const actual = await vi.importActual<typeof import('./api')>('./api')
  return { ...actual, voiceApi: { ...actual.voiceApi, ...api } }
})

const { VoiceSessionSurface, GovernedOpen, classifyOpen } =
  await import('./session-surface')
const { VoiceView } = await import('./voice-view')
await import('./i18n')

function wrap(node: ReactNode) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(<QueryClientProvider client={qc}>{node}</QueryClientProvider>)
}

const INPUT: VoiceOpenInput = {
  session_ref: 's1',
  agent_ref: 'a1',
  model_ref: 'm1',
  provider_ref: 'p1',
}

beforeEach(() => {
  api.decisions.mockReset()
  api.open.mockReset()
  perm.admin = true
  stream.status = 'closed'
  stream.enabled = undefined
})

describe('el open gobernado tiene CINCO desenlaces, no dos', () => {
  it('un 403 con veredicto es una DECISIÓN de política, no un fallo', () => {
    const err = new ApiError(
      403,
      'forbidden',
      'forbidden',
      undefined,
      undefined,
      {
        op_status: 'blocked',
        policy_verdict: 'denied',
        detail: 'default-deny',
      },
    )
    expect(classifyOpen(null, err)).toBe('denied')
  })

  it('`no_gate` NO se colapsa en «denegado» — es un hueco del despliegue', () => {
    // Y llega junto a `denied`: el motor deniega Y no tiene puerta. Decir sólo
    // «denegado» esconde que en este despliegue no hay nada que aprobar.
    const err = new ApiError(
      403,
      'forbidden',
      'forbidden',
      undefined,
      undefined,
      {
        policy_verdict: 'denied',
        gate_status: 'no_gate',
      },
    )
    expect(classifyOpen(null, err)).toBe('noGate')
  })

  it('un 502 es «no se pudo mirar», no una denegación', () => {
    const err = new ApiError(
      502,
      'bad_gateway',
      'bad gateway',
      undefined,
      undefined,
      {
        error: 'approval gate unavailable',
      },
    )
    expect(classifyOpen(null, err)).toBe('unavailable')
  })

  it('un 202 con approval_ref es la SEGUNDA FASE, no un éxito', () => {
    expect(
      classifyOpen(
        {
          op: 'open_request',
          op_status: 'requested',
          policy_verdict: 'allowed',
          approval_ref: 'ap-1',
        },
        undefined,
      ),
    ).toBe('approval')
  })

  it('sólo un dispatch_ref significa abierta', () => {
    expect(
      classifyOpen(
        {
          op: 'open',
          op_status: 'dispatched',
          policy_verdict: 'allowed',
          dispatch_ref: 'd-1',
        },
        undefined,
      ),
    ).toBe('opened')
  })
})

describe('el open es admin y el literal es el del motor', () => {
  it('sin `voice:session:admin` el botón NO se dibuja', async () => {
    perm.admin = false
    const { container } = wrap(<GovernedOpen input={INPUT} />)
    await waitFor(() => expect(container.textContent).toBe(''))
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('con el permiso, pulsarlo envía el cuerpo y dibuja el desenlace', async () => {
    api.open.mockRejectedValue(
      new ApiError(403, 'forbidden', 'forbidden', undefined, undefined, {
        policy_verdict: 'denied',
        detail: 'default-deny',
      }),
    )
    wrap(<GovernedOpen input={INPUT} />)
    await userEvent.click(
      await screen.findByRole('button', { name: /open session/i }),
    )

    const status = await screen.findByRole('status')
    expect(status.textContent).toMatch(/default-deny decision, not a failure/i)
    expect(api.open).toHaveBeenCalledWith(INPUT)
  })
})

describe('la superficie viva no finge estar viva', () => {
  it('sin sesión no abre el flujo ni pide decisiones', async () => {
    wrap(<VoiceSessionSurface sessionRef={null} />)

    expect(await screen.findByTestId('voice-surface-idle')).toBeInTheDocument()
    expect(stream.enabled).toBe(false)
    expect(api.decisions).not.toHaveBeenCalled()
  })

  it('dibuja el estado REAL de la conexión, no un verde fijo', async () => {
    stream.status = 'error'
    api.decisions.mockResolvedValue({ items: [] })
    wrap(<VoiceSessionSurface sessionRef="s1" />)

    const badge = await screen.findByText('stream failed')
    expect(screen.queryByText('live')).toBeNull()
    // ⛔ Y EL COLOR TAMBIEN, que es lo que se ve antes de leer: un «stream failed» pintado
    //    en verde sigue mintiendo. Sin esta linea, un mutante que fija la variante a
    //    `success` pasaba las diez celdas — medido.
    expect(badge.className).toMatch(/text-danger/)
  })

  it('una decisión sin motivo lo DICE, no se calla', async () => {
    api.decisions.mockResolvedValue({
      items: [
        {
          id: 'd1',
          session_ref: 's1',
          agent_ref: 'a1',
          requested_model_ref: 'gpt',
          requested_provider_ref: 'openai',
          op: 'open_request',
          policy_verdict: 'denied',
          gate_status: 'no_gate',
          op_status: 'blocked',
          actor: 'u1',
          actor_kind: 'user',
          occurred_at: '2026-08-20T10:00:00Z',
        },
      ],
    })
    wrap(<VoiceSessionSurface sessionRef="s1" />)

    expect(await screen.findByText(/no reason recorded/i)).toBeInTheDocument()
  })
})

// --- el cableado ---------------------------------------------------------------
//
// Un componente que nadie renderiza pasa TODAS sus celdas. Y un `grep` que compruebe
// que el fichero lo MENCIONA no ve la capa de render: esta celda empieza en la vista
// madre, pulsa una fila y comprueba que la superficie aparece de verdad.

describe('la superficie se alcanza desde la vista de voz', () => {
  it('al pulsar una sesión, deja de estar ociosa y pide SUS decisiones', async () => {
    api.sessions.mockResolvedValue({
      items: [
        {
          id: 'v1',
          session_ref: 's-42',
          agent_ref: 'a1',
          model_ref: 'm1',
          provider_ref: 'p1',
          principal_ref: 'u1',
          policy_ref: 'pol1',
          language_code: 'es',
          state: 'open',
        },
      ],
    })
    api.policies.mockResolvedValue({ items: [] })
    api.decisions.mockResolvedValue({ items: [] })
    wrap(<VoiceView />)

    // Ancla POSITIVA: la vista arranca ociosa porque no hay sesión elegida.
    expect(await screen.findByTestId('voice-surface-idle')).toBeInTheDocument()
    expect(api.decisions).not.toHaveBeenCalled()

    // La tabla no pinta el session_ref: se pulsa por una columna que SI muestra.
    await userEvent.click(await screen.findByText('a1'))
    await waitFor(() => expect(api.decisions).toHaveBeenCalledWith('s-42'))
  })
})
