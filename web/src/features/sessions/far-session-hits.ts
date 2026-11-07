// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHEN THE LOADED PAGE HAS NO MATCH, THE ONE SEARCH ANSWERS (#3990). The list holds the
// most recent sessions; an older one is found by the console's search (GET /v1/search),
// the same answer the palette gives under the same key, and it opens by its address.
import { useQuery } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import {
  SEARCH_KIND_SESSION,
  searchConsole,
  searchKeys,
  type ConsoleSearchResult,
} from '@/lib/api/search'
import { useAuth } from '@/lib/auth/context'

/** A pause in typing before the engine is asked, as the palette waits. */
const SEARCH_DELAY_MS = 250
/** The shortest query the engine is asked about. */
const SEARCH_MIN_CHARS = 2

/** The sessions the engine finds for `query`, asked only while `enabled`. */
export function useFarSessionHits(
  query: string,
  enabled: boolean,
): ConsoleSearchResult[] {
  const { activeTenant } = useAuth()
  const [term, setTerm] = useState('')
  useEffect(() => {
    const id = window.setTimeout(
      () => setTerm(query.trim().toLowerCase()),
      SEARCH_DELAY_MS,
    )
    return () => window.clearTimeout(id)
  }, [query])
  const hits = useQuery({
    queryKey: searchKeys.query(activeTenant, term),
    queryFn: () => searchConsole(term),
    enabled: enabled && term.length >= SEARCH_MIN_CHARS && !!activeTenant,
    staleTime: 30_000,
    retry: false,
  })
  return (hits.data?.results ?? []).filter(
    (hit) => hit.kind === SEARCH_KIND_SESSION,
  )
}
