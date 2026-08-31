// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Fallback, Image, Root } from '@radix-ui/react-avatar'
import { cva, type VariantProps } from 'class-variance-authority'
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * Avatar — user/agent identity chip over Radix Avatar. A flat `muted` circle that
 * renders an image when it loads and a text fallback (initials, or a lucide icon)
 * otherwise — Radix handles the load/error swap so there is no broken-image flash.
 * Three sizes for member rows (`sm`), default lists (`base`), and profile headers (`lg`).
 */
const avatarVariants = cva(
  'relative flex shrink-0 overflow-hidden rounded-full bg-muted',
  {
    variants: {
      size: {
        sm: 'size-6 text-[0.625rem]',
        base: 'size-7 text-xs',
        lg: 'size-9 text-sm',
      },
    },
    defaultVariants: { size: 'base' },
  },
)

export function Avatar({
  className,
  size,
  ...props
}: ComponentProps<typeof Root> & VariantProps<typeof avatarVariants>) {
  return <Root className={cn(avatarVariants({ size }), className)} {...props} />
}

export function AvatarImage({
  className,
  ...props
}: ComponentProps<typeof Image>) {
  return (
    <Image
      className={cn('aspect-square h-full w-full object-cover', className)}
      {...props}
    />
  )
}

export function AvatarFallback({
  className,
  ...props
}: ComponentProps<typeof Fallback>) {
  return (
    <Fallback
      className={cn(
        'flex h-full w-full items-center justify-center font-medium text-muted-foreground bg-muted',
        className,
      )}
      {...props}
    />
  )
}

export { avatarVariants }
