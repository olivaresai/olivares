// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Estate's last hour at fleet scale (#2565): past FLEET_SESSIONS live sessions a build
// that supplies `estateFleet` draws it instead of the swimlanes, with the swimlanes' own props.
// Community supplies none and keeps the swimlanes at any count.
import type { ReactNode } from 'react'
import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mergeSessions } from '@/features/sessions/provenance'
import type { SwimlaneProps } from './swimlane'
import { approval, NOW, run } from './swimlane.fixture'
import { FLEET_SESSIONS } from './swimlane-model'
import { EstateLanes } from './estate-lanes'

const panels = vi.hoisted(() => ({
  estateFleet: undefined as undefined | ((p: SwimlaneProps) => ReactNode),
}))
vi.mock('@/features/extensions', () => ({ PANEL_EXTENSIONS: panels }))
// The swimlanes read every session's hour; here only which of the two is drawn counts.
vi.mock('./swimlane', () => ({
  Swimlane: (p: SwimlaneProps) => <p>lanes for {p.sessions.length} sessions</p>,
}))

afterEach(() => {
  panels.estateFleet = undefined
})

/** `live` running sessions and one that ended, as Estate merges them. */
function estate(live: number): SwimlaneProps {
  const runs = Array.from({ length: live }, (_, i) => run(`r${i}`, 'running'))
  runs.push(run('ended', 'succeeded'))
  return {
    sessions: mergeSessions([], runs),
    approvals: [],
    now: NOW,
  }
}

const fleet = (p: SwimlaneProps) => (
  <p>
    fleet for {p.sessions.length} sessions
    <span>
      {p.approvals.length} approvals, read {String(p.approvalsRead)}, at {p.now}
    </span>
    <button type="button" onClick={() => p.onOpen?.('live:x')}>
      open
    </button>
  </p>
)

describe('EstateLanes', () => {
  it('draws the fleet panel past FLEET_SESSIONS live sessions, with the same sessions', () => {
    panels.estateFleet = fleet
    render(<EstateLanes {...estate(FLEET_SESSIONS + 1)} />)
    expect(
      screen.getByText(`fleet for ${FLEET_SESSIONS + 2} sessions`),
    ).toBeInTheDocument()
    expect(screen.queryByText(/^lanes/)).toBeNull()
  })

  it('hands the fleet panel every prop the lanes get', () => {
    panels.estateFleet = fleet
    const onOpen = vi.fn()
    render(
      <EstateLanes
        {...estate(FLEET_SESSIONS + 1)}
        approvals={[approval('ap-1', 'osn_r1', '16:30')]}
        approvalsRead={false}
        onOpen={onOpen}
      />,
    )
    expect(
      screen.getByText(`1 approvals, read false, at ${NOW}`),
    ).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'open' }))
    expect(onOpen).toHaveBeenCalledWith('live:x')
  })

  it('keeps the lanes at FLEET_SESSIONS live sessions: an ended one does not count', () => {
    panels.estateFleet = fleet
    render(<EstateLanes {...estate(FLEET_SESSIONS)} />)
    expect(
      screen.getByText(`lanes for ${FLEET_SESSIONS + 1} sessions`),
    ).toBeInTheDocument()
    expect(screen.queryByText(/^fleet/)).toBeNull()
  })

  it('keeps the lanes at any count when the build supplies no fleet panel', () => {
    render(<EstateLanes {...estate(200)} />)
    expect(screen.getByText('lanes for 201 sessions')).toBeInTheDocument()
  })
})
