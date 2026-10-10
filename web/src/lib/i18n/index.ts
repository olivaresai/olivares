// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import i18n, { type BackendModule, type ResourceKey } from 'i18next'
import { initReactI18next } from 'react-i18next'
import LanguageDetector from 'i18next-browser-languagedetector'

import enCommon from './locales/en/common.json'
import enNav from './locales/en/nav.json'
import enAuth from './locales/en/auth.json'
import enErrors from './locales/en/errors.json'
import enSettings from './locales/en/settings.json'
import esCommon from './locales/es/common.json'
import esNav from './locales/es/nav.json'
import esErrors from './locales/es/errors.json'
import esSettings from './locales/es/settings.json'
import zhCommon from './locales/zh/common.json'
import zhNav from './locales/zh/nav.json'
import zhErrors from './locales/zh/errors.json'
import zhSettings from './locales/zh/settings.json'
import jaCommon from './locales/ja/common.json'
import jaNav from './locales/ja/nav.json'
import jaErrors from './locales/ja/errors.json'
import jaSettings from './locales/ja/settings.json'
import deCommon from './locales/de/common.json'
import deNav from './locales/de/nav.json'
import deErrors from './locales/de/errors.json'
import deSettings from './locales/de/settings.json'
import ruCommon from './locales/ru/common.json'
import ruNav from './locales/ru/nav.json'
import ruErrors from './locales/ru/errors.json'
import ruSettings from './locales/ru/settings.json'
import frCommon from './locales/fr/common.json'
import frNav from './locales/fr/nav.json'
import frErrors from './locales/fr/errors.json'
import frSettings from './locales/fr/settings.json'
// The auth dictionaries other than English, by URL (fetched on demand, see authBackend).
import deAuthUrl from './locales/de/auth.json?url'
import esAuthUrl from './locales/es/auth.json?url'
import frAuthUrl from './locales/fr/auth.json?url'
import jaAuthUrl from './locales/ja/auth.json?url'
import ruAuthUrl from './locales/ru/auth.json?url'
import zhAuthUrl from './locales/zh/auth.json?url'

/**
 * i18n foundation. The console shipped EN + ES from day one (ES = Spain)
 * added ZH (Simplified) / JA / DE / RU / FR for the public launch. The console bundles
 * the foundation namespaces below; a feature-module adds its OWN
 * namespace (no key collisions) by calling `registerTranslations('<module>', …)`
 * from its feature registration, so its strings live beside its code.
 *
 * Every language MUST carry the same key set as English — a missing key would
 * fall back to English silently. `scripts/check-i18n-parity.mjs` (in the push gate
 * + mainline-ci) and `parity.test.ts` fail on any missing/extra key or placeholder
 * drift, so the fallback can never hide an untranslated string in review.
 *
 * NO hardcoded user-facing strings anywhere — everything goes through a key.
 */
export const SUPPORTED_LANGUAGES = [
  { code: 'en', label: 'English' },
  { code: 'es', label: 'Español' },
  { code: 'zh', label: '中文' },
  { code: 'ja', label: '日本語' },
  { code: 'de', label: 'Deutsch' },
  { code: 'ru', label: 'Русский' },
  { code: 'fr', label: 'Français' },
] as const

export type LanguageCode = (typeof SUPPORTED_LANGUAGES)[number]['code']

/** Ordered list of language codes — the canonical parity set. */
export const LANGUAGE_CODES = SUPPORTED_LANGUAGES.map(
  (l) => l.code,
) as readonly LanguageCode[]

/** Foundation namespaces available at init. Features add theirs at runtime. */
export const FOUNDATION_NAMESPACES = [
  'common',
  'nav',
  'auth',
  'errors',
  'settings',
] as const

// Lazy auth dictionaries; English stays bundled for offline fallback. Fetched, not imported: a
// failed import stays failed in the browser until the page reloads; a fetch is simply made again.
const authUrls = {
  es: esAuthUrl,
  zh: zhAuthUrl,
  ja: jaAuthUrl,
  de: deAuthUrl,
  ru: ruAuthUrl,
  fr: frAuthUrl,
}

async function fetchDictionary(url: string): Promise<ResourceKey> {
  const response = await fetch(url, { credentials: 'same-origin' })
  if (!response.ok)
    throw new Error(`Translation resource answered ${response.status}`)
  return (await response.json()) as ResourceKey
}

export const authBackend: BackendModule = {
  type: 'backend',
  init() {},
  read(language, namespace, callback) {
    if (namespace === 'auth' && language === 'en') {
      callback(null, enAuth)
      return
    }
    const url = authUrls[language as keyof typeof authUrls]
    if (namespace !== 'auth' || !url) {
      callback(new Error('Translation resource is not available'), false)
      return
    }
    void fetchDictionary(url).then(
      (bundle) => callback(null, bundle),
      (error: unknown) =>
        callback(
          error instanceof Error
            ? error
            : new Error('Translation resource could not load'),
          true,
        ),
    )
  },
}

const resources = {
  en: {
    common: enCommon,
    nav: enNav,
    auth: enAuth,
    errors: enErrors,
    settings: enSettings,
  },
  es: {
    common: esCommon,
    nav: esNav,
    errors: esErrors,
    settings: esSettings,
  },
  zh: {
    common: zhCommon,
    nav: zhNav,
    errors: zhErrors,
    settings: zhSettings,
  },
  ja: {
    common: jaCommon,
    nav: jaNav,
    errors: jaErrors,
    settings: jaSettings,
  },
  de: {
    common: deCommon,
    nav: deNav,
    errors: deErrors,
    settings: deSettings,
  },
  ru: {
    common: ruCommon,
    nav: ruNav,
    errors: ruErrors,
    settings: ruSettings,
  },
  fr: {
    common: frCommon,
    nav: frNav,
    errors: frErrors,
    settings: frSettings,
  },
}

export const i18nReady = i18n
  .use(LanguageDetector)
  .use(authBackend)
  .use(initReactI18next)
  .init({
    resources,
    partialBundledLanguages: true,
    // Retry on language selection or reload, without delaying fallback.
    maxRetries: 0,
    fallbackLng: 'en',
    supportedLngs: LANGUAGE_CODES as unknown as string[],
    // Resolve region/script variants to the base language: en-US→en, zh-CN/zh-Hans→zh.
    load: 'languageOnly',
    nonExplicitSupportedLngs: true,
    defaultNS: 'common',
    ns: FOUNDATION_NAMESPACES as unknown as string[],
    interpolation: { escapeValue: false },
    returnNull: false,
    react: { useSuspense: false },
    detection: {
      order: ['localStorage', 'navigator', 'htmlTag'],
      caches: ['localStorage'],
      lookupLocalStorage: 'olivares.lang',
    },
  })

// Keep the document language in sync (a11y + correct hyphenation/quotes).
function syncHtmlLang(lng: string) {
  if (typeof document !== 'undefined') document.documentElement.lang = lng
}
syncHtmlLang(i18n.resolvedLanguage ?? i18n.language)
i18n.on('languageChanged', syncHtmlLang)

// The person's latest choice wins: a dictionary that arrives late must not switch back to it.
let latestChoice = 0

/** Switch UI language (persisted by the detector's localStorage cache). A dictionary that could
 * not load before (offline) is fetched again first: i18next never reads a failed namespace a
 * second time, and loading before switching keeps the words and the language together. */
export async function setLanguage(code: LanguageCode): Promise<void> {
  const choice = ++latestChoice
  if (!i18n.hasResourceBundle(code, 'auth'))
    await i18n.reloadResources(code, 'auth')
  if (choice === latestChoice) await i18n.changeLanguage(code)
}

/**
 * The active UI language resolved to one of {@link SUPPORTED_LANGUAGES}.
 * i18next's `resolvedLanguage` already maps region variants + fallback, but we
 * re-check against the supported set so callers always get a known code.
 */
export function currentLanguage(): LanguageCode {
  const resolved = i18n.resolvedLanguage ?? i18n.language
  return (LANGUAGE_CODES.find((c) => c === resolved) ?? 'en') as LanguageCode
}

/**
 * Register a feature-module's translation bundle under its own namespace. Every
 * supported language is registered when present; English is required (it is the
 * fallback + parity baseline). The parity check guarantees the rest are complete.
 */
export function registerTranslations(
  namespace: string,
  bundles: { en: Record<string, unknown> } & Partial<
    Record<LanguageCode, Record<string, unknown>>
  >,
): void {
  for (const code of LANGUAGE_CODES) {
    const bundle = bundles[code]
    if (bundle) i18n.addResourceBundle(code, namespace, bundle, true, true)
  }
}

export default i18n
