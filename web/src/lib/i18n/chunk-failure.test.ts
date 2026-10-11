// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Offline, a language whose auth dictionary cannot load keeps the console in English; back
// online, choosing it again loads it, without a reload. The dictionary used to be a module
// import, and a failed import stays failed in the browser until the page reloads.
import { afterEach, expect, it, vi } from 'vitest'
import i18n, { authBackend, setLanguage } from './index'

afterEach(async () => {
  vi.restoreAllMocks()
  await setLanguage('en')
})

it('keeps English while a dictionary cannot load, and loads it when chosen again', async () => {
  const online = globalThis.fetch
  const network = vi
    .spyOn(globalThis, 'fetch')
    .mockRejectedValue(new TypeError('Failed to fetch'))
  await setLanguage('es')
  expect(i18n.t('auth:login.title')).toBe('Sign in')
  expect(i18n.hasResourceBundle('es', 'auth')).toBe(false)
  expect(network).toHaveBeenCalledWith(
    expect.stringMatching(/\/locales\/es\/auth\.json/),
    expect.anything(),
  )

  network.mockImplementation(online)
  await setLanguage('en')
  await setLanguage('es')
  // prettier-ignore
  const spanishSignIn = 'Iniciar sesión' // language-data: Spanish auth dictionary assertion
  expect(i18n.t('auth:login.title')).toBe(spanishSignIn)
})

it('marks a dictionary that did not load as retryable', async () => {
  vi.spyOn(globalThis, 'fetch').mockResolvedValue(
    new Response('unavailable', { status: 503 }),
  )
  const failure = await new Promise<{ error: unknown; retryable: unknown }>(
    (resolve) => {
      authBackend.read('fr', 'auth', (error, retryable) => {
        resolve({ error, retryable })
      })
    },
  )
  expect(failure.error).toBeInstanceOf(Error)
  expect(failure.retryable).toBe(true)
})

it('keeps the latest choice when an earlier dictionary arrives late', async () => {
  const online = globalThis.fetch
  let release = () => {}
  const held = new Promise<void>((resolve) => {
    release = resolve
  })
  await setLanguage('es')
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
    if (String(input).includes('/locales/ja/')) await held
    return online(input, init)
  })
  const late = setLanguage('ja')
  await setLanguage('en')
  release()
  await late
  expect(i18n.language).toBe('en')
  expect(document.documentElement.lang).toBe('en')
  expect(localStorage.getItem('olivares.lang')).toBe('en')
})
