// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ⛔ EL TESTIGO DE TRANSPORTE, que la batería de la pantalla NO puede tener: allí el módulo
//    de API está mockeado entero, así que un mutante que reciba el `limit` y NO lo ponga en
//    la URL escaparía — comprobar que la vista llamó con `{limit:1000}` no dice nada sobre
//    lo que sale por el cable. Lo pidió el contraste, y en `security` ese mutante existía y
//    escapaba hasta que se escribió este fichero hermano.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { killswitchApi } from './api'

function jsonOk() {
  return new Response('{"items":[],"has_more":false}', {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}

beforeEach(() => {
  vi.restoreAllMocks()
  useSessionStore.setState({ token: 't' } as never)
  useTenantStore.setState({ activeTenant: 't1' } as never)
})

describe('los parámetros de lista llegan a la URL, no sólo a la firma', () => {
  it('las paradas mandan `limit`', async () => {
    const spy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonOk())
    await killswitchApi.list({ limit: 1000 })
    const urls = spy.mock.calls.map((c) =>
      String(c[0] as Request | string | URL),
    )
    expect(urls.some((u) => /\/killswitch\?.*limit=1000/.test(u))).toBe(true)
  })

  it('las reglas del guardián mandan `limit`', async () => {
    const spy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonOk())
    await killswitchApi.listGuardianRules({ limit: 1000 })
    const urls = spy.mock.calls.map((c) =>
      String(c[0] as Request | string | URL),
    )
    expect(urls.some((u) => /\/guardian\/rules\?.*limit=1000/.test(u))).toBe(
      true,
    )
  })

  it('el rastro de contención manda `limit`', async () => {
    const spy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonOk())
    await killswitchApi.listGuardianActions({ limit: 1000 })
    const urls = spy.mock.calls.map((c) =>
      String(c[0] as Request | string | URL),
    )
    expect(urls.some((u) => /\/guardian\/actions\?.*limit=1000/.test(u))).toBe(
      true,
    )
  })
})
