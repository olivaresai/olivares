// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { viewById } from '@/features/navigation/model'
import { useAuth } from '@/lib/auth/context'

/** The Operations page's path when the person may open it, else null. Now and Health >
 *  System link to it, so neither names a page the route would then refuse. */
export function useOperationsPath(): string | null {
  const { can } = useAuth()
  const view = viewById('operations')
  return view && (!view.permission || can(view.permission)) ? view.path : null
}
