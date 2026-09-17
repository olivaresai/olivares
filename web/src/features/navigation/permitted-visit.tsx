// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useEffect } from 'react'
import { usePersonalNavigation } from './personal-navigation'

/** Mount in the existing admitted branch only. No read/action success is implied. */
export function PermittedVisit({ id }: { id: string }) {
  const record = usePersonalNavigation()?.recordVisit
  useEffect(() => {
    record?.(id)
  }, [record, id])
  return null
}

/** Inside AppLayout + TenantGate: Settings' real admission is authenticated utility access. */
export function SettingsVisit() {
  return <PermittedVisit id="settings" />
}
