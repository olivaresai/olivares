// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The point read of one binding, at the TRANSPORT: the component tests mock the
// typed client, so only this file can see that the AbortSignal the Details cycle
// hands to `agentOpsApi.getBinding` actually reaches `http.get` — without it an
// abort ends the cycle on screen and leaves the request running. Measured by
// mutation: a `getBinding` that drops the option keeps every component test green.
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/api/client', () => ({
  http: {
    get: vi.fn(),
    post: vi.fn(),
    patch: vi.fn(),
    put: vi.fn(),
    putRaw: vi.fn(),
    delete: vi.fn(),
  },
}))
import { http } from '@/lib/api/client'
import { agentOpsApi } from './api'

const get = vi.mocked(http.get)

beforeEach(() => {
  vi.clearAllMocks()
  get.mockResolvedValue({ binding_ref: 'psb_1' })
})

describe('agentOpsApi.getBinding — the transport contract of the point read', () => {
  it('reads the binding route for the encoded reference and forwards the signal', async () => {
    const ac = new AbortController()
    await agentOpsApi.getBinding('psb/odd ref', { signal: ac.signal })
    expect(get).toHaveBeenCalledOnce()
    const [path, opts] = get.mock.calls[0]
    expect(path).toBe('/v1/m/sessions/provider-source-bindings/psb%2Fodd%20ref')
    expect((opts as { signal?: AbortSignal }).signal).toBe(ac.signal)
    // The same object the caller aborts is the one the transport watches.
    ac.abort()
    expect((opts as { signal?: AbortSignal }).signal?.aborted).toBe(true)
  })

  it('is callable without options, as every existing caller shape allows', async () => {
    await agentOpsApi.getBinding('psb_1')
    const [path, opts] = get.mock.calls[0]
    expect(path).toBe('/v1/m/sessions/provider-source-bindings/psb_1')
    expect(
      (opts as { signal?: AbortSignal } | undefined)?.signal,
    ).toBeUndefined()
  })

  it('issues no other request: one GET, nothing posted', async () => {
    await agentOpsApi.getBinding('psb_1', {
      signal: new AbortController().signal,
    })
    expect(get).toHaveBeenCalledOnce()
    expect(http.post).not.toHaveBeenCalled()
    expect(http.patch).not.toHaveBeenCalled()
    expect(http.delete).not.toHaveBeenCalled()
  })
})
