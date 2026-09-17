// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { create } from 'zustand'
import { useCallback, useLayoutEffect, useRef } from 'react'
import { useQueryClient, type QueryClient } from '@tanstack/react-query'
import { queryKeys } from '@/lib/api/query'
import type { Whoami } from '@/lib/api/types'
import { useSessionStore } from './session'
import { useTenantStore } from './tenant'

/** Non-secret identity only. AAL changes are expected during this ceremony. */
export function stepUpPrincipal(
  principal: Whoami | null | undefined,
): string | null {
  return principal?.user_id
    ? JSON.stringify([
        principal.kind,
        principal.user_id,
        principal.actor,
        principal.superadmin,
      ])
    : null
}

export interface StepUpAttempt {
  readonly signal: AbortSignal
  readonly sessionEffects: 'none'
  /** Also passed to HTTP: the check runs at actual dispatch, not just at the caller. */
  dispatchGuard: () => void
  current: () => boolean
  retire: () => void
}

export interface StepUpOwner {
  readonly principal: string | null
  readonly tenant: string | null
  readonly credentialGeneration: number
  current: () => boolean
  retire: () => void
  onRetire: (listener: () => void) => () => void
  begin: (expectedRequest?: () => boolean) => StepUpAttempt
}

/** One owner episode, never revived by A→B→A. Subscriptions catch movements even
 * before React commits a render. No bearer, session ID, or credential is copied.
 * Registration/elevation already sent can persist; retirement stops continuation.
 */
export function createStepUpOwner(queryClient: QueryClient): StepUpOwner {
  const principal = stepUpPrincipal(
    queryClient.getQueryData<Whoami>(queryKeys.whoami),
  )
  const tenant = useTenantStore.getState().activeTenant
  const credentialGeneration = useSessionStore.getState().credentialGeneration
  let retired = false
  let revision = 0
  let attempt: StepUpAttempt | undefined
  const listeners = new Set<() => void>()
  const unsubscribe: Array<() => void> = []
  const retire = () => {
    if (retired) return
    retired = true
    attempt?.retire()
    unsubscribe.forEach((fn) => fn())
    listeners.forEach((fn) => fn())
    listeners.clear()
  }
  const current = () => {
    if (
      !retired &&
      (tenant !== useTenantStore.getState().activeTenant ||
        credentialGeneration !==
          useSessionStore.getState().credentialGeneration ||
        principal !==
          stepUpPrincipal(queryClient.getQueryData<Whoami>(queryKeys.whoami)))
    )
      retire()
    return !retired
  }
  unsubscribe.push(
    useTenantStore.subscribe(current),
    useSessionStore.subscribe(current),
    queryClient.getQueryCache().subscribe(current),
  )
  const owner: StepUpOwner = {
    principal,
    tenant,
    credentialGeneration,
    current,
    retire,
    onRetire: (listener) => {
      if (retired) listener()
      else listeners.add(listener)
      return () => {
        listeners.delete(listener)
      }
    },
    begin: (expectedRequest = () => true) => {
      attempt?.retire()
      const version = ++revision
      const controller = new AbortController()
      const isCurrent = () => {
        if (!current() || version !== revision || !expectedRequest())
          controller.abort()
        return !controller.signal.aborted
      }
      attempt = {
        signal: controller.signal,
        sessionEffects: 'none',
        current: isCurrent,
        dispatchGuard: () => {
          isCurrent()
          controller.signal.throwIfAborted()
        },
        retire: () => controller.abort(),
      }
      return attempt
    },
  }
  // Consumption removes the request before a resumed mutation may leave its
  // queue. A later demand must still retire that old owner permanently, even
  // if the replacement is itself cleared before the old transport resumes.
  unsubscribe.push(
    useStepUpStore.subscribe((state, previous) => {
      if (
        state.request &&
        state.request.instance !== previous.request?.instance &&
        state.request.owner !== owner
      )
        retire()
    }),
  )
  return owner
}

/** Capture at the start of an action. A new action and unmount retire its predecessor.
 * The hook owns cleanup; callers only keep the small owner/attempt interface.
 */
export function useStepUpOwner() {
  const queryClient = useQueryClient()
  const owner = useRef<StepUpOwner | null>(null)
  const mounted = useRef(false)
  useLayoutEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
      owner.current?.retire()
    }
  }, [])
  return useCallback(() => {
    owner.current?.retire()
    const next = createStepUpOwner(queryClient)
    if (!mounted.current) next.retire()
    owner.current = next
    return next
  }, [queryClient])
}

export interface StepUpDemand {
  action: string
  retry?: () => void
  owner: StepUpOwner
  /** Explicit operation opt-in, never inferred from shared action copy. */
  enrollment?: 'connector-add'
}
export interface StepUpRequest extends StepUpDemand {
  readonly instance: number
}
interface StepUpState {
  contextRevision: number
  request: StepUpRequest | null
  require: (request: StepUpDemand) => boolean
  current: (instance: number) => boolean
  dropRetry: (instance: number) => void
  consume: (instance: number) => void
  clear: (instance?: number) => void
}

let instances = 0
export const useStepUpStore = create<StepUpState>((set, get) => {
  let resume: (() => void) | undefined
  let detach: (() => void) | undefined
  return {
    contextRevision: 0,
    request: null,
    require: (demand) => {
      if (get().request || !demand.owner.current()) return false
      const instance = ++instances
      resume = demand.retry
      // A captured public retry is a consume request, never the mutation itself.
      const retry = resume ? () => get().consume(instance) : undefined
      set({ request: { ...demand, instance, retry } })
      detach = demand.owner.onRetire(() => get().clear(instance))
      return true
    },
    current: (instance) => {
      const request = get().request
      return request?.instance === instance && request.owner.current()
    },
    dropRetry: (instance) => {
      if (!get().current(instance)) return
      resume = undefined
      set({ request: { ...get().request!, retry: undefined } })
    },
    consume: (instance) => {
      if (!get().current(instance)) return
      const retry = resume
      get().clear(instance)
      retry?.()
    },
    clear: (instance) => {
      if (instance !== undefined && get().request?.instance !== instance) return
      resume = undefined
      detach?.()
      detach = undefined
      set({ request: null })
    },
  }
})

/** The AuthProvider hookup makes even a batched A→B→A observable to dialog owners.
 * Owners still guard live stores at dispatch; this revision also retires rendered intent.
 */
export function observeStepUpContext(queryClient: QueryClient): () => void {
  const snapshot = () =>
    JSON.stringify([
      stepUpPrincipal(queryClient.getQueryData<Whoami>(queryKeys.whoami)),
      useTenantStore.getState().activeTenant,
      useSessionStore.getState().credentialGeneration,
    ])
  let previous = snapshot()
  const changed = () => {
    const next = snapshot()
    if (next === previous) return
    previous = next
    useStepUpStore.getState().clear()
    useStepUpStore.setState((s) => ({ contextRevision: s.contextRevision + 1 }))
  }
  const unsubscribe = [
    useTenantStore.subscribe(changed),
    useSessionStore.subscribe(changed),
    queryClient.getQueryCache().subscribe(changed),
  ]
  return () => unsubscribe.forEach((fn) => fn())
}
