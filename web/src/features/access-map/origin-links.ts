// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { isSessionAddress } from '@/features/sessions/session-address'
import { type AuthorityTarget, NHI_ROSTER_TARGET } from './authority'

/** A console page that shows the thing an origin node or edge names. */
export type OriginLink = Omit<AuthorityTarget, 'key'> & {
  key: AuthorityTarget['key'] | 'session' | 'agent'
}

/**
 * Where an origin leads, by the pages the console really has: a session opens on the
 * work surface at its address; an agent and an identity have no page of their own, so
 * they open the list that holds them (the Agents tab, the identity roster). A resource
 * has no page that addresses one, so it leads nowhere rather than to a guess.
 *
 * `ref` is the engine's own reference, exact. The caller must not pass a label it made
 * up for display: a session link built from a shortened id would open another session.
 */
export function originLinks(
  kind: string,
  ref: string | undefined,
): OriginLink[] {
  switch (kind) {
    case 'session': {
      const address = `sess:${ref ?? ''}`
      if (!ref || !isSessionAddress(address)) return []
      return [
        {
          key: 'session',
          to: '/sessions',
          search: { session: address },
          permission: 'sessions:live:read',
        },
      ]
    }
    case 'agent':
      return [
        {
          key: 'agent',
          to: '/console',
          search: { tab: 'agents' },
          permission: 'tenant:admin',
        },
      ]
    case 'identity':
      return [NHI_ROSTER_TARGET]
    default:
      return []
  }
}
