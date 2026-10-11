// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DICTATION IN A REAL BROWSER, against a live `--seed-demo` engine, with a WAV file as the
// microphone (Chromium's fake capture device). It asserts what no unit test can: that the
// engine serves the worker, the runtime and the model, that the words reach the composer
// field and stay there unsent, and that NOTHING during the whole visit, page or worker,
// leaves the engine's origin.
//
// Environment (the spec SKIPS without them): PLAYWRIGHT_BASE_URL and DEMO_TENANT, as in
// scripts/web-e2e-demo.sh; DICTATION_WAV, an absolute path to a WAV of speech, and
// DICTATION_EXPECT, comma-separated words its transcript must contain. Optional:
// DICTATION_LANG (console language, default en), DICTATION_THEME (dark|light),
// DICTATION_WIDTH (default 1280) and DICTATION_CAPTURES, a directory for one screenshot per
// state plus report.json.
//
// ⛔ THE ONE STAND-IN: the demo estate holds discovered sessions, not a running tool, and
// the composer only exists on a session with a live run. The spec answers the run reads
// (/v1/m/sessions/runs) with one labelled running run. Nothing is sent to it: the spec
// never presses Send, and it fails if the field's text is submitted.
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import whisper from '../src/features/dictation/whisper-model.json' with { type: 'json' }

const demoTenant = process.env.DEMO_TENANT ?? ''
const WAV = process.env.DICTATION_WAV ?? ''
const EXPECT = (process.env.DICTATION_EXPECT ?? '')
  .split(',')
  .map((word) => word.trim().toLowerCase())
  .filter(Boolean)
const LANG = process.env.DICTATION_LANG ?? 'en'
const THEME = process.env.DICTATION_THEME === 'light' ? 'light' : 'dark'
const WIDTH = Number(process.env.DICTATION_WIDTH ?? 1280)
const CAPTURES = process.env.DICTATION_CAPTURES ?? ''

const RUN_REF = 'run_dictation_standin'
const RUN = {
  run_ref: RUN_REF,
  name: 'Dictation check (stand-in run)',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'running',
  provider_driver: 'claude',
  last_event_seq: 0,
  created_at: new Date().toISOString(),
  started_at: new Date().toISOString(),
}

test.use({
  viewport: { width: WIDTH, height: WIDTH < 600 ? 844 : 800 },
  colorScheme: THEME,
  permissions: ['microphone'],
  launchOptions: {
    args: [
      '--use-fake-ui-for-media-stream',
      '--use-fake-device-for-media-stream',
      `--use-file-for-fake-audio-capture=${WAV}%noloop`,
      // Any name but the engine's fails to resolve: an egress would surface as an error too.
      '--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1, EXCLUDE localhost',
    ],
  },
})

/** Seconds of audio in a PCM WAV (RIFF header byte rate and data chunk size). */
function wavSeconds(file: string): number {
  const wav = readFileSync(file)
  const byteRate = wav.readUInt32LE(28)
  let offset = 12
  while (offset + 8 <= wav.length) {
    const id = wav.toString('ascii', offset, offset + 4)
    const size = wav.readUInt32LE(offset + 4)
    if (id === 'data') return size / byteRate
    offset += 8 + size
  }
  throw new Error(`${file}: no data chunk`)
}

async function signIn(page: Page) {
  await page.goto('/login')
  await page.locator('input[type=email]').first().fill('demo@olivares.local')
  await page
    .locator('input[type=password]')
    .first()
    .fill('olivares-demo-estate')
  await page.locator('button[type=submit]').first().click()
  await page.waitForURL((url) => !url.pathname.startsWith('/login'), {
    timeout: 20_000,
  })
}

test('dictation fills the composer from the microphone and never leaves the engine', async ({
  page,
  context,
  baseURL,
}) => {
  test.skip(!demoTenant, 'needs DEMO_TENANT (scripts/web-e2e-demo.sh)')
  test.skip(
    !WAV || EXPECT.length === 0,
    'needs DICTATION_WAV and DICTATION_EXPECT',
  )
  test.setTimeout(240_000)
  const origin = new URL(baseURL!).origin
  const seconds = wavSeconds(WAV)

  // Every request of the visit, the dictation worker's included.
  const requests: string[] = []
  const errors: string[] = []
  context.on('request', (request) => requests.push(request.url()))
  page.on('console', (message) => {
    if (message.type() === 'error') errors.push(message.text())
  })
  page.on('pageerror', (error) => errors.push(error.message))

  let sent = false
  await context.route('**/v1/m/sessions/runs**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (request.method() !== 'GET') {
      sent = true
      return route.fulfill({ status: 409, json: { error: 'stand-in run' } })
    }
    if (path.endsWith('/runs')) {
      return route.fulfill({ json: { items: [RUN], has_more: false } })
    }
    if (path.endsWith(`/runs/${RUN_REF}`)) return route.fulfill({ json: RUN })
    return route.fulfill({ json: { items: [], has_more: false, cursor: '' } })
  })
  await context.addInitScript(
    ([theme, lang]) => {
      window.localStorage.setItem('olivares.theme', theme)
      window.localStorage.setItem('olivares.lang', lang)
    },
    [THEME, LANG],
  )

  const shots: string[] = []
  const capture = async (state: string) => {
    if (!CAPTURES) return
    mkdirSync(CAPTURES, { recursive: true })
    const file = join(CAPTURES, `${LANG}-${WIDTH}-${THEME}-${state}.png`)
    await page.screenshot({ path: file })
    shots.push(file)
  }

  await signIn(page)
  await page.goto(`/sessions?session=${encodeURIComponent(`run:${RUN_REF}`)}`)
  const field = page.getByTestId('launcher-input')
  const dictate = page.getByTestId('composer-dictate')
  const status = page.getByTestId('work-composer').getByRole('status')
  await expect(field).toBeVisible({ timeout: 30_000 })
  await expect(dictate).toBeVisible()
  await field.scrollIntoViewIfNeeded()
  await capture('idle')
  const axe = await new AxeBuilder({ page })
    .include('[data-testid="work-composer"]')
    .analyze()
  expect(
    axe.violations.map((v) => `${v.id}: ${v.help}`),
    'axe on the composer with its microphone',
  ).toEqual([])

  const pressed = Date.now()
  await dictate.click()
  await capture('loading')
  await expect(dictate).toHaveAttribute('aria-pressed', 'true', {
    timeout: 120_000,
  })
  const listening = Date.now()
  await capture('listening')
  // The fake microphone plays the file once, from the moment the page opened it, which is
  // when the button turns to listening.
  await page.waitForTimeout(seconds * 1000 + 1500)
  await dictate.click()
  await capture('transcribing')
  await expect(field).not.toHaveValue('', { timeout: 120_000 })
  const transcribed = Date.now()
  await expect(status).toHaveText('')
  await capture('result')

  const transcript = await field.inputValue()
  const foreign = requests.filter(
    (url) => !/^(data|blob):/.test(url) && new URL(url).origin !== origin,
  )
  if (CAPTURES) {
    writeFileSync(
      join(CAPTURES, `${LANG}-${WIDTH}-${THEME}-report.json`),
      JSON.stringify(
        {
          origin,
          wav: WAV,
          wavSeconds: seconds,
          transcript,
          loadMs: listening - pressed,
          transcribeMs: transcribed - listening - seconds * 1000 - 1500,
          voiceFiles: requests.filter((url) => url.includes('/assets/voice/')),
          requests: requests.length,
          foreign,
          errors,
          shots,
        },
        null,
        2,
      ),
    )
  }
  expect(foreign, 'requests that left the engine origin').toEqual([])
  // The positive control: the worker's own fetches were seen, from the engine, and they are
  // the pinned model files and the runtime, nothing else.
  const model = `${origin}/assets/voice/${whisper.revision}/`
  const fetched = new Set(
    requests
      .filter((url) => url.includes('/assets/voice/'))
      .map((url) => url.replace(model, '')),
  )
  expect([...fetched].sort()).toEqual(Object.keys(whisper.files).sort())
  expect(
    requests.some((url) =>
      /\/assets\/ort-wasm-simd-threaded\.asyncify-[^/]+\.wasm$/.test(url),
    ),
    'the runtime came from the engine',
  ).toBe(true)
  expect(
    errors.filter((error) => /Content Security Policy/i.test(error)),
  ).toEqual([])
  for (const word of EXPECT) expect(transcript.toLowerCase()).toContain(word)
  expect(sent, 'the dictated text was submitted').toBe(false)
})
