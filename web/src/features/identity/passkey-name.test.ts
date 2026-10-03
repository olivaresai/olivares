// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import type { TFunction } from 'i18next'
import { defaultPasskeyName, deviceKind } from './passkey-name'

const t = ((key: string, o?: { device?: string }) =>
  key.endsWith('defaultName')
    ? `Passkey on ${o?.device}`
    : 'Passkey') as unknown as TFunction

describe('the default passkey name', () => {
  it('names the device the passkey was made on', () => {
    expect(
      deviceKind({ userAgent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5)' }),
    ).toBe('Mac')
    expect(
      deviceKind({
        userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)',
      }),
    ).toBe('iPhone')
    expect(
      deviceKind({ userAgent: 'Mozilla/5.0 (Linux; Android 15; Pixel 9)' }),
    ).toBe('Android')
    expect(
      deviceKind({ userAgent: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64)' }),
    ).toBe('Windows')
    expect(deviceKind({ userAgent: 'Mozilla/5.0 (X11; Linux x86_64)' })).toBe(
      'Linux',
    )
    expect(
      defaultPasskeyName(t, { userAgent: 'Mozilla/5.0 (X11; Linux x86_64)' }),
    ).toBe('Passkey on Linux')
  })

  it('is just "Passkey" when the device cannot be told', () => {
    expect(defaultPasskeyName(t, { userAgent: 'curl/8' })).toBe('Passkey')
  })
})
