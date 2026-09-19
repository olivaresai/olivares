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
import { useViewAccess } from '@/features/navigation/authorization'
import { RAIL_KEYS } from '@/features/navigation/rail-keys'
import { FEATURE_VIEWS } from '@/features/registry'
import {
  auditKeybindings,
  isTypingTarget,
  resolveBinding,
} from '@/lib/keybindings/model'
import { COMMAND_GROUP, KEY_GROUPS, KEYBINDINGS } from '@/lib/keybindings/table'
import { useCommandStore } from '@/stores/command'
import { usePreferencesStore } from '@/stores/preferences'
import { COMPOSER_INPUT_ID } from './work-composer-id'

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

/** True while any Radix dialog/sheet is open — g-navigation behind a modal
 * would move the page under the operator's feet. */
function modalIsOpen(): boolean {
  return !!document.querySelector(
    '[role="dialog"][data-state="open"], [role="alertdialog"][data-state="open"]',
  )
}

/** One chord, as keys an operator can read. `Mod` prints as ⌘ and the page says what
 * that means away from a Mac; `Space` prints as its own name rather than a blank. */
function chordTokens(keys: string): string[] {
  return keys
    .split('+')
    .map((p) => p.trim())
    .filter(Boolean)
    .map((p) => (p.toLowerCase() === 'mod' ? '⌘' : p))
}

/**
 * Global keyboard shortcuts. `g`+letter navigation sequences, and every
 * chord the DECLARED TABLE holds, resolved through it rather than matched by hand.
 *
 * ⛔ THIS IS ALSO THE PAGE THAT DOCUMENTS THEM, and that is the design: the overlay
 *    RENDERS the table, so the documentation cannot drift from the behaviour. A row
 *    that fires and is not listed, or is listed and does not fire, is not reachable
 *    from here.
 *
 * ⛔ AND IT REPORTS WHAT IT COULD NOT HONOUR. An invalid rule is ignored — never fatal
 *    — but silence would leave the operator pressing a key that does nothing with no
 *    way to learn why, so the page names it.
 */
export function GlobalShortcuts() {
  const { t } = useTranslation(['common', 'nav'])
  const navigate = useNavigate()
  // The SAME projection the sidebar, palette and directories use. G1-B adds no
  // administration shortcut: NAV_SHORTCUTS is unchanged, and this only stops the existing
  // entries from resolving through a predicate the others no longer share.
  const { navigable } = useViewAccess()
  const [helpOpen, setHelpOpen] = useState(false)
  const armedUntil = useRef(0)
  const { valid, problems } = auditKeybindings(KEYBINDINGS)

  const visible = NAV_SHORTCUTS.filter(({ featureId }) => {
    const view = FEATURE_VIEWS.find((v) => v.id === featureId)
    return !!view && !view.hideInNav && navigable(view)
  })

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // THE SHELL NEVER STEALS A KEYSTROKE FROM A FIELD. Every rule below is
      // shell-wide (no `when`), so this guard is what keeps `/` and `?` from firing
      // while the operator is typing a session name — including into the launcher,
      // which listens for its own chords itself, in its own context.
      const typing = isTypingTarget(e.target)

      // ⌘K FIRST, and it is the one chord that still works while typing: the palette
      // is how an operator LEAVES a field they opened by accident. It moved here from
      // `command-menu.tsx` so the console has ONE keyboard authority and one table.
      const chordCommand = resolveBinding(KEYBINDINGS, e)
      if (chordCommand === 'palette.open') {
        e.preventDefault()
        useCommandStore.getState().toggle()
        return
      }
      // Folding the rail is the other chord that works WHILE TYPING, and for the
      // same reason as the palette — it is how an operator gets the width back without
      // leaving the field they are in. Both are modified chords, so neither can be
      // typed by accident into a session name.
      if (chordCommand === 'nav.toggleRail') {
        e.preventDefault()
        usePreferencesStore.getState().toggleSidebar()
        return
      }
      if (typing) return

      if (chordCommand === 'help.toggle') {
        e.preventDefault()
        armedUntil.current = 0
        setHelpOpen((v) => !v)
        return
      }
      if (chordCommand === 'launcher.focus') {
        // `/` focuses the composer. The element is found by id rather than by a ref
        // through three components: this module must not import the composer, whose
        // queries would then load in every chunk that only wanted to move focus.
        //
        // ⛔ AND WHERE NO COMPOSER IS MOUNTED, THE KEY STILL REACHES ONE. The composer
        //    used to be in the shell, so the field was on every route and a missing
        //    element could only mean "this principal may not start runs". It is now
        //    mounted by the two screens where starting work IS the work, so
        //    a missing element usually means "you are somewhere else" — and the honest
        //    answer to `/` there is to go where the work is, not to swallow the key.
        //    `/sessions` is the destination because it mounts the composer beside the
        //    list the run will join; nothing is auto-typed and nothing is auto-started.
        const field = document.getElementById(COMPOSER_INPUT_ID)
        e.preventDefault()
        armedUntil.current = 0
        if (field instanceof HTMLElement) {
          field.focus()
        } else {
          void navigate({ to: '/sessions' as never } as never)
        }
        return
      }

      // Below here only the leader machinery — never with a modifier, never with
      // shift held. It is NOT in the table: a leader sequence is two keystrokes with a
      // timeout and a destination read from the feature registry, not a chord, and a
      // second copy of that list here would go stale against the registry.
      if (e.metaKey || e.ctrlKey || e.altKey) return
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
          {/* THE TABLE, RENDERED. Not a hand-written list beside it: a row that fires
              and is not printed here would be a keyboard secret, and a row printed here
              that does not fire would be a lie — and both are possible the moment the
              two are separate lists. */}
          {KEY_GROUPS.map((group) => {
            const rows = valid.filter(
              ({ rule }) => COMMAND_GROUP[rule.command] === group,
            )
            if (rows.length === 0) return null
            return (
              <section key={group}>
                <h3 className="mb-2 text-caption font-medium tracking-wider text-muted-foreground uppercase">
                  {t(`common:keys.group.${group}`)}
                </h3>
                <dl className="flex flex-col gap-1.5 text-body">
                  {rows.map(({ rule }, i) => (
                    <div
                      key={`${rule.command}:${i}`}
                      className="flex items-center justify-between gap-3"
                      data-testid={`keybinding-${rule.command}`}
                    >
                      <dt className="min-w-0 truncate">
                        {t(`common:keys.command.${rule.command}`)}
                        {rule.when ? (
                          <span className="ml-1.5 text-caption text-muted-foreground">
                            {t(`common:keys.when.${rule.when}`)}
                          </span>
                        ) : null}
                      </dt>
                      <dd className="flex shrink-0 gap-1">
                        {chordTokens(rule.keys).map((token) => (
                          <Kbd key={token}>{token}</Kbd>
                        ))}
                      </dd>
                    </div>
                  ))}
                </dl>
              </section>
            )
          })}

          {/* PRECEDENCE, STATED. It is the whole rule, and an operator who reads two
              rows with the same keys needs to know which one wins. */}
          <p className="text-caption text-muted-foreground">
            {t('common:keys.precedence')} {t('common:keys.modNote')}
          </p>

          {problems.length > 0 ? (
            // IGNORED, NEVER FATAL — AND NEVER SILENT. A rule the console could not
            // honour is named here, or the operator presses a key that does nothing
            // with no way to find out why.
            <p
              className="text-caption text-danger"
              data-testid="keybinding-problems"
            >
              {t('common:keys.problems', {
                count: problems.length,
                commands: problems
                  .map((p) => p.rule.command || p.rule.keys)
                  .join(', '),
              })}
            </p>
          ) : null}

          {/* THE RAIL'S OWN KEYS, from the module that implements them. They are not in
              the chord table because the shell does not resolve them — `railKey` does,
              inside the rail, the way `DataTable` owns the arrows of a grid. Printing
              them here from their own source is what keeps the page honest about `p`. */}
          <section>
            <h3 className="mb-2 text-caption font-medium tracking-wider text-muted-foreground uppercase">
              {t('common:keys.group.navRail')}
            </h3>
            <dl className="flex flex-col gap-1.5 text-body">
              {RAIL_KEYS.map(({ command, keys }) => (
                <div
                  key={command}
                  className="flex items-center justify-between gap-3"
                  data-testid={`keybinding-${command}`}
                >
                  <dt className="min-w-0 break-words">
                    {t(`common:keys.command.${command}`)}
                  </dt>
                  <dd className="flex shrink-0 gap-1">
                    <Kbd>{keys}</Kbd>
                  </dd>
                </div>
              ))}
            </dl>
            <p className="mt-2 text-caption text-muted-foreground">
              {t('common:keys.navRailNote')}
            </p>
          </section>

          <section>
            <h3 className="mb-2 text-caption font-medium tracking-wider text-muted-foreground uppercase">
              {t('common:shortcuts.navigation')}
            </h3>
            <dl className="grid grid-cols-1 gap-1.5 text-body sm:grid-cols-2">
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
