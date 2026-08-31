// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { create } from 'zustand'
import { persist } from 'zustand/middleware'

/**
 * Session store — holds the opaque bearer session token (olvs_…) returned by
 * POST /v1/auth/login.
 *
 * STORAGE TRADEOFF (conscious): the engine authenticates with a bearer token, not
 * a cookie, so the token MUST be readable by JS to set the Authorization header.
 * We persist it to localStorage so an operator stays signed in across reloads. The
 * mitigations are the engine's strict same-origin CSP (no third-party/inline
 * scripts — see cmd/olivares/webui.go), the same-origin embed, and that the
 * token is short-lived and SERVER-SIDE revocable (logout/expiry). No PII is cached
 * here. Server `401` is the real expiry gate (see configureApiClient onUnauthorized).
 */
interface SessionState {
  token: string | null
  sessionId: string | null
  expiresAt: string | null
  setSession: (s: {
    token: string
    sessionId: string
    expiresAt: string
  }) => void
  clear: () => void
}

export const useSessionStore = create<SessionState>()(
  persist(
    (set) => ({
      token: null,
      sessionId: null,
      expiresAt: null,
      setSession: (s) =>
        set({ token: s.token, sessionId: s.sessionId, expiresAt: s.expiresAt }),
      clear: () => set({ token: null, sessionId: null, expiresAt: null }),
    }),
    { name: 'olivares.session' },
  ),
)
