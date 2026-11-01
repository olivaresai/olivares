// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Behavioral tests of the Reach view (issue #2553): the two-column graph matches
// the access map for the demo estate, a destination a pending approval names is
// rendered WAITING (dashed, clock), the table alternative carries the same data,
// and the rendered DOM passes axe. The graph column lists are aria-hidden — the
// role="img" label is the accessible answer, so assertions read both.
//
// The shared approval fixture is synthetic: two pending reach requests
// (workspace.write payments-api, mcp.enable issue-tracker), one approved, one
// rejected and one non-family pending — only the two pending reach requests may
// dash anything. The network-request tests use the demo estate's own pending row
// (scripts/seed-demo-dataset.py PENDING_APPROVALS: registry.terraform.io).
import axe from 'axe-core'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createQueryClient } from '@/lib/api/query'
import { createTestQueryClient, renderIntel } from '@/test/intel'

const perm = { approvals: true, sessions: true }
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (p: string) =>
      p === 'governance:approval:read'
        ? perm.approvals
        : p === 'sessions:live:read'
          ? perm.sessions
          : true,
  }),
}))

// The view is in the address (?view=table): the router answers with the real URL, and a
// navigation writes it there, so a test reads back what a pasted link would carry.
const navigate = vi.hoisted(() =>
  vi.fn((opts: { search: (cur: Record<string, unknown>) => object }) => {
    const cur = Object.fromEntries(new URLSearchParams(window.location.search))
    const next = new URLSearchParams()
    for (const [k, v] of Object.entries(opts.search(cur)))
      if (v !== undefined) next.set(k, String(v))
    const q = next.toString()
    window.history.replaceState(null, '', q ? `/reach?${q}` : '/reach')
    return Promise.resolve()
  }),
)
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigate,
  useRouterState: ({
    select,
  }: {
    select: (s: { location: { searchStr: string } }) => unknown
  }) => select({ location: { searchStr: window.location.search } }),
}))

const api = vi.hoisted(() => ({
  graph: vi.fn(),
  agents: vi.fn(),
  sessions: vi.fn(),
  pendingApprovals: vi.fn(),
}))
vi.mock('./api', () => ({
  reachApi: api,
  reachKeys: {
    graph: (t: string | null) => ['reach', t, 'graph'],
    agents: (t: string | null) => ['reach', t, 'agents'],
    approvals: (t: string | null) => ['reach', t, 'approvals'],
    sessions: (t: string | null) => ['reach', t, 'sessions'],
  },
}))

import ReachView from './reach-view'

const GRAPH = {
  edges: [
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
      resource_kind: 'mcp_server',
      resource_ref: 'github',
    },
    {
      id: 'e3',
      origin_kind: 'agent',
      origin_id: 'a2',
      origin_ref: 'codex-1',
      resource_kind: 'resource',
      resource_ref: 'payments-api',
    },
    {
      id: 'e4',
      origin_kind: 'agent',
      origin_id: 'a2',
      origin_ref: 'codex-1',
      resource_kind: 'provider',
      resource_ref: 'api.openai.com',
    },
  ],
  has_more: false,
}
const AGENTS = {
  items: [
    {
      id: 'a1',
      name: 'Claude Code',
      kind: 'claude-code',
      external_id: 'claude-code-1',
    },
    { id: 'a2', name: 'Codex', kind: 'codex', external_id: 'codex-1' },
  ],
}
const APPROVALS = {
  items: [
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
    // Seed-shaped decided and non-family rows: none of them may dash anything.
    {
      id: 'ap3',
      action: 'workspace.write',
      subject_kind: 'workspace',
      subject_ref: 'data-pipelines',
      status: 'approved',
    },
    {
      id: 'ap4',
      action: 'evidence.export',
      subject_kind: 'audit',
      subject_ref: 'q3-access-review',
      status: 'rejected',
    },
    {
      id: 'ap5',
      action: 'deploy.promote',
      subject_kind: 'deployment',
      subject_ref: 'payments-api/production',
      status: 'pending',
    },
  ],
}

// The seeded estate's MCP reach (cmd/olivares/seed/seed.go:437,441): session
// origins, both sessions running Claude Code, and the request one of them opened.
const SESSION_GRAPH = {
  edges: [
    {
      id: 's9',
      origin_kind: 'session',
      origin_id: 'id-sess-coder-7a3f',
      origin_ref: 'sess-coder-7a3f',
      resource_kind: 'mcp.server',
      resource_ref: 'github',
    },
    {
      id: 's11',
      origin_kind: 'session',
      origin_id: 'id-sess-coder-9c21',
      origin_ref: 'sess-coder-9c21',
      resource_kind: 'mcp.server',
      resource_ref: 'filesystem',
    },
  ],
  has_more: false,
}
const SESSIONS = {
  items: [
    { session_ref: 'sess-coder-7a3f', live_ref: 'l1', engine: 'claude' },
    { session_ref: 'sess-coder-9c21', live_ref: 'l2', engine: 'claude' },
  ],
}
const SESSION_REQUEST = {
  items: [
    {
      id: 'n1',
      action: 'network.connect',
      subject_kind: 'network_destination',
      subject_ref: 'registry.terraform.io',
      session_ref: 'sess-coder-7a3f',
      status: 'pending',
    },
  ],
}

beforeEach(() => {
  window.history.replaceState(null, '', '/reach')
  navigate.mockClear()
  perm.approvals = true
  perm.sessions = true
  api.graph.mockReset().mockResolvedValue(GRAPH)
  api.agents.mockReset().mockResolvedValue(AGENTS)
  api.sessions.mockReset().mockResolvedValue({ items: [] })
  api.pendingApprovals.mockReset().mockResolvedValue(APPROVALS)
})

describe('ReachView graph', () => {
  it('counts destinations and waiting requests in the header meta', async () => {
    // 4 destinations reached by edges + the waiting-only issue-tracker from the
    // pending approval; payments-api is both reached and waiting.
    renderIntel(<ReachView />)
    expect(
      await screen.findByText('5 destinations, 2 waiting'),
    ).toBeInTheDocument()
  })

  it('renders the two-column graph with an accessible label naming tools, reach and the waiting set', async () => {
    const { container } = renderIntel(<ReachView />)
    const img = await screen.findByRole('img', { name: /Reach graph/ })
    // Exact label: every spoken destination names its kind, and a waiting
    // destination is announced as waiting, never as reached.
    expect(img.getAttribute('aria-label')).toBe(
      'Reach graph. Claude Code reaches storefront-web (Folder), github (MCP server). ' +
        'Codex reaches payments-api (Folder), api.openai.com (Network endpoint). ' +
        'Waiting for approval: issue-tracker (MCP server), payments-api (Folder).',
    )
    // The two columns carry the same data the label speaks.
    const tools = container.querySelectorAll('[data-tool]')
    expect([...tools].map((el) => el.textContent)).toEqual([
      'Claude Code',
      'Codex',
    ])
    const dests = container.querySelectorAll('[data-dest]')
    expect([...dests].map((el) => el.getAttribute('data-dest'))).toEqual([
      'payments-api',
      'storefront-web',
      'github',
      'issue-tracker',
      'api.openai.com',
    ])
  })

  it('renders every waiting destination dashed and every reached destination solid', async () => {
    const { container } = renderIntel(<ReachView />)
    await screen.findByRole('img', { name: /Reach graph/ })
    // The waiting-only destination gets THE dashed stub from the mid rail.
    const stubs = container.querySelectorAll('path.edge.is-wait')
    expect(stubs).toHaveLength(1)
    expect(stubs[0].getAttribute('stroke-dasharray')).toBe('4 3')
    // The four reached links stay solid.
    expect(container.querySelectorAll('path.edge:not(.is-wait)')).toHaveLength(
      4,
    )
    // issue-tracker: waiting-only — dashed stub, dashed underline, clock.
    const issue = container.querySelector('[data-dest="issue-tracker"]')!
    expect(issue.classList.contains('is-wait')).toBe(true)
    expect(issue.querySelector('svg')).not.toBeNull() // the clock icon
    expect(issue.querySelector('span')!.className).toContain('border-dashed')
    // payments-api: reached AND waiting — solid edge (e3 above) plus the dashed
    // mark and clock; the reach is real, the new write request is pending.
    const payments = container.querySelector('[data-dest="payments-api"]')!
    expect(payments.classList.contains('is-wait')).toBe(true)
    expect(payments.querySelector('svg')).not.toBeNull()
    expect(payments.querySelector('span')!.className).toContain('border-dashed')
    // github: reached, nothing pending — no dash, no clock.
    const github = container.querySelector('[data-dest="github"]')!
    expect(github.classList.contains('is-wait')).toBe(false)
    expect(github.querySelector('svg')).toBeNull()
    expect(github.querySelector('span')!.className).not.toContain(
      'border-dashed',
    )
  })

  it('draws a new network request as a dashed endpoint until it is approved', async () => {
    const user = userEvent.setup()
    api.pendingApprovals.mockResolvedValue({
      items: [
        {
          id: 'n1',
          action: 'network.connect',
          subject_kind: 'network_destination',
          subject_ref: 'registry.terraform.io',
          status: 'pending',
        },
      ],
    })
    const { container } = renderIntel(
      <main>
        <ReachView />
      </main>,
    )
    await screen.findByRole('img', {
      name: /Waiting for approval: registry\.terraform\.io \(Network endpoint\)\./,
    })
    const row = container.querySelector('[data-dest="registry.terraform.io"]')!
    expect(row.classList.contains('is-wait')).toBe(true)
    expect(container.querySelectorAll('path.edge.is-wait')).toHaveLength(1)
    const graphAxe = await axe.run(document.body)
    expect(graphAxe.violations.map((v) => v.id)).toEqual([])
    await user.click(screen.getByRole('button', { name: 'Table' }))
    const waiting = within(await screen.findByRole('table'))
      .getByText('registry.terraform.io')
      .closest('tr')!
    expect(
      within(waiting)
        .getAllByRole('cell')
        .map((cell) => cell.textContent),
    ).toEqual([
      '—',
      'registry.terraform.io',
      'Network endpoint',
      'Waiting for approval',
    ])
  })

  it("draws session reach solid under the session's tool and its request dashed from that tool", async () => {
    const user = userEvent.setup()
    api.graph.mockResolvedValue(SESSION_GRAPH)
    api.agents.mockResolvedValue({ items: [] })
    api.sessions.mockResolvedValue(SESSIONS)
    api.pendingApprovals.mockResolvedValue(SESSION_REQUEST)
    const { container } = renderIntel(
      <main>
        <ReachView />
      </main>,
    )
    const img = await screen.findByRole('img', { name: /Reach graph/ })
    expect(img.getAttribute('aria-label')).toBe(
      'Reach graph. Claude Code reaches filesystem (MCP server), github (MCP server). ' +
        'Claude Code is waiting for approval to reach registry.terraform.io (Network endpoint).',
    )
    expect(
      [...container.querySelectorAll('[data-tool]')].map(
        (el) => el.textContent,
      ),
    ).toEqual(['Claude Code'])
    // Two solid MCP lines and one dashed line, all from the tool row (M0): no stub.
    expect(container.querySelectorAll('path.edge:not(.is-wait)')).toHaveLength(
      2,
    )
    const dashed = container.querySelectorAll('path.edge.is-wait')
    expect(dashed).toHaveLength(1)
    expect(dashed[0].getAttribute('d')).toMatch(/^M0 /)
    expect(dashed[0].getAttribute('stroke-dasharray')).toBe('4 3')
    expect(
      screen.queryByText('Some tool names could not be loaded.'),
    ).toBeNull()
    expect((await axe.run(document.body)).violations.map((v) => v.id)).toEqual(
      [],
    )
    await user.click(screen.getByRole('button', { name: 'Table' }))
    const rows = within(await screen.findByRole('table'))
      .getAllByRole('row')
      .slice(1)
      .map((row) =>
        within(row)
          .getAllByRole('cell')
          .map((cell) => cell.textContent),
      )
    expect(rows).toEqual([
      ['Claude Code', 'github', 'MCP server', 'Reached'],
      ['Claude Code', 'filesystem', 'MCP server', 'Reached'],
      [
        'Claude Code',
        'registry.terraform.io',
        'Network endpoint',
        'Waiting for approval',
      ],
    ])
  })

  it('waits for the live sessions before drawing, so no session ref flashes as a tool', async () => {
    api.graph.mockResolvedValue(SESSION_GRAPH)
    api.agents.mockResolvedValue({ items: [] })
    api.sessions.mockImplementation(() => new Promise(() => {}))
    renderIntel(<ReachView />)
    expect(await screen.findByText('Loading reach…')).toBeInTheDocument()
    expect(screen.queryByRole('img', { name: /Reach graph/ })).toBeNull()
    expect(screen.queryByText(/sess-coder/)).toBeNull()
  })

  it('names a session by its ref, and says so, when the live sessions cannot be read', async () => {
    perm.sessions = false
    api.graph.mockResolvedValue(SESSION_GRAPH)
    api.agents.mockResolvedValue({ items: [] })
    renderIntel(<ReachView />)
    const img = await screen.findByRole('img', { name: /Reach graph/ })
    expect(img.getAttribute('aria-label')).toBe(
      'Reach graph. sess-coder-7a3f reaches github (MCP server). ' +
        'sess-coder-9c21 reaches filesystem (MCP server). ' +
        'Waiting for approval: issue-tracker (MCP server), payments-api (Folder).',
    )
    expect(api.sessions).not.toHaveBeenCalled()
    expect(
      screen.getByText('Some tool names could not be loaded.'),
    ).toBeInTheDocument()
  })

  it('draws every line and the waiting underline in the text-2 tone (3:1 or more on both themes)', async () => {
    // graphite-400 is 2.7:1 on the light surface and border-line about 1.2:1: below
    // the 3:1 WCAG 1.4.11 asks of a graphic that carries meaning.
    const { container } = renderIntel(<ReachView />)
    await screen.findByRole('img', { name: /Reach graph/ })
    const svg = container.querySelector('svg.text-text-2')
    expect(svg).not.toBeNull()
    const paths = [...container.querySelectorAll('path.edge')]
    expect(paths.length).toBeGreaterThan(0)
    for (const path of paths)
      expect(path.getAttribute('stroke')).toBe('currentColor')
    const underline = container.querySelector(
      '[data-dest="issue-tracker"] span',
    )!
    expect(underline.className).toContain('border-text-2')
    expect(underline.className).not.toContain('border-line')
  })

  it('says "1 destination" for one destination', async () => {
    api.graph.mockResolvedValue({ edges: [], has_more: false })
    api.pendingApprovals.mockResolvedValue({
      items: [
        {
          id: 'n1',
          action: 'network.connect',
          subject_kind: 'network_destination',
          subject_ref: 'registry.terraform.io',
          status: 'pending',
        },
      ],
    })
    renderIntel(<ReachView />)
    expect(
      await screen.findByText('1 destination, 1 waiting'),
    ).toBeInTheDocument()
  })

  it('drops the dash when the request is approved and the person comes back to Reach', async () => {
    // The console's own client (30 s staleTime): the waiting set must not come
    // back from cache once the request has left the pending queue.
    const qc = createQueryClient()
    const pending = {
      id: 'n1',
      action: 'network.connect',
      subject_kind: 'network_destination',
      subject_ref: 'registry.terraform.io',
      status: 'pending',
    }
    api.pendingApprovals.mockResolvedValue({ items: [pending] })
    const first = renderIntel(<ReachView />, { queryClient: qc })
    expect(
      await screen.findByText('5 destinations, 1 waiting'),
    ).toBeInTheDocument()
    first.unmount()

    api.pendingApprovals.mockResolvedValue({ items: [] })
    const { container } = renderIntel(<ReachView />, { queryClient: qc })
    expect(
      await screen.findByText('4 destinations, 0 waiting'),
    ).toBeInTheDocument()
    expect(
      container.querySelector('[data-dest="registry.terraform.io"]'),
    ).toBeNull()
    qc.clear()
  })

  it('shows the two legend states: reached and waiting for approval', async () => {
    renderIntel(<ReachView />)
    expect(await screen.findByText('Reached')).toBeInTheDocument()
    expect(screen.getByText('Waiting for approval')).toBeInTheDocument()
  })

  it('labels a partial graph when the access map has more edges', async () => {
    api.graph.mockResolvedValue({ ...GRAPH, has_more: true })
    renderIntel(<ReachView />)
    expect(await screen.findByText(/Partial view/)).toBeInTheDocument()
  })

  it('passes axe with zero violations', async () => {
    const { container } = renderIntel(<ReachView />)
    await screen.findByRole('img', { name: /Reach graph/ })
    const results = await axe.run(container)
    expect(
      results.violations,
      `axe violations: ${results.violations
        .map((v) => `${v.id} ${v.help} (${v.nodes.length})`)
        .join('; ')}`,
    ).toEqual([])
  })
})

describe('ReachView table alternative', () => {
  it('toggles to a table with the same rows and honest waiting statuses', async () => {
    const user = userEvent.setup()
    // The shell mounts every view inside its <main> landmark; mirror that so the
    // page-level axe run (which requires content in landmarks) matches real usage.
    renderIntel(
      <main>
        <ReachView />
      </main>,
    )
    await screen.findByRole('img', { name: /Reach graph/ })

    await user.click(screen.getByRole('button', { name: 'Table' }))

    const table = await screen.findByRole('table')
    expect(within(table).getByText('Tool')).toBeInTheDocument()
    expect(within(table).getByText('Destination')).toBeInTheDocument()
    expect(within(table).getByText('Kind')).toBeInTheDocument()
    expect(within(table).getByText('Status')).toBeInTheDocument()

    // One row per solid link plus one honest row per waiting-only destination.
    const rows = within(table).getAllByRole('row').slice(1)
    const rowTexts = rows.map((row) =>
      within(row)
        .getAllByRole('cell')
        .map((cell) => cell.textContent),
    )
    expect(rowTexts).toEqual([
      ['Claude Code', 'storefront-web', 'Folder', 'Reached'],
      ['Claude Code', 'github', 'MCP server', 'Reached'],
      ['Codex', 'payments-api', 'Folder', 'Reached · request waiting'],
      ['Codex', 'api.openai.com', 'Network endpoint', 'Reached'],
      ['—', 'issue-tracker', 'MCP server', 'Waiting for approval'],
    ])
    const results = await axe.run(document.body)
    expect(results.violations).toEqual([])
    // The choice is in the address, so a pasted link opens the table.
    expect(window.location.search).toBe('?view=table')
    await user.click(screen.getByRole('button', { name: 'Graph' }))
    expect(window.location.search).toBe('')
  })

  it('opens on the table a link names, and says when the link names no view', async () => {
    window.history.replaceState(null, '', '/reach?view=table')
    const first = renderIntel(<ReachView />)
    expect(await screen.findByRole('table')).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Table', pressed: true }),
    ).toBeInTheDocument()
    first.unmount()

    window.history.replaceState(null, '', '/reach?view=pie')
    renderIntel(<ReachView />)
    expect(
      await screen.findByRole('img', { name: /Reach graph/ }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('table')).toBeNull()
    expect(
      await screen.findByText(
        /1 value in this link could not be used \(view\)/,
      ),
    ).toBeInTheDocument()
  })
})

describe('ReachView states', () => {
  it('renders the empty state when nothing has reached anything yet', async () => {
    api.graph.mockResolvedValue({ edges: [], has_more: false })
    api.pendingApprovals.mockResolvedValue({ items: [] })
    renderIntel(<ReachView />)
    expect(
      await screen.findByText('No reach recorded yet.'),
    ).toBeInTheDocument()
  })

  it('without approval read, the meta counts destinations without a waiting number and the queue is not polled', async () => {
    perm.approvals = false
    renderIntel(<ReachView />)
    expect(await screen.findByText('4 destinations')).toBeInTheDocument()
    expect(api.pendingApprovals).not.toHaveBeenCalled()
    // Every line solid is not "nothing waits": the view says the queue is not shown.
    expect(
      screen.getByText(
        'Waiting requests are not shown: you cannot read approvals.',
      ),
    ).toBeInTheDocument()
  })

  it('surfaces the approval read failure instead of pretending zero waiting', async () => {
    api.pendingApprovals.mockRejectedValue(new Error('boom'))
    renderIntel(<ReachView />)
    // Count-only meta (no waiting number to lie with) plus an explicit alert.
    expect(await screen.findByText('4 destinations')).toBeInTheDocument()
    expect(
      screen.getByText('Waiting requests could not be loaded.'),
    ).toBeInTheDocument()
  })

  it('does not claim an empty estate while the waiting read failed', async () => {
    api.graph.mockResolvedValue({ edges: [], has_more: false })
    api.pendingApprovals.mockRejectedValue(new Error('boom'))
    renderIntel(<ReachView />)
    expect(
      await screen.findByText('Waiting requests could not be loaded.'),
    ).toBeInTheDocument()
    expect(screen.getByText('No reach recorded yet.')).toBeInTheDocument()
  })

  it('surfaces the graph read failure instead of pretending an empty estate', async () => {
    api.graph.mockRejectedValue(new Error('boom'))
    renderIntel(<ReachView />)
    expect(
      await screen.findByText(/Could not load the reach graph/),
    ).toBeInTheDocument()
    expect(screen.queryByText(/destinations/)).toBeNull()
  })

  it('shows the loading state until the reads settle', async () => {
    api.graph.mockImplementation(() => new Promise(() => {}))
    renderIntel(<ReachView />)
    expect(screen.getByText('Loading reach…')).toBeInTheDocument()
    expect(screen.queryByText(/destinations/)).toBeNull()
  })

  it('waits for the tool names before drawing, so no raw ref stands in for a name', async () => {
    api.agents.mockImplementation(() => new Promise(() => {}))
    const qc = createTestQueryClient()
    renderIntel(<ReachView />, { queryClient: qc })
    // The graph and the waiting queue settle; only the names are still out.
    await waitFor(() =>
      expect(qc.getQueryState(['reach', 't1', 'graph'])?.status).toBe(
        'success',
      ),
    )
    await waitFor(() =>
      expect(qc.getQueryState(['reach', 't1', 'approvals'])?.status).toBe(
        'success',
      ),
    )
    expect(screen.getByText('Loading reach…')).toBeInTheDocument()
    expect(screen.queryByRole('img', { name: /Reach graph/ })).toBeNull()
  })

  it('labels an agents page cut: tool names may be missing', async () => {
    api.agents.mockResolvedValue({ items: AGENTS.items, has_more: true })
    renderIntel(<ReachView />)
    expect(
      await screen.findByText('Some tool names could not be loaded.'),
    ).toBeInTheDocument()
  })

  it('labels an approvals page cut: the waiting list may be incomplete', async () => {
    api.pendingApprovals.mockResolvedValue({
      items: APPROVALS.items,
      has_more: true,
    })
    renderIntel(<ReachView />)
    expect(
      await screen.findByText('The waiting list is incomplete.'),
    ).toBeInTheDocument()
  })
})
