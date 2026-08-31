// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'

const http = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  postWithMeta: vi.fn(),
  put: vi.fn(),
  del: vi.fn(),
  patch: vi.fn(),
}))
vi.mock('@/lib/api/client', () => ({ http }))

import { governanceApi } from './api'

beforeEach(() => vi.clearAllMocks())

/** El motor NO devuelve cuerpo al registrar: contesta 201 si creó la fila y 200 si PROMOVIÓ una
 *  identidad que ya existía (`agentidentity.go`, `WriteHeader` sin JSON). La única señal es el
 *  código, así que esta traducción es la línea portante de toda la pantalla.
 *
 *  ⛔ ESTA CELDA NACIÓ DE UNA MUTACIÓN QUE SOBREVIVIÓ: la batería del diálogo mockea
 *  `registerAgent`, así que forzar `promoted: false` en la API la dejaba ENTERA en verde. Probar
 *  cómo reacciona la UI a `{promoted}` no prueba de dónde sale ese booleano. */
describe('registerAgent — el código HTTP es el único dato', () => {
  it('201 es CREADO', async () => {
    http.postWithMeta.mockResolvedValue({ status: 201, data: undefined })
    await expect(
      governanceApi.registerAgent({
        identity_ref: 'a',
        sponsor_ref: 'user:1',
      }),
    ).resolves.toEqual({ promoted: false })
  })

  it('200 es PROMOVIDO', async () => {
    http.postWithMeta.mockResolvedValue({ status: 200, data: undefined })
    await expect(
      governanceApi.registerAgent({
        identity_ref: 'a',
        sponsor_ref: 'user:1',
      }),
    ).resolves.toEqual({ promoted: true })
  })

  it('llama a la ruta del motor con el cuerpo tal cual', async () => {
    http.postWithMeta.mockResolvedValue({ status: 201, data: undefined })
    const body = {
      identity_ref: 'a',
      sponsor_ref: 'user:1',
      criticality: 'high',
    }
    await governanceApi.registerAgent(body)
    expect(http.postWithMeta).toHaveBeenCalledWith(
      '/v1/m/governance/agents',
      body,
    )
  })
})
