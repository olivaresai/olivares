// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useMemo, useRef } from 'react'
import { useAuth } from '@/lib/auth/context'
import { useSessionStore } from '@/stores/session'
import { agentOpsKeys } from './api'

/**
 * THE AUTHORITY BOUNDARY of the provider-profile surfaces: who is acting, in which
 * tenant, under which credential. Anything read or half-done under one boundary is
 * over when the boundary changes — an open sheet, a revealed home, a roster read, a
 * pending confirmation — because none of it was authorized for the next one.
 *
 * The key is deliberately made of three NON-SECRET facts:
 *   · the principal's user id (whoami), not its token;
 *   · the active tenant id (the X-Olivares-Tenant the reads were made under);
 *   · the session store's CREDENTIAL GENERATION — a small counter the session
 *     lifecycle advances whenever the credential it holds effectively changes: a
 *     login, a logout, a 401 clear, and a RENEWAL. That last one matters: the
 *     engine's `POST /v1/auth/refresh` rotates the bearer in place and returns the
 *     SAME session id (core/auth/authenticator.go RefreshSession), so a boundary
 *     that watched the session id slept through every real renewal. Neither the
 *     token nor the session id ever enters a key, a log or a React tree.
 *
 * It is a UI intent contract, not a security boundary: the engine authorizes every
 * request on its own. What this buys is that the console never keeps painting, or
 * acts on, what it read for somebody else — or for an earlier credential.
 */
export interface AuthBoundary {
  /** Opaque, stable while nothing changed; different the moment anything did. */
  key: string
  /**
   * The boundary as ONE opaque number, for query keys: a monotonic epoch per distinct
   * (principal, tenant, credential generation) this page load has seen. It carries no
   * id and no token, and it is what partitions the profile plane's cache
   * (agentOpsKeys.*).
   */
  epoch: number
  principal: string | null
  tenant: string | null
  /** The session store's generation this boundary was built on (see stores/session). */
  credentialGeneration: number
}

const boundaryEpochs = new Map<string, number>()
let nextBoundaryEpoch = 1

/** A monotonic epoch per distinct boundary key, never the key itself. */
function boundaryEpochOf(key: string): number {
  let e = boundaryEpochs.get(key)
  if (e === undefined) {
    e = nextBoundaryEpoch++
    boundaryEpochs.set(key, e)
  }
  return e
}

export function useAuthBoundary(): AuthBoundary {
  const { principal, activeTenant } = useAuth()
  const credentialGeneration = useSessionStore((s) => s.credentialGeneration)
  const queryClient = useQueryClient()
  const principalId = principal?.user_id ?? null
  const boundary = useMemo(() => {
    const key = `${principalId ?? '-'}|${activeTenant ?? '-'}|c${credentialGeneration}`
    return {
      key,
      epoch: boundaryEpochOf(key),
      principal: principalId,
      tenant: activeTenant,
      credentialGeneration,
    }
  }, [principalId, activeTenant, credentialGeneration])

  // WHEN THE BOUNDARY MOVES, THE PREVIOUS ONE'S READS END. The new boundary's keys
  // are different (nothing of the old one can be painted for it), but the old
  // entries are still in the cache and an old request may still be in flight: they
  // are cancelled (the abort reaches the network through the reads' signals) and
  // removed, so a late answer has nowhere to land and no stale row waits for a
  // return to that boundary. The same goes for the MutationCache: a mutation keyed
  // under the old scope (a submitted adoption, with its profile reference and name)
  // is removed too. TanStack never collects a pending mutation on its own, and
  // keeps a settled one for its gcTime after its last observer leaves, so without
  // this its variables would outlive the boundary that submitted them. A request
  // already sent is not recalled: its answer reaches a retired owner, which reports
  // nothing. Only this plane's own entries for that one boundary are touched — no
  // other tenant, feature, query or mutation.
  const previous = useRef<{ tenant: string | null; epoch: number } | null>(null)
  useEffect(() => {
    const prev = previous.current
    if (prev && prev.epoch !== boundary.epoch) {
      const scope = agentOpsKeys.boundaryScope(prev.tenant, prev.epoch)
      void queryClient.cancelQueries({ queryKey: scope })
      queryClient.removeQueries({ queryKey: scope })
      const mutations = queryClient.getMutationCache()
      for (const mutation of mutations.findAll({ mutationKey: scope })) {
        mutations.remove(mutation)
      }
    }
    previous.current = { tenant: boundary.tenant, epoch: boundary.epoch }
  }, [boundary, queryClient])

  return boundary
}

/**
 * AuthBoundaryCustody — the cleanup above, mounted ONCE by the application shell for the
 * whole page load. A provider-room component observes only the boundary moves that happen
 * while it is mounted, and the room is unmounted by its tab or its route guard long before
 * a principal signs out, a credential rotates or a tenant changes elsewhere in the console.
 * This instance observes every move, so what a retired boundary left under its scope — a
 * list, a point read, a submitted adoption's profile reference and its adopt mutation's
 * variables — leaves the query and mutation caches AT the move,
 * whether or not a provider screen is open: a terminal 401 followed by another sign-in, a
 * same-session rotation, a tenant switch and its round trip included. It renders nothing,
 * makes no request and starts no timer.
 */
export function AuthBoundaryCustody(): null {
  useAuthBoundary()
  return null
}

/**
 * AuthorityLostError — thrown by a mutation function that re-checked the CURRENT
 * permission at dispatch and found it gone. A control opened under an earlier
 * permission (a confirmation left open, a queued click, a stale closure) must not
 * produce a request after that permission left; the engine would refuse it anyway,
 * but a refusal is not the same as never asking. Claimed by the caller's `onError`
 * (usePrivilegedMutation consults it below the authorization boundary) and reported
 * as a calm notice, never as a red failure.
 */
export class AuthorityLostError extends Error {
  constructor() {
    super('authority lost before dispatch')
    this.name = 'AuthorityLostError'
  }
}
