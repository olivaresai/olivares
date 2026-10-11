// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE PROJECTION OF "MAY I OPEN THIS VIEW", FOR EVERY CONSUMER THAT ASKS.
//
// The sidebar, the ⌘K palette, the `g`-shortcuts, the nine area directories and the route
// gate all answered that question with `can(view.permission)` — the whoami reflection.
// That predicate is exactly right for the views whose authority IS a tenant-wide
// permission, and it cannot express the one this lot migrates: `sessions:channel:admin`
// may be held through a workspace-scoped authored grant the permission set never names,
// or held in the set and forbidden by policy. Both directions are real, and a boolean
// mirror gets both wrong.
//
// So the shape changes and the answer does not: every consumer keeps receiving one
// predicate over a `FeatureView`. For an ordinary view it still delegates to
// `can(view.permission)`. For a view that DECLARES a capability question it asks the
// engine instead, and the answer has three values rather than two.
//
// ⛔ UNKNOWN IS NOT AUTHORITY, AND IT IS NOT A REFUSAL EITHER.
//    · An INSTALLED static link may stay visible while the answer is unknown — the same
//      way in the sidebar, the palette and the directory, so the three cannot disagree
//      about whether a module exists. What must not happen is the protected CHILD
//      loading: navigation is a table of contents, and a link is not a permit.
//    · An ESTABLISHED `not_reachable` removes the view from all three.
//    · Unknown NEVER becomes Forbidden. A route that cannot establish its answer says so.
//
// ⛔ AND SURFACE AUTHORITY IS NOT ENTITY AUTHORITY. A route carrying a valid entity deep
//    link asks its OWN exact operation question and may render that entity even while the
//    collection is `not_reachable` — without fetching the collection. That is not a
//    loophole: a principal admitted to one row and not to the list is the ordinary case
//    of a scoped grant, and making the deep link wait for the collection would invent a
//    prerequisite the engine does not have.
import { useMemo } from 'react'
import { adminChannelFromSearch } from '@/features/communications/capabilities'
import { FEATURE_VIEWS, type FeatureView } from '@/features/registry'
import {
  useCapability,
  type CapabilityAccess,
  type CapabilityObservation,
  type NormalizedCapabilityQuestion,
} from '@/lib/auth/capabilities'
import { useAuth } from '@/lib/auth/context'
import { isGlobalAccount } from '@/lib/auth/rbac'
import type { ViewGate } from '@/features/navigation/model'
import { moduleEnabled, useModulesStore } from '@/stores/modules'
import { useWorkspaceStore } from '@/stores/workspace'

/**
 * What this principal may do with a view RIGHT NOW.
 *
 *  · `allowed`  — established: the view opens and its protected child may load.
 *  · `denied`   — established refusal: it disappears from navigation and the route refuses.
 *  · `unknown`  — not established: an installed link may remain, nothing protected loads,
 *                 and it is never rendered as a refusal.
 */
export type ViewAccessState = 'allowed' | 'denied' | 'unknown'

export interface ViewAccess {
  /** The established answer for one view. */
  state: (view: FeatureView) => ViewAccessState
  /**
   * Whether the view may be offered and read RIGHT NOW: not refused, and its module runs. A
   * page links to another (the composer's approval, an entity's next room), the session rail
   * reads through this, and so do favorites and recents. Navigation LISTS through `listed`.
   */
  navigable: ViewGate
  /**
   * Whether navigation lists the view: every view the person may open, from the first
   * sign-in, whether or not its module runs here. This is the single rule the sidebar,
   * palette, shortcuts, All areas and the phone bar share, which is what stops them
   * disagreeing. Only an established refusal hides a view; an off module is listed and
   * flagged by `isOff`. A view it does not list keeps its route.
   */
  listed: ViewGate
  /**
   * Whether the view's module is off on this installation (or the section's, when one is
   * given). Navigation draws such an entry dimmed with an "Off" tag; its page offers to
   * turn the module on. Meaningful only for a view that is `listed`.
   */
  isOff: ViewGate
}

/**
 * The views that declare a capability question. Read once at module scope: the hook below
 * probes them in a FIXED order, so the number of hooks it runs cannot depend on data.
 * This lot declares exactly one; a second is added as its own line, never as a loop.
 */
const CAPABILITY_VIEWS = FEATURE_VIEWS.filter((v) => v.capability !== undefined)

/** The single capability view of this lot, or undefined in a tree that has none. */
const ADMINISTRATION_VIEW = CAPABILITY_VIEWS[0]

function stateOfObservation(access: CapabilityAccess): ViewAccessState {
  switch (access) {
    case 'reachable':
    case 'allowed':
      return 'allowed'
    case 'not_reachable':
    case 'denied':
      // An ESTABLISHED refusal from the engine. `undisclosed` is deliberately NOT here:
      // a non-verdict asserts no denial, so treating it as one would publish the very
      // distinction the concealment exists to withhold.
      return 'denied'
    default:
      return 'unknown'
  }
}

/**
 * The shared projection. Every consumer calls this and nothing else; the capability probe
 * runs under ONE TanStack key, so five consumers on a page share one observation and one
 * request rather than five.
 */
export function useViewAccess(): ViewAccess {
  const { can, principal, activeTenant } = useAuth()
  const modulesOff = useModulesStore((s) => s.off)
  const workspace = useWorkspaceStore((s) => s.activeWorkspace)
  // The declared surface question of the one capability view, or null when this tree has
  // none and when no workspace is selected (nothing to ask about is not a permission).
  const declared = ADMINISTRATION_VIEW?.capability?.surface(workspace) ?? null
  // The SUBMITTED question is the declared one unless this is the known unsupported
  // family, which is not asked at all. Navigation keeps its UNKNOWN either way: a
  // question that is not submitted has no answer, and no answer is not a refusal, so the
  // installed link stays exactly as visible as it was.
  const observation = useCapability(
    isGlobalAccount(principal, activeTenant) ? null : declared,
  )
  return useMemo<ViewAccess>(() => {
    const state = (view: FeatureView): ViewAccessState => {
      if (view.capability !== undefined) {
        if (view.id !== ADMINISTRATION_VIEW?.id) return 'unknown'
        return stateOfObservation(observation.access)
      }
      // ⛔ THE ONE `can()` OVER THE WHOLE REGISTRY. Every consumer's own
      //    `can(view.permission)` moved here, so the reflection is evaluated once and
      //    identically. `view.permission` still resolves to the registry's literals, which
      //    is what `scripts/check-console-perms.mjs` reads to prove the console never asks
      //    for a permission the engine does not declare.
      return !view.permission || can(view.permission) ? 'allowed' : 'denied'
    }
    // A view whose engine module is not enabled here is not offered at all (ARCH C1); its
    // route says so plainly (RequirePermission).
    // A section is asked with its own literal permission, never as a copy of its view: a
    // copy hides the view's permission and capability from the census. Its module is
    // that permission's; the view's own module is already asked above.
    const navigable: ViewGate = (view, section) =>
      state(view) !== 'denied' &&
      moduleEnabled(modulesOff, view.permission, view.id) &&
      (!section ||
        (can(section.requires) && moduleEnabled(modulesOff, section.requires)))
    // Navigation lists everything not refused (a person never sees a door they may not
    // open) and flags what is off. A section is asked with its own permission.
    const listed: ViewGate = (view, section) =>
      state(view) !== 'denied' && (!section || can(section.requires))
    const isOff: ViewGate = (view, section) =>
      !moduleEnabled(modulesOff, view.permission, view.id) ||
      (!!section && !moduleEnabled(modulesOff, section.requires))
    return { state, navigable, listed, isOff }
  }, [can, observation.access, modulesOff])
}

/* ── the route gate's own answer ──────────────────────────────────────────────── */

/**
 * What a ROUTE renders. It is finer than `ViewAccessState` because a route can say things
 * navigation cannot: that it is still checking, and that the engine declined to disclose.
 *
 * ⛔ `observed` AND `question` DECIDE NOTHING. They are neutral lifecycle metadata: the
 *    exact answer this decision was taken from, and the normalized question it was taken
 *    about. `kind` is unchanged and remains the only thing that gates a route.
 *
 *    They exist because `kind` is deliberately COARSER than the answer, and one consumer
 *    needs the difference: `unavailable` is reached by a local `unknown` — an expiry, a
 *    movement, a transport that did not answer, none of them a statement about this
 *    operator — and equally by `step_up_required`, which is a gate the engine decided
 *    before any lookup. A surface that keeps the operator's own unsent work across the
 *    first must not keep it across the second, and from `kind` alone it cannot tell.
 *
 *    Nothing here is a permit and nothing here is secret: `question` is the same
 *    normalized question the observation was made with, and `observed` is one of eight
 *    published states.
 */
export interface RouteAccess {
  readonly kind:
    'permitted' | 'checking' | 'forbidden' | 'undisclosed' | 'unavailable'
  /** The exact answer `kind` was derived from, or null for a reflection-only route. */
  readonly observed: CapabilityAccess | null
  /** The question that answer is about, or null when nothing was asked. */
  readonly question: NormalizedCapabilityQuestion | null
  /**
   * Whether this decision is the known unsupported global-account family: a declared
   * question that was deliberately NOT submitted. Like `observed` and `question` it is
   * neutral metadata and gates nothing — `kind` is still the only thing that gates a
   * route, and for this family it is `unavailable`, never `permitted` and never
   * `forbidden`. It exists so the route can say WHICH account to sign in with instead of
   * announcing a retry that is never going to be made.
   */
  readonly globalAccount: boolean
}

function routeOf(observation: CapabilityObservation): RouteAccess {
  const of = (kind: RouteAccess['kind']): RouteAccess => ({
    kind,
    observed: observation.access,
    question: observation.question,
    globalAccount: false,
  })
  switch (observation.access) {
    case 'allowed':
    case 'reachable':
      return of('permitted')
    case 'denied':
    case 'not_reachable':
      return of('forbidden')
    case 'undisclosed':
      return of('undisclosed')
    case 'checking':
      return of('checking')
    default:
      // Local stale, transport, malformed, a target-free step-up gate: retryable and
      // neutral. Never Forbidden — the console did not establish a refusal.
      return of('unavailable')
  }
}

/** The reflection's own answer, which no capability question stands behind. */
const REFLECTED = (kind: RouteAccess['kind']): RouteAccess => ({
  kind,
  observed: null,
  question: null,
  globalAccount: false,
})

/**
 * A DECLARED question that was not submitted because the caller is the known unsupported
 * global account. It is `unavailable`, so the protected child never mounts, and it
 * carries the console's own `unknown`: nothing was asked, so nothing was established.
 */
const SUPPRESSED: RouteAccess = {
  kind: 'unavailable',
  observed: 'unknown',
  question: null,
  globalAccount: true,
}

/**
 * The route decision for one view at one location.
 *
 * BOTH questions are probed on every render — with a null question when they do not apply
 * — so the hook count never depends on the url. The DEEP LINK wins when it is present and
 * valid: its exact entity operation is an independent decision, and a `not_reachable`
 * collection is not evidence about a row.
 */
export function useRouteAccess(view: FeatureView, search: string): RouteAccess {
  const { can, principal, activeTenant } = useAuth()
  const workspace = useWorkspaceStore((s) => s.activeWorkspace)
  const capability = view.capability
  const suppressed = isGlobalAccount(principal, activeTenant)
  const deepLinkQuestion = useMemo(
    () => capability?.deepLink?.(workspace, search) ?? null,
    [capability, workspace, search],
  )
  const surfaceQuestion = useMemo(
    () => (deepLinkQuestion ? null : (capability?.surface(workspace) ?? null)),
    [capability, workspace, deepLinkQuestion],
  )
  // ⛔ DECLARED ABOVE, SUBMITTED HERE, AND THE TWO ARE NOT THE SAME FACT. Both questions
  //    are still probed on every render, so the hook count does not depend on the url or
  //    on the account; what changes for the known unsupported family is that the
  //    submitted question is null, so no request is made and no cadence is started.
  const deepLink = useCapability(suppressed ? null : deepLinkQuestion)
  const surface = useCapability(suppressed ? null : surfaceQuestion)
  if (!capability)
    return !view.permission || can(view.permission)
      ? REFLECTED('permitted')
      : REFLECTED('forbidden')
  // ⛔ NOTHING TO ASK IS NOT THE SAME AS NO ANSWER YET, and conflating them costs the
  //    operator the page they are standing on. A capability view with no question — this
  //    one, whenever no workspace is selected — has no protected child to gate: the
  //    question that would authorize a collection does not exist, so the collection cannot
  //    be requested, and each surface inside the room ALSO gates itself on its own current
  //    observation. Refusing the route here would replace "Select a workspace" with
  //    "access could not be established", which is both false and a dead end.
  //
  //    An `unknown` from a question that WAS asked is different and is refused below: there
  //    the child could load, and the only thing stopping it would be this gate.
  //
  // ⛔ AND IT IS JUDGED ON THE DECLARED QUESTIONS, NEVER ON THE SUBMITTED ONES. A
  //    suppressed question is still DECLARED: the collection it would authorize exists
  //    and the protected child is one render away, so reading the suppression as "nothing
  //    to ask" would turn a question nobody answered into a permit. That is the exact
  //    hazard this branch is ordered against, and it is why the family check comes after.
  if (!deepLinkQuestion && !surfaceQuestion) return REFLECTED('permitted')
  if (suppressed) return SUPPRESSED
  return routeOf(deepLinkQuestion ? deepLink : surface)
}

/**
 * The valid entity deep link of a location, or null. Exported so the view can gate its own
 * collection on the SAME reading the route gated on — two readings of one url is how a
 * route opens a sheet while the screen behind it still fetches the list.
 */
export function administrationDeepLink(search: string): string | null {
  return adminChannelFromSearch(search)
}
