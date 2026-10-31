// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The console's service worker (#2840). It does two things and nothing else: it shows a
// notification when the engine pushes one, and it opens the session that notification is about
// when the person taps it. It caches nothing and answers no fetch: the console is always the
// server's own.
//
// The push is end to end encrypted to this browser (RFC 8291), so the push service that relays it
// never reads it. What it carries is { kind, name?, url, tag }: no sentence. The words come from
// the person's own language, which the console leaves in the Cache for this worker to read
// (features/settings/web-push.ts); the fallback below is only for a push that arrives before
// they were stored.
'use strict'

const TEXTS_CACHE = 'olivares-push'
const TEXTS_URL = '/push-texts.json'
const FALLBACK_TEXTS = {
  app: 'Olivares',
  approval: 'This session needs you.',
  finished: 'The session finished.',
  failed: 'The session stopped with an error.',
}

self.addEventListener('install', () => {
  // A new worker replaces the old at once: there is no cached shell for the old one to protect.
  self.skipWaiting()
})

self.addEventListener('activate', (event) => {
  event.waitUntil(self.clients.claim())
})

async function readTexts() {
  try {
    const hit = await (await caches.open(TEXTS_CACHE)).match(TEXTS_URL)
    return { ...FALLBACK_TEXTS, ...(hit ? await hit.json() : {}) }
  } catch {
    return FALLBACK_TEXTS
  }
}

// ownPath keeps a link on this console: a push can only ever open a page of the origin that
// registered this worker, whatever the message says.
function ownPath(raw) {
  if (typeof raw !== 'string') return '/'
  try {
    const url = new URL(raw, self.location.origin)
    return url.origin === self.location.origin ? url.pathname + url.search : '/'
  } catch {
    return '/'
  }
}

self.addEventListener('push', (event) => {
  event.waitUntil(
    (async () => {
      let message = {}
      try {
        message = event.data ? event.data.json() : {}
      } catch {
        // A push that does not parse still shows something: a browser that gets a push and no
        // notification may drop the subscription.
      }
      const texts = await readTexts()
      const tag =
        typeof message.tag === 'string' && message.tag ? message.tag : undefined
      await self.registration.showNotification(
        typeof message.name === 'string' && message.name
          ? message.name
          : texts.app,
        {
          body: texts[message.kind] || texts.app,
          tag,
          // A newer message about the same session alerts again, but only an approval is worth it.
          renotify: tag !== undefined && message.kind === 'approval',
          icon: '/icon-192.png',
          data: { url: ownPath(message.url) },
        },
      )
    })(),
  )
})

self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const url = ownPath(event.notification.data && event.notification.data.url)
  event.waitUntil(
    (async () => {
      const windows = await self.clients.matchAll({
        type: 'window',
        includeUncontrolled: true,
      })
      for (const win of windows) {
        try {
          // The console that is already open takes the person to the session; its own
          // state is not what they came back for.
          const focused = await win.focus()
          await (focused || win).navigate(url)
          return
        } catch {
          // A window this worker does not control cannot be navigated: try the next, then a new one.
        }
      }
      await self.clients.openWindow(url)
    })(),
  )
})
