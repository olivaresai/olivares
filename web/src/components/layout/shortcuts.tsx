// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useNavigate } from '@tanstack/react-router'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from '@/components/ui/dialog'
import { Kbd } from '@/components/ui/kbd'
import { useAuth } from '@/lib/auth/context'
import { FEATURE_VIEWS } from '@/features/registry'

/** How long a leader sequence stays armed after pressing `g`. */
const SEQUENCE_TIMEOUT_MS = 1200

/** The g+letter navigation map: letter → feature id from the registry.
 * A binding only fires (and only shows in the help overlay) when RBAC lets the
 * operator see that feature — the same visibility rule as the sidebar. */
export const NAV_SHORTCUTS: ReadonlyArray<{ key: string; featureId: string }> =
  [
    { key: 'h', featureId: 'home' },
    { key: 'w', featureId: 'workspaceDashboard' },
    { key: 'i', featureId: 'inventory' },
    { key: 's', featureId: 'sessions' },
    { key: 'a', featureId: 'automations' },
    { key: 'e', featureId: 'eventing' },
    { key: 'n', featureId: 'alerting' },
    { key: 'o', featureId: 'orchestration' },
    { key: 'c', featureId: 'console' },
    { key: 'p', featureId: 'permissions' },
    { key: 'm', featureId: 'models' },
    { key: 'f', featureId: 'finops' },
    { key: 'd', featureId: 'dashboards' },
    { key: 'u', featureId: 'audit' },
    { key: 'k', featureId: 'knowledge' },
  ]

/** True when the event originates somewhere typing is expected — shortcuts must
 * never steal keystrokes from a form control or an editable region. */
function isTypingTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  const tag = target.tagName
  return (
    tag === 'INPUT' ||
    tag === 'TEXTAREA' ||
    tag === 'SELECT' ||
    target.isContentEditable
  )
}

/** True while any Radix dialog/sheet is open — g-navigation behind a modal
 * would move the page under the operator's feet. */
function modalIsOpen(): boolean {
  return !!document.querySelector(
    '[role="dialog"][data-state="open"], [role="alertdialog"][data-state="open"]',
  )
}

/** Global keyboard shortcuts: `g`+letter navigation sequences and the
 * `?` help overlay. Mounted once in the app shell next to the ⌘K palette. */
export function GlobalShortcuts() {
  const { t } = useTranslation(['common', 'nav'])
  const navigate = useNavigate()
  const { can } = useAuth()
  const [helpOpen, setHelpOpen] = useState(false)
  const armedUntil = useRef(0)

  const visible = NAV_SHORTCUTS.filter(({ featureId }) => {
    const view = FEATURE_VIEWS.find((v) => v.id === featureId)
    return (
      !!view && !view.hideInNav && (!view.permission || can(view.permission))
    )
  })

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey) return
      if (isTypingTarget(e.target)) return

      if (e.key === '?') {
        e.preventDefault()
        armedUntil.current = 0
        setHelpOpen((v) => !v)
        return
      }
      // Below here only the leader machinery — never with shift held.
      if (e.shiftKey) return
      if (helpOpen || modalIsOpen()) return

      const now = Date.now()
      if (e.key === 'g') {
        armedUntil.current = now + SEQUENCE_TIMEOUT_MS
        return
      }
      if (armedUntil.current < now) return
      armedUntil.current = 0
      const binding = visible.find((b) => b.key === e.key)
      if (!binding) return
      const view = FEATURE_VIEWS.find((v) => v.id === binding.featureId)
      if (!view) return
      e.preventDefault()
      void navigate({ to: view.path as never })
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [helpOpen, navigate, visible])

  return (
    <Dialog open={helpOpen} onOpenChange={setHelpOpen}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('common:shortcuts.title')}</DialogTitle>
          <DialogDescription>
            {t('common:shortcuts.description')}
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-4">
          <section>
            <h3 className="mb-2 text-xs font-medium tracking-wider text-muted-foreground uppercase">
              {t('common:shortcuts.general')}
            </h3>
            <dl className="flex flex-col gap-1.5 text-sm">
              <div className="flex items-center justify-between gap-3">
                <dt>{t('common:shortcuts.openPalette')}</dt>
                <dd className="flex gap-1">
                  <Kbd>⌘</Kbd>
                  <Kbd>K</Kbd>
                </dd>
              </div>
              <div className="flex items-center justify-between gap-3">
                <dt>{t('common:shortcuts.showHelp')}</dt>
                <dd>
                  <Kbd>?</Kbd>
                </dd>
              </div>
            </dl>
          </section>
          <section>
            <h3 className="mb-2 text-xs font-medium tracking-wider text-muted-foreground uppercase">
              {t('common:shortcuts.navigation')}
            </h3>
            <dl className="grid grid-cols-1 gap-1.5 text-sm sm:grid-cols-2">
              {visible.map(({ key, featureId }) => (
                <div
                  key={featureId}
                  className="flex items-center justify-between gap-3"
                >
                  <dt className="truncate">{t(`nav:items.${featureId}`)}</dt>
                  <dd className="flex gap-1">
                    <Kbd>g</Kbd>
                    <Kbd>{key}</Kbd>
                  </dd>
                </div>
              ))}
            </dl>
          </section>
        </div>
      </DialogContent>
    </Dialog>
  )
}
