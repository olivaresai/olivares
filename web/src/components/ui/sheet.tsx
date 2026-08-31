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
 * Dialog, just edge-anchored). Right side by default. Flat `bg-elevated` panel with
 * a single strong hairline on the inner edge; slides in via translate keyed off
 * data-state (≤220ms ease-out). Mirrors Dialog's Header/Footer/Title/Description and
 * carries the same built-in top-right X close.
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
        'fixed inset-0 z-50 bg-overlay backdrop-blur-[2px]',
        'transition-opacity duration-200 ease-out',
        'data-[state=open]:opacity-100 data-[state=closed]:opacity-0',
        className,
      )}
      {...props}
    />
  )
}

const sheetVariants = cva(
  cn(
    'fixed z-50 flex flex-col gap-4 bg-elevated shadow-xl p-6',
    'transition-transform duration-200 ease-out',
  ),
  {
    variants: {
      side: {
        right:
          'inset-y-0 right-0 h-full w-full max-w-sm border-l border-border-strong data-[state=closed]:translate-x-full data-[state=open]:translate-x-0',
        left: 'inset-y-0 left-0 h-full w-full max-w-sm border-r border-border-strong data-[state=closed]:-translate-x-full data-[state=open]:translate-x-0',
        top: 'inset-x-0 top-0 w-full border-b border-border-strong data-[state=closed]:-translate-y-full data-[state=open]:translate-y-0',
        bottom:
          'inset-x-0 bottom-0 w-full border-t border-border-strong data-[state=closed]:translate-y-full data-[state=open]:translate-y-0',
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
      className={cn(
        'mt-auto flex flex-col-reverse gap-2 sm:flex-row sm:justify-end',
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
      className={cn(
        'text-lg font-display font-semibold leading-tight text-foreground',
        className,
      )}
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
      className={cn('text-sm text-muted-foreground', className)}
      {...props}
    />
  )
}
