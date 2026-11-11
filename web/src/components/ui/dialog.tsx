// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import * as DialogPrimitive from '@radix-ui/react-dialog'
import { VisuallyHidden } from '@radix-ui/react-visually-hidden'
import { X } from 'lucide-react'
import type { ComponentProps, HTMLAttributes } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'

/**
 * Dialog — the centered modal primitive over Radix Dialog, as direction A2 draws it: the
 * highest surface (`bg-raised`) with the pop shadow and a hairline inside it instead of an
 * outline, a heading title, a small round close and the answers (`DialogFooter`) on their own
 * band behind a hairline. The overlay is a calm dim (`bg-overlay`); enter/exit are subtle
 * data-state opacity (overlay) + opacity/scale (content) transitions at the panel duration
 * token. The close is built into `DialogContent` with an sr-only label. Radix supplies focus
 * trap, Escape-to-close, scroll-lock and full ARIA — never hand-roll those.
 */

export const Dialog = DialogPrimitive.Root
export const DialogTrigger = DialogPrimitive.Trigger
export const DialogPortal = DialogPrimitive.Portal
export const DialogClose = DialogPrimitive.Close

export { VisuallyHidden }

export function DialogOverlay({
  className,
  ...props
}: ComponentProps<typeof DialogPrimitive.Overlay>) {
  return (
    <DialogPrimitive.Overlay
      className={cn(
        'fixed inset-0 z-50 bg-overlay',
        'transition-opacity duration-[var(--duration-panel)] ease-out',
        'data-[state=open]:opacity-100 data-[state=closed]:opacity-0',
        className,
      )}
      {...props}
    />
  )
}

export interface DialogContentProps extends ComponentProps<
  typeof DialogPrimitive.Content
> {
  /** Hide the built-in top-right X close button. */
  hideClose?: boolean
  /** A consent, approval or step-up dialog: a brand or pack cannot restyle it (TrustChrome). */
  trust?: boolean
}

export function DialogContent({
  className,
  children,
  hideClose = false,
  trust = false,
  ...props
}: DialogContentProps) {
  const { t } = useTranslation('common')
  return (
    <DialogPortal>
      <DialogOverlay />
      <DialogPrimitive.Content
        data-trust-chrome={trust ? '' : undefined}
        className={cn(
          trust && 'text-foreground',
          'fixed left-1/2 top-1/2 z-50 grid w-[calc(100%-2rem)] max-w-lg max-h-[calc(100dvh-2rem)] -translate-x-1/2 -translate-y-1/2 gap-4 overflow-y-auto',
          'rounded-panel border-0 bg-raised p-6 shadow-[var(--shadow-pop),inset_0_0_0_1px_var(--line)]',
          // The answers' band reaches the dialog's foot when nothing but the close follows it.
          'has-[>[data-slot=dialog-footer]:last-child]:pb-0 has-[>[data-slot=dialog-footer]+[data-slot=dialog-close]:last-child]:pb-0',
          'transition-[opacity,scale] duration-[var(--duration-panel)] ease-out',
          'data-[state=open]:opacity-100 data-[state=closed]:opacity-0',
          'data-[state=open]:scale-100 data-[state=closed]:scale-[0.98]',
          className,
        )}
        {...props}
      >
        {children}
        {!hideClose && (
          <DialogPrimitive.Close
            data-slot="dialog-close"
            className={cn(
              // A small round close, as in a GNOME dialog, with a 44 px hit area.
              'absolute right-4 top-4 inline-flex size-7 items-center justify-center rounded-full',
              "bg-active text-text-2 transition-colors ease-out outline-none after:absolute after:-inset-2 after:content-['']",
              'hover:bg-[var(--pressed)] hover:text-text',
              'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus',
              'disabled:pointer-events-none disabled:opacity-45',
              '[&_svg]:size-3.5 [&_svg]:shrink-0',
            )}
          >
            <X />
            <span className="sr-only">{t('actions.close')}</span>
          </DialogPrimitive.Close>
        )}
      </DialogPrimitive.Content>
    </DialogPortal>
  )
}

export function DialogHeader({
  className,
  ...props
}: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn('flex flex-col gap-1.5 text-left', className)}
      {...props}
    />
  )
}

export function DialogFooter({
  className,
  ...props
}: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      data-slot="dialog-footer"
      className={cn(
        // The answers sit on their own band behind a hairline, edge to edge under the
        // body's padding.
        '-mx-6 mt-2 flex flex-col-reverse gap-2 border-t border-line bg-frame/30 px-6 py-3.5 *:min-w-22 sm:flex-row sm:justify-end',
        className,
      )}
      {...props}
    />
  )
}

export function DialogTitle({
  className,
  ...props
}: ComponentProps<typeof DialogPrimitive.Title>) {
  return (
    <DialogPrimitive.Title
      className={cn('font-display text-heading text-foreground', className)}
      {...props}
    />
  )
}

export function DialogDescription({
  className,
  ...props
}: ComponentProps<typeof DialogPrimitive.Description>) {
  return (
    <DialogPrimitive.Description
      className={cn('text-body text-muted-foreground', className)}
      {...props}
    />
  )
}
