// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import './i18n'
const { api, auth, toast } = vi.hoisted(() => ({
  api: {
    inventory: vi.fn(),
    plan: vi.fn(),
    install: vi.fn(),
    job: vi.fn(),
    detect: vi.fn(),
  },
  auth: {
    isSuperadmin: true,
    // The tool card asks can('sessions:provider:read') (the refused-key read). These
    // cases are about the install and sign-in rows, so the role reads no providers.
    can: () => false,
    activeTenant: null,
    principal: { user_id: 'root' },
  },
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))
vi.mock('./api', () => ({ agentToolsApi: api }))
vi.mock('@/components/ui/toaster', () => ({ toast }))
// The first-hour cards at the top read the tools' own sign-in status; these cases
// are about the version manager below them, so the tools answer "installed, signed in".
const signIn = vi.hoisted(() => ({
  status: vi.fn((driver: string) =>
    Promise.resolve({ driver, installed: true, signed_in: true }),
  ),
}))
vi.mock('@/features/first-hour/api', async (orig) => ({
  ...((await orig()) as object),
  signInApi: signIn,
}))
import { ApiError } from '@/lib/api/errors'
import { AgentToolsView } from './agent-tools-view'
function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <AgentToolsView />
    </QueryClientProvider>,
  )
}
beforeEach(() => {
  vi.clearAllMocks()
  auth.isSuperadmin = true
  api.inventory.mockResolvedValue({
    drivers: ['claude', 'codex', 'grok'],
    inventory: { installed: [], leftovers: [] },
    read_only: false,
    jobs: [],
  })
  api.plan.mockResolvedValue({
    driver: 'claude',
    version: '2.1.261',
    digest: 'approved-digest',
    verification: 'openpgp',
    executable: '/tools/claude/claude',
  })
  api.install.mockResolvedValue({
    id: 'job1',
    state: 'running',
    driver: 'claude',
    version: '2.1.261',
    progress: 'downloading',
  })
  api.job.mockResolvedValue({
    id: 'job1',
    state: 'succeeded',
    driver: 'claude',
    version: '2.1.261',
    progress: 'placed',
  })
})
it('requires a system administrator without reading host inventory', async () => {
  auth.isSuperadmin = false
  mount()
  expect(await screen.findByText(/system administrator/i)).toBeInTheDocument()
  expect(api.inventory).not.toHaveBeenCalled()
})
it('previews the verified version before submitting the exact approval and reports the outcome', async () => {
  mount()
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: /review claude code/i }),
  )
  expect(await screen.findByText('2.1.261')).toBeInTheDocument()
  expect(api.install).not.toHaveBeenCalled()
  await user.click(
    screen.getByRole('button', { name: /install approved version/i }),
  )
  await waitFor(() =>
    expect(api.install).toHaveBeenCalledWith(
      expect.objectContaining({
        plan_digest: 'approved-digest',
        request_id: expect.any(String),
      }),
      expect.any(Object),
    ),
  )
  expect(await screen.findByText(/installation complete/i)).toBeInTheDocument()
})
it('never offers installation after planning fails', async () => {
  api.plan.mockRejectedValue(new Error('Signature verifier unavailable'))
  mount()
  await userEvent.click(
    await screen.findByRole('button', { name: /review claude code/i }),
  )
  expect(
    await screen.findByText('Signature verifier unavailable'),
  ).toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: /install approved version/i }),
  ).not.toBeInTheDocument()
})
it('keeps the same request ID when retrying an ambiguous install response', async () => {
  api.install
    .mockRejectedValueOnce(new Error('Connection lost'))
    .mockResolvedValue({ id: 'job1', state: 'succeeded' })
  mount()
  await userEvent.click(
    await screen.findByRole('button', { name: /review claude code/i }),
  )
  await userEvent.click(
    await screen.findByRole('button', { name: /install approved version/i }),
  )
  await screen.findByText('Connection lost')
  await userEvent.click(
    screen.getByRole('button', { name: /install approved version/i }),
  )
  await waitFor(() => expect(api.install).toHaveBeenCalledTimes(2))
  expect(api.install.mock.calls[0][0]).toEqual(api.install.mock.calls[1][0])
})

it('states the verification strength of every available tool before planning', async () => {
  api.inventory.mockResolvedValue({
    drivers: ['claude', 'codex', 'grok', 'opencode', 'ollama'],
    verification_levels: {
      claude: 'openpgp',
      codex: 'sigstore-cosign',
      grok: 'none-origin-only',
      opencode: 'github-release-sha256',
      ollama: 'github-release-sha256',
    },
    inventory: { installed: [], leftovers: [] },
    read_only: false,
    jobs: [],
  })
  mount()
  expect(
    await screen.findByText('Publisher-signed release (OpenPGP).'),
  ).toBeInTheDocument()
  expect(
    screen.getByText('Package digest and publisher signatures verified.'),
  ).toBeInTheDocument()
  expect(
    screen.getByText('Official HTTPS origin; no publisher signature.'),
  ).toBeInTheDocument()
  expect(
    screen.getAllByText(
      'Pinned SHA-256 from the official release metadata; no publisher signature.',
    ),
  ).toHaveLength(2)
  expect(api.plan).not.toHaveBeenCalled()
})

it('requires an explicit path action to probe an unregistered CLI version', async () => {
  api.detect
    .mockResolvedValueOnce({
      candidates: [
        {
          path: '/usr/local/bin/claude',
          match: 'unregistered-observed',
          executable: true,
          probe_skipped: 'Not executed without explicit selection',
        },
      ],
    })
    .mockResolvedValueOnce({
      candidates: [
        {
          path: '/usr/local/bin/claude',
          match: 'unregistered-observed',
          executable: true,
          version: '2.1.261',
        },
      ],
    })
  const user = userEvent.setup()
  mount()
  await user.click(
    await screen.findByRole('button', { name: 'Detect Claude Code' }),
  )
  expect(
    await screen.findByText('Not executed without explicit selection'),
  ).toBeInTheDocument()
  expect(api.detect).toHaveBeenCalledTimes(1)
  await user.click(
    screen.getByRole('button', {
      name: 'Probe version at /usr/local/bin/claude',
    }),
  )
  await waitFor(() => expect(api.detect).toHaveBeenCalledTimes(2))
  expect(await screen.findByText(/2.1.261/)).toBeInTheDocument()
})

it('shows a top-level detection failure instead of claiming no executable exists', async () => {
  api.detect.mockResolvedValueOnce({
    candidates: [],
    probe_error: 'Host inventory could not be read',
  })
  mount()
  await userEvent.click(
    await screen.findByRole('button', { name: 'Detect Claude Code' }),
  )
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Host inventory could not be read',
  )
  expect(
    screen.queryByText('No host executable detected.'),
  ).not.toBeInTheDocument()
})

it('reports a failed selected probe without a completion toast', async () => {
  api.detect
    .mockResolvedValueOnce({
      candidates: [
        {
          path: '/usr/local/bin/claude',
          match: 'unregistered-observed',
          executable: true,
        },
      ],
    })
    .mockResolvedValueOnce({
      candidates: [],
      probe_error: 'The selected host probe failed',
    })
  mount()
  await userEvent.click(
    await screen.findByRole('button', { name: 'Detect Claude Code' }),
  )
  await userEvent.click(
    await screen.findByRole('button', {
      name: 'Probe version at /usr/local/bin/claude',
    }),
  )
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'The selected host probe failed',
  )
  expect(toast.success).not.toHaveBeenCalled()
})

it.each([
  { probe_error: 'probe exited 1: broken runtime' },
  { probe_skipped: 'refused: selected executable is not safe to run' },
])('preserves the selected candidate failure reason: %o', async (failure) => {
  const path = '/usr/local/bin/claude'
  api.detect
    .mockResolvedValueOnce({
      candidates: [{ path, match: 'unregistered-observed', executable: true }],
    })
    .mockResolvedValueOnce({
      candidates: [
        { path, match: 'unregistered-observed', executable: true, ...failure },
      ],
      probe_error:
        'One or more selected probes failed. Inspect each candidate’s reason.',
    })
  mount()
  await userEvent.click(
    await screen.findByRole('button', { name: 'Detect Claude Code' }),
  )
  await userEvent.click(
    await screen.findByRole('button', { name: `Probe version at ${path}` }),
  )
  expect(await screen.findByRole('alert')).toHaveTextContent(
    failure.probe_error ?? failure.probe_skipped!,
  )
  expect(toast.success).not.toHaveBeenCalled()
})

// EU18/EU20 (WEB 01f5fe2b): the page maps a failed read like every other panel. A
// refused inventory says the person has no access, not that the engine failed.
it('says the person has no access when the inventory read is refused', async () => {
  api.inventory.mockRejectedValue(
    new ApiError(
      403,
      'forbidden',
      'A system administrator session is required.',
    ),
  )
  mount()
  expect(
    await screen.findByText('You do not have access to Agent tools.'),
  ).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /try again|retry/i })).toBeNull()
})

// EU on RC10: Claude Code showed "Installed" in its card and "No managed installation"
// below it. A tool on the server that Olivares did not install now says so.
it('says a tool found on the server was not installed by Olivares', async () => {
  mount()
  // Claude Code and Codex; findAll resolves at its first match, so wait for both.
  await waitFor(() =>
    expect(
      screen.getAllByText('Installed on this server, not by Olivares'),
    ).toHaveLength(2),
  )
  // Grok Build has no such status: its line is unchanged.
  expect(screen.getByText('No managed installation')).toBeInTheDocument()
})

// HU2-30 (RC10): after Review -> Install, the sign-in row kept "Not installed" next to
// "Installed" until a reload. A finished install reads the tools' own status again.
it('reads the tool status again when an install finishes', async () => {
  mount()
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: /review claude code/i }),
  )
  await screen.findByText('2.1.261')
  const before = signIn.status.mock.calls.filter(([d]) => d === 'claude').length
  await user.click(
    screen.getByRole('button', { name: /install approved version/i }),
  )
  expect(await screen.findByText(/installation complete/i)).toBeInTheDocument()
  await waitFor(() =>
    expect(
      signIn.status.mock.calls.filter(([d]) => d === 'claude').length,
    ).toBeGreaterThan(before),
  )
})
