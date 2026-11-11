// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The launch dialog's opt-in "new git worktree": offered only once a folder is chosen,
// unticked by default, and sent only when ticked. A launch that does not tick it is the
// request it always was (the exact-body test in run-create-first-message.test.tsx pins
// that no `worktree` key appears).
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import type { ComponentProps } from 'react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return {
    ...real,
    agentOpsApi: {
      createRun: vi.fn(),
      listWorkspaces: vi.fn(),
      listProfiles: vi.fn(),
      profileLaunchReadiness: vi.fn(),
    },
  }
})
vi.mock('@/features/workspace-templates/api', () => ({
  templatesApi: { list: vi.fn(), apply: vi.fn() },
  templatesKeys: {
    list: (t: string | null, p?: unknown) => ['tpl', t, 'list', p ?? null],
    detail: (t: string | null, id: string) => ['tpl', t, 'detail', id],
  },
}))
vi.mock('@tanstack/react-router', async (orig) => ({
  ...(await orig<typeof import('@tanstack/react-router')>()),
  useNavigate: () => vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))
// The edition's ceiling on what a session may do, by the engine's names in order: every
// one is allowed unless a test lowers it.
const ceiling = vi.hoisted(() => ({ rank: 5 }))
vi.mock('./permission-restriction', async (orig) => {
  const order = [
    'read-only',
    'supervised',
    'auto-accept-edits',
    'edits-and-commands',
    'auto',
    'full',
  ]
  return {
    ...((await orig()) as Record<string, unknown>),
    usePermissionRestriction: () => ({
      allows: (name: string) => order.indexOf(name) <= ceiling.rank,
      reason: 'Your organization allows up to edits and commands.',
    }),
  }
})

import { templatesApi } from '@/features/workspace-templates/api'
import { agentOpsApi } from './api'
import { fixtureReadiness } from './launch-readiness.fixture'
import { RunCreateDialog } from './run-create-dialog'
import { PERMISSION_CHOICES, PERMISSION_MODE } from './session-launch'
import type { ProviderProfileDTO } from './types'

const homeA: ProviderProfileDTO = {
  profile_ref: 'ppf_a',
  driver: 'claude',
  environment_ref: 'xenv_1',
  display_name: 'Home A',
  state: 'active',
  local_environment: true,
  operable: true,
}

function wrap(props: Partial<ComponentProps<typeof RunCreateDialog>> = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <RunCreateDialog open onOpenChange={vi.fn()} {...props} />
    </QueryClientProvider>,
  )
}

const postedBody = () =>
  vi.mocked(agentOpsApi.createRun).mock.calls[0][0] as unknown as Record<
    string,
    unknown
  >

const CRITICAL_WARNING =
  'This is a privileged launch (it removes the per-tool prompt). It requires a two-person approval and is recorded — it may stay pending until approved.'

const CLASSIFIED_WARNING =
  'A read-write mount of a classified workspace is a privileged launch (approval + recording).'
const CEILING = /allows up to edits and commands/
const startButton = () => screen.getByRole('button', { name: 'Start' })
// The engine's built-in Edits and commands template: built in, with its allowlist.
const EDITS_TEMPLATE = {
  id: 'tpl_edits',
  name: 'Edits and commands',
  builtin: true,
  body: { policies: { allowed_tools: ['Read', 'Edit', 'Bash'] } },
}

async function openAdvanced(user: ReturnType<typeof userEvent.setup>) {
  const toggle = await screen.findByRole('button', { name: 'Advanced options' })
  if (toggle.getAttribute('aria-expanded') !== 'true') await user.click(toggle)
  expect(
    await screen.findByText('Local requirements checked'),
  ).toBeInTheDocument()
}

async function chooseFolder(user: ReturnType<typeof userEvent.setup>) {
  await openAdvanced(user)
  await user.click(screen.getByLabelText('Folder'))
  await user.click(
    await screen.findByRole('option', { name: /Project folder/ }),
  )
}

const worktreeBox = () =>
  screen.queryByRole('checkbox', { name: 'Work in a new git worktree' })

beforeEach(() => {
  vi.clearAllMocks()
  ceiling.rank = 5
  vi.mocked(agentOpsApi.listWorkspaces).mockResolvedValue({
    items: [
      {
        workspace_ref: 'folder-a',
        name: 'Project folder',
        state: 'active',
        root_path: '/project',
        mount_mode: 'rw',
        max_read_bytes: 1024,
        dlp_mode: 'off',
      },
    ],
    has_more: false,
  })
  vi.mocked(agentOpsApi.listProfiles).mockResolvedValue({
    items: [homeA],
    has_more: false,
  })
  vi.mocked(templatesApi.list).mockResolvedValue({ items: [], has_more: false })
  vi.mocked(agentOpsApi.createRun).mockResolvedValue({} as never)
  vi.mocked(agentOpsApi.profileLaunchReadiness).mockImplementation(
    async (ref: string) => fixtureReadiness({ profile_ref: ref }),
  )
})

describe('RunCreateDialog — new git worktree', () => {
  it('is offered only with a folder and starts unticked', async () => {
    const user = userEvent.setup()
    wrap()
    await openAdvanced(user)
    expect(worktreeBox()).not.toBeInTheDocument()
    await user.click(screen.getByLabelText('Folder'))
    await user.click(
      await screen.findByRole('option', { name: /Project folder/ }),
    )
    expect(worktreeBox()).toBeInTheDocument()
    expect(worktreeBox()).not.toBeChecked()
  })

  it('launches in the folder itself unless the box is ticked', async () => {
    const user = userEvent.setup()
    wrap()
    await chooseFolder(user)
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody().workspace_ref).toBe('folder-a')
    expect(postedBody()).not.toHaveProperty('worktree')
  })

  it('asks the engine for a worktree when the box is ticked', async () => {
    const user = userEvent.setup()
    wrap()
    await chooseFolder(user)
    await user.click(worktreeBox() as HTMLElement)
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody().workspace_ref).toBe('folder-a')
    expect(postedBody().worktree).toBe(true)
  })

  it('opens on the new worktree the New session form chose, at the folder as it is', async () => {
    const user = userEvent.setup()
    wrap({ initialWorkspaceRef: 'folder-a', initialWorktree: true })
    await openAdvanced(user)
    expect(worktreeBox()).toBeChecked()
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody().workspace_ref).toBe('folder-a')
    expect(postedBody().worktree).toBe(true)
    expect(postedBody()).not.toHaveProperty('worktree_from')
  })

  it('sends no worktree when the person unticks the one the form chose', async () => {
    const user = userEvent.setup()
    wrap({ initialWorkspaceRef: 'folder-a', initialWorktree: true })
    await openAdvanced(user)
    await user.click(worktreeBox() as HTMLElement)
    expect(worktreeBox()).not.toBeChecked()
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody().workspace_ref).toBe('folder-a')
    expect(postedBody()).not.toHaveProperty('worktree')
  })

  it('forgets the choice when the folder is cleared', async () => {
    const user = userEvent.setup()
    wrap()
    await chooseFolder(user)
    await user.click(worktreeBox() as HTMLElement)
    await user.click(screen.getByLabelText('Folder'))
    await user.click(
      await screen.findByRole('option', {
        name: 'Default (My files)',
      }),
    )
    expect(worktreeBox()).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody().workspace_ref).toBe('')
    expect(postedBody()).not.toHaveProperty('worktree')
  })
})

describe('RunCreateDialog — what the agent may do, from the New session form', () => {
  it('opens on the permission the form chose and sends it', async () => {
    const user = userEvent.setup()
    wrap({ initialPermission: 'readOnly' })
    await openAdvanced(user)
    expect(
      screen.getByRole('combobox', { name: 'Permission mode' }),
    ).toHaveTextContent('Read only')
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody().permission_mode).toBe('plan')
  })

  it("opens Edit files and run commands on the engine's built-in template", async () => {
    vi.mocked(templatesApi.list).mockResolvedValue({
      items: [EDITS_TEMPLATE] as never,
      has_more: false,
    })
    vi.mocked(templatesApi.apply).mockResolvedValue({
      applied: true,
      conflicts: [],
    })
    const user = userEvent.setup()
    wrap({ initialPermission: 'editsAndCommands' })
    await openAdvanced(user)
    await waitFor(() =>
      expect(
        screen.getByRole('combobox', { name: 'Template' }),
      ).toHaveTextContent('Edits and commands'),
    )
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Start' })).toBeEnabled(),
    )
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody().template_id).toBe('tpl_edits')
  })

  // The quick form sends PERMISSION_MODE for each choice, with the built-in template for
  // Edit files and run commands (session-launch.ts): the advanced launch posts the same.
  for (const choice of PERMISSION_CHOICES) {
    it(`sends for ${choice} what the quick form sends`, async () => {
      vi.mocked(templatesApi.list).mockResolvedValue({
        items: [EDITS_TEMPLATE] as never,
        has_more: false,
      })
      vi.mocked(templatesApi.apply).mockResolvedValue({
        applied: true,
        conflicts: [],
      })
      const user = userEvent.setup()
      wrap({ initialPermission: choice })
      await openAdvanced(user)
      if (choice === 'editsAndCommands') {
        await waitFor(() =>
          expect(
            screen.getByRole('combobox', { name: 'Template' }),
          ).toHaveTextContent('Edits and commands'),
        )
        // The mode it shows is the template's own, which Start's pair stands for.
        expect(
          screen.getByRole('combobox', { name: 'Permission mode' }),
        ).toHaveTextContent('Runs allowed tools without asking')
      }
      await waitFor(() =>
        expect(screen.getByRole('button', { name: 'Start' })).toBeEnabled(),
      )
      await user.click(screen.getByRole('button', { name: 'Start' }))
      await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
      expect(postedBody().permission_mode).toBe(PERMISSION_MODE[choice])
      if (choice === 'editsAndCommands') {
        expect(postedBody().template_id).toBe('tpl_edits')
      } else {
        expect(postedBody()).not.toHaveProperty('template_id')
      }
    })
  }

  it("opens closed on the form's default, as before, and open on any other choice", async () => {
    const { unmount } = wrap({ initialPermission: 'editsAndCommands' })
    expect(
      await screen.findByRole('button', { name: 'Advanced options' }),
    ).toHaveAttribute('aria-expanded', 'false')
    unmount()
    wrap({ initialPermission: 'ask' })
    expect(
      await screen.findByRole('button', { name: 'Advanced options' }),
    ).toHaveAttribute('aria-expanded', 'true')
  })

  it('says so when Edit files and run commands cannot be asked for here, and asks instead', async () => {
    const user = userEvent.setup()
    wrap({ initialPermission: 'editsAndCommands' })
    expect(
      await screen.findByText(
        'Edit files and run commands needs the built-in Edits and commands template, which you cannot see here, so this launch asks before every action.',
      ),
    ).toBeVisible()
    await openAdvanced(user)
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody().permission_mode).toBe('default')
    expect(postedBody()).not.toHaveProperty('template_id')
  })

  it("asks no two-person approval for the form's default nor for Full, as the engine asks none", async () => {
    vi.mocked(templatesApi.list).mockResolvedValue({
      items: [EDITS_TEMPLATE] as never,
      has_more: false,
    })
    vi.mocked(templatesApi.apply).mockResolvedValue({
      applied: true,
      conflicts: [],
    })
    const user = userEvent.setup()
    const { unmount } = wrap({ initialPermission: 'editsAndCommands' })
    await openAdvanced(user)
    await waitFor(() =>
      expect(
        screen.getByRole('combobox', { name: 'Template' }),
      ).toHaveTextContent('Edits and commands'),
    )
    expect(screen.queryByText(CRITICAL_WARNING)).toBeNull()
    unmount()
    wrap({ initialPermission: 'full' })
    await openAdvanced(user)
    expect(screen.queryByText(CRITICAL_WARNING)).toBeNull()
  })

  it('warns where the engine asks a second person: dontAsk alone, a classified folder written', async () => {
    const user = userEvent.setup()
    const { unmount } = wrap()
    await openAdvanced(user)
    await user.click(screen.getByRole('combobox', { name: 'Permission mode' }))
    await user.click(
      await screen.findByRole('option', {
        name: 'Runs allowed tools without asking',
      }),
    )
    expect(await screen.findByText(CRITICAL_WARNING)).toBeVisible()
    unmount()
    vi.mocked(agentOpsApi.listWorkspaces).mockResolvedValue({
      items: [
        {
          workspace_ref: 'folder-a',
          name: 'Project folder',
          state: 'active',
          root_path: '/project',
          mount_mode: 'rw',
          max_read_bytes: 1024,
          dlp_mode: 'label',
        },
      ],
      has_more: false,
    })
    wrap({ initialWorkspaceRef: 'folder-a' })
    await openAdvanced(user)
    expect(await screen.findByText(CLASSIFIED_WARNING)).toBeVisible()
  })

  it("starts the form's default under a ceiling of edits and commands, and still holds Full", async () => {
    ceiling.rank = 3
    vi.mocked(templatesApi.list).mockResolvedValue({
      items: [EDITS_TEMPLATE] as never,
      has_more: false,
    })
    vi.mocked(templatesApi.apply).mockResolvedValue({
      applied: true,
      conflicts: [],
    })
    const user = userEvent.setup()
    const { unmount } = wrap({ initialPermission: 'editsAndCommands' })
    await openAdvanced(user)
    await waitFor(() =>
      expect(
        screen.getByRole('combobox', { name: 'Template' }),
      ).toHaveTextContent('Edits and commands'),
    )
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Start' })).toBeEnabled(),
    )
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    unmount()
    vi.mocked(agentOpsApi.createRun).mockClear()
    wrap({ initialPermission: 'full' })
    await openAdvanced(user)
    await waitFor(() =>
      expect(startButton()).toHaveAccessibleDescription(CEILING),
    )
    expect(startButton()).toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'Start' }))
    expect(agentOpsApi.createRun).not.toHaveBeenCalled()
  })

  it("holds Start while the template list for the form's default is read", async () => {
    vi.mocked(templatesApi.list).mockReturnValue(new Promise<never>(() => {}))
    const user = userEvent.setup()
    wrap({ initialPermission: 'editsAndCommands' })
    await openAdvanced(user)
    await waitFor(() =>
      expect(startButton()).toHaveAccessibleDescription('Loading…'),
    )
  })

  it('under that ceiling starts any built-in allowlist template, and holds a custom one', async () => {
    ceiling.rank = 3
    vi.mocked(templatesApi.list).mockResolvedValue({
      items: [
        { ...EDITS_TEMPLATE, id: 'tpl_review', name: 'Code Review' },
        {
          ...EDITS_TEMPLATE,
          id: 'tpl_custom',
          name: 'Our own',
          builtin: false,
        },
      ] as never,
      has_more: false,
    })
    vi.mocked(templatesApi.apply).mockResolvedValue({
      applied: true,
      conflicts: [],
      merged: { permission_mode: 'dontAsk' },
    })
    const user = userEvent.setup()
    const pick = async (name: string) => {
      await user.click(screen.getByRole('combobox', { name: 'Template' }))
      await user.click(await screen.findByRole('option', { name }))
    }
    wrap()
    await openAdvanced(user)
    await pick('Our own')
    await waitFor(() =>
      expect(startButton()).toHaveAccessibleDescription(CEILING),
    )
    await pick('Code Review')
    await waitFor(() => expect(startButton()).toBeEnabled())
    await user.click(startButton())
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody().template_id).toBe('tpl_review')
  })

  it("offers Retry when the template list for the form's default fails, blaming no one", async () => {
    vi.mocked(templatesApi.list).mockRejectedValue(new Error('unreachable'))
    wrap({ initialPermission: 'editsAndCommands' })
    expect(
      (await screen.findAllByRole('button', { name: 'Retry' }))[0],
    ).toBeVisible()
    expect(screen.queryByText(/which you cannot see here/)).toBeNull()
  })
})
