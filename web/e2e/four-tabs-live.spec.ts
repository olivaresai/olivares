// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// the four tab surfaces built this week, CLICKED against a LIVE olivares
// binary (real API + real SQLite). Sibling of onboarding-first-hour.spec.ts and
// deliberately NOT of management-views.spec.ts, whose own header says it "intercepts
// every /v1 call with fixtures … WITHOUT a backend" (management-views.spec.ts:8-11).
// A click harness built on fixtures would reproduce, at screen scale, the defect this
// campaign already paid for twice: tool-pinning's suite was green because the double
// accepted what production rejects with 400.
//
// WHY A CLICK AND NOT A URL. console:walk discovers ROUTES from the app's own
// navigation, so what lives INSIDE a route is invisible to it by construction — it
// proves the parent mounts, not that the tab paints (CANON-OPERATIVO §0-COBERTURA).
// None of these containers writes the tab to the URL: /compliance and /alerting use
// an uncontrolled `Tabs defaultValue`, so the ONLY way in is the trigger. Deleting the
// tab-to-URL write in two other views once left 164 files and 1644 vitest cells green,
// because those suites mock useNavigate. A real click is exactly what that cannot see.
//
// WHAT EACH SURFACE HAS TO SHOW, in the order the brief ranks them: the tab is reached
// by CLICK; its panel paints what the ENGINE serves; its empty state SAYS "none"
// rather than going blank or spinning forever; and its main action is honestly gated.
//
// ⚠ TWO THINGS THE BRIEF PREDICTED THAT MEASUREMENT CONTRADICTED — see the session log
// for the full account with file:line:
//
//   1. THERE IS NO AAL3 WALL ON THESE SURFACES. The module route WRAPPER applies no
//      assurance gate — authn, tenant resolution and authorization only
//      (core/api/modules.go:17-21,36-45) — and none of these four modules adds one:
//      `grep AAL3 modules/{compliance,notify,capabilities,eventing}` = 0. Measured
//      against a live engine: POST /v1/m/compliance/retention/sweep answered 200 for a
//      password session at aal=1. So the wall these writes actually meet is COMMERCIAL,
//      not assurance — which is the third leg of the brief's triad, and the one this
//      spec can prove live.
//      ⚠ NOT "module routes have no AAL3": deploy and governance enforce it INSIDE their
//      handlers (modules/deploy/helpers.go:74, modules/governance/breakglass.go:186,
//      approvals.go:577) by testing mc.Principal.AAL directly rather than calling the
//      engine's requireAAL3. Grepping the helper's NAME is a sweep narrower than that
//      claim, and it is how the broader version of this note was first written.
//   2. THE DEAD-LETTER QUEUE IS NOT IN /alerting. Notify's delivery log is read-only by
//      design ("There is no per-delivery redeliver in notify's API",
//      alerting-view.tsx:9). The dead letters with a redeliver action are the /eventing
//      tab (eventing-view.tsx:156,186-187). Both are clicked below: the one the brief
//      named, and the one that actually carries the action it described.
//
// THE ORACLE IS INDEPENDENT OF THE CONSOLE, which is what keeps the retention leg
// honest: `agent.memory` is a data class the ENGINE compiles in (dataclass.go), and it
// appears nowhere in the console's production source — only in a vitest fixture. If it
// is on screen, the live engine put it there and no bundled default could have.
//
// scripts/web-e2e.sh gives this spec a VIRGIN ENGINE OF ITS OWN, and that is load-
// bearing rather than incidental: it derives the first-boot specs by grepping for a
// spec that fills `locator('#token')` (web-e2e.sh:169-173). A pristine engine is the
// only way the empty states below are the real thing instead of a leftover.
import {
  expect,
  test,
  type Locator,
  type Page,
  type Request,
  type Response,
} from '@playwright/test'

const setupToken = process.env.PLAYWRIGHT_SETUP_TOKEN ?? ''
const PASSWORD = 'correct-horse-battery-staple-42'

/** The engine's copy for a failed read, and for the generic failure toast. Asserted
 *  ABSENT wherever a deliberate boundary is drawn: a console that shouts "something
 *  went wrong" at its own open-core seam teaches operators to distrust true errors. */
const GENERIC_FAILURE = /something went wrong|couldn't load|could not load/i

/**
 * Click a tab open and PROVE the click is what opened it.
 *
 * The `aria-selected=false` assertion before the click is the load-bearing half: it
 * fails if the tab is already the default (so the test could pass without clicking)
 * and it fails if the trigger is gone (the wiring mutant). Radix mounts only the
 * active panel, so the returned tabpanel is the one this click revealed — every later
 * assertion is scoped to it rather than to the page, so a stray match elsewhere in the
 * shell cannot satisfy an assertion about this tab.
 */
async function openTab(page: Page, name: RegExp): Promise<Locator> {
  const tab = page.getByRole('tab', { name })
  await expect(tab).toBeVisible({ timeout: 15_000 })
  await expect(tab).toHaveAttribute('aria-selected', 'false')
  await tab.click()
  await expect(tab).toHaveAttribute('aria-selected', 'true')
  const panel = page.getByRole('tabpanel')
  await expect(panel).toBeVisible()
  // The panel Radix revealed must be the one THIS trigger owns. Without it, a mutant that
  // swapped two TabsContent values would leave every later assertion pointing at a panel
  // the operator did not ask for, and they would all still pass.
  const panelId = await tab.getAttribute('aria-controls')
  expect(panelId, 'the clicked trigger must own a panel').toBeTruthy()
  await expect(panel).toHaveAttribute('id', panelId as string)
  return panel
}

/**
 * `toBeVisible()` IS NOT PAINT, and that gap is measured rather than assumed: against the
 * Playwright pinned here, a probe of `<div role=tabpanel style=opacity:0>` answers visible
 * AND text-visible. So a global opacity regression would leave every assertion in this file
 * green over a surface a human sees as blank — raised by the the model contrast of this
 * spec, which built that probe rather than arguing about it.
 *
 * Element.checkVisibility({opacityProperty}) is the browser's own answer to the same
 * question and, unlike a computed-style read on the node, it accounts for an ANCESTOR's
 * opacity — which is where such a regression actually lives.
 */
async function expectPainted(locator: Locator, what: string) {
  await expect(locator, what).toBeVisible({ timeout: 15_000 })
  const painted = await locator.evaluate((el) =>
    el.checkVisibility({
      opacityProperty: true,
      visibilityProperty: true,
      contentVisibilityAuto: true,
    }),
  )
  expect(
    painted,
    `${what}: laid out but NOT painted (opacity/visibility)`,
  ).toBe(true)
}

/**
 * Bind a screen assertion to what the ENGINE actually answered.
 *
 * Without this the harness proves only that the console renders a state — a mutant that
 * deleted the request and set the seam flag locally would satisfy every NIS 2 assertion
 * below, which is precisely the hole the contrast found. Awaiting the response also removes
 * the "assert an empty list before the list has loaded" race for free.
 */
function expectResponse(
  page: Page,
  path: string,
  status: number,
  method = 'GET',
): Promise<Response> {
  return page.waitForResponse(
    (r) =>
      r.url().includes(path) &&
      r.request().method() === method &&
      r.status() === status,
    { timeout: 20_000 },
  )
}

/** Capture the response regardless of status so the assertion reports the promised
 * Rendered/Fired/Effect message instead of timing out when the engine refuses it. */
function observeResponse(
  page: Page,
  path: string,
  method = 'GET',
): Promise<Response> {
  return page.waitForResponse(
    (r) =>
      new URL(r.url()).pathname === path && r.request().method() === method,
    { timeout: 30_000 },
  )
}

type AuthenticatedFetchOptions = {
  method?: 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE'
  body?: unknown
  headers?: Record<string, string>
}

type AuthenticatedResponse<T> = {
  status: number
  body: T
  text: string
  etag: string | null
  idempotencyReplayed: string | null
}

/**
 * Seed only the disposable first-boot engine, with the SAME bearer and tenant the
 * console is using. Keeping this in the page avoids a second login and makes the
 * local Zustand stores the single credential source; no token is logged or copied
 * into the Playwright report.
 */
async function authenticatedFetch<T>(
  page: Page,
  path: string,
  options: AuthenticatedFetchOptions = {},
): Promise<AuthenticatedResponse<T>> {
  const result = await page.evaluate(
    async ({ requestPath, requestOptions }) => {
      function storedState(key: string): Record<string, unknown> {
        const raw = localStorage.getItem(key)
        if (!raw) throw new Error(`authenticated fetch: missing ${key}`)
        const parsed = JSON.parse(raw) as {
          state?: Record<string, unknown>
        }
        return parsed.state ?? {}
      }

      const session = storedState('olivares.session')
      const tenantStore = storedState('olivares.tenant')
      const token = typeof session.token === 'string' ? session.token : ''
      const tenant =
        typeof tenantStore.activeTenant === 'string'
          ? tenantStore.activeTenant
          : ''
      if (!token) throw new Error('authenticated fetch: session has no bearer')

      const headers = new Headers(requestOptions.headers ?? {})
      headers.set('Accept', 'application/json')
      headers.set('Authorization', `Bearer ${token}`)
      if (tenant) headers.set('X-Olivares-Tenant', tenant)
      if (requestOptions.body !== undefined)
        headers.set('Content-Type', 'application/json')

      const response = await fetch(requestPath, {
        method: requestOptions.method ?? 'GET',
        headers,
        body:
          requestOptions.body === undefined
            ? undefined
            : JSON.stringify(requestOptions.body),
        credentials: 'same-origin',
      })
      const text = await response.text()
      let body: unknown = null
      if (text) {
        try {
          body = JSON.parse(text)
        } catch {
          body = text
        }
      }
      return {
        status: response.status,
        body,
        text,
        etag: response.headers.get('ETag'),
        idempotencyReplayed: response.headers.get('Idempotency-Replayed'),
      }
    },
    { requestPath: path, requestOptions: options },
  )
  return result as AuthenticatedResponse<T>
}

/**
 * The honest-empty check, and it is THREE conditions because the failure modes are
 * three. A panel that says "no dead letters" passes; one that renders nothing passes
 * the naive text check by accident, so the copy must be VISIBLE; and one still
 * spinning would eventually time out, so the loading sentinel is asserted gone rather
 * than waited on. An error state is excluded explicitly: "I could not look" must never
 * be read as "there is nothing".
 */
async function expectHonestEmpty(
  panel: Locator,
  copy: RegExp,
  listed: Response,
) {
  // THE ENGINE SAID NONE — not the console deciding to show an empty state. Asserted first
  // so "the screen says none" is only ever read on top of a list the engine really emptied.
  const body = (await listed.json()) as { items?: unknown[] }
  expect(Array.isArray(body.items), 'the engine answered a list envelope').toBe(
    true,
  )
  expect(body.items, 'a virgin engine has nothing here').toHaveLength(0)

  await expectPainted(panel.getByText(copy), `empty-state copy ${copy}`)
  // AsyncSection's busy branch renders role=status with this sr-only text INSIDE the panel
  // (features/_intel/async.tsx:38-40), so this is not a vacuous count.
  await expect(panel.getByText('Loading…')).toHaveCount(0)
  // Its error branch renders ErrorState, which is role=alert (ui/error-state.tsx:41).
  await expect(panel.getByRole('alert')).toHaveCount(0)
  await expect(panel.getByText(GENERIC_FAILURE)).toHaveCount(0)
}

// The name says FIVE and says READ, deliberately. An earlier title claimed the four tabs
// "gate their writes", and the the model contrast was right that the file does not
// establish it: only NIS 2 submits a write, retention opens its dialog without confirming,
// and tool pins and deliveries are reads. What every surface here DOES establish is that it
// is reachable by click, that what it shows came from the engine, and that the engine's
// refusals are drawn honestly.
test('live tab surfaces, Settings, and five parent compositions reach engine effects', async ({
  page,
}) => {
  test.skip(
    !setupToken,
    'PLAYWRIGHT_SETUP_TOKEN not set — run via scripts/web-e2e.sh',
  )
  test.setTimeout(300_000)
  page.setDefaultTimeout(10_000)

  await test.step('first boot: create the administrator and sign in', async () => {
    await page.goto('/setup')
    await page.locator('#token').fill(setupToken)
    await page.locator('#setup-email').fill('admin@example.com')
    await page.locator('#setup-password').fill(PASSWORD)
    await page.getByRole('button', { name: /create administrator/i }).click()

    await page.waitForURL('**/login')
    await page.locator('#email').fill('admin@example.com')
    await page.locator('#password').fill(PASSWORD)
    await page.getByRole('button', { name: /^sign in$/i }).click()
    await expect(
      page.getByRole('link', { name: 'Inventory', exact: true }),
    ).toBeVisible()
  })

  // --- 1 · NIS 2 (a tab of /compliance, from #689) ----------------------------
  //
  // The write here is the clearest COMMERCIAL boundary in the four: the engine answers
  // 501 for classify because the add-on is not linked, and the dialog is built to say
  // so in place rather than to raise the red toast (nis2-view.tsx:392-439). Measured on
  // a live community build: {"error":{"message":"NIS 2 significant-incident
  // classification requires the Olivares enterprise add-on (nis2incident); not linked
  // in this build"}} with HTTP 501.
  await test.step('NIS 2: reached by click, honest empty, and a 501 drawn as a boundary', async () => {
    await page.goto('/compliance')
    const listed = expectResponse(page, '/v1/m/compliance/nis2/incidents', 200)
    const panel = await openTab(page, /NIS 2 incidents/i)

    // Paints its own surface, not the posture tab it replaced.
    await expectPainted(
      panel.getByText('NIS 2 significant incidents', { exact: true }),
      'NIS 2 section title',
    )
    // The engine hardcodes provisional=true, and the console renders the honesty rule
    // where the operator acts rather than leaving it to be discovered.
    await expectPainted(
      panel.getByText(/DECISION SUPPORT, not a legal classification/i),
      'the provisional-verdict caveat',
    )

    await expectHonestEmpty(panel, /No classified incidents/i, await listed)

    // THE BOUNDARY. Fill the real form and submit it against the live engine, and bind the
    // screen to the ENGINE'S OWN 501 — a console that hard-coded this notice and stopped
    // calling the engine would satisfy every assertion below without this line.
    await panel.getByRole('button', { name: /classify incident/i }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog).toBeVisible()
    await dialog.getByLabel(/incident reference/i).fill('INC-2026-0042')
    await dialog
      .getByLabel(/impact document/i)
      .fill('{"users_affected":10,"awareness_time":"2026-08-11T00:00:00Z"}')
    const refused = expectResponse(
      page,
      '/v1/m/compliance/nis2/incidents/classify',
      501,
      'POST',
    )
    await dialog.getByRole('button', { name: /^classify$/i }).click()
    const seamBody = (await refused).body().then((b) => b.toString())
    expect(await seamBody, 'the engine names the add-on it is missing').toMatch(
      /enterprise add-on/i,
    )

    // The add-on seam, explained IN PLACE: the badge, the sentence, and the dialog still
    // open with the operator's document intact. If a future build routed this through the
    // shared privileged hook, the generic failure toast would appear and this goes red.
    await expectPainted(
      dialog.getByText('Add-on', { exact: true }),
      'the add-on badge',
    )
    await expectPainted(
      dialog.getByText(
        /enterprise NIS 2 add-on, which is not linked in this build/i,
      ),
      'the seam explanation',
    )
    await expectPainted(
      dialog.getByText(/Reading, exporting and deleting/i),
      'what the boundary does NOT take away',
    )
    // Page-scoped, not panel-scoped, because a toast renders in a portal outside the panel:
    // scoping these two to the panel would make them vacuous.
    await expect(page.getByText(GENERIC_FAILURE)).toHaveCount(0)
    // Not authorized would be the WRONG news here: nobody can grant a permission that
    // makes an unlinked add-on appear.
    await expect(page.getByText(/not authorized|forbidden/i)).toHaveCount(0)
    // The document survives the refusal — the round trip is not thrown away.
    await expect(dialog.getByLabel(/incident reference/i)).toHaveValue(
      'INC-2026-0042',
    )

    await dialog.getByRole('button', { name: /cancel/i }).click()
    await expect(dialog).toBeHidden()
  })

  // --- 2 · Retention (a tab of /compliance, from #693) ------------------------
  //
  // The one surface of the four that paints ENGINE-SERVED ROWS on a virgin engine: the
  // data-class registry is compiled into the binary, so an empty list here would mean a
  // response that could not be read as the registry — which the view says in as many
  // words rather than shrugging (retention-view.tsx:210-218).
  await test.step('Retention: paints the engine-compiled class registry, and states its purge gate', async () => {
    const classes = expectResponse(
      page,
      '/v1/m/compliance/retention/classes',
      200,
    )
    const panel = await openTab(page, /^Retention$/i)

    await expectPainted(
      panel.getByText('Retention schedules', { exact: true }),
      'Retention section title',
    )

    // THE PROOF THAT THE ENGINE IS ON THE OTHER END, asserted on BOTH sides of the wire:
    // `agent.memory` and `session.timeline` are ids the engine compiles in, and neither
    // literal exists anywhere in the console's production source (only in a vitest
    // fixture) — so no bundled default could put them on this screen.
    const served = (await classes).json() as Promise<{
      items: { id: string }[]
    }>
    const ids = (await served).items.map((c) => c.id)
    expect(ids, 'the engine serves its compiled-in class registry').toEqual(
      expect.arrayContaining(['agent.memory', 'session.timeline']),
    )
    await expectPainted(
      panel.getByText('agent.memory', { exact: true }),
      'the agent.memory class row',
    )
    await expectPainted(
      panel.getByText('session.timeline', { exact: true }),
      'the session.timeline class row',
    )
    await expect(panel.getByRole('alert')).toHaveCount(0)

    // The gate, stated where the operator acts: a schedule is a document until a human
    // approves the purge.
    await expect(
      panel.getByText(
        /A schedule is a document until a human approves its purge/i,
      ),
    ).toBeVisible()

    // THE DESTRUCTIVE VERB, OPENED BUT NOT CONFIRMED. On a pristine engine nothing is
    // armed, and the dialog has to SAY that rather than offer a bare confirm — the
    // "nothing" vs "I could not look" distinction this view calls the most expensive
    // defect in the repository (retention-view.tsx:170-181).
    await panel.getByRole('button', { name: /run sweep/i }).click()
    const sweep = page.getByRole('dialog')
    await expect(sweep).toBeVisible()
    await expect(
      sweep.getByText(/No schedule currently authorises destruction/i),
    ).toBeVisible()
    await expect(
      sweep.getByText(/A legal hold always beats a purge/i),
    ).toBeVisible()
    await expect(page.getByText(GENERIC_FAILURE)).toHaveCount(0)

    // Deliberately NOT confirmed: this harness measures the gate, it does not run a
    // destructive pass to see what happens.
    await sweep.getByRole('button', { name: /cancel/i }).click()
    await expect(sweep).toBeHidden()
  })

  // --- 3 · Tool pins (inside /capabilities, from #690) ------------------------
  //
  // The second commercial boundary, and the one drawn on a READ rather than a write:
  // GET /v1/m/capabilities/toolpins answers 501 on a community build, and the tab
  // renders that as a calm capability notice instead of an error state
  // (tool-pins.tsx:265-273). Measured live: {"error":{"message":"tool pinning is an
  // enterprise add-on (no verifier wired)"}}.
  await test.step('Tool pins: a 501 on the LIST renders as a capability notice, not a failure', async () => {
    await page.goto('/capabilities')
    // Same causality bind as NIS 2, on a READ this time: without it, a build that dropped
    // the request and always showed the notice would pass.
    const refusedRead = expectResponse(page, '/v1/m/capabilities/toolpins', 501)
    const panel = await openTab(page, /Tool pins/i)
    expect(
      (await (await refusedRead).body()).toString(),
      'the engine names the add-on it is missing',
    ).toMatch(/enterprise add-on/i)

    await expectPainted(
      panel.getByText(/Tool pins are an enterprise capability/i),
      'the enterprise-capability notice',
    )
    await expectPainted(
      panel.getByText(/no enterprise verifier wired yet/i),
      'why the capability is unavailable',
    )

    // The three wrong answers this notice exists instead of: a red error, a permission
    // refusal, and a bare empty table that would read as "you have no pins".
    await expect(panel.getByRole('alert')).toHaveCount(0)
    await expect(panel.getByText(GENERIC_FAILURE)).toHaveCount(0)
    await expect(
      panel.getByText(/not authorized|do not have permission/i),
    ).toHaveCount(0)
    await expect(panel.getByText('No tool pins', { exact: true })).toHaveCount(
      0,
    )
  })

  // --- 4 · Dead letters (a tab of /eventing, from #692) -----------------------
  //
  // The brief placed this in /alerting; measurement put it here (see the header note).
  // Both are clicked: this one carries the redeliver action, and the /alerting one is
  // the read-only delivery log the brief was pointing at.
  await test.step('Dead letters: reached by click on /eventing, and honestly empty', async () => {
    await page.goto('/eventing')
    const listed = expectResponse(page, '/v1/m/eventing/dead-letters', 200)
    const panel = await openTab(page, /Dead letters/i)

    await expectPainted(
      panel.getByText('Dead-letter queue', { exact: true }),
      'Dead-letter queue title',
    )
    await expectHonestEmpty(panel, /No dead letters/i, await listed)
    // The empty state explains WHEN something would appear here, so an operator does not
    // read "none" as "the queue is not wired".
    await expectPainted(
      panel.getByText(
        /appear here when deliveries exhaust all retry attempts/i,
      ),
      'the reason the queue is empty',
    )
  })

  await test.step('Deliveries on /alerting: the read-only log the brief named', async () => {
    await page.goto('/alerting')
    const listed = expectResponse(page, '/v1/m/notify/deliveries', 200)
    const panel = await openTab(page, /^Deliveries$/i)

    // Notify's log is read-only BY DESIGN — there is no per-delivery redeliver in its
    // API — so the absence of a redeliver control here is the contract, not a gap, and it
    // is asserted rather than left implied.
    await expectPainted(
      panel.getByText(/recorded delivery attempts \(read-only\)/i),
      'the read-only statement',
    )
    await expect(panel.getByRole('button', { name: /redeliver/i })).toHaveCount(
      0,
    )
    await expectHonestEmpty(panel, /No deliveries/i, await listed)
  })

  // --- 6 · Automations: real parent → workflow create → editor ----------------
  await test.step('Automations: the parent creates a workflow and opens its editor', async () => {
    const workflowName = 'Live composition workflow'
    await page.goto('/automations')
    const listed = expectResponse(page, '/v1/m/orchestration/workflows', 200)
    const panel = await openTab(page, /^Workflows$/i)
    const workflowPage = (await (await listed).json()) as { items: unknown[] }
    expect(
      workflowPage.items,
      'automations Rendered: the disposable engine was not workflow-empty',
    ).toEqual([])

    // A virgin list deliberately offers the action in both the card header and
    // EmptyState. Use the latter so this click proves the empty composition works.
    const empty = panel
      .locator('[data-slot="empty-state"]')
      .filter({ hasText: /no workflows yet/i })
    const createButton = empty.getByRole('button', {
      name: /^new workflow$/i,
    })
    await expect(
      createButton,
      'automations Rendered: the real parent did not mount New workflow',
    ).toBeEnabled()
    await createButton.click()

    const dialog = page.getByRole('dialog')
    await expect(
      dialog,
      'automations Rendered: the parent action did not mount the create dialog',
    ).toBeVisible()
    const workflowNameInput = dialog.getByLabel(/^Name/i)
    await expect(
      workflowNameInput,
      'automations Rendered: create dialog did not expose its required Name field',
    ).toBeVisible()
    await workflowNameInput.fill(workflowName)

    const posted = observeResponse(
      page,
      '/v1/m/orchestration/workflows',
      'POST',
    )
    const detail = page.waitForResponse(
      (response) => {
        const path = new URL(response.url()).pathname
        return (
          path.startsWith('/v1/m/orchestration/workflows/') &&
          response.request().method() === 'GET'
        )
      },
      { timeout: 30_000 },
    )
    await dialog.getByRole('button', { name: /create workflow/i }).click()
    const created = await posted
    expect(
      created.status(),
      'automations Fired: workflow POST did not return 201',
    ).toBe(201)
    const workflow = (await created.json()) as { id: string; name: string }
    expect(
      workflow.name,
      'automations Fired: the engine did not receive the authored workflow name',
    ).toBe(workflowName)
    const read = await detail
    expect(
      read.status(),
      'automations Effect: the editor detail GET did not return 200',
    ).toBe(200)
    expect(
      new URL(read.url()).pathname,
      'automations Effect: the editor did not read the created workflow',
    ).toBe(`/v1/m/orchestration/workflows/${workflow.id}`)
    await expectPainted(
      page.getByText(/back to workflows/i),
      'automations Effect: the successful create did not open the real editor',
    )
  })

  // --- 7 · Backups: real parent → accepted job → JobProgress ------------------
  await test.step('Backups: the parent starts an accepted job and renders JobProgress', async () => {
    await page.goto('/backups')
    const trigger = page.getByRole('button', { name: /create backup/i })
    await expect(
      trigger,
      'backups Rendered: the real parent did not expose Create backup',
    ).toBeEnabled()
    await trigger.click()

    const dialog = page.getByRole('dialog')
    const passphrase = dialog.getByLabel(/passphrase/i)
    await expect(
      passphrase,
      'backups Rendered: the parent did not mount the backup passphrase field',
    ).toBeVisible()
    await passphrase.fill('live-backup-passphrase-42')

    const posted = observeResponse(page, '/v1/console/dr/backup', 'POST')
    const streamed = page.waitForResponse(
      (response) => {
        const path = new URL(response.url()).pathname
        return (
          path.startsWith('/v1/console/dr/jobs/') &&
          path.endsWith('/stream') &&
          response.request().method() === 'GET'
        )
      },
      { timeout: 30_000 },
    )
    await dialog.getByRole('button', { name: /start backup/i }).click()
    const accepted = await posted
    expect(
      accepted.status(),
      'backups Fired: backup POST did not return 202',
    ).toBe(202)
    const job = (await accepted.json()) as { job_id: string }
    expect(
      job.job_id,
      'backups Fired: the accepted engine response did not name a job',
    ).toBeTruthy()
    await expect(
      dialog.getByRole('progressbar'),
      'backups Effect: the accepted job did not replace the form with JobProgress',
    ).toBeVisible()
    const stream = await streamed
    expect(
      stream.status(),
      'backups Effect: JobProgress did not open its live engine stream',
    ).toBe(200)
    expect(
      new URL(stream.url()).pathname,
      'backups Effect: JobProgress subscribed to a different job',
    ).toBe(`/v1/console/dr/jobs/${job.job_id}/stream`)
    await expect(
      dialog.getByRole('progressbar'),
      'backups Effect: the job SSE snapshot did not replace the initializing value',
    ).not.toHaveAttribute('aria-valuenow', '0')
  })

  // --- 8 · Model operations: real parent → brokered deployment → row ----------
  await test.step('Model operations: the parent creates a brokered deployment row', async () => {
    const deploymentName = 'Live brokered deployment'
    const deploymentsPath = '/v1/m/models/inference-deployments'
    await page.goto('/model-operations')
    const initiallyListed = expectResponse(page, deploymentsPath, 200)
    const panel = await openTab(page, /^Deployments$/i)
    await initiallyListed

    const createButton = panel.getByRole('button', {
      name: /new deployment/i,
    })
    await expect(
      createButton,
      'model-operations Rendered: the parent did not mount New deployment',
    ).toBeEnabled()
    await createButton.click()

    const dialog = page.getByRole('dialog')
    const deploymentNameInput = dialog.getByLabel(/^Name/i)
    await expect(
      deploymentNameInput,
      'model-operations Rendered: create dialog did not expose its required Name field',
    ).toBeVisible()
    await deploymentNameInput.fill(deploymentName)
    const typeSelect = dialog.getByLabel(/deployment type/i)
    await expect(
      typeSelect,
      'model-operations Rendered: deployment type control is missing',
    ).toBeEnabled()
    await typeSelect.click()
    await page.getByRole('option', { name: /brokered/i }).click()
    await dialog
      .getByLabel(/provider endpoint/i)
      .fill('https://provider.invalid/v1')

    const posted = observeResponse(page, deploymentsPath, 'POST')
    const refetched = expectResponse(page, deploymentsPath, 200)
    await dialog.getByRole('button', { name: /^create$/i }).click()
    const created = await posted
    expect(
      created.status(),
      'model-operations Fired: brokered deployment POST did not return 201',
    ).toBe(201)
    const deployment = (await created.json()) as {
      name: string
      deployment_type: string
      endpoint_ref: string
    }
    expect(
      deployment,
      'model-operations Fired: the engine did not preserve the brokered discriminator',
    ).toMatchObject({
      name: deploymentName,
      deployment_type: 'brokered',
      endpoint_ref: 'https://provider.invalid/v1',
    })
    await refetched
    await expect(
      page.getByRole('row').filter({ hasText: deploymentName }),
      'model-operations Effect: the engine-created deployment did not reach the table',
    ).toBeVisible()
  })

  // --- 9 · Red-team: owned inventory → register → consent → run ---------------
  await test.step('Red-team: an owned agent completes register, authorize, and launch', async () => {
    const agentName = 'Live red-team inventory agent'
    const targetName = 'Live red-team consent target'
    const agentSeed = await authenticatedFetch<{ id: string }>(
      page,
      '/v1/agents',
      {
        method: 'POST',
        body: {
          name: agentName,
          kind: 'e2e',
          external_id: 'four-tabs-live-redteam-agent',
        },
      },
    )
    expect(
      agentSeed.status,
      'red-team seed Fired: owned-agent POST did not return 201',
    ).toBe(201)
    expect(
      agentSeed.body.id,
      'red-team seed Effect: the disposable engine did not return an agent id',
    ).toBeTruthy()

    const targetsPath = '/v1/m/redteam/targets'
    const initialTargets = expectResponse(page, targetsPath, 200)
    await page.goto('/red-team')
    await initialTargets
    const register = page.getByRole('button', { name: /register agent/i })
    await expect(
      register,
      'red-team Rendered: the real parent did not expose Register agent',
    ).toBeEnabled()
    await register.click()

    const registerDialog = page.getByRole('dialog')
    await registerDialog.getByLabel(/agent reference/i).fill(agentSeed.body.id)
    await registerDialog.getByLabel(/display name/i).fill(targetName)
    await registerDialog.getByLabel(/^scope/i).fill('input,output')
    const registeredResponse = observeResponse(page, targetsPath, 'POST')
    const registeredRefetch = expectResponse(page, targetsPath, 200)
    await registerDialog.getByRole('button', { name: /^register$/i }).click()
    const registered = await registeredResponse
    expect(
      registered.status(),
      'red-team Fired: target registration POST did not return 201',
    ).toBe(201)
    const target = (await registered.json()) as {
      id: string
      agent_ref: string
      authorized: boolean
    }
    expect(
      target,
      'red-team Fired: registration did not retain the owned agent without granting consent',
    ).toMatchObject({ agent_ref: agentSeed.body.id, authorized: false })
    await registeredRefetch

    const targetRow = page.getByRole('row').filter({ hasText: targetName })
    await expect(
      targetRow,
      'red-team Effect: the registered target did not reach the parent table',
    ).toBeVisible()
    await targetRow.getByRole('button', { name: /^authorize$/i }).click()
    const authorizeDialog = page.getByRole('dialog')
    await expect(
      authorizeDialog,
      'red-team Rendered: the target row did not mount the consent dialog',
    ).toBeVisible()
    const authorizePath = `${targetsPath}/${target.id}/authorize`
    const authorizedResponse = observeResponse(page, authorizePath, 'POST')
    const authorizedRefetch = expectResponse(page, targetsPath, 200)
    await authorizeDialog.getByRole('button', { name: /^authorize$/i }).click()
    const authorized = await authorizedResponse
    expect(
      authorized.status(),
      'red-team Fired: consent POST did not return 200',
    ).toBe(200)
    expect(
      ((await authorized.json()) as { authorized: boolean }).authorized,
      'red-team Effect: the engine did not persist consent',
    ).toBe(true)
    await authorizedRefetch
    await expect(
      targetRow.getByText(/authorized/i).first(),
      'red-team Effect: the authorized state did not return to the row',
    ).toBeVisible()

    await targetRow.getByRole('button', { name: /launch run/i }).click()
    const launchDialog = page.getByRole('dialog')
    await expect(
      launchDialog,
      'red-team Rendered: the authorized row did not mount Launch run',
    ).toBeVisible()
    const runsPath = '/v1/m/redteam/runs'
    const launchedResponse = observeResponse(page, runsPath, 'POST')
    await launchDialog.getByRole('button', { name: /launch run/i }).click()
    const launched = await launchedResponse
    expect(
      launched.status(),
      'red-team Fired: authorized launch POST did not return 201',
    ).toBe(201)
    const run = (await launched.json()) as {
      id: string
      target_ref: string
      status: string
      total: number
      skipped: number
    }
    expect(
      run.target_ref,
      'red-team Fired: launch was not bound to the consent target id',
    ).toBe(target.id)
    expect(
      run,
      'red-team Effect: an unwired sandbox must be reported as degraded, never as a pass',
    ).toMatchObject({ status: 'degraded', skipped: run.total })
    expect(
      run.total,
      'red-team Effect: the requested battery did not contain any probes',
    ).toBeGreaterThan(0)

    const runsListed = expectResponse(page, runsPath, 200)
    const runsPanel = await openTab(page, /^Runs$/i)
    const runPage = (await (await runsListed).json()) as {
      items: { id: string }[]
    }
    expect(
      runPage.items.map((item) => item.id),
      'red-team Effect: the live runs list omitted the launched run',
    ).toContain(run.id)
    await expect(
      runsPanel.getByRole('row').filter({ hasText: target.id }),
      'red-team Effect: the launched run did not reach the Runs table',
    ).toBeVisible()
    await expect(
      runsPanel.getByRole('row').filter({ hasText: target.id }),
      'red-team Effect: the parent hid the degraded/pending-sandbox state',
    ).toContainText(/pending sandbox/i)
  })

  // --- 10 · Work: default scope → validate/plan/apply → detail + ETag ----------
  await test.step('Work: seed through validate/plan/apply, then open engine detail', async () => {
    const workTitle = 'Live composition work item'
    const workPath = '/v1/m/sessions/work-items'
    const [workspaceResult, whoamiResult] = await Promise.all([
      authenticatedFetch<{
        items: { id: string; is_default: boolean }[]
      }>(page, '/v1/workspaces'),
      authenticatedFetch<{ actor: string; user_id: string }>(
        page,
        '/v1/auth/whoami',
      ),
    ])
    expect(
      workspaceResult.status,
      'work seed Fired: workspace inventory did not return 200',
    ).toBe(200)
    expect(
      whoamiResult.status,
      'work seed Fired: whoami did not return 200',
    ).toBe(200)
    const defaultWorkspace = workspaceResult.body.items.find(
      (workspace) => workspace.is_default,
    )
    expect(
      defaultWorkspace,
      'work seed Effect: first boot did not expose its default workspace',
    ).toBeTruthy()
    expect(
      whoamiResult.body.user_id,
      'work seed Effect: whoami did not expose the canonical owner identity',
    ).toBeTruthy()

    const body = {
      workspace_id: defaultWorkspace?.id,
      work_kind: 'implementation',
      title: workTitle,
      brief_md: 'Created through the live validate, plan, and apply protocol.',
      context_refs: [],
      priority: 'p1',
      owner_kind: 'user',
      // The work kernel resolves a user owner by canonical identity UUID, not by
      // its display/actor string (cmd/olivares/workkernel.go:82-122).
      owner_ref: whoamiResult.body.user_id,
      provenance_kind: 'human',
      provenance_ref: 'e2e:four-tabs-live',
      acceptance: [
        {
          criterion_key: 'live-composition',
          ordinal: 0,
          statement: 'The parent view opens the engine-authored item and ETag.',
          required: true,
        },
      ],
    }
    const validated = await authenticatedFetch<{
      verdict: string
      plan_hash: string
    }>(page, `${workPath}?mode=validate`, { method: 'POST', body })
    expect(
      validated.status,
      'work seed Fired: validate did not return 200',
    ).toBe(200)
    expect(
      validated.body,
      'work seed Effect: validate was not clean and write-free',
    ).toMatchObject({ verdict: 'LIMPIO', plan_hash: '' })

    const planned = await authenticatedFetch<{
      verdict: string
      plan_hash: string
    }>(page, `${workPath}?mode=plan`, { method: 'POST', body })
    expect(planned.status, 'work seed Fired: plan did not return 200').toBe(200)
    expect(planned.body.verdict, 'work seed Effect: plan was not clean').toBe(
      'LIMPIO',
    )
    expect(
      planned.body.plan_hash,
      'work seed Effect: plan did not return the canonical hash',
    ).toMatch(/^[0-9a-f]{64}$/)

    const idempotencyKey = await page.evaluate(() => crypto.randomUUID())
    const applied = await authenticatedFetch<{
      result_id: string
      verdict: string
    }>(page, `${workPath}?mode=apply`, {
      method: 'POST',
      body,
      headers: {
        'Idempotency-Key': idempotencyKey,
        'If-Plan-Hash': planned.body.plan_hash,
      },
    })
    expect(applied.status, 'work seed Fired: apply did not return 200').toBe(
      200,
    )
    expect(
      applied.body.verdict,
      'work seed Effect: apply did not commit a clean result',
    ).toBe('LIMPIO')
    expect(
      applied.etag,
      'work seed Effect: apply did not return the server ETag',
    ).toBe('"v1"')
    const itemId = applied.body.result_id
    expect(
      itemId,
      'work seed Effect: apply did not identify the created work item',
    ).toBeTruthy()

    const replayed = await authenticatedFetch<typeof applied.body>(
      page,
      `${workPath}?mode=apply`,
      {
        method: 'POST',
        body,
        headers: {
          'Idempotency-Key': idempotencyKey,
          'If-Plan-Hash': planned.body.plan_hash,
        },
      },
    )
    expect(
      replayed.status,
      'work seed replay Fired: retry did not return 200',
    ).toBe(200)
    expect(
      replayed.idempotencyReplayed,
      'work seed replay Effect: engine did not identify the exact retry',
    ).toBe('true')
    expect(
      replayed.body,
      'work seed replay Effect: exact retry did not preserve the durable receipt',
    ).toEqual(applied.body)

    const rebound = await authenticatedFetch<{
      code: string
      error: { code: string }
    }>(page, `${workPath}?mode=apply`, {
      method: 'POST',
      body: { ...body, title: `${workTitle} rebound` },
      headers: {
        'Idempotency-Key': idempotencyKey,
        'If-Plan-Hash': planned.body.plan_hash,
      },
    })
    expect(
      rebound.status,
      'work seed replay Fired: divergent key reuse did not return 409',
    ).toBe(409)
    expect(
      rebound.body.error?.code ?? rebound.body.code,
      'work seed replay Effect: engine did not name idempotency_key_reused',
    ).toBe('idempotency_key_reused')

    const listed = expectResponse(page, workPath, 200)
    await page.goto('/work')
    const workList = (await (await listed).json()) as {
      items: { id: string; title: string }[]
    }
    expect(
      workList.items.map((item) => item.id),
      'work Effect: the engine list omitted the applied work item',
    ).toContain(itemId)
    const row = page.getByRole('button', { name: new RegExp(workTitle, 'i') })
    await expect(
      row,
      'work Rendered: the real parent did not render the applied work item',
    ).toBeEnabled()

    const detailPath = `${workPath}/${itemId}`
    const detailResponse = observeResponse(page, detailPath)
    await row.click()
    const detail = await detailResponse
    expect(
      detail.status(),
      'work Fired: row click detail GET did not return 200',
    ).toBe(200)
    const etag = detail.headers()['etag'] ?? null
    expect(etag, 'work Fired: detail GET omitted the authoritative ETag').toBe(
      applied.etag,
    )
    const snapshot = (await detail.json()) as { item: { id: string } }
    expect(
      snapshot.item.id,
      'work Fired: detail GET returned a different work item',
    ).toBe(itemId)

    const detailSheet = page.getByRole('dialog')
    await expect(
      detailSheet.getByRole('heading').filter({ hasText: workTitle }),
      'work Effect: the parent-mounted detail did not paint the engine title',
    ).toBeVisible()
    await expect(
      detailSheet.getByText(etag as string, { exact: true }),
      'work Effect: the parent-mounted detail did not paint the server ETag',
    ).toBeVisible()
  })

  // --- 11 · Settings: all 18 controls, including its local-only preferences -----
  //
  // Settings is a real authenticated route outside FEATURE_VIEWS. Its Profile and
  // About panels paint engine answers; Appearance intentionally persists only
  // non-sensitive client preferences. The distinction is asserted instead of
  // manufacturing an HTTP call for controls whose source names a local state setter.
  await test.step('Settings: 18 controls reach their handler and promised effect', async () => {
    // A full navigation creates a fresh QueryClient. Arm both waits first so the
    // assertions bind Profile/About to this live engine rather than to visible copy.
    const serverInfoRequest = expectResponse(page, '/v1/server-info', 200)
    const whoamiRequest = expectResponse(page, '/v1/auth/whoami', 200)
    await page.goto('/settings')
    const [serverInfoResponse, whoamiResponse] = await Promise.all([
      serverInfoRequest,
      whoamiRequest,
    ])
    const serverInfo = (await serverInfoResponse.json()) as {
      version: string
      engine: string
      license: { status: string; licensee: string }
    }
    const whoami = (await whoamiResponse.json()) as {
      actor: string
      grants: unknown[]
    }

    const exercised = new Set<string>()
    const expectedControls = [
      'tab/profile',
      'tab/appearance',
      'tab/about',
      'trigger/theme',
      'trigger/density',
      'trigger/language',
      'theme/light',
      'theme/dark',
      'theme/system',
      'density/comfortable',
      'density/compact',
      'language/en',
      'language/es',
      'language/zh',
      'language/ja',
      'language/de',
      'language/ru',
      'language/fr',
    ]

    const tabs = page.getByRole('tab')
    await expect(
      tabs,
      'settings tabs Rendered: Profile, Appearance, and About must exist',
    ).toHaveCount(3)

    // Appearance is inactive initially; visiting it first makes Profile's later
    // activation causal rather than a click on the default-selected control.
    const firstAppearance = await openTab(page, /^Appearance$/i)
    await expect(
      firstAppearance.getByRole('combobox'),
      'settings tab/appearance Effect: expected three preference controls',
    ).toHaveCount(3)
    exercised.add('tab/appearance')

    const profile = await openTab(page, /^Profile$/i)
    await expectPainted(
      profile.getByText(whoami.actor, { exact: true }),
      'settings tab/profile Effect: actor returned by live whoami',
    )
    await expect(
      profile.getByText(String(whoami.grants.length), { exact: true }),
      'settings tab/profile Effect: membership count returned by live whoami',
    ).toBeVisible()
    exercised.add('tab/profile')

    const about = await openTab(page, /^About$/i)
    await expectPainted(
      about.getByText(serverInfo.version, { exact: true }),
      'settings tab/about Effect: version returned by live server-info',
    )
    await expect(
      about.getByText(serverInfo.engine, { exact: true }),
      'settings tab/about Effect: store engine returned by live server-info',
    ).toBeVisible()
    await expect(
      about.getByText(serverInfo.license.status, { exact: true }),
      'settings tab/about Effect: license status returned by live server-info',
    ).toBeVisible()
    exercised.add('tab/about')

    await openTab(page, /^Appearance$/i)

    async function exerciseTrigger(
      id: 'theme' | 'density' | 'language',
      optionNames: readonly string[],
    ) {
      const ledgerId = `trigger/${id}`
      // Radix temporarily aria-hides the panel while its portalled listbox is
      // open. A role-scoped descendant locator therefore disappears mid-click;
      // the stable id still names the same owned trigger throughout the cycle.
      const trigger = page.locator(`#${id}`)
      await expect(
        trigger,
        `settings ${ledgerId} Rendered: trigger is missing`,
      ).toBeEnabled()
      await expect(trigger).toHaveAttribute('aria-expanded', 'false')
      await trigger.click()
      await expect(
        trigger,
        `settings ${ledgerId} Fired: click did not open a listbox`,
      ).toHaveAttribute('aria-expanded', 'true')
      const listbox = page.getByRole('listbox')
      await expect(
        listbox.getByRole('option'),
        `settings ${ledgerId} Effect: wrong option cardinality`,
      ).toHaveCount(optionNames.length)
      for (const name of optionNames)
        await expect(
          listbox.getByRole('option', { name, exact: true }),
          `settings ${ledgerId} Effect: missing option ${name}`,
        ).toBeVisible()
      await page.keyboard.press('Escape')
      exercised.add(ledgerId)
    }

    await exerciseTrigger('theme', ['Light', 'Dark', 'System'])
    await exerciseTrigger('density', ['Comfortable', 'Compact'])
    await exerciseTrigger('language', [
      'English',
      'Español',
      '中文',
      '日本語',
      'Deutsch',
      'Русский',
      'Français',
    ])

    async function chooseOption(
      selectId: 'theme' | 'density' | 'language',
      label: string,
      ledgerId: string,
    ) {
      const trigger = page.locator(`#${selectId}`)
      await trigger.click()
      const option = page.getByRole('option', { name: label, exact: true })
      await expect(
        option,
        `settings ${ledgerId} Rendered: option is missing`,
      ).toBeEnabled()
      await option.click()
      await expect(
        trigger,
        `settings ${ledgerId} Fired: selected value did not reach the trigger`,
      ).toContainText(label)
      exercised.add(ledgerId)
    }

    // Preference controls have no server mutation by contract. Ignore the session's
    // independent proactive refresh if its clock happens to fire during this step.
    const preferenceMutations: string[] = []
    const recordPreferenceMutation = (request: Request) => {
      if (
        request.method() !== 'GET' &&
        !request.url().endsWith('/v1/auth/refresh')
      )
        preferenceMutations.push(`${request.method()} ${request.url()}`)
    }
    page.on('request', recordPreferenceMutation)

    for (const [code, label] of [
      ['light', 'Light'],
      ['dark', 'Dark'],
      ['system', 'System'],
    ] as const) {
      const ledgerId = `theme/${code}`
      await chooseOption('theme', label, ledgerId)
      const applied = await page.evaluate((theme) => {
        const systemDark = window.matchMedia(
          '(prefers-color-scheme: dark)',
        ).matches
        return {
          stored: localStorage.getItem('olivares.theme'),
          dark: document.documentElement.classList.contains('dark'),
          expectedDark: theme === 'dark' || (theme === 'system' && systemDark),
        }
      }, code)
      expect(
        applied.stored,
        `settings ${ledgerId} Effect: logical theme was not persisted`,
      ).toBe(code)
      expect(
        applied.dark,
        `settings ${ledgerId} Effect: document theme does not match resolution`,
      ).toBe(applied.expectedDark)
    }

    // Comfortable and English are defaults: return to each from another value so
    // their own handlers must fire rather than selecting the already-current item.
    for (const [code, label] of [
      ['compact', 'Compact'],
      ['comfortable', 'Comfortable'],
    ] as const) {
      const ledgerId = `density/${code}`
      await chooseOption('density', label, ledgerId)
      await expect
        .poll(
          () =>
            page.evaluate(() => {
              const value = JSON.parse(
                localStorage.getItem('olivares.prefs') ?? '{}',
              ) as { state?: { density?: string } }
              return value.state?.density
            }),
          { message: `settings ${ledgerId} Effect: density was not persisted` },
        )
        .toBe(code)
    }

    for (const [code, label] of [
      ['es', 'Español'],
      ['zh', '中文'],
      ['ja', '日本語'],
      ['de', 'Deutsch'],
      ['ru', 'Русский'],
      ['fr', 'Français'],
      ['en', 'English'],
    ] as const) {
      const ledgerId = `language/${code}`
      await chooseOption('language', label, ledgerId)
      await expect
        .poll(
          () =>
            page.evaluate(() => ({
              stored: localStorage.getItem('olivares.lang'),
              documentLanguage: document.documentElement.lang,
            })),
          {
            message: `settings ${ledgerId} Effect: language was not persisted and synchronized`,
          },
        )
        .toEqual({ stored: code, documentLanguage: code })
    }

    page.off('request', recordPreferenceMutation)
    expect(
      preferenceMutations,
      'settings Appearance Effect: local preferences emitted an engine mutation',
    ).toEqual([])
    expect(
      [...exercised].sort(),
      'settings control ledger: Rendered/Fired/Effect did not account for exactly 18 controls',
    ).toEqual([...expectedControls].sort())
  })

  await page.screenshot({
    path: 'playwright-report/four-tabs-live.png',
    fullPage: true,
  })
})
