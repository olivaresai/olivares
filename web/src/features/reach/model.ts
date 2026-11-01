// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The Reach projection (issue #2553): the access map read as "what the tools reach".
// Pure function over the engine's EXISTING reads — the access-map graph, the agents
// list, the live-sessions page and the pending approval queue — no new mechanism,
// no fetching here.
//
// Honesty rules (same spirit as the access-map types):
// - A destination is WAITING only when a PENDING approval names it. Never infer a
//   request from anything else, and never render a waiting destination as reached.
// - A waiting destination that also has solid edges keeps them: the approval is a
//   new request ON a reached destination (e.g. write on a read folder), not a lie
//   about the existing reach.
// - A tool is a registered agent, or the driver a session runs (Claude Code,
//   Codex, …): a session's reach is its tool's reach. Identity origins are not
//   tools; non-family resource kinds (postgres.table, s3.bucket, mcp.tool, …) are
//   not destinations of this view.
import { toolName } from '@/features/agentops/engine-tools'
import type {
  ReachApproval,
  ReachEdge,
  ReachGraphResponse,
  ReachSession,
  ReachTool,
} from './types'

export type ReachFamily = 'folders' | 'mcp' | 'providers'

export interface ReachDestination {
  /** Stable key: `<canonical kind>\u0000<ref>` — same key edges and approvals join on. */
  id: string
  ref: string
  family: ReachFamily
  /** A pending approval names this destination: the view renders it dashed. */
  waiting: boolean
}

export interface ReachLink {
  /** The access-map edge id, or the approval id of a waiting link. */
  id: string
  toolId: string
  destinationId: string
}

export interface ReachModel {
  /** Tools that reach, or wait to reach, at least one destination. */
  tools: ReachTool[]
  destinations: ReachDestination[]
  /** Solid: the access map has an edge from the tool to the destination. */
  links: ReachLink[]
  /** Dashed: a pending approval names the destination and the session that asked. */
  waitingLinks: ReachLink[]
  /** Destination ids a solid or dashed tool line lands on — a waiting destination
   * outside it is drawn as an unattributed stub. */
  linkedDestIds: Set<string>
  /** A session's tool is unknown (off the live page, unreadable, or no driver
   * declared), so the session is named by its ref: the view says so. */
  sessionsByRef: boolean
  /** The graph page was cut: this model is a subset, label it as such. */
  hasMore: boolean
}

/** Access-map resource kinds this view shows as destinations (sourcescope
 * scopeableKindFor: folder sources are `resource`, MCP `mcp_server`, providers
 * `provider`). Observed telemetry spells the MCP kind `mcp.server` (the seeded
 * demo graph, cmd/olivares/seed/seed.go); both name the same family, so neither
 * spelling silently hides an MCP edge the access map shows. Everything else —
 * mcp.tool, postgres.table, s3.bucket — is not a destination family. */
const FAMILY_BY_KIND: Record<string, ReachFamily> = {
  resource: 'folders',
  mcp_server: 'mcp',
  'mcp.server': 'mcp',
  provider: 'providers',
}

/** The canonical kind a family is stored under (approvals name families, not kinds). */
const KIND_BY_FAMILY: Record<ReachFamily, string> = {
  folders: 'resource',
  mcp: 'mcp_server',
  providers: 'provider',
}

/** Pending-approval subject kinds that name a reach destination. A new network
 * destination is network.connect on a network_destination named by its host (the
 * demo estate's registry.terraform.io, scripts/seed-demo-dataset.py): it joins the
 * endpoints, where a provider's host already sits. Database, repository, deployment
 * and audit subjects are not reach destinations: they have their own surfaces. */
const FAMILY_BY_SUBJECT_KIND: Record<string, ReachFamily> = {
  workspace: 'folders',
  mcp_server: 'mcp',
  network_destination: 'providers',
}

/** The i18n key naming one destination of a family (graph label and table). */
export const KIND_LABEL_KEY: Record<ReachFamily, string> = {
  folders: 'kindFolder',
  mcp: 'kindMcp',
  providers: 'kindProvider',
}

export function familyOrder(family: ReachFamily): number {
  return family === 'folders' ? 0 : family === 'mcp' ? 1 : 2
}

/** The id prefix of a session named by its ref: its tool is not known. */
const BY_REF = 'session\u0000'
const destId = (kind: string, ref: string) => `${kind}\u0000${ref}`
const compareNames = (a: string, b: string) =>
  a.localeCompare(b, undefined, { sensitivity: 'base' })

export function buildReach(
  graph: ReachGraphResponse,
  agents: ReachTool[],
  approvals: ReachApproval[],
  sessions: ReachSession[],
): ReachModel {
  // Two agent indexes: the canonical join is origin_id → agent.id (the engine's
  // resolved id); origin_ref → external_id is only the fallback for edges that
  // lack origin_id, because external_id is settable and not unique.
  const agentsById = new Map<string, ReachTool>()
  const agentsByExternalId = new Map<string, ReachTool>()
  for (const a of agents) {
    agentsById.set(a.id, { id: a.id, name: a.name, kind: a.kind })
    if (a.external_id) agentsByExternalId.set(a.external_id, a)
  }
  // A session's driver by either of its references; a row that declares none says
  // nothing. Two rows that announce one session_ref with different drivers name
  // no driver: the ref stands instead.
  const engineByRef = new Map<string, string | null>()
  for (const s of sessions) {
    if (!s.engine) continue
    for (const ref of [s.session_ref, s.live_ref]) {
      if (!ref) continue
      const seen = engineByRef.get(ref)
      engineByRef.set(
        ref,
        seen === undefined || seen === s.engine ? s.engine : null,
      )
    }
  }

  const tools = new Map<string, ReachTool>()
  const destinations = new Map<string, ReachDestination>()
  // One link per tool and destination: the map keeps an edge per signal
  // (observed, granted), and this view draws none of them apart.
  const links = new Map<string, ReachLink>()
  const waitingLinks = new Map<string, ReachLink>()

  const addDest = (family: ReachFamily, ref: string) => {
    const id = destId(KIND_BY_FAMILY[family], ref)
    if (!destinations.has(id))
      destinations.set(id, { id, ref, family, waiting: false })
    return id
  }

  // The tool a session runs, or — off the live page, or with no driver declared —
  // the session named by its (already redacted) ref: hiding reach is worse than
  // showing an unnamed origin.
  const sessionTool = (ref: string): ReachTool => {
    const engine = engineByRef.get(ref)
    return engine
      ? { id: `engine\u0000${engine}`, name: toolName(engine), kind: engine }
      : { id: `${BY_REF}${ref}`, name: ref, kind: '' }
  }

  const edgeTool = (e: ReachEdge): ReachTool | undefined => {
    if (e.origin_kind === 'session') {
      const ref = e.origin_ref || e.origin_id
      return ref ? sessionTool(ref) : undefined
    }
    if (e.origin_kind !== 'agent' || (!e.origin_id && !e.origin_ref)) return
    // An edge with origin_id is that agent's alone: off the agents page it is named
    // by its ref, never handed to an agent that claims its external_id.
    const agent = e.origin_id
      ? agentsById.get(e.origin_id)
      : agentsByExternalId.get(e.origin_ref!)
    return (
      agent ?? {
        id: e.origin_id ?? e.origin_ref ?? '',
        name: e.origin_ref || e.origin_id || '',
        kind: '',
      }
    )
  }

  for (const e of graph.edges) {
    const family =
      e.resource_kind && Object.hasOwn(FAMILY_BY_KIND, e.resource_kind)
        ? FAMILY_BY_KIND[e.resource_kind]
        : undefined
    if (!family || !e.resource_ref) continue
    // Only TOOL reach makes a destination row: a resource reached by an identity
    // alone is not "what the tools reach" and would render as an orphan.
    const tool = edgeTool(e)
    if (!tool) continue
    const destinationId = addDest(family, e.resource_ref)
    tools.set(tool.id, tool)
    const key = `${tool.id}\u0000${destinationId}`
    if (!links.has(key))
      links.set(key, { id: e.id, toolId: tool.id, destinationId })
  }

  for (const ap of approvals) {
    if (ap.status !== 'pending' || !ap.subject_kind || !ap.subject_ref) continue
    const family = Object.hasOwn(FAMILY_BY_SUBJECT_KIND, ap.subject_kind)
      ? FAMILY_BY_SUBJECT_KIND[ap.subject_kind]
      : undefined
    if (!family) continue
    const destinationId = addDest(family, ap.subject_ref)
    destinations.get(destinationId)!.waiting = true
    // The session that asked names the tool the dashed line starts from; a tool
    // that already reaches the destination keeps its solid line (the dashed label
    // says the new request is waiting).
    if (!ap.session_ref) continue
    const tool = sessionTool(ap.session_ref)
    const key = `${tool.id}\u0000${destinationId}`
    if (links.has(key) || waitingLinks.has(key)) continue
    tools.set(tool.id, tool)
    waitingLinks.set(key, { id: ap.id, toolId: tool.id, destinationId })
  }

  return {
    tools: [...tools.values()].sort((a, b) => compareNames(a.name, b.name)),
    destinations: [...destinations.values()].sort(
      (a, b) =>
        familyOrder(a.family) - familyOrder(b.family) ||
        compareNames(a.ref, b.ref),
    ),
    links: [...links.values()],
    waitingLinks: [...waitingLinks.values()],
    linkedDestIds: new Set(
      [...links.values(), ...waitingLinks.values()].map((l) => l.destinationId),
    ),
    sessionsByRef: [...tools.values()].some((t) => t.id.startsWith(BY_REF)),
    hasMore: graph.has_more,
  }
}
