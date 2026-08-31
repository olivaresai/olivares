// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement, ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { fetchMock, downloadMock, authState, navigate } = vi.hoisted(() => ({
  fetchMock: vi.fn(),
  downloadMock: vi.fn(),
  authState: { can: (_p: string): boolean => true },
  navigate: vi.fn(),
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@tanstack/react-router', () => ({
  // useUrlState follows the location, so the mock answers with the REAL URL.
  // A constant '' passed while the effect read window.location; since
  // f9426990b the effect reads this value, and a constant would erase the
  // state the URL just seeded.
  useRouterState: () => window.location.search,
  useNavigate: () => navigate,
  Link: ({ to, children }: { to: string; children: ReactNode }) => (
    <a href={to}>{children}</a>
  ),
}))
// A stored view is server data: it can carry a severity floor this view never
// offered (and keys it does not own at all).
vi.mock('@/features/saved-views', () => ({
  SavedViewsMenu: ({
    onApply,
  }: {
    onApply: (params: Record<string, string>) => void
  }) => (
    <button
      type="button"
      onClick={() =>
        onApply({
          severity: 'legendary',
          category: 'policy_drift',
          kind: 'agent',
          unknown: 'ignored',
        })
      }
    >
      Apply saved posture view
    </button>
  ),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return {
    ...actual,
    fetchPostureExport: fetchMock,
    downloadBlob: downloadMock,
  }
})

import { PostureExportView } from './posture-export-view'

const doc = {
  tenant: 't1',
  note: 'Read-only ground-truth posture for control-tower enrichment.',
  inventory: [{ kind: 'agent' }, { kind: 'model' }],
  inventory_truncated: false,
  posture_drift: {
    unexpected_count: 1,
    unused_grant_count: 2,
    inventory_grant_count: 0,
    truncated: false,
  },
  findings: [{ kind: 'policy_drift' }],
  findings_truncated: false,
}

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = () => true
  window.history.replaceState(null, '', '/posture-export')
  fetchMock.mockResolvedValue({
    doc,
    blob: new Blob([JSON.stringify(doc)], { type: 'application/json' }),
  })
})
afterEach(() => {
  window.history.replaceState(null, '', '/')
})

/** The last navigate() argument, as useUrlState shapes it. */
function lastNavigation() {
  const call = navigate.mock.calls.at(-1)?.[0] as
    | {
        replace?: boolean
        search: (cur: Record<string, unknown>) => Record<string, unknown>
      }
    | undefined
  if (!call) throw new Error('navigate() was never called')
  return call
}

describe('PostureExportView', () => {
  it('forbids a reader without the permission', () => {
    authState.can = () => false
    wrap(<PostureExportView />)
    expect(screen.queryByRole('button', { name: /export posture/i })).not.toBeInTheDocument()
  })

  it('exports through the real endpoint, downloads, and summarizes', async () => {
    const user = userEvent.setup()
    wrap(<PostureExportView />)

    await user.click(screen.getByRole('button', { name: /export posture/i }))

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        expect.objectContaining({ severity: '' }),
      ),
    )
    expect(downloadMock).toHaveBeenCalled()
    // The summary reflects the REAL document the backend returned: 2 inventory
    // entities and a drift total of 3 (1 unexpected + 2 unused + 0 inventory grants).
    expect(await screen.findByText('2')).toBeInTheDocument()
    expect(screen.getByText('3')).toBeInTheDocument()
  })

  it('passes the chosen severity floor to the backend', async () => {
    const user = userEvent.setup()
    wrap(<PostureExportView />)

    await user.click(screen.getByRole('combobox', { name: /minimum severity/i }))
    await user.click(await screen.findByRole('option', { name: /high/i }))
    await user.click(screen.getByRole('button', { name: /export posture/i }))

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        expect.objectContaining({ severity: 'high' }),
      ),
    )
  })
})

//the three filters ARE the scope of the exported document, so the link
// that produced an export has to be the link that reproduces it.
describe('PostureExportView — URL state', () => {
  it('round-trips every filter through the URL and patches by replace', async () => {
    window.history.replaceState(
      null,
      '',
      '/posture-export?severity=high&category=policy_drift&kind=agent',
    )
    const user = userEvent.setup()
    wrap(<PostureExportView />)

    // URL → state.
    expect(
      screen.getByRole('combobox', { name: /minimum severity/i }),
    ).toHaveTextContent(/high/i)
    expect(screen.getByRole('textbox', { name: 'Category' })).toHaveValue(
      'policy_drift',
    )
    expect(screen.getByRole('textbox', { name: 'Inventory kind' })).toHaveValue(
      'agent',
    )

    // state → URL: one key changes, the rest of the search survives untouched,
    // and a filter never grows the history stack.
    await user.click(
      screen.getByRole('combobox', { name: /minimum severity/i }),
    )
    await user.click(await screen.findByRole('option', { name: /critical/i }))

    const patched = lastNavigation()
    expect(patched.replace).toBe(true)
    expect(
      patched.search({
        severity: 'high',
        category: 'policy_drift',
        kind: 'agent',
        tab: 'not-ours',
      }),
    ).toEqual({
      severity: 'critical',
      category: 'policy_drift',
      kind: 'agent',
      tab: 'not-ours',
    })

    // The default stays OUT of the URL: back to "any severity" removes the key
    // rather than writing it, so a pristine view has a clean shareable link.
    await user.click(
      screen.getByRole('combobox', { name: /minimum severity/i }),
    )
    await user.click(
      await screen.findByRole('option', { name: /any severity/i }),
    )
    expect(lastNavigation().search({ severity: 'critical' })).toHaveProperty(
      'severity',
      undefined,
    )

    // …and the state the URL produced is the state the export is taken at.
    await user.click(screen.getByRole('button', { name: /export posture/i }))
    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith({
        severity: '',
        category: 'policy_drift',
        kind: 'agent',
      }),
    )
  })

  it('falls back to the default for a severity floor it never offered, and says so', async () => {
    window.history.replaceState(
      null,
      '',
      '/posture-export?severity=galactic&category=policy_drift',
    )
    const user = userEvent.setup()
    wrap(<PostureExportView />)

    // Falling back silently would hand the recipient a WIDER export than the
    // link claims while looking identical, so the view has to say it.
    expect(screen.getByTestId('url-state-notice')).toBeInTheDocument()
    expect(
      screen.getByRole('combobox', { name: /minimum severity/i }),
    ).toHaveTextContent(/any severity/i)
    // The key that was fine is still applied.
    expect(screen.getByRole('textbox', { name: 'Category' })).toHaveValue(
      'policy_drift',
    )

    await user.click(screen.getByRole('button', { name: /export posture/i }))
    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith({
        severity: '',
        category: 'policy_drift',
        kind: '',
      }),
    )
  })

  it('re-sanitises a saved view before it can reach the request', async () => {
    const user = userEvent.setup()
    wrap(<PostureExportView />)

    await user.click(
      screen.getByRole('button', { name: 'Apply saved posture view' }),
    )

    expect(await screen.findByTestId('url-state-notice')).toHaveTextContent(
      /saved/i,
    )
    expect(lastNavigation().replace).toBe(true)

    await user.click(screen.getByRole('button', { name: /export posture/i }))
    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith({
        severity: '',
        category: 'policy_drift',
        kind: 'agent',
      }),
    )
    // The stored key this view does not own never reaches the request.
    expect(fetchMock).not.toHaveBeenCalledWith(
      expect.objectContaining({ unknown: 'ignored' }),
    )
  })
})

describe('PostureExportView free-text filters', () => {
  // The two free-text keys had NO test that typed into them: their
  // onChange -> patchScope wiring was asserted nowhere, so swapping the two
  // handlers would have left every other test green.
  it('writes each free-text filter to its OWN url key', async () => {
    wrap(<PostureExportView />)
    const user = userEvent.setup()

    await user.type(screen.getByLabelText(/category/i), 'a')
    // Swapping the two handlers would leave every other test in this file
    // green, because none of them types into either control.
    expect(lastNavigation().search({})).toEqual({ category: 'a' })

    await user.type(screen.getByLabelText(/kind/i), 'b')
    expect(lastNavigation().search({})).toEqual({ kind: 'b' })
  })

  it('bounds the input so a too-long term cannot be written at all', () => {
    wrap(<PostureExportView />)
    // The decoder refuses anything over MAX_TERM_LEN. Without the same bound on
    // the control, the value reaches state and the URL first and is only
    // rejected on the way back, which empties the field under the operator.
    expect(screen.getByLabelText(/category/i)).toHaveAttribute(
      'maxlength',
      '128',
    )
    expect(screen.getByLabelText(/kind/i)).toHaveAttribute('maxlength', '128')
  })
})
