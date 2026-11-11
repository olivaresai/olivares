// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Slot } from '@radix-ui/react-slot'
import { cva, type VariantProps } from 'class-variance-authority'
import type { ButtonHTMLAttributes, Ref } from 'react'
import { cn } from '@/lib/utils'

/**
 * Button — the design-system action primitive, drawn as direction A2 draws it: bold labels and
 * one state layer. The orange `primary` is the ONE suggested verb a view exists for, in the
 * graphite ink (white on the orange is 2.7:1); every other labelled action is `secondary`, which
 * rests on the selection token (10 % of its ink in the drawing, shipped as the token under the
 * contrast ruling's 0.75 factor) and deepens to 15 % of its ink under the pointer and 24 % pressed
 * (`outline` is the same button, kept so no caller breaks); toolbars and pane headers use the flat
 * `ghost`, which `aria-pressed` turns into a selected toggle (the selection fill and a bold label);
 * icon buttons (`icon`, `icon-sm`) keep a 42 px hit area around their 34/30 px face, with that
 * space reserved in the layout, and are 44 px on a phone. The destructive answer is OUTLINED in
 * its colour and never filled at rest (a fill lowers its label under 4.5:1 on a warm row): 8 %
 * under the pointer, a 2 px edge pressed; the solid fill is for an irreversible confirm step only.
 * Heights: small 30, regular the density token, large 44, and 44 px high on a phone. Nothing
 * scales or moves when pressed; motion is the duration token, none under reduced motion. A link
 * is the accent text with its underline at 45 %, full under the pointer: the accent ink, never
 * the orange fill. Focus is a 2 px outline in the focus color with a 2 px offset (an outline, so
 * forced-colors mode keeps it). A control that cannot act — the native `disabled` attribute
 * or `aria-disabled="true"` — is faded to 45 %, and its reason is visible text beside it (wrap
 * it in `DisabledReason`). An `aria-disabled` control keeps its pointer events, so
 * `DisabledReason` refuses the click instead of letting it fall through to a row behind it, and
 * shows the reason on hover; each variant paints its rest look back under the pointer and when
 * pressed (`aria-disabled:hover:`, `aria-disabled:active:`), so it never looks pressable.
 */
const SECONDARY =
  'bg-active text-text hover:bg-[color-mix(in_srgb,var(--text)_15%,transparent)] active:bg-[color-mix(in_srgb,var(--text)_24%,transparent)] aria-disabled:hover:bg-active aria-disabled:active:bg-active'
const GHOST =
  'bg-transparent font-medium text-text-2 hover:bg-hover hover:text-text active:bg-[var(--pressed)] aria-pressed:bg-active aria-pressed:font-bold aria-pressed:text-text aria-disabled:hover:bg-transparent aria-disabled:hover:text-text-2 aria-disabled:active:bg-transparent'
const DANGER =
  'bg-transparent text-bad shadow-[inset_0_0_0_1px_color-mix(in_srgb,var(--bad)_45%,transparent)] hover:bg-[color-mix(in_srgb,var(--bad)_8%,transparent)] hover:shadow-[inset_0_0_0_1px_color-mix(in_srgb,var(--bad)_70%,transparent)] active:bg-[color-mix(in_srgb,var(--bad)_8%,transparent)] active:shadow-[inset_0_0_0_2px_var(--bad)] aria-disabled:hover:bg-transparent aria-disabled:hover:shadow-[inset_0_0_0_1px_color-mix(in_srgb,var(--bad)_45%,transparent)] aria-disabled:active:bg-transparent aria-disabled:active:shadow-[inset_0_0_0_1px_color-mix(in_srgb,var(--bad)_45%,transparent)]'

const buttonVariants = cva(
  cn(
    'inline-flex shrink-0 items-center justify-center gap-2 whitespace-nowrap rounded-ctl border border-transparent',
    'text-body font-bold transition-[color,background-color,border-color,box-shadow,filter] ease-out select-none outline-none',
    'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus',
    'disabled:pointer-events-none disabled:opacity-45',
    'aria-disabled:cursor-not-allowed aria-disabled:opacity-45',
    '[&_svg]:size-4 [&_svg]:shrink-0',
    // A key hint inside a button is a soft key in the button's own ink.
    '[&_kbd]:bg-current/14 [&_kbd]:text-inherit [&_kbd]:shadow-none',
  ),
  {
    variants: {
      variant: {
        primary:
          'bg-accent text-on-accent hover:bg-accent-hover active:bg-accent-active aria-disabled:hover:bg-accent aria-disabled:active:bg-accent',
        secondary: SECONDARY,
        outline: SECONDARY,
        icon: GHOST,
        ghost: GHOST,
        danger: DANGER,
        destructive: DANGER,
        'destructive-solid':
          'bg-danger-solid text-danger-solid-foreground hover:brightness-110 active:brightness-95 aria-disabled:hover:brightness-100 aria-disabled:active:brightness-100',
        link: 'h-auto rounded-none border-0 p-0 font-medium text-accent-text underline underline-offset-[3px] decoration-1 decoration-accent-text/45 hover:decoration-accent-text aria-disabled:hover:decoration-accent-text/45',
      },
      size: {
        sm: 'h-[30px] gap-1.5 px-[11px] text-caption max-md:min-h-11 [&_svg]:size-3.5',
        md: 'h-[var(--density-control)] px-3.5 max-md:min-h-11',
        base: 'h-[var(--density-control)] px-3.5 max-md:min-h-11',
        lg: 'h-11 px-5 text-body-l max-md:min-h-11',
        // Absolute offsets start inside the 1 px border, so the hit area is 42 px:
        // 4 px (icon) and 6 px (icon-sm) outside the face. That space is reserved
        // in the layout so a later action cannot cover this one's face; 44 would
        // need a wider margin and move every icon button by a pixel.
        icon: "relative m-1 size-[var(--density-control)] after:absolute after:-inset-[5px] after:content-[''] max-md:size-11",
        'icon-sm':
          "relative m-1.5 size-[30px] after:absolute after:-inset-[7px] after:content-[''] max-md:size-11 [&_svg]:size-3.5",
      },
    },
    // A link inside a sentence keeps the line's height on a phone.
    compoundVariants: [{ variant: 'link', className: 'max-md:min-h-0' }],
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
      className={cn(
        buttonVariants({
          variant,
          size: size ?? (variant === 'icon' ? 'icon' : undefined),
        }),
        className,
      )}
      {...props}
    />
  )
}

export { buttonVariants }
