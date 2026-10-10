// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Field } from './field'
import { Input } from './input'
import { Select, SelectTrigger, SelectValue } from './select'

describe('Field required state', () => {
  it('announces a required input without changing native form validation', () => {
    render(
      <Field label="Subject reference" required>
        <Input />
      </Field>,
    )
    const input = screen.getByRole('textbox', { name: 'Subject reference' })
    expect(input).toBeRequired()
    expect((input as HTMLInputElement).checkValidity()).toBe(true)
  })

  it('announces a required input supplied through the render function', () => {
    render(
      <Field label="Name" required>
        {(props) => <Input {...props} />}
      </Field>,
    )
    expect(screen.getByRole('textbox', { name: 'Name' })).toBeRequired()
  })

  it('announces a required selector on the actual combobox', () => {
    render(
      <Field label="Subject kind" required>
        <Select>
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
        </Select>
      </Field>,
    )
    expect(
      screen.getByRole('combobox', { name: 'Subject kind' }),
    ).toBeRequired()
  })

  it('keeps optional fields optional', () => {
    render(
      <Field label="Name">
        <Input />
      </Field>,
    )
    expect(screen.getByRole('textbox', { name: 'Name' })).not.toBeRequired()
  })

  it('preserves explicit required state on inputs and selectors', () => {
    render(
      <>
        <Field label="Name" required>
          <Input aria-required={false} />
        </Field>
        <Field label="Subject kind" required>
          <Select>
            <SelectTrigger aria-required={false}>
              <SelectValue />
            </SelectTrigger>
          </Select>
        </Field>
        <Field label="Native requirement">
          <Input required />
        </Field>
      </>,
    )
    expect(screen.getByRole('textbox', { name: 'Name' })).not.toBeRequired()
    expect(
      screen.getByRole('combobox', { name: 'Subject kind' }),
    ).not.toBeRequired()
    expect(
      screen.getByRole('textbox', { name: 'Native requirement' }),
    ).toBeRequired()
  })
})
