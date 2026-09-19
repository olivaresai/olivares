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
//
// ⛔ THE PANES TAKE THE HEIGHT THEY HAVE, AND THEY USED TO TAKE 60 dvh.
//    `xl:h-[60dvh] xl:min-h-[26rem]` was not an arbitrary number: it was the only way
//    to make a workbench look deliberate INSIDE a page that scrolls, which is what the
//    shell used to impose on all 77 routes. The shell now declares two frames and this
//    route picks the workbench, so the panes are `flex-1 min-h-0` and divide the
//    viewport they were given. On a 900 px screen that is about 700 px of rail instead
//    of 540, and on a 1200 px screen it is 1000 instead of 720 — the difference between
//    a surface that fills the screen and one that leaves a third of it grey.
//
// ⛔ AND THE COMPOSER IS DOCKED AT THE BOTTOM OF THE NARRATIVE PANE. It is the same
//    component the shell used to dock to the bottom of the VIEWPORT, on every route
//    including the 60 that cannot start a session. Here it belongs to the pane it sits
//    in: the narrative is where work is read, and the composer is how the next one
//    starts. `frame="docked"` is a top hairline rather than a card, because a rounded
//    panel floating at the foot of a pane reads as a separate thing rather than as that
//    pane's own input.
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { WorkComposer } from '@/components/layout/work-composer'
import { isTypingTarget, resolveBinding } from '@/lib/keybindings/model'
import { KEYBINDINGS } from '@/lib/keybindings/table'
import { cn } from '@/lib/utils'
import type { ConversationItem } from './conversation-frames'
import { primaryRun, type UnifiedSession } from './provenance'
import {
  WORK_PANES,
  type EvidenceBlock,
  type SessionAddress,
  type WorkPane,
} from './session-address'
import { SessionContextPane } from './session-context-pane'
import { groupOf } from './session-groups'
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
  const run = primaryRun(resolution.session.runs)
  const [inspected, setInspected] = useState<ConversationItem | null>(null)

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
    <div
      className="flex min-h-0 min-w-0 flex-1 flex-col gap-2"
      data-testid="work-surface"
    >
      {/* Below `xl` only one pane fits, and which one is in the address bar — so a
          link shared from a laptop opens on the pane its author was reading. */}
      <div
        className="flex shrink-0 gap-1 xl:hidden"
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

      {/* THE THREE REGIONS: rail · work · inspector, at the widths the shell's own
          tokens name. Each pane scrolls on its own so reading the trace does not move
          the rail. `min-h-0` on the grid AND on each pane, or a pane refuses to shrink
          below its content and the whole surface grows a second scrollbar instead. */}
      <div className="grid min-h-0 flex-1 gap-2 xl:grid-cols-[minmax(0,var(--console-rail-width))_minmax(0,1fr)_minmax(0,var(--console-inspector-width))]">
        <section
          id={paneId('rail')}
          aria-label={t('surface.pane.rail')}
          className={cn(
            'min-h-0 overflow-y-auto rounded-lg border border-border bg-surface',
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

        {/* THE WORK PANE: the narrative scrolls and the composer is docked under it.
            The composer is `shrink-0` inside this column and the narrative is
            `flex-1 min-h-0`, so the composer RESERVES its height — the narrative's last
            line is never behind it, which is the whole difference between a docked
            input and the bar that used to float at the bottom of the viewport. */}
        <section
          id={paneId('narrative')}
          aria-label={t('surface.pane.narrative')}
          className={cn(
            'flex min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-surface',
            address.pane !== 'narrative' && 'hidden xl:flex',
          )}
        >
          <div className="min-h-0 flex-1 overflow-y-auto p-4">
            <SessionNarrative
              resolution={resolution}
              pinned={!!selected && pinned.has(selected)}
              onTogglePin={onTogglePin}
              onOpenDetail={onOpenDetail}
              evidence={address.evidence}
              onExpandEvidence={onEvidence}
              emptyAction={emptyAction}
              inspectedId={inspected?.id ?? null}
              onInspect={setInspected}
            />
          </div>
          <WorkComposer
            frame="docked"
            attached={run ? { run, group: groupOf(resolution.session) } : null}
          />
        </section>

        <section
          id={paneId('context')}
          aria-label={t('surface.pane.context')}
          className={cn(
            'min-h-0 overflow-y-auto rounded-lg border border-border bg-surface p-4',
            address.pane !== 'context' && 'hidden xl:block',
          )}
        >
          <SessionContextPane resolution={resolution} inspected={inspected} />
        </section>
      </div>
    </div>
  )
}
