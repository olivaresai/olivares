// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// the honest invariants of the attack paths panel.
//
// Each case pins a sentence that would be a LIE if the code changed underneath it, and
// is written to die with one specific mutation. The ones that matter are the ways this
// screen can look right and mislead on a security surface: querying without a subject
// or with the wrong parameter (the engine answers 400 and it would look empty),
// drawing a path without saying its attribution is the WEAKEST LINK's, and reading an
// absent sensitivity as "not sensitive".
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { StrictMode, type ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { AttackPath } from './types'

const api = {
  attackReachability: vi.fn(),
  attackEscalation: vi.fn(),
  attackExfil: vi.fn(),
}

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('./api', async () => {
  const actual = await vi.importActual<typeof import('./api')>('./api')
  return { ...actual, accessMapApi: { ...actual.accessMapApi, ...api } }
})

const { AttackPathsPanel } = await import('./attack-paths')
const { AccessDetailSheet } = await import('./detail')
const { DRIFT_UNREAD } = await import('./authority')
await import('./i18n')

/** Each analysis SEALS AN AUDIT ROW, so it does not fire on selection: it is asked
 *  for. Every case that waits for data goes through here. */
async function ask() {
  await userEvent.click(
    await screen.findByRole('button', { name: /analyse attack paths/i }),
  )
}

function wrap(node: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{node}</QueryClientProvider>)
}

/** Four firm hops and one unattributed: the engine composes it to `unknown` because it
 *  takes the WEAKEST LINK (`weakestAttribution`). */
const WEAK: AttackPath = {
  kind: 'escalation',
  steps: [
    { node_kind: 'agent', node_id: 'a1', node_name: 'billing-bot' },
    { node_kind: 'role', node_id: 'r9', node_name: 'finance-admin' },
  ],
  attribution: 'unknown',
  min_confidence: 'approximate',
}

/** Without `max_sensitivity`: the field is `omitempty`, so absent and empty are the same
 *  bytes — and neither claims that nothing is sensitive. */
const NO_SENS: AttackPath = {
  kind: 'reachability',
  steps: [{ node_kind: 'agent', node_id: 'a2', node_name: 'sync-bot' }],
  attribution: 'firm',
  min_confidence: 'attributed',
}

beforeEach(() => {
  api.attackReachability.mockReset()
  api.attackEscalation.mockReset()
  api.attackExfil.mockReset()
})

describe('attack paths — without a subject nothing is queried', () => {
  it('asks the engine for NOTHING when no subject is chosen', async () => {
    wrap(<AttackPathsPanel subject={null} />)

    expect(await screen.findByTestId('attack-paths-idle')).toBeInTheDocument()
    // The mutation that kills: removing the `subject` guard. The handlers answer 400 without
    // their root id and that failure cannot be drawn as "no paths".
    expect(api.attackReachability).not.toHaveBeenCalled()
    expect(api.attackEscalation).not.toHaveBeenCalled()
    expect(api.attackExfil).not.toHaveBeenCalled()
  })

  it('an agent asks for reachability and escalation, never exfil', async () => {
    api.attackReachability.mockResolvedValue({ paths: [] })
    api.attackEscalation.mockResolvedValue({ paths: [] })
    api.attackExfil.mockResolvedValue({ paths: [] })
    wrap(<AttackPathsPanel subject={{ id: 'a1', kind: 'agent' }} />)
    await ask()

    await waitFor(() =>
      expect(api.attackReachability).toHaveBeenCalledWith('a1'),
    )
    expect(api.attackEscalation).toHaveBeenCalledWith('a1')
    expect(
      api.attackExfil,
      'EXFIL_SUBJECT_CONTRACT: agent selection must not invoke resource-scoped exfil',
    ).not.toHaveBeenCalled()
  })

  it('a resource asks for exfil with its id, never the agent analyses', async () => {
    api.attackExfil.mockResolvedValue({ paths: [] })
    wrap(<AttackPathsPanel subject={{ id: 'r7', kind: 'resource' }} />)
    await ask()

    await waitFor(() => expect(api.attackExfil).toHaveBeenCalledWith('r7'))
    expect(api.attackReachability).not.toHaveBeenCalled()
    expect(api.attackEscalation).not.toHaveBeenCalled()
  })

  it('an audited read is neither retried nor repeated on refocus', async () => {
    const qc = new QueryClient({
      defaultOptions: {
        queries: {
          retry: 1,
          retryDelay: 0,
          staleTime: 0,
          refetchOnWindowFocus: true,
          refetchOnReconnect: true,
        },
      },
    })
    api.attackExfil.mockRejectedValue(new Error('boom'))
    render(
      <StrictMode>
        <QueryClientProvider client={qc}>
          <AttackPathsPanel subject={{ id: 'r7', kind: 'resource' }} />
        </QueryClientProvider>
      </StrictMode>,
    )

    await ask()
    expect(await screen.findByRole('alert')).toBeInTheDocument()
    window.dispatchEvent(new Event('focus'))
    window.dispatchEvent(new Event('online'))
    await new Promise((resolve) => setTimeout(resolve, 50))

    expect(
      api.attackExfil,
      'EXFIL_AUDIT_CONTRACT: one deliberate analysis click must produce exactly one audited GET',
    ).toHaveBeenCalledTimes(1)
  })

  it('reopening and clicking makes a new audited read', async () => {
    const qc = new QueryClient()
    api.attackExfil.mockResolvedValue({ paths: [] })
    const first = render(
      <QueryClientProvider client={qc}>
        <AttackPathsPanel subject={{ id: 'r7', kind: 'resource' }} />
      </QueryClientProvider>,
    )
    await ask()
    await waitFor(() => expect(api.attackExfil).toHaveBeenCalledTimes(1))
    first.unmount()

    render(
      <QueryClientProvider client={qc}>
        <AttackPathsPanel subject={{ id: 'r7', kind: 'resource' }} />
      </QueryClientProvider>,
    )
    await ask()
    await waitFor(() =>
      expect(
        api.attackExfil,
        'EXFIL_AUDIT_CONTRACT: every deliberate reopen and click must produce one new audited GET',
      ).toHaveBeenCalledTimes(2),
    )
  })
})

describe('attack paths — an audited read is ASKED for', () => {
  it('with an agent chosen it does NOT query until the operator asks', async () => {
    api.attackReachability.mockResolvedValue({ paths: [NO_SENS] })
    api.attackEscalation.mockResolvedValue({ paths: [] })
    api.attackExfil.mockResolvedValue({ paths: [] })
    wrap(<AttackPathsPanel subject={{ id: 'a1', kind: 'agent' }} />)

    // POSITIVE anchor first: the button exists, so the absence means something.
    await screen.findByRole('button', { name: /analyse attack paths/i })
    // The mutation that kills: mounting the analyses on receiving `subject`. Each query
    // seals a row, so selecting would write TWO for an agent or ONE for a resource and
    // the ledger could no longer tell "investigated" from "moved the pointer".
    expect(api.attackReachability).not.toHaveBeenCalled()
    expect(api.attackEscalation).not.toHaveBeenCalled()
    expect(api.attackExfil).not.toHaveBeenCalled()
  })

  it('with a resource chosen it does not query until an explicit request either', async () => {
    api.attackExfil.mockResolvedValue({ paths: [] })
    wrap(<AttackPathsPanel subject={{ id: 'r7', kind: 'resource' }} />)

    // Leave React Query enough time to expose an eager mount. The custom message is
    // evaluated after the effect window so a first-tick absence cannot pass vacuously.
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(
      api.attackExfil,
      'EXFIL_RESOURCE_IDLE_CONTRACT: resource selection must not auto-run audited exfil',
    ).not.toHaveBeenCalled()
    expect(
      screen.getByRole('button', { name: /analyse attack paths/i }),
    ).toBeInTheDocument()
  })
})

describe("attack paths — the attribution is the weakest link's", () => {
  it('labels the weakest link and draws `unknown` as "not attributed"', async () => {
    api.attackReachability.mockResolvedValue({ paths: [] })
    api.attackEscalation.mockResolvedValue({ paths: [WEAK] })
    api.attackExfil.mockResolvedValue({ paths: [] })
    wrap(<AttackPathsPanel subject={{ id: 'a1', kind: 'agent' }} />)
    await ask()

    expect(await screen.findByText(/weakest link/i)).toBeInTheDocument()
    expect(await screen.findByText('not attributed')).toBeInTheDocument()
  })
})

describe('attack paths — a capped analysis says it is partial', () => {
  it('shows the cut note beside the paths and beside "No paths found"', async () => {
    api.attackReachability.mockResolvedValue({
      paths: [NO_SENS],
      truncated: true,
    })
    api.attackEscalation.mockResolvedValue({ paths: [], truncated: true })
    wrap(<AttackPathsPanel subject={{ id: 'a1', kind: 'agent' }} />)
    await ask()

    expect(await screen.findAllByText(/capped this analysis/i)).toHaveLength(2)
    expect(screen.getByText('No paths found.')).toBeInTheDocument()
  })

  it('shows no cut note for a complete answer', async () => {
    api.attackReachability.mockResolvedValue({ paths: [NO_SENS] })
    api.attackEscalation.mockResolvedValue({ paths: [] })
    wrap(<AttackPathsPanel subject={{ id: 'a1', kind: 'agent' }} />)
    await ask()

    expect(await screen.findByText('not stated')).toBeInTheDocument()
    expect(screen.queryByText(/capped this analysis/i)).toBeNull()
  })
})

describe('attack paths — an absent sensitivity is not "not sensitive"', () => {
  it('says "not stated" instead of staying silent', async () => {
    api.attackReachability.mockResolvedValue({ paths: [NO_SENS] })
    api.attackEscalation.mockResolvedValue({ paths: [] })
    api.attackExfil.mockResolvedValue({ paths: [] })
    wrap(<AttackPathsPanel subject={{ id: 'a1', kind: 'agent' }} />)
    await ask()

    expect(await screen.findByText('not stated')).toBeInTheDocument()
  })

  it('tells "no path" apart from "could not be read"', async () => {
    api.attackReachability.mockRejectedValue(new Error('boom'))
    api.attackEscalation.mockResolvedValue({ paths: [] })
    api.attackExfil.mockResolvedValue({ paths: [] })
    wrap(<AttackPathsPanel subject={{ id: 'a1', kind: 'agent' }} />)
    await ask()

    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toMatch(/could not be read/i)
    // Not inside waitFor on purpose: an absence assertion there holds on the first tick
    // and would pass with the defect in place.
    expect(alert.textContent).not.toMatch(/No paths found/i)
  })

  it('a 200 without a paths array is unreadable, never a zero', async () => {
    api.attackExfil.mockResolvedValue({})
    wrap(<AttackPathsPanel subject={{ id: 'r7', kind: 'resource' }} />)
    await ask()

    await waitFor(() =>
      expect(
        screen.queryByRole('alert'),
        'EXFIL_SHAPE_CONTRACT: a successful response without a paths array must render unreadable, never empty',
      ).not.toBeNull(),
    )
    expect(screen.queryByText('No paths found.')).not.toBeInTheDocument()
  })

  it('a 200 null is unreadable, never a zero or a render exception', async () => {
    api.attackExfil.mockResolvedValue(null)
    wrap(<AttackPathsPanel subject={{ id: 'r7', kind: 'resource' }} />)
    await ask()

    await waitFor(() =>
      expect(
        screen.queryByRole('alert'),
        'EXFIL_NULL_SHAPE_CONTRACT: a successful null response must render unreadable, never empty or crash',
      ).not.toBeNull(),
    )
    expect(screen.queryByText('No paths found.')).not.toBeInTheDocument()
  })

  it('a path without the contractual shape is unreadable and does not break PathRow', async () => {
    api.attackExfil.mockResolvedValue({ paths: [{}] })
    wrap(<AttackPathsPanel subject={{ id: 'r7', kind: 'resource' }} />)
    await ask()

    await waitFor(() =>
      expect(
        screen.queryByRole('alert'),
        'EXFIL_PATH_SHAPE_CONTRACT: malformed path entries must render unreadable, never empty or crash',
      ).not.toBeNull(),
    )
    expect(screen.queryByText('No paths found.')).not.toBeInTheDocument()
  })

  it('a path without any link is unreadable, never a real chain', async () => {
    api.attackExfil.mockResolvedValue({
      paths: [
        {
          kind: 'exfil',
          steps: [],
          attribution: 'firm',
          min_confidence: 'attributed',
        },
      ],
    })
    wrap(<AttackPathsPanel subject={{ id: 'r7', kind: 'resource' }} />)
    await ask()

    await waitFor(() =>
      expect(
        screen.queryByRole('alert'),
        'EXFIL_EMPTY_STEPS_CONTRACT: a path without a chain must render unreadable',
      ).not.toBeNull(),
    )
  })

  it('a valid path of another analysis is not presented under the requested label', async () => {
    api.attackReachability.mockResolvedValue({
      paths: [{ ...NO_SENS, kind: 'exfil' }],
    })
    api.attackEscalation.mockResolvedValue({ paths: [] })
    wrap(<AttackPathsPanel subject={{ id: 'a1', kind: 'agent' }} />)
    await ask()

    await waitFor(() =>
      expect(
        screen.queryByRole('alert'),
        'EXFIL_KIND_CONTRACT: response paths must match the requested analysis kind',
      ).not.toBeNull(),
    )
    expect(screen.queryByText('sync-bot')).not.toBeInTheDocument()
  })
})

// --- the wiring ----------------------------------------------------------------
//
// A component nobody renders passes ALL of its component cases. This one starts at the
// detail sheet and checks the two contractual roots: agent for
// reachability/escalation and resource for exfiltration.

const sheetProps = {
  onClose: () => {},
  // Make the cluster test exercise the actual `!selection.cluster` expansion guard.
  // Without this callback, the button is absent regardless of the guard.
  onExpand: () => {},
  // The module's own NAMED constant: building the object by hand is what broke the
  // typecheck, and two copies of a type are two places to disagree.
  drift: DRIFT_UNREAD,
  recheck: { status: 'idle' as const },
  onRecheck: () => {},
  onCheckDrift: () => {},
  canDrift: false,
}

describe('the panel is reached from the detail sheet with the correct root', () => {
  it('appears when the chosen node is an AGENT', async () => {
    api.attackReachability.mockResolvedValue({ paths: [NO_SENS] })
    api.attackEscalation.mockResolvedValue({ paths: [] })
    api.attackExfil.mockResolvedValue({ paths: [] })
    wrap(
      <AccessDetailSheet
        {...sheetProps}
        selection={{
          type: 'node',
          id: 'a2',
          kind: 'agent',
          ref: 'sync-bot',
          role: 'origin',
        }}
      />,
    )
    await ask()

    expect(await screen.findByText('not stated')).toBeInTheDocument()
  })

  it('a resource reaches exfil with resource_id', async () => {
    api.attackExfil.mockResolvedValue({ paths: [] })
    wrap(
      <AccessDetailSheet
        {...sheetProps}
        selection={{
          type: 'node',
          id: 'r7',
          kind: 'postgres.table',
          ref: 'kb:invoices',
          role: 'resource',
        }}
      />,
    )

    await ask()
    await waitFor(() => expect(api.attackExfil).toHaveBeenCalledWith('r7'))
    expect(api.attackReachability).not.toHaveBeenCalled()
    expect(api.attackEscalation).not.toHaveBeenCalled()
  })

  it('a cluster does not offer exfil because its id does not exist in the engine', async () => {
    wrap(
      <AccessDetailSheet
        {...sheetProps}
        selection={{
          type: 'node',
          id: 'cluster:appdb.public',
          kind: 'postgres.table',
          ref: 'appdb.public (1201)',
          role: 'resource',
          cluster: true,
        }}
      />,
    )

    await screen.findByText('appdb.public (1201)')
    expect(
      screen.queryByRole('button', { name: /expand neighbors/i }),
      'EXFIL_CLUSTER_EXPAND_CONTRACT: synthetic cluster selection must not expose expansion',
    ).toBeNull()
    expect(
      screen.queryByRole('button', { name: /analyse attack paths/i }),
      'EXFIL_CLUSTER_PANEL_CONTRACT: synthetic cluster selection must expose neither expand nor attack-path actions',
    ).toBeNull()
    expect(api.attackExfil).not.toHaveBeenCalled()
  })

  it('a cluster that keeps kind=agent offers no analysis either', async () => {
    wrap(
      <AccessDetailSheet
        {...sheetProps}
        selection={{
          type: 'node',
          id: 'cluster:agents',
          kind: 'agent',
          ref: 'agents (1201)',
          role: 'origin',
          cluster: true,
        }}
      />,
    )

    await screen.findByText('agents (1201)')
    expect(
      screen.queryByRole('button', { name: /analyse attack paths/i }),
      'EXFIL_AGENT_CLUSTER_CONTRACT: synthetic agent clusters must expose no attack-path action',
    ).toBeNull()
    expect(api.attackReachability).not.toHaveBeenCalled()
    expect(api.attackEscalation).not.toHaveBeenCalled()
  })
})
