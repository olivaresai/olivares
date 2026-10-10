// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const ops = vi.hoisted(() => ({
  resolveProfile: vi.fn(),
  listProfiles: vi.fn(),
  createProfile: vi.fn(),
  listWorkspaces: vi.fn(),
  createWorkspace: vi.fn(),
  createRun: vi.fn(),
  getRun: vi.fn(),
  input: vi.fn(),
  inputText: vi.fn(),
}))
const http = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  delete: vi.fn(),
}))
vi.mock('./api', () => ({ agentOpsApi: ops }))
vi.mock('@/lib/api', () => ({ http }))

import { ApiError } from '@/lib/api/errors'
import { sentTurnsOf, useSentTurns } from '@/features/sessions/sent-turns'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import {
  launchSession,
  needsApproval,
  type QuickLaunch,
  type SessionLaunch,
} from './session-launch'

// A launch under an authority that holds: these tests are about what it sends, and
// that every request carries it.
const launchAuthority = {
  tenant: 'tenant-a',
  signal: new AbortController().signal,
  dispatchGuard: () => {},
}
const launch = (l: SessionLaunch) => launchSession(l, launchAuthority)
/** The quick form's launch: its choices and the first message it was asked. */
const startSession = ({ prompt, ...quick }: QuickLaunch & { prompt: string }) =>
  launch({ quick, message: prompt })

beforeEach(() => {
  vi.clearAllMocks()
  useSentTurns.setState({ byRun: {} })
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
  it('uses the supplied registration even when another folder has the same path', async () => {
    ops.listWorkspaces.mockResolvedValue({
      items: [
        { workspace_ref: 'ws_other', root_path: '/project', state: 'active' },
      ],
    })
    await startSession({
      driver: 'claude',
      folder: '/project',
      workspace_ref: 'ws_saved',
      permission: 'ask',
      prompt: '',
    })
    expect(ops.createRun).toHaveBeenCalledWith(
      expect.objectContaining({ workspace_ref: 'ws_saved' }),
      launchAuthority,
    )
    expect(ops.listWorkspaces).not.toHaveBeenCalled()
    expect(ops.createWorkspace).not.toHaveBeenCalled()
  })

  it('keeps the refusal of a deregistered folder without registering it again', async () => {
    const refused = new ApiError(
      400,
      'bad_request',
      'workspace_ref is not a registered workspace',
    )
    ops.createRun.mockImplementation(async (run) => {
      if (run.workspace_ref === 'ws_removed') throw refused
      return { run_ref: 'run_1', provider_driver: 'codex' }
    })
    await expect(
      startSession({
        driver: 'codex',
        folder: '/project',
        workspace_ref: 'ws_removed',
        permission: 'ask',
        prompt: 'hello',
      }),
    ).rejects.toBe(refused)
    expect(ops.listWorkspaces).not.toHaveBeenCalled()
    expect(ops.createWorkspace).not.toHaveBeenCalled()
    expect(ops.inputText).not.toHaveBeenCalled()
  })

  it('launches in the registered folder even when newer folders fill the first page', async () => {
    const saved = {
      workspace_ref: 'ws_saved',
      root_path: '/project',
      state: 'active',
      mount_mode: 'ro',
    }
    ops.listWorkspaces.mockImplementation(async (query) => ({
      items:
        query.root_path === '/project' && query.state === 'active'
          ? [saved]
          : Array.from({ length: 200 }, (_, n) => ({
              workspace_ref: `ws_${n}`,
              root_path: `/other/${n}`,
              state: 'active',
            })),
      has_more: !query.root_path,
    }))
    await startSession({
      driver: 'claude',
      folder: '/project',
      prompt: '',
      permission: 'ask',
    })
    expect(ops.createRun).toHaveBeenCalledWith(
      expect.objectContaining({ workspace_ref: 'ws_saved' }),
      launchAuthority,
    )
    expect(ops.createWorkspace).not.toHaveBeenCalled()
  })

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
    expect(ops.resolveProfile).toHaveBeenCalledWith('claude', launchAuthority)
    expect(ops.createRun.mock.calls[0][0].provider_profile_ref).toBe('ppf_key')
    expect(ops.listProfiles).not.toHaveBeenCalled()
    expect(ops.createProfile).not.toHaveBeenCalled()
  })

  // A tool with several profiles: the person chose one, and that one is the session's.
  it('starts on the profile the person chose, and does not ask the engine to resolve', async () => {
    await startSession({
      driver: 'claude',
      folder: '',
      prompt: '',
      permission: 'ask',
      profileRef: 'ppf_b',
    })
    expect(ops.resolveProfile).not.toHaveBeenCalled()
    expect(ops.createRun.mock.calls[0][0].provider_profile_ref).toBe('ppf_b')
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
      expect.objectContaining({
        root_path: '/home/fran/app',
        name: 'app',
        // The engine's folder default; Register workspace sends the same one
        // (agentops/workspaces-dlp-default.test.tsx).
        dlp_mode: 'off',
      }),
      launchAuthority,
    )
    expect(ops.createRun).toHaveBeenCalledWith(
      expect.objectContaining({
        provider_profile_ref: 'ppf_new',
        workspace_ref: 'ws_1',
        template_id: 'tpl-ec',
      }),
      launchAuthority,
    )
    // HU2-37: no model chosen, so none is sent and the engine resolves it.
    expect(ops.createRun.mock.calls[0][0]).not.toHaveProperty('model')
    expect(JSON.parse(ops.input.mock.calls[0][1])).toEqual({
      type: 'user',
      message: { role: 'user', content: 'Run the tests' },
    })
  })

  it.each([
    ['missing', []],
    ['renamed', [{ id: 'tpl-ec', name: 'Renamed preset', builtin: true }]],
    [
      'not built in',
      [{ id: 'tpl-ec', name: 'Edits and commands', builtin: false }],
    ],
    ['empty id', [{ id: '', name: 'Edits and commands', builtin: true }]],
    [
      'outside the page',
      Array.from({ length: 100 }, (_, n) => ({
        id: `tpl-${n}`,
        name: `Other preset ${n}`,
        builtin: true,
      })),
    ],
  ])('refuses edits and commands when its template is %s', async (_, items) => {
    http.get.mockResolvedValue({ items })
    await expect(
      startSession({
        driver: 'claude',
        folder: '',
        prompt: 'Run the tests',
        permission: 'editsAndCommands',
      }),
    ).rejects.toThrow(/Edits and commands.*session was not started/)
    expect(ops.createRun).not.toHaveBeenCalled()
    expect(ops.input).not.toHaveBeenCalled()
    expect(ops.inputText).not.toHaveBeenCalled()
  })

  it('keeps a template lookup error and does not start the session', async () => {
    const error = new ApiError(
      503,
      'unavailable',
      'Template lookup unavailable',
    )
    http.get.mockRejectedValue(error)
    await expect(
      startSession({
        driver: 'codex',
        folder: '',
        prompt: 'Run the tests',
        permission: 'editsAndCommands',
      }),
    ).rejects.toBe(error)
    expect(ops.createRun).not.toHaveBeenCalled()
    expect(ops.input).not.toHaveBeenCalled()
    expect(ops.inputText).not.toHaveBeenCalled()
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
    expect(ops.inputText).toHaveBeenCalledWith(
      'run_2',
      'hello',
      undefined,
      launchAuthority,
    )
  })

  // HU2-37: the New session dialog's model reaches the launch only when the user chose one.
  it('starts with the model chosen in New session', async () => {
    await startSession({
      driver: 'codex',
      folder: '',
      prompt: '',
      permission: 'ask',
      model: ' gpt-5.6-terra ',
    })
    expect(ops.createRun).toHaveBeenCalledTimes(1)
    expect(ops.createRun.mock.calls[0][0].model).toBe('gpt-5.6-terra')
  })

  it('sends no model when the provider default is kept', async () => {
    await startSession({
      driver: 'codex',
      folder: '',
      prompt: '',
      permission: 'ask',
      model: '',
    })
    expect(ops.createRun.mock.calls[0][0]).not.toHaveProperty('model')
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
      expect(ops.resolveProfile).toHaveBeenCalledWith(driver, launchAuthority)
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

  // HU2-27: the engine refused Codex's first turn (a refused key) and the dialog said
  // "The session did not start" while the session ran on, with no trace of the message.
  it('opens a session whose first message was refused, with the message and the reason', async () => {
    vi.useFakeTimers()
    const reason =
      'the provider reports that this profile is not authenticated (auth_required); a turn cannot be started'
    ops.createRun.mockResolvedValue({
      run_ref: 'run_3',
      provider_driver: 'codex',
    })
    ops.inputText.mockRejectedValue(new ApiError(409, 'conflict', reason))
    const started = startSession({
      driver: 'codex',
      folder: '',
      prompt: 'Say hi.',
      permission: 'editsAndCommands',
    })
    await vi.runAllTimersAsync()
    expect((await started).run_ref).toBe('run_3')
    expect(sentTurnsOf('run_3')).toEqual([{ text: 'Say hi.', refused: reason }])
    vi.useRealTimers()
  })

  it('notes a first message the engine took', async () => {
    await startSession({
      driver: 'claude',
      folder: '',
      prompt: ' hello ',
      permission: 'ask',
    })
    expect(sentTurnsOf('run_1')).toEqual([{ text: 'hello' }])
  })

  // SR4C on 3cf9f18a: a start that completes after a sign-out notes nothing.
  it('notes nothing when the sign-in changed while the start was out', async () => {
    // Claude Code takes its first message as a stream-json line (agentOpsApi.input).
    ops.input.mockImplementation(async () => {
      useSessionStore.setState((s) => ({
        credentialGeneration: s.credentialGeneration + 1,
      }))
      return { accepted: true }
    })
    await startSession({
      driver: 'claude',
      folder: '',
      prompt: 'hello',
      permission: 'ask',
    })
    expect(sentTurnsOf('run_1')).toBeUndefined()
    expect(useSentTurns.getState().byRun).toEqual({})
  })

  it('still fails when the tool stopped as it started', async () => {
    vi.useFakeTimers()
    ops.inputText.mockRejectedValue(
      new ApiError(
        409,
        'conflict',
        'stopped',
        undefined,
        {},
        {
          run_ref: 'run_2',
          reason: 'codex exited',
        },
      ),
    )
    ops.createRun.mockResolvedValue({
      run_ref: 'run_2',
      provider_driver: 'codex',
    })
    const started = startSession({
      driver: 'codex',
      folder: '',
      prompt: 'hello',
      permission: 'ask',
    })
    const failed = expect(started).rejects.toThrow('stopped')
    await vi.runAllTimersAsync()
    await failed
    expect(sentTurnsOf('run_2')).toBeUndefined()
    vi.useRealTimers()
  })
})

// The quick form and the advanced dialog launch the same choices, the profile the engine
// resolved, a registered folder, a permission mode and a first message, as one run.
describe('the same choices from either New session form', () => {
  it('launch the same run', async () => {
    ops.listWorkspaces.mockResolvedValue({
      items: [
        { workspace_ref: 'ws_1', root_path: '/home/fran/app', state: 'active' },
      ],
    })
    await startSession({
      driver: 'claude',
      folder: '/home/fran/app',
      prompt: 'Run the tests',
      permission: 'editsOnly',
    })
    // The run the advanced dialog builds for them (run-create-first-message.test.tsx
    // pins how it builds it).
    await launch({
      run: {
        name: '',
        transport: 'stream-json',
        permission_mode: 'acceptEdits',
        effort: '',
        model: '',
        workspace_ref: 'ws_1',
        isolation: 'native',
        env_allow: [],
        provider_profile_ref: 'ppf_new',
      },
      message: 'Run the tests',
    })
    const [quick, advanced] = ops.createRun.mock.calls.map((c) => c[0])
    expect(advanced).toEqual(quick)
    expect(quick).toMatchObject({ name: 'Run the tests' })
    expect(ops.input).toHaveBeenCalledTimes(2)
    expect(ops.input.mock.calls[1][1]).toBe(ops.input.mock.calls[0][1])
  })
})

// #500: a launch that waits for an approval answers 202 waiting_approval. The console
// posted the first message 20 times into the waiting run, gave up after 15 s and kept it
// only as a "Not sent" note; after the approval the session ran with no turn.
describe('a first message whose launch waits for an approval', () => {
  const waiting = {
    run_ref: 'run_5',
    provider_driver: 'codex',
    state: 'waiting_approval',
  }
  let state: string
  let reason: string | undefined
  beforeEach(() => {
    vi.useFakeTimers()
    state = 'waiting_approval'
    reason = undefined
    ops.createRun.mockResolvedValue(waiting)
    ops.getRun.mockImplementation(async () => ({ ...waiting, state, reason }))
    ops.inputText.mockImplementation(async () => {
      if (state !== 'running')
        throw new ApiError(
          409,
          'module_error',
          `session is not running (state=${state})`,
        )
      return { accepted: true }
    })
  })
  afterEach(() => vi.useRealTimers())

  const start = () =>
    startSession({
      driver: 'codex',
      folder: '',
      prompt: ' say hello ',
      permission: 'editsAndCommands',
    })

  it('is sent once after the approval, when the session runs', async () => {
    let opened = false
    const started = start().then((run) => {
      opened = true
      return run
    })
    // The dialog opens the session at once: it does not wait for the approval.
    await vi.advanceTimersByTimeAsync(0)
    expect(opened).toBe(true)
    await vi.advanceTimersByTimeAsync(30_000)
    expect(ops.inputText).not.toHaveBeenCalled()
    expect(sentTurnsOf('run_5')).toEqual([{ text: 'say hello', waiting: true }])
    state = 'pending'
    await vi.advanceTimersByTimeAsync(5_000)
    expect(ops.inputText).not.toHaveBeenCalled()
    state = 'running'
    await vi.advanceTimersByTimeAsync(30_000)
    expect(ops.inputText).toHaveBeenCalledTimes(1)
    expect(ops.inputText.mock.calls[0].slice(0, 2)).toEqual([
      'run_5',
      'say hello',
    ])
    expect(sentTurnsOf('run_5')).toEqual([{ text: 'say hello' }])
    expect((await started).run_ref).toBe('run_5')
  })

  it.each([
    ['declined', 'human review rejected'],
    ['expired', 'approval expired'],
  ])(
    'stays as not sent, with the reason, when the launch is %s',
    async (ended, why) => {
      const started = start()
      state = ended
      reason = why
      await vi.advanceTimersByTimeAsync(30_000)
      expect(ops.inputText).not.toHaveBeenCalled()
      expect(sentTurnsOf('run_5')).toEqual([
        { text: 'say hello', refused: why },
      ])
      await started
    },
  )

  it('reads again through a passing error or state, and sends once', async () => {
    await start()
    ops.getRun.mockRejectedValueOnce(new ApiError(503, 'unavailable', 'busy'))
    state = 'starting'
    await vi.advanceTimersByTimeAsync(10_000)
    expect(sentTurnsOf('run_5')).toEqual([{ text: 'say hello', waiting: true }])
    state = 'running'
    await vi.advanceTimersByTimeAsync(10_000)
    expect(ops.inputText).toHaveBeenCalledTimes(1)
    expect(sentTurnsOf('run_5')).toEqual([{ text: 'say hello' }])
  })

  it.each([403, 404])(
    'stays as not sent when the run answers %i',
    async (status) => {
      await start()
      ops.getRun.mockRejectedValue(
        new ApiError(status, 'denied', 'run not found'),
      )
      await vi.advanceTimersByTimeAsync(10_000)
      expect(ops.getRun).toHaveBeenCalledTimes(1)
      expect(sentTurnsOf('run_5')).toEqual([
        { text: 'say hello', refused: 'run not found' },
      ])
    },
  )

  it('stays as not sent when the run cannot be read for a minute', async () => {
    await start()
    ops.getRun.mockRejectedValue(new ApiError(503, 'unavailable', 'busy'))
    await vi.advanceTimersByTimeAsync(58_000)
    expect(sentTurnsOf('run_5')).toEqual([{ text: 'say hello', waiting: true }])
    await vi.advanceTimersByTimeAsync(2_000)
    expect(ops.getRun).toHaveBeenCalledTimes(30)
    expect(sentTurnsOf('run_5')).toEqual([
      { text: 'say hello', refused: 'busy' },
    ])
    await vi.advanceTimersByTimeAsync(60_000)
    expect(ops.getRun).toHaveBeenCalledTimes(30)
  })

  it('stays as not sent, with the reason, when the running session refuses it', async () => {
    await start()
    state = 'running'
    ops.inputText.mockRejectedValue(
      new ApiError(409, 'conflict', 'auth_required'),
    )
    await vi.advanceTimersByTimeAsync(30_000)
    expect(ops.inputText).toHaveBeenCalledTimes(20)
    expect(sentTurnsOf('run_5')).toEqual([
      { text: 'say hello', refused: 'auth_required' },
    ])
  })

  it('is never sent after switching organization and back', async () => {
    await start()
    const tenant = useTenantStore.getState().activeTenant
    useTenantStore.setState({ activeTenant: 'tenant-b' })
    useTenantStore.setState({ activeTenant: tenant })
    state = 'running'
    await vi.advanceTimersByTimeAsync(30_000)
    expect(ops.getRun).not.toHaveBeenCalled()
    expect(ops.inputText).not.toHaveBeenCalled()
    expect(useSentTurns.getState().byRun).toEqual({})
  })

  it('is never sent after a sign-out', async () => {
    const started = start()
    await vi.advanceTimersByTimeAsync(0)
    useSessionStore.setState((s) => ({
      credentialGeneration: s.credentialGeneration + 1,
    }))
    state = 'running'
    await vi.advanceTimersByTimeAsync(30_000)
    expect(ops.getRun).not.toHaveBeenCalled()
    expect(ops.inputText).not.toHaveBeenCalled()
    expect(useSentTurns.getState().byRun).toEqual({})
    await started
  })
})

// The engine names the refusal by its code (409 approval_required); its sentence may
// change or be translated, so the form never reads it.
describe('a launch the gate holds for an approval', () => {
  it('is detected by its code', () => {
    expect(
      needsApproval(
        new ApiError(409, 'approval_required', 'Approve it first.'),
      ),
    ).toBe(true)
  })

  it('is not guessed from a sentence', () => {
    expect(
      needsApproval(
        new ApiError(409, 'conflict', 'This session needs a human approval.'),
      ),
    ).toBe(false)
    expect(needsApproval(new Error('needs human approval'))).toBe(false)
  })
})
