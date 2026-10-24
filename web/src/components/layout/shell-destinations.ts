// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE PLACES OF THE TOP BAR, AS REGISTRY IDS.
//
// Every page sits in ONE place: the area and section its own entry declares
// (FeatureView.navigation). All areas, the area directories, the palette and the row of
// pages above a page all read that place. The top bar adds no grouping of its own: it
// names the places (Now, Sessions, Tools, Rules, Ledger) and a place stays current on
// every page of its section. The phone bar is the first three places.
//
// Path, icon and permission are read from the registry entry at render time, so a place
// can never point at a route the registry does not hold, and a principal is never offered
// a door the route guard then refuses.
import type { TFunction } from 'i18next'
import type { LucideIcon } from 'lucide-react'
import {
  SETTINGS_UTILITY,
  areaOfView,
  authorizedSections,
  viewById,
  viewLabel,
  type NavLocation,
  type ViewGate,
} from '@/features/navigation/model'
import { NAV_AREAS, type FeatureView, type NavArea } from '@/features/registry'

import { APPROVALS_SECTION, type ShellSection } from './shell-sections'

export { APPROVALS_SECTION, type ShellSection } from './shell-sections'

type ShellEntry = string | ShellSection

/**
 * The top bar's places, in order: Now (the overview), Sessions, Tools, Rules (the
 * approval queue first) and Ledger. Business adds Organization: its view ships with the
 * Business build, and Community registers none, so Community is never offered it. Every
 * other page is in its area, under All areas (the account menu, the phone's More and ⌘K),
 * so a page is never offered twice.
 */
export const SHELL_DESTINATIONS: readonly ShellEntry[] = [
  'home',
  'sessions',
  'agent-tools',
  APPROVALS_SECTION,
  'audit',
  'organization',
]

/** The views that are THEMSELVES places — not a view reached only through one of its
 * sections (Rules opens a tab of Permissions). */
export const DESTINATION_VIEW_IDS: readonly string[] =
  SHELL_DESTINATIONS.filter((e): e is string => typeof e === 'string')

/**
 * The name a person reads for a view: a place by the top bar's own label (Estate, Tools,
 * Ledger), any other view by its registry label. The star, Favorites and Recent use it, so a
 * starred page reads as the top bar names it.
 */
export function destinationLabel(t: TFunction, viewId: string): string {
  return DESTINATION_VIEW_IDS.includes(viewId)
    ? t(`nav:shell.journeys.${viewId}`)
    : viewLabel(t, viewId)
}

/** The section place the current address IS (Rules: Permissions with ?tab=approvals),
 * or null. The address is the section's, so no other place is marked and no row of pages
 * is drawn. */
export function sectionAt(
  viewId: string,
  search: Readonly<Record<string, unknown>>,
): ShellSection | null {
  for (const e of SHELL_DESTINATIONS)
    if (
      typeof e !== 'string' &&
      e.view === viewId &&
      Object.entries(e.search).every(([k, v]) => search[k] === v)
    )
      return e
  return null
}

const placeOf = (view: FeatureView): string | null =>
  view.navigation.kind === 'root'
    ? null
    : `${view.navigation.areaId}/${view.navigation.sectionId}`

/**
 * The place (its label key) that stays current on this address, or null: the section the
 * address is, else the view itself (or the screen a door opens), else the first place of
 * the same section. Only a place this principal is offered counts, so a page whose places
 * it may not open marks none.
 */
export function currentDestination(
  location: NavLocation,
  search: Readonly<Record<string, unknown>>,
  gate: ViewGate,
): string | null {
  if (location.kind !== 'view' && location.kind !== 'home') return null
  const offered = journeyDestinations(gate)
  const has = (key: string) => offered.some((d) => d.key === key)
  const section = sectionAt(location.view.id, search)
  if (section && has(section.key)) return section.key
  const view = location.view.doorTo
    ? (viewById(location.view.doorTo) ?? location.view)
    : location.view
  if (has(view.id)) return view.id
  const place = placeOf(view)
  if (!place) return null
  const pin = offered.find(
    (d) => !d.search && placeOf(viewById(d.id)!) === place,
  )
  return pin?.key ?? null
}

/** True when the address belongs to Settings: the Settings utility itself or any page of
 * its area (System & settings), which the top bar does not repeat as places. */
export function inSettingsArea(location: NavLocation): boolean {
  if (location.kind === 'settings') return true
  return (
    location.kind === 'view' &&
    areaOfView(location.view)?.id === SETTINGS_UTILITY.areaId
  )
}

export interface PageLink {
  id: string
  path: string
}

/**
 * The pages beside this one, drawn as a row above it: the listed pages of its own section,
 * in registry order. Settings stands for its whole area, so a page there (and Settings
 * itself) lists the area's pages, led by Settings. Empty where there is nothing to move
 * between.
 */
export function pagesBeside(location: NavLocation, gate: ViewGate): PageLink[] {
  const link = (v: FeatureView): PageLink => ({ id: v.id, path: v.path })
  if (inSettingsArea(location)) {
    return [
      { id: SETTINGS_UTILITY.id, path: SETTINGS_UTILITY.path },
      ...authorizedSections(SETTINGS_UTILITY.areaId, gate).flatMap((s) =>
        s.views.map(link),
      ),
    ]
  }
  if (location.kind !== 'view' || location.view.navigation.kind === 'root')
    return []
  const { areaId, sectionId } = location.view.navigation
  return (
    authorizedSections(areaId, gate)
      .find((s) => s.sectionId === sectionId)
      ?.views.map(link) ?? []
  )
}

export interface ShellDestination {
  /** The registry id of the view it opens. */
  id: string
  /** The label key under `nav:shell.journeys`: the id, or the section's own key. */
  key: string
  path: string
  /** The search a section destination opens its view with. */
  search?: Readonly<Record<string, string>>
  icon: LucideIcon
}

/** The registry views these ids name that this principal may open, in the given order.
 * An id the registry does not hold, a detail view or a view hidden from navigation is
 * never offered. */
export function shellDestinations(
  ids: readonly ShellEntry[],
  gate: ViewGate,
): ShellDestination[] {
  return ids.flatMap((entry) => {
    const section = typeof entry === 'string' ? null : entry
    const view = viewById(section ? section.view : (entry as string))
    if (!view || view.hideInNav || view.navigation.kind === 'detail') return []
    // The section's own permission goes through the SAME gate, on top of the view's.
    if (!gate(view, section ?? undefined)) return []
    return [
      {
        id: view.id,
        key: section ? section.key : view.id,
        path: view.path,
        search: section?.search,
        icon: view.icon,
      },
    ]
  })
}

/** The places this principal may open, in order. */
export function journeyDestinations(gate: ViewGate): ShellDestination[] {
  return shellDestinations(SHELL_DESTINATIONS, gate)
}

/** The phone bar's links: the first three places this principal may open. New (a verb)
 * sits after the second, and More (the area directory) closes the bar. */
export function phoneBarDestinations(gate: ViewGate): ShellDestination[] {
  return journeyDestinations(gate).slice(0, 3)
}

/** One area of All areas: the area, then its pages. */
export interface DirectoryArea {
  area: NavArea
  views: FeatureView[]
}

/**
 * ALL AREAS, TWO LEVELS DEEP: each area this principal may open, then its pages in section
 * order (a section orders the pages; it is not a level of its own). A page the bar already
 * shows this principal as a place is not listed again, so each place appears once in the
 * navigation, and an area left with no page is not listed. `places` is what the bar shows
 * at this width: the top bar's places, or the phone bar's first three below 761 px, whose
 * other places then stay in their areas.
 */
export function directoryAreas(
  gate: ViewGate,
  places: readonly ShellDestination[] = journeyDestinations(gate),
): DirectoryArea[] {
  const shown = new Set(places.map((d) => d.id))
  return NAV_AREAS.map((area) => ({
    area,
    views: authorizedSections(area.id, gate)
      .flatMap((s) => s.views)
      .filter((v) => !shown.has(v.id)),
  })).filter((a) => a.views.length > 0)
}

/** The areas this principal may open: the union of its authorized leaves. An area has
 * no permission of its own, so a permitted leaf never needs a parent grant. */
export function authorizedAreas(gate: ViewGate): NavArea[] {
  return NAV_AREAS.filter((a) => authorizedSections(a.id, gate).length > 0)
}
