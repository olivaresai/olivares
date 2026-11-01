// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import type { ComponentType } from 'react'
import { render, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
const extension = vi.hoisted(() => ({
  Preflight: undefined as ComponentType | undefined,
}))
vi.mock('@/features/extensions', () => ({
  LAUNCH_IDENTITY_EXTENSION: extension,
}))
import { LaunchPreflight } from './launch-identity'

it('renders the edition preflight when supplied', () => {
  extension.Preflight = () => <p>Launch policy fixture</p>
  render(<LaunchPreflight />)
  expect(screen.getByText('Launch policy fixture')).toBeVisible()
})
it('keeps the default launch form unchanged without an edition preflight', () => {
  extension.Preflight = undefined
  const { container } = render(<LaunchPreflight />)
  expect(container).toBeEmptyDOMElement()
})
