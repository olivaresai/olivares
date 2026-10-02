// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, waitFor } from '@/test/intel'
import './i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
const timelineById = vi.hoisted(() => vi.fn())
vi.mock('./api', async (original) => {
  const real = await original<typeof import('./api')>()
  return { ...real, sessionsApi: { ...real.sessionsApi, timelineById } }
})

import { SessionEvidence } from './session-evidence'

describe('SessionEvidence reads the rows the session wrote on other channels (HU 029)', () => {
  it('counts the PEP hook row’s tool calls as turns and resources', async () => {
    timelineById.mockImplementation(async (ref: string) =>
      ref === 'lr-hook'
        ? {
            items: [
              {
                at: '2026-10-01T18:33:09Z',
                kind: 'tool',
                tool_ref: 'Bash',
                resource_ref: 'echo',
              },
              {
                at: '2026-10-01T18:33:10Z',
                kind: 'tool',
                tool_ref: 'Bash',
                resource_ref: 'echo',
              },
            ],
            has_more: false,
          }
        : { items: [], has_more: false },
    )
    renderIntel(
      <SessionEvidence
        liveRef="lr-managed"
        sessionRef="claude-sid"
        echoRefs={['lr-hook', 'lr-usage']}
        expanded="activity"
        onExpand={() => {}}
      />,
    )
    await waitFor(() =>
      expect(screen.getByTestId('evidence-toggle-activity')).toHaveTextContent(
        /2$/,
      ),
    )
    expect(screen.getByTestId('evidence-toggle-resources')).toHaveTextContent(
      /1$/,
    )
    expect(timelineById).toHaveBeenCalledWith('lr-hook', expect.anything())
  })

  it('a block with nothing to show is not shown, and no evidence at all when every block is empty (Root review, 09)', async () => {
    timelineById.mockImplementation(async (ref: string) =>
      ref === 'lr-hook'
        ? {
            items: [
              {
                at: '2026-10-02T08:15:09Z',
                kind: 'tool',
                tool_ref: 'Bash',
                resource_ref: 'echo',
              },
            ],
            has_more: false,
          }
        : { items: [], has_more: false },
    )
    const view = renderIntel(
      <SessionEvidence
        liveRef="lr-managed"
        echoRefs={['lr-hook']}
        expanded="activity"
        onExpand={() => {}}
      />,
    )
    expect(
      await screen.findByTestId('evidence-toggle-activity'),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('evidence-toggle-checks')).toBeNull()
    expect(screen.queryByText('This session reported no findings.')).toBeNull()
    view.unmount()
    timelineById.mockResolvedValue({ items: [], has_more: false })
    renderIntel(
      <SessionEvidence
        liveRef="lr-empty"
        expanded="activity"
        onExpand={() => {}}
      />,
    )
    await waitFor(() =>
      expect(timelineById).toHaveBeenCalledWith('lr-empty', expect.anything()),
    )
    await waitFor(() =>
      expect(screen.queryByTestId('evidence-loading')).toBeNull(),
    )
    expect(screen.queryByTestId('session-evidence')).toBeNull()
  })
})
