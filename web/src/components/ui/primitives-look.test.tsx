// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE PAGE PRIMITIVES WEAR THE v26.10 LOOK, WITH THE SAME API. Each case names the part of the
// look the design fixes for that primitive (the redesign's tokens and component rules): the
// orange fill with its border for the one primary action, a dashed boundary and the third
// text tone for a control that cannot act, a 2 px focus outline (an outline, so forced-colors
// mode keeps it), controls on the canvas, the orange for a checked switch and the selected
// tab, and the state panels in the state set's words and tones.
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Button } from './button'
import { EmptyState } from './empty-state'
import { ErrorState, ForbiddenState } from './error-state'
import { Input } from './input'
import { Kbd } from './kbd'
import { Select, SelectTrigger, SelectValue } from './select'
import { Skeleton } from './skeleton'
import { Switch } from './switch'
import { Tabs, TabsList, TabsTrigger } from './tabs'

const classes = (el: Element) => (el.getAttribute('class') ?? '').split(/\s+/)

describe('the v26.10 look of the page primitives', () => {
  it('Button: the primary fill carries its border and the on-accent text', () => {
    render(<Button variant="primary">New session</Button>)
    const c = classes(screen.getByRole('button'))
    expect(c).toEqual(
      expect.arrayContaining([
        'bg-accent',
        'border-accent-border',
        'text-on-accent',
      ]),
    )
  })

  it('Button: a control that cannot act is dashed, in the third tone, never faded', () => {
    render(<Button aria-disabled="true">Deploy</Button>)
    const c = classes(screen.getByRole('button'))
    expect(c).toEqual(
      expect.arrayContaining([
        'aria-disabled:border-dashed',
        'aria-disabled:border-ctl-border',
        'aria-disabled:text-text-3',
        'disabled:border-dashed',
        'disabled:text-text-3',
      ]),
    )
    expect(c).not.toContain('disabled:opacity-50')
  })

  it('Button: focus is a 2 px outline in the focus color', () => {
    render(<Button>Retry</Button>)
    expect(classes(screen.getByRole('button'))).toEqual(
      expect.arrayContaining([
        'focus-visible:outline-2',
        'focus-visible:outline-offset-2',
        'focus-visible:outline-focus',
      ]),
    )
  })

  it('Button: the quiet variant is the second text tone', () => {
    render(<Button variant="ghost">Open diagnostics</Button>)
    expect(classes(screen.getByRole('button'))).toContain('text-text-2')
  })

  it('Input and Select: on the canvas, with the focus outline and the dashed unavailable look', () => {
    render(
      <>
        <Input aria-label="Host" />
        <Select>
          <SelectTrigger aria-label="Region">
            <SelectValue />
          </SelectTrigger>
        </Select>
      </>,
    )
    for (const el of [
      screen.getByLabelText('Host'),
      screen.getByRole('combobox', { name: 'Region' }),
    ]) {
      const c = classes(el)
      expect(c).toEqual(
        expect.arrayContaining([
          'bg-canvas',
          'border-ctl-border',
          'focus-visible:outline-focus',
          'aria-disabled:border-dashed',
        ]),
      )
    }
  })

  it('Switch: the checked track is the orange fill with the on-accent thumb', () => {
    render(<Switch aria-label="Notify" defaultChecked />)
    const track = screen.getByRole('switch', { name: 'Notify' })
    expect(classes(track)).toEqual(
      expect.arrayContaining([
        'bg-ctl-border',
        'data-[state=checked]:bg-accent',
      ]),
    )
    expect(classes(track.firstElementChild!)).toContain(
      'data-[state=checked]:bg-on-accent',
    )
  })

  it('Tabs: the selected tab is marked by the selection orange, the others are the second tone', () => {
    render(
      <Tabs defaultValue="items">
        <TabsList aria-label="Work">
          <TabsTrigger value="items">Items</TabsTrigger>
          <TabsTrigger value="decisions">Decisions</TabsTrigger>
        </TabsList>
      </Tabs>,
    )
    const c = classes(screen.getByRole('tab', { name: 'Items' }))
    expect(c).toEqual(
      expect.arrayContaining([
        'text-text-2',
        'data-[state=active]:text-text',
        'data-[state=active]:border-accent-strong',
      ]),
    )
  })

  it('EmptyState: the description is the second tone at the small step', () => {
    render(
      <EmptyState
        title="No routes yet"
        description="Add a route to send findings."
      />,
    )
    const description = screen.getByText('Add a route to send findings.')
    expect(classes(description)).toEqual(
      expect.arrayContaining(['text-caption', 'text-text-2']),
    )
  })

  it('ErrorState: a banner on the failure fill that says what could not be read', () => {
    render(<ErrorState retry={() => {}} requestId="01J8ZK4Q7M" />)
    const alert = screen.getByRole('alert')
    const banner = alert.querySelector('[data-slot="state-banner"]')!
    expect(banner).not.toBeNull()
    expect(classes(banner)).toEqual(
      expect.arrayContaining(['border-bad', 'bg-bad-soft']),
    )
    expect(alert).toHaveTextContent('01J8ZK4Q7M')
  })

  it('ForbiddenState: calm, a shield in the third tone and never the failure color', () => {
    render(<ForbiddenState />)
    const status = screen.getByRole('status')
    expect(status.innerHTML).not.toMatch(/\b(bg|text|border)-(bad|danger)/)
    expect(status.querySelector('[data-slot="state-icon"]')).not.toBeNull()
  })

  it('Skeleton and Kbd: the selection fill and the strong hairline', () => {
    const { container } = render(
      <>
        <Skeleton className="h-3" />
        <Kbd>N</Kbd>
      </>,
    )
    expect(classes(container.firstElementChild!)).toContain('bg-active')
    expect(classes(screen.getByText('N'))).toEqual(
      expect.arrayContaining([
        'border-line-strong',
        'bg-surface',
        'text-text-2',
      ]),
    )
  })
})
