// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// the launch dialog is where a workspace template becomes a restriction. What
// matters here is what leaves the browser: a template ID and nothing else. The terms
// themselves (the tool allowlist, the duration ceiling, the DLP floor) are read from
// the stored template by the ENGINE, because a client that could post its own
// allowlist could post an empty one.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('./api', () => ({
  agentOpsApi: { createRun: vi.fn(), listWorkspaces: vi.fn() },
  agentOpsKeys: {
    all: (t: string | null) => ['agentops', t],
    workspaces: (t: string | null) => ['agentops', t, 'ws'],
  },
}))
vi.mock('@/features/workspace-templates/api', () => ({
  templatesApi: { list: vi.fn(), apply: vi.fn() },
  templatesKeys: {
    list: (t: string | null, p?: unknown) => ['tpl', t, 'list', p ?? null],
    detail: (t: string | null, id: string) => ['tpl', t, 'detail', id],
  },
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

import { templatesApi } from '@/features/workspace-templates/api'
import { agentOpsApi } from './api'
import { RunCreateDialog } from './run-create-dialog'

const tpl = {
  id: 'tpl-1',
  name: 'Security Audit',
  description: 'read-only',
  version: 1,
  author: 'system',
  builtin: true,
  body: {},
  created_at: '',
  updated_at: '',
}

function wrap(initialTemplateId?: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <RunCreateDialog
        open
        onOpenChange={vi.fn()}
        initialTemplateId={initialTemplateId}
      />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(agentOpsApi.listWorkspaces).mockResolvedValue({
    items: [],
    has_more: false,
  })
  vi.mocked(templatesApi.list).mockResolvedValue({
    items: [tpl],
    has_more: false,
  })
  vi.mocked(agentOpsApi.createRun).mockResolvedValue({} as never)
})

describe('RunCreateDialog — workspace templates', () => {
  it('mounts the picker badge from the loaded templates and has_more', async () => {
    vi.mocked(templatesApi.list).mockResolvedValue({
      items: Array.from({ length: 17 }, (_, i) => ({
        ...tpl,
        id: `tpl-${i}`,
        name: `Template ${i}`,
      })),
      has_more: true,
    })
    wrap()
    expect(
      await screen.findByText('Loaded 17 templates; there are more'),
    ).toBeVisible()
  })

  it('launches with the template id and posts no restriction of its own', async () => {
    vi.mocked(templatesApi.apply).mockResolvedValue({
      applied: true,
      conflicts: [],
    })
    wrap('tpl-1')

    await userEvent.click(
      screen.getByRole('button', { name: /create & launch/i }),
    )
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalled())

    // `as unknown as` y no un cast directo: `CreateRunRequest` es CERRADO y TypeScript rechaza la
    // conversión a un índice abierto — con razón, porque el caso pregunta por claves que ese tipo
    // NO declara. Ensanchar por `unknown` dice que se inspecciona el cuerpo REAL del cable.
    const body = vi.mocked(agentOpsApi.createRun).mock
      .calls[0][0] as unknown as Record<string, unknown>
    expect(body.template_id).toBe('tpl-1')
    // The engine owns the merge. Anything restriction-shaped in this body would be a
    // client-side restriction, which is not one.
    expect(body).not.toHaveProperty('allowed_tools')
    expect(body).not.toHaveProperty('max_session_duration_minutes')
  })

  it('sends no template_id when none is chosen', async () => {
    wrap()
    await userEvent.click(
      screen.getByRole('button', { name: /create & launch/i }),
    )
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalled())

    // `as unknown as` y no un cast directo: `CreateRunRequest` es CERRADO y TypeScript rechaza la
    // conversión a un índice abierto — con razón, porque el caso pregunta por claves que ese tipo
    // NO declara. Ensanchar por `unknown` dice que se inspecciona el cuerpo REAL del cable.
    const body = vi.mocked(agentOpsApi.createRun).mock
      .calls[0][0] as unknown as Record<string, unknown>
    expect(body).not.toHaveProperty('template_id')
    expect(templatesApi.apply).not.toHaveBeenCalled()
  })

  it('blocks the launch and names the reason when the engine cannot enforce the template', async () => {
    vi.mocked(templatesApi.apply).mockResolvedValue({
      applied: false,
      conflicts: [],
      unenforceable: [
        'hooks: the session launch does not provision hooks into the child',
      ],
    })
    wrap('tpl-1')

    // The refusal is shown BEFORE the launch, not as a surprise 422 after pressing it.
    expect(
      await screen.findByText(/does not provision hooks/i),
    ).toBeInTheDocument()
    const submit = screen.getByRole('button', { name: /create & launch/i })
    await waitFor(() => expect(submit).toBeDisabled())
    expect(agentOpsApi.createRun).not.toHaveBeenCalled()
  })

  it('warns that a pinned dontAsk launch is CRITICAL, reading the merged mode not the form', async () => {
    // A template with a tool allow-list pins dontAsk — a privileged launch that takes
    // human approval and is recorded. The form still says "default", so a warning computed
    // from the form field would stay silent for exactly the launch that needs it.
    vi.mocked(templatesApi.apply).mockResolvedValue({
      applied: true,
      conflicts: [],
      merged: { permission_mode: 'dontAsk', allowed_tools: ['Read'] },
    })
    wrap('tpl-1')
    expect(
      await screen.findByText(/human approval|approval|recorded/i),
    ).toBeInTheDocument()
  })

  it("shows which of the operator's own choices the template overrides", async () => {
    vi.mocked(templatesApi.apply).mockResolvedValue({
      applied: true,
      conflicts: [
        {
          field: 'permission_mode',
          old_value: 'bypassPermissions',
          new_value: 'dontAsk',
        },
      ],
    })
    wrap('tpl-1')

    // Scoped to the override list: "dontAsk" is also a permission-mode option, so a
    // bare text match would pass on the select and prove nothing.
    const field = await screen.findByText('permission_mode')
    const entry = field.closest('li')
    expect(entry).not.toBeNull()
    expect(entry).toHaveTextContent('bypassPermissions')
    expect(entry).toHaveTextContent('dontAsk')
    // An override is not a refusal: the launch stays available.
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: /create & launch/i }),
      ).not.toBeDisabled(),
    )
  })
})
