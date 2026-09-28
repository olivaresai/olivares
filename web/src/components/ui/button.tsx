// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Slot } from '@radix-ui/react-slot'
import { cva, type VariantProps } from 'class-variance-authority'
import type { ButtonHTMLAttributes, Ref } from 'react'
import { cn } from '@/lib/utils'

/**
 * Button — the design-system action primitive. The orange `primary` is reserved for
 * the ONE intentful action on a surface (restraint = trust); most buttons are
 * `secondary`/`ghost`. Destructive actions are a ghost danger by default and only
 * become a solid `destructive` fill on an irreversible confirm step. Flat, no
 * gradients, hairline-bordered, fast color-only transitions.
 *
 * The v26.10 look (same props): the primary fill carries its darker orange border and the
 * on-accent text at weight 600; `ghost` is the quiet second tone; focus is a 2 px outline
 * in the focus color with a 2 px offset (an outline, so forced-colors mode keeps it).
 * A control that cannot act — the native `disabled` attribute or `aria-disabled="true"` —
 * is never faded: it keeps full legibility with a dashed control boundary and the third
 * text tone, and its reason is visible text beside it (wrap it in `DisabledReason`).
 */
const buttonVariants = cva(
  cn(
    'inline-flex shrink-0 items-center justify-center gap-2 whitespace-nowrap rounded-ctl border border-transparent',
    'text-body font-medium transition-[color,background-color,border-color,filter] duration-100 ease-out select-none outline-none',
    'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus',
    'disabled:pointer-events-none disabled:border-dashed disabled:border-ctl-border disabled:bg-transparent disabled:text-text-3',
    'aria-disabled:cursor-not-allowed aria-disabled:border-dashed aria-disabled:border-ctl-border aria-disabled:bg-transparent aria-disabled:text-text-3',
    'aria-disabled:hover:bg-transparent aria-disabled:hover:text-text-3 aria-disabled:hover:brightness-100',
    '[&_svg]:size-4 [&_svg]:shrink-0',
  ),
  {
    variants: {
      variant: {
        primary:
          'border-accent-border bg-accent font-semibold text-on-accent hover:brightness-106 active:bg-accent-active disabled:bg-surface aria-disabled:bg-surface aria-disabled:hover:bg-surface',
        secondary: 'border-line-strong bg-transparent text-text hover:bg-hover',
        outline: 'border-line-strong bg-transparent text-text hover:bg-hover',
        ghost: 'bg-transparent text-text-2 hover:bg-hover hover:text-text',
        destructive: 'border-transparent bg-bad-soft text-bad hover:border-bad',
        'destructive-solid':
          'bg-danger-solid text-danger-solid-foreground hover:brightness-110 active:brightness-95',
        link: 'h-auto rounded-none border-0 p-0 text-accent-text underline underline-offset-[3px] decoration-1 hover:decoration-2',
      },
      size: {
        sm: 'h-7 gap-1.5 px-2.5 text-caption [&_svg]:size-3.5',
        base: 'h-8 px-3',
        lg: 'h-10 rounded-[9px] px-4 text-body-l',
        icon: 'size-8',
        'icon-sm': 'size-7 [&_svg]:size-3.5',
      },
    },
    defaultVariants: { variant: 'secondary', size: 'base' },
  },
)

export interface ButtonProps
  extends
    ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {
  /** Render as the child element (Radix Slot) — e.g. an anchor or router Link. */
  asChild?: boolean
  /** React 19 passes `ref` as an ordinary prop; the spread below forwards it to the element. */
  ref?: Ref<HTMLButtonElement>
}

export function Button({
  className,
  variant,
  size,
  asChild = false,
  ...props
}: ButtonProps) {
  const Comp = asChild ? Slot : 'button'
  return (
    <Comp
      className={cn(buttonVariants({ variant, size }), className)}
      {...props}
    />
  )
}

export { buttonVariants }
