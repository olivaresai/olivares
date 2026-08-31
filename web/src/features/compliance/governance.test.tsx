// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// the honesty invariants of the governed data-lifecycle screens.
//
// Every test here pins a statement the console makes about DESTRUCTION. They are
// written so a mutation kills them: flip a polarity, drop the covering holds,
// close the dialog on a 202, and one fails. A console that gets these wrong does
// not merely look wrong — it tells a DPO that data is gone when it is not, or
// that nothing is preserved when something is.
import { describe, expect, it, vi } from 'vitest'
import {
  DEFAULT_AUTH,
  renderIntel,
  screen,
  userEvent,
  waitFor,
} from '@/test/intel'
import '@/features/_intel'
import { ApiError } from '@/lib/api/errors'
import { ERASURE_STATUSES, ERASURE_STATUS_CLEAN } from './types'

const holdsApi = {
  holds: vi.fn(),
  hold: vi.fn(),
  holdEvents: vi.fn(),
  checkHold: vi.fn(),
  createHold: vi.fn(),
  releaseHold: vi.fn(),
  dataClasses: vi.fn(),
  erasures: vi.fn(),
  erasure: vi.fn(),
  erasureReceipt: vi.fn(),
  executeErasure: vi.fn(),
  createErasure: vi.fn(),
  dataSubjectErasureStatus: vi.fn(),
  calendar: vi.fn(),
}

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ ...DEFAULT_AUTH }),
}))

/** The toaster is not mounted in these tests, so a toast never reaches the DOM.
 *  What matters here is that the WARNING tone was used rather than success — a
 *  completed_with_gaps erasure celebrated as clean is the defect. */
const toastSpy = {
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
  info: vi.fn(),
}
vi.mock('@/components/ui/toaster', () => ({ toast: toastSpy }))

vi.mock('./api', async () => {
  const actual = await vi.importActual<typeof import('./api')>('./api')
  return {
    ...actual,
    complianceApi: holdsApi,
  }
})

const { HoldsTab } = await import('./holds-view')
const { ErasureTab } = await import('./erasure-view')
const { CalendarTab } = await import('./calendar-view')
const { incompleteReason } = await import('./api')
await import('./i18n')

/** Type the confirm phrase into ConfirmDialog's guard input.
 *  Anchored on #confirm-phrase (confirm-dialog.tsx:99) because these dialogs also
 *  render a reason textarea, so getByRole('textbox') is ambiguous by design. */
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

const ACTIVE_HOLD = {
  id: 'lh-1',
  matter_ref: 'CASE-42',
  scope_kind: 'subject' as const,
  subject_kind: 'user',
  subject_ref: 'u-7',
  reason: 'litigation hold',
  status: 'active' as const,
  created_by: 'dpo@example.com',
  created_at: '2026-08-01T10:00:00Z',
}

const PENDING_ERASURE = {
  id: 'er-1',
  subject_kind: 'user',
  subject_token: 'tok-1',
  subject: 'u-7',
  data_classes: ['session_transcript'],
  case_ref: 'DSAR-9',
  reason: 'Art. 17',
  requested_by: 'dpo@example.com',
  status: 'received' as const,
  created_at: '2026-08-01T10:00:00Z',
}

function resetMocks() {
  Object.values(toastSpy).forEach((fn) => fn.mockReset())
  Object.values(holdsApi).forEach((fn) => fn.mockReset())
  holdsApi.holds.mockResolvedValue({ items: [] })
  holdsApi.erasures.mockResolvedValue({ items: [] })
  holdsApi.erasure.mockResolvedValue({
    ...PENDING_ERASURE,
    status: 'completed',
  })
  holdsApi.dataClasses.mockResolvedValue({ items: [] })
  holdsApi.holdEvents.mockResolvedValue({ items: [] })
  holdsApi.checkHold.mockResolvedValue({ held: false })
  holdsApi.calendar.mockResolvedValue({
    milestones: [],
    watchlist: [],
    disclaimer: '',
  })
}

describe('incompleteReason — the line between "done" and "not done"', () => {
  it('classifies BOTH of the engine 202s, which are different news', () => {
    expect(
      incompleteReason({
        status: 202,
        data: { status: 'pending_approval', approval_ref: 'a' },
      }),
    ).toBe('pending_approval')
    // The second 202 (erasure.go:1233) comes AFTER local targets were erased.
    // Collapsing it into the first would tell an operator nothing was destroyed.
    expect(
      incompleteReason({ status: 202, data: { status: 'provider_pending' } }),
    ).toBe('provider_pending')
  })

  it('treats ANY 202 as incomplete, including shapes it does not recognise', () => {
    // The rule is the STATUS, not the body string. A future 202 the engine adds
    // must never fall through to the success branch.
    expect(incompleteReason({ status: 202, data: {} })).toBe('unknown')
    expect(incompleteReason({ status: 202, data: null })).toBe('unknown')
    expect(
      incompleteReason({ status: 202, data: { status: 'brand_new' } }),
    ).toBe('unknown')
  })

  it('does NOT treat a 200 as incomplete', () => {
    expect(
      incompleteReason({ status: 200, data: { status: 'completed' } }),
    ).toBeNull()
    // A released hold comes back as the hold DTO under 200.
    expect(
      incompleteReason({
        status: 200,
        data: { id: 'lh-1', status: 'released' },
      }),
    ).toBeNull()
    // Even a 200 whose body happens to carry the pending string is a 200.
    expect(
      incompleteReason({ status: 200, data: { status: 'pending_approval' } }),
    ).toBeNull()
  })
})

describe('HoldsTab — a release that did not happen is never reported as one', () => {
  it('says the hold is STILL ACTIVE when the engine answers 202', async () => {
    resetMocks()
    holdsApi.holds.mockResolvedValue({ items: [ACTIVE_HOLD] })
    holdsApi.releaseHold.mockResolvedValue({
      status: 202,
      data: {
        status: 'pending_approval',
        approval_ref: 'ap-9',
        detail: 'release awaiting dual-control approval (2 distinct humans)',
      },
    })
    const user = userEvent.setup()
    renderIntel(<HoldsTab canAdmin canRead />)

    await user.click(await screen.findByRole('button', { name: /release/i }))
    // The typed-phrase guard: a release cannot be a single misclick.
    await typeConfirmPhrase(user, 'RELEASE')
    await user.click(screen.getByRole('button', { name: /release hold/i }))

    expect(await screen.findByText(/STILL ACTIVE/i)).toBeInTheDocument()
    expect(screen.getAllByText(/ap-9/).length).toBeGreaterThan(0)
    // And it must NOT claim success.
    expect(document.body.textContent ?? '').not.toMatch(/hold released/i)
  })

  it('does not report a release the hold itself does not confirm', async () => {
    // Same allowlist rule on the other governed verb: a 200 whose DTO still says
    // `active` is not a release, and saying so would tell an operator
    // preservation was lifted when it was not.
    resetMocks()
    holdsApi.holds.mockResolvedValue({ items: [ACTIVE_HOLD] })
    holdsApi.releaseHold.mockResolvedValue({
      status: 200,
      data: { ...ACTIVE_HOLD, status: 'active' },
    })
    const user = userEvent.setup()
    renderIntel(<HoldsTab canAdmin canRead />)

    await user.click(await screen.findByRole('button', { name: /^release$/i }))
    await typeConfirmPhrase(user, 'RELEASE')
    await user.click(screen.getByRole('button', { name: /release hold/i }))

    await waitFor(() => expect(toastSpy.warning).toHaveBeenCalled())
    expect(toastSpy.success).not.toHaveBeenCalled()
  })

  it('does not attribute dual control to a 202 it does not recognise', async () => {
    resetMocks()
    holdsApi.holds.mockResolvedValue({ items: [ACTIVE_HOLD] })
    holdsApi.releaseHold.mockResolvedValue({
      status: 202,
      data: { status: 'future_gate' },
    })
    const user = userEvent.setup()
    renderIntel(<HoldsTab canAdmin canRead />)

    await user.click(await screen.findByRole('button', { name: /^release$/i }))
    await typeConfirmPhrase(user, 'RELEASE')
    await user.click(screen.getByRole('button', { name: /release hold/i }))

    // Still not released — but the reason must not be invented. Assert on the
    // sentence that DISTINGUISHES the two messages, not on "two distinct
    // humans", which also appears in the tab's standing asymmetry notice.
    expect(
      await screen.findByText(/reason this console does not recognise/i),
    ).toBeInTheDocument()
    expect(screen.queryByText(/preservation stays in force until/i)).toBeNull()
  })

  it('shows what a hold covers, in words, rather than a bare enum', async () => {
    resetMocks()
    holdsApi.holds.mockResolvedValue({ items: [ACTIVE_HOLD] })
    renderIntel(<HoldsTab canAdmin canRead />)
    expect(
      await screen.findByText(/Covers subject user:u-7/i),
    ).toBeInTheDocument()
  })

  it('hides the release action from a reader without hold:admin', async () => {
    resetMocks()
    holdsApi.holds.mockResolvedValue({ items: [ACTIVE_HOLD] })
    renderIntel(<HoldsTab canAdmin={false} canRead />)
    await screen.findByText('CASE-42')
    expect(screen.queryByRole('button', { name: /release/i })).toBeNull()
  })
})

describe('creates do not celebrate an unconfirmed 2xx', () => {
  // ONE ROW PER CLAUSE of confirmedCreate, each violating a single one. Rows
  // that break several clauses at once cannot discriminate: removing one clause
  // from the code leaves the others catching the row, and the suite stays green.
  // Measured in the sixth round — removing the HTTP check or the blank-id check
  // left 58/58 passing.
  it.each([
    // HTTP status alone: id and status both valid.
    [
      'a 202 with an otherwise valid body',
      { status: 202, data: { id: 'lh-1', status: 'active' } },
    ],
    // id alone: 201 and the right status.
    ['a 201 with no id', { status: 201, data: { status: 'active' } }],
    [
      'a 201 with a blank id',
      { status: 201, data: { id: '   ', status: 'active' } },
    ],
    // status alone: 201 and a valid id.
    [
      'a 201 with the wrong status',
      { status: 201, data: { id: 'lh-1', status: 'released' } },
    ],
  ])('warns when placing a hold returns %s', async (_label, response) => {
    resetMocks()
    holdsApi.createHold.mockResolvedValue(response)
    const user = userEvent.setup()
    renderIntel(<HoldsTab canAdmin canRead />)

    await user.click(
      await screen.findByRole('button', { name: /place a hold/i }),
    )
    await user.type(screen.getByLabelText(/matter reference/i), 'CASE-1')
    await user.type(screen.getByLabelText(/subject kind/i), 'user')
    await user.type(screen.getByLabelText(/subject reference/i), 'u-7')
    await user.type(screen.getByLabelText(/^reason/i), 'litigation')
    await user.click(screen.getByRole('button', { name: /^place hold$/i }))

    await waitFor(() => expect(toastSpy.warning).toHaveBeenCalled())
    expect(toastSpy.success).not.toHaveBeenCalled()
  })

  it('DOES celebrate a hold the engine confirms as active', async () => {
    resetMocks()
    holdsApi.createHold.mockResolvedValue({ status: 201, data: ACTIVE_HOLD })
    const user = userEvent.setup()
    renderIntel(<HoldsTab canAdmin canRead />)

    await user.click(
      await screen.findByRole('button', { name: /place a hold/i }),
    )
    await user.type(screen.getByLabelText(/matter reference/i), 'CASE-1')
    await user.type(screen.getByLabelText(/subject kind/i), 'user')
    await user.type(screen.getByLabelText(/subject reference/i), 'u-7')
    await user.type(screen.getByLabelText(/^reason/i), 'litigation')
    await user.click(screen.getByRole('button', { name: /^place hold$/i }))

    await waitFor(() => expect(toastSpy.success).toHaveBeenCalled())
    expect(toastSpy.warning).not.toHaveBeenCalled()
  })

  it.each([
    [
      'a 202 with an otherwise valid body',
      { status: 202, data: { id: 'er-1', status: 'received' } },
    ],
    ['a 201 with no id', { status: 201, data: { status: 'received' } }],
    [
      'a 201 with a blank id',
      { status: 201, data: { id: '  ', status: 'received' } },
    ],
    [
      'a 201 with the wrong status',
      { status: 201, data: { id: 'er-1', status: 'completed' } },
    ],
  ])('warns when registering a DSAR returns %s', async (_label, response) => {
    resetMocks()
    holdsApi.createErasure.mockResolvedValue(response)
    const user = userEvent.setup()
    renderIntel(<ErasureTab canAdmin canRead />)

    await user.click(
      await screen.findByRole('button', { name: /register a request/i }),
    )
    await user.type(screen.getByLabelText(/subject reference/i), 'u-7')
    await user.type(screen.getByLabelText(/case reference/i), 'DSAR-9')
    await user.click(
      screen.getByRole('button', { name: /^register request$/i }),
    )

    await waitFor(() => expect(toastSpy.warning).toHaveBeenCalled())
    expect(toastSpy.success).not.toHaveBeenCalled()
  })

  it('DOES celebrate a DSAR the engine confirms as received', async () => {
    // The positive counterweight the sixth round found missing: without it,
    // changing the expected status in the view left the suite green.
    resetMocks()
    holdsApi.createErasure.mockResolvedValue({
      status: 201,
      data: { ...PENDING_ERASURE, status: 'received' },
    })
    const user = userEvent.setup()
    renderIntel(<ErasureTab canAdmin canRead />)

    await user.click(
      await screen.findByRole('button', { name: /register a request/i }),
    )
    await user.type(screen.getByLabelText(/subject reference/i), 'u-7')
    await user.type(screen.getByLabelText(/case reference/i), 'DSAR-9')
    await user.click(
      screen.getByRole('button', { name: /^register request$/i }),
    )

    await waitFor(() => expect(toastSpy.success).toHaveBeenCalled())
    expect(toastSpy.warning).not.toHaveBeenCalled()
  })
})

describe('ErasureTab — the three ways an RTBF console lies', () => {
  it('states irreversibility BEFORE the operator can confirm', async () => {
    resetMocks()
    holdsApi.erasures.mockResolvedValue({ items: [PENDING_ERASURE] })
    const user = userEvent.setup()
    renderIntel(<ErasureTab canAdmin canRead />)

    await user.click(await screen.findByRole('button', { name: /^execute$/i }))
    expect(await screen.findByText(/IRREVERSIBLE/)).toBeInTheDocument()
    expect(screen.getByText(/no undo and no restore/i)).toBeInTheDocument()
    // The two gates are named before confirming, not after.
    expect(screen.getByText(/two independent gates/i)).toBeInTheDocument()
  })

  it('renders the covering holds when a legal hold vetoes the erasure (423)', async () => {
    resetMocks()
    holdsApi.erasures.mockResolvedValue({ items: [PENDING_ERASURE] })
    holdsApi.executeErasure.mockRejectedValue(
      new ApiError(
        423,
        'legal_hold',
        'blocked by an active legal hold',
        undefined,
        {
          holds: [{ id: 'lh-7', matter_ref: 'CASE-42', scope_kind: 'subject' }],
        },
      ),
    )
    const user = userEvent.setup()
    renderIntel(<ErasureTab canAdmin canRead />)

    await user.click(await screen.findByRole('button', { name: /^execute$/i }))
    await typeConfirmPhrase(user, 'ERASE')
    await user.click(screen.getByRole('button', { name: /execute erasure/i }))

    // "Blocked" is not enough: the operator must see BY WHAT.
    expect(await screen.findByText(/CASE-42/)).toBeInTheDocument()
    expect(
      screen.getByText(/preservation wins over erasure|must be released/i),
    ).toBeInTheDocument()
  })

  it('says nothing was erased when the engine answers 202', async () => {
    resetMocks()
    holdsApi.erasures.mockResolvedValue({ items: [PENDING_ERASURE] })
    holdsApi.executeErasure.mockResolvedValue({
      status: 202,
      data: { status: 'pending_approval', approval_ref: 'ap-3' },
    })
    const user = userEvent.setup()
    renderIntel(<ErasureTab canAdmin canRead />)

    await user.click(await screen.findByRole('button', { name: /^execute$/i }))
    await typeConfirmPhrase(user, 'ERASE')
    await user.click(screen.getByRole('button', { name: /execute erasure/i }))

    expect(
      await screen.findByText(/NOTHING HAS BEEN ERASED/i),
    ).toBeInTheDocument()
  })

  it('does NOT report a provider_pending 202 as an executed erasure', async () => {
    // Found by the sol-max contrast: the engine returns 202 provider_pending
    // AFTER local targets were erased, and matching only the pending_approval
    // string sent it down the success branch — "Erasure executed and receipt
    // sealed" with no key shredded and no receipt.
    resetMocks()
    holdsApi.erasures.mockResolvedValue({ items: [PENDING_ERASURE] })
    holdsApi.executeErasure.mockResolvedValue({
      status: 202,
      data: {
        status: 'provider_pending',
        detail: 'provider: 1 erased, 2 pending',
      },
    })
    const user = userEvent.setup()
    renderIntel(<ErasureTab canAdmin canRead />)

    await user.click(await screen.findByRole('button', { name: /^execute$/i }))
    await typeConfirmPhrase(user, 'ERASE')
    await user.click(screen.getByRole('button', { name: /execute erasure/i }))

    expect(await screen.findByText(/PARTIALLY ERASED/i)).toBeInTheDocument()
    // It must NOT claim nothing happened either — local data WAS erased.
    expect(document.body.textContent ?? '').not.toMatch(
      /NOTHING HAS BEEN ERASED/i,
    )
  })

  it('treats an unrecognised 202 as incomplete rather than as success', async () => {
    resetMocks()
    holdsApi.erasures.mockResolvedValue({ items: [PENDING_ERASURE] })
    holdsApi.executeErasure.mockResolvedValue({ status: 202, data: {} })
    const user = userEvent.setup()
    renderIntel(<ErasureTab canAdmin canRead />)

    await user.click(await screen.findByRole('button', { name: /^execute$/i }))
    await typeConfirmPhrase(user, 'ERASE')
    await user.click(screen.getByRole('button', { name: /execute erasure/i }))

    expect(await screen.findByText(/did NOT complete/i)).toBeInTheDocument()
  })

  it('distinguishes an RTBF coordinator 423 from a legal hold', async () => {
    // Both are 423. Telling an operator to release a hold that does not exist
    // sends them looking for the wrong thing.
    resetMocks()
    holdsApi.erasures.mockResolvedValue({ items: [PENDING_ERASURE] })
    holdsApi.executeErasure.mockRejectedValue(
      new ApiError(
        423,
        'rtbf_coordinator',
        'blocked by coordinator',
        undefined,
        {
          blockers: ['worm_lock active on archive tier'],
          warnings: ['replica lag 4m'],
        },
      ),
    )
    const user = userEvent.setup()
    renderIntel(<ErasureTab canAdmin canRead />)

    await user.click(await screen.findByRole('button', { name: /^execute$/i }))
    await typeConfirmPhrase(user, 'ERASE')
    await user.click(screen.getByRole('button', { name: /execute erasure/i }))

    expect(
      await screen.findByText(/worm_lock active on archive tier/),
    ).toBeInTheDocument()
    expect(screen.getByText(/NOT a legal hold/i)).toBeInTheDocument()
  })

  it.each([
    ['a state this build does not know', 'some_future_state'],
    ['no status at all', undefined],
    ['a non-terminal state', 'executing'],
  ])(
    'does not celebrate an erasure whose persisted state is %s',
    async (_label, persistedStatus) => {
      // ALLOWLIST GUARD for the console: only `completed` may produce a success
      // toast. Written as a denylist (warn only on completed_with_gaps), every
      // row here produced a clean success — which is how a DPO closes a DSAR
      // that did not finish.
      resetMocks()
      holdsApi.erasures.mockResolvedValue({ items: [PENDING_ERASURE] })
      holdsApi.executeErasure.mockResolvedValue({
        status: 200,
        data: { erasure_id: 'er-1', key_shredded: true },
      })
      holdsApi.erasure.mockResolvedValue({
        ...PENDING_ERASURE,
        status: persistedStatus,
      })
      const user = userEvent.setup()
      renderIntel(<ErasureTab canAdmin canRead />)

      await user.click(
        await screen.findByRole('button', { name: /^execute$/i }),
      )
      await typeConfirmPhrase(user, 'ERASE')
      await user.click(screen.getByRole('button', { name: /execute erasure/i }))

      await waitFor(() => expect(toastSpy.warning).toHaveBeenCalled())
      expect(toastSpy.success).not.toHaveBeenCalled()
    },
  )

  it('DOES celebrate a genuinely completed erasure', async () => {
    // The counterweight: without it, "always warn" would satisfy the rows above.
    resetMocks()
    holdsApi.erasures.mockResolvedValue({ items: [PENDING_ERASURE] })
    holdsApi.executeErasure.mockResolvedValue({
      status: 200,
      data: { erasure_id: 'er-1', key_shredded: true },
    })
    holdsApi.erasure.mockResolvedValue({
      ...PENDING_ERASURE,
      status: 'completed',
    })
    const user = userEvent.setup()
    renderIntel(<ErasureTab canAdmin canRead />)

    await user.click(await screen.findByRole('button', { name: /^execute$/i }))
    await typeConfirmPhrase(user, 'ERASE')
    await user.click(screen.getByRole('button', { name: /execute erasure/i }))

    await waitFor(() => expect(toastSpy.success).toHaveBeenCalled())
    expect(toastSpy.warning).not.toHaveBeenCalled()
  })

  it('does not title an unknown 423 as a legal hold', async () => {
    // The body may explain itself, but the TITLE must not assert a cause the
    // response never gave.
    resetMocks()
    holdsApi.erasures.mockResolvedValue({ items: [PENDING_ERASURE] })
    holdsApi.executeErasure.mockRejectedValue(
      new ApiError(423, 'future_veto', 'a future policy veto', undefined, {}),
    )
    const user = userEvent.setup()
    renderIntel(<ErasureTab canAdmin canRead />)

    await user.click(await screen.findByRole('button', { name: /^execute$/i }))
    await typeConfirmPhrase(user, 'ERASE')
    await user.click(screen.getByRole('button', { name: /execute erasure/i }))

    expect(await screen.findByText(/a future policy veto/)).toBeInTheDocument()
    expect(screen.queryByText(/Blocked by a legal hold/i)).toBeNull()
  })

  it('warns instead of celebrating when the erasure completed WITH GAPS', async () => {
    // The REAL contract: the 200 body is the receipt, which has no status field
    // (erasure.go:163-181). The terminal status lives on the persisted request.
    // A first version of this test invented `status` on the receipt, so it
    // passed while the console still showed a clean success toast.
    resetMocks()
    holdsApi.erasures.mockResolvedValue({ items: [PENDING_ERASURE] })
    holdsApi.executeErasure.mockResolvedValue({
      status: 200,
      data: { erasure_id: 'er-1', key_shredded: true, verify_ok: true },
    })
    holdsApi.erasure.mockResolvedValue({
      ...PENDING_ERASURE,
      status: 'completed_with_gaps',
    })
    const user = userEvent.setup()
    renderIntel(<ErasureTab canAdmin canRead />)

    await user.click(await screen.findByRole('button', { name: /^execute$/i }))
    await typeConfirmPhrase(user, 'ERASE')
    await user.click(screen.getByRole('button', { name: /execute erasure/i }))

    await waitFor(() => expect(toastSpy.warning).toHaveBeenCalled())
    expect(String(toastSpy.warning.mock.calls[0][0])).toMatch(/WITH GAPS/i)
    // And it must NOT be celebrated as a clean success.
    expect(toastSpy.success).not.toHaveBeenCalled()
  })

  it('discloses what is retained and the provider floor on the receipt', async () => {
    resetMocks()
    holdsApi.erasures.mockResolvedValue({
      items: [{ ...PENDING_ERASURE, status: 'completed' as const }],
    })
    holdsApi.erasureReceipt.mockResolvedValue({
      erasure_id: 'er-1',
      subject_kind: 'user',
      subject_token: 'tok-1',
      targets: [{ label: 'sessions', rows: 3 }],
      account_outcome: 'erased',
      provider_outcome: 'not_wired: no provider eraser configured',
      key_shredded: true,
      verify_ok: true,
      verify_checked: 42,
      retained: [
        { records: 'audit ledger events', basis: 'GDPR Art. 17(3)(b)' },
      ],
      case_ref: 'DSAR-9',
      ledger_seq: 7,
      manifest_hash: 'abc123',
      occurred_at: '2026-08-02T00:00:00Z',
      provider_floor_days: 30,
      provider_floor_known: true,
      provider_floor_source: 'covered-models',
    })
    const user = userEvent.setup()
    renderIntel(<ErasureTab canAdmin canRead />)

    await user.click(await screen.findByRole('button', { name: /receipt/i }))
    expect(await screen.findByText(/audit ledger events/)).toBeInTheDocument()
    expect(screen.getByText(/Art. 17\(3\)\(b\)/)).toBeInTheDocument()
    // Deleting our copy does not delete the provider's — never omitted.
    expect(
      screen.getByText(/30 days regardless of this erasure/i),
    ).toBeInTheDocument()
    // The legs the engine reports separately — a not-wired provider is exactly
    // why a request ends completed_with_gaps, so the receipt must show it.
    expect(
      screen.getByText(/not_wired: no provider eraser configured/),
    ).toBeInTheDocument()
    // Both legs, not just the provider one: omitting account_outcome used to
    // slip through this assertion.
    expect(screen.getByText('erased')).toBeInTheDocument()
  })
})

describe('console allowlist guard — the whole vocabulary, not examples', () => {
  // The Go guard walks the engine's status set; the console one used to test
  // three hand-picked examples, so a mutant that celebrated `received` or
  // `failed` left the suite green. This walks the same vocabulary.
  it.each(ERASURE_STATUSES.filter((s) => s !== ERASURE_STATUS_CLEAN))(
    'does not celebrate an erasure the engine persisted as %s',
    async (status) => {
      resetMocks()
      holdsApi.erasures.mockResolvedValue({ items: [PENDING_ERASURE] })
      holdsApi.executeErasure.mockResolvedValue({
        status: 200,
        data: { erasure_id: 'er-1', key_shredded: true },
      })
      holdsApi.erasure.mockResolvedValue({ ...PENDING_ERASURE, status })
      const user = userEvent.setup()
      renderIntel(<ErasureTab canAdmin canRead />)

      await user.click(
        await screen.findByRole('button', { name: /^execute$/i }),
      )
      await typeConfirmPhrase(user, 'ERASE')
      await user.click(screen.getByRole('button', { name: /execute erasure/i }))

      await waitFor(() => expect(toastSpy.warning).toHaveBeenCalled())
      expect(toastSpy.success).not.toHaveBeenCalled()
    },
  )

  it('the console vocabulary matches the engine, or this guard is not guarding', async () => {
    // Sync guard, same shape as the Go one and equally fail-closed: if the
    // engine source cannot be read, that is a failure, not a skip.
    const { readFile } = await import('node:fs/promises')
    const { resolve } = await import('node:path')
    // Anchored on the vitest cwd (web/), not import.meta.url, which vite does
    // not give as a file: URL. If the layout moves, this throws rather than
    // quietly stopping to guard.
    const src = await readFile(
      resolve(process.cwd(), '../modules/compliance/erasure.go'),
      'utf8',
    )
    // The pattern must cover every shape a Go declaration can take: alignment or
    // none, an explicit type, and BOTH string literal forms — interpreted and
    // raw (backticks). Each was found by a contrast, in that order; the raw one
    // is a valid Go constant that used to leave both guards green.
    const found = [
      ...src.matchAll(
        /erasureStatus[A-Za-z]+(?:\s+[A-Za-z0-9_.[\]]+)?\s*:?=\s*(?:"([a-z_]+)"|`([a-z_]+)`)/g,
      ),
    ].map((m) => m[1] ?? m[2])
    expect(found.length).toBeGreaterThan(0)
    // EQUALITY, both directions. Testing only engine ⊆ console lets the console
    // vocabulary rot: a status it carries that the engine dropped stays green.
    for (const status of found) {
      expect(
        ERASURE_STATUSES as readonly string[],
        `the engine defines ${status} and the console vocabulary does not carry it`,
      ).toContain(status)
    }
    for (const status of ERASURE_STATUSES) {
      expect(
        found,
        `the console carries ${status} which the engine no longer defines`,
      ).toContain(status)
    }
  })
})

describe('the two counterweights the final round found missing', () => {
  it('DOES report a release the hold confirms as released', async () => {
    // Without the positive case, "always warn on release" satisfies the
    // negative ones and the allowlist proves nothing.
    resetMocks()
    holdsApi.holds.mockResolvedValue({ items: [ACTIVE_HOLD] })
    holdsApi.releaseHold.mockResolvedValue({
      status: 200,
      data: { ...ACTIVE_HOLD, status: 'released', released_by: 'dpo' },
    })
    const user = userEvent.setup()
    renderIntel(<HoldsTab canAdmin canRead />)

    await user.click(await screen.findByRole('button', { name: /^release$/i }))
    await typeConfirmPhrase(user, 'RELEASE')
    await user.click(screen.getByRole('button', { name: /release hold/i }))

    await waitFor(() => expect(toastSpy.success).toHaveBeenCalled())
    expect(toastSpy.warning).not.toHaveBeenCalled()
  })

  it('warns when the read-back that confirms the erasure fails', async () => {
    // "I could not check" is not "it is clean" — the same rule the CLI exit code
    // follows. This row was missing, so the failure branch was unproven.
    resetMocks()
    holdsApi.erasures.mockResolvedValue({ items: [PENDING_ERASURE] })
    holdsApi.executeErasure.mockResolvedValue({
      status: 200,
      data: { erasure_id: 'er-1', key_shredded: true },
    })
    holdsApi.erasure.mockRejectedValue(new ApiError(500, 'internal', 'boom'))
    const user = userEvent.setup()
    renderIntel(<ErasureTab canAdmin canRead />)

    await user.click(await screen.findByRole('button', { name: /^execute$/i }))
    await typeConfirmPhrase(user, 'ERASE')
    await user.click(screen.getByRole('button', { name: /execute erasure/i }))

    await waitFor(() => expect(toastSpy.warning).toHaveBeenCalled())
    expect(toastSpy.success).not.toHaveBeenCalled()
  })
})

describe('CalendarTab — a provisional agreement is not law', () => {
  it('marks a not-in-force milestone distinctly from one in force', async () => {
    resetMocks()
    holdsApi.calendar.mockResolvedValue({
      milestones: [
        {
          id: 'm1',
          regime: 'eu_ai_act',
          date: '2026-08-02',
          title: 'GPAI obligations apply',
          effect: 'applies',
          status: 'in_force',
          source: { url: 'https://x', title: 'OJ', publisher: 'EU' },
          verified_on: '2026-06-01',
        },
        {
          id: 'm2',
          regime: 'eu_ai_act',
          date: '2026-09-01',
          title: 'Provisional text',
          effect: 'none yet',
          status: 'provisional_agreement',
          source: { url: 'https://y', title: 'Council', publisher: 'EU' },
          verified_on: '2026-06-01',
        },
      ],
      watchlist: [],
      disclaimer: 'provisional_agreement entries are NOT in-force law',
    })
    renderIntel(<CalendarTab framework={null} />)

    const inForce = await screen.findByText('In force')
    const provisional = screen.getByText(/Provisional agreement — not law/i)
    // Different semantic colour, not just different words: success vs warning.
    expect(inForce.className).toMatch(/text-success/)
    expect(provisional.className).toMatch(/text-warning/)
    expect(inForce.className).not.toEqual(provisional.className)
  })

  it('never renders a date without its primary source and verification date', async () => {
    resetMocks()
    holdsApi.calendar.mockResolvedValue({
      milestones: [
        {
          id: 'm1',
          regime: 'eu_ai_act',
          date: '2026-08-02',
          title: 'GPAI obligations apply',
          effect: 'applies',
          status: 'in_force',
          source: {
            url: 'https://eur-lex.europa.eu/x',
            title: 'Regulation (EU) 2024/1689',
            publisher: 'Official Journal',
          },
          verified_on: '2026-06-01',
        },
      ],
      watchlist: [],
      disclaimer: 'd',
    })
    renderIntel(<CalendarTab framework={null} />)

    const link = await screen.findByRole('link', {
      name: /Regulation \(EU\) 2024\/1689/,
    })
    expect(link).toHaveAttribute('href', 'https://eur-lex.europa.eu/x')
    expect(screen.getByText(/verified 2026-06-01/i)).toBeInTheDocument()
  })
})
