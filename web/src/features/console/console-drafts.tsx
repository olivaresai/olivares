// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  createContext,
  useContext,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import {
  sameContext,
  useCapabilityPreflight,
  type CapabilityAccess,
  type CapabilityContext,
} from '@/lib/auth/capabilities'
import type { RequestOptions } from '@/lib/api/client'
import {
  AGENT_CREATION,
  GROUP_CREATION,
  GRANT_CREATION,
  useConsoleOperation,
} from './console-authority'

interface Drafts {
  values: Map<string, unknown>
  generations: Map<string, number>
  admitted: boolean
  refused: readonly string[]
  inContext: () => boolean
  current: (key: string) => boolean
}
const DraftContext = createContext<Drafts | null>(null)

function terminalAnswer(access: CapabilityAccess | null) {
  return (
    access !== null &&
    !['allowed', 'reachable', 'checking', 'unknown'].includes(access)
  )
}

/** Only unsent input survives a rights refresh. Protected components still unmount. */
export function ConsoleDraftBoundary({
  admitted,
  access,
  children,
}: {
  admitted: boolean
  access: CapabilityAccess | null
  children: ReactNode
}) {
  const authority = useCapabilityPreflight()
  const agents = useConsoleOperation(AGENT_CREATION, 'agent:write')
  const groups = useConsoleOperation(GROUP_CREATION, 'agent:write')
  const grants = useConsoleOperation(GRANT_CREATION, 'governance:rbac:admin')
  const refused = [
    terminalAnswer(agents.access) ? 'agent:create:' : '',
    terminalAnswer(groups.access) ? 'group:create:' : '',
    terminalAnswer(grants.access) ? 'grant:create:' : '',
  ]
    .filter(Boolean)
    .join(',')
  const terminal = access === null ? !admitted : terminalAnswer(access)
  // Tenant-wide rooms do not depend on workspace selection. Their own identity
  // boundary still retires input on an account, tenant or credential change.
  const context =
    access === null && authority.context
      ? {
          principalKind: authority.context.principalKind,
          actor: authority.context.actor,
          tenant: authority.context.tenant,
          credentialGeneration: authority.context.credentialGeneration,
        }
      : authority.context
  const partition = JSON.stringify([
    context,
    terminal ? (access ?? 'refused') : null,
  ])
  return (
    <DraftOwner
      key={partition}
      admitted={admitted}
      refused={refused}
      context={authority.context}
      live={authority.live}
    >
      {children}
    </DraftOwner>
  )
}

function DraftOwner({
  admitted,
  refused,
  context,
  live,
  children,
}: {
  admitted: boolean
  refused: string
  context: CapabilityContext | null
  live: () => CapabilityContext | null
  children: ReactNode
}) {
  const [values] = useState(() => new Map<string, unknown>())
  const [generations] = useState(() => new Map<string, number>())
  const alive = useRef(false)
  const committed = useRef(false)
  const committedRefusals = useRef<readonly string[]>([])
  useLayoutEffect(() => {
    alive.current = true
    return () => {
      alive.current = false
    }
  }, [])
  useLayoutEffect(() => {
    committed.current = admitted
    return () => {
      committed.current = false
    }
  }, [admitted])
  useLayoutEffect(() => {
    const prefixes = refused ? refused.split(',') : []
    committedRefusals.current = prefixes
    for (const prefix of prefixes) {
      for (const key of values.keys())
        if (key.startsWith(prefix)) values.delete(key)
      generations.set(prefix, (generations.get(prefix) ?? 0) + 1)
    }
  }, [refused, values, generations])
  const inContext = () =>
    alive.current && context !== null && sameContext(context, live())
  const current = (key: string) =>
    committed.current &&
    inContext() &&
    !committedRefusals.current.some((prefix) => key.startsWith(prefix))
  return (
    <DraftContext.Provider
      value={{
        values,
        generations,
        admitted,
        refused: refused ? refused.split(',') : [],
        current,
        inContext,
      }}
    >
      {children}
    </DraftContext.Provider>
  )
}

/** A normal local state outside the route boundary; only creation fields opt in. */
export function useConsoleDraft<T>(key: string | null, initial: T) {
  const drafts = useContext(DraftContext)
  const refused =
    !!key && !!drafts?.refused.some((prefix) => key.startsWith(prefix))
  const [value, setValue] = useState<T>(() =>
    key && !refused && drafts?.admitted && drafts.values.has(key)
      ? (drafts.values.get(key) as T)
      : initial,
  )
  const [wasRefused, setWasRefused] = useState(refused)
  // The route may remain mounted on an admitted sibling. Retire local fields,
  // including the opening flag, as well as the boundary's saved values.
  if (wasRefused !== refused) {
    setWasRefused(refused)
    if (refused) setValue(initial)
  }
  const update = (next: T) => {
    if (key && drafts && !drafts.current(key)) return
    if (key && drafts) {
      if (key.endsWith(':open') && next === true && value === false) {
        const prefix = key.slice(0, -4)
        for (const field of drafts.values.keys())
          if (field.startsWith(prefix)) drafts.values.delete(field)
        drafts.generations.set(
          prefix,
          (drafts.generations.get(prefix) ?? 0) + 1,
        )
      }
      drafts.values.set(key, next)
    }
    setValue(next)
  }
  return [value, update] as const
}

export function useDiscardConsoleDraft() {
  const drafts = useContext(DraftContext)
  const opening = new Map(drafts?.generations)
  return (prefix: string) => {
    // A sent input is no longer an unsent draft, even during a route refresh.
    // An old opening cannot erase a later one in the same account and scope.
    if (
      !drafts?.inContext() ||
      drafts.generations.get(prefix) !== opening.get(prefix)
    )
      return
    for (const key of drafts.values.keys())
      if (key.startsWith(prefix)) drafts.values.delete(key)
  }
}

/** Once bytes are sent, this input must not return as an unsent creation. */
export function useConsoleDraftDispatch(prefix: string) {
  const discard = useDiscardConsoleDraft()
  return (authority: Pick<RequestOptions, 'signal' | 'dispatchGuard'>) => ({
    ...authority,
    dispatchGuard: () => {
      authority.dispatchGuard?.()
      discard(prefix)
    },
  })
}
