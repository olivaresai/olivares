// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The Activity tab: what the session's tool is doing, as the engine read it from the tool's
// own transcript. The answer below is the engine's for the real Claude Code session in
// modules/sessions/testdata/claude-2.1.296-activity.jsonl, plus a task list.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: { activity: vi.fn(), activityAgent: vi.fn() },
}))

import { agentOpsApi } from '@/features/agentops/api'
import type { RunActivityDTO, RunDTO } from '@/features/agentops/types'
import { ApiError } from '@/lib/api/errors'
import i18n from '@/lib/i18n'
import { SessionActivity } from './session-activity'
import { en, es, zh, ja, de, ru, fr } from './i18n'

const run = {
  run_ref: 'run_1',
  state: 'running',
  provider_driver: 'claude',
  workspace_path: '/home/dev/demo',
} as unknown as RunDTO

const ACTIVITY: RunActivityDTO = {
  driver: 'claude',
  transcript: 'read',
  model: 'claude-haiku-5-5',
  effort: 'medium',
  context_tokens: 78582,
  compactions: [{ trigger: 'auto', pre_tokens: 180000, post_tokens: 21000 }],
  agents: [
    {
      tool_use_id: 'toolu_a',
      agent_id: 'a442c29bf1c636f28',
      type: 'general-purpose',
      description: 'Write agent-a.txt',
      state: 'completed',
      duration_ms: 5059,
      tokens: 72734,
      tool_uses: 1,
      model: 'claude-haiku-5-5',
      summary: 'done',
    },
    {
      tool_use_id: 'toolu_b',
      type: 'general-purpose',
      description: 'Write agent-b.txt',
      state: 'running',
      background: true,
    },
  ],
  tasks: [
    {
      tool_use_id: 'toolu_c',
      task_id: 'blu934u8g',
      kind: 'shell',
      description: 'Run sleep 5 then echo finished in background',
      command: 'sleep 5; echo finished',
      state: 'completed',
      summary:
        'Background command "Run sleep 5 then echo finished in background" completed (exit code 0)',
    },
  ],
  files: [
    { path: '/home/dev/demo/one.txt', added: 1, removed: 1, edits: 1 },
    { path: '/home/dev/demo/two.txt', added: 1, removed: 1, edits: 1 },
    {
      path: '/home/dev/demo/three.txt',
      added: 1,
      removed: 0,
      created: true,
      edits: 1,
    },
  ],
  todos: [
    { content: 'Edit the files', status: 'completed' },
    {
      content: 'Wait for the job',
      status: 'in_progress',
      active_form: 'Waiting for the job',
    },
    { content: 'Reply', status: 'pending' },
  ],
  skills: ['tdd-workflow'],
  mcp_servers: ['github'],
}

function show(onOpenChanges?: () => void, shown: RunDTO = run) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const view = render(
    <QueryClientProvider client={qc}>
      <SessionActivity run={shown} onOpenChanges={onOpenChanges} />
    </QueryClientProvider>,
  )
  return {
    rerender: (next: RunDTO) =>
      view.rerender(
        <QueryClientProvider client={qc}>
          <SessionActivity run={next} onOpenChanges={onOpenChanges} />
        </QueryClientProvider>,
      ),
  }
}

beforeEach(async () => {
  await i18n.changeLanguage('en')
  vi.clearAllMocks()
  vi.mocked(agentOpsApi.activity).mockResolvedValue(ACTIVITY)
})

afterEach(async () => {
  // Unmount language subscribers before restoring the shared i18n instance.
  cleanup()
  await i18n.changeLanguage('en')
})

describe('SessionActivity', () => {
  it('shows the context window reported by Codex', async () => {
    vi.mocked(agentOpsApi.activity).mockResolvedValue({
      ...ACTIVITY,
      driver: 'codex',
      ...{ context_window: 258400 },
    })
    show(undefined, { ...run, provider_driver: 'codex' })
    const context = await screen.findByTestId('activity-context')
    expect(within(context).getByText('78,582 / 258,400')).toBeInTheDocument()
  })

  it.each(Object.entries({ en, es, zh, ja, de, ru, fr }))(
    '%s compaction plural forms retain the formatted count placeholder',
    (_language, catalog) => {
      const forms = Object.entries(catalog.activity).filter(([key]) =>
        key.startsWith('compactedTimes_'),
      )
      expect(forms.length).toBeGreaterThan(0)
      for (const [key, value] of forms) {
        expect(value, key).toContain('{{n}}')
      }
    },
  )

  const frenchCompactedOne = 'Compacté 1 fois' // language-data: French expected translation
  const frenchCompactedTwo = 'Compacté 2 fois' // language-data: French expected translation
  it.each([
    ['en', 1, 'Compacted 1 time'],
    ['en', 2, 'Compacted 2 times'],
    ['es', 1, 'Compactado 1 vez'],
    ['es', 2, 'Compactado 2 veces'],
    ['fr', 1, frenchCompactedOne],
    ['fr', 2, frenchCompactedTwo],
    ['de', 1, '1-mal verdichtet'],
    ['de', 2, '2-mal verdichtet'],
    ['ru', 1, 'Сжат 1 раз'],
    ['ru', 2, 'Сжат 2 раза'],
    ['ja', 1, '1 回圧縮'],
    ['ja', 2, '2 回圧縮'],
    ['zh', 1, '已压缩 1 次'],
    ['zh', 2, '已压缩 2 次'],
  ] as const)(
    'renders %s compactions with count %i as "%s"',
    async (language, count, expected) => {
      await i18n.changeLanguage(language)
      vi.mocked(agentOpsApi.activity).mockResolvedValue({
        ...ACTIVITY,
        compactions: Array.from(
          { length: count },
          () => ACTIVITY.compactions[0]!,
        ),
      })
      show()
      const context = await screen.findByTestId('activity-context')
      expect(within(context).getByText(expected)).toBeInTheDocument()
    },
  )

  it('shows workflow progress when agents have no phase assignment', async () => {
    vi.mocked(agentOpsApi.activity).mockResolvedValue({
      ...ACTIVITY,
      tasks: [
        {
          tool_use_id: 'w1',
          kind: 'workflow',
          state: 'running',
          workflow: {
            run_id: 'wf_1',
            agents_total: 2,
            agents_done: 1,
            phases: [],
          },
        },
      ],
    })
    show()
    expect(await screen.findByText('1 of 2 agents done')).toBeInTheDocument()
    expect(screen.queryByRole('list', { name: 'Phases' })).toBeNull()
  })

  const frenchProgress = 'Agents terminés : 1 sur 2' // language-data: French expected translation
  it.each([
    ['en', 'Phases', '1 of 2 agents done'],
    ['es', 'Fases', '1 de 2 agentes completados'],
    ['fr', 'Phases', frenchProgress],
    ['de', 'Phasen', '1 von 2 Agenten fertig'],
    ['ja', 'フェーズ', '2 エージェント中 1 完了'],
    ['ru', 'Этапы', 'Завершено агентов: 1 из 2'],
    ['zh', '阶段', '2 个智能体中 1 个已完成'],
  ])(
    'shows workflow phases and agent progress in %s',
    async (lang, label, progress) => {
      await i18n.changeLanguage(lang)
      try {
        const answer = {
          ...ACTIVITY,
          tasks: [
            {
              tool_use_id: 'w1',
              kind: 'workflow',
              description: 'Build',
              state: 'running',
              workflow: {
                run_id: 'wf_1',
                name: 'Build',
                agents_total: 2,
                agents_done: 1,
                phases: [
                  {
                    index: 1,
                    title: 'Prepare',
                    state: 'completed',
                    agents_total: 1,
                    agents_done: 1,
                  },
                  {
                    index: 2,
                    title: 'Finish',
                    detail: 'Run final checks',
                    state: 'running',
                    agents_total: 1,
                    agents_done: 0,
                  },
                ],
              },
            },
          ],
        }
        vi.mocked(agentOpsApi.activity).mockResolvedValue(
          answer as RunActivityDTO,
        )
        show()
        const phases = await screen.findByRole('list', { name: label })
        expect(within(phases).getByText('Prepare')).toBeInTheDocument()
        expect(within(phases).getByText('Finish')).toBeInTheDocument()
        expect(within(phases).getByText('Run final checks')).toBeInTheDocument()
        const items = within(phases).getAllByRole('listitem')
        expect(items[0]).not.toHaveAttribute('aria-current')
        if (lang === 'en') {
          expect(items[0]).toHaveTextContent('1 of 1 agents done')
          expect(items[1]).toHaveTextContent('0 of 1 agents done')
        }
        expect(items[1]).not.toHaveAttribute('aria-current')
        expect(screen.getByText(progress)).toBeInTheDocument()
      } finally {
        await i18n.changeLanguage('en')
      }
    },
  )

  it.each([
    [200000, 39.291, '78,582 tokens · 39.3% of 200,000'],
    [1000000, 7.8582, '78,582 tokens · 7.9% of 1,000,000'],
    [null, null, '78,582 tokens · window unknown'],
  ])('shows the reported context window %s', async (window, percent, label) => {
    vi.mocked(agentOpsApi.activity).mockResolvedValue({
      ...ACTIVITY,
      ...{ context_window: window, context_percent: percent },
    })
    show()
    const context = await screen.findByTestId('activity-context')
    expect(within(context).getByText(label)).toBeInTheDocument()
  })

  it('shows the plan, the agents, the background work, the files and the context', async () => {
    show()
    const todos = await screen.findByTestId('activity-todos')
    expect(within(todos).getByText('1 of 3 done')).toBeInTheDocument()
    // The item in progress is named as the tool names it while it works.
    expect(within(todos).getByText('Waiting for the job')).toBeInTheDocument()

    const agents = screen.getByTestId('activity-agents')
    expect(within(agents).getByText('2 · 1 running')).toBeInTheDocument()
    expect(within(agents).getByText('Write agent-a.txt')).toBeInTheDocument()
    expect(within(agents).getByText('5.1s')).toBeInTheDocument()
    expect(
      within(agents).getByRole('img', { name: 'Completed' }),
    ).toBeInTheDocument()
    expect(
      within(agents).getByRole('img', { name: 'Running' }),
    ).toBeInTheDocument()

    const tasks = screen.getByTestId('activity-tasks')
    expect(
      within(tasks).getByText('sleep 5; echo finished'),
    ).toBeInTheDocument()
    expect(within(tasks).getByText(/exit code 0/)).toBeInTheDocument()

    // Files inside the session's folder are named by their place in it.
    const files = screen.getAllByTestId('activity-file')
    expect(
      files.map((f) => f.querySelector('.font-mono')?.textContent),
    ).toEqual(['one.txt', 'two.txt', 'three.txt'])
    expect(files[2]).toHaveTextContent('new')
    expect(files[0]).toHaveTextContent('1 added, 1 removed')

    const context = screen.getByTestId('activity-context')
    expect(
      within(context).getByText('claude-haiku-5-5 · medium'),
    ).toBeInTheDocument()
    expect(
      within(context).getByText('78,582 tokens · window unknown'),
    ).toBeInTheDocument()
    expect(within(context).getByText('Compacted 1 time')).toBeInTheDocument()
    expect(within(context).getByText('tdd-workflow')).toBeInTheDocument()
    expect(within(context).getByText('github')).toBeInTheDocument()
    expect(agentOpsApi.activity).toHaveBeenCalledWith(
      'run_1',
      expect.anything(),
    )
  })

  it("opens a subagent's transcript, and only a subagent that has one", async () => {
    const user = userEvent.setup()
    vi.mocked(agentOpsApi.activityAgent).mockResolvedValue({
      agent_id: 'a442c29bf1c636f28',
      turns: [
        { role: 'user', text: 'Write agent-a.txt containing A' },
        { role: 'tool', text: 'Write {"file_path":"agent-a.txt"}' },
        { role: 'assistant', text: 'done' },
      ],
    })
    show()
    const first = await screen.findByRole('button', {
      name: /Write agent-a\.txt/,
    })
    expect(first).toHaveAttribute('aria-expanded', 'false')
    // A running agent has no id yet, so there is no transcript to open.
    expect(
      screen.getByRole('button', { name: /Write agent-b\.txt/ }),
    ).toBeDisabled()
    await user.click(first)
    expect(first).toHaveAttribute('aria-expanded', 'true')
    const transcript = await screen.findByTestId('activity-agent-transcript')
    expect(
      await within(transcript).findByText('Write agent-a.txt containing A'),
    ).toBeInTheDocument()
    expect(
      within(transcript).getByText(/1 tool call · 72,734 tokens/),
    ).toBeInTheDocument()
    expect(agentOpsApi.activityAgent).toHaveBeenCalledWith(
      'run_1',
      'a442c29bf1c636f28',
      expect.anything(),
    )
  })

  it('opens the Changes from a file', async () => {
    const user = userEvent.setup()
    const onOpenChanges = vi.fn()
    show(onOpenChanges)
    await user.click((await screen.findAllByTestId('activity-file'))[1]!)
    expect(onOpenChanges).toHaveBeenCalledTimes(1)
  })

  it('says nothing has happened yet, and still shows the context', async () => {
    vi.mocked(agentOpsApi.activity).mockResolvedValue({
      ...ACTIVITY,
      agents: [],
      tasks: [],
      files: [],
      todos: [],
      compactions: [],
    })
    show()
    expect(await screen.findByTestId('activity-empty')).toBeInTheDocument()
    expect(screen.queryByTestId('activity-agents')).toBeNull()
    expect(screen.getByText('Not compacted')).toBeInTheDocument()
  })

  it("says why the engine does not show a session's activity, and Retry asks again", async () => {
    const user = userEvent.setup()
    vi.mocked(agentOpsApi.activity).mockRejectedValueOnce(
      new ApiError(
        409,
        'conflict',
        'this session was given vault secrets or a repository credential',
      ),
    )
    show()
    expect(await screen.findByRole('alert')).toHaveTextContent('vault secrets')
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByTestId('session-activity')).toBeInTheDocument()
    expect(agentOpsApi.activity).toHaveBeenCalledTimes(2)
  })

  it('says when the transcript should be there and is not, instead of showing nothing', async () => {
    vi.mocked(agentOpsApi.activity).mockResolvedValue({
      ...ACTIVITY,
      transcript: 'missing',
      agents: [],
      tasks: [],
      files: [],
      todos: [],
    })
    show()
    expect(await screen.findByTestId('activity-missing')).toHaveTextContent(
      'not in its account home',
    )
    expect(screen.queryByTestId('activity-empty')).toBeNull()
  })

  it('reads again when the session stops, so nothing it started stays running', async () => {
    const view = show()
    await screen.findByTestId('session-activity')
    vi.mocked(agentOpsApi.activity).mockResolvedValue({
      ...ACTIVITY,
      agents: ACTIVITY.agents.map((a) => ({
        ...a,
        state: 'completed' as const,
      })),
    })
    view.rerender({ ...run, state: 'stopped' } as RunDTO)
    await waitFor(() => expect(agentOpsApi.activity).toHaveBeenCalledTimes(2))
    expect(
      await within(screen.getByTestId('activity-agents')).findByText('2'),
    ).toBeInTheDocument()
  })
})
