// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { http } from '@/lib/api/client'

// Web Push for the installed console (#2840): this browser subscribes to the engine's own push
// server, and the engine tells it, tab closed, that a session needs an approval or has ended.
// public/sw.js shows the notification. The message is encrypted to this browser, so the push
// service (Google, Mozilla or Apple) relays it without reading it.

const WORKER = '/sw.js'
const PUSH = '/v1/m/sessions/push'

/** The Cache entry public/sw.js reads its words from; it is the person's language, written here. */
const TEXTS_CACHE = 'olivares-push'
const TEXTS_URL = '/push-texts.json'

/** What a notification says, by the kind of message the engine pushes. */
export interface PushTexts {
  app: string
  approval: string
  finished: string
  failed: string
}

/** The words a closed tab shows, from this console's own translations. */
export function pushTextsFrom(t: (key: string) => string): PushTexts {
  return {
    app: 'Olivares',
    approval: t('settings:sessionNotifications.needsYou'),
    finished: t('settings:sessionNotifications.ended'),
    failed: t('settings:sessionNotifications.failed'),
  }
}

/**
 * An iPhone or iPad gives a web page notifications only once it is on the Home Screen, and
 * then only in that installed app; in Safari the page has neither Notification nor PushManager.
 */
export function needsInstallForPush(): boolean {
  if (pushSupported() || typeof navigator === 'undefined') return false
  const ios =
    /iPad|iPhone|iPod/.test(navigator.userAgent) ||
    (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)
  const installed =
    window.matchMedia?.('(display-mode: standalone)').matches === true
  return ios && !installed
}

/** True where this browser can hold a push subscription at all (iOS only once installed). */
export function pushSupported(): boolean {
  return (
    typeof navigator !== 'undefined' &&
    'serviceWorker' in navigator &&
    typeof window !== 'undefined' &&
    typeof document !== 'undefined' &&
    !document.querySelector('meta[name="olivares-local-tls"]') &&
    'PushManager' in window &&
    typeof Notification !== 'undefined'
  )
}

/**
 * Register the worker once the console is up. Idempotent: the browser keeps one registration
 * and checks the script for a new version on its own. A browser that refuses is left as it was.
 */
export function registerServiceWorker(): void {
  if (
    document.querySelector('meta[name="olivares-local-tls"]') ||
    !('serviceWorker' in navigator) ||
    !window.isSecureContext
  )
    return
  navigator.serviceWorker.register(WORKER).catch(() => undefined)
}

function keyBytes(base64url: string): Uint8Array<ArrayBuffer> {
  const padded = base64url.replace(/-/g, '+').replace(/_/g, '/')
  const raw = atob(padded + '='.repeat((4 - (padded.length % 4)) % 4))
  return Uint8Array.from(raw, (c) => c.charCodeAt(0))
}

function sameKey(a: ArrayBuffer | null | undefined, b: Uint8Array): boolean {
  if (!a) return false
  const x = new Uint8Array(a)
  return x.length === b.length && x.every((v, i) => v === b[i])
}

/** The id the engine gives a device: the hex SHA-256 of its endpoint. */
async function deviceId(endpoint: string): Promise<string> {
  const digest = await crypto.subtle.digest(
    'SHA-256',
    new TextEncoder().encode(endpoint),
  )
  return Array.from(new Uint8Array(digest), (b) =>
    b.toString(16).padStart(2, '0'),
  ).join('')
}

async function storeTexts(texts: PushTexts): Promise<void> {
  const cache = await caches.open(TEXTS_CACHE)
  await cache.put(
    TEXTS_URL,
    new Response(JSON.stringify(texts), {
      headers: { 'Content-Type': 'application/json' },
    }),
  )
}

/** Whether this browser holds a push subscription now. */
export async function currentPush(): Promise<boolean> {
  if (!pushSupported()) return false
  const registration = await navigator.serviceWorker.getRegistration(WORKER)
  return (await registration?.pushManager.getSubscription()) != null
}

/**
 * Subscribe this browser and register it with the engine, or confirm that it already is: the
 * engine keeps one row per browser, so calling this on every visit is cheap and keeps the keys,
 * and the words of the person's language, current. A subscription made for another engine key
 * (the key was rotated) is replaced. The engine refusing leaves no subscription behind.
 */
export async function enablePush(texts: PushTexts): Promise<void> {
  if (!pushSupported())
    throw new Error('Web Push is unavailable on this connection.')
  const registration = await navigator.serviceWorker.register(WORKER)
  await navigator.serviceWorker.ready
  const { public_key } = await http.get<{ public_key: string }>(`${PUSH}/key`)
  const serverKey = keyBytes(public_key)

  let subscription = await registration.pushManager.getSubscription()
  if (
    subscription &&
    !sameKey(subscription.options.applicationServerKey, serverKey)
  ) {
    await subscription.unsubscribe()
    subscription = null
  }
  const fresh = subscription === null
  subscription ??= await registration.pushManager.subscribe({
    userVisibleOnly: true,
    applicationServerKey: serverKey,
  })

  const { endpoint, keys } = subscription.toJSON()
  try {
    await http.post(`${PUSH}/subscriptions`, {
      endpoint,
      keys: { p256dh: keys?.p256dh, auth: keys?.auth },
    })
  } catch (error) {
    if (fresh) await subscription.unsubscribe().catch(() => undefined)
    throw error
  }
  await storeTexts(texts)
}

/**
 * Forget this browser: the engine's row first, then the subscription. A browser that cannot
 * reach the engine still unsubscribes; the engine drops the dead row at its next push.
 */
export async function disablePush(): Promise<void> {
  if (!pushSupported()) return
  const registration = await navigator.serviceWorker.getRegistration(WORKER)
  const subscription = await registration?.pushManager.getSubscription()
  if (!subscription) return
  await http
    .delete(`${PUSH}/subscriptions/${await deviceId(subscription.endpoint)}`)
    .catch(() => undefined)
  await subscription.unsubscribe()
}
