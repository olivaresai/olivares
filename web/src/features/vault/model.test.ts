// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import type { SecretDTO } from '@/features/console/api'
import {
  LABEL_RE,
  entryName,
  findRows,
  parseItem,
  primaryValue,
  safeLink,
  serializeItem,
  vaultRows,
} from './model'

const secret = (name: string, hint: string, description = ''): SecretDTO => ({
  name,
  hint,
  description,
  created_at: '2026-10-01T10:00:00Z',
  updated_at: '2026-10-02T10:00:00Z',
})

describe('vaultRows', () => {
  const rows = vaultRows(
    [
      secret('vault/u1/forum', 'login'),
      secret('vault/u1/bank', 'login+totp', 'Main account'),
      secret('vault/u1/safe', 'note'),
      secret('vault/u1/github-2fa', 'totp'),
      secret('vault/u2/other', 'login'),
      secret('mcp/github', 'a1b2c3d4e5f6'),
      secret('vault/u1/', 'login'),
    ],
    'u1',
  )

  it("keeps only the person's own items, by label", () => {
    expect(rows.map((r) => r.label)).toEqual([
      'bank',
      'forum',
      'github-2fa',
      'safe',
    ])
    expect(rows.map((r) => r.name)).toContain('vault/u1/bank')
  })

  it('reads the kind and the authenticator secret from the hint', () => {
    const byLabel = Object.fromEntries(rows.map((r) => [r.label, r]))
    expect(byLabel.bank).toMatchObject({ kind: 'login', hasCode: true })
    expect(byLabel.forum).toMatchObject({ kind: 'login', hasCode: false })
    expect(byLabel.safe).toMatchObject({ kind: 'note', hasCode: false })
    expect(byLabel['github-2fa']).toMatchObject({ kind: 'totp', hasCode: true })
    expect(byLabel.bank.description).toBe('Main account')
  })

  it('shows a hint it does not know as a note, never as a crash', () => {
    const [row] = vaultRows([secret('vault/u1/odd', 'a1b2c3d4e5f6')], 'u1')
    expect(row).toMatchObject({ kind: 'note', hasCode: false })
  })
})

describe('findRows', () => {
  const rows = vaultRows(
    [
      secret('vault/u1/bank', 'login+totp', 'Main account'),
      secret('vault/u1/forum', 'login'),
      secret('vault/u1/safe', 'note', 'Code of the safe'),
    ],
    'u1',
  )

  it('finds by label, description or kind, every word', () => {
    expect(findRows(rows, 'bank').map((r) => r.label)).toEqual(['bank'])
    expect(findRows(rows, 'SAFE code').map((r) => r.label)).toEqual(['safe'])
    expect(findRows(rows, 'note').map((r) => r.label)).toEqual(['safe'])
    expect(findRows(rows, 'bank safe')).toEqual([])
  })

  it('shows everything for an empty query', () => {
    expect(findRows(rows, '  ')).toHaveLength(3)
  })
})

describe('the item document', () => {
  it('serializes without empty fields, in the order the engine seals them', () => {
    expect(
      serializeItem({
        type: 'login',
        totp: 'JBSWY3DPEHPK3PXP',
        password: 'pw',
        username: 'ann',
        url: '',
        note: '',
      }),
    ).toBe(
      '{"type":"login","username":"ann","password":"pw","totp":"JBSWY3DPEHPK3PXP"}',
    )
  })

  it('round trips what it serializes', () => {
    const item = {
      type: 'login' as const,
      username: 'ann',
      password: 'pw',
      url: 'https://bank.example',
      note: 'two\nlines',
      totp: 'JBSWY3DPEHPK3PXP',
    }
    expect(parseItem(serializeItem(item))).toEqual(item)
  })

  it('refuses a value that is not an item', () => {
    for (const bad of [
      'plain-password',
      '[]',
      'null',
      '{"type":"card"}',
      '{}',
    ]) {
      expect(parseItem(bad), bad).toBeNull()
    }
  })

  it('drops fields of the wrong type instead of trusting them', () => {
    expect(parseItem('{"type":"note","note":4,"password":{"a":1}}')).toEqual({
      type: 'note',
    })
  })

  it('copies the password of a login, the text of a note, the key of an authenticator', () => {
    expect(
      primaryValue({ type: 'login', username: 'ann', password: 'pw' }),
    ).toBe('pw')
    // A login with no password has nothing to copy: never its username under that name.
    expect(primaryValue({ type: 'login', username: 'ann' })).toBe('')
    expect(primaryValue({ type: 'note', note: 'text' })).toBe('text')
    expect(primaryValue({ type: 'totp', totp: 'KEY' })).toBe('KEY')
  })
})

describe('names and links', () => {
  it('builds the entry name the engine expects', () => {
    expect(entryName('u1', 'bank')).toBe('vault/u1/bank')
  })

  it("accepts the secret store's name characters and nothing else", () => {
    for (const ok of ['bank', 'work-2', 'a.b_c'])
      expect(LABEL_RE.test(ok)).toBe(true)
    for (const bad of ['', 'bank account', 'a/b', '\u00e9', 'x'.repeat(129)])
      expect(LABEL_RE.test(bad), bad).toBe(false)
  })

  it('links only http and https, adding https to a bare host', () => {
    expect(safeLink('https://bank.example/login')).toBe(
      'https://bank.example/login',
    )
    expect(safeLink('bank.example')).toBe('https://bank.example/')
    expect(safeLink('http://intranet')).toBe('http://intranet/')
    for (const bad of [
      'javascript:alert(1)',
      'data:text/html,x',
      'file:///etc/passwd',
      '',
      undefined,
    ])
      expect(safeLink(bad), String(bad)).toBeNull()
  })
})
