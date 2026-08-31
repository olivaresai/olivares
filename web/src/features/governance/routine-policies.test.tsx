// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// · plan 3.6 — the routine-policy console. These tests exist for one
// reason above all others: the two list controls are TRI-STATE, and a `?? []`
// anywhere in the render path paints a policy that DENIES EVERY CRON as one
// that constrains nothing. The engine keeps the states distinct on purpose
// (routines.go:55-61); the console has to keep them distinct on screen.
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import {
  routineListState,
  type RoutinePolicyDTO,
  type RoutinePostureDTO,
} from './types'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

const authState = vi.hoisted(() => ({
  activeTenant: 'tenant-one' as string | null,
  can: (_permission: string): boolean => true,
  principal: { actor: 'user:operator', kind: 'user' } as {
    actor: string
    kind: string
  } | null,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({
  listRoutinePolicies: vi.fn(),
  routinePosture: vi.fn(),
  getRoutinePolicy: vi.fn(),
  createRoutinePolicy: vi.fn(),
  updateRoutinePolicy: vi.fn(),
  deleteRoutinePolicy: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, governanceApi: api }
})

import { RoutinePoliciesView } from './routine-policies-view'

function wrap(ui: ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>,
  )
}

/** A policy with NO allowlist authored: null means "any cron". */
const anyCron: RoutinePolicyDTO = {
  id: 'pol-any',
  name: 'any-cron-allowed',
  scope_kind: 'tenant',
  enabled: true,
  max_cadence_seconds: 300,
  max_active_routines: 10,
  require_approval: false,
  allowed_cron_patterns: null,
  blocked_environments: null,
  allowed_cron_patterns_unreadable: false,
  blocked_environments_unreadable: false,
}

/** The SAME shape but with an AUTHORED EMPTY allowlist: deny every cron. */
const denyAllCron: RoutinePolicyDTO = {
  id: 'pol-deny',
  name: 'deny-every-cron',
  scope_kind: 'tenant',
  enabled: true,
  max_cadence_seconds: 300,
  max_active_routines: 10,
  require_approval: false,
  allowed_cron_patterns: [],
  blocked_environments: null,
  allowed_cron_patterns_unreadable: false,
  blocked_environments_unreadable: false,
}

/**
 * The FOURTH state: the stored column could not be parsed. It arrives as `[]`
 * exactly like the deny-all above, so only the flag separates them — and the
 * two mean opposite things to enforcement.
 */
const unreadableCron: RoutinePolicyDTO = {
  id: 'pol-unreadable',
  name: 'corrupt-column',
  scope_kind: 'tenant',
  enabled: true,
  max_cadence_seconds: 300,
  max_active_routines: 0,
  require_approval: false,
  allowed_cron_patterns: [],
  blocked_environments: null,
  allowed_cron_patterns_unreadable: true,
  blocked_environments_unreadable: false,
}

const listedCron: RoutinePolicyDTO = {
  id: 'pol-listed',
  name: 'hourly-only',
  scope_kind: 'workspace',
  scope_ref: 'engineering',
  enabled: true,
  max_cadence_seconds: 0,
  max_active_routines: 0,
  require_approval: true,
  allowed_cron_patterns: ['0 * * * *'],
  blocked_environments: ['prod'],
  allowed_cron_patterns_unreadable: false,
  blocked_environments_unreadable: false,
}

function posture(
  policies: RoutinePolicyDTO[],
  overrides: Partial<RoutinePostureDTO['effective']> = {},
): RoutinePostureDTO {
  return {
    total_policies: policies.length,
    enabled_policies: policies.filter((p) => p.enabled).length,
    policies,
    effective: {
      scope_workspace_ref: '',
      scope_user_ref: '',
      scope_user_known: true,
      default_workspace_ref: 'ws-default',
      in_force: policies.length > 0,
      indeterminate: false,
      indeterminate_axis: '',
      min_interval_seconds: 300,
      require_approval: false,
      cron_allowlist_in_force: false,
      cron_allowed: [],
      blocked_environments: [],
      active_caps: [],
      policy_refs: policies.map((p) => p.id),
      digest: 'sha256:deadbeef',
      ...overrides,
    },
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = () => true
  api.listRoutinePolicies.mockResolvedValue({
    items: [],
    has_more: false,
  })
  api.routinePosture.mockResolvedValue(posture([]))
})

describe('routineListState', () => {
  // The pure function under the whole panel. If this collapses, everything
  // rendered on top of it is wrong in the direction that reads as permissive.
  it('keeps null, [], [entries] and unreadable as four distinct states', () => {
    expect(routineListState(null)).toBe('unset')
    expect(routineListState([])).toBe('empty')
    expect(routineListState(['0 * * * *'])).toBe('listed')
    // The engine projects an unreadable column as [], so the flag has to win
    // over the value or the fourth state collapses into the second.
    expect(routineListState([], true)).toBe('unreadable')
    expect(routineListState(null, true)).toBe('unreadable')
  })
})

describe('RoutinePoliciesView tri-state rendering', () => {
  it('renders a null allowlist and an EMPTY allowlist with different text', async () => {
    api.listRoutinePolicies.mockResolvedValue({
      items: [anyCron, denyAllCron],
      has_more: false,
    })
    api.routinePosture.mockResolvedValue(posture([anyCron, denyAllCron]))

    wrap(<RoutinePoliciesView />)

    const anyCell = await screen.findByTestId('routine-cron-pol-any')
    const denyCell = await screen.findByTestId('routine-cron-pol-deny')

    // The whole point: two different states must not read the same.
    expect(anyCell.textContent?.trim()).not.toBe(denyCell.textContent?.trim())
    // And each must say what it actually is, not a shrug.
    expect(anyCell).toHaveAttribute('data-state', 'unset')
    expect(denyCell).toHaveAttribute('data-state', 'empty')
    expect(anyCell.textContent).toMatch(/any/i)
    expect(denyCell.textContent).toMatch(/den/i)
  })

  it('renders an authored list of patterns as the entries themselves', async () => {
    api.listRoutinePolicies.mockResolvedValue({
      items: [listedCron],
      has_more: false,
    })
    api.routinePosture.mockResolvedValue(posture([listedCron]))

    wrap(<RoutinePoliciesView />)

    const cell = await screen.findByTestId('routine-cron-pol-listed')
    expect(cell).toHaveAttribute('data-state', 'listed')
    expect(cell.textContent).toContain('0 * * * *')
  })

  it('separates an unreadable column from an authored deny-all', async () => {
    api.listRoutinePolicies.mockResolvedValue({
      items: [denyAllCron, unreadableCron],
      has_more: false,
    })
    api.routinePosture.mockResolvedValue(posture([denyAllCron, unreadableCron]))

    wrap(<RoutinePoliciesView />)

    const authored = await screen.findByTestId('routine-cron-pol-deny')
    const broken = await screen.findByTestId('routine-cron-pol-unreadable')

    // Both arrive as [] on the wire. If the flag were dropped they would render
    // identically, and an unreadable policy that DENIES CLOSED would read as a
    // deliberate deny-all an operator could reason about.
    expect(authored).toHaveAttribute('data-state', 'empty')
    expect(broken).toHaveAttribute('data-state', 'unreadable')
    expect(broken.textContent?.trim()).not.toBe(authored.textContent?.trim())
    expect(broken).toHaveAccessibleDescription(/could not parse|denies closed/i)
  })
})

describe('RoutinePoliciesView posture header', () => {
  it('shows the composed floor and the policies it came from', async () => {
    api.listRoutinePolicies.mockResolvedValue({
      items: [anyCron, listedCron],
      has_more: false,
    })
    api.routinePosture.mockResolvedValue(
      posture([anyCron, listedCron], {
        min_interval_seconds: 900,
        require_approval: true,
        policy_refs: ['pol-any', 'pol-listed'],
      }),
    )

    wrap(<RoutinePoliciesView />)

    const floor = await screen.findByTestId('posture-floor')
    expect(floor.textContent).toMatch(/900/)
    // Drill-down: the matched policies are named, not just counted.
    const header = screen.getByTestId('routine-posture')
    const refs = within(header).getAllByTestId('posture-policy-ref')
    expect(refs).toHaveLength(2)
    expect(refs.map((r) => r.textContent).join(' ')).toContain(
      'any-cron-allowed',
    )
  })

  it('separates "no allowlist" from a composed deny-all', async () => {
    api.routinePosture.mockResolvedValue(
      posture([denyAllCron], {
        cron_allowlist_in_force: true,
        cron_allowed: [],
      }),
    )
    api.listRoutinePolicies.mockResolvedValue({
      items: [denyAllCron],
      has_more: false,
    })

    const denyAll = wrap(<RoutinePoliciesView />)
    const denyCell = await screen.findByTestId('posture-cron')
    expect(denyCell).toHaveAttribute('data-state', 'empty')
    expect(denyCell.textContent).toMatch(/den/i)
    const denyText = denyCell.textContent
    denyAll.unmount()

    // The other branch of the same field: with no allowlist in force,
    // cron_allowed is ALSO [] — only the boolean separates them. Asserting one
    // side alone would pass with the branch inverted.
    api.routinePosture.mockResolvedValue(
      posture([anyCron], { cron_allowlist_in_force: false, cron_allowed: [] }),
    )
    api.listRoutinePolicies.mockResolvedValue({
      items: [anyCron],
      has_more: false,
    })
    wrap(<RoutinePoliciesView />)
    const anyCell = await screen.findByTestId('posture-cron')
    expect(anyCell).toHaveAttribute('data-state', 'unset')
    expect(anyCell.textContent).not.toBe(denyText)
  })

  it('reports a failed posture read instead of rendering it as zero policies', async () => {
    api.listRoutinePolicies.mockResolvedValue({
      items: [anyCron],
      has_more: false,
    })
    api.routinePosture.mockRejectedValue(new ApiError(503, 'unavailable', 'nope'))

    wrap(<RoutinePoliciesView />)

    // With rows in the table below, "0 enabled of 0" would be a claim that this
    // tenant has no governance — the opposite of "we could not ask".
    expect(await screen.findByTestId('posture-error')).toBeInTheDocument()
    expect(screen.queryByText(/0 enabled of 0/i)).not.toBeInTheDocument()
    expect(screen.getByTestId('posture-retry')).toBeInTheDocument()
  })

  it('echoes the scope the answer was resolved for, and flags a drifted draft', async () => {
    api.routinePosture.mockResolvedValue(
      posture([anyCron], { scope_user_ref: 'user:alice', scope_user_known: true }),
    )
    api.listRoutinePolicies.mockResolvedValue({
      items: [anyCron],
      has_more: false,
    })

    wrap(<RoutinePoliciesView />)

    const echo = await screen.findByTestId('posture-applied-scope')
    expect(echo.textContent).toContain('user:alice')
    expect(echo).toHaveAttribute('data-stale', 'false')

    // Typing a new ref without resolving must mark the shown answer stale: it
    // still describes the OLD scope.
    const user = userEvent.setup()
    await user.type(screen.getByTestId('posture-scope-user'), 'user:bob')
    await waitFor(() =>
      expect(screen.getByTestId('posture-applied-scope')).toHaveAttribute(
        'data-stale',
        'true',
      ),
    )
  })

  it('renders an indeterminate resolution as a refusal, not as "no controls"', async () => {
    api.routinePosture.mockResolvedValue(
      posture([anyCron], {
        indeterminate: true,
        indeterminate_axis: 'workspace',
        in_force: false,
      }),
    )
    api.listRoutinePolicies.mockResolvedValue({
      items: [anyCron],
      has_more: false,
    })

    wrap(<RoutinePoliciesView />)

    const banner = await screen.findByTestId('posture-indeterminate')
    expect(banner).toBeInTheDocument()
    expect(banner.textContent).toMatch(/workspace/)
    // A deny-closed resolution is an alert, not a quiet caption.
    expect(banner).toHaveAttribute('role', 'alert')

    // THE OTHER HALF, and it is the half this test was named for. The banner
    // above was the only thing asserted, while the origin line a few elements
    // down — in the SAME <section> — fell through to "No enabled policy governs
    // this scope." because in_force is false for a refusal too. The screen
    // therefore said "this is a refusal, not an absence of controls" and "no
    // policy governs this scope" at once, and an operator who read the second
    // one concluded the routine was ungoverned. Asserting the ABSENCE is the
    // point: the banner assertion alone stayed green through that.
    const section = screen.getByTestId('routine-posture')
    expect(section.textContent).not.toMatch(
      /No enabled policy governs this scope/i,
    )
    expect(section.textContent).toMatch(/the resolution was REFUSED/i)
  })

  // The mirror case, so the fix cannot be "always say refused": a genuinely
  // ungoverned scope must STILL say that no policy governs it. Without this, a
  // component that printed the refusal copy unconditionally would pass above.
  it('still reports a genuinely ungoverned scope as having no policy', async () => {
    api.routinePosture.mockResolvedValue(
      posture([anyCron], {
        indeterminate: false,
        indeterminate_axis: '',
        in_force: false,
      }),
    )
    api.listRoutinePolicies.mockResolvedValue({
      items: [anyCron],
      has_more: false,
    })

    wrap(<RoutinePoliciesView />)

    // Wait for the POSTURE, not just the section: the section renders straight
    // away with a "Loading…" body, and asserting on it then would pass the
    // absence checks below for the wrong reason.
    expect(
      await screen.findByText(/No enabled policy governs this scope/i),
    ).toBeInTheDocument()
    const section = screen.getByTestId('routine-posture')
    expect(section.textContent).not.toMatch(/the resolution was REFUSED/i)
    expect(screen.queryByTestId('posture-indeterminate')).not.toBeInTheDocument()
  })

  it('re-resolves the posture for the scope the operator names', async () => {
    wrap(<RoutinePoliciesView />)
    await screen.findByTestId('routine-posture')

    const user = userEvent.setup()
    const userInput = screen.getByTestId('posture-scope-user')
    await user.type(userInput, 'user:alice')
    await user.click(screen.getByTestId('posture-scope-apply'))

    await waitFor(() => {
      expect(api.routinePosture).toHaveBeenCalledWith(
        expect.objectContaining({ user_ref: 'user:alice' }),
      )
    })
  })
})

describe('RoutinePoliciesView permissions', () => {
  it('shows the no-permission state, not a blank screen, without routine:read', async () => {
    authState.can = (permission: string) =>
      permission !== 'governance:routine:read'

    wrap(<RoutinePoliciesView />)

    expect(await screen.findByRole('status')).toBeInTheDocument()
    // And it must not have asked the engine for data it cannot read.
    expect(api.listRoutinePolicies).not.toHaveBeenCalled()
    expect(api.routinePosture).not.toHaveBeenCalled()
  })

  it('surfaces a 403 from the engine as the same calm state, never a blank screen', async () => {
    api.listRoutinePolicies.mockRejectedValue(
      new ApiError(403, 'forbidden', 'forbidden'),
    )
    api.routinePosture.mockRejectedValue(
      new ApiError(403, 'forbidden', 'forbidden'),
    )

    wrap(<RoutinePoliciesView />)

    expect(await screen.findByTestId('routine-forbidden')).toBeInTheDocument()
  })

  it('hides authoring controls for a read-only operator', async () => {
    authState.can = (permission: string) =>
      permission !== 'governance:routine:admin'
    api.listRoutinePolicies.mockResolvedValue({
      items: [anyCron],
      has_more: false,
    })

    wrap(<RoutinePoliciesView />)

    await screen.findByTestId('routine-policy-row-pol-any')
    expect(screen.queryByTestId('routine-policy-new')).not.toBeInTheDocument()
  })
})

describe('RoutinePolicyEditorDialog write intents', () => {
  // These exist because the first version of this panel had NO editor test, and
  // that gap is exactly what let a fail-open through: the dialog re-sent every
  // list on every save, which turned an unreadable column into a real empty one
  // and dropped the deny-closed it was producing.
  it('does not write a list the engine reported as unreadable', async () => {
    api.listRoutinePolicies.mockResolvedValue({
      items: [unreadableCron],
      has_more: false,
    })
    api.routinePosture.mockResolvedValue(posture([unreadableCron]))
    api.updateRoutinePolicy.mockResolvedValue(unreadableCron)

    wrap(<RoutinePoliciesView />)
    const user = userEvent.setup()

    await user.click(await screen.findByTestId('routine-policy-row-pol-unreadable'))
    // Change something unrelated — the cadence toggle — and save.
    await user.click(await screen.findByTestId('routine-editor-approval'))
    await user.click(screen.getByTestId('routine-editor-save'))

    await waitFor(() => expect(api.updateRoutinePolicy).toHaveBeenCalled())
    const [id, body] = api.updateRoutinePolicy.mock.calls[0]
    expect(id).toBe('pol-unreadable')
    // The PUT decoder reads an ABSENT key as "leave it alone". Sending [] here
    // would repair the column and turn a refusal into "no constraint".
    expect(body).not.toHaveProperty('allowed_cron_patterns')
    // The readable one is still written explicitly, so it cannot be cleared by
    // omission either.
    expect(body).toHaveProperty('blocked_environments')
  })

  it('refuses "only these patterns" with nothing listed, instead of writing a deny-all', async () => {
    api.listRoutinePolicies.mockResolvedValue({ items: [], has_more: false })

    wrap(<RoutinePoliciesView />)
    const user = userEvent.setup()

    await user.click(await screen.findByTestId('routine-policy-new'))
    await screen.findByTestId('routine-editor-name')
    await user.type(screen.getByTestId('routine-editor-name'), 'p1')

    // Pick the "only these patterns" mode and leave the box empty. parseEntries
    // would yield [] — an authored deny-all the operator never chose.
    await user.click(screen.getByTestId('routine-editor-cron-mode'))
    await user.click(await screen.findByRole('option', { name: /only these/i }))

    expect(screen.getByTestId('routine-editor-save')).toBeDisabled()
    await user.type(screen.getByTestId('routine-editor-cron-entries'), '0 * * * *')
    expect(screen.getByTestId('routine-editor-save')).toBeEnabled()
  })

  it('refuses an emptied active cap, which Number() would read as "no cap"', async () => {
    api.listRoutinePolicies.mockResolvedValue({ items: [], has_more: false })

    wrap(<RoutinePoliciesView />)
    const user = userEvent.setup()

    await user.click(await screen.findByTestId('routine-policy-new'))
    await screen.findByTestId('routine-editor-name')
    await user.type(screen.getByTestId('routine-editor-name'), 'p1')
    expect(screen.getByTestId('routine-editor-save')).toBeEnabled()

    await user.clear(screen.getByTestId('routine-editor-max-active'))
    // Number('') === 0, and 0 is the legal value meaning "no cap" — so an empty
    // box would silently REMOVE a control rather than fail.
    expect(screen.getByTestId('routine-editor-save')).toBeDisabled()
  })
})

describe('RoutinePoliciesView round-2 contrast fixes', () => {
  it('marks the composed values as superseded when the resolution is indeterminate', async () => {
    api.listRoutinePolicies.mockResolvedValue({
      items: [anyCron],
      has_more: false,
    })
    api.routinePosture.mockResolvedValue(
      posture([anyCron], {
        indeterminate: true,
        indeterminate_axis: 'allowed_cron_patterns',
        cron_allowlist_in_force: false,
      }),
    )

    wrap(<RoutinePoliciesView />)

    // The banner already says "refused". Rendering "any cron" underneath it
    // unqualified contradicts it: enforcement denies closed regardless of what
    // the partial fold produced.
    await screen.findByTestId('posture-indeterminate')
    expect(screen.getByTestId('posture-superseded')).toBeInTheDocument()
    expect(screen.getByTestId('posture-values')).toHaveAttribute(
      'data-superseded',
      'true',
    )
  })

  it('cannot ask for a named user and an unanswerable user axis at once', async () => {
    wrap(<RoutinePoliciesView />)
    const user = userEvent.setup()
    await screen.findByTestId('routine-posture')

    await user.type(screen.getByTestId('posture-scope-user'), 'user:alice')
    await user.click(screen.getByTestId('posture-scope-user-unknown'))
    // The engine 400s on that combination, so the UI must not be able to build
    // it: an unanswerable axis has no owner.
    expect(screen.getByTestId('posture-scope-user')).toBeDisabled()

    await user.click(screen.getByTestId('posture-scope-apply'))
    await waitFor(() => {
      expect(api.routinePosture).toHaveBeenCalledWith(
        expect.objectContaining({ user_known: 'false' }),
      )
    })
    expect(api.routinePosture).not.toHaveBeenCalledWith(
      expect.objectContaining({ user_ref: 'user:alice' }),
    )
  })
})
