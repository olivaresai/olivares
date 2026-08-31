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
 * Dialog — the centered modal primitive over Radix Dialog. Flat warm-graphite
 * panel: `bg-elevated`, strong hairline border, `rounded-xl`, `shadow-xl`. The
 * overlay is a calm dim (`bg-overlay`); enter/exit are subtle data-state opacity
 * (overlay) + opacity/scale (content) transitions, ≤200ms. A top-right X close
 * is built into `DialogContent` with an sr-only label. Radix supplies focus trap,
 * Escape-to-close, scroll-lock and full ARIA — never hand-roll those.
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
        'fixed inset-0 z-50 bg-overlay backdrop-blur-[2px]',
        'transition-opacity duration-150 ease-out',
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
}

export function DialogContent({
  className,
  children,
  hideClose = false,
  ...props
}: DialogContentProps) {
  const { t } = useTranslation('common')
  return (
    <DialogPortal>
      <DialogOverlay />
      <DialogPrimitive.Content
        className={cn(
          'fixed left-1/2 top-1/2 z-50 grid w-full max-w-lg -translate-x-1/2 -translate-y-1/2 gap-4',
          'bg-elevated border border-border-strong rounded-xl shadow-xl p-6',
          'transition duration-200 ease-out',
          'data-[state=open]:opacity-100 data-[state=closed]:opacity-0',
          'data-[state=open]:scale-100 data-[state=closed]:scale-[0.98]',
          className,
        )}
        {...props}
      >
        {children}
        {!hideClose && (
          <DialogPrimitive.Close
            className={cn(
              'absolute right-4 top-4 inline-flex size-7 items-center justify-center rounded-md',
              'text-muted-foreground transition-colors duration-100 ease-out outline-none',
              'hover:bg-muted hover:text-foreground',
              'focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
              'disabled:pointer-events-none disabled:opacity-50',
              '[&_svg]:size-4 [&_svg]:shrink-0',
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
      className={cn(
        'flex flex-col-reverse gap-2 sm:flex-row sm:justify-end',
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
      className={cn(
        'text-lg font-display font-semibold leading-tight text-foreground',
        className,
      )}
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
      className={cn('text-sm text-muted-foreground', className)}
      {...props}
    />
  )
}
