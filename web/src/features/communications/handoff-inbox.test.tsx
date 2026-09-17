// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE PERSONAL PAGE, and what it is NOT allowed to show: the collection is
// content-free, its filter is one exact server state, its pages come from the
// server's own continuation, and an elapsed window is not an expiry.
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))
const api = vi.hoisted(() => ({ listHandoffInbox: vi.fn() }))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, ...api }
})

import { ApiError, NetworkError } from '@/lib/api/errors'
import { HandoffInbox } from './handoff-inbox'
import {
  handoffRowOf,
  renderWithQuery,
  scopeOf,
  WORK_ITEM_ID,
  WS,
} from './test-harness'
import './i18n'

const id = (n: number) =>
  `0192f2c0-cccc-7000-8000-${String(n).padStart(12, '0')}`

beforeEach(() => {
  vi.clearAllMocks()
})

describe('the content-free personal handoff page', () => {
  it('asks for the explicit workspace, the OFFERED filter and the page size, and shows identity without content', async () => {
    api.listHandoffInbox.mockResolvedValue({
      items: [handoffRowOf({ deliveryId: id(1) })],
      has_more: false,
    })
    const { result } = renderWithQuery(() => (
      <HandoffInbox
        scope={scopeOf()}
        canDeliveryRead
        globalSuperadminAccount={false}
        onOpenHandoff={() => {}}
      />
    ))
    await waitFor(() => expect(api.listHandoffInbox).toHaveBeenCalledTimes(1))
    expect(api.listHandoffInbox.mock.calls[0][0]).toEqual({
      workspace_id: WS,
      // The initial filter is the server's own `offered`, and it is sent EXPLICITLY
      // rather than left to the engine's default: the screen and the request agree.
      state: 'offered',
      limit: 50,
      continuation: undefined,
    })
    expect(await screen.findByText(WORK_ITEM_ID)).toBeVisible()
    expect(screen.getByText(id(1))).toBeVisible()
    // THE ROW CANNOT SHOW WHAT THE ROUTE DOES NOT SEND. The fixture carries no
    //    summary because the engine sends none; this asserts the screen invents none.
    //    The POSITIVE CONTROL on the same selector is deliberate: this list renders
    //    inline rather than through a portal, but a negative assertion against an
    //    empty node passes without measuring anything, so the node is proved
    //    non-empty first.
    expect(result.container.textContent).toMatch(new RegExp(WORK_ITEM_ID))
    expect(result.container.textContent).not.toMatch(
      /Deploy freeze needs an owner/,
    )
    expect(
      screen.getByText(/Open a row to read its protected context/i),
    ).toBeVisible()
  })

  it('pages on the server continuation and RESETS the chain when the filter changes', async () => {
    api.listHandoffInbox.mockResolvedValueOnce({
      items: [handoffRowOf({ deliveryId: id(1) })],
      has_more: true,
      continuation: 'h3n1.page-two',
    })
    api.listHandoffInbox.mockResolvedValueOnce({
      items: [handoffRowOf({ deliveryId: id(2) })],
      has_more: false,
    })
    const user = userEvent.setup()
    renderWithQuery(() => (
      <HandoffInbox
        scope={scopeOf()}
        canDeliveryRead
        globalSuperadminAccount={false}
        onOpenHandoff={() => {}}
      />
    ))
    expect(
      await screen.findByText('More offers exist beyond this page'),
    ).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Load more' }))
    await waitFor(() => expect(api.listHandoffInbox).toHaveBeenCalledTimes(2))
    expect(api.listHandoffInbox.mock.calls[1][0]).toEqual({
      workspace_id: WS,
      state: 'offered',
      limit: 50,
      continuation: 'h3n1.page-two',
    })

    // Changing the filter is a NEW query: the engine mints continuations per filter
    // domain and refuses one minted for another, so no old token may travel.
    api.listHandoffInbox.mockResolvedValueOnce({ items: [], has_more: false })
    await user.click(screen.getByLabelText('State'))
    await user.click(await screen.findByRole('option', { name: 'Accepted' }))
    await waitFor(() => expect(api.listHandoffInbox).toHaveBeenCalledTimes(3))
    expect(api.listHandoffInbox.mock.calls[2][0]).toEqual({
      workspace_id: WS,
      state: 'accepted',
      limit: 50,
      continuation: undefined,
    })
  })

  it('offers the five server states and no invented one', async () => {
    api.listHandoffInbox.mockResolvedValue({ items: [], has_more: false })
    const user = userEvent.setup()
    renderWithQuery(() => (
      <HandoffInbox
        scope={scopeOf()}
        canDeliveryRead
        globalSuperadminAccount={false}
        onOpenHandoff={() => {}}
      />
    ))
    await waitFor(() => expect(api.listHandoffInbox).toHaveBeenCalled())
    await user.click(screen.getByLabelText('State'))
    const options = await screen.findAllByRole('option')
    expect(options.map((o) => o.textContent)).toEqual([
      'Offered',
      'Accepted',
      'Rejected',
      'Withdrawn',
      'Expired',
    ])
  })

  it('an elapsed window is labelled as a WINDOW, never as a recorded expiry', async () => {
    api.listHandoffInbox.mockResolvedValue({
      items: [
        handoffRowOf({
          deliveryId: id(3),
          state: 'offered',
          deadlineElapsed: true,
        }),
      ],
      has_more: false,
    })
    renderWithQuery(() => (
      <HandoffInbox
        scope={scopeOf()}
        canDeliveryRead
        globalSuperadminAccount={false}
        onOpenHandoff={() => {}}
      />
    ))
    expect(await screen.findByText('Window elapsed')).toBeVisible()
    // The persisted state is still what the engine says it is. Scoped to the ROW,
    // because "Offered" is also the current value of the filter control.
    const row = screen.getByText(id(3)).closest('tr') as HTMLElement
    expect(within(row).getByText('Offered')).toBeVisible()
    expect(within(row).queryByText('Expired')).toBeNull()
  })

  it('opens a row by its CARRIER delivery id — the only id the detail route accepts', async () => {
    api.listHandoffInbox.mockResolvedValue({
      items: [handoffRowOf({ deliveryId: id(4), handoffId: 'never-used' })],
      has_more: false,
    })
    const opened: string[] = []
    const user = userEvent.setup()
    renderWithQuery(() => (
      <HandoffInbox
        scope={scopeOf()}
        canDeliveryRead
        globalSuperadminAccount={false}
        onOpenHandoff={(d) => opened.push(d)}
      />
    ))
    await user.click(await screen.findByText(WORK_ITEM_ID))
    expect(opened).toEqual([id(4)])
  })

  it('an empty authorized page is EMPTY, not a refusal and not a failure to look', async () => {
    api.listHandoffInbox.mockResolvedValue({ items: [], has_more: false })
    renderWithQuery(() => (
      <HandoffInbox
        scope={scopeOf()}
        canDeliveryRead
        globalSuperadminAccount={false}
        onOpenHandoff={() => {}}
      />
    ))
    expect(
      await screen.findByText('No Offered handoffs in this workspace'),
    ).toBeVisible()
    expect(
      screen.getByText(/not a refusal and not a failure to look/i),
    ).toBeVisible()
  })

  it('with NO explicit workspace it asks for nothing at all', async () => {
    renderWithQuery(() => (
      <HandoffInbox
        scope={scopeOf({ workspace: null, workspaceName: null })}
        canDeliveryRead
        globalSuperadminAccount={false}
        onOpenHandoff={() => {}}
      />
    ))
    await Promise.resolve()
    expect(api.listHandoffInbox).not.toHaveBeenCalled()
  })

  it('without delivery:read it asks for nothing: the collection is not fetched to be hidden', async () => {
    renderWithQuery(() => (
      <HandoffInbox
        scope={scopeOf()}
        canDeliveryRead={false}
        globalSuperadminAccount={false}
        onOpenHandoff={() => {}}
      />
    ))
    await Promise.resolve()
    expect(api.listHandoffInbox).not.toHaveBeenCalled()
  })

  // UIQ1 D1 — the live list used DataTable's generic 503 copy. The engine's
  // typed unknown (evidence_unavailable / NO_HE_PODIDO_MIRAR) must stay unknown.
  it('a 503 evidence_unavailable stays unknown: not empty, not success, not the raw verdict', async () => {
    api.listHandoffInbox.mockRejectedValue(
      new ApiError(
        503,
        'evidence_unavailable',
        'evidence_unavailable',
        'req-uiq1',
        {},
        {
          code: 'evidence_unavailable',
          error: {
            code: 'evidence_unavailable',
            message: 'evidence_unavailable',
          },
          verdict: 'NO_HE_PODIDO_MIRAR',
        },
      ),
    )
    renderWithQuery(() => (
      <HandoffInbox
        scope={scopeOf()}
        canDeliveryRead
        globalSuperadminAccount={false}
        onOpenHandoff={() => {}}
      />
    ))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/handoff information cannot currently/i)
    expect(alert).not.toHaveTextContent(/unexpected error/i)
    expect(alert).not.toHaveTextContent('NO_HE_PODIDO_MIRAR')
    expect(
      screen.queryByText('No Offered handoffs in this workspace'),
    ).not.toBeInTheDocument()
    expect(screen.getByText(/req-uiq1/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
  })

  it('a transport failure is still the network state, not unknown and not empty', async () => {
    api.listHandoffInbox.mockRejectedValue(new NetworkError('socket closed'))
    renderWithQuery(() => (
      <HandoffInbox
        scope={scopeOf()}
        canDeliveryRead
        globalSuperadminAccount={false}
        onOpenHandoff={() => {}}
      />
    ))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/could not reach the control plane/i)
    expect(alert).not.toHaveTextContent(/handoff information cannot currently/i)
    expect(
      screen.queryByText('No Offered handoffs in this workspace'),
    ).not.toBeInTheDocument()
  })

  it('a 403 is a denial, not unknown and not an empty inbox', async () => {
    api.listHandoffInbox.mockRejectedValue(new ApiError(403, 'forbidden', 'no'))
    renderWithQuery(() => (
      <HandoffInbox
        scope={scopeOf()}
        canDeliveryRead
        globalSuperadminAccount={false}
        onOpenHandoff={() => {}}
      />
    ))
    expect(await screen.findByText(/not authorized/i)).toBeVisible()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(
      screen.queryByText('No Offered handoffs in this workspace'),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByText(/handoff information cannot currently/i),
    ).not.toBeInTheDocument()
  })
})

// R57/HA4 — THE ACCOUNT THAT CANNOT BE A RECIPIENT. Measured in HA3 against a real
// estate: a global superadmin session is refused tenant-scoped principal evidence
// (`core/auth/principal_evidence.go:262`, and `:315` for a global token), so this
// collection answers it 503 `evidence_unavailable` and nothing else ever will. The
// authority boundary is the engine's and stays exactly where it is; what changes is
// that the console stops asking a question whose only answer is "I could not look",
// and says which account the offers are addressed to instead.
describe('the personal page under a global superadmin account', () => {
  const GUIDANCE = 'Use a member account for personal handoffs'

  it('says which account holds handoffs and asks the engine NOTHING', async () => {
    const { result } = renderWithQuery(() => (
      <HandoffInbox
        scope={scopeOf()}
        canDeliveryRead
        globalSuperadminAccount
        onOpenHandoff={() => {}}
      />
    ))
    expect(await screen.findByText(GUIDANCE)).toBeVisible()
    // The whole point: not one request, not even to discover what is already known.
    await Promise.resolve()
    expect(api.listHandoffInbox).not.toHaveBeenCalled()

    // AND IT IS NONE OF THE FOUR SENTENCES THAT WOULD BE FALSE HERE. Not an empty
    // inbox (nothing was read), not the typed unknown (nothing was asked), not a
    // refusal of authority (the engine denied nothing), not a transport failure.
    expect(
      screen.queryByText('No Offered handoffs in this workspace'),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByText(/handoff information cannot currently/i),
    ).not.toBeInTheDocument()
    expect(screen.queryByText(/not authorized/i)).not.toBeInTheDocument()
    expect(
      screen.queryByText(/could not reach the control plane/i),
    ).not.toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()

    // The feature is not hidden and not removed: the page is still here, with its
    // own title, and it tells the operator the one act that IS available to them.
    expect(screen.getByText('Handoffs addressed to you')).toBeVisible()
    expect(
      screen.getByText(/Sign in with a member account for this organization/),
    ).toBeVisible()

    const region = result.container.querySelector(
      '[data-slot="handoff-account-guidance"]',
    ) as HTMLElement
    expect(region).toHaveAttribute('role', 'status')
    expect(region).toHaveAttribute('aria-live', 'polite')
    // No button and no link INSIDE the guidance: this caller cannot complete a
    // setup or an administrative act from here, so none is offered.
    expect(within(region).queryAllByRole('button')).toHaveLength(0)
    expect(within(region).queryAllByRole('link')).toHaveLength(0)
  })

  it('leaves no control that could start the read, and clicking refresh asks nothing', async () => {
    renderWithQuery(() => (
      <HandoffInbox
        scope={scopeOf()}
        canDeliveryRead
        globalSuperadminAccount
        onOpenHandoff={() => {}}
      />
    ))
    expect(await screen.findByText(GUIDANCE)).toBeVisible()
    // The controls stay MOUNTED — the page keeps its shape — and inert.
    const refresh = screen.getByRole('button', { name: 'Refresh' })

    // ⛔ THE CLICK IS MEASURED FIRST, AND THE ORDER IS THE POINT. With the disabled
    //    assertion first, dropping only the attribute fails the case there and NOTHING
    //    ever measures whether a request escapes — the two layers would be one
    //    assertion. This way the request check runs against whichever layer survives:
    //    with the attribute gone the callback guard is what has to hold, and with both
    //    gone `refetch()` fires regardless of `enabled` and this line is what catches it.
    fireEvent.click(refresh)
    await Promise.resolve()
    expect(api.listHandoffInbox).not.toHaveBeenCalled()

    expect(refresh).toBeDisabled()
    expect(screen.getByLabelText('State')).toBeDisabled()
    expect(screen.getByLabelText('Page size')).toBeDisabled()
    // Pagination and retry are not disabled controls, they are ABSENT: the table
    // that owns them is not mounted.
    expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
  })

  it('a member who BECOMES global loses the rows already in the cache, and asks nothing more', async () => {
    api.listHandoffInbox.mockResolvedValue({
      items: [handoffRowOf({ deliveryId: id(5) })],
      has_more: true,
      continuation: 'h3n1.page-two',
    })
    let global = false
    const opened: string[] = []
    const { rerender } = renderWithQuery(() => (
      <HandoffInbox
        scope={scopeOf()}
        canDeliveryRead
        globalSuperadminAccount={global}
        onOpenHandoff={(d) => opened.push(d)}
      />
    ))
    // POSITIVE CONTROL FIRST: the member really did read a row, so the assertions
    // below measure a row that DISAPPEARS instead of one that never existed.
    expect(await screen.findByText(id(5))).toBeVisible()
    expect(api.listHandoffInbox).toHaveBeenCalledTimes(1)

    global = true
    rerender()

    // The page is still cached under this exact query key — the key did not move —
    // and it is not shown: the table is not rendered, so no row can be read or opened.
    await waitFor(() => expect(screen.getByText(GUIDANCE)).toBeVisible())
    expect(screen.queryByText(id(5))).toBeNull()
    expect(screen.queryByText(WORK_ITEM_ID)).toBeNull()
    expect(screen.queryByRole('table')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()
    expect(
      screen.queryByText('More offers exist beyond this page'),
    ).not.toBeInTheDocument()
    expect(opened).toEqual([])
    expect(api.listHandoffInbox).toHaveBeenCalledTimes(1)
  })

  it('a global account that becomes a MEMBER resumes its own scoped read', async () => {
    api.listHandoffInbox.mockResolvedValue({
      items: [handoffRowOf({ deliveryId: id(6) })],
      has_more: false,
    })
    let global = true
    const { rerender } = renderWithQuery(() => (
      <HandoffInbox
        scope={scopeOf()}
        canDeliveryRead
        globalSuperadminAccount={global}
        onOpenHandoff={() => {}}
      />
    ))
    expect(await screen.findByText(GUIDANCE)).toBeVisible()
    expect(api.listHandoffInbox).not.toHaveBeenCalled()

    global = false
    rerender()

    // Not merely "a request happens": the SAME request this page always makes, with
    // its explicit workspace, its `offered` filter, its page size and its tenant.
    await waitFor(() => expect(api.listHandoffInbox).toHaveBeenCalledTimes(1))
    expect(api.listHandoffInbox.mock.calls[0][0]).toEqual({
      workspace_id: WS,
      state: 'offered',
      limit: 50,
      continuation: undefined,
    })
    expect(api.listHandoffInbox.mock.calls[0][1]).toEqual({ tenant: 't1' })
    expect(await screen.findByText(id(6))).toBeVisible()
    expect(screen.queryByText(GUIDANCE)).toBeNull()
  })

  it('the guidance is for the ACCOUNT, never for a missing permission: without delivery:read the page is unchanged', async () => {
    renderWithQuery(() => (
      <HandoffInbox
        scope={scopeOf()}
        canDeliveryRead={false}
        globalSuperadminAccount={false}
        onOpenHandoff={() => {}}
      />
    ))
    await Promise.resolve()
    expect(api.listHandoffInbox).not.toHaveBeenCalled()
    expect(screen.queryByText(GUIDANCE)).toBeNull()
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeDisabled()
    expect(screen.getByLabelText('State')).toBeEnabled()
  })
})
