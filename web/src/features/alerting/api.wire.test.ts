// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT THE CONSOLE ACTUALLY PUTS ON THE WIRE.
//
// Its sibling api.test.ts mocks `@/lib/api/client`, so every assertion there is about
// what this module MEANT to send. That is precisely the layer at which the campaign's
// two measured defects survive a green cell:
//
//   - the double encoding: http.post runs its `body` through JSON.stringify
//     (lib/api/client.ts:139), so a STRING body leaves re-quoted and a mocked `http`
//     records the pre-encoding value and reports agreement;
//   - the missing precondition: a mocked client answers 200 to a request the engine
//     would refuse with 400 (this is how capabilities.test.tsx:189-192 stayed green
//     while approve/unpin were broken).
//
// So this file mocks ONLY globalThis.fetch. Every expectation below is about the
// Request a browser would issue. The engine-side half of the same contract — the very
// same path, issued against the real router — is
// modules/notify/consolecontract_test.go, which reads it out of api.ts rather than
// retyping it.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { notifyApi } from './api'

let lastUrl: string | undefined
let lastInit: RequestInit | undefined

function mockFetch(status: number, body?: unknown) {
  globalThis.fetch = vi.fn(async (url: string, init?: RequestInit) => {
    lastUrl = String(url)
    lastInit = init
    return new Response(body === undefined ? null : JSON.stringify(body), {
      status,
      headers: new Headers({ 'Content-Type': 'application/json' }),
    })
  }) as never
}

afterEach(() => {
  lastUrl = undefined
  lastInit = undefined
  vi.restoreAllMocks()
})

describe('the notify outbox client, on the wire', () => {
  it('lists the dead-letter queue as a query filter on the outbox route', async () => {
    mockFetch(200, { items: [], has_more: false })
    await notifyApi.listOutbox({ status: 'dead' })
    // The DLQ is not its own route: it is GET /outbox filtered by status
    // (modules/notify/api.go:46, outbox_api.go:59-61).
    expect(lastUrl).toBe('/v1/m/notify/outbox?status=dead')
    expect(lastInit?.method).toBe('GET')
  })

  it('carries the keyset cursor and limit the engine reads', async () => {
    mockFetch(200, { items: [], has_more: false })
    await notifyApi.listOutbox({ status: 'dead', cursor: 'c1', limit: 50 })
    // notify's listQuery reads exactly ?cursor and ?limit (helpers.go:112-123).
    expect(lastUrl).toBe('/v1/m/notify/outbox?status=dead&cursor=c1&limit=50')
  })

  it('requeues with NO body and NO Content-Type', async () => {
    mockFetch(200, { id: 'ob-1', status: 'queued' })
    await notifyApi.redeliverOutbox('ob-1')

    expect(lastUrl).toBe('/v1/m/notify/outbox/ob-1/redeliver')
    expect(lastInit?.method).toBe('POST')
    // THE ASSERTION THIS FILE EXISTS FOR. handleRedeliverOutbox reads the id from the
    // path and decodes nothing else (modules/notify/outbox_api.go:93-98). `undefined`,
    // not '', not '{}', and above all not '"ob-1"' — a string argument to http.post
    // would arrive here re-quoted by client.ts:139, which is the one remaining live
    // instance of that defect elsewhere in this console.
    expect(lastInit?.body).toBeUndefined()
    expect(new Headers(lastInit?.headers).get('Content-Type')).toBeNull()
  })

  it('encodes an id that would otherwise change the path', async () => {
    mockFetch(200, { id: 'x', status: 'queued' })
    await notifyApi.redeliverOutbox('ob/1')
    // Without encodeURIComponent this would address a different route entirely.
    expect(lastUrl).toBe('/v1/m/notify/outbox/ob%2F1/redeliver')
  })

  it('surfaces a refused requeue as an ApiError carrying the engine STATUS', async () => {
    mockFetch(409, {
      error: {
        code: 'conflict',
        message:
          'delivery is in flight; only a terminal (delivered/dead) row can be redelivered',
      },
    })
    // The view chooses its sentence from `status`, never from this prose — the prose is
    // not a contract and is not translated. So the status has to survive the client.
    await expect(notifyApi.redeliverOutbox('ob-1')).rejects.toBeInstanceOf(
      ApiError,
    )
    mockFetch(409, { error: { code: 'conflict', message: 'in flight' } })
    await expect(notifyApi.redeliverOutbox('ob-1')).rejects.toMatchObject({
      status: 409,
    })
  })

  it('refuses a list whose items are not an array instead of showing it as empty', async () => {
    // "No dead letters" and "the server did not answer properly" must never look the
    // same on a DLQ screen (lib/api/client.ts assertListEnvelope).
    mockFetch(200, { items: null, has_more: false })
    await expect(
      notifyApi.listOutbox({ status: 'dead' }),
    ).rejects.toBeInstanceOf(ApiError)
  })
})
