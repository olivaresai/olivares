// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, it, expect } from 'vitest'
import { mcpHTTPSURL, mcpPublicJWKS } from './mcp-gateway-api'
describe('MCP public configuration boundary', () => {
  it('refuses credentials and ambiguous destinations', () => {
    for (const url of [
      'http://tools.test/mcp',
      'https://user:secret@tools.test/mcp',
      'https://tools.test/mcp?token=secret',
      'https://tools.test/mcp#secret',
      ' https://tools.test/mcp',
    ])
      expect(mcpHTTPSURL(url)).toBe(false)
    expect(mcpHTTPSURL('https://tools.test/mcp')).toBe(true)
  })
  it('refuses private keys and arbitrary credential properties', () => {
    expect(mcpPublicJWKS('{"keys":[{"kty":"EC","d":"private"}]}')).toBeNull()
    expect(
      mcpPublicJWKS('{"keys":[{"kty":"EC","credential":"secret"}]}'),
    ).toBeNull()
    expect(mcpPublicJWKS('{"keys":[{"kty":"oct","k":"secret"}]}')).toBeNull()
    expect(
      mcpPublicJWKS(
        '{"keys":[{"kty":"EC","crv":"P-256","x":"public","y":"public"}]}',
      ),
    ).not.toBeNull()
  })
})
