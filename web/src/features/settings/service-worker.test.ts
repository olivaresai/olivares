// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { runInNewContext } from 'node:vm'
import { describe, expect, it, vi } from 'vitest'

// The worker is a plain script in public/, served as it is. This runs that file in a sandbox
// with a worker's `self`, so what is proved is what ships.
const SOURCE = readFileSync(join(__dirname, '../../../public/sw.js'), 'utf8')

const ORIGIN = 'https://console.example'

type Listener = (event: Record<string, unknown>) => void

function boot(texts: Record<string, string> | null = null) {
  const listeners = new Map<string, Listener>()
  const showNotification = vi.fn().mockResolvedValue(undefined)
  const clients = {
    claim: vi.fn().mockResolvedValue(undefined),
    matchAll: vi.fn().mockResolvedValue([]),
    openWindow: vi.fn().mockResolvedValue(undefined),
  }
  const self = {
    addEventListener: (type: string, fn: Listener) => listeners.set(type, fn),
    skipWaiting: vi.fn(),
    location: { origin: ORIGIN },
    registration: { showNotification },
    clients,
  }
  const caches = {
    open: async () => ({
      match: async () => (texts ? { json: async () => texts } : undefined),
    }),
  }
  runInNewContext(SOURCE, { self, caches, URL })
  return { listeners, self, showNotification, clients }
}

/** Dispatch an event and wait for what it handed to waitUntil. */
async function fire(
  listeners: Map<string, Listener>,
  type: string,
  event: Record<string, unknown>,
) {
  let pending: Promise<unknown> = Promise.resolve()
  listeners.get(type)!({
    ...event,
    waitUntil: (p: Promise<unknown>) => (pending = p),
  })
  await pending
}

const pushOf = (body: unknown) => ({
  data: { json: () => (typeof body === 'string' ? JSON.parse(body) : body) },
})

describe('push', () => {
  it('shows the person’s own words for the kind, titled by the session name', async () => {
    const { listeners, showNotification } = boot({
      app: 'Olivares',
      approval: 'Braucht Sie',
      finished: 'Beendet',
      failed: 'Fehler',
    })
    await fire(
      listeners,
      'push',
      pushOf({
        kind: 'approval',
        name: 'deploy fix',
        url: '/sessions?session=run%3Aabc&pane=narrative',
        tag: 'run:abc',
      }),
    )
    expect(showNotification).toHaveBeenCalledTimes(1)
    const [title, options] = showNotification.mock.calls[0]!
    expect(title).toBe('deploy fix')
    expect(options).toMatchObject({
      body: 'Braucht Sie',
      tag: 'run:abc',
      renotify: true,
      data: { url: '/sessions?session=run%3Aabc&pane=narrative' },
    })
  })

  it('titles an unnamed session with the product and alerts again only for an approval', async () => {
    const { listeners, showNotification } = boot(null)
    await fire(
      listeners,
      'push',
      pushOf({ kind: 'failed', url: '/x', tag: 'run:1' }),
    )
    const [title, options] = showNotification.mock.calls[0]!
    expect(title).toBe('Olivares')
    expect(options.body).toBe('The session stopped with an error.')
    expect(options.renotify).toBe(false)
  })

  it('shows something for a push it cannot read, because a push with no notification costs the subscription', async () => {
    const { listeners, showNotification } = boot(null)
    await fire(listeners, 'push', {
      data: {
        json: () => {
          throw new SyntaxError('not json')
        },
      },
    })
    await fire(listeners, 'push', {}) // no data at all
    expect(showNotification).toHaveBeenCalledTimes(2)
    for (const [title, options] of showNotification.mock.calls) {
      expect(title).toBe('Olivares')
      expect(options.data.url).toBe('/')
      expect(options.tag).toBeUndefined()
      expect(options.renotify).toBe(false)
    }
  })

  it('keeps every link on this console, whatever the message says', async () => {
    for (const hostile of [
      'https://evil.example/phish',
      '//evil.example/x',
      'javascript:alert(1)',
      'data:text/html,hi',
    ]) {
      const { listeners, showNotification } = boot(null)
      await fire(listeners, 'push', pushOf({ kind: 'approval', url: hostile }))
      const url = showNotification.mock.calls[0]![1].data.url as string
      expect(url.startsWith('/') && !url.startsWith('//')).toBe(true)
      expect(url).not.toContain('evil')
    }
  })
})

describe('notificationclick', () => {
  const click = (url: string) => ({
    notification: { close: vi.fn(), data: { url } },
  })

  it('takes the console that is already open to the session', async () => {
    const { listeners, clients } = boot(null)
    const navigate = vi.fn().mockResolvedValue(undefined)
    const win = { focus: vi.fn().mockResolvedValue({ navigate }), navigate }
    clients.matchAll.mockResolvedValue([win])
    const event = click('/sessions?session=run%3Aabc&pane=narrative')
    await fire(listeners, 'notificationclick', event)
    expect(event.notification.close).toHaveBeenCalled()
    expect(navigate).toHaveBeenCalledWith(
      '/sessions?session=run%3Aabc&pane=narrative',
    )
    expect(clients.openWindow).not.toHaveBeenCalled()
  })

  it('opens a window when no console is open, or the open one cannot be navigated', async () => {
    const closed = boot(null)
    await fire(
      closed.listeners,
      'notificationclick',
      click('/sessions?session=run%3Aabc'),
    )
    expect(closed.clients.openWindow).toHaveBeenCalledWith(
      '/sessions?session=run%3Aabc',
    )

    const stuck = boot(null)
    stuck.clients.matchAll.mockResolvedValue([
      { focus: vi.fn().mockRejectedValue(new Error('not controlled')) },
    ])
    await fire(
      stuck.listeners,
      'notificationclick',
      click('/sessions?session=run%3Aabc'),
    )
    expect(stuck.clients.openWindow).toHaveBeenCalledWith(
      '/sessions?session=run%3Aabc',
    )
  })

  it('never opens a foreign address, even from a stored notification', async () => {
    const { listeners, clients } = boot(null)
    await fire(listeners, 'notificationclick', click('https://evil.example/x'))
    expect(clients.openWindow).toHaveBeenCalledWith('/')
  })
})

describe('lifecycle', () => {
  it('replaces the old worker at once and claims the open consoles', async () => {
    const { listeners, self, clients } = boot(null)
    await fire(listeners, 'install', {})
    expect(self.skipWaiting).toHaveBeenCalled()
    await fire(listeners, 'activate', {})
    expect(clients.claim).toHaveBeenCalled()
  })

  it('answers no fetch: the console is always the server’s own', () => {
    expect(boot(null).listeners.has('fetch')).toBe(false)
  })
})
