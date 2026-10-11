// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { CatchBoundary, type ErrorRouteComponent } from '@tanstack/react-router'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it } from 'vitest'
import { RouteErrorPage } from './route-error'

// Checked by the app typecheck: the boundary can pass any thrown value.
const ErrorPage: ErrorRouteComponent = RouteErrorPage

it.each([
  ['Error', new Error('private transport detail')],
  ['string', 'private transport detail'],
  ['object', { message: 'private transport detail' }],
  ['null', null],
  ['undefined', undefined],
  ['false', false],
  ['zero', 0],
])('announces and recovers from a thrown %s', async (_, error) => {
  const user = userEvent.setup()
  let failed = true
  function View() {
    if (failed) throw error
    return <p>View ready</p>
  }
  render(
    <CatchBoundary getResetKey={() => 'view'} errorComponent={ErrorPage}>
      <View />
    </CatchBoundary>,
  )

  expect(screen.getByRole('alert')).toHaveTextContent('This view crashed')
  expect(
    screen.getByRole('heading', { name: 'This view crashed' }),
  ).toBeInTheDocument()
  expect(screen.queryByText(/private transport detail/)).not.toBeInTheDocument()
  expect(screen.queryByText('View ready')).not.toBeInTheDocument()

  failed = false
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  expect(screen.getByText('View ready')).toBeInTheDocument()
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})
