// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, renderIntel, screen } from '@/test/intel'
import { http } from '@/lib/api/client'
import { preferenceStorage } from '@/lib/preference-storage'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { useModulesStore } from '@/stores/modules'

const auth = vi.hoisted(() => ({ isSuperadmin: false }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    ...auth,
    principal: { kind: 'user', user_id: 'u-1' },
    activeTenant: 't1',
    can: () => true,
  }),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
  Toaster: () => null,
}))

import { ModuleGate, QueryErrorState } from './query-error-state'

afterEach(() => {
  auth.isSuperadmin = false
  useModulesStore.getState().setOff([])
  preferenceStorage.attach(null, {})
  vi.unstubAllGlobals()
})

// 08b's real answers (EU/captures/refresh08b): a group forbid on agent:read, and a read
// of the dormant models module.
const forbidden = new ApiError(403, 'forbidden', 'forbidden')
const moduleOff = new ApiError(
  404,
  'module_not_enabled',
  'The models module is not enabled on this node. An administrator can enable it in the module settings.',
)

describe('QueryErrorState: one mapping from a failed read to what a panel says (EU18, EU20)', () => {
  it('a 403 says who can grant it and offers no Retry', () => {
    const retry = vi.fn()
    renderIntel(
      <QueryErrorState error={forbidden} subject="agents" retry={retry} />,
    )
    expect(
      screen.getByText('You do not have access to agents.'),
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        'Your roles or a group policy do not allow it. An owner or administrator of this organization can grant it.',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /retry/i })).toBeNull()
    expect(screen.queryByText('Something went wrong')).toBeNull()
  })

  it('a 403 that names the policy denying it says which', () => {
    renderIntel(
      <QueryErrorState
        error={
          new ApiError(403, 'forbidden', 'forbidden', undefined, {
            policy_name: 'Contractors',
          })
        }
      />,
    )
    expect(screen.getByText('You do not have access to this.')).toBeVisible()
    expect(
      screen.getByText(
        'The policy “Contractors” denies it. An owner or administrator of this organization can change it.',
      ),
    ).toBeInTheDocument()
  })

  it('module_not_enabled says the module is off, with the administrator’s enable action and no Retry', async () => {
    auth.isSuperadmin = true
    renderIntel(
      <QueryErrorState error={moduleOff} module="models" retry={vi.fn()} />,
    )
    expect(
      screen.getByText('Models is not enabled on this installation.'),
    ).toBeInTheDocument()
    expect(
      await screen.findByRole('button', { name: 'Turn on Models' }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /retry/i })).toBeNull()
  })

  it('a person who cannot choose modules reads the line without the action', () => {
    renderIntel(<QueryErrorState error={moduleOff} module="models" />)
    expect(
      screen.getByText('Models is not enabled on this installation.'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('a capability this build does not include (501) is said calmly, without Retry (09 signing)', () => {
    // R1 09 Community, GET /v1/m/reporting/signing, as answered.
    renderIntel(
      <QueryErrorState
        error={
          new ApiError(
            501,
            'Not Implemented',
            'evidence bundle signing is an enterprise capability and is not wired in this build',
          )
        }
        subject="report signing"
        retry={vi.fn()}
      />,
    )
    const line = screen.getByText(
      'This edition does not include report signing.',
    )
    // Inside a Community page it is one row: the sentence, no heading and no mark.
    expect(line.tagName).toBe('P')
    expect(line).toHaveAttribute('data-slot', 'not-in-edition')
    expect(line).toHaveAttribute('role', 'status')
    expect(line).toHaveClass('text-body', 'text-text-2')
    expect(line).not.toHaveClass('font-extrabold')
    expect(line.querySelector('svg')).toBeNull()
    expect(screen.queryByRole('heading')).toBeNull()
    expect(screen.queryByRole('button', { name: /retry/i })).toBeNull()
    expect(screen.queryByText('Something went wrong')).toBeNull()
  })

  it('a 5xx is the failure, with Retry and its request id', () => {
    renderIntel(
      <QueryErrorState
        error={new ApiError(500, 'internal', 'boom', 'req-08b-1')}
        retry={vi.fn()}
      />,
    )
    expect(screen.getByText('Something went wrong')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
    expect(screen.getByText('req-08b-1')).toBeInTheDocument()
  })

  it('a network failure says the engine could not be reached, with Retry', () => {
    renderIntel(
      <QueryErrorState error={new NetworkError('down')} retry={vi.fn()} />,
    )
    expect(screen.getByText('Control plane unreachable')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
  })
})

describe('ModuleGate: a panel that reads another module (EU18)', () => {
  it('does not mount its reads or its New while the module is off', () => {
    useModulesStore.getState().setOff(['models'])
    renderIntel(
      <ModuleGate module="models">
        <button type="button">New model group</button>
      </ModuleGate>,
    )
    expect(
      screen.getByText('Models is not enabled on this installation.'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'New model group' })).toBeNull()
  })

  it('mounts the panel where the module runs', () => {
    renderIntel(
      <ModuleGate module="models">
        <button type="button">New model group</button>
      </ModuleGate>,
    )
    expect(
      screen.getByRole('button', { name: 'New model group' }),
    ).toBeInTheDocument()
  })
})

// THE ONE MOUNT of the edition line: every query-backed panel renders its read's error
// through QueryErrorState, so a read the engine refuses by edition reads as one calm line
// naming the edition whole, in place of the panel's body, through this one place.
describe('QueryErrorState: a read refused by edition', () => {
  // The cases below are an administrator's, who can add a license (the license tab's gate).
  beforeEach(() => {
    auth.isSuperadmin = true
  })

  /** A panel as the console draws one: its body, or its read's error state. */
  function KnowledgePanel() {
    const read = useQuery({
      queryKey: ['knowledge-bases'],
      queryFn: () => http.get<{ items: unknown[] }>('/v1/m/knowledge/bases'),
    })
    if (read.isPending) return <p>Loading</p>
    if (read.isError)
      return (
        <QueryErrorState
          error={read.error}
          subject="knowledge bases"
          module="knowledge"
          retry={() => void read.refetch()}
        />
      )
    return <p>Your knowledge bases</p>
  }

  /** The engine answers every read with this error envelope. */
  function engineAnswers(status: number, error: Record<string, unknown>) {
    vi.stubGlobal(
      'fetch',
      vi.fn(
        async () =>
          new Response(JSON.stringify({ error }), {
            status,
            headers: { 'Content-Type': 'application/json' },
          }),
      ),
    )
  }

  const RAW =
    /\b501\b|\b403\b|business_required|edition_required|addon_requires_license|\bknowledge\b|\bsso\b/

  /** What a person can meet: the text, and every attribute a browser shows or reads aloud. */
  function expectNoRawWords() {
    expect(document.body.textContent ?? '').not.toMatch(RAW)
    for (const el of document.body.querySelectorAll('*'))
      for (const name of ['title', 'aria-label', 'aria-description'])
        expect(el.getAttribute(name) ?? '').not.toMatch(RAW)
  }

  const WAYS_ON =
    'Add a license in Settings > Edition, or see olivares.ai/pricing.'

  // The panel's module is wider than what an answer refuses: a refusal that names no
  // capability says the unnamed line, and is told again; one that names it is told once.
  const UNNAMED = 'This is part of Olivares AI Business.'

  it.each([
    [
      '403 edition_required',
      403,
      {
        code: 'edition_required',
        message: 'Knowledge is part of Olivares AI Engineer.',
        edition: 'engineer',
        capability: 'knowledge',
      },
      'Knowledge is part of Olivares AI Engineer.',
      'Olivares AI Engineer',
    ],
    [
      '501 business_required, naming no edition',
      501,
      { code: 'business_required', message: 'requires Business' },
      UNNAMED,
      null,
    ],
    [
      '501 business_required naming its edition, as the engine answers now',
      501,
      {
        code: 'business_required',
        message: 'requires Business',
        edition: 'business',
      },
      UNNAMED,
      null,
    ],
    [
      '403 addon_requires_license naming its edition',
      403,
      {
        code: 'addon_requires_license',
        message: 'the identity add-on is required',
        edition: 'business',
      },
      UNNAMED,
      null,
    ],
    [
      '501 departments_unavailable, an older code that names its edition',
      501,
      {
        code: 'departments_unavailable',
        message: 'departments are not available',
        edition: 'business',
      },
      UNNAMED,
      null,
    ],
  ])(
    '%s: one line in place of the body, never naming the panel for it',
    async (_, status, error, lead, tag) => {
      engineAnswers(status, error)
      renderIntel(<KnowledgePanel />)
      const line = await screen.findByText(lead, { exact: false })
      expect(line.closest('[role="status"]')).toHaveTextContent(
        `${lead} ${WAYS_ON}`,
      )
      expect(screen.getAllByRole('status')).toHaveLength(1)
      expect(screen.queryByText('Your knowledge bases')).toBeNull()
      expect(screen.queryByText(/You do not have access/)).toBeNull()
      expect(screen.queryByText(/does not include/)).toBeNull()
      expect(screen.queryByRole('button', { name: /retry/i })).toBeNull()
      expectNoRawWords()

      cleanup()
      renderIntel(<KnowledgePanel />)
      if (tag) {
        expect(await screen.findByText(tag)).toBeInTheDocument()
        expect(screen.queryByText(/is part of|Add a license/)).toBeNull()
      } else {
        expect(
          await screen.findByText(lead, { exact: false }),
        ).toBeInTheDocument()
      }
      expect(screen.queryByText('Your knowledge bases')).toBeNull()
      expect(screen.queryByText(/You do not have access/)).toBeNull()
      expectNoRawWords()
    },
  )

  // Whoever cannot add a license (the license tab's own gate) is never sent to a place they
  // cannot use: the line asks them to ask an administrator, with no link.
  it('a member is told to ask an administrator, with no way to a place they cannot use', async () => {
    auth.isSuperadmin = false
    engineAnswers(403, {
      code: 'edition_required',
      message: 'Knowledge is part of Olivares AI Engineer.',
      edition: 'engineer',
      capability: 'knowledge',
    })
    renderIntel(<KnowledgePanel />)
    const line = await screen.findByText(/Knowledge is part of/)
    expect(line.closest('[role="status"]')).toHaveTextContent(
      'Knowledge is part of Olivares AI Engineer. Ask an administrator.',
    )
    expect(screen.queryByText(/Add a license|olivares\.ai\/pricing/)).toBeNull()
    expect(screen.queryByRole('link')).toBeNull()
  })

  it('an administrator gets both ways on, as links', async () => {
    engineAnswers(403, {
      code: 'edition_required',
      message: 'Knowledge is part of Olivares AI Engineer.',
      edition: 'engineer',
      capability: 'knowledge',
    })
    renderIntel(<KnowledgePanel />)
    expect(
      await screen.findByRole('link', { name: 'Settings > Edition' }),
    ).toHaveAttribute('href', '/settings?section=edition')
    expect(
      screen.getByRole('link', { name: 'olivares.ai/pricing' }),
    ).toHaveAttribute('href', 'https://olivares.ai/pricing')
  })

  it('403 addon_requires_license naming no edition: the engine sentence, never «no access»', async () => {
    engineAnswers(403, {
      code: 'addon_requires_license',
      message: 'The identity add-on is not licensed. Reads keep working.',
    })
    renderIntel(<KnowledgePanel />)
    expect(
      await screen.findByText(
        'The identity add-on is not licensed. Reads keep working.',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByText(/You do not have access/)).toBeNull()
    expect(screen.queryByText(/Olivares AI/)).toBeNull()
    expect(screen.queryByText('Your knowledge bases')).toBeNull()
    expectNoRawWords()
  })

  it('a plain 403 stays the no-permission state', async () => {
    engineAnswers(403, { code: 'forbidden', message: 'forbidden' })
    renderIntel(<KnowledgePanel />)
    expect(
      await screen.findByText('You do not have access to knowledge bases.'),
    ).toBeInTheDocument()
    expect(screen.queryByText(/Olivares AI/)).toBeNull()
  })

  it('a 404 module_not_enabled stays the module-off line', async () => {
    engineAnswers(404, {
      code: 'module_not_enabled',
      message: 'The knowledge module is not enabled on this node.',
      module: 'knowledge',
    })
    renderIntel(<KnowledgePanel />)
    expect(
      await screen.findByText('Knowledge is not enabled on this installation.'),
    ).toBeInTheDocument()
    expect(screen.queryByText(/Olivares AI/)).toBeNull()
  })

  it('a 500 stays the failure, with Retry', async () => {
    engineAnswers(500, { code: 'internal', message: 'boom' })
    renderIntel(<KnowledgePanel />)
    expect(await screen.findByText('Something went wrong')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
    expect(screen.queryByText(/Olivares AI/)).toBeNull()
  })
})
