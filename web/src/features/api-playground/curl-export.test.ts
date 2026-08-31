// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { generateCurl } from './curl-export'

describe('generateCurl', () => {
  it('generates GET request without body', () => {
    const curl = generateCurl({
      method: 'GET',
      url: 'https://localhost:8443/v1/agents',
      headers: { Authorization: 'Bearer olvs_test123' },
      body: null,
    })
    expect(curl).toContain('curl')
    expect(curl).not.toContain('-X')
    expect(curl).toContain('-H')
    expect(curl).toContain('Authorization: Bearer olvs_test123')
    expect(curl).toContain('https://localhost:8443/v1/agents')
  })

  it('generates POST request with body', () => {
    const curl = generateCurl({
      method: 'POST',
      url: 'https://localhost:8443/v1/agents',
      headers: {
        Authorization: 'Bearer olvs_test123',
        'Content-Type': 'application/json',
      },
      body: '{"name":"test","kind":"claude-code"}',
    })
    expect(curl).toContain('-X POST')
    expect(curl).toContain('-d')
    expect(curl).toContain('{"name":"test","kind":"claude-code"}')
  })

  it('generates DELETE request with body', () => {
    const curl = generateCurl({
      method: 'DELETE',
      url: 'https://localhost:8443/v1/agents/abc-123',
      headers: {
        Authorization: 'Bearer olvs_test123',
        'Content-Type': 'application/json',
      },
      body: '{"reason":"retired"}',
    })
    expect(curl).toContain('-X DELETE')
    expect(curl).toContain('-d')
    expect(curl).toContain('{"reason":"retired"}')
  })

  it('omits an empty DELETE body', () => {
    const curl = generateCurl({
      method: 'DELETE',
      url: 'https://localhost:8443/v1/agents/abc-123',
      headers: { Authorization: 'Bearer olvs_test123' },
      body: '   ',
    })
    expect(curl).not.toContain('-d')
  })

  it('generates PATCH request with body', () => {
    const curl = generateCurl({
      method: 'PATCH',
      url: 'https://localhost:8443/v1/agents/abc-123',
      headers: {
        Authorization: 'Bearer olvs_test123',
        'Content-Type': 'application/json',
      },
      body: '{"name":"updated"}',
    })
    expect(curl).toContain('-X PATCH')
    expect(curl).toContain('-d')
  })

  it('includes multiple headers', () => {
    const curl = generateCurl({
      method: 'GET',
      url: 'https://localhost:8443/v1/agents',
      headers: {
        Authorization: 'Bearer olvs_test123',
        'X-Olivares-Tenant': 'tenant-uuid',
        Accept: 'application/json',
      },
      body: null,
    })
    expect(curl).toContain('Authorization: Bearer olvs_test123')
    expect(curl).toContain('X-Olivares-Tenant: tenant-uuid')
    expect(curl).toContain('Accept: application/json')
  })

  it('shell-escapes values with special characters', () => {
    const curl = generateCurl({
      method: 'POST',
      url: 'https://localhost:8443/v1/auth/login',
      headers: { 'Content-Type': 'application/json' },
      body: '{"email":"user@test.com","password":"p@ss w0rd!"}',
    })
    expect(curl).toContain("'")
  })

  it('skips empty header values', () => {
    const curl = generateCurl({
      method: 'GET',
      url: 'https://localhost:8443/v1/agents',
      headers: { Authorization: 'Bearer token', 'X-Empty': '' },
      body: null,
    })
    expect(curl).not.toContain('X-Empty')
  })
})
