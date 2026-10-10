// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import * as SelectPrimitive from '@radix-ui/react-select'
import { useFieldControl } from '@/components/ui/field'
import { Check, ChevronDown, ChevronUp } from 'lucide-react'
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * Select — Radix select dressed as a control-plane input. The trigger reads exactly
 * like an Input (h-8, on the canvas, the control boundary, the focus outline) with a
 * chevron affordance; the content is a raised, hairline-bordered popover with the popover
 * shadow. Items highlight on the hover fill and show an orange Check + accent-text label
 * when chosen. Rendered in
 * `popper` position so it never overflows its container. Re-exports the Radix
 * Root/Group/Value so callers compose a full, accessible listbox.
 */
export const Select = SelectPrimitive.Root
export const SelectGroup = SelectPrimitive.Group
export const SelectValue = SelectPrimitive.Value

export function SelectTrigger({
  className,
  children,
  ...props
}: ComponentProps<typeof SelectPrimitive.Trigger>) {
  // El trigger es el ÚNICO nodo del DOM de un `<Select>`, así que es el que tiene que llevar el
  // nombre accesible. Dentro de un `Field`, la etiqueta se enlaza aquí: `cloneElement` no puede
  // hacerlo porque el hijo directo del `Field` es la raíz de Radix, que no pinta nada. Fuera de un
  // `Field` esto es `null` y no cambia nada.
  //
  // ⛔ LO QUE EL LLAMANTE PONGA GANA SIEMPRE. Un `aria-label` explícito —o un `aria-labelledby`
  // apuntando a otra cosa— es una decisión, no un olvido, y este enlace no la pisa. Y `aria-label`
  // cuenta como nombre aunque venga de otra vía: sin esa comprobación, un trigger correctamente
  // etiquetado a mano acabaría con DOS nombres compitiendo.
  const campo = useFieldControl()
  const nombrado =
    props['aria-label'] != null || props['aria-labelledby'] != null
  const heredado = campo
    ? {
        'aria-labelledby': nombrado ? undefined : campo['aria-labelledby'],
        'aria-describedby':
          props['aria-describedby'] ?? campo['aria-describedby'],
        'aria-invalid': props['aria-invalid'] ?? campo['aria-invalid'],
        'aria-required': props['aria-required'] ?? campo['aria-required'],
      }
    : {}
  return (
    <SelectPrimitive.Trigger
      data-slot="select-trigger"
      {...heredado}
      className={cn(
        'flex h-8 w-full items-center justify-between gap-2 rounded-ctl border border-ctl-border bg-canvas px-3',
        'text-body text-text transition-colors duration-100 ease-out outline-none',
        'data-[placeholder]:text-text-3',
        'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus',
        'aria-[invalid=true]:border-bad aria-[invalid=true]:focus-visible:outline-bad',
        'disabled:pointer-events-none disabled:border-dashed disabled:bg-surface disabled:text-text-3',
        'aria-disabled:cursor-not-allowed aria-disabled:border-dashed aria-disabled:bg-surface aria-disabled:text-text-3',
        '[&>span]:line-clamp-1 [&_svg]:size-4 [&_svg]:shrink-0',
        className,
      )}
      {...props}
    >
      {children}
      <SelectPrimitive.Icon asChild>
        <ChevronDown className="size-4 text-text-3" />
      </SelectPrimitive.Icon>
    </SelectPrimitive.Trigger>
  )
}

export function SelectContent({
  className,
  children,
  position = 'popper',
  sideOffset = 4,
  ...props
}: ComponentProps<typeof SelectPrimitive.Content>) {
  return (
    <SelectPrimitive.Portal>
      <SelectPrimitive.Content
        data-slot="select-content"
        position={position}
        sideOffset={sideOffset}
        className={cn(
          'relative z-50 max-h-[var(--radix-select-content-available-height)] min-w-[8rem] overflow-hidden',
          'rounded-card border border-line-strong bg-raised text-text shadow-[var(--shadow-pop)]',
          'transition-opacity duration-150 ease-out',
          'data-[state=closed]:opacity-0 data-[state=open]:opacity-100',
          position === 'popper' &&
            'w-full min-w-[var(--radix-select-trigger-width)] origin-[var(--radix-select-content-transform-origin)]',
          className,
        )}
        {...props}
      >
        <SelectScrollUpButton />
        <SelectPrimitive.Viewport
          className={cn(
            'p-1',
            position === 'popper' &&
              'h-[var(--radix-select-trigger-height)] w-full',
          )}
        >
          {children}
        </SelectPrimitive.Viewport>
        <SelectScrollDownButton />
      </SelectPrimitive.Content>
    </SelectPrimitive.Portal>
  )
}

export function SelectLabel({
  className,
  ...props
}: ComponentProps<typeof SelectPrimitive.Label>) {
  return (
    <SelectPrimitive.Label
      data-slot="select-label"
      className={cn('px-2 py-1.5 text-overline text-text-3', className)}
      {...props}
    />
  )
}

export function SelectItem({
  className,
  children,
  ...props
}: ComponentProps<typeof SelectPrimitive.Item>) {
  return (
    <SelectPrimitive.Item
      data-slot="select-item"
      className={cn(
        'relative flex h-8 w-full cursor-default items-center rounded-[6px] py-1 pr-8 pl-2 text-body outline-none select-none',
        'data-[highlighted]:bg-hover data-[highlighted]:text-text',
        'data-[state=checked]:font-medium data-[state=checked]:text-accent-text',
        'data-[disabled]:pointer-events-none data-[disabled]:text-text-3',
        '[&_svg]:size-4 [&_svg]:shrink-0',
        className,
      )}
      {...props}
    >
      <span className="absolute right-2 flex size-4 items-center justify-center">
        <SelectPrimitive.ItemIndicator>
          <Check className="size-4" />
        </SelectPrimitive.ItemIndicator>
      </span>
      <SelectPrimitive.ItemText>{children}</SelectPrimitive.ItemText>
    </SelectPrimitive.Item>
  )
}

export function SelectSeparator({
  className,
  ...props
}: ComponentProps<typeof SelectPrimitive.Separator>) {
  return (
    <SelectPrimitive.Separator
      data-slot="select-separator"
      className={cn('-mx-1 my-1 h-px bg-line', className)}
      {...props}
    />
  )
}

export function SelectScrollUpButton({
  className,
  ...props
}: ComponentProps<typeof SelectPrimitive.ScrollUpButton>) {
  return (
    <SelectPrimitive.ScrollUpButton
      data-slot="select-scroll-up"
      className={cn(
        'flex cursor-default items-center justify-center py-1 text-text-3',
        className,
      )}
      {...props}
    >
      <ChevronUp className="size-4" />
    </SelectPrimitive.ScrollUpButton>
  )
}

export function SelectScrollDownButton({
  className,
  ...props
}: ComponentProps<typeof SelectPrimitive.ScrollDownButton>) {
  return (
    <SelectPrimitive.ScrollDownButton
      data-slot="select-scroll-down"
      className={cn(
        'flex cursor-default items-center justify-center py-1 text-text-3',
        className,
      )}
      {...props}
    >
      <ChevronDown className="size-4" />
    </SelectPrimitive.ScrollDownButton>
  )
}
