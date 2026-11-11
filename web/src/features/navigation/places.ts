// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ONE NAVIGATION: Overview, work, governance and administration, with Settings
// at the foot. Each place holds registry views; the sidebar links its
// lead, and the row above a page moves between the pages of its place: at most five, the rest
// under the row's More menu. A place is offered
// when the principal may open any of its pages, through the same projection the route guard
// uses, so the sidebar never offers a door the route then refuses, and a Business page
// appears only where its edition registers it.
//
// A view the lists below do not name (an edition view) joins the place of its registry
// section (SECTION_PLACE), so no page is ever left out of the navigation. The lists name an
// edition view only where IA.md puts it apart from its section (nodes, the circuit breaker,
// residency, tenants, posture export); an id this build does not register lists nothing.
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
import type { PaidEdition } from '@/components/ui/edition-note'
import { useOpensAtEdition } from '@/features/console/commercial-edition'
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

/** The sidebar group a place sits in, the key under nav:groups; the first places have none. */
export type PlaceGroup = 'control' | 'admin'

export interface Place {
  /** Stable id: the i18n key under nav:places and the badge key. */
  id: PlaceId
  icon: LucideIcon
  group?: PlaceGroup
  /** The registry views of its row, its lead first (doors and details are never listed). */
  views: readonly string[]
  /** The rest of its views, under the row's More menu; a place's page all the same. */
  more?: readonly string[]
  /** The section its lead opens on, when the principal holds it (Rules: the approval queue). */
  section?: ShellSection
}

/** The sidebar's places in order: the first ones, then governance and administration. One
 * noun each. */
export const PLACES: readonly Place[] = [
  { id: 'now', icon: House, views: ['home'] },
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
      'automations',
      'orchestration',
      'recordings',
    ],
    more: [
      'gitPublication',
      'sandbox',
      'voice',
      'sourceDiff',
      'session-viewer',
    ],
  },
  {
    id: 'files',
    icon: Folder,
    // What do I have? The workspace, what is configured in it, what connectors
    // discovered; routes /estate and /inventory keep their paths.
    views: ['workspaceDashboard', 'inventory', 'estate', 'workspace-templates'],
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
  {
    id: 'rules',
    icon: ShieldCheck,
    group: 'control',
    views: ['permissions', 'claudePolicy', 'routinePolicies'],
    more: ['agentcoreExport'],
    section: APPROVALS_SECTION,
  },
  {
    id: 'usage',
    icon: ChartColumn,
    group: 'control',
    views: ['usage', 'storedBudgets', 'adoption', 'teamCosts', 'finops'],
  },
  {
    id: 'activity',
    icon: Activity,
    group: 'control',
    views: ['audit', 'accessMap', 'logs', 'evals'],
    more: ['observability', 'dashboards', 'retention', 'reach'],
  },
  {
    id: 'people',
    icon: Users,
    group: 'admin',
    views: ['identity', 'organization', 'console'],
  },
  {
    id: 'security',
    icon: Shield,
    group: 'admin',
    // Posture, supply chain, compliance, Red team, the kill switch; posture export under
    // More.
    views: ['security', 'killswitch', 'attestation', 'compliance', 'redteam'],
    more: ['postureExport'],
  },
  {
    id: 'operations',
    icon: Server,
    group: 'admin',
    // The installation and its plumbing: hosts, health, backups, system updates and
    // webhooks; alerting, the inference proxy, deployment, nodes, the circuit breaker,
    // residency and tenants under More.
    views: ['operations', 'health', 'backups', 'updates', 'eventing'],
    more: [
      'alerting',
      'inferenceProxy',
      'deploy',
      'fleetNodes',
      'circuitBreaker',
      'residency',
      'tenants',
    ],
  },
]

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
 * A closed row: what a higher edition adds on a page, and the lowest edition that holds it
 * (the registry's own field). A page's own row says nav:business.<id>; the registered
 * views placed above Community that a place closes are one row per edition, at the first
 * one's address.
 */
export interface ClosedRow {
  id: string
  minEdition: PaidEdition
  /** A registered view's address: its row opens the view, which says the edition line. */
  path?: string
  /** A registered view's: its stored data stays readable below its edition. */
  readsBelow?: boolean
  /** The registered views one row closes on its place's page, in place order. */
  views?: readonly string[]
}

const business = (id: string): ClosedRow => ({ id, minEdition: 'business' })

/**
 * What a higher edition adds where Community has the page: one closed row per capability,
 * keyed by the page that shows it (the lead page of its place, Sessions on Work; Retention
 * for its schedules), each naming the lowest edition that holds it (docs/editions.md
 * placement; if in doubt, Business). The shell draws a row after that page's content
 * exactly where the edition held here does not hold it (editionCovers, BusinessRows).
 *
 * Each edition is a copy of the engine's placement, which no read of the console carries
 * yet; the comment on each row names the line it copies.
 */
const BUSINESS_ROWS: Readonly<Record<string, readonly ClosedRow[]>> = {
  // docs/editions.md: Work, Orchestration; Records, Recording of privileged actions.
  // No row for the session cockpit: nothing places it above Community yet.
  work: [business('sessions')],
  // docs/editions.md: Identity sources, Assigning provider profiles and AI tools to
  // departments, groups and people; Testing and spend, budgets that throttle or block.
  'agent-tools': [business('providerAssignment'), business('limitAssignment')],
  // docs/editions.md: Identity sources, Directories and Group nesting.
  identity: [business('people')],
  // docs/editions.md: Authorization, Custom Cedar policies.
  permissions: [business('rules')],
  usage: [
    // modules.json, finops: business.
    business('usage'),
    // docs/editions.md: Testing and spend, budgets that alert or block.
    business('usageBudgets'),
  ],
  security: [
    // docs/editions.md: Compliance, Scheduled reports.
    business('securityCompliance'),
    // modules.json, redteam: business.
    business('securityRedTeam'),
  ],
  operations: [
    // docs/editions.md: Install, Deployment and integration.
    business('operationsDeploy'),
    // docs/editions.md: workspace residency is Business; several organizations,
    // Enterprise.
    business('operationsResidency'),
  ],
  // docs/editions.md: Records, Retention schedules.
  retention: [business('retentionSchedules')],
}

/**
 * The closed rows a page shows where `closed` holds: its own, then the registered views
 * placed above Community in the place it leads, folded into one row per edition that names
 * them all and opens the first, so a place never grows a row per module. Which rows are
 * closed is the shell's question, decided by editionCovers (BusinessRows), never by the
 * build; by default every row a page may show.
 */
export function businessRows(
  viewId: string,
  closed: (row: ClosedRow) => boolean = () => true,
): readonly ClosedRow[] {
  const place = ALL_PLACES.find((p) => p.views[0] === viewId)
  const folded = new Map<PaidEdition, ClosedRow>()
  for (const v of place ? placeViews(place) : []) {
    if (!v.minEdition) continue
    const row = { id: v.id, minEdition: v.minEdition, path: v.path }
    if (!closed({ ...row, readsBelow: v.readsBelow })) continue
    const first = folded.get(v.minEdition)
    folded.set(v.minEdition, {
      ...(first ?? row),
      views: [...(first?.views ?? []), v.id],
    })
  }
  return [...(BUSINESS_ROWS[viewId] ?? []).filter(closed), ...folded.values()]
}

/**
 * Whether a registered view opens at the edition held here (its `minEdition`, decided by
 * editionCovers, or its `readsBelow` while its module runs): the one answer the navigation
 * (authorization.ts) and the route gate (RequirePermission) take. A view it does not open
 * leaves the navigation, is a closed row for an administrator (businessRows) and says the
 * edition line at its address. It reads the setup gate's own server-info query (same key
 * and function, 60 s fresh, one retry): like every reader of it, a mount after the answer
 * is a minute old re-reads it once.
 */
export const useViewOpens = useOpensAtEdition

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
  /** The search its link carries: a section of a page that is a page of the row. */
  search?: Readonly<Record<string, string>>
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
    .flatMap((v): Omit<PageLink, 'more'>[] => {
      // The section the place opens on (Rules: the approval queue) is a page of its own,
      // first; its view then opens on its other sections.
      const s = place.section
      return s?.view === v.id && gate(v, s)
        ? [
            { id: s.key, path: v.path, search: s.search },
            { id: v.id, path: v.path, search: s.others },
          ]
        : [{ id: v.id, path: v.path }]
    })
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
  group?: PlaceGroup
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
    group: place.group,
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
