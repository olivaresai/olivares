// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A file beside what git HEAD holds for it: the plain view stays the
// default, the Changes view compares HEAD's text with the current text, and with nothing
// to compare against there is no switch at all.
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

vi.mock('@/components/ui/code-diff', () => ({
  CodeDiff: ({
    original,
    modified,
    originalLabel,
    modifiedLabel,
  }: {
    original: string
    modified: string
    originalLabel: string
    modifiedLabel: string
  }) => (
    <div data-testid="code-diff">
      <span data-testid="diff-original" aria-label={originalLabel}>
        {original}
      </span>
      <span data-testid="diff-modified" aria-label={modifiedLabel}>
        {modified}
      </span>
    </div>
  ),
}))

import { ApiError } from '@/lib/api/errors'
import { HeadDiff } from './head-diff'
import { headUnavailable } from './head-unavailable'

describe('HeadDiff', () => {
  it('shows only the file when HEAD has nothing to compare against', () => {
    render(
      <HeadDiff committed={undefined} current="new text">
        <p>the plain file</p>
      </HeadDiff>,
    )
    expect(screen.getByText('the plain file')).toBeInTheDocument()
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('offers Changes since HEAD, with the file as the default view', async () => {
    const user = userEvent.setup()
    render(
      <HeadDiff committed={'# Title\n'} current={'# Title\nedited\n'}>
        <p>the plain file</p>
      </HeadDiff>,
    )
    expect(screen.getByText('the plain file')).toBeInTheDocument()
    expect(screen.queryByTestId('code-diff')).toBeNull()
    expect(screen.getByRole('button', { name: 'File' })).toHaveAttribute(
      'aria-pressed',
      'true',
    )

    await user.click(screen.getByRole('button', { name: 'Changes since HEAD' }))
    expect(screen.queryByText('the plain file')).toBeNull()
    expect(screen.getByLabelText('HEAD (committed)')).toHaveTextContent(
      '# Title',
    )
    expect(screen.getByLabelText('HEAD (committed)')).not.toHaveTextContent(
      'edited',
    )
    expect(screen.getByLabelText('Current')).toHaveTextContent('edited')

    await user.click(screen.getByRole('button', { name: 'File' }))
    expect(screen.getByText('the plain file')).toBeInTheDocument()
    expect(screen.queryByTestId('code-diff')).toBeNull()
  })

  it('says so when the file is identical to HEAD, and an empty HEAD file is still a file', async () => {
    const user = userEvent.setup()
    const { rerender } = render(
      <HeadDiff committed="same" current="same">
        <p>the plain file</p>
      </HeadDiff>,
    )
    await user.click(screen.getByRole('button', { name: 'Changes since HEAD' }))
    expect(screen.getByText('No differences from HEAD.')).toBeInTheDocument()
    expect(screen.queryByTestId('code-diff')).toBeNull()

    // An empty committed file is a comparison (everything added), not "no HEAD".
    rerender(
      <HeadDiff committed="" current="now has text">
        <p>the plain file</p>
      </HeadDiff>,
    )
    expect(screen.getByTestId('code-diff')).toBeInTheDocument()
    expect(screen.getByLabelText('Current')).toHaveTextContent('now has text')
  })

  it('keeps the file view itself when HEAD arrives, so an editor keeps its place', () => {
    const { rerender } = render(
      <HeadDiff committed={undefined} current="text">
        <p>the plain file</p>
      </HeadDiff>,
    )
    const before = screen.getByText('the plain file')
    rerender(
      <HeadDiff committed="older" current="text">
        <p>the plain file</p>
      </HeadDiff>,
    )
    expect(screen.getByText('the plain file')).toBe(before)
    expect(screen.getByRole('group', { name: 'View' })).toBeInTheDocument()
  })

  it('says the comparison could not be made, under the file, only when asked to', () => {
    const { rerender } = render(
      <HeadDiff committed={undefined} current="text" unavailable>
        <p>the plain file</p>
      </HeadDiff>,
    )
    expect(screen.getByText('the plain file')).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent(
      'Could not compare with HEAD.',
    )
    rerender(
      <HeadDiff committed={undefined} current="text">
        <p>the plain file</p>
      </HeadDiff>,
    )
    expect(screen.queryByRole('status')).toBeNull()
  })
})

describe('headUnavailable', () => {
  it('treats a 404 and no error as nothing to say, and every other failure as worth saying', () => {
    expect(headUnavailable(null)).toBe(false)
    expect(headUnavailable(undefined)).toBe(false)
    expect(headUnavailable(new ApiError(404, 'not_found', 'not in HEAD'))).toBe(
      false,
    )
    for (const status of [403, 413, 500, 503]) {
      expect(headUnavailable(new ApiError(status, 'x', 'x'))).toBe(true)
    }
    expect(headUnavailable(new TypeError('network'))).toBe(true)
  })
})
