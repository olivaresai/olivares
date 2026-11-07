// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useRef } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { agentOpsApi } from './api'
import { PassageNotes } from './passage-notes'
import type { RunDTO, WorkspaceDTO } from './types'
import './i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ can: () => true }),
}))

const workspace: WorkspaceDTO = {
  workspace_ref: 'ws-1',
  root_path: '/work/shop',
  mount_mode: 'rw',
  max_read_bytes: 1048576,
  dlp_mode: 'off',
  state: 'active',
}
const live: RunDTO = {
  run_ref: 'r-1',
  name: 'alpha',
  workspace_ref: 'ws-1',
  provider_driver: 'claude',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'running',
  last_event_seq: 0,
  pep_provisioned: true,
  record_io: false,
  critical: false,
}

function Rendered() {
  const ref = useRef<HTMLElement | null>(null)
  return (
    <>
      <article ref={ref}>
        <p>The checkout keeps the old postal rule.</p>
      </article>
      <PassageNotes
        workspace={workspace}
        path="docs/guide.md"
        container={ref}
      />
    </>
  )
}

describe('PassageNotes — notes on a Markdown passage, sent to a session', () => {
  it('quotes the selected passage with its note and sends them to a live session of the workspace', async () => {
    vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue({
      items: [live, { ...live, run_ref: 'r-2', workspace_ref: 'ws-other' }],
      has_more: false,
    })
    const input = vi
      .spyOn(agentOpsApi, 'input')
      .mockResolvedValue({ accepted: true })
    const user = userEvent.setup()
    render(
      <QueryClientProvider client={new QueryClient()}>
        <Rendered />
      </QueryClientProvider>,
    )
    const range = document.createRange()
    range.selectNodeContents(
      screen.getByText('The checkout keeps the old postal rule.'),
    )
    window.getSelection()!.removeAllRanges()
    window.getSelection()!.addRange(range)
    await user.click(screen.getByRole('button', { name: 'Add a note' }))
    await user.type(
      screen.getByRole('textbox', { name: 'Note' }),
      'Validate Canadian codes too.',
    )
    await user.click(screen.getByRole('button', { name: 'Add' }))
    // Only the live session of this workspace is offered.
    const target = await screen.findByRole('combobox', {
      name: 'Send notes to',
    })
    expect(target.querySelectorAll('option')).toHaveLength(1)
    await user.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(input).toHaveBeenCalledTimes(1))
    const [ref, line] = input.mock.calls[0]!
    expect(ref).toBe('r-1')
    const text = JSON.parse(line).message.content as string
    expect(text).toBe(
      'Notes on docs/guide.md:\n\n> The checkout keeps the old postal rule.\n\nValidate Canadian codes too.',
    )
  })
})
