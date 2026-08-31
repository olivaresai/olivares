// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// RecordingPolicyPanel tests. Each case pins ONE load-bearing property of the
// recording-policy authoring console (noted in a comment), following the mocking
// style of recordings.test.tsx: toast, the auth context (configurable `can`), and
// the './api' module are mocked; the panel is rendered in isolation.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { recordingKeys } from './api'
import type { RecordingConfig } from './types'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
  info: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

// `can` is mutable so a single test can strip the config permission.
const authState = vi.hoisted(() => ({
  activeTenant: 't1' as string | null,
  can: (_permission: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({
  getConfig: vi.fn(),
  updateConfig: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, recordingApi: api }
})

import { RecordingPolicyPanel } from './recording-config-panel'

const config: RecordingConfig = {
  namespaces: ['governance', 'identity'],
  breakglass_always: true,
  consent: 'required',
  idle_seconds: 900,
  retention_days: 365,
  // Forced false server-side: retention is a tag, not a purge.
  retention_enforced: false,
  ai_summaries: true,
}

function renderPanel(ui: ReactNode = <RecordingPolicyPanel />) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const utils = render(
    <QueryClientProvider client={qc}>{ui}</QueryClientProvider>,
  )
  return { qc, ...utils }
}

beforeEach(() => {
  api.getConfig.mockReset()
  api.updateConfig.mockReset()
  toast.success.mockReset()
  toast.error.mockReset()
  toast.warning.mockReset()
  toast.info.mockReset()
  authState.can = () => true
  authState.activeTenant = 't1'
  api.getConfig.mockResolvedValue({ ...config })
  api.updateConfig.mockResolvedValue({ ...config })
})
afterEach(() => vi.clearAllMocks())

describe('RecordingPolicyPanel — recording-policy authoring', () => {
  // Property: getConfig populates namespaces/consent/idle/retention/ai_summaries.
  it('loads the persisted policy into the editor', async () => {
    renderPanel()

    // namespaces render as chips
    expect(await screen.findByText('governance')).toBeInTheDocument()
    expect(screen.getByText('identity')).toBeInTheDocument()
    // idle + retention numeric inputs carry the loaded values
    expect(screen.getByLabelText(/idle-seal timeout/i)).toHaveValue(900)
    expect(screen.getByLabelText(/retention class/i)).toHaveValue(365)
    // consent = required → its toggle is on; ai_summaries = true → on
    expect(
      screen.getByRole('switch', { name: /require acknowledgement/i }),
    ).toBeChecked()
    expect(
      screen.getByRole('switch', { name: /ai-derived summaries/i }),
    ).toBeChecked()
  })

  // Property: updateConfig fires with the edited body and invalidates config key.
  it('saves the edited body and invalidates the config query key', async () => {
    const { qc } = renderPanel()
    const invalidate = vi.spyOn(qc, 'invalidateQueries')

    const idle = await screen.findByLabelText(/idle-seal timeout/i)
    await userEvent.clear(idle)
    await userEvent.type(idle, '1200')
    await userEvent.click(screen.getByRole('button', { name: /save policy/i }))

    await waitFor(() =>
      expect(api.updateConfig).toHaveBeenCalledWith({
        namespaces: ['governance', 'identity'],
        consent: 'required',
        idle_seconds: 1200,
        retention_days: 365,
        ai_summaries: true,
      }),
    )
    await waitFor(() =>
      expect(invalidate).toHaveBeenCalledWith({
        queryKey: recordingKeys.config('t1'),
      }),
    )
  })

  // Property: breakglass_always is an immutable indicator, never a control.
  it('renders break-glass as a locked indicator with no editable control', async () => {
    renderPanel()

    // The honest locked copy renders…
    expect(await screen.findByText(/always recorded/i)).toBeInTheDocument()
    expect(screen.getByText(/always on/i)).toBeInTheDocument()
    // …and there is NO toggle/checkbox to turn break-glass recording off.
    expect(screen.queryByRole('switch', { name: /break-?glass/i })).toBeNull()
    expect(screen.queryByRole('checkbox', { name: /break-?glass/i })).toBeNull()
  })

  // Property: retention_enforced=false is tag-only, not an enforced purge.
  it('presents retention as a classification tag, not an auto-delete', async () => {
    renderPanel()
    await screen.findByLabelText(/retention class/i)

    // tag-only badge (exact) + the honest "never purged" copy
    expect(screen.getByText('Tag only')).toBeInTheDocument()
    expect(screen.getByText(/never purged/i)).toBeInTheDocument()
    // It must NOT promise an auto-delete the backend does not perform.
    expect(screen.queryByText(/auto-?delete/i)).toBeNull()
    expect(screen.queryByText(/deleted after/i)).toBeNull()
  })

  // Property (negative): saving triggers no AAL3/WebAuthn step-up ceremony.
  it('saves without any AAL3/WebAuthn step-up dialog', async () => {
    renderPanel()

    const ai = await screen.findByRole('switch', {
      name: /ai-derived summaries/i,
    })
    await userEvent.click(ai) // dirty the form — no step-up should intervene
    await userEvent.click(screen.getByRole('button', { name: /save policy/i }))

    await waitFor(() => expect(api.updateConfig).toHaveBeenCalled())
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(
      screen.queryByText(
        /webauthn|step-?up|hardware-verified|aal3|passkey|\bpiv\b/i,
      ),
    ).toBeNull()
  })

  // Property: without recording:config:admin the editor is not offered, and save
  // is unreachable — the read is not even attempted (no doomed-to-403 form).
  it('offers no editor without recording:config:admin', async () => {
    authState.can = () => false
    renderPanel()

    expect(
      await screen.findByText(/requires recording:config:admin/i),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /save policy/i })).toBeNull()
    expect(api.getConfig).not.toHaveBeenCalled()
  })

  // Property: an unknown-namespace 400 is surfaced verbatim, not swallowed (there
  // is no catalog route to pre-validate against, so the server is authoritative).
  it('surfaces an unknown-namespace 400 verbatim', async () => {
    api.updateConfig.mockRejectedValue(
      new ApiError(
        400,
        'invalid_argument',
        'unknown module namespace notamodule; recorded namespaces must name mounted modules',
      ),
    )
    renderPanel()

    const add = await screen.findByLabelText(/add a recorded namespace/i)
    await userEvent.type(add, 'notamodule')
    await userEvent.click(screen.getByRole('button', { name: /^add$/i }))
    // Well-shaped, so accepted client-side — the engine is the authority on whether
    // it names a mounted module.
    expect(await screen.findByText('notamodule')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: /save policy/i }))
    expect(
      await screen.findByText(/unknown module namespace notamodule/i),
    ).toBeInTheDocument()
  })

  // Property: an invalid retention value shows a TRANSLATED bounds message, never
  // the raw i18n key. i18next returns the key string on a missing key, so a missing
  // policy.retention.error would leak "policy.retention.error" into a role=alert.
  it('shows a translated error for an invalid retention value, not the raw key', async () => {
    api.getConfig.mockResolvedValue(config)
    renderPanel()

    const retention = await screen.findByLabelText(/retention class/i)
    await userEvent.clear(retention)
    await userEvent.type(retention, '5000') // above the 3650 ceiling

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/between 1 and 3650/i)
    // The raw key must never reach the user.
    expect(alert).not.toHaveTextContent('policy.retention.error')
  })
})
