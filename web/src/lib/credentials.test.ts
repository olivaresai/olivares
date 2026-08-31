// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { looksLikeCredential } from './credentials'

describe('looksLikeCredential (inline-credential guard mirror)', () => {
  it('flags basic-auth userinfo in a URL', () => {
    expect(looksLikeCredential('https://user:s3cret@host/mcp')).toBe(true)
  })

  it('flags token/secret/password assignments', () => {
    expect(looksLikeCredential('https://api?token=abc123')).toBe(true)
    expect(looksLikeCredential('password=hunter2')).toBe(true)
    expect(looksLikeCredential('X-Api-Key: api_key=zzz')).toBe(true)
    expect(looksLikeCredential('client_secret=oops')).toBe(true)
  })

  it('passes clean references and locators', () => {
    expect(looksLikeCredential('$GITHUB_TOKEN')).toBe(false)
    expect(looksLikeCredential('vault://prod/db#password_ref')).toBe(false)
    expect(
      looksLikeCredential('npx -y @modelcontextprotocol/server-github'),
    ).toBe(false)
    expect(looksLikeCredential('https://mcp.example.com/sse')).toBe(false)
  })

  it('treats empty / nullish as not a credential', () => {
    expect(looksLikeCredential('')).toBe(false)
    expect(looksLikeCredential(undefined)).toBe(false)
    expect(looksLikeCredential(null)).toBe(false)
  })
})
