// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axe from 'axe-core'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, authState } = vi.hoisted(() => ({
  api: {
    searchSubjects: vi.fn(),
    searchResources: vi.fn(),
    accessReviewExport: vi.fn(),
    effectiveRights: vi.fn(),
  },
  authState: { can: (_p: string): boolean => true },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: api }
})

import { AccessReviewSection } from './roles-access-review-section'
// Shared with the CLI fixture tests.
import answer from './testdata/effective-rights.json'

function renderSection() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <AccessReviewSection />
    </QueryClientProvider>,
  )
}

async function ask(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole('tab', { name: 'Why' }))
  await user.type(screen.getByLabelText(/Subject id/), ' u-1 ')
  await user.type(screen.getByLabelText(/Resource id/), 'a-1')
  await user.click(screen.getByRole('button', { name: 'Explain' }))
}

describe('EffectiveRightsPanel', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    authState.can = () => true
  })

  it('asks the engine for the subject at the node and shows its path and rights', async () => {
    api.effectiveRights.mockResolvedValue(answer)
    const user = userEvent.setup()
    renderSection()

    await ask(user)

    expect(api.effectiveRights).toHaveBeenCalledExactlyOnceWith({
      subject_type: 'user',
      subject_id: 'u-1',
      kind: 'agent',
      id: 'a-1',
    })
    const path = await screen.findByRole('list', { name: 'Path' })
    const steps = within(path).getAllByRole('listitem')
    expect(steps.map((s) => s.textContent)).toEqual([
      'workspacepayments',
      'agent_groupops(in workspace default)',
      'agenta-1',
    ])
    const rows = within(screen.getByRole('table')).getAllByRole('row').slice(1)
    expect(rows.map((r) => r.textContent)).toEqual([
      'SupervisorNot heldRemoved by IRF irf-1 at workspace payments',
      'BrowseHeldGrant g-1 at workspace payments via user u-1 > group g-1 > group g-2',
      'ReadNot heldRemoved by forbid forbid-1 at workspace payments',
      'WriteUnknown',
      'Access ControlNot applicable',
    ])
    expect(
      screen.getByText('Directory membership as of 2026-10-27T06:30:00Z'),
    ).toBeInTheDocument()
    expect(screen.getByText(/It is not a deny/)).toBeInTheDocument()
  })

  it('accepts an answer from the previous version without explanation fields', async () => {
    api.effectiveRights.mockResolvedValue({
      subject: answer.subject,
      node: answer.node,
      assurance: answer.assurance,
      path: answer.path,
      rights: [{ name: 'Read', state: 'held' }],
    })
    const user = userEvent.setup()
    renderSection()

    await ask(user)

    expect(await screen.findByRole('table')).toHaveTextContent('ReadHeld')
    expect(
      screen.queryByText(/Directory membership as of/),
    ).not.toBeInTheDocument()
    expect(screen.queryByText(/Grant /)).not.toBeInTheDocument()
  })

  it('renders the form and the answer without accessibility violations', async () => {
    api.effectiveRights.mockResolvedValue(answer)
    const user = userEvent.setup()
    const { container } = renderSection()

    await ask(user)
    await screen.findByRole('list', { name: 'Path' })

    // JSDOM has no layout or computed colors, so contrast belongs to the browser checks.
    const results = await axe.run(container, {
      rules: { 'color-contrast': { enabled: false } },
    })
    expect(results.violations.map((v) => `${v.id}: ${v.nodes.length}`)).toEqual(
      [],
    )
  })

  it('shows no unknown note when every right was decided', async () => {
    api.effectiveRights.mockResolvedValue({
      ...answer,
      rights: answer.rights.filter((r) => r.state !== 'unknown'),
    })
    const user = userEvent.setup()
    renderSection()

    await ask(user)

    await screen.findByRole('list', { name: 'Path' })
    expect(screen.queryByText(/It is not a deny/)).not.toBeInTheDocument()
  })

  it('asks for a token subject at a session when those are chosen', async () => {
    api.effectiveRights.mockResolvedValue(answer)
    const user = userEvent.setup()
    renderSection()

    await user.click(screen.getByRole('tab', { name: 'Why' }))
    await user.click(screen.getByRole('combobox', { name: 'Subject type' }))
    await user.click(await screen.findByRole('option', { name: 'Token' }))
    await user.click(screen.getByRole('combobox', { name: 'Resource type' }))
    await user.click(await screen.findByRole('option', { name: 'Session' }))
    await user.type(screen.getByLabelText(/Subject id/), 't-9')
    await user.type(screen.getByLabelText(/Resource id/), 's-1')
    await user.click(screen.getByRole('button', { name: 'Explain' }))

    expect(api.effectiveRights).toHaveBeenCalledExactlyOnceWith({
      subject_type: 'token',
      subject_id: 't-9',
      kind: 'session',
      id: 's-1',
    })
  })

  it('clears the answer when the question changes', async () => {
    api.effectiveRights.mockResolvedValue(answer)
    const user = userEvent.setup()
    renderSection()

    await ask(user)
    await screen.findByRole('list', { name: 'Path' })
    await user.type(screen.getByLabelText(/Subject id/), '2')

    expect(screen.queryByRole('list', { name: 'Path' })).not.toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })

  it('refuses a body without the path or the rights', async () => {
    api.effectiveRights.mockResolvedValue({})
    const user = userEvent.setup()
    renderSection()

    await ask(user)

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Could not run the query',
    )
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })

  it('shows the engine refusal instead of an answer', async () => {
    api.effectiveRights.mockRejectedValue(new Error('not found'))
    const user = userEvent.setup()
    renderSection()

    await ask(user)

    expect(await screen.findByRole('alert')).toHaveTextContent('not found')
    expect(screen.queryByRole('list', { name: 'Path' })).not.toBeInTheDocument()
  })

  it('is offered only to an authorization administrator', () => {
    authState.can = (p) => p === 'authz:read'
    renderSection()

    expect(
      screen.getByRole('tab', { name: /Who can access/ }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('tab', { name: 'Why' })).not.toBeInTheDocument()
  })
})
