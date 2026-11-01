// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { createHash } from 'node:crypto'
import { createInstance } from 'i18next'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import enSettings from '@/lib/i18n/locales/en/settings.json'
import esSettings from '@/lib/i18n/locales/es/settings.json'

const http = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  delete: vi.fn(),
}))
vi.mock('@/lib/api/client', () => ({ http }))

import {
  registerServiceWorker,
  currentPush,
  disablePush,
  enablePush,
  pushSupported,
  pushTextsFrom,
  type PushTexts,
} from './web-push'

const KEY = 'BFakeApplicationServerKey_-AAAA'
const TEXTS: PushTexts = {
  app: 'Olivares',
  approval: 'Needs you',
  finished: 'Ended',
  failed: 'Failed',
}
const ENDPOINT = 'https://fcm.googleapis.com/fcm/send/device-1'

function keyBytes(b64url: string): Uint8Array {
  return new Uint8Array(Buffer.from(b64url, 'base64url'))
}

interface Fake {
  sub: ReturnType<typeof makeSub>
  manager: {
    getSubscription: ReturnType<typeof vi.fn>
    subscribe: ReturnType<typeof vi.fn>
  }
  register: ReturnType<typeof vi.fn>
  stored: Map<string, string>
}

function makeSub(endpoint = ENDPOINT, serverKey: string = KEY) {
  return {
    endpoint,
    options: { applicationServerKey: keyBytes(serverKey).buffer },
    toJSON: () => ({
      endpoint,
      expirationTime: null,
      keys: { p256dh: 'P256DH', auth: 'AUTH' },
    }),
    unsubscribe: vi.fn().mockResolvedValue(true),
  }
}

function install(existing: ReturnType<typeof makeSub> | null = null): Fake {
  const sub = makeSub()
  const manager = {
    getSubscription: vi.fn().mockResolvedValue(existing),
    subscribe: vi.fn().mockResolvedValue(sub),
  }
  const registration = { pushManager: manager }
  const register = vi.fn().mockResolvedValue(registration)
  vi.stubGlobal('navigator', {
    ...navigator,
    serviceWorker: {
      register,
      ready: Promise.resolve(registration),
      getRegistration: vi.fn().mockResolvedValue(registration),
    },
  })
  vi.stubGlobal('PushManager', function PushManager() {})
  vi.stubGlobal('Notification', { permission: 'granted' })
  const stored = new Map<string, string>()
  vi.stubGlobal('caches', {
    open: async () => ({
      put: async (request: string, response: Response) => {
        stored.set(request, await response.text())
      },
    }),
  })
  return { sub, manager, register, stored }
}

beforeEach(() => {
  http.get.mockReset().mockResolvedValue({ public_key: KEY })
  http.post.mockReset().mockResolvedValue({ id: 'x' })
  http.delete.mockReset().mockResolvedValue(undefined)
})
afterEach(() => vi.unstubAllGlobals())

describe('pushTextsFrom', () => {
  it.each([
    ['en', enSettings],
    ['es', esSettings],
  ] as const)(
    'resolves %s Settings text with a common default namespace',
    async (lng, settings) => {
      const i18n = createInstance()
      await i18n.init({
        lng,
        defaultNS: 'common',
        resources: { [lng]: { common: {}, settings } },
      })
      expect(pushTextsFrom(i18n.t.bind(i18n))).toEqual({
        app: 'Olivares',
        approval: settings.sessionNotifications.needsYou,
        finished: settings.sessionNotifications.ended,
        failed: settings.sessionNotifications.failed,
      })
    },
  )
})

describe('pushSupported', () => {
  it('is false where the browser has no service worker or no push manager', () => {
    expect(pushSupported()).toBe(false)
    install()
    expect(pushSupported()).toBe(true)
    vi.stubGlobal('Notification', undefined)
    expect(pushSupported()).toBe(false)
  })
})

describe('enablePush', () => {
  it('subscribes with the engine key and hands the engine exactly what it stores', async () => {
    const fake = install()
    await enablePush(TEXTS)

    expect(fake.register).toHaveBeenCalledWith('/sw.js')
    expect(http.get).toHaveBeenCalledWith('/v1/m/sessions/push/key')
    const options = fake.manager.subscribe.mock.calls[0]![0]
    expect(options.userVisibleOnly).toBe(true)
    expect(new Uint8Array(options.applicationServerKey)).toEqual(keyBytes(KEY))
    // The engine's strict body: no expirationTime, nothing a browser adds later.
    expect(http.post).toHaveBeenCalledWith(
      '/v1/m/sessions/push/subscriptions',
      {
        endpoint: ENDPOINT,
        keys: { p256dh: 'P256DH', auth: 'AUTH' },
      },
    )
  })

  it('leaves the words for the closed tab in the cache the worker reads', async () => {
    const fake = install()
    await enablePush(TEXTS)
    expect(JSON.parse(fake.stored.get('/push-texts.json')!)).toEqual(TEXTS)
  })

  it('keeps a subscription already made with the engine key and does not subscribe twice', async () => {
    const existing = makeSub()
    const fake = install(existing)
    await enablePush(TEXTS)
    expect(fake.manager.subscribe).not.toHaveBeenCalled()
    expect(existing.unsubscribe).not.toHaveBeenCalled()
    expect(http.post).toHaveBeenCalledTimes(1)
  })

  it('replaces a subscription made for another key, as after a key rotation', async () => {
    const stale = makeSub(ENDPOINT, 'BAnotherServerKey_-AAAAAAAA')
    const fake = install(stale)
    await enablePush(TEXTS)
    expect(stale.unsubscribe).toHaveBeenCalledTimes(1)
    expect(fake.manager.subscribe).toHaveBeenCalledTimes(1)
  })

  it('drops the browser subscription when the engine refuses it, so none is left orphaned', async () => {
    const fake = install()
    http.post.mockRejectedValue(new Error('503'))
    await expect(enablePush(TEXTS)).rejects.toThrow('503')
    expect(fake.sub.unsubscribe).toHaveBeenCalledTimes(1)
    expect(fake.stored.size).toBe(0)
  })

  it('does not subscribe when the engine has no push key', async () => {
    const fake = install()
    http.get.mockRejectedValue(new Error('503'))
    await expect(enablePush(TEXTS)).rejects.toThrow('503')
    expect(fake.manager.subscribe).not.toHaveBeenCalled()
  })
})

describe('disablePush', () => {
  it('removes the device by the id the engine computes, then unsubscribes the browser', async () => {
    const existing = makeSub()
    install(existing)
    await disablePush()
    const id = createHash('sha256').update(ENDPOINT).digest('hex')
    expect(http.delete).toHaveBeenCalledWith(
      `/v1/m/sessions/push/subscriptions/${id}`,
    )
    expect(existing.unsubscribe).toHaveBeenCalledTimes(1)
  })

  it('still unsubscribes the browser when the engine cannot be reached', async () => {
    const existing = makeSub()
    install(existing)
    http.delete.mockRejectedValue(new Error('offline'))
    await disablePush()
    expect(existing.unsubscribe).toHaveBeenCalledTimes(1)
  })

  it('is a no-op for a browser that never subscribed', async () => {
    install(null)
    await disablePush()
    expect(http.delete).not.toHaveBeenCalled()
  })
})

describe('currentPush', () => {
  it('reports whether this browser holds a subscription', async () => {
    install(makeSub())
    expect(await currentPush()).toBe(true)
    install(null)
    expect(await currentPush()).toBe(false)
  })
})

it('does not fetch a service worker or push keys on local TLS', async () => {
  const fake = install()
  vi.stubGlobal('isSecureContext', true)
  document.head.insertAdjacentHTML(
    'beforeend',
    '<meta name="olivares-local-tls" content="true">',
  )
  try {
    registerServiceWorker()
    expect(pushSupported()).toBe(false)
    await expect(enablePush(TEXTS)).rejects.toThrow()
    expect(fake.register).not.toHaveBeenCalled()
    expect(http.get).not.toHaveBeenCalled()
  } finally {
    document.querySelector('meta[name="olivares-local-tls"]')?.remove()
  }
})
