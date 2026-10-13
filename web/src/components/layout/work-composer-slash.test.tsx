// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The "/" menu of the session composer (#1801): typing "/" lists what the session's own
// tool reports (commands, skills, agents), searchable, by keyboard, announced to a
// screen reader. The menu inserts text; what the text does is the tool's, under the same
// governance as any typed message.
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createQueryClient } from '@/lib/api/query'
import { renderIntel } from '@/test/intel'

const auth = vi.hoisted(() => ({
  activeTenant: 'tnt-demo' as string | null,
  isSuperadmin: false,
  can: (_: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

const listOrgs = vi.hoisted(() => vi.fn())
vi.mock('@/lib/api/endpoints', async (importOriginal) => {
  const real = await importOriginal<typeof import('@/lib/api/endpoints')>()
  return { ...real, systemApi: { ...real.systemApi, listOrgs } }
})

const api = vi.hoisted(() => ({
  listProfiles: vi.fn(),
  listWorkspaces: vi.fn(),
  input: vi.fn(),
  inputText: vi.fn(),
  getRun: vi.fn(),
  commands: vi.fn(),
}))
vi.mock('@/features/agentops/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/agentops/api')>()),
  agentOpsApi: api,
}))

const toasts = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock('@/components/ui/toaster', () => ({ toast: toasts }))

import { WorkComposer } from './work-composer'
import type { RunCommandsDTO, RunDTO } from '@/features/agentops/types'

const RUNNING: RunDTO = {
  run_ref: 'run_live',
  name: 'nightly',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'running',
  last_event_seq: 0,
  pep_provisioned: true,
  record_io: true,
  critical: false,
  provider_driver: 'claude',
  provider_profile_ref: 'ppf_team',
}

const CATALOG: RunCommandsDTO = {
  tool: 'claude',
  reported: true,
  commands: [
    {
      name: 'review-notes',
      description: 'Review my notes',
      source: 'user',
      kind: 'command',
    },
    {
      name: 'deploy-check',
      description: 'Check the project before a deploy',
      argument_hint: '<env>',
      source: 'project',
      kind: 'command',
    },
    {
      name: 'compact',
      description: 'Free up context by summarizing the conversation',
      source: 'built-in',
      kind: 'command',
    },
    {
      name: 'Explore',
      description: 'Fast read-only search agent',
      source: '',
      kind: 'agent',
    },
  ],
}

beforeEach(() => {
  vi.clearAllMocks()
  auth.activeTenant = 'tnt-demo'
  auth.can = () => true
  listOrgs.mockResolvedValue({ items: [], has_more: false })
  api.listProfiles.mockResolvedValue({ items: [], has_more: false })
  api.listWorkspaces.mockResolvedValue({ items: [], has_more: false })
  api.input.mockResolvedValue({ accepted: true })
  api.inputText.mockResolvedValue({ accepted: true })
  api.commands.mockResolvedValue(CATALOG)
})

/** What a Claude run is sent for one typed sentence: a stream-json user frame. */
const userFrame = (text: string) =>
  JSON.stringify({ type: 'user', message: { role: 'user', content: text } })

function renderComposer() {
  renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
  return screen.getByRole('textbox', { name: 'Session turn' })
}

describe('the "/" menu', () => {
  it('lists what the tool reports as soon as "/" is typed, with each source', async () => {
    const user = userEvent.setup()
    const box = renderComposer()
    expect(api.commands).not.toHaveBeenCalled()

    await user.type(box, '/')

    const list = await screen.findByRole('listbox', {
      name: 'Commands and skills',
    })
    expect(api.commands).toHaveBeenCalledWith('run_live', expect.anything())
    const options = within(list).getAllByRole('option')
    expect(options.map((o) => o.textContent)).toEqual([
      expect.stringContaining('/review-notes'),
      expect.stringContaining('/deploy-check'),
      expect.stringContaining('/compact'),
      expect.stringContaining('@agent-Explore'),
    ])
    expect(within(options[0]).getByText('User')).toBeInTheDocument()
    expect(within(options[1]).getByText('Project')).toBeInTheDocument()
    expect(within(options[1]).getByText('<env>')).toBeInTheDocument()
    expect(
      within(options[1]).getByText(/Check the project/),
    ).toBeInTheDocument()
    expect(within(options[2]).getByText('Built-in')).toBeInTheDocument()
    expect(within(options[3]).getByText('Agent')).toBeInTheDocument()
    // The field is still a text box the person types in; the menu only annotates it.
    expect(box).toHaveAttribute('aria-controls', list.id)
    expect(box).toHaveAttribute('aria-autocomplete', 'list')
  })

  it('narrows as the word is typed and says how many are shown', async () => {
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, '/dep')
    const list = await screen.findByRole('listbox')
    expect(
      within(list)
        .getAllByRole('option')
        .map((o) => o.textContent),
    ).toEqual([expect.stringContaining('/deploy-check')])
    expect(screen.getByTestId('slash-status')).toHaveTextContent(
      '/deploy-check, 1 of 1',
    )
  })

  it('has its live region before the menu opens, and only its text changes', async () => {
    const user = userEvent.setup()
    const box = renderComposer()
    const status = screen.getByTestId('slash-status')
    expect(status).toHaveAttribute('role', 'status')
    expect(status).toHaveTextContent('')
    await user.type(box, '/')
    await screen.findByRole('listbox')
    // The same element, now saying the first option, then the one the arrows reach.
    expect(screen.getByTestId('slash-status')).toBe(status)
    expect(status).toHaveTextContent('/review-notes, 1 of 4')
    await user.keyboard('{ArrowDown}')
    expect(status).toHaveTextContent('/deploy-check, 2 of 4')
    await user.keyboard('{Escape}')
    expect(status).toHaveTextContent('')
  })

  it('moves with the arrow keys, inserts with Enter, and a second Enter sends', async () => {
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, '/')
    const list = await screen.findByRole('listbox')
    const options = within(list).getAllByRole('option')
    expect(options[0]).toHaveAttribute('aria-selected', 'true')
    expect(box).toHaveAttribute('aria-activedescendant', options[0].id)

    await user.keyboard('{ArrowDown}{ArrowDown}')
    expect(options[2]).toHaveAttribute('aria-selected', 'true')
    expect(box).toHaveAttribute('aria-activedescendant', options[2].id)
    expect(screen.getByTestId('slash-status')).toHaveTextContent(
      '/compact, 3 of 4',
    )
    await user.keyboard('{ArrowUp}{ArrowUp}{ArrowUp}')
    // Wraps around: first -> last.
    expect(options[3]).toHaveAttribute('aria-selected', 'true')
    await user.keyboard('{ArrowDown}{ArrowDown}{ArrowDown}')
    expect(options[2]).toHaveAttribute('aria-selected', 'true')

    await user.keyboard('{Enter}')
    expect(box).toHaveValue('/compact ')
    expect(api.input).not.toHaveBeenCalled()
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()

    await user.keyboard('{Enter}')
    await waitFor(() =>
      expect(api.input).toHaveBeenCalledWith(
        'run_live',
        userFrame('/compact'),
        undefined,
      ),
    )
  })

  it('inserts with Tab and with a click, and mentions an agent as the tool does', async () => {
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, '/dep')
    await screen.findByRole('listbox')
    await user.keyboard('{Tab}')
    expect(box).toHaveValue('/deploy-check ')

    await user.clear(box)
    await user.type(box, '/')
    await user.click(await screen.findByRole('option', { name: /Explore/ }))
    expect(box).toHaveValue('@agent-Explore ')
    expect(box).toHaveFocus()
  })

  it('Escape closes it, keeps the draft, and it comes back with the next character', async () => {
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, '/de')
    await screen.findByRole('listbox')
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
    expect(box).toHaveValue('/de')
    await user.keyboard('p')
    expect(await screen.findByRole('listbox')).toBeInTheDocument()
  })

  it('does not take Enter from a message that is not a command', async () => {
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, 'run /compact{Enter}')
    expect(api.commands).not.toHaveBeenCalled()
    await waitFor(() =>
      expect(api.input).toHaveBeenCalledWith(
        'run_live',
        userFrame('run /compact'),
        undefined,
      ),
    )
  })

  it('closes at the first space: arguments are typed in the field, not in the menu', async () => {
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, '/compact')
    await screen.findByRole('listbox')
    await user.type(box, ' keep the plan')
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
    await user.keyboard('{Enter}')
    await waitFor(() =>
      expect(api.input).toHaveBeenCalledWith(
        'run_live',
        userFrame('/compact keep the plan'),
        undefined,
      ),
    )
  })

  it('says in one line that the tool reported nothing, and Enter still sends what was typed', async () => {
    api.commands.mockResolvedValue({
      tool: 'codex',
      reported: false,
      commands: [],
    })
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, '/model')
    expect(
      await within(await screen.findByTestId('slash-menu')).findByText(
        'This tool reported no commands for the session.',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
    await user.keyboard('{Enter}')
    await waitFor(() =>
      expect(api.input).toHaveBeenCalledWith(
        'run_live',
        userFrame('/model'),
        undefined,
      ),
    )
  })

  it('says so when nothing matches', async () => {
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, '/zzz')
    expect(
      await within(await screen.findByTestId('slash-menu')).findByText(
        'No command matches “zzz”.',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
  })

  it('says the read failed when a refresh fails after the tool had reported nothing', async () => {
    api.commands.mockResolvedValueOnce({
      tool: 'codex',
      reported: false,
      commands: [],
    })
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, '/')
    await within(await screen.findByTestId('slash-menu')).findByText(
      'This tool reported no commands for the session.',
    )
    await user.clear(box)
    api.commands.mockRejectedValue(new Error('504'))
    await user.type(box, '/')
    expect(
      await within(await screen.findByTestId('slash-menu')).findByText(
        "The tool's commands could not be read.",
      ),
    ).toBeInTheDocument()
  })

  it('shows the read failure in one line when no catalog was ever read', async () => {
    api.commands.mockRejectedValue(new Error('504'))
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, '/')
    expect(
      await within(await screen.findByTestId('slash-menu')).findByText(
        "The tool's commands could not be read.",
      ),
    ).toBeInTheDocument()
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
    expect(toasts.error).not.toHaveBeenCalled()
  })

  it("shows a skill the tool reports after the first time the menu opened, with the console's real cache defaults", async () => {
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />, {
      queryClient: createQueryClient(),
    })
    const box = screen.getByRole('textbox', { name: 'Session turn' })
    await user.type(box, '/')
    await screen.findByRole('listbox')
    expect(screen.queryByRole('option', { name: /brand-new/ })).toBeNull()
    await user.clear(box)
    await waitFor(() =>
      expect(screen.queryByRole('listbox')).not.toBeInTheDocument(),
    )

    // The person adds a skill to the project folder; the tool reports it on the next ask.
    api.commands.mockResolvedValue({
      ...CATALOG,
      commands: [
        ...CATALOG.commands,
        {
          name: 'brand-new',
          description: 'Added without restarting',
          source: 'project',
          kind: 'command',
        },
      ],
    })
    await user.type(box, '/')
    expect(
      await screen.findByRole('option', { name: /brand-new/ }),
    ).toBeInTheDocument()
    expect(api.commands).toHaveBeenCalledTimes(2)
  })

  it('closes when the session stops while "/" is typed, and asks nothing more', async () => {
    const user = userEvent.setup()
    const view = renderIntel(
      <WorkComposer attached={{ run: RUNNING, group: 'active' }} />,
    )
    const box = screen.getByRole('textbox', { name: 'Session turn' })
    await user.type(box, '/de')
    await screen.findByRole('listbox')
    expect(api.commands).toHaveBeenCalledTimes(1)
    view.rerender(
      <WorkComposer
        attached={{ run: { ...RUNNING, state: 'stopped' }, group: 'settled' }}
      />,
    )
    await waitFor(() =>
      expect(screen.queryByRole('listbox')).not.toBeInTheDocument(),
    )
    expect(screen.getByRole('textbox', { name: 'Session turn' })).toBeDisabled()
    expect(screen.getByTestId('slash-status')).toHaveTextContent('')
  })

  it('says the tool reported nothing when it reported an empty list', async () => {
    api.commands.mockResolvedValue({
      tool: 'claude',
      reported: true,
      commands: [],
    })
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, '/')
    expect(
      await within(await screen.findByTestId('slash-menu')).findByText(
        'This tool reported no commands for the session.',
      ),
    ).toBeInTheDocument()
    expect(screen.getByTestId('slash-status')).not.toHaveTextContent(
      /No command matches/,
    )
  })

  it('says it is reading while an earlier "nothing reported" is asked again', async () => {
    api.commands.mockResolvedValueOnce({
      tool: 'codex',
      reported: false,
      commands: [],
    })
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />, {
      queryClient: createQueryClient(),
    })
    const box = screen.getByRole('textbox', { name: 'Session turn' })
    await user.type(box, '/')
    await within(await screen.findByTestId('slash-menu')).findByText(
      'This tool reported no commands for the session.',
    )
    await user.clear(box)
    api.commands.mockReturnValue(new Promise(() => {}))
    await user.type(box, '/')
    expect(
      await within(await screen.findByTestId('slash-menu')).findByText(
        "Reading the tool's commands…",
      ),
    ).toBeInTheDocument()
    expect(screen.getByTestId('slash-status')).toHaveTextContent(
      "Reading the tool's commands…",
    )
  })

  it('shows no source tag for a source the console does not know, never a raw key', async () => {
    api.commands.mockResolvedValue({
      tool: 'claude',
      reported: true,
      commands: [
        // a newer tool may name a source this console has no word for
        {
          name: 'managed-one',
          description: 'From a policy',
          source: 'managed',
          kind: 'command',
        },
      ],
    })
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, '/')
    const option = await screen.findByRole('option', { name: /managed-one/ })
    expect(option.textContent).not.toMatch(/launcher\.slash/)
    expect(option.textContent).toBe('/managed-oneFrom a policy')
  })

  it('keeps two entries with the same name and different sources, both selectable', async () => {
    api.commands.mockResolvedValue({
      tool: 'claude',
      reported: true,
      commands: [
        {
          name: 'same',
          description: 'from the user',
          source: 'user',
          kind: 'command',
        },
        {
          name: 'same',
          description: 'from the project',
          source: 'project',
          kind: 'command',
        },
      ],
    })
    const user = userEvent.setup()
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    const box = renderComposer()
    await user.type(box, '/sa')
    expect(await screen.findAllByRole('option')).toHaveLength(2)
    // React reports two children with one key; the key carries the source for this.
    const duplicateKey = consoleError.mock.calls.some((call) =>
      String(call[0]).includes('same key'),
    )
    consoleError.mockRestore()
    expect(duplicateKey).toBe(false)
  })

  it('opens again for the same word after Escape, a send and a new draft', async () => {
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, '/de')
    await screen.findByRole('listbox')
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
    await user.keyboard('{Enter}') // sends "/de" as typed
    await waitFor(() => expect(api.input).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(box).toHaveValue(''))
    await user.type(box, '/de')
    expect(await screen.findByRole('listbox')).toBeInTheDocument()
  })

  it('does not take Ctrl+Enter: the background send is still a send', async () => {
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, '/dep')
    await screen.findByRole('listbox')
    await user.keyboard('{Control>}{Enter}{/Control}')
    await waitFor(() =>
      expect(api.input).toHaveBeenCalledWith(
        'run_live',
        userFrame('/dep'),
        undefined,
      ),
    )
  })

  it('keeps Escape for itself while it is open, and gives it back when it is closed', async () => {
    const outside = vi.fn()
    window.addEventListener('keydown', outside)
    try {
      const user = userEvent.setup()
      const box = renderComposer()
      await user.type(box, '/de')
      await screen.findByRole('listbox')
      await user.keyboard('{Escape}')
      expect(outside).not.toHaveBeenCalledWith(
        expect.objectContaining({ key: 'Escape' }),
      )
      await user.keyboard('{Escape}') // the menu is closed now: the key goes on
      expect(outside).toHaveBeenCalledWith(
        expect.objectContaining({ key: 'Escape' }),
      )
    } finally {
      window.removeEventListener('keydown', outside)
    }
  })

  it('does not open, nor ask, without an organization', async () => {
    auth.activeTenant = null
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, '/')
    expect(screen.queryByTestId('slash-menu')).not.toBeInTheDocument()
    expect(api.commands).not.toHaveBeenCalled()
  })

  it('puts the name that was typed in full first, and Enter sends it instead of inserting another', async () => {
    api.commands.mockResolvedValue({
      tool: 'claude',
      reported: true,
      commands: [
        {
          name: 'review-pr',
          description: 'Review a pull request',
          source: 'project',
          kind: 'command',
        },
        {
          name: 'review',
          description: 'Review the change',
          source: 'built-in',
          kind: 'command',
        },
      ],
    } satisfies RunCommandsDTO)
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, '/review')
    const list = await screen.findByRole('listbox')
    expect(
      within(list)
        .getAllByRole('option')
        .map((o) => o.textContent),
    ).toEqual([
      expect.stringContaining('Review the change'),
      expect.stringContaining('/review-pr'),
    ])
    await user.keyboard('{Enter}')
    await waitFor(() =>
      expect(api.input).toHaveBeenCalledWith(
        'run_live',
        userFrame('/review'),
        undefined,
      ),
    )
    expect(box).toHaveValue('')
  })

  it('still inserts when what was typed is only the start of a name', async () => {
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, '/compac')
    await screen.findByRole('listbox')
    await user.keyboard('{Enter}')
    expect(box).toHaveValue('/compact ')
    expect(api.input).not.toHaveBeenCalled()
  })

  it('says the list may be out of date when a refresh fails after the tool had reported', async () => {
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, '/')
    await screen.findByRole('listbox')
    await user.clear(box)
    api.commands.mockRejectedValue(new Error('504'))
    await user.type(box, '/')
    const menu = await screen.findByTestId('slash-menu')
    expect(
      await within(menu).findByText(/may be out of date/),
    ).toBeInTheDocument()
    // The list the tool reported before is still there to choose from.
    expect(within(menu).getAllByRole('option')).toHaveLength(4)
    // Said once, by its own live region; the option region keeps saying only the option.
    expect(screen.getByTestId('slash-notice-status')).toHaveTextContent(
      /may be out of date/,
    )
    expect(screen.getByTestId('slash-status')).not.toHaveTextContent(
      /out of date/,
    )
  })

  it('says the list may be out of date when the engine serves what the tool reported before', async () => {
    api.commands.mockResolvedValue({ ...CATALOG, stale: true })
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, '/dep')
    const menu = await screen.findByTestId('slash-menu')
    expect(
      await within(menu).findByText(/may be out of date/),
    ).toBeInTheDocument()
  })

  it('says nothing about age when the list is current', async () => {
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, '/')
    await screen.findByRole('listbox')
    expect(screen.queryByText(/may be out of date/)).not.toBeInTheDocument()
    expect(box).toBeInTheDocument()
  })

  it('closes when the field loses focus, and comes back with the next character', async () => {
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, '/de')
    await screen.findByRole('listbox')
    await user.click(document.body)
    expect(box).not.toHaveFocus()
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
    expect(box).toHaveValue('/de')
    await user.click(box)
    await user.keyboard('p')
    expect(await screen.findByRole('listbox')).toBeInTheDocument()
  })

  it('picks with the primary button only, so a context menu does not insert', async () => {
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, '/')
    const option = await screen.findByRole('option', { name: /compact/ })
    fireEvent.mouseDown(option, { button: 2 })
    expect(box).toHaveValue('/')
    expect(screen.getByRole('listbox')).toBeInTheDocument()
    fireEvent.mouseDown(option, { button: 0 })
    expect(box).toHaveValue('/compact ')
  })

  it('does not take an agent of the same name for the command that was typed in full', async () => {
    api.commands.mockResolvedValue({
      tool: 'claude',
      reported: true,
      commands: [
        {
          name: 'review',
          description: 'A review agent',
          source: '',
          kind: 'agent',
        },
        {
          name: 'review',
          description: 'Review the change',
          source: 'built-in',
          kind: 'command',
        },
      ],
    } satisfies RunCommandsDTO)
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, '/review')
    await screen.findByRole('listbox')
    await user.keyboard('{Enter}')
    await waitFor(() =>
      expect(api.input).toHaveBeenCalledWith(
        'run_live',
        userFrame('/review'),
        undefined,
      ),
    )
  })

  it('closes while a send is pending, so a click cannot rewrite the draft under it', async () => {
    api.input.mockReturnValue(new Promise(() => {}))
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, '/compact')
    await screen.findByRole('listbox')
    await user.keyboard('{Enter}')
    await waitFor(() => expect(api.input).toHaveBeenCalled())
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
    expect(box).toHaveValue('/compact')
  })

  it('says only that the read failed when the list was empty and a refresh fails', async () => {
    api.commands.mockResolvedValueOnce({
      tool: 'claude',
      reported: true,
      commands: [],
    })
    const user = userEvent.setup()
    const box = renderComposer()
    await user.type(box, '/')
    await within(await screen.findByTestId('slash-menu')).findByText(
      'This tool reported no commands for the session.',
    )
    await user.clear(box)
    api.commands.mockRejectedValue(new Error('504'))
    await user.type(box, '/')
    const menu = await screen.findByTestId('slash-menu')
    expect(
      await within(menu).findByText("The tool's commands could not be read."),
    ).toBeInTheDocument()
    expect(
      within(menu).queryByText(/may be out of date/),
    ).not.toBeInTheDocument()
    expect(screen.getByTestId('slash-notice-status')).toHaveTextContent('')
  })
})
