// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// olivares#2781: a failed fetch on a page opened over https: names the certificate, a failed
// fetch on a page opened over http: keeps "unreachable". Both states, in every surface that
// words a NetworkError.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import { NetworkError } from '@/lib/api/errors'
import i18n from '@/lib/i18n'
import { networkErrorText } from './network-error-text'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ isSuperadmin: false, activeTenant: 't1', can: () => true }),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
  Toaster: () => null,
}))

import { QueryErrorState } from '@/components/layout/query-error-state'

const realLocation = window.location

function openedAs(url: string) {
  const { protocol, origin } = new URL(url)
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { ...realLocation, protocol, origin },
  })
}

afterEach(() => {
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: realLocation,
  })
})

describe('networkErrorText', () => {
  it('over http the engine does not answer: unreachable, as before', () => {
    openedAs('http://localhost:8080/login')
    const text = networkErrorText(i18n.t)
    expect(text.title).toBe('Control plane unreachable')
    expect(text.line).toBe('Control plane unreachable')
    expect(text.description).toMatch(/engine is running/i)
  })

  it('over https it names the certificate, the address and the next step', () => {
    openedAs('https://192.168.8.17:8443/login')
    const text = networkErrorText(i18n.t)
    expect(text.title).not.toMatch(/unreachable/i)
    expect(text.description).toContain('https://192.168.8.17:8443')
    expect(text.description).toMatch(/certificate/i)
    expect(text.description).toContain('tls.crt.ca')
    expect(text.description).toMatch(/engine is running/i)
    expect(text.line).toBe(text.description)
  })
})

describe('QueryErrorState on a NetworkError', () => {
  it('over http says the control plane is unreachable', () => {
    openedAs('http://localhost:8080/')
    renderIntel(<QueryErrorState error={new NetworkError('down')} />)
    expect(screen.getByText('Control plane unreachable')).toBeInTheDocument()
  })

  it('over https names the certificate instead', () => {
    openedAs('https://192.168.8.17:8443/')
    renderIntel(<QueryErrorState error={new NetworkError('down')} />)
    expect(screen.queryByText('Control plane unreachable')).toBeNull()
    expect(screen.getByText(/tls\.crt\.ca/)).toBeInTheDocument()
  })
})
