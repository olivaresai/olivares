// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Toaster as Sonner } from 'sonner'
import type { ComponentProps, CSSProperties } from 'react'
import { cn } from '@/lib/utils'
import { useThemeStore } from '@/stores/theme'
import { TOAST_MS, toast } from './toast'

/**
 * Toaster — the app-wide notification surface, wrapping `sonner`, drawn as direction A2
 * draws a toast: one line on the INVERSE surface (`--inverse`, its words `--on-inverse`),
 * a pill with the pop shadow, at most one action in `--inverse-accent`, and a way to dismiss
 * it at the end of the line. It sits low and centred on the sheet it speaks about, not on
 * the window: beside the sidebar it moves right by half the sidebar's width (248 px, or the
 * 60 px rail); on a phone it sits above the bottom bar. `unstyled` hands every slot to
 * these classes, because sonner's own skin is unlayered CSS that would win over them. A toast's kind is its icon's shape and its words,
 * never a colour alone. Theme follows the app's resolved light/dark so toasts never flash
 * the wrong scheme. Re-exports `toast` so callers do `toast.success(...)` from one import.
 *
 * The container is a polite live region, so every toast is announced, and its action
 * and close buttons are ordinary buttons. Lifetime (6 seconds, an error until it is
 * dismissed) is the rule in `./toast`.
 */
export type ToasterProps = ComponentProps<typeof Sonner>

/** One line, up to 560 px, never wider than the screen less its gutters. */
const TOASTER_WIDTH = {
  '--width': 'min(560px, calc(100vw - 32px))',
} as CSSProperties

const ON_INVERSE_FOCUS =
  'outline-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--on-inverse)]'

export function Toaster({
  toastOptions,
  className,
  style,
  ...props
}: ToasterProps) {
  const theme = useThemeStore((s) => s.resolved)

  return (
    <Sonner
      theme={theme}
      position="bottom-center"
      offset={{ bottom: 48 }}
      richColors={false}
      duration={TOAST_MS}
      closeButton
      className={cn(
        // Below 761 px the phone's bottom bar (56 px) shows: the toast sits above it.
        'max-[761px]:bottom-[76px]!',
        'min-[761px]:[body:has([data-slot=sidebar]:not([data-folded]))_&]:left-[calc(50%+124px)]!',
        'min-[761px]:[body:has([data-slot=sidebar][data-folded])_&]:left-[calc(50%+30px)]!',
        className,
      )}
      style={{ ...TOASTER_WIDTH, ...style }}
      toastOptions={{
        ...toastOptions,
        unstyled: true,
        classNames: {
          toast: cn(
            'inset-x-0 mx-auto flex w-fit max-w-full items-center gap-1.5 font-sans',
            'min-h-11 rounded-[22px] bg-[var(--inverse)] py-1.5 ps-5 pe-1.5 text-body font-medium text-[var(--on-inverse)] shadow-pop',
          ),
          title: 'text-body font-medium',
          description: 'text-caption opacity-80',
          icon: 'shrink-0 [&_svg]:size-4',
          content: 'me-2.5 flex min-w-0 flex-col gap-0.5',
          actionButton: cn(
            'h-8 shrink-0 cursor-pointer rounded-full bg-current/10 px-3.5 text-body font-bold text-[var(--inverse-accent)]',
            'transition-colors hover:bg-current/15 active:bg-current/20',
            ON_INVERSE_FOCUS,
          ),
          cancelButton: cn(
            'h-8 shrink-0 cursor-pointer rounded-full bg-current/10 px-3.5 text-body font-bold',
            'transition-colors hover:bg-current/15 active:bg-current/24',
            ON_INVERSE_FOCUS,
          ),
          closeButton: cn(
            'order-last inline-flex size-8 shrink-0 cursor-pointer items-center justify-center rounded-full opacity-80',
            'transition-colors hover:bg-current/10 hover:opacity-100 active:bg-current/24',
            ON_INVERSE_FOCUS,
          ),
          ...toastOptions?.classNames,
        },
      }}
      {...props}
    />
  )
}

// eslint-disable-next-line react-refresh/only-export-components
export { toast }
