// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import { lazy, type ComponentType } from 'react'
import { expect, it } from 'vitest'
import { lazyView } from './lazy-view'

it('holds skeleton rows, never a spinner, while a view loads', () => {
  const Pending = lazy(() => new Promise<{ default: ComponentType }>(() => {}))
  render(<>{lazyView(Pending)()}</>)
  const status = screen.getByRole('status')
  expect(status).toHaveTextContent('Loading…')
  expect(status).toHaveAttribute('data-state', 'loading')
  expect(
    status.querySelectorAll('[data-slot="state-block-skeleton"]').length,
  ).toBeGreaterThan(0)
})
