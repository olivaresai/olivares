// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'

const ops = vi.hoisted(() => ({
  resolveProfile: vi.fn(),
  listProfiles: vi.fn(),
  createProfile: vi.fn(),
  listWorkspaces: vi.fn(),
  createWorkspace: vi.fn(),
  createRun: vi.fn(),
  input: vi.fn(),
  inputText: vi.fn(),
}))
const http = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  delete: vi.fn(),
}))
const tools = vi.hoisted(() => ({
  plan: vi.fn(),
  install: vi.fn(),
  job: vi.fn(),
}))
vi.mock('@/features/agentops/api', () => ({ agentOpsApi: ops }))
vi.mock('@/features/agent-tools/api', () => ({ agentToolsApi: tools }))
vi.mock('@/lib/api', () => ({ http }))

import { ApiError } from '@/lib/api/errors'
import { installLatest, startSession } from './api'

beforeEach(() => {
  vi.clearAllMocks()
  ops.resolveProfile.mockResolvedValue({
    profile: { profile_ref: 'ppf_new', driver: 'claude', state: 'active' },
    reason: 'own_login',
    created: false,
  })
  ops.listWorkspaces.mockResolvedValue({ items: [] })
  ops.createWorkspace.mockImplementation((b) =>
    Promise.resolve({ workspace_ref: 'ws_1', state: 'active', ...b }),
  )
  ops.createRun.mockResolvedValue({ run_ref: 'run_1', provider_driver: '' })
  ops.input.mockResolvedValue({ accepted: true })
  ops.inputText.mockResolvedValue({ accepted: true })
  http.get.mockResolvedValue({
    items: [
      { id: 'tpl-ec', name: 'Edits and commands', builtin: true },
      { id: 'tpl-ro', name: 'Read only', builtin: true },
    ],
  })
})

describe('the first-hour launch', () => {
  // HU 030: which profile a session runs under was chosen here and differently
  // in the CLI. It is the engine's one rule now; the console asks and never picks.
  it('starts on the profile the engine resolves for the tool, and picks none itself', async () => {
    ops.resolveProfile.mockResolvedValue({
      profile: {
        profile_ref: 'ppf_key',
        driver: 'claude',
        state: 'active',
        auth_source: 'managed_injection',
      },
      reason: 'api_key',
      provider: {
        provider_ref: 'prv_a',
        kind: 'anthropic',
        display_name: 'Team key',
      },
      created: true,
    })
    await startSession({
      driver: 'claude',
      folder: '',
      prompt: '',
      permission: 'ask',
    })
    expect(ops.resolveProfile).toHaveBeenCalledWith('claude')
    expect(ops.createRun.mock.calls[0][0].provider_profile_ref).toBe('ppf_key')
    expect(ops.listProfiles).not.toHaveBeenCalled()
    expect(ops.createProfile).not.toHaveBeenCalled()
  })

  it("shows the engine's sentence when the tool has nothing to run on, and starts nothing", async () => {
    const sentence =
      'Claude Code is not signed in and Providers has nothing it can use. Sign it in under AI tools, or add an Anthropic key in Providers.'
    ops.resolveProfile.mockRejectedValue(
      new ApiError(409, 'conflict', sentence),
    )
    await expect(
      startSession({
        driver: 'claude',
        folder: '/home/fran/app',
        prompt: 'hi',
        permission: 'ask',
      }),
    ).rejects.toThrow(sentence)
    expect(ops.createWorkspace).not.toHaveBeenCalled()
    expect(ops.createRun).not.toHaveBeenCalled()
  })

  it('starts Claude Code with the chosen permission, in the folder, with the first message', async () => {
    await startSession({
      driver: 'claude',
      folder: '/home/fran/app',
      prompt: 'Run the tests',
      permission: 'editsAndCommands',
    })
    expect(ops.createWorkspace).toHaveBeenCalledWith(
      expect.objectContaining({ root_path: '/home/fran/app', name: 'app' }),
    )
    expect(ops.createRun).toHaveBeenCalledWith(
      expect.objectContaining({
        provider_profile_ref: 'ppf_new',
        workspace_ref: 'ws_1',
        template_id: 'tpl-ec',
        model: '',
      }),
    )
    expect(JSON.parse(ops.input.mock.calls[0][1])).toEqual({
      type: 'user',
      message: { role: 'user', content: 'Run the tests' },
    })
  })

  // Changed with N1 (Root 2026-10-02, HU 043): every preset applies to every tool, so
  // Codex starts with the chosen preset too (it used to get no template and no mode).
  it('starts Codex with a text turn and the same preset as any tool', async () => {
    ops.createRun.mockResolvedValue({
      run_ref: 'run_2',
      provider_driver: 'codex',
    })
    await startSession({
      driver: 'codex',
      folder: '',
      prompt: 'hello',
      permission: 'editsAndCommands',
    })
    expect(ops.createRun.mock.calls[0][0].template_id).toBe('tpl-ec')
    expect(ops.createRun.mock.calls[0][0].workspace_ref).toBe('')
    expect(ops.inputText).toHaveBeenCalledWith('run_2', 'hello')
  })

  // Design FH 016: vault secrets travel by NAME; the server opens the values.
  it('names the chosen vault secrets on the launch, and only their names', async () => {
    await startSession({
      driver: 'claude',
      folder: '',
      prompt: '',
      permission: 'editsOnly',
      secretEnv: [{ env: 'GITHUB_TOKEN', secret: 'env/github' }],
    })
    expect(ops.createRun.mock.calls[0][0].secret_env).toEqual([
      { env: 'GITHUB_TOKEN', secret: 'env/github' },
    ])
  })

  it('names no secret when none was chosen', async () => {
    await startSession({
      driver: 'claude',
      folder: '',
      prompt: '',
      permission: 'editsOnly',
    })
    expect(ops.createRun.mock.calls[0][0].secret_env).toBeUndefined()
  })
  it.each([
    ['grok', 'Grok Build'],
    ['opencode', 'OpenCode'],
  ] as const)(
    'starts %s on its resolved profile with the chosen preset (HU 043)',
    async (driver, name) => {
      ops.createRun.mockResolvedValue({
        run_ref: 'run_3',
        provider_driver: driver,
      })
      await startSession({
        driver,
        folder: '/home/fran/app',
        prompt: '',
        permission: 'readOnly',
      })
      expect(ops.resolveProfile).toHaveBeenCalledWith(driver)
      expect(ops.createRun.mock.calls[0][0]).toEqual(
        expect.objectContaining({
          permission_mode: 'plan',
          name: `${name} in app`,
        }),
      )
      expect(ops.createRun.mock.calls[0][0].template_id).toBeUndefined()
    },
  )

  it('sends no message when none was typed', async () => {
    await startSession({
      driver: 'claude',
      folder: '',
      prompt: '  ',
      permission: 'ask',
    })
    expect(ops.input).not.toHaveBeenCalled()
    expect(ops.createRun.mock.calls[0][0].permission_mode).toBe('default')
  })
})

describe('one-click install', () => {
  it('waits its turn when another tool is installing, instead of failing', async () => {
    vi.useFakeTimers()
    tools.plan.mockResolvedValue({ digest: 'd1' })
    tools.install
      .mockRejectedValueOnce(
        new ApiError(
          409,
          'install_busy',
          'Another host tool installation is running.',
        ),
      )
      .mockResolvedValueOnce({ id: 'j1', state: 'running' })
    tools.job.mockResolvedValue({ id: 'j1', state: 'succeeded' })
    const done = installLatest('claude')
    await vi.runAllTimersAsync()
    expect((await done).state).toBe('succeeded')
    expect(tools.install).toHaveBeenCalledTimes(2)
    vi.useRealTimers()
  })
})
