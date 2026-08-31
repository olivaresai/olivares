// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { AdmissionPolicy } from './types'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
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
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, catalogApi: { ...actual.catalogApi, ...api } }
})

import { AdmissionPolicyTab } from './admission-policy'
import { policyConfigured } from './types'
import './i18n'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const ROOT = '-----BEGIN CERTIFICATE-----\nAAA\n-----END CERTIFICATE-----'

/** Both cards must have finished loading before indexing: findAllByRole returns as soon as
 *  ONE button exists, so `edit[1]` would be undefined and the click a silent no-op. */
async function botonesEditar() {
  await waitFor(() =>
    expect(
      screen.getAllByRole('button', { name: /edit policy/i }),
    ).toHaveLength(2),
  )
  return screen.getAllByRole('button', { name: /edit policy/i })
}

/** What the engine writes when NO policy exists: a map, not the DTO — `configured:false`
 *  plus a SYNTHETIC note (`modules/catalog/mcpadmission.go`). */
const unconfigured: AdmissionPolicy = {
  require_signed: false,
  require_subject_digest: false,
  configured: false,
  note: 'no MCP-entry admission policy configured; entries are admitted without signature verification',
}

/** A SAVED policy. Note what is NOT here: `configured`. The engine omits it once a policy
 *  exists, so any check shaped `if (!p.configured)` reads this as unconfigured. */
const saved: AdmissionPolicy = {
  require_signed: true,
  require_subject_digest: true,
  trusted_roots: [ROOT],
  allowed_identities: ['ci@olivares.ai'],
  allowed_issuers: ['https://token.actions.githubusercontent.com'],
  allowed_predicates: ['https://slsa.dev/provenance/v1'],
  note: 'signed by the platform CI',
  attested_by: 'user:1234',
  attested_at: '2026-08-01T10:00:00Z',
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = () => true
  api.admissionPolicy.mockResolvedValue(unconfigured)
  api.putAdmissionPolicy.mockResolvedValue(saved)
})

describe('policyConfigured — absence means CONFIGURED', () => {
  it('reads the three shapes the engine can send', () => {
    expect(policyConfigured(unconfigured)).toBe(false)
    expect(policyConfigured(saved)).toBe(true) // absent → configured
    expect(policyConfigured(undefined)).toBe(false)
  })
})

describe('AdmissionPolicyTab — the two kinds are independent', () => {
  it('reads BOTH policies, one per kind', async () => {
    wrap(<AdmissionPolicyTab />)
    await waitFor(() => expect(api.admissionPolicy).toHaveBeenCalledTimes(2))
    const kinds = api.admissionPolicy.mock.calls.map((c) => c[0]).sort()
    expect(kinds).toEqual(['connector', 'mcp'])
  })

  it('an unconfigured policy says everything is admitted, not "secure"', async () => {
    wrap(<AdmissionPolicyTab />)
    // findAllByText resolves on the FIRST match, so it cannot assert a count: the second
    // card is still loading. Wait for the number itself.
    await waitFor(() =>
      expect(screen.getAllByText(/every entry is admitted/i)).toHaveLength(2),
    )
  })

  // The pair is the point: if both fixtures rendered the same text, `configured` would not
  // be load-bearing and a wrong reading of it could not be seen from here.
  it('a SAVED policy (no `configured` field) does NOT read as unconfigured', async () => {
    api.admissionPolicy.mockResolvedValue(saved)
    wrap(<AdmissionPolicyTab />)
    await waitFor(() =>
      expect(screen.getAllByText(/^Enforcing$/)).toHaveLength(2),
    )
    expect(screen.queryByText(/every entry is admitted/i)).toBeNull()
  })

  it('hides the editor without catalog:entry:admin', async () => {
    authState.can = (p) => p !== 'catalog:entry:admin'
    wrap(<AdmissionPolicyTab />)
    await screen.findAllByText(/every entry is admitted/i)
    expect(screen.queryByRole('button', { name: /edit policy/i })).toBeNull()
  })
})

describe('PolicyForm — the write body is built, never spread', () => {
  it('never sends `configured` or the synthetic note from the stub', async () => {
    const user = userEvent.setup()
    wrap(<AdmissionPolicyTab />)
    const edit = await botonesEditar()
    await user.click(edit[0])

    // Adding a root is not a tightening, so it saves without confirmation.
    fireEvent.change(await screen.findByLabelText(/trusted roots/i), {
      target: { value: ROOT },
    })
    await user.click(screen.getByRole('button', { name: /^save$/i }))

    await waitFor(() => expect(api.putAdmissionPolicy).toHaveBeenCalledTimes(1))
    const [kind, body] = api.putAdmissionPolicy.mock.calls[0]
    expect(kind).toBe('mcp')
    expect(body).not.toHaveProperty('configured')
    expect(body).not.toHaveProperty('note')
    expect(body.require_signed).toBe(false)
    expect(body.require_subject_digest).toBe(false)
    expect(body.trusted_roots).toEqual([ROOT])
  })

  it('sends predicates as one entry per line', async () => {
    const user = userEvent.setup()
    wrap(<AdmissionPolicyTab />)
    const edit = await botonesEditar()
    await user.click(edit[0])
    fireEvent.change(await screen.findByLabelText(/allowed predicates/i), {
      target: { value: 'https://slsa.dev/provenance/v1\n\n  spdx  \n' },
    })
    await user.click(screen.getByRole('button', { name: /^save$/i }))

    await waitFor(() => expect(api.putAdmissionPolicy).toHaveBeenCalledTimes(1))
    expect(api.putAdmissionPolicy.mock.calls[0][1].allowed_predicates).toEqual([
      'https://slsa.dev/provenance/v1',
      'spdx',
    ])
  })

  it('blocks enforcement with no anchor (client mirror of the engine 400)', async () => {
    const user = userEvent.setup()
    wrap(<AdmissionPolicyTab />)
    const edit = await botonesEditar()
    await user.click(edit[0])
    await user.click(await screen.findByLabelText(/require signed entries/i))

    expect(
      screen.getByText(/needs at least one trusted root or key/i),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^save$/i })).toBeDisabled()
    expect(api.putAdmissionPolicy).not.toHaveBeenCalled()
  })

  it('blocks a PRIVATE KEY pasted into the public anchor field', async () => {
    const user = userEvent.setup()
    wrap(<AdmissionPolicyTab />)
    const edit = await botonesEditar()
    await user.click(edit[0])
    fireEvent.change(await screen.findByLabelText(/trusted keys/i), {
      target: {
        value: '-----BEGIN PRIVATE KEY-----\nAAA\n-----END PRIVATE KEY-----',
      },
    })
    expect(
      screen.getByText(/trust material must be public/i),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^save$/i })).toBeDisabled()
  })

  it('confirms before a tightening change, and does not PUT until confirmed', async () => {
    const user = userEvent.setup()
    // Already enforcing without the digest requirement: turning it on tightens.
    api.admissionPolicy.mockResolvedValue({
      require_signed: true,
      require_subject_digest: false,
      trusted_roots: [ROOT],
    } as AdmissionPolicy)
    wrap(<AdmissionPolicyTab />)
    const edit = await botonesEditar()
    await user.click(edit[0])
    await user.click(await screen.findByLabelText(/require subject digest/i))
    await user.click(screen.getByRole('button', { name: /^save$/i }))

    expect(await screen.findByText(/confirm trust change/i)).toBeInTheDocument()
    expect(api.putAdmissionPolicy).not.toHaveBeenCalled()
  })

  it('the CONNECTOR card writes the connector policy, not the MCP one', async () => {
    const user = userEvent.setup()
    wrap(<AdmissionPolicyTab />)
    const edit = await botonesEditar()
    await user.click(edit[1])
    fireEvent.change(await screen.findByLabelText(/trusted roots/i), {
      target: { value: ROOT },
    })
    await user.click(screen.getByRole('button', { name: /^save$/i }))

    await waitFor(() => expect(api.putAdmissionPolicy).toHaveBeenCalledTimes(1))
    expect(api.putAdmissionPolicy.mock.calls[0][0]).toBe('connector')
  })
})
