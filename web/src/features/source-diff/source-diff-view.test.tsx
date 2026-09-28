// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { flushSync } from 'react-dom'
import { createRoot } from 'react-dom/client'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { FEATURE_VIEWS } from '@/features/registry'
import census from '@/features/route-census.json'
import i18n from '@/lib/i18n'
import { useTenantStore } from '@/stores/tenant'
import { SourceDiffError } from './source-diff-api'
import type { GitHostDiff, GitHostDiffQuery } from './source-diff-model'
import { FailureNotice, SourceDiffView } from './source-diff-view'

const resources = {
  requires: 'Requires',
  permission: 'system:admin',
  host: 'Host',
  repository: 'Repository',
  base: 'Base',
  head: 'Head',
  swap: 'Swap',
  swapDisabled: 'Enter both refs before swapping them.',
  compare: 'Compare',
  copyLink: 'Copy link',
  changedFiles: 'Changed files',
  keysHint:
    'Alt and an arrow move to the next or previous file or hunk. Arrow keys move in this list.',
  loading: 'Loading the comparison…',
  emptyTitle: 'No differences between these refs.',
  editRefs: 'Edit base or head',
  editComparison: 'Edit the comparison',
  truncatedFiles: 'Stopped at 100 files.',
  truncatedBytes: 'Stopped at 256 KiB of hunk text.',
  binary: 'This file is binary. There is no line diff.',
  removed: 'This file was removed. The host sent no patch.',
  removedNotBinary: 'This is not a binary file.',
  copyPath: 'Copy path',
  badRequest:
    'A source is required. host, repository, base, and head are required.',
  unknownSource: 'This source is not on the roster.',
  forbidden:
    'This comparison needs system:admin. Only a superadmin holds it. An owner or an admin cannot grant it.',
  hostForbidden: 'The Git host refused the read.',
  hostForbiddenBody: 'system:admin is not what is missing.',
  unknownRef: 'unknown ref',
  upstream: 'git host read failed',
  tryAgain: 'Try again',
  returnToSources: 'Return to sources',
  unavailable: 'Not available on this installation yet.',
  tooLarge: 'This comparison is too large for one read. Narrow the refs.',
  rateLimited: 'The Git host rate limit was reached.',
  retryIn: 'Try again in {{seconds}} s.',
  signIn: 'Sign in to compare.',
  openSource: 'Open the source',
  colOld: 'Old',
  colNew: 'New',
  colChange: 'Change',
  colText: 'Text',
  binaryTag: 'Binary',
  truncatedTag: 'Truncated',
  countPartial: '+{{added}} −{{removed}}, partial',
  countFull: '+{{added}} −{{removed}}',
  fileCount: '{{count}} files',
  addedLine: 'Added, new line {{line}}.',
  removedLine: 'Removed, old line {{line}}.',
  contextLine: 'Context, line {{line}}.',
  diffLabel: 'Unified diff of {{path}}',
  status: {
    added: 'Added',
    modified: 'Modified',
    removed: 'Removed',
    renamed: 'Renamed',
    copied: 'Copied',
    changed: 'Changed',
    unchanged: 'Unchanged',
  },
}

const query: GitHostDiffQuery = {
  source: 'github-1',
  host: 'github',
  repository: 'acme/web',
  base: 'main',
  head: 'deadbeef',
}

const PATCH =
  [
    '@@ -14,3 +14,4 @@',
    ' class FrameQueue:',
    '-    def put(self, frame):',
    '-        self.disk.write(frame)',
    '+    async def put(self, frame):',
    '+        await self._q.put(frame)',
    '+        return None',
  ].join('\n') + '\n'

function file(
  over: Partial<GitHostDiff['files'][number]>,
): GitHostDiff['files'][number] {
  return {
    path: 'ingest/queue.py',
    previous_path: '',
    status: 'modified',
    binary: false,
    truncated: false,
    hunks: [PATCH],
    ...over,
  }
}

function diff(over: Partial<GitHostDiff> = {}): GitHostDiff {
  return {
    ...query,
    truncated: false,
    files: [file({})],
    ...over,
  }
}

function renderView(
  reader: (query: GitHostDiffQuery) => Promise<GitHostDiff>,
  next: GitHostDiffQuery = query,
): void {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const ui: ReactElement = (
    <QueryClientProvider client={client}>
      <SourceDiffView query={next} onChange={vi.fn()} reader={reader} />
    </QueryClientProvider>
  )
  render(ui)
}

beforeEach(() => {
  useTenantStore.setState({ activeTenant: null })
  i18n.addResourceBundle('en', 'source-diff', resources, true, true)
})

describe('source diff view', () => {
  it('isolates comparison files across active tenants and reuses each tenant cache', async () => {
    const first = diff({ files: [file({ path: 'first.txt' })] })
    const second = diff({ files: [file({ path: 'second.txt' })] })
    let finishSecond!: (value: GitHostDiff) => void
    const pendingSecond = new Promise<GitHostDiff>((resolve) => {
      finishSecond = resolve
    })
    const reader = vi
      .fn()
      .mockResolvedValueOnce(first)
      .mockReturnValueOnce(pendingSecond)
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: Infinity } },
    })
    const view = () => (
      <QueryClientProvider client={client}>
        <SourceDiffView query={query} onChange={vi.fn()} reader={reader} />
      </QueryClientProvider>
    )
    useTenantStore.setState({ activeTenant: 'tenant-first' })
    const rendered = render(view())
    expect(
      await screen.findByRole('option', { name: /first.txt/ }),
    ).toBeVisible()

    act(() => useTenantStore.setState({ activeTenant: 'tenant-second' }))
    expect(screen.queryByRole('option', { name: /first.txt/ })).toBeNull()
    await waitFor(() => expect(reader).toHaveBeenCalledTimes(2))
    await act(async () => finishSecond(second))
    expect(
      await screen.findByRole('option', { name: /second.txt/ }),
    ).toBeVisible()

    rendered.rerender(view())
    expect(screen.getByRole('option', { name: /second.txt/ })).toBeVisible()
    expect(reader).toHaveBeenCalledTimes(2)
    act(() => useTenantStore.setState({ activeTenant: 'tenant-first' }))
    expect(
      await screen.findByRole('option', { name: /first.txt/ }),
    ).toBeVisible()
    expect(screen.queryByRole('option', { name: /second.txt/ })).toBeNull()
    expect(reader).toHaveBeenCalledTimes(2)
  })

  it('names the comparison page before a repository is selected', () => {
    renderView(async () => diff(), {
      source: '',
      host: '',
      repository: '',
      base: '',
      head: '',
    })
    expect(
      screen.getByRole('heading', { level: 1, name: 'Source diff' }),
    ).toBeVisible()
  })

  it('announces an added line and a removed line without using color alone', async () => {
    renderView(async () => diff())
    const added = await screen.findByRole('row', { name: /Added, new line 15/ })
    expect(added).toHaveTextContent('+')
    expect(added).toHaveTextContent('15')
    const removed = screen.getByRole('row', {
      name: /Removed, old line 15/,
    })
    expect(removed).toHaveTextContent('−')
    expect(removed).toHaveTextContent('15')
    expect(
      screen.getByRole('columnheader', { name: 'Old' }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('columnheader', { name: 'New' }),
    ).toBeInTheDocument()
  })

  it('shows a removed file with no patch, and not as binary', async () => {
    const user = userEvent.setup()
    renderView(async () =>
      diff({
        files: [
          file({}),
          file({
            path: 'old/notes.txt',
            status: 'removed',
            binary: false,
            hunks: [],
          }),
        ],
      }),
    )
    await screen.findByRole('option', { name: /Modified/ })
    await user.click(screen.getByRole('option', { name: /notes.txt/ }))
    expect(
      screen.getByText('This file was removed. The host sent no patch.'),
    ).toBeInTheDocument()
    expect(screen.getByText('This is not a binary file.')).toBeInTheDocument()
    expect(
      screen.queryByText('This file is binary. There is no line diff.'),
    ).not.toBeInTheDocument()
  })

  it('shows a binary file with no line rows', async () => {
    const user = userEvent.setup()
    renderView(async () =>
      diff({
        files: [
          file({}),
          file({
            path: 'assets/logo.png',
            status: 'added',
            binary: true,
            hunks: [],
          }),
        ],
      }),
    )
    await screen.findByRole('option', { name: /queue.py/ })
    await user.click(screen.getByRole('option', { name: /logo.png/ }))
    expect(
      screen.getByText('This file is binary. There is no line diff.'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('row', { name: /Added, new line/ })).toBeNull()
    expect(screen.getByText('Binary')).toBeInTheDocument()
  })

  it('shows previous_path then path for a rename', async () => {
    renderView(async () =>
      diff({
        files: [
          file({
            path: 'notes/now.txt',
            previous_path: 'old/notes.txt',
            status: 'renamed',
          }),
        ],
      }),
    )
    expect(
      await screen.findByRole('option', {
        name: /old\/notes.txt → notes\/now.txt/,
      }),
    ).toHaveTextContent('Renamed')
  })

  it('marks a truncated file partial and does not call the count a total', async () => {
    renderView(async () =>
      diff({
        files: [file({ truncated: true })],
      }),
    )
    const option = await screen.findByRole('option', { name: /queue.py/ })
    expect(option).toHaveTextContent('partial')
    expect(option).toHaveTextContent('+3 −2')
    expect(option).not.toHaveTextContent('total')
    expect(option).toHaveTextContent('Truncated')
  })

  it('names 100 files or 256 KiB from the response flag', async () => {
    const many = Array.from({ length: 100 }, (_, index) =>
      file({ path: `f${index}.txt`, hunks: ['@@ -1 +1 @@\n-a\n+b'] }),
    )
    const { unmount } = render(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <SourceDiffView
          query={query}
          onChange={vi.fn()}
          reader={async () => diff({ truncated: true, files: many })}
        />
      </QueryClientProvider>,
    )
    expect(await screen.findByText('Stopped at 100 files.')).toBeInTheDocument()
    unmount()
    renderView(async () =>
      diff({
        truncated: true,
        files: [file({})],
      }),
    )
    expect(
      await screen.findByText('Stopped at 256 KiB of hunk text.'),
    ).toBeInTheDocument()
  })

  it('does not say the comparison is loading before the query is complete', () => {
    renderView(async () => diff(), {
      ...query,
      repository: '',
    })
    expect(
      screen.queryByText('Loading the comparison…'),
    ).not.toBeInTheDocument()
    expect(screen.queryByText(/\d+ files/)).not.toBeInTheDocument()
  })

  it('says one file in the singular', async () => {
    renderView(async () => diff({ files: [file({ path: 'only.py' })] }))
    expect(await screen.findByText('1 file')).toBeInTheDocument()
    expect(screen.queryByText('1 files')).not.toBeInTheDocument()
  })

  it('counts the returned files and the kept lines in the header', async () => {
    renderView(async () =>
      diff({
        files: [
          file({ path: 'a.py' }),
          file({
            path: 'b.py',
            hunks: ['@@ -1 +1 @@\n-x\n+y\n'],
          }),
        ],
      }),
    )
    expect(await screen.findByText('2 files')).toBeInTheDocument()
    expect(screen.getByText('+4 −3')).toBeInTheDocument()
  })

  it('does not show a file count while loading', async () => {
    renderView(() => new Promise(() => {}))
    expect(
      await screen.findByText('Loading the comparison…'),
    ).toBeInTheDocument()
    expect(screen.queryByText('0 files')).not.toBeInTheDocument()
    expect(screen.queryByRole('option')).not.toBeInTheDocument()
  })

  it('moves focus to the base field from the edit actions', async () => {
    const user = userEvent.setup()
    const cases: Array<{
      name: string
      run: () => Promise<GitHostDiff>
    }> = [
      {
        name: 'Edit base or head',
        run: async () => diff({ files: [] }),
      },
      {
        name: 'Edit the comparison',
        run: async () => {
          throw new SourceDiffError('badRequest', 'bad_request')
        },
      },
      {
        name: 'Edit base or head',
        run: async () => {
          throw new SourceDiffError('unknownRef', 'unknown_ref')
        },
      },
      {
        name: 'Edit the comparison',
        run: async () => {
          throw new SourceDiffError('tooLarge', 'diff_too_large')
        },
      },
    ]
    for (const item of cases) {
      const view = render(
        <QueryClientProvider
          client={
            new QueryClient({ defaultOptions: { queries: { retry: false } } })
          }
        >
          <SourceDiffView query={query} onChange={vi.fn()} reader={item.run} />
        </QueryClientProvider>,
      )
      await user.click(await screen.findByRole('button', { name: item.name }))
      expect(document.activeElement).toBe(
        screen.getByRole('textbox', { name: 'Base' }),
      )
      view.unmount()
    }
  })

  it('shows no differences when the file list is empty', async () => {
    renderView(async () => diff({ files: [], truncated: false }))
    expect(
      await screen.findByText('No differences between these refs.'),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Edit base or head' }),
    ).toBeInTheDocument()
  })

  it('shows who can hold system:admin on a permission refusal', async () => {
    renderView(async () => {
      throw new SourceDiffError('forbidden', 'forbidden')
    })
    const message = await screen.findByText(/Only a superadmin holds it/)
    expect(message).toHaveTextContent('cannot grant')
    expect(message).not.toHaveTextContent('ask an administrator')
    expect(
      screen.getByRole('button', { name: 'Return to sources' }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Try again' }),
    ).not.toBeInTheDocument()
  })

  it('names the host refusal for the roster', async () => {
    renderView(async () => {
      throw new SourceDiffError('hostForbidden', 'git_host_forbidden')
    })
    expect(
      await screen.findByRole('button', { name: 'Return to sources' }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Open the source' }),
    ).not.toBeInTheDocument()
  })

  it('does not paint a sign-in panel when the read is unauthenticated', async () => {
    renderView(async () => {
      throw new SourceDiffError('unauthenticated', 'unauthenticated')
    })
    await screen.findByRole('textbox', { name: 'Repository' })
    await waitFor(() => {
      expect(
        screen.queryByText('Loading the comparison…'),
      ).not.toBeInTheDocument()
    })
    expect(screen.queryByText(/sign in/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/git host read failed/i)).not.toBeInTheDocument()
  })

  it('does not ask for system:admin when the host refused', async () => {
    renderView(async () => {
      throw new SourceDiffError('hostForbidden', 'git_host_forbidden')
    })
    expect(
      await screen.findByText('The Git host refused the read.'),
    ).toBeInTheDocument()
    expect(screen.queryByText(/cannot grant/)).not.toBeInTheDocument()
    expect(screen.queryByText(/ask an administrator/)).not.toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Return to sources' }),
    ).toBeInTheDocument()
  })

  it('says a complete query names a source that is not on the roster', async () => {
    renderView(async () => {
      throw new SourceDiffError('badRequest', 'bad_request')
    })
    expect(await screen.findByText(/not on the roster/i)).toBeInTheDocument()
    expect(screen.queryByText(/are required/i)).not.toBeInTheDocument()
  })

  it('keeps the required fields when the query is incomplete', () => {
    renderView(async () => diff(), { ...query, repository: '' })
    expect(
      screen.getByText(/host, repository, base, and head are required/i),
    ).toBeInTheDocument()
    expect(screen.queryByText(/not on the roster/i)).not.toBeInTheDocument()
  })

  it('shows 400, 404, 502, 501, 422, and 503 with one action each', async () => {
    const cases: Array<{
      kind: ConstructorParameters<typeof SourceDiffError>[0]
      code: string
      text: string
      action: string
      retryAt?: number
    }> = [
      {
        kind: 'badRequest',
        code: 'bad_request',
        text: 'not on the roster',
        action: 'Edit the comparison',
      },
      {
        kind: 'unknownRef',
        code: 'unknown_ref',
        text: 'unknown ref',
        action: 'Edit base or head',
      },
      {
        kind: 'upstream',
        code: 'upstream',
        text: 'git host read failed',
        action: 'Try again',
      },
      {
        kind: 'unavailable',
        code: 'git_host_diff_unavailable',
        text: 'Not available on this installation yet.',
        action: 'Return to sources',
      },
      {
        kind: 'tooLarge',
        code: 'diff_too_large',
        text: 'too large for one read',
        action: 'Edit the comparison',
      },
      {
        kind: 'rateLimited',
        code: 'rate_limited',
        text: 'rate limit',
        action: 'Try again',
        retryAt: Date.now() + 60_000,
      },
    ]
    for (const item of cases) {
      const { unmount } = render(
        <QueryClientProvider
          client={
            new QueryClient({ defaultOptions: { queries: { retry: false } } })
          }
        >
          <SourceDiffView
            query={query}
            onChange={vi.fn()}
            reader={async () => {
              throw new SourceDiffError(item.kind, item.code, item.retryAt)
            }}
          />
        </QueryClientProvider>,
      )
      expect(
        await screen.findByText(new RegExp(item.text, 'i')),
      ).toBeInTheDocument()
      const action = screen.getByRole('button', { name: item.action })
      if (item.kind === 'rateLimited') expect(action).toBeDisabled()
      if (item.kind === 'unavailable') {
        expect(
          screen.queryByRole('button', { name: 'Try again' }),
        ).not.toBeInTheDocument()
      }
      if (item.kind === 'badRequest') {
        expect(screen.queryByText(/are required/i)).not.toBeInTheDocument()
      }
      unmount()
    }
  })

  it('keeps the last diff after a failed refresh and clears it for a permission refusal', async () => {
    let mode: 'ok' | 'upstream' | 'forbidden' = 'ok'
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <SourceDiffView
          query={query}
          onChange={vi.fn()}
          reader={async () => {
            if (mode === 'upstream') {
              throw new SourceDiffError('upstream', 'upstream')
            }
            if (mode === 'forbidden') {
              throw new SourceDiffError('forbidden', 'forbidden')
            }
            return diff()
          }}
        />
      </QueryClientProvider>,
    )
    expect(
      await screen.findByRole('option', { name: /queue.py/ }),
    ).toBeInTheDocument()
    mode = 'upstream'
    await act(async () => {
      await client
        .refetchQueries({ queryKey: ['source-diff'] })
        .catch(() => undefined)
    })
    expect(await screen.findByText(/git host read failed/i)).toBeInTheDocument()
    expect(screen.getByRole('option', { name: /queue.py/ })).toBeInTheDocument()
    mode = 'forbidden'
    await act(async () => {
      await client
        .refetchQueries({ queryKey: ['source-diff'] })
        .catch(() => undefined)
    })
    expect(await screen.findByText(/cannot grant/)).toBeInTheDocument()
    expect(screen.queryByRole('option')).not.toBeInTheDocument()
  })

  it('disables Try again on the first render when the retry time is still ahead', () => {
    const host = document.createElement('div')
    document.body.appendChild(host)
    const root = createRoot(host)
    flushSync(() => {
      root.render(
        <FailureNotice
          failure={
            new SourceDiffError(
              'rateLimited',
              'rate_limited',
              Date.now() + 60_000,
            )
          }
          onRetry={() => undefined}
          onEdit={() => undefined}
        />,
      )
    })
    const button = [...host.querySelectorAll('button')].find(
      (item) => item.textContent === 'Try again',
    )
    expect(button).toBeTruthy()
    expect(button?.hasAttribute('disabled')).toBe(true)
    root.unmount()
    host.remove()
  })

  it('moves focus to the selected file and keeps it there on Enter', async () => {
    renderView(async () =>
      diff({
        files: [
          file({ path: 'a.txt', hunks: ['@@ -1 +1 @@\n-a\n+b\n'] }),
          file({ path: 'b.txt', hunks: ['@@ -0,0 +1 @@\n+c\n'] }),
          file({ path: 'c.txt', hunks: ['@@ -0,0 +1 @@\n+d\n'] }),
        ],
      }),
    )
    const press = async (target: Element, init: KeyboardEventInit) => {
      await act(async () => {
        target.dispatchEvent(
          new KeyboardEvent('keydown', { bubbles: true, ...init }),
        )
      })
    }
    const first = await screen.findByRole('option', { name: /a.txt/ })
    first.focus()
    await press(first, { key: 'ArrowDown' })
    const second = screen.getByRole('option', { name: /b.txt/ })
    expect(document.activeElement).toBe(second)
    expect(second.className).toContain('outline-focus')
    await press(second, { key: 'Enter' })
    expect(second).toHaveAttribute('aria-selected', 'true')
    expect(document.activeElement).toBe(second)
    await press(second, { key: 'End' })
    const third = screen.getByRole('option', { name: /c.txt/ })
    expect(document.activeElement).toBe(third)
    await press(third, { key: 'Home' })
    expect(document.activeElement).toBe(first)
    await press(first, { key: 'ArrowDown', altKey: true })
    expect(document.activeElement).toBe(second)
  })

  it('windows the diff above 200 lines and moves the window', async () => {
    const user = userEvent.setup()
    const lines = ['@@ -1,201 +1,201 @@']
    for (let index = 1; index <= 201; index += 1) {
      lines.push(` LINE-${String(index).padStart(4, '0')}`)
    }
    renderView(async () =>
      diff({ files: [file({ hunks: [lines.join('\n') + '\n'] })] }),
    )
    const region = await screen.findByRole('region', { name: /queue.py/ })
    expect(within(region).getByText('LINE-0001')).toBeInTheDocument()
    expect(within(region).queryByText('LINE-0201')).not.toBeInTheDocument()
    region.focus()
    await user.keyboard('{PageDown}')
    expect(within(region).getByText('LINE-0201')).toBeInTheDocument()
  })
})

describe('source diff registration', () => {
  it('is a system administration leaf with a label in every locale', () => {
    const view = FEATURE_VIEWS.find((item) => item.id === 'sourceDiff')
    expect(view?.path).toBe('/console/sources/diff')
    expect(view?.permission).toBe('system:admin')
    expect(view?.navigation).toMatchObject({
      areaId: 'system',
      sectionId: 'administration',
    })
    expect(view?.helpHref).toBe('/reference/console')
    expect(view?.hideInNav).not.toBe(true)
    expect(census.paths).toContain('/console/sources/diff')
    for (const language of ['en', 'es', 'de', 'fr', 'ja', 'ru', 'zh']) {
      const nav = JSON.parse(
        readFileSync(
          resolve('src/lib/i18n/locales', language, 'nav.json'),
          'utf8',
        ),
      ) as { items: Record<string, string> }
      expect(nav.items.sourceDiff.length).toBeGreaterThan(0)
    }
  })
})
