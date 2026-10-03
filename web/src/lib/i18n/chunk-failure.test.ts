// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, it, vi } from 'vitest'

// The chunk transport fails; English remains bundled in the actual foundation.
vi.mock('./locales/es/auth.json', () => {
  throw new Error('Offline auth chunk fixture')
})

import i18n, { authBackend, setLanguage } from './index'

it('retains translated login and marks a failed chunk as retryable', async () => {
  await setLanguage('es')
  expect(i18n.t('auth:login.title')).toBe('Sign in')
  expect(i18n.hasResourceBundle('es', 'auth')).toBe(false)
  const failure = await new Promise<{ error: unknown; retryable: unknown }>(
    (resolve) => {
      authBackend.read('es', 'auth', (error, retryable) => {
        resolve({ error, retryable })
      })
    },
  )
  expect(failure.error).toBeInstanceOf(Error)
  expect(failure.retryable).toBe(true)
  await setLanguage('en')
})
