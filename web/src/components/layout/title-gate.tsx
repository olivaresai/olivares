// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE PLACE YOU CLICK IS THE PLACE YOU LAND ON. The sidebar link of a place opens one page
// for each person: the first page of the place that person may open, and the tab title names
// that page by the place. Which page that is depends on the person (a fresh install has no
// Security or Health module, so its Security and Operations links open Kill switch and
// Backups), so the shell, which knows what this person may open, tells the title here.
import { useEffect, type ReactNode } from 'react'
import { useViewAccess } from '@/features/navigation/authorization'
import { setTitleGate } from './document-title'

export function TitleGate({ children }: { children: ReactNode }) {
  const { listed } = useViewAccess()
  // The root title sits above the sign-in: it learns here which pages this person may open.
  useEffect(() => {
    setTitleGate(listed)
    return () => setTitleGate(undefined)
  }, [listed])
  return children
}
