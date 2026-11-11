// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Maximize2, Minimize2 } from 'lucide-react'
import { Dialog, DialogContent, DialogTitle } from './dialog'
import { Button } from './button'
import { cn } from '@/lib/utils'
import '@/features/shared/i18n'

/** Review over the thread, with Radix's focus trap and return-to-trigger behavior. */
export function DiffDialog({
  title,
  onClose,
  children,
}: {
  title: string
  onClose: () => void
  children: ReactNode
}) {
  const { t } = useTranslation('shared')
  const [maximized, setMaximized] = useState(false)
  const [opener] = useState(() => document.activeElement)
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent
        aria-describedby={undefined}
        onCloseAutoFocus={(event) => {
          event.preventDefault()
          if (opener instanceof HTMLElement) opener.focus()
        }}
        // Below md a bottom sheet the width of the screen; from md a card over the thread.
        className={cn(
          'inset-x-0 bottom-0 top-auto w-full max-w-none max-h-none translate-x-0 translate-y-0 content-start rounded-b-none pb-[max(1.5rem,env(safe-area-inset-bottom))]',
          'md:inset-x-auto md:bottom-auto md:left-1/2 md:top-1/2 md:-translate-x-1/2 md:-translate-y-1/2 md:rounded-panel md:pb-6',
          maximized
            ? 'h-[calc(100dvh-1rem)] md:w-[calc(100vw-1rem)]'
            : 'h-[calc(100dvh-2rem)] md:h-[85dvh] md:w-[80vw]',
        )}
      >
        <div className="flex min-w-0 items-center gap-3 pr-8">
          <DialogTitle className="min-w-0 flex-1 break-all">
            {title}
          </DialogTitle>
          <Button
            type="button"
            size="sm"
            variant="ghost"
            onClick={() => setMaximized(!maximized)}
          >
            {maximized ? <Minimize2 /> : <Maximize2 />}
            {t(maximized ? 'diff.restore' : 'diff.maximize')}
          </Button>
        </div>
        {children}
      </DialogContent>
    </Dialog>
  )
}
