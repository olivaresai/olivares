// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The first screen before any session: one mark, one heading, one sentence and one next
// step, said to the person who stands there. Only a system administrator installs or
// signs in a tool; anyone else is told who gets it ready.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const auth = vi.hoisted(() => ({ superadmin: true }))
// Every right but opening AI tools (sessions:account:read): the composer's note then
// names who gets a tool ready.
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: (permission: string) => permission !== 'sessions:account:read',
    isSuperadmin: auth.superadmin,
    activeTenant: 'tnt-a',
  }),
}))
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => vi.fn(),
  useRouterState: () => '',
}))
const installed = vi.hoisted(() => ({ claude: false }))
vi.mock('./api', async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  signInApi: {
    status: (driver: string) =>
      Promise.resolve({
        driver,
        installed: driver === 'claude' && installed.claude,
        signed_in: false,
      }),
  },
}))

import { agentOpsApi } from '@/features/agentops/api'
import { providersApi } from '@/features/providers/api'
import { NetworkError } from '@/lib/api/errors'
import { useTenantStore } from '@/stores/tenant'
import { NewSessionDialog, WorkStart } from './first-hour'
import { readinessOf } from './readiness.fixture'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const INSTALL_SENTENCE =
  'Olivares runs the official Claude Code or Codex on this server. One is enough.'

beforeEach(() => {
  vi.restoreAllMocks()
  auth.superadmin = true
  installed.claude = false
  useTenantStore.setState({ activeTenant: 'tnt-a' })
  vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue({
    items: [],
    has_more: false,
  } as Awaited<ReturnType<typeof agentOpsApi.listRuns>>)
  vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue({
    items: [],
    has_more: false,
  } as Awaited<ReturnType<typeof agentOpsApi.listWorkspaces>>)
  vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue({
    items: [],
    has_more: false,
  })
  vi.spyOn(agentOpsApi, 'listProfiles').mockResolvedValue({
    items: [],
    has_more: false,
  })
  vi.spyOn(agentOpsApi, 'listFolderCandidates').mockResolvedValue({
    tools: [],
    folders: [],
  })
  vi.spyOn(providersApi, 'list').mockResolvedValue({
    items: [],
    has_more: false,
  } as Awaited<ReturnType<typeof providersApi.list>>)
  vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(readinessOf({}))
})

describe('the first screen before any session', () => {
  it('sits under the brand mark: one heading and one sentence', async () => {
    wrap(<WorkStart />)
    const heading = await screen.findByRole('heading', {
      name: 'Start a session',
    })
    const mark = heading.parentElement?.previousElementSibling
    expect(mark?.tagName.toLowerCase()).toBe('svg')
    expect(mark).toHaveAttribute('aria-hidden', 'true')
    expect(screen.getAllByRole('heading')).toHaveLength(1)
  })

  it('describes the disabled Start by the one sentence that says what to do first', async () => {
    wrap(<WorkStart />)
    expect(await screen.findByTestId('tool-claude')).toBeVisible()
    expect(screen.getByText(INSTALL_SENTENCE)).toBeVisible()
    const start = await screen.findByRole('button', { name: 'Start' })
    expect(start).toBeDisabled()
    expect(start).toHaveAccessibleDescription(
      new RegExp(`^${INSTALL_SENTENCE.replace(/\./g, '\\.')}`),
    )
  })

  it('does not offer someone who cannot install a tool an Install; the composer says who gets it ready', async () => {
    auth.superadmin = false
    wrap(<WorkStart />)
    const start = await screen.findByRole('button', { name: 'Start' })
    expect(start).toBeDisabled()
    expect(screen.queryByTestId('tool-claude')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Install' })).toBeNull()
    expect(screen.queryByText(INSTALL_SENTENCE)).toBeNull()
    expect(
      screen.getByText(/Ask an administrator to get it ready\./),
    ).toBeVisible()
    expect(start).toHaveAccessibleDescription(
      /^Claude Code has nothing to run on yet\./,
    )
  })

  it('says the folders the tools already worked in in one line under the composer, the list behind Review', async () => {
    vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
      readinessOf({ claude: 'own_login' }),
    )
    vi.spyOn(agentOpsApi, 'listFolderCandidates').mockResolvedValue({
      tools: [{ tool: 'claude', name: 'Claude Code', readable: true }],
      folders: [
        {
          path: '/srv/work/api',
          tool: 'claude',
          last_used: new Date().toISOString(),
          conversations: 4,
          preselected: true,
        },
      ],
    })
    const create = vi.spyOn(agentOpsApi, 'createWorkspace')
    wrap(<WorkStart />)
    expect(
      await screen.findByText('1 folder your tools already worked in.'),
    ).toBeVisible()
    const review = screen.getByRole('button', { name: 'Review' })
    expect(
      screen.queryByRole('group', { name: 'Bring your folders' }),
    ).toBeNull()
    expect(screen.getAllByRole('heading')).toHaveLength(1)
    const composer = screen.getByRole('textbox', {
      name: 'First message (optional)',
    })
    expect(
      composer.compareDocumentPosition(review) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    await userEvent.setup().click(review)
    const dialog = await screen.findByRole('dialog', {
      name: 'Bring your folders',
    })
    expect(
      within(dialog).getByRole('checkbox', {
        name: 'Register /srv/work/api (Claude Code)',
      }),
    ).toBeChecked()
    expect(create).not.toHaveBeenCalled()
  })

  it('says nothing about folders when the tools worked in none', async () => {
    vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
      readinessOf({ claude: 'own_login' }),
    )
    wrap(<WorkStart />)
    await screen.findByRole('button', { name: 'Start' })
    expect(screen.queryByText(/your tools already worked in/)).toBeNull()
    expect(screen.queryByRole('button', { name: 'Review' })).toBeNull()
  })

  it('says the folders could not be read, with Retry, when the read fails', async () => {
    vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
      readinessOf({ claude: 'own_login' }),
    )
    const read = vi
      .spyOn(agentOpsApi, 'listFolderCandidates')
      .mockRejectedValue(new NetworkError('down'))
    wrap(<WorkStart />)
    expect(
      await screen.findByText("Could not look for your tools' folders."),
    ).toBeVisible()
    expect(screen.queryByText(/your tools already worked in/)).toBeNull()
    expect(read).toHaveBeenCalledTimes(1)
    await userEvent.setup().click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(read).toHaveBeenCalledTimes(2))
  })

  it('holds a loading line, never a bare spinner, while the engine says which tools can start', async () => {
    vi.spyOn(agentOpsApi, 'toolsReadiness').mockReturnValue(
      new Promise(() => {}),
    )
    wrap(<WorkStart />)
    expect(await screen.findByRole('status')).toHaveTextContent('Loading…')
  })
})

describe('the New session dialog with no tool ready', () => {
  it('tells someone who cannot install a tool who gets it ready, with no Install', async () => {
    auth.superadmin = false
    wrap(<NewSessionDialog open onOpenChange={() => {}} />)
    expect(
      await screen.findByText(/Ask an administrator to get it ready\./),
    ).toBeVisible()
    expect(screen.queryByRole('button', { name: 'Install' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Start session' })).toBeDisabled()
  })

  it('holds a loading line while the engine says which tools can start', async () => {
    vi.spyOn(agentOpsApi, 'toolsReadiness').mockReturnValue(
      new Promise(() => {}),
    )
    wrap(<NewSessionDialog open onOpenChange={() => {}} />)
    expect(await screen.findByRole('status')).toHaveTextContent('Loading…')
  })

  it('still offers a system administrator the tools to install', async () => {
    wrap(<NewSessionDialog open onOpenChange={() => {}} />)
    expect(
      await screen.findAllByRole('button', { name: 'Install' }),
    ).not.toHaveLength(0)
  })
})
