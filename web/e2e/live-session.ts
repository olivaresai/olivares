// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { Page } from '@playwright/test'

// The console signs in with a cookie session: the login answers `{session_id, csrf_token,
// expires_at}` and sets this HttpOnly cookie, whose value IS the session token (core/api/
// browser_session.go). A spec that also reads the API as that same session needs the token,
// and the cookie jar of its own context is where it is.
//
// ⛔ `page.request` CANNOT CARRY THAT COOKIE ON ITS OWN. It is `Secure`, and Playwright's
//    API client sends a Secure cookie only over https or to `localhost`, never to
//    `http://127.0.0.1` where every browser script here serves. The engine then answers 401
//    to a call the page itself would have made successfully. Measured on the supported demo
//    run (#1181). The token goes in `Authorization: Bearer`, the form the engine accepts
//    from any non-browser caller.
const SESSION_COOKIE = '__Host-olivares-session'

/**
 * A session the engine refuses is a broken fixture, not data still on its way. A poll that
 * treats every non-OK answer as "not yet" runs out its timeout and blames the data; this
 * names the refusal the moment it happens.
 */
export function assertSessionAccepted(response: { status(): number }): void {
  const status = response.status()
  if (status === 401 || status === 403)
    throw new Error(`the engine refused the session token (HTTP ${status})`)
}

/** The signed-in page's session token, for API calls made beside the page. */
export async function sessionToken(page: Page): Promise<string> {
  const cookie = (await page.context().cookies()).find(
    (entry) => entry.name === SESSION_COOKIE,
  )
  if (!cookie) throw new Error('the sign-in left no session cookie')
  return cookie.value
}
