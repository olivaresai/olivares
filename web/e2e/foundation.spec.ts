// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ADM-CORE-05 — verifies the CSP Level 3 + Trusted Types policy against the REAL
// olivares binary serving the embedded bundle (the per-response nonce injection
// only happens in Go serving, not vite dev). scripts/web-e2e.sh boots the engine and
// exports the harness env; without it these are skipped.
import { expect, test } from '@playwright/test'

const harness = process.env.PLAYWRIGHT_SETUP_TOKEN ?? ''

test.describe('Foundation — CSP L3 + Trusted Types', () => {
  test.skip(!harness, 'run via scripts/web-e2e.sh against the real binary')

  test('serves a strict L3 CSP with a per-response nonce, and the SPA boots under it', async ({
    page,
  }) => {
    const resp = await page.goto('/')
    const headers = resp?.headers() ?? {}
    const csp = headers['content-security-policy'] ?? ''

    // L3 hardening.
    expect(csp).toContain("'strict-dynamic'")
    expect(csp).toMatch(/script-src 'nonce-[^']+'/)
    expect(csp).toContain("object-src 'none'")
    expect(csp).toContain("base-uri 'none'")
    expect(csp).toContain("frame-ancestors 'none'")
    expect(csp).toContain("require-trusted-types-for 'script'")
    expect(csp).toContain('trusted-types olivares-html default')
    // DOMPurify's own policy name. Drop it and the safety net stops sanitising and
    // starts returning "" — silently, with no thrown error (see buildCSP).
    expect(csp).toContain('dompurify')
    expect(csp).not.toContain('sha256') // hashing replaced by nonce
    // The nonce'd document must not be cached.
    expect(headers['cache-control']).toContain('no-store')

    // The nonce in the header must be substituted into the served HTML (no
    // placeholder left) — otherwise the scripts would not be authorized.
    const nonce = csp.match(/'nonce-([^']+)'/)?.[1]
    expect(nonce, 'CSP carries a script nonce').toBeTruthy()
    const html = (await resp?.text()) ?? ''
    expect(html).not.toContain('__CSP_NONCE__')
    expect(html).toContain(`nonce="${nonce}"`)

    // The app actually MOUNTED under the strict policy → the nonced module entry +
    // its chunks ran, and Trusted Types did not break React. #root is populated.
    await expect(page.locator('#root')).not.toBeEmpty()
  })

  // WHY THIS TEST IS SHAPED LIKE THIS (2026-08-05). Its previous version filtered
  // console text through /content security policy|trusted ?types|refused to
  // (load|execute|apply)/i and passed — while Chromium was blocking two real things
  // on every single load. Chrome's wording for a Trusted Types sink is "This document
  // requires 'TrustedScript' assignment. The action has been blocked.", and
  // 'TrustedScript' does not match 'trusted ?types'. The gate was green by REGEX
  // BLINDNESS, not by cleanliness, and it stayed green for as long as it existed.
  //
  // So the primary evidence is now the browser's own `securitypolicyviolation` event,
  // which carries the effective directive and a sample and needs no pattern matching
  // at all. The console filter is kept as a second net, widened to Chrome's real
  // phrasing — but it is no longer the thing the verdict rests on.
  test('boots with no CSP / Trusted Types violations (browser-reported)', async ({
    page,
  }) => {
    await page.addInitScript(() => {
      ;(window as unknown as { __cspViolations: unknown[] }).__cspViolations = []
      document.addEventListener('securitypolicyviolation', (e) => {
        ;(
          window as unknown as { __cspViolations: unknown[] }
        ).__cspViolations.push({
          directive: e.effectiveDirective,
          blockedURI: e.blockedURI,
          sample: e.sample,
          source: `${e.sourceFile}:${e.lineNumber}`,
        })
      })
    })

    const consoleHits: string[] = []
    const suspect =
      /content security policy|trusted ?types|trustedscript|trustedhtml|trustedscripturl|refused to (load|execute|apply|evaluate)|violates the following/i
    page.on('console', (m) => {
      if (m.type() === 'error' && suspect.test(m.text())) consoleHits.push(m.text())
    })
    page.on('pageerror', (e) => {
      if (suspect.test(String(e))) consoleHits.push(String(e))
    })

    await page.goto('/')
    await page.waitForLoadState('networkidle')

    const reported = await page.evaluate(
      () => (window as unknown as { __cspViolations: unknown[] }).__cspViolations,
    )
    expect(reported, JSON.stringify(reported, null, 2)).toEqual([])
    expect(consoleHits, consoleHits.join('\n')).toEqual([])
  })

  // The check the old console filter could not have made even with a correct regex:
  // a safety net that ERASES everything throws nothing and logs nothing, so silence
  // is not evidence that it works. The payload discriminates in both directions —
  // the <b> must survive and the handler must not — so neither a net that passes
  // everything nor one that drops everything can satisfy it.
  test('the Trusted Types safety net sanitises rather than erases', async ({
    page,
  }) => {
    await page.goto('/')
    await page.waitForLoadState('networkidle')
    const got = await page.evaluate(() => {
      const el = document.createElement('div')
      // Assigning a raw string to a sink is precisely what routes through the
      // browser-invoked `default` policy — the path a stray dependency would take.
      el.innerHTML = '<b>ok</b><img src=x onerror="alert(1)">'
      return el.innerHTML
    })
    expect(got, 'safe markup must survive the safety net').toContain('<b>ok</b>')
    expect(got, 'the event handler must not survive it').not.toContain('onerror')
  })
})
