// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Toaster as Sonner, toast } from 'sonner'
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'
import { useThemeStore } from '@/stores/theme'

/**
 * Toaster — the app-wide notification surface, wrapping `sonner` and re-skinned
 * to brandv4. We disable sonner's `richColors` (it ships saturated fills
 * that clash with our calm palette) and instead map every slot to our tokens: an
 * elevated, hairline-bordered card, muted descriptions, a copper action button,
 * and a thin semantic left rule per intent (success/error/warning/info). Theme
 * follows the app's resolved light/dark so toasts never flash the wrong scheme.
 * Re-exports `toast` so callers do `toast.success(...)` from one import.
 */
export type ToasterProps = ComponentProps<typeof Sonner>

export function Toaster({ toastOptions, ...props }: ToasterProps) {
  const theme = useThemeStore((s) => s.resolved)

  return (
    <Sonner
      theme={theme}
      position="bottom-right"
      richColors={false}
      closeButton
      toastOptions={{
        ...toastOptions,
        classNames: {
          toast: cn(
            'group font-sans bg-elevated border border-border-strong text-foreground',
            'rounded-lg shadow-lg gap-2 p-3 text-sm',
          ),
          title: 'text-sm font-medium text-foreground',
          description: 'text-xs text-muted-foreground',
          icon: 'shrink-0 [&_svg]:size-4',
          content: 'gap-0.5',
          actionButton: cn(
            'h-7 rounded-md bg-accent px-2.5 text-xs font-medium text-accent-foreground',
            'hover:bg-accent-hover active:bg-accent-active',
            'outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
          ),
          cancelButton: cn(
            'h-7 rounded-md border border-border-strong bg-surface px-2.5 text-xs font-medium text-foreground',
            'hover:bg-muted',
            'outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
          ),
          closeButton: cn(
            'border border-border-strong bg-surface text-muted-foreground',
            'hover:bg-muted hover:text-foreground',
            'outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
          ),
          success:
            'border-l-2 border-l-success-line [&_[data-icon]]:text-success',
          error: 'border-l-2 border-l-danger-line [&_[data-icon]]:text-danger',
          warning:
            'border-l-2 border-l-warning-line [&_[data-icon]]:text-warning',
          info: 'border-l-2 border-l-info-line [&_[data-icon]]:text-info',
          ...toastOptions?.classNames,
        },
      }}
      {...props}
    />
  )
}

export { toast }
