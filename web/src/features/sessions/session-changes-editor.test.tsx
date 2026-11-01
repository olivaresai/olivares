// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A changed file opens in the person's own editor: the link follows the editor they chose in
// Settings, lands on the first line that differs from git HEAD, and takes the editor's SSH
// form when the session's folder is on another machine.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: { changes: vi.fn(), changedFile: vi.fn() },
}))

import { agentOpsApi } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import {
  CLIENT_SETTING_DEFAULTS,
  useClientSettings,
} from '@/features/settings/preferences'
import { SessionChanges } from './session-changes'

const withFolder = {
  run_ref: 'run_1',
  state: 'stopped',
  workspace_path: '/srv/example/project',
} as unknown as RunDTO
const withoutFolder = {
  run_ref: 'run_1',
  state: 'stopped',
} as unknown as RunDTO

function file(path: string, text: string) {
  return { path, size: text.length, text, binary: false, truncated: false }
}

async function openReadme(run: RunDTO) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <SessionChanges run={run} />
    </QueryClientProvider>,
  )
  await userEvent.setup().click(await screen.findByText('README.md'))
  await screen.findByTestId('changes-file')
}

beforeEach(() => {
  vi.clearAllMocks()
  useClientSettings.setState({ ...CLIENT_SETTING_DEFAULTS })
  vi.mocked(agentOpsApi.changes).mockResolvedValue({
    folder: 'project',
    files: [
      { path: 'README.md', size: 30, modified_at: '2026-10-06T10:00:00Z' },
    ],
    truncated: false,
  })
  vi.mocked(agentOpsApi.changedFile).mockImplementation(
    async (_run, path, opts) =>
      opts?.rev === 'HEAD'
        ? file(path, '# Title\n')
        : file(path, '# Title\nchanged by the agent\n'),
  )
})

describe('SessionChanges — open in the editor', () => {
  it('offers the file in VS Code at the first line that differs from HEAD', async () => {
    await openReadme(withFolder)
    expect(
      await screen.findByRole('link', { name: 'Open in VS Code' }),
    ).toHaveAttribute('href', 'vscode://file/srv/example/project/README.md:2')
  })

  it('follows the editor chosen in Settings', async () => {
    useClientSettings.setState({ editor: 'zed' })
    await openReadme(withFolder)
    expect(
      await screen.findByRole('link', { name: 'Open in Zed' }),
    ).toHaveAttribute('href', 'zed://file/srv/example/project/README.md:2')
  })

  it('takes the editor’s SSH form when the folder is on another machine', async () => {
    useClientSettings.setState({ editorSsh: 'me@devbox' })
    await openReadme(withFolder)
    expect(
      await screen.findByRole('link', { name: 'Open in VS Code' }),
    ).toHaveAttribute(
      'href',
      'vscode://vscode-remote/ssh-remote+me@devbox/srv/example/project/README.md:2',
    )
  })

  it('offers nothing for a session that has no folder here', async () => {
    await openReadme(withoutFolder)
    expect(screen.queryByRole('link', { name: /Open in/ })).toBeNull()
  })

  it('offers nothing when the SSH host cannot be a host', async () => {
    useClientSettings.setState({ editorSsh: 'a/b?c' })
    await openReadme(withFolder)
    expect(screen.queryByRole('link', { name: /Open in/ })).toBeNull()
  })
})
