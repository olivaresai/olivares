// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useEffect, useId, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { useClientSettings } from './preferences'
import {
  currentPush,
  disablePush,
  enablePush,
  needsInstallForPush,
  pushSupported,
  pushTextsFrom,
} from './web-push'

export function SessionNotificationSetting({
  owner,
}: {
  owner: string | null
}) {
  const { t } = useTranslation('settings')
  const id = useId()
  const selected = useClientSettings((s) => s.sessionNotificationsFor)
  const setSelected = useClientSettings((s) => s.setSessionNotificationsFor)
  const enabled = owner !== null && selected === owner
  const [permission, setPermission] = useState(() =>
    typeof Notification === 'undefined'
      ? 'unsupported'
      : Notification.permission,
  )
  const [pendingFor, setPendingFor] = useState<string | null>(null)
  // Whether this browser is also registered for push, so a closed tab is told too. 'off' until
  // the switch is on; 'failed' keeps the in-page notices and says what is missing.
  const [push, setPush] = useState<'off' | 'on' | 'failed'>('off')
  const pending = owner !== null && pendingFor === owner
  const requestOwner = useRef(owner)
  useEffect(() => {
    requestOwner.current = owner
    return () => {
      requestOwner.current = null
    }
  }, [owner])

  // A page that opens with the switch already on shows what this browser really holds. Only
  // that opening state: a switch turned on here knows its own outcome from enablePush below.
  const [openedOn] = useState(enabled)
  useEffect(() => {
    if (!openedOn || !pushSupported()) return
    let current = true
    void currentPush()
      .catch(() => false)
      .then((on) => current && setPush(on ? 'on' : 'failed'))
    return () => {
      current = false
    }
  }, [openedOn])

  const toggle = async () => {
    if (enabled && permission !== 'default') {
      setSelected(null)
      setPush('off')
      void disablePush().catch(() => undefined)
      return
    }
    if (!owner || typeof Notification === 'undefined') return
    setPendingFor(owner)
    try {
      // The browser must see the person's click, before any other asynchronous work.
      const answer =
        Notification.permission === 'default'
          ? await Notification.requestPermission()
          : Notification.permission
      if (requestOwner.current !== owner) return
      setPermission(answer)
      if (answer === 'granted') {
        setSelected(owner)
        if (pushSupported()) {
          try {
            await enablePush(pushTextsFrom(t))
            if (requestOwner.current === owner) setPush('on')
          } catch {
            if (requestOwner.current === owner) setPush('failed')
          }
        }
      }
    } catch {
      if (requestOwner.current === owner) setPermission('unsupported')
    } finally {
      if (requestOwner.current === owner) setPendingFor(null)
    }
  }

  const status =
    permission === 'denied'
      ? t('sessionNotifications.denied')
      : permission === 'unsupported'
        ? needsInstallForPush()
          ? t('sessionNotifications.installHint')
          : t('sessionNotifications.unavailable')
        : enabled && push === 'on'
          ? t('sessionNotifications.pushOn')
          : enabled && push === 'failed'
            ? t('sessionNotifications.pushUnavailable')
            : ''
  return (
    <div className="grid grid-cols-1 items-center gap-3 border-t border-line py-3.5 sm:grid-cols-[minmax(0,1fr)_auto]">
      <div className="min-w-0">
        <p className="text-body font-medium text-text">
          {t('sessionNotifications.title')}
        </p>
        <p id={id} className="text-caption text-text-2">
          {t('sessionNotifications.hint')}
        </p>
        <p role="status" className="text-caption text-text-2">
          {status}
        </p>
      </div>
      <Button
        type="button"
        variant="outline"
        aria-pressed={enabled}
        aria-describedby={id}
        disabled={
          pending ||
          !owner ||
          (!enabled &&
            (permission === 'denied' || permission === 'unsupported'))
        }
        onClick={() => void toggle()}
        className="justify-self-start aria-pressed:bg-accent-soft aria-pressed:text-accent-text sm:justify-self-end"
      >
        {t('sessionNotifications.notifyMe')}
      </Button>
    </div>
  )
}
