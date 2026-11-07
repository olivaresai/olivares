// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// MODEL AND EFFORT WITHOUT AN "ADVANCED" STEP, on the production console against a live
// seeded engine whose child is the fixture driver (no provider account, no model turn).
// A person looking for them used to press Advanced in the message bar and got a raw stdin
// line. The fixture's tools are not signed in, so the launch dialog (profile chosen) is
// the form under test; the New session form's own pick is covered by its component tests.
// Run it through scripts/e2e-session-conversation.sh.
import { expect, test, type Page } from '@playwright/test'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { openAdvancedSession } from './session-launch-form'
import { sessionToken } from './live-session'

const demoTenant = process.env.DEMO_TENANT ?? ''
const fixtureOn = process.env.CONVERSATION_FIXTURE === '1'
const EMAIL = 'demo@olivares.local'
const PASSWORD = 'olivares-demo-estate'

test.use({ screenshot: 'off', trace: 'off', video: 'off' })

async function signIn(page: Page) {
  await page.context().addInitScript(() => {
    try {
      window.localStorage.setItem('olivares.theme', 'light')
      window.localStorage.setItem('olivares.lang', 'en')
    } catch {
      /* private mode */
    }
  })
  await page.goto('/login')
  await page.locator('input[type=email]').first().fill(EMAIL)
  await page.locator('input[type=password]').first().fill(PASSWORD)
  await page.locator('button[type=submit]').first().click()
  await page.waitForURL((url) => !url.pathname.startsWith('/login'), {
    timeout: 30_000,
  })
}

test.describe('cached sign-in eligibility in the launch forms', () => {
  test.setTimeout(120_000)
  test.beforeEach(() => {
    test.skip(!demoTenant || !fixtureOn, 'Seeded engine required')
  })

  for (const next of ['key-backed', 'not-ready'] as const) {
    test(`a cached sign-in cannot override a ${next} tool`, async ({
      page,
    }) => {
      let state: 'own-login' | typeof next = 'own-login'
      let changedReadiness = 0
      let catalogReads = 0
      const profile = {
        profile_ref: 'ppf_browser_fixture',
        driver: 'claude',
        environment_ref: 'xenv_fixture',
        display_name: 'Fixture sign-in',
        state: 'active',
        local_environment: true,
        operable: true,
      }
      // Authentication and the shell use the live seeded engine. Only the provider
      // answers are fixtures: this checks a cache/readiness transition without a vendor login.
      await page.route(
        /\/v1\/m\/sessions\/provider-accounts(?:\?|$)/,
        (route) => route.fulfill({ json: { items: [], has_more: false } }),
      )
      await page.route(
        /\/v1\/m\/sessions\/provider-profiles(?:\?|$)/,
        (route) =>
          route.fulfill({
            json: {
              items: [
                profile,
                {
                  ...profile,
                  profile_ref: 'ppf_other_fixture',
                  display_name: 'Other sign-in',
                },
              ],
              has_more: false,
            },
          }),
      )
      await page.route('**/provider-profiles/readiness', (route) => {
        if (state !== 'own-login') changedReadiness++
        return route.fulfill({
          json: {
            tools: [
              {
                driver: 'claude',
                ready: state !== 'not-ready',
                reason: state === 'key-backed' ? 'api_key' : 'own_login',
                ...(state === 'key-backed'
                  ? {
                      provider: {
                        provider_ref: 'prv_fixture',
                        kind: 'anthropic',
                        display_name: 'Fixture key',
                        models: ['fixture-model'],
                        default_model: 'fixture-model',
                      },
                    }
                  : {}),
                ...(state === 'not-ready'
                  ? {
                      code: 'nothing_to_run_on',
                      message: 'Fixture tool is not ready.',
                    }
                  : {}),
              },
              // Keep the launch form mounted when Claude becomes unavailable.
              { driver: 'codex', ready: true, reason: 'own_login' },
            ],
          },
        })
      })
      await page.route('**/provider-profiles/resolve?*', (route) =>
        route.fulfill({
          json: {
            reason: 'own_login',
            ...(new URL(route.request().url()).searchParams.get('driver') ===
            'claude'
              ? { profile }
              : {}),
          },
        }),
      )
      await page.route(
        '**/provider-profiles/ppf_browser_fixture/models',
        (route) => {
          catalogReads++
          return route.fulfill({
            json: {
              profile_ref: profile.profile_ref,
              driver: 'claude',
              state: 'fresh',
              free_text: true,
              default_model: 'fixture-model',
              checked_at: '2026-10-13T09:00:00Z',
              refresh_interval_seconds: 120,
              models: [
                {
                  id: 'fixture-model',
                  display_name: 'Fixture model',
                  is_default: true,
                  effort_levels: ['low', 'high'],
                },
              ],
            },
          })
        },
      )
      await signIn(page)
      // The New session form, through its link (New session itself focuses the composer).
      await page.goto('/sessions?newSession=1')
      const form = page.getByRole('dialog')
      await expect(form).toBeVisible()
      // Store an explicit tool choice; the first-ready fallback would move to Codex
      // when Claude becomes unavailable and would not exercise Claude's cached data.
      const tool = form.getByRole('combobox', { name: 'Tool', exact: true })
      await tool.click()
      await page.getByRole('option', { name: 'Codex', exact: true }).click()
      await tool.click()
      await page
        .getByRole('option', { name: 'Claude Code', exact: true })
        .click()
      await expect.poll(() => catalogReads).toBeGreaterThan(0)
      await expect(
        form.getByRole('button', { name: 'Start', exact: true }),
      ).toBeEnabled()
      const initialCatalogReads = catalogReads
      // The first form really fetched and cached the reuse sign-in under this authority.
      state = next
      // Let the existing 10-second readiness freshness window expire. Visibility changes
      // use TanStack's normal refetch path; no cache injection or page reload is involved.
      await expect
        .poll(
          async () => {
            await page.evaluate(() =>
              window.dispatchEvent(new Event('visibilitychange')),
            )
            return changedReadiness
          },
          { timeout: 25_000, intervals: [250, 1_000] },
        )
        .toBeGreaterThan(0)
      if (next === 'key-backed')
        await expect(
          form.getByRole('combobox', { name: 'Model', exact: true }),
        ).toContainText('fixture-model')
      else
        await expect(
          form.getByRole('button', { name: 'Start', exact: true }),
        ).toBeDisabled()
      await form
        .getByRole('button', { name: 'More options', exact: true })
        .click()
      await form
        .getByRole('button', { name: 'Advanced launch options', exact: true })
        .click()
      await expect(
        form.getByRole('combobox', { name: 'Account', exact: true }),
      ).toContainText('Select an account')
      await expect(
        form.getByRole('button', { name: 'Start', exact: true }),
      ).toBeDisabled()
      expect(catalogReads).toBe(initialCatalogReads)
    })
  }
})

test.describe('model and effort are chosen where a session is started', () => {
  test.setTimeout(120_000)

  test.beforeEach(() => {
    test.skip(
      !demoTenant || !fixtureOn,
      'DEMO_TENANT and CONVERSATION_FIXTURE=1 required — run through scripts/e2e-session-conversation.sh',
    )
  })

  for (const width of [1280, 390]) {
    test(`message-bar Fast Off reaches a new live session (${width})`, async ({
      page,
    }, info) => {
      await page.setViewportSize({ width, height: 900 })
      await page.route('**/provider-profiles/*/models', (route) =>
        route.fulfill({
          json: {
            state: 'fresh',
            default_model: 'fixture-claude',
            models: [{ id: 'fixture-claude', supports_fast_mode: true }],
          },
        }),
      )
      await signIn(page)
      await page.goto(
        `/sessions?session=run:${process.env.E2E_CAPTURE_RUN_REF}&pane=narrative`,
      )
      const composer = page.getByTestId('work-composer')
      await composer.getByTestId('composer-tool-model').click()
      await page
        .getByRole('combobox', { name: 'Fast mode', exact: true })
        .click()
      await page.getByRole('option', { name: 'Off', exact: true }).click()
      await page.screenshot({
        path: info.outputPath(`composer-speed-${width}.png`),
      })
      await page.getByTestId('composer-new-with-choice').click()
      const dialog = page.getByRole('dialog')
      await expect(
        dialog.getByRole('combobox', { name: 'Fast mode', exact: true }),
      ).toContainText('Off')
      await expect(
        dialog.getByRole('button', { name: 'Advanced options', exact: true }),
      ).toHaveAttribute('aria-expanded', 'false')
      const response = page.waitForResponse(
        (res) =>
          res.url().endsWith('/v1/m/sessions/runs') &&
          res.request().method() === 'POST',
      )
      await dialog.getByRole('button', { name: 'Start', exact: true }).click()
      const launched = await response
      expect(launched.status()).toBe(201)
      expect(launched.request().postDataJSON().fast_mode).toBe(false)
      const run = await launched.json()
      expect(run.fast_mode).toBe(false)
      const saved = await page.request.get(
        `/v1/m/sessions/runs/${run.run_ref}`,
        {
          headers: {
            Authorization: `Bearer ${await sessionToken(page)}`,
            'X-Olivares-Tenant': demoTenant,
          },
        },
      )
      expect(saved.status()).toBe(200)
      expect((await saved.json()).fast_mode).toBe(false)
    })
  }

  test('a model and its effort are set in front of Advanced options and shown on the session', async ({
    page,
  }) => {
    await signIn(page)
    await page.setViewportSize({ width: 1280, height: 900 })
    const dialog = await openAdvancedSession(page)
    // Model and effort are on the form itself: Advanced options stays closed.
    await expect(
      dialog.getByRole('button', { name: 'Advanced options', exact: true }),
    ).toHaveAttribute('aria-expanded', 'false')
    await dialog.getByRole('button', { name: 'Model', exact: true }).click()
    await page.getByPlaceholder('Search models').fill('sonnet')
    await page.getByText('Use “sonnet”').click()
    await dialog.getByRole('combobox', { name: 'Effort', exact: true }).click()
    await page.getByRole('option', { name: 'high', exact: true }).click()
    await dialog.screenshot({ path: 'test-results/model-effort-form.png' })

    const created = page.waitForResponse(
      (r) =>
        r.request().method() === 'POST' &&
        r.url().endsWith('/v1/m/sessions/runs'),
    )
    await dialog.getByRole('button', { name: 'Start', exact: true }).click()
    const response = await created
    expect(response.ok()).toBeTruthy()
    expect(response.request().postDataJSON()).toMatchObject({
      model: 'sonnet',
      effort: 'high',
    })

    // The composer says what the session runs on, by name: the model the engine
    // launched `sonnet` on, never the alias; the header names the session
    // and its tool only.
    const composer = page.getByTestId('work-composer')
    await expect(composer).toHaveAttribute('data-attached', 'true', {
      timeout: 30_000,
    })
    await expect(page.getByTestId('thread-tool')).toHaveText('Claude Code')
    // Model and effort are two pickers.
    await expect(composer.getByTestId('composer-tool-model')).toHaveText(
      'Claude Sonnet 5.5',
    )
    await expect(composer.getByTestId('composer-effort')).toHaveText('High')
    await expect(composer.getByTestId('composer-tool-model')).toHaveAttribute(
      'title',
      /claude-sonnet-5-5/,
    )
    // A production console shows no raw stdin line and no control named Advanced.
    await expect(composer.getByText(/advanced/i)).toHaveCount(0)
    await expect(composer.getByTestId('composer-wire-input')).toHaveCount(0)
    await page.screenshot({ path: 'test-results/model-effort-session.png' })
  })
})

test.describe('folder-first launch', () => {
  // Provider discovery and fresh-tenant reads are stand-ins; launch, Files and Terminal use the engine.
  test('the first composer requires a folder and starts its selected model there', async ({
    page,
  }) => {
    test.skip(!demoTenant || !fixtureOn, 'Seeded engine required')
    test.setTimeout(120_000)
    writeFileSync(
      join(process.env.E2E_SESSION_FOLDER!, 'folder-first.txt'),
      'hello\n',
    )
    await signIn(page)
    const headers = {
      Authorization: `Bearer ${await sessionToken(page)}`,
      'X-Olivares-Tenant': demoTenant,
    }
    const profiles = await page.request.get(
      '/v1/m/sessions/provider-profiles',
      { headers },
    )
    expect(profiles.ok()).toBe(true)
    const profile = (await profiles.json()).items.find(
      (p: { display_name: string }) => p.display_name === 'Fixture Claude',
    )
    expect(profile).toBeTruthy()
    await page.route('**/provider-profiles/readiness', (route) =>
      route.fulfill({
        json: {
          tools: [{ driver: 'claude', ready: true, reason: 'own_login' }],
        },
      }),
    )
    await page.route(/\/provider-profiles\/resolve(?:\?.*)?$/, (route) =>
      route.fulfill({ json: { reason: 'own_login', profile } }),
    )
    await page.route('**/provider-profiles/*/models', (route) =>
      route.fulfill({
        json: {
          profile_ref: profile.profile_ref,
          driver: 'claude',
          state: 'fresh',
          free_text: false,
          default_model: 'opus',
          checked_at: new Date().toISOString(),
          refresh_interval_seconds: 120,
          models: [
            { id: 'opus', is_default: true, effort_levels: ['low', 'high'] },
            { id: 'sonnet', effort_levels: ['low', 'high'] },
          ],
        },
      }),
    )
    // No registered or remembered folder on this initial read, as on a fresh tenant.
    await page.route(
      /\/v1\/m\/sessions\/(?:workspaces|runs)(?:\?.*)?$/,
      async (route) => {
        if (route.request().method() === 'GET')
          await route.fulfill({ json: { items: [], has_more: false } })
        else await route.continue()
      },
    )
    await page.goto('/sessions')
    await page
      .locator('#work-pane-rail')
      .getByRole('button', { name: 'New session', exact: true })
      .click()
    const folder = page.getByRole('button', {
      name: 'Folder: Choose a folder',
      exact: true,
    })
    await expect(folder).toBeVisible()
    const message = page.getByRole('textbox', {
      name: 'First message (optional)',
    })
    await message.fill('Inspect this folder')
    await message.press('Enter')
    await expect(
      page.getByRole('button', { name: 'Start', exact: true }),
    ).toBeDisabled()
    await page.getByRole('button', { name: /^Model: / }).click()
    await page.getByRole('option', { name: /Sonnet/i }).click()
    await page.getByRole('button', { name: /^Effort: / }).click()
    await page.getByRole('menuitemradio', { name: 'High', exact: true }).click()
    await folder.click()
    await page.getByRole('menuitemradio', { name: 'Another folder…' }).click()
    await page
      .getByRole('textbox', { name: 'Folder', exact: true })
      .fill(process.env.E2E_SESSION_FOLDER!)
    // Subsequent reads are real: Files and Terminal must resolve the folder just launched.
    await page.unroute(/\/v1\/m\/sessions\/(?:workspaces|runs)(?:\?.*)?$/)
    const launched = page.waitForResponse(
      (r) =>
        r.url().endsWith('/v1/m/sessions/runs') &&
        r.request().method() === 'POST',
    )
    await message.press('Enter')
    const response = await launched
    expect(response.status()).toBe(201)
    const run = await response.json()
    expect(run.workspace_ref).toBeTruthy()
    expect(response.request().postDataJSON()).toMatchObject({
      model: 'sonnet',
      effort: 'high',
    })
    expect(run.model_ref).toBe('claude-sonnet-5-5')
    expect(run.effort).toBe('high')
    await page.goto(
      `/sessions?session=run:${run.run_ref}&panel=files&pane=context`,
    )
    await expect(page.getByTestId('context-no-files')).toHaveCount(0)
    await expect(
      page.getByRole('radio', { name: 'Files', exact: true }),
    ).toHaveAttribute('aria-checked', 'true')
    await expect(
      page.getByText('folder-first.txt', { exact: true }).first(),
    ).toBeVisible()
    await page.getByRole('radio', { name: 'Terminal', exact: true }).click()
    const terminalOpened = page.waitForResponse(
      (r) =>
        /\/terminals$/.test(new URL(r.url()).pathname) &&
        r.request().method() === 'POST',
    )
    await page
      .getByRole('button', { name: 'Open terminal', exact: true })
      .click()
    expect((await terminalOpened).status()).toBe(201)
    await expect(page.getByTestId('terminal-screen')).toBeVisible()
    await page.locator('.xterm-helper-textarea').focus()
    await page.keyboard.type('pwd')
    await page.keyboard.press('Enter')
    await expect(page.getByTestId('terminal-screen')).toContainText(
      process.env.E2E_SESSION_FOLDER!,
    )
    await page
      .getByRole('button', { name: 'Close terminal', exact: true })
      .click()
    await page
      .locator('#work-pane-rail')
      .getByRole('button', { name: 'New session', exact: true })
      .click()
    await page.getByRole('button', { name: /^Folder: / }).click()
    await page
      .getByRole('menuitemradio', {
        name: 'A new folder for this session',
        exact: true,
      })
      .click()
    const privateLaunch = page.waitForResponse(
      (r) =>
        r.url().endsWith('/v1/m/sessions/runs') &&
        r.request().method() === 'POST',
    )
    await page
      .getByRole('textbox', { name: 'First message (optional)' })
      .fill('Work without a registered folder')
    await page.getByRole('button', { name: 'Start', exact: true }).click()
    const privateResponse = await privateLaunch
    expect(privateResponse.status()).toBe(201)
    const privateRun = await privateResponse.json()
    expect(privateRun.workspace_ref).toBeFalsy()
    await page.goto(
      `/sessions?session=run:${privateRun.run_ref}&pane=context&panel=files`,
    )
    for (const name of ['Changes', 'Files', 'Terminal'])
      await expect(page.getByRole('radio', { name, exact: true })).toHaveCount(
        0,
      )
    await expect(
      page.getByRole('radio', { name: 'Details', exact: true }),
    ).toHaveAttribute('aria-checked', 'true')
    console.log(
      `folder-first: run model=${run.model_ref}, effort=${run.effort}; Files listed; terminal opened; private folder hides unavailable panes`,
    )
  })
})
