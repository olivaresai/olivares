// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { cva, type VariantProps } from 'class-variance-authority'
import { Loader2 } from 'lucide-react'
import type { ComponentProps } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'

/**
 * Spinner — the indeterminate-progress mark: a spinning lucide Loader2. Inherits
 * `currentColor`, so it adopts its container's text color (use inside a Button or on
 * an accent surface as-is). It carries `role="status"` + an `aria-label` so screen
 * readers announce the loading state; the spin respects `prefers-reduced-motion`.
 *
 * The default accessible name is the LOCALIZED `common:states.loading` so a
 * standalone spinner is announced in the active console language, not English — the
 * ~10 bare `<Spinner />` call sites need no change. Button spinners that pass
 * `aria-hidden` are unaffected (the name is suppressed). Callers can still override
 * `aria-label` with a context-specific name.
 */
const spinnerVariants = cva('animate-spin shrink-0 text-current', {
  variants: {
    size: {
      sm: 'size-3.5',
      base: 'size-4',
      lg: 'size-6',
    },
  },
  defaultVariants: { size: 'base' },
})

export function Spinner({
  className,
  size,
  'aria-label': ariaLabel,
  ...props
}: ComponentProps<typeof Loader2> & VariantProps<typeof spinnerVariants>) {
  const { t } = useTranslation('common')
  return (
    <Loader2
      role="status"
      aria-label={ariaLabel ?? t('states.loading')}
      className={cn(spinnerVariants({ size }), className)}
      {...props}
    />
  )
}

export { spinnerVariants }
