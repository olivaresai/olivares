// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE PAGE PRIMITIVES WEAR THE 1.0 LOOK, WITH THE SAME API (console 1.0). Each
// case names the part of the look the design fixes for that primitive. The controls are drawn
// as direction A2 draws them: bold labels; the regular button on the selection token (10 % of
// its ink in the drawing, 15 % under the pointer, 24 % pressed); the orange with the graphite ink
// for the one suggested action; the destructive answer outlined, never filled at rest; small 30,
// regular by the density token, large 44, and 44 px high on a phone; icon buttons with a 42 px hit
// area; one press layer; a control that cannot act faded to 45 %, and one that keeps its pointer
// events (aria-disabled) never looks pressable; a 2 px focus outline (an outline, so forced-colors mode
// keeps it); fields as wells; the switch a full pill that turns orange; the toggle group's choice
// raised and bold; the keycap a soft key. Then the selected tab as a raised segment, and the
// state panels in the state set's words and tones. The states and overlays are drawn as
// direction A2 draws them too: the state page with its large icon, a real title, Retry and the
// description at the large body step, made small inside a panel; a menu item's focus ring
// inside its row, and its press layer over the hover.
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { Button } from './button'
import { Checkbox } from './checkbox'
import { Combobox } from './combobox'
import { CopyButton } from './copy-button'
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from './dropdown-menu'
import { EmptyState } from './empty-state'
import { ErrorState, ForbiddenState, errorDetail } from './error-state'
import { Field } from './field'
import { MetricStat, StatGrid } from './figures'
import { Input } from './input'
import { Kbd } from './kbd'
import { Meter } from './meter'
import { Notice } from './notice'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from './select'
import { Skeleton } from './skeleton'
import { Switch } from './switch'
import { Tabs, TabsList, TabsTrigger } from './tabs'
import { Textarea } from './textarea'
import { Segmented } from './segmented'
import { StatusChip } from './status-chip'
import { CountBadge } from './count-badge'
import { Badge } from './badge'
import { BoxedList, BoxedListRow } from './boxed-list'
import { Card, CardContent, CardHeader, CardTitle } from './card'

const classes = (el: Element) => (el.getAttribute('class') ?? '').split(/\s+/)

describe('the v26.10 look of the page primitives', () => {
  it('states are unboxed and counts use a quiet pill', () => {
    render(
      <>
        <StatusChip tone="warn">Needs you</StatusChip>
        <CountBadge count={2} attention />
      </>,
    )
    expect(screen.getByText('Needs you')).not.toHaveClass(
      'border',
      'rounded-sm',
    )
    expect(screen.getByText('2')).toHaveClass('rounded-full', 'bg-warn-soft')
  })

  it('Tabs lie flat with no track; Segmented keeps the neutral track and 28 px selection', () => {
    render(
      <>
        <Tabs defaultValue="one">
          <TabsList>
            <TabsTrigger value="one">First</TabsTrigger>
          </TabsList>
        </Tabs>
        <Segmented
          aria-label="View"
          options={[{ value: 'one', label: 'List' }]}
          value="one"
          onValueChange={() => {}}
        />
      </>,
    )
    expect(screen.getByRole('tablist')).toHaveClass(
      'bg-transparent',
      'rounded-ctl',
    )
    expect(screen.getByRole('radiogroup')).toHaveClass(
      'bg-hover',
      'rounded-ctl',
    )
    // The toggle group is concentric: 9 px outside, 6 px inside, 3 px between.
    expect(screen.getByRole('radiogroup')).toHaveClass('p-[3px]')
    expect(screen.getByRole('radio')).toHaveClass('h-7', 'rounded-sm')
  })

  it('small segmented controls retain the 28 px face', () => {
    render(
      <Segmented
        size="sm"
        aria-label="View"
        options={[{ value: 'one', label: 'List' }]}
        value="one"
        onValueChange={() => {}}
      />,
    )
    expect(screen.getByRole('radio')).toHaveClass('h-7')
    expect(screen.getByRole('radio')).not.toHaveClass('h-6')
  })

  it('boxed facts use a 6 px radius', () => {
    render(<Badge>Read only</Badge>)
    expect(screen.getByText('Read only')).toHaveClass('rounded-[6px]')
    // A2: a 22 px tag on the text tint, 12/500, no outline; a status fact is bold.
    expect(screen.getByText('Read only')).toHaveClass(
      'h-[22px]',
      'bg-muted',
      'text-overline',
      'font-medium',
      'text-text-2',
    )
    expect(screen.getByText('Read only')).not.toHaveClass('border')
    render(<Badge variant="success">Valid</Badge>)
    expect(screen.getByText('Valid')).toHaveClass(
      'bg-success-soft',
      'font-bold',
    )
    expect(screen.getByText('Valid')).not.toHaveClass('border')
  })
  it('Button: the suggested action is the orange with the graphite ink, flat, with a bold label', () => {
    render(<Button variant="primary">New session</Button>)
    const c = classes(screen.getByRole('button'))
    expect(c).toEqual(
      expect.arrayContaining([
        'bg-accent',
        'text-on-accent',
        'font-bold',
        'hover:bg-accent-hover',
        'active:bg-accent-active',
      ]),
    )
    // Flat: no edge of its own and no lift.
    expect(c).not.toContain('border-accent-border')
    expect(c.some((k) => /^(hover:)?brightness/.test(k))).toBe(false)
  })

  it('Button: a control that cannot act is faded to 45 % and does not react to the pointer', () => {
    render(<Button aria-disabled="true">Deploy</Button>)
    const c = classes(screen.getByRole('button'))
    expect(c).toEqual(
      expect.arrayContaining([
        'disabled:opacity-45',
        'disabled:pointer-events-none',
        'aria-disabled:opacity-45',
        'aria-disabled:cursor-not-allowed',
      ]),
    )
    expect(c.some((k) => k.includes('border-dashed'))).toBe(false)
    expect(c).not.toContain('aria-disabled:text-text-3')
  })

  it('Button: an aria-disabled control keeps its rest look under the pointer and when pressed', () => {
    // It keeps its pointer events so its reason shows on hover, so every hover and press layer
    // has an aria-disabled twin that paints the rest look back: it must not look pressable.
    const variants = [
      'primary',
      'secondary',
      'outline',
      'ghost',
      'icon',
      'danger',
      'destructive',
      'destructive-solid',
      'link',
    ] as const
    render(
      <>
        {variants.map((v) => (
          <Button key={v} variant={v} aria-disabled="true">
            {v}
          </Button>
        ))}
      </>,
    )
    const root = (k: string) => k.split(':').pop()!.split('-')[0]
    for (const v of variants) {
      const c = classes(screen.getByRole('button', { name: v }))
      const atRest = c.filter((k) => !k.includes(':'))
      for (const layer of ['hover', 'active']) {
        const quiet = c.filter((t) => t.startsWith(`aria-disabled:${layer}:`))
        for (const k of c.filter((k) => k.startsWith(`${layer}:`))) {
          const twins = quiet.filter((t) => root(t) === root(k))
          expect(twins, `${v}: ${k} has an aria-disabled twin`).not.toEqual([])
          for (const t of twins) {
            const look = t.slice(`aria-disabled:${layer}:`.length)
            expect(
              atRest.includes(look) || look === 'brightness-100',
              `${v}: ${t} paints the rest look`,
            ).toBe(true)
          }
        }
      }
    }
  })

  it('Button: the regular button rests on the selection token (10 % in the drawing), 15 % of its ink under the pointer, 24 % pressed, and outline is the same button', () => {
    render(
      <>
        <Button variant="secondary">Export</Button>
        <Button variant="outline">Filters</Button>
      </>,
    )
    const secondary = classes(screen.getByRole('button', { name: 'Export' }))
    const outline = classes(screen.getByRole('button', { name: 'Filters' }))
    expect(secondary).toEqual(
      expect.arrayContaining([
        'bg-active',
        'text-text',
        'font-bold',
        'hover:bg-[color-mix(in_srgb,var(--text)_15%,transparent)]',
        'active:bg-[color-mix(in_srgb,var(--text)_24%,transparent)]',
      ]),
    )
    expect(secondary).not.toContain('border-line-strong')
    // Motion comes from the duration token (none under reduced motion), not a fixed time.
    expect(secondary.some((k) => /^duration-\d+$/.test(k))).toBe(false)
    expect(outline).toEqual(secondary)
  })

  it('Button: the destructive answer is outlined in its colour and never filled at rest', () => {
    render(
      <>
        <Button variant="danger">Deny</Button>
        <Button variant="destructive-solid">Delete forever</Button>
      </>,
    )
    const deny = classes(screen.getByRole('button', { name: 'Deny' }))
    expect(deny).toEqual(
      expect.arrayContaining([
        'bg-transparent',
        'text-bad',
        'shadow-[inset_0_0_0_1px_color-mix(in_srgb,var(--bad)_45%,transparent)]',
        'hover:bg-[color-mix(in_srgb,var(--bad)_8%,transparent)]',
        'active:shadow-[inset_0_0_0_2px_var(--bad)]',
      ]),
    )
    expect(deny).not.toContain('bg-bad-soft')
    // The solid fill is for the confirm step of an irreversible act only.
    expect(
      classes(screen.getByRole('button', { name: 'Delete forever' })),
    ).toEqual(
      expect.arrayContaining([
        'bg-danger-solid',
        'text-danger-solid-foreground',
      ]),
    )
  })

  it('Button: small 30, regular by the density token, large 44, and 44 px high on a phone', () => {
    render(
      <>
        <Button size="sm">Small</Button>
        <Button>Regular</Button>
        <Button size="lg">Large</Button>
        <Button variant="link">Details</Button>
      </>,
    )
    const of = (name: string) => classes(screen.getByRole('button', { name }))
    expect(of('Small')).toEqual(
      expect.arrayContaining(['h-[30px]', 'text-caption', 'max-md:min-h-11']),
    )
    expect(of('Regular')).toEqual(
      expect.arrayContaining([
        'h-[var(--density-control)]',
        'text-body',
        'max-md:min-h-11',
      ]),
    )
    expect(of('Large')).toEqual(
      expect.arrayContaining(['h-11', 'text-body-l', 'max-md:min-h-11']),
    )
    // A link inside a sentence keeps the line's height on a phone.
    expect(of('Details')).toContain('max-md:min-h-0')
    // A key hint inside a button is a soft key in the button's own ink.
    expect(of('Regular')).toEqual(
      expect.arrayContaining([
        '[&_kbd]:bg-current/14',
        '[&_kbd]:text-inherit',
        '[&_kbd]:shadow-none',
      ]),
    )
  })

  it('Button: a link is the accent text with its underline at 45 %, full under the pointer', () => {
    render(<Button variant="link">Show details</Button>)
    const c = classes(screen.getByRole('button'))
    expect(c).toEqual(
      expect.arrayContaining([
        'text-accent-text',
        'underline',
        'decoration-accent-text/45',
        'hover:decoration-accent-text',
      ]),
    )
    expect(c).not.toContain('text-text')
    // The accent ink, never the orange fill.
    expect(c).not.toContain('bg-accent')
  })

  it('Button: an icon button has a 42 px hit area around its 34 or 30 px face, and is 44 px on a phone', () => {
    render(
      <>
        <Button size="icon" variant="ghost" aria-label="Stop">
          ■
        </Button>
        <Button size="icon-sm" variant="ghost" aria-label="Close">
          ×
        </Button>
        <Button
          size="icon"
          variant="icon"
          aria-label="Show the panel"
          aria-pressed="true"
        >
          ▣
        </Button>
      </>,
    )
    const hitArea = [
      'relative',
      'after:absolute',
      "after:content-['']",
      'max-md:size-11',
    ]
    expect(classes(screen.getByRole('button', { name: 'Stop' }))).toEqual(
      expect.arrayContaining([
        'size-[var(--density-control)]',
        'm-1',
        'after:-inset-[5px]',
        ...hitArea,
      ]),
    )
    expect(classes(screen.getByRole('button', { name: 'Close' }))).toEqual(
      expect.arrayContaining([
        'size-[30px]',
        'm-1.5',
        'after:-inset-[7px]',
        ...hitArea,
      ]),
    )
    // A pressed toggle button is selected: the selection fill, the full ink and a bold label.
    expect(
      classes(screen.getByRole('button', { name: 'Show the panel' })),
    ).toEqual(
      expect.arrayContaining([
        'aria-pressed:bg-active',
        'aria-pressed:text-text',
        'aria-pressed:font-bold',
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

  it('Button: the flat variant is the second text tone at 500, with the hover and press layers', () => {
    render(<Button variant="ghost">Open diagnostics</Button>)
    expect(classes(screen.getByRole('button'))).toEqual(
      expect.arrayContaining([
        'bg-transparent',
        'text-text-2',
        'font-medium',
        'hover:bg-hover',
        'hover:text-text',
        'active:bg-[var(--pressed)]',
      ]),
    )
  })

  it('Fields are wells: a recessed fill, an inner edge, a 3:1 boundary that darkens under the pointer and takes the ring', () => {
    render(
      <>
        <Input aria-label="Host" />
        <Textarea aria-label="Notes" />
        <Select>
          <SelectTrigger aria-label="Region">
            <SelectValue />
          </SelectTrigger>
        </Select>
        <Combobox
          aria-label="Owner"
          options={[]}
          value={null}
          onChange={() => undefined}
        />
      </>,
    )
    const host = screen.getByLabelText('Host')
    const notes = screen.getByLabelText('Notes')
    const region = screen.getByRole('combobox', { name: 'Region' })
    const owner = screen.getByRole('button', { name: 'Owner' })
    for (const el of [host, notes, region, owner]) {
      const c = classes(el)
      expect(c).toEqual(
        expect.arrayContaining([
          'bg-[var(--well)]',
          'shadow-[var(--shadow-well)]',
          'border-ctl-border',
          'rounded-ctl',
          'hover:border-text-2',
          'focus-visible:border-focus',
          'focus-visible:outline-2',
          'focus-visible:-outline-offset-2',
          'focus-visible:outline-focus',
          'aria-[invalid=true]:border-bad',
          'aria-disabled:opacity-45',
          'disabled:opacity-45',
        ]),
      )
      expect(c.some((k) => k.includes('border-dashed'))).toBe(false)
      expect(c).not.toContain('bg-canvas')
    }
    // One height with the buttons beside them, 44 px on a phone.
    for (const el of [host, region, owner])
      expect(classes(el)).toEqual(
        expect.arrayContaining([
          'h-[var(--density-control)]',
          'max-md:min-h-11',
        ]),
      )
    // A read-only value sits flat in its well.
    expect(classes(host)).toContain('read-only:shadow-none')
  })

  it('Select: inside a boxed row it is a combo row, its value and a chevron with no box', () => {
    render(
      <Select>
        <SelectTrigger aria-label="Language">
          <SelectValue />
        </SelectTrigger>
      </Select>,
    )
    const c = classes(screen.getByRole('combobox', { name: 'Language' }))
    const inRow = '[[data-slot=boxed-list-row]_&]:'
    expect(c).toEqual(
      expect.arrayContaining(
        [
          'border-transparent',
          'bg-transparent',
          'shadow-none',
          'justify-end',
          'font-medium',
          'text-text-2',
          'hover:border-transparent',
          'hover:bg-hover',
          'hover:text-text',
        ].map((k) => inRow + k),
      ),
    )
  })

  it('Select: an option is 44 px high on a phone', () => {
    render(
      <Select defaultOpen defaultValue="en">
        <SelectTrigger aria-label="Language">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="en">English</SelectItem>
          <SelectItem value="es" hint="Castellano">
            Spanish
          </SelectItem>
        </SelectContent>
      </Select>,
    )
    for (const name of [/english/i, /spanish/i])
      expect(screen.getByRole('option', { name })).toHaveClass(
        'max-md:min-h-11',
      )
  })

  it('Switch: a full pill, a grey track and a white knob off, the orange with a graphite knob and an accent edge on', () => {
    render(<Switch aria-label="Notify" defaultChecked />)
    const track = screen.getByRole('switch', { name: 'Notify' })
    expect(classes(track)).toEqual(
      expect.arrayContaining([
        'h-6',
        'w-11',
        'rounded-full',
        'bg-ctl-border',
        'data-[state=checked]:bg-accent',
        // The accent edge is the outermost pixel, as on the checkbox (3:1 on every surface).
        'data-[state=checked]:border-accent-strong',
        'data-[state=checked]:hover:bg-accent-hover',
        // Under the pointer the off track darkens: toward the ink in light, the frame in dark.
        'data-[state=unchecked]:hover:bg-[color-mix(in_srgb,var(--text)_12%,var(--ctl-border))]',
        'dark:data-[state=unchecked]:hover:bg-[color-mix(in_srgb,var(--frame)_8%,var(--ctl-border))]',
        // 44 px of pointer target over a 24 px track: 11 px from inside its 1 px border.
        'before:-inset-y-[11px]',
        'disabled:opacity-45',
        'aria-disabled:opacity-45',
      ]),
    )
    expect(classes(track)).not.toContain('data-[state=checked]:bg-foreground')
    expect(classes(track)).not.toContain(
      'data-[state=checked]:shadow-[inset_0_0_0_1px_var(--accent-strong)]',
    )
    // The knob that is off is white: the surface in light, the inverse surface in dark and
    // black (the black theme's text ink is grey and falls under 3:1 on the track).
    const knob = classes(track.firstElementChild!)
    expect(knob).toEqual(
      expect.arrayContaining([
        'size-[18px]',
        'bg-surface',
        'dark:bg-[var(--inverse)]',
        'data-[state=checked]:bg-on-accent',
      ]),
    )
    expect(knob).not.toContain('dark:bg-text')
  })

  it('Checkbox: checked and mixed are the orange with the graphite mark and an accent edge', () => {
    render(
      <>
        <Checkbox aria-label="Agree" defaultChecked />
        <Checkbox aria-label="Mix" checked="indeterminate" />
      </>,
    )
    // Both state variants render, so the pinned classes are the ones a real
    // checked / indeterminate box paints, not just the static class list.
    // (RTL's `checked` filter cannot express 'mixed' — the attribute is read
    // directly instead.)
    expect(
      screen.getByRole('checkbox', { name: 'Agree', checked: true }),
    ).toBeTruthy()
    const mix = screen.getByRole('checkbox', { name: 'Mix' })
    expect(mix.getAttribute('aria-checked')).toBe('mixed')
    expect(mix.getAttribute('data-state')).toBe('indeterminate')
    for (const name of ['Agree', 'Mix']) {
      const c = classes(screen.getByRole('checkbox', { name }))
      expect(c).toEqual(
        expect.arrayContaining([
          'size-[18px]',
          'rounded-[5px]',
          'border-[1.5px]',
          'bg-surface',
          'border-ctl-border',
          'data-[state=checked]:bg-accent',
          'data-[state=indeterminate]:bg-accent',
          'data-[state=checked]:border-accent-strong',
          'data-[state=indeterminate]:border-accent-strong',
          'data-[state=checked]:text-on-accent',
          'data-[state=indeterminate]:text-on-accent',
          'focus-visible:outline-focus',
          'disabled:opacity-45',
        ]),
      )
      expect(c).not.toContain('data-[state=checked]:bg-foreground')
    }
  })

  it('Segmented: the chosen option sits on the surface with the card shadow and a bold label', () => {
    render(
      <Segmented
        aria-label="Theme"
        options={[
          { value: 'light', label: 'Light' },
          { value: 'dark', label: 'Dark' },
        ]}
        value="dark"
        onValueChange={() => {}}
      />,
    )
    const chosen = classes(screen.getByRole('radio', { name: 'Dark' }))
    expect(chosen).toEqual(
      expect.arrayContaining([
        'data-[state=on]:bg-surface',
        'data-[state=on]:font-bold',
        'data-[state=on]:text-text',
        'data-[state=on]:shadow-[var(--shadow-card)]',
        'dark:data-[state=on]:bg-[color-mix(in_srgb,var(--text)_15%,transparent)]',
        'hover:bg-hover',
        'focus-visible:-outline-offset-2',
        'max-md:min-h-11',
      ]),
    )
    expect(chosen).not.toContain('data-[state=on]:bg-raised')
  })

  it('Field: the label in bold, the help in the second tone, the error with its icon', () => {
    render(
      <Field label="Name" description="Shown on invites." error="Required.">
        <Input />
      </Field>,
    )
    expect(screen.getByText('Name')).toHaveClass('font-bold', 'text-body')
    expect(screen.getByText('Shown on invites.')).toHaveClass(
      'text-caption',
      'text-text-2',
    )
    const alert = screen.getByRole('alert')
    expect(alert).toHaveTextContent('Required.')
    expect(alert).toHaveClass('font-medium', 'text-bad')
    expect(alert.querySelector('svg[aria-hidden="true"]')).not.toBeNull()
    expect(alert.closest('[data-slot="field"]')).toHaveClass('gap-[7px]')
  })

  it('CopyButton: a flat icon button in the second tone, with the press layer', () => {
    render(
      <CopyButton
        text={() => 'olivares login'}
        label="Copy the command"
        copiedLabel="Copied"
        failedLabel="Could not copy"
      />,
    )
    expect(
      classes(screen.getByRole('button', { name: 'Copy the command' })),
    ).toEqual(
      expect.arrayContaining([
        'rounded-sm',
        'text-text-2',
        'hover:bg-hover',
        'hover:text-text',
        'active:bg-[var(--pressed)]',
      ]),
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
        'data-[state=active]:bg-active',
        // A ring, not a theme shadow: --shadow-card is no Tailwind token and is none in dark.
        'data-[state=active]:shadow-[0_0_0_1px_var(--line-strong)]',
      ]),
    )
    expect(c).not.toContain('data-[state=active]:border-accent-strong')
    expect(c).not.toContain('data-[state=active]:shadow-card')
  })

  it('EmptyState: the description is the second tone at the large body step', () => {
    render(
      <EmptyState
        title="No routes yet"
        description="Add a route to send findings."
      />,
    )
    const description = screen.getByText('Add a route to send findings.')
    expect(classes(description)).toEqual(
      expect.arrayContaining(['text-body-l', 'text-text-2']),
    )
  })

  it('ErrorState: a state page whose large icon is the failure color, a real title, Retry and the cause last', () => {
    render(<ErrorState retry={() => {}} requestId="01J8ZK4Q7M" />)
    const alert = screen.getByRole('alert')
    const icon = alert.querySelector('[data-slot="state-icon"]')!
    expect(classes(icon)).toEqual(
      expect.arrayContaining(['text-bad', '[&_svg]:size-18']),
    )
    expect(alert.querySelector('[data-slot="notice"]')).toBeNull()
    expect(classes(screen.getByText('Something went wrong'))).toEqual(
      expect.arrayContaining(['text-[1.375rem]/7', 'font-extrabold']),
    )
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
    expect(alert.lastElementChild).toHaveTextContent('01J8ZK4Q7M')
  })

  it('ErrorState: in a panel, the same page made small, with a 40 px icon and the title at the heading step', () => {
    render(<ErrorState size="panel" retry={() => {}} />)
    const alert = screen.getByRole('alert')
    expect(classes(alert)).toContain('py-8')
    expect(classes(alert)).not.toContain('pt-26')
    const icon = classes(alert.querySelector('[data-slot="state-icon"]')!)
    expect(icon).toEqual(
      expect.arrayContaining(['text-bad', '[&_svg]:size-10']),
    )
    expect(icon).not.toContain('[&_svg]:size-18')
    const title = classes(screen.getByText('Something went wrong'))
    expect(title).toEqual(
      expect.arrayContaining(['text-heading', 'font-extrabold']),
    )
    expect(title).not.toContain('text-[1.375rem]/7')
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
  })

  it('DropdownMenu: an item shows keyboard focus as a 2 px ring inside the row, and its press layer wins over the hover', () => {
    render(
      <DropdownMenu open>
        <DropdownMenuTrigger>Actions</DropdownMenuTrigger>
        <DropdownMenuContent>
          <DropdownMenuItem>Rename</DropdownMenuItem>
          <DropdownMenuCheckboxItem checked>
            Wrap lines
          </DropdownMenuCheckboxItem>
        </DropdownMenuContent>
      </DropdownMenu>,
    )
    for (const item of [
      screen.getByRole('menuitem', { name: 'Rename' }),
      screen.getByRole('menuitemcheckbox', { name: 'Wrap lines' }),
    ]) {
      expect(classes(item)).toEqual(
        expect.arrayContaining([
          'focus-visible:outline-2',
          'focus-visible:-outline-offset-2',
          'focus-visible:outline-focus',
          // More specific than `data-[highlighted]:bg-hover`, so it paints under a mouse.
          'data-[highlighted]:active:bg-[var(--pressed)]',
        ]),
      )
      expect(classes(item)).not.toContain('active:bg-[var(--pressed)]')
    }
  })

  it('Notice: one sentence inside its pane, a soft fill and no border', () => {
    render(
      <>
        <Notice icon={<span />}>A session reaches only its provider.</Notice>
        <Notice tone="warn" icon={<span />} role="status">
          Shown from the last reading.
        </Notice>
      </>,
    )
    const calm = screen
      .getByText('A session reaches only its provider.')
      .closest('[data-slot="notice"]')!
    expect(classes(calm)).toEqual(
      expect.arrayContaining(['rounded-card', 'text-caption', 'text-text-2']),
    )
    expect(classes(calm).some((c) => /^border/.test(c))).toBe(false)
    expect(classes(calm).some((c) => c.startsWith('shadow-'))).toBe(false)
    // A2: a tinted band whose own edge is an inner line of its color.
    expect(screen.getByRole('status')).toHaveClass(
      'bg-warn-soft',
      'shadow-[inset_0_0_0_1px_color-mix(in_srgb,var(--warn)_32%,transparent)]',
    )
  })

  it('Figures strip: one raised strip of hairline cells, label, display figure, comparison line', () => {
    render(
      <StatGrid>
        <MetricStat label="Sessions" value="12" caption="3 more than Monday" />
        <MetricStat label="Cost" value="$4.10" />
      </StatGrid>,
    )
    const label = screen.getByText('Sessions')
    // A2: a surface one step above the page with the card shadow and no outline.
    const strip = label.closest('[data-slot="figures"]')!
    expect(strip).toHaveClass(
      'rounded-card',
      'overflow-hidden',
      'bg-surface',
      'shadow-[var(--shadow-card)]',
    )
    expect(strip).not.toHaveClass('border')
    // A cell is split from the next by a hairline, not drawn as a card of its own.
    const cell = label.closest('[data-slot="figure"]')!
    expect(cell).toHaveClass('border-e', 'border-b')
    expect(cell).not.toHaveClass('rounded-card')
    expect(label).toHaveClass('text-text-2')
    expect(label).not.toHaveClass('uppercase')
    expect(screen.getByText('12')).toHaveClass(
      'font-display',
      'text-display',
      'tabular-nums',
    )
    expect(screen.getByText('3 more than Monday')).toHaveClass('text-text-3')
  })

  it('Boxed list and card: a surface with the card shadow, no outline, hairlines between rows only', () => {
    render(
      <>
        <BoxedList aria-label="General">
          <BoxedListRow>Language</BoxedListRow>
          <BoxedListRow>Theme</BoxedListRow>
        </BoxedList>
        <Card aria-label="Spend">
          <CardContent>$41.20</CardContent>
        </Card>
      </>,
    )
    for (const box of [
      screen.getByLabelText('General'),
      screen.getByLabelText('Spend'),
    ]) {
      expect(box).toHaveClass(
        'rounded-card',
        'bg-surface',
        'shadow-[var(--shadow-card)]',
      )
    }
    expect(classes(screen.getByLabelText('General'))).not.toContain('border')
    // The card keeps a border that paints nothing, so a caller's warm edge still shows.
    expect(screen.getByLabelText('Spend')).toHaveClass(
      'border',
      'border-transparent',
    )
    expect(screen.getByText('Theme')).toHaveClass('border-t', 'border-line')
    // A row is as tall as the list-row density says, 9 px above and below its text.
    expect(screen.getByText('Theme')).toHaveClass(
      'min-h-[var(--density-row-list)]',
      'py-[9px]',
    )
    expect(classes(screen.getByText('Theme'))).not.toContain('min-h-[52px]')
    expect(classes(screen.getByText('Theme'))).not.toContain('py-3')
  })

  it('Card: the header carries no hairline; the content under it starts at the header padding', () => {
    render(
      <Card>
        <CardHeader>
          <CardTitle>Spend</CardTitle>
        </CardHeader>
        <CardContent>$41.20</CardContent>
      </Card>,
    )
    const header = screen.getByRole('heading', { name: 'Spend' }).parentElement!
    expect(header).toHaveAttribute('data-slot', 'card-header')
    expect(classes(header)).not.toContain('border-b')
    // The header keeps its padding, so a header alone or over a flush table still has it;
    // the content right under a header drops its own top padding instead of doubling it.
    expect(header).toHaveClass('p-4')
    expect(screen.getByText('$41.20')).toHaveClass(
      'p-4',
      '[[data-slot=card-header]+&]:pt-0',
    )
    expect(header.parentElement).toHaveAttribute('data-slot', 'card')
  })

  it('Meter: a 6 px fill on a 2 px rail anyone can see, amber where the caller says so', () => {
    render(
      <>
        <Meter value={40} label="Week" />
        <Meter value={91} warn label="Five hours" />
        <Meter value={130} label="Over" />
      </>,
    )
    const week = screen.getByRole('meter', { name: 'Week' })
    expect(week).toHaveAttribute('aria-valuenow', '40')
    expect(week).toHaveClass('h-1.5', 'rounded-full')
    // A2: the track is a 2 px rail in the control boundary ink (3:1), not a 1.3:1 tint,
    // and the fill is not clipped by it.
    expect(week).toHaveClass(
      'bg-[linear-gradient(var(--ctl-border),var(--ctl-border))]',
      'bg-size-[100%_2px]',
      'bg-center',
      'bg-no-repeat',
    )
    expect(week).not.toHaveClass('bg-muted', 'overflow-hidden')
    expect(week.firstElementChild).toHaveClass('bg-text-2', 'rounded-full')
    expect(week.firstElementChild).toHaveStyle({ width: '40%' })
    const near = screen.getByRole('meter', { name: 'Five hours' })
    expect(near.firstElementChild).toHaveClass('bg-warn')
    // Past the limit the bar is full, never wider than its track.
    const over = screen.getByRole('meter', { name: 'Over' })
    expect(over.firstElementChild).toHaveStyle({ width: '100%' })
  })

  it('ForbiddenState: calm, a shield in the third tone and never the failure color', () => {
    render(<ForbiddenState />)
    const status = screen.getByRole('status')
    expect(status.innerHTML).not.toMatch(/\b(bg|text|border)-(bad|danger)/)
    expect(status.querySelector('[data-slot="state-icon"]')).not.toBeNull()
  })

  it('Skeleton and Kbd: the selection fill, and the keycap a soft key', () => {
    const { container } = render(
      <>
        <Skeleton className="h-3" />
        <Kbd>N</Kbd>
      </>,
    )
    expect(classes(container.firstElementChild!)).toContain('bg-active')
    const key = classes(screen.getByText('N'))
    expect(key).toEqual(
      expect.arrayContaining([
        'bg-[color-mix(in_srgb,var(--text)_8%,transparent)]',
        'shadow-[inset_0_-1.5px_0_var(--line-strong)]',
        'font-semibold',
        'text-text-2',
      ]),
    )
    // A soft key, not a bordered box.
    expect(key).not.toContain('border')
    expect(key).not.toContain('border-line-strong')
  })
})

describe('ErrorState: the cause behind «Copy detail»', () => {
  // The engine's answer, with a header, a cookie and a token beside its envelope.
  const failure = () =>
    new ApiError(
      503,
      'server_error',
      'The engine did not answer in time.',
      'req-7Q',
      { authorization: 'Bearer s3cret-token' },
      { error: { code: 'server_error' }, cookie: 'sid=s3cret-cookie' },
      '30',
    )

  it('copies exactly the status, the code, the message and the request id, and says so', async () => {
    const user = userEvent.setup()
    const write = vi
      .spyOn(navigator.clipboard, 'writeText')
      .mockResolvedValue(undefined)
    render(<ErrorState retry={() => {}} detail={errorDetail(failure())} />)
    // A person reads the sentence; the cause is not printed.
    expect(screen.queryByText(/server_error/)).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Copy detail' }))
    expect(write).toHaveBeenCalledWith(
      '503 server_error: The engine did not answer in time.\nrequest_id=req-7Q',
    )
    await waitFor(() =>
      expect(screen.getByRole('status')).toHaveTextContent('Copied'),
    )
  })

  it('copies no header, cookie or token the response carried', async () => {
    const user = userEvent.setup()
    const write = vi
      .spyOn(navigator.clipboard, 'writeText')
      .mockResolvedValue(undefined)
    render(<ErrorState retry={() => {}} detail={errorDetail(failure())} />)
    await user.click(screen.getByRole('button', { name: 'Copy detail' }))
    expect(write).toHaveBeenCalledTimes(1)
    expect(write.mock.calls[0][0]).not.toMatch(
      /s3cret|Bearer|authorization|cookie|sid=/i,
    )
  })

  it('offers no «Copy detail» without a detail', () => {
    render(<ErrorState retry={() => {}} requestId="01J8ZK4Q7M" />)
    expect(screen.queryByRole('button', { name: 'Copy detail' })).toBeNull()
  })

  it('shows the detail to select by hand when the clipboard refuses', async () => {
    const user = userEvent.setup()
    vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(
      new Error('denied'),
    )
    render(<ErrorState retry={() => {}} detail="503 server_error: down" />)
    expect(screen.queryByText('503 server_error: down')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Copy detail' }))
    await waitFor(() =>
      expect(screen.getByRole('status')).toHaveTextContent(
        'Could not copy. Select the text and copy it.',
      ),
    )
    expect(screen.getByText('503 server_error: down')).toBeInTheDocument()
  })

  it('reads a cause only from what the engine or the transport said', () => {
    expect(
      errorDetail(new NetworkError('The control plane is unreachable.')),
    ).toBe('NetworkError: The control plane is unreachable.')
    // A crash's own words may hold anything: nothing to copy.
    expect(errorDetail(new Error('private render detail'))).toBeUndefined()
    expect(errorDetail('a thrown string')).toBeUndefined()
  })
})
