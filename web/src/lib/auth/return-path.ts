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

/** Sign-in can return only to a console page on this origin. Authorization
 * remains the destination route's responsibility. Keep its query and fragment. */
export function consoleReturnPath(
  candidate: unknown,
  origin: string,
): string | null {
  if (
    typeof candidate !== 'string' ||
    !candidate.startsWith('/') ||
    candidate.startsWith('//') ||
    /[\\\u0000-\u0020]/.test(candidate) ||
    isSignInPath(candidate)
  )
    return null
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
