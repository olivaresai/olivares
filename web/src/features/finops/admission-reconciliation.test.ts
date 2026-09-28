// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { finopsApi, finopsKeys } from './api'

/**
 * The engine serves two admission reconciliations and only one of them is a read.
 * `GET /admission/reconciliation` reports (budget read); `POST /admission/reconcile`
 * runs recovery, sweeps lapsed holds and files the drift finding (budget write). A
 * console that called the second would move the ledger whenever a screen opened, so
 * this checks the verb and the path of the request that leaves, and that the report
 * comes back as the engine published it.
 */

let requests: Array<{ url: string; method: string }> = []

function captureFetch(body: unknown): void {
  globalThis.fetch = vi.fn(async (url: string, init?: RequestInit) => {
    requests.push({ url: String(url), method: init?.method ?? 'GET' })
    return new Response(JSON.stringify(body), {
      status: 200,
      headers: new Headers({ 'Content-Type': 'application/json' }),
    })
  }) as never
}

const counters = {
  pending_retired: 0,
  owed_released: 0,
  owed_cleared: 0,
  owed_remaining: 0,
  legacy_pending: 0,
  legacy_retired: 0,
  legacy_owes_release: 0,
  legacy_stop: 'absent',
  unresolved: 0,
  undecodable: 0,
  undecodable_cleared: 0,
  frontier_blocked: 0,
  corrupt: 0,
}

afterEach(() => {
  requests = []
})

describe('the console admission reconciliation', () => {
  it('asks for the READ, with GET, and never for the job', async () => {
    captureFetch({
      swept_expired: 0,
      active: 2,
      committed: 5,
      released: 1,
      expired_unsettled: 0,
      active_lapsed: 0,
      idempotency_orphans: 0,
      ...counters,
      drift: false,
      note: 'reservation ledger matches commits and releases',
    })

    await finopsApi.admissionReconciliation({ tenant: 't-admission' })

    expect(requests).toHaveLength(1)
    expect(new URL(requests[0].url, 'http://test').pathname).toBe(
      '/v1/m/finops/admission/reconciliation',
    )
    expect(requests[0].method).toBe('GET')
  })

  it('returns the counters as the engine publishes them, corrupt rows included', async () => {
    captureFetch({
      swept_expired: 0,
      active: 3,
      committed: 0,
      released: 0,
      expired_unsettled: 0,
      active_lapsed: 2,
      idempotency_orphans: 1,
      ...counters,
      owed_remaining: 4,
      legacy_stop: 'waiting',
      unresolved: 1,
      corrupt: 2,
      drift: true,
      note: 'reservation ledger drifted from caller settlement',
    })

    const report = await finopsApi.admissionReconciliation({
      tenant: 't-admission',
    })

    expect(report.drift).toBe(true)
    // A lapsed hold the job has not swept is read here, and swept_expired stays 0
    // because this route sweeps nothing.
    expect(report.active_lapsed).toBe(2)
    expect(report.swept_expired).toBe(0)
    expect(report.corrupt).toBe(2)
    expect(report.unresolved).toBe(1)
    expect(report.owed_remaining).toBe(4)
    expect(report.legacy_stop).toBe('waiting')
    expect(report.finding_ref).toBeUndefined()
  })

  it('has a query key of its own per tenant', () => {
    expect(finopsKeys.admissionReconciliation('t-admission')).toEqual([
      'finops',
      't-admission',
      'admission',
      'reconciliation',
    ])
  })
})
