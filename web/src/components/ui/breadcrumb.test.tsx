// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The breadcrumb is ONE line (console-ui-layout-density, 2026-09-06): the wrapping
// list is what broke the topbar at 1024/390 px. jsdom cannot measure the ellipsis,
// so this pins the contract the layout depends on — no wrap, shrinkable items, a
// truncating page crumb whose full text remains its accessible name and tooltip.
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from './breadcrumb'

const TITLE = 'Deployment & integration'

function Trail({ title }: { title?: string }) {
  return (
    <Breadcrumb>
      <BreadcrumbList>
        <BreadcrumbItem>
          <BreadcrumbLink href="/">Overview</BreadcrumbLink>
        </BreadcrumbItem>
        <BreadcrumbSeparator />
        <BreadcrumbItem>
          <BreadcrumbPage title={title}>{TITLE}</BreadcrumbPage>
        </BreadcrumbItem>
      </BreadcrumbList>
    </Breadcrumb>
  )
}

describe('Breadcrumb — single line, truncating', () => {
  it('never wraps and lets every item shrink', () => {
    render(<Trail />)
    const list = screen.getByRole('navigation', { name: 'Breadcrumb' })
      .firstElementChild as HTMLElement
    expect(list.tagName).toBe('OL')
    expect(list).toHaveClass('flex-nowrap', 'min-w-0')
    expect(list).not.toHaveClass('flex-wrap')
    for (const li of list.querySelectorAll('li:not([aria-hidden])'))
      expect(li).toHaveClass('min-w-0')
    expect(screen.getByRole('link', { name: 'Overview' })).toHaveClass(
      'truncate',
      'min-w-0',
    )
  })

  it('truncates the page crumb while its full text stays the name and the tooltip', () => {
    render(<Trail />)
    const page = screen.getByRole('link', { name: TITLE })
    expect(page).toHaveAttribute('aria-current', 'page')
    expect(page).toHaveClass('truncate')
    expect(page).toHaveAttribute('title', TITLE)
    expect(page).toHaveTextContent(TITLE)
  })

  it('lets a caller override the tooltip', () => {
    render(<Trail title="Custom" />)
    expect(screen.getByRole('link', { name: TITLE })).toHaveAttribute(
      'title',
      'Custom',
    )
  })
})
