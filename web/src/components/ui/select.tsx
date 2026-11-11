// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import * as SelectPrimitive from '@radix-ui/react-select'
import { useFieldControl } from '@/components/ui/field'
import { fieldWell } from '@/components/ui/input'
import { Check, ChevronDown, ChevronUp } from 'lucide-react'
import type { ComponentProps, ReactNode } from 'react'
import { cn } from '@/lib/utils'

/**
 * Select — Radix select dressed as a control-plane field. The trigger is the same field well
 * as an Input (the density token's height, 44 px on a phone) with a chevron in the second tone;
 * inside a boxed row it is a combo row instead, its value and the chevron with no box, as a
 * settings row draws it. The content is a raised, hairline-bordered popover with the popover
 * shadow. Items highlight on the hover fill, show an orange Check + accent-text label when
 * chosen, and are 44 px high on a phone. Rendered in
 * `popper` position so it never overflows its container. Re-exports the Radix
 * Root/Group/Value so callers compose a full, accessible listbox.
 */
export const Select = SelectPrimitive.Root
export const SelectGroup = SelectPrimitive.Group
export const SelectValue = SelectPrimitive.Value

/** A select inside a boxed row is a combo row: the value and a chevron, no box. */
const IN_ROW =
  '[[data-slot=boxed-list-row]_&]:justify-end [[data-slot=boxed-list-row]_&]:border-transparent [[data-slot=boxed-list-row]_&]:bg-transparent [[data-slot=boxed-list-row]_&]:shadow-none [[data-slot=boxed-list-row]_&]:font-medium [[data-slot=boxed-list-row]_&]:text-text-2 [[data-slot=boxed-list-row]_&]:hover:border-transparent [[data-slot=boxed-list-row]_&]:hover:bg-hover [[data-slot=boxed-list-row]_&]:hover:text-text'

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
        fieldWell,
        'flex h-[var(--density-control)] items-center justify-between gap-2 ps-[11px] pe-2 max-md:min-h-11',
        'data-[placeholder]:text-text-3',
        IN_ROW,
        '[&>span]:line-clamp-1 [&_svg]:size-4 [&_svg]:shrink-0',
        className,
      )}
      {...props}
    >
      {children}
      <SelectPrimitive.Icon asChild>
        <ChevronDown className="size-4 text-text-2" />
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
  hint,
  ...props
}: ComponentProps<typeof SelectPrimitive.Item> & {
  /** A second line under the label, in the list only: the trigger shows the label. */
  hint?: ReactNode
}) {
  return (
    <SelectPrimitive.Item
      data-slot="select-item"
      className={cn(
        'relative flex w-full cursor-default rounded-[6px] py-1 pr-8 pl-2 text-body outline-none select-none max-md:min-h-11',
        hint ? 'flex-col items-start' : 'h-8 items-center',
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
      {hint && <span className="text-caption text-text-3">{hint}</span>}
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
