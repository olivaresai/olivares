// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The palette remembers the control that opened it so closing can give focus back
// (console-navigation-n1). Pinned here because the browser measurement that motivated it
// cannot run in jsdom; the DOM half is in the slice's browser evidence.
import { afterEach, describe, expect, it } from 'vitest'
import type { CapabilityContext } from '@/lib/auth/capabilities'
import { useCommandStore } from './command'

afterEach(() => {
  useCommandStore.setState({ open: false, opener: null, pendingAction: null })
  document.body.innerHTML = ''
})

describe('command store opener', () => {
  it('remembers the focused control when the palette opens and hands it out once', () => {
    const button = document.createElement('button')
    document.body.appendChild(button)
    button.focus()
    useCommandStore.getState().setOpen(true)
    expect(useCommandStore.getState().opener).toBe(button)
    // Re-asserting open keeps the original opener; the dialog's own input is never it.
    useCommandStore.getState().setOpen(true)
    expect(useCommandStore.getState().opener).toBe(button)
    useCommandStore.getState().setOpen(false)
    expect(useCommandStore.getState().takeOpener()).toBe(button)
    expect(useCommandStore.getState().takeOpener()).toBeNull()
  })

  it('records nothing when the body had focus, and captures on toggle as on setOpen', () => {
    ;(document.activeElement as HTMLElement | null)?.blur?.()
    useCommandStore.getState().toggle()
    expect(useCommandStore.getState().open).toBe(true)
    expect(useCommandStore.getState().opener).toBeNull()
    useCommandStore.getState().toggle()
    const link = document.createElement('a')
    link.href = '#'
    document.body.appendChild(link)
    link.focus()
    useCommandStore.getState().toggle()
    expect(useCommandStore.getState().opener).toBe(link)
  })
})

/* ── the queued verb and the identity that queued it ──────────────────────────── */

/** A context is plain data — principal class, actor, tenant, credential generation,
 *  workspace and the local movement counter. Nothing here is or carries a credential. */
const CONTEXT = (over: Partial<CapabilityContext> = {}): CapabilityContext => ({
  principalKind: 'user',
  actor: 'u-1',
  tenant: 't1',
  credentialGeneration: 3,
  workspace: null,
  lifetime: 7,
  ...over,
})

describe('the pending palette command is bound to the context that queued it', () => {
  it('hands the verb to its own feature exactly once, in the same identity', () => {
    const store = useCommandStore.getState()
    store.setPendingAction('alerting', 'createRoute', CONTEXT())
    expect(useCommandStore.getState().pendingAction).toEqual({
      featureId: 'alerting',
      action: 'createRoute',
      context: CONTEXT(),
    })
    expect(store.consumeAction('alerting', CONTEXT())).toBe('createRoute')
    // ONE SHOT: the second arrival at the same page gets nothing, which is what stops a
    // verb the operator chose once from opening a form on every later visit.
    expect(useCommandStore.getState().pendingAction).toBeNull()
    expect(store.consumeAction('alerting', CONTEXT())).toBeNull()
  })

  it("leaves another feature's command where it is", () => {
    const store = useCommandStore.getState()
    store.setPendingAction('alerting', 'createRoute', CONTEXT())
    // FIRES IF: consumption stops being feature-scoped — eventing would swallow the
    // alerting command, and the operator's chosen verb would vanish silently on a page
    // that was never its target.
    expect(store.consumeAction('eventing', CONTEXT())).toBeNull()
    expect(useCommandStore.getState().pendingAction).not.toBeNull()
    expect(store.consumeAction('alerting', CONTEXT())).toBe('createRoute')
  })

  it.each([
    ['tenant', CONTEXT({ tenant: 't2' })],
    ['principal class', CONTEXT({ principalKind: 'api_token' })],
    ['actor', CONTEXT({ actor: 'u-2' })],
    ['credential generation', CONTEXT({ credentialGeneration: 4 })],
    ['workspace', CONTEXT({ workspace: 'w-1' })],
    // A → B → A: every value equal again, and a movement counter that cannot come back.
    ['round trip', CONTEXT({ lifetime: 9 })],
    ['unestablished identity', null],
  ])('retires the command when the %s moved', (_label, now) => {
    const store = useCommandStore.getState()
    store.setPendingAction('alerting', 'createRoute', CONTEXT())
    expect(store.consumeAction('alerting', now)).toBeNull()
    // AND IT IS GONE. A retired command left in the store would be waiting for the next
    // arrival — the operator who did not choose it.
    expect(useCommandStore.getState().pendingAction).toBeNull()
  })

  it('queues nothing at all without an established identity', () => {
    const store = useCommandStore.getState()
    store.setPendingAction('alerting', 'createRoute', null)
    expect(useCommandStore.getState().pendingAction).toBeNull()
    // FIRES IF: an unbindable command is queued anyway — it could then only be made
    // usable by relaxing the match, i.e. by letting whoever arrives next consume it.
    expect(store.consumeAction('alerting', CONTEXT())).toBeNull()
  })
})
