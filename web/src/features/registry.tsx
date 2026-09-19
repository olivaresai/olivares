// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { LucideIcon } from 'lucide-react'
import { lazy, Suspense, type ComponentType, type ReactNode } from 'react'
import {
  administrationDeepLinkQuestion,
  administrationSurfaceQuestion,
} from '@/features/communications/capabilities'
import { ChannelAdminContinuity } from '@/features/communications/channel-admin-continuity'
import type {
  CapabilityAccess,
  NormalizedCapabilityQuestion,
} from '@/lib/auth/capabilities'
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
  Bot,
  Boxes,
  CalendarClock,
  ClipboardCheck,
  ClipboardList,
  Coins,
  Compass,
  Container,
  Cpu,
  Database,
  DatabaseBackup,
  Disc3,
  DollarSign,
  FileBarChart,
  FileCheck2,
  Fingerprint,
  FlaskConical,
  Gauge,
  Globe,
  Handshake,
  HeartPulse,
  IdCard,
  Inbox,
  KeyRound,
  Layers,
  LayoutDashboard,
  LayoutTemplate,
  Library,
  Link2,
  Logs,
  MailPlus,
  MessagesSquare,
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
  Server,
  Settings2,
  Share2,
  Shield,
  ShieldAlert,
  ShieldCheck,
  Siren,
  SlidersHorizontal,
  Swords,
  Telescope,
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
// K3 first increment (I1): ONE room with THREE doors, the pattern again. The
// catalog (`/communications`, sessions:channel:read), the personal inbox
// (`/communications/inbox`, sessions:delivery:read) and channel creation
// (`/communications/new`, sessions:channel:write) mount the same view opened on a
// different tab, because the engine declares those three tiers independently: a
// principal holding only delivery:read must reach its own mailbox without the
// catalog, and one holding only channel:write must be able to create with explicit
// grants without reading anything. They are doors, not redirects — RequirePermission
// blocks a route on the ONE permission its entry declares.
const CommunicationsView = lazy(() =>
  import('./communications').then((m) => ({
    default: m.CommunicationsView,
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
// The provider-profile plane: its OWN room with two doors, same pattern as above.
// `/provider-profiles` (sessions:profile:read) and `/provider-bindings`
// (sessions:profile-binding:read) mount one view; the entrance names the tab that
// opens first. They exist because the plane's read tiers are independent of runs
// and live sessions, and a principal holding only one of them had no route.
const ProviderAdminView = lazy(() =>
  import('./agentops/provider-admin-view').then((m) => ({
    default: m.ProviderAdminView,
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
  /** Which hub holds this noun's ANCHOR view — where someone asking for it belongs. */
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
      'providerProfiles',
      'providerBindings',
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
      // maintainer when #694 landed without either census entry; console lane may overrule.
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

/**
 * THE NINE AREAS (N1, 2026-09-06) — the navigational structure the console is BROWSED by.
 *
 * Ratified by root (an internal design note (not shipped)) over the
 * proposal's ROUTE-MAP: every published route keeps its path, permission, component, actions,
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
 * `hub`, `HUB_ORDER` and `PRODUCT_NOUNS` stay as search and compatibility vocabulary (the
 * guide generator and the noun index still read them); the shell's PRIMARY ordering is this.
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
  /**
   * The directory page's route. Mounted by app/routes.tsx as a LITERAL createRoute per area
   * (the guide generator and the census read literals), so navigation/routes.test.ts pins that
   * the mounted set equals this list in both directions.
   */
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
    sections: ['capabilities', 'knowledge', 'artifacts'],
    helpHref: '/reference/console',
  },
  {
    id: 'work-communications',
    path: '/areas/work-communications',
    icon: MessagesSquare,
    sections: ['work', 'communications'],
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
    sections: ['access', 'policy', 'defense', 'boundaries'],
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
    sections: [
      'operations',
      'cost-adoption',
      'audit-recordings',
      'evaluation-evidence',
    ],
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
 * cannot be registered without a place (the compiler refuses it) and route-map.test.ts pins
 * each existing entry to the ratified ROUTE-MAP row. */
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
    navigation: { kind: 'root' },
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
    navigation: { kind: 'feature', areaId: 'system', sectionId: 'maintenance' },
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
    navigation: {
      kind: 'feature',
      areaId: 'infrastructure',
      sectionId: 'estate',
    },
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
    navigation: {
      kind: 'feature',
      areaId: 'infrastructure',
      sectionId: 'estate',
    },
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
    navigation: { kind: 'feature', areaId: 'ai', sectionId: 'sessions' },
    helpHref: '/reference/modules/ii-sessions',
    hub: 'operate',
    icon: Activity,
    permission: 'sessions:live:read',
    element: lazyView(SessionsWorkspaceView, { entrance: 'observe' as const }),
  },
  {
    id: 'accessMap',
    path: '/access-map',
    navigation: {
      kind: 'feature',
      areaId: 'security-identity',
      sectionId: 'access',
    },
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
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'audit-recordings',
    },
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
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'operations',
    },
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
    navigation: {
      kind: 'feature',
      areaId: 'system',
      sectionId: 'administration',
    },
    helpHref: '/reference/modules/xx-multi-tenancy',
    hub: 'govern',
    icon: SlidersHorizontal,
    permission: 'tenant:admin',
    element: lazyView(ConsoleView),
  },
  {
    id: 'capabilities',
    path: '/capabilities',
    navigation: {
      kind: 'feature',
      areaId: 'data-context',
      sectionId: 'capabilities',
    },
    helpHref: '/reference/modules/v-capabilities',
    hub: 'connect',
    icon: Plug,
    permission: 'capabilities:catalog:read',
    element: lazyView(CapabilitiesView),
  },
  {
    id: 'protocolBindings',
    path: '/communications/protocol-bindings',
    navigation: {
      kind: 'feature',
      areaId: 'work-communications',
      sectionId: 'communications',
    },
    helpHref: '/reference/modules/ii-sessions',
    hub: 'connect',
    icon: Cable,
    permission: 'sessions:protocol-binding:read',
    element: lazyView(ProtocolBindingsView),
  },
  {
    // K3 I1 — the CATALOG door: visible channels and the channel card, gated on the
    // read tier the engine requires on `GET /channels` and `GET /channels/{id}`.
    // Sending gates inside on sessions:message-send:write and the channel's own bits.
    id: 'communications',
    path: '/communications',
    navigation: {
      kind: 'feature',
      areaId: 'work-communications',
      sectionId: 'communications',
    },
    helpHref: '/reference/modules/ii-sessions',
    hub: 'operate',
    icon: MessagesSquare,
    permission: 'sessions:channel:read',
    element: lazyView(CommunicationsView, { entrance: 'catalog' as const }),
  },
  {
    // K3 I1 — the INBOX door: the exact personal mailbox, delivery and message reads
    // and the explicit Ack. Its own permission because the engine declares
    // sessions:delivery:read independently of channel:read; message reads gate on
    // sessions:message:read and the Ack on sessions:delivery:write inside.
    id: 'communicationsInbox',
    path: '/communications/inbox',
    navigation: {
      kind: 'feature',
      areaId: 'work-communications',
      sectionId: 'communications',
    },
    helpHref: '/reference/modules/ii-sessions',
    hub: 'operate',
    icon: Inbox,
    permission: 'sessions:delivery:read',
    element: lazyView(CommunicationsView, { entrance: 'inbox' as const }),
  },
  {
    // K3 I1 — the CREATE door: `POST /channels` with explicit initial grants, usable
    // by a principal that cannot read the catalog at all.
    id: 'communicationsNew',
    path: '/communications/new',
    navigation: {
      kind: 'feature',
      areaId: 'work-communications',
      sectionId: 'communications',
    },
    helpHref: '/reference/modules/ii-sessions',
    hub: 'operate',
    icon: MailPlus,
    permission: 'sessions:channel:write',
    element: lazyView(CommunicationsView, { entrance: 'new' as const }),
  },
  {
    // K3 I3 — the Handoffs door: the personal page of work-responsibility offers
    // addressed to this principal, the protected offer context behind each one and
    // the accept/reject response. Its own route because the personal collection is
    // a `sessions:delivery:read` surface a principal may hold without the catalog,
    // like the ordinary inbox beside it; responding is gated apart, on
    // `sessions:handoff-response:write`, where the act happens. The icon is
    // distinct from the inbox's because every registered view needs its own glyph.
    id: 'communicationsHandoffs',
    path: '/communications/handoffs',
    navigation: {
      kind: 'feature',
      areaId: 'work-communications',
      sectionId: 'communications',
    },
    helpHref: '/reference/modules/ii-sessions',
    hub: 'operate',
    icon: Handshake,
    permission: 'sessions:delivery:read',
    element: lazyView(CommunicationsView, { entrance: 'handoffs' as const }),
  },
  {
    // K3 I2 — the ADMINISTRATION door: the administrable catalog
    // (`GET /channels/administration`), the grant history, `PATCH /channels`, grant
    // and revoke. Its own permission because the engine declares
    // sessions:channel:admin independently of channel:read: a principal holding
    // core admin and a local admin bit — and no local read bit — must reach it
    // without the catalog. The engine decides the local bit on every read.
    id: 'communicationsAdministration',
    path: '/communications/administration',
    navigation: {
      kind: 'feature',
      areaId: 'work-communications',
      sectionId: 'communications',
    },
    helpHref: '/reference/modules/ii-sessions',
    hub: 'operate',
    icon: KeyRound,
    // ⛔ KEPT, AND IT NO LONGER DECIDES. `sessions:channel:admin` is a tenant-wide
    //    membership fact, and this door's authority is not: it may be held through a
    //    workspace-scoped authored grant the permission set never names, and it may be
    //    reflected here while an authored policy forbids the same operation. So the
    //    engine is asked (`capability` below) and this string stays for what it still
    //    truthfully is — the reflection, read by every unmigrated consumer and by the
    //    census that proves the console never asks for a permission the engine does not
    //    declare. Removing it would not tighten anything; it would delete the record.
    permission: 'sessions:channel:admin',
    capability: {
      surface: administrationSurfaceQuestion,
      deepLink: administrationDeepLinkQuestion,
    },
    // The one view whose answer expires on a budget while an operator is typing into it.
    // The boundary holds their touched fields and nothing else, above the cut that
    // rebuilds this room every few seconds; see channel-admin-continuity.tsx.
    continuity: ChannelAdminContinuity,
    element: lazyView(CommunicationsView, {
      entrance: 'administration' as const,
    }),
  },
  {
    id: 'permissions',
    path: '/permissions',
    navigation: {
      kind: 'feature',
      areaId: 'security-identity',
      sectionId: 'access',
    },
    helpHref: '/reference/modules/vi-governance',
    hub: 'govern',
    icon: ShieldCheck,
    permission: 'governance:identity:read',
    element: lazyView(GovernanceView),
  },
  {
    id: 'identity',
    path: '/identity',
    navigation: {
      kind: 'feature',
      areaId: 'security-identity',
      sectionId: 'access',
    },
    helpHref: '/reference/modules/vi-governance',
    hub: 'govern',
    icon: Fingerprint,
    permission: 'governance:identity:read',
    element: lazyView(IdentityView),
  },
  {
    id: 'claudePolicy',
    path: '/claude-policy',
    navigation: {
      kind: 'feature',
      areaId: 'security-identity',
      sectionId: 'policy',
    },
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
    navigation: {
      kind: 'feature',
      areaId: 'security-identity',
      sectionId: 'policy',
    },
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
    navigation: {
      kind: 'feature',
      areaId: 'security-identity',
      sectionId: 'boundaries',
    },
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
    navigation: {
      kind: 'feature',
      areaId: 'deployment',
      sectionId: 'deployments',
    },
    helpHref: '/reference/modules/vii-deploy',
    hub: 'connect',
    icon: Rocket,
    permission: 'deploy:deployment:read',
    element: lazyView(DeployView),
  },
  {
    id: 'knowledge',
    path: '/knowledge',
    navigation: {
      kind: 'feature',
      areaId: 'data-context',
      sectionId: 'knowledge',
    },
    helpHref: '/reference/modules/viii-knowledge',
    hub: 'connect',
    icon: BookOpen,
    permission: 'knowledge:kb:read',
    element: lazyView(KnowledgeView),
  },
  {
    id: 'catalog',
    path: '/catalog',
    navigation: {
      kind: 'feature',
      areaId: 'data-context',
      sectionId: 'capabilities',
    },
    helpHref: '/reference/modules/xiv-catalog',
    hub: 'connect',
    icon: Library,
    permission: 'catalog:entry:read',
    element: lazyView(CatalogView),
  },
  {
    id: 'killswitch',
    path: '/killswitch',
    navigation: {
      kind: 'feature',
      areaId: 'security-identity',
      sectionId: 'defense',
    },
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
    navigation: {
      kind: 'feature',
      areaId: 'work-communications',
      sectionId: 'work',
    },
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
    navigation: { kind: 'feature', areaId: 'ai', sectionId: 'sessions' },
    helpHref: '/how-to/run-claude-code-with-olivares',
    hub: 'operate',
    icon: Terminal,
    permission: 'sessions:run:read',
    element: lazyView(SessionsWorkspaceView, { entrance: 'operate' as const }),
  },
  {
    // The provider-profile plane's own door, gated on ITS read tier. The plane is
    // also a tab inside `/agentops` and `/sessions`, but those routes require run:read
    // or live:read, so a principal holding only sessions:profile:read could reach no
    // screen for a permission the engine declares. Same view as the next entry, opened
    // on the profiles tab; write/admin actions gate further inside (sessions:profile:
    // write/admin). Two doors into one room — not a redirect, and the generic route
    // guard keeps declaring exactly one permission per entry.
    id: 'providerProfiles',
    path: '/provider-profiles',
    navigation: { kind: 'feature', areaId: 'ai', sectionId: 'environments' },
    helpHref: '/reference/modules/ii-sessions',
    hub: 'operate',
    icon: IdCard,
    permission: 'sessions:profile:read',
    element: lazyView(ProviderAdminView, { entrance: 'profiles' as const }),
  },
  {
    // The source-binding door, gated on the binding plane's OWN read tier, which is
    // independent of the profile tiers. Opens on the tenant-wide bindings table; bind
    // and revoke gate on sessions:profile-binding:write/admin inside, and binding also
    // needs the deployment-wide source authority the engine decides on the roster read.
    id: 'providerBindings',
    path: '/provider-bindings',
    navigation: { kind: 'feature', areaId: 'ai', sectionId: 'environments' },
    helpHref: '/reference/modules/ii-sessions',
    hub: 'operate',
    icon: Link2,
    permission: 'sessions:profile-binding:read',
    element: lazyView(ProviderAdminView, { entrance: 'bindings' as const }),
  },
  {
    // Agent-artifact supply chain. This is tenant-estate metadata and its
    // own models.agent_aibom ledger, not the lineage of one owned model.
    id: 'agentArtifacts',
    path: '/agent-artifacts',
    navigation: {
      kind: 'feature',
      areaId: 'data-context',
      sectionId: 'artifacts',
    },
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
    navigation: { kind: 'feature', areaId: 'ai', sectionId: 'environments' },
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
    navigation: { kind: 'feature', areaId: 'automation', sectionId: 'events' },
    helpHref: '/reference/modules/eventing',
    hub: 'automate',
    icon: Bell,
    permission: 'eventing:subscription:read',
    commandActions: [
      { id: 'createSubscription', permission: 'eventing:subscription:write' },
    ],
    element: lazyView(EventingView),
  },
  {
    //Automations — the unified aggregator over schedules, event
    // subscriptions and alert routes, plus the trigger catalog. Gated on the
    // schedules read perm (the core rail); each panel inside degrades
    // independently on a per-rail 403 (deny-closed, never a blank page).
    id: 'automations',
    path: '/automations',
    navigation: {
      kind: 'feature',
      areaId: 'automation',
      sectionId: 'workflows',
    },
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
    navigation: {
      kind: 'feature',
      areaId: 'security-identity',
      sectionId: 'policy',
    },
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
    navigation: { kind: 'feature', areaId: 'automation', sectionId: 'events' },
    helpHref: '/reference/modules/xv-notify',
    hub: 'automate',
    icon: Siren,
    permission: 'notify:route:read',
    commandActions: [{ id: 'createRoute', permission: 'notify:route:write' }],
    element: lazyView(AlertingView),
  },

  // Intelligence ()
  {
    id: 'models',
    path: '/models',
    navigation: { kind: 'feature', areaId: 'ai', sectionId: 'models' },
    helpHref: '/reference/modules/x-models',
    hub: 'connect',
    icon: Cpu,
    permission: 'models:catalog:read',
    element: lazyView(ModelsView),
  },
  {
    id: 'modelOps',
    path: '/model-operations',
    navigation: { kind: 'feature', areaId: 'ai', sectionId: 'models' },
    helpHref: '/reference/modules/xxiii-model-operations',
    hub: 'connect',
    icon: BadgeCheck,
    permission: 'models:registry:read',
    element: lazyView(ModelOpsView),
  },
  {
    id: 'finops',
    path: '/finops',
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'cost-adoption',
    },
    helpHref: '/reference/modules/xi-finops',
    hub: 'prove',
    icon: Coins,
    permission: 'finops:spend:read',
    element: lazyView(FinOpsView),
  },
  {
    id: 'adoption',
    path: '/adoption',
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'cost-adoption',
    },
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
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'evaluation-evidence',
    },
    helpHref: '/reference/modules/xii-evals',
    hub: 'prove',
    icon: ClipboardCheck,
    permission: 'evals:run:read',
    element: lazyView(EvalsView),
  },
  {
    id: 'security',
    path: '/security',
    navigation: {
      kind: 'feature',
      areaId: 'security-identity',
      sectionId: 'defense',
    },
    helpHref: '/reference/modules/ix-security',
    hub: 'prove',
    icon: ShieldAlert,
    permission: 'security:finding:read',
    element: lazyView(SecurityView),
  },
  {
    id: 'recordings',
    path: '/recordings',
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'audit-recordings',
    },
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
    navigation: {
      kind: 'detail',
      areaId: 'observation',
      sectionId: 'audit-recordings',
      parentViewId: 'recordings',
    },
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
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'evaluation-evidence',
    },
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
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'evaluation-evidence',
    },
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
    navigation: {
      kind: 'feature',
      areaId: 'automation',
      sectionId: 'workflows',
    },
    helpHref: '/reference/modules/iv-orchestration',
    hub: 'automate',
    icon: Workflow,
    permission: 'orchestration:graph:read',
    commandActions: [
      { id: 'createSchedule', permission: 'orchestration:schedule:write' },
    ],
    element: lazyView(OrchestrationView),
  },
  {
    id: 'voice',
    path: '/voice',
    navigation: { kind: 'feature', areaId: 'ai', sectionId: 'execution' },
    helpHref: '/reference/modules/xvi-voice',
    hub: 'operate',
    icon: AudioLines,
    permission: 'voice:session:read',
    element: lazyView(VoiceView),
  },
  {
    id: 'sandbox',
    path: '/sandbox',
    navigation: { kind: 'feature', areaId: 'ai', sectionId: 'execution' },
    helpHref: '/reference/modules/xvii-sandbox',
    hub: 'operate',
    icon: FlaskConical,
    permission: 'sandbox:run:read',
    element: lazyView(SandboxView),
  },
  {
    id: 'redteam',
    path: '/red-team',
    navigation: {
      kind: 'feature',
      areaId: 'security-identity',
      sectionId: 'defense',
    },
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
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'operations',
    },
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
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'cost-adoption',
    },
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
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'evaluation-evidence',
    },
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
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'operations',
    },
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
    navigation: {
      kind: 'feature',
      areaId: 'ai',
      sectionId: 'provider-reference',
    },
    helpHref: '/reference/modules/x-models',
    hub: 'connect',
    icon: Layers,
    permission: 'models:platforms:read',
    element: lazyView(PlatformsView),
  },
  {
    id: 'rateLimits',
    path: '/rate-limits',
    navigation: {
      kind: 'feature',
      areaId: 'ai',
      sectionId: 'provider-reference',
    },
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
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'evaluation-evidence',
    },
    helpHref: '/how-to/verify-a-release',
    hub: 'prove',
    icon: PackageCheck,
    permission: 'observability:attestation:read',
    element: lazyView(AttestationView),
  },
  {
    id: 'apiPlayground',
    path: '/api-playground',
    navigation: { kind: 'feature', areaId: 'system', sectionId: 'development' },
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
    navigation: { kind: 'feature', areaId: 'system', sectionId: 'maintenance' },
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
    navigation: { kind: 'feature', areaId: 'system', sectionId: 'maintenance' },
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
    navigation: {
      kind: 'feature',
      areaId: 'system',
      sectionId: 'administration',
    },
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
    navigation: {
      kind: 'feature',
      areaId: 'security-identity',
      sectionId: 'boundaries',
    },
    helpHref: '/reference/modules/xiii-compliance',
    hub: 'govern',
    icon: Globe,
    permission: 'system:admin',
    element: lazyView(ResidencyView),
  },
]

/**
 * RETIRED PATHS THAT STILL RESOLVE. Empty on purpose: Re-hubbed all 51 views and
 * moved NOT ONE url, because a hub is a nav heading and the route tree is generated
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

/**
 * The visible (non-hidden) views of one area, grouped by section in the area's section order
 * and, inside a section, in registry order. A section with no view is omitted; a view naming
 * a section its area does not declare is a defect route-map.test.ts reports, not one this
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
