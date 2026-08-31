// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import * as DropdownMenuPrimitive from '@radix-ui/react-dropdown-menu'
import { Check, ChevronRight, Circle } from 'lucide-react'
import type { ComponentProps, HTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

/**
 * DropdownMenu — the contextual action menu over Radix DropdownMenu. Dense
 * (h-8 items, text-sm), flat `bg-elevated` panel with a strong hairline and
 * `shadow-md`. Highlighted items wash to `bg-muted`; the `destructive` item variant
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
          'z-50 min-w-44 overflow-hidden bg-elevated border border-border-strong rounded-md shadow-md p-1',
          'transition-opacity duration-150 ease-out',
          'data-[state=open]:opacity-100 data-[state=closed]:opacity-0',
          className,
        )}
        {...props}
      />
    </DropdownMenuPrimitive.Portal>
  )
}

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
        'flex h-8 cursor-default select-none items-center gap-2 rounded-sm px-2 text-sm outline-none',
        'transition-colors duration-100 ease-out',
        'data-[highlighted]:bg-muted',
        'data-[disabled]:pointer-events-none data-[disabled]:opacity-50',
        '[&_svg]:size-4 [&_svg]:shrink-0 [&_svg]:text-muted-foreground',
        inset && 'pl-8',
        variant === 'destructive' &&
          'text-danger data-[highlighted]:bg-danger-soft data-[highlighted]:text-danger [&_svg]:text-danger',
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
        'relative flex h-8 cursor-default select-none items-center gap-2 rounded-sm py-0 pl-8 pr-2 text-sm outline-none',
        'transition-colors duration-100 ease-out',
        'data-[highlighted]:bg-muted',
        'data-[disabled]:pointer-events-none data-[disabled]:opacity-50',
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
        'relative flex h-8 cursor-default select-none items-center gap-2 rounded-sm py-0 pl-8 pr-2 text-sm outline-none',
        'transition-colors duration-100 ease-out',
        'data-[highlighted]:bg-muted',
        'data-[disabled]:pointer-events-none data-[disabled]:opacity-50',
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
        'px-2 py-1.5 text-xs font-medium text-muted-foreground',
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
      className={cn('-mx-1 my-1 h-px bg-border', className)}
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
        'ml-auto text-xs tracking-widest text-muted-foreground tabular-nums',
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
        'flex h-8 cursor-default select-none items-center gap-2 rounded-sm px-2 text-sm outline-none',
        'transition-colors duration-100 ease-out',
        'data-[highlighted]:bg-muted data-[state=open]:bg-muted',
        '[&_svg]:size-4 [&_svg]:shrink-0 [&_svg]:text-muted-foreground',
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
        'z-50 min-w-44 overflow-hidden bg-elevated border border-border-strong rounded-md shadow-md p-1',
        'transition-opacity duration-150 ease-out',
        'data-[state=open]:opacity-100 data-[state=closed]:opacity-0',
        className,
      )}
      {...props}
    />
  )
}
