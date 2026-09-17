// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it, vi } from 'vitest'

const { http } = vi.hoisted(() => ({
  http: { get: vi.fn(), put: vi.fn(), post: vi.fn(), delete: vi.fn() },
}))
vi.mock('@/lib/api/client', () => ({ http }))

import {
  inferenceProxyApi,
  isContentFirewall,
  isContentFirewallState,
} from './api'

describe('content-firewall client', () => {
  it('calls the exact engine operation through the authenticated transport', async () => {
    http.get.mockResolvedValue({
      pep: 'messages_proxy',
      state: 'unobserved',
      note: '',
    })
    await inferenceProxyApi.getContentFirewall()
    expect(http.get).toHaveBeenCalledWith(
      '/v1/m/inferenceproxy/content-firewall',
    )
  })

  it('accepts exactly the four published states', () => {
    for (const s of [
      'unobserved',
      'pep_not_composed',
      'inspector_absent',
      'inspector_attached',
    ]) {
      expect(isContentFirewallState(s)).toBe(true)
    }
    for (const s of [
      '',
      'attached',
      'inspector_partially_attached',
      null,
      7,
      undefined,
    ]) {
      expect(isContentFirewallState(s)).toBe(false)
    }
  })

  it('accepts only a complete body, and tolerates an additive field', () => {
    const ok = { pep: 'messages_proxy', state: 'inspector_attached', note: '' }
    expect(isContentFirewall(ok)).toBe(true)
    expect(isContentFirewall({ ...ok, note: 'a scope note' })).toBe(true)
    expect(isContentFirewall({ ...ok, observed_at: 'x' })).toBe(true)
    for (const state of [
      'unobserved',
      'pep_not_composed',
      'inspector_absent',
    ]) {
      expect(isContentFirewall({ ...ok, state })).toBe(true)
    }
    for (const bad of [
      { ...ok, pep: 'hook_pep' },
      { state: 'inspector_attached', note: '' },
      { ...ok, note: 7 },
      { ...ok, note: undefined },
      { pep: 'messages_proxy', state: 'inspector_attached' },
      { ...ok, state: 'inspector_partially_attached' },
      { ...ok, state: undefined },
      null,
      undefined,
      [ok],
      'messages_proxy',
      7,
    ]) {
      expect(isContentFirewall(bad)).toBe(false)
    }
  })
})
