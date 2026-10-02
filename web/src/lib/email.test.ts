// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The console never refuses an address the engine and the CLI accept, and never passes one
// they refuse (core/auth ValidateEmail, ID 2026-10-02).
import { describe, expect, it } from 'vitest'
import { isEngineEmail, normalizeEmail } from './email'

describe('isEngineEmail: the engine rule (normalizeEmail + net/mail, bare mailbox)', () => {
  it.each([
    'ana@acme.io',
    '  Ana@Acme.IO  ',
    'name+tag@corp.internal',
    'name@host',
    'first.last@sub.example.com',
    "o'brien@example.com",
    'üser@exämple.com',
    'a@[192.168.1.10]',
  ])('accepts %s', (email) => {
    expect(isEngineEmail(email)).toBe(true)
  })

  it.each([
    'bad@',
    '@example.com',
    'bad',
    '',
    'a@@b',
    'a@b@c',
    '.a@x',
    'a.@x',
    'a..b@x',
    'a@x.',
    'a@.x',
    'a@x..y',
    'a b@x',
    'Ana <ana@acme.io>',
    '<ana@acme.io>',
    'ana@acme.io, bob@acme.io',
    '"ana"@acme.io',
    'ana@acme.io (work)',
    'a@[not-an-ip]',
  ])('refuses %s', (email) => {
    expect(isEngineEmail(email)).toBe(false)
  })

  it('normalizes as the engine does: trimmed, lower case', () => {
    expect(normalizeEmail('  Ana@Acme.IO ')).toBe('ana@acme.io')
  })
})
