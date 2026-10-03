// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { create } from 'zustand'

/** The browser owns the HttpOnly cookie. JavaScript holds only metadata and a
 * CSRF token, in memory. Nothing in this store can authenticate on its own. */
interface SessionState {
  csrfToken: string | null
  sessionId: string | null
  expiresAt: string | null
  ready: boolean
  credentialGeneration: number
  setSession: (s: {
    csrfToken: string
    sessionId: string
    expiresAt: string
  }) => void
  clear: () => void
}

function forgetLegacySession(): void {
  try {
    localStorage.removeItem('olivares.session')
  } catch {
    // Storage may be disabled; an HttpOnly session still works.
  }
}

export const useSessionStore = create<SessionState>((set) => ({
  csrfToken: null,
  sessionId: null,
  expiresAt: null,
  ready: false,
  credentialGeneration: 0,
  setSession: (s) => {
    forgetLegacySession()
    set((cur) => ({
      csrfToken: s.csrfToken,
      sessionId: s.sessionId,
      expiresAt: s.expiresAt,
      ready: true,
      credentialGeneration:
        cur.csrfToken === s.csrfToken && cur.sessionId === s.sessionId
          ? cur.credentialGeneration
          : cur.credentialGeneration + 1,
    }))
  },
  clear: () => {
    forgetLegacySession()
    set((cur) => ({
      csrfToken: null,
      sessionId: null,
      expiresAt: null,
      ready: true,
      credentialGeneration:
        cur.csrfToken === null && cur.sessionId === null
          ? cur.credentialGeneration
          : cur.credentialGeneration + 1,
    }))
  },
}))
