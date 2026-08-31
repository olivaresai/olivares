// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE HALF OF THE CONTRACT THE CONSOLE OWNS.
//
// THE CLASS THIS CLOSES. Until today two suites were green and said opposite things:
// capabilities.test.tsx asserted `approveToolPin` was called with `{tool, from_drift}`
// and passed, while modules/capabilities/toolpins_evidence_test.go:166-193 proved the
// engine answers 400 to exactly that body. Neither was wrong about what it measured —
// the component test mocks `./api`, so its double accepted what production rejects, and
// nothing anywhere compared the two. A mocked mutationFn cannot fail a contract test; it
// IS the contract, restated.
//
// So this file mocks NOTHING of the client's REQUEST path. It stubs `fetch` at the very
// edge, drives the REAL builders and the REAL shared http client, and records the exact
// wire request into a golden fixture that
// modules/capabilities/toolpins_console_contract_test.go replays against the REAL engine
// handler. For the REQUEST, neither side can be green alone with a broken payload:
//
//   - the console drifts, golden not regenerated  → this file fails (captured ≠ golden)
//   - the console drifts, golden regenerated too  → the Go test fails (engine 4xx)
//
// ⚠ THE RESPONSE HALF IS WEAKER, AND SAYING SO IS THE POINT — the the model contrast of
// Named it. `accepted()` below is a HAND-WRITTEN stub: this file never sees a body
// the engine produced, so it cannot catch a response-shape drift on its own. What closes
// that direction is the Go side asserting the 202 body carries every field this file's
// `toolPinActionSchema` requires. Two halves of one claim, in two languages — not an
// end-to-end run, and it does not replace one.
//
// Regenerate deliberately with OLIVARES_UPDATE_GOLDEN=1, and read the Go test's verdict
// before believing the new file.
import { readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { configureApiClient } from '@/lib/api/client'
import {
  buildApproveIntent,
  buildUnpinIntent,
  capabilitiesApi,
  resolveToolPinIntent,
  type DriftedToolPin,
} from './api'

// Resolved from the vitest root (web/), not from import.meta.url: the transform does not
// hand these modules a file: URL. A wrong cwd fails loudly on the read — never silently.
const FIXTURE_PATH = resolve(
  process.cwd(),
  'src/features/capabilities/testdata/toolpin-console-wire.json',
)

/** One captured HTTP request, in the shape the Go replay consumes. Header names are
 * lowercased by the Headers object; Go canonicalizes them on Set, so the replay is
 * unaffected and the golden stays diff-stable. */
interface WireRequest {
  method: string
  path: string
  headers: Record<string, string>
  body: unknown
}

interface WireFixture {
  /** The pin row the console read from GET /toolpins. The Go test seeds the engine's
   * durable fake with EXACTLY this, so `expected_version` and
   * `expected_drift_fingerprint` are checked against the state they were read from —
   * the round trip, not a coincidence. */
  pin: DriftedToolPin
  /** Fixed so the golden is byte-stable. The real generator is asserted separately. */
  idempotency_key: string
  requests: { approve: WireRequest; unpin: WireRequest }
}

function readFixture(): WireFixture {
  return JSON.parse(readFileSync(FIXTURE_PATH, 'utf8')) as WireFixture
}

let captured: WireRequest[] = []

/** A 202 exactly as the engine writes it (modules/capabilities/toolpins.go:224-228) —
 * the client parses the body, so a lazier stub would fail for the wrong reason. */
function accepted(version: number): Response {
  return new Response(
    JSON.stringify({
      tool: 'github.search',
      operation_id: 'op-1',
      apply_state: 'applied',
      version,
      evidence_ref: 'ref-1',
    }),
    { status: 202, headers: { 'Content-Type': 'application/json' } },
  )
}

beforeEach(() => {
  captured = []
  configureApiClient({
    getToken: () => 'test-token',
    getTenant: () => 't1',
    onUnauthorized: () => {},
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init: RequestInit) => {
      const headers = new Headers(init.headers)
      captured.push({
        method: init.method ?? 'GET',
        path: String(url),
        headers: Object.fromEntries(headers.entries()),
        body: init.body ? JSON.parse(String(init.body)) : null,
      })
      return accepted(8)
    }),
  )
})
// restoreAllMocks, not just unstubAllGlobals: the golden case spies on crypto.randomUUID,
// and a spy that survives teardown would pin every later key to the same value — which
// would make "two intentions are two keys" pass for a client that mints one key forever.
afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('tool-pin wire contract — what the console actually sends', () => {
  it('matches the golden the engine test replays', async () => {
    const fixture = readFixture()
    vi.spyOn(crypto, 'randomUUID').mockReturnValue(
      fixture.idempotency_key as `${string}-${string}-${string}-${string}-${string}`,
    )

    const approveIntent = buildApproveIntent(fixture.pin)
    const unpinIntent = buildUnpinIntent(fixture.pin)
    await capabilitiesApi.sendToolPinIntent(approveIntent)
    await capabilitiesApi.sendToolPinIntent(unpinIntent)

    resolveToolPinIntent(approveIntent)
    resolveToolPinIntent(unpinIntent)
    const [approve, unpin] = captured
    if (process.env.OLIVARES_UPDATE_GOLDEN === '1') {
      writeFileSync(
        FIXTURE_PATH,
        `${JSON.stringify({ ...fixture, requests: { approve, unpin } }, null, 2)}\n`,
      )
    }
    const golden = readFixture()
    expect(approve).toEqual(golden.requests.approve)
    expect(unpin).toEqual(golden.requests.unpin)
  })

  // liveIntents is module state whose lifetime is deliberately longer than a dialog, so a
  // test that leaves an intention unresolved hands its key to the next test. Each case
  // below resolves what it builds — the same discipline the view follows — rather than
  // reaching for a test-only reset that production would never call.

  // The three properties the golden alone cannot prove, because a golden records ONE
  // capture and these are about how the next one is produced.
  it('carries the Idempotency-Key as a HEADER, never in the body', async () => {
    const fixture = readFixture()
    const intent = buildApproveIntent(fixture.pin)
    await capabilitiesApi.sendToolPinIntent(intent)
    resolveToolPinIntent(intent)
    const [req] = captured
    expect(req.headers['idempotency-key']).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i,
    )
    expect(req.body).not.toHaveProperty('Idempotency-Key')
    expect(req.body).not.toHaveProperty('idempotency_key')
  })

  it('REUSES one key across a retry of the same intention', async () => {
    const fixture = readFixture()
    // One intention, sent twice — what a confirm-click after a network failure does.
    const intent = buildApproveIntent(fixture.pin)
    await capabilitiesApi.sendToolPinIntent(intent)
    await capabilitiesApi.sendToolPinIntent(intent)
    expect(captured[0].headers['idempotency-key']).toBe(
      captured[1].headers['idempotency-key'],
    )
    // ...and once the intention RESOLVES, the next one is genuinely a new key, or the
    // guard above would pass for a client that hardcodes one key forever.
    resolveToolPinIntent(intent)
    const second = buildApproveIntent(fixture.pin)
    await capabilitiesApi.sendToolPinIntent(second)
    expect(captured[2].headers['idempotency-key']).not.toBe(
      captured[0].headers['idempotency-key'],
    )
    resolveToolPinIntent(second)
  })

  it('keeps the key when the operator closes and reopens an UNRESOLVED decision', async () => {
    // The expensive case, named by the the model contrast of: the server committed
    // the effect and the response was lost. The operator closes the dialog and clicks the
    // same action again. With a key minted per click that second attempt is a different
    // OperationID, so the engine can no longer dedup it to the original outcome.
    const fixture = readFixture()
    const first = buildApproveIntent(fixture.pin)
    await capabilitiesApi.sendToolPinIntent(first)
    // Dialog closed, dialog reopened — a fresh call to the builder, no shared object.
    const reopened = buildApproveIntent(fixture.pin)
    await capabilitiesApi.sendToolPinIntent(reopened)
    expect(reopened.key).toBe(first.key)
    expect(captured[1].headers['idempotency-key']).toBe(
      captured[0].headers['idempotency-key'],
    )

    // But a decision taken against a MOVED row is a different intention and must not
    // inherit the key — reusing it with a different body is what the engine refuses as a
    // replay, and rightly.
    resolveToolPinIntent(first)
    const moved = buildApproveIntent({ ...fixture.pin, version: 99 })
    expect(moved.key).not.toBe(first.key)
    resolveToolPinIntent(moved)
  })

  it('sends the preconditions read from the pin row, not invented ones', async () => {
    const fixture = readFixture()
    const pin: DriftedToolPin = { ...fixture.pin, version: 41 }
    const a = buildApproveIntent(pin)
    const u = buildUnpinIntent(pin)
    await capabilitiesApi.sendToolPinIntent(a)
    await capabilitiesApi.sendToolPinIntent(u)
    resolveToolPinIntent(a)
    resolveToolPinIntent(u)
    expect(captured[0].body).toMatchObject({
      expected_version: 41,
      expected_drift_fingerprint: pin.drift_fingerprint,
      from_drift: true,
    })
    expect(captured[1].body).toMatchObject({ expected_version: 41 })
  })
})
