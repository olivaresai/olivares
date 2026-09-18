// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ONE PLACE TO SAY WHAT TO RUN, and the line that says what it will apply to.
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'

const auth = vi.hoisted(() => ({
  activeTenant: 'tnt-demo' as string | null,
  can: (_: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

const navigateMock = vi.hoisted(() => vi.fn())
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigateMock,
  useRouterState: () => '',
}))

const api = vi.hoisted(() => ({
  listProfiles: vi.fn(),
  listWorkspaces: vi.fn(),
  createRun: vi.fn(),
}))
vi.mock('@/features/agentops/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/agentops/api')>()),
  agentOpsApi: api,
}))

const toasts = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock('@/components/ui/toaster', () => ({ toast: toasts }))

import { ShellLauncher } from './shell-launcher'

const PROFILE = {
  profile_ref: 'ppf_team',
  driver: 'claude',
  environment_ref: 'env-prod',
  display_name: 'Team account',
  state: 'active' as const,
  local_environment: true,
  operable: true,
}

const WORKSPACE = {
  workspace_ref: 'ws-main',
  name: 'main',
  state: 'active',
  mount_mode: 'ro',
  dlp_mode: 'off',
}

beforeEach(() => {
  vi.clearAllMocks()
  auth.activeTenant = 'tnt-demo'
  auth.can = () => true
  api.listProfiles.mockResolvedValue({ items: [PROFILE], has_more: false })
  api.listWorkspaces.mockResolvedValue({ items: [WORKSPACE], has_more: false })
  api.createRun.mockResolvedValue({ run_ref: 'run-77' })
})

async function open() {
  const user = userEvent.setup()
  renderIntel(<ShellLauncher />)
  await screen.findByTestId('launcher-input')
  return user
}

/** Choose the one profile through its real Radix trigger. */
async function pickProfile(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByTestId('launcher-profile'))
  await user.click(await screen.findByRole('option', { name: /Team account/ }))
}

describe('ShellLauncher — what it offers, and to whom', () => {
  it('is not rendered at all without the permission that would run it', async () => {
    // An offer that ends in a 403 is a magic pushbutton, which the front door does not
    // offer. The SCOPE LINE stays: knowing what the next action applies to is not a
    // privilege.
    auth.can = (p: string) => p !== 'sessions:run:write'
    renderIntel(<ShellLauncher />)
    expect(await screen.findByTestId('shell-scope-line')).toBeInTheDocument()
    expect(screen.queryByTestId('launcher-input')).toBeNull()
    expect(api.listProfiles).not.toHaveBeenCalled()
  })

  it('says WHY there is nothing to type into, and offers the one action', async () => {
    // Never a disabled field with no reason: B2 makes the profile mandatory
    // server-side, so with none registered there is exactly one thing to do.
    api.listProfiles.mockResolvedValue({ items: [], has_more: false })
    const user = userEvent.setup()
    renderIntel(<ShellLauncher />)
    expect(
      await screen.findByText(/No provider profile is registered/i),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('launcher-input')).toBeNull()
    await user.click(screen.getByTestId('launcher-add-provider'))
    expect(navigateMock).toHaveBeenCalledWith(
      expect.objectContaining({ to: '/provider-profiles' }),
    )
  })

  it('will not start without the one field the server cannot default', async () => {
    const user = await open()
    await user.type(screen.getByTestId('launcher-input'), 'nightly index')
    expect(screen.getByTestId('launcher-start')).toBeDisabled()
    await pickProfile(user)
    expect(screen.getByTestId('launcher-start')).toBeEnabled()
  })
})

describe('ShellLauncher — starting', () => {
  it('sends the reference only, and moves the address to the new session', async () => {
    const user = await open()
    await user.type(screen.getByTestId('launcher-input'), 'nightly index')
    await pickProfile(user)
    await user.click(screen.getByTestId('launcher-start'))

    await waitFor(() => expect(api.createRun).toHaveBeenCalledTimes(1))
    const body = api.createRun.mock.calls[0][0]
    expect(body.name).toBe('nightly index')
    expect(body.provider_profile_ref).toBe('ppf_team')
    // Only the REFERENCE leaves the browser: the server resolves the homes.
    expect(Object.keys(body)).not.toContain('key')

    // A run whose managed row is not proven yet is addressed BY ITS RUN — the third
    // shape `session-address` resolves — so the link is valid the instant it answers.
    await waitFor(() =>
      expect(navigateMock).toHaveBeenCalledWith(
        expect.objectContaining({
          to: '/sessions',
          search: { session: 'run:run-77' },
        }),
      ),
    )
  })

  it('Enter starts it', async () => {
    const user = await open()
    await pickProfile(user)
    await user.click(screen.getByTestId('launcher-input'))
    await user.keyboard('a{Enter}')
    await waitFor(() => expect(api.createRun).toHaveBeenCalledTimes(1))
  })

  it('Ctrl+Enter starts it and KEEPS the operator here, ready for the next one', async () => {
    // "Start in the background": launching must not block the next launch.
    const user = await open()
    await pickProfile(user)
    const field = screen.getByTestId('launcher-input')
    await user.click(field)
    await user.keyboard('first{Control>}{Enter}{/Control}')

    await waitFor(() => expect(api.createRun).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(field).toHaveValue(''))
    expect(navigateMock).not.toHaveBeenCalled()
    expect(toasts.success).toHaveBeenCalled()
    // The SCOPE is kept, so the next draft starts where the last one did.
    expect(screen.getByTestId('launcher-profile')).toHaveTextContent(
      'Team account',
    )
  })

  it('surfaces the ENGINE’s refusal, not a sentence composed here', async () => {
    api.createRun.mockRejectedValue(
      new Error('budget cap reached for this workspace'),
    )
    const user = await open()
    await pickProfile(user)
    await user.click(screen.getByTestId('launcher-start'))
    await waitFor(() =>
      expect(toasts.error).toHaveBeenCalledWith(
        'budget cap reached for this workspace',
      ),
    )
  })
})

describe('ScopeLine — the scope of the next action', () => {
  it('names the organization, and says plainly when there is no workspace yet', async () => {
    renderIntel(<ShellLauncher />)
    const line = await screen.findByTestId('shell-scope-line')
    expect(line).toHaveTextContent('tnt-demo')
    expect(line).toHaveTextContent('No workspace')
    // The environment belongs to the PROFILE, so with none chosen there is none to
    // report — and the line says so instead of borrowing a value from the topbar.
    expect(line).toHaveTextContent('Not declared')
  })

  it('shows the environment the CHOSEN profile will run in', async () => {
    const user = await open()
    await pickProfile(user)
    await waitFor(() =>
      expect(screen.getByTestId('shell-scope-line')).toHaveTextContent(
        'env-prod',
      ),
    )
  })
})

describe('ShellLauncher — the three ways to have nothing to type into', () => {
  it('with no organization, says which one to choose first', async () => {
    auth.activeTenant = null
    renderIntel(<ShellLauncher />)
    expect(
      await screen.findByText(/Select an organization/i),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('launcher-input')).toBeNull()
    expect(api.listProfiles).not.toHaveBeenCalled()
  })

  it('with no permission to READ profiles, says that, and offers no door it cannot open', async () => {
    // This principal may start a run and may not choose the one field the server
    // cannot default. Offering "add a provider profile" would send them to a screen
    // they may not read either.
    auth.can = (p: string) => p !== 'sessions:profile:read'
    renderIntel(<ShellLauncher />)
    expect(
      await screen.findByText(/does not include permission to read them/i),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('launcher-input')).toBeNull()
    expect(screen.queryByTestId('launcher-add-provider')).toBeNull()
  })

  it('keeps the scope line in every one of them', async () => {
    auth.activeTenant = null
    renderIntel(<ShellLauncher />)
    expect(await screen.findByTestId('shell-scope-line')).toBeInTheDocument()
  })
})

describe('ShellLauncher — it does not offer a control it is about to remove', () => {
  it('renders NO field while the provider-profile plane is still answering', async () => {
    // Measured by the live spec, not imagined: the first version treated "not answered
    // yet" as "there are some", so a deployment with none painted a usable field and
    // replaced it a moment later. The browser caught it as a focus() on an element that
    // had just been removed.
    let settle: (value: {
      items: never[]
      has_more: boolean
    }) => void = () => {}
    api.listProfiles.mockReturnValue(
      new Promise((resolve) => {
        settle = resolve
      }),
    )
    renderIntel(<ShellLauncher />)
    await screen.findByTestId('shell-scope-line')
    expect(screen.queryByTestId('launcher-input')).toBeNull()
    expect(screen.queryByTestId('launcher-add-provider')).toBeNull()

    settle({ items: [], has_more: false })
    // …and once it HAS answered, the honest state appears.
    expect(
      await screen.findByTestId('launcher-add-provider'),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('launcher-input')).toBeNull()
  })

  it('says a FAILED read failed, rather than calling it an empty plane', async () => {
    api.listProfiles.mockRejectedValue(new Error('boom'))
    renderIntel(<ShellLauncher />)
    expect(await screen.findByText(/could not be read/i)).toBeInTheDocument()
    expect(screen.queryByTestId('launcher-add-provider')).toBeNull()
  })
})
