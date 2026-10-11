// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test, type Page } from '@playwright/test'
import { createServer, preview } from 'vite'
import { signInFixture } from './fixture-sign-in'

const credentials = {
  email: 'proxy-admin@example.test',
  password: 'disposable-proxy-browser-password',
}

// Uses a virgin engine, just like smoke.spec.ts. Never retain authentication
// bodies, cookies, traces, or automatic password-field snapshots.
test.use({ trace: 'off', screenshot: 'off', video: 'off' })

async function browserSessionFlow(page: Page, origin: string) {
  await page.context().clearCookies()
  const headers = {
    Origin: origin,
    'Sec-Fetch-Site': 'same-origin',
    'X-Olivares-Session': 'cookie',
  }
  const login = await page.request.post(`${origin}/v1/auth/login`, {
    headers,
    data: credentials,
  })
  expect(login.status(), 'same-origin cookie login').toBe(200)
  await page.context().clearCookies()
  await signInFixture(page, credentials, 30_000)
  await expect(page).toHaveURL(`${origin}/`)
  await expect(
    page.getByLabel('Journeys').getByRole('link', { name: 'Now', exact: true }),
  ).toBeVisible()
  const cookies = await page.context().cookies()
  const cookie = cookies.find(
    (entry) => entry.name === '__Host-olivares-session',
  )
  expect(
    Boolean(cookie?.httpOnly && cookie.secure && cookie.sameSite === 'Strict'),
  ).toBe(true)
  await page.reload()
  await expect(
    page.getByLabel('Journeys').getByRole('link', { name: 'Now', exact: true }),
  ).toBeVisible()

  async function sessionRequest(
    method: 'GET' | 'POST',
    path: string,
    extraHeaders: Record<string, string> = {},
  ) {
    // Chromium accepts Secure cookies on loopback HTTP; Playwright's API client
    // does not send them there automatically. Use the real browser cookie, read
    // again after rotation, for the API-level denial probes on both transports.
    const currentCookie = (await page.context().cookies()).find(
      (entry) => entry.name === '__Host-olivares-session',
    )
    try {
      return await page.request.fetch(`${origin}${path}`, {
        method,
        headers: {
          ...headers,
          Cookie: currentCookie
            ? `${currentCookie.name}=${currentCookie.value}`
            : '',
          ...extraHeaders,
        },
      })
    } catch {
      throw new Error(
        'Disposable session request failed; no auth values retained',
      )
    }
  }
  const metadata = await sessionRequest('GET', '/v1/auth/browser-session')
  expect(metadata.status()).toBe(200)
  const session = await metadata.json()
  expect(typeof session.csrf_token).toBe('string')
  expect(Object.hasOwn(session, 'token')).toBe(false)

  const attacks: Record<string, string>[] = [
    { Origin: 'https://attacker.example' },
    { Origin: 'null' },
    { Origin: `${origin.replace(/:\d+$/, '')}:1` },
    {
      Origin: origin.startsWith('https:')
        ? origin.replace(/^https:/, 'http:')
        : origin.replace(/^http:/, 'https:'),
    },
    { 'Sec-Fetch-Site': 'cross-site' },
    { 'Sec-Fetch-Site': 'same-site' },
  ]
  if (origin !== process.env.VITE_API_TARGET) {
    attacks.push(
      { Origin: process.env.VITE_API_TARGET! },
      {
        Origin: 'https://attacker.example',
        'X-Forwarded-Host': 'attacker.example',
        'X-Forwarded-Proto': 'https',
      },
    )
  }
  for (const attack of attacks) {
    const denied = await page.request.post(`${origin}/v1/auth/login`, {
      headers: { ...headers, ...attack },
      data: credentials,
    })
    expect(denied.status(), 'cross-origin cookie login').toBe(403)
  }
  const badPassword = await page.request.post(`${origin}/v1/auth/login`, {
    headers,
    data: { ...credentials, password: 'incorrect-disposable-password' },
  })
  expect(badPassword.status(), 'password validation').toBe(401)

  for (const csrf of ['', 'incorrect-csrf']) {
    const denied = await sessionRequest('POST', '/v1/auth/refresh', {
      'X-CSRF-Token': csrf,
    })
    expect(denied.status(), 'invalid CSRF refresh').toBe(403)
  }
  const crossOrigin = await sessionRequest('POST', '/v1/auth/refresh', {
    Origin: 'https://attacker.example',
    'X-CSRF-Token': session.csrf_token,
  })
  expect(crossOrigin.status()).toBe(403)

  const refreshed = await sessionRequest('POST', '/v1/auth/refresh', {
    'X-CSRF-Token': session.csrf_token,
  })
  expect(refreshed.status()).toBe(200)
  const rotated = await refreshed.json()
  expect(rotated.csrf_token !== session.csrf_token).toBe(true)
  const logout = await sessionRequest('POST', '/v1/auth/logout', {
    'X-CSRF-Token': rotated.csrf_token,
  })
  expect(logout.status()).toBe(204)
  const loggedOut = await sessionRequest('GET', '/v1/auth/browser-session')
  expect(loggedOut.status()).toBe(401)
}

test('embedded and Vite dev/preview preserve cookie sessions and CSRF', async ({
  page,
  browser,
  baseURL,
}) => {
  test.setTimeout(180_000)
  expect(baseURL, 'live engine URL').toBeTruthy()
  expect(
    process.env.PLAYWRIGHT_SETUP_TOKEN,
    'virgin engine setup token',
  ).toBeTruthy()
  try {
    await page.goto('/setup')
    await page.locator('#token').fill(process.env.PLAYWRIGHT_SETUP_TOKEN!)
    await page.locator('#setup-email').fill(credentials.email)
    await page.locator('#setup-password').fill(credentials.password)
    await page.getByRole('button', { name: /create administrator/i }).click()
    await page.waitForURL((url) => url.pathname !== '/setup', {
      timeout: 30_000,
    })
  } catch {
    throw new Error('Could not set up the disposable proxy engine')
  }

  const previousTarget = process.env.VITE_API_TARGET
  process.env.VITE_API_TARGET = baseURL!
  try {
    await browserSessionFlow(page, baseURL!)
    for (const mode of ['dev', 'preview'] as const) {
      const options = {
        server: { host: '127.0.0.1', port: 0, open: false },
        preview: { host: '127.0.0.1', port: 0, open: false },
      }
      const server =
        mode === 'dev' ? await createServer(options) : await preview(options)
      try {
        if ('listen' in server) await server.listen()
        const origin = server.resolvedUrls!.local[0].replace(/\/$/, '')
        for (const path of ['/healthz', '/openapi.json']) {
          const response = await page.request.get(`${origin}${path}`)
          expect(response.status(), `${mode} ${path}`).toBe(200)
        }
        const proxyPage = await browser.newPage({ baseURL: origin })
        try {
          await browserSessionFlow(proxyPage, origin)
        } finally {
          await proxyPage.close()
        }
      } finally {
        await server.close()
      }
    }
  } finally {
    if (previousTarget === undefined) delete process.env.VITE_API_TARGET
    else process.env.VITE_API_TARGET = previousTarget
  }
})
