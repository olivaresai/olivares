// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// La costura Field ↔ SelectTrigger: que la etiqueta del campo NOMBRE al combobox.
//
// ⛔ POR QUÉ ESTA CELDA EXISTE Y POR QUÉ SE CONSULTA POR NOMBRE ACCESIBLE. El defecto no era
// «falta un atributo»: era que `Field` auto-asocia clonando su hijo único, y con un `<Select>` el
// hijo único es la raíz de Radix, que **no pinta ningún nodo**. El `aria-labelledby` se evaporaba
// y el `role="combobox"` quedaba sin nombre. Una aserción sobre el atributo pasaría con el
// atributo puesto en el sitio equivocado; `getByRole('combobox', { name })` pregunta lo que de
// verdad importa —¿cómo se llama esto para quien no lo ve?— y por eso es la aserción.
//
// Medido en el navegador antes de escribirla: axe daba `button-name` `noAttr` sobre los dos
// combobox de /alerting, y la forma se repite en **77 sitios** de la consola.
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { Field } from './field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from './select'

function CampoConSelect(props: {
  label?: string
  aria?: string
  error?: string
}) {
  return (
    <Field label={props.label} error={props.error}>
      <Select defaultValue="a">
        <SelectTrigger aria-label={props.aria}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="a">A</SelectItem>
        </SelectContent>
      </Select>
    </Field>
  )
}

describe('Field ↔ SelectTrigger', () => {
  it('la etiqueta del Field nombra al combobox', () => {
    render(<CampoConSelect label="Severity" />)
    expect(
      screen.getByRole('combobox', { name: 'Severity' }),
    ).toBeInTheDocument()
  })

  it('un aria-label explícito del llamante GANA — no se pisa una decisión', () => {
    render(<CampoConSelect label="Severity" aria="Nivel de gravedad" />)
    expect(
      screen.getByRole('combobox', { name: 'Nivel de gravedad' }),
    ).toBeInTheDocument()
  })

  it('el error del Field llega al combobox como aria-invalid', () => {
    render(<CampoConSelect label="Severity" error="Requerido" />)
    expect(screen.getByRole('combobox')).toHaveAttribute('aria-invalid', 'true')
  })

  // CONTROL QUE NO DEBE DISPARAR: fuera de un `Field` no hay nada que heredar, y el contexto
  // devuelve `null`. Si esta celda se pusiera roja al mutar la costura, la costura estaría
  // inventando un nombre en vez de propagar el que existe.
  it('fuera de un Field el trigger no inventa ningún nombre', () => {
    render(
      <Select defaultValue="a">
        <SelectTrigger>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="a">A</SelectItem>
        </SelectContent>
      </Select>,
    )
    const t = screen.getByRole('combobox')
    expect(t).not.toHaveAttribute('aria-labelledby')
  })
})
