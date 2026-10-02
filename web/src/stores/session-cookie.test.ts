// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import { beforeEach, expect, it, vi } from 'vitest'
import { useSessionStore } from './session'

beforeEach(() => localStorage.clear())

it('never persists the session bearer when a session is adopted', () => {
  useSessionStore.getState().setSession({
    csrfToken: 'csrf_fixture',
    sessionId: 'sid',
    expiresAt: '2030-01-01T00:00:00Z',
  })
  expect(localStorage.getItem('olivares.session')).toBeNull()
})

it('copies only non-secret fields when a transport envelope contains an extra bearer', () => {
  const response = {
    csrfToken: 'csrf_fixture',
    sessionId: 'sid',
    expiresAt: '',
    token: 'olvs_fixture',
  }
  useSessionStore.getState().setSession(response)
  expect('token' in useSessionStore.getState()).toBe(false)
  expect(localStorage.getItem('olivares.session')).toBeNull()
})

it('adopts and clears a cookie session when browser storage is disabled', () => {
  const remove = vi
    .spyOn(Storage.prototype, 'removeItem')
    .mockImplementation(() => {
      throw new DOMException('Storage disabled', 'SecurityError')
    })
  try {
    useSessionStore.getState().setSession({
      csrfToken: 'csrf_fixture',
      sessionId: 'sid',
      expiresAt: '',
    })
    expect(useSessionStore.getState().sessionId).toBe('sid')
    useSessionStore.getState().clear()
    expect(useSessionStore.getState().sessionId).toBeNull()
  } finally {
    remove.mockRestore()
  }
})
