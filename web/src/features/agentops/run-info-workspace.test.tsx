// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import './i18n'

import { RunInfo } from './run-detail'
import type { RunDTO } from './types'

// Measured 2026-09-18: a governed session created without a workspace
// ran the agent in the ENGINE's own working directory, and no surface said where a
// session was working at all — the walk could only answer it by reading the child's
// own init frame. The engine now gives such a session a directory of its own and
// records it; this is the console half of showing it BY NAME.

const base: RunDTO = {
  run_ref: 'run-1',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'running',
  last_event_seq: 3,
  pep_provisioned: false,
  record_io: false,
  critical: false,
}

describe('RunInfo names the directory a session works in', () => {
  it("shows the session's OWN directory and says that is what it is", () => {
    render(
      <RunInfo
        run={{
          ...base,
          workspace_path: '/var/lib/olivares/session-workspaces/run-1',
        }}
      />,
    )
    expect(
      screen.getByText(
        '/var/lib/olivares/session-workspaces/run-1 (its own directory for this session)',
      ),
    ).toBeInTheDocument()
  })

  it('shows a registered workspace as the path it resolved to, without that label', () => {
    render(
      <RunInfo
        run={{
          ...base,
          workspace_ref: 'ws-7',
          workspace_path: '/srv/projects/acme',
        }}
      />,
    )
    expect(screen.getByText('/srv/projects/acme')).toBeInTheDocument()
    expect(
      screen.queryByText(/its own directory for this session/),
    ).not.toBeInTheDocument()
  })

  it('reads as none for a run that predates the recorded directory', () => {
    render(<RunInfo run={base} />)
    // The label is present AND its value is the explicit "None", not an empty cell:
    // a run that never recorded a directory is a different fact from one working in
    // the engine's tree, and the row must not read as either by omission.
    //
    // The value is read through the label's own row (KvRow renders <dt> label and
    // <dd> value as siblings) rather than by text: "None" is the value of most
    // rows on a bare run, so a bare getByText would match somebody else's cell —
    // or throw on the several it finds.
    const label = screen.getByText('Working directory')
    const value = label.nextElementSibling
    expect(value).not.toBeNull()
    expect(value).toHaveTextContent(/^None$/)
  })
})
