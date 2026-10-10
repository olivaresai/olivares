// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE PAGE PRIMITIVES WEAR THE 1.0 LOOK, WITH THE SAME API (console 1.0). Each
// case names the part of the look the design fixes for that primitive: the orange fill for the
// one primary action; every other labelled action a tonal fill with no border (`outline` is the
// same button); a control that cannot act keeps its text legible in the third tone with no
// dashed border; links in the text colour; icon buttons with a 40 px hit area; a 2 px focus
// outline (an outline, so forced-colors mode keeps it); controls on the canvas; the orange for
// a checked switch; the selected tab as a raised segment; and the state panels in the state
// set's words and tones.
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

  it('Button: a control that cannot act stays legible in the third tone, with no dashed border', () => {
    render(<Button aria-disabled="true">Deploy</Button>)
    const c = classes(screen.getByRole('button'))
    expect(c).toEqual(
      expect.arrayContaining([
        'aria-disabled:text-text-3',
        'disabled:text-text-3',
      ]),
    )
    expect(c.some((k) => k.includes('border-dashed'))).toBe(false)
    expect(c).not.toContain('disabled:opacity-50')
  })

  it('Button: secondary is a tonal fill with no border, and outline is the same button', () => {
    render(
      <>
        <Button variant="secondary">Export</Button>
        <Button variant="outline">Filters</Button>
      </>,
    )
    const secondary = classes(screen.getByRole('button', { name: 'Export' }))
    const outline = classes(screen.getByRole('button', { name: 'Filters' }))
    expect(secondary).toEqual(
      expect.arrayContaining(['bg-active', 'text-text']),
    )
    expect(secondary).not.toContain('border-line-strong')
    expect(outline).toEqual(secondary)
  })

  it('Button: a link is the text colour, never orange', () => {
    render(<Button variant="link">Show details</Button>)
    const c = classes(screen.getByRole('button'))
    expect(c).toContain('text-text')
    expect(c).not.toContain('text-accent-text')
  })

  it('Button: an icon button has a 40 px hit area around its 32 or 28 px face', () => {
    render(
      <>
        <Button size="icon" variant="ghost" aria-label="Stop">
          ■
        </Button>
        <Button size="icon-sm" variant="ghost" aria-label="Close">
          ×
        </Button>
      </>,
    )
    const hitArea = ['relative', 'after:absolute', "after:content-['']"]
    expect(classes(screen.getByRole('button', { name: 'Stop' }))).toEqual(
      expect.arrayContaining([
        'size-8',
        'm-1',
        'after:-inset-[5px]',
        ...hitArea,
      ]),
    )
    expect(classes(screen.getByRole('button', { name: 'Close' }))).toEqual(
      expect.arrayContaining([
        'size-7',
        'm-1.5',
        'after:-inset-[7px]',
        ...hitArea,
      ]),
    )
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

  it('Tabs: the selected tab is a raised segment, the others the second tone, no orange rule', () => {
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
        'data-[state=active]:bg-raised',
        // A ring, not a theme shadow: --shadow-card is no Tailwind token and is none in dark.
        'data-[state=active]:shadow-[0_0_0_1px_var(--line-strong)]',
      ]),
    )
    expect(c).not.toContain('data-[state=active]:border-accent-strong')
    expect(c).not.toContain('data-[state=active]:shadow-card')
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
