// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE CONSOLE HALF OF THE GOLDEN PATH. `scripts/e2e-golden-path.sh` boots one engine,
// walks the CLI half against it and then hands this spec the SAME engine, the SAME
// administrator and the SAME provider profile. Nothing here is mocked, seeded or
// intercepted: the run it starts is a real governed session and the turn it sends is a
// real model turn.
//
// ⛔ IT IS NOT A SCREENSHOT TEST. The captures exist so a person can look; the
//    ASSERTIONS are what fail. A leg that could only be judged by eye is recorded in
//    the result file as an observation with its evidence, never asserted as a pass.
//
// ⛔ IT REPORTS BACK IN A FILE, because the run it starts is one half of a claim the
//    harness makes: that the CLI and the console describe ONE system. E2E_RESULT gets
//    the run this spec launched and every address its rail is showing, and the harness
//    compares that against `agent session ls`. A spec that only asserted against itself
//    could not have caught the disagreement it exists to catch.
//
// Environment (all set by the harness; the spec SKIPS rather than inventing them):
//   PLAYWRIGHT_BASE_URL  the engine this run booted
//   E2E_EMAIL/E2E_PASSWORD   the administrator `auth bootstrap` created
//   E2E_PROFILE_REF      the profile `agent deploy` registered
//   E2E_PROMPT           the bounded prompt (a few tokens)
//   E2E_CLI_RUN          the run the CLI half launched
//   E2E_CAPTURES         where the PNGs go
//   E2E_RESULT           where this spec writes what it saw
//   E2E_MODEL_TURN       "0" walks the path without sending a prompt
import { expect, test, type Locator, type Page } from '@playwright/test'
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { signInFixture } from './fixture-sign-in'

const EMAIL = process.env.E2E_EMAIL ?? ''
const PASSWORD = process.env.E2E_PASSWORD ?? ''
const PROFILE_REF = process.env.E2E_PROFILE_REF ?? ''
const PROFILE_NAME = process.env.E2E_PROFILE_NAME ?? ''
const PROMPT = process.env.E2E_PROMPT ?? 'Reply with the single word OK'
const CLI_RUN = process.env.E2E_CLI_RUN ?? ''
const CLI_RUN_NAME = process.env.E2E_CLI_RUN_NAME ?? ''
const CAPTURES = process.env.E2E_CAPTURES ?? 'playwright-report/golden-path'
const RESULT = process.env.E2E_RESULT ?? ''
const MODEL_TURN = process.env.E2E_MODEL_TURN !== '0'

// This spec signs a real administrator in and types a real password. The runner must not
// retain either in an automatic artifact.
test.use({ screenshot: 'off', trace: 'off', video: 'off' })

type Leg = { leg: string; result: string; ms: number; evidence: string }

const result: {
  console_run_ref: string
  console_answer: string
  rail_addresses: string[]
  cli_run_addressable: boolean
  observations: string[]
  robustness: Leg[]
  defects: { owner: string; what: string; evidence: string }[]
} = {
  console_run_ref: '',
  console_answer: '',
  rail_addresses: [],
  cli_run_addressable: false,
  observations: [],
  robustness: [],
  defects: [],
}

function save() {
  if (!RESULT) return
  writeFileSync(RESULT, JSON.stringify(result, null, 2))
}

function observe(line: string) {
  result.observations.push(line)
  console.log(`    observation: ${line}`)
  save()
}

// A defect found in the browser is reported to the HARNESS rather than thrown, so that
// every defect in this lane is numbered once, in one table, with one owner — and so that
// finding one does not abort the legs after it.
function defect(owner: string, what: string, evidence: string) {
  result.defects.push({ owner, what, evidence })
  console.log(`    ⛔ ${owner}: ${what}`)
  save()
}

function leg(l: Leg) {
  result.robustness.push(l)
  console.log(`    console leg: ${l.leg} → ${l.result} (${l.ms} ms)`)
  save()
}

async function shot(page: Page, name: string) {
  mkdirSync(CAPTURES, { recursive: true })
  await page.screenshot({
    path: join(CAPTURES, `${name}.png`),
    fullPage: false,
  })
}

async function signIn(page: Page) {
  await signInFixture(page, { email: EMAIL, password: PASSWORD }, 30_000)
}

/** Home owns the draft; /sessions may already be attached to the CLI's run. */
async function draftComposer(page: Page) {
  await page.goto('/')
  const composer = page.getByTestId('work-composer')
  await expect(composer).toBeVisible({ timeout: 30_000 })
  await expect(composer).not.toHaveAttribute('data-attached', 'true')
  await composer
    .locator(
      '[data-testid="launcher-input"], [data-testid="launcher-add-provider"], [data-testid="launcher-blocked"]',
    )
    .waitFor({ timeout: 30_000 })
  await expect(composer.getByTestId('launcher-input')).toBeVisible()
  return composer
}

async function chooseProfile(page: Page, composer: Locator) {
  await composer.getByTestId('launcher-profile').click()
  // Radix paints the display name; its internal value is not a DOM contract. A
  // missing or ambiguous name fails here. The launch POST corroborates the full ref.
  const wanted = page.getByRole('option', {
    name: PROFILE_NAME || PROFILE_REF,
    exact: true,
  })
  await expect(
    wanted,
    'exactly one option must name the CLI profile',
  ).toHaveCount(1)
  await wanted.click()
}

function identifier(page: Page, label: string, value: string) {
  return page
    .getByTestId('context-identifiers')
    .locator('dl > div')
    .filter({ has: page.getByText(label, { exact: true }) })
    .getByTestId('ref-chip')
    .and(page.getByTitle(value, { exact: true }))
    .and(page.getByRole('button', { name: `Copy ${value}`, exact: true }))
}

async function runIsVisible(page: Page, runRef: string, name: string) {
  const run = identifier(page, 'Run reference', runRef)
  const profile = identifier(page, 'Provider profile', PROFILE_REF)
  const heading = page
    .getByTestId('session-narrative')
    .getByRole('heading', { level: 2, name, exact: true })
  return (
    (await run.count()) === 1 &&
    (await run.isVisible()) &&
    (await profile.count()) === 1 &&
    (await profile.isVisible()) &&
    (await heading.count()) === 1 &&
    (await heading.isVisible())
  )
}

function record(value: unknown): Record<string, unknown> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('expected a JSON object; payload omitted')
  }
  return value as Record<string, unknown>
}

function answerKind(raw: string): 'assistant' | 'result' | null {
  for (const line of raw.split('\n')) {
    let frame: Record<string, unknown>
    try {
      frame = record(JSON.parse(line))
    } catch {
      continue
    }
    if (
      frame.type === 'result' &&
      frame.subtype === 'success' &&
      frame.is_error !== true
    )
      return 'result'
    if (frame.type === 'assistant') {
      const message = frame.message
      if (
        message === null ||
        typeof message !== 'object' ||
        Array.isArray(message)
      )
        continue
      const content = record(message).content
      if (typeof content === 'string' && content.trim()) return 'assistant'
      if (
        Array.isArray(content) &&
        content.some((block: unknown) => {
          if (
            block === null ||
            typeof block !== 'object' ||
            Array.isArray(block)
          )
            return false
          const part = record(block)
          return (
            part.type === 'text' &&
            typeof part.text === 'string' &&
            !!part.text.trim()
          )
        })
      )
        return 'assistant'
    }
  }
  return null
}

test.describe('the golden path, in the console', () => {
  // Signing in, launching a real session and waiting for a real model turn is three
  // round trips to a loaded box and one to a provider. The default 30 s budget measures
  // the harness, not the product.
  test.setTimeout(300_000)

  test.beforeEach(() => {
    test.skip(
      !EMAIL || !PASSWORD || !PROFILE_REF,
      'run this through scripts/e2e-golden-path.sh: it supplies the engine, the administrator and the profile',
    )
  })

  test('log in, start a real session from the composer, watch it answer', async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    const t0 = Date.now()
    await signIn(page)
    leg({
      leg: 'console: log in as the administrator the CLI created',
      result: 'signed in',
      ms: Date.now() - t0,
      evidence: '01-login-1440.png',
    })
    await shot(page, '01-signed-in-1440')

    // THE WORK SURFACE. This is the screen the product's own design calls the place work
    // happens, so the walk opens it directly rather than through a menu it might reach.
    await page.goto('/sessions')
    await expect(page.getByTestId('work-surface')).toBeVisible({
      timeout: 30_000,
    })
    await shot(page, '02-work-surface-1440')

    // The supported Home draft must launch with the SAME profile the CLI deployed.
    const draft = await draftComposer(page)
    const consoleName = 'e2e-console'
    await draft.getByTestId('launcher-input').fill(consoleName)
    await chooseProfile(page, draft)
    await expect(draft.getByTestId('launcher-start')).toBeEnabled()
    await shot(page, '03-composer-ready-1440')

    const launchStart = Date.now()
    const launchDeadline = launchStart + 60_000
    const origin = new URL(page.url()).origin
    // Observe the real response without interception. Only the expected identity
    // fields are asserted: never retain headers, credentials or full payloads.
    const [created] = await Promise.all([
      page.waitForResponse(
        (response) => {
          const url = new URL(response.url())
          return (
            response.request().method() === 'POST' &&
            url.origin === origin &&
            url.pathname === '/v1/m/sessions/runs'
          )
        },
        { timeout: 60_000 },
      ),
      draft.getByTestId('launcher-start').click(),
    ])
    expect(created.ok(), 'the real session creation must be accepted').toBe(
      true,
    )
    const submitted = record(created.request().postDataJSON())
    expect(submitted.provider_profile_ref, 'submitted profile identity').toBe(
      PROFILE_REF,
    )
    expect(submitted.name, 'submitted session name').toBe(consoleName)
    const run = record(await created.json())
    expect(run.provider_profile_ref, 'created profile identity').toBe(
      PROFILE_REF,
    )
    expect(run.name, 'created session name').toBe(consoleName)
    const runRef = run.run_ref
    if (typeof runRef !== 'string' || !runRef) {
      throw new Error('creation returned no run reference; payload omitted')
    }
    const address = `run:${runRef}`
    await page.waitForURL(
      (url) =>
        url.pathname === '/sessions' &&
        url.searchParams.get('session') === address,
      // Response observation and navigation share the existing launch budget.
      { timeout: Math.max(1, launchDeadline - Date.now()) },
    )
    await expect
      .poll(() => runIsVisible(page, runRef, consoleName), {
        message:
          'the created run and profile must have exact copyable references beside the session name',
        timeout: 60_000,
      })
      .toBe(true)
    // Failed creation, navigation or identity readback leaves this empty. The
    // independent harness must never receive a substituted run as launch evidence.
    result.console_run_ref = runRef
    save()
    leg({
      leg: 'console: start a governed session from the composer',
      result: `run ${runRef}, profile ${PROFILE_REF}, named ${consoleName}`,
      ms: Date.now() - launchStart,
      evidence: '04-launched-1440.png',
    })
    await shot(page, '04-launched-1440')

    const attached = page.getByTestId('work-composer')
    await expect(attached).toHaveAttribute('data-attached', 'true')
    if (MODEL_TURN) {
      // A sentence now belongs on the work surface. The product chooses {text}
      // or the historical driver's user frame; the operator need not type NDJSON.
      const box = attached.getByTestId('launcher-input')
      const candidates = page
        .getByTestId('session-conversation')
        .locator(
          '[data-testid="conversation-item"][data-kind="assistant"], [data-testid="conversation-item"][data-kind="result"]',
        )
      await expect(
        candidates,
        'a new run must not supply an earlier answer as this turn',
      ).toHaveCount(0)
      const turnStart = Date.now()
      await expect(
        box,
        'the launching administrator can send a turn',
      ).toBeEnabled()
      await box.fill(PROMPT)
      const [accepted] = await Promise.all([
        page.waitForResponse(
          (response) => {
            const url = new URL(response.url())
            return (
              response.request().method() === 'POST' &&
              url.origin === origin &&
              url.pathname ===
                `/v1/m/sessions/runs/${encodeURIComponent(runRef)}/input`
            )
          },
          { timeout: 30_000 },
        ),
        attached.getByTestId('composer-send').click(),
      ])
      expect(accepted.ok(), 'the exact run accepted the submitted turn').toBe(
        true,
      )
      const turn = record(accepted.request().postDataJSON())
      if (typeof turn.text === 'string') {
        expect(turn.text, 'the sentence sent to this run').toBe(PROMPT)
      } else {
        expect(
          typeof turn.line,
          'the historical driver receives a user frame',
        ).toBe('string')
        const frame = record(JSON.parse(String(turn.line)))
        expect(frame.type).toBe('user')
        const message = record(frame.message)
        expect(message.role).toBe('user')
        expect(message.content).toBe(PROMPT)
      }
      await expect(box).toHaveValue('')

      let streamed = ''
      let kind: 'assistant' | 'result' | null = null
      let answered = false
      const observationStart = performance.now()
      const observation = {
        stage: 'poll',
        failedStage: '',
        pending: false,
        completed: 0,
        lastCompletedAt: observationStart,
      }
      try {
        await expect
          .poll(
            async () => {
              observation.pending = true
              try {
                observation.stage = 'candidate-count'
                if (!(await candidates.count())) return false
                // A marker locates a candidate; the inspector's actual source
                // must prove assistant text or a successful result.
                observation.stage = 'candidate-selection'
                await candidates.last().click({ timeout: 1000 })
                const wire = page.getByTestId('context-wire')
                observation.stage = 'wire-visibility'
                if (!(await wire.isVisible())) return false
                observation.stage = 'wire-read'
                streamed = await wire.innerText({ timeout: 1000 })
                observation.stage = 'frame-classification'
                kind = answerKind(streamed)
                return kind !== null
              } catch {
                // A failed read is terminal for this poll, never a false/empty
                // observation. Discard the exception's potentially sensitive text.
                observation.failedStage = observation.stage
                throw new Error('golden output observation failed')
              } finally {
                observation.pending = false
                if (!observation.failedStage) {
                  observation.completed++
                  observation.lastCompletedAt = performance.now()
                }
              }
            },
            { timeout: 180_000, intervals: [1000] },
          )
          .toBe(true)
        answered = true
      } catch (error) {
        const elapsed = Math.round(performance.now() - observationStart)
        const matcher =
          error instanceof Error && 'matcherResult' in error
            ? error.matcherResult
            : null
        // The pinned poll returns a failed toBe matcher on budget exhaustion.
        // Its next 1000ms sample may not fit; a shorter test deadline or a still
        // pending read cannot certify this observation window as complete.
        const exhausted =
          matcher !== null &&
          typeof matcher === 'object' &&
          !Array.isArray(matcher) &&
          record(matcher).name === 'toBe' &&
          record(matcher).pass === false
        const completeWindow =
          observation.completed > 0 &&
          observation.lastCompletedAt - observationStart >= 179_000
        if (
          !observation.failedStage &&
          !observation.pending &&
          exhausted &&
          completeWindow
        ) {
          defect(
            'console',
            `the output polling window exhausted after ${elapsed} ms and ${observation.completed} successful observations; no inspectable assistant text or successful result frame was observed`,
            '05-answer-1440.png',
          )
        } else {
          const stage =
            observation.failedStage ||
            (observation.pending ? observation.stage : 'poll')
          const failureClass = observation.failedStage
            ? 'callback-failure'
            : observation.pending
              ? 'unfinished-read'
              : !observation.completed
                ? 'no-observations'
                : !exhausted
                  ? 'unexpected-poll-failure'
                  : 'incomplete-window'
          defect(
            'instrument',
            `output observation failed: stage=${stage}, class=${failureClass}, elapsed_ms=${elapsed}; model answer status is unknown`,
            '05-answer-1440.png',
          )
        }
      }
      if (answered && kind) {
        result.console_answer = streamed.slice(-4000)
        save()
        leg({
          leg: 'console: a real model turn, sent and streamed back',
          result: `${kind} frame inspected (${streamed.length} characters)`,
          ms: Date.now() - turnStart,
          evidence: '05-answer-1440.png',
        })
      }
      await shot(page, '05-answer-1440')
    }

    // THE INSPECTOR'S SCOPE. What a session runs as is the console's answer to "under
    // whose authority did this happen", and it must be on the screen, not in a log.
    await attached.getByTestId('composer-advanced').click()
    const scope = attached.getByTestId('work-scope-line')
    if (await scope.isVisible()) {
      const line = (await scope.innerText()).replace(/\s+/g, ' ')
      await expect(scope).toContainText('Organization')
      await expect(scope).toContainText('Workspace')
      await expect(scope).toContainText('Environment')
      observe(`scope line: ${line}`)
      // WHOSE AUTHORITY THIS RAN UNDER is the one question this line exists to answer,
      // and an opaque identifier does not answer it. The organization has a NAME — the
      // administrator typed it into `auth bootstrap` — and the line prints the uuid.
      if (
        /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/.test(
          line.split(' · ')[0] ?? '',
        )
      ) {
        defect(
          'console',
          `the scope line identifies the organization by a raw uuid instead of the name its administrator gave it: "${line}"`,
          '06-inspector-1440.png',
        )
      }
    } else {
      defect(
        'console',
        'the work surface renders no scope line, so the screen does not say whose authority the work runs under',
        '06-inspector-1440.png',
      )
    }
    await page.goto(
      `/sessions?session=${encodeURIComponent(address)}&pane=context`,
    )
    await expect(page.getByTestId('session-context')).toBeVisible({
      timeout: 60_000,
    })
    await shot(page, '06-inspector-1440')

    await shot(page, '07-after-turn-1440')
  })

  // ⛔ ITS OWN TEST, and that is a correction the first run taught. The rail was read at
  //    the end of the turn case, so when that case failed on an ambiguous locator the
  //    harness received an EMPTY rail and reported a CLI/console disagreement that did
  //    not exist. An input to somebody else's verdict must not be collected inside a
  //    case that can fail for its own reasons.
  test('the rail lists the governed sessions, and the harness compares them', async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1600, height: 1000 })
    await signIn(page)
    await page.goto('/sessions')
    await expect(page.getByTestId('rail-row').first()).toBeVisible({
      timeout: 60_000,
    })
    const addresses = await page
      .getByTestId('rail-row')
      .evaluateAll((rows) =>
        rows.map((r) => r.getAttribute('data-address') ?? '').filter(Boolean),
      )
    result.rail_addresses = addresses
    save()
    observe(
      `the rail shows ${addresses.length} row(s): ${addresses.join(', ')}`,
    )
    await shot(page, '07-rail-1600')

    // ⛔ THE JOIN IS NOT STRING EQUALITY ON THE ROW KEY, and believing it was produced a
    //    false finding in this lane's first run. `provenance.ts` keys a row `live:<ref>`
    //    the moment the plane proves a managed row for it, so the run the CLI launched
    //    STOPS being keyed by its run_ref exactly when it starts working. What an
    //    operator is owed is that the run they hold a reference to OPENS — so that is
    //    what is measured, using the third address shape the console documents.
    if (CLI_RUN) {
      await page.goto(
        `/sessions?session=${encodeURIComponent(`run:${CLI_RUN}`)}`,
      )
      await expect(page.getByTestId('work-surface')).toBeVisible({
        timeout: 60_000,
      })

      // Full references are deliberately abbreviated in visible text; the
      // inspector's named copy controls retain them. A suffix or a name alone
      // cannot establish that this address resolved the CLI's exact run/profile.
      expect(
        CLI_RUN_NAME,
        'the harness supplies the CLI session name',
      ).toBeTruthy()
      let named = false
      try {
        await expect
          .poll(() => runIsVisible(page, CLI_RUN, CLI_RUN_NAME), {
            timeout: 60_000,
            intervals: [1000],
          })
          .toBe(true)
        named = true
      } catch {
        // Preserve the independent rail input even when this readback fails.
      }
      result.cli_run_addressable = named
      save()
      await shot(page, '13-cli-run-in-console-1600')
      observe(
        `opening run:${CLI_RUN} exposes its exact run/profile references and name: ${named}`,
      )
      if (!named) {
        defect(
          'console',
          `the run the CLI launched (${CLI_RUN}, named ${CLI_RUN_NAME}) is not named on the screen its own run address opens, so the two surfaces cannot be joined`,
          '13-cli-run-in-console-1600.png',
        )
      }
    }
  })

  test('the deployment the CLI made is visible in the console', async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await signIn(page)

    const t0 = Date.now()
    await page.goto('/provider-profiles')
    // The actual profile row keeps the complete identity on its cell title;
    // the painted display name and shortened reference are not identity oracles.
    const profileCell = page
      .locator('td')
      .and(page.getByTitle(PROFILE_REF, { exact: true }))
    let visible = false
    try {
      await expect
        .poll(
          async () =>
            (await profileCell.count()) === 1 &&
            (await profileCell.isVisible()),
          { timeout: 60_000, intervals: [1000] },
        )
        .toBe(true)
      visible = true
    } catch {
      // Report this leg and continue to the independently meaningful Deploy view.
    }
    await shot(page, '08-provider-profiles-1440')
    leg({
      leg: 'console: the profile `agent deploy` registered is on the deployment screen',
      result: visible ? 'visible by reference' : 'NOT VISIBLE by reference',
      ms: Date.now() - t0,
      evidence: '08-provider-profiles-1440.png',
    })
    if (!visible) {
      defect(
        'console',
        `/provider-profiles does not show ${PROFILE_REF}; the deployment the CLI made is not observable there`,
        '08-provider-profiles-1440.png',
      )
    }

    // ⛔ AND THE OTHER "DEPLOY", which is where an operator goes looking. The CLI verb
    //    that registered the profile above is `olivares agent deploy`; the console's
    //    Deployment module is the desired-vs-real deployment plane, a different thing
    //    with the same word. An operator who runs the CLI's deploy and then opens
    //    Deployment is told nothing has been deployed.
    await page.goto('/deploy')
    await page
      .waitForLoadState('networkidle', { timeout: 30_000 })
      .catch(() => {})
    const deployText = await page.locator('body').innerText()
    await shot(page, '09-deploy-1440')
    if (/no deployments declared yet/i.test(deployText)) {
      defect(
        'docs',
        'after `olivares agent deploy` succeeded, the console screen called Deployment says "No deployments declared yet": the CLI verb and the console module share the word `deploy` and mean different things, and nothing on either screen says so',
        '09-deploy-1440.png',
      )
    }
  })

  test('the same walk on the light theme and at 390 px', async ({ page }) => {
    await page.context().addInitScript(() => {
      try {
        window.localStorage.setItem('olivares.theme', 'light')
      } catch {
        /* private mode: the run then measures the default, and the capture says so */
      }
    })
    await page.setViewportSize({ width: 1440, height: 900 })
    await signIn(page)
    await expect(page.locator('html')).not.toHaveClass(/dark/)
    await page.goto('/sessions')
    await expect(page.getByTestId('work-surface')).toBeVisible({
      timeout: 30_000,
    })
    await shot(page, '10-work-surface-1440-light')

    const t0 = Date.now()
    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto('/sessions')
    await expect(page.getByTestId('work-surface')).toBeVisible({
      timeout: 30_000,
    })
    // BELOW `xl` exactly one pane is in front, chosen by the switcher. A surface that
    // kept three panes at 390 px would be three columns of about 130 px each.
    await expect(page.getByTestId('pane-button-rail')).toBeVisible()
    const railBox = await page.locator('#work-pane-rail').boundingBox()
    await shot(page, '11-work-surface-390-light')
    // Below xl the attached composer belongs to Narrative, not the rail pane.
    await page.getByTestId('pane-button-narrative').click()
    await expect(page.getByTestId('pane-button-narrative')).toHaveAttribute(
      'aria-pressed',
      'true',
    )
    const narrowAttached = page.getByTestId('work-composer')
    await expect(narrowAttached).toBeVisible()
    await expect(narrowAttached).toHaveAttribute('data-attached', 'true')
    await expect(narrowAttached.getByTestId('launcher-input')).toBeVisible()

    // Home's phone draft exposes both pickers under Advanced. Prepare it without
    // launching another session: the harness still compares the CLI and console pair.
    const narrowDraft = await draftComposer(page)
    await narrowDraft.getByTestId('launcher-input').fill('e2e-mobile-draft')
    await narrowDraft.getByTestId('composer-advanced').click()
    await expect(narrowDraft.getByTestId('launcher-workspace')).toBeVisible()
    await chooseProfile(page, narrowDraft)
    await expect(narrowDraft.getByTestId('launcher-start')).toBeEnabled()
    await expect(narrowDraft.getByTestId('work-scope-line')).toBeVisible()
    await shot(page, '12-composer-390-light')
    leg({
      leg: 'console: light theme at 1440 and the work surface at 390 px',
      result: `one pane in front (rail ${Math.round(railBox?.width ?? 0)} px wide); attached input visible; Home draft profile/workspace controls visible and launch enabled`,
      ms: Date.now() - t0,
      evidence: '12-composer-390-light.png',
    })
  })

  test.afterAll(() => {
    save()
  })
})
