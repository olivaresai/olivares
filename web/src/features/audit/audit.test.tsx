// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { ApiError } from '@/lib/api/errors'
import type { AuditEventDTO } from '@/lib/api/types'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
  info: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

const auth = vi.hoisted(() => ({
  activeTenant: 't1' as string | null,
  isSuperadmin: false,
  can: () => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

const navigate = vi.hoisted(() => vi.fn())
vi.mock('@tanstack/react-router', () => ({
  // useUrlState follows the location, so the mock answers with the REAL URL.
  // A constant '' passed while the effect read window.location; since
  // f9426990b the effect reads this value, and a constant would erase the
  // state the URL just seeded.
  useRouterState: () => window.location.search,
  useNavigate: () => navigate,
}))

vi.mock('@/features/saved-views', () => ({
  SavedViewsMenu: ({
    onApply,
  }: {
    onApply: (params: Record<string, string>) => void
  }) => (
    <button
      type="button"
      onClick={() =>
        onApply({ actor: 'user:saved', action: 'policy.', unknown: 'ignored' })
      }
    >
      Apply saved audit view
    </button>
  ),
}))

const api = vi.hoisted(() => ({
  list: vi.fn(),
  systemList: vi.fn(),
  verify: vi.fn(),
  pubkey: vi.fn(),
}))
vi.mock('@/lib/api/endpoints', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api/endpoints')>()
  return { ...actual, auditApi: api }
})

const exp = vi.hoisted(() => ({
  fetchAuditExport: vi.fn(),
  downloadBlob: vi.fn(),
  exportFilename: vi.fn((f: string) => `olivares-audit-${f}.log`),
}))
vi.mock('./export', () => exp)

import { AuditEventSheet } from './audit-detail'
import { AuditView } from './audit-view'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider delayDuration={0}>{ui}</TooltipProvider>
    </QueryClientProvider>,
  )
}

const ev1: AuditEventDTO = {
  id: 'evt-0000000000000001',
  seq: 12,
  occurred_at: '2026-06-20T10:00:00Z',
  actor: 'user:alice',
  actor_kind: 'user',
  action: 'agent.create',
  target_kind: 'agent',
  target_id: 'ag-1',
  prev_hash: 'aa'.repeat(32),
  hash: 'bb'.repeat(32),
  sig: 'c2lnbmF0dXJl',
}

const ev2: AuditEventDTO = {
  id: 'evt-0000000000000002',
  seq: 13,
  occurred_at: '2026-06-20T10:05:00Z',
  actor: '',
  actor_kind: 'system',
  action: 'audit.read',
  prev_hash: 'bb'.repeat(32),
  hash: 'cc'.repeat(32),
}

const verifyOk = {
  ok: true,
  chain: { ok: true, checked: 5, break_at: 0, reason: '' },
  checkpoints: {
    ok: true,
    status: 'ok' as const,
    count: 2,
    latest_attested_seq: 13,
    first_bad_seq: 0,
    reason: '',
  },
}

const verifyBroken = {
  ok: false,
  chain: { ok: false, checked: 3, break_at: 4, reason: 'hash mismatch' },
  checkpoints: {
    ok: true,
    status: 'ok' as const,
    count: 1,
    latest_attested_seq: 3,
    first_bad_seq: 0,
    reason: '',
  },
}

// A structurally-intact chain that has never been attested — a first-boot install
// before the checkpoint scheduler has fired. This fixture used to claim the engine
// returns checkpoints.ok=TRUE with count=0; it does not, and never did (measured
// against a clean install: ok=false, reason="no-checkpoints"). The wrong fixture is
// why the view's red path went unnoticed: no test ever fed the shape the engine
// actually sends. Mirrors core/api/handlers_audit.go handleAuditVerify.
const verifyNoCheckpoints = {
  ok: true,
  chain: { ok: true, checked: 5, break_at: 0, reason: '' },
  checkpoints: {
    ok: false,
    status: 'pending' as const,
    count: 0,
    latest_attested_seq: 0,
    first_bad_seq: 0,
    reason: 'no-checkpoints',
  },
}

// A checkpoint that EXISTS and does not verify — the tamper case. This is the one
// that must stay loud; it is the control for the calm "pending" state above.
const verifyForgedCheckpoint = {
  ok: false,
  chain: { ok: true, checked: 9, break_at: 0, reason: '' },
  checkpoints: {
    ok: false,
    status: 'failed' as const,
    count: 2,
    latest_attested_seq: 4,
    first_bad_seq: 7,
    reason: 'checkpoint-sig-invalid',
  },
}

beforeEach(() => {
  for (const fn of Object.values(api)) fn.mockReset()
  for (const fn of Object.values(exp)) fn.mockReset?.()
  exp.exportFilename.mockImplementation(
    (f: string) => `olivares-audit-${f}.log`,
  )
  toast.success.mockReset()
  toast.error.mockReset()
  toast.warning.mockReset()
  navigate.mockReset()
  auth.isSuperadmin = false
  auth.activeTenant = 't1'
  window.history.replaceState(null, '', '/audit')
  api.list.mockResolvedValue({ items: [], has_more: false })
  api.systemList.mockResolvedValue({ items: [], has_more: false })
  api.verify.mockResolvedValue(verifyOk)
  api.pubkey.mockResolvedValue({
    algorithm: 'ed25519',
    public_key: 'QkFTRTY0S0VZ',
  })
})
afterEach(() => {
  vi.clearAllMocks()
  window.history.replaceState(null, '', '/')
})

describe('AuditView — ledger list', () => {
  it('renders ledger events with action, actor (+kind) and a hash chip', async () => {
    api.list.mockResolvedValue({ items: [ev1, ev2], has_more: false })
    wrap(<AuditView />)

    expect(await screen.findByText('agent.create')).toBeInTheDocument()
    const row1 = screen.getByText('agent.create').closest('tr')!
    expect(within(row1).getByText('user:alice')).toBeInTheDocument()
    expect(within(row1).getByText('User')).toBeInTheDocument()
    // The hash is shown as a truncated fingerprint chip (head…tail of the hex).
    expect(within(row1).getByText(/bbbbbbbb…bbbbbb/)).toBeInTheDocument()

    // A system-actor event with no actor string falls back to the "system" label.
    const row2 = screen.getByText('audit.read').closest('tr')!
    expect(within(row2).getByText('system')).toBeInTheDocument()
    expect(within(row2).getByText('System')).toBeInTheDocument()
  })

  it('reading the ledger is disclosed as itself audited', async () => {
    wrap(<AuditView />)
    expect(
      await screen.findByText(/recorded in the.*ledger|itself.*audit/i),
    ).toBeInTheDocument()
  })

  it('sends validated server-side filters and RFC3339 UTC bounds', async () => {
    wrap(<AuditView />)

    await userEvent.type(
      await screen.findByRole('textbox', { name: 'Actor' }),
      'user:alice',
    )
    await userEvent.type(
      screen.getByRole('textbox', { name: 'Action prefix' }),
      'agent.',
    )
    fireEvent.change(screen.getByLabelText('Since'), {
      target: { value: '2026-07-24T12:30' },
    })

    await waitFor(() =>
      expect(api.list).toHaveBeenLastCalledWith({
        from: 1,
        limit: 100,
        actor: 'user:alice',
        action: 'agent.',
        since: new Date('2026-07-24T12:30').toISOString(),
      }),
    )
  })

  it('continues a sparse filtered scan with next_from and shows its honest status', async () => {
    window.history.replaceState(null, '', '/audit?action=agent.')
    api.list.mockImplementation(({ from }: { from?: number }) =>
      from === 1
        ? Promise.resolve({
            items: [ev1],
            next_from: 500,
            scan_complete: false,
            has_more: true,
          })
        : Promise.resolve({
            items: [],
            next_from: 700,
            scan_complete: true,
            has_more: false,
          }),
    )
    wrap(<AuditView />)

    expect(
      await screen.findByText(
        /Scanned up to seq 499 — not the full ledger; Load more continues the scan/,
      ),
    ).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Load more' }))

    await waitFor(() =>
      expect(api.list).toHaveBeenCalledWith({
        from: 500,
        limit: 100,
        action: 'agent.',
      }),
    )
  })

  it('applies a saved view to URL-backed filter state', async () => {
    wrap(<AuditView />)
    await userEvent.click(
      await screen.findByRole('button', { name: 'Apply saved audit view' }),
    )

    await waitFor(() =>
      expect(api.list).toHaveBeenLastCalledWith({
        from: 1,
        limit: 100,
        actor: 'user:saved',
        action: 'policy.',
      }),
    )
    expect(navigate).toHaveBeenCalledWith(
      expect.objectContaining({ replace: true }),
    )
  })

  it('ignores invalid timestamp and scope values from the URL', async () => {
    window.history.replaceState(
      null,
      '',
      '/audit?actor=user%3Aalice&since=not-a-date&scope=other',
    )
    wrap(<AuditView />)

    await waitFor(() =>
      expect(api.list).toHaveBeenCalledWith({
        from: 1,
        limit: 100,
        actor: 'user:alice',
      }),
    )
    expect(api.systemList).not.toHaveBeenCalled()
  })
})

describe('AuditView — chain verification', () => {
  it('renders the intact verdict when the engine reports ok', async () => {
    wrap(<AuditView />)
    await userEvent.click(
      await screen.findByRole('button', { name: /verify chain/i }),
    )
    expect(await screen.findByText('Integrity verified')).toBeInTheDocument()
    expect(screen.getByText(/Chain · 5 links/)).toBeInTheDocument()
    expect(screen.getByText(/Checkpoints · 2 signed/)).toBeInTheDocument()
    expect(api.verify).toHaveBeenCalledTimes(1)
  })

  it('surfaces the break seq when the chain is broken', async () => {
    api.verify.mockResolvedValue(verifyBroken)
    wrap(<AuditView />)
    await userEvent.click(
      await screen.findByRole('button', { name: /verify chain/i }),
    )
    expect(await screen.findByText('Integrity failed')).toBeInTheDocument()
    expect(
      screen.getByText(/Chain broken at seq 4 — hash mismatch/),
    ).toBeInTheDocument()
  })

  it('does not paint zero signed checkpoints as a green pass', async () => {
    api.verify.mockResolvedValue(verifyNoCheckpoints)
    wrap(<AuditView />)
    await userEvent.click(
      await screen.findByRole('button', { name: /verify chain/i }),
    )
    // The honest "no attestation coverage" label, never a green "0 signed".
    expect(
      await screen.findByText('No signed checkpoints yet'),
    ).toBeInTheDocument()
    expect(screen.queryByText(/Checkpoints · 0 signed/)).toBeNull()
  })

  // A ledger nobody has attested yet is not a broken ledger. Painting the
  // "Checkpoint signature failed at seq 0" line on a healthy first-boot install
  // is how an operator is taught that this red means nothing.
  it('does not accuse an unattested ledger of a checkpoint failure', async () => {
    api.verify.mockResolvedValue(verifyNoCheckpoints)
    wrap(<AuditView />)
    await userEvent.click(
      await screen.findByRole('button', { name: /verify chain/i }),
    )
    expect(
      await screen.findByText('No signed checkpoints yet'),
    ).toBeInTheDocument()
    expect(screen.queryByText(/Checkpoint signature failed/i)).toBeNull()
    expect(screen.queryByText(/no-checkpoints/)).toBeNull()
    // The chain verdict itself is still the green one — nothing was hidden.
    expect(screen.getByText('Integrity verified')).toBeInTheDocument()
  })

  // The control for the test above: a checkpoint that EXISTS and fails to verify
  // must still be loud, naming its seq and its reason.
  it('keeps a forged checkpoint loud, with its seq and reason', async () => {
    api.verify.mockResolvedValue(verifyForgedCheckpoint)
    wrap(<AuditView />)
    await userEvent.click(
      await screen.findByRole('button', { name: /verify chain/i }),
    )
    expect(
      await screen.findByText(
        /Checkpoint signature failed at seq 7 — checkpoint-sig-invalid/,
      ),
    ).toBeInTheDocument()
    // And the headline verdict goes red with it.
    expect(screen.getByText('Integrity failed')).toBeInTheDocument()
    expect(screen.queryByText('No signed checkpoints yet')).toBeNull()
  })
})

describe('AuditView — export', () => {
  it('exports the selected format and downloads the engine bytes', async () => {
    exp.fetchAuditExport.mockResolvedValue(new Blob(['cef-bytes']))
    wrap(<AuditView />)
    await userEvent.click(
      await screen.findByRole('button', { name: /^export$/i }),
    )
    await waitFor(() =>
      expect(exp.fetchAuditExport).toHaveBeenCalledWith('cef'),
    )
    expect(exp.downloadBlob).toHaveBeenCalledTimes(1)
    expect(toast.success).toHaveBeenCalled()
  })

  it('does not download on a 403 and warns instead', async () => {
    exp.fetchAuditExport.mockRejectedValue(
      new ApiError(403, 'forbidden', 'nope'),
    )
    wrap(<AuditView />)
    await userEvent.click(
      await screen.findByRole('button', { name: /^export$/i }),
    )
    await waitFor(() => expect(toast.warning).toHaveBeenCalled())
    expect(exp.downloadBlob).not.toHaveBeenCalled()
  })

  it('exports with the active server filters when the toggle is on', async () => {
    exp.fetchAuditExport.mockResolvedValue(new Blob(['cef-bytes']))
    wrap(<AuditView />)
    await userEvent.type(
      await screen.findByRole('textbox', { name: 'Actor' }),
      'user:alice',
    )
    expect(
      screen.getByRole('checkbox', {
        name: 'Use current filters & range',
      }),
    ).toBeChecked()
    await userEvent.click(screen.getByRole('button', { name: /^export$/i }))

    await waitFor(() =>
      expect(exp.fetchAuditExport).toHaveBeenCalledWith('cef', {
        actor: 'user:alice',
      }),
    )
  })

  it('exports an optional inclusive sequence range', async () => {
    exp.fetchAuditExport.mockResolvedValue(new Blob(['cef-bytes']))
    wrap(<AuditView />)
    await userEvent.type(
      await screen.findByRole('spinbutton', { name: 'From seq' }),
      '10',
    )
    await userEvent.type(
      screen.getByRole('spinbutton', { name: 'To seq' }),
      '25',
    )
    await userEvent.click(screen.getByRole('button', { name: /^export$/i }))

    await waitFor(() =>
      expect(exp.fetchAuditExport).toHaveBeenCalledWith('cef', {
        from: 10,
        to: 25,
      }),
    )
  })
})

describe('AuditView — Ed25519 verification key', () => {
  it('reveals the checkpoint public key on demand', async () => {
    wrap(<AuditView />)
    await userEvent.click(
      await screen.findByRole('button', { name: /verification key/i }),
    )
    expect(await screen.findByText('ed25519')).toBeInTheDocument()
    expect(api.pubkey).toHaveBeenCalledTimes(1)
  })
})

describe('AuditView — system ledger scope (superadmin)', () => {
  it('hides the scope toggle from non-superadmins', async () => {
    wrap(<AuditView />)
    await screen.findByRole('button', { name: /verify chain/i })
    expect(screen.queryByRole('combobox', { name: /ledger scope/i })).toBeNull()
  })

  it('lets a superadmin read the system chain (list-only, no verify/export)', async () => {
    auth.isSuperadmin = true
    api.systemList.mockResolvedValue({
      items: [{ ...ev1, action: 'user.create' }],
      has_more: false,
    })
    wrap(<AuditView />)

    await userEvent.click(
      screen.getByRole('combobox', { name: /ledger scope/i }),
    )
    await userEvent.click(
      await screen.findByRole('option', { name: /system ledger/i }),
    )

    expect(await screen.findByText('user.create')).toBeInTheDocument()
    await waitFor(() => expect(api.systemList).toHaveBeenCalled())
    // The system chain is read-only here: verify/export controls are gone…
    expect(screen.queryByRole('button', { name: /verify chain/i })).toBeNull()
    // …replaced by the honest caveat.
    expect(screen.getByText(/read-only here/i)).toBeInTheDocument()
  })
})

describe('AuditEventSheet — evidence detail', () => {
  it('shows the event facts and the chain-link fingerprints', () => {
    wrap(<AuditEventSheet event={ev1} open onOpenChange={() => {}} />)
    expect(screen.getByText('Event #12')).toBeInTheDocument()
    expect(screen.getByText('Chain integrity')).toBeInTheDocument()
    expect(screen.getByText('Previous hash')).toBeInTheDocument()
    expect(screen.getByText('Event hash')).toBeInTheDocument()
    // A signed event shows its PER-EVENT signature row + badge. The label used to
    // read "Signed checkpoint", which is a different artefact entirely: the engine
    // separates the two signing domains on purpose (core/audit/eventsig.go —
    // `eventDomain` is "distinct from checkpointDomain by design"), and on the same
    // virgin install Security > Forensics correctly says "No checkpoint written
    // yet". One screen contradicted the other about the same ledger.
    expect(screen.getByText(/Signed event/)).toBeInTheDocument()
    // Regression: this badge must never claim a checkpoint again.
    expect(screen.queryByText(/checkpoint/i)).not.toBeInTheDocument()
    // prev_hash fingerprint is rendered truncated (head…tail).
    expect(screen.getByText(/aaaaaaaa…aaaaaa/)).toBeInTheDocument()
  })
})

describe('AuditView refuses an impossible ledger bound', () => {
  // The third copy of the same defect, in the view the rest of this wave copies
  // from: its own sanitiser accepted 2026-02-30 and queried the 2nd of March.
  // The shared parser existed by then; this view was simply never pointed at it.
  it('does not query the 2nd of March for the 30th of February', async () => {
    window.history.replaceState(null, '', '/audit?since=2026-02-30')
    wrap(<AuditView />)

    await waitFor(() => expect(api.list).toHaveBeenCalled())
    expect(api.list.mock.calls.at(-1)?.[0].since).toBeUndefined()
  })

  it('sends the minute its control shows, not the nanoseconds the URL carried', async () => {
    window.history.replaceState(null, '', '/audit?since=2026-07-24T12%3A30%3A45.123456789Z')
    wrap(<AuditView />)

    await waitFor(() => expect(api.list).toHaveBeenCalled())
    // The control renders minutes; preserving nanosecond precision made two
    // visually identical links query different windows.
    expect(api.list.mock.calls.at(-1)?.[0].since).toBe(
      '2026-07-24T12:30:00.000Z',
    )
  })
})

describe('P3 — la columna de tiempo tiene ancho propio', () => {
  /**
   * ⛔ EL DEFECTO, medido en Chrome con datos reales sobre el `dist` commiteado: la
   * columna WHEN se quedaba en **78 px** y «6 minutes ago» salia en **TRES lineas**.
   *
   * ⚠ Y lo que NO era la cura, porque lo medi antes de creerlo: `whitespace-nowrap`
   * a solas no arregla nada —a 78 px el texto sigue sin caber— y ninguna politica de
   * ruptura tampoco. Era ANCHURA. `data-table.tsx:642-645` ya aplicaba
   * `width: header.getSize()` cuando difiere del 150 por defecto; lo que faltaba era
   * que la columna PIDIERA su tamaño.
   *
   * Este caso mira el `<th>` renderizado, no la fuente: jsdom no compone layout, pero
   * la anchura declarada si llega al DOM y es lo que el navegador respeta.
   *
   * EL MUTANTE: quitar `size: 120` de la columna `occurred_at`. Falla aqui.
   */
  it('la cabecera WHEN declara una anchura suficiente para una hora relativa', async () => {
    api.list.mockResolvedValue({ items: [ev1], has_more: false })
    wrap(<AuditView />)
    const th = await screen.findByRole('columnheader', { name: /when/i })
    const ancho = Number.parseFloat((th as HTMLElement).style.width)
    expect(
      ancho,
      'la columna de tiempo no pide anchura: volveria a los 78 px en los que «6 minutes ago» ocupa tres lineas',
    ).toBeGreaterThanOrEqual(110)
  })
})
