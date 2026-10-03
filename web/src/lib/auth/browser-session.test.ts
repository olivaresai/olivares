// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import { beforeEach, expect, it, vi } from 'vitest'
import { useSessionStore } from '@/stores/session'
import { restoreBrowserSession, restoreOnPageLoad } from './browser-session'

const fetcher = vi.fn()
const metadata = {
  csrf_token: 'csrf-fixture',
  session_id: 'sid',
  expires_at: '2030-01-01T00:00:00Z',
}
beforeEach(() => {
  localStorage.clear()
  useSessionStore.getState().clear()
  useSessionStore.setState({ ready: false })
  fetcher.mockReset()
  vi.stubGlobal('fetch', fetcher)
})

it('restores an HttpOnly session on reload without readable browser credentials', async () => {
  fetcher.mockResolvedValue(new Response(JSON.stringify(metadata)))
  await restoreBrowserSession()
  const request = fetcher.mock.calls[0][1]
  expect(request.method).toBe('GET')
  expect(request.credentials).toBe('same-origin')
  expect(request.headers.has('Authorization')).toBe(false)
  expect(useSessionStore.getState().sessionId).toBe('sid')
  expect(localStorage.getItem('olivares.session')).toBeNull()
})

it.each(['olvs_fixture', 'olvt_scoped_fixture'])(
  'migrates a legacy session once and removes storage (%s)',
  async (token) => {
    localStorage.setItem(
      'olivares.session',
      JSON.stringify({ state: { token } }),
    )
    fetcher.mockResolvedValueOnce(new Response('', { status: 401 }))
    fetcher.mockResolvedValueOnce(new Response(JSON.stringify(metadata)))
    await Promise.all([restoreBrowserSession(), restoreBrowserSession()])
    expect(fetcher).toHaveBeenCalledTimes(2)
    const request = fetcher.mock.calls[1][1]
    expect(request.method).toBe('POST')
    expect(request.headers.get('Authorization')).toBe(`Bearer ${token}`)
    expect(request.headers.get('X-Olivares-Session')).toBe('cookie')
    expect(localStorage.getItem('olivares.session')).toBeNull()
    expect(useSessionStore.getState().csrfToken).toBe(metadata.csrf_token)
    expect('token' in useSessionStore.getState()).toBe(false)
  },
)

it('a transient migration failure retains the legacy session for retry', async () => {
  const stored = JSON.stringify({ state: { token: 'olvs_fixture' } })
  localStorage.setItem('olivares.session', stored)
  fetcher.mockRejectedValue(new Error('fixture offline'))
  await expect(restoreBrowserSession()).rejects.toThrow()
  expect(localStorage.getItem('olivares.session')).toBe(stored)
  expect(useSessionStore.getState().sessionId).toBeNull()
})

it('an expired or revoked session is cleared instead of renewed', async () => {
  localStorage.setItem(
    'olivares.session',
    JSON.stringify({ state: { token: 'olvs_expired' } }),
  )
  fetcher.mockResolvedValue(new Response('', { status: 401 }))
  await restoreBrowserSession()
  expect(localStorage.getItem('olivares.session')).toBeNull()
  expect(useSessionStore.getState().ready).toBe(true)
  expect(useSessionStore.getState().sessionId).toBeNull()
})

it('recovers a cookie from an interrupted migration without replaying its stale bearer', async () => {
  localStorage.setItem(
    'olivares.session',
    JSON.stringify({ state: { token: 'olvs_rotated' } }),
  )
  fetcher.mockResolvedValue(new Response(JSON.stringify(metadata)))
  await restoreBrowserSession()
  expect(fetcher).toHaveBeenCalledTimes(1)
  expect(fetcher.mock.calls[0][1].method).toBe('GET')
  expect(localStorage.getItem('olivares.session')).toBeNull()
  expect(useSessionStore.getState().sessionId).toBe('sid')
})

it.each([200, 401])(
  'a delayed restore (%s) leaves a newer sign-in intact',
  async (status) => {
    let finish!: (response: Response) => void
    fetcher.mockImplementationOnce(
      () =>
        new Promise<Response>((resolve) => {
          finish = resolve
        }),
    )
    const restoring = restoreBrowserSession()
    const newer = {
      csrfToken: 'newer-csrf',
      sessionId: 'newer-session',
      expiresAt: '2030-01-02T00:00:00Z',
    }
    useSessionStore.getState().setSession(newer)
    finish(
      new Response(status === 200 ? JSON.stringify(metadata) : null, {
        status,
      }),
    )
    await restoring
    expect(useSessionStore.getState()).toMatchObject(newer)
  },
)

it('a retired restore cannot send captured legacy credentials after a new sign-in', async () => {
  localStorage.setItem(
    'olivares.session',
    JSON.stringify({ state: { token: 'olvs_old' } }),
  )
  let finish!: (response: Response) => void
  fetcher.mockImplementationOnce(
    () =>
      new Promise<Response>((resolve) => {
        finish = resolve
      }),
  )
  fetcher.mockResolvedValue(new Response(JSON.stringify(metadata)))
  const restoring = restoreBrowserSession()
  useSessionStore.getState().setSession({
    csrfToken: 'newer-csrf',
    sessionId: 'newer-session',
    expiresAt: '',
  })
  finish(new Response(null, { status: 401 }))
  await restoring
  expect(fetcher).toHaveBeenCalledTimes(1)
  expect(useSessionStore.getState().sessionId).toBe('newer-session')
})

it('a delayed JSON body cannot replace a newer session', async () => {
  let finish!: (body: unknown) => void
  const response = new Response()
  response.json = () =>
    new Promise((resolve) => {
      finish = resolve
    })
  fetcher.mockResolvedValue(response)
  const restoring = restoreBrowserSession()
  await vi.waitFor(() => expect(finish).toBeDefined())
  useSessionStore.getState().setSession({
    csrfToken: 'newer-csrf',
    sessionId: 'newer-session',
    expiresAt: '',
  })
  finish(metadata)
  await restoring
  expect(useSessionStore.getState().sessionId).toBe('newer-session')
})

it('clearing an anonymous session cancels an in-flight restore', async () => {
  let finish!: (response: Response) => void
  fetcher.mockImplementationOnce(
    () =>
      new Promise<Response>((resolve) => {
        finish = resolve
      }),
  )
  const restoring = restoreBrowserSession()
  useSessionStore.getState().clear()
  finish(new Response(JSON.stringify(metadata)))
  await restoring
  expect(useSessionStore.getState().sessionId).toBeNull()
  expect(useSessionStore.getState().ready).toBe(true)
})

// Root on FH 034: every first hour since 06 showed one red request. On a clean install
// the page-load restore was sent before server-info, and the setup gate answered it 409
// setup_required. No session can exist before the first administrator, so it is not sent.
it('a clean install sends no session restore until setup is done', async () => {
  localStorage.setItem(
    'olivares.session',
    JSON.stringify({ state: { token: 'olvs_fixture' } }),
  )
  await restoreOnPageLoad(async () => ({ setup_required: true }))
  expect(fetcher).not.toHaveBeenCalled()
  expect(useSessionStore.getState().ready).toBe(true)
  expect(useSessionStore.getState().sessionId).toBeNull()
  expect(localStorage.getItem('olivares.session')).toBeNull()
})

it('after setup the page-load restore runs as before', async () => {
  fetcher.mockResolvedValue(new Response(JSON.stringify(metadata)))
  await restoreOnPageLoad(async () => ({ setup_required: false }))
  expect(fetcher.mock.calls[0][0]).toBe('/v1/auth/browser-session')
  expect(useSessionStore.getState().sessionId).toBe('sid')
})

it('when server-info cannot be read, the restore runs as before', async () => {
  fetcher.mockResolvedValue(new Response(JSON.stringify(metadata)))
  await restoreOnPageLoad(async () => {
    throw new Error('fixture offline')
  })
  expect(useSessionStore.getState().sessionId).toBe('sid')
})
