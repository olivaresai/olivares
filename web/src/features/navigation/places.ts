// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ONE NAVIGATION: the left sidebar's places, in order (Sessions, Files and Tools, then
// the places under More, then Settings at the foot). Each place holds registry views; the sidebar links its
// lead, and the row above a page moves between the pages of its place: at most five, the rest
// under the row's More menu. A place is offered
// when the principal may open any of its pages, through the same projection the route guard
// uses, so the sidebar never offers a door the route then refuses, and a Business page
// appears only where its edition registers it.
//
// A view the lists below do not name (an edition view) joins the place of its registry
// section (SECTION_PLACE), so no page is ever left out of the navigation. The lists name an
// edition view only where IA.md puts it apart from its section (residency, tenants); an id
// this build does not register lists nothing.
import type { TFunction } from 'i18next'
import type { LucideIcon } from 'lucide-react'
import {
  Activity,
  ChartColumn,
  Folder,
  House,
  MessageSquare,
  Server,
  Shield,
  ShieldCheck,
  Users,
  Wrench,
} from 'lucide-react'
import { BUSINESS_BUILD } from '@/features/business-build'
import {
  APPROVALS_SECTION,
  type ShellSection,
} from '@/components/layout/shell-sections'
import {
  FEATURE_VIEWS,
  nounsForView,
  type FeatureView,
} from '@/features/registry'
import {
  SETTINGS_UTILITY,
  areaLabel,
  areaOfView,
  fold,
  isListed,
  resolveLocation,
  sectionLabel,
  viewById,
  viewDescription,
  viewLabel,
  type NavSearchEntry,
  type ViewGate,
} from './model'

export type PlaceId =
  | 'now'
  | 'sessions'
  | 'files'
  | 'tools'
  | 'rules'
  | 'usage'
  | 'activity'
  | 'people'
  | 'security'
  | 'operations'
  | 'settings'

export interface Place {
  /** Stable id: the i18n key under nav:places and the badge key. */
  id: PlaceId
  icon: LucideIcon
  /** The registry views of its row, its lead first (doors and details are never listed). */
  views: readonly string[]
  /** The rest of its views, under the row's More menu; a place's page all the same. */
  more?: readonly string[]
  /** The section its lead opens on, when the principal holds it (Rules: the approval queue). */
  section?: ShellSection
}

/** The sidebar's places in order: the first ones, then the ones under More. One noun each,
 * no group above them. */
export const PLACES: readonly Place[] = [
  {
    id: 'sessions',
    icon: MessageSquare,
    // The sessions and the work around them: work items, channels (the four communications
    // doors follow their room), voice and the sandbox; Git publication, source diff and the
    // recordings under More.
    views: [
      'sessions',
      'agentops',
      'work',
      'communications',
      'communicationsInbox',
      'communicationsNew',
      'communicationsHandoffs',
      'communicationsAdministration',
      'voice',
      'sandbox',
    ],
    more: ['gitPublication', 'sourceDiff', 'recordings', 'session-viewer'],
  },
  {
    id: 'files',
    icon: Folder,
    // What do I have? The workspace, what is configured in it, what connectors
    // discovered; routes /estate and /inventory keep their paths.
    views: ['workspaceDashboard', 'estate', 'inventory', 'workspace-templates'],
  },
  {
    id: 'tools',
    icon: Wrench,
    // What the sessions run on: the AI tools (their provider doors follow them), models,
    // MCP and skills, knowledge; the reference pages behind them under More.
    views: [
      'agent-tools',
      'providers',
      'providerProfiles',
      'providerBindings',
      'providerAccounts',
      'models',
      'capabilities',
      'knowledge',
    ],
    more: [
      'modelOps',
      'platforms',
      'rateLimits',
      'protocolBindings',
      'apiPlayground',
      'catalog',
      'agentArtifacts',
    ],
  },
  { id: 'now', icon: House, views: ['home'] },
  {
    id: 'rules',
    icon: ShieldCheck,
    views: ['permissions', 'claudePolicy', 'routinePolicies'],
    more: ['agentcoreExport'],
    section: APPROVALS_SECTION,
  },
  {
    id: 'usage',
    icon: ChartColumn,
    views: ['usage', 'finops', 'storedBudgets', 'adoption'],
  },
  {
    id: 'activity',
    icon: Activity,
    views: ['audit', 'accessMap', 'logs', 'evals'],
    more: ['observability', 'dashboards', 'retention', 'reach'],
  },
  {
    id: 'people',
    icon: Users,
    views: ['identity', 'organization', 'console'],
  },
  {
    id: 'security',
    icon: Shield,
    // Posture, supply chain, compliance, Red team, the kill switch.
    views: ['security', 'attestation', 'compliance', 'redteam', 'killswitch'],
  },
  {
    id: 'operations',
    icon: Server,
    // The installation and its plumbing: hosts, health, backups, system updates and
    // webhooks; alerting, the inference proxy, deployment, residency and tenants under More.
    views: ['operations', 'health', 'backups', 'updates', 'eventing'],
    more: ['alerting', 'inferenceProxy', 'deploy', 'residency', 'tenants'],
  },
]

/** What the navigation shows a new person: Sessions, Files and Tools. Every other place is
 * one click away under More and in the palette; none is removed or turned off by this. */
export const FIRST_PLACES: ReadonlySet<PlaceId> = new Set<PlaceId>([
  'sessions',
  'files',
  'tools',
])

/** Settings, pinned at the foot: the Settings utility, then the pages it holds. */
export const SETTINGS_PLACE: Place = {
  id: 'settings',
  icon: SETTINGS_UTILITY.icon,
  views: ['vault', 'onboarding'],
}

const ALL_PLACES: readonly Place[] = [...PLACES, SETTINGS_PLACE]

/** Where a view the lists above do not name goes: the place of its registry section. */
const SECTION_PLACE: Readonly<Record<string, PlaceId>> = {
  'infrastructure/estate': 'files',
  'ai/sessions': 'sessions',
  'ai/environments': 'tools',
  'ai/models': 'tools',
  'ai/execution': 'sessions',
  'ai/provider-reference': 'tools',
  'data-context/capabilities': 'tools',
  'data-context/knowledge': 'tools',
  'work-communications/work': 'sessions',
  'automation/workflows': 'sessions',
  'automation/events': 'operations',
  'security-identity/access': 'people',
  'security-identity/policy': 'rules',
  'security-identity/defense': 'security',
  'deployment/deployments': 'operations',
  'observation/operations': 'operations',
  'observation/cost-adoption': 'usage',
  'observation/audit-recordings': 'activity',
  'system/administration': 'people',
  'system/maintenance': 'operations',
  'system/development': 'tools',
  'system/preferences': 'settings',
}

/**
 * What Business adds where Community has the page: one closed row per capability, keyed by
 * the page that shows it (the lead page of its place, Sessions on Work; Retention for its
 * schedules), text under nav:business. Community draws them after that page's content; a
 * Business build registers the real pages and draws none.
 */
const BUSINESS_ROWS: Readonly<Record<string, readonly string[]>> = {
  work: ['sessions'],
  identity: ['people'],
  permissions: ['rules'],
  usage: ['usage'],
  security: ['securityCompliance', 'securityRedTeam'],
  operations: ['operationsDeploy', 'operationsResidency'],
  retention: ['retentionSchedules'],
}

/** The closed Business rows a page shows: its nav:business keys, none in a Business build. */
export function businessRows(viewId: string): readonly string[] {
  return BUSINESS_BUILD ? [] : (BUSINESS_ROWS[viewId] ?? [])
}

export function placeById(id: PlaceId): Place {
  return ALL_PLACES.find((p) => p.id === id)!
}

/** The views a place names, its row then its More. */
const namedBy = (place: Place) => [...place.views, ...(place.more ?? [])]

const named = (view: FeatureView) =>
  ALL_PLACES.find((p) => namedBy(p).includes(view.id))

/** The place a registry view belongs to: named by a place, else its registry section's. */
export function placeOfView(view: FeatureView): PlaceId {
  const place = named(view)
  if (place) return place.id
  if (view.navigation.kind === 'root') return 'now'
  return (
    SECTION_PLACE[`${view.navigation.areaId}/${view.navigation.sectionId}`] ??
    'settings'
  )
}

/** The views of one place in its order, then the edition views its sections bring. */
function placeViews(place: Place): FeatureView[] {
  return [
    ...namedBy(place).flatMap((id) => viewById(id) ?? []),
    ...FEATURE_VIEWS.filter((v) => !named(v) && placeOfView(v) === place.id),
  ]
}

/** A row shows at most this many pages of its place; More holds the rest. */
const ROW_PAGES = 5

export interface PageLink {
  id: string
  path: string
  /** Under the row's More menu, not in the row itself. */
  more: boolean
}

/**
 * The pages this principal may open in a place, in its order: the row above a page, then its
 * More. Settings is led by the Settings utility. A detail view (a recording's viewer) and a
 * view hidden from navigation are never listed; a door is listed only where its room is not
 * (isListed). The row holds at most ROW_PAGES pages; the place's `more` and the overflow (a
 * closed room's doors, an edition page past a full row) go under More, still in place order.
 */
export function placePages(id: PlaceId, gate: ViewGate): PageLink[] {
  const place = placeById(id)
  const listed = placeViews(place)
    .filter(
      (v) =>
        !v.hideInNav && v.navigation.kind === 'feature' && isListed(v, gate),
    )
    .map((v) => ({ id: v.id, path: v.path }))
  const pages =
    id === 'settings'
      ? [{ id: SETTINGS_UTILITY.id, path: SETTINGS_UTILITY.path }, ...listed]
      : listed
  const row = pages
    .filter((p) => !place.more?.includes(p.id))
    .slice(0, ROW_PAGES)
  return pages.map((p) => ({ ...p, more: !row.includes(p) }))
}

export interface SidebarPlace {
  id: PlaceId
  icon: LucideIcon
  path: string
  search?: Readonly<Record<string, string>>
}

const MAY_OPEN_EVERYTHING: ViewGate = () => true

/** The page a place's link opens: its first page the gate lists. */
function leadOf(place: Place, gate: ViewGate): FeatureView | undefined {
  return placeViews(place).find(
    (v) =>
      !v.hideInNav &&
      (v.navigation.kind === 'root' ||
        (v.navigation.kind === 'feature' && isListed(v, gate))),
  )
}

/** The place a view is the lead page of, for a person the gate describes, or null. */
export function placeLedBy(
  view: FeatureView,
  gate: ViewGate = MAY_OPEN_EVERYTHING,
): PlaceId | null {
  return PLACES.find((p) => leadOf(p, gate)?.id === view.id)?.id ?? null
}

/**
 * The place whose sidebar link opens this address for a person the gate describes, or null.
 * That place names the page in the tab title: the place you click is the place you land on.
 * Settings is a page of its own whose General section (the default) is the Settings place.
 */
export function placeOpenedAt(
  pathname: string,
  search: Readonly<Record<string, unknown>>,
  gate: ViewGate = MAY_OPEN_EVERYTHING,
): PlaceId | null {
  const location = resolveLocation(pathname)
  switch (location.kind) {
    case 'home':
      return 'now'
    case 'settings':
      return search.section === undefined || search.section === 'general'
        ? 'settings'
        : null
    case 'view':
      return placeLedBy(location.view, gate)
    default:
      return null
  }
}

/** Where a place's link opens for this principal, or null when it may open none of it. */
function placeLink(place: Place, gate: ViewGate): SidebarPlace | null {
  const at = (path: string, search?: Readonly<Record<string, string>>) => ({
    id: place.id,
    icon: place.icon,
    path,
    search,
  })
  if (place.id === 'settings') return at(SETTINGS_UTILITY.path)
  const lead = leadOf(place, gate)
  if (!lead) return null
  const section = place.section
  return section && section.view === lead.id && gate(lead, section)
    ? at(lead.path, section.search)
    : at(lead.path)
}

/** The sidebar for this principal: the places it may open, in order. */
export function sidebarPlaces(gate: ViewGate): SidebarPlace[] {
  return PLACES.flatMap((p) => placeLink(p, gate) ?? [])
}

/** The Settings link at the sidebar's foot. */
export function settingsPlace(): SidebarPlace {
  return placeLink(SETTINGS_PLACE, () => true)!
}

/** The place an address belongs to, or null (an unknown path). Settings holds every
 * Settings section. */
export function currentPlace(pathname: string): PlaceId | null {
  const location = resolveLocation(pathname)
  switch (location.kind) {
    case 'home':
      return 'now'
    case 'settings':
      return 'settings'
    case 'view':
      return placeOfView(location.parent ?? location.view)
    default:
      return null
  }
}

// --- the palette's index ------------------------------------------------------------

function aliasesFor(t: TFunction, id: string): string[] {
  const out: string[] = []
  for (const lng of [undefined, 'en'] as const) {
    const raw = t(`nav:aliases.${id}`, {
      defaultValue: '',
      ...(lng ? { lng } : {}),
    })
    for (const part of String(raw).split('·'))
      if (part.trim()) out.push(part.trim())
  }
  const en = t(`nav:items.${id}`, { lng: 'en', defaultValue: '' })
  if (en) out.push(String(en))
  return [...new Set(out)]
}

/**
 * What the ⌘K palette searches: every non-hidden view and the Settings utility, with the
 * current label, the English fallback label, explicit former names, path, the place that
 * holds it and the thirteen nouns. Each result says its place, the word of the sidebar; the
 * retired area directories and place names are no destination here, and their names stay
 * search words only, so "Oversight" still finds what it held. Authorization is NOT applied
 * here (`authorizedEntries` does).
 */
export function buildNavSearchIndex(t: TFunction): NavSearchEntry[] {
  const entries: NavSearchEntry[] = []
  let order = 0
  for (const v of FEATURE_VIEWS) {
    if (v.hideInNav || v.navigation.kind === 'detail') continue
    const pageName = viewLabel(t, v.id)
    // The page a place opens is listed by the place, as in the sidebar and the tab title; its
    // own name stays a search word.
    const led = placeLedBy(v)
    const label = led ? t(`nav:places.${led}`) : pageName
    const description = viewDescription(t, v.id)
    const context = t(`nav:places.${placeOfView(v)}`)
    const area = areaOfView(v)
    const former = area
      ? [
          areaLabel(t, area.id),
          ...(v.navigation.kind === 'feature'
            ? [sectionLabel(t, area.id, v.navigation.sectionId)]
            : []),
        ]
      : []
    entries.push({
      kind: 'view',
      id: v.id,
      path: v.path,
      icon: v.icon,
      label,
      context,
      description,
      order: order++,
      haystack: {
        label: fold(label),
        aliases: [...aliasesFor(t, v.id), ...(led ? [pageName] : [])].map(fold),
        path: fold(v.path),
        context: [
          fold(context),
          ...former.map(fold),
          ...nounsForView(v.id).map((n) => fold(String(t(`nav:nouns.${n}`)))),
        ],
        description: fold(description),
      },
    })
  }
  const settingsLabel = viewLabel(t, SETTINGS_UTILITY.id)
  const settingsFormer = [
    areaLabel(t, SETTINGS_UTILITY.areaId),
    sectionLabel(t, SETTINGS_UTILITY.areaId, SETTINGS_UTILITY.sectionId),
  ]
  entries.push({
    kind: 'settings',
    id: SETTINGS_UTILITY.id,
    path: SETTINGS_UTILITY.path,
    icon: SETTINGS_UTILITY.icon,
    label: settingsLabel,
    // The utility is the Settings place itself: nothing to say beside its name.
    context: '',
    description: viewDescription(t, SETTINGS_UTILITY.id),
    order,
    haystack: {
      label: fold(settingsLabel),
      aliases: aliasesFor(t, SETTINGS_UTILITY.id).map(fold),
      path: fold(SETTINGS_UTILITY.path),
      context: settingsFormer.map(fold),
      description: fold(viewDescription(t, SETTINGS_UTILITY.id)),
    },
  })
  return entries
}
