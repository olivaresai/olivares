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
  /**
   * CREDENTIAL GENERATION — a small, non-secret counter that advances every time the
   * credential this store holds EFFECTIVELY changes: a login, a renewal, a logout, a
   * 401 clear. It exists so a consumer can learn "the credential moved" without
   * reading the token (a secret) or the session id — which a renewal does NOT change:
   * `POST /v1/auth/refresh` rotates the bearer credential of the calling session in
   * place and answers with the SAME `session_id` (core/auth/authenticator.go,
   * RefreshSession; core/api/handlers_auth.go). A boundary that watched the session
   * id therefore slept through every real renewal.
   *
   * The comparison lives here, in the one place that already owns the current
   * credential: `setSession` and `clear` compare the incoming credential with the
   * held one and advance the counter when they differ. No copy, hash, map or history
   * of a token exists anywhere else, and only this number leaves the store. It is
   * not persisted: it counts THIS page load's transitions, starting at 0 with
   * whatever credential was rehydrated.
   */
  credentialGeneration: number
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
      credentialGeneration: 0,
      setSession: (s) =>
        set((cur) => ({
          token: s.token,
          sessionId: s.sessionId,
          expiresAt: s.expiresAt,
          // Same bearer and same session: nothing about WHO is calling changed (an
          // expiry alone is not a credential). Anything else is a new credential —
          // a rotated bearer under the same session id included.
          credentialGeneration:
            cur.token === s.token && cur.sessionId === s.sessionId
              ? cur.credentialGeneration
              : cur.credentialGeneration + 1,
        })),
      clear: () =>
        set((cur) => ({
          token: null,
          sessionId: null,
          expiresAt: null,
          // Ending a credential is a change of credential; clearing nothing is not.
          credentialGeneration:
            cur.token === null && cur.sessionId === null
              ? cur.credentialGeneration
              : cur.credentialGeneration + 1,
        })),
    }),
    {
      name: 'olivares.session',
      // What reaches localStorage is exactly what did before: the credential and its
      // expiry. The generation is a per-load fact and would be wrong on the next one.
      partialize: (s) => ({
        token: s.token,
        sessionId: s.sessionId,
        expiresAt: s.expiresAt,
      }),
    },
  ),
)
