// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// Gate 4 — the hold flow and the erasure flow, end to end in a real browser.
//
// These two flows are the reason the session exists: before it, a DPO could only
// place a hold or run a GDPR erasure with curl. Unit tests already pin the
// component logic; what only a browser can prove is that the flow COMPLETES —
// the tab is reachable, the dialog opens, the typed-phrase guard actually blocks
// the confirm button until the phrase matches, and the engine's answer lands on
// screen.
//
// The engine is mocked per-test rather than through the shared fixtures file:
// these flows turn on the exact STATUS the engine returns (202 vs 423 vs 200),
// which a shared 200-for-everything fixture cannot express — and mocking here
// keeps this spec from colliding with other lanes editing fixtures.ts.
import { expect, test, type Page } from '@playwright/test'

const BASE = '**/v1/m/compliance'

const ACTIVE_HOLD = {
  id: 'lh-1',
  matter_ref: 'CASE-42',
  scope_kind: 'subject',
  subject_kind: 'user',
  subject_ref: 'u-7',
  reason: 'litigation hold',
  status: 'active',
  created_by: 'dpo@example.com',
  created_at: '2026-08-01T10:00:00Z',
}

const ERASURE_REQUEST = {
  id: 'er-1',
  subject_kind: 'user',
  subject_token: 'tok-1',
  subject: 'u-7',
  data_classes: ['session_transcript'],
  case_ref: 'DSAR-9',
  reason: 'Art. 17 request',
  requested_by: 'dpo@example.com',
  status: 'received',
  created_at: '2026-08-01T10:00:00Z',
}

/** Seed an authenticated session and answer every /v1 call. `overrides` maps a
 *  path suffix to a [status, body] pair so a test can make the engine answer 202
 *  or 423 on exactly one route. */
async function mockEngine(
  page: Page,
  overrides: Record<string, [number, unknown]> = {},
) {
  await page.addInitScript(() => {
    localStorage.setItem(
      'olivares.session',
      JSON.stringify({
        state: {
          token: 'olvs_demo',
          sessionId: 's1',
          expiresAt: '2030-01-01T00:00:00Z',
        },
        version: 0,
      }),
    )
    localStorage.setItem(
      'olivares.tenant',
      JSON.stringify({ state: { activeTenant: 't-demo' }, version: 0 }),
    )
    localStorage.setItem('olivares.lang', 'en')
  })

  await page.route('**/v1/**', async (route) => {
    const url = new URL(route.request().url())
    const p = url.pathname
    const method = route.request().method()

    for (const [suffix, [status, body]] of Object.entries(overrides)) {
      if (p.endsWith(suffix)) {
        return route.fulfill({
          status,
          contentType: 'application/json',
          body: JSON.stringify(body),
        })
      }
    }

    const json = (body: unknown, status = 200) =>
      route.fulfill({
        status,
        contentType: 'application/json',
        body: JSON.stringify(body),
      })

    if (p.endsWith('/v1/auth/whoami')) {
      return json({
        kind: 'user',
        user_id: 'u-demo',
        actor: 'user:admin@demo',
        display_name: 'Demo Admin',
        superadmin: true,
        grants: [{ tenant: 't-demo', role: 'owner' }],
      })
    }
    if (p.endsWith('/v1/m/compliance/holds') && method === 'GET') {
      return json({ items: [ACTIVE_HOLD] })
    }
    if (p.endsWith('/v1/m/compliance/erasure') && method === 'GET') {
      return json({ items: [ERASURE_REQUEST] })
    }
    if (p.endsWith('/v1/m/compliance/summary')) {
      return json({ frameworks: [], disclaimer: 'not a certification' })
    }
    if (p.endsWith('/stream')) {
      return route.fulfill({
        status: 200,
        contentType: 'text/event-stream',
        body: ': connected\n\n',
      })
    }
    return json({ items: [], has_more: false })
  })
}

async function openTab(page: Page, name: RegExp) {
  await page.goto('/compliance')
  await page.getByRole('tab', { name }).click()
}

test.describe('legal hold flow', () => {
  test('a DPO reaches holds, sees the scope in words, and a 202 release says the hold is STILL ACTIVE', async ({
    page,
  }) => {
    await mockEngine(page, {
      '/holds/lh-1/release': [
        202,
        {
          status: 'pending_approval',
          approval_ref: 'ap-9',
          detail: 'release awaiting dual-control approval (2 distinct humans)',
        },
      ],
    })
    await openTab(page, /legal holds/i)

    // The hold is reachable without curl, and its scope reads as what it matches.
    await expect(page.getByText('CASE-42')).toBeVisible()
    await expect(page.getByText(/Covers subject user:u-7/i)).toBeVisible()

    await page.getByRole('button', { name: /^release$/i }).click()

    // The guard is real: confirm stays disabled until the phrase matches.
    const confirm = page.getByRole('button', { name: /release hold/i })
    await expect(confirm).toBeDisabled()
    await page.locator('#confirm-phrase').fill('RELEASE')
    await expect(confirm).toBeEnabled()
    await confirm.click()

    // The engine opened an approval: nothing was released.
    await expect(page.getByText(/STILL ACTIVE/i)).toBeVisible()
    await expect(page.getByText(/hold released/i)).toHaveCount(0)
  })

  test('the create dialog previews what is ALREADY preserved before confirming', async ({
    page,
  }) => {
    await mockEngine(page, {
      '/holds/check': [
        200,
        {
          held: true,
          holds: [
            { id: 'lh-9', matter_ref: 'CASE-99', scope_kind: 'subject' },
          ],
        },
      ],
    })
    await openTab(page, /legal holds/i)
    await page.getByRole('button', { name: /place a hold/i }).click()

    await page.getByLabel(/matter reference/i).fill('CASE-42')
    await page.getByLabel(/subject kind/i).fill('user')
    await page.getByLabel(/subject reference/i).fill('u-7')

    // The engine's own matching rule, surfaced BEFORE the operator commits.
    await expect(page.getByText(/Already covered by 1 active hold/i)).toBeVisible()
    await expect(page.getByText(/CASE-99/)).toBeVisible()
  })

  test('the preview never answers about a subject the operator has moved on from', async ({
    page,
  }) => {
    // The engine answers "held" ONLY for u-7. If the panel keeps showing that
    // answer after the field changes, an operator reads "already preserved"
    // about a subject nothing preserves — the expensive failure of this dialog.
    // The engine is SLOW here on purpose. The guard protects the window between
    // "the field changed" and "the new answer arrived"; with an instant server
    // that window is a few hundred milliseconds and the final state is correct
    // either way — a first version of this test passed with the guard removed,
    // which meant it was testing nothing. A deliberate delay makes the window
    // wide enough to observe, so removing the guard fails this test.
    await mockEngine(page)
    await page.route('**/v1/m/compliance/holds/check**', async (route) => {
      const ref = new URL(route.request().url()).searchParams.get('subject_ref')
      await new Promise((r) => setTimeout(r, 1500))
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(
          ref === 'u-7'
            ? {
                held: true,
                holds: [
                  { id: 'lh-9', matter_ref: 'CASE-99', scope_kind: 'subject' },
                ],
              }
            : { held: false },
        ),
      })
    })

    await openTab(page, /legal holds/i)
    await page.getByRole('button', { name: /place a hold/i }).click()
    await page.getByLabel(/subject kind/i).fill('user')

    const subjectRef = page.getByLabel(/subject reference/i)
    await subjectRef.fill('u-7')
    await expect(page.getByText(/Already covered by 1 active hold/i)).toBeVisible({
      timeout: 10_000,
    })

    // Move to a different subject, then assert on the window the guard exists
    // for: the ~300ms of debounce during which the query key still holds "u-7".
    //
    // The assertion is `toHaveCount(0)` with a timeout SHORTER than that window.
    // Waiting longer proves nothing — without the guard the stale answer is
    // eventually replaced anyway, which is exactly how the first version of this
    // test passed against the mutation. Failing fast is the point.
    await subjectRef.fill('u-8')
    await expect(page.getByText(/Already covered/i)).toHaveCount(0, {
      timeout: 200,
    })
    await expect(page.getByText(/Checking what already preserves this/i)).toBeVisible({
      timeout: 1000,
    })

    // And the real answer for the new subject does land.
    await expect(page.getByText(/Nothing currently preserves this/i)).toBeVisible({
      timeout: 10_000,
    })
  })

  test('the preview asks the engine again rather than serving a cached answer', async ({
    page,
  }) => {
    // The client default is staleTime 30s. For a dashboard that is right; for
    // "is this subject preserved RIGHT NOW" it is the bug the sol-max contrast
    // found: preview u-7, place a hold, reopen within 30 seconds, and TanStack
    // serves the cached held=false without asking. This counts the requests,
    // because the rendered text is identical either way.
    let checks = 0
    await mockEngine(page)
    await page.route('**/v1/m/compliance/holds/check**', async (route) => {
      checks += 1
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ held: false }),
      })
    })

    await openTab(page, /legal holds/i)
    const open = async () => {
      await page.getByRole('button', { name: /place a hold/i }).click()
      await page.getByLabel(/subject kind/i).fill('user')
      await page.getByLabel(/subject reference/i).fill('u-7')
      await expect(
        page.getByText(/Nothing currently preserves this/i),
      ).toBeVisible({ timeout: 10_000 })
    }

    await open()
    const afterFirst = checks
    expect(afterFirst).toBeGreaterThan(0)

    // Close and reopen well inside the 30s default staleTime, same identifiers.
    await page.keyboard.press('Escape')
    await open()

    expect(
      checks,
      'the scope preview must re-ask the engine, not serve a cached answer about preservation',
    ).toBeGreaterThan(afterFirst)
  })
})

test.describe('GDPR erasure flow', () => {
  test('irreversibility is stated before confirming, and the typed phrase gates it', async ({
    page,
  }) => {
    await mockEngine(page, {
      '/erasure/er-1/execute': [
        202,
        { status: 'pending_approval', approval_ref: 'ap-3' },
      ],
    })
    await openTab(page, /erasure/i)

    await expect(page.getByText('DSAR-9')).toBeVisible()
    await page.getByRole('button', { name: /^execute$/i }).click()

    await expect(page.getByText(/IRREVERSIBLE/)).toBeVisible()
    await expect(page.getByText(/two independent gates/i)).toBeVisible()

    const confirm = page.getByRole('button', { name: /execute erasure/i })
    await expect(confirm).toBeDisabled()
    await page.locator('#confirm-phrase').fill('ERASE')
    await expect(confirm).toBeEnabled()
    await confirm.click()

    // A 202 erased NOTHING and the console says exactly that.
    await expect(page.getByText(/NOTHING HAS BEEN ERASED/i)).toBeVisible()
  })

  test('a legal hold veto (423) names the holds that blocked it', async ({
    page,
  }) => {
    await mockEngine(page, {
      '/erasure/er-1/execute': [
        423,
        {
          error: {
            code: 'legal_hold',
            message: 'blocked by an active legal hold',
            holds: [
              { id: 'lh-7', matter_ref: 'CASE-42', scope_kind: 'subject' },
            ],
          },
        },
      ],
    })
    await openTab(page, /erasure/i)

    await page.getByRole('button', { name: /^execute$/i }).click()
    await page.locator('#confirm-phrase').fill('ERASE')
    await page.getByRole('button', { name: /execute erasure/i }).click()

    await expect(page.getByText(/Blocked by a legal hold/i)).toBeVisible()
    // "Blocked" alone sends the operator back to curl; the matter must be named.
    await expect(page.getByText('CASE-42')).toBeVisible()
  })

  test('an executed erasure shows a receipt with what is retained and the provider floor', async ({
    page,
  }) => {
    await mockEngine(page, {
      '/v1/m/compliance/erasure': [
        200,
        { items: [{ ...ERASURE_REQUEST, status: 'completed' }] },
      ],
      '/erasure/er-1/receipt': [
        200,
        {
          erasure_id: 'er-1',
          subject_kind: 'user',
          subject_token: 'tok-1',
          targets: [{ label: 'sessions', rows: 3 }],
          account_outcome: 'ok',
          provider_outcome: 'ok',
          key_shredded: true,
          verify_ok: true,
          verify_checked: 42,
          retained: [
            {
              records: 'audit ledger events',
              basis: 'GDPR Art. 17(3)(b) legal obligation',
            },
          ],
          case_ref: 'DSAR-9',
          ledger_seq: 7,
          manifest_hash: 'abc123def456',
          occurred_at: '2026-08-02T00:00:00Z',
          provider_floor_days: 30,
          provider_floor_known: true,
          provider_floor_source: 'covered-models',
        },
      ],
    })
    await openTab(page, /erasure/i)

    await page.getByRole('button', { name: /receipt/i }).click()

    await expect(page.getByText(/Key shredded/i)).toBeVisible()
    await expect(page.getByText(/Ledger verified \(42 events\)/i)).toBeVisible()
    // The honest half: what survives, and the provider copy we cannot delete.
    await expect(page.getByText(/audit ledger events/)).toBeVisible()
    await expect(page.getByText(/Art\. 17\(3\)\(b\)/)).toBeVisible()
    await expect(
      page.getByText(/30 days regardless of this erasure/i),
    ).toBeVisible()
  })
})

test.describe('the regulatory calendar reaches an operator', () => {
  test('renders milestones with their source, and marks what is not law', async ({
    page,
  }) => {
    await mockEngine(page, {
      '/v1/m/compliance/calendar': [
        200,
        {
          milestones: [
            {
              id: 'm1',
              regime: 'eu_ai_act',
              date: '2026-08-02',
              title: 'GPAI obligations apply',
              effect: 'Obligations become applicable',
              status: 'in_force',
              source: {
                url: 'https://eur-lex.europa.eu/x',
                title: 'Regulation (EU) 2024/1689',
                publisher: 'Official Journal',
              },
              verified_on: '2026-06-01',
            },
            {
              id: 'm2',
              regime: 'eu_ai_act',
              date: '2026-09-01',
              title: 'Provisional text',
              effect: 'Not yet binding',
              status: 'provisional_agreement',
              source: {
                url: 'https://consilium.europa.eu/y',
                title: 'Council press release',
                publisher: 'Council of the EU',
              },
              verified_on: '2026-06-01',
            },
          ],
          watchlist: [],
          disclaimer:
            'provisional_agreement entries are NOT in-force law',
        },
      ],
    })
    await openTab(page, /regulatory calendar/i)

    await expect(page.getByText('In force')).toBeVisible()
    await expect(
      page.getByText(/Provisional agreement — not law/i),
    ).toBeVisible()
    // Every date carries its citation; that is why the calendar can ship.
    await expect(
      page.getByRole('link', { name: /Regulation \(EU\) 2024\/1689/ }),
    ).toHaveAttribute('href', 'https://eur-lex.europa.eu/x')
    await expect(page.getByText(/verified 2026-06-01/i).first()).toBeVisible()
  })
})
