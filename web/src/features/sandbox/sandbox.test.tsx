// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  renderIntel,
  screen,
  userEvent,
  waitFor,
  within,
} from '@/test/intel'
import { expectNoRawI18nKeys } from '@/test/i18n-keys'
import '@/features/_intel' // register the shared `intel` namespace for badges/notices
import { ComparisonCard, OutputsList, RunsTable, ScenariosTable } from './components'
import {
  comparisonsFixture,
  outputsFixture,
  runsFixture,
  scenariosFixture,
} from './fixtures'
import './i18n'

// The principal's EFFECTIVE permission set, exactly as the engine hands it to the
// console: can() is membership, so a test states the set and nothing else.
// `confinedWorkspace` is here because the permission set CANNOT carry it: a confined
// editor holds sandbox:scenario:write and the engine still refuses the route.
const auth = vi.hoisted(() => ({
  perms: new Set<string>(),
  confinedWorkspace: null as string | null,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (p: string) => auth.perms.has(p),
    confinedWorkspace: auth.confinedWorkspace,
  }),
}))

// The real HTTP verbs, so the wrappers' method + path + body have a regression of
// their own: every other cell here substitutes sandboxApi, which means a typo in the
// route would keep all of them green while the engine answered 404.
const httpMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('@/lib/api/client', () => ({ http: httpMock }))

const api = vi.hoisted(() => ({
  scenarios: vi.fn(),
  runs: vi.fn(),
  comparisons: vi.fn(),
  outputs: vi.fn(),
  createScenario: vi.fn(),
  archiveScenario: vi.fn(),
  replay: vi.fn(),
  compare: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  sandboxApi: api,
}))

import { SandboxView } from './sandbox-view'

/** Open the scenarios tab of a freshly rendered view, with the given rights. */
async function openScenariosTab(
  permissions: string[],
  confinedWorkspace: string | null = null,
) {
  auth.perms = new Set(permissions)
  auth.confinedWorkspace = confinedWorkspace
  const user = userEvent.setup()
  renderIntel(<SandboxView />)
  await user.click(screen.getByRole('tab', { name: 'Scenarios' }))
  // Wait for the TABLE, never for role="status": the loading skeleton and the empty
  // state both carry it, so a bare findByRole('status') can resolve on an empty node
  // while the section is still resolving.
  await screen.findByRole('grid')
  return user
}

beforeEach(() => {
  vi.clearAllMocks()
  auth.perms = new Set<string>()
  auth.confinedWorkspace = null
  api.scenarios.mockResolvedValue({
    items: scenariosFixture,
    has_more: false,
  })
  api.runs.mockResolvedValue({ items: runsFixture, has_more: false })
  api.comparisons.mockResolvedValue({
    items: comparisonsFixture,
    has_more: false,
  })
  api.outputs.mockResolvedValue({ items: [], has_more: false })
  api.createScenario.mockResolvedValue({
    ...scenariosFixture[0],
    id: 'scn-new',
  })
  api.archiveScenario.mockResolvedValue({
    ...scenariosFixture[0],
    status: 'archived',
  })
})

describe('RunsTable — isolation is shown literally', () => {
  it('renders the runner, the isolated flag and the destroyed marker for a run', () => {
    renderIntel(<RunsTable runs={runsFixture} />)
    const table = screen.getByRole('grid')
    // the runner backend, recorded literally (in-proc mock for run-001)
    expect(within(table).getAllByText(/in-proc mock/i).length).toBeGreaterThan(
      0,
    )
    // isolation guarantee + ephemeral-state-discarded marker
    expect(within(table).getAllByText(/^Isolated$/).length).toBeGreaterThan(0)
    expect(within(table).getAllByText(/^Destroyed$/).length).toBeGreaterThan(0)
    // the OS-level container backend is also surfaced, never collapsed into the mock
    expect(within(table).getByText(/^Container$/i)).toBeInTheDocument()
  })

  it('shows a degraded run as "executed, not scored" — NEVER a pass', () => {
    renderIntel(<RunsTable runs={runsFixture} />)
    const table = screen.getByRole('grid')
    // run-002 is degraded: it must read as executed-not-scored, not a pass
    expect(within(table).getByText(/Executed, not scored/i)).toBeInTheDocument()
    // the degraded run carries no fabricated pass/score
    const degradedRow = within(table)
      .getByText('prompt:refund-eligibility')
      .closest('tr') as HTMLElement
    expect(within(degradedRow).getByText(/Degraded/i)).toBeInTheDocument()
    expect(within(degradedRow).queryByText(/^Pass$/)).not.toBeInTheDocument()
    expect(
      within(degradedRow).getByText(/Executed, not scored/i),
    ).toBeInTheDocument()
  })

  it('renders a real pass only for a scored, passing run', () => {
    // a single scored+passing run, so the score and pass are unambiguous
    renderIntel(<RunsTable runs={[runsFixture[0]]} />)
    const table = screen.getByRole('grid')
    // run-001: score 0.94 + pass
    expect(within(table).getByText('0.94')).toBeInTheDocument()
    expect(within(table).getByText(/^Pass$/)).toBeInTheDocument()
    // and it is NOT the "executed, not scored" honesty path
    expect(
      within(table).queryByText(/Executed, not scored/i),
    ).not.toBeInTheDocument()
  })
})

describe('ComparisonCard — verdict + delta', () => {
  it('renders an improved verdict with a positive signed delta', () => {
    const improved = comparisonsFixture[0]
    renderIntel(<ComparisonCard comparison={improved} />)
    expect(screen.getByText(/^Improved$/)).toBeInTheDocument()
    expect(screen.getByText('+0.06')).toBeInTheDocument()
    // baseline vs candidate scores are both shown
    expect(screen.getByText('0.88')).toBeInTheDocument()
    expect(screen.getByText('0.94')).toBeInTheDocument()
  })

  it('renders a regressed verdict with a negative signed delta', () => {
    const regressed = comparisonsFixture[1]
    renderIntel(<ComparisonCard comparison={regressed} />)
    expect(screen.getByText(/^Regressed$/)).toBeInTheDocument()
    expect(screen.getByText('-0.14')).toBeInTheDocument()
  })

  it('reports an inconclusive verdict honestly when there is no scored suite', () => {
    const inconclusive = comparisonsFixture[3]
    renderIntel(<ComparisonCard comparison={inconclusive} />)
    expect(screen.getByText(/^Inconclusive$/)).toBeInTheDocument()
    expect(
      screen.getByText(/verdict is inconclusive without a scored baseline/i),
    ).toBeInTheDocument()
  })
})

describe('ScenariosTable — the step count is DERIVED from what the engine sends', () => {
  it('counts the steps in the payload, with no steps_count field anywhere', () => {
    // The engine projects the steps themselves and no count (scenarios.go:44-51).
    // These three carry 3, 4 and 1 steps; before the column read a field that
    // does not exist on the wire, so every real row rendered nothing at all.
    renderIntel(<ScenariosTable scenarios={scenariosFixture} />)
    const table = screen.getByRole('grid')
    const checkout = within(table)
      .getByText('Checkout agent — happy path')
      .closest('tr') as HTMLElement
    expect(within(checkout).getByText('3')).toBeInTheDocument()
    const refund = within(table)
      .getByText('Refund policy — edge cases')
      .closest('tr') as HTMLElement
    expect(within(refund).getByText('4')).toBeInTheDocument()
    // and no fixture smuggles the count back in as its own property
    for (const scenario of scenariosFixture) {
      expect(scenario).not.toHaveProperty('steps_count')
    }
  })

  it('offers no archive action when the container passed no handler', () => {
    renderIntel(<ScenariosTable scenarios={scenariosFixture} />)
    expect(screen.queryAllByRole('button', { name: 'Archive' })).toHaveLength(0)
  })

  it('shows an em-dash, not a badge, when the scenario declares no subject', () => {
    // subject_kind is omitempty and the console can now author without one, so this
    // is a real row. A badge would assert that "—" is the kind it exercises.
    const noKind = [{ ...scenariosFixture[0], subject_kind: undefined }]
    renderIntel(<ScenariosTable scenarios={noKind} />)
    const row = screen
      .getByText('Checkout agent — happy path')
      .closest('tr') as HTMLElement
    const dash = within(row).getByText('—')
    expect(dash.className).not.toMatch(/bg-muted|rounded-full|border/)
    // the localized badge for a REAL kind still renders, proving the contrast
    renderIntel(<ScenariosTable scenarios={[scenariosFixture[0]]} />)
    expect(screen.getAllByText('Agent').length).toBeGreaterThan(0)
  })

  it('renders 0 rather than crashing when the wire sends a non-array steps', () => {
    // `steps` is typed T[] because the engine promises an array, but the type is a
    // compile-time claim about a WIRE payload. This is the branch that keeps a lying
    // payload to one honest cell instead of a TypeError that unmounts the panel — it
    // has no fixture, so without this case the guard would be dead code that reads
    // like a defence.
    const lying = [
      { ...scenariosFixture[0], steps: null as unknown as [] },
    ]
    renderIntel(<ScenariosTable scenarios={lying} />)
    const row = screen
      .getByText('Checkout agent — happy path')
      .closest('tr') as HTMLElement
    expect(within(row).getByText('0')).toBeInTheDocument()
  })
})

describe('Scenario authoring — the console calls what the engine already served', () => {
  it('creates a scenario with the body the engine accepts, and refreshes the list', async () => {
    const user = await openScenariosTab(['sandbox:scenario:write'])
    await user.click(screen.getByRole('button', { name: 'New scenario' }))

    // Wait for the FORM, not for a status node (the skeleton and the empty state
    // both use role="status").
    const name = await screen.findByLabelText(/^Name/)
    await user.type(name, 'Refund policy — edge cases')
    await user.type(screen.getByLabelText('Subject'), 'prompt')
    await user.type(screen.getByLabelText('Step 1 key'), 'step-1-classify')
    await user.type(
      screen.getByLabelText('Step 1 input'),
      'Classify this refund request.',
    )
    await user.click(screen.getByRole('button', { name: 'Add mock' }))
    await user.type(
      screen.getByLabelText('Mock 1 resource'),
      'policy.refunds',
    )
    await user.type(
      screen.getByLabelText('Mock 1 response'),
      'Refunds within 30 days are eligible.',
    )
    // A row the operator opened and left blank is not a step: it is dropped, not
    // sent as {"key":"","input":""} for the engine to name `step-2`. Both halves are
    // exercised — the mocks filter used to be a survivor, because only the steps
    // filter had a blank row to drop.
    await user.click(screen.getByRole('button', { name: 'Add step' }))
    await user.click(screen.getByRole('button', { name: 'Add mock' }))

    // The dialog's copy is real copy, not unresolved keys (`catalog.lookup` is a
    // legitimate dotted placeholder, allowed one literal at a time).
    expectNoRawI18nKeys(document.body as HTMLElement, ['catalog.lookup'])

    await user.click(screen.getByRole('button', { name: 'Create scenario' }))

    await waitFor(() =>
      expect(api.createScenario).toHaveBeenCalledWith({
        name: 'Refund policy — edge cases',
        description: '',
        subject_kind: 'prompt',
        steps: [
          { key: 'step-1-classify', input: 'Classify this refund request.' },
        ],
        mocks: [
          {
            resource: 'policy.refunds',
            response: 'Refunds within 30 days are eligible.',
          },
        ],
      }),
    )
    // The list reflects it without a manual reload: the tenant-scoped key is
    // invalidated, so the scenarios query runs a second time.
    await waitFor(() => expect(api.scenarios).toHaveBeenCalledTimes(2))
  })

  it('keeps the dialog open and shows the engine’s refusal verbatim', async () => {
    const { ApiError } = await import('@/lib/api/errors')
    api.createScenario.mockRejectedValue(
      new ApiError(
        409,
        'conflict',
        'a scenario with this name already exists',
        undefined,
        undefined,
        undefined,
      ),
    )
    const user = await openScenariosTab(['sandbox:scenario:write'])
    await user.click(screen.getByRole('button', { name: 'New scenario' }))
    await user.type(await screen.findByLabelText(/^Name/), 'Checkout agent')
    await user.click(screen.getByRole('button', { name: 'Create scenario' }))

    const refusal = await screen.findByRole('alert')
    expect(refusal).toHaveTextContent('a scenario with this name already exists')
    // still open, with the typed name intact — the operator can rename and retry
    expect(screen.getByLabelText(/^Name/)).toHaveValue('Checkout agent')
    expect(api.scenarios).toHaveBeenCalledTimes(1)
  })

  it('does not offer authoring at all without sandbox:scenario:write', async () => {
    await openScenariosTab(['sandbox:scenario:read'])
    expect(
      screen.queryByRole('button', { name: 'New scenario' }),
    ).not.toBeInTheDocument()
  })

  it('hides both actions from a CONFINED member that holds both permissions', async () => {
    // The effective set carries the module writes for a confined editor — the engine
    // strips only the global recon reads (core/auth/effective.go:60-95) — but both
    // routes mount with Handle, so the authorization request has no workspace and a
    // confined mutation with an indeterminate target is forbidden outright
    // (modules/governance/grants.go:723-731). Asking the set alone would offer a
    // button whose 403 is guaranteed, not a race.
    await openScenariosTab(
      ['sandbox:scenario:read', 'sandbox:scenario:write', 'sandbox:scenario:admin'],
      'ws-payments',
    )
    expect(
      screen.queryByRole('button', { name: 'New scenario' }),
    ).not.toBeInTheDocument()
    expect(screen.queryAllByRole('button', { name: 'Archive' })).toHaveLength(0)
  })

  it('tells the confined member WHY, instead of the read-only reason', async () => {
    api.scenarios.mockResolvedValue({ items: [], has_more: false })
    auth.perms = new Set(['sandbox:scenario:write'])
    auth.confinedWorkspace = 'ws-payments'
    const user = userEvent.setup()
    renderIntel(<SandboxView />)
    await user.click(screen.getByRole('tab', { name: 'Scenarios' }))
    expect(
      await screen.findByText(/membership is confined to a workspace/i),
    ).toBeInTheDocument()
    // NOT the "you do not hold the permission" reason — it holds it
    expect(
      screen.queryByText(/authored by a caller holding/i),
    ).not.toBeInTheDocument()
  })

  it('tells a read-only principal WHO can author, when the list is empty', async () => {
    api.scenarios.mockResolvedValue({ items: [], has_more: false })
    auth.perms = new Set(['sandbox:scenario:read'])
    const user = userEvent.setup()
    renderIntel(<SandboxView />)
    await user.click(screen.getByRole('tab', { name: 'Scenarios' }))
    expect(
      await screen.findByText(/authored by a caller holding/i),
    ).toBeInTheDocument()
    expect(
      screen.queryByText(/Record the first synthetic fixture/i),
    ).not.toBeInTheDocument()
  })

  it('invites the principal who CAN author to record the first one', async () => {
    api.scenarios.mockResolvedValue({ items: [], has_more: false })
    auth.perms = new Set(['sandbox:scenario:write'])
    const user = userEvent.setup()
    renderIntel(<SandboxView />)
    await user.click(screen.getByRole('tab', { name: 'Scenarios' }))
    expect(
      await screen.findByText(/Record the first synthetic fixture/i),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'New scenario' }),
    ).toBeInTheDocument()
  })
})

describe('The wrappers themselves — method, path and body reach the engine', () => {
  it('posts the create and archive routes the engine actually mounts', async () => {
    // The REAL module, not the double the rest of this file installs: a typo in BASE
    // or in either path is invisible to every other cell here, because they all
    // assert against a mock that would answer any path at all.
    const real = await vi.importActual<typeof import('./api')>('./api')
    httpMock.post.mockResolvedValue({})

    await real.sandboxApi.createScenario({ name: 'Checkout agent' })
    expect(httpMock.post).toHaveBeenCalledWith('/v1/m/sandbox/scenarios', {
      name: 'Checkout agent',
    })

    await real.sandboxApi.archiveScenario('scn-checkout-flow')
    expect(httpMock.post).toHaveBeenCalledWith(
      '/v1/m/sandbox/scenarios/scn-checkout-flow/archive',
      {},
    )
    // and no GET was used for either: both are POSTs, as the routes are mounted
    expect(httpMock.get).not.toHaveBeenCalled()
  })
})

describe('Scenario archiving — admin-tier, confirmed, reflected in the list', () => {
  it('archives the chosen scenario after a confirmation naming it', async () => {
    const user = await openScenariosTab(['sandbox:scenario:admin'])
    // Two ACTIVE scenarios get the action; the archived one does not — the engine's
    // archive is idempotent, so offering it again would be an audited no-op.
    const buttons = screen.getAllByRole('button', { name: 'Archive' })
    expect(buttons).toHaveLength(2)

    const row = within(screen.getByRole('grid'))
      .getByText('Checkout agent — happy path')
      .closest('tr') as HTMLElement
    await user.click(within(row).getByRole('button', { name: 'Archive' }))

    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText(/Checkout agent — happy path/),
    ).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Archive' }))

    await waitFor(() =>
      expect(api.archiveScenario).toHaveBeenCalledWith('scn-checkout-flow'),
    )
    await waitFor(() => expect(api.scenarios).toHaveBeenCalledTimes(2))
  })

  it('does not archive on the row click alone — the confirmation is the gate', async () => {
    const user = await openScenariosTab(['sandbox:scenario:admin'])
    const row = within(screen.getByRole('grid'))
      .getByText('Checkout agent — happy path')
      .closest('tr') as HTMLElement
    await user.click(within(row).getByRole('button', { name: 'Archive' }))
    // Asserted BEFORE waiting for the dialog on purpose: if the button ever wired
    // straight to the mutation, this line is what fails — not a missing dialog.
    expect(api.archiveScenario).not.toHaveBeenCalled()
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
  })

  it('does not offer archiving to a principal with only the write right', async () => {
    await openScenariosTab(['sandbox:scenario:write'])
    // authoring IS offered, so this proves the two rights are asked separately and
    // not collapsed into one "can write" tier
    expect(
      screen.getByRole('button', { name: 'New scenario' }),
    ).toBeInTheDocument()
    expect(screen.queryAllByRole('button', { name: 'Archive' })).toHaveLength(0)
  })
})

describe('OutputsList — synthetic, mock-miss is a deterministic marker', () => {
  it('renders the deterministic mock-miss marker, never a real resource', () => {
    renderIntel(<OutputsList outputs={outputsFixture['run-002']} />)
    // the deterministic marker is rendered verbatim
    expect(screen.getByText('[[mock-miss:ledger.balance]]')).toBeInTheDocument()
    // and it is labelled a mock-miss, distinct from a mock hit
    expect(screen.getAllByText(/Mock-miss/i).length).toBeGreaterThan(0)
    expect(screen.getAllByText(/Mock hit/i).length).toBeGreaterThan(0)
    // the synthetic-output caveat is always present (not production output)
    expect(
      screen.getByText(/bounded synthetic outputs from the mock runner/i),
    ).toBeInTheDocument()
  })
})

describe('SandboxView — las tres acciones y sus dos niveles de permiso', () => {
  /**
   * ⛔ COMPARAR NO ES EJECUTAR, y el motor lo escalona: ejecutar y repetir son
   * `sandbox:run:write`; **comparar es `sandbox:run:admin`** porque una comparación es evidencia
   * de decisión pre/post-despliegue y se persiste append-only.
   *
   * EL MUTANTE: gatear las tres con el permiso de ejecución. Se le ofrece a quien sólo ejecuta el
   * botón que produce la evidencia con la que se decide desplegar.
   */
  it('quien puede EJECUTAR pero no administrar no ve Comparar', async () => {
    auth.perms = new Set(['sandbox:run:write', 'sandbox:scenario:read'])
    auth.confinedWorkspace = null
    api.runs.mockResolvedValue({ items: [], has_more: false })
    api.comparisons.mockResolvedValue({ items: [], has_more: false })
    api.scenarios.mockResolvedValue({ items: [], has_more: false })
    renderIntel(<SandboxView />)
    expect(
      await screen.findByRole('button', { name: /Replay a session/i }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /Compare variants/i }),
    ).toBeNull()
  })

  /** LA DIRECCIÓN QUE NO DEBE DISPARAR: con el nivel admin, Comparar SÍ está. */
  it('con sandbox:run:admin sí aparece Comparar', async () => {
    auth.perms = new Set(['sandbox:run:write', 'sandbox:run:admin'])
    auth.confinedWorkspace = null
    api.runs.mockResolvedValue({ items: [], has_more: false })
    api.comparisons.mockResolvedValue({ items: [], has_more: false })
    api.scenarios.mockResolvedValue({ items: [], has_more: false })
    renderIntel(<SandboxView />)
    expect(
      await screen.findByRole('button', { name: /Compare variants/i }),
    ).toBeInTheDocument()
  })

  /**
   * ⛔ Y LA REPETICIÓN AVISA ANTES DE LANZARSE: sin línea temporal reconstruible el motor devuelve
   * la repetición DEGRADADA con cero pasos, «never fabricated». Un resultado de cero pasos se lee
   * como «la sesión no hizo nada», y no es eso — es que no se pudo reconstruir.
   */
  it('el diálogo de repetición explica qué significan cero pasos', async () => {
    auth.perms = new Set(['sandbox:run:write'])
    auth.confinedWorkspace = null
    api.runs.mockResolvedValue({ items: [], has_more: false })
    api.comparisons.mockResolvedValue({ items: [], has_more: false })
    api.scenarios.mockResolvedValue({ items: [], has_more: false })
    const user = userEvent.setup()
    renderIntel(<SandboxView />)
    await user.click(
      await screen.findByRole('button', { name: /Replay a session/i }),
    )
    expect(
      await screen.findByText(/timeline that could not be rebuilt/i),
    ).toBeInTheDocument()
  })
})
