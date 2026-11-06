// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'

const address = vi.hoisted(() => ({ race: undefined as string | undefined }))
vi.mock('@/lib/hooks/use-url-state', () => ({ useUrlState: () => [address] }))
vi.mock('./sessions-workspace-view', () => ({
  SessionsWorkspaceView: () => <div>Usual sessions</div>,
}))
vi.mock('./session-race', () => ({
  SessionRace: ({ refs }: { refs: string[] }) => (
    <div>Race: {refs.join(',')}</div>
  ),
}))
import { RaceAwareSessionsView } from './session-race-route'

beforeEach(() => {
  address.race = undefined
})

it('preserves the usual sessions destination without a race address', () => {
  render(<RaceAwareSessionsView entrance="operate" />)
  expect(screen.getByText('Usual sessions')).toBeInTheDocument()
})

it.each([
  'not-a-run,another',
  '019aaa00-0000-7000-8000-000000000001',
  '019aaa00-0000-7000-8000-000000000001,019aaa00-0000-7000-8000-000000000001',
])('refuses an invalid or duplicate race address: %s', (value) => {
  address.race = value
  render(<RaceAwareSessionsView entrance="operate" />)
  expect(screen.getByRole('alert')).toBeInTheDocument()
  expect(screen.queryByText(/^Race:/)).toBeNull()
})

it('opens distinct session references from the shared race link', () => {
  address.race =
    '019aaa00-0000-7000-8000-000000000001,019aaa00-0000-7000-8000-000000000002'
  render(<RaceAwareSessionsView entrance="operate" />)
  expect(screen.getByText(`Race: ${address.race}`)).toBeInTheDocument()
})
