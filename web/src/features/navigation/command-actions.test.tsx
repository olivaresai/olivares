// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE CONSUMPTION SEAM: a verb queued by the palette opens a form only if the principal
// still holds ITS OWN mutation permission, in the identity that queued it.
//
// The three writes and the permissions the ENGINE requires for them are asserted here
// against the registry itself, so the pairing cannot drift into prose. `orchestration` is
// the one that proves a verb tier could never have been derived: its view reads
// `orchestration:graph:read` and its verb writes `orchestration:schedule:write`.
//
// Nothing is mocked away that decides anything: the store is the real store, the context
// is the real `liveCapabilityContext` over a real QueryClient and the real tenant,
// session and workspace stores. Only `useAuth` is a stand-in, and its `can` is an honest
// predicate over the permission string — never `() => true`.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const authState = vi.hoisted(() => ({
  held: new Set<string>(),
  activeTenant: 't1' as string | null,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: (p: string) => authState.held.has(p),
    activeTenant: authState.activeTenant,
  }),
}))

import { queryKeys } from '@/lib/api/query'
import { liveCapabilityContext } from '@/lib/auth/capabilities'
import { useCommandStore } from '@/stores/command'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { useWorkspaceStore } from '@/stores/workspace'
import { FEATURE_VIEWS } from '@/features/registry'
import deNav from '@/lib/i18n/locales/de/nav.json'
import enNav from '@/lib/i18n/locales/en/nav.json'
import esNav from '@/lib/i18n/locales/es/nav.json'
import frNav from '@/lib/i18n/locales/fr/nav.json'
import jaNav from '@/lib/i18n/locales/ja/nav.json'
import ruNav from '@/lib/i18n/locales/ru/nav.json'
import zhNav from '@/lib/i18n/locales/zh/nav.json'
import { commandActionOf, usePendingCommandAction } from './command-actions'

/** The seven catalogs the console ships. */
const NAV_CATALOGS = {
  en: enNav,
  es: esNav,
  de: deNav,
  fr: frNav,
  ja: jaNav,
  ru: ruNav,
  zh: zhNav,
} as Record<string, { commandActions?: Record<string, Record<string, string>> }>

const PRINCIPAL = {
  kind: 'user',
  user_id: 'u-1',
  actor: 'u-1',
  display_name: 'Ada',
  superadmin: false,
  grants: [],
}

const clients: QueryClient[] = []
function client(): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.whoami, PRINCIPAL)
  clients.push(qc)
  return qc
}

function Host({
  featureId,
  actionId,
  onOpen,
}: {
  featureId: string
  actionId: string
  onOpen: () => void
}) {
  usePendingCommandAction(featureId, actionId, onOpen)
  return null
}

/**
 * Arrive at the target view with whatever the store currently holds, and stay mounted.
 *
 * ⛔ THE OBSERVER OUTLIVES THE CALL, and a cell that queues a SECOND command must retire
 *    it first. The hook watches the store, so an observer left mounted from an earlier
 *    arrival consumes the next command the instant it is queued — which would quietly
 *    turn a movement cell into a measurement of the first observer.
 */
function arrive(qc: QueryClient, featureId: string, actionId: string) {
  const opened = vi.fn()
  const view = render(
    <QueryClientProvider client={qc}>
      <Host featureId={featureId} actionId={actionId} onOpen={opened} />
    </QueryClientProvider>,
  )
  return Object.assign(opened, { leave: () => view.unmount() })
}

/** Queue a verb the way the palette does: bound to the identity that is live now. */
function selectInPalette(qc: QueryClient, featureId: string, actionId: string) {
  useCommandStore
    .getState()
    .setPendingAction(featureId, actionId, liveCapabilityContext(qc))
  expect(useCommandStore.getState().pendingAction).not.toBeNull()
}

beforeEach(() => {
  authState.held = new Set([
    'notify:route:read',
    'notify:route:write',
    'eventing:subscription:read',
    'eventing:subscription:write',
    'orchestration:graph:read',
    'orchestration:schedule:write',
  ])
  authState.activeTenant = 't1'
  useTenantStore.setState({ activeTenant: 't1' })
  useSessionStore.setState({ credentialGeneration: 0 })
  useWorkspaceStore.setState({ activeWorkspace: null })
  useCommandStore.setState({ pendingAction: null, open: false, opener: null })
})

afterEach(() => {
  for (const qc of clients.splice(0)) qc.clear()
})

describe('the registry declares each verb with its own mutation permission', () => {
  it.each([
    ['eventing', 'createSubscription', 'eventing:subscription:write'],
    ['alerting', 'createRoute', 'notify:route:write'],
    ['orchestration', 'createSchedule', 'orchestration:schedule:write'],
  ])('%s/%s writes with %s', (featureId, actionId, permission) => {
    expect(commandActionOf(featureId, actionId)).toEqual({
      id: actionId,
      permission,
    })
    const view = FEATURE_VIEWS.find((v) => v.id === featureId)
    // AND IT IS NOT THE PAGE'S. The whole defect was the palette gating a write on the
    // view's read permission; if these ever coincide the guard has stopped guarding.
    expect(view?.permission).toBeDefined()
    expect(view?.permission).not.toBe(permission)
  })

  // ⛔ THE KEY DID NOT MOVE, IN ANY OF THE SEVEN. The descriptor replaced a bare string,
  //    and `id` is still the segment the label hangs off — so a translation that used to
  //    resolve must still resolve. Derived from the registry rather than from a list
  //    written here: a verb added without its seven translations fails this cell.
  it.each(Object.keys(NAV_CATALOGS))(
    'keeps a label for every declared verb in %s',
    (language) => {
      const catalog = NAV_CATALOGS[language]
      const declared = FEATURE_VIEWS.flatMap((v) =>
        (v.commandActions ?? []).map((a) => [v.id, a.id] as const),
      )
      expect(declared.length).toBeGreaterThan(0)
      for (const [viewId, actionId] of declared) {
        const label = catalog.commandActions?.[viewId]?.[actionId]
        expect(
          label,
          `nav:commandActions.${viewId}.${actionId} missing in ${language}`,
        ).toBeTruthy()
      }
    },
  )

  it('refuses a verb the registry does not declare', () => {
    expect(commandActionOf('alerting', 'deleteEverything')).toBeNull()
    expect(commandActionOf('home', 'createRoute')).toBeNull()
  })
})

describe('a queued verb is consumed once, by its own feature, in its own identity', () => {
  it('opens the form for the principal who holds the write permission', () => {
    const qc = client()
    selectInPalette(qc, 'alerting', 'createRoute')
    const opened = arrive(qc, 'alerting', 'createRoute')
    expect(opened).toHaveBeenCalledTimes(1)
    expect(useCommandStore.getState().pendingAction).toBeNull()
    opened.leave()
    // A second arrival opens nothing: the command was spent, not remembered.
    expect(arrive(qc, 'alerting', 'createRoute')).not.toHaveBeenCalled()
  })

  it('refuses — and still clears — after the grant is revoked in flight', () => {
    const qc = client()
    selectInPalette(qc, 'alerting', 'createRoute')
    authState.held.delete('notify:route:write')
    const refused = arrive(qc, 'alerting', 'createRoute')
    expect(refused).not.toHaveBeenCalled()
    // FIRES IF: the command survives its own refusal — restoring the grant and walking
    // back in would then open a form nobody asked for on that visit.
    expect(useCommandStore.getState().pendingAction).toBeNull()
    refused.leave()
    authState.held.add('notify:route:write')
    expect(arrive(qc, 'alerting', 'createRoute')).not.toHaveBeenCalled()
  })

  it('refuses after the tenant moves, and after a round trip back', () => {
    const qc = client()
    selectInPalette(qc, 'alerting', 'createRoute')
    useTenantStore.setState({ activeTenant: 't2' })
    authState.activeTenant = 't2'
    const first = arrive(qc, 'alerting', 'createRoute')
    expect(first).not.toHaveBeenCalled()
    // The operator leaves the page before choosing again; without this the observer above
    // would consume the next command before the round trip happens.
    first.leave()

    // A → B → A: every VALUE matches the queued context again. Only the local movement
    // counter says the authority went somewhere and came back.
    selectInPalette(qc, 'alerting', 'createRoute')
    useTenantStore.setState({ activeTenant: 't1' })
    useTenantStore.setState({ activeTenant: 't2' })
    authState.activeTenant = 't2'
    expect(arrive(qc, 'alerting', 'createRoute')).not.toHaveBeenCalled()
    expect(useCommandStore.getState().pendingAction).toBeNull()
  })

  it('refuses after a credential refresh that kept the same display identity', () => {
    const qc = client()
    selectInPalette(qc, 'alerting', 'createRoute')
    useSessionStore.setState({ credentialGeneration: 1 })
    expect(arrive(qc, 'alerting', 'createRoute')).not.toHaveBeenCalled()
  })

  it('refuses after a workspace movement', () => {
    const qc = client()
    selectInPalette(qc, 'alerting', 'createRoute')
    useWorkspaceStore.setState({ activeWorkspace: 'w-9' })
    expect(arrive(qc, 'alerting', 'createRoute')).not.toHaveBeenCalled()
  })

  it('refuses when the arriving page can establish no identity at all', () => {
    const qc = client()
    selectInPalette(qc, 'alerting', 'createRoute')
    // The principal is gone from the cache: nothing to match, and "I could not tell" is
    // not an admission.
    qc.removeQueries({ queryKey: queryKeys.whoami })
    expect(arrive(qc, 'alerting', 'createRoute')).not.toHaveBeenCalled()
    expect(useCommandStore.getState().pendingAction).toBeNull()
  })

  it('refuses with no tenant selected, because the write has no target', () => {
    const qc = client()
    selectInPalette(qc, 'alerting', 'createRoute')
    authState.activeTenant = null
    expect(arrive(qc, 'alerting', 'createRoute')).not.toHaveBeenCalled()
  })

  it('does not let one feature consume — or discard — another feature’s command', () => {
    const qc = client()
    selectInPalette(qc, 'alerting', 'createRoute')
    const other = arrive(qc, 'eventing', 'createSubscription')
    expect(other).not.toHaveBeenCalled()
    expect(useCommandStore.getState().pendingAction).not.toBeNull()
    other.leave()
    expect(arrive(qc, 'alerting', 'createRoute')).toHaveBeenCalledTimes(1)
  })

  it('does not open a form for a verb the registry never declared', () => {
    const qc = client()
    useCommandStore
      .getState()
      .setPendingAction('alerting', 'forcedVerb', liveCapabilityContext(qc))
    expect(arrive(qc, 'alerting', 'forcedVerb')).not.toHaveBeenCalled()
    expect(useCommandStore.getState().pendingAction).toBeNull()
  })

  // ⛔ THE CORRECTION-1 PROPERTY, at the seam: the observer is ALREADY MOUNTED when the
  //    verb is chosen. A mount-only consumer saw nothing here, because selecting a verb
  //    for the page you are on remounts nothing.
  it('acts on a command queued while it is already mounted, once', () => {
    const qc = client()
    const opened = arrive(qc, 'alerting', 'createRoute')
    expect(opened).not.toHaveBeenCalled()

    act(() => selectInPalette(qc, 'alerting', 'createRoute'))
    expect(opened).toHaveBeenCalledTimes(1)
    expect(useCommandStore.getState().pendingAction).toBeNull()

    // And only once: nothing re-opens while it stays mounted.
    act(() => {
      useCommandStore.setState({ pendingAction: null })
    })
    expect(opened).toHaveBeenCalledTimes(1)
  })

  it('refuses a command queued while it is mounted without the permission', () => {
    const qc = client()
    authState.held.delete('notify:route:write')
    const opened = arrive(qc, 'alerting', 'createRoute')

    act(() => selectInPalette(qc, 'alerting', 'createRoute'))

    expect(opened).not.toHaveBeenCalled()
    // Retired where it was refused, not left for the next arrival.
    expect(useCommandStore.getState().pendingAction).toBeNull()
  })

  it('opens nothing when nothing was queued', () => {
    const qc = client()
    expect(arrive(qc, 'alerting', 'createRoute')).not.toHaveBeenCalled()
  })
})
