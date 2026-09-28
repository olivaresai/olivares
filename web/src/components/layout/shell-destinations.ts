// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SHELL'S DESTINATIONS, AS REGISTRY IDS.
//
// The sidebar's five journeys and the phone bar name registry views by id and nothing
// else. Path, icon and permission are read from the registry entry at render time, so a
// journey can never point at a route the registry does not hold, and a principal is never
// offered a door the route guard then refuses. `registry.shell-destinations.test.tsx`
// proves both.
import type { LucideIcon } from 'lucide-react'
import {
  authorizedSections,
  viewById,
  type ViewGate,
} from '@/features/navigation/model'
import { NAV_AREAS, type NavArea } from '@/features/registry'

/** The journeys of the sidebar, in their pinned order: Home, Sessions, Workspaces,
 * AI tools, Deploy. */
export const JOURNEY_IDS = [
  'home',
  'sessions',
  'workspaceDashboard',
  'providers',
  'deploy',
] as const

/** The phone bar's links. New (a verb) sits between Sessions and AI tools, and More
 * (the area directory) closes the bar; neither is a destination. */
export const PHONE_BAR_IDS = ['home', 'sessions', 'providers'] as const

export interface ShellDestination {
  /** The registry id: the label key under `nav:shell.journeys`. */
  id: string
  path: string
  icon: LucideIcon
  /** Home matches only itself; every other journey also owns its children. */
  exact: boolean
}

/** The registry views these ids name that this principal may open, in the given order.
 * An id the registry does not hold, a detail view or a view hidden from navigation is
 * never offered. */
export function shellDestinations(
  ids: readonly string[],
  gate: ViewGate,
): ShellDestination[] {
  return ids.flatMap((id) => {
    const view = viewById(id)
    if (!view || view.hideInNav || view.navigation.kind === 'detail') return []
    if (!gate(view)) return []
    return [
      {
        id: view.id,
        path: view.path,
        icon: view.icon,
        exact: view.path === '/',
      },
    ]
  })
}

export function journeyDestinations(gate: ViewGate): ShellDestination[] {
  return shellDestinations(JOURNEY_IDS, gate)
}

export function phoneBarDestinations(gate: ViewGate): ShellDestination[] {
  return shellDestinations(PHONE_BAR_IDS, gate)
}

/** The areas this principal may open: the union of its authorized leaves. An area has
 * no permission of its own, so a permitted leaf never needs a parent grant. */
export function authorizedAreas(gate: ViewGate): NavArea[] {
  return NAV_AREAS.filter((a) => authorizedSections(a.id, gate).length > 0)
}
