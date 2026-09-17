// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  useSyncExternalStore,
  type ReactNode,
} from 'react'
import { useRouter, useRouterState } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { useAuth } from '@/lib/auth/context'
import { queryKeys } from '@/lib/api/query'
import { authApi } from '@/lib/api/endpoints'
import type { Whoami } from '@/lib/api/types'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { useViewAccess } from './authorization'
import { createSessionRecents, recentDestination } from './session-recents'
import {
  createPersonalNavigationSession,
  favoriteStorageKey,
  resolvePersonalLink,
  type PersonalLink,
} from './personal-navigation-store'

const PersonalContext = createContext<ReturnType<
  typeof usePersonalSession
> | null>(null)

function identity(principal: Whoami | null): string | Whoami | null {
  return principal?.kind === 'user' &&
    typeof principal.user_id === 'string' &&
    principal.user_id.trim()
    ? JSON.stringify(['user', principal.user_id])
    : principal
}

function usePersonalSession() {
  const client = useQueryClient()
  const { principal, activeTenant, status } = useAuth()
  const generation = useSessionStore((s) => s.credentialGeneration)
  const owner = identity(principal)
  // AuthProvider can still expose cached whoami while a newly installed credential
  // is being resolved. Do not read a persistent partition from that interim identity.
  // Re-use the existing whoami endpoint for a local identity check; never cancel,
  // refetch or replace the shared auth query. Both answers must agree. A failure
  // leaves personal navigation inactive without changing the rest of the shell.
  const [verified, setVerified] = useState<{
    generation: number
    kind: string
    user: string | null
    current: () => boolean
  } | null>(null)
  const [verificationMovement, setVerificationMovement] = useState(0)
  useEffect(() => {
    const controller = new AbortController()
    const matches = () =>
      status === 'authenticated' &&
      generation === useSessionStore.getState().credentialGeneration &&
      activeTenant === useTenantStore.getState().activeTenant &&
      principal === client.getQueryData<Whoami>(queryKeys.whoami)
    const current = () => !controller.signal.aborted && matches()
    const invalidate = () => {
      if (current()) return
      controller.abort()
      // A→B→A can finish before React commits. The retired call must stay retired,
      // and the returned context needs a fresh verification, not the old answer.
      if (matches()) setVerificationMovement((n) => n + 1)
    }
    // Subscribe BEFORE dispatch. These are the same sources that retire personal
    // navigation; effect cleanup alone runs too late after a source movement.
    const tenantOff = useTenantStore.subscribe(invalidate)
    const credentialOff = useSessionStore.subscribe(invalidate)
    const queryOff = client.getQueryCache().subscribe(invalidate)
    invalidate()
    void (async () => {
      if (!current()) return
      try {
        const answer = await authApi.whoami({
          signal: controller.signal,
          sessionEffects: 'none',
          dispatchGuard: () => {
            if (!current()) controller.abort()
            controller.signal.throwIfAborted()
          },
        })
        if (!current()) return
        const user = identity(answer)
        // Bind even a queued successful update to its revocable call lifetime.
        // A transition before React commits this state cannot open the old partition.
        setVerified({
          generation,
          kind: answer.kind,
          user: typeof user === 'string' ? user : null,
          current,
        })
      } catch {
        /* No established current identity: keep preferences in quarantine. */
      }
    })()
    return () => {
      controller.abort()
      tenantOff()
      credentialOff()
      queryOff()
    }
  }, [
    client,
    generation,
    owner,
    principal,
    activeTenant,
    status,
    verificationMovement,
  ])
  const access = useViewAccess()
  // Subscribe to source transitions, not just React commits: a batched A→B→A must
  // revoke the old lifetime too. No token, actor, display name or session ID is read.
  const [movement, setMovement] = useState(0)
  const session = useMemo(
    () =>
      createPersonalNavigationSession({
        key: favoriteStorageKey(
          window.location.origin,
          principal,
          activeTenant,
        ),
        current: () =>
          status === 'authenticated' &&
          verified?.generation === generation &&
          verified.current() &&
          verified.kind === principal?.kind &&
          verified.user === (typeof owner === 'string' ? owner : null) &&
          generation === useSessionStore.getState().credentialGeneration &&
          activeTenant === useTenantStore.getState().activeTenant &&
          owner ===
            identity(client.getQueryData<Whoami>(queryKeys.whoami) ?? null) &&
          principal === client.getQueryData<Whoami>(queryKeys.whoami),
      }),
    // A source can leave and return between renders; movement creates a fresh lifetime.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [
      client,
      owner,
      principal,
      activeTenant,
      status,
      generation,
      verified,
      movement,
    ],
  )
  useEffect(() => {
    const invalidate = () => {
      if (session.live()) return
      // QueryClient notifies before AuthProvider commits its new principal. Wait
      // for that render instead of repeatedly rebuilding against the old snapshot.
      if (session.contextCurrent()) setMovement((n) => n + 1)
    }
    const tenantOff = useTenantStore.subscribe(invalidate)
    const credentialOff = useSessionStore.subscribe(invalidate)
    const queryOff = client.getQueryCache().subscribe(invalidate)
    window.addEventListener('storage', session.storageChanged)
    invalidate()
    return () => {
      tenantOff()
      credentialOff()
      queryOff()
      window.removeEventListener('storage', session.storageChanged)
      session.revoke()
    }
  }, [client, session])
  const router = useRouter()
  const location = useRouterState({ select: (s) => s.location })
  const idle = useRouterState({ select: (s) => s.status === 'idle' })
  const [recentMovement, setRecentMovement] = useState(0)
  const recentSession = useMemo(
    () =>
      createSessionRecents(
        () =>
          status === 'authenticated' &&
          // Presence only, never use the session ID as identity. This observes a logout
          // even when clear → login is batched before AuthProvider can commit anonymous.
          useSessionStore.getState().sessionId !== null &&
          activeTenant === useTenantStore.getState().activeTenant &&
          owner ===
            identity(client.getQueryData<Whoami>(queryKeys.whoami) ?? null),
      ),
    // Same owner on a credential renewal retains memory; a departed lifetime cannot return.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [client, owner, activeTenant, status, recentMovement],
  )
  useEffect(() => {
    const invalidate = () => {
      if (recentSession.live()) return
      if (recentSession.contextCurrent()) setRecentMovement((n) => n + 1)
    }
    const tenantOff = useTenantStore.subscribe(invalidate)
    const credentialOff = useSessionStore.subscribe(invalidate)
    const queryOff = client.getQueryCache().subscribe(invalidate)
    const routeOff = router.subscribe('onResolved', ({ toLocation }) => {
      recentSession.arrive(
        recentDestination(toLocation.pathname, toLocation.searchStr),
      )
    })
    invalidate()
    return () => {
      tenantOff()
      credentialOff()
      queryOff()
      routeOff()
      recentSession.revoke()
    }
  }, [client, router, recentSession])
  const recents = useSyncExternalStore(
    recentSession.subscribe,
    recentSession.getSnapshot,
    recentSession.getSnapshot,
  )
  // Only a mounted permitted marker calls this. The current router is an exclusion
  // check, never a source of admission. A callback also belongs to this auth lease
  // and location: pending navigation and late effects from retired owners cannot add.
  const recordVisit = useCallback(
    (id: string) => {
      if (
        !session.isActive() ||
        !recentSession.live() ||
        !idle ||
        router.state.status !== 'idle' ||
        router.state.location !== location
      )
        return
      // The marker carries the guard's admission, including its no-workspace
      // placeholder. Navigation visibility only removes established denials; it
      // must not invent a second admission rule from an unknown surface question.
      const target = resolvePersonalLink({ kind: 'feature', id })
      if (
        id !== 'settings' &&
        (!target || !('navigation' in target) || !access.navigable(target))
      )
        return
      if (recentDestination(location.pathname, location.searchStr) !== id)
        return
      const leaf = router.state.matches.at(-1)
      if (
        !leaf ||
        leaf.status !== 'success' ||
        leaf.pathname !== location.pathname ||
        ('globalNotFound' in leaf && leaf.globalNotFound)
      )
        return
      recentSession.arrive(id)
      recentSession.admit(id)
    },
    [session, recentSession, idle, router, location, access],
  )
  const snapshot = useSyncExternalStore(
    session.subscribe,
    session.getSnapshot,
    session.getSnapshot,
  )
  const visible = (link: PersonalLink) => {
    const target = resolvePersonalLink(link)
    return (
      session.isActive() &&
      !!target &&
      (!('navigation' in target) || access.navigable(target))
    )
  }
  return {
    favorites: snapshot.favorites.filter(visible),
    recents: recentSession.isActive() ? recents.filter(visible) : [],
    recordVisit,
    removeRecent: (id: string) => {
      if (session.live()) recentSession.remove(id)
    },
    clearRecents: () => {
      if (session.live()) recentSession.clear()
    },
    temporary: snapshot.temporary,
    available: status === 'authenticated' && session.isActive(),
    visible,
    setFavorite: (link: PersonalLink, saved: boolean) => {
      if (visible(link)) session.setFavorite(link, saved)
    },
    clearFavorites: session.clearFavorites,
  }
}

export function PersonalNavigationProvider({
  children,
}: {
  children: ReactNode
}) {
  const value = usePersonalSession()
  return (
    <PersonalContext.Provider value={value}>
      {children}
    </PersonalContext.Provider>
  )
}

// Optional for isolated shell consumers; the authenticated AppLayout always provides it.
// eslint-disable-next-line react-refresh/only-export-components
export function usePersonalNavigation() {
  return useContext(PersonalContext)
}
