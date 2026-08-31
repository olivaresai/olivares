// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'

const http = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  patch: vi.fn(),
  put: vi.fn(),
}))
vi.mock('@/lib/api/client', () => ({ http }))

import { workflowsApi, workflowsKeys } from './api'
import { EVIDENCE_PAGE } from '../api'

beforeEach(() => vi.clearAllMocks())

describe('workflows API', () => {
  it('replaces the full graph with an encoded workflow id', async () => {
    const steps = [
      {
        ref: 'wait',
        kind: 'wait' as const,
        config: { seconds: 5 },
        depends_on: [],
      },
    ]
    await workflowsApi.updateSteps('workflow/one', steps)
    expect(http.put).toHaveBeenCalledWith(
      '/v1/m/orchestration/workflows/workflow%2Fone/steps',
      { steps },
    )
  })

  it('uses an empty body for phase one and the approval for phase two', async () => {
    await workflowsApi.run('workflow-1')
    await workflowsApi.run('workflow-1', 'approval-1')
    expect(http.post).toHaveBeenNthCalledWith(
      1,
      '/v1/m/orchestration/workflows/workflow-1/run',
      {},
    )
    expect(http.post).toHaveBeenNthCalledWith(
      2,
      '/v1/m/orchestration/workflows/workflow-1/run',
      { approval_ref: 'approval-1' },
    )
  })

  it('scopes every query key by tenant', () => {
    expect(workflowsKeys.list('tenant-a')).toEqual([
      'automations-workflows',
      'tenant-a',
      'list',
    ])
    expect(workflowsKeys.run('tenant-a', 'workflow-1', 'run-1')).toEqual([
      'automations-workflows',
      'tenant-a',
      'detail',
      'workflow-1',
      'runs',
      'run-1',
    ])
  })
})

describe('las dos listas de OPCIONES del editor piden su techo', () => {
  // ⛔ Van a los MISMOS handlers que los raíles de la pestaña de aterrizaje
  // (`modules/orchestration/schedules.go:343` y `modules/notify/route.go:141`, los dos con
  // `listQuery(r)`), así que sin `limit` paginaban a 100. Aquí el recorte no es una cifra mal: es
  // un selector al que le faltan opciones que existen, y el operador no puede elegir lo que no ve.
  it('schedules pide el techo', async () => {
    await workflowsApi.schedules()
    expect(http.get).toHaveBeenCalledWith('/v1/m/orchestration/schedules', {
      query: { limit: EVIDENCE_PAGE },
    })
  })

  it('routes pide el techo', async () => {
    await workflowsApi.routes()
    expect(http.get).toHaveBeenCalledWith('/v1/m/notify/routes', {
      query: { limit: EVIDENCE_PAGE },
    })
  })

  it('un llamante que pida otro techo GANA', async () => {
    await workflowsApi.routes({ limit: 5 })
    expect(http.get).toHaveBeenCalledWith('/v1/m/notify/routes', {
      query: { limit: 5 },
    })
  })
})
