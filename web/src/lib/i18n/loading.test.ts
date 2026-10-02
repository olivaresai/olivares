// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { createInstance } from 'i18next'
import LanguageDetector from 'i18next-browser-languagedetector'
import { afterEach, describe, expect, it, vi } from 'vitest'
import i18n, { authBackend, LANGUAGE_CODES, setLanguage } from './index'
import enAuth from './locales/en/auth.json'

async function languageInstance(language?: string) {
  const instance = createInstance().use(authBackend).use(LanguageDetector)
  await instance.init({
    lng: language,
    resources: { en: { auth: enAuth } },
    partialBundledLanguages: true,
    maxRetries: 0,
    fallbackLng: 'en',
    supportedLngs: [...LANGUAGE_CODES],
    load: 'languageOnly',
    nonExplicitSupportedLngs: true,
    defaultNS: 'auth',
    ns: ['auth'],
    detection: {
      order: ['localStorage'],
      lookupLocalStorage: 'olivares.lang',
      caches: ['localStorage'],
    },
  })
  return instance
}

afterEach(async () => {
  vi.restoreAllMocks()
  await setLanguage('en')
})

describe('authentication translations', () => {
  it('keeps inactive auth languages out of the initial resource store', () => {
    expect(i18n.hasResourceBundle('en', 'auth')).toBe(true)
    for (const language of ['es', 'zh', 'ja', 'de', 'ru', 'fr']) {
      expect(i18n.hasResourceBundle(language, 'auth'), language).toBe(false)
    }
  })
  it.each([
    ['en', 'Sign in'],
    ['es', 'Iniciar sesión'],
    ['zh', '登录'],
    ['ja', 'サインイン'],
    ['de', 'Anmelden'],
    ['ru', 'Вход'],
    ['fr', 'Se connecter'],
  ])(
    'loads persisted %s without unrelated auth dictionaries',
    async (code, title) => {
      localStorage.setItem('olivares.lang', code)
      const instance = await languageInstance()
      expect(instance.t('login.title')).toBe(title)
      expect(instance.language).toBe(code)
      for (const other of LANGUAGE_CODES.filter(
        (lng) => lng !== code && lng !== 'en',
      )) {
        expect(instance.hasResourceBundle(other, 'auth'), other).toBe(false)
      }
      expect(instance.t('login.title', { lng: 'en' })).toBe('Sign in')
    },
  )

  it('resolves a regional persisted language and retains English missing-key fallback', async () => {
    localStorage.setItem('olivares.lang', 'es-ES')
    const instance = await languageInstance()
    expect(instance.t('login.title')).toBe('Iniciar sesión')
    instance.addResourceBundle('en', 'auth', {
      fallbackProbe: 'Available offline',
    })
    expect(instance.t('fallbackProbe')).toBe('Available offline')
  })

  it('does not change visible language until the requested dictionary is ready', async () => {
    const instance = await languageInstance('en')
    const read = authBackend.read.bind(authBackend)
    let finish: (() => void) | undefined
    vi.spyOn(authBackend, 'read').mockImplementationOnce(
      (language, namespace, callback) => {
        finish = () => read(language, namespace, callback)
      },
    )
    const switching = instance.changeLanguage('es')
    expect(instance.t('login.title')).toBe('Sign in')
    finish!()
    await switching
    expect(instance.t('login.title')).toBe('Iniciar sesión')
    await instance.changeLanguage('en')
    expect(instance.t('login.title')).toBe('Sign in')
    await instance.changeLanguage('es')
    expect(instance.t('login.title')).toBe('Iniciar sesión')
  })

  it('keeps login usable offline after a failed chunk and supports explicit retry', async () => {
    vi.spyOn(authBackend, 'read').mockImplementationOnce(
      (_language, _namespace, callback) => {
        callback(new Error('Offline chunk fixture'), true)
      },
    )
    const instance = await languageInstance('es')
    expect(instance.t('login.title')).toBe('Sign in')
    expect(instance.hasResourceBundle('es', 'auth')).toBe(false)
    await instance.reloadResources(['es'], ['auth'])
    expect(instance.t('login.title')).toBe('Iniciar sesión')
    expect(instance.hasResourceBundle('es', 'auth')).toBe(true)
  })

  it('switches and persists the console language through its public helper', async () => {
    await setLanguage('es')
    expect(i18n.t('auth:login.title')).toBe('Iniciar sesión')
    expect(document.documentElement.lang).toBe('es')
    expect(localStorage.getItem('olivares.lang')).toBe('es')
    await setLanguage('en')
    expect(i18n.t('auth:login.title')).toBe('Sign in')
  })
})
