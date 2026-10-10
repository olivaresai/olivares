// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, it } from 'vitest'
import { ssoStartHref } from '@/app/pages/login-sso'
import {
  consoleReturnPath,
  isPlainOriginPath,
  isSignInPath,
} from './return-path'
const origin = 'https://console.example.test:8443'

it.each([
  '/audit?from=2026-09-30&kind=write#entry-7',
  '/session-viewer/run-17?at=4#output',
  '/settings',
  '/areas/ai',
  '/',
])('preserves a requested console page (%s)', (path) => {
  expect(consoleReturnPath(path, origin)).toBe(path)
})
it.each([
  undefined,
  '//attacker.example',
  '/\\attacker.example',
  'https://attacker.example/sessions',
  'https://console.example.test:8443/audit',
  'javascript:alert(1)',
  '/v1/auth/logout',
  '/openapi.json',
  '/login?returnTo=/login',
  '/setup',
  '/unknown',
  '/%2f%2fattacker.example',
  '/audit\n//attacker.example',
])('rejects a non-console or unsafe destination (%s)', (path) => {
  expect(consoleReturnPath(path, origin)).toBeNull()
})

it.each([
  '/login',
  '/login?returnTo=%2Faudit',
  '/setup/',
  '/accept-invite?token=x',
])('knows a sign-in page (%s)', (path) => {
  expect(isSignInPath(path)).toBe(true)
  expect(consoleReturnPath(path, origin)).toBeNull()
})
it.each(['/', '/sessions', '/loginx', '/settings?tab=login'])(
  'is not fooled by a lookalike (%s)',
  (path) => {
    expect(isSignInPath(path)).toBe(false)
  },
)

// Every character U+0000 to U+0020 and the backslash end a plain path; U+0021 does not.
const refused = [
  '\\',
  ...Array.from({ length: 0x21 }, (_, code) => String.fromCharCode(code)),
]
it('refuses a path with a backslash, space or control character, in both callers', () => {
  for (const c of refused) {
    const path = `/audit${c}x`
    expect(isPlainOriginPath(path)).toBe(false)
    expect(consoleReturnPath(path, origin)).toBeNull()
    expect(ssoStartHref(`/v1/auth/sso/start${c}x`, null, origin)).toBeNull()
  }
  expect(isPlainOriginPath('/audit!x')).toBe(true)
  expect(ssoStartHref('/v1/auth/sso/start', null, origin)).toBe(
    '/v1/auth/sso/start?browser_session=1',
  )
})
