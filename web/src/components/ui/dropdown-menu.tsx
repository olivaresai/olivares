// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import * as DropdownMenuPrimitive from '@radix-ui/react-dropdown-menu'
import { Check, ChevronRight, Circle } from 'lucide-react'
import type { ComponentProps, HTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

/**
 * DropdownMenu — the contextual action menu over Radix DropdownMenu, as direction A2 draws
 * a popover menu: the highest surface (`bg-raised`) with the pop shadow and a hairline inside
 * it, no outline; 34 px items (44 px on a phone) whose quiet icons light under the pointer,
 * the hover layer and a press layer; headings at 12/700. The `destructive` item variant
 * shifts to danger text + a danger-soft highlight. Radix handles roving focus,
 * typeahead and full keyboard nav — never reimplement those.
 */

export const DropdownMenu = DropdownMenuPrimitive.Root
export const DropdownMenuTrigger = DropdownMenuPrimitive.Trigger
export const DropdownMenuGroup = DropdownMenuPrimitive.Group
export const DropdownMenuPortal = DropdownMenuPrimitive.Portal
export const DropdownMenuSub = DropdownMenuPrimitive.Sub
export const DropdownMenuRadioGroup = DropdownMenuPrimitive.RadioGroup

export function DropdownMenuContent({
  className,
  sideOffset = 4,
  ...props
}: ComponentProps<typeof DropdownMenuPrimitive.Content>) {
  return (
    <DropdownMenuPrimitive.Portal>
      <DropdownMenuPrimitive.Content
        sideOffset={sideOffset}
        className={cn(
          'z-50 min-w-44 overflow-hidden rounded-card border-0 bg-raised p-1.5 shadow-[var(--shadow-pop),inset_0_0_0_1px_var(--line)]',
          'transition-opacity duration-[var(--duration-panel)] ease-out',
          'data-[state=open]:opacity-100 data-[state=closed]:opacity-0',
          className,
        )}
        {...props}
      />
    </DropdownMenuPrimitive.Portal>
  )
}

/** Keyboard focus: a 2 px ring in the focus colour, inside the row. */
const ITEM_FOCUS =
  'focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-focus'

export interface DropdownMenuItemProps extends ComponentProps<
  typeof DropdownMenuPrimitive.Item
> {
  /** Inset to align with checkbox/radio rows that reserve a leading indicator. */
  inset?: boolean
  /** Danger styling for irreversible/destructive actions. */
  variant?: 'default' | 'destructive'
}

export function DropdownMenuItem({
  className,
  inset = false,
  variant = 'default',
  ...props
}: DropdownMenuItemProps) {
  return (
    <DropdownMenuPrimitive.Item
      data-variant={variant}
      className={cn(
        'flex h-[34px] cursor-default select-none items-center gap-[11px] rounded-sm px-2.5 text-body outline-none max-md:min-h-11',
        'transition-colors ease-out',
        'data-[highlighted]:bg-hover data-[highlighted]:active:bg-[var(--pressed)]',
        ITEM_FOCUS,
        'data-[disabled]:pointer-events-none data-[disabled]:opacity-45',
        '[&_svg]:size-4 [&_svg]:shrink-0 [&_svg]:text-text-2 data-[highlighted]:[&_svg]:text-text',
        inset && 'pl-8',
        variant === 'destructive' &&
          'text-danger data-[highlighted]:bg-danger-soft data-[highlighted]:text-danger [&_svg]:text-danger data-[highlighted]:[&_svg]:text-danger',
        className,
      )}
      {...props}
    />
  )
}

export function DropdownMenuCheckboxItem({
  className,
  children,
  checked,
  ...props
}: ComponentProps<typeof DropdownMenuPrimitive.CheckboxItem>) {
  return (
    <DropdownMenuPrimitive.CheckboxItem
      checked={checked}
      className={cn(
        'relative flex h-[34px] cursor-default select-none items-center gap-[11px] rounded-sm py-0 pl-8 pr-2.5 text-body outline-none max-md:min-h-11',
        'transition-colors ease-out',
        'data-[highlighted]:bg-hover data-[highlighted]:active:bg-[var(--pressed)]',
        ITEM_FOCUS,
        'data-[disabled]:pointer-events-none data-[disabled]:opacity-45',
        '[&_svg]:size-4 [&_svg]:shrink-0',
        className,
      )}
      {...props}
    >
      <span className="absolute left-2 flex size-4 items-center justify-center">
        <DropdownMenuPrimitive.ItemIndicator>
          <Check className="text-accent-text" />
        </DropdownMenuPrimitive.ItemIndicator>
      </span>
      {children}
    </DropdownMenuPrimitive.CheckboxItem>
  )
}

export function DropdownMenuRadioItem({
  className,
  children,
  ...props
}: ComponentProps<typeof DropdownMenuPrimitive.RadioItem>) {
  return (
    <DropdownMenuPrimitive.RadioItem
      className={cn(
        'relative flex h-[34px] cursor-default select-none items-center gap-[11px] rounded-sm py-0 pl-8 pr-2.5 text-body outline-none max-md:min-h-11',
        'transition-colors ease-out',
        'data-[highlighted]:bg-hover data-[highlighted]:active:bg-[var(--pressed)]',
        ITEM_FOCUS,
        'data-[disabled]:pointer-events-none data-[disabled]:opacity-45',
        className,
      )}
      {...props}
    >
      <span className="absolute left-2 flex size-4 items-center justify-center">
        <DropdownMenuPrimitive.ItemIndicator>
          <Circle className="size-2 fill-accent-text text-accent-text" />
        </DropdownMenuPrimitive.ItemIndicator>
      </span>
      {children}
    </DropdownMenuPrimitive.RadioItem>
  )
}

export interface DropdownMenuLabelProps extends ComponentProps<
  typeof DropdownMenuPrimitive.Label
> {
  inset?: boolean
}

export function DropdownMenuLabel({
  className,
  inset = false,
  ...props
}: DropdownMenuLabelProps) {
  return (
    <DropdownMenuPrimitive.Label
      className={cn(
        'px-2.5 pt-2 pb-1 text-overline font-bold text-muted-foreground',
        inset && 'pl-8',
        className,
      )}
      {...props}
    />
  )
}

export function DropdownMenuSeparator({
  className,
  ...props
}: ComponentProps<typeof DropdownMenuPrimitive.Separator>) {
  return (
    <DropdownMenuPrimitive.Separator
      className={cn('mx-1 my-1.5 h-px bg-line', className)}
      {...props}
    />
  )
}

export function DropdownMenuShortcut({
  className,
  ...props
}: HTMLAttributes<HTMLSpanElement>) {
  return (
    <span
      className={cn(
        'ml-auto text-caption tracking-widest text-muted-foreground tabular-nums',
        className,
      )}
      {...props}
    />
  )
}

export function DropdownMenuSubTrigger({
  className,
  inset = false,
  children,
  ...props
}: ComponentProps<typeof DropdownMenuPrimitive.SubTrigger> & {
  inset?: boolean
}) {
  return (
    <DropdownMenuPrimitive.SubTrigger
      className={cn(
        'flex h-[34px] cursor-default select-none items-center gap-[11px] rounded-sm px-2.5 text-body outline-none max-md:min-h-11',
        'transition-colors ease-out',
        'data-[highlighted]:bg-hover data-[state=open]:bg-hover',
        ITEM_FOCUS,
        '[&_svg]:size-4 [&_svg]:shrink-0 [&_svg]:text-text-2 data-[highlighted]:[&_svg]:text-text',
        inset && 'pl-8',
        className,
      )}
      {...props}
    >
      {children}
      <ChevronRight className="ml-auto" />
    </DropdownMenuPrimitive.SubTrigger>
  )
}

export function DropdownMenuSubContent({
  className,
  sideOffset = 4,
  ...props
}: ComponentProps<typeof DropdownMenuPrimitive.SubContent>) {
  return (
    <DropdownMenuPrimitive.SubContent
      sideOffset={sideOffset}
      className={cn(
        'z-50 min-w-44 overflow-hidden rounded-card border-0 bg-raised p-1.5 shadow-[var(--shadow-pop),inset_0_0_0_1px_var(--line)]',
        'transition-opacity duration-[var(--duration-panel)] ease-out',
        'data-[state=open]:opacity-100 data-[state=closed]:opacity-0',
        className,
      )}
      {...props}
    />
  )
}
