// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import type { TemplateDTO } from './types'

// ---- Hoist mock objects (must appear before vi.mock calls) ----

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
  info: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

const api = vi.hoisted(() => ({
  list: vi.fn(),
  get: vi.fn(),
  create: vi.fn(),
  update: vi.fn(),
  remove: vi.fn(),
  duplicate: vi.fn(),
  apply: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, templatesApi: api }
})

// Components imported AFTER vi.mock declarations.
import { TemplatesView } from './templates-view'
import { TemplateCard } from './template-card'
import { TemplateEditor } from './template-editor'

// ---- Wrap helper: QueryClient + TooltipProvider (matches recordings/session-viewer pattern) ----

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider delayDuration={0}>{ui}</TooltipProvider>
    </QueryClientProvider>,
  )
}

// ---- Mock data ----

const builtinTemplate: TemplateDTO = {
  id: 'tpl-builtin',
  name: 'Secure Coding Session',
  description: 'Built-in template for secure coding environments.',
  version: 1,
  author: 'Olivares.AI',
  builtin: true,
  body: {},
  created_at: '2026-06-01T00:00:00Z',
  updated_at: '2026-06-01T00:00:00Z',
}

const customTemplate: TemplateDTO = {
  id: 'tpl-custom',
  name: 'My Custom Template',
  description: 'A user-defined custom template.',
  version: 2,
  author: 'user:alice',
  builtin: false,
  body: {},
  created_at: '2026-06-10T00:00:00Z',
  updated_at: '2026-06-10T00:00:00Z',
}

// ---- Lifecycle ----

beforeEach(() => {
  for (const fn of Object.values(api)) fn.mockReset()
  for (const fn of Object.values(toast)) fn.mockReset()
  // Default: return an empty list so background queries never reject.
  api.list.mockResolvedValue({ items: [], has_more: false })
})
afterEach(() => vi.clearAllMocks())

// ---- Tests ----

describe('TemplatesView', () => {
  it('renders built-in and custom templates', async () => {
    api.list.mockResolvedValue({
      items: [builtinTemplate, customTemplate],
      has_more: false,
    })
    wrap(<TemplatesView />)

    // Both template names must appear in the card grid.
    expect(await screen.findByText('Secure Coding Session')).toBeInTheDocument()
    expect(screen.getByText('My Custom Template')).toBeInTheDocument()
  })

  it('filters by builtin', async () => {
    wrap(<TemplatesView />)

    // Wait for initial list query.
    await waitFor(() => expect(api.list).toHaveBeenCalledTimes(1))

    // Open the filter Select and choose 'Built-in'.
    await userEvent.click(screen.getByRole('combobox'))
    await userEvent.click(await screen.findByRole('option', { name: 'Built-in' }))

    // The query must be re-issued with builtin: true.
    await waitFor(() =>
      expect(api.list).toHaveBeenCalledWith(
        expect.objectContaining({ builtin: true }),
      ),
    )
  })
})

describe('TemplateCard', () => {
  it('shows lock icon for built-in', () => {
    wrap(<TemplateCard template={builtinTemplate} onEdit={vi.fn()} onApply={vi.fn()} />)

    // The Lock SVG carries aria-label="Built-in" (from t('catalog.builtin')).
    expect(screen.getByLabelText('Built-in')).toBeInTheDocument()
  })

  //"Apply to session" LAUNCHES a session under the template. Until then it
  // POSTed /apply — an endpoint that answered `applied:true, conflicts:[]` for every
  // template — and raised a toast, while nothing anywhere was applied to anything.
  it('apply asks for confirmation, then opens the launch dialog for this template', async () => {
    const onApply = vi.fn()
    api.apply.mockResolvedValue({ applied: true, conflicts: [] })
    wrap(<TemplateCard template={customTemplate} onEdit={vi.fn()} onApply={onApply} />)

    await userEvent.click(screen.getByRole('button', { name: 'Edit' }))
    await userEvent.click(
      await screen.findByRole('menuitem', { name: /apply to session/i }),
    )
    // Nothing happens until the dialog is confirmed.
    expect(api.apply).not.toHaveBeenCalled()
    expect(onApply).not.toHaveBeenCalled()

    await userEvent.click(
      await screen.findByRole('button', { name: 'Apply to session' }),
    )
    await waitFor(() =>
      expect(api.apply).toHaveBeenCalledWith(customTemplate.id),
    )
    // The session the template governs is the one about to be launched.
    await waitFor(() => expect(onApply).toHaveBeenCalledWith(customTemplate))
  })

  it('a template the engine cannot enforce does not reach the launch dialog', async () => {
    const onApply = vi.fn()
    api.apply.mockResolvedValue({
      applied: false,
      conflicts: [],
      unenforceable: ['hooks: the session launch does not provision hooks into the child'],
    })
    wrap(<TemplateCard template={customTemplate} onEdit={vi.fn()} onApply={onApply} />)

    await userEvent.click(screen.getByRole('button', { name: 'Edit' }))
    await userEvent.click(
      await screen.findByRole('menuitem', { name: /apply to session/i }),
    )
    await userEvent.click(
      await screen.findByRole('button', { name: 'Apply to session' }),
    )
    // The reason is shown, and no launch is offered for a template whose terms the
    // launch would refuse — the operator is not sent to fill in a doomed form.
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(expect.stringContaining('hooks')),
    )
    expect(onApply).not.toHaveBeenCalled()
  })

  it('apply failure reports an error toast', async () => {
    api.apply.mockRejectedValue(new Error('boom'))
    wrap(<TemplateCard template={customTemplate} onEdit={vi.fn()} onApply={vi.fn()} />)

    await userEvent.click(screen.getByRole('button', { name: 'Edit' }))
    await userEvent.click(
      await screen.findByRole('menuitem', { name: /apply to session/i }),
    )
    await userEvent.click(
      await screen.findByRole('button', { name: 'Apply to session' }),
    )
    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    expect(toast.success).not.toHaveBeenCalled()
  })

  it('duplicate creates new template', async () => {
    api.duplicate.mockResolvedValue({ ...customTemplate, id: 'tpl-copy' })
    wrap(<TemplateCard template={customTemplate} onEdit={vi.fn()} onApply={vi.fn()} />)

    // The DropdownMenuTrigger button uses aria-label="Edit".
    await userEvent.click(screen.getByRole('button', { name: 'Edit' }))

    // Duplicate menuitem is rendered in the Radix portal.
    await userEvent.click(
      await screen.findByRole('menuitem', { name: /duplicate/i }),
    )

    await waitFor(() =>
      expect(api.duplicate).toHaveBeenCalledWith(
        customTemplate.id,
        expect.stringContaining(customTemplate.name),
      ),
    )
  })
})

describe('TemplateEditor', () => {
  it('validates name required', async () => {
    wrap(<TemplateEditor open={true} onOpenChange={vi.fn()} />)

    // Submit without entering a name.
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))

    // Field renders a FieldError with role="alert" when nameError is set.
    expect(screen.getByRole('alert')).toBeInTheDocument()
    expect(api.create).not.toHaveBeenCalled()
  })

  it('submits create request', async () => {
    api.create.mockResolvedValue(customTemplate)
    const onOpenChange = vi.fn()
    wrap(<TemplateEditor open={true} onOpenChange={onOpenChange} />)

    // Fill in the name input (placeholder comes from t('editor.namePlaceholder')).
    await userEvent.type(
      screen.getByPlaceholderText('e.g. Secure coding session'),
      'My New Template',
    )

    // Submit the form.
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(api.create).toHaveBeenCalledWith(
        expect.objectContaining({ name: 'My New Template' }),
      ),
    )
  })
})
