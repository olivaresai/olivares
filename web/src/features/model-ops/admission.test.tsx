// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Admission tab tests — the security core. Each case pins one dangerous invariant:
// the three policy states are distinct; the PUT body is built atomically and NEVER
// carries the GET stub's `configured` into the fail-closed decoder; enforcement without
// an anchor is blocked in the client; the ?verified filter omits the param for "All";
// and a 200 with admitted:false is a WARNING with the verbatim reason, not a success.
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
import type { AdmissionPolicy, ModelAdmission } from '@/features/models/types'

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
  admissionPolicy: vi.fn(),
  putAdmissionPolicy: vi.fn(),
  modelAdmissions: vi.fn(),
  admitVersion: vi.fn(),
}))
vi.mock('@/features/models/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/models/api')>()
  return { ...actual, modelsApi: api }
})

import { modelsKeys } from '@/features/models/api'
import { AdmissionTab } from './admission'
import { AdmitDialog } from './shared'
import './i18n'
import '@/features/_intel'

const unconfigured: AdmissionPolicy = {
  require_signed: false,
  require_artifact_digests: false,
  configured: false,
  note: 'observe mode — no policy configured',
}
const enforcePolicy: AdmissionPolicy = {
  require_signed: true,
  require_artifact_digests: false,
  trusted_roots: [
    '-----BEGIN CERTIFICATE-----\nAAA\n-----END CERTIFICATE-----',
  ],
}

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  authState.can = () => true
  api.admissionPolicy.mockReset()
  api.putAdmissionPolicy.mockReset()
  api.modelAdmissions.mockReset()
  api.admitVersion.mockReset()
  toast.success.mockReset()
  toast.warning.mockReset()
  toast.error.mockReset()
  api.modelAdmissions.mockResolvedValue({ items: [] })
  api.putAdmissionPolicy.mockResolvedValue(enforcePolicy)
})
afterEach(() => vi.clearAllMocks())

describe('PolicySummary — three distinct states', () => {
  it('shows the unconfigured/observe state and the empty derived mode', async () => {
    api.admissionPolicy.mockResolvedValue(unconfigured)
    wrap(<AdmissionTab />)
    expect(
      await screen.findByText(/observe mode — not configured/i),
    ).toBeInTheDocument()
    expect(screen.getByText(/admits nothing/i)).toBeInTheDocument()
  })

  it('shows the enforce state when require_signed is on', async () => {
    api.admissionPolicy.mockResolvedValue(enforcePolicy)
    wrap(<AdmissionTab />)
    expect(await screen.findByText(/enforce mode/i)).toBeInTheDocument()
  })
})

describe('PolicyForm — atomic, dedicated PUT DTO', () => {
  // The crown-jewel correctness test: editing the UNCONFIGURED stub and saving must send
  // a body WITHOUT `configured` (or it would 400 the DisallowUnknownFields decoder).
  it('never sends `configured` from the GET stub on PUT', async () => {
    const user = userEvent.setup()
    api.admissionPolicy.mockResolvedValue(unconfigured)
    wrap(<AdmissionTab />)

    await user.click(
      await screen.findByRole('button', { name: /edit policy/i }),
    )
    // Add a trust root (no enforcement change, no anchor removed → saves directly).
    const roots = await screen.findByLabelText(/trusted roots/i)
    fireEvent.change(roots, {
      target: {
        value: '-----BEGIN CERTIFICATE-----\nZZZ\n-----END CERTIFICATE-----',
      },
    })
    await user.click(screen.getByRole('button', { name: /^save$/i }))

    await waitFor(() => expect(api.putAdmissionPolicy).toHaveBeenCalledTimes(1))
    const body = api.putAdmissionPolicy.mock.calls[0][0]
    expect(body).not.toHaveProperty('configured')
    expect(body).not.toHaveProperty('note') // synthetic stub note not carried
    expect(body.require_signed).toBe(false)
    expect(body.trusted_roots).toEqual([
      '-----BEGIN CERTIFICATE-----\nZZZ\n-----END CERTIFICATE-----',
    ])
  })

  it('blocks enforcement with no trust anchor (client mirror of the 400)', async () => {
    const user = userEvent.setup()
    api.admissionPolicy.mockResolvedValue(unconfigured)
    wrap(<AdmissionTab />)
    await user.click(
      await screen.findByRole('button', { name: /edit policy/i }),
    )
    // Turn enforcement on with no root/key configured.
    await user.click(await screen.findByLabelText(/require signed models/i))
    expect(
      screen.getByText(/enforcement needs at least one trusted root or key/i),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^save$/i })).toBeDisabled()
  })

  it('blocks save when a PEM textarea has residue outside its blocks', async () => {
    const user = userEvent.setup()
    api.admissionPolicy.mockResolvedValue(unconfigured)
    wrap(<AdmissionTab />)
    await user.click(
      await screen.findByRole('button', { name: /edit policy/i }),
    )
    const roots = await screen.findByLabelText(/trusted roots/i)
    // One complete block plus a truncated second block that splitPemBlocks would drop.
    fireEvent.change(roots, {
      target: {
        value:
          '-----BEGIN CERTIFICATE-----\nAAA\n-----END CERTIFICATE-----\n-----BEGIN CERTIFICATE-----\nBBB',
      },
    })
    expect(
      screen.getByText(/only whole begin…end blocks are saved/i),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^save$/i })).toBeDisabled()
  })

  // The two remaining rules of the client mirror. Measured 2026-08-19: mutating either one
  // in `@/lib/admission/policy` left this battery GREEN — the anchor and residue rules had
  // cells, these two did not, so half the mirror was decorative.
  it('blocks save when a PRIVATE KEY is pasted into a public anchor field', async () => {
    const user = userEvent.setup()
    api.admissionPolicy.mockResolvedValue(unconfigured)
    wrap(<AdmissionTab />)
    await user.click(
      await screen.findByRole('button', { name: /edit policy/i }),
    )
    const keys = await screen.findByLabelText(/trusted keys/i)
    fireEvent.change(keys, {
      target: {
        value: '-----BEGIN PRIVATE KEY-----\nAAA\n-----END PRIVATE KEY-----',
      },
    })
    expect(
      screen.getByText(/trust material must be public/i),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^save$/i })).toBeDisabled()
  })

  it('blocks save when an identity is set with no issuer', async () => {
    const user = userEvent.setup()
    api.admissionPolicy.mockResolvedValue(unconfigured)
    wrap(<AdmissionTab />)
    await user.click(
      await screen.findByRole('button', { name: /edit policy/i }),
    )
    const identities = await screen.findByLabelText(/allowed identities/i)
    fireEvent.change(identities, { target: { value: 'ci@olivares.ai' } })
    expect(
      screen.getByText(/must be set together, or neither/i),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^save$/i })).toBeDisabled()
  })

  it('confirms before tightening an enforced policy with artifact digests', async () => {
    const user = userEvent.setup()
    // Already enforcing (require_signed + a root); enabling digests is a tightening.
    api.admissionPolicy.mockResolvedValue(enforcePolicy)
    wrap(<AdmissionTab />)
    await user.click(
      await screen.findByRole('button', { name: /edit policy/i }),
    )
    await user.click(await screen.findByLabelText(/require artifact digests/i))
    await user.click(screen.getByRole('button', { name: /^save$/i }))
    // A dangerous-change confirmation intercepts the save — no PUT yet.
    expect(await screen.findByText(/confirm trust change/i)).toBeInTheDocument()
    expect(api.putAdmissionPolicy).not.toHaveBeenCalled()
  })
})

describe('Verdict inventory — the ?verified filter', () => {
  it('omits the param for "All" and sends it for a real filter', async () => {
    const user = userEvent.setup()
    api.admissionPolicy.mockResolvedValue(unconfigured)
    wrap(<AdmissionTab />)
    // Initial load (All) → no verified param. El `limit` SÍ va siempre: es el techo del
    // motor, no un filtro, y sin él la lista sale recortada a 100 sin decirlo.
    await waitFor(() => expect(api.modelAdmissions).toHaveBeenCalled())
    expect(api.modelAdmissions).toHaveBeenLastCalledWith({ limit: 1000 })

    await user.click(screen.getByRole('combobox'))
    // Radix Select renders both a hidden native <select> and the popup listbox — scope
    // the option lookup to the listbox so it is unambiguous.
    const listbox = await screen.findByRole('listbox')
    // Exact name — "Unverified only" also contains "verified only".
    await user.click(
      within(listbox).getByRole('option', { name: 'Verified only' }),
    )
    await waitFor(() =>
      expect(api.modelAdmissions).toHaveBeenLastCalledWith({
        verified: true,
        limit: 1000,
      }),
    )
  })

  /**
   * ⛔ EL AVISO DE RECORTE, EN LAS DOS DIRECCIONES. `handleListAdmissions` publica `has_more`
   * y la consola lo tiraba: la pantalla enseñaba las cien primeras filas por `id ASC` y se
   * leía «éstos son los veredictos». La casilla negativa es la que impide que el aviso salga
   * cuando la lista está completa — un aviso que aparece siempre no informa de nada.
   */
  /**
   * ⛔ LA GUARDA DEL ERROR, con dato viejo sembrado: sin él la casilla pasaría sin llegar a la
   * guarda, porque un rechazo desde el primer intento deja `data` en `undefined`.
   */
  it('un refetch fallido retira el aviso aunque el dato viejo diga has_more', async () => {
    const clave = modelsKeys.modelAdmissions('t1', { limit: 1000 })
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: 0 } },
    })
    qc.setQueryData(clave, { items: [], has_more: true })
    api.admissionPolicy.mockResolvedValue(unconfigured)
    api.modelAdmissions.mockRejectedValue(new Error('boom'))
    render(
      <QueryClientProvider client={qc}>
        <AdmissionTab />
      </QueryClientProvider>,
    )
    await waitFor(() => expect(api.modelAdmissions).toHaveBeenCalled())
    await waitFor(() => expect(qc.getQueryState(clave)?.error).toBeTruthy())
    expect(
      screen.queryByText(/Loaded \d+ verdicts; there are more/i),
    ).toBeNull()
  })

  it('declara el recorte cuando el motor dice has_more, y no cuando no', async () => {
    api.admissionPolicy.mockResolvedValue(unconfigured)
    api.modelAdmissions.mockResolvedValue({ items: [], has_more: true })
    wrap(<AdmissionTab />)
    expect(
      await screen.findByText(/Loaded \d+ verdicts; there are more/i),
    ).toBeVisible()

    cleanup()
    api.modelAdmissions.mockResolvedValue({ items: [], has_more: false })
    wrap(<AdmissionTab />)
    await waitFor(() => expect(api.modelAdmissions).toHaveBeenCalled())
    expect(
      screen.queryByText(/Loaded \d+ verdicts; there are more/i),
    ).toBeNull()
  })
})

describe('AdmitDialog — a 200 admitted:false is a warning, not a success', () => {
  const verdict: ModelAdmission = {
    id: 'a1',
    version_ref: 'v1',
    signature_verified: false,
    artifact_verified: false,
    tlog_present: false,
    tlog_verified: false,
    resource_count: 0,
    reason: 'no trusted key matched the signature',
  }

  it('warns with the recorded reason when the policy is not satisfied', async () => {
    const user = userEvent.setup()
    api.admitVersion.mockResolvedValue({
      admitted: false,
      enforced: true,
      admission: verdict,
    })
    wrap(
      <AdmitDialog
        versionId="v1"
        versionLabel="v1"
        open
        onOpenChange={() => {}}
      />,
    )
    const bundle = await screen.findByLabelText(/signature bundle/i)
    fireEvent.change(bundle, { target: { value: '{"mediaType":"x"}' } })
    await user.click(screen.getByRole('button', { name: /^admit$/i }))

    await waitFor(() => expect(toast.warning).toHaveBeenCalledTimes(1))
    expect(toast.warning.mock.calls[0][1]).toMatchObject({
      description: 'no trusted key matched the signature',
    })
    expect(toast.success).not.toHaveBeenCalled()
  })

  it('sends a body with only the fields the operator supplied', async () => {
    const user = userEvent.setup()
    api.admitVersion.mockResolvedValue({
      admitted: true,
      enforced: true,
      admission: { ...verdict, signature_verified: true },
    })
    wrap(
      <AdmitDialog
        versionId="v1"
        versionLabel="v1"
        open
        onOpenChange={() => {}}
      />,
    )
    const bundle = await screen.findByLabelText(/signature bundle/i)
    fireEvent.change(bundle, { target: { value: '{"mediaType":"x"}' } })
    await user.click(screen.getByRole('button', { name: /^admit$/i }))

    await waitFor(() => expect(api.admitVersion).toHaveBeenCalledTimes(1))
    const [id, body] = api.admitVersion.mock.calls[0]
    expect(id).toBe('v1')
    expect(body).toEqual({ bundle: { mediaType: 'x' } })
    // no empty resolved_digests / model_ref / aibom_ref / note
    expect(body).not.toHaveProperty('note')
    expect(toast.success).toHaveBeenCalled()
  })

  it('does not admit on a malformed (non-object) bundle', async () => {
    wrap(
      <AdmitDialog
        versionId="v1"
        versionLabel="v1"
        open
        onOpenChange={() => {}}
      />,
    )
    const bundle = await screen.findByLabelText(/signature bundle/i)
    fireEvent.change(bundle, { target: { value: '"just a string"' } })
    expect(screen.getByText(/expected a json object/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^admit$/i })).toBeDisabled()
  })
})
