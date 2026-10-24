// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The reach projection (issue #2553): tools on the left, destinations on the right
// (folders, MCP servers, provider endpoints), and a destination a pending approval
// names rendered WAITING (dashed), never invented as reached. Pure model tests —
// the view renders this, the engine keeps the truth.
//
// FIXTURE_* is a hand-made estate that holds every family shape (agent edges to
// folders, MCP servers and providers); it is NOT the seeded demo. Its edges carry
// origin_id because the engine's edgeDTO ALWAYS does (modules/access-map/dto.go).
// The seeded-demo tests at the end are built from cmd/olivares/seed/seed.go's own
// edges and sessions.
import { describe, expect, it } from 'vitest'
import { buildReach, familyOrder } from './model'
import type {
  ReachApproval,
  ReachEdge,
  ReachGraphResponse,
  ReachSession,
  ReachTool,
} from './types'

/** Four agents as GET /v1/agents returns them; only three hold family edges. */
const FIXTURE_AGENTS: ReachTool[] = [
  {
    id: 'a1',
    name: 'Claude Code',
    kind: 'claude-code',
    external_id: 'claude-code-1',
  },
  { id: 'a2', name: 'Codex', kind: 'codex', external_id: 'codex-1' },
  { id: 'a3', name: 'OpenCode', kind: 'opencode', external_id: 'opencode-1' },
  { id: 'a4', name: 'Gemini CLI', kind: 'gemini', external_id: 'gemini-1' },
]

const FIXTURE_EDGES: ReachEdge[] = [
  {
    id: 'e1',
    origin_kind: 'agent',
    origin_id: 'a1',
    origin_ref: 'claude-code-1',
    resource_kind: 'resource',
    resource_ref: 'storefront-web',
  },
  {
    id: 'e2',
    origin_kind: 'agent',
    origin_id: 'a1',
    origin_ref: 'claude-code-1',
    resource_kind: 'resource',
    resource_ref: 'data-pipelines',
  },
  {
    id: 'e3',
    origin_kind: 'agent',
    origin_id: 'a1',
    origin_ref: 'claude-code-1',
    resource_kind: 'mcp_server',
    resource_ref: 'github',
  },
  {
    id: 'e4',
    origin_kind: 'agent',
    origin_id: 'a1',
    origin_ref: 'claude-code-1',
    resource_kind: 'mcp_server',
    resource_ref: 'postgres-analytics',
  },
  {
    id: 'e5',
    origin_kind: 'agent',
    origin_id: 'a1',
    origin_ref: 'claude-code-1',
    resource_kind: 'provider',
    resource_ref: 'api.anthropic.com',
  },
  {
    id: 'e6',
    origin_kind: 'agent',
    origin_id: 'a2',
    origin_ref: 'codex-1',
    resource_kind: 'resource',
    resource_ref: 'payments-api',
  },
  {
    id: 'e7',
    origin_kind: 'agent',
    origin_id: 'a2',
    origin_ref: 'codex-1',
    resource_kind: 'provider',
    resource_ref: 'api.openai.com',
  },
  {
    id: 'e8',
    origin_kind: 'agent',
    origin_id: 'a3',
    origin_ref: 'opencode-1',
    resource_kind: 'resource',
    resource_ref: 'docs-site',
  },
  {
    id: 'e9',
    origin_kind: 'agent',
    origin_id: 'a3',
    origin_ref: 'opencode-1',
    resource_kind: 'provider',
    resource_ref: 'api.anthropic.com',
  },
  // A tool-level observation is not a destination family.
  {
    id: 'e11',
    origin_kind: 'agent',
    origin_id: 'a1',
    origin_ref: 'claude-code-1',
    resource_kind: 'mcp.tool',
    resource_ref: 'github/search-issues',
  },
]

const FIXTURE_GRAPH: ReachGraphResponse = {
  edges: FIXTURE_EDGES,
  has_more: false,
}

const FIXTURE_APPROVALS: ReachApproval[] = [
  // The two waiting destinations of the demo estate.
  {
    id: 'ap1',
    action: 'mcp.enable',
    subject_kind: 'mcp_server',
    subject_ref: 'issue-tracker',
    status: 'pending',
  },
  {
    id: 'ap2',
    action: 'workspace.write',
    subject_kind: 'workspace',
    subject_ref: 'payments-api',
    status: 'pending',
  },
  // Not pending — an approved request must not dash anything.
  {
    id: 'ap3',
    action: 'workspace.write',
    subject_kind: 'workspace',
    subject_ref: 'data-pipelines',
    status: 'approved',
  },
  // Pending, but the subject is not a destination family (deployment, audit).
  {
    id: 'ap4',
    action: 'deploy.promote',
    subject_kind: 'deployment',
    subject_ref: 'payments-api/production',
    status: 'pending',
  },
  {
    id: 'ap5',
    action: 'evidence.export',
    subject_kind: 'audit',
    subject_ref: 'q3-access-review',
    status: 'pending',
  },
]

describe('buildReach', () => {
  it('lists exactly the agents that have family edges, named by the agents join, sorted by name', () => {
    const m = buildReach(FIXTURE_GRAPH, FIXTURE_AGENTS, FIXTURE_APPROVALS, [])
    expect(m.tools.map((t) => t.name)).toEqual([
      'Claude Code',
      'Codex',
      'OpenCode',
    ])
    // Gemini CLI has no edges — it reaches nothing, so it is not a row of this graph.
    expect(m.tools.some((t) => t.name === 'Gemini CLI')).toBe(false)
  })

  it('projects destinations for the three families in family order, sorted by ref', () => {
    const m = buildReach(FIXTURE_GRAPH, FIXTURE_AGENTS, FIXTURE_APPROVALS, [])
    expect(m.destinations.map((d) => d.ref)).toEqual([
      'data-pipelines',
      'docs-site',
      'payments-api',
      'storefront-web',
      'github',
      'issue-tracker',
      'postgres-analytics',
      'api.anthropic.com',
      'api.openai.com',
    ])
    expect(m.destinations.map((d) => d.family)).toEqual([
      'folders',
      'folders',
      'folders',
      'folders',
      'mcp',
      'mcp',
      'mcp',
      'providers',
      'providers',
    ])
  })

  it('matches the access map edge for edge: one solid link per agent-origin edge in a family, keyed by the canonical agent id', () => {
    const m = buildReach(FIXTURE_GRAPH, FIXTURE_AGENTS, FIXTURE_APPROVALS, [])
    const key = (l: { toolId: string; destinationId: string }) =>
      `${l.toolId}->${l.destinationId}`
    const links = new Set(m.links.map(key))
    expect(links).toEqual(
      new Set([
        'a1->resource\u0000storefront-web',
        'a1->resource\u0000data-pipelines',
        'a1->mcp_server\u0000github',
        'a1->mcp_server\u0000postgres-analytics',
        'a1->provider\u0000api.anthropic.com',
        'a2->resource\u0000payments-api',
        'a2->provider\u0000api.openai.com',
        'a3->resource\u0000docs-site',
        'a3->provider\u0000api.anthropic.com',
      ]),
    )
    // A non-family edge never becomes a link.
    expect(m.links.some((l) => l.id === 'e11')).toBe(false)
  })

  it('joins the tool by the canonical origin_id, never by the settable external_id', () => {
    // external_id is NOT unique and is settable on create: an attacker-controlled
    // agent could adopt another tool's external id. origin_id is the engine's
    // resolved agent id and must win.
    const agents: ReachTool[] = [
      {
        id: 'a1',
        name: 'Claude Code',
        kind: 'claude-code',
        external_id: 'claude-code-1',
      },
      {
        id: 'b1',
        name: 'Forged',
        kind: 'claude-code',
        external_id: 'claude-code-1',
      },
    ]
    const graph: ReachGraphResponse = {
      edges: [
        {
          id: 'f1',
          origin_kind: 'agent',
          origin_id: 'a1',
          origin_ref: 'claude-code-1',
          resource_kind: 'resource',
          resource_ref: 'infra',
        },
      ],
      has_more: false,
    }
    const m = buildReach(graph, agents, [], [])
    expect(m.tools.map((t) => t.name)).toEqual(['Claude Code'])
    expect(m.links[0].toolId).toBe('a1')
  })

  it('falls back to the external_id join only when the edge lacks origin_id', () => {
    const graph: ReachGraphResponse = {
      edges: [
        {
          id: 'g1',
          origin_kind: 'agent',
          origin_ref: 'codex-1',
          resource_kind: 'resource',
          resource_ref: 'infra',
        },
      ],
      has_more: false,
    }
    const m = buildReach(graph, FIXTURE_AGENTS, [], [])
    expect(m.tools.map((t) => t.name)).toEqual(['Codex'])
  })

  it('never gives an edge whose origin_id is off the agents page to an agent claiming its external_id', () => {
    // The real origin (a1) is beyond the fetched page; a forged agent adopted its
    // external id. The edge stays the unknown origin's, named by its ref.
    const forged: ReachTool[] = [
      {
        id: 'b1',
        name: 'Forged',
        kind: 'claude-code',
        external_id: 'claude-code-1',
      },
    ]
    const graph: ReachGraphResponse = {
      edges: [
        {
          id: 'f2',
          origin_kind: 'agent',
          origin_id: 'a1',
          origin_ref: 'claude-code-1',
          resource_kind: 'resource',
          resource_ref: 'infra',
        },
      ],
      has_more: false,
    }
    const m = buildReach(graph, forged, [], [])
    expect(m.tools.map((t) => [t.id, t.name])).toEqual([
      ['a1', 'claude-code-1'],
    ])
    expect(m.links[0].toolId).toBe('a1')
  })

  it('names an origin agent the list does not carry by its redacted ref instead of dropping the edge', () => {
    const graph: ReachGraphResponse = {
      edges: [
        {
          id: 'x1',
          origin_kind: 'agent',
          origin_id: 'zz1',
          origin_ref: 'unknown-ext',
          resource_kind: 'resource',
          resource_ref: 'infra',
        },
      ],
      has_more: false,
    }
    const m = buildReach(graph, FIXTURE_AGENTS, [], [])
    expect(m.tools.map((t) => t.name)).toEqual(['unknown-ext'])
    expect(m.links).toHaveLength(1)
  })

  it('draws one link per tool and destination when the map holds an observed and a granted edge', () => {
    // The access map keeps one edge per signal (seed.go: the coder agent's
    // observed and policy-granted reads of the same table); the view shows no
    // signal, so two edges between the same pair are one line and one row.
    const edge = {
      origin_kind: 'agent',
      origin_id: 'a1',
      origin_ref: 'claude-code-1',
      resource_kind: 'resource',
      resource_ref: 'infra',
    }
    const m = buildReach(
      {
        edges: [
          { ...edge, id: 'o1' },
          { ...edge, id: 'p1' },
        ],
        has_more: false,
      },
      FIXTURE_AGENTS,
      [],
      [],
    )
    expect(m.links).toEqual([
      { id: 'o1', toolId: 'a1', destinationId: m.destinations[0].id },
    ])
  })

  it('keeps the observed mcp.server spelling in the MCP family', () => {
    // Observed telemetry spells the kind mcp.server (the seeded graph); the
    // sourcescope canonical kind is mcp_server. Both are MCP servers.
    const graph: ReachGraphResponse = {
      edges: [
        {
          id: 'h1',
          origin_kind: 'agent',
          origin_id: 'a1',
          origin_ref: 'claude-code-1',
          resource_kind: 'mcp.server',
          resource_ref: 'github',
        },
      ],
      has_more: false,
    }
    const m = buildReach(graph, FIXTURE_AGENTS, [], [])
    expect(m.destinations).toHaveLength(1)
    expect(m.destinations[0].family).toBe('mcp')
    expect(m.destinations[0].ref).toBe('github')
  })

  it('marks a destination WAITING when a pending approval names it, and reached-and-waiting stays honest', () => {
    const m = buildReach(FIXTURE_GRAPH, FIXTURE_AGENTS, FIXTURE_APPROVALS, [])
    const byRef = new Map(m.destinations.map((d) => [d.ref, d]))
    // issue-tracker has no edge: the pending mcp.enable approval is its only appearance — waiting, not reached.
    expect(byRef.get('issue-tracker')?.waiting).toBe(true)
    // payments-api IS reached (Codex) AND has a pending write request: still waiting-marked, links stay solid.
    expect(byRef.get('payments-api')?.waiting).toBe(true)
    // An approved request (data-pipelines) marks nothing.
    expect(byRef.get('data-pipelines')?.waiting).toBe(false)
    // A pending approval for a non-family subject (deployment, audit) marks nothing.
    expect(
      m.destinations.some((d) => d.ref === 'payments-api/production'),
    ).toBe(false)
    expect(m.destinations.some((d) => d.ref === 'q3-access-review')).toBe(false)
  })

  it('shows a new network request dashed among the endpoints until it is approved', () => {
    const request: ReachApproval = {
      id: 'n1',
      action: 'network.connect',
      subject_kind: 'network_destination',
      subject_ref: 'registry.terraform.io',
      status: 'pending',
    }
    const waiting = buildReach(FIXTURE_GRAPH, FIXTURE_AGENTS, [request], [])
    const dest = waiting.destinations.find(
      (d) => d.ref === 'registry.terraform.io',
    )
    expect(dest).toMatchObject({ family: 'providers', waiting: true })
    expect(waiting.linkedDestIds.has(dest!.id)).toBe(false)
    // Approved: the request leaves the pending queue and nothing is dashed.
    const approved = buildReach(
      FIXTURE_GRAPH,
      FIXTURE_AGENTS,
      [{ ...request, status: 'approved' }],
      [],
    )
    expect(
      approved.destinations.some((d) => d.ref === 'registry.terraform.io'),
    ).toBe(false)
  })

  it('dashes a pending request on an endpoint the tools already reach, keeping its solid links', () => {
    const m = buildReach(
      FIXTURE_GRAPH,
      FIXTURE_AGENTS,
      [
        {
          id: 'n2',
          action: 'network.connect',
          subject_kind: 'network_destination',
          subject_ref: 'api.anthropic.com',
          status: 'pending',
        },
      ],
      [],
    )
    const dest = m.destinations.filter((d) => d.ref === 'api.anthropic.com')
    expect(dest).toHaveLength(1)
    expect(dest[0]).toMatchObject({ family: 'providers', waiting: true })
    expect(m.linkedDestIds.has(dest[0].id)).toBe(true)
  })

  it('a rejected request never dashes its destination', () => {
    const m = buildReach(
      FIXTURE_GRAPH,
      FIXTURE_AGENTS,
      [
        {
          id: 'r1',
          action: 'workspace.write',
          subject_kind: 'workspace',
          subject_ref: 'payments-api',
          status: 'rejected',
        },
      ],
      [],
    )
    const payments = m.destinations.find((d) => d.ref === 'payments-api')
    expect(payments?.waiting).toBe(false)
  })

  it('keeps a folder and an MCP server with the same ref as two destinations', () => {
    const graph: ReachGraphResponse = {
      edges: [
        {
          id: 'c1',
          origin_kind: 'agent',
          origin_id: 'a1',
          origin_ref: 'claude-code-1',
          resource_kind: 'resource',
          resource_ref: 'github',
        },
        {
          id: 'c2',
          origin_kind: 'agent',
          origin_id: 'a1',
          origin_ref: 'claude-code-1',
          resource_kind: 'mcp_server',
          resource_ref: 'github',
        },
      ],
      has_more: false,
    }
    const m = buildReach(graph, FIXTURE_AGENTS, [], [])
    expect(m.destinations.map((d) => d.ref)).toEqual(['github', 'github'])
    expect(m.destinations.map((d) => d.family)).toEqual(['folders', 'mcp'])
  })

  it('lists a waiting destination with no solid link (a new request) with an empty link set', () => {
    const m = buildReach(FIXTURE_GRAPH, FIXTURE_AGENTS, FIXTURE_APPROVALS, [])
    const waiting = m.destinations.find((d) => d.ref === 'issue-tracker')
    expect(waiting).toBeDefined()
    expect(m.links.some((l) => l.destinationId === waiting!.id)).toBe(false)
  })

  it('a waiting-only destination survives a graph with no edges at all', () => {
    const m = buildReach(
      { edges: [], has_more: false },
      FIXTURE_AGENTS,
      [
        {
          id: 'w1',
          action: 'mcp.enable',
          subject_kind: 'mcp_server',
          subject_ref: 'issue-tracker',
          status: 'pending',
        },
      ],
      [],
    )
    expect(m.destinations.map((d) => d.ref)).toEqual(['issue-tracker'])
    expect(m.destinations[0].waiting).toBe(true)
    expect(m.links).toEqual([])
    expect(m.linkedDestIds.has(m.destinations[0].id)).toBe(false)
  })

  it('linkedDestIds names exactly the destinations a tool line lands on', () => {
    const m = buildReach(FIXTURE_GRAPH, FIXTURE_AGENTS, FIXTURE_APPROVALS, [])
    const byRef = new Map(m.destinations.map((d) => [d.ref, d]))
    expect(m.linkedDestIds.has(byRef.get('payments-api')!.id)).toBe(true)
    expect(m.linkedDestIds.has(byRef.get('github')!.id)).toBe(true)
    expect(m.linkedDestIds.has(byRef.get('issue-tracker')!.id)).toBe(false)
    expect(m.linkedDestIds.size).toBe(8)
  })

  it("puts a session's reach under the tool it runs, beside the agents", () => {
    const graph: ReachGraphResponse = {
      edges: [
        {
          id: 'se1',
          origin_kind: 'session',
          origin_id: 'sid-1',
          origin_ref: 'sess-1',
          resource_kind: 'resource',
          resource_ref: 'infra',
        },
      ],
      has_more: false,
    }
    const m = buildReach(
      graph,
      FIXTURE_AGENTS,
      [],
      [{ session_ref: 'sess-1', live_ref: 'live-1', engine: 'opencode' }],
    )
    expect(m.tools.map((t) => t.name)).toEqual(['OpenCode'])
    expect(m.links).toEqual([
      {
        id: 'se1',
        toolId: m.tools[0]!.id,
        destinationId: 'resource\u0000infra',
      },
    ])
    expect(m.sessionsByRef).toBe(false)
  })

  it('names a session by its ref when two live rows give it different drivers', () => {
    const graph: ReachGraphResponse = {
      edges: [
        {
          id: 'se2',
          origin_kind: 'session',
          origin_id: 'sid-2',
          origin_ref: 'sess-2',
          resource_kind: 'mcp_server',
          resource_ref: 'github',
        },
      ],
      has_more: false,
    }
    const m = buildReach(
      graph,
      [],
      [],
      [
        { session_ref: 'sess-2', live_ref: 'live-a', engine: 'claude' },
        { session_ref: 'sess-2', live_ref: 'live-b', engine: 'codex' },
      ],
    )
    expect(m.tools.map((t) => t.name)).toEqual(['sess-2'])
    expect(m.sessionsByRef).toBe(true)
  })

  it('joins a session by its live_ref, and a row with no driver never hides a declared one', () => {
    const graph: ReachGraphResponse = {
      edges: [
        {
          id: 'se4',
          origin_kind: 'session',
          origin_id: 'sid-4',
          origin_ref: 'live-4',
          resource_kind: 'resource',
          resource_ref: 'infra',
        },
        {
          id: 'se5',
          origin_kind: 'session',
          origin_id: 'sid-5',
          origin_ref: 'sess-5',
          resource_kind: 'resource',
          resource_ref: 'docs-site',
        },
      ],
      has_more: false,
    }
    const m = buildReach(
      graph,
      [],
      [],
      [
        { session_ref: 'sess-4', live_ref: 'live-4', engine: 'codex' },
        // The declared driver first: a later row with none must not undo it.
        { session_ref: 'sess-5', live_ref: 'live-5b', engine: 'claude' },
        { session_ref: 'sess-5', live_ref: 'live-5a' },
      ],
    )
    expect(m.tools.map((t) => t.name)).toEqual(['Claude Code', 'Codex'])
    expect(m.sessionsByRef).toBe(false)
  })

  it('keeps the solid line when the session that asked already reaches the destination', () => {
    const graph: ReachGraphResponse = {
      edges: [
        {
          id: 'se3',
          origin_kind: 'session',
          origin_id: 'sid-3',
          origin_ref: 'sess-3',
          resource_kind: 'mcp_server',
          resource_ref: 'github',
        },
      ],
      has_more: false,
    }
    const m = buildReach(
      graph,
      [],
      [
        {
          id: 'w3',
          action: 'mcp.enable',
          subject_kind: 'mcp_server',
          subject_ref: 'github',
          session_ref: 'sess-3',
          status: 'pending',
        },
      ],
      [{ session_ref: 'sess-3', engine: 'claude' }],
    )
    expect(m.links).toHaveLength(1)
    expect(m.waitingLinks).toEqual([])
    expect(m.destinations[0]).toMatchObject({ ref: 'github', waiting: true })
  })

  it('dashes a request from the session named by its ref when its tool is unknown, and says so', () => {
    const m = buildReach(
      { edges: [], has_more: false },
      [],
      [
        {
          id: 'w6',
          action: 'network.connect',
          subject_kind: 'network_destination',
          subject_ref: 'registry.terraform.io',
          session_ref: 'sess-6',
          status: 'pending',
        },
      ],
      [],
    )
    expect(m.tools.map((t) => t.name)).toEqual(['sess-6'])
    expect(m.waitingLinks).toEqual([
      {
        id: 'w6',
        toolId: m.tools[0]!.id,
        destinationId: 'provider\u0000registry.terraform.io',
      },
    ])
    expect(m.sessionsByRef).toBe(true)
  })

  it('draws one dashed line when a tool asks twice for the same destination', () => {
    const ask = (id: string, session_ref: string): ReachApproval => ({
      id,
      action: 'network.connect',
      subject_kind: 'network_destination',
      subject_ref: 'registry.terraform.io',
      session_ref,
      status: 'pending',
    })
    const m = buildReach(
      { edges: [], has_more: false },
      [],
      [ask('w7', 'sess-7a'), ask('w8', 'sess-7b')],
      [
        { session_ref: 'sess-7a', engine: 'claude' },
        { session_ref: 'sess-7b', engine: 'claude' },
      ],
    )
    expect(m.tools.map((t) => t.name)).toEqual(['Claude Code'])
    expect(m.waitingLinks.map((l) => l.id)).toEqual(['w7'])
  })

  it('never makes an identity origin a tool', () => {
    const graph: ReachGraphResponse = {
      edges: [
        {
          id: 'id1',
          origin_kind: 'identity',
          origin_id: 'nhi-1',
          origin_ref: 'etl_role',
          resource_kind: 'resource',
          resource_ref: 'infra',
        },
      ],
      has_more: false,
    }
    const m = buildReach(graph, [], [], [])
    expect(m.tools).toEqual([])
    expect(m.destinations).toEqual([])
  })

  it('passes has_more through so the view can label a partial graph', () => {
    const m = buildReach(
      { ...FIXTURE_GRAPH, has_more: true },
      FIXTURE_AGENTS,
      FIXTURE_APPROVALS,
      [],
    )
    expect(m.hasMore).toBe(true)
  })
})

describe('the seeded demo estate', () => {
  // `serve --seed-demo` (scripts/web-e2e-demo.sh): the access-map edges of
  // cmd/olivares/seed/seed.go Observations, as the graph read returns them. The
  // agents reach only postgres.table and s3.bucket; the MCP reach is the
  // sessions' (seed.go:437,441), and both sessions run Claude Code (engine
  // "claude", seed.go TeamSessions).
  const seededEdge = (
    id: string,
    originKind: string,
    originRef: string,
    resourceKind: string,
    resourceRef: string,
  ): ReachEdge => ({
    id,
    origin_kind: originKind,
    origin_id: `id-${originRef}`,
    origin_ref: originRef,
    resource_kind: resourceKind,
    resource_ref: resourceRef,
  })
  const seededEdges: ReachEdge[] = [
    seededEdge(
      's1',
      'agent',
      'agent-claude-coder-7',
      'postgres.table',
      'appdb.public.customers',
    ),
    seededEdge(
      's2',
      'agent',
      'agent-claude-coder-7',
      'postgres.table',
      'appdb.public.orders',
    ),
    seededEdge('s3', 'agent', 'agent-claude-coder-7', 's3.bucket', 'exports'),
    seededEdge(
      's4',
      'agent',
      'agent-claude-coder-7',
      'postgres.table',
      'appdb.public.secrets',
    ),
    seededEdge(
      's5',
      'identity',
      'shared-pool',
      'postgres.table',
      'appdb.public.logs',
    ),
    seededEdge(
      's6',
      'agent',
      'agent-claude-index-9',
      'postgres.table',
      'appdb.public.billing',
    ),
    seededEdge(
      's7',
      'agent',
      'agent-claude-review-3',
      'postgres.table',
      'appdb.public.reviews',
    ),
    seededEdge(
      's8',
      'session',
      'sess-coder-7a3f',
      'agent.task',
      'worker-indexer',
    ),
    seededEdge('s9', 'session', 'sess-coder-7a3f', 'mcp.server', 'github'),
    seededEdge(
      's10',
      'session',
      'sess-coder-7a3f',
      'mcp.tool',
      'github/create_issue',
    ),
    seededEdge('s11', 'session', 'sess-coder-9c21', 'mcp.server', 'filesystem'),
    seededEdge(
      's12',
      'identity',
      'etl_role',
      'postgres.table',
      'appdb.public.billing',
    ),
    seededEdge(
      's13',
      'identity',
      'app_role',
      'postgres.table',
      'appdb.public.archive',
    ),
  ]
  // seed.go Agents(), as GET /v1/agents returns them.
  const seededAgents: ReachTool[] = [
    {
      id: 'id-agent-claude-coder-7',
      name: 'Claude Code — billing service',
      kind: 'claude_code',
      external_id: 'agent-claude-coder-7',
    },
    {
      id: 'id-agent-claude-review-3',
      name: 'Claude Code — PR reviewer',
      kind: 'claude_code',
      external_id: 'agent-claude-review-3',
    },
    {
      id: 'id-agent-claude-index-9',
      name: 'Claude Code — repo indexer',
      kind: 'claude_code',
      external_id: 'agent-claude-index-9',
    },
  ]
  // seed.go TeamSessions(), as the live-sessions page returns them.
  const seededSessions: ReachSession[] = [
    { session_ref: 'sess-coder-7a3f', live_ref: 'live-1', engine: 'claude' },
    { session_ref: 'sess-codex-4b8d', live_ref: 'live-2', engine: 'codex' },
    { session_ref: 'sess-oc-1f62', live_ref: 'live-3', engine: 'opencode' },
    { session_ref: 'sess-coder-9c21', live_ref: 'live-6', engine: 'claude' },
  ]
  // The request the real-use spec opens (e2e/reach-waiting.spec.ts), beside the
  // queue's non-reach subjects (scripts/seed-demo-dataset.py PENDING_APPROVALS).
  const seededApprovals: ReachApproval[] = [
    {
      id: 'p1',
      action: 'db.migrate',
      subject_kind: 'database',
      subject_ref: 'payments-staging',
      status: 'pending',
    },
    {
      id: 'p2',
      action: 'network.connect',
      subject_kind: 'network_destination',
      subject_ref: 'registry.terraform.io',
      session_ref: 'sess-coder-7a3f',
      status: 'pending',
    },
    {
      id: 'p3',
      action: 'github.write',
      subject_kind: 'repository',
      subject_ref: 'storefront-web#482',
      status: 'pending',
    },
  ]

  it("draws the MCP servers the access map shows solid under the sessions' tool, and the request dashed from it", () => {
    const m = buildReach(
      { edges: seededEdges, has_more: false },
      seededAgents,
      seededApprovals,
      seededSessions,
    )
    expect(m.tools.map((t) => t.name)).toEqual(['Claude Code'])
    const refOf = new Map(m.destinations.map((d) => [d.id, d.ref]))
    expect(m.links.map((l) => refOf.get(l.destinationId)).sort()).toEqual([
      'filesystem',
      'github',
    ])
    expect(
      m.waitingLinks.map((l) => [l.toolId, refOf.get(l.destinationId)]),
    ).toEqual([[m.tools[0]!.id, 'registry.terraform.io']])
    expect(m.destinations.map((d) => [d.ref, d.family, d.waiting])).toEqual([
      ['filesystem', 'mcp', false],
      ['github', 'mcp', false],
      ['registry.terraform.io', 'providers', true],
    ])
  })

  it('names a session the live page does not carry by its ref, never dropping its reach', () => {
    const m = buildReach(
      { edges: seededEdges, has_more: false },
      seededAgents,
      seededApprovals,
      [],
    )
    expect(m.tools.map((t) => t.name)).toEqual([
      'sess-coder-7a3f',
      'sess-coder-9c21',
    ])
    expect(m.links).toHaveLength(2)
    expect(m.waitingLinks).toHaveLength(1)
  })
})

describe('familyOrder', () => {
  it('orders folders before MCP servers before providers', () => {
    const fams = ['providers', 'folders', 'mcp'] as const
    expect([...fams].sort((a, b) => familyOrder(a) - familyOrder(b))).toEqual([
      'folders',
      'mcp',
      'providers',
    ])
  })
})
