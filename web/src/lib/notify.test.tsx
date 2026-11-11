// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE NOTICE RULE, held by a test.
//
//   A notice is a toast with at most one action, or an inline state in the place it
//   belongs (empty state, error state, field help). It is never a permanent banner above
//   content. Conditions that block use (expired license, offline, a stop) show once per
//   page, in one line, with their one action.
//
// Part 1 holds the toast: it lasts 6 seconds, an error stays until dismissed, it is
// announced, its action is a button the keyboard reaches, and a dismissed condition is
// not shown again until it changes, remembered per person. Part 2 holds the census: every
// banner-like component the console exports is listed here with its category, so a new one
// fails until someone decides where it belongs.
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { join, relative } from 'node:path'
import {
  act,
  render,
  renderHook,
  screen,
  waitFor,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Toaster, toast } from '@/components/ui/toaster'
import {
  dismissAllNotices,
  noticeSeen,
  notify,
  rememberNotice,
  resolveNotice,
  type Notice,
} from '@/lib/notify'
import { useConditionNotice } from '@/lib/use-condition-notice'
import { preferenceStorage } from '@/lib/preference-storage'

const REMOVAL_MS = 500 // sonner unmounts a closed toast after its exit animation

async function elapse(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

describe('notify: the one toast', () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    preferenceStorage.attach(null, {}, ['https://console.test', 'user', 'ana'])
    render(<Toaster />)
  })
  afterEach(async () => {
    act(() => {
      toast.dismiss()
    })
    await elapse(REMOVAL_MS)
    vi.useRealTimers()
  })

  it('lasts 6 seconds', async () => {
    act(() => notify({ message: 'Link value ignored' }))
    expect(await screen.findByText('Link value ignored')).toBeInTheDocument()
    await elapse(5_500)
    expect(screen.getByText('Link value ignored')).toBeInTheDocument()
    await elapse(500 + REMOVAL_MS)
    expect(screen.queryByText('Link value ignored')).toBeNull()
  })

  it('is one pill on the inverse surface, low on the sheet, announced politely', async () => {
    act(() => notify({ message: 'report.xlsx saved to Invoices 2026' }))
    const text = await screen.findByText('report.xlsx saved to Invoices 2026')
    const pill = text.closest('[data-sonner-toast]')!
    expect(pill).toHaveClass(
      'bg-[var(--inverse)]',
      'text-[var(--on-inverse)]',
      'rounded-[22px]',
      'shadow-pop',
    )
    const toaster = pill.closest('[data-sonner-toaster]')!
    expect(toaster).toHaveAttribute('data-y-position', 'bottom')
    expect(toaster).toHaveAttribute('data-x-position', 'center')
    // Wherever the phone's bottom bar shows (below 761 px), the toast sits 76 px up,
    // above the bar's 56 px, instead of covering it.
    expect(toaster).toHaveClass('max-[761px]:bottom-[76px]!')
    expect(toaster.closest('section')).toHaveAttribute('aria-live', 'polite')
    // A way to dismiss it, at the end of the line.
    expect(screen.getByRole('button', { name: /close/i })).toHaveClass(
      'order-last',
    )
  })

  it('keeps an error until it is dismissed', async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    act(() => notify({ level: 'error', message: 'Could not save' }))
    await elapse(60_000)
    expect(screen.getByText('Could not save')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /close/i }))
    await elapse(REMOVAL_MS)
    expect(screen.queryByText('Could not save')).toBeNull()
  })

  it('keeps an error raised straight through toast.error until it is dismissed', async () => {
    act(() => {
      toast.error('Could not delete')
    })
    await elapse(60_000)
    expect(screen.getByText('Could not delete')).toBeInTheDocument()
  })

  it('is announced politely and its one action is a button the keyboard reaches', async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    const onClick = vi.fn()
    act(() =>
      notify({
        level: 'warning',
        message: 'Sessions could not be loaded',
        action: { label: 'Retry', onClick },
      }),
    )
    const text = await screen.findByText('Sessions could not be loaded')
    expect(text.closest('[aria-live]')).toHaveAttribute('aria-live', 'polite')
    const buttons = screen.getAllByRole('button', { name: 'Retry' })
    expect(buttons).toHaveLength(1)
    // The toast, its close button and its action are all tab stops: Tab reaches the action.
    for (let i = 0; i < 6 && !buttons[0].matches(':focus'); i++) {
      await user.tab()
    }
    expect(buttons[0]).toHaveFocus()
    await user.keyboard('{Enter}')
    expect(onClick).toHaveBeenCalledTimes(1)
  })

  it('does not show a dismissed condition again until it changes', async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    const shown = { condition: 'engine', signature: 'unreachable' }
    act(() => notify({ ...shown, level: 'warning', message: 'Engine down' }))
    await user.click(await screen.findByRole('button', { name: /close/i }))
    await elapse(REMOVAL_MS)

    act(() => notify({ ...shown, level: 'warning', message: 'Engine down' }))
    await elapse(REMOVAL_MS)
    expect(screen.queryByText('Engine down')).toBeNull()

    act(() =>
      notify({
        condition: 'engine',
        signature: 'refused',
        level: 'warning',
        message: 'Engine refused',
      }),
    )
    expect(await screen.findByText('Engine refused')).toBeInTheDocument()
  })

  it('remembers a dismissal for that person only', async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    const shown = { condition: 'engine', signature: 'unreachable' }
    act(() => notify({ ...shown, level: 'warning', message: 'Engine down' }))
    await user.click(await screen.findByRole('button', { name: /close/i }))
    await elapse(REMOVAL_MS)

    act(() =>
      preferenceStorage.attach(null, {}, [
        'https://console.test',
        'user',
        'luis',
      ]),
    )
    act(() => notify({ ...shown, level: 'warning', message: 'Engine down' }))
    expect(await screen.findByText('Engine down')).toBeInTheDocument()
  })

  it('takes down a toast told for another state when the new state was already dismissed', async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    const refused = {
      condition: 'engine',
      signature: 'refused',
      level: 'warning' as const,
      message: 'Engine refused',
    }
    const down = {
      condition: 'engine',
      signature: 'unreachable',
      level: 'warning' as const,
      message: 'Engine down',
    }
    act(() => notify(refused))
    await user.click(await screen.findByRole('button', { name: /close/i }))
    await elapse(REMOVAL_MS)

    act(() => notify(down))
    expect(await screen.findByText('Engine down')).toBeInTheDocument()
    // The condition moves back to the state this person already dismissed.
    act(() => notify(refused))
    // sonner takes a programmatic dismissal down on the next animation frame.
    await waitFor(() => expect(screen.queryByText('Engine down')).toBeNull())
    // Taking it down was not the person closing it: it is told again when it returns.
    act(() => notify(down))
    expect(await screen.findByText('Engine down')).toBeInTheDocument()
  })

  it('keeps a condition on screen when its action is used, until the condition is resolved', async () => {
    // Retry that fails again must not leave a failure with no notice: sonner closes a toast
    // on its action by default, and the owner of a condition is the one who knows it ended.
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    const onClick = vi.fn()
    act(() =>
      notify({
        condition: 'engine',
        level: 'error',
        message: 'Engine down',
        action: { label: 'Retry', onClick },
      }),
    )
    await user.click(await screen.findByRole('button', { name: 'Retry' }))
    expect(onClick).toHaveBeenCalledTimes(1)
    await elapse(REMOVAL_MS)
    expect(screen.getByText('Engine down')).toBeInTheDocument()

    act(() => resolveNotice('engine'))
    await waitFor(() => expect(screen.queryByText('Engine down')).toBeNull())
  })

  it('shows a condition again after it was resolved', async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    const shown = {
      condition: 'engine',
      signature: '',
      level: 'error' as const,
    }
    act(() => notify({ ...shown, message: 'Engine down' }))
    await user.click(await screen.findByRole('button', { name: /close/i }))
    await elapse(REMOVAL_MS)

    act(() => resolveNotice('engine'))
    act(() => notify({ ...shown, message: 'Engine down' }))
    expect(await screen.findByText('Engine down')).toBeInTheDocument()
  })

  it('does not count a resolved condition as dismissed by the person', async () => {
    const shown = {
      condition: 'engine',
      signature: '',
      level: 'error' as const,
    }
    act(() => notify({ ...shown, message: 'Engine down' }))
    await screen.findByText('Engine down')
    act(() => resolveNotice('engine'))
    await elapse(REMOVAL_MS)

    act(() => notify({ ...shown, message: 'Engine down' }))
    expect(await screen.findByText('Engine down')).toBeInTheDocument()
  })

  it('does not write a dismissal into the preferences of another person', async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    act(() =>
      notify({
        condition: 'engine',
        level: 'error',
        message: 'Engine down',
        signature: '',
      }),
    )
    await screen.findByText('Engine down')
    // Another person signs in; the error toast of the first is still up and they close it.
    preferenceStorage.attach(null, {}, ['https://console.test', 'user', 'bo'])
    await user.click(screen.getByRole('button', { name: /close/i }))
    await elapse(REMOVAL_MS)
    expect(preferenceStorage.getItem('olivares.notices.dismissed')).toBeNull()
  })

  it('remembers nothing when every toast comes down at sign-out', async () => {
    const shown = {
      condition: 'engine',
      level: 'error' as const,
      message: 'Engine down',
      signature: '',
    }
    act(() => notify(shown))
    await screen.findByText('Engine down')
    act(() => dismissAllNotices())
    await elapse(REMOVAL_MS)
    await elapse(REMOVAL_MS)
    // Nobody closed it: the next person to meet the same condition is told.
    expect(preferenceStorage.getItem('olivares.notices.dismissed')).toBeNull()
    act(() => notify(shown))
    expect(await screen.findByText('Engine down')).toBeInTheDocument()
  })
})

describe('useConditionNotice: a dismissal ends with its condition', () => {
  const down = { level: 'error' as const, message: 'Runs could not be loaded' }
  type Said = Omit<Notice, 'condition'> | null | undefined
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    preferenceStorage.attach(null, {}, ['https://console.test', 'user', 'ana'])
    render(<Toaster />)
  })
  afterEach(async () => {
    act(() => {
      toast.dismiss()
    })
    await elapse(REMOVAL_MS)
    vi.useRealTimers()
  })

  async function closeAndLeave() {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    const first = renderHook(() => useConditionNotice('runs', down))
    await user.click(await screen.findByRole('button', { name: /close/i }))
    await elapse(REMOVAL_MS)
    first.unmount()
  }

  it('tells the condition again when it cleared while the person was away', async () => {
    await closeAndLeave()
    // A new visit: the read is pending (nothing to say yet), then it answers and the
    // condition does not hold (known clear), then it fails again.
    const visit = renderHook(
      ({ said }: { said: Said }) => useConditionNotice('runs', said),
      { initialProps: { said: undefined as Said } },
    )
    visit.rerender({ said: null })
    visit.rerender({ said: down })
    expect(await screen.findByText(down.message)).toBeInTheDocument()
  })

  it('keeps it hidden while the same failure is still pending on a new visit', async () => {
    await closeAndLeave()
    const visit = renderHook(
      ({ said }: { said: Said }) => useConditionNotice('runs', said),
      { initialProps: { said: undefined as Said } },
    )
    visit.rerender({ said: down })
    await elapse(REMOVAL_MS)
    expect(screen.queryByText(down.message)).toBeNull()
  })

  it('takes its toast down when the page is left, and tells it again on return', async () => {
    const gone = { level: 'error' as const, message: 'Page it was told on' }
    const first = renderHook(() => useConditionNotice('left-page', gone))
    expect(await screen.findByText(gone.message)).toBeInTheDocument()
    // An error toast never expires: left behind, its Retry would call a page that is gone.
    first.unmount()
    // A toast taken down by code leaves a frame later than one the person closes: two waits.
    await elapse(REMOVAL_MS)
    await elapse(REMOVAL_MS)
    expect(screen.queryByText(gone.message)).toBeNull()
    // Leaving is not closing: nobody dismissed it, so the person is told on return.
    renderHook(() => useConditionNotice('left-page', gone))
    expect(await screen.findByText(gone.message)).toBeInTheDocument()
    expect(preferenceStorage.getItem('olivares.notices.dismissed')).toBeNull()
  })
})

// --- the census ----------------------------------------------------------------------

/**
 * Where each banner-like component belongs. `toast`: it raises a notice on the toast
 * surface. `inline`: it sits in the place it belongs, beside the data, the action or the
 * state it qualifies. `exception`: a condition that blocks use, shown once per page.
 * There is no `banner` category: a permanent strip above content has no home.
 */
type Where = 'toast' | 'inline' | 'exception'

const CENSUS: Record<string, Where> = {
  // toast
  ReloadNotice: 'toast',
  RecordingNoticeSlot: 'toast', // where a Business build puts the "this surface is recorded" strip; its consent dialog blocks
  // The strip itself ships with the Business console, assembled into this tree.
  ...(existsSync(join(__dirname, '../features/recordings/recording-notice.tsx'))
    ? { RecordingNotice: 'toast' as const }
    : {}),
  UrlStateNotice: 'toast', // draws nothing: the element form of useUrlStateNotice
  // inline, in the state it belongs to
  ModuleOffNotice: 'inline',
  Notice: 'inline', // the one boxed inline notice (components/ui/notice.tsx)
  BusinessRequiredNotice: 'inline',
  CapabilityNotice: 'inline',
  FailureNotice: 'inline',
  UnavailableNotice: 'inline',
  PostureUnavailableNotice: 'inline',
  IngestUnavailableNotice: 'inline',
  InventoryUnavailableNotice: 'inline',
  UnmodeledSurfaceNotice: 'inline',
  UnknownNotice: 'inline',
  BrokenNotice: 'inline',
  PasskeyAddressNotice: 'inline',
  LimitNotice: 'inline',
  // inline, field help or a caveat beside the data it qualifies. IntelNotice, and so
  // CaveatNotice and TruncatedNotice, draws a caption with no box: it is not a banner
  NamesUnreadNotice: 'inline',
  ContractPendingNotice: 'inline',
  IntelNotice: 'inline',
  TruncatedNotice: 'inline',
  CaveatNotice: 'inline',
  BoundaryBanner: 'inline',
}

function sources(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) return sources(path)
    return /\.tsx?$/.test(entry.name) && !/\.(test|gen)\.tsx?$/.test(entry.name)
      ? [path]
      : []
  })
}

const SRC = join(__dirname, '..')
const FILES = sources(SRC).map((path) => ({
  path: relative(SRC, path),
  text: readFileSync(path, 'utf8'),
}))

// THE EDITION LINE'S MEMORY (EditionNote) lives in the same value as the dismissed notices,
// with a bound of its own: the capabilities a person met never push a dismissed notice out,
// and the value stays under the engine's cap for one preference (16384 bytes,
// modules/consoleviews/ui_state.go).
describe('the edition line remembered beside the dismissed notices', () => {
  const DISMISSED = 'olivares.notices.dismissed'
  const stored = () => preferenceStorage.getItem(DISMISSED) ?? ''

  beforeEach(() => {
    preferenceStorage.attach(null, {}, ['https://console.test', 'user', 'ana'])
  })

  it('many capabilities never push a dismissed notice out', () => {
    rememberNotice('engine-unreachable', 'offline')
    rememberNotice('license-expired', '2026-11-01')
    for (let i = 0; i < 40; i++)
      rememberNotice(`edition-note:capability-${i}`, 'business')
    expect(noticeSeen('engine-unreachable', 'offline')).toBe(true)
    expect(noticeSeen('license-expired', '2026-11-01')).toBe(true)
    expect(noticeSeen('edition-note:capability-39', 'business')).toBe(true)
  })

  it('stays under the engine cap however many notices and capabilities there are', () => {
    for (let i = 0; i < 40; i++)
      rememberNotice(`condition-${i}-${'n'.repeat(40)}`, 'state')
    for (let i = 0; i < 300; i++)
      rememberNotice(`edition-note:${'c'.repeat(58)}-${i}`, 'enterprise')
    expect(new TextEncoder().encode(stored()).length).toBeLessThan(16384)
    expect(noticeSeen(`condition-39-${'n'.repeat(40)}`, 'state')).toBe(true)
    const newest = `edition-note:${'c'.repeat(58)}-299`
    expect(noticeSeen(newest, 'enterprise')).toBe(true)
  })

  it('keeps no capability key that is not a short printable one', () => {
    rememberNotice(`edition-note:${'x'.repeat(5000)}`, 'business')
    rememberNotice('edition-note:knöwledge', 'business')
    expect(stored()).not.toMatch(/xxxx|knöwledge/)
  })
})

describe('the notice census', () => {
  it('lists every banner-like component the console exports', () => {
    const found = new Set<string>()
    for (const { text } of FILES) {
      for (const m of text.matchAll(
        /^export (?:default )?(?:function|const|class) ((?:[A-Z]\w*)?(?:Notice|Banner)\w*)/gm,
      )) {
        found.add(m[1])
      }
    }
    expect([...found].sort()).toEqual(Object.keys(CENSUS).sort())
  })

  it('raises toasts through the one toaster module', () => {
    const direct = FILES.filter(({ text }) => /from 'sonner'/.test(text))
      .map(({ path }) => path)
      .sort()
    // The surface and the lifetime rule are the only two files that know sonner.
    expect(direct).toEqual([
      join('components', 'ui', 'toast.ts'),
      join('components', 'ui', 'toaster.tsx'),
    ])
  })

  it('gives a toast at most one action', () => {
    const second = FILES.filter(({ text }) => /\bcancel:\s*\{/.test(text)).map(
      ({ path }) => path,
    )
    expect(second).toEqual([])
  })
})
