// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { LucideIcon } from 'lucide-react'
import { lazy, Suspense, type ComponentType, type ReactNode } from 'react'
import {
  Building2,
  Activity,
  AudioLines,
  BadgeCheck,
  BarChart3,
  Bell,
  BookOpen,
  Cable,
  CalendarCog,
  CloudUpload,
  Code2,
  Boxes,
  ClipboardCheck,
  ClipboardList,
  Coins,
  Compass,
  Cpu,
  DatabaseBackup,
  Disc3,
  DollarSign,
  FileBarChart,
  FileCheck2,
  Fingerprint,
  FlaskConical,
  Gauge,
  Globe,
  HeartPulse,
  Layers,
  LayoutDashboard,
  LayoutTemplate,
  Library,
  Logs,
  Network,
  OctagonAlert,
  PackageCheck,
  PackageSearch,
  PanelsTopLeft,
  Play,
  Plug,
  Radar,
  Rocket,
  Scale,
  ScrollText,
  Share2,
  ShieldAlert,
  ShieldCheck,
  Siren,
  SlidersHorizontal,
  Swords,
  Terminal,
  Timer,
  Waypoints,
  Workflow,
  Zap,
} from 'lucide-react'
import { Spinner } from '@/components/ui/spinner'
import type { RouteAlias } from './route-census'

// Estate overview — the home front door (route `/`), the real overview that
// replaced the foundation placeholder (the last one). Named export → default for lazy().
const HomeView = lazy(() =>
  import('./home/home-view').then((m) => ({ default: m.HomeView })),
)

// Management views, code-split so the heavier graph/editor deps load on demand.
const CapabilitiesView = lazy(() => import('./capabilities/capabilities-view'))
const CatalogView = lazy(() => import('./catalog/catalog-view'))
const GovernanceView = lazy(() => import('./governance/governance-view'))
//Claude Code governance console (CodeMirror editors load on demand).
const ClaudePolicyView = lazy(
  () => import('./claude-policy/claude-policy-view'),
)
//(plan 3.6) routine-policy console: the operator surface for the
// controls the plane has ENFORCED since. Its own route rather than a sixth
// tab under /permissions, because it carries its own permission pair
// (governance:routine:read / :admin) — behind the identity-gated route an
// operator holding only the routine perms could not reach it at all.
const RoutinePoliciesView = lazy(
  () => import('./governance/routine-policies-view'),
)
//AgentCore Cedar export (engine). Its own route for the SAME reason
// the routine policies above have one, and it is not a style preference: the
// engine declares governance:agentcore-export:admin in Module.Permissions()
// (governance.go:397), which is what makes a permission independently grantable
// — the note beside it exists because undeclared route permissions "would
// have stayed undelegable". Hanging this off the identity-gated /permissions
// view would have put an operator holding exactly the delegated export
// permission behind a governance:identity:read wall and made the surface
// unreachable for the one principal it was delegated to.
const AgentCoreExportView = lazy(
  () => import('./governance/agentcore-export-route'),
)
// Identity & NHI console (WebGL WIF graph loads on demand).
const IdentityView = lazy(() => import('./identity/identity-view'))
// Control console (FASE X): user onboarding, SSO/IdP, workspaces &
// agent-groups, scoped admin — the configure surface hang panels off.
const ConsoleView = lazy(() => import('./console/console-view'))
const DeployView = lazy(() => import('./deploy/deploy-view'))
const KnowledgeView = lazy(() => import('./knowledge/knowledge-view'))
// Estate kill switch console (one-click emergency stop, dual-control
// re-enable, forced post-review, evidence pack, guardian containment rules).
const KillswitchView = lazy(() => import('./killswitch/killswitch-view'))
// Work cockpit — the console over the K1 durable cross-session work kernel
// (work items, dependencies, acceptance criteria and the decision record).
const WorkView = lazy(() =>
  import('./work').then((m) => ({ default: m.WorkView })),
)
const ProtocolBindingsView = lazy(() =>
  import('./protocol-bindings').then((m) => ({
    default: m.ProtocolBindingsView,
  })),
)
//ONE destination for sessions, whichever way they reached the plane. Both
// `/sessions` (observe) and `/agentops` (operate) mount this view and open the SAME
// card, so an operator no longer has to know whether a session was DISCOVERED or
// LAUNCHED in order to pick a nav section. Each route keeps its own path, permission
// and nav entry — they are two doors into one room, NOT redirects: RequirePermission
// blocks a route on the one permission its entry declares, so pointing `/agentops` at
// `/sessions` would hand a run-only operator a Forbidden page instead of their runs.
const SessionsWorkspaceView = lazy(() =>
  import('./sessions/sessions-workspace-view').then((m) => ({
    default: m.SessionsWorkspaceView,
  })),
)

// Intelligence views, code-split (charts / React Flow load on demand). These
// export named components, so map them to a default for lazy().
const ModelsView = lazy(() =>
  import('./models/models-view').then((m) => ({ default: m.ModelsView })),
)
const ModelOpsView = lazy(() =>
  import('./model-ops/model-ops-view').then((m) => ({
    default: m.ModelOpsView,
  })),
)
const AgentArtifactsView = lazy(
  () => import('./agent-artifacts/agent-artifacts-view'),
)
const FinOpsView = lazy(() =>
  import('./finops/finops-view').then((m) => ({ default: m.FinOpsView })),
)
//(gap #12) Claude Code adoption / productivity dashboard.
const AdoptionView = lazy(() =>
  import('./adoption/adoption-view').then((m) => ({ default: m.AdoptionView })),
)
const EvalsView = lazy(() =>
  import('./evals/evals-view').then((m) => ({ default: m.EvalsView })),
)
const SecurityView = lazy(() =>
  import('./security/security-view').then((m) => ({ default: m.SecurityView })),
)
// Privileged session recording console (replay / verify / seal).
const RecordingsView = lazy(() =>
  import('./recordings/recordings-view').then((m) => ({
    default: m.RecordingsView,
  })),
)
// Session recording viewer — detail page reached from RecordingsView rows.
const SessionViewerPage = lazy(() =>
  import('./session-viewer/session-viewer-page').then((m) => ({
    default: m.SessionViewerPage,
  })),
)
// Workspace templates catalog — create/edit/duplicate/archive reusable
// session configuration templates (hooks, settings, connectors, policies).
const TemplatesView = lazy(() =>
  import('./workspace-templates/templates-view').then((m) => ({
    default: m.TemplatesView,
  })),
)
const ComplianceView = lazy(() =>
  import('./compliance/compliance-view').then((m) => ({
    default: m.ComplianceView,
  })),
)
const OrchestrationView = lazy(() =>
  import('./orchestration/orchestration-view').then((m) => ({
    default: m.OrchestrationView,
  })),
)
const VoiceView = lazy(() =>
  import('./voice/voice-view').then((m) => ({ default: m.VoiceView })),
)
const SandboxView = lazy(() =>
  import('./sandbox/sandbox-view').then((m) => ({ default: m.SandboxView })),
)
const RedTeamView = lazy(() =>
  import('./redteam/redteam-view').then((m) => ({ default: m.RedTeamView })),
)

// Eventing (webhook event subscriptions) console — outbound webhooks,
// event log, delivery tracking, and dead-letter queue with redeliver.
const EventingView = lazy(() =>
  import('./eventing/eventing-view').then((m) => ({
    default: m.EventingView,
  })),
)

// Automations — the unified page over the three automation rails
// (schedules · event subscriptions · alert routes) + the trigger catalog.
const AutomationsView = lazy(() =>
  import('./automations/automations-view').then((m) => ({
    default: m.AutomationsView,
  })),
)

// Team cost attribution — team-level spend with sparklines and expandable
// project/model breakdown rows. Gated on finops:spend:read (same as the FinOps
// module; the team-summary endpoint enforces it server-side).
const TeamCostsView = lazy(() =>
  import('./team-costs/team-costs-view').then((m) => ({
    default: m.TeamCostsView,
  })),
)

// Executive dashboards (module XXI), code-split (charts load on demand).
const ExecutiveView = lazy(() =>
  import('./executive/executive-view').then((m) => ({
    default: m.ExecutiveView,
  })),
)

// System dashboards: cross-cutting admin views over the Fase-F depth —
// observability (ingestion-health + trace drill-down), platform surfaces /
// compliance matrix + per-platform model lifecycle, the read-only rate-limit
// inventory, and supply-chain attestation. Several are honest declared-contract
// seams where the backend exposes no live API yet. Code-split.
const ObservabilityView = lazy(() =>
  import('./observability/observability-view').then((m) => ({
    default: m.ObservabilityView,
  })),
)
const PlatformsView = lazy(() =>
  import('./platforms/platforms-view').then((m) => ({
    default: m.PlatformsView,
  })),
)
const RateLimitsView = lazy(() =>
  import('./rate-limits/rate-limits-view').then((m) => ({
    default: m.RateLimitsView,
  })),
)
const AttestationView = lazy(() =>
  import('./attestation/attestation-view').then((m) => ({
    default: m.AttestationView,
  })),
)

// Onboarding wizard — first-time deployment setup (overview group).
const OnboardingView = lazy(() =>
  import('./onboarding').then((m) => ({ default: m.OnboardingView })),
)

//Inference proxy — the per-tenant policy-enforcing proxy admin: config gates,
// egress DLP rules and device-authorization approvals (AAL3 on writes).
const InferenceProxyView = lazy(() =>
  import('./inference-proxy/inference-proxy-view').then((m) => ({
    default: m.InferenceProxyView,
  })),
)

//Alerting — notify route CRUD, live route test, provisioned destinations and
// the read-only delivery log.
const AlertingView = lazy(() =>
  import('./alerting/alerting-view').then((m) => ({
    default: m.AlertingView,
  })),
)

// Per-workspace dashboard (overview group). Shows agents, sessions,
// resources and groups scoped to the workspace selected in the topbar switcher.
const WorkspaceDashboardView = lazy(() =>
  import('./workspace-dashboard/workspace-dashboard-view').then((m) => ({
    default: m.WorkspaceDashboardView,
  })),
)

//API Playground — interactive try-it console for the control-plane API.
const ApiPlaygroundView = lazy(() =>
  import('./api-playground/api-playground-view').then((m) => ({
    default: m.ApiPlaygroundView,
  })),
)

//Backup/Restore — DR management console (backup trigger, list, restore,
// schedule). Superadmin-only system view.
const BackupsView = lazy(() =>
  import('./backups/backups-view').then((m) => ({
    default: m.BackupsView,
  })),
)

//Log Viewer — real-time engine log stream with SSE, filters, search.
// Superadmin-only system view.
const LogsView = lazy(() =>
  import('./logs/logs-view').then((m) => ({
    default: m.LogsView,
  })),
)

// C07-02 Tenants — withdraw/restore a tenant's service. Superadmin-only.
const TenantsView = lazy(() =>
  import('./tenants/tenants-view').then((m) => ({
    default: m.TenantsView,
  })),
)

//Data residency — set/clear each org's region pin with a two-step
// confirm + AAL3 step-up. Superadmin-only system view.
const ResidencyView = lazy(() =>
  import('./residency/residency-view').then((m) => ({
    default: m.ResidencyView,
  })),
)

//Reports — the reporting module (modules/reporting) made navigable: generate +
// download the built-in reports on demand, manage the schedules when wired.
const ReportingView = lazy(() => import('./reporting/reporting-view'))

//Posture export — one-click GRC export of the read-only ground-truth posture
// (modules/posture-export) for a control tower to ingest.
const PostureExportView = lazy(
  () => import('./posture-export/posture-export-view'),
)

// Visibility views, code-split (the access-map / dependency-map React Flow
// graphs load on demand). Named exports mapped to a default for lazy().
const InventoryView = lazy(() =>
  import('./inventory').then((m) => ({ default: m.InventoryView })),
)

const AccessMapView = lazy(() =>
  import('./access-map').then((m) => ({ default: m.AccessMapView })),
)
const HealthView = lazy(() =>
  import('./health').then((m) => ({ default: m.HealthView })),
)
//Audit / Evidence Explorer — the tamper-evident ledger over the CORE /v1/audit
// surface (list, verify chain + checkpoints, export to WORM/SIEM, Ed25519 pubkey).
const AuditView = lazy(() =>
  import('./audit').then((m) => ({ default: m.AuditView })),
)

/** Calm centered spinner while a code-split view's chunk loads. */
function ViewLoading() {
  return (
    <div className="flex min-h-[40vh] items-center justify-center">
      <Spinner />
    </div>
  )
}

/** Wrap a lazily-loaded view in a Suspense boundary (the route tree renders the
 * element synchronously, so each lazy view carries its own boundary). */
function lazyView<P extends object>(
  View: ComponentType<P>,
  props?: P,
): () => ReactNode {
  // `props` exists for the two-doors-one-room case: `/sessions` and `/agentops`
  // mount the SAME view with a different entrance, rather than the registry carrying
  // two near-identical components that would drift apart the first time one is edited.
  return () => (
    <Suspense fallback={<ViewLoading />}>
      <View {...((props ?? {}) as P)} />
    </Suspense>
  )
}

/**
 * THE VIEW-REGISTRATION CONTRACT.
 *
 * The whole console — sidebar, command palette, and router — is generated from
 * FEATURE_VIEWS. The foundation seeded every product module with a placeholder so
 * navigation was complete from day one; each feature session then REPLACED its entry's
 * `element` with a real `lazy()` view from its own features/<module>/ folder, keeping
 * the rest of the shape — id, path, group, icon, permission — and never editing the
 * shell. As of every entry (including the `/` home overview, the last one) is a
 * real view; there are no placeholders left.
 *
 * `permission` mirrors the backend RBAC (core/auth): the nav item and route are
 * hidden/blocked unless `useAuth().can(permission)` is true. The backend remains
 * the source of truth — this only avoids offering an action that would 403.
 */
/**
 * THE FIVE HUBS — the nav axis is now the JOB, not the layer.
 *
 * Ordered by the F.1 chain (`an internal design note (not shipped):615`):
 * "cinco hubs Operate/Automate/Connect/Govern/Prove … Conservar rutas y funciones
 * existentes". The six layer groups it replaces (overview · visibility · management ·
 * intelligence · executive · system) named where a view sat in the architecture, which
 * is a fact about US, not about the operator's errand — and it concentrated 18 of 51
 * entries in `management` alone (measured today; the audit measured 17 of 49 in
 * P2-12, so the concentration had grown, not shrunk).
 *
 * ⚠ THIS AXIS IS ORTHOGONAL TO `path`. Re-hubbing a view changes its heading and
 * nothing else: the route tree is generated from `path` (app/routes.tsx:84), so NOT ONE
 * URL MOVED in this change. That is the conservation guarantee, and it is proved rather
 * than asserted by registry.route-conservation.test.ts, which pins every published path
 * against a committed census.
 */
export type HubId = 'operate' | 'automate' | 'connect' | 'govern' | 'prove'

/** Render order of the hubs: run it → let it run → plug it in → rule it → show it. */
export const HUB_ORDER: HubId[] = [
  'operate',
  'automate',
  'connect',
  'govern',
  'prove',
]

/**
 * THE THIRTEEN NOUNS — the OTHER language this console has to be explicable in.
 *
 * question (canon §0, and §5 of the audit that answered it) names thirteen
 * things an engineer must be able to SEE and MANAGE: sessions, agents, connections,
 * identities, models, rules, automations, groups, states, workflows, tasks, protocols,
 * infrastructure. The hubs above are VERBS; these are the NOUNS, and a navigation that
 * can only be explained in one of the two vocabularies has lost the other.
 *
 * They are not a second sidebar — one screen can only have one primary ordering. They
 * are the SEARCH index: `nounsForView()` maps each noun's label onto the views that
 * manage it, so typing "sesiones" finds every session surface across hubs while the
 * headings stay job-shaped.
 *
 * `hub` is where an operator asking for that noun should be sent FIRST. Several nouns
 * legitimately appear under more than one hub — that is declared and measured, not
 * hidden, by registry.nouns.test.ts, and the two that reach THREE hubs (`agents`,
 * `infrastructure`) are written up as findings in
 * an internal design note (not shipped) rather than filed under
 * whichever hub objected least.
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
  /** The hub this noun's ANCHOR view sits in — where someone asking for it belongs. */
  hub: HubId
  /**
   * Views that read or write this noun as a first-class object OF THEIR OWN SURFACE —
   * not every view that merely mentions it, or the filter returns everything.
   *
   * `views[0]` is the ANCHOR: the view whose hub must equal `hub` above, pinned by
   * registry.nouns.test.ts. It is deliberately NOT a display order. The sidebar renders
   * hub by hub in registry order, so there is no one position for a noun's results to be
   * "first" in; an earlier version of this comment promised primacy the navigation never
   * implemented, and the adversarial contrast measured 9 of 13 anchors not shown first.
   * The claim was the defect, not the ordering.
   */
  views: readonly string[]
}

/** The thirteen nouns, in the order asked for them.*/
export const PRODUCT_NOUNS: readonly ProductNoun[] = [
  {
    id: 'sessions',
    hub: 'operate',
    views: [
      'sessions',
      'agentops',
      'workspace-templates',
      'voice',
      'recordings',
      'session-viewer',
    ],
  },
  // The widest noun in the product: FOUR hubs. Running an agent, administering the
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
    hub: 'operate',
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
    hub: 'connect',
    views: [
      'capabilities',
      'console',
      'knowledge',
      'catalog',
      'inventory',
      'platforms',
    ],
  },
  {
    id: 'identities',
    hub: 'govern',
    views: ['identity', 'permissions', 'console', 'accessMap'],
  },
  // `finops` is here because it owns the MODEL RATE CATALOG, not merely because it
  // reports on models (features/finops/api.ts model-rates CRUD); `evals` because it
  // groups and scores by model.
  {
    id: 'models',
    hub: 'connect',
    views: ['models', 'modelOps', 'platforms', 'rateLimits', 'finops', 'evals'],
  },
  {
    id: 'rules',
    hub: 'govern',
    views: [
      'permissions',
      'claudePolicy',
      'routinePolicies',
      // Exporting policy OUT to a remote AgentCore engine is still policy: it sits with
      // claudePolicy and routinePolicies rather than in UNNAMED, because the ratchet's own
      // note prefers giving a view back a noun to declaring it unfindable. Placed by the
      // integrator when #694 landed without either census entry; console lane may overrule.
      'agentcoreExport',
      'inferenceProxy',
      'accessMap',
    ],
  },
  {
    id: 'automations',
    hub: 'automate',
    views: ['automations', 'eventing', 'alerting'],
  },
  {
    id: 'groups',
    hub: 'govern',
    views: ['console', 'permissions', 'workspaceDashboard'],
  },
  {
    id: 'states',
    hub: 'operate',
    views: ['home', 'health', 'observability', 'logs', 'workspaceDashboard'],
  },
  { id: 'workflows', hub: 'automate', views: ['orchestration', 'automations'] },
  // Landed `/work`, the K1 cockpit — this noun was "AUSENTE COMO CONCEPTO" in the
  // Audit and is no longer: re-measured 2026-08-11, web/src/features/work/ exists.
  { id: 'tasks', hub: 'operate', views: ['work', 'sandbox', 'orchestration'] },
  {
    id: 'protocols',
    hub: 'connect',
    views: [
      'capabilities',
      'protocolBindings',
      'observability',
      'apiPlayground',
    ],
  },
  // Spans THREE hubs — see the audit note. "Infrastructure" means the CLIENT's estate
  // (deploy, platforms), OUR plane's own runtime (backups), and where bytes may sit
  // (residency, inference proxy) — three errands the word does not distinguish.
  {
    id: 'infrastructure',
    hub: 'connect',
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
    ],
  },
]

/** The nouns a given view manages — the search index's second axis. */
export function nounsForView(viewId: string): NounId[] {
  return PRODUCT_NOUNS.filter((n) => n.views.includes(viewId)).map((n) => n.id)
}

export interface FeatureView {
  /** Stable id — also the i18n key under nav.items/nav.descriptions and the route id. */
  id: string
  /**
   * Route path under the authenticated app shell. THE PUBLISHED CONTRACT: an operator's
   * bookmark, a runbook's deep link and a docs cross-reference are all this string.
   * Changing one is a breaking change even when the screen survives — which is why
   * every value here is pinned by route-census.json and can only be retired through a
   * declared alias that still resolves (ROUTE_ALIASES below).
   */
  path: string
  /** Which of the five jobs this view serves. Nav axis only — never affects `path`. */
  hub: HubId
  icon: LucideIcon
  /** RBAC permission required to see/enter this view; undefined = any signed-in user. */
  permission?: string
  /** The route content — a `lazy()` view from the module's own features/ folder. */
  element: () => ReactNode
  /** Hide from the sidebar while staying routable and RBAC-guarded — for a view
   * reached only by deep link (session-viewer sets it: its path is parameterized,
   * so navigating to the literal path would 404). Honoured by `viewsByGroup` AND
   * the ⌘K command palette — every list offering direct navigation must
   * filter it. */
  hideInNav?: boolean
  /** Docs-site page for this view, as a site-relative slug (`/reference/…`).
   * The topbar renders it as the contextual help link (docs.olivares.ai + slug);
   * registry-help.test.ts pins every slug to a page that actually exists. */
  helpHref: string
  /** Palette actions: quick verbs ⌘K offers for this view (e.g. "new
   * subscription"), gated by the SAME permission as the view. `action` is
   * consumed by the view on mount via the pending-command store; the i18n label
   * lives under nav:commandActions.<id>.<action>. */
  commandActions?: readonly string[]
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
}

/**
 * The seeded registry. Module → layer mapping per the master catalog (README.md):
 *  Visibility   I Inventory · II Sessions · III Access map · XXII Health
 *  Management   V MCP/skills · VI Permissions · VII Deploy · VIII Knowledge · XIV Catalog
 *  Intelligence X Models · XI FinOps · XII Evals · IX Security · XIII Compliance ·
 *               IV Orchestration · XVI Voice · XVII Sandbox · XVIII Red-teaming
 *  Executive    XXI Dashboards
 */
export const FEATURE_VIEWS: FeatureView[] = [
  {
    id: 'home',
    path: '/',
    helpHref: '/',
    hub: 'operate',
    icon: LayoutDashboard,
    element: lazyView(HomeView),
  },

  // Onboarding wizard: first-time deployment setup. Superadmin-gated so only
  // the operator who configures the deployment sees it. Not hidden — it surfaces
  // in the nav as a persistent reminder until the operator completes or dismisses.
  {
    id: 'onboarding',
    path: '/onboarding',
    helpHref: '/start/quickstart',
    hub: 'connect',
    // Compass, not Rocket: Rocket is Deploy's icon — a guided first-run
    // is wayfinding, and every registered view must carry a unique glyph.
    icon: Compass,
    permission: 'system:admin',
    element: lazyView(OnboardingView),
  },

  // Per-workspace dashboard: agents, sessions, resources and groups scoped
  // to the workspace selected in the topbar switcher. Gated on tenant:read (the
  // same permission the workspace list requires).
  {
    id: 'workspaceDashboard',
    path: '/workspace',
    helpHref: '/reference/modules/xx-multi-tenancy',
    hub: 'operate',
    // PanelsTopLeft, not Layers: Layers belongs to Platforms; a dashboard
    // of scoped panels is what this view actually is.
    icon: PanelsTopLeft,
    permission: 'tenant:read',
    element: lazyView(WorkspaceDashboardView),
  },

  // Visibility ()
  {
    id: 'inventory',
    path: '/inventory',
    helpHref: '/reference/modules/i-inventory',
    hub: 'connect',
    icon: Boxes,
    permission: 'inventory:catalog:read',
    element: lazyView(InventoryView),
  },
  {
    //the OBSERVE door into the unified sessions room. Same view and same card
    // as `/agentops`; this entrance keeps the visibility framing and the live-read
    // permission the observed half needs.
    id: 'sessions',
    path: '/sessions',
    helpHref: '/reference/modules/ii-sessions',
    hub: 'operate',
    icon: Activity,
    permission: 'sessions:live:read',
    element: lazyView(SessionsWorkspaceView, { entrance: 'observe' as const }),
  },
  {
    id: 'accessMap',
    path: '/access-map',
    helpHref: '/reference/modules/iii-access-map',
    hub: 'govern',
    icon: Network,
    permission: 'accessmap:graph:read',
    element: lazyView(AccessMapView),
  },
  {
    //Audit / Evidence Explorer over the core ledger (/v1/audit). Gated on
    // audit:read (the same RBAC the backend enforces); export/verify gate the same
    // perm server-side, the superadmin system-ledger toggle is hidden otherwise.
    id: 'audit',
    path: '/audit',
    savedViewsFeatureId: 'audit',
    helpHref: '/reference/modules/ix-security',
    hub: 'prove',
    icon: FileCheck2,
    permission: 'audit:read',
    element: lazyView(AuditView),
  },
  {
    id: 'health',
    path: '/health',
    helpHref: '/reference/modules/xxii-health',
    hub: 'operate',
    icon: HeartPulse,
    permission: 'health:status:read',
    element: lazyView(HealthView),
  },

  // Management ()
  {
    // Control console: the configure surface (onboard users, SSO/IdP,
    // workspaces & agent-groups, scoped admin). Gated on tenant:admin so org
    // admins/owners + superadmins see it; each tab gates its writes further.
    id: 'console',
    path: '/console',
    helpHref: '/reference/modules/xx-multi-tenancy',
    hub: 'govern',
    icon: SlidersHorizontal,
    permission: 'tenant:admin',
    element: lazyView(ConsoleView),
  },
  {
    id: 'capabilities',
    path: '/capabilities',
    helpHref: '/reference/modules/v-capabilities',
    hub: 'connect',
    icon: Plug,
    permission: 'capabilities:catalog:read',
    element: lazyView(CapabilitiesView),
  },
  {
    id: 'protocolBindings',
    path: '/communications/protocol-bindings',
    helpHref: '/reference/modules/ii-sessions',
    hub: 'connect',
    icon: Cable,
    permission: 'sessions:protocol-binding:read',
    element: lazyView(ProtocolBindingsView),
  },
  {
    id: 'permissions',
    path: '/permissions',
    helpHref: '/reference/modules/vi-governance',
    hub: 'govern',
    icon: ShieldCheck,
    permission: 'governance:identity:read',
    element: lazyView(GovernanceView),
  },
  {
    id: 'identity',
    path: '/identity',
    helpHref: '/reference/modules/vi-governance',
    hub: 'govern',
    icon: Fingerprint,
    permission: 'governance:identity:read',
    element: lazyView(IdentityView),
  },
  {
    id: 'claudePolicy',
    path: '/claude-policy',
    helpHref: '/how-to/connectors/claude-code-hooks-pep',
    hub: 'govern',
    icon: ScrollText,
    permission: 'governance:claude-policy:read',
    element: lazyView(ClaudePolicyView),
  },
  {
    //(plan 3.6) routine governance: cadence floors, concurrency caps,
    // approval requirements, cron allowlists and blocked environments for
    // Claude Code Routines. Gated on governance:routine:read — the same RBAC
    // the six engine routes enforce (governance.go:528-533); authoring gates
    // separately on governance:routine:admin inside the view.
    id: 'routinePolicies',
    path: '/routine-policies',
    helpHref: '/reference/modules/vi-governance',
    hub: 'govern',
    // CalendarCog, not Timer (taken by Orchestration) and not Workflow: this is
    // governance OVER a schedule, not the schedule itself.
    icon: CalendarCog,
    permission: 'governance:routine:read',
    element: lazyView(RoutinePoliciesView),
  },
  {
    //the console half of the AgentCore Cedar export. Both engine
    // routes require governance:agentcore-export:admin (governance.go:563-564):
    // planning reads remote AWS policy metadata and applying mutates the remote
    // engine, so there is no read tier to gate on and the ADMIN permission is
    // the honest gate. The registry already carries admin-gated entries for the
    // same reason (system:admin at :453, tenant:admin at :536,
    // recording:session:admin at :794) — a nav permission is "what this
    // principal may reach", not "a :read suffix".
    id: 'agentcoreExport',
    path: '/agentcore-export',
    helpHref: '/reference/modules/vi-governance',
    // `hub`, not `group`. #694 was written before main renamed the nav axis, and its NEW entry
    // came through the merge with the old field name because there was nothing to merge it
    // against — a CLEAN merge that produced a type error, which the build caught. 'govern' is
    // measured, not guessed: of the six entries #694 marked group:'management', main places five
    // in 'govern', and this one carries a governance:*:admin permission and vi-governance docs.
    hub: 'govern',
    // CloudUpload, not Upload (a plain upload) and not ShieldCheck (taken by
    // Permissions): this pushes local policy OUT to a remote cloud engine.
    icon: CloudUpload,
    permission: 'governance:agentcore-export:admin',
    element: lazyView(AgentCoreExportView),
  },
  {
    id: 'deploy',
    path: '/deploy',
    helpHref: '/reference/modules/vii-deploy',
    hub: 'connect',
    icon: Rocket,
    permission: 'deploy:deployment:read',
    element: lazyView(DeployView),
  },
  {
    id: 'knowledge',
    path: '/knowledge',
    helpHref: '/reference/modules/viii-knowledge',
    hub: 'connect',
    icon: BookOpen,
    permission: 'knowledge:kb:read',
    element: lazyView(KnowledgeView),
  },
  {
    id: 'catalog',
    path: '/catalog',
    helpHref: '/reference/modules/xiv-catalog',
    hub: 'connect',
    icon: Library,
    permission: 'catalog:entry:read',
    element: lazyView(CatalogView),
  },
  {
    id: 'killswitch',
    path: '/killswitch',
    helpHref: '/how-to/cookbook/kill-switch-drill',
    hub: 'operate',
    icon: OctagonAlert,
    permission: 'governance:killswitch:read',
    element: lazyView(KillswitchView),
  },
  {
    // Work cockpit — the durable cross-session backlog (K1). Gated on the base
    // work-read perm; write/admin actions gate further inside the view
    // (sessions:work:write / :admin), and the decisions tab on
    // sessions:decision:read. All six reach whoami's effective set, measured on the
    // wire by cmd/olivares/work_console_whoami_reach_test.go.
    id: 'work',
    path: '/work',
    helpHref: '/reference/modules/ii-sessions',
    hub: 'operate',
    icon: ClipboardList,
    permission: 'sessions:work:read',
    element: lazyView(WorkView),
  },
  {
    //Claude Code operate portal (FASE V) unified. Gated on the base
    // run-read perm; create/stop/cleanup actions gate further inside the view
    // (sessions:run:write/admin). Same view and same card as `/sessions`: this
    // entrance keeps the operate framing (launch, workspaces) and its own permission,
    // so nothing an operator could reach before became unreachable.
    id: 'agentops',
    path: '/agentops',
    helpHref: '/how-to/run-claude-code-with-olivares',
    hub: 'operate',
    icon: Terminal,
    permission: 'sessions:run:read',
    element: lazyView(SessionsWorkspaceView, { entrance: 'operate' as const }),
  },
  {
    // Agent-artifact supply chain. This is tenant-estate metadata and its
    // own models.agent_aibom ledger, not the lineage of one owned model.
    id: 'agentArtifacts',
    path: '/agent-artifacts',
    helpHref: '/reference/modules/xxiii-model-operations',
    hub: 'prove',
    icon: PackageSearch,
    permission: 'models:registry:read',
    element: lazyView(AgentArtifactsView),
  },
  {
    // Workspace templates catalog — reusable session configuration snapshots
    // (hooks, settings, connectors, policies). Gated on the base template-read perm;
    // create/edit/archive actions gate further inside the view.
    id: 'workspace-templates',
    path: '/workspace-templates',
    helpHref: '/reference/modules/ii-sessions',
    hub: 'operate',
    icon: LayoutTemplate,
    permission: 'sessions:template:read',
    element: lazyView(TemplatesView),
  },
  {
    // Eventing (webhook event subscriptions) — outbound webhooks, event log,
    // delivery tracking, and dead-letter queue. Gated on the subscription-read perm;
    // write actions gate further inside the view (eventing:subscription:write).
    id: 'eventing',
    path: '/eventing',
    helpHref: '/reference/modules/eventing',
    hub: 'automate',
    icon: Bell,
    permission: 'eventing:subscription:read',
    commandActions: ['createSubscription'],
    element: lazyView(EventingView),
  },
  {
    //Automations — the unified aggregator over schedules, event
    // subscriptions and alert routes, plus the trigger catalog. Gated on the
    // schedules read perm (the core rail); each panel inside degrades
    // independently on a per-rail 403 (deny-closed, never a blank page).
    id: 'automations',
    path: '/automations',
    helpHref: '/reference/modules/iv-orchestration',
    hub: 'automate',
    icon: Zap,
    permission: 'orchestration:schedule:read',
    element: lazyView(AutomationsView),
  },
  {
    //Inference proxy admin — config gates, egress DLP rules and device
    // approvals. Gated on the proxy config-read perm; config writes need editor,
    // DLP writes need admin, and every write requires an AAL3 step-up in the view.
    id: 'inferenceProxy',
    path: '/inference-proxy',
    helpHref: '/reference/modules/inferenceproxy',
    hub: 'govern',
    icon: Waypoints,
    permission: 'inferenceproxy:config:read',
    element: lazyView(InferenceProxyView),
  },
  {
    //Alerting — notify routes (event → destination) CRUD + live test, and the
    // read-only delivery log. Gated on the route-read perm; create/edit need write,
    // delete/test need admin (enforced server-side and mirrored inside the view).
    id: 'alerting',
    path: '/alerting',
    helpHref: '/reference/modules/xv-notify',
    hub: 'automate',
    icon: Siren,
    permission: 'notify:route:read',
    commandActions: ['createRoute'],
    element: lazyView(AlertingView),
  },

  // Intelligence ()
  {
    id: 'models',
    path: '/models',
    helpHref: '/reference/modules/x-models',
    hub: 'connect',
    icon: Cpu,
    permission: 'models:catalog:read',
    element: lazyView(ModelsView),
  },
  {
    id: 'modelOps',
    path: '/model-operations',
    helpHref: '/reference/modules/xxiii-model-operations',
    hub: 'connect',
    icon: BadgeCheck,
    permission: 'models:registry:read',
    element: lazyView(ModelOpsView),
  },
  {
    id: 'finops',
    path: '/finops',
    helpHref: '/reference/modules/xi-finops',
    hub: 'prove',
    icon: Coins,
    permission: 'finops:spend:read',
    element: lazyView(FinOpsView),
  },
  {
    id: 'adoption',
    path: '/adoption',
    helpHref: '/reference/modules/claudeadoption',
    hub: 'prove',
    icon: Gauge,
    // Team/org adoption views are viewer-read; the per-developer drill-down is gated
    // deny-closed inside the view (adoption:developer:read).
    permission: 'adoption:metrics:read',
    element: lazyView(AdoptionView),
  },
  {
    id: 'evals',
    path: '/evals',
    helpHref: '/reference/modules/xii-evals',
    hub: 'prove',
    icon: ClipboardCheck,
    permission: 'evals:run:read',
    element: lazyView(EvalsView),
  },
  {
    id: 'security',
    path: '/security',
    helpHref: '/reference/modules/ix-security',
    hub: 'prove',
    icon: ShieldAlert,
    permission: 'security:finding:read',
    element: lazyView(SecurityView),
  },
  {
    id: 'recordings',
    path: '/recordings',
    savedViewsFeatureId: 'recordings',
    helpHref: '/reference/modules/recording',
    hub: 'prove',
    icon: Disc3,
    permission: 'recording:session:admin',
    element: lazyView(RecordingsView),
  },
  {
    // Session recording viewer — detail page reached by clicking a row in
    // RecordingsView. Not a sidebar entry; navigation is deep-link only.
    id: 'session-viewer',
    path: '/session-viewer/$id',
    helpHref: '/reference/modules/recording',
    hub: 'prove',
    icon: Play,
    permission: 'recording:session:admin',
    element: lazyView(SessionViewerPage),
    hideInNav: true,
  },
  {
    id: 'compliance',
    path: '/compliance',
    helpHref: '/reference/modules/xiii-compliance',
    hub: 'prove',
    // Scale, not ScrollText: ScrollText is the Claude-policy icon —
    // compliance is the scales of regulation, not a policy document.
    icon: Scale,
    permission: 'compliance:framework:read',
    element: lazyView(ComplianceView),
  },
  {
    //Posture export — one-click read-only export of the ground-truth posture
    // (inventory, least-privilege drift, findings) for a control tower to ingest.
    // Gated on the export read perm the backend enforces on /v1/m/posture/export.
    id: 'postureExport',
    path: '/posture-export',
    savedViewsFeatureId: 'posture-export',
    helpHref: '/reference/modules/posture-export',
    hub: 'prove',
    icon: Share2,
    permission: 'posture:export:read',
    element: lazyView(PostureExportView),
  },
  {
    id: 'orchestration',
    path: '/orchestration',
    helpHref: '/reference/modules/iv-orchestration',
    hub: 'automate',
    icon: Workflow,
    permission: 'orchestration:graph:read',
    commandActions: ['createSchedule'],
    element: lazyView(OrchestrationView),
  },
  {
    id: 'voice',
    path: '/voice',
    helpHref: '/reference/modules/xvi-voice',
    hub: 'operate',
    icon: AudioLines,
    permission: 'voice:session:read',
    element: lazyView(VoiceView),
  },
  {
    id: 'sandbox',
    path: '/sandbox',
    helpHref: '/reference/modules/xvii-sandbox',
    hub: 'operate',
    icon: FlaskConical,
    permission: 'sandbox:run:read',
    element: lazyView(SandboxView),
  },
  {
    id: 'redteam',
    path: '/red-team',
    helpHref: '/reference/modules/xviii-redteam',
    hub: 'prove',
    icon: Swords,
    permission: 'redteam:target:read',
    element: lazyView(RedTeamView),
  },

  // Executive (). No dedicated backend permission: module XXI is a web-only
  // rollup of the other modules' read APIs, so the route is open to any signed-in
  // user and each KPI pillar is gated INSIDE the view by its source's read
  // permission (a reader who can't see /finops never sees the cost KPI, and the
  // exported PDF therefore can't leak it). docs/SECURITY-HARDENING.md.
  {
    id: 'dashboards',
    path: '/dashboards',
    helpHref: '/reference/modules/xxi-executive-dashboards',
    hub: 'prove',
    icon: BarChart3,
    element: lazyView(ExecutiveView),
  },
  {
    // Team cost attribution — team-level spend with sparklines and expandable
    // project/model breakdown rows. Same permission gate as FinOps: the backend
    // enforces finops:spend:read on the /analytics/team-summary endpoint.
    id: 'team-costs',
    path: '/team-costs',
    savedViewsFeatureId: 'team-costs',
    helpHref: '/reference/modules/xi-finops',
    hub: 'prove',
    icon: DollarSign,
    permission: 'finops:spend:read',
    element: lazyView(TeamCostsView),
  },
  {
    //Reports — on-demand generation + download of the five built-in reports
    // (compliance evidence, audit summary, FinOps, access review, executive), plus
    // the scheduler surface when the enterprise build wires it. Gated on the
    // reporting read perm the backend enforces on /v1/m/reporting/reports.
    id: 'reporting',
    path: '/reporting',
    helpHref: '/reference/modules/reporting',
    hub: 'prove',
    icon: FileBarChart,
    permission: 'reporting:report:read',
    element: lazyView(ReportingView),
  },

  // System (). Cross-cutting admin dashboards over the Fase-F depth. Each route
  // gates on its source module's existing read permission (the backend stays the
  // source of truth); the views themselves are honest about what is live vs a
  // declared-contract seam.
  {
    id: 'observability',
    path: '/observability',
    savedViewsFeatureId: 'observability',
    helpHref: '/reference/modules/observability',
    hub: 'operate',
    icon: Radar,
    permission: 'health:status:read',
    element: lazyView(ObservabilityView),
  },
  {
    id: 'platforms',
    path: '/platforms',
    helpHref: '/reference/modules/x-models',
    hub: 'connect',
    icon: Layers,
    permission: 'models:platforms:read',
    element: lazyView(PlatformsView),
  },
  {
    id: 'rateLimits',
    path: '/rate-limits',
    helpHref: '/reference/modules/x-models',
    hub: 'govern',
    // Timer, not Gauge: Gauge belongs to Adoption; rate limits are about
    // time windows, and every registered view must carry a unique glyph.
    icon: Timer,
    permission: 'models:ratelimits:read',
    element: lazyView(RateLimitsView),
  },
  {
    id: 'attestation',
    path: '/attestation',
    helpHref: '/how-to/verify-a-release',
    hub: 'prove',
    icon: PackageCheck,
    permission: 'observability:attestation:read',
    element: lazyView(AttestationView),
  },
  {
    id: 'apiPlayground',
    path: '/api-playground',
    helpHref: '/reference/modules/xix-api-manage-as-code',
    hub: 'connect',
    icon: Code2,
    permission: 'tenant:admin',
    element: lazyView(ApiPlaygroundView),
  },

  //Backup/Restore — DR management console: list, trigger, download,
  // restore with dual-confirmation, scheduling. Superadmin-only.
  {
    id: 'backups',
    path: '/backups',
    helpHref: '/how-to/backup-and-restore',
    hub: 'operate',
    icon: DatabaseBackup,
    permission: 'system:admin',
    element: lazyView(BackupsView),
  },
  //Log Viewer — real-time engine log stream (SSE), with level/module
  // filters, search, pause/resume. Superadmin-only.
  {
    id: 'logs',
    path: '/logs',
    helpHref: '/how-to/troubleshooting',
    hub: 'operate',
    // Logs, not ScrollText: ScrollText is the Claude-policy icon, and
    // lucide ships a literal Logs glyph for a log stream.
    icon: Logs,
    permission: 'system:admin',
    element: lazyView(LogsView),
  },
  // C07-02 Tenants — retirar y restaurar el servicio de un tenant.
  //
  // ⚠ NO va dentro de «Data residency» aunque ésa ya liste los mismos orgs: esa pantalla trata de
  // DÓNDE viven los datos, y colgarle una acción de ciclo de vida sería una pantalla que miente
  // sobre lo que es. Además el roster de residencia no muestra el `status`, así que hoy un
  // operador no puede ver que un tenant está suspendido.
  //
  // ⚠ Y no es la superficie de C07-09: `/admin/tenants*` **no existe** (404 medido en motor vivo
  // el 2026-08-18). Ésta se construye sobre `/v1/system/orgs*`, que sí existen; cuando aterrice
  // aquella API, extenderá esta vista en vez de estrenar otra.
  {
    id: 'tenants',
    path: '/tenants',
    helpHref: '/how-to/troubleshooting',
    hub: 'operate',
    icon: Building2,
    permission: 'system:admin',
    element: lazyView(TenantsView),
  },
  //Data residency — org region pin set/clear with two-step confirm +
  // AAL3. Superadmin-only; the org roster + region PUT are authzSystem routes.
  {
    id: 'residency',
    path: '/residency',
    helpHref: '/reference/modules/xiii-compliance',
    hub: 'govern',
    icon: Globe,
    permission: 'system:admin',
    element: lazyView(ResidencyView),
  },
]

/**
 * RETIRED PATHS THAT STILL RESOLVE. Empty on purpose: Re-hubbed all 51 views and
 * moved NOT ONE url, because the hub is a nav heading and the route tree is generated
 * from `path` (app/routes.tsx:84). Conservation here is structural, not compensated.
 *
 * The mechanism exists anyway, for the session that DOES move a path: add the entry and
 * `/old` keeps landing on `/new` (app/routes.tsx mounts a redirect per alias) instead of
 * 404-ing an operator's bookmark. route-census.test.ts drives every branch of the
 * checker with synthetic fixtures, so this list being empty today does not leave the
 * alias path unverified.
 */
export const ROUTE_ALIASES: readonly RouteAlias[] = []

/** Group the visible (non-hidden) views by hub, in registry order. */
export function viewsByHub(): Record<HubId, FeatureView[]> {
  const out = {
    operate: [],
    automate: [],
    connect: [],
    govern: [],
    prove: [],
  } as Record<HubId, FeatureView[]>
  for (const v of FEATURE_VIEWS) {
    if (!v.hideInNav) out[v.hub].push(v)
  }
  return out
}
