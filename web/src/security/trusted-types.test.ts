// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ADM-CORE-05 — Trusted Types policy. jsdom has no window.trustedTypes, so we mock
// the createPolicy registry and assert the two policies are installed with the
// right names and that their callbacks sanitise HTML / refuse script sinks.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { checkSafetyNet, installTrustedTypes, trustedHTML } from './trusted-types'

interface PolicyOptions {
  createHTML: (s: string) => string
  createScriptURL: (s: string) => string
  createScript: (s: string) => string
}

function mockTrustedTypes() {
  const policies = new Map<string, PolicyOptions>()
  const createPolicy = vi.fn((name: string, opts: PolicyOptions) => {
    policies.set(name, opts)
    return { name, ...opts }
  })
  ;(window as unknown as { trustedTypes: unknown }).trustedTypes = {
    createPolicy,
  }
  return { policies, createPolicy }
}

afterEach(() => {
  delete (window as unknown as { trustedTypes?: unknown }).trustedTypes
  vi.restoreAllMocks()
})

describe('Trusted Types — ADM-CORE-05', () => {
  it('installs both the default safety net and the named policy', () => {
    const { policies, createPolicy } = mockTrustedTypes()
    installTrustedTypes()
    expect(createPolicy).toHaveBeenCalledTimes(2)
    expect(policies.has('default')).toBe(true)
    expect(policies.has('olivares-html')).toBe(true)
  })

  it('createHTML strips scripts and event handlers (DOMPurify)', () => {
    const { policies } = mockTrustedTypes()
    installTrustedTypes()
    const clean = policies
      .get('default')!
      .createHTML(
        '<b>ok</b><img src=x onerror="alert(1)"><script>bad()</script>',
      )
    expect(clean).toContain('<b>ok</b>')
    expect(clean).not.toContain('<script>')
    expect(clean).not.toContain('onerror')
  })

  it('refuses cross-origin script URLs and string-to-script', () => {
    const { policies } = mockTrustedTypes()
    installTrustedTypes()
    const def = policies.get('default')!
    expect(() => def.createScriptURL('https://evil.example/x.js')).toThrow()
    // Same-origin is allowed (relative resolves against location.origin).
    expect(def.createScriptURL('/assets/app.js')).toBe('/assets/app.js')
    expect(() => def.createScript('alert(1)')).toThrow()
  })

  it('no-ops safely where Trusted Types is unsupported', () => {
    // No window.trustedTypes installed → must not throw.
    expect(() => installTrustedTypes()).not.toThrow()
  })

  // Scope note, deliberately explicit: jsdom has no Trusted Types, so the failure
  // this probe exists to catch — DOMPurify unable to mint its own policy, recursing
  // through the `default` one and returning "" — CANNOT be reproduced here. What
  // this asserts is that the probe itself discriminates: it says 'sanitising' for a
  // healthy sanitiser and 'blanking' for one that erases. The real-browser half is
  // web/e2e/foundation.spec.ts, and it is the half that has the enforcement.
  it('checkSafetyNet reports sanitising when the sanitiser is healthy', () => {
    expect(checkSafetyNet()).toBe('sanitising')
  })

  it('checkSafetyNet reports blanking when the sanitiser erases everything', async () => {
    const purify = (await import('dompurify')).default
    const spy = vi.spyOn(purify, 'sanitize').mockReturnValue('')
    expect(checkSafetyNet()).toBe('blanking')
    spy.mockRestore()
  })

  it('install reports a blanking safety net instead of failing silently', async () => {
    mockTrustedTypes()
    const purify = (await import('dompurify')).default
    const spy = vi.spyOn(purify, 'sanitize').mockReturnValue('')
    const err = vi.spyOn(console, 'error').mockImplementation(() => {})
    installTrustedTypes()
    expect(err).toHaveBeenCalledTimes(1)
    expect(String(err.mock.calls[0]?.[0])).toContain('dompurify')
    spy.mockRestore()
    err.mockRestore()
  })

  it('trustedHTML sanitises via the named policy after install', () => {
    mockTrustedTypes()
    installTrustedTypes()
    const out = trustedHTML('<i>x</i><script>bad()</script>') as string
    expect(out).toContain('<i>x</i>')
    expect(out).not.toContain('<script>')
  })
})
