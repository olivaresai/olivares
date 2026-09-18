// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE WORK SURFACE — three panes at `xl`, one pane at a time below it.
//
// Two reviews said the same thing twice: a first screen should be *"a place to WORK"*,
// and this one was *"a list with a detail"*. Three panes are what a place to
// work looks like — what needs you, what happened, and what it applies to, all on
// screen at once, so reading one does not cost the other two.
//
// ⛔ THE LIST IS NOT DELETED, AND THAT IS THE CANON'S FIRST HARD RULE: the answer to a
//    problem is never to remove a function. The table keeps every column, facet, search
//    and sort it had; the surface is the DEFAULT presentation and the table is one
//    click away. A rail cannot sort by cost across two hundred rows, and a table cannot
//    tell an operator what a session is doing right now — they are not the same
//    instrument and this screen holds both.
//
// ⛔ ALL THREE PANES ARE IN THE DOM AT EVERY WIDTH, and below `xl` the two that are not
//    in front are hidden with a CLASS, never unmounted. Unmounting would throw away the
//    evidence read and the rail's scroll position every time an operator switched pane
//    on a laptop, and would make the pane switch — which is URL state — cost a request.
//
// ⛔ AND THE SWITCHER IS NOT A TAB STRIP. At `xl` all three panes are visible, so there
//    is nothing to switch and the control is not rendered: a tablist that claims three
//    tabs while all three panels are on screen describes a screen that does not exist.
import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { isTypingTarget, resolveBinding } from '@/lib/keybindings/model'
import { KEYBINDINGS } from '@/lib/keybindings/table'
import { cn } from '@/lib/utils'
import type { UnifiedSession } from './provenance'
import {
  WORK_PANES,
  type EvidenceBlock,
  type SessionAddress,
  type WorkPane,
} from './session-address'
import { SessionContextPane } from './session-context-pane'
import { SessionNarrative } from './session-narrative'
import type { SessionResolution } from './use-session-resolution'
import { WorkRail } from './work-rail'
import './i18n'

export interface WorkSurfaceProps {
  sessions: readonly UnifiedSession[]
  loading: boolean
  address: SessionAddress
  resolution: SessionResolution
  pinned: ReadonlySet<string>
  onTogglePin: ((address: string) => void) | null
  onOpen: (session: UnifiedSession) => void
  onPane: (pane: WorkPane) => void
  onEvidence: (block: EvidenceBlock) => void
  onOpenDetail: () => void
  /** The one thing to do when there is nothing here — already gated by the caller. */
  emptyAction?: React.ReactNode
}

/** The id of a pane's region, so the switcher can name what it controls. */
const paneId = (pane: WorkPane) => `work-pane-${pane}`

export function WorkSurface({
  sessions,
  loading,
  address,
  resolution,
  pinned,
  onTogglePin,
  onOpen,
  onPane,
  onEvidence,
  onOpenDetail,
  emptyAction,
}: WorkSurfaceProps) {
  const { t } = useTranslation('sessions')
  const selected = address.address

  /**
   * MOVING BETWEEN PANES FROM THE KEYBOARD. The two chords are table rows under
   * `workSurface`, so they exist only while this surface is mounted and never while the
   * operator is typing. The handler sets no state — it reports the pane, and the URL
   * owns it, like every other choice on this screen.
   */
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (isTypingTarget(e.target)) return
      const command = resolveBinding(KEYBINDINGS, e, { workSurface: true })
      if (command !== 'surface.nextPane' && command !== 'surface.previousPane')
        return
      e.preventDefault()
      const at = WORK_PANES.indexOf(address.pane)
      const step = command === 'surface.nextPane' ? 1 : -1
      // Wrapping IS right here, and it is the opposite of the rail's rule: three panes
      // are a cycle an operator walks round, not a list with an end to discover.
      const next =
        WORK_PANES[(at + step + WORK_PANES.length) % WORK_PANES.length]
      onPane(next)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [address.pane, onPane])

  return (
    <div className="flex flex-col gap-3" data-testid="work-surface">
      {/* Below `xl` only one pane fits, and which one is in the address bar — so a
          link shared from a laptop opens on the pane its author was reading. */}
      <div
        className="flex gap-1 xl:hidden"
        role="group"
        aria-label={t('surface.paneSwitcher')}
      >
        {WORK_PANES.map((pane) => (
          <button
            key={pane}
            type="button"
            aria-pressed={address.pane === pane}
            aria-controls={paneId(pane)}
            data-testid={`pane-button-${pane}`}
            onClick={() => onPane(pane)}
            className={cn(
              'flex-1 rounded-md border px-2 py-1.5 text-caption font-medium outline-none transition-colors',
              'focus-visible:ring-2 focus-visible:ring-ring',
              address.pane === pane
                ? 'border-accent-line bg-accent-soft text-accent-text'
                : 'border-border text-muted-foreground hover:bg-muted',
            )}
          >
            {t(`surface.pane.${pane}`)}
          </button>
        ))}
      </div>

      <div className="grid gap-3 xl:grid-cols-[minmax(0,17rem)_minmax(0,1fr)_minmax(0,19rem)]">
        {/* A FIXED PANE HEIGHT AT `xl`, AND IT IS A DECISION. Each pane scrolls on its
            own so reading the trace does not move the rail, and the surface keeps the
            shape of a workbench rather than growing a page-long column per pane. The
            height is viewport-relative with a floor, so a short laptop screen still
            shows a usable rail instead of three slivers. */}
        <section
          id={paneId('rail')}
          aria-label={t('surface.pane.rail')}
          className={cn(
            'overflow-y-auto rounded-lg border border-border bg-surface xl:h-[60dvh] xl:min-h-[26rem]',
            address.pane !== 'rail' && 'hidden xl:block',
          )}
        >
          <WorkRail
            sessions={sessions}
            selected={selected}
            onOpen={onOpen}
            pinned={pinned}
            onTogglePin={onTogglePin}
            emptyAction={emptyAction}
            loading={loading}
          />
        </section>

        <section
          id={paneId('narrative')}
          aria-label={t('surface.pane.narrative')}
          className={cn(
            'overflow-y-auto rounded-lg border border-border bg-surface p-4 xl:h-[60dvh] xl:min-h-[26rem]',
            address.pane !== 'narrative' && 'hidden xl:block',
          )}
        >
          <SessionNarrative
            resolution={resolution}
            pinned={!!selected && pinned.has(selected)}
            onTogglePin={onTogglePin}
            onOpenDetail={onOpenDetail}
            evidence={address.evidence}
            onExpandEvidence={onEvidence}
            emptyAction={emptyAction}
          />
        </section>

        <section
          id={paneId('context')}
          aria-label={t('surface.pane.context')}
          className={cn(
            'overflow-y-auto rounded-lg border border-border bg-surface p-4 xl:h-[60dvh] xl:min-h-[26rem]',
            address.pane !== 'context' && 'hidden xl:block',
          )}
        >
          <SessionContextPane resolution={resolution} />
        </section>
      </div>
    </div>
  )
}
