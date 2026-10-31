// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ONE NAVIGATION: the left sidebar's places, in the ratified order (Work, Tools, Control,
// Admin, then Settings at the foot). Each place holds registry views; the sidebar links its
// lead, and the row above a page moves between the pages of its place. A place is offered
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
  BookOpen,
  Briefcase,
  ChartColumn,
  Folder,
  House,
  Layers,
  MessageSquare,
  Network,
  Plug,
  ScrollText,
  Server,
  Shield,
  ShieldCheck,
  Users,
  Wrench,
} from 'lucide-react'
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
  | 'work'
  | 'ai-tools'
  | 'models'
  | 'mcp-skills'
  | 'sources'
  | 'rules'
  | 'usage'
  | 'activity'
  | 'access'
  | 'people'
  | 'security'
  | 'operations'
  | 'license'
  | 'settings'

export type PlaceGroupId = 'work' | 'tools' | 'control' | 'admin'

export interface Place {
  /** Stable id: the i18n key under nav:places and the badge key. */
  id: PlaceId
  icon: LucideIcon
  /** The registry views it holds, its lead first. */
  views: readonly string[]
  /** The section its lead opens on, when the principal holds it (Rules: the approval queue). */
  section?: ShellSection
  /** A place that is a Settings section, not a registry view (License: the edition). */
  settingsSection?: string
}

export const PLACE_GROUPS: readonly {
  id: PlaceGroupId
  places: readonly Place[]
}[] = [
  {
    id: 'work',
    places: [
      { id: 'now', icon: House, views: ['home'] },
      {
        id: 'sessions',
        icon: MessageSquare,
        views: [
          'sessions',
          'agentops',
          'voice',
          'recordings',
          'session-viewer',
        ],
      },
      {
        id: 'files',
        icon: Folder,
        views: ['workspaceDashboard', 'workspace-templates', 'inventory'],
      },
      {
        id: 'work',
        icon: Briefcase,
        views: [
          'work',
          'communications',
          'communicationsInbox',
          'communicationsNew',
          'communicationsHandoffs',
          'communicationsAdministration',
          'eventing',
          'alerting',
          'sandbox',
          'gitPublication',
          'sourceDiff',
        ],
      },
    ],
  },
  {
    id: 'tools',
    places: [
      {
        id: 'ai-tools',
        icon: Wrench,
        views: [
          'agent-tools',
          'providers',
          'providerProfiles',
          'providerBindings',
          'providerAccounts',
        ],
      },
      {
        id: 'models',
        icon: Layers,
        views: ['models', 'modelOps', 'platforms', 'rateLimits'],
      },
      {
        id: 'mcp-skills',
        icon: Plug,
        views: ['capabilities', 'protocolBindings', 'apiPlayground'],
      },
      {
        id: 'sources',
        icon: BookOpen,
        views: ['knowledge', 'catalog', 'agentArtifacts'],
      },
    ],
  },
  {
    id: 'control',
    places: [
      {
        id: 'rules',
        icon: ShieldCheck,
        views: [
          'permissions',
          'claudePolicy',
          'routinePolicies',
          'agentcoreExport',
        ],
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
        views: [
          'audit',
          'observability',
          'logs',
          'evals',
          'dashboards',
          'retention',
        ],
      },
      { id: 'access', icon: Network, views: ['accessMap', 'reach'] },
    ],
  },
  {
    id: 'admin',
    places: [
      {
        id: 'people',
        icon: Users,
        views: ['identity', 'organization', 'console'],
      },
      {
        id: 'security',
        icon: Shield,
        // Posture, supply chain, compliance, Red team, the kill switch.
        views: [
          'security',
          'attestation',
          'compliance',
          'redteam',
          'killswitch',
        ],
      },
      {
        id: 'operations',
        icon: Server,
        views: [
          'estate',
          'health',
          'deploy',
          'backups',
          'residency',
          'inferenceProxy',
          'tenants',
        ],
      },
      {
        id: 'license',
        icon: ScrollText,
        views: [],
        settingsSection: 'edition',
      },
    ],
  },
]

/** Settings, pinned at the foot: the Settings utility, then the pages it holds. */
export const SETTINGS_PLACE: Place = {
  id: 'settings',
  icon: SETTINGS_UTILITY.icon,
  views: ['vault', 'onboarding'],
}

const ALL_PLACES: readonly Place[] = [
  ...PLACE_GROUPS.flatMap((g) => g.places),
  SETTINGS_PLACE,
]

/** Where a view the lists above do not name goes: the place of its registry section. */
const SECTION_PLACE: Readonly<Record<string, PlaceId>> = {
  'infrastructure/estate': 'files',
  'ai/sessions': 'sessions',
  'ai/environments': 'ai-tools',
  'ai/models': 'models',
  'ai/execution': 'work',
  'ai/provider-reference': 'models',
  'data-context/capabilities': 'mcp-skills',
  'data-context/knowledge': 'sources',
  'work-communications/work': 'work',
  'automation/workflows': 'work',
  'automation/events': 'work',
  'security-identity/access': 'people',
  'security-identity/policy': 'rules',
  'security-identity/defense': 'security',
  'deployment/deployments': 'operations',
  'observation/operations': 'operations',
  'observation/cost-adoption': 'usage',
  'observation/audit-recordings': 'activity',
  'system/administration': 'people',
  'system/maintenance': 'operations',
  'system/development': 'mcp-skills',
  'system/preferences': 'settings',
}

export function placeById(id: PlaceId): Place {
  return ALL_PLACES.find((p) => p.id === id)!
}

const named = (view: FeatureView) =>
  ALL_PLACES.find((p) => p.views.includes(view.id))

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
    ...place.views.flatMap((id) => viewById(id) ?? []),
    ...FEATURE_VIEWS.filter((v) => !named(v) && placeOfView(v) === place.id),
  ]
}

export interface PageLink {
  id: string
  path: string
}

/**
 * The pages this principal may open in a place, in its order: the row above a page. Settings
 * is led by the Settings utility. A detail view (a recording's viewer) and a view hidden from
 * navigation are never listed; a door is listed only where its room is not (isListed).
 */
export function placePages(id: PlaceId, gate: ViewGate): PageLink[] {
  const pages = placeViews(placeById(id))
    .filter(
      (v) =>
        !v.hideInNav && v.navigation.kind === 'feature' && isListed(v, gate),
    )
    .map((v) => ({ id: v.id, path: v.path }))
  return id === 'settings'
    ? [{ id: SETTINGS_UTILITY.id, path: SETTINGS_UTILITY.path }, ...pages]
    : pages
}

export interface SidebarPlace {
  id: PlaceId
  icon: LucideIcon
  path: string
  search?: Readonly<Record<string, string>>
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
  if (place.settingsSection)
    return at(SETTINGS_UTILITY.path, { section: place.settingsSection })
  const lead = placeViews(place).find(
    (v) =>
      !v.hideInNav &&
      (v.navigation.kind === 'root' ||
        (v.navigation.kind === 'feature' && isListed(v, gate))),
  )
  if (!lead) return null
  const section = place.section
  return section && section.view === lead.id && gate(lead, section)
    ? at(lead.path, section.search)
    : at(lead.path)
}

/** The sidebar for this principal: each group with the places it may open, in order. */
export function sidebarGroups(
  gate: ViewGate,
): { id: PlaceGroupId; places: SidebarPlace[] }[] {
  return PLACE_GROUPS.map((g) => ({
    id: g.id,
    places: g.places.flatMap((p) => placeLink(p, gate) ?? []),
  })).filter((g) => g.places.length > 0)
}

/** The Settings link at the sidebar's foot. */
export function settingsPlace(): SidebarPlace {
  return placeLink(SETTINGS_PLACE, () => true)!
}

/** The place an address belongs to, or null (an unknown path). */
export function currentPlace(
  pathname: string,
  search: Readonly<Record<string, unknown>>,
): PlaceId | null {
  const location = resolveLocation(pathname)
  switch (location.kind) {
    case 'home':
      return 'now'
    case 'settings':
      return (
        ALL_PLACES.find(
          (p) => p.settingsSection && p.settingsSection === search.section,
        )?.id ?? 'settings'
      )
    case 'view':
      return placeOfView(location.parent ?? location.view)
    default:
      return null
  }
}

// --- the palette's index ------------------------------------------------------------

/** `Group › Place`: where the left navigation holds a page. Settings has no group. */
function placeContext(t: TFunction, place: PlaceId): string {
  const group = PLACE_GROUPS.find((g) => g.places.some((p) => p.id === place))
  const label = t(`nav:places.${place}`)
  return group ? `${t(`nav:groups.${group.id}`)} › ${label}` : label
}

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
 * holds it and the thirteen nouns. Each result says `Group › Place`, the words of the
 * sidebar; the retired area directories are no destination here, and their names stay
 * search words only, so "Oversight" still finds what it held. Authorization is NOT applied
 * here (`authorizedEntries` does), so one index serves every principal and the ranking
 * never depends on who is asking.
 */
export function buildNavSearchIndex(t: TFunction): NavSearchEntry[] {
  const entries: NavSearchEntry[] = []
  let order = 0
  for (const v of FEATURE_VIEWS) {
    if (v.hideInNav || v.navigation.kind === 'detail') continue
    const label = viewLabel(t, v.id)
    const description = viewDescription(t, v.id)
    const place = placeOfView(v)
    const context = placeContext(t, place)
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
        aliases: aliasesFor(t, v.id).map(fold),
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
