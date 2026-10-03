// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SHELL'S DESTINATIONS, AS REGISTRY IDS.
//
// The sidebar's destinations and the phone bar name registry views by id and nothing
// else. Path, icon and permission are read from the registry entry at render time, so a
// journey can never point at a route the registry does not hold, and a principal is never
// offered a door the route guard then refuses. `registry.shell-destinations.test.tsx`
// proves both.
import type { TFunction } from 'i18next'
import type { LucideIcon } from 'lucide-react'
import {
  authorizedSections,
  viewById,
  viewLabel,
  type GatedSection,
  type ViewGate,
} from '@/features/navigation/model'
import { NAV_AREAS, type NavArea } from '@/features/registry'

/**
 * THE SIDEBAR'S GROUPS (console remake 26.10, CONCEPT-IA): a short work section, then the
 * product's three verbs — Manage, Integrate, Secure. Each entry is ONE existing registry view;
 * a destination that merges several views (Agents, Identities & access, Approvals) replaces its
 * entry here in its own slice. Everything else stays one step away in All areas and ⌘K.
 */
/**
 * A destination that is a SECTION of a registry view rather than the view itself: the
 * approval queue is the first tab of Permissions. It is offered only when the principal
 * may open the view AND holds the section's own permission, and it links with the
 * section's search so the view opens on it.
 */
export interface ShellSection extends GatedSection {
  /** The label key under `nav:shell.journeys` and the key its count is given under. */
  key: string
  /** The registry view that holds the section. */
  view: string
  search: Readonly<Record<string, string>>
}

export const APPROVALS_SECTION: ShellSection = {
  key: 'approvals',
  view: 'permissions',
  search: { tab: 'approvals' },
  requires: 'governance:approval:read',
}

type ShellEntry = string | ShellSection

export const SHELL_GROUPS = [
  { id: 'work', ids: ['home', 'sessions', 'work', APPROVALS_SECTION] },
  { id: 'manage', ids: ['inventory', 'finops', 'accessMap', 'deploy'] },
  // MCP servers opens the page where servers are added, tested and enabled (HU 025): the
  // discovered catalog ("MCP & skills") stays in All areas, not as a second MCP page.
  { id: 'integrate', ids: ['agent-tools', 'mcpServers', 'knowledge'] },
  { id: 'secure', ids: ['identity', 'claudePolicy', 'audit', 'killswitch'] },
] as const satisfies readonly { id: string; ids: readonly ShellEntry[] }[]

export type ShellGroupId = (typeof SHELL_GROUPS)[number]['id']

/** Every destination of the sidebar, in its pinned order (the groups, flattened). */
export const JOURNEY_IDS: readonly string[] = SHELL_GROUPS.flatMap((g) =>
  g.ids.map((e: ShellEntry) => (typeof e === 'string' ? e : e.view)),
)

/** The views that are THEMSELVES sidebar destinations — not a view reached only through
 * one of its sections (Approvals opens a tab of Permissions). The top bar's short trail
 * and the "All areas" highlight follow these; a section's view keeps its registry trail. */
export const DESTINATION_VIEW_IDS: readonly string[] = SHELL_GROUPS.flatMap(
  (g) => g.ids.flatMap((e: ShellEntry) => (typeof e === 'string' ? [e] : [])),
)

/**
 * The name a person reads for a view: a sidebar destination by the sidebar's own label
 * (Sessions, Cost, Policies), any other view by its registry label. The top bar already
 * names a destination this way; the star, Favorites and Recent use it too, so a starred
 * page reads as the sidebar names it (09b: "Observe sessions" beside "Sessions").
 */
export function destinationLabel(t: TFunction, viewId: string): string {
  return DESTINATION_VIEW_IDS.includes(viewId)
    ? t(`nav:shell.journeys.${viewId}`)
    : viewLabel(t, viewId)
}

/**
 * DESTINATIONS THAT SPAN SEVERAL VIEWS (CONCEPT-IA, grouping confirmed by Root): the
 * sidebar entry stays current on every member, and a strip above each member page moves
 * between the members this principal may open. Keyed by the destination's own view id,
 * which is the first member — except the footer's Settings, which is not a registry view
 * and so is not listed among its own members. Each view keeps its own path, permission and
 * deep links; nothing is re-routed.
 */
export const SETTINGS_DESTINATION = 'settings'

export const DESTINATION_MEMBERS: Readonly<Record<string, readonly string[]>> =
  {
    sessions: ['sessions', 'agentops'],
    work: [
      'work',
      'communicationsHandoffs',
      'communicationsInbox',
      'communications',
      'communicationsNew',
      'communicationsAdministration',
      'automations',
      'orchestration',
      'eventing',
    ],
    inventory: [
      'inventory',
      'workspaceDashboard',
      'health',
      'observability',
      'dashboards',
      'alerting',
    ],
    finops: ['finops', 'rateLimits', 'adoption'],
    deploy: ['deploy', 'gitPublication'],
    // AI tools is one list of tools with API keys as its only other tab (HU on
    // refresh 01: ten tabs). Profiles, accounts, bindings, models, platforms, sandbox
    // and voice stay in All areas.
    'agent-tools': ['agent-tools', 'providers'],
    mcpServers: ['mcpServers', 'catalog', 'protocolBindings'],
    knowledge: ['knowledge', 'agentArtifacts'],
    identity: ['identity', 'permissions', 'tenants'],
    claudePolicy: [
      'claudePolicy',
      'routinePolicies',
      'inferenceProxy',
      'residency',
      'redteam',
      'agentcoreExport',
    ],
    audit: [
      'audit',
      'compliance',
      'postureExport',
      'reporting',
      'attestation',
      'evals',
      'security',
      // Privileged session recording: human administrative actions as hash-chained
      // evidence anchored to the ledger (features/recordings) — evidence, not agent
      // sessions.
      'recordings',
    ],
    [SETTINGS_DESTINATION]: [
      'console',
      'backups',
      'logs',
      'onboarding',
      'apiPlayground',
    ],
  }

/** Members that are another DOOR into their destination's own view, not a page of their
 * own: /agentops mounts the Sessions room with its own permission. They read as
 * the destination itself — the journey trail — rather than "Sessions / <page>". */
export const DESTINATION_DOORS: ReadonlySet<string> = new Set(['agentops'])

/** The destination (its own view id, or "settings") that a view is a member of, or null. */
export function destinationOf(viewId: string): string | null {
  for (const [destination, members] of Object.entries(DESTINATION_MEMBERS)) {
    if (members.includes(viewId)) return destination
  }
  return null
}

/** The section destination the current address IS (Approvals: Permissions with
 * ?tab=approvals), or null. A section wins over its view's membership: the address is the
 * section's, so no other destination is marked and no member strip is drawn. */
export function sectionAt(
  viewId: string,
  search: Readonly<Record<string, unknown>>,
): ShellSection | null {
  for (const g of SHELL_GROUPS)
    for (const e of g.ids as readonly ShellEntry[])
      if (
        typeof e !== 'string' &&
        e.view === viewId &&
        Object.entries(e.search).every(([k, v]) => search[k] === v)
      )
        return e
  return null
}

/** True when the current address belongs to a sidebar destination other than by being
 * one: a section destination (its view with the section's search) or a member of a
 * destination that spans several views. The sidebar then marks that destination, and "All
 * areas" does not claim the page too. */
export function coveredByDestination(
  viewId: string,
  search: Readonly<Record<string, unknown>>,
): boolean {
  return destinationOf(viewId) !== null || sectionAt(viewId, search) !== null
}

/** The phone bar's links. New (a verb) sits between Sessions and AI tools, and More
 * (the area directory) closes the bar; neither is a destination. */
export const PHONE_BAR_IDS = ['home', 'sessions', 'agent-tools'] as const

export interface ShellDestination {
  /** The registry id of the view it opens. */
  id: string
  /** The label key under `nav:shell.journeys`: the id, or the section's own key. */
  key: string
  path: string
  /** The search a section destination opens its view with. */
  search?: Readonly<Record<string, string>>
  icon: LucideIcon
  /** Home matches only itself; every other journey also owns its children. */
  exact: boolean
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
        exact: view.path === '/',
      },
    ]
  })
}

export function journeyDestinations(gate: ViewGate): ShellDestination[] {
  return shellDestinations(JOURNEY_IDS, gate)
}

export interface ShellGroup {
  id: ShellGroupId
  destinations: ShellDestination[]
}

/** The groups this principal may open, each with its permitted destinations; a group with
 * none is not drawn. */
export function groupedDestinations(gate: ViewGate): ShellGroup[] {
  return SHELL_GROUPS.map((g) => ({
    id: g.id,
    destinations: shellDestinations(g.ids, gate),
  })).filter((g) => g.destinations.length > 0)
}

export function phoneBarDestinations(gate: ViewGate): ShellDestination[] {
  return shellDestinations(PHONE_BAR_IDS, gate)
}

/** The areas this principal may open: the union of its authorized leaves. An area has
 * no permission of its own, so a permitted leaf never needs a parent grant. */
export function authorizedAreas(gate: ViewGate): NavArea[] {
  return NAV_AREAS.filter((a) => authorizedSections(a.id, gate).length > 0)
}
