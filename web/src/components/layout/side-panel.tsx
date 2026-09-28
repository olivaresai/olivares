// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SIDE PANEL (redesign §3.2): a column beside the work, 380 px (340 px at or below
// 1360 px), and an overlay sheet over the work below 1100 px. The frame owns its place and
// its toggle; a page owns its content. A page declares a panel by rendering
// `<SidePanelContent title=…>`: the frame then shows the toggle in the top bar and draws
// the content in the panel, through a portal, so the page's state stays the page's.
//
// A page that declares nothing gets no panel and no toggle: the work keeps the width.
import { PanelRight, X } from 'lucide-react'
import {
  createContext,
  useContext,
  useEffect,
  useId,
  useMemo,
  useState,
  type ReactNode,
} from 'react'
import { createPortal } from 'react-dom'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'

/** Below this width the panel is an overlay sheet over the work. */
const OVERLAY_QUERY = '(max-width: 1099px)'

interface SidePanelState {
  /** The declared panel's title, or null when the page declares none. */
  title: string | null
  open: boolean
  host: HTMLElement | null
  panelId: string
  declare: (title: string | null) => void
  setOpen: (open: boolean) => void
  setHost: (host: HTMLElement | null) => void
}

const SidePanelContext = createContext<SidePanelState | null>(null)

function startsOpen(): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function')
    return true
  return !window.matchMedia(OVERLAY_QUERY).matches
}

export function SidePanelProvider({ children }: { children: ReactNode }) {
  const [title, declare] = useState<string | null>(null)
  const [open, setOpen] = useState(startsOpen)
  const [host, setHost] = useState<HTMLElement | null>(null)
  const panelId = useId()
  const value = useMemo(
    () => ({ title, open, host, panelId, declare, setOpen, setHost }),
    [title, open, host, panelId],
  )
  return (
    <SidePanelContext.Provider value={value}>
      {children}
    </SidePanelContext.Provider>
  )
}

/** A page's panel. Rendered wherever the page likes; drawn in the frame's panel. */
export function SidePanelContent({
  title,
  children,
}: {
  title: string
  children: ReactNode
}) {
  const panel = useContext(SidePanelContext)
  const declare = panel?.declare
  useEffect(() => {
    if (!declare) return
    declare(title)
    return () => declare(null)
  }, [declare, title])
  if (!panel?.host || !panel.open) return null
  return createPortal(children, panel.host)
}

/** The panel's place beside the work. Nothing is drawn while no page declares a panel. */
export function SidePanelHost() {
  const { t } = useTranslation('nav')
  const panel = useContext(SidePanelContext)
  const title = panel?.title ?? null
  const open = panel?.open ?? false
  const panelId = panel?.panelId
  const setOpen = panel?.setOpen
  const setHost = panel?.setHost
  if (!title || !open || !setOpen || !setHost) return null
  const close = () => setOpen(false)
  return (
    <>
      {/* Below 1100 px the panel covers the work; the scrim closes it. */}
      <div
        aria-hidden
        onClick={close}
        className="absolute inset-0 z-10 hidden bg-scrim max-[1099px]:block"
      />
      <aside
        id={panelId}
        aria-label={title}
        data-slot="side-panel"
        onKeyDown={(e) => {
          if (e.key === 'Escape') close()
        }}
        className={cn(
          'flex min-h-0 w-[380px] shrink-0 flex-col border-l border-line bg-canvas max-[1360px]:w-[340px]',
          'max-[1099px]:absolute max-[1099px]:inset-y-0 max-[1099px]:right-0 max-[1099px]:z-20 max-[1099px]:max-w-full max-[1099px]:shadow-pop',
        )}
      >
        <div className="flex h-12 shrink-0 items-center gap-2 border-b border-line pr-2 pl-4">
          <h2 className="min-w-0 flex-1 truncate text-heading">{title}</h2>
          <button
            type="button"
            onClick={close}
            aria-label={t('shell.panel.close')}
            className="grid size-8 place-items-center rounded-ctl text-text-2 outline-none hover:bg-hover hover:text-text focus-visible:ring-2 focus-visible:ring-focus"
          >
            <X aria-hidden className="size-4" />
          </button>
        </div>
        <div
          ref={setHost}
          className="flex min-h-0 flex-1 flex-col overflow-y-auto"
        />
      </aside>
    </>
  )
}

/** The top bar's panel toggle: present only while a page declares a panel. */
export function SidePanelToggle() {
  const { t } = useTranslation('nav')
  const panel = useContext(SidePanelContext)
  if (!panel?.title) return null
  return (
    <button
      type="button"
      onClick={() => panel.setOpen(!panel.open)}
      aria-pressed={panel.open}
      aria-controls={panel.open ? panel.panelId : undefined}
      aria-label={t(panel.open ? 'shell.panel.hide' : 'shell.panel.show', {
        title: panel.title,
      })}
      className="grid size-8 shrink-0 place-items-center rounded-ctl text-text-2 outline-none hover:bg-hover hover:text-text focus-visible:ring-2 focus-visible:ring-focus aria-pressed:bg-active aria-pressed:text-text"
    >
      <PanelRight aria-hidden className="size-4" />
    </button>
  )
}
