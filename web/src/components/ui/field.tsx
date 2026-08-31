// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  cloneElement,
  createContext,
  isValidElement,
  useContext,
  useId,
  type ReactNode,
} from 'react'
import { cn } from '@/lib/utils'
import { Label } from '@/components/ui/label'

/**
 * Field — the reusable form-row primitive compose with react-hook-form. It
 * stacks: Label (with a danger `*` when required) → control (children) → optional
 * muted description → optional danger error (role="alert"). It owns id wiring so the
 * control is correctly labelled and described:
 *  - if `htmlFor` is given it is used verbatim (the caller controls the control id);
 *  - otherwise a stable id is generated and exposed via `renderProps` so the control
 *    can adopt it together with `aria-describedby` / `aria-invalid`.
 * The error replaces the description for the screen-reader announcement but both can
 * render; an empty `error` keeps layout calm (no reserved space).
 */
export interface FieldRenderProps {
  /** id to put on the control (`<Input id={...}>`). */
  id: string
  /** Label element id, for `aria-labelledby`. Present only when the Field has a
   * `label`. Unlike `<label htmlFor>`, this names button-based controls (combobox /
   * select triggers), where the AccName algorithm ignores the for-association. */
  'aria-labelledby'?: string
  /** Space-joined ids for `aria-describedby` (description + error, present ones only). */
  'aria-describedby'?: string
  /** `true` when `error` is set, for `aria-invalid`. */
  'aria-invalid'?: boolean
}

export interface FieldProps {
  /** Caption text; rendered through the design-system Label. */
  label?: ReactNode
  /** Explicit control id. When omitted, a stable id is generated. */
  htmlFor?: string
  /** Helper text under the control (xs, muted). */
  description?: ReactNode
  /** Error message — when truthy it renders danger text with role="alert". */
  error?: ReactNode
  /** Appends a danger `*` to the label and sets aria on the control. */
  required?: boolean
  className?: string
  /**
   * Either plain control nodes, or a render function receiving the resolved
   * id / aria attributes so the control wires up labelling + invalid state.
   */
  children: ReactNode | ((props: FieldRenderProps) => ReactNode)
}

export function FieldDescription({
  className,
  ...props
}: React.ComponentProps<'p'>) {
  return (
    <p
      data-slot="field-description"
      className={cn('text-xs text-muted-foreground', className)}
      {...props}
    />
  )
}

export function FieldError({
  className,
  children,
  ...props
}: React.ComponentProps<'p'>) {
  if (!children) return null
  return (
    <p
      data-slot="field-error"
      role="alert"
      className={cn('text-xs text-danger', className)}
      {...props}
    >
      {children}
    </p>
  )
}

/**
 * ⛔ EL ENLACE QUE `cloneElement` NO PUEDE HACER, y por qué existe este contexto.
 *
 * El auto-asociado de abajo clona **el hijo único** con `aria-labelledby`. Eso funciona cuando el
 * hijo es un elemento del DOM (`<Input>`, `<textarea>`), y **no funciona en absoluto cuando es
 * `<Select>`**: la raíz de Radix no pinta ningún nodo, así que el atributo se evapora y el
 * `role="combobox"` que sí llega al DOM —el trigger— se queda **sin nombre accesible**. Un
 * `<label htmlFor>` tampoco lo salva: el algoritmo AccName ignora la for-asociación en controles
 * de tipo botón, cosa que la propia cabecera de `FieldRenderProps` ya decía.
 *
 * **Medido, no supuesto:** axe da `button-name` con `messageKey: "noAttr"` sobre los dos combobox
 * de /alerting, y el barrido de la forma `<Field label>` con un `<Select>` de hijo directo da
 * **77 sitios** en la consola. Arreglarlos uno a uno sería 77 parches y el 78º nacería roto.
 *
 * ⚠ **Lo que este contexto NO reparte es el `id`**, y la omisión es deliberada: dos triggers
 * dentro de un mismo `Field` adoptarían el MISMO id y un id duplicado es a su vez un defecto de
 * accesibilidad. Se reparte sólo lo que nombra y describe, que es lo que falta.
 */
const FieldControlContext = createContext<FieldRenderProps | null>(null)

/** Lo que un control de tipo botón necesita para que la etiqueta del `Field` lo nombre.
 *  Devuelve `null` fuera de un `Field`, que es un uso legítimo. */
export function useFieldControl(): FieldRenderProps | null {
  return useContext(FieldControlContext)
}

export function Field({
  label,
  htmlFor,
  description,
  error,
  required = false,
  className,
  children,
}: FieldProps) {
  const generatedId = useId()
  const controlId = htmlFor ?? generatedId
  const labelId = label != null ? `${controlId}-label` : undefined
  const descriptionId = description ? `${controlId}-description` : undefined
  const errorId = error ? `${controlId}-error` : undefined
  const describedBy =
    [descriptionId, errorId].filter(Boolean).join(' ') || undefined

  const renderProps: FieldRenderProps = {
    id: controlId,
    'aria-labelledby': labelId,
    'aria-describedby': describedBy,
    'aria-invalid': error ? true : undefined,
  }

  // For plain (non-render-prop) children, auto-associate id + labelling + the
  // description/error + the invalid state with the single control element, so the
  // label, helper text and error are announced even when the caller didn't thread
  // renderProps. `aria-labelledby` (not just `<label htmlFor>`) is what names
  // a button-based control — combobox/select triggers — where the for-association is
  // ignored by AccName. Existing props on the child always win.
  let content: ReactNode
  if (typeof children === 'function') {
    content = children(renderProps)
  } else if (isValidElement(children)) {
    const childProps = children.props as {
      id?: string
      'aria-labelledby'?: string
      'aria-describedby'?: string
      'aria-invalid'?: boolean
    }
    content = cloneElement(children, {
      id: childProps.id ?? controlId,
      'aria-labelledby': childProps['aria-labelledby'] ?? labelId,
      'aria-describedby': childProps['aria-describedby'] ?? describedBy,
      'aria-invalid': childProps['aria-invalid'] ?? (error ? true : undefined),
    } as Partial<typeof childProps>)
  } else {
    content = children
  }

  return (
    <div data-slot="field" className={cn('flex flex-col gap-1.5', className)}>
      {label != null && (
        <Label id={labelId} htmlFor={controlId}>
          {label}
          {required && (
            <span className="ml-0.5 text-danger" aria-hidden="true">
              *
            </span>
          )}
        </Label>
      )}
      <FieldControlContext.Provider value={renderProps}>
        {content}
      </FieldControlContext.Provider>
      {description != null && (
        <FieldDescription id={descriptionId}>{description}</FieldDescription>
      )}
      <FieldError id={errorId}>{error}</FieldError>
    </div>
  )
}
