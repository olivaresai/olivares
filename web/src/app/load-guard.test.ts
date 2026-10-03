// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Root, 2026-10-02 (FH 08:37Z, WEB, EU walk2 11:02Z on 09b): over the engine's self-signed
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

  it('a failed image or a classic script is not a reason to reload', () => {
    const img = document.createElement('img')
    document.body.appendChild(img)
    img.dispatchEvent(new Event('error'))
    failedScript('text/javascript')
    expect(reload).not.toHaveBeenCalled()
  })

  it('says it in the console language', () => {
    localStorage.setItem('olivares.lang', 'es')
    failedScript()
    expect(document.querySelector('[data-load-guard-line]')).toHaveTextContent(
      'Recargando la consola…',
    )
  })
})
