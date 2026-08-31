// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// C07-04 — la cadena de custodia de una supresión: el expediente que dice POR QUÉ, no sólo en qué
// acabó. Y la diferencia entre un evento que se puede demostrar y uno que sólo está registrado.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent } from '@/test/intel'
import '@/features/_intel'

const api = {
  erasures: vi.fn(),
  erasureEvents: vi.fn(),
  erasure: vi.fn(),
  erasureReceipt: vi.fn(),
  executeErasure: vi.fn(),
  createErasure: vi.fn(),
  dataSubjectErasureStatus: vi.fn(),
}

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
  Toaster: () => null,
}))
vi.mock('./api', async () => {
  const actual = await vi.importActual<typeof import('./api')>('./api')
  return { ...actual, complianceApi: api }
})

const { ErasureTab } = await import('./erasure-view')
await import('./i18n')

const SOLICITUD = {
  id: 'er-1',
  subject_kind: 'user',
  subject_token: 'tok-1',
  subject: 'u-7',
  data_classes: ['session_transcript'],
  case_ref: 'DSAR-9',
  reason: 'Art. 17',
  requested_by: 'dpo@example.com',
  status: 'blocked_hold' as const,
  created_at: '2026-08-01T10:00:00Z',
}

const ANCLADO = {
  id: 'ev-1',
  event: 'hold_blocked',
  actor: 'dpo@example.com',
  actor_kind: 'user',
  ledger_seq: 42,
  ledger_hash: 'abcdef0123456789aa',
  occurred_at: '2026-08-01T10:05:00Z',
}

async function abrirCustodia(user: ReturnType<typeof userEvent.setup>) {
  renderIntel(<ErasureTab canAdmin canRead />)
  await user.click(await screen.findByRole('button', { name: /Custody/i }))
}

beforeEach(() => {
  api.erasures.mockReset().mockResolvedValue({ items: [SOLICITUD] })
  api.erasureEvents.mockReset().mockResolvedValue({ items: [ANCLADO] })
})

describe('la cadena de custodia de una supresión', () => {
  it('pide los eventos de ESA solicitud', async () => {
    const user = userEvent.setup()
    await abrirCustodia(user)
    expect(api.erasureEvents).toHaveBeenCalledWith('er-1')
  })

  /**
   * ⛔ EL CONTROL QUE MÁS IMPORTA, y es de EVIDENCIA: `appendErasureEvent`
   * (`erasure.go:185-213`) sella cada evento contra la cabeza del ledger **sólo si la hay** —
   * `head, ok := sc.Audit().Head(ctx)`; si `ok` es falso quedan `seq: 0` y `hash: ""`, y
   * `ledger_hash` es `omitempty`, así que ni viaja.
   *
   * ⇒ Un evento **anclado** se puede atar a la cadena firmada: es prueba. Uno **sin anclar**
   * existe igual y **no se puede demostrar**: es una afirmación.
   *
   * EL MUTANTE: pintar las dos filas igual. El expediente parecería demostrado sin serlo — y es
   * el documento que se le enseña a un regulador para justificar que una supresión no se hizo.
   */
  it('un evento SIN ancla se marca como no demostrable', async () => {
    api.erasureEvents.mockResolvedValue({
      items: [{ ...ANCLADO, ledger_seq: 0, ledger_hash: undefined }],
    })
    const user = userEvent.setup()
    await abrirCustodia(user)
    expect(
      await screen.findByText(/NOT anchored to the ledger/i),
    ).toBeInTheDocument()
  })

  /**
   * LA DIRECCIÓN QUE NO DEBE DISPARAR: con ancla se enseña la secuencia y el hash, y NO se avisa.
   * Sin esta casilla, una pantalla que gritara «sin anclar» en todas las filas pasaría la de
   * arriba y desacreditaría un expediente que sí está sellado.
   */
  it('un evento anclado enseña su secuencia y no avisa', async () => {
    const user = userEvent.setup()
    await abrirCustodia(user)
    expect(await screen.findByText(/ledger #42/i)).toBeInTheDocument()
    expect(screen.queryByText(/NOT anchored to the ledger/i)).toBeNull()
  })

  /**
   * El quórum de dos (`erasure.go:61`) se NOMBRA. «Aprobado» sin decir quién pierde la evidencia
   * de que fueron dos personas, que es lo único que distingue una autorización de una firma sola.
   */
  it('nombra a los aprobadores del quórum', async () => {
    api.erasureEvents.mockResolvedValue({
      items: [
        {
          ...ANCLADO,
          event: 'approval_requested',
          approvers: ['dpo@example.com', 'ciso@example.com'],
        },
      ],
    })
    const user = userEvent.setup()
    await abrirCustodia(user)
    expect(
      await screen.findByText(/dpo@example.com, ciso@example.com/i),
    ).toBeInTheDocument()
  })
})
