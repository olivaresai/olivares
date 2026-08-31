// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// the honesty invariants of the retention screen.
//
// Everything this screen says is a claim about DESTRUCTION, so every cell below pins
// a sentence that would be a lie if the code changed underneath it. They are written
// to be killed by a mutation: flip the 202 branch, drop the null guard, widen the
// sweep scope, offer purge on a class that cannot be purged — and exactly one fails.
//
// The last describe block starts in the PARENT view and clicks the tab, because the
// measured failure mode here is not a broken component: it is a component nobody
// renders. Deleting one tab's wiring left 1644 tests green two days ago.
import { describe, expect, it, vi } from 'vitest'
import {
  DEFAULT_AUTH,
  fireEvent,
  renderIntel,
  screen,
  userEvent,
  waitFor,
  within,
} from '@/test/intel'
import '@/features/_intel'
import { ApiError } from '@/lib/api/errors'
import type { DataClassEntry, RetentionPolicy, RetentionSummary } from './types'

const api = {
  dataClasses: vi.fn(),
  retentionPolicies: vi.fn(),
  retentionRuns: vi.fn(),
  putRetentionPolicy: vi.fn(),
  deleteRetentionPolicy: vi.fn(),
  runRetentionSweep: vi.fn(),
  // Consumed by the parent container in the last block.
  summary: vi.fn(),
  frameworks: vi.fn(),
  holds: vi.fn(),
  erasures: vi.fn(),
  calendar: vi.fn(),
  risk: vi.fn(),
  residency: vi.fn(),
  evidence: vi.fn(),
}

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ ...DEFAULT_AUTH }),
}))

/** The toaster is not mounted here, so a toast never reaches the DOM. What matters
 *  is WHICH tone was used: a pending approval announced as a success is the defect. */
const toastSpy = {
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
  info: vi.fn(),
}
vi.mock('@/components/ui/toaster', () => ({ toast: toastSpy }))

vi.mock('./api', async () => {
  const actual = await vi.importActual<typeof import('./api')>('./api')
  return { ...actual, complianceApi: api }
})

const { RetentionTab } = await import('./retention-view')
const { ComplianceView } = await import('./compliance-view')
await import('./i18n')

// --- fixtures mirroring the engine's own DTOs ---------------------------------

const CLASS_MEMORY: DataClassEntry = {
  id: 'agent.memory',
  purgeable: true,
  model_io: true,
  note: 'Governed agent memory.',
}
/** Not purgeable in the engine registry (dataclass.go:134,140): the console must not
 *  offer a disposition validateRetentionPolicy rejects with a 400. */
const CLASS_LEDGER: DataClassEntry = {
  id: 'audit.ledger',
  purgeable: false,
  model_io: false,
  recommended_days: 2555,
}

const POLICY_ARMED: RetentionPolicy = {
  id: 'p-1',
  data_class: 'agent.memory',
  retention_days: 30,
  disposition: 'purge',
  enabled: true,
  model_io: true,
  provider_floor_known: false,
  approval_ref: 'ap-1',
}
const POLICY_DRAFT: RetentionPolicy = {
  id: 'p-2',
  data_class: 'audit.ledger',
  retention_days: 2555,
  disposition: 'retain',
  enabled: true,
  model_io: false,
  provider_floor_known: false,
}

function seed(opts?: {
  classes?: DataClassEntry[]
  policies?: RetentionPolicy[]
}) {
  // Call history too, not just the toasts: a cell asserting a write NEVER happened
  // reads a previous cell's call otherwise, and passes or fails by test ORDER.
  for (const fn of Object.values(api)) fn.mockClear()
  toastSpy.success.mockClear()
  toastSpy.warning.mockClear()
  toastSpy.error.mockClear()
  api.dataClasses.mockResolvedValue({
    items: opts?.classes ?? [CLASS_MEMORY, CLASS_LEDGER],
  })
  api.retentionPolicies.mockResolvedValue({
    items: opts?.policies ?? [POLICY_ARMED, POLICY_DRAFT],
  })
  api.retentionRuns.mockResolvedValue({ items: [] })
}

/** Type ConfirmDialog's guard phrase. Anchored on #confirm-phrase
 *  (confirm-dialog.tsx) because these dialogs render other inputs by design. */
async function typeConfirmPhrase(
  user: ReturnType<typeof userEvent.setup>,
  phrase: string,
) {
  const input = await waitFor(() => {
    const el = document.getElementById('confirm-phrase')
    if (!(el instanceof HTMLInputElement)) {
      throw new Error('confirm-phrase guard input not rendered')
    }
    return el
  })
  await user.type(input, phrase)
}

// --- the 202 -------------------------------------------------------------------

describe('a pending approval is NOT a saved purge', () => {
  it('renders "the purge is NOT in force" and never toasts success', async () => {
    seed({ classes: [CLASS_MEMORY], policies: [] })
    api.putRetentionPolicy.mockResolvedValue({
      status: 202,
      data: { status: 'pending_approval', approval_ref: 'ap-pending' },
      headers: new Headers(),
    })
    const user = userEvent.setup()
    renderIntel(<RetentionTab canAdmin canRead />)

    await user.click(
      await screen.findByRole('button', { name: /Add schedule/i }),
    )
    // Arm a purge: disposition → Purge, then confirm.
    await user.click(await screen.findByRole('combobox'))
    await user.click(await screen.findByRole('option', { name: 'Purge' }))
    await user.click(screen.getByRole('button', { name: /^Review$/i }))
    await typeConfirmPhrase(user, 'PURGE')
    await user.click(screen.getByRole('button', { name: /Request approval/i }))

    // THE SENTENCE. Not "saved": saved with the purge switched OFF.
    expect(
      await screen.findByText(/the purge is NOT in force/i),
    ).toBeInTheDocument()
    // HashChip renders the ref twice (the chip and its copyable full value), so this
    // asks whether the operator can SEE the reference, not how many nodes carry it.
    expect(screen.getAllByText(/ap-pending/).length).toBeGreaterThan(0)
    // And the success toast never fires — the whole failure mode of a 202 is that it
    // is shaped like a success.
    expect(toastSpy.success).not.toHaveBeenCalled()
  })

  it('goes back to the form when the review step is cancelled', async () => {
    seed({ classes: [CLASS_MEMORY], policies: [] })
    const user = userEvent.setup()
    renderIntel(<RetentionTab canAdmin canRead />)

    await user.click(
      await screen.findByRole('button', { name: /Add schedule/i }),
    )
    await user.click(await screen.findByRole('combobox'))
    await user.click(await screen.findByRole('option', { name: 'Purge' }))
    // fireEvent, not user.type: jsdom refuses setSelectionRange on input[type=number],
    // so `{selectall}` silently no-ops and the digits APPEND — 365 + "90" = 36590, past
    // the engine's 36500 bound, which disables the submit and makes this cell fail for
    // a reason that has nothing to do with what it is testing.
    fireEvent.change(screen.getByRole('spinbutton'), {
      target: { value: '90' },
    })
    await user.click(screen.getByRole('button', { name: /^Review$/i }))

    // Reading what you are about to arm and deciding to shorten the window must not
    // cost you the window you already typed — that is how an operator learns to click
    // through the guard instead of reading it.
    //
    // Scoped to the CONFIRM dialog: while Radix swaps the two, the outgoing form is
    // still in the DOM, and its Cancel closes the whole thing. A bare byRole('Cancel')
    // here picked the wrong one and reported the fix as broken.
    const confirm = (await waitFor(() => {
      const el = document
        .getElementById('confirm-phrase')
        ?.closest('[role=dialog]')
      if (!(el instanceof HTMLElement))
        throw new Error('confirm dialog not rendered')
      return el
    })) as HTMLElement
    await user.click(within(confirm).getByRole('button', { name: /Cancel/i }))
    expect(await screen.findByRole('spinbutton')).toHaveValue(90)
    expect(api.putRetentionPolicy).not.toHaveBeenCalled()
  })

  it('sends EXACTLY the four keys the engine decodes', async () => {
    seed({ classes: [CLASS_MEMORY], policies: [] })
    api.putRetentionPolicy.mockResolvedValue({
      status: 200,
      data: { ...POLICY_ARMED, disposition: 'retain', enabled: true },
      headers: new Headers(),
    })
    const user = userEvent.setup()
    renderIntel(<RetentionTab canAdmin canRead />)

    await user.click(
      await screen.findByRole('button', { name: /Add schedule/i }),
    )
    await user.click(screen.getByRole('button', { name: /Save schedule/i }))

    await waitFor(() => expect(api.putRetentionPolicy).toHaveBeenCalled())
    const [dataClass, body] = api.putRetentionPolicy.mock.calls[0]
    expect(dataClass).toBe('agent.memory')
    // With a basis typed it is FOUR keys; blank, the console omits it and sends three.
    // Both shapes go through the real router in s696_test.go.
    // The engine decodes with DisallowUnknownFields (helpers.go:99): one extra key is
    // a flat 400 on a request that is otherwise perfect. s696_test.go asserts the same
    // contract from the engine's side, against these exact bytes.
    expect(Object.keys(body).sort()).toEqual([
      'disposition',
      'enabled',
      'retention_days',
    ])
  })

  it('adds `basis` and nothing else when the operator documents one', async () => {
    seed({ classes: [CLASS_MEMORY], policies: [] })
    api.putRetentionPolicy.mockResolvedValue({
      status: 200,
      data: { ...POLICY_ARMED, disposition: 'retain' },
      headers: new Headers(),
    })
    const user = userEvent.setup()
    renderIntel(<RetentionTab canAdmin canRead />)

    await user.click(
      await screen.findByRole('button', { name: /Add schedule/i }),
    )
    await user.type(screen.getByRole('textbox'), 'SOX 7y schedule')
    await user.click(screen.getByRole('button', { name: /Save schedule/i }))

    await waitFor(() => expect(api.putRetentionPolicy).toHaveBeenCalled())
    const [, body] = api.putRetentionPolicy.mock.calls[0]
    expect(Object.keys(body).sort()).toEqual([
      'basis',
      'disposition',
      'enabled',
      'retention_days',
    ])
    expect(body.basis).toBe('SOX 7y schedule')
  })

  it('refetches after a REFUSAL too, because the engine persisted before refusing', async () => {
    seed({ classes: [CLASS_MEMORY], policies: [POLICY_ARMED] })
    // 503 = no approval gate wired. The engine already forced enabled=false and
    // PERSISTED the schedule (retention.go:301-312) before answering — so the cached
    // row still saying "deleted on every sweep" is now a statement about a control the
    // engine has switched off.
    api.putRetentionPolicy.mockRejectedValue(
      new ApiError(503, 'unavailable', 'no approval gate is wired'),
    )
    const user = userEvent.setup()
    renderIntel(<RetentionTab canAdmin canRead />)

    await user.click(
      await screen.findByRole('button', { name: /Edit schedule/i }),
    )
    await user.click(screen.getByRole('button', { name: /^Review$/i }))
    await typeConfirmPhrase(user, 'PURGE')
    await user.click(screen.getByRole('button', { name: /Request approval/i }))

    await waitFor(() => expect(toastSpy.warning).toHaveBeenCalled())
    // The refetch is the assertion: one read on mount, a second after the refusal.
    await waitFor(() =>
      expect(api.retentionPolicies.mock.calls.length).toBeGreaterThan(1),
    )
  })
})

// --- the unscheduled class ------------------------------------------------------

describe('a class with no schedule states so', () => {
  it('says nothing is disposed of, rather than rendering a blank row', async () => {
    seed({ classes: [CLASS_MEMORY, CLASS_LEDGER], policies: [] })
    renderIntel(<RetentionTab canAdmin canRead />)

    const rows = await screen.findAllByText(
      /nothing here is disposed of on a timetable/i,
    )
    expect(rows).toHaveLength(2)
    // The advisory number is offered where the registry has one, and is never
    // presented as a schedule that exists.
    expect(
      screen.getByText(/Advisory guidance: 2555 days/i),
    ).toBeInTheDocument()
  })

  it('does not offer Purge for a class the engine refuses to purge', async () => {
    seed({ classes: [CLASS_LEDGER], policies: [] })
    const user = userEvent.setup()
    renderIntel(<RetentionTab canAdmin canRead />)

    await user.click(
      await screen.findByRole('button', { name: /Add schedule/i }),
    )
    await user.click(await screen.findByRole('combobox'))
    expect(
      await screen.findByRole('option', { name: 'Retain' }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('option', { name: 'Purge' }),
    ).not.toBeInTheDocument()
  })
})

// --- the sweep ------------------------------------------------------------------

describe('the sweep confirmation carries its own scope', () => {
  it('names only the schedules that actually authorise destruction', async () => {
    seed()
    const user = userEvent.setup()
    renderIntel(<RetentionTab canAdmin canRead />)

    await user.click(await screen.findByRole('button', { name: /Run sweep/i }))

    // Scoped to the DIALOG on purpose: the same sentence appears on the row behind
    // it, and a query that matched either would pass with an empty confirmation.
    const dialog = await screen.findByRole('dialog')
    expect(
      await within(dialog).findByText(/rows older than 30 days/i),
    ).toBeInTheDocument()
    expect(within(dialog).getByText('agent.memory')).toBeInTheDocument()
    // The retain schedule destroys nothing, so promising it here would overstate
    // what the sweep does. `armed` is the engine's own worklist predicate.
    expect(dialog.textContent).not.toMatch(/2555/)
    expect(dialog.textContent).not.toMatch(/audit\.ledger/)
  })

  it('REFUSES to confirm when the schedules could not be read', async () => {
    // ⛔ THE MOST EXPENSIVE DEFECT IN THIS REPOSITORY, reached in the destructive verb:
    // `policies` falls back to [] on a failed read, so "nothing is armed" and "I could
    // not look" produce the same empty array — and the confirmation would have promised
    // "this sweep will delete nothing" while the engine, which rebuilds its worklist
    // server-side (retention.go:535-565), deletes on the very same click.
    seed()
    api.retentionPolicies.mockRejectedValue(
      new ApiError(500, 'internal', 'the store is unreachable'),
    )
    const user = userEvent.setup()
    renderIntel(<RetentionTab canAdmin canRead />)

    await user.click(await screen.findByRole('button', { name: /Run sweep/i }))
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText(
        /no way to tell you what this sweep would delete/i,
      ),
    ).toBeInTheDocument()
    // NOT the reassuring sentence, and NOT a confirm button of any kind.
    expect(dialog.textContent).not.toMatch(/will delete nothing/i)
    expect(
      within(dialog).queryByRole('button', { name: /^Run sweep$/i }),
    ).not.toBeInTheDocument()
    expect(document.getElementById('confirm-phrase')).toBeNull()
  })

  it('REFUSES while a refetch is in flight, because the scope on screen is the old one', async () => {
    // The narrower half of the same rule, and it survived a mutant until this cell
    // existed: `isSuccess` stays true during a refetch and TanStack keeps serving the
    // PREVIOUS data (measured), so a scope check that only asks isSuccess confirms a
    // sweep against schedules that have just changed.
    //
    // The sequence is the ordinary one, not a contrivance: remove a schedule, then
    // reach for Run sweep before the re-read lands.
    seed({ classes: [CLASS_MEMORY], policies: [POLICY_ARMED] })
    api.deleteRetentionPolicy.mockResolvedValue(undefined)
    api.retentionPolicies
      .mockResolvedValueOnce({ items: [POLICY_ARMED] })
      // The re-read the delete triggers never lands.
      .mockReturnValue(new Promise(() => {}))
    const user = userEvent.setup()
    renderIntel(<RetentionTab canAdmin canRead />)

    await user.click(
      await screen.findByRole('button', { name: /Remove schedule/i }),
    )
    const removeDialog = await screen.findByRole('dialog')
    await user.click(
      within(removeDialog).getByRole('button', { name: /Remove schedule/i }),
    )
    await waitFor(() => expect(api.deleteRetentionPolicy).toHaveBeenCalled())

    await user.click(await screen.findByRole('button', { name: /Run sweep/i }))
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText(
        /no way to tell you what this sweep would delete/i,
      ),
    ).toBeInTheDocument()
    expect(document.getElementById('confirm-phrase')).toBeNull()
  })

  it('says plainly that a sweep with nothing armed deletes nothing', async () => {
    seed({ policies: [POLICY_DRAFT] })
    const user = userEvent.setup()
    renderIntel(<RetentionTab canAdmin canRead />)

    await user.click(await screen.findByRole('button', { name: /Run sweep/i }))
    expect(
      await screen.findByText(/this sweep will delete nothing/i),
    ).toBeInTheDocument()
  })

  it('survives `classes: null`, the shape an un-armed tenant gets back', async () => {
    seed({ policies: [POLICY_DRAFT] })
    // retention.go:500 — a Go nil slice with no omitempty marshals to null, and the
    // shared list-envelope guard only covers `items` (client.ts:216).
    const summary = {
      trigger: 'manual',
      classes: null,
      examined: 0,
      purged: 0,
      excluded_held: 0,
      skipped_class_holds: 0,
      truncated: false,
    } as unknown as RetentionSummary
    api.runRetentionSweep.mockResolvedValue(summary)
    const user = userEvent.setup()
    renderIntel(<RetentionTab canAdmin canRead />)

    await user.click(await screen.findByRole('button', { name: /Run sweep/i }))
    await typeConfirmPhrase(user, 'PURGE')
    await user.click(screen.getByRole('button', { name: /^Run sweep$/i }))

    expect(
      await screen.findByText(
        /No schedule was in force, so nothing was examined/i,
      ),
    ).toBeInTheDocument()
  })

  it('a FAILED sweep is not a sweep that did nothing', async () => {
    // Each batch is its own committed transaction (retention.go:585-646); runRetention
    // returns on the first batch error (`:647-648`) and the handler throws the partial
    // summary away (`:486-492`). So rows deleted before it stopped are gone, and a bare
    // "request failed" toast reads as "nothing happened" about a destructive verb.
    seed()
    api.runRetentionSweep.mockRejectedValue(
      new ApiError(500, 'internal', 'the store went away mid-sweep'),
    )
    const user = userEvent.setup()
    renderIntel(<RetentionTab canAdmin canRead />)

    await screen.findByText('agent.memory')
    const runsBefore = api.retentionRuns.mock.calls.length
    await user.click(await screen.findByRole('button', { name: /Run sweep/i }))
    await typeConfirmPhrase(user, 'PURGE')
    await user.click(screen.getByRole('button', { name: /^Run sweep$/i }))

    await waitFor(() => expect(toastSpy.error).toHaveBeenCalled())
    expect(String(toastSpy.error.mock.calls[0][0])).toMatch(/already gone/i)
    // And the certificates are re-read: what WAS sealed before it stopped is the only
    // record of what this click destroyed.
    await waitFor(() =>
      expect(api.retentionRuns.mock.calls.length).toBeGreaterThan(runsBefore),
    )
  })

  it('reports a truncated sweep as unfinished, not as complete', async () => {
    seed()
    api.runRetentionSweep.mockResolvedValue({
      trigger: 'manual',
      classes: [
        {
          data_class: 'agent.memory',
          cutoff: '2026-07-12T00:00:00Z',
          examined: 10000,
          purged: 10000,
          excluded_held: 0,
          skipped_class_hold: false,
          truncated: true,
        },
      ],
      examined: 10000,
      purged: 10000,
      excluded_held: 0,
      skipped_class_holds: 0,
      truncated: true,
    } satisfies RetentionSummary)
    const user = userEvent.setup()
    renderIntel(<RetentionTab canAdmin canRead />)

    await user.click(await screen.findByRole('button', { name: /Run sweep/i }))
    await typeConfirmPhrase(user, 'PURGE')
    await user.click(screen.getByRole('button', { name: /^Run sweep$/i }))

    expect(await screen.findByText(/did NOT finish/i)).toBeInTheDocument()
    expect(
      screen.queryByText(/completed across every schedule in force/i),
    ).not.toBeInTheDocument()
    // A sweep that deleted 10000 rows and stopped short is still not a clean pass, so
    // it never gets the success toast either.
    expect(toastSpy.success).not.toHaveBeenCalled()
  })
})

// --- the window the sweep actually cuts at --------------------------------------

describe('a regulatory floor holds the sweep back, and the screen says so', () => {
  /** SEC 17a-4-shaped floor: six years, longer than the stored 30-day schedule. The
   *  engine's author-time refusal (retention.go:262-267) only guards NEW writes, so
   *  this state is reached whenever the floor is raised — or the add-on linked — after
   *  the schedule was written, and the sweep then clamps the cutoff UP (`:579-581`). */
  const CLAMPED: RetentionPolicy = {
    ...POLICY_ARMED,
    retention_days: 30,
    regulatory_floor: {
      class: 'agent.memory',
      min_days: 2190,
      basis: 'SEC 17a-4(a)',
      mode: 'compliance',
    },
  }

  it('reports the FLOOR as the deletion window, not the schedule', async () => {
    seed({ classes: [CLASS_MEMORY], policies: [CLAMPED] })
    renderIntel(<RetentionTab canAdmin canRead />)

    // "deleted after 30 days" would report as destroyed data the floor is still
    // preserving — the direction of this error that costs a DPO a discovery answer.
    const row = await screen.findByText(
      /Rows older than 2190 days are deleted/i,
    )
    expect(row).toBeInTheDocument()
    expect(row.textContent).toMatch(/SEC 17a-4/)
    expect(
      screen.queryByText(
        /^Rows older than 30 days are deleted on every sweep\.$/,
      ),
    ).not.toBeInTheDocument()
  })

  it('carries the same number into the sweep confirmation', async () => {
    seed({ classes: [CLASS_MEMORY], policies: [CLAMPED] })
    const user = userEvent.setup()
    renderIntel(<RetentionTab canAdmin canRead />)

    await user.click(await screen.findByRole('button', { name: /Run sweep/i }))
    const dialog = await screen.findByRole('dialog')
    expect(
      await within(dialog).findByText(/rows older than 2190 days/i),
    ).toBeInTheDocument()
  })

  it('leaves an unclamped schedule reading exactly as it is written', async () => {
    // Direction of non-firing: a helper that always returned the floor, or that always
    // rendered the clamped wording, would pass both cells above. In the open-core build
    // there is no governor and no floor, which is the ordinary case.
    seed({ classes: [CLASS_MEMORY], policies: [POLICY_ARMED] })
    renderIntel(<RetentionTab canAdmin canRead />)

    expect(
      await screen.findByText(
        /Rows older than 30 days are deleted on every sweep\./,
      ),
    ).toBeInTheDocument()
    expect(
      screen.queryByText(/floor holds the sweep back/i),
    ).not.toBeInTheDocument()
  })
})

// --- the disclosure window ------------------------------------------------------

describe('an unknown provider floor is not zero days', () => {
  it('refuses to state a "gone everywhere after" window it cannot support', async () => {
    seed({
      classes: [CLASS_MEMORY],
      // model_io class, provider floor un-wired ⇒ the engine omits
      // effective_disclosure_days entirely (retention.go:149).
      policies: [{ ...POLICY_ARMED, effective_disclosure_days: undefined }],
    })
    renderIntel(<RetentionTab canAdmin canRead />)

    expect(
      await screen.findByText(/provider retention floor is unknown/i),
    ).toBeInTheDocument()
    expect(
      screen.queryByText(/Gone everywhere after 0 days/i),
    ).not.toBeInTheDocument()
  })

  it('states the window when the engine computed one', async () => {
    seed({
      classes: [CLASS_MEMORY],
      policies: [{ ...POLICY_ARMED, effective_disclosure_days: 90 }],
    })
    renderIntel(<RetentionTab canAdmin canRead />)

    expect(
      await screen.findByText(/Gone everywhere after 90 days/i),
    ).toBeInTheDocument()
  })
})

// --- permissions ----------------------------------------------------------------

describe('the verb is gated apart from the read', () => {
  it('renders the schedules but no write affordance for a reader', async () => {
    seed()
    renderIntel(<RetentionTab canAdmin={false} canRead />)

    expect(await screen.findByText('agent.memory')).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /Run sweep/i }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /Edit schedule/i }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /Remove schedule/i }),
    ).not.toBeInTheDocument()
  })
})

// --- the wiring -----------------------------------------------------------------

describe('the tab is reachable from the compliance view', () => {
  it('renders the retention plane after a click on its trigger', async () => {
    seed()
    api.summary.mockResolvedValue({
      frameworks: [],
      disclaimer: 'Not a certification.',
    })
    const user = userEvent.setup()
    renderIntel(<ComplianceView />)

    // Starts in the PARENT and presses: a tab whose content is never mounted is
    // exactly what a component-level suite cannot see.
    await user.click(await screen.findByRole('tab', { name: 'Retention' }))
    expect(await screen.findByText('Retention schedules')).toBeInTheDocument()
    expect(await screen.findByText('agent.memory')).toBeInTheDocument()
  })
})
