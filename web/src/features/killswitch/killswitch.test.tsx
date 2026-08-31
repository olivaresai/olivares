// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ApiError } from '@/lib/api/errors'
import { useStepUpStore } from '@/stores/step-up'
import {
  agentStopFixture,
  calmStateFixture,
  estateStopFixture,
  estateStoppedStateFixture,
  evidencePackFixture,
  guardianActionsFixture,
  guardianRulesFixture,
  reenabledUnreviewedFixture,
  reenablePendingFixture,
  reviewedStopFixture,
} from './fixtures'

// --- mocks -------------------------------------------------------------------

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

const authState = vi.hoisted(() => ({
  activeTenant: 't1' as string | null,
  can: (_p: string): boolean => true,
  principal: null as { actor: string; kind: string } | null,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

// The reenable dialog links to the approvals view; render the router Link as a
// plain anchor so no RouterProvider is needed (home.test.tsx pattern).
vi.mock('@tanstack/react-router', () => ({
  //useUrlState follows the location, so the mock has to answer it.
  useRouterState: () => '',
  Link: ({
    children,
    to,
    className,
  }: {
    children: ReactNode
    to: string
    className?: string
  }) => (
    <a href={to} className={className}>
      {children}
    </a>
  ),
}))

const api = vi.hoisted(() => ({
  list: vi.fn(),
  state: vi.fn(),
  get: vi.fn(),
  engage: vi.fn(),
  reenable: vi.fn(),
  review: vi.fn(),
  evidence: vi.fn(),
  listGuardianRules: vi.fn(),
  createGuardianRule: vi.fn(),
  updateGuardianRule: vi.fn(),
  deleteGuardianRule: vi.fn(),
  listGuardianActions: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, killswitchApi: api }
})

import KillswitchView from './killswitch-view'
import { ReenableDialog } from './reenable-dialog'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  authState.can = () => true
  authState.principal = null
  for (const fn of Object.values(api)) fn.mockReset()
  toast.success.mockReset()
  toast.error.mockReset()
  toast.warning.mockReset()
  // Default resolutions so background queries never reject.
  api.state.mockResolvedValue(calmStateFixture)
  api.list.mockResolvedValue({ items: [], has_more: false })
  api.listGuardianRules.mockResolvedValue({ items: [], has_more: false })
  api.listGuardianActions.mockResolvedValue({ items: [], has_more: false })
})
afterEach(() => vi.clearAllMocks())

describe('KillswitchView — engage (the one-click emergency stop)', () => {
  it('la ceremonia del RE-ENABLE es alcanzable, y no sólo está escrita', async () => {
    // ⛔ NINGUNA GUARDA TEXTUAL PRUEBA ALCANZABILIDAD. El contraste `sol max` mutó la rama de
    // esta pantalla a `err.isStepUpRequired && false` —código que compila y que la guarda de
    // clase lee como correcto porque el token sigue delante— y las 22 celdas siguieron
    // verdes: nadie ejercía este camino. Sólo ejecutarlo lo demuestra.
    useStepUpStore.setState({ request: null })
    api.reenable.mockRejectedValue(
      new ApiError(403, 'step_up_required', 'assurance level too low'),
    )
    wrap(
      <ReenableDialog stop={estateStopFixture} open onOpenChange={() => {}} />,
    )

    await userEvent.click(
      await screen.findByRole('button', { name: /request re-enable/i }),
    )

    await waitFor(() =>
      expect(useStepUpStore.getState().request).not.toBeNull(),
    )
    expect(toast.warning).not.toHaveBeenCalled()
    // Y reanuda: el re-enable se reintenta con la ceremonia, no se pierde.
    const { retry } = useStepUpStore.getState().request ?? {}
    expect(retry).toBeTypeOf('function')
    api.reenable.mockResolvedValue({ status: 'pending' })
    retry?.()
    await waitFor(() => expect(api.reenable).toHaveBeenCalledTimes(2))
  })

  it('un step_up_required en la PARADA abre la ceremonia y no acusa al operador', async () => {
    // ⛔ La escritura más seria de la consola: parar la flota. Ramificaba por `isForbidden`,
    // que es SÓLO el status 403 (lib/api/errors.ts:59), y un `step_up_required` lo satisface
    // también — así que en una emergencia el operador leía «no tienes autorización» (falso:
    // tiene el permiso) en vez de la ceremonia de un toque que levanta la negativa.
    useStepUpStore.setState({ request: null })
    api.engage.mockRejectedValue(
      new ApiError(403, 'step_up_required', 'assurance level too low'),
    )
    wrap(<KillswitchView />)

    await userEvent.type(
      screen.getByLabelText(/^reason/i),
      'Prompt-injection cascade',
    )
    await userEvent.click(
      await screen.findByRole('button', { name: /emergency stop/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(
      within(dialog).getByRole('button', { name: /engage kill switch/i }),
    )

    // Ancla POSITIVA: la ceremonia se pidió.
    await waitFor(() =>
      expect(useStepUpStore.getState().request).not.toBeNull(),
    )
    // Y la acusación NO acompaña.
    expect(toast.warning).not.toHaveBeenCalled()

    // ⛔ Y LA CEREMONIA REANUDA LA PARADA, que es lo que la copy PROMETE («the action
    // resumes»). Mi celda anterior sólo comprobaba que `request` no era null: el contraste
    // `sol max` midió que ninguna de las cuatro operaciones se reenviaba tras elevar —el
    // host ejecuta `retry?.()` y el retry estaba vacío—, así que el operador hacía la
    // ceremonia entera y la flota seguía sin pararse. Aquí se EJERCE el reintento.
    const { retry } = useStepUpStore.getState().request ?? {}
    expect(retry).toBeTypeOf('function')
    api.engage.mockResolvedValue(estateStopFixture)
    retry?.()
    await waitFor(() => expect(api.engage).toHaveBeenCalledTimes(2))
    // Con las MISMAS variables: la razón tecleada no se pierde por el camino.
    expect(api.engage.mock.calls[1]).toEqual(api.engage.mock.calls[0])
  })

  it('y un 403 SIN código de ceremonia conserva la negativa de ROL', async () => {
    // Control negativo: sin código, la negativa es de rol, es cierta, y se queda. Sin esta
    // celda, mandar los DOS 403 a la ceremonia también pasaría — el defecto simétrico.
    useStepUpStore.setState({ request: null })
    api.engage.mockRejectedValue(new ApiError(403, 'forbidden', 'no'))
    wrap(<KillswitchView />)

    await userEvent.type(
      screen.getByLabelText(/^reason/i),
      'Prompt-injection cascade',
    )
    await userEvent.click(
      await screen.findByRole('button', { name: /emergency stop/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(
      within(dialog).getByRole('button', { name: /engage kill switch/i }),
    )

    await waitFor(() => expect(toast.warning).toHaveBeenCalledTimes(1))
    expect(useStepUpStore.getState().request).toBeNull()
  })

  it('gates engage on a MANDATORY reason and a danger confirm step (estate scope)', async () => {
    api.engage.mockResolvedValue(estateStopFixture)
    wrap(<KillswitchView />)

    const engageBtn = await screen.findByRole('button', {
      name: /emergency stop/i,
    })
    // No reason yet → the engine would 400; the button never offers the call.
    expect(engageBtn).toBeDisabled()

    await userEvent.type(
      screen.getByLabelText(/^reason/i),
      'Prompt-injection cascade',
    )
    expect(engageBtn).toBeEnabled()
    await userEvent.click(engageBtn)

    // The deliberate confirm step (no single-click estate stop).
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText(/stop the entire estate\?/i),
    ).toBeInTheDocument()
    await userEvent.click(
      within(dialog).getByRole('button', { name: /engage kill switch/i }),
    )

    await waitFor(() => expect(api.engage).toHaveBeenCalledTimes(1))
    // Estate-wide: scope_ref MUST be absent (the engine 400s a non-empty one).
    expect(api.engage.mock.calls[0][0]).toEqual({
      scope_kind: 'estate',
      reason: 'Prompt-injection cascade',
    })
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
  })

  it('agent scope additionally requires the agent ref and sends it as scope_ref', async () => {
    api.engage.mockResolvedValue(agentStopFixture)
    wrap(<KillswitchView />)

    await userEvent.type(screen.getByLabelText(/^reason/i), 'rogue agent')
    await userEvent.click(screen.getByRole('combobox', { name: /scope/i }))
    await userEvent.click(
      await screen.findByRole('option', { name: /single agent/i }),
    )

    const engageBtn = screen.getByRole('button', { name: /emergency stop/i })
    expect(engageBtn).toBeDisabled() // ref still missing
    await userEvent.type(screen.getByLabelText(/^agent/i), 'support-triage')
    expect(engageBtn).toBeEnabled()
    await userEvent.click(engageBtn)

    const dialog = await screen.findByRole('dialog')
    await userEvent.click(
      within(dialog).getByRole('button', { name: /engage kill switch/i }),
    )
    await waitFor(() => expect(api.engage).toHaveBeenCalledTimes(1))
    expect(api.engage.mock.calls[0][0]).toEqual({
      scope_kind: 'agent',
      scope_ref: 'support-triage',
      reason: 'rogue agent',
    })
  })

  it('a 409 surfaces the EXISTING stop instead of swallowing it', async () => {
    api.engage.mockRejectedValue(
      new ApiError(
        409,
        'conflict',
        'an active stop for this scope already exists (ks-estate-1); re-enable it under dual-control first',
      ),
    )
    wrap(<KillswitchView />)

    await userEvent.type(screen.getByLabelText(/^reason/i), 'second stop')
    await userEvent.click(
      screen.getByRole('button', { name: /emergency stop/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(
      within(dialog).getByRole('button', { name: /engage kill switch/i }),
    )

    await waitFor(() => expect(toast.warning).toHaveBeenCalled())
    // The engine's message NAMES the existing stop — relayed, not replaced.
    expect(toast.warning.mock.calls[0][1]?.description).toMatch(/ks-estate-1/)
    expect(toast.error).not.toHaveBeenCalled()
  })
})

describe('KillswitchView — live state + stop rows', () => {
  it('renders the estate-stopped banner and the stop row (who/when/scope/reason/AAL/revoked)', async () => {
    api.state.mockResolvedValue(estateStoppedStateFixture)
    api.list.mockResolvedValue({
      items: [estateStopFixture],
      has_more: false,
    })
    wrap(<KillswitchView />)

    expect(await screen.findByText('ESTATE STOPPED')).toBeInTheDocument()
    expect(
      screen.getByText(/3 pending actuation approvals revoked/i),
    ).toBeInTheDocument()
    // The row: engager handle, reason, source, recorded AAL, lifecycle chip.
    expect(screen.getAllByText('user:u-1').length).toBeGreaterThan(0)
    expect(
      screen.getAllByText(/prompt-injection cascade across the fleet/i).length,
    ).toBeGreaterThan(0)
    expect(screen.getByText('Operator')).toBeInTheDocument()
    expect(screen.getByText('AAL3')).toBeInTheDocument()
    expect(screen.getByText('Active')).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /re-enable/i }),
    ).toBeInTheDocument()
  })

  it('a guardian-engaged agent stop names its source and rule', async () => {
    api.state.mockResolvedValue({
      estate_stopped: false,
      active: [agentStopFixture],
    })
    api.list.mockResolvedValue({ items: [agentStopFixture], has_more: false })
    wrap(<KillswitchView />)

    expect(await screen.findByText('1 agent stop active')).toBeInTheDocument()
    expect(screen.getByText('Guardian')).toBeInTheDocument()
    expect(screen.getByText('gr-1')).toBeInTheDocument()
    // Guardian engages record no operator session — no AAL chip is fabricated.
    expect(screen.queryByText(/^AAL/)).toBeNull()
  })
})

describe('KillswitchView — dual-control re-enable', () => {
  it('202 pending: shows the approval id, the 2-human progress and the /permissions link', async () => {
    api.list.mockResolvedValue({ items: [estateStopFixture], has_more: false })
    api.state.mockResolvedValue(estateStoppedStateFixture)
    api.reenable.mockResolvedValue(reenablePendingFixture)
    wrap(<KillswitchView />)

    await userEvent.click(
      await screen.findByRole('button', { name: /re-enable/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(
      within(dialog).getByRole('button', { name: /request re-enable/i }),
    )

    await waitFor(() => expect(api.reenable).toHaveBeenCalledTimes(1))
    expect(api.reenable.mock.calls[0][0]).toBe('ks-estate-1')
    // The pending panel: approval identity, progress toward the two humans, and
    // the pointer to the approvals view where they decide.
    expect(
      await within(dialog).findByText(/awaiting dual-control approval/i),
    ).toBeInTheDocument()
    expect(within(dialog).getByText('apr-77')).toBeInTheDocument()
    expect(within(dialog).getByText('1 of 2 approvals')).toBeInTheDocument()
    const link = within(dialog).getByRole('link', {
      name: /open the approvals queue/i,
    })
    expect(link).toHaveAttribute('href', '/permissions')
    // Re-POST to complete is offered.
    expect(
      within(dialog).getByRole('button', { name: /check status/i }),
    ).toBeInTheDocument()
  })

  it('200 once approved: completes with the "post-review due" cue and closes', async () => {
    api.list.mockResolvedValue({ items: [estateStopFixture], has_more: false })
    api.reenable.mockResolvedValue({
      ...estateStopFixture,
      status: 'reenabled',
      reenabled_by: 'user:u-2',
    })
    wrap(<KillswitchView />)

    await userEvent.click(
      await screen.findByRole('button', { name: /re-enable/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(
      within(dialog).getByRole('button', { name: /request re-enable/i }),
    )

    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    expect(toast.success.mock.calls[0][0]).toMatch(
      /re-enabled under dual-control/i,
    )
    expect(toast.success.mock.calls[0][1]?.description).toMatch(
      /post-review is now due/i,
    )
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('a 409 dual_control_required is relayed calmly with the engine message', async () => {
    api.list.mockResolvedValue({ items: [estateStopFixture], has_more: false })
    api.reenable.mockRejectedValue(
      new ApiError(
        409,
        'dual_control_required',
        'dual-control floor: re-enable requires approval by at least 2 distinct humans',
      ),
    )
    wrap(<KillswitchView />)

    await userEvent.click(
      await screen.findByRole('button', { name: /re-enable/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(
      within(dialog).getByRole('button', { name: /request re-enable/i }),
    )

    await waitFor(() => expect(toast.warning).toHaveBeenCalled())
    expect(toast.warning.mock.calls[0][0]).toMatch(/dual-control required/i)
    expect(toast.warning.mock.calls[0][1]?.description).toMatch(
      /2 distinct humans/,
    )
    expect(toast.error).not.toHaveBeenCalled()
  })
})

describe('KillswitchView — forced post-review', () => {
  it('a re-enabled unreviewed stop shows "Post-review due" and the review dialog enforces the note', async () => {
    api.list.mockResolvedValue({
      items: [reenabledUnreviewedFixture],
      has_more: false,
    })
    api.review.mockResolvedValue({
      ...reenabledUnreviewedFixture,
      reviewed: true,
    })
    wrap(<KillswitchView />)

    expect(await screen.findByText('Post-review due')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /^review$/i }))

    const dialog = await screen.findByRole('dialog')
    const submit = within(dialog).getByRole('button', {
      name: /record review/i,
    })
    // The note is mandatory — nothing to submit without it.
    expect(submit).toBeDisabled()
    await userEvent.type(
      within(dialog).getByLabelText(/^note/i),
      'Stop justified; prompt hardened.',
    )
    expect(submit).toBeEnabled()
    await userEvent.click(submit)

    await waitFor(() => expect(api.review).toHaveBeenCalledTimes(1))
    expect(api.review.mock.calls[0][0]).toBe('ks-reen-1')
    expect(api.review.mock.calls[0][1]).toEqual({
      note: 'Stop justified; prompt hardened.',
    })
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
  })
})

describe('KillswitchView — evidence pack download', () => {
  /**
   * ⛔ UN PAQUETE ACOTADO SE DESCARGABA CON UN «hecho» LIMPIO. El motor acota la cronología y los
   * hallazgos cuando son muchos y lo declara en el propio JSON
   * (`modules/governance/killswitch_evidence.go:117-118`), pero quien no abre la cabecera del
   * fichero se lleva la cronología de un incidente **creyéndola completa** — y la completitud es
   * exactamente lo que se le exige a una evidencia.
   *
   * EL MUTANTE: el toast de éxito de siempre. Esta casilla lo mata porque exige el aviso.
   */
  it('un paquete ACOTADO se descarga con aviso, no con un «hecho» limpio', async () => {
    api.list.mockResolvedValue({
      items: [reviewedStopFixture],
      has_more: false,
    })
    api.evidence.mockResolvedValue({
      ...evidencePackFixture,
      timeline_truncated: true,
    })
    URL.createObjectURL = vi.fn(() => 'blob:mock') as never
    URL.revokeObjectURL = vi.fn() as never
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    wrap(<KillswitchView />)
    await userEvent.click(
      await screen.findByRole('button', { name: /download evidence pack/i }),
    )

    await waitFor(() => expect(toast.warning).toHaveBeenCalled())
    expect(toast.warning.mock.calls[0][0]).toMatch(/BOUNDED/i)
    expect(toast.success).not.toHaveBeenCalled()
  })

  /**
   * LA DIRECCIÓN QUE NO DEBE DISPARAR: un paquete COMPLETO sigue descargándose con el éxito de
   * siempre. Sin esta casilla, avisar en todos los paquetes convertiría la advertencia en ruido y
   * dejaría de leerse justo el día que el paquete sí viene recortado.
   */
  it('un paquete completo no avisa de truncado', async () => {
    api.list.mockResolvedValue({
      items: [reviewedStopFixture],
      has_more: false,
    })
    api.evidence.mockResolvedValue(evidencePackFixture)
    URL.createObjectURL = vi.fn(() => 'blob:mock') as never
    URL.revokeObjectURL = vi.fn() as never
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    wrap(<KillswitchView />)
    await userEvent.click(
      await screen.findByRole('button', { name: /download evidence pack/i }),
    )

    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    expect(toast.warning).not.toHaveBeenCalled()
  })

  it('fetches the JSON pack and triggers a blob download named killswitch-<id>-evidence.json', async () => {
    api.list.mockResolvedValue({
      items: [reviewedStopFixture],
      has_more: false,
    })
    api.evidence.mockResolvedValue(evidencePackFixture)

    const createObjectURL = vi.fn(() => 'blob:mock')
    const revokeObjectURL = vi.fn()
    URL.createObjectURL = createObjectURL as never
    URL.revokeObjectURL = revokeObjectURL as never
    let downloadedAs = ''
    const click = vi
      .spyOn(HTMLAnchorElement.prototype, 'click')
      .mockImplementation(function (this: HTMLAnchorElement) {
        downloadedAs = this.download
      })

    wrap(<KillswitchView />)
    await userEvent.click(
      await screen.findByRole('button', { name: /download evidence pack/i }),
    )

    await waitFor(() => expect(api.evidence).toHaveBeenCalledWith('ks-rev-1'))
    await waitFor(() => expect(click).toHaveBeenCalled())
    expect(downloadedAs).toBe('killswitch-ks-rev-1-evidence.json')
    expect(createObjectURL).toHaveBeenCalledTimes(1)
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:mock')
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    click.mockRestore()
  })
})

describe('KillswitchView — guardian rules + containment trail', () => {
  it('creates a rule through the dialog with the safe defaults (min_severity high, one human approval)', async () => {
    api.createGuardianRule.mockResolvedValue(guardianRulesFixture[0])
    wrap(<KillswitchView />)

    await userEvent.click(
      await screen.findByRole('button', { name: /new rule/i }),
    )
    const dialog = await screen.findByRole('dialog')
    const submit = within(dialog).getByRole('button', { name: /create rule/i })
    expect(submit).toBeDisabled() // a name is required
    await userEvent.type(
      within(dialog).getByLabelText(/^name/i),
      'contain-exfil',
    )
    await userEvent.click(submit)

    await waitFor(() => expect(api.createGuardianRule).toHaveBeenCalledTimes(1))
    expect(api.createGuardianRule.mock.calls[0][0]).toEqual({
      name: 'contain-exfil',
      enabled: true,
      min_severity: 'high',
      action: 'stop_agent',
      mode: 'approval',
    })
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
  })

  it('la TABLA enseña el tier de una regla acotada, y «cualquiera» cuando no lo está', async () => {
    // ⛔ El contraste `sol max` señaló que mi celda medía el ENVÍO y nunca la REPRESENTACIÓN:
    // el formulario mandaba el campo, el DTO lo conservaba… y la tabla saltaba de `match_kinds`
    // a `min_severity`, así que una regla acotada seguía viéndose como si aplicara a todas.
    // Enviar sin poder leer no cierra el hueco: lo mueve.
    api.listGuardianRules.mockResolvedValue({
      items: [
        { ...guardianRulesFixture[0], id: 'r-tier', agent_tier: 'critical' },
        { ...guardianRulesFixture[0], id: 'r-any', agent_tier: undefined },
      ],
      has_more: false,
    })
    wrap(<KillswitchView />)

    // Ancla POSITIVA: la fila acotada muestra su tier.
    expect(await screen.findByText('critical')).toBeInTheDocument()
    // Y la que no acota lo dice, en vez de callarlo.
    expect(screen.getAllByText(/any|cualquiera/i).length).toBeGreaterThan(0)
  })

  it('manda el agent_tier elegido, y NO lo inventa cuando el operador lo deja en «any»', async () => {
    // El motor acepta `agent_tier` desde `modules/governance/guardian.go:192` y lo valida a
    // low|medium|high|critical (`:267-270`); la consola no lo conocía, así que ese eje de la
    // regla era inalcanzable desde la interfaz. Aquí van las dos mitades:
    //   · POSITIVO — el tier elegido viaja en el cuerpo
    //   · NO-DISPARO — el caso «any» NO añade la clave (la celda de arriba, con `toEqual`
    //     sobre el cuerpo exacto, ya lo cubre y sigue verde: mandarlo vacío la pondría roja)
    api.createGuardianRule.mockResolvedValue(guardianRulesFixture[0])
    wrap(<KillswitchView />)

    await userEvent.click(
      await screen.findByRole('button', { name: /new rule/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await userEvent.type(
      within(dialog).getByLabelText(/^name/i),
      'contain-critical',
    )
    await userEvent.click(within(dialog).getByLabelText(/agent risk tier/i))
    await userEvent.click(
      await screen.findByRole('option', { name: 'critical' }),
    )
    await userEvent.click(
      within(dialog).getByRole('button', { name: /create rule/i }),
    )

    await waitFor(() => expect(api.createGuardianRule).toHaveBeenCalledTimes(1))
    expect(api.createGuardianRule.mock.calls[0][0]).toEqual({
      name: 'contain-critical',
      enabled: true,
      min_severity: 'high',
      action: 'stop_agent',
      mode: 'approval',
      agent_tier: 'critical',
    })
  })

  it('toggles a rule enabled state via PUT', async () => {
    api.listGuardianRules.mockResolvedValue({
      items: [guardianRulesFixture[0]],
      has_more: false,
    })
    api.updateGuardianRule.mockResolvedValue({
      ...guardianRulesFixture[0],
      enabled: false,
    })
    wrap(<KillswitchView />)

    await userEvent.click(
      await screen.findByRole('switch', {
        name: /disable rule contain-exfil/i,
      }),
    )
    await waitFor(() =>
      expect(api.updateGuardianRule).toHaveBeenCalledWith('gr-1', {
        enabled: false,
      }),
    )
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
  })

  it('deletes a rule only after confirming the cancellation warning', async () => {
    api.listGuardianRules.mockResolvedValue({
      items: [guardianRulesFixture[0]],
      has_more: false,
    })
    api.deleteGuardianRule.mockResolvedValue(undefined)
    wrap(<KillswitchView />)

    await userEvent.click(
      await screen.findByRole('button', {
        name: /delete rule contain-exfil/i,
      }),
    )
    const dialog = await screen.findByRole('dialog')
    expect(api.deleteGuardianRule).not.toHaveBeenCalled()
    expect(
      within(dialog).getByText(
        /pending approval-mode containments of this rule will be cancelled/i,
      ),
    ).toBeInTheDocument()
    expect(
      within(dialog).getByText(
        /executed action trail is preserved as evidence/i,
      ),
    ).toBeInTheDocument()
    expect(
      within(dialog).getByText(/same name will not inherit dedup suppression/i),
    ).toBeInTheDocument()

    await userEvent.click(
      within(dialog).getByRole('button', { name: /^delete rule$/i }),
    )
    await waitFor(() =>
      expect(api.deleteGuardianRule).toHaveBeenCalledWith('gr-1'),
    )
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
  })

  it.each([
    [404, 'not_found', 'rule disappeared', /guardian rule not found/i],
    [409, 'conflict', 'rule is busy', /guardian rule deletion blocked/i],
  ])(
    'shows a %i delete refusal inline',
    async (status, code, message, heading) => {
      api.listGuardianRules.mockResolvedValue({
        items: [guardianRulesFixture[0]],
        has_more: false,
      })
      api.deleteGuardianRule.mockRejectedValue(
        new ApiError(status, code, message),
      )
      wrap(<KillswitchView />)

      await userEvent.click(
        await screen.findByRole('button', {
          name: /delete rule contain-exfil/i,
        }),
      )
      const dialog = await screen.findByRole('dialog')
      await userEvent.click(
        within(dialog).getByRole('button', { name: /^delete rule$/i }),
      )

      const alert = await within(dialog).findByRole('alert')
      expect(within(alert).getByText(heading)).toBeInTheDocument()
      expect(within(alert).getByText(message)).toBeInTheDocument()
      expect(toast.error).not.toHaveBeenCalled()
    },
  )

  it('renders the containment trail with honest status chips (executed vs pending HITL)', async () => {
    api.listGuardianRules.mockResolvedValue({
      items: guardianRulesFixture,
      has_more: false,
    })
    api.listGuardianActions.mockResolvedValue({
      items: guardianActionsFixture,
      has_more: false,
    })
    wrap(<KillswitchView />)

    // Anchor on row content ('Executed' alone would match the column header).
    expect(await screen.findByText('apr-90')).toBeInTheDocument()
    expect(screen.getByText('Pending')).toBeInTheDocument()
    // Header + the executed containment's status chip.
    expect(screen.getAllByText('Executed').length).toBeGreaterThan(1)
    // The executed one names its finding and the honest engine detail.
    expect(screen.getByText('data_exfil_attempt')).toBeInTheDocument()
    expect(screen.getByText('kill switch engaged')).toBeInTheDocument()
  })
})

describe('KillswitchView — RBAC gating', () => {
  it('hides engage, re-enable, evidence and rule authoring without the admin grants', async () => {
    authState.can = (p) => !p.endsWith(':admin')
    api.list.mockResolvedValue({ items: [estateStopFixture], has_more: false })
    api.state.mockResolvedValue(estateStoppedStateFixture)
    api.listGuardianRules.mockResolvedValue({
      items: [guardianRulesFixture[0]],
      has_more: false,
    })
    wrap(<KillswitchView />)

    // The read view still shows the truth…
    expect(await screen.findByText('ESTATE STOPPED')).toBeInTheDocument()
    // …but offers no privileged action the backend would 403.
    expect(screen.queryByRole('button', { name: /emergency stop/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /re-enable/i })).toBeNull()
    expect(
      screen.queryByRole('button', { name: /download evidence pack/i }),
    ).toBeNull()
    expect(screen.queryByRole('button', { name: /new rule/i })).toBeNull()
    expect(await screen.findByText('contain-exfil')).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /delete rule contain-exfil/i }),
    ).toBeNull()
    expect(
      screen.getByRole('switch', { name: /disable rule contain-exfil/i }),
    ).toBeDisabled()
  })
})
