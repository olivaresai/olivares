// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'

const http = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  patch: vi.fn(),
}))

vi.mock('@/lib/api/client', () => ({ http }))

import { orchestrationApi, orchestrationKeys } from './api'
import { scheduleFormSchema } from './orchestration-view'

beforeEach(() => {
  vi.clearAllMocks()
  http.post.mockResolvedValue({})
  http.patch.mockResolvedValue({})
})

describe('el ledger del estate pide el modo de orden EXPLÍCITO', () => {
  it('manda `order=newest` además del límite', async () => {
    http.get.mockResolvedValue({ items: [], has_more: false })
    await orchestrationApi.decisions({ limit: 1000 })
    // ⛔ FIRES IF: alguien quita `order: 'newest'`. El motor deja su default cronológico
    //    y paginable a propósito —el store no emite cursor para un Sort personalizado—,
    //    así que sin este parámetro la pantalla enseñaría el tramo MÁS ANTIGUO del
    //    ledger y su aviso de recorte estaría hablando de otra cosa.
    //
    //    Este caso vive AQUÍ y no en el de la pantalla: allí `orchestrationApi.decisions`
    //    está mockeado entero, así que el objeto `query` de este fichero no se ejecuta
    //    nunca. Medido: el mutante que quitaba `order` ESCAPABA a la batería de la vista.
    expect(http.get).toHaveBeenCalledWith('/v1/m/orchestration/decisions', {
      query: { order: 'newest', limit: 1000 },
    })
  })

  it('la ruta POR SCHEDULE no lo manda: su cursor sigue siendo la forma de recorrerla', async () => {
    http.get.mockResolvedValue({ items: [], has_more: false })
    await orchestrationApi.scheduleDecisions('sched-1')
    expect(http.get).toHaveBeenCalledWith(
      '/v1/m/orchestration/schedules/sched-1/decisions',
    )
  })
})

describe('orchestration schedule API wrappers', () => {
  it('creates a schedule without sending desired_status', async () => {
    const body = {
      name: 'Nightly batch',
      subject_kind: 'agent' as const,
      subject_ref: 'agent:batch',
      trigger_kind: 'cron' as const,
      cadence_spec: 'opaque cadence text',
      expected_interval_seconds: 3_600,
      grace_factor: 2,
    }

    await orchestrationApi.createSchedule(body)

    expect(http.post).toHaveBeenCalledWith(
      '/v1/m/orchestration/schedules',
      body,
    )
    expect(http.post.mock.calls[0][1]).not.toHaveProperty('desired_status')
  })

  it('patches only mutable schedule fields', async () => {
    const body = {
      subject_ref: 'swarm:ops',
      cadence_spec: 'agent.completed',
      expected_interval_seconds: 0,
      grace_factor: 3,
      desired_status: 'paused' as const,
    }

    await orchestrationApi.updateSchedule('sched-1', body)

    expect(http.patch).toHaveBeenCalledWith(
      '/v1/m/orchestration/schedules/sched-1',
      body,
    )
    expect(http.patch.mock.calls[0][1]).not.toHaveProperty('name')
    expect(http.patch.mock.calls[0][1]).not.toHaveProperty('subject_kind')
    expect(http.patch.mock.calls[0][1]).not.toHaveProperty('trigger_kind')
  })

  it('uses the same fire wrapper for approval request and execution phases', async () => {
    await orchestrationApi.fireSchedule('sched-1')
    await orchestrationApi.fireSchedule('sched-1', {
      approval_ref: 'approval-42',
    })

    expect(http.post).toHaveBeenNthCalledWith(
      1,
      '/v1/m/orchestration/schedules/sched-1/fire',
      undefined,
    )
    expect(http.post).toHaveBeenNthCalledWith(
      2,
      '/v1/m/orchestration/schedules/sched-1/fire',
      { approval_ref: 'approval-42' },
    )
  })

  it('keeps schedule authoring keys tenant-scoped', () => {
    expect(orchestrationKeys.schedules('tenant-a')).toEqual([
      'orchestration',
      'tenant-a',
      'schedules',
    ])
    expect(orchestrationKeys.schedule('tenant-b', 'sched-1')).toEqual([
      'orchestration',
      'tenant-b',
      'schedules',
      'sched-1',
    ])
  })

  it('lists schedule revisions with the keyset cursor', async () => {
    await orchestrationApi.scheduleRevisions('sched-1', {
      cursor: 'rev-cursor',
      limit: 50,
    })
    expect(http.get).toHaveBeenCalledWith(
      '/v1/m/orchestration/schedules/sched-1/revisions',
      { query: { cursor: 'rev-cursor', limit: 50 } },
    )
  })

  it('posts the selected schedule revision for restore', async () => {
    await orchestrationApi.restoreSchedule('sched-1', 'rev-2')
    expect(http.post).toHaveBeenCalledWith(
      '/v1/m/orchestration/schedules/sched-1/restore',
      { revision_id: 'rev-2' },
    )
  })
})

const cronInput = {
  name: 'Nightly batch',
  subject_kind: 'agent' as const,
  subject_ref: 'agent:batch',
  trigger_kind: 'cron' as const,
  cadence_spec: 'opaque cadence text',
  expected_interval_seconds: 60,
  grace_factor: 2,
}

describe('scheduleFormSchema', () => {
  it('accepts opaque cadence text without parsing cron syntax', () => {
    expect(scheduleFormSchema.safeParse(cronInput).success).toBe(true)
  })

  it('requires name and subject_ref', () => {
    expect(
      scheduleFormSchema.safeParse({ ...cronInput, name: ' ' }).success,
    ).toBe(false)
    expect(
      scheduleFormSchema.safeParse({ ...cronInput, subject_ref: '' }).success,
    ).toBe(false)
  })

  it('requires cadence_spec for cron and event, but not manual', () => {
    expect(
      scheduleFormSchema.safeParse({ ...cronInput, cadence_spec: '' }).success,
    ).toBe(false)
    expect(
      scheduleFormSchema.safeParse({
        ...cronInput,
        trigger_kind: 'event',
        cadence_spec: '',
        expected_interval_seconds: 0,
      }).success,
    ).toBe(false)
    expect(
      scheduleFormSchema.safeParse({
        ...cronInput,
        trigger_kind: 'manual',
        cadence_spec: '',
        expected_interval_seconds: 0,
      }).success,
    ).toBe(true)
  })

  it('enforces the cron interval bounds and the backend non-cron zero sentinel', () => {
    expect(
      scheduleFormSchema.safeParse({
        ...cronInput,
        expected_interval_seconds: 59,
      }).success,
    ).toBe(false)
    expect(
      scheduleFormSchema.safeParse({
        ...cronInput,
        expected_interval_seconds: 31_622_400,
      }).success,
    ).toBe(true)
    expect(
      scheduleFormSchema.safeParse({
        ...cronInput,
        expected_interval_seconds: 31_622_401,
      }).success,
    ).toBe(false)
    expect(
      scheduleFormSchema.safeParse({
        ...cronInput,
        trigger_kind: 'event',
        expected_interval_seconds: 60,
      }).success,
    ).toBe(false)
  })

  it('enforces grace_factor bounds and defaults it to 2', () => {
    expect(
      scheduleFormSchema.safeParse({ ...cronInput, grace_factor: 0 }).success,
    ).toBe(false)
    expect(
      scheduleFormSchema.safeParse({ ...cronInput, grace_factor: 10 }).success,
    ).toBe(true)
    expect(
      scheduleFormSchema.safeParse({ ...cronInput, grace_factor: 11 }).success,
    ).toBe(false)

    const { grace_factor: _grace, ...withoutGrace } = cronInput
    const parsed = scheduleFormSchema.parse(withoutGrace)
    expect(parsed.grace_factor).toBe(2)
  })
})
