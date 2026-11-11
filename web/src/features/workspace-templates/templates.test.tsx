// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { ApiError } from '@/lib/api/errors'
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

    // Both template names must appear in the list.
    expect(await screen.findByText('Secure Coding Session')).toBeInTheDocument()
    expect(screen.getByText('My Custom Template')).toBeInTheDocument()
  })

  it('lists your templates first and folds the built-ins in a closed group', async () => {
    api.list.mockResolvedValue({
      items: [builtinTemplate, customTemplate],
      has_more: false,
    })
    const user = userEvent.setup()
    wrap(<TemplatesView />)

    const yours = await screen.findByRole('heading', { name: 'Your templates' })
    const summary = screen.getByText('Built-in templates (1)')
    const fold = summary.closest('details')
    expect(fold).not.toBeNull()
    expect(
      yours.compareDocumentPosition(summary) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    // The built-in sits inside the closed fold; the person's own template does not.
    expect(fold).not.toHaveAttribute('open')
    expect(fold).toContainElement(screen.getByText('Secure Coding Session'))
    expect(fold).not.toContainElement(screen.getByText('My Custom Template'))
    // The groups replace the All / Built-in / Custom select.
    expect(screen.queryByRole('combobox')).toBeNull()

    await user.click(summary)
    expect(fold).toHaveAttribute('open')
  })

  it('teaches what a template is and offers the primary when the person has none', async () => {
    api.list.mockResolvedValue({ items: [builtinTemplate], has_more: false })
    wrap(<TemplatesView />)

    expect(
      await screen.findByText('You have no templates yet'),
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        'A template is a reusable starting point for new sessions.',
      ),
    ).toBeInTheDocument()
    // No heading over an empty group, and the built-ins stay reachable below it.
    expect(screen.queryByRole('heading', { name: 'Your templates' })).toBeNull()
    expect(
      screen.getByText('Built-in templates (1)').closest('details'),
    ).not.toHaveAttribute('open')
    expect(
      screen.getAllByRole('button', { name: 'New template' }),
    ).toHaveLength(2)
  })

  it('announces loading while the list is read', () => {
    api.list.mockReturnValue(new Promise(() => {}))
    wrap(<TemplatesView />)

    expect(screen.getByRole('status')).toHaveAttribute('data-state', 'loading')
  })

  it('a failed read says so in the pane and offers Retry', async () => {
    api.list.mockRejectedValue(new ApiError(500, 'internal', 'boom'))
    wrap(<TemplatesView />)

    expect(
      await screen.findByText('Could not load templates'),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
    expect(screen.queryByText('boom')).toBeNull()
  })

  it('a refused read names the page and who can grant it, without Retry', async () => {
    api.list.mockRejectedValue(new ApiError(403, 'forbidden', 'no'))
    wrap(<TemplatesView />)

    expect(
      await screen.findByText('You do not have access to Templates.'),
    ).toBeInTheDocument()
    expect(screen.getByText(/administrator/i)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /retry/i })).toBeNull()
  })
})

describe('TemplateCard', () => {
  it('reads the model and the first line of the instructions on one helper line', () => {
    wrap(
      <TemplateCard
        template={{
          ...customTemplate,
          body: {
            settings: {
              model: 'claude-opus',
              custom_instructions: 'Run the tests first.\nThen fix one bug.',
            },
          },
        }}
        onEdit={vi.fn()}
        onApply={vi.fn()}
      />,
    )

    expect(
      screen.getByText('claude-opus · Run the tests first.'),
    ).toBeInTheDocument()
    expect(screen.queryByText(customTemplate.description)).toBeNull()
    expect(screen.queryByText(/Then fix one bug/)).toBeNull()
  })

  it('falls back to the description when the template has no instructions', () => {
    wrap(
      <TemplateCard
        template={customTemplate}
        onEdit={vi.fn()}
        onApply={vi.fn()}
      />,
    )

    expect(screen.getByText(customTemplate.description)).toBeInTheDocument()
  })

  it('says Built-in once for a built-in', () => {
    wrap(
      <TemplateCard
        template={builtinTemplate}
        onEdit={vi.fn()}
        onApply={vi.fn()}
      />,
    )

    expect(screen.getAllByText('Built-in')).toHaveLength(1)
  })

  //"Apply to session" LAUNCHES a session under the template. Until then it
  // POSTed /apply — an endpoint that answered `applied:true, conflicts:[]` for every
  // template — and raised a toast, while nothing anywhere was applied to anything.
  it('apply asks for confirmation, then opens the launch dialog for this template', async () => {
    const onApply = vi.fn()
    api.apply.mockResolvedValue({ applied: true, conflicts: [] })
    wrap(
      <TemplateCard
        template={customTemplate}
        onEdit={vi.fn()}
        onApply={onApply}
      />,
    )

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
      unenforceable: [
        'hooks: the session launch does not provision hooks into the child',
      ],
    })
    wrap(
      <TemplateCard
        template={customTemplate}
        onEdit={vi.fn()}
        onApply={onApply}
      />,
    )

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
      expect(toast.error).toHaveBeenCalledWith(
        expect.stringContaining('hooks'),
      ),
    )
    expect(onApply).not.toHaveBeenCalled()
  })

  it('apply failure reports an error toast', async () => {
    api.apply.mockRejectedValue(new Error('boom'))
    wrap(
      <TemplateCard
        template={customTemplate}
        onEdit={vi.fn()}
        onApply={vi.fn()}
      />,
    )

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

  // A template that a department or group is assigned cannot be archived: the engine says why
  // (409), and the administrator reads it, not only that the archive failed.
  it('archiving an assigned template tells the administrator why', async () => {
    const why =
      'this template is assigned to a department or group; unassign it before archiving it'
    api.remove.mockRejectedValue(new ApiError(409, 'conflict', why))
    wrap(
      <TemplateCard
        template={customTemplate}
        onEdit={vi.fn()}
        onApply={vi.fn()}
      />,
    )

    await userEvent.click(screen.getByRole('button', { name: 'Edit' }))
    await userEvent.click(
      await screen.findByRole('menuitem', { name: /archive/i }),
    )
    await userEvent.click(
      await screen.findByRole('button', { name: 'Archive' }),
    )
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Failed to archive template.', {
        description: why,
      }),
    )
  })

  it('duplicate creates new template', async () => {
    api.duplicate.mockResolvedValue({ ...customTemplate, id: 'tpl-copy' })
    wrap(
      <TemplateCard
        template={customTemplate}
        onEdit={vi.fn()}
        onApply={vi.fn()}
      />,
    )

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
  it('preserves stored ai_attribution when renaming a template', async () => {
    api.update.mockResolvedValue(customTemplate)
    wrap(
      <TemplateEditor
        open={true}
        onOpenChange={vi.fn()}
        template={{
          ...customTemplate,
          body: { settings: { ai_attribution: 'off' } },
        }}
      />,
    )
    const user = userEvent.setup()
    const name = screen.getByLabelText(/^Name/)
    await user.clear(name)
    await user.type(name, 'Renamed template')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(api.update).toHaveBeenCalledWith(customTemplate.id, {
        name: 'Renamed template',
        description: customTemplate.description,
        body: { settings: { ai_attribution: 'off' } },
      }),
    )
  })

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

  it('saves and reloads the optional truncation protection', async () => {
    api.create.mockResolvedValue(customTemplate)
    const created = wrap(<TemplateEditor open={true} onOpenChange={vi.fn()} />)
    const label = 'Require truncation protection for read-only sessions'
    const choice = screen.getByRole('checkbox', { name: label })
    expect(choice).not.toBeChecked()
    await userEvent.type(
      screen.getByPlaceholderText('e.g. Secure coding session'),
      'Strict read only',
    )
    await userEvent.click(choice)
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(api.create).toHaveBeenCalled())
    expect(
      api.create.mock.calls[0][0].body.policies.require_truncate_protection,
    ).toBe(true)
    created.unmount()
    api.update.mockResolvedValue(customTemplate)
    wrap(
      <TemplateEditor
        open={true}
        onOpenChange={vi.fn()}
        template={{
          ...customTemplate,
          body: {
            policies: {
              require_truncate_protection: true,
              sessions_disabled: true,
              require_read_only_folders: true,
            },
          },
        }}
      />,
    )
    expect(screen.getByRole('checkbox', { name: label })).toBeChecked()
    await userEvent.click(screen.getByRole('checkbox', { name: label }))
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(api.update).toHaveBeenCalled())
    expect(
      api.update.mock.calls[0][1].body.policies?.require_truncate_protection ??
        false,
    ).toBe(false)
    expect(api.update.mock.calls[0][1].body.policies.sessions_disabled).toBe(
      true,
    )
    expect(
      api.update.mock.calls[0][1].body.policies.require_read_only_folders,
    ).toBe(true)
  })

  it('a template can let its sessions message each other (peers_rule, MC)', async () => {
    api.create.mockResolvedValue(customTemplate)
    wrap(<TemplateEditor open={true} onOpenChange={vi.fn()} />)
    await userEvent.type(
      screen.getByPlaceholderText('e.g. Secure coding session'),
      'Peers',
    )
    await userEvent.click(
      screen.getByRole('checkbox', {
        name: 'Sessions from this template can message each other',
      }),
    )
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(api.create).toHaveBeenCalledWith(
        expect.objectContaining({
          body: expect.objectContaining({ peers_rule: 'same-template' }),
        }),
      ),
    )
  })

  // The sheet asks a name and a description; every other section is a closed disclosure whose
  // summary counts what it holds (never a secret), and an invalid field inside a closed one
  // opens it and takes the focus instead of saving.
  it('opens on name and description; a field error opens its section', async () => {
    const user = userEvent.setup()
    wrap(<TemplateEditor open={true} onOpenChange={vi.fn()} />)
    expect(screen.getByLabelText(/^Name/)).toBeVisible()
    expect(screen.getByLabelText('Description')).toBeVisible()
    const duration = screen.getByLabelText('Max session duration (minutes)')
    for (const label of [
      'Model',
      'Secrets as environment variables',
      'Max session duration (minutes)',
      'Connectors',
    ])
      expect(screen.getByLabelText(label)).not.toBeVisible()
    // Sentence-case titles of one noun, and every closed section says what it holds even when empty.
    for (const [title, held] of [
      ['Hooks', 'None'],
      ['Settings', 'Defaults'],
      ['Secrets', 'None'],
      ['Connectors', 'None'],
      ['Policies', 'Defaults'],
    ])
      expect(screen.getByText(title).closest('summary')).toHaveTextContent(
        `${title}${held}`,
      )

    await user.click(screen.getByText('Secrets'))
    await user.type(
      screen.getByLabelText('Secrets as environment variables'),
      'GITHUB_TOKEN=env/github',
    )
    const secrets = screen.getByText('Secrets').closest('summary')!
    expect(secrets).toHaveTextContent('1 variable')
    expect(secrets).not.toHaveTextContent('env/github')

    await user.click(screen.getByText('Policies'))
    await user.type(duration, '0')
    await user.click(screen.getByText('Policies'))
    expect(duration).not.toBeVisible()
    await user.type(screen.getByLabelText(/^Name/), 'Short')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(duration).toBeVisible()
    expect(duration).toHaveFocus()
    expect(api.create).not.toHaveBeenCalled()
  })

  // The closed Settings summary never hides a mode that asks less than default behind
  // the model name; the mode reads as the tool takes it, in every locale.
  it('the closed settings summary names a mode above default beside the model', () => {
    wrap(
      <TemplateEditor
        open={true}
        onOpenChange={vi.fn()}
        template={{
          ...customTemplate,
          body: {
            settings: { model: 'x', permission_mode: 'bypassPermissions' },
          },
        }}
      />,
    )
    expect(screen.getByText('Settings').closest('summary')).toHaveTextContent(
      'SettingsModel: x · bypassPermissions',
    )
  })

  // A tool allowlist with no stated mode launches as dontAsk (the server derives it,
  // templateapply.go): the editor shows that mode in the select and the closed summary.
  it('shows the dontAsk an allowlist implies when the template states no mode', () => {
    wrap(
      <TemplateEditor
        open={true}
        onOpenChange={vi.fn()}
        template={{
          ...customTemplate,
          body: {
            settings: { model: 'x' },
            policies: { allowed_tools: ['Bash'] },
          },
        }}
      />,
    )
    expect(screen.getByText('Settings').closest('summary')).toHaveTextContent(
      'SettingsModel: x · dontAsk',
    )
    expect(screen.getByLabelText('Permission mode')).toHaveTextContent(
      'dontAsk',
    )
  })

  // Design first-hour check: a template names vault secrets (env/…) its sessions receive as
  // environment variables, by name only.
  it('saves the vault secrets a session receives, by name', async () => {
    api.create.mockResolvedValue(customTemplate)
    wrap(<TemplateEditor open={true} onOpenChange={vi.fn()} />)
    await userEvent.type(
      screen.getByPlaceholderText('e.g. Secure coding session'),
      'With GitHub',
    )
    await userEvent.type(
      screen.getByLabelText('Secrets as environment variables'),
      'GITHUB_TOKEN=env/github, AWS_KEY=env/aws',
    )
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(api.create).toHaveBeenCalled())
    expect(api.create.mock.calls[0][0].body.settings.secret_env).toEqual([
      { env: 'GITHUB_TOKEN', secret: 'env/github' },
      { env: 'AWS_KEY', secret: 'env/aws' },
    ])
  })
})
