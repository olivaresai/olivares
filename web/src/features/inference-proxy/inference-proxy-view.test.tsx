// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, authState, assur } = vi.hoisted(() => ({
  api: {
    getConfig: vi.fn(),
    putConfig: vi.fn(),
    listDLPRules: vi.fn(),
    putDLPRule: vi.fn(),
    deleteDLPRule: vi.fn(),
    approveDevice: vi.fn(),
  },
  authState: { can: (_p: string): boolean => true, activeTenant: 't1' as string | null },
  assur: { aal: 3 },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  useAssurance: () => ({ aal: assur.aal, amr: [] }),
  StepUpPanel: () => <div>step-up-required</div>,
}))
vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  inferenceProxyApi: api,
}))

import { InferenceProxyView } from './inference-proxy-view'

const config = {
  fail_open: false,
  record_mandatory: true,
  response_dlp_mode: 'off' as const,
  gate_model_access: true,
  gate_budget: false,
  gate_residency: false,
  gate_context_window: false,
  gate_dlp_request: false,
  gate_dlp_response: false,
  ceilings_enforce: false,
}

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = () => true
  authState.activeTenant = 't1'
  assur.aal = 3
  api.getConfig.mockResolvedValue({ ...config })
  api.putConfig.mockResolvedValue({ ...config })
  api.listDLPRules.mockResolvedValue({
    items: [{ id: 'r1', class: 'pii', action: 'deny', note: 'sensitive' }],
    has_more: false,
  })
  api.putDLPRule.mockResolvedValue({ id: 'r2', class: 'secret', action: 'deny' })
  api.deleteDLPRule.mockResolvedValue(undefined)
  api.approveDevice.mockResolvedValue({
    id: 'd1',
    user_code: 'ABCD',
    state: 'approved',
  })
})

describe('el aviso de recorte de reglas DLP', () => {
  // ⛔ QUE FIJA ESTE PAR, y por que son DOS. Con `has_more: true` la pantalla tiene que DECIRLO;
  //    sin el, tiene que callarse. Un solo caso —el positivo— lo pasaria un aviso pintado SIEMPRE,
  //    que es peor que no tenerlo: convierte una senal en decoracion y ensena al operador a
  //    ignorarla. El par distingue «avisa» de «avisa cuando toca».
  it('con `has_more` lo dice, y con la cuenta CARGADA', async () => {
    api.listDLPRules.mockResolvedValue({
      items: [
        { id: 'r1', class: 'pii', action: 'deny', note: '' },
        { id: 'r2', class: 'secret', action: 'allow', note: '' },
      ],
      has_more: true,
    })
    wrap(<InferenceProxyView />)
    // La cifra que se ensena es la que hay DELANTE (2), no el techo: «se muestran 1000» seria un
    // numero de configuracion que no ayuda a decidir si mirar mas.
    expect(await screen.findByText(/2/)).toBeVisible()
    expect(
      await screen.findByText(/there are more/i),
      'con has_more la pantalla tiene que declarar el recorte',
    ).toBeVisible()
  })

  it('sin `has_more` NO dice nada', async () => {
    api.listDLPRules.mockResolvedValue({
      items: [{ id: 'r1', class: 'pii', action: 'deny', note: '' }],
      has_more: false,
    })
    wrap(<InferenceProxyView />)
    await screen.findByText('pii')
    expect(
      screen.queryByText(/there are more/i),
      'un aviso que sale siempre no es una senal: es decoracion',
    ).not.toBeInTheDocument()
  })
})

describe('InferenceProxyView', () => {
  it('forbids without the config read permission', () => {
    authState.can = () => false
    wrap(<InferenceProxyView />)
    expect(api.getConfig).not.toHaveBeenCalled()
  })

  it('saves config with pointer defaults filled and the toggled gate flipped', async () => {
    const user = userEvent.setup()
    wrap(<InferenceProxyView />)

    await screen.findByText('Gates & ceilings')
    await user.click(await screen.findByRole('switch', { name: 'Budget' }))
    await user.click(
      await screen.findByRole('button', { name: /save configuration/i }),
    )

    await waitFor(() =>
      expect(api.putConfig).toHaveBeenCalledWith(
        expect.objectContaining({
          gate_budget: true, // the flip
          gate_model_access: true, // pointer default preserved
          ceilings_enforce: false,
          fail_open: false,
        }),
      ),
    )
    // AND record_mandatory IS ABSENT, which this assertion used to require the
    // opposite of. The server reads that field's PRESENCE as an explicit evidence
    // choice and stores it in a separate provenance column, so re-sending the value
    // we had just read recorded a decision the operator never made — and a tenant
    // marked as having chosen no longer yields to the audit spool's declared
    // `degrade`. Flipping a budget gate is not a statement about evidence.
    const [sent] = api.putConfig.mock.calls.at(-1)!
    expect(sent).not.toHaveProperty('record_mandatory')
  })

  it('sends record_mandatory only when the operator actually toggles it', async () => {
    const user = userEvent.setup()
    wrap(<InferenceProxyView />)
    await screen.findByText('Gates & ceilings')
    await user.click(await screen.findByRole('switch', { name: 'Require recording' }))
    await user.click(
      await screen.findByRole('button', { name: /save configuration/i }),
    )
    await waitFor(() => expect(api.putConfig).toHaveBeenCalled())
    const [sent] = api.putConfig.mock.calls.at(-1)!
    // The switch was on (the fixture says so), so touching it turns evidence OFF —
    // and THAT is a decision, which must travel and must be recorded as one.
    expect(sent).toHaveProperty('record_mandatory', false)
  })

  it('Reset cancels the evidence choice, not just the draft', async () => {
    const user = userEvent.setup()
    wrap(<InferenceProxyView />)
    await screen.findByText('Gates & ceilings')
    // Touch the evidence switch, then change your mind and Reset.
    await user.click(await screen.findByRole('switch', { name: 'Require recording' }))
    await user.click(await screen.findByRole('button', { name: /^reset$/i }))
    // Now edit something unrelated and save.
    await user.click(await screen.findByRole('switch', { name: 'Budget' }))
    await user.click(
      await screen.findByRole('button', { name: /save configuration/i }),
    )
    await waitFor(() => expect(api.putConfig).toHaveBeenCalled())
    const [sent] = api.putConfig.mock.calls.at(-1)!
    // Reset cleared the draft but left the intent flag set, so the save recorded the
    // very evidence choice Reset was pressed to cancel.
    expect(sent).not.toHaveProperty('record_mandatory')
  })

  it('abandons the edit when the active tenant changes', async () => {
    const user = userEvent.setup()
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    // A FRESH element each time, same client and same component type. Passing the
    // identical element object back to rerender makes React bail out of the subtree,
    // and the test would then pass or fail for a reason that has nothing to do with
    // the tenant.
    const tree = () => (
      <QueryClientProvider client={qc}>
        <InferenceProxyView />
      </QueryClientProvider>
    )
    const { rerender } = render(tree())
    await screen.findByText('Gates & ceilings')
    await user.click(await screen.findByRole('switch', { name: 'Require recording' }))
    expect(screen.getByRole('button', { name: /^reset$/i })).toBeEnabled()

    // The tenant switcher only calls setActiveTenant; nothing unmounts this section,
    // and the HTTP client stamps the header from whatever tenant is active when the
    // request is built. An edit carried across would be PUT, whole, against the wrong
    // tenant — including an evidence choice made for a different one. Same client and
    // same element type on purpose: a remount would pass this test for the wrong reason.
    authState.activeTenant = 't2'
    rerender(tree())

    // The section re-queries under the new key, so wait for it to come back before
    // asking anything: with no draft and no data there is no action row at all, and a
    // missing button is not the same statement as a disabled one.
    const reset = await screen.findByRole('button', { name: /^reset$/i })
    expect(reset).toBeDisabled() // nothing is dirty: the draft did not travel
    expect(
      screen.getByRole('switch', { name: 'Require recording' }),
    ).toBeChecked() // and the unsaved toggle did not either — this is t2's own value
  })

  it('shows recording as ON when the response omits the field', async () => {
    const { record_mandatory: _omitted, ...withoutMandatory } = config
    api.getConfig.mockResolvedValue(withoutMandatory)
    wrap(<InferenceProxyView />)
    await screen.findByText('Gates & ceilings')
    // The server's default for a tenant with no row is MANDATORY. Normalising an
    // absent field to `false` showed the operator the opposite of the posture in
    // force, and a later save would have written that misreading back.
    expect(
      await screen.findByRole('switch', { name: 'Require recording' }),
    ).toBeChecked()
  })

  it('refuses all six write intents below AAL3 and exposes them after step-up', async () => {
    const user = userEvent.setup()
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const tree = () => (
      <QueryClientProvider client={qc}>
        <InferenceProxyView />
      </QueryClientProvider>
    )
    assur.aal = 1
    const { rerender } = render(tree())

    // Config save/reset, DLP create/edit/delete, and device approve/deny are all
    // absent at AAL1. In particular, no API write can be fired by a control that
    // remains behind the three StepUpPanel mount points.
    await waitFor(() =>
      expect(screen.getAllByText('step-up-required')).toHaveLength(3),
    )
    expect(
      screen.queryByRole('button', { name: /save configuration/i }),
    ).toBeNull()
    expect(
      screen.queryByRole('button', { name: /add rule/i }),
    ).toBeNull()
    expect(
      screen.queryByRole('button', { name: /edit rule for pii/i }),
    ).toBeNull()
    expect(
      screen.queryByRole('button', { name: /delete rule for pii/i }),
    ).toBeNull()
    expect(screen.queryByLabelText(/user code/i)).toBeNull()
    expect(api.putConfig).not.toHaveBeenCalled()
    expect(api.putDLPRule).not.toHaveBeenCalled()
    expect(api.deleteDLPRule).not.toHaveBeenCalled()
    expect(api.approveDevice).not.toHaveBeenCalled()

    // The assurance store changes when StepUpPanel completes its WebAuthn/PIV
    // ceremony. Re-render the same mounted view/client and exercise every write.
    assur.aal = 3
    rerender(tree())
    await user.click(await screen.findByRole('switch', { name: 'Budget' }))
    await user.click(
      await screen.findByRole('button', { name: /save configuration/i }),
    )
    await waitFor(() => expect(api.putConfig).toHaveBeenCalledTimes(1))

    await user.click(await screen.findByRole('button', { name: /add rule/i }))
    let dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText(/classification/i), 'secret')
    await user.click(within(dialog).getByRole('button', { name: /add rule/i }))
    await waitFor(() => expect(api.putDLPRule).toHaveBeenCalledTimes(1))

    await user.click(
      await screen.findByRole('button', { name: /edit rule for pii/i }),
    )
    dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: /save rule/i }))
    await waitFor(() => expect(api.putDLPRule).toHaveBeenCalledTimes(2))

    await user.click(
      await screen.findByRole('button', { name: /delete rule for pii/i }),
    )
    dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: /delete rule/i }))
    await waitFor(() => expect(api.deleteDLPRule).toHaveBeenCalledTimes(1))

    let code = await screen.findByLabelText(/user code/i)
    await user.type(code, 'ABCD')
    await user.click(screen.getByRole('button', { name: /^approve$/i }))
    await waitFor(() => expect(api.approveDevice).toHaveBeenCalledTimes(1))
    code = await screen.findByLabelText(/user code/i)
    await user.type(code, 'EFGH')
    await user.click(screen.getByRole('button', { name: /^deny$/i }))
    await waitFor(() => expect(api.approveDevice).toHaveBeenCalledTimes(2))
  })

  it('creates a DLP rule (upsert by class)', async () => {
    const user = userEvent.setup()
    wrap(<InferenceProxyView />)

    await user.click(await screen.findByRole('button', { name: /add rule/i }))
    const dialog = await screen.findByRole('dialog')
    await user.type(
      within(dialog).getByLabelText(/classification/i),
      'secret',
    )
    await user.click(within(dialog).getByRole('button', { name: /add rule/i }))

    await waitFor(() =>
      expect(api.putDLPRule).toHaveBeenCalledWith({
        class: 'secret',
        action: 'deny',
        note: undefined,
      }),
    )
  })

  it('deletes a DLP rule after a confirm', async () => {
    const user = userEvent.setup()
    wrap(<InferenceProxyView />)

    await user.click(
      await screen.findByRole('button', { name: /delete rule for pii/i }),
    )
    const confirm = await screen.findByRole('dialog')
    expect(api.deleteDLPRule).not.toHaveBeenCalled()
    await user.click(
      within(confirm).getByRole('button', { name: /delete rule/i }),
    )
    await waitFor(() => expect(api.deleteDLPRule).toHaveBeenCalledWith('r1'))
  })

  it('approves and denies a device code', async () => {
    const user = userEvent.setup()
    wrap(<InferenceProxyView />)

    const codeInput = await screen.findByLabelText(/user code/i)
    await user.type(codeInput, 'ABCD')
    await user.click(screen.getByRole('button', { name: /^approve$/i }))
    await waitFor(() =>
      expect(api.approveDevice).toHaveBeenCalledWith({
        user_code: 'ABCD',
        deny: false,
      }),
    )

    await user.type(await screen.findByLabelText(/user code/i), 'EFGH')
    await user.click(screen.getByRole('button', { name: /^deny$/i }))
    await waitFor(() =>
      expect(api.approveDevice).toHaveBeenCalledWith({
        user_code: 'EFGH',
        deny: true,
      }),
    )
  })
})
