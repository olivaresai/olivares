// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  createContext,
  useContext,
  useState,
  type ReactNode,
  type Ref,
} from 'react'
import { createPortal } from 'react-dom'

/**
 * PagePrimaryAction / PageSecondaryActions — the controls of a TABBED screen, declared
 * inside the tab that owns them and rendered in the page header's own slots.
 *
 * ⛔ WHY A SLOT AND NOT A PROP. `PageHeader.primaryAction` is the right shape for a page
 *    whose verb belongs to the whole page (`/workspace-templates` creates a template
 *    whatever you are looking at). It is the WRONG shape for the tabbed screens this
 *    console has, and the brief for this pass says why in one line: *on a tabbed screen
 *    the verb belongs to the active tab*. `/catalog` under "Entries" offers *New entry*;
 *    under "Policy" that verb does not exist. A page-level prop would either show a verb
 *    that is wrong for what is on screen, or force every view to mirror its tab state
 *    into its header — and on eleven of these screens the verb lives inside a CHILD
 *    component that owns the dialog it opens, so mirroring means lifting eleven pieces of
 *    state across component boundaries. That is shotgun surgery: eleven edits, eleven
 *    chances to change behaviour, for one layout decision.
 *
 *    Declaring the verb inside the tab keeps its SCOPE visible in the source — the button
 *    is written where it applies — while the header keeps ONE place on screen where the
 *    verb ever appears. Radix unmounts an inactive `TabsContent` (no `forceMount` in this
 *    tree, checked), so exactly one tab's declaration is mounted at a time.
 *
 * ⛔ AND WHY THE HOSTS ARE DETACHED ELEMENTS CREATED ONCE, not nodes captured by a ref.
 *    A ref callback cannot give a portal a target on the FIRST render: it runs during
 *    commit, so the earliest a `useState(node)` host can be read is the second commit,
 *    and the button would paint somewhere else first and jump. The provider creates its
 *    own two `<span>`s at mount, hands them down unchanged, and each slot merely ADOPTS
 *    one into the header's control row. The portals therefore have stable targets from
 *    the first render and the controls paint once, in the right place.
 *
 * ⛔ AND WHY THE FALLBACK RENDERS IN PLACE INSTEAD OF NOTHING. Without a provider —
 *    a view rendered bare in a unit test, a screen outside the authenticated shell — the
 *    host is null. Rendering nothing would DELETE the verb silently, which is the exact
 *    failure class the first pass over `cn` found and paid for. In place is wrong
 *    on screen and impossible to miss; `renderIntel` and `AppLayout` both provide, so the
 *    fallback is what a mistake looks like, not what the console does.
 *
 * ⛔ AND WHY THERE ARE TWO SLOTS AND NOT ONE. `PageHeader` has always had two — `actions`
 *    for the secondary controls and `primaryAction` for the verb — and a tab needs both
 *    or its second control has nowhere to go but back into the filter row.
 *    `/governance`'s approvals tab offers *New request* AND *Run sweep*; its identities
 *    tab offers *Resync*. With one slot those stay where they were and the rule "the
 *    filter row keeps filters only" ships with exceptions in it — and a gate with
 *    exceptions is worth much less than a gate without.
 */
type Hosts = { primary: HTMLElement; secondary: HTMLElement } | null

const HostContext = createContext<Hosts>(null)

function detachedHost(name: string): HTMLElement {
  const el = document.createElement('span')
  // `contents` so the adopted wrapper is not a box: the control stays a direct flex
  // item of the header's control row and keeps its gap.
  el.style.display = 'contents'
  el.setAttribute(name, '')
  return el
}

/**
 * Owns the two host elements for one page. Mounted by `AppLayout` around the routed
 * content, and by `renderIntel`, so a test tree has the same DOM as the console.
 */
export function PageActionsProvider({ children }: { children: ReactNode }) {
  const [hosts] = useState<Hosts>(() => ({
    primary: detachedHost('data-page-primary-action'),
    secondary: detachedHost('data-page-secondary-actions'),
  }))
  return <HostContext.Provider value={hosts}>{children}</HostContext.Provider>
}

/**
 * Adopts the host into the slot, and ONLY when it is not already there.
 *
 * ⛔ THE GUARD IS THE WHOLE FUNCTION, and it is not defensive coding. `ref={(el) => …}`
 *    is a new function on every render, so React detaches and re-attaches it each time
 *    the header re-renders — and a header re-renders on every poll, because it carries
 *    the refresh state and the live dot. An unconditional `appendChild` MOVES a live
 *    subtree on each of those, and moving a node that contains the focused element blurs
 *    it: an operator who had tabbed to the verb loses it a second later, silently.
 *    Measured with `page-actions.test.tsx`'s focus case, which fails without this guard.
 *
 *    And there is no cleanup on purpose: the host is MOVED when the next header adopts
 *    it, and when a route with no header is on screen it stays inside the detached
 *    subtree of the header that unmounted — which paints nothing, which is right.
 */
function useAdopt(
  pick: (h: NonNullable<Hosts>) => HTMLElement,
): Ref<HTMLSpanElement> {
  const hosts = useContext(HostContext)
  return (el) => {
    if (!el || !hosts) return
    const host = pick(hosts)
    if (host.parentNode !== el) el.appendChild(host)
  }
}

/**
 * Rendered by `PageHeader`, LAST in the control row, so a verb declared by a tab lands
 * exactly where `PageHeader.primaryAction` lands and the header has one rightmost verb.
 */
export function PagePrimaryActionSlot() {
  return <span className="contents" ref={useAdopt((h) => h.primary)} />
}

/** Rendered by `PageHeader` beside `actions`, before the primary action. */
export function PageSecondaryActionsSlot() {
  return <span className="contents" ref={useAdopt((h) => h.secondary)} />
}

/** Declares THE verb of the screen while this subtree is mounted. */
export function PagePrimaryAction({ children }: { children: ReactNode }) {
  const hosts = useContext(HostContext)
  if (!hosts) return <>{children}</>
  return createPortal(children, hosts.primary)
}

/** Declares the tab's secondary controls — never the verb the operator came for. */
export function PageSecondaryActions({ children }: { children: ReactNode }) {
  const hosts = useContext(HostContext)
  if (!hosts) return <>{children}</>
  return createPortal(children, hosts.secondary)
}
