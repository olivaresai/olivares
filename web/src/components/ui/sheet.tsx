// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import * as DialogPrimitive from '@radix-ui/react-dialog'
import { cva, type VariantProps } from 'class-variance-authority'
import { X } from 'lucide-react'
import type { ComponentProps, HTMLAttributes } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'

/**
 * Sheet — a side drawer built on Radix Dialog (same focus trap / Escape / ARIA as
 * Dialog, just edge-anchored). Right side by default. Drawn as direction A2 draws a
 * dialog: the highest surface (`bg-raised`) with the pop shadow and a hairline on the inner
 * edge, a heading title, a small round close and the answers on their own band; slides in
 * via translate keyed off data-state at the sheet duration token. Mirrors Dialog's
 * Header/Footer/Title/Description.
 */

export const Sheet = DialogPrimitive.Root
export const SheetTrigger = DialogPrimitive.Trigger
export const SheetPortal = DialogPrimitive.Portal
export const SheetClose = DialogPrimitive.Close

function SheetOverlay({
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

const sheetVariants = cva(
  cn(
    'fixed z-50 flex flex-col gap-4 bg-raised shadow-pop p-6',
    // The answers' band reaches the sheet's foot when nothing but the close follows it.
    'has-[>[data-slot=sheet-footer]:last-child]:pb-0 has-[>[data-slot=sheet-footer]+[data-slot=sheet-close]:last-child]:pb-0',
    'transition-transform duration-[var(--duration-sheet)] ease-out',
  ),
  {
    variants: {
      side: {
        right:
          'inset-y-0 right-0 h-full w-full max-w-sm border-l border-line data-[state=closed]:translate-x-full data-[state=open]:translate-x-0',
        left: 'inset-y-0 left-0 h-full w-full max-w-sm border-r border-line data-[state=closed]:-translate-x-full data-[state=open]:translate-x-0',
        top: 'inset-x-0 top-0 w-full border-b border-line data-[state=closed]:-translate-y-full data-[state=open]:translate-y-0',
        bottom:
          'inset-x-0 bottom-0 w-full border-t border-line data-[state=closed]:translate-y-full data-[state=open]:translate-y-0',
      },
    },
    defaultVariants: { side: 'right' },
  },
)

export interface SheetContentProps
  extends
    ComponentProps<typeof DialogPrimitive.Content>,
    VariantProps<typeof sheetVariants> {
  /** Hide the built-in top-right X close button. */
  hideClose?: boolean
}

export function SheetContent({
  className,
  children,
  side = 'right',
  hideClose = false,
  ...props
}: SheetContentProps) {
  const { t } = useTranslation('common')
  return (
    <SheetPortal>
      <SheetOverlay />
      <DialogPrimitive.Content
        className={cn(sheetVariants({ side }), className)}
        {...props}
      >
        {children}
        {!hideClose && (
          <DialogPrimitive.Close
            data-slot="sheet-close"
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
    </SheetPortal>
  )
}

export function SheetHeader({
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

export function SheetFooter({
  className,
  ...props
}: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      data-slot="sheet-footer"
      className={cn(
        // The answers sit on their own band behind a hairline, at the bottom of the sheet.
        '-mx-6 mt-auto flex flex-col-reverse gap-2 border-t border-line bg-frame/30 px-6 py-3.5 *:min-w-22 sm:flex-row sm:justify-end',
        className,
      )}
      {...props}
    />
  )
}

export function SheetTitle({
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

export function SheetDescription({
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
