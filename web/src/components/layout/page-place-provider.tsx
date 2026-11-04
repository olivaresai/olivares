// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE PLACE YOU CLICK IS THE PLACE YOU LAND ON. The sidebar link of a place opens one page
// for each person: the first page of the place that person may open. That page is named by
// the place in the top bar, the tab title and its heading. Which page that is depends on the
// person (a fresh install has no Security or Health module, so its Security and Operations
// links open Kill switch and Backups), so the shell, which knows what this person may open,
// says it once here.
import { useRouterState } from '@tanstack/react-router'
import { useEffect, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { PagePlaceContext } from '@/components/ui/page-place'
import { useViewAccess } from '@/features/navigation/authorization'
import { placeOpenedAt } from '@/features/navigation/places'
import { setTitleGate } from './document-title'

export function PagePlaceProvider({ children }: { children: ReactNode }) {
  const { t } = useTranslation('nav')
  const { listed } = useViewAccess()
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const search = useRouterState({
    select: (s) => (s.location.search ?? {}) as Record<string, unknown>,
  })
  // The root title sits above the sign-in: it learns here which pages this person may open.
  useEffect(() => {
    setTitleGate(listed)
    return () => setTitleGate(undefined)
  }, [listed])
  const place = placeOpenedAt(pathname, search, listed)
  return (
    <PagePlaceContext.Provider value={place ? t(`places.${place}`) : null}>
      {children}
    </PagePlaceContext.Provider>
  )
}
