// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { LucideIcon } from 'lucide-react'
import type { ComponentType, ReactNode } from 'react'
import type {
  CapabilityAccess,
  NormalizedCapabilityQuestion,
} from '@/lib/auth/capabilities'
import {
  Bot,
  CalendarClock,
  Container,
  Database,
  MessagesSquare,
  Server,
  Settings2,
  Shield,
  Telescope,
} from 'lucide-react'
import type { RouteAlias } from './route-census'
import { FEATURE_EXTENSIONS } from './extensions'

/**
 * THE VIEW-REGISTRATION CONTRACT.
 *
 * The whole console — sidebar, command palette, and router — is generated from
 * FEATURE_VIEWS, and FEATURE_VIEWS is collected from the directories: each
 * `features/<dir>/views.tsx` exports `VIEWS`, the entries of the views whose page lives in
 * that directory (id, path, navigation, icon, permission, a `lazy()` element from the same
 * folder, and its `order`). Adding a view touches its own directory and, when a module's
 * state governs it, that module's `console_entries` row (docs/module-spec.md).
 *
 * `permission` mirrors the backend RBAC (core/auth): the nav item and route are
 * hidden/blocked unless `useAuth().can(permission)` is true. The backend remains
 * the source of truth — this only avoids offering an action that would 403.
 */
/**
 * THE THIRTEEN NOUNS — the words this console has to be searchable by.
 *
 * The product question (canon §0, and §5 of the 2026-08-11 audit that answered it) names thirteen
 * things an engineer must be able to SEE and MANAGE: sessions, agents, connections,
 * identities, models, rules, automations, groups, states, workflows, tasks, protocols,
 * infrastructure. A page's PLACE is its area and section (`navigation` below); these are
 * not a second place. They are SEARCH words: `nounsForView()` maps each noun's label onto the
 * views that manage it, so typing "sesiones" finds every session surface wherever it sits.
 * Measured when the previous navigation axis was retired: dropping these words too would have cost the palette
 * 359 of its 629 hits on them, and 32 of the 91 queries (13 words, 7 languages) would have
 * found nothing.
 */
export type NounId =
  | 'sessions'
  | 'agents'
  | 'connections'
  | 'identities'
  | 'models'
  | 'rules'
  | 'automations'
  | 'groups'
  | 'states'
  | 'workflows'
  | 'tasks'
  | 'protocols'
  | 'infrastructure'

export interface ProductNoun {
  id: NounId
  /**
   * Views that read or write this noun as a first-class object OF THEIR OWN SURFACE —
   * not every view that merely mentions it, or the filter returns everything.
   */
  views: readonly string[]
}

/** The thirteen nouns, in the order the question above lists them. */
const DECLARED_PRODUCT_NOUNS: readonly ProductNoun[] = [
  {
    id: 'sessions',
    views: [
      'sessions',
      'agentops',
      'workspace-templates',
      // AI tools: the tools a session runs (install, sign in), beside API keys.
      'agent-tools',
      'providers',
      'providerProfiles',
      'providerBindings',
      'providerAccounts',
      'voice',
      'recordings',
      'session-viewer',
      // K3 I1: the communication kernel's console — channels, direct notices and
      // the personal inbox between sessions, agents and users. Three doors, one
      // room, all under Operate, so the span of this noun does not widen.
      'communications',
      'communicationsInbox',
      'communicationsNew',
      // K3 I2: channel administration (configuration and grant history) is the
      // fourth door of the same room, still under Operate.
      'communicationsAdministration',
      // K3 I3: the personal handoff page — offers of work responsibility addressed
      // to this principal. Fifth door of the same room, still under Operate, so the
      // span of this noun does not widen.
      'communicationsHandoffs',
    ],
  },
  // The widest noun in the product. Running an agent, administering the
  // roster, containing one, proving what it shipped and adversarially testing it are
  // five errands on one word. Each entry earns its place by CRUD or control, not by
  // mentioning agents:
  //   console      — the Agents tab creates/edits/deactivates/deletes under agent:read|write
  //   killswitch   — the engine's stop scopes are literally estate and AGENT
  //                  (modules/governance/killswitch.go:63-64, re-measured 2026-08-11;
  //                  the audit cited :62-63, one line of drift)
  //   evals/redteam— both take agents as SUBJECTS you select, authorise and score
  {
    id: 'agents',
    views: [
      'agentops',
      'console',
      'agentArtifacts',
      'inventory',
      'killswitch',
      'evals',
      'redteam',
    ],
  },
  // `console` earns this one too: its Connectors and Workspace Connectors tabs are where
  // a connector is actually added and configured. `capabilities` governs MCP, skills and
  // tools — it does not replace that surface.
  {
    id: 'connections',
    views: [
      'capabilities',
      // The skills catalog: packs a session receives and where each is assigned.
      'skills',
      'mcpServers',
      'console',
      'knowledge',
      'catalog',
      'inventory',
      'platforms',
      // Git publication binds an approved credential and repository to a Git host: the
      // connection to GitHub or GitLab is the object it manages.
      'gitPublication',
    ],
  },
  {
    id: 'identities',
    views: ['identity', 'permissions', 'console', 'accessMap'],
  },
  // `finops` is here because it owns the MODEL RATE CATALOG, not merely because it
  // reports on models (features/finops/api.ts model-rates CRUD); `evals` because it
  // groups and scores by model.
  {
    id: 'models',
    views: ['models', 'modelOps', 'platforms', 'rateLimits', 'finops', 'evals'],
  },
  {
    id: 'rules',
    views: [
      'permissions',
      'claudePolicy',
      'routinePolicies',
      'storedBudgets',
      // Exporting policy OUT to a remote AgentCore engine is still policy: it sits with
      // claudePolicy and routinePolicies rather than in UNNAMED, because the ratchet's own
      // note prefers giving a view back a noun to declaring it unfindable. Placed by the
      // maintainer when #694 landed without either census entry; a later console pass may move it.
      'agentcoreExport',
      'inferenceProxy',
      'accessMap',
    ],
  },
  {
    id: 'automations',
    views: ['automations', 'eventing', 'alerting'],
  },
  {
    id: 'groups',
    views: ['console', 'permissions', 'workspaceDashboard'],
  },
  {
    id: 'states',
    views: ['home', 'health', 'observability', 'logs', 'workspaceDashboard'],
  },
  { id: 'workflows', views: ['orchestration', 'automations'] },
  // Landed `/work`, the K1 cockpit — this noun was "AUSENTE COMO CONCEPTO" in the
  // Audit and is no longer: re-measured 2026-08-11, web/src/features/work/ exists.
  { id: 'tasks', views: ['work', 'sandbox', 'orchestration'] },
  {
    id: 'protocols',
    views: [
      'capabilities',
      'mcpServers',
      'protocolBindings',
      'observability',
      'apiPlayground',
    ],
  },
  // "Infrastructure" means the CLIENT's estate
  // (deploy, platforms), OUR plane's own runtime (backups), and where bytes may sit
  // (residency, inference proxy) — three errands the word does not distinguish.
  {
    id: 'infrastructure',
    views: [
      'deploy',
      'platforms',
      'backups',
      'residency',
      'inferenceProxy',
      // C07-02: el ciclo de vida de tenant es la MISMA faena que residency y backups —operar el
      // despliegue— y no `identities`, que trata de quién puede hacer qué DENTRO de un tenant. El
      // sustantivo ya abarca `operate`, así que esto no ensancha el span.
      'tenants',
      'estate',
    ],
  },
]

/** The nouns a given view manages — the search index's second axis. */
export function nounsForView(viewId: string): NounId[] {
  return PRODUCT_NOUNS.filter((n) => n.views.includes(viewId)).map((n) => n.id)
}

/**
 * THE NINE AREAS (N1, 2026-09-06) — the navigational structure the console is BROWSED by.
 *
 * Every published route keeps its path, permission, component, actions,
 * shortcuts, docs link, nouns and Saved Views namespace, and is additionally placed in exactly
 * ONE area and ONE section of it. The areas are a structure for finding things, never a
 * capability ceiling: a future capability adds a section or an area when a journey justifies
 * it, and nothing here decides what a principal may do — `permission` on the leaf still does.
 *
 * Each area also mounts a DIRECTORY page (`path`, under the authenticated shell) that lists the
 * area's authorized entries with the console's own labels and descriptions. A directory is
 * visible when ANY of its leaves is authorized (the union), so no parent-level permission was
 * invented for it. It is a page of links, not an operational dashboard: it requests no counts,
 * no availability and no readiness, because no aggregate contract exists for those yet.
 *
 * This is the console's one navigation structure. The sidebar pins pages of it, the row above a
 * page lists its section, the trail and the palette name its area, and the guide's console
 * reference is grouped by it. `PRODUCT_NOUNS` above are search words, not a second place.
 */
export type AreaId =
  | 'infrastructure'
  | 'ai'
  | 'data-context'
  | 'work-communications'
  | 'automation'
  | 'security-identity'
  | 'deployment'
  | 'observation'
  | 'system'

export interface NavArea {
  /** Stable id — also the i18n key under nav:areas.<id> and the preference key. */
  id: AreaId
  /** The directory page's route; app/routes.tsx mounts one per area. */
  path: `/areas/${string}`
  icon: LucideIcon
  /** Section ids in display order; every leaf of the area names one of these. */
  sections: readonly string[]
  /** Docs page the topbar help link opens on the directory page. */
  helpHref: string
}

/** The nine areas, in the ratified order (the eight original domains' relative order kept;
 * Work & communications inserted between Data and Automation). */
export const NAV_AREAS: readonly NavArea[] = [
  {
    id: 'infrastructure',
    path: '/areas/infrastructure',
    icon: Server,
    sections: ['estate'],
    helpHref: '/reference/console',
  },
  {
    id: 'ai',
    path: '/areas/ai',
    icon: Bot,
    sections: [
      'sessions',
      'environments',
      'models',
      'execution',
      'provider-reference',
    ],
    helpHref: '/reference/console',
  },
  {
    id: 'data-context',
    path: '/areas/data-context',
    icon: Database,
    sections: ['capabilities', 'knowledge'],
    helpHref: '/reference/console',
  },
  {
    id: 'work-communications',
    path: '/areas/work-communications',
    icon: MessagesSquare,
    sections: ['work'],
    helpHref: '/reference/console',
  },
  {
    id: 'automation',
    path: '/areas/automation',
    icon: CalendarClock,
    sections: ['workflows', 'events'],
    helpHref: '/reference/console',
  },
  {
    id: 'security-identity',
    path: '/areas/security-identity',
    icon: Shield,
    sections: ['access', 'policy', 'defense'],
    helpHref: '/reference/console',
  },
  {
    id: 'deployment',
    path: '/areas/deployment',
    icon: Container,
    sections: ['deployments'],
    helpHref: '/reference/console',
  },
  {
    id: 'observation',
    path: '/areas/observation',
    icon: Telescope,
    sections: ['operations', 'cost-adoption', 'audit-recordings'],
    helpHref: '/reference/console',
  },
  {
    id: 'system',
    path: '/areas/system',
    icon: Settings2,
    // `preferences` holds the pinned Settings utility (app/routes.tsx settingsRoute), which is
    // not a FEATURE_VIEWS entry; navigation/model.ts places it there explicitly.
    sections: ['administration', 'maintenance', 'development', 'preferences'],
    helpHref: '/reference/console',
  },
]

/**
 * ONE PALETTE VERB: its stable id and the permission the ENGINE requires for the write it
 * opens. Both fields are required — a mutation action that declares no permission would
 * fall back to the page's, which is the defect this type exists to make unrepresentable.
 *
 * `id` is unchanged and remains the i18n key segment (`nav:commandActions.<viewId>.<id>`)
 * and the value the view consumes from the pending-command store, so no translation and no
 * consumer key moves. `permission` is a registry LITERAL, which is what
 * `scripts/check-console-perms.mjs` reads to prove the console never asks for a permission
 * the engine does not declare.
 */
export interface CommandAction {
  readonly id: string
  readonly permission: string
}

/** Where a view sits in the area structure. Exactly one shape per view, typed so a new entry
 * cannot be registered without a place (the compiler refuses it), navigation/model.test.ts pins
 * each Community entry to its place and refuses a section its area does not declare. */
export type FeatureNavigation =
  /** The `/` overview: the root of the structure, not a member of any area. */
  | { readonly kind: 'root' }
  /** An ordinary leaf: listed in its area's directory, sidebar section and palette. */
  | {
      readonly kind: 'feature'
      readonly areaId: AreaId
      readonly sectionId: string
    }
  /** A deep-link-only detail reached from `parentViewId` (the recording viewer): breadcrumbs
   * resolve through the parent, and no directory, sidebar or palette lists it. */
  | {
      readonly kind: 'detail'
      readonly areaId: AreaId
      readonly sectionId: string
      readonly parentViewId: string
    }

export interface FeatureView {
  /** Stable id — also the i18n key under nav.items/nav.descriptions and the route id. */
  id: string
  /** Managed objects of an edition view, using the existing search vocabulary. */
  readonly nouns?: readonly NounId[]
  /**
   * Route path under the authenticated app shell. THE PUBLISHED CONTRACT: an operator's
   * bookmark, a runbook's deep link and a docs cross-reference are all this string.
   * Changing one is a breaking change even when the screen survives — which is why
   * every value here is pinned by route-census.json and can only be retired through a
   * declared alias that still resolves (ROUTE_ALIASES below).
   */
  path: string
  /**
   * @deprecated Ignored. The retired hub axis (Operate, Automate, Connect, Govern, Prove);
   * a page's place is `navigation`. Kept optional so an edition view that still declares it
   * compiles; remove once no extension sets it.
   */
  hub?: string
  icon: LucideIcon
  /** RBAC permission required to see/enter this view; undefined = any signed-in user. */
  permission?: string
  /** The route content — a `lazy()` view from the module's own features/ folder. */
  element: () => ReactNode
  /**
   * The frame the shell gives this view: `work` divides the viewport into panes that scroll
   * inside themselves, `document` is one scrolling column. Absent, the shell follows its own
   * list (components/layout/page-frames.tsx), which is how /sessions and /agentops are
   * framed. An edition view sets it, because its path is not in that list.
   */
  readonly frame?: 'work' | 'document'
  /** Hide from the sidebar while staying routable and RBAC-guarded — for a view
   * reached only by deep link (session-viewer sets it: its path is parameterized,
   * so navigating to the literal path would 404). Honoured by `viewsByGroup` AND
   * the ⌘K command palette — every list offering direct navigation must
   * filter it. */
  hideInNav?: boolean
  /** The registry id of the view whose screen this one mounts under its own permission
   * (agentops is a second door into Sessions). Navigation lists the door only to a
   * principal who cannot open that view, so one screen is one entry; the route stays open
   * to deep links, and the top bar reads it as that view. */
  doorTo?: string
  /** Docs-site page for this view, as a site-relative slug (`/reference/…`).
   * The topbar renders it as the contextual help link (docs.olivares.ai + slug);
   * registry-help.test.ts pins every slug to a page that actually exists. */
  helpHref: string
  /** Palette actions: quick verbs ⌘K offers for this view (e.g. "new
   * subscription"). Each one DECLARES ITS OWN MUTATION PERMISSION and is consumed by the
   * view on mount via the pending-command store; the i18n label still lives under
   * nav:commandActions.<id>.<action.id>, unchanged.
   *
   * ⛔ THE VIEW'S OWN `permission` DOES NOT AUTHORIZE THESE, and until 2026-09-11 it was
   *    the only thing that did. `commandActions` was `readonly string[]`, so a verb had
   *    nowhere to say what it needed, and the palette filtered the list by `navigable(v)`
   *    — the view's READ permission. specification04 §1 says the opposite in as many
   *    words: "An action in the palette requires its mutation permission, target, and
   *    current context; read permission for its page does not authorize it."
   *
   *    `orchestration` shows why a verb tier cannot be derived either: its view reads
   *    `orchestration:graph:read` and its action writes `orchestration:schedule:write` —
   *    a DIFFERENT RESOURCE, not a stronger verb on the same one. Hence a declared pair,
   *    with no fallback: an action with no permission is an action nobody may run. */
  commandActions?: readonly CommandAction[]
  /**
   * Saved-views namespace for this view (plan 3.7). It partitions stored
   * views SERVER-SIDE — `savedViewsApi.list(featureId)` and the (tenant,
   * feature, owner, name) unique index — and NOTHING enforces uniqueness or
   * membership: the module validates only the slug FORMAT
   * (`^[a-z0-9][a-z0-9-]{0,63}$`, consoleviews.go), and the menu groups blindly
   * by whatever value it is handed. A value reused by two views therefore mixes
   * their saved views together, which is a DATA defect and not a cosmetic one.
   *
   * It is a declared, immutable field rather than a value derived from `path`
   * or `id` at runtime: paths change with product wording, `id` is camelCase and
   * would fail the server's slug pattern outright, and `/` and `/$id` produce no
   * clean namespace at all. registry.saved-views.test.ts pins format,
   * uniqueness and the one production value that already exists in the wild.
   */
  savedViewsFeatureId?: string
  /**
   * The view's place in the nine-area structure (N1). Required: an entry with no place
   * would be reachable by url and findable by nobody. Areas are the union of their
   * authorized leaves; this field never widens or narrows `permission`.
   */
  navigation: FeatureNavigation
  /**
   * OPTIONAL, and it does NOT replace `permission` (G1-B).
   *
   * A view whose authority the whoami reflection cannot express declares the registered
   * questions the engine answers instead. `permission` stays exactly as it was: it remains
   * the reflection every unmigrated consumer reads, it remains what
   * `scripts/check-console-perms.mjs` proves the engine declares, and it is NOT deleted
   * merely because this view stopped deciding with it.
   *
   * The question BUILDERS live in the owning feature — this field points at them and
   * declares nothing about the wire itself, so there is exactly one place in the console
   * where a registered question is spelled out.
   */
  capability?: FeatureCapability
  /**
   * OPTIONAL, and it AUTHORIZES NOTHING.
   *
   * A view whose authority expires and is re-answered on a budget is torn down and rebuilt
   * at every gap in that answer — the gate mounts a protected child only for a current
   * positive, and the capability layer never replays an expired one. Measured on this
   * view: 34–46 ms of teardown every ~5 s, destroying whatever the operator had typed and
   * not yet sent.
   *
   * A view may therefore declare ONE boundary that the gate mounts in a stable position
   * AROUND ITS OWN ALREADY-DECIDED ANSWER: the protected children when they are permitted,
   * the gate's notice otherwise. The boundary never receives the protected tree without an
   * admission, cannot mount it, and cannot be handed a fabricated one — the gate still
   * computes the decision. What it may do is outlive the subtree, so the operator's unsent
   * text is not spent by a refresh.
   *
   * `admitted` and `access` are informational: the gate's decision and the exact answer it
   * came from, which is the only way to tell an interruption (`checking`, `unknown`) from
   * an answer (a refusal, a concealment, a step-up). Views that declare nothing here keep
   * their behaviour exactly.
   */
  continuity?: ComponentType<{
    admitted: boolean
    access: CapabilityAccess | null
    children: ReactNode
  }>
}

/**
 * How a view asks the engine instead of the reflection.
 *
 * `surface` decides NAVIGATION and the collection route: may this principal load this
 * view's collection in the current workspace? `deepLink`, when present, decides a route
 * whose url names ONE entity — an independent question that a `not_reachable` surface
 * neither answers nor forbids. Both return null when there is nothing to ask (no
 * workspace selected, no valid entity in the url); null is not a permission.
 */
export interface FeatureCapability {
  surface: (workspace: string | null) => NormalizedCapabilityQuestion | null
  deepLink?: (
    workspace: string | null,
    search: string,
  ) => NormalizedCapabilityQuestion | null
}

/**
 * A built-in view as its own directory declares it in `features/<dir>/views.tsx`.
 * `order` is its place in FEATURE_VIEWS (and so in every list that keeps registry order:
 * a section's leaves, the palette): ascending, unique, in steps of ten so a new view
 * slots in without renumbering. registry.entries.test.ts refuses a repeated value.
 */
export interface ViewEntry extends FeatureView {
  order: number
}

/** Every `features/<dir>/views.tsx`, so a new view is registered by its own directory
 * (and its module row, core/modulespec/modules.json `console_entries`), never here. */
const BUILTIN_VIEWS = Object.values(
  import.meta.glob<readonly ViewEntry[]>('./*/views.tsx', {
    eager: true,
    import: 'VIEWS',
  }),
)
  .flat()
  .sort((a, b) => a.order - b.order)

// Consumers share these permission-bearing records. Seal both the registry and
// each record before export so an alias cannot replace an authority declaration.
// Edition views (./extensions) follow the built-in ones.
export const FEATURE_VIEWS: readonly FeatureView[] = Object.freeze(
  [...BUILTIN_VIEWS, ...FEATURE_EXTENSIONS].map((view) => Object.freeze(view)),
)

/** Cost navigation uses the installed edition's path and permission together. */
export const COST_VIEW: FeatureView =
  FEATURE_VIEWS.find((view) => view.id === 'finops') ??
  FEATURE_VIEWS.find((view) => view.id === 'storedBudgets')!

/** Edition views join the vocabulary; only link views carried by this edition. */
export const PRODUCT_NOUNS: readonly ProductNoun[] = DECLARED_PRODUCT_NOUNS.map(
  (noun) => ({
    ...noun,
    views: [
      ...new Set([
        ...noun.views,
        ...FEATURE_EXTENSIONS.filter((view) =>
          view.nouns?.includes(noun.id),
        ).map((view) => view.id),
      ]),
    ].filter((id) => FEATURE_VIEWS.some((view) => view.id === id)),
  }),
)

/**
 * RETIRED PATHS THAT STILL RESOLVE. Empty on purpose: every regrouping of the navigation
 * (hubs, then areas, then the one model the sidebar derives from) moved NOT ONE url, because
 * a place is a nav fact and the route tree is generated from `path` (app/routes.tsx).
 * Conservation here is structural, not compensated.
 *
 * The mechanism exists anyway, for the session that DOES move a path: add the entry and
 * `/old` keeps landing on `/new` (app/routes.tsx mounts a redirect per alias) instead of
 * 404-ing an operator's bookmark. route-census.test.ts drives every branch of the
 * checker with synthetic fixtures, so this list being empty today does not leave the
 * alias path unverified.
 */
export const ROUTE_ALIASES: readonly RouteAlias[] = [
  {
    from: '/finops',
    to: '/stored-budgets',
    note: 'Business feature; Community keeps stored-budget reads and export.',
  },
  {
    from: '/red-team',
    to: '/areas/security-identity',
    note: 'Red team is a Business feature.',
  },
  {
    from: '/team-costs',
    to: '/areas/observation',
    note: 'Team cost analysis is a Business feature.',
  },
].filter((alias) => !FEATURE_VIEWS.some((view) => view.path === alias.from))

/**
 * The visible (non-hidden) views of one area, grouped by section in the area's section order
 * and, inside a section, in registry order. A section with no view is omitted; a view naming
 * a section its area does not declare is a defect navigation/model.test.ts reports, not one this
 * silently files somewhere.
 */
export function viewsByArea(
  areaId: AreaId,
): { sectionId: string; views: FeatureView[] }[] {
  const area = NAV_AREAS.find((a) => a.id === areaId)
  if (!area) return []
  return area.sections
    .map((sectionId) => ({
      sectionId,
      views: FEATURE_VIEWS.filter(
        (v) =>
          !v.hideInNav &&
          v.navigation.kind === 'feature' &&
          v.navigation.areaId === areaId &&
          v.navigation.sectionId === sectionId,
      ),
    }))
    .filter((s) => s.views.length > 0)
}
