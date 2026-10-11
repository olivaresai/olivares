// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The microphone inside the real composer: dictated words join what was typed, the field
// keeps the focus, and nothing is sent until the person presses Send.
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'
import type { RunDTO } from '@/features/agentops/types'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 'tnt-demo',
    isSuperadmin: false,
    can: () => true,
  }),
}))

const api = vi.hoisted(() => ({
  listProfiles: vi.fn(),
  listWorkspaces: vi.fn(),
  input: vi.fn(),
  inputText: vi.fn(),
  getRun: vi.fn(),
}))
vi.mock('@/features/agentops/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/agentops/api')>()),
  agentOpsApi: api,
}))

vi.mock('@/features/dictation/capture', () => ({
  canCapture: () => true,
  startCapture: vi.fn().mockResolvedValue({
    stop: vi.fn().mockResolvedValue(new Float32Array(16_000)),
    cancel: vi.fn(),
  }),
}))
vi.mock('@/features/dictation/recognizer', () => ({
  loadRecognizer: vi.fn().mockResolvedValue(undefined),
  transcribe: vi.fn().mockResolvedValue('and run the tests.'),
}))

import { WorkComposer } from '@/components/layout/work-composer'

const RUNNING: RunDTO = {
  run_ref: 'run_live',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'running',
  last_event_seq: 0,
  pep_provisioned: true,
  record_io: true,
  critical: false,
  provider_driver: 'claude',
}

beforeEach(() => {
  api.listProfiles.mockResolvedValue({ items: [], has_more: false })
  api.listWorkspaces.mockResolvedValue({ items: [], has_more: false })
})

describe('dictation in the composer', () => {
  it('adds the words to what was typed, keeps the focus there and sends nothing', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    const field = await screen.findByTestId('launcher-input')
    await user.type(field, 'Fix the build')

    const dictate = screen.getByRole('button', { name: 'Dictate' })
    await user.click(dictate)
    await waitFor(() => expect(dictate).toHaveAttribute('aria-pressed', 'true'))
    await user.click(dictate)

    await waitFor(() =>
      expect(field).toHaveValue('Fix the build and run the tests.'),
    )
    expect(field).toHaveFocus()
    expect(api.inputText).not.toHaveBeenCalled()
    expect(api.input).not.toHaveBeenCalled()
  })

  it('cannot dictate into a session that cannot take a turn', async () => {
    renderIntel(
      <WorkComposer
        attached={{ run: { ...RUNNING, state: 'stopped' }, group: 'active' }}
      />,
    )
    expect(
      await screen.findByRole('button', { name: 'Dictate' }),
    ).toBeDisabled()
  })
})
