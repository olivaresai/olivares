// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// useUrlState contract tests: mount-seeding from the URL, owned-key merging,
// clearing via ''/undefined, replace semantics, and non-owned-param survival.
import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const navigateMock = vi.fn()
// The hook now subscribes to the router's location so an external URL change
// re-seeds it. The mock exposes a setter so a test can move the location the
// way Back/Forward or a foreign navigate() would.
const routerState = vi.hoisted(() => ({
  searchStr: '',
  listeners: new Set<() => void>(),
}))
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigateMock,
  useRouterState: ({ select }: { select: (s: unknown) => unknown }) => {
    const [, force] = reactUseState(0)
    reactUseEffect(() => {
      const fn = () => force((n) => n + 1)
      routerState.listeners.add(fn)
      return () => {
        routerState.listeners.delete(fn)
      }
    }, [])
    return select({ location: { searchStr: routerState.searchStr } })
  },
}))

function setSearchStr(next: string) {
  routerState.searchStr = next
  for (const fn of routerState.listeners) fn()
}

/** Seed the browser and router snapshots before mounting a hook. */
function seedLocation(next: string) {
  window.history.replaceState(null, '', next)
  routerState.searchStr = window.location.search
}

import { useEffect as reactUseEffect, useState as reactUseState } from 'react'
import { useUrlState, useValidatedUrlState } from './use-url-state'

/** Run the search-updater the hook handed to navigate over a current search. */
function applyNavigate(
  call: { search: (cur: Record<string, unknown>) => Record<string, unknown> },
  cur: Record<string, unknown>,
) {
  return call.search(cur)
}

describe('useUrlState', () => {
  beforeEach(() => {
    seedLocation('/audit')
    navigateMock.mockClear()
  })
  afterEach(() => {
    seedLocation('/')
  })

  it('seeds owned keys from the URL on mount, ignoring foreign ones', () => {
    seedLocation('/audit?q=deny&actor=user%3A1&tab=x')
    const { result } = renderHook(() => useUrlState(['q', 'actor']))
    expect(result.current[0]).toEqual({ q: 'deny', actor: 'user:1' })
  })

  it('patch sets a key and reflects it via a replace navigation', () => {
    const { result } = renderHook(() => useUrlState(['q']))
    act(() => result.current[1]({ q: 'deny' }))
    expect(result.current[0]).toEqual({ q: 'deny' })
    expect(navigateMock).toHaveBeenCalledTimes(1)
    const call = navigateMock.mock.calls[0][0]
    expect(call.replace).toBe(true)
    expect(applyNavigate(call, { tab: 'x' })).toEqual({ tab: 'x', q: 'deny' })
  })

  it('empty string and undefined both clear a key (defaults stay out of the URL)', () => {
    seedLocation('/audit?q=deny&actor=user%3A1')
    const { result } = renderHook(() => useUrlState(['q', 'actor']))
    act(() => result.current[1]({ q: '', actor: undefined }))
    expect(result.current[0]).toEqual({})
    const call = navigateMock.mock.calls[0][0]
    // The updater must explicitly hand undefined to the router so it DROPS the
    // params, while preserving params the hook does not own.
    expect(
      applyNavigate(call, { q: 'deny', actor: 'user:1', tab: 'x' }),
    ).toEqual({ q: undefined, actor: undefined, tab: 'x' })
  })

  it('patch only touches the keys present in the patch', () => {
    seedLocation('/audit?q=deny&actor=user%3A1')
    const { result } = renderHook(() => useUrlState(['q', 'actor']))
    act(() => result.current[1]({ q: 'allow' }))
    expect(result.current[0]).toEqual({ q: 'allow', actor: 'user:1' })
    const call = navigateMock.mock.calls[0][0]
    expect(applyNavigate(call, { q: 'deny', actor: 'user:1' })).toEqual({
      q: 'allow',
      actor: 'user:1',
    })
  })

  it('never writes keys it does not own, even if patched', () => {
    const { result } = renderHook(() => useUrlState(['q']))
    act(() => result.current[1]({ q: 'x', tab: 'evil' } as never))
    expect(result.current[0]).toEqual({ q: 'x' })
    const call = navigateMock.mock.calls[0][0]
    expect(applyNavigate(call, {})).toEqual({ q: 'x' })
  })
})

describe('useUrlState external URL changes', () => {
  beforeEach(() => {
    seedLocation('/audit')
    navigateMock.mockClear()
  })

  // Before the hook seeded ONCE from a lazy useState initialiser and never
  // looked at the URL again. Browser Back/Forward, or any navigate() from
  // another component, moved the URL while the view kept the old state — the
  // two desynced in silence, and nothing in this file noticed.
  it('re-seeds when the location changes underneath it', () => {
    seedLocation('/audit?q=deny')
    const { result } = renderHook(() => useUrlState(['q']))
    expect(result.current[0]).toEqual({ q: 'deny' })

    act(() => {
      window.history.replaceState(null, '', '/audit?q=allow')
      setSearchStr('?q=allow')
    })
    expect(result.current[0]).toEqual({ q: 'allow' })
  })

  it('drops a key the new location no longer carries', () => {
    seedLocation('/audit?q=deny&actor=user%3A1')
    const { result } = renderHook(() => useUrlState(['q', 'actor']))
    expect(result.current[0]).toEqual({ q: 'deny', actor: 'user:1' })

    act(() => {
      window.history.replaceState(null, '', '/audit?q=deny')
      setSearchStr('?q=deny')
    })
    expect(result.current[0]).toEqual({ q: 'deny' })
  })

  it('follows the router payload before window.location catches up', () => {
    seedLocation('/audit?q=old')
    const { result } = renderHook(() => useUrlState(['q']))
    expect(result.current[0]).toEqual({ q: 'old' })

    // TanStack notifies subscribers with the next location. The browser global
    // is deliberately left on ?q=old to reproduce the real transition window.
    act(() => setSearchStr('?q=new'))
    expect(
      result.current[0],
      'URL_STATE_ROUTER_CONTRACT: subscribed searchStr must win before window.location catches up',
    ).toEqual({ q: 'new' })
  })

  it('does not navigate from inside a state updater', () => {
    // A side effect in a React reducer runs twice under StrictMode. It is
    // harmless while every write is an idempotent replace and a landmine the
    // moment one is not.
    const { result } = renderHook(() => useUrlState(['q']))
    act(() => result.current[1]({ q: 'x' }))
    expect(navigateMock).toHaveBeenCalledTimes(1)
  })
})

describe('useValidatedUrlState', () => {
  beforeEach(() => {
    seedLocation('/audit')
    navigateMock.mockClear()
  })

  // Condition (b) of the brief: a value read from the URL is untrusted, and
  // when it is rejected the view must fall back to its default AND SAY SO.
  // Neither existing consumer did the second half — both dropped bad values in
  // silence, so a shared deep-link could quietly show different data than the
  // author saw.
  const decode = (raw: Record<string, string | undefined>) => {
    const issues: string[] = []
    let scope: 'tenant' | 'system' = 'tenant'
    if (raw.scope === 'system') scope = 'system'
    else if (raw.scope !== undefined) issues.push('scope')
    return { value: { scope }, issues }
  }

  it('reports the rejected key and falls back to the default', () => {
    seedLocation('/audit?scope=DROP+TABLE')
    const { result } = renderHook(() => useValidatedUrlState(['scope'], decode))
    expect(result.current[0]).toEqual({ scope: 'tenant' })
    expect(result.current[2]).toEqual(['scope'])
  })

  it('reports nothing when every value is accepted', () => {
    seedLocation('/audit?scope=system')
    const { result } = renderHook(() => useValidatedUrlState(['scope'], decode))
    expect(result.current[0]).toEqual({ scope: 'system' })
    expect(result.current[2]).toEqual([])
  })

  it('clears the report once the offending value is patched away', () => {
    seedLocation('/audit?scope=nonsense')
    const { result } = renderHook(() => useValidatedUrlState(['scope'], decode))
    expect(result.current[2]).toEqual(['scope'])
    act(() => result.current[1]({ scope: undefined }))
    expect(result.current[2]).toEqual([])
  })
})

describe('useValidatedUrlState cleans the address bar', () => {
  beforeEach(() => {
    seedLocation('/audit')
    navigateMock.mockClear()
  })

  const decode = (raw: Record<string, string | undefined>) => {
    const issues: string[] = []
    let scope: 'tenant' | 'system' = 'tenant'
    if (raw.scope === 'system') scope = 'system'
    else if (raw.scope !== undefined) issues.push('scope')
    return { value: { scope }, issues }
  }

  it('removes the refused key from the URL, with a replace', () => {
    seedLocation('/audit?scope=nonsense')
    renderHook(() => useValidatedUrlState(['scope'], decode))
    // A value that is not in effect must not stay in a link the operator is
    // about to copy: the recipient would be told again that it failed.
    expect(navigateMock).toHaveBeenCalledTimes(1)
    const call = navigateMock.mock.calls[0][0]
    expect(call.replace).toBe(true)
    expect(call.search({ scope: 'nonsense', tab: 'x' })).toEqual({
      scope: undefined,
      tab: 'x',
    })
  })

  it('keeps reporting after the cleanup, so the notice can be read', () => {
    seedLocation('/audit?scope=nonsense')
    const { result } = renderHook(() => useValidatedUrlState(['scope'], decode))
    // Without the latch the report would vanish with the key it described.
    expect(result.current[2]).toEqual(['scope'])
  })

  it('does not touch the URL when nothing was refused', () => {
    seedLocation('/audit?scope=system')
    renderHook(() => useValidatedUrlState(['scope'], decode))
    expect(navigateMock).not.toHaveBeenCalled()
  })
})

describe('UrlStateNotice re-arms on a new rejection', () => {
  it('speaks again for a fresh rejection naming the same key', async () => {
    const { UrlStateNotice } =
      await import('@/features/shared/url-state-notice')
    const { render, screen } = await import('@testing-library/react')
    const user = (await import('@testing-library/user-event')).default.setup()

    const first = ['scope']
    const { rerender } = render(<UrlStateNotice issues={first} />)
    await user.click(screen.getByTestId('url-state-notice-dismiss'))
    expect(screen.queryByTestId('url-state-notice')).not.toBeInTheDocument()

    // Re-rendering the SAME event must stay dismissed…
    rerender(<UrlStateNotice issues={first} />)
    expect(screen.queryByTestId('url-state-notice')).not.toBeInTheDocument()

    // …but a NEW event naming the same key must speak. The hook marks the event
    // with a new array instance, because it deliberately does NOT pass through
    // an empty state in between: it latches the complaint across its own URL
    // cleanup so the notice survives long enough to be read.
    rerender(<UrlStateNotice issues={['scope']} />)
    expect(screen.getByTestId('url-state-notice')).toBeInTheDocument()
  })
})
