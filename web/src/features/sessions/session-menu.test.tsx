// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A SESSION ROW'S ACTIONS (#2795): what each item does, against the engine's routes.
// How the menu opens (right-click, `⋯`, Shift+F10) is `work-rail.test.tsx`'s.
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import type { RunDTO } from '@/features/agentops/types'
import { renderIntel } from '@/test/intel'
import { mergeSessions, type Grants, type UnifiedSession } from './provenance'
import { useSessionMenu } from './session-menu'
import type { LiveDTO } from './types'
import './i18n'
import '@/features/agentops/i18n'

const auth = vi.hoisted(() => ({
  activeTenant: 'tnt-demo' as string | null,
  can: (_: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

const toasts = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast: toasts }))

const ops = vi.hoisted(() => ({
  rename: vi.fn(),
  archive: vi.fn(),
  unarchive: vi.fn(),
  messages: vi.fn(),
  cleanup: vi.fn(),
  deleteRun: vi.fn(),
}))
vi.mock('@/features/agentops/api', async (importOriginal) => {
  const real = await importOriginal<typeof import('@/features/agentops/api')>()
  return {
    ...real,
    agentOpsApi: { ...(real.agentOpsApi as object), ...ops },
  }
})

const ALL: Grants = {
  liveRead: true,
  runRead: true,
  runWrite: true,
  runAdmin: true,
}

function run(over: Partial<RunDTO> = {}): RunDTO {
  return {
    run_ref: 'run-a',
    name: 'nightly',
    transport: 'stream-json',
    permission_mode: 'default',
    isolation: 'native',
    state: 'stopped',
    last_event_seq: 0,
    pep_provisioned: true,
    record_io: false,
    critical: false,
    provider_driver: 'claude',
    ...over,
  }
}

const launched = (over: Partial<RunDTO> = {}) =>
  mergeSessions([], [run(over)])[0]

const observed: UnifiedSession = mergeSessions(
  [
    {
      session_ref: 'sess-o',
      live_ref: 'lr-o',
      attribution: 'observed',
      cc_state: 'idle',
      input_tokens: 0,
      output_tokens: 0,
      cost_micro_usd: 0,
      event_count: 0,
      tool_call_count: 0,
      first_event_at: '2026-09-18T09:00:00Z',
      last_event_at: '2026-09-18T09:01:00Z',
      duration_seconds: 42,
    } satisfies LiveDTO,
  ],
  [],
)[0]

function Harness({
  session,
  grants = ALL,
  pinned = false,
  onTogglePin = null,
  returnFocus,
}: {
  session: UnifiedSession
  grants?: Grants
  pinned?: boolean
  onTogglePin?: (() => void) | null
  returnFocus?: (address: string) => void
}) {
  const menu = useSessionMenu(grants, returnFocus)
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger>Actions</DropdownMenuTrigger>
        <DropdownMenuContent>
          {menu.items(session, { pinned, onTogglePin })}
        </DropdownMenuContent>
      </DropdownMenu>
      {menu.dialogs}
    </>
  )
}

async function openMenu(props: Parameters<typeof Harness>[0]) {
  const user = userEvent.setup()
  const { unmount } = renderIntel(<Harness {...props} />)
  await user.click(screen.getByRole('button', { name: 'Actions' }))
  return { user, unmount, menu: screen.getByRole('menu') }
}

const item = (menu: HTMLElement, name: string | RegExp) =>
  within(menu).getByRole('menuitem', { name })

beforeEach(() => {
  vi.resetAllMocks()
})

// `userEvent.setup()` puts its own clipboard on `navigator`: what was copied is read there.
const copied = () => navigator.clipboard.readText()

describe('the session menu — what a row can do', () => {
  it('offers every action for a launched session, in one menu', async () => {
    const { menu } = await openMenu({
      session: launched(),
      onTogglePin: vi.fn(),
    })
    expect(
      within(menu)
        .getAllByRole('menuitem')
        .map((el) => el.textContent?.replace(/p$/, '')),
    ).toEqual([
      'Rename…',
      'Pin',
      'Archive',
      'Copy link',
      'Copy session id',
      'Copy transcript',
      'Delete…',
    ])
  })

  it('offers only the copies for a session Olivares observes and did not start', async () => {
    // No run: the engine keeps a name, an archive mark and a record only for a run.
    const { menu } = await openMenu({ session: observed })
    expect(
      within(menu)
        .getAllByRole('menuitem')
        .map((el) => el.textContent),
    ).toEqual(['Copy link', 'Copy session id'])
  })

  it('leaves out what the person may not do', async () => {
    const { menu } = await openMenu({
      session: launched(),
      grants: { ...ALL, runWrite: false, runAdmin: false },
    })
    expect(within(menu).queryByRole('menuitem', { name: 'Rename…' })).toBeNull()
    expect(within(menu).queryByRole('menuitem', { name: 'Archive' })).toBeNull()
    expect(within(menu).queryByRole('menuitem', { name: /Delete/ })).toBeNull()
  })

  it('offers rename and archive to a writer, and delete only to an administrator', async () => {
    const { menu } = await openMenu({
      session: launched(),
      grants: { ...ALL, runAdmin: false },
    })
    expect(item(menu, 'Rename…')).toBeInTheDocument()
    expect(item(menu, 'Archive')).toBeInTheDocument()
    expect(within(menu).queryByRole('menuitem', { name: /Delete/ })).toBeNull()
  })

  it('offers no transcript to a person who may not read the session’s run', async () => {
    const { menu } = await openMenu({
      session: launched(),
      grants: { ...ALL, runRead: false },
    })
    expect(
      within(menu).queryByRole('menuitem', { name: 'Copy transcript' }),
    ).toBeNull()
    expect(item(menu, 'Copy link')).toBeInTheDocument()
  })
})

describe('the session menu — rename', () => {
  it('stores the new name on the server', async () => {
    ops.rename.mockResolvedValue(run({ name: 'release prep' }))
    const { user, menu } = await openMenu({ session: launched() })
    await user.click(item(menu, 'Rename…'))
    const dialog = await screen.findByRole('dialog')
    const field = within(dialog).getByRole('textbox', { name: 'Name' })
    expect(field).toHaveValue('nightly')
    await user.clear(field)
    await user.type(field, '  release prep {Enter}')
    await waitFor(() =>
      expect(ops.rename).toHaveBeenCalledWith('run-a', 'release prep'),
    )
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('says why when the engine refuses the name, and keeps the dialog', async () => {
    const { ApiError } = await import('@/lib/api/errors')
    ops.rename.mockRejectedValue(
      new ApiError(400, 'bad_request', 'the name is too long'),
    )
    const { user, menu } = await openMenu({ session: launched() })
    await user.click(item(menu, 'Rename…'))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(toasts.error).toHaveBeenCalledWith('the name is too long'),
    )
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })
})

describe('the session menu — pin and archive', () => {
  it('pins and unpins through the caller', async () => {
    const onTogglePin = vi.fn()
    const { user, menu } = await openMenu({
      session: launched(),
      pinned: true,
      onTogglePin,
    })
    await user.click(item(menu, /Unpin/))
    expect(onTogglePin).toHaveBeenCalledTimes(1)
  })

  it('archives a session, and brings an archived one back', async () => {
    ops.archive.mockResolvedValue(run({ archived_at: '2026-10-21T12:00:00Z' }))
    const first = await openMenu({ session: launched() })
    await first.user.click(item(first.menu, 'Archive'))
    await waitFor(() => expect(ops.archive).toHaveBeenCalledWith('run-a'))
    await waitFor(() =>
      expect(toasts.success).toHaveBeenCalledWith(
        'Archived. It is under Archived at the end of the list.',
      ),
    )
    first.unmount()

    ops.unarchive.mockResolvedValue(run())
    const second = await openMenu({
      session: launched({ archived_at: '2026-10-21T12:00:00Z' }),
    })
    await second.user.click(item(second.menu, 'Unarchive'))
    await waitFor(() => expect(ops.unarchive).toHaveBeenCalledWith('run-a'))
  })

  it('gives focus back to the list once an archived row has left it', async () => {
    // The row leaves the list only when the list is read again, after the menu closed:
    // focus that fell to the page then goes to the list, by the row's address.
    let answer = (_: RunDTO) => {}
    ops.archive.mockReturnValue(
      new Promise<RunDTO>((resolve) => {
        answer = resolve
      }),
    )
    const returnFocus = vi.fn()
    const { user, menu } = await openMenu({
      session: launched(),
      returnFocus,
    })
    await user.click(item(menu, 'Archive'))
    // The row the menu gave focus back to is gone by the time the engine answers.
    ;(document.activeElement as HTMLElement | null)?.blur()
    answer(run({ archived_at: '2026-10-21T12:00:00Z' }))
    await waitFor(() => expect(returnFocus).toHaveBeenCalledWith('run:run-a'))
  })
})

describe('the session menu — copy', () => {
  it('copies a link that opens this session', async () => {
    const { user, menu } = await openMenu({ session: launched() })
    await user.click(item(menu, 'Copy link'))
    await waitFor(async () =>
      expect(await copied()).toBe(
        `${window.location.origin}/sessions?session=run%3Arun-a`,
      ),
    )
  })

  it('copies the id the CLI and the API take: the run’s, not the tool’s own', async () => {
    const { user, menu } = await openMenu({
      session: launched({ claude_session_id: 'sess-tool-1' }),
    })
    await user.click(item(menu, 'Copy session id'))
    await waitFor(async () => expect(await copied()).toBe('run-a'))
  })

  it('copies an observed session’s own reference, as it has no run', async () => {
    const { user, menu } = await openMenu({ session: observed })
    await user.click(item(menu, 'Copy session id'))
    await waitFor(async () => expect(await copied()).toBe('sess-o'))
  })

  it('says how many transcript records it could not read, instead of plain Copied', async () => {
    ops.messages.mockResolvedValue({
      driver: 'claude',
      transcript: 'read',
      total: 1,
      unreadable: 2,
      items: [{ id: 1, role: 'user', first_line: 'hi', text: 'hi' }],
    })
    const { user, menu } = await openMenu({ session: launched() })
    await user.click(item(menu, 'Copy transcript'))
    await waitFor(() =>
      expect(toasts.success).toHaveBeenCalledWith(
        'Copied. 2 records of the transcript could not be read and are not in it.',
      ),
    )
    expect(await copied()).toBe('You:\nhi\n')
  })

  it('says which messages the engine cut at 64 KiB, instead of plain Copied', async () => {
    ops.messages.mockResolvedValue({
      driver: 'claude',
      transcript: 'read',
      total: 2,
      items: [
        {
          id: 1,
          role: 'user',
          first_line: 'log',
          text: 'log [...]',
          clipped: true,
        },
        { id: 2, role: 'assistant', first_line: 'ok', text: 'ok' },
      ],
    })
    const { user, menu } = await openMenu({ session: launched() })
    await user.click(item(menu, 'Copy transcript'))
    await waitFor(() =>
      expect(toasts.warning).toHaveBeenCalledWith(
        'Some messages are longer than 64 KiB and were cut short in the copy; each one ends in [...].',
      ),
    )
    expect(await copied()).toBe('You:\nlog [...]\n\nClaude Code:\nok\n')
  })

  it('refuses a transcript whose next page is the page it just read', async () => {
    ops.messages.mockResolvedValue({
      driver: 'claude',
      transcript: 'read',
      total: 2,
      items: [{ id: 1, role: 'user', first_line: 'a', text: 'a' }],
      next: '1',
    })
    const { user, menu } = await openMenu({ session: launched() })
    await user.click(item(menu, 'Copy transcript'))
    await waitFor(() =>
      expect(toasts.error).toHaveBeenCalledWith(
        'The transcript changed while it was being copied. Try again.',
      ),
    )
    expect(ops.messages).toHaveBeenCalledTimes(2)
    expect(await copied()).toBe('')
  })

  it('refuses to copy first lines when the engine sent no whole texts', async () => {
    // An engine that ignores `text=1` answers the index: pasting it as the transcript
    // would be a shorter conversation that says it is the whole one.
    ops.messages.mockResolvedValue({
      driver: 'claude',
      transcript: 'read',
      total: 1,
      items: [{ id: 1, role: 'user', first_line: 'fix it' }],
    })
    const { user, menu } = await openMenu({ session: launched() })
    await user.click(item(menu, 'Copy transcript'))
    await waitFor(() =>
      expect(toasts.error).toHaveBeenCalledWith(
        'The engine sent only the first line of each message, so the transcript was not copied.',
      ),
    )
    expect(await copied()).toBe('')
  })

  it('refuses a transcript that stops being readable after its first page', async () => {
    ops.messages
      .mockResolvedValueOnce({
        driver: 'claude',
        transcript: 'read',
        total: 2,
        items: [{ id: 1, role: 'user', first_line: 'a', text: 'a' }],
        next: '1',
      })
      .mockResolvedValueOnce({
        driver: 'claude',
        transcript: 'missing',
        total: 0,
        items: [],
      })
    const { user, menu } = await openMenu({ session: launched() })
    await user.click(item(menu, 'Copy transcript'))
    await waitFor(() =>
      expect(toasts.error).toHaveBeenCalledWith(
        'The transcript changed while it was being copied. Try again.',
      ),
    )
    expect(await copied()).toBe('')
  })

  it('copies the whole transcript as text, every page of it', async () => {
    ops.messages
      .mockResolvedValueOnce({
        driver: 'claude',
        transcript: 'read',
        total: 3,
        items: [
          { id: 1, role: 'user', first_line: 'fix it', text: 'fix it' },
          {
            id: 2,
            role: 'assistant',
            first_line: 'Done.',
            text: 'Done.\nTwo files.',
          },
        ],
        next: '2',
      })
      .mockResolvedValueOnce({
        driver: 'claude',
        transcript: 'read',
        total: 3,
        items: [{ id: 3, role: 'user', first_line: 'thanks', text: 'thanks' }],
      })
    const { user, menu } = await openMenu({ session: launched() })
    await user.click(item(menu, 'Copy transcript'))
    await waitFor(async () =>
      expect(await copied()).toBe(
        'You:\nfix it\n\nClaude Code:\nDone.\nTwo files.\n\nYou:\nthanks\n',
      ),
    )
    expect(ops.messages).toHaveBeenNthCalledWith(1, 'run-a', {
      role: 'all',
      order: 'asc',
      text: 1,
      limit: 50,
      cursor: undefined,
    })
    expect(ops.messages).toHaveBeenNthCalledWith(2, 'run-a', {
      role: 'all',
      order: 'asc',
      text: 1,
      limit: 50,
      cursor: '2',
    })
  })

  it('says so when the tool has written no transcript, and copies nothing', async () => {
    ops.messages.mockResolvedValue({
      driver: 'claude',
      transcript: 'pending',
      total: 0,
      items: [],
    })
    const { user, menu } = await openMenu({ session: launched() })
    await user.click(item(menu, 'Copy transcript'))
    await waitFor(() =>
      expect(toasts.error).toHaveBeenCalledWith(
        "This session's tool has not written a transcript to copy yet.",
      ),
    )
    expect(await copied()).toBe('')
  })
})

describe('the session menu — delete', () => {
  it('asks first, then cleans up and deletes a stopped session', async () => {
    ops.cleanup.mockResolvedValue(run({ state: 'cleaned' }))
    ops.deleteRun.mockResolvedValue({ deleted: true })
    const { user, menu } = await openMenu({ session: launched() })
    await user.click(item(menu, 'Delete…'))
    const dialog = await screen.findByRole('dialog')
    expect(ops.deleteRun).not.toHaveBeenCalled()
    await user.click(within(dialog).getByRole('button', { name: 'Delete' }))
    await waitFor(() => expect(ops.deleteRun).toHaveBeenCalledWith('run-a'))
    expect(ops.cleanup).toHaveBeenCalledWith('run-a', false)
  })

  it('retries only the delete when the cleanup before it went through', async () => {
    // Released but not deleted: a second cleanup would be refused (state=cleaned), so
    // the retry must not ask for one.
    const { ApiError } = await import('@/lib/api/errors')
    ops.cleanup.mockResolvedValue(run({ state: 'cleaned' }))
    ops.deleteRun
      .mockRejectedValueOnce(new ApiError(500, 'internal', 'live rows remain'))
      .mockResolvedValueOnce({ deleted: true })
    const { user, menu } = await openMenu({ session: launched() })
    await user.click(item(menu, 'Delete…'))
    const dialog = await screen.findByRole('dialog')
    const confirm = within(dialog).getByRole('button', { name: 'Delete' })
    await user.click(confirm)
    await waitFor(() =>
      expect(toasts.error).toHaveBeenCalledWith('live rows remain'),
    )
    await user.click(confirm)
    await waitFor(() => expect(ops.deleteRun).toHaveBeenCalledTimes(2))
    expect(ops.cleanup).toHaveBeenCalledTimes(1)
  })

  it('does not offer Delete on a running session: it says to stop it first', async () => {
    const { menu } = await openMenu({ session: launched({ state: 'running' }) })
    const del = item(menu, /Delete/)
    expect(del).toHaveAttribute('aria-disabled', 'true')
    expect(del).toHaveTextContent('Stop it first')
  })
})
