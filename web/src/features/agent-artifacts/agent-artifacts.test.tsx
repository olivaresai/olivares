// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Contract tests for the agent-artifact registry and its separate AIBOM ledger.
// These pin the command DTO boundary, unrepresentable posture states, retained
// conflict errors, receipt-only sealed export, permissions and ledger-head honesty.
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { ApiError } from '@/lib/api/errors'
import { useStepUpStore } from '@/stores/step-up'
import type { AgentArtifact, AibomSeal } from './types'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

const authState = vi.hoisted(() => ({
  activeTenant: 'tenant-1' as string | null,
  can: (_permission: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({
  artifacts: vi.fn(),
  createArtifact: vi.fn(),
  deleteArtifact: vi.fn(),
  liveAibom: vi.fn(),
  sealAibom: vi.fn(),
  aibomSeals: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, agentArtifactsApi: api }
})

const downloadJson = vi.hoisted(() => vi.fn())
vi.mock('./export', () => ({ downloadJson }))

import { agentArtifactsKeys } from './api'
import { AgentArtifactsView } from './agent-artifacts-view'
import './i18n'
import '@/features/_intel'

const artifact: AgentArtifact = {
  id: 'aa-1',
  artifact_class: 'skill',
  name: 'Skill analyzer',
  version: '1.2.0',
  provenance: 'internal registry',
  source_ref: 'git:skills/analyzer',
  content_hash: 'abc123',
  content_alg: 'sha256',
  posture_grade: 'B',
  posture_issues: 3,
  posture_scanned: true,
  verified: true,
  attested_by: 'operator@example.com',
  attested_at: '2026-07-22T08:00:00Z',
  note: 'Reviewed before registration',
}

const seal: AibomSeal = {
  id: 'seal-1',
  owned_ref: 'agent-artifacts',
  serial_number: 'urn:uuid:agent-seal-1',
  content_hash: 'feedface',
  spec_version: '1.6',
  component_count: 1,
  ledger_seq: 0,
  ledger_hash: '',
  scope_note:
    'Coverage reflects what was REGISTERED; an artifact never registered is not represented.',
  generated_by: 'operator@example.com',
  generated_at: '2026-07-22T08:30:00Z',
}

function wrap(ui: ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <TooltipProvider delayDuration={0}>{ui}</TooltipProvider>
    </QueryClientProvider>,
  )
}

async function openCreate() {
  wrap(<AgentArtifactsView />)
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: 'Register artifact' }),
  )
  return user
}

async function selectOption(
  user: ReturnType<typeof userEvent.setup>,
  label: string,
  option: string,
) {
  await user.click(screen.getByRole('combobox', { name: label }))
  const listbox = await screen.findByRole('listbox')
  await user.click(within(listbox).getByRole('option', { name: option }))
}

beforeEach(() => {
  authState.activeTenant = 'tenant-1'
  authState.can = () => true
  api.artifacts.mockReset().mockResolvedValue({ items: [], has_more: false })
  api.createArtifact.mockReset().mockResolvedValue(artifact)
  api.deleteArtifact.mockReset().mockResolvedValue(undefined)
  api.liveAibom.mockReset().mockResolvedValue({
    bomFormat: 'CycloneDX',
    serialNumber: 'live-document',
  })
  api.sealAibom.mockReset().mockResolvedValue({
    seal,
    aibom: { bomFormat: 'CycloneDX', serialNumber: 'sealed-document' },
  })
  api.aibomSeals.mockReset().mockResolvedValue({
    items: [],
    has_more: false,
  })
  downloadJson.mockReset()
  toast.success.mockReset()
  toast.error.mockReset()
  toast.warning.mockReset()
})

afterEach(() => vi.clearAllMocks())

describe('artifact create command', () => {
  it('routes a step-up 403 on create to the ceremony, not to the role accusation', async () => {
    // ⛔ El `onError` a mano colapsaba las DOS primeras respuestas en «tu rol no puede»:
    // `isForbidden` es sólo el status (lib/api/errors.ts:59) y un step_up_required lo
    // satisface también, así que el operador recibía una acusación por un permiso que SÍ
    // tiene, y la ceremonia que levanta la negativa no se abría. Ahora el reporte se
    // delega en useFailedActionReporter, que tiene esa rama primero.
    useStepUpStore.setState({ request: null })
    api.createArtifact.mockRejectedValueOnce(
      new ApiError(403, 'step_up_required', 'assurance level too low'),
    )
    const user = await openCreate()
    await user.type(screen.getByLabelText(/^Name/), 'Release skill')
    await user.click(screen.getByRole('button', { name: 'Create' }))

    // Ancla POSITIVA antes de la ausencia: sin ella la aserción de abajo se cumple en el
    // primer tick y la celda pasaría con el defecto puesto.
    await waitFor(() =>
      expect(useStepUpStore.getState().request).not.toBeNull(),
    )
    expect(toast.warning).not.toHaveBeenCalled()
  })

  it('keeps the calm role warning when the 403 carries no ceremony', async () => {
    // Control negativo del anterior: un 403 SIN código de ceremonia sigue avisando en
    // tono calmado, que es cierto y no se toca.
    useStepUpStore.setState({ request: null })
    api.createArtifact.mockRejectedValueOnce(
      new ApiError(403, 'forbidden', 'no'),
    )
    const user = await openCreate()
    await user.type(screen.getByLabelText(/^Name/), 'Release skill')
    await user.click(screen.getByRole('button', { name: 'Create' }))

    await waitFor(() => expect(toast.warning).toHaveBeenCalled())
    expect(useStepUpStore.getState().request).toBeNull()
  })

  it('derives the scanned body from a grade and sends no server-set fields', async () => {
    const user = await openCreate()
    await user.type(screen.getByLabelText(/^Name/), 'Release skill')
    await selectOption(user, 'Recorded scan result', 'Grade B')
    fireEvent.change(screen.getByLabelText('Recorded issues'), {
      target: { value: '3' },
    })

    await user.click(screen.getByRole('button', { name: 'Create' }))

    await waitFor(() => expect(api.createArtifact).toHaveBeenCalledTimes(1))
    const body = api.createArtifact.mock.calls[0][0]
    expect(body).toMatchObject({
      artifact_class: 'skill',
      name: 'Release skill',
      posture_scanned: true,
      posture_grade: 'B',
      posture_issues: 3,
      verified: false,
    })
    expect(body).not.toHaveProperty('id')
    expect(body).not.toHaveProperty('attested_by')
    expect(body).not.toHaveProperty('attested_at')
  })

  it('sends an honest unscanned body with no grade and zero issues', async () => {
    const user = await openCreate()
    await user.type(screen.getByLabelText(/^Name/), 'Unscanned skill')

    await user.click(screen.getByRole('button', { name: 'Create' }))

    await waitFor(() => expect(api.createArtifact).toHaveBeenCalledTimes(1))
    const body = api.createArtifact.mock.calls[0][0]
    expect(body.posture_scanned).toBe(false)
    expect(body.posture_issues).toBe(0)
    expect(body).not.toHaveProperty('posture_grade')
    expect(body).not.toHaveProperty('id')
    expect(body).not.toHaveProperty('attested_by')
  })

  it('uses one posture selector and resets issues when returning to Not scanned', async () => {
    const user = await openCreate()
    const issues = screen.getByLabelText('Recorded issues')

    expect(issues).toBeDisabled()
    expect(issues).toHaveValue(0)
    expect(screen.queryByLabelText(/posture scanned/i)).not.toBeInTheDocument()

    await selectOption(user, 'Recorded scan result', 'Grade D')
    expect(issues).toBeEnabled()
    fireEvent.change(issues, { target: { value: '7' } })
    expect(issues).toHaveValue(7)

    await selectOption(user, 'Recorded scan result', 'Not scanned')
    expect(issues).toBeDisabled()
    expect(issues).toHaveValue(0)
  })

  it('keeps the dialog and its input open after a duplicate 409', async () => {
    api.createArtifact.mockRejectedValueOnce(
      new ApiError(409, 'conflict', 'unique constraint failed'),
    )
    const user = await openCreate()
    await user.type(screen.getByLabelText(/^Name/), 'Existing skill')
    await user.click(screen.getByRole('button', { name: 'Create' }))

    expect(
      await screen.findByText('Artifact already registered'),
    ).toBeInTheDocument()
    expect(screen.getByText(/choose a different identity/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/^Name/)).toHaveValue('Existing skill')
    expect(
      screen.getByRole('dialog', { name: 'Register agent artifact' }),
    ).toBeInTheDocument()
  })
})

describe('agent supply-chain AIBOM', () => {
  it('confirms before sealing and downloads the receipt BOM, not the live preview', async () => {
    const user = userEvent.setup()
    const liveAibom = {
      bomFormat: 'CycloneDX',
      serialNumber: 'live-document',
    }
    const sealedAibom = {
      bomFormat: 'CycloneDX',
      serialNumber: 'sealed-document',
    }
    api.liveAibom.mockResolvedValue(liveAibom)
    api.sealAibom.mockResolvedValue({ seal, aibom: sealedAibom })

    wrap(<AgentArtifactsView />)
    await user.click(
      screen.getByRole('tab', { name: 'Agent supply-chain AIBOM' }),
    )
    await user.click(
      await screen.findByRole('button', { name: 'Preview live BOM' }),
    )
    const liveDialog = await screen.findByRole('dialog', {
      name: 'Live agent supply-chain BOM',
    })
    expect(within(liveDialog).getByText(/live-document/)).toBeInTheDocument()
    await user.click(
      within(liveDialog).getAllByRole('button', { name: 'Close' })[0],
    )

    await user.click(screen.getByRole('button', { name: 'Seal current BOM' }))
    const confirm = await screen.findByRole('dialog', {
      name: 'Seal agent supply-chain BOM',
    })
    expect(api.sealAibom).not.toHaveBeenCalled()
    expect(within(confirm).getByText(/does not archive/i)).toBeInTheDocument()
    await user.click(
      within(confirm).getByRole('button', { name: 'Seal and receive BOM' }),
    )

    const receipt = await screen.findByRole('dialog', {
      name: 'Sealed BOM receipt',
    })
    await user.click(
      within(receipt).getByRole('button', { name: 'Download sealed BOM' }),
    )
    expect(downloadJson).toHaveBeenCalledTimes(1)
    expect(downloadJson).toHaveBeenCalledWith(
      sealedAibom,
      expect.stringContaining('seal-1'),
    )
    expect(downloadJson).not.toHaveBeenCalledWith(liveAibom, expect.any(String))
  })

  it('renders a zero ledger sequence as no prior audit head', async () => {
    const user = userEvent.setup()
    api.aibomSeals.mockResolvedValue({ items: [seal], has_more: false })
    wrap(<AgentArtifactsView />)

    await user.click(
      screen.getByRole('tab', { name: 'Agent supply-chain AIBOM' }),
    )

    expect(
      await screen.findByText('0 (no prior audit head)'),
    ).toBeInTheDocument()
  })
})

describe('read-only permissions', () => {
  it('hides create, delete and seal while retaining read access', async () => {
    const user = userEvent.setup()
    authState.can = (permission) => permission !== 'models:registry:write'
    api.artifacts.mockResolvedValue({ items: [artifact], has_more: false })
    wrap(<AgentArtifactsView />)

    expect(
      screen.queryByRole('button', { name: 'Register artifact' }),
    ).not.toBeInTheDocument()
    await user.click(await screen.findByText('Skill analyzer'))
    expect(
      screen.queryByRole('button', { name: 'Delete' }),
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Close' }))

    await user.click(
      screen.getByRole('tab', { name: 'Agent supply-chain AIBOM' }),
    )
    expect(
      screen.queryByRole('button', { name: 'Seal current BOM' }),
    ).not.toBeInTheDocument()
    expect(
      await screen.findByRole('button', { name: 'Preview live BOM' }),
    ).toBeEnabled()
  })
})

describe('el registro y el ledger declaran su recorte', () => {
  /**
   * ⛔ EL TECHO LLEGA A LAS DOS LLAMADAS. Sin `limit` el repositorio genérico pagina a 100 y
   * los dos handlers publican un `has_more` que esta pantalla tiraba. En el registro eso se
   * leía «éstos son los artefactos del parque»; en el ledger —que es APPEND-ONLY— lo que se
   * perdía era la evidencia MÁS RECIENTE, porque la página va por `id ASC`.
   *
   * Testigo de TRANSPORTE: mide lo que sale por la llamada, no lo que el código pretende.
   */
  it('pide el techo real del motor en el registro y en el ledger', async () => {
    const user = userEvent.setup()
    wrap(<AgentArtifactsView />)
    await waitFor(() => expect(api.artifacts).toHaveBeenCalled())
    expect(api.artifacts).toHaveBeenLastCalledWith({ limit: 1000 })

    await user.click(
      screen.getByRole('tab', { name: 'Agent supply-chain AIBOM' }),
    )
    await waitFor(() => expect(api.aibomSeals).toHaveBeenCalled())
    expect(api.aibomSeals).toHaveBeenLastCalledWith({ limit: 1000 })
  })

  /** El aviso del registro, en las dos direcciones. */
  it('el registro declara el recorte con has_more, y no sin él', async () => {
    api.artifacts.mockResolvedValue({ items: [artifact], has_more: true })
    wrap(<AgentArtifactsView />)
    expect(
      await screen.findByText(/Loaded \d+ artifacts; there are more/i),
    ).toBeVisible()

    cleanup()
    api.artifacts.mockResolvedValue({ items: [artifact], has_more: false })
    wrap(<AgentArtifactsView />)
    await waitFor(() => expect(api.artifacts).toHaveBeenCalled())
    expect(
      screen.queryByText(/Loaded \d+ artifacts; there are more/i),
    ).toBeNull()
  })

  /** Y el del ledger, también en las dos. */
  it('el ledger declara el recorte con has_more, y no sin él', async () => {
    const user = userEvent.setup()
    api.aibomSeals.mockResolvedValue({ items: [seal], has_more: true })
    wrap(<AgentArtifactsView />)
    await user.click(
      screen.getByRole('tab', { name: 'Agent supply-chain AIBOM' }),
    )
    expect(
      await screen.findByText(/Loaded \d+ seals; there are more/i),
    ).toBeVisible()

    cleanup()
    api.aibomSeals.mockResolvedValue({ items: [seal], has_more: false })
    const user2 = userEvent.setup()
    wrap(<AgentArtifactsView />)
    await user2.click(
      screen.getByRole('tab', { name: 'Agent supply-chain AIBOM' }),
    )
    await waitFor(() => expect(api.aibomSeals).toHaveBeenCalled())
    expect(screen.queryByText(/Loaded \d+ seals; there are more/i)).toBeNull()
  })

  /**
   * LA DIRECCIÓN QUE NO DEBE DISPARAR: «no pude leer» no es «hay más». Sin esta casilla, el
   * aviso se quedaría flotando sobre un estado de error, que es la lectura contraria.
   */
  it('un fallo de lectura del ledger no pinta un aviso de recorte', async () => {
    const user = userEvent.setup()
    api.aibomSeals.mockRejectedValue(new Error('boom'))
    wrap(<AgentArtifactsView />)
    await user.click(
      screen.getByRole('tab', { name: 'Agent supply-chain AIBOM' }),
    )
    await waitFor(() => expect(api.aibomSeals).toHaveBeenCalled())
    expect(screen.queryByText(/Loaded \d+ seals; there are more/i)).toBeNull()
  })

  /**
   * ⛔ LA GUARDA DEL LEDGER, con dato viejo sembrado. La casilla de arriba rechaza desde el
   * primer intento y por eso NO llega a la guarda; ésta sí. Sin ella, un mutante que quitara
   * el `&& !history.error` escaparía — lo dijo el contraste externo con el mutante escrito.
   */
  it('un refetch fallido retira el aviso del ledger aunque el dato viejo diga has_more', async () => {
    const user = userEvent.setup()
    const clave = agentArtifactsKeys.aibomSeals('tenant-1', { limit: 1000 })
    const queryClient = new QueryClient({
      defaultOptions: {
        queries: { retry: false, staleTime: 0 },
        mutations: { retry: false },
      },
    })
    queryClient.setQueryData(clave, { items: [seal], has_more: true })
    api.aibomSeals.mockRejectedValue(new Error('boom'))
    render(
      <QueryClientProvider client={queryClient}>
        <TooltipProvider delayDuration={0}>
          <AgentArtifactsView />
        </TooltipProvider>
      </QueryClientProvider>,
    )
    await user.click(
      screen.getByRole('tab', { name: 'Agent supply-chain AIBOM' }),
    )
    await waitFor(() => expect(api.aibomSeals).toHaveBeenCalled())
    await waitFor(() =>
      expect(queryClient.getQueryState(clave)?.error).toBeTruthy(),
    )
    expect(screen.queryByText(/Loaded \d+ seals; there are more/i)).toBeNull()
  })

  /**
   * ⛔ Y LA RAMA FILTRADA DEL REGISTRO. El testigo de transporte de arriba sólo prueba «todas
   * las clases»: un mutante que retirara el techo SÓLO de la rama con clase escaparía, y la
   * pantalla enseñaría cien filas mientras el aviso habla de las que cargó. El filtro lo
   * aplica el MOTOR (`handleListAgentArtifacts` lo añade antes de `repo.List`), así que la
   * clase y el techo tienen que viajar juntos.
   */
  it('el techo viaja también en la rama filtrada por clase', async () => {
    const user = userEvent.setup()
    wrap(<AgentArtifactsView />)
    await waitFor(() => expect(api.artifacts).toHaveBeenCalled())
    await selectOption(user, 'Artifact class', 'Skill')
    await waitFor(() =>
      expect(api.artifacts).toHaveBeenLastCalledWith({
        artifact_class: 'skill',
        limit: 1000,
      }),
    )
  })

  /**
   * ⛔ Y EL CASO QUE DE VERDAD EJERCE LA GUARDA DEL REGISTRO, que hay que montar con dato
   * viejo en la caché: con la consulta fallando desde el primer intento `data` es `undefined`
   * y el aviso no sale **aunque se quite el `&& !query.error`** — la casilla pasaría sin
   * llegar a la guarda. Lo cazó un mutante que la quitaba y ESCAPABA. react-query CONSERVA el
   * último dato bueno mientras marca el error, así que ése es el escenario real.
   */
  it('un refetch fallido retira el aviso del registro aunque el dato viejo diga has_more', async () => {
    const clave = agentArtifactsKeys.artifacts('tenant-1', undefined, {
      limit: 1000,
    })
    const queryClient = new QueryClient({
      defaultOptions: {
        queries: { retry: false, staleTime: 0 },
        mutations: { retry: false },
      },
    })
    queryClient.setQueryData(clave, { items: [artifact], has_more: true })
    api.artifacts.mockRejectedValue(new Error('boom'))
    render(
      <QueryClientProvider client={queryClient}>
        <TooltipProvider delayDuration={0}>
          <AgentArtifactsView />
        </TooltipProvider>
      </QueryClientProvider>,
    )
    await waitFor(() => expect(api.artifacts).toHaveBeenCalled())
    await waitFor(() =>
      expect(queryClient.getQueryState(clave)?.error).toBeTruthy(),
    )
    expect(
      screen.queryByText(/Loaded \d+ artifacts; there are more/i),
    ).toBeNull()
  })
})

/**
 * ⛔ EL AVISO DE RECORTE NO SE SUPERPONE AL ESTADO VACIO — testigo de CABLEADO. Este aviso y el
 *    `empty` del DataTable cuelgan de la MISMA lista (`query.data?.items`), asi que con
 *    `{items: [], has_more: true}` salian a la vez «No artifacts registered» y «Loaded 0
 *    artifacts; there are more»: un mensaje que se contradice solo. Lo nombro the reviewer al
 *    re-verificar (F-04).
 *
 * ⛔ Y VA EN LAS DOS DIRECCIONES A PROPOSITO. Con solo el caso vacio, borrar el aviso ENTERO
 *    tambien pasaria: el testigo no distinguiria «no se superpone» de «no existe».
 */
describe('AgentArtifactsView — el aviso de recorte y el vacio no conviven', () => {
  it('lista vacia con has_more: sale el vacio y NINGUN aviso', async () => {
    api.artifacts.mockResolvedValue({ items: [], has_more: true })
    wrap(<AgentArtifactsView />)
    expect(await screen.findByText('No artifacts registered')).toBeVisible()
    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })

  it('con filas y has_more, el aviso SI sale', async () => {
    api.artifacts.mockResolvedValue({ items: [artifact], has_more: true })
    wrap(<AgentArtifactsView />)
    expect(
      await screen.findByText('Loaded 1 artifacts; there are more'),
    ).toBeVisible()
  })
})
