// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useEffect, useRef, useState } from 'react'
import { useRouterState } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import {
  notificationOwner,
  useClientSettings,
} from '@/features/settings/preferences'
import {
  decodeSessionAddress,
  PANE_PARAM,
  SESSION_PARAM,
} from '@/features/sessions/session-address'
import { useMinWidth } from '@/lib/hooks/use-min-width'
import { useSessionRail } from './use-session-rail'
import {
  attentionEvent,
  isOnScreen,
  type AttentionSnapshot,
} from './session-attention'

/** One observer for the whole authenticated shell, including its phone layout. */
export function SessionNotifications() {
  const boundary = useAuthBoundary()
  const owner = notificationOwner(boundary.principal, boundary.tenant)
  const selected = useClientSettings((s) => s.sessionNotificationsFor)
  return owner && selected === owner ? (
    <SessionAttentionObserver key={boundary.key} />
  ) : null
}

function SessionAttentionObserver() {
  const { t } = useTranslation('settings')
  const rail = useSessionRail(true)
  const wide = useMinWidth(761)
  const selection = useRouterState({
    select: (s) => {
      const path = s.location.pathname
      if (path !== '/sessions' && path !== '/agentops') return null
      const search = s.location.search as Record<string, unknown>
      return decodeSessionAddress({
        [SESSION_PARAM]:
          typeof search[SESSION_PARAM] === 'string'
            ? search[SESSION_PARAM]
            : undefined,
        [PANE_PARAM]:
          typeof search[PANE_PARAM] === 'string'
            ? search[PANE_PARAM]
            : undefined,
      }).value
    },
  })
  const opened =
    selection && (wide || selection.pane !== 'rail') ? selection.address : null
  const [hidden, setHidden] = useState(() => document.hidden)
  const previous = useRef(new Map<string, AttentionSnapshot>())
  const delivered = useRef(new Map<string, number>())
  const notifications = useRef(
    new Map<
      string,
      { notification: Notification; event: 'needsYou' | 'finished' }
    >(),
  )
  // A failure while hidden is explained once the person is back and can read it.
  const failed = useRef<'none' | 'pending' | 'shown'>('none')
  const title = useRef('')

  useEffect(() => {
    // Predecessor cleanup runs before setup; a renewed credential cannot capture its count.
    const baseTitle = document.title
    title.current = baseTitle
    const update = () => setHidden(document.hidden)
    document.addEventListener('visibilitychange', update)
    const openNotifications = notifications.current
    return () => {
      document.removeEventListener('visibilitychange', update)
      document.title = baseTitle
      for (const { notification } of openNotifications.values())
        notification.close()
      openNotifications.clear()
    }
  }, [])

  useEffect(() => {
    const current = rail.attentionReady
      ? new Map(rail.attention.map((s) => [s.key, s]))
      : previous.current
    for (const [key, { notification, event }] of notifications.current) {
      const session = current.get(key)
      if (
        !session ||
        (event === 'needsYou' && session.state !== 'needs-you') ||
        session.run !== previous.current.get(key)?.run ||
        session.state === 'ended' ||
        (!hidden && isOnScreen(session, opened))
      ) {
        notification.close()
        notifications.current.delete(key)
      }
    }
    for (const key of delivered.current.keys())
      if (!current.has(key)) delivered.current.delete(key)
    const count = [...current.values()].filter(
      (s) => s.state === 'needs-you' && (hidden || !isOnScreen(s, opened)),
    ).length
    document.title = count ? `(${count}) ${title.current}` : title.current
    if (!hidden && failed.current === 'pending') {
      toast.message(t('sessionNotifications.unavailable'))
      failed.current = 'shown'
    }
    if (!rail.attentionReady) return
    const now = Date.now()
    for (const session of rail.attention) {
      const event = attentionEvent(previous.current.get(session.key), session, {
        hidden,
        opened,
        now,
        lastNotified: delivered.current.get(session.key),
      })
      if (
        !event ||
        !hidden ||
        typeof Notification === 'undefined' ||
        Notification.permission !== 'granted'
      )
        continue
      try {
        // Close the old one first: a browser may identify a notification by its tag.
        notifications.current.get(session.key)?.notification.close()
        const notification = new Notification(session.name, {
          body: t(`sessionNotifications.${event}`),
          tag: session.key,
          silent: true,
        })
        notifications.current.set(session.key, { notification, event })
        delivered.current.set(session.key, now)
      } catch {
        notifications.current.delete(session.key)
        if (failed.current === 'none') failed.current = 'pending'
      }
    }
    previous.current = current
  }, [rail.attention, rail.attentionReady, hidden, opened, t])
  return null
}
