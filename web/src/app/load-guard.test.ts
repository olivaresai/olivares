// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Root, 2026-10-02 (first-hour check:37Z, WEB, EU walk2 11:02Z on 09b): over the engine's self-signed
// TLS, Chromium failed module chunks with ERR_CERT_VERIFIER_CHANGED on first load and the
// console stayed blank with no /v1 call. The guard in index.html runs before any bundle;
// these cases run THAT script, as shipped, in the page.
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import {
  afterAll,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from 'vitest'
import { RELOAD_NOTICE_KEY } from '@/app/reload-notice'
import {
  readSessionDraft,
  sessionDraftKey,
  writeSessionDraft,
} from '@/lib/session-drafts'

const html = readFileSync(join(__dirname, '../../index.html'), 'utf8')
const guard =
  /<script nonce="__CSP_NONCE__" data-load-guard>([\s\S]*?)<\/script>/.exec(
    html,
  )?.[1]

const reload = vi.fn()
const realLocation = window.location

function failedScript(type = 'module') {
  const el = document.createElement('script')
  el.type = type
  document.head.appendChild(el)
  el.dispatchEvent(new Event('error'))
  el.remove()
}

beforeAll(() => {
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { ...realLocation, reload },
  })
  // eslint-disable-next-line @typescript-eslint/no-implied-eval
  new Function(guard as string)()
})
afterAll(() => {
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: realLocation,
  })
})
beforeEach(() => {
  reload.mockClear()
  sessionStorage.clear()
  localStorage.clear()
  document.body.innerHTML = '<div id="root"></div>'
})

describe('the console load guard (index.html)', () => {
  it('is in the document, under the per-response nonce', () => {
    expect(guard).toBeTruthy()
  })

  it('a module chunk that fails to load: one plain line, and one reload', () => {
    failedScript()
    expect(reload).toHaveBeenCalledTimes(1)
    const line = document.querySelector('[data-load-guard-line]')
    expect(line).toHaveTextContent('Reloading the console…')
    expect(line).toHaveAttribute('role', 'status')
  })

  it('never loops: a second failure within two minutes does not reload again', () => {
    failedScript()
    failedScript()
    expect(reload).toHaveBeenCalledTimes(1)
  })

  it('a lazy chunk that fails (vite:preloadError) reloads once, and its throw is held', () => {
    const event = new Event('vite:preloadError', { cancelable: true })
    window.dispatchEvent(event)
    expect(reload).toHaveBeenCalledTimes(1)
    expect(event.defaultPrevented).toBe(true)
  })

  // the regression: the upgrade hand-off — the old tab's lazy chunk is gone after the
  // engine swapped builds; the reload lands the new build, and the next document
  // says so once ("Updated, reloaded", app/reload-notice.tsx) where the guard
  // flags it. The key name MUST stay identical to RELOAD_NOTICE_KEY there: this
  // script runs before any bundle, so the agreement is by name, not import.
  it('flags the one-time notice for the next document when a lazy chunk reloads', () => {
    window.dispatchEvent(new Event('vite:preloadError', { cancelable: true }))
    expect(reload).toHaveBeenCalledTimes(1)
    expect(sessionStorage.getItem(RELOAD_NOTICE_KEY)).toBe('1')
  })

  it('keeps the notice when modulepreload fails before Vite reports the lazy import', () => {
    const link = document.createElement('link')
    link.rel = 'modulepreload'
    document.head.appendChild(link)
    link.dispatchEvent(new Event('error'))
    link.remove()
    expect(reload).toHaveBeenCalledTimes(1)
    expect(sessionStorage.getItem(RELOAD_NOTICE_KEY)).toBe('1')
    const event = new Event('vite:preloadError', { cancelable: true })
    window.dispatchEvent(event)
    expect(reload).toHaveBeenCalledTimes(1)
    expect(event.defaultPrevented).toBe(true)
  })

  it.each(['loading', 'interactive'] as const)(
    'a first-load transport failure (%s) reloads without flagging the notice',
    (readyStateValue) => {
      const readyState = vi
        .spyOn(document, 'readyState', 'get')
        .mockReturnValue(readyStateValue)
      try {
        failedScript()
        expect(reload).toHaveBeenCalledTimes(1)
        expect(sessionStorage.getItem(RELOAD_NOTICE_KEY)).toBeNull()
      } finally {
        readyState.mockRestore()
      }
    },
  )

  it('an unsent session draft survives the reload the guard starts', () => {
    const draftKey = sessionDraftKey(
      'tenant',
      { kind: 'user', actor: 'user:alice', user_id: 'alice' },
      'run',
    )!
    writeSessionDraft(draftKey, 'Half-written turn')
    window.dispatchEvent(new Event('vite:preloadError', { cancelable: true }))
    expect(reload).toHaveBeenCalledTimes(1)
    // Drafts are the tab's sessionStorage (lib/session-drafts.ts), which a
    // document reload keeps: the composer reopens with the half-written text.
    expect(readSessionDraft(draftKey)).toBe('Half-written turn')
  })

  it('offline, a lazy chunk that fails is left to its caller: no reload, its throw not held', () => {
    const onLine = vi
      .spyOn(window.navigator, 'onLine', 'get')
      .mockReturnValue(false)
    try {
      const event = new Event('vite:preloadError', { cancelable: true })
      window.dispatchEvent(event)
      expect(reload).not.toHaveBeenCalled()
      expect(event.defaultPrevented).toBe(false)
      expect(document.querySelector('[data-load-guard-line]')).toBeNull()
    } finally {
      onLine.mockRestore()
    }
    // Back online, the one reload is still there for the failure it exists for.
    failedScript()
    expect(reload).toHaveBeenCalledTimes(1)
  })

  it('a failed image or a classic script is not a reason to reload', () => {
    const img = document.createElement('img')
    document.body.appendChild(img)
    img.dispatchEvent(new Event('error'))
    failedScript('text/javascript')
    expect(reload).not.toHaveBeenCalled()
  })

  it.each([
    ['es', 'Recargando la consola…'],
    ['it', 'Ricaricamento della console…'],
    ['ko', '콘솔을 다시 불러오는 중…'],
  ])('says it in the console language (%s)', (lang, text) => {
    localStorage.setItem('olivares.lang', lang)
    failedScript()
    expect(document.querySelector('[data-load-guard-line]')).toHaveTextContent(
      text,
    )
  })
})
