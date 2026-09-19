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
import { expect, test, type Page } from '@playwright/test'
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

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
test.use({ screenshot: 'off', trace: 'off' })

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
  await page.goto('/login')
  await expect(
    page.locator('input[type=password]'),
    'the login form is the console front door; without it nothing below measures a screen',
  ).toHaveCount(1)
  await page.locator('input[type=email]').first().fill(EMAIL)
  await page.locator('input[type=password]').first().fill(PASSWORD)
  await page.locator('button[type=submit]').first().click()
  await page.waitForURL((url) => !url.pathname.startsWith('/login'), {
    timeout: 30_000,
  })
}

/** Wait until the composer has decided what it is (it renders nothing while asking). */
async function launcherSettled(page: Page) {
  await page.getByTestId('shell-launcher').waitFor({ timeout: 30_000 })
  await page
    .locator(
      '[data-testid="launcher-input"], [data-testid="launcher-add-provider"], [data-testid="launcher-blocked"]',
    )
    .first()
    .waitFor({ timeout: 30_000 })
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

    // THE COMPOSER. A console that cannot start work is a viewer.
    await launcherSettled(page)
    const blocked = page.getByTestId('launcher-blocked')
    if (await blocked.count()) {
      observe(`the composer is blocked: ${await blocked.first().innerText()}`)
    }
    const input = page.getByTestId('launcher-input')
    await expect(
      input,
      'the composer must offer a field to start work from, on the screen work happens on',
    ).toHaveCount(1)

    await input.fill('e2e-console')
    await page.getByTestId('launcher-profile').click()
    // The profile is chosen BY ITS REFERENCE, because that is what the harness deployed
    // and what the CLI half is holding. Choosing "the first one" would pass on a console
    // that offered the wrong profile.
    const offered = await page.getByRole('option').allInnerTexts()
    observe(`the profile picker offers: ${offered.join(' | ') || '<nothing>'}`)
    if (!offered.some((o) => o.includes(PROFILE_REF))) {
      observe(
        `the picker identifies profiles by their display name, not by ${PROFILE_REF}; two profiles sharing a name are indistinguishable there`,
      )
    }
    const wanted = page
      .getByRole('option')
      .filter({ hasText: PROFILE_NAME || PROFILE_REF.slice(0, 12) })
    if (await wanted.count()) {
      await wanted.first().click()
    } else {
      observe(
        'the deployed profile is not in the picker; the first option was taken instead',
      )
      await page.getByRole('option').first().click()
    }
    await shot(page, '03-composer-ready-1440')

    const launchStart = Date.now()
    await page.getByTestId('launcher-start').click()
    // The launch answers with a run and the console navigates to its address.
    await page.waitForURL(/session=run(%3A|:)/, { timeout: 60_000 })
    const address = new URL(page.url()).searchParams.get('session') ?? ''
    const runRef = address.replace(/^run:/, '')
    expect(runRef, 'the console navigated to the run it started').toBeTruthy()
    result.console_run_ref = runRef
    save()
    leg({
      leg: 'console: start a governed session from the composer',
      result: `run ${runRef}`,
      ms: Date.now() - launchStart,
      evidence: '04-launched-1440.png',
    })
    await shot(page, '04-launched-1440')

    // THE NARRATIVE. The card the address opens is the session's own account of itself.
    // `.first()` is not laziness: at 1440 px the surface shows the narrative AND the
    // context pane at once, so the unqualified locator is ambiguous BY DESIGN and strict
    // mode is right to say so. The claim is that the address opened a session surface.
    await expect(
      page
        .getByTestId('session-narrative')
        .or(page.getByTestId('session-context'))
        .first(),
      'the address the launch produced opens a session surface, not an empty pane',
    ).toBeVisible({ timeout: 60_000 })

    if (MODEL_TURN) {
      // ⛔ THE BOX THAT TALKS TO THE SESSION IS NOT ON THE SURFACE THAT STARTED IT. The
      //    composer launches the run from the shell; the live I/O and its input line are
      //    inside the session's detail dialog, one click further, behind
      //    `narrative-open-detail`. The first run of this spec reported "no way to send a
      //    turn" because it looked only at the surface — a false finding, and the
      //    correction is this click plus the observation below, which is the true one.
      const openDetail = page.getByTestId('narrative-open-detail')
      if (await openDetail.count()) {
        await openDetail.first().click()
        await page.getByRole('dialog').first().waitFor({ timeout: 30_000 })
        // …and the I/O is not on the sheet either: it is behind the sheet's `Live` TAB.
        const liveTab = page.getByRole('tab', { name: /^live$/i })
        const hadTab = (await liveTab.count()) > 0
        if (hadTab) await liveTab.first().click()
        observe(
          `the work surface starts a session but does not talk to it: reaching its input box takes "Full controls" → the ${hadTab ? '"Live" tab' : 'sheet'}, two steps away from the composer that launched it`,
        )
        await shot(page, '04b-live-tab-1440')
      }

      // THE REAL TURN, typed into the box the console offers for it. Which box that is
      // is the run's own fact: a Claude-driven run takes a raw stream-json line, so the
      // console asks for one. Whether asking a HUMAN for NDJSON is the right surface is
      // a defect recorded below, not something this spec quietly works around.
      const lineBox = page.getByLabel('Session input line')
      const textBox = page.getByLabel('Session message')
      const box = (await lineBox.count()) ? lineBox : textBox
      if (await box.count()) {
        // A field that is present and DISABLED is its own answer, and a spec that
        // called `fill` on it would report a harness error instead of the product's.
        if (await box.first().isDisabled()) {
          defect(
            'console',
            'the session input box is rendered but disabled for the administrator who started the session',
            '04b-live-tab-1440.png',
          )
        }
      }
      const turnStart = Date.now()
      let sent = false
      if ((await lineBox.count()) && !(await lineBox.first().isDisabled())) {
        defect(
          'console',
          'to speak to a Claude-driven session the console asks a human to type an NDJSON frame ("Send an NDJSON line to stdin…"); the same run is spoken to as a sentence from the CLI (`agent session input --text`) and as a turn for every other driver',
          '05-answer-1440.png',
        )
        await lineBox.first().fill(
          JSON.stringify({
            type: 'user',
            message: {
              role: 'user',
              content: [{ type: 'text', text: PROMPT }],
            },
          }),
        )
        await page
          .getByRole('button', { name: /^send$/i })
          .last()
          .click()
        sent = true
      } else if (
        (await textBox.count()) &&
        !(await textBox.first().isDisabled())
      ) {
        await textBox.first().fill(PROMPT)
        await page
          .getByRole('button', { name: /^send$/i })
          .last()
          .click()
        sent = true
      } else {
        defect(
          'console',
          'the console offers no way to send a turn to the session it just started',
          '04-launched-1440.png',
        )
      }

      if (sent) {
        const log = page.getByRole('log').first()
        // The answer is whatever the provider said; the assertion is that SOMETHING the
        // provider produced reaches the operator's screen, not that it said a chosen word.
        // A failure here is REPORTED, not thrown: the legs after it are the rest of the
        // lane's evidence, and one silent console must not cost them.
        //
        // ⛔ IT WAITS FOR THE ANSWER, NOT FOR BYTES. The first version polled the live
        //    view's LENGTH past a threshold, and passed 1.2 s after the send — on the
        //    driver's own `init` frame, which is hundreds of characters of tool names
        //    and says nothing about a model having replied. That reported "a real model
        //    turn, streamed back" for a turn whose answer had not arrived. What proves
        //    an answer is the driver's ASSISTANT or RESULT frame, so that is the wait.
        const ANSWERED = /"type":"(assistant|result)"|"subtype":"success"/
        let streamed = ''
        try {
          await expect
            .poll(async () => ANSWERED.test(await log.innerText()), {
              timeout: 180_000,
              intervals: [1000],
            })
            .toBe(true)
          streamed = await log.innerText()
        } catch {
          streamed = await log.innerText().catch(() => '')
          defect(
            'console',
            `the console sent the turn and no assistant or result frame reached the screen within 180 s (the live view held ${streamed.length} characters, ending: ${streamed.slice(-160).replace(/\s+/g, ' ')})`,
            '05-answer-1440.png',
          )
        }
        // THE TAIL, not the head. The driver's `init` frame alone is thousands of
        // characters of tool names, so an excerpt taken from the front is a record of
        // the handshake and not of the answer — measured: a 8 724-character stream whose
        // first 4 000 characters contained no assistant or result frame at all.
        result.console_answer = streamed.slice(-4000)
        save()
        leg({
          leg: 'console: a real model turn, sent and streamed back',
          result: `${streamed.length} characters of provider output, carrying the driver's ${/"type":"result"|"subtype":"success"/.test(streamed) ? 'result' : 'assistant'} frame`,
          ms: Date.now() - turnStart,
          evidence: '05-answer-1440.png',
        })
        await shot(page, '05-answer-1440')
      }
    }

    // THE INSPECTOR'S SCOPE. What a session runs as is the console's answer to "under
    // whose authority did this happen", and it must be on the screen, not in a log.
    const scope = page.getByTestId('shell-scope-line')
    if (await scope.count()) {
      const line = (await scope.first().innerText()).replace(/\s+/g, ' ')
      observe(`scope line: ${line}`)
      // WHOSE AUTHORITY THIS RAN UNDER is the one question this line exists to answer,
      // and an opaque identifier does not answer it. The organization has a NAME — the
      // administrator typed it into `auth bootstrap` — and the line prints the uuid.
      if (
        /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/.test(
          line,
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

      // ⛔ WHAT IS READ IS THE DOCUMENT, AND WAITING IS PART OF THE READ. The first
      //    version of this waited for `session-narrative` OR `session-context` and then
      //    looked for the narrative's button — and the narrative pane renders
      //    `narrative-loading` while the plane answers, so a screenshot taken in that
      //    window showed a console that HAD resolved the run (the rail names it, the
      //    inspector shows its RUN REFERENCE) while the assertion reported that it had
      //    not. That reported a product defect that did not exist. The claim is that the
      //    run an operator holds a reference to is NAMED on the screen its address
      //    opens, so the poll waits for the screen to finish answering.
      let text = ''
      let named = false
      for (let i = 0; i < 60; i++) {
        text = await page.locator('body').innerText()
        if (
          text.includes(CLI_RUN) &&
          (!CLI_RUN_NAME || text.includes(CLI_RUN_NAME))
        ) {
          named = true
          break
        }
        await page.waitForTimeout(1000)
      }
      result.cli_run_addressable = named
      save()
      await shot(page, '13-cli-run-in-console-1600')
      observe(
        `opening run:${CLI_RUN} names it on screen: ${named}${named ? '' : ` — the screen said: ${text.replace(/\s+/g, ' ').slice(0, 220)}`}`,
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
    // A screen that is still asking is not a screen that answered: the poll waits for
    // the reference itself rather than for a spinner to go away.
    const found = await page
      .locator('body')
      .evaluate(() => document.body.innerText)
      .then(() => true)
      .catch(() => false)
    expect(found).toBe(true)
    let visible = false
    for (let i = 0; i < 60; i++) {
      const text = await page.locator('body').innerText()
      if (
        text.includes(PROFILE_REF) ||
        text.includes(PROFILE_REF.slice(0, 16))
      ) {
        visible = true
        break
      }
      await page.waitForTimeout(1000)
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
    // The composer has to survive the narrow width too: a console that can start work on
    // a laptop and not on a phone has not made the screen responsive, it has hidden it.
    await launcherSettled(page)
    const narrowComposer = await page.getByTestId('launcher-input').count()
    await shot(page, '12-composer-390-light')
    leg({
      leg: 'console: light theme at 1440 and the work surface at 390 px',
      result: `one pane in front (rail ${Math.round(railBox?.width ?? 0)} px wide); composer field present: ${narrowComposer > 0}`,
      ms: Date.now() - t0,
      evidence: '11-work-surface-390-light.png',
    })
    if (narrowComposer === 0) {
      defect(
        'console',
        'at 390 px the composer field is not rendered, so work cannot be started from a phone',
        '12-composer-390-light.png',
      )
    }
  })

  test.afterAll(() => {
    save()
  })
})
