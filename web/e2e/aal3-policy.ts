// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { randomBytes } from 'node:crypto'
import {
  expect,
  type Browser,
  type BrowserContext,
  type Page,
} from '@playwright/test'
import { journeyNowLink } from './browser-session'

// The demo estate asks for no step-up (policy `none`). To photograph the step-up notice a
// password session sees under an AAL3 policy, the policy has to be `passkey`. The engine
// refuses to raise it unless the calling session just used a passkey: with no passkey enrolled
// it answers 409 `passkey_not_enrolled` (asserted by scripts/first-hour-smoke.sh), and with one
// enrolled but not used, 403 `step_up_required`. So the seeder cannot set it; the console can,
// with a Chromium virtual authenticator, exactly as settings-sign-in.spec.ts does.
//
// The policy is deployment-wide: while it is `passkey`, every administrative tab of every session
// without a fresh passkey shows the step-up notice instead of its content. enable() therefore runs
// in its own browser context, and restore() must run after the capture, pass or fail.
// The passkey stays on the demo account of the throwaway estate: the engine would allow deleting
// it once the policy is Off, but nothing needs it gone. Each instance names its own passkey, so
// a second pass on the same estate does not match two rows.
const SETTINGS = '/settings?section=signIn'
const LOOPBACK = new Set(['localhost', '127.0.0.1', '[::1]'])

interface DemoLogin {
  email: string
  password: string
  tenant: string
}

export class Aal3Policy {
  private context?: BrowserContext
  private page?: Page
  // True from the moment the policy may have changed; restore() lowers it only then.
  private raised = false
  private readonly passkeyName = `Docs capture passkey ${randomBytes(3).toString('hex')}`
  private readonly browser: Browser
  private readonly baseURL: string
  private readonly demo: DemoLogin

  constructor(browser: Browser, baseURL: string, demo: DemoLogin) {
    this.browser = browser
    this.baseURL = baseURL
    this.demo = demo
  }

  // Sign in as the demo administrator in a separate context, register a virtual passkey and
  // raise the policy to Passkey. The session that takes the capture is another context and
  // stays on its password.
  async enable(): Promise<void> {
    // This changes a deployment's authentication policy and registers a passkey on its
    // administrator: it runs against the throwaway engine of the harness and nothing else.
    const { hostname } = new URL(this.baseURL)
    if (!LOOPBACK.has(hostname)) {
      throw new Error(
        `docs-captures: the AAL3 policy is raised only on a loopback engine, not on ${hostname}`,
      )
    }
    const context = await this.browser.newContext({
      baseURL: this.baseURL,
      viewport: { width: 1440, height: 1000 },
    })
    // Kept before anything else, so restore() always closes the context.
    this.context = context
    await context.addInitScript(
      ([tenant]) => {
        localStorage.setItem(
          'olivares.tenant',
          JSON.stringify({ state: { activeTenant: tenant }, version: 0 }),
        )
        localStorage.setItem(
          'olivares.lang',
          JSON.stringify({ state: { lang: 'en' }, version: 0 }),
        )
      },
      [this.demo.tenant],
    )
    const page = await context.newPage()
    this.page = page
    const cdp = await context.newCDPSession(page)
    await cdp.send('WebAuthn.enable')
    await cdp.send('WebAuthn.addVirtualAuthenticator', {
      options: {
        protocol: 'ctap2',
        transport: 'internal',
        hasResidentKey: true,
        hasUserVerification: true,
        isUserVerified: true,
        automaticPresenceSimulation: true,
      },
    })

    await page.goto('/login')
    await page.locator('#email').fill(this.demo.email)
    await page.locator('#password').fill(this.demo.password)
    await page.getByRole('button', { name: /^sign in$/i }).click()
    await expect(journeyNowLink(page)).toBeVisible({ timeout: 60_000 })

    await page.goto(SETTINGS)
    // An estate that already asks for step-up is not this harness's to change, and restore()
    // would lower it to Off.
    await expect(
      page.getByRole('radio', { name: 'Off', exact: true }),
      'docs-captures: the estate already asks for step-up; refusing to change a policy this harness did not set',
    ).toHaveAttribute('aria-checked', 'true', { timeout: 15_000 })

    await page
      .getByRole('button', { name: 'Register passkey', exact: true })
      .click()
    await page
      .getByLabel('Passkey name', { exact: true })
      .fill(this.passkeyName)
    await page
      .getByRole('dialog')
      .getByRole('button', { name: 'Register passkey', exact: true })
      .click()
    await expect(
      page.getByText(this.passkeyName, { exact: true }),
    ).toBeVisible()

    this.raised = true
    await page.getByRole('radio', { name: 'Passkey', exact: true }).click()
    await page
      .getByRole('button', { name: 'Authenticate with passkey', exact: true })
      .click()
    await expect(
      page.getByRole('radio', { name: 'Passkey', exact: true }),
    ).toHaveAttribute('aria-checked', 'true')
    // Read back from the engine, not from the click.
    await page.goto(SETTINGS)
    await expect(
      page.getByRole('radio', { name: 'Passkey', exact: true }),
    ).toHaveAttribute('aria-checked', 'true')
  }

  // Put the policy back to Off and read it back from the engine, then close the context.
  // A no-op when enable() never ran. Closing the context also drops the virtual authenticator.
  async restore(): Promise<void> {
    const { context, page } = this
    if (!context) return
    this.context = undefined
    this.page = undefined
    let failure: unknown
    try {
      if (this.raised && page) await this.lower(page)
    } catch (error) {
      failure = error
    }
    try {
      await context.close()
    } catch (closeError) {
      // The policy error says why the run is red; a context already going away does not.
      if (failure === undefined) throw closeError
    }
    if (failure !== undefined) throw failure
  }

  // Lowering needs a session that meets the current level. The administrator session holds the
  // passkey step-up of enable(), which outlasts the capture by far; if it had lapsed, the engine
  // would refuse the write, the radio would never turn on and the wait below would fail loudly.
  private async lower(page: Page): Promise<void> {
    await page.goto(SETTINGS)
    const off = page.getByRole('radio', { name: 'Off', exact: true })
    if ((await off.getAttribute('aria-checked')) !== 'true') {
      await off.click()
      // The radio follows the engine, not the click: it turns on once the write lands. Wait for
      // that before reloading, which would abandon a pending write.
      await expect(
        page.getByRole('radio', { name: 'Off', exact: true, checked: true }),
      ).toBeVisible({ timeout: 15_000 })
    }
    await page.goto(SETTINGS)
    await expect(
      page.getByRole('radio', { name: 'Off', exact: true }),
      'docs-captures: the AAL3 policy was not put back to Off; later captures would show the step-up notice',
    ).toHaveAttribute('aria-checked', 'true', { timeout: 15_000 })
  }
}
