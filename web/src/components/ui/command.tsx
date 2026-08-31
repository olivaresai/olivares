// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import * as Dialog from '@radix-ui/react-dialog'
import { VisuallyHidden } from '@radix-ui/react-visually-hidden'
import { Command as CommandPrimitive } from 'cmdk'
import { Search } from 'lucide-react'
import type { ComponentProps, ReactNode } from 'react'
import { cn } from '@/lib/utils'

/**
 * Command — the ⌘K palette, built over `cmdk` (Radix-grade a11y: roving focus,
 * type-ahead filter, aria-activedescendant) with our brandv4 skin. Flat,
 * hairline-divided, dense. The active row gets the copper soft fill plus a thin
 * left copper rule (a calm "you are here" marker, not a glow). `CommandDialog`
 * drops the same list into an xl-radius overlay panel for the global launcher;
 * result ids inside items should be set in `font-mono` by the consumer.
 */
export function Command({
  className,
  ...props
}: ComponentProps<typeof CommandPrimitive>) {
  return (
    <CommandPrimitive
      className={cn(
        'flex flex-col overflow-hidden bg-elevated text-foreground',
        className,
      )}
      {...props}
    />
  )
}

export function CommandInput({
  className,
  ...props
}: ComponentProps<typeof CommandPrimitive.Input>) {
  return (
    <div
      className="flex items-center gap-2 border-b border-border px-3"
      cmdk-input-wrapper=""
    >
      <Search
        className="size-4 shrink-0 text-muted-foreground"
        aria-hidden="true"
      />
      <CommandPrimitive.Input
        className={cn(
          'h-11 w-full bg-transparent text-sm text-foreground outline-none',
          'placeholder:text-muted-foreground disabled:cursor-not-allowed disabled:opacity-50',
          className,
        )}
        {...props}
      />
    </div>
  )
}

export function CommandList({
  className,
  ...props
}: ComponentProps<typeof CommandPrimitive.List>) {
  return (
    <CommandPrimitive.List
      className={cn(
        'max-h-80 overflow-y-auto overflow-x-hidden p-1',
        className,
      )}
      {...props}
    />
  )
}

export function CommandEmpty(
  props: ComponentProps<typeof CommandPrimitive.Empty>,
) {
  return (
    <CommandPrimitive.Empty
      className="py-6 text-center text-sm text-muted-foreground"
      {...props}
    />
  )
}

export function CommandGroup({
  className,
  ...props
}: ComponentProps<typeof CommandPrimitive.Group>) {
  return (
    <CommandPrimitive.Group
      className={cn(
        'overflow-hidden text-foreground',
        '[&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1.5',
        '[&_[cmdk-group-heading]]:text-xs [&_[cmdk-group-heading]]:font-medium',
        '[&_[cmdk-group-heading]]:text-muted-foreground',
        className,
      )}
      {...props}
    />
  )
}

export function CommandSeparator({
  className,
  ...props
}: ComponentProps<typeof CommandPrimitive.Separator>) {
  return (
    <CommandPrimitive.Separator
      className={cn('-mx-1 my-1 h-px bg-border', className)}
      {...props}
    />
  )
}

export function CommandItem({
  className,
  ...props
}: ComponentProps<typeof CommandPrimitive.Item>) {
  return (
    <CommandPrimitive.Item
      className={cn(
        'relative flex h-9 cursor-default select-none items-center gap-2 rounded-md px-2 text-sm outline-none',
        'border-l-2 border-transparent transition-colors duration-100 ease-out',
        'data-[selected=true]:border-accent-text data-[selected=true]:bg-accent-soft',
        'data-[selected=true]:text-accent-soft-foreground',
        'data-[disabled=true]:pointer-events-none data-[disabled=true]:opacity-50',
        '[&_svg]:size-4 [&_svg]:shrink-0 [&_svg]:text-muted-foreground',
        'data-[selected=true]:[&_svg]:text-accent-soft-foreground',
        className,
      )}
      {...props}
    />
  )
}

export function CommandShortcut({
  className,
  ...props
}: ComponentProps<'span'>) {
  return (
    <span
      className={cn(
        'ml-auto flex h-5 items-center rounded-sm border border-border bg-muted px-1.5',
        'font-mono text-[11px] leading-none tracking-wider text-muted-foreground',
        className,
      )}
      {...props}
    />
  )
}

export interface CommandDialogProps extends ComponentProps<typeof Dialog.Root> {
  /** Accessible dialog title (visually hidden by default to keep the bare palette look). */
  title?: string
  /** Accessible description for screen readers. */
  description?: string
  /** Extra classes for the dialog panel. */
  className?: string
  children: ReactNode
}

export function CommandDialog({
  title = 'Command palette',
  description = 'Search for a command to run.',
  className,
  children,
  ...props
}: CommandDialogProps) {
  return (
    <Dialog.Root {...props}>
      <Dialog.Portal>
        <Dialog.Overlay
          className={cn(
            'fixed inset-0 z-50 bg-overlay backdrop-blur-[2px]',
            'transition-opacity duration-150 ease-out',
            'data-[state=closed]:opacity-0 data-[state=open]:opacity-100',
          )}
        />
        <Dialog.Content
          className={cn(
            'fixed left-1/2 top-[15%] z-50 w-full max-w-xl -translate-x-1/2',
            'overflow-hidden rounded-xl border border-border-strong bg-elevated p-0 shadow-xl',
            'transition-all duration-150 ease-out',
            'data-[state=closed]:opacity-0 data-[state=closed]:scale-[0.98]',
            'data-[state=open]:opacity-100 data-[state=open]:scale-100',
            className,
          )}
        >
          <VisuallyHidden>
            <Dialog.Title>{title}</Dialog.Title>
            <Dialog.Description>{description}</Dialog.Description>
          </VisuallyHidden>
          {/* `label` becomes cmdk's hidden root label that names the role=combobox
              search input (its aria-labelledby points at it) — without it the input
              has no accessible name. */}
          <Command
            label={title}
            className="[&_[cmdk-group-heading]]:text-muted-foreground"
          >
            {children}
          </Command>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}
