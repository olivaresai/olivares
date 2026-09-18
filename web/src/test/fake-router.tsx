// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A LOCATION WITH A HISTORY, FOR TESTS THAT MOCK THE ROUTER.
//
// Every console test that mounts a view without a RouterProvider replaces
// `@tanstack/react-router` with a two-line stub: `useRouterState: () => ''` and an
// anchor for `Link`. That was enough while views kept their state in `useState`.
//
// It stops being enough the moment a view's state IS the URL. A stub that always
// answers the empty string cannot represent "the operator opened this session", so a
// test over such a view either asserts nothing or — worse — passes because the view
// fell back to its default and the default happened to be what the assertion wanted.
//
// This module is the smallest thing that is not that: one in-memory location, a real
// entry stack, and `navigate()` semantics that match the ones `useUrlState` relies on
// (merge over the current search, `replace` versus push, keep the hash). It is a
// DOUBLE, not a router: no route matching, no loaders, no params. A test that needs
// those needs the real router.
import type { ReactNode } from 'react'
import { useSyncExternalStore } from 'react'

interface Entry {
  pathname: string
  searchStr: string
  hash: string
}

function parse(url: string): Entry {
  const hashCut = url.indexOf('#')
  const hash = hashCut === -1 ? '' : url.slice(hashCut)
  const withoutHash = hashCut === -1 ? url : url.slice(0, hashCut)
  const queryCut = withoutHash.indexOf('?')
  return {
    pathname: queryCut === -1 ? withoutHash : withoutHash.slice(0, queryCut),
    searchStr: queryCut === -1 ? '' : withoutHash.slice(queryCut),
    hash,
  }
}

function format(entry: Entry): string {
  return `${entry.pathname}${entry.searchStr}${entry.hash}`
}

const listeners = new Set<() => void>()
let entries: Entry[] = [parse('/')]
let index = 0

function notify() {
  for (const fn of listeners) fn()
}

/** The search of the current entry, as an object of string values. */
function currentSearch(): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const [k, v] of new URLSearchParams(entries[index].searchStr)) out[k] = v
  return out
}

function writeSearch(next: Record<string, unknown>, replace: boolean) {
  const params = new URLSearchParams()
  for (const [k, v] of Object.entries(next)) {
    // TanStack drops undefined; the empty string is how `useUrlState` clears a key.
    if (v === undefined || v === '') continue
    params.set(k, String(v))
  }
  const search = params.toString()
  const entry: Entry = {
    pathname: entries[index].pathname,
    searchStr: search ? `?${search}` : '',
    hash: entries[index].hash,
  }
  if (format(entry) === format(entries[index])) return
  if (replace) entries[index] = entry
  else {
    // A push truncates whatever was ahead, exactly like a browser.
    entries = [...entries.slice(0, index + 1), entry]
    index = entries.length - 1
  }
  notify()
}

/**
 * The test's handle on the location. `reset` belongs in a `beforeEach`: the module
 * state outlives one test, and a leaked address is the kind of cross-test coupling
 * that turns one failure into a file of them.
 */
export const fakeRouter = {
  reset(url = '/') {
    entries = [parse(url)]
    index = 0
    notify()
  },
  /** Navigate as an external actor would — a pasted link, another component. */
  go(url: string) {
    entries = [...entries.slice(0, index + 1), parse(url)]
    index = entries.length - 1
    notify()
  },
  back() {
    if (index > 0) {
      index -= 1
      notify()
    }
  },
  forward() {
    if (index < entries.length - 1) {
      index += 1
      notify()
    }
  },
  /** The whole current address, the way an operator would copy it. */
  url() {
    return format(entries[index])
  },
  searchStr() {
    return entries[index].searchStr
  },
  /** How many entries deep the stack is — the only way to see push vs replace. */
  depth() {
    return entries.length
  },
}

interface NavigateArgs {
  to?: string
  search?:
    | Record<string, unknown>
    | ((cur: Record<string, unknown>) => Record<string, unknown>)
  replace?: boolean
  hash?: boolean | string
}

function navigate(args: NavigateArgs) {
  if (args.to !== undefined && args.search === undefined) {
    if (args.replace) {
      entries[index] = parse(args.to)
      notify()
    } else fakeRouter.go(args.to)
    return
  }
  const next =
    typeof args.search === 'function'
      ? args.search(currentSearch())
      : (args.search ?? currentSearch())
  writeSearch(next, args.replace === true)
}

function subscribe(fn: () => void) {
  listeners.add(fn)
  return () => {
    listeners.delete(fn)
  }
}

function snapshot() {
  return entries[index]
}

/**
 * The module double. Use it from a `vi.mock` factory:
 *
 *   vi.mock('@tanstack/react-router', async () =>
 *     (await import('@/test/fake-router')).fakeRouterModule())
 *
 * `useRouter` answers undefined, which is what the older stubs answered and what the
 * shared tab strip expects without a RouterProvider.
 */
export function fakeRouterModule() {
  return {
    useNavigate: () => navigate,
    useRouter: () => undefined,
    useRouterState: ({ select }: { select?: (s: unknown) => unknown } = {}) => {
      const entry = useSyncExternalStore(subscribe, snapshot, snapshot)
      const state = { location: { ...entry, href: format(entry) } }
      return select ? select(state) : state
    },
    Link: ({
      children,
      to,
      ...rest
    }: { children?: ReactNode; to?: string } & Record<string, unknown>) => (
      <a href={to} {...rest}>
        {children}
      </a>
    ),
  }
}
