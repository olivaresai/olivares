// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { create } from 'zustand'

/** The browser owns the HttpOnly cookie. JavaScript holds only metadata and a
 * CSRF token, in memory. Nothing in this store can authenticate on its own. */
/** Why the console's session ended without the person signing out: `expired` when its own
 * expiry had passed, `ended` when the engine refused it earlier (signed out elsewhere, a
 * password change). The sign-in page says which. */
export type SessionEndReason = 'expired' | 'ended'

interface SessionState {
  csrfToken: string | null
  sessionId: string | null
  expiresAt: string | null
  ready: boolean
  credentialGeneration: number
  endReason: SessionEndReason | null
  setSession: (s: {
    csrfToken: string
    sessionId: string
    expiresAt: string
  }) => void
  clear: (reason?: SessionEndReason) => void
  /** The engine refused the session (an authenticated 401). It answers one code for both
   * cases, so the session's own expiry tells an expired session from an ended one. */
  ended: () => void
}

function forgetLegacySession(): void {
  try {
    localStorage.removeItem('olivares.session')
  } catch {
    // Storage may be disabled; an HttpOnly session still works.
  }
}

export const useSessionStore = create<SessionState>((set, get) => ({
  csrfToken: null,
  sessionId: null,
  expiresAt: null,
  ready: false,
  credentialGeneration: 0,
  endReason: null,
  setSession: (s) => {
    forgetLegacySession()
    set((cur) => ({
      csrfToken: s.csrfToken,
      sessionId: s.sessionId,
      expiresAt: s.expiresAt,
      ready: true,
      endReason: null,
      credentialGeneration:
        cur.csrfToken === s.csrfToken && cur.sessionId === s.sessionId
          ? cur.credentialGeneration
          : cur.credentialGeneration + 1,
    }))
  },
  clear: (reason) => {
    forgetLegacySession()
    set((cur) => ({
      csrfToken: null,
      sessionId: null,
      expiresAt: null,
      ready: true,
      // Only a live session sets the reason. A repeated notice once it is gone (the
      // refresh's 401, then the request's own) keeps it; a sign-out clears it.
      endReason: reason
        ? cur.sessionId !== null
          ? reason
          : cur.endReason
        : null,
      credentialGeneration:
        cur.csrfToken === null && cur.sessionId === null
          ? cur.credentialGeneration
          : cur.credentialGeneration + 1,
    }))
  },
  ended: () => {
    const expiresAt = get().expiresAt
    const expired = !!expiresAt && Date.parse(expiresAt) <= Date.now()
    get().clear(expired ? 'expired' : 'ended')
  },
}))
