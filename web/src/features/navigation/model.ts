// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE NAVIGATION MODEL (N1) — every consumer of the nine-area structure resolves through here.
//
// Sidebar, mobile drawer, breadcrumbs, command palette, the nine directory pages and the
// sidebar filter all need the same four answers: where does a path sit, which entries of an
// area may THIS principal see, what is the trail above the current page, and which entries
// match a query. Answering them in four places is how a re-grouping buries a screen: one
// consumer keeps an older list and the operator finds the module in the palette but not in
// the sidebar. So the answers are computed once, from the single FEATURE_VIEWS registry and
// NAV_AREAS, and every consumer derives.
//
// What this module deliberately does NOT do:
//   - decide access. `isAuthorized` APPLIES the gate its caller handed in — the effective
//     set the ENGINE handed whoami, or, for a view that declares one, the registered
//     capability question the engine answered — and nothing here widens or narrows it. An
//     area is visible when ANY of its leaves is; there is no parent permission and no
//     "admin of the area", and an area never becomes visible because a leaf's answer is
//     merely unknown rather than refused: that judgement belongs to the gate, once.
//   - read the network. No counts, no readiness, no availability: a directory is links.
//   - move a url. Paths come from the registry; the census pins them.
import type { LucideIcon } from 'lucide-react'
import type { TFunction } from 'i18next'
import {
  FEATURE_VIEWS,
  NAV_AREAS,
  nounsForView,
  viewsByArea,
  type AreaId,
  type FeatureView,
  type NavArea,
} from '@/features/registry'
import { Settings } from 'lucide-react'

/**
 * The pinned Settings utility (app/routes.tsx settingsRoute) is not a FEATURE_VIEWS entry
 * and has no permission: every signed-in principal reaches it. It belongs to System & settings
 * → Preferences in the directory and the breadcrumb, while the sidebar keeps it pinned at the
 * foot and does NOT repeat it inside the area list (root decision: its presence in the
 * directory "does not require duplicating the link in the same sidebar list").
 */
export const SETTINGS_UTILITY = {
  id: 'settings',
  path: '/settings',
  areaId: 'system' as AreaId,
  sectionId: 'preferences',
  icon: Settings as LucideIcon,
} as const

/**
 * Membership of the effective permission set — `useAuth().can`, handed in by the caller.
 * The parameter is named `permits`, not `can`, on purpose: scripts/check-console-perms.mjs
 * resolves every call spelled `can()` to the RBAC mirror and refuses one it cannot read,
 * and a parameter cannot be traced to lib/auth.
 */
export type PermissionCheck = (permission: string) => boolean

/**
 * MAY THIS PRINCIPAL OPEN THIS VIEW — the predicate every projection below now takes.
 *
 * It receives the whole `FeatureView` rather than a permission string because a view's
 * authority is not always a permission: `communicationsAdministration` is decided by a
 * registered capability question the whoami reflection cannot express (G1-B). Passing the
 * view keeps ONE predicate for every consumer while letting the answer come from wherever
 * that view's authority actually lives.
 *
 * ⛔ THE `can(v.permission)` CALLS THAT USED TO SIT IN THE CONSUMERS DID NOT DISAPPEAR.
 *    They moved, all of them, into the ONE place that builds this predicate —
 *    `features/navigation/authorization.ts`. That is deliberate: it is what keeps the
 *    sidebar, the palette, the shortcuts and the directory from drifting apart, and it is
 *    what keeps every registry permission literal resolvable by the console-permission
 *    census, which reads `can()` call sites and fails on an argument it cannot read.
 */
export type ViewGate = (view: FeatureView) => boolean

/**
 * The historical permission-only predicate, as a view gate. For a caller that genuinely
 * has nothing but a permission check — and for this module's own tests, which are about
 * the AREA algebra and not about where an answer comes from.
 *
 * ⛔ It is NOT the production path. Production builds its gate in
 *    `features/navigation/authorization.ts`, because this adapter would answer a
 *    capability-backed view from the reflection — the exact defect G1-B closes.
 */
export function permissionGate(permits: PermissionCheck): ViewGate {
  return (view) => !view.permission || permits(view.permission)
}

export function areaById(id: string): NavArea | undefined {
  return NAV_AREAS.find((a) => a.id === id)
}

export function viewById(id: string): FeatureView | undefined {
  return FEATURE_VIEWS.find((v) => v.id === id)
}

/** The area a view belongs to; `null` for the `/` root. */
export function areaOfView(view: FeatureView): NavArea | null {
  return view.navigation.kind === 'root'
    ? null
    : (areaById(view.navigation.areaId) ?? null)
}

/** A view the principal may open — the same rule the route gate enforces. */
export function isAuthorized(view: FeatureView, gate: ViewGate): boolean {
  return gate(view)
}

export interface AreaSection {
  sectionId: string
  views: FeatureView[]
}

/**
 * The leaves of an area THIS principal may open, by section. Hidden (deep-link-only) views
 * are never listed: they are reached from their parent, not from a directory.
 */
export function authorizedSections(
  areaId: AreaId,
  gate: ViewGate,
): AreaSection[] {
  return viewsByArea(areaId)
    .map((s) => ({
      sectionId: s.sectionId,
      views: s.views.filter((v) => isAuthorized(v, gate)),
    }))
    .filter((s) => s.views.length > 0)
}

/** OR of the area's authorized leaves — the only rule that makes an area visible. */
export function isAreaVisible(areaId: AreaId, gate: ViewGate): boolean {
  return authorizedSections(areaId, gate).length > 0
}

/** The areas the sidebar and the palette offer, in the ratified order. */
export function visibleAreas(gate: ViewGate): NavArea[] {
  return NAV_AREAS.filter((a) => isAreaVisible(a.id, gate))
}

// --- where am I -----------------------------------------------------------------------

/** Registry paths may carry a dynamic segment (`/session-viewer/$id`); match the static
 * prefix up to the first param so a resolved detail url names its view. */
function matchBase(path: string): string {
  const paramAt = path.indexOf('/$')
  return paramAt === -1 ? path : path.slice(0, paramAt)
}

export type NavLocation =
  | { kind: 'home'; view: FeatureView }
  | { kind: 'area'; area: NavArea }
  | {
      kind: 'view'
      view: FeatureView
      area: NavArea | null
      /** The parent of a deep-link-only detail (the recording behind a viewer). */
      parent: FeatureView | null
    }
  | { kind: 'settings' }
  | { kind: 'unknown' }

/**
 * Resolve a pathname against the registry and the area model. Longest registry prefix wins
 * (`/model-operations` is never shadowed by `/models`); an area directory matches its exact
 * path only, because nothing is mounted beneath it.
 */
export function resolveLocation(pathname: string): NavLocation {
  if (pathname === SETTINGS_UTILITY.path) return { kind: 'settings' }
  const area = NAV_AREAS.find((a) => a.path === pathname)
  if (area) return { kind: 'area', area }
  let best: { view: FeatureView; len: number } | null = null
  for (const v of FEATURE_VIEWS) {
    if (v.path === '/') continue
    const base = matchBase(v.path)
    if (pathname === base || pathname.startsWith(`${base}/`)) {
      if (!best || base.length > best.len) best = { view: v, len: base.length }
    }
  }
  if (best) {
    const parent =
      best.view.navigation.kind === 'detail'
        ? (viewById(best.view.navigation.parentViewId) ?? null)
        : null
    return {
      kind: 'view',
      view: best.view,
      area: areaOfView(best.view),
      parent,
    }
  }
  if (pathname === '/') {
    const home = FEATURE_VIEWS.find((v) => v.path === '/')
    if (home) return { kind: 'home', view: home }
  }
  return { kind: 'unknown' }
}

/** The registry id the breadcrumb names for a pathname (`settings` for the utility), or null. */
export function currentViewId(pathname: string): string | null {
  const loc = resolveLocation(pathname)
  switch (loc.kind) {
    case 'home':
      return loc.view.id
    case 'view':
      return loc.view.id
    case 'settings':
      return SETTINGS_UTILITY.id
    default:
      return null
  }
}

/** The area an active location belongs to — what the sidebar opens on arrival. */
export function activeAreaId(loc: NavLocation): AreaId | null {
  switch (loc.kind) {
    case 'area':
      return loc.area.id
    case 'view':
      return loc.area?.id ?? null
    case 'settings':
      return SETTINGS_UTILITY.areaId
    default:
      return null
  }
}

// --- labels ---------------------------------------------------------------------------

/** The console's own label for a view (nav:items.<id>) — the sidebar, palette and breadcrumb
 * all use this one key, which registry.nav-labels.test.ts pins in every language. */
export function viewLabel(t: TFunction, viewId: string): string {
  return t(`nav:items.${viewId}`)
}

export function areaLabel(t: TFunction, areaId: AreaId): string {
  return t(`nav:areas.${areaId}.label`)
}

/** The task question the directory opens with ("What sessions, profiles and models do I…"). */
export function areaQuestion(t: TFunction, areaId: AreaId): string {
  return t(`nav:areas.${areaId}.question`)
}

export function sectionLabel(
  t: TFunction,
  areaId: AreaId,
  sectionId: string,
): string {
  return t(`nav:areas.${areaId}.sections.${sectionId}`)
}

/** A missing description degrades to no subtitle, never to a raw key. */
export function viewDescription(t: TFunction, viewId: string): string {
  return t(`nav:descriptions.${viewId}`, { defaultValue: '' })
}

// --- breadcrumbs ----------------------------------------------------------------------

export interface Crumb {
  label: string
  /** Absent on the current page (the last crumb), present on every ancestor. */
  to?: string
}

/**
 * The trail above a location, resolved from the model — never by splitting the url.
 *
 *   /               → Overview
 *   /areas/ai       → Overview › AI                        (the area IS the location)
 *   /agentops       → AI › Operate sessions
 *   /session-viewer/x → Observability & evidence › Recordings › Session viewer
 *   /settings       → System & settings › Settings
 *
 * Sections are grouping labels, not pages, so they never appear as crumbs. Ancestors link,
 * the current page does not. "Overview › Overview" is never produced.
 */
export function breadcrumbTrail(t: TFunction, loc: NavLocation): Crumb[] {
  switch (loc.kind) {
    case 'home':
      return [{ label: viewLabel(t, loc.view.id) }]
    case 'area':
      return [
        { label: viewLabel(t, 'home'), to: '/' },
        { label: areaLabel(t, loc.area.id) },
      ]
    case 'view': {
      const trail: Crumb[] = []
      if (loc.area)
        trail.push({ label: areaLabel(t, loc.area.id), to: loc.area.path })
      if (loc.parent)
        trail.push({ label: viewLabel(t, loc.parent.id), to: loc.parent.path })
      trail.push({ label: viewLabel(t, loc.view.id) })
      return trail
    }
    case 'settings':
      return [
        {
          label: areaLabel(t, SETTINGS_UTILITY.areaId),
          to: areaById(SETTINGS_UTILITY.areaId)?.path,
        },
        { label: viewLabel(t, SETTINGS_UTILITY.id) },
      ]
    default:
      return []
  }
}

// --- search ---------------------------------------------------------------------------

/**
 * Fold case and strip accents so a Spanish operator typing "sesiones" matches "Sesión"
 * and a German one typing "prufung" matches "Prüfung". Without this the filter is a
 * trap in the Latin-script languages: it looks like it works until the word carries a
 * diacritic, and then it silently reports nothing.
 *
 * ⚠ THE STRIP IS SCOPED TO LATIN BASE LETTERS ON PURPOSE. A blanket
 * `.replace(/\p{Diacritic}/gu, '')` — which this had, until the adversarial contrast
 * measured it — is not accent-insensitivity outside Latin, it is corruption: NFD turns
 * Russian "й" into "и" + a combining breve and Japanese "が" into "か" + the combining
 * voiced mark, and stripping those changes the letter. "Задачи" would stop matching
 * itself. Recomposing with NFC afterwards leaves every non-Latin word exactly as typed.
 */
export function fold(s: string): string {
  return s
    .normalize('NFD')
    .replace(/([A-Za-z])[\u0300-\u036f]+/g, '$1')
    .normalize('NFC')
    .toLowerCase()
}

export type NavSearchKind = 'area' | 'view' | 'settings'

export interface NavSearchEntry {
  kind: NavSearchKind
  /** Registry id, area id, or `settings`. Unique together with `kind`. */
  id: string
  path: string
  icon: LucideIcon
  label: string
  /** `Area › Section` in the current language — shown beside a result to disambiguate. */
  context: string
  description: string
  areaId: AreaId | null
  sectionId: string | null
  /** Registry position: the stable tie-break, never activity or recency. */
  order: number
  /** Folded haystacks, one per field so the ranker can tell a label hit from a description hit. */
  haystack: {
    label: string
    /** English label plus explicit former names (nav:aliases.<id>) in both languages. */
    aliases: string[]
    path: string
    context: string[]
    description: string
  }
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
 * The common projection the sidebar filter and the ⌘K palette search over: every area,
 * every non-hidden view and the Settings utility, with the current label, the English
 * fallback label, explicit former names, path, area/section, the thirteen nouns and the
 * five historical hub words. Authorization is NOT applied here — callers filter with `can`
 * — so one index serves every principal and the ranking never depends on who is asking.
 */
export function buildNavSearchIndex(t: TFunction): NavSearchEntry[] {
  const entries: NavSearchEntry[] = []
  let order = 0
  for (const area of NAV_AREAS) {
    const label = areaLabel(t, area.id)
    const question = areaQuestion(t, area.id)
    entries.push({
      kind: 'area',
      id: area.id,
      path: area.path,
      icon: area.icon,
      label,
      context: '',
      description: question,
      areaId: area.id,
      sectionId: null,
      order: order++,
      haystack: {
        label: fold(label),
        aliases: [fold(String(t(`nav:areas.${area.id}.label`, { lng: 'en' })))],
        path: fold(area.path),
        context: area.sections.map((s) => fold(sectionLabel(t, area.id, s))),
        description: fold(question),
      },
    })
  }
  for (const v of FEATURE_VIEWS) {
    if (v.hideInNav || v.navigation.kind === 'detail') continue
    const label = viewLabel(t, v.id)
    const description = viewDescription(t, v.id)
    const area = areaOfView(v)
    const sectionId =
      v.navigation.kind === 'feature' ? v.navigation.sectionId : null
    const contextParts: string[] = []
    if (area) contextParts.push(areaLabel(t, area.id))
    if (area && sectionId)
      contextParts.push(sectionLabel(t, area.id, sectionId))
    entries.push({
      kind: 'view',
      id: v.id,
      path: v.path,
      icon: v.icon,
      label,
      context: contextParts.join(' › '),
      description,
      areaId: area?.id ?? null,
      sectionId,
      order: order++,
      haystack: {
        label: fold(label),
        aliases: aliasesFor(t, v.id).map(fold),
        path: fold(v.path),
        context: [
          ...contextParts.map(fold),
          fold(String(t(`nav:hubs.${v.hub}`))),
          ...nounsForView(v.id).map((n) => fold(String(t(`nav:nouns.${n}`)))),
        ],
        description: fold(description),
      },
    })
  }
  const settingsLabel = viewLabel(t, SETTINGS_UTILITY.id)
  const settingsArea = areaLabel(t, SETTINGS_UTILITY.areaId)
  const settingsSection = sectionLabel(
    t,
    SETTINGS_UTILITY.areaId,
    SETTINGS_UTILITY.sectionId,
  )
  entries.push({
    kind: 'settings',
    id: SETTINGS_UTILITY.id,
    path: SETTINGS_UTILITY.path,
    icon: SETTINGS_UTILITY.icon,
    label: settingsLabel,
    context: `${settingsArea} › ${settingsSection}`,
    description: viewDescription(t, SETTINGS_UTILITY.id),
    areaId: SETTINGS_UTILITY.areaId,
    sectionId: SETTINGS_UTILITY.sectionId,
    order,
    haystack: {
      label: fold(settingsLabel),
      aliases: aliasesFor(t, SETTINGS_UTILITY.id).map(fold),
      path: fold(SETTINGS_UTILITY.path),
      context: [fold(settingsArea), fold(settingsSection)],
      description: fold(viewDescription(t, SETTINGS_UTILITY.id)),
    },
  })
  return entries
}

/**
 * The index narrowed to what THIS principal may open: a view by its own permission, an area
 * by the union of its leaves, the Settings utility always. The sidebar filter and the ⌘K
 * palette both project through here and then rank through `rankNavMatches`, so the two
 * surfaces can never disagree on membership or on order (independent review F1).
 */
export function authorizedEntries(
  entries: readonly NavSearchEntry[],
  gate: ViewGate,
): NavSearchEntry[] {
  return entries.filter((e) => {
    if (e.kind === 'area') return isAreaVisible(e.id as AreaId, gate)
    if (e.kind === 'view') {
      const view = viewById(e.id)
      return !!view && isAuthorized(view, gate)
    }
    return true
  })
}

/** Word starts: a query of "ses" matches "Observe sessions" at the second word. */
function wordPrefix(hay: string, needle: string): boolean {
  if (!hay) return false
  return hay.split(/[\s/&·›()-]+/).some((w) => w.startsWith(needle))
}

/**
 * Score one entry against one folded needle. Exact label, label prefix and label words come
 * first; former names, English labels and paths next; the area/section/noun vocabulary after
 * that; a description hit last. A number, so the palette and the sidebar agree.
 */
function scoreField(e: NavSearchEntry, needle: string): number {
  const h = e.haystack
  if (h.label === needle) return 100
  if (h.label.startsWith(needle)) return 90
  if (wordPrefix(h.label, needle)) return 80
  if (h.aliases.some((a) => a === needle)) return 75
  if (h.aliases.some((a) => a.startsWith(needle))) return 70
  if (h.aliases.some((a) => wordPrefix(a, needle))) return 65
  if (h.path === needle || h.path === `/${needle}`) return 62
  if (h.path.includes(needle)) return 60
  if (h.label.includes(needle)) return 55
  if (h.aliases.some((a) => a.includes(needle))) return 52
  if (h.context.some((c) => wordPrefix(c, needle))) return 50
  if (h.context.some((c) => c.includes(needle))) return 45
  if (h.description.includes(needle)) return 30
  return 0
}

/**
 * Rank the index against a query. Empty query → every entry in registry order. Otherwise every
 * word of the query must match somewhere (AND), the score is the whole query's best field when
 * it matches as a phrase and the weakest word's best field otherwise, and ties keep registry
 * order — never activity, never recency, never a random reshuffle.
 */
export function rankNavMatches(
  entries: readonly NavSearchEntry[],
  query: string,
): NavSearchEntry[] {
  const needle = fold(query.trim())
  if (!needle) return [...entries]
  const words = needle.split(/\s+/).filter(Boolean)
  const scored: { e: NavSearchEntry; score: number }[] = []
  for (const e of entries) {
    let score = scoreField(e, needle)
    if (score === 0 && words.length > 1) {
      // Every word must match somewhere (AND); one miss is a miss. Measured while writing
      // the test: a `Math.max(min - 5, 1)` floor let a missed word through with score 1,
      // and "control console" listed every area behind the real hit.
      let min = Infinity
      for (const w of words) {
        const s = scoreField(e, w)
        if (s === 0) {
          min = 0
          break
        }
        min = Math.min(min, s)
      }
      // A phrase that only matches word by word ranks below the same words as a phrase.
      score = min === 0 || min === Infinity ? 0 : Math.max(min - 5, 1)
    }
    if (score > 0) scored.push({ e, score })
  }
  scored.sort((a, b) => b.score - a.score || a.e.order - b.e.order)
  return scored.map((s) => s.e)
}
