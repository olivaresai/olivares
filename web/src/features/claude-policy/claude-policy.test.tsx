// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

//the view now reflects the active tab in the URL (`?tab=`) so the access map can
// link straight at policy-as-code. That makes useNavigate a render-time dependency, and
// this suite mounts the view without a router — every tab click was raising an unhandled
// "Cannot read properties of null (reading 'navigate')" while the assertions still
// passed. Stubbing it here keeps the run free of errors that mask real ones.
vi.mock('@tanstack/react-router', () => ({ useNavigate: () => vi.fn() }))

const authState = vi.hoisted(() => ({
  activeTenant: 't1' as string | null,
  can: (_p: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({
  listDrift: vi.fn(),
  getFinding: vi.fn(),
  dryRun: vi.fn(),
  publish: vi.fn(),
  listVersions: vi.fn(),
  getVersion: vi.fn(),
  getDistribution: vi.fn(),
  pdpValidate: vi.fn(),
  pdpExplain: vi.fn(),
  pdpDryRun: vi.fn(),
  pdpVersions: vi.fn(),
  pdpGetVersion: vi.fn(),
  pdpActive: vi.fn(),
  pdpPublish: vi.fn(),
  pdpRollback: vi.fn(),
  pdpTestStatus: vi.fn(),
  listManagedAgentHitl: vi.fn(),
  threadEvents: vi.fn(),
  confirmTool: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, claudePolicyApi: api }
})

import ClaudePolicyView from './claude-policy-view'
import './i18n'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = () => true
  api.listDrift.mockResolvedValue({ items: [], has_more: false })
  api.listVersions.mockResolvedValue({ items: [], has_more: false })
  api.getDistribution.mockResolvedValue({
    surface: 'managed-settings',
    scopes: [],
    notes: [],
  })
  api.listManagedAgentHitl.mockResolvedValue({ items: [], has_more: false })
  api.threadEvents.mockResolvedValue({ items: [], has_more: false })
  api.pdpVersions.mockResolvedValue({ items: [], has_more: false })
  api.pdpActive.mockResolvedValue({
    engine: 'cedar',
    authored: { present: false },
    managed: { present: false },
    adopted: { present: false },
  })
  api.pdpTestStatus.mockResolvedValue({
    engine: 'cedar',
    available: false,
    reason: 'no stored test artifact is available for any cedar revision',
  })
})

async function gotoTab(name: RegExp) {
  await userEvent.click(screen.getByRole('tab', { name }))
}

describe('ClaudePolicyView', () => {
  it('opens on the drift tab and reads the REAL findings endpoint (self-audited)', async () => {
    wrap(<ClaudePolicyView />)
    await waitFor(() => expect(api.listDrift).toHaveBeenCalled())
    expect(await screen.findByText(/No drift findings/i)).toBeInTheDocument()
  })

  /**
   * ⛔ EN ESTA PANTALLA LA AUSENCIA ES LA AFIRMACIÓN. La celda de arriba comprueba que sin
   * hallazgos se dice «No drift findings»; ésta comprueba lo otro: que cuando el motor avisa de
   * que hay MÁS de los que caben, la pantalla lo dice. Sin esto, un recorte silencioso convierte
   * «no caben» en «no hay», que es exactamente el error que este módulo existe para no cometer —
   * y que la celda «an uncomputed drift is an honest unknown» ya vigila por el otro lado.
   *
   * La cadena va EXACTA: un `/\d+/` no distingue el número de filas cargadas del techo pedido.
   */
  it('avisa cuando el motor dice que hay MÁS deriva de la que cabe', async () => {
    // ⛔ MIL FILAS, NO CERO. La primera versión de esta celda usaba `items: []` con
    //    `has_more: true`, y el contraste demostró que ese estado el motor NO lo produce: el store
    //    pide `limit+1` y sólo enciende `HasMore` cuando recorta, así que con el techo en 1000 un
    //    `has_more:true` implica exactamente 1000 filas. Un caso que fija un estado imposible
    //    prueba el componente, no el contrato.
    const muchas = Array.from({ length: 1000 }, (_, i) => ({
      id: `f-${i}`,
      subject_kind: 'claude_policy',
      subject_ref: `s-${i}`,
      kind: 'policy_drift',
      severity: 'medium',
      status: 'open',
    }))
    api.listDrift.mockResolvedValue({ items: muchas, has_more: true })
    wrap(<ClaudePolicyView />)
    expect(
      await screen.findByText('Loaded 1000 drift findings; there are more'),
    ).toBeInTheDocument()
  })

  it('CONTRAFACTUAL · sin recorte no hay aviso', async () => {
    api.listDrift.mockResolvedValue({ items: [], has_more: false })
    wrap(<ClaudePolicyView />)
    // Cota: la pestaña SÍ pintó (si no, la ausencia del aviso no diría nada).
    expect(await screen.findByText(/No drift findings/i)).toBeInTheDocument()
    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })

  it('managed-settings guided form exposes verified managed-only keys', async () => {
    wrap(<ClaudePolicyView />)
    await gotoTab(/Managed settings/i)
    expect(await screen.findByText('allowManagedHooksOnly')).toBeInTheDocument()
    // Verified correction: spec's unverified keys must NOT appear.
    expect(screen.queryByText('forceLoginMethod')).not.toBeInTheDocument()
    expect(screen.getAllByText(/managed-only/i).length).toBeGreaterThan(0)
  })

  it('hooks: enforcement OFF by default and applyPermissionRule NOT in PreToolUse', async () => {
    wrap(<ClaudePolicyView />)
    await gotoTab(/Hooks/i)
    expect(await screen.findByText(/OFF by default/i)).toBeInTheDocument()
    expect(
      screen.getByText(/applyPermissionRule is NOT available here/i),
    ).toBeInTheDocument()
  })

  it('sandbox: shows the egress allowlist and the domain-fronting caveat', async () => {
    wrap(<ClaudePolicyView />)
    await gotoTab(/Sandbox & egress/i)
    expect(await screen.findByText('api.anthropic.com')).toBeInTheDocument()
    expect(
      screen.getByText(/does NOT terminate or inspect TLS/i),
    ).toBeInTheDocument()
  })

  it('publish is a confirmed, audited action that surfaces drift verification', async () => {
    api.publish.mockResolvedValue({
      surface: 'managed-settings',
      revision: 1,
      drift: [
        {
          id: 'f1',
          kind: 'policy_drift',
          severity: 'high',
          status: 'open',
          title: 'Observed config diverges',
        },
      ],
    })
    wrap(<ClaudePolicyView />)
    await gotoTab(/Managed settings/i)
    await userEvent.click(
      await screen.findByRole('button', { name: /^Publish$/i }),
    )
    // ConfirmDialog (privileged + audit notice) appears.
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getAllByText(/audit ledger/i).length).toBeGreaterThan(
      0,
    )
    await userEvent.click(
      within(dialog).getByRole('button', { name: /^Publish$/i }),
    )
    await waitFor(() =>
      expect(api.publish).toHaveBeenCalledWith(
        'managed-settings',
        expect.any(String),
      ),
    )
    expect(
      await screen.findByText('Observed config diverges'),
    ).toBeInTheDocument()
    expect(toast.success).toHaveBeenCalled()
  })

  it('an uncomputed drift is an honest unknown, NEVER rendered as "no drift"', async () => {
    api.publish.mockResolvedValue({
      surface: 'managed-settings',
      revision: 1,
      drift: [],
      drift_computed: false,
      distribution: 'enqueue-failed',
      notes: ['drift not computed: no observed host config available'],
    })
    wrap(<ClaudePolicyView />)
    await gotoTab(/Managed settings/i)
    await userEvent.click(
      await screen.findByRole('button', { name: /^Publish$/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(
      within(dialog).getByRole('button', { name: /^Publish$/i }),
    )
    expect(
      await screen.findByText(/Drift was NOT computed/i),
    ).toBeInTheDocument()
    expect(screen.queryByText(/^No drift:/i)).not.toBeInTheDocument()
    // The deny-closed distribution failure is surfaced, never softened.
    expect(screen.getByText(/Distribution FAILED/i)).toBeInTheDocument()
  })

  it('the distribution tab shows the per-scope truth (verified vs unverified)', async () => {
    api.getDistribution.mockResolvedValue({
      surface: 'managed-settings',
      latest_revision: 2,
      artifact: {
        revision: 2,
        artifact_sha256: 'ab12',
        key_fingerprint: 'deadbeefdeadbeef',
      },
      scopes: [
        {
          scope: 'host-a',
          reported_revision: 2,
          verified: true,
          current: true,
          content_reported: true,
          drift_count: 0,
          open_findings: 0,
        },
        {
          scope: 'host-b',
          reported_revision: 1,
          verified: false,
          current: false,
          content_reported: false,
          drift_count: 3,
          open_findings: 3,
        },
      ],
      notes: [],
    })
    wrap(<ClaudePolicyView />)
    await gotoTab(/Managed settings/i)
    await userEvent.click(screen.getByRole('tab', { name: /Distribution/i }))
    expect(await screen.findByText('host-a')).toBeInTheDocument()
    const hostB = (await screen.findByText('host-b')).closest('li')!
    expect(within(hostB).getByText(/UNVERIFIED/)).toBeInTheDocument()
    expect(within(hostB).getByText(/3 open finding/i)).toBeInTheDocument()
    expect(within(hostB).getByText(/content UNKNOWN/i)).toBeInTheDocument()
    expect(within(hostB).getByText(/stale/i)).toBeInTheDocument()
  })

  it('a not-yet-implemented declared endpoint shows the honest pending seam, not a red error', async () => {
    api.dryRun.mockRejectedValue(new ApiError(404, 'not_found', 'no route'))
    wrap(<ClaudePolicyView />)
    await gotoTab(/Managed settings/i)
    await userEvent.click(
      await screen.findByRole('button', { name: /Dry-run/i }),
    )
    expect(await screen.findByText(/Backend pending/i)).toBeInTheDocument()
    expect(toast.error).not.toHaveBeenCalled()
  })

  it('reflects RBAC: without admin, Publish is hidden (backend stays authoritative)', async () => {
    authState.can = (p: string) => !p.endsWith(':admin')
    wrap(<ClaudePolicyView />)
    await gotoTab(/Managed settings/i)
    // Dry-run (read) is visible; Publish (admin) is not.
    expect(
      await screen.findByRole('button', { name: /Dry-run/i }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /^Publish$/i }),
    ).not.toBeInTheDocument()
  })

  it('policy-as-code states the REAL three-valued Cedar model, not the stale deny-overlay', async () => {
    wrap(<ClaudePolicyView />)
    await gotoTab(/Policy-as-code/i)
    // The routes are registered and live — no "post-v1 candidate" seam.
    expect(await screen.findByText(/Live API/i)).toBeInTheDocument()
    expect(screen.queryByText(/post-v1 candidate/i)).not.toBeInTheDocument()
    // A permit GRANTS beyond RBAC; the old "can only NARROW" copy is gone.
    expect(
      screen.getByText(
        /a permit GRANTS the request within its resolved scope tree/i,
      ),
    ).toBeInTheDocument()
    expect(
      screen.queryByText(/can only NARROW an existing RBAC grant/i),
    ).not.toBeInTheDocument()
    // The bounds that ARE true are still stated.
    expect(
      screen.getByText(
        /degrades to abstain instead of continuing to authorize/i,
      ),
    ).toBeInTheDocument()
  })

  it('managed-agent HITL reads requires_action findings and flags the vault lateral risk', async () => {
    wrap(<ClaudePolicyView />)
    await gotoTab(/Managed-agent HITL/i)
    await waitFor(() => expect(api.listManagedAgentHitl).toHaveBeenCalled())
    expect(await screen.findByText(/workspace-scoped/i)).toBeInTheDocument()
    expect(screen.getByText(/No pending confirmations/i)).toBeInTheDocument()
  })

  it('managed-agent HITL: review surfaces the concrete tool and allow is confirmed + audited', async () => {
    api.listManagedAgentHitl.mockResolvedValue({
      items: [
        {
          id: 'h1',
          kind: 'governance',
          severity: 'info',
          status: 'open',
          subject_kind: 'anthropic.managed_agent',
          subject_ref: 'sess_123',
          title: 'Tool call awaiting confirmation',
          detail_hash: 'abc123def456',
        },
      ],
      has_more: false,
    })
    api.threadEvents.mockResolvedValue({
      items: [
        {
          id: 'ev1',
          type: 'agent.tool_use',
          tool_name: 'Bash',
          tool_use_id: 'tu1',
        },
      ],
      has_more: false,
    })
    api.confirmTool.mockResolvedValue(undefined)
    wrap(<ClaudePolicyView />)
    await gotoTab(/Managed-agent HITL/i)
    await userEvent.click(
      await screen.findByRole('button', { name: /Review/i }),
    )
    expect(await screen.findByText('Bash')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /^Allow$/i }))
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(
      within(dialog).getByRole('button', { name: /confirm|allow/i }),
    )
    await waitFor(() =>
      expect(api.confirmTool).toHaveBeenCalledWith('sess_123', {
        tool_use_id: 'tu1',
        result: 'allow',
      }),
    )
  })
})
