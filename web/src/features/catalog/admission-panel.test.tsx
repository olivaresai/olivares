// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// AdmissionPanel tests. Each case pins ONE load-bearing property of the durable
// attestation-admission surface (noted in a comment). The toaster, the auth context
// (configurable `can`), and the './api' module are mocked; the panel is rendered in
// isolation, mirroring catalog.test.tsx's style.
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdmissionDTO, EntryDTO } from './types'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
  info: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

const authState = vi.hoisted(() => ({
  activeTenant: 't1' as string | null,
  can: (_p: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({
  listAdmissions: vi.fn(),
  admitEntry: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, catalogApi: api }
})

import { ApiError } from '@/lib/api/errors'
import { useStepUpStore } from '@/stores/step-up'
import { AdmissionPanel } from './admission-panel'
import './i18n'

const mcpEntry: EntryDTO = {
  id: 'e-mcp',
  kind: 'mcp',
  name: 'GitHub MCP',
  slug: 'github-mcp',
  version: '1.0.0',
  status: 'draft',
  spec: { transport: 'stdio', endpoint: 'npx server' },
  signed: false,
}
const connectorEntry: EntryDTO = {
  ...mcpEntry,
  id: 'e-conn',
  kind: 'connector',
}
const agentEntry: EntryDTO = { ...mcpEntry, id: 'e-agent', kind: 'agent' }

const verifiedVerdict: AdmissionDTO = {
  entry_ref: 'e-mcp',
  subject_name: 'mcp/github',
  subject_digest: 'b'.repeat(64),
  predicate_type: 'https://slsa.dev/provenance/v1',
  method: 'bare-key',
  signer_identity: 'ci@acme.io',
  signature_verified: true,
  artifact_verified: true,
  tlog_present: true,
  tlog_verified: true,
  reason: 'signature verified over the expected artifact digest',
  attested_by: 'user:1234',
  attested_at: '2026-06-01T10:00:00Z',
}

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  authState.can = () => true
  api.listAdmissions.mockReset()
  api.admitEntry.mockReset()
  toast.success.mockReset()
  toast.warning.mockReset()
  toast.error.mockReset()
  api.listAdmissions.mockResolvedValue({ items: [] })
})
afterEach(() => vi.clearAllMocks())

describe('AdmissionPanel — durable admission verdict', () => {
  // Property: a verified verdict renders the durable "why" — posture + verbatim reason
  // + the artifact digest it covered — the record that outlives any toast.
  it('ofrece la ceremonia en vez de esconder el veredicto cuando el 403 es de ASEGURAMIENTO', async () => {
    // Este panel existe para ENSEÑAR el veredicto de admisión. Con `isForbidden` leído
    // primero —y es sólo el status, lib/api/errors.ts:59— un `step_up_required` lo tapaba
    // con un «no autorizado» falso, sobre un permiso que el operador SÍ tiene.
    useStepUpStore.setState({ request: null })
    api.listAdmissions.mockRejectedValue(
      new ApiError(403, 'step_up_required', 'assurance level too low'),
    )
    wrap(<AdmissionPanel entry={mcpEntry} />)

    // Ancla POSITIVA antes de cualquier ausencia.
    expect(
      await screen.findByText(/step-up|verification|verificación/i),
    ).toBeInTheDocument()
  })

  it('conserva la negativa de ROL cuando el 403 no trae código de ceremonia', async () => {
    useStepUpStore.setState({ request: null })
    api.listAdmissions.mockRejectedValue(new ApiError(403, 'forbidden', 'no'))
    wrap(<AdmissionPanel entry={mcpEntry} />)

    // ⛔ ANCLA POSITIVA. Esta celda era SÓLO ausencias y el contraste `sol max` mutó la rama
    // de rol de `<ForbiddenState />` a `<ErrorState />` sin que se enterara: dos ausencias se
    // cumplen igual pinte lo que pinte. La distinción es el rol ARIA — frontera de permiso
    // `role="status"`, avería `role="alert"` (error-state.tsx:41 vs :99).
    expect(await screen.findByRole('status')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    await waitFor(() =>
      expect(
        screen.queryByText(/step-up|verificación/i),
      ).not.toBeInTheDocument(),
    )
    expect(useStepUpStore.getState().request).toBeNull()
  })

  it('renders a verified verdict with its verbatim reason and covered digest', async () => {
    api.listAdmissions.mockResolvedValue({ items: [verifiedVerdict] })
    wrap(<AdmissionPanel entry={mcpEntry} />)

    expect(await screen.findByText('Attestation verified')).toBeInTheDocument()
    expect(
      screen.getByText('signature verified over the expected artifact digest'),
    ).toBeInTheDocument()
    expect(screen.getByText('b'.repeat(64))).toBeInTheDocument()
    // queried the mcp endpoint for THIS entry.
    expect(api.listAdmissions).toHaveBeenCalledWith('mcp', 'e-mcp')
  })

  // Property: a signature-verified-but-unbound verdict is NEVER shown as "verified" —
  // the honest tri-state (warning), so an unconfirmed artifact binding is not laundered.
  it('shows a signature-verified but digest-unbound verdict as a warning, not verified', async () => {
    api.listAdmissions.mockResolvedValue({
      items: [{ ...verifiedVerdict, artifact_verified: false }],
    })
    wrap(<AdmissionPanel entry={mcpEntry} />)

    expect(
      await screen.findByText('Signature verified, digest unbound'),
    ).toBeInTheDocument()
    expect(screen.queryByText('Attestation verified')).toBeNull()
  })

  // Property: a denied verdict is honest (danger) and shows the refusal reason verbatim
  // — the durable answer to "why was this refused?".
  it('renders a denied verdict as "not verified" with its reason', async () => {
    api.listAdmissions.mockResolvedValue({
      items: [
        {
          ...verifiedVerdict,
          signature_verified: false,
          artifact_verified: false,
          reason: 'no trusted key matched the bundle signature',
        },
      ],
    })
    wrap(<AdmissionPanel entry={mcpEntry} />)

    expect(await screen.findByText('Not verified')).toBeInTheDocument()
    expect(
      screen.getByText('no trusted key matched the bundle signature'),
    ).toBeInTheDocument()
  })

  // Property: no verdict is explained (a deny-closed entry cannot approve until admitted),
  // never a blank panel.
  it('explains the absence when no verdict is recorded', async () => {
    api.listAdmissions.mockResolvedValue({ items: [] })
    wrap(<AdmissionPanel entry={mcpEntry} />)
    expect(
      await screen.findByText('No admission verdict recorded'),
    ).toBeInTheDocument()
  })

  // Property: the panel is only for admission-gated catalog kinds (mcp/connector);
  // an agent entry gets no panel and never queries an admission endpoint.
  it('renders nothing for a non-gated kind', () => {
    wrap(<AdmissionPanel entry={agentEntry} />)
    expect(screen.queryByText('Attestation admission')).toBeNull()
    expect(api.listAdmissions).not.toHaveBeenCalled()
  })

  // Property: a connector entry queries the connector endpoint (kind-correct dispatch).
  it('queries the connector endpoint for a connector entry', async () => {
    api.listAdmissions.mockResolvedValue({ items: [] })
    wrap(<AdmissionPanel entry={connectorEntry} />)
    await waitFor(() =>
      expect(api.listAdmissions).toHaveBeenCalledWith('connector', 'e-conn'),
    )
  })

  // Property: the Admit action is admin-gated (catalog:entry:admin).
  it('hides the Admit action without catalog:entry:admin', async () => {
    authState.can = (p) => p !== 'catalog:entry:admin'
    api.listAdmissions.mockResolvedValue({ items: [] })
    wrap(<AdmissionPanel entry={mcpEntry} />)
    await screen.findByText('No admission verdict recorded')
    expect(
      screen.queryByRole('button', { name: /admit attestation/i }),
    ).toBeNull()
  })
})

describe('AdmissionPanel — admit action (honest result branching)', () => {
  // LOAD-BEARING property: a 200 with admitted:false ("recorded but did not satisfy the
  // policy") must surface as a WARNING with the verdict reason — NEVER a green success.
  // This fails if the admit is switched to usePrivilegedMutation (unconditional success).
  it('la escritura de admisión abre la ceremonia y NO acusa al operador', async () => {
    // Esta es la única escritura del residuo que destruye trabajo TECLEADO: el bundle que el
    // operador acaba de pegar. El `onError` a mano colapsaba los dos 403 en «no autorizado»,
    // así que perdía el bundle Y le mandaba a pedir un permiso que ya tiene.
    useStepUpStore.setState({ request: null })
    api.listAdmissions.mockResolvedValue({ items: [] })
    api.admitEntry.mockRejectedValue(
      new ApiError(403, 'step_up_required', 'assurance level too low'),
    )
    wrap(<AdmissionPanel entry={mcpEntry} />)

    await screen.findByText('No admission verdict recorded')
    await userEvent.click(
      screen.getByRole('button', { name: /admit attestation/i }),
    )
    const bundle = await screen.findByLabelText(/attestation bundle/i)
    fireEvent.change(bundle, { target: { value: '{"mediaType":"x"}' } })
    await userEvent.click(
      screen.getByRole('button', { name: /verify and admit/i }),
    )

    // Ancla POSITIVA: la ceremonia se pidió. Sin ella la ausencia de abajo es vacua.
    await waitFor(() =>
      expect(useStepUpStore.getState().request).not.toBeNull(),
    )
    expect(toast.warning).not.toHaveBeenCalled()

    // ⛔ Y LA REANUDACIÓN SE EJERCE, no se tipa. Aquí decía `toBeTypeOf('function')`, que es
    // una comprobación de TIPO: el contraste mutó el callback a `() => undefined` —una
    // función perfectamente válida que no reintenta nada— y la celda siguió verde. Lo que
    // hay que probar es que tras la ceremonia la escritura se repite CON EL BUNDLE TECLEADO,
    // que es el trabajo que esta reanudación existe para no perder.
    const { retry } = useStepUpStore.getState().request ?? {}
    expect(retry).toBeTypeOf('function')
    retry?.()
    await waitFor(() => expect(api.admitEntry).toHaveBeenCalledTimes(2))
    // El invariante es «las MISMAS variables» (admission-panel.tsx:299-305), no un texto:
    // el panel PARSEA el bundle antes de llamar, así que se compara la llamada entera con
    // la primera. Y se nombra el contenido tecleado para que un cambio de parseo se vea.
    expect(api.admitEntry.mock.calls[1]).toEqual(api.admitEntry.mock.calls[0])
    expect(api.admitEntry.mock.calls[1]?.[1]).toMatchObject({
      bundle: { mediaType: 'x' },
    })
  })

  it('warns (not succeeds) when the attestation is recorded but not admitted', async () => {
    api.listAdmissions.mockResolvedValue({ items: [] })
    api.admitEntry.mockResolvedValue({
      admitted: false,
      enforced: true,
      admission: {
        ...verifiedVerdict,
        signature_verified: false,
        reason: 'no trusted key matched',
      },
    })
    wrap(<AdmissionPanel entry={mcpEntry} />)

    await screen.findByText('No admission verdict recorded')
    await userEvent.click(
      screen.getByRole('button', { name: /admit attestation/i }),
    )
    const bundle = await screen.findByLabelText(/attestation bundle/i)
    fireEvent.change(bundle, { target: { value: '{"mediaType":"x"}' } })
    await userEvent.click(
      screen.getByRole('button', { name: /verify and admit/i }),
    )

    await waitFor(() => expect(toast.warning).toHaveBeenCalled())
    expect(toast.warning.mock.calls[0][1]).toMatchObject({
      description: 'no trusted key matched',
    })
    expect(toast.success).not.toHaveBeenCalled()
  })

  // Property: a genuine admit (admitted:true) toasts success and invalidates the verdict.
  it('reports success when the attestation is admitted', async () => {
    api.listAdmissions.mockResolvedValue({ items: [] })
    api.admitEntry.mockResolvedValue({
      admitted: true,
      enforced: true,
      admission: verifiedVerdict,
    })
    wrap(<AdmissionPanel entry={mcpEntry} />)

    await screen.findByText('No admission verdict recorded')
    await userEvent.click(
      screen.getByRole('button', { name: /admit attestation/i }),
    )
    const bundle = await screen.findByLabelText(/attestation bundle/i)
    fireEvent.change(bundle, { target: { value: '{"mediaType":"x"}' } })
    await userEvent.click(
      screen.getByRole('button', { name: /verify and admit/i }),
    )

    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    expect(toast.warning).not.toHaveBeenCalled()
    expect(api.admitEntry).toHaveBeenCalledWith('e-mcp', {
      bundle: { mediaType: 'x' },
    })
  })

  // Property: an invalid (non-JSON) bundle disables submit — never posts garbage.
  it('disables submit until the bundle is valid JSON', async () => {
    api.listAdmissions.mockResolvedValue({ items: [] })
    wrap(<AdmissionPanel entry={mcpEntry} />)
    await screen.findByText('No admission verdict recorded')
    await userEvent.click(
      screen.getByRole('button', { name: /admit attestation/i }),
    )
    const bundle = await screen.findByLabelText(/attestation bundle/i)
    fireEvent.change(bundle, { target: { value: 'not json' } })
    expect(
      await screen.findByText('The bundle must be valid JSON.'),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /verify and admit/i }),
    ).toBeDisabled()
    expect(api.admitEntry).not.toHaveBeenCalled()
  })
})
