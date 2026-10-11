// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { resolveLocation } from '@/features/navigation/model'

/** The pages a signed-out person is sent to. Never a page to return to after
 * signing in: that would send them straight back to sign-in. */
const SIGN_IN_PATHS = ['/login', '/setup', '/accept-invite']

export function isSignInPath(href: string): boolean {
  const path = href.split(/[?#]/)[0].replace(/\/+$/, '')
  return SIGN_IN_PATHS.some((p) => path === p || path.startsWith(p + '/'))
}

/** A path on this origin as written: one leading slash, and no backslash, space or control
 * character, each of which a URL parser may drop or rewrite into another address. */
export function isPlainOriginPath(value: unknown): value is string {
  if (
    typeof value !== 'string' ||
    !value.startsWith('/') ||
    value.startsWith('//')
  )
    return false
  for (const c of value) if (c === '\\' || c <= ' ') return false
  return true
}

/** Sign-in can return only to a console page on this origin. Authorization
 * remains the destination route's responsibility. Keep its query and fragment. */
export function consoleReturnPath(
  candidate: unknown,
  origin: string,
): string | null {
  if (!isPlainOriginPath(candidate) || isSignInPath(candidate)) return null
  try {
    const url = new URL(candidate, origin)
    if (
      url.origin !== origin ||
      resolveLocation(url.pathname).kind === 'unknown'
    )
      return null
    return url.pathname + url.search + url.hash
  } catch {
    return null
  }
}
