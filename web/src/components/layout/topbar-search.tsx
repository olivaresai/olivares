// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE TOP BAR'S SEARCH, OWNED BY A PLACE (console Ledger, issue #2554). The top bar shows
// search with ⌘K everywhere; a place whose list is a query (the Ledger) declares its own
// field by rendering `<TopbarSearch>`, and the frame draws it where search was, through a
// portal, so the field's state stays the page's. The same shape as the side panel: the
// frame owns the place, the page owns the content. Without a provider (a view rendered
// bare in a test) the field renders in place rather than nowhere.
import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react'
import { createPortal } from 'react-dom'

interface TopbarSearchState {
  claimed: boolean
  host: HTMLElement | null
  claim: (on: boolean) => void
  setHost: (host: HTMLElement | null) => void
}

const TopbarSearchContext = createContext<TopbarSearchState | null>(null)

export function TopbarSearchProvider({ children }: { children: ReactNode }) {
  const [claimed, claim] = useState(false)
  const [host, setHost] = useState<HTMLElement | null>(null)
  const value = useMemo(
    () => ({ claimed, host, claim, setHost }),
    [claimed, host],
  )
  return (
    <TopbarSearchContext.Provider value={value}>
      {children}
    </TopbarSearchContext.Provider>
  )
}

/** A page's own search field, drawn in the top bar while the page is mounted. */
export function TopbarSearch({ children }: { children: ReactNode }) {
  const search = useContext(TopbarSearchContext)
  const claim = search?.claim
  useEffect(() => {
    if (!claim) return
    claim(true)
    return () => claim(false)
  }, [claim])
  if (!search) return <>{children}</>
  return search.host ? createPortal(children, search.host) : null
}

/** In the top bar: the page's field when one is declared, the shell's search otherwise. */
export function TopbarSearchSlot({ fallback }: { fallback: ReactNode }) {
  const search = useContext(TopbarSearchContext)
  const claimed = search?.claimed ?? false
  const setHost = search?.setHost
  if (!claimed || !setHost) return <>{fallback}</>
  return (
    <div
      ref={setHost}
      data-slot="topbar-search"
      className="flex min-w-0 shrink items-center"
    />
  )
}
