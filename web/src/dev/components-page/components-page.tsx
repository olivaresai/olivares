// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE COMPONENTS PAGE: a capture-only page that shows the page primitives and the eight states
// the way the design's state set shows them, so the hosted vehicle can capture them at
// 1440, 1280 and 390 in both themes and set them beside the mockup. It is not a product
// route: the registry, the router and the route census never see it, and the product build
// does not include it (its own HTML entry, built only by the vehicle).
import {
  Ban,
  CircleHelp,
  CircleX,
  Clock,
  Filter,
  Lock,
  Plus,
  Rows3,
} from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import { CodeLine } from '@/components/ui/code-line'
import { DisabledReason } from '@/components/ui/disabled-reason'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Kbd } from '@/components/ui/kbd'
import { MonoMark } from '@/components/ui/mono-mark'
import { Segmented } from '@/components/ui/segmented'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { StateBlock } from '@/components/ui/state-block'
import { StatusGlyph, type Status } from '@/components/ui/status-glyph'
import { Switch } from '@/components/ui/switch'
import { Tag } from '@/components/ui/tag'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { RunningRing } from '@/components/ui/status-glyph'
import type { DemoText } from './demo-text'

export type PageView = 'states' | 'primitives'

const noop = () => undefined

function Card({
  id,
  icon,
  name,
  code,
  children,
}: {
  id: string
  icon: ReactNode
  name: string
  code: string
  children: ReactNode
}) {
  return (
    <section
      aria-labelledby={id}
      className="flex min-h-0 min-w-0 flex-col overflow-hidden rounded-card border border-line bg-surface shadow-[var(--shadow-card)]"
    >
      <h2
        id={id}
        className="m-0 flex items-center gap-2 border-b border-line px-3.5 py-2.5 text-caption font-semibold text-text-2"
      >
        <span aria-hidden="true" className="flex [&_svg]:size-3.5">
          {icon}
        </span>
        <span className="min-w-0">{name}</span>
        <span
          aria-hidden="true"
          className="ms-auto font-mono text-mono-s font-normal text-text-3"
        >
          {code}
        </span>
      </h2>
      <div className="flex min-h-0 flex-1 flex-col">{children}</div>
    </section>
  )
}

const STALE_GLYPHS: Status[] = ['working', 'needs-you', 'done']

function StatesBoard({ d }: { d: DemoText }) {
  return (
    <div className="grid grid-cols-1 gap-3 min-[761px]:grid-cols-2 min-[1101px]:grid-cols-4">
      <Card
        id="s1"
        icon={<RunningRing />}
        name={d.names.loading}
        code="loading"
      >
        <StateBlock state="loading" label={d.loadingLabel} />
      </Card>
      <Card id="s2" icon={<Rows3 />} name={d.names.empty} code="empty">
        <StateBlock
          state="empty"
          className="flex-1"
          title={d.emptyTitle}
          description={d.emptyText}
          command="olivares agent session create"
          action={
            <Button variant="primary" size="sm" onClick={noop}>
              <Plus aria-hidden="true" />
              {d.newSession}
            </Button>
          }
        />
      </Card>
      <Card id="s3" icon={<Filter />} name={d.names.filtered} code="filtered">
        <StateBlock
          state="filtered"
          className="flex-1"
          title={d.filteredTitle}
          filters={d.filters}
          hiddenCount={12}
          onClear={noop}
        />
      </Card>
      <Card
        id="s4"
        icon={<CircleX className="text-bad" />}
        name={d.names.error}
        code="error"
      >
        <StateBlock
          state="error"
          request="GET /v1/sessions · timeout after 10 s"
          requestId="01J8ZK4Q7M"
          time="18:06:41 UTC"
          onRetry={noop}
          onOpenDiagnostics={noop}
        />
      </Card>
      <Card
        id="s5"
        icon={<Clock className="text-warn" />}
        name={d.names.stale}
        code="stale"
      >
        <StateBlock
          state="stale"
          lastGood="18:04 UTC"
          failedAt="18:06"
          onRetry={noop}
        >
          <ul className="m-0 flex list-none flex-col gap-3 p-0">
            {d.staleRows.map((row, i) => (
              <li
                key={row.title}
                className="grid grid-cols-[14px_minmax(0,1fr)_auto] items-center gap-2.5 text-caption"
              >
                <StatusGlyph status={STALE_GLYPHS[i]} showLabel={false} />
                <span className="text-text">{row.title}</span>
                <span className="text-text-3 tabular-nums">{row.time}</span>
              </li>
            ))}
          </ul>
        </StateBlock>
      </Card>
      <Card id="s6" icon={<Lock />} name={d.names.disabled} code="disabled">
        <StateBlock
          state="disabled"
          reason={d.deployReason}
          action={<a href="#review-plan">{d.reviewPlan}</a>}
        >
          <Button size="sm" onClick={noop}>
            {d.deploy}
          </Button>
        </StateBlock>
        <hr className="mx-4 my-0 border-0 border-t border-line" />
        <StateBlock
          state="disabled"
          reason={d.signedOut}
          action={<a href="#sign-in">{d.signIn}</a>}
        >
          <Button size="sm" onClick={noop}>
            {d.startSession}
          </Button>
        </StateBlock>
      </Card>
      <Card id="s7" icon={<Ban />} name={d.names.noAccess} code="no-access">
        <StateBlock
          state="no-access"
          className="flex-1"
          title={d.noAccessTitle}
          action={
            <Button size="sm" onClick={noop}>
              {d.askOwner}
            </Button>
          }
        />
      </Card>
      <Card
        id="s8"
        icon={<CircleHelp className="text-info" />}
        name={d.names.unknown}
        code="unknown-outcome"
      >
        <StateBlock
          state="unknown-outcome"
          requestId="01J8ZK9V2C"
          elapsed="8 s"
          confirmedNothingStarted={false}
          retryLabel={d.startAgain}
          onRetry={noop}
        />
      </Card>
    </div>
  )
}

function Section({
  id,
  title,
  children,
}: {
  id: string
  title: string
  children: ReactNode
}) {
  return (
    <section
      aria-labelledby={id}
      className="flex min-w-0 flex-col gap-3 rounded-card border border-line bg-surface p-4 shadow-[var(--shadow-card)]"
    >
      <h2 id={id} className="m-0 text-heading text-text">
        {title}
      </h2>
      {children}
    </section>
  )
}

const GLYPHS: Status[] = [
  'working',
  'needs-you',
  'done',
  'failed',
  'paused',
  'idle',
]

function PrimitivesBoard({ d }: { d: DemoText }) {
  const [segment, setSegment] = useState('0')
  const [region, setRegion] = useState('0')
  return (
    <div className="grid grid-cols-1 gap-3 min-[761px]:grid-cols-2 min-[1101px]:grid-cols-3">
      <Section id="p1" title={d.sections.buttons}>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="primary" onClick={noop}>
            {d.buttons.primary}
            <Kbd className="border-[rgba(26,18,6,.25)] bg-[rgba(26,18,6,.12)] text-on-accent">
              N
            </Kbd>
          </Button>
          <Button onClick={noop}>{d.buttons.secondary}</Button>
          <Button variant="ghost" onClick={noop}>
            {d.buttons.quiet}
          </Button>
          <Button variant="destructive" onClick={noop}>
            {d.buttons.danger}
          </Button>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" onClick={noop}>
            {d.buttons.small}
          </Button>
          <Button size="lg" variant="primary" onClick={noop}>
            {d.buttons.large}
          </Button>
        </div>
        <DisabledReason
          disabled
          reason={d.deployReason}
          action={<a href="#review-plan">{d.reviewPlan}</a>}
        >
          <Button variant="primary" onClick={noop}>
            {d.deploy}
          </Button>
        </DisabledReason>
      </Section>

      <Section id="p2" title={d.sections.fields}>
        <Field label={d.fields.host} description={d.fields.hostHint}>
          <Input mono defaultValue="obs-01.telescopes.lan" />
        </Field>
        <Field label={d.fields.region}>
          <Select value={region} onValueChange={setRegion}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {d.fields.regions.map((r, i) => (
                <SelectItem key={r} value={String(i)}>
                  {r}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      </Section>

      <Section id="p3" title={d.sections.choices}>
        <label className="flex items-center justify-between gap-3 text-body text-text">
          <span className="min-w-0">{d.fields.notify}</span>
          <Switch defaultChecked />
        </label>
        <label className="flex items-center justify-between gap-3 text-body text-text">
          <span className="min-w-0">{d.fields.reduceMotion}</span>
          <Switch />
        </label>
        <Segmented
          aria-label={d.sections.choices}
          options={d.segments.map((label, i) =>
            i === 2
              ? {
                  value: String(i),
                  label,
                  disabled: true as const,
                  reason: d.segmentReason,
                }
              : { value: String(i), label },
          )}
          value={segment}
          onValueChange={setSegment}
        />
      </Section>

      <Section id="p4" title={d.sections.tabs}>
        <Tabs defaultValue="0">
          <TabsList aria-label={d.sections.tabs}>
            {d.tabs.map((tab, i) => (
              <TabsTrigger key={tab.label} value={String(i)}>
                {tab.label}
                {tab.count ? (
                  <span className="text-overline text-text-3 tabular-nums">
                    {tab.count}
                  </span>
                ) : null}
              </TabsTrigger>
            ))}
          </TabsList>
          {d.tabs.map((tab, i) => (
            <TabsContent
              key={tab.label}
              value={String(i)}
              className="text-caption text-text-2"
            >
              {d.tabPanel}
            </TabsContent>
          ))}
        </Tabs>
      </Section>

      <Section id="p5" title={d.sections.tags}>
        <div className="flex flex-wrap items-center gap-2">
          <Tag tone="ok">{d.tags.ready}</Tag>
          <Tag tone="warn">{d.tags.signedOut}</Tag>
          <Tag tone="info">{d.tags.update}</Tag>
          <Tag tone="bad">{d.tags.cannotCheck}</Tag>
          <Tag>{d.tags.notInstalled}</Tag>
          <Tag tone="accent">{d.tags.scope}</Tag>
          <Tag mono>v2.1.4</Tag>
        </div>
      </Section>

      <Section id="p6" title={d.sections.glyphs}>
        <ul className="m-0 grid list-none grid-cols-2 gap-x-4 gap-y-2 p-0">
          {GLYPHS.map((status) => (
            <li key={status}>
              <StatusGlyph
                status={status}
                detail={status === 'working' ? '6m 12s' : undefined}
              />
            </li>
          ))}
        </ul>
      </Section>

      <Section id="p7" title={d.sections.marks}>
        <ul className="m-0 flex list-none flex-wrap items-center gap-4 p-0">
          {d.marks.map((name, i) => (
            <li
              key={name}
              className="flex items-center gap-2 text-caption text-text"
            >
              <MonoMark
                name={name}
                size={i === 0 ? 'lg' : 'base'}
                hue={i < 4 ? ((i + 1) as 1 | 2 | 3 | 4) : undefined}
              />
              <span>{name}</span>
            </li>
          ))}
        </ul>
      </Section>

      <Section id="p8" title={d.sections.code}>
        <CodeLine command={d.command} />
      </Section>
    </div>
  )
}

export function ComponentsPage({
  d,
  view,
  onView,
}: {
  d: DemoText
  view: PageView
  onView: (view: PageView) => void
}) {
  return (
    <div className="min-h-dvh bg-frame p-0 text-body text-text min-[761px]:p-2">
      <div className="flex min-h-[calc(100dvh-1rem)] min-w-0 flex-col overflow-hidden bg-canvas min-[761px]:rounded-panel min-[761px]:border min-[761px]:border-line min-[761px]:shadow-[var(--shadow-card)]">
        <header className="flex min-h-[52px] flex-wrap items-center gap-x-3 gap-y-2 border-b border-line px-3 py-2 min-[761px]:ps-5">
          <nav
            aria-label={d.crumbLabel}
            className="min-w-0 text-body font-medium"
          >
            <ol className="m-0 flex list-none flex-wrap items-center gap-2 p-0">
              <li className="text-text-2">{d.crumbUp}</li>
              <li aria-hidden="true" className="text-text-3">
                /
              </li>
              <li aria-current="page" className="text-text">
                {d.crumbCurrent}
              </li>
            </ol>
          </nav>
          <span className="ms-auto hidden text-caption text-text-3 min-[1101px]:inline">
            {d.note}
          </span>
          <Segmented
            size="sm"
            aria-label={d.viewLabel}
            options={[
              { value: 'states', label: d.views.states },
              { value: 'primitives', label: d.views.primitives },
            ]}
            value={view}
            onValueChange={onView}
          />
        </header>
        <main className="min-w-0 flex-1 p-3 min-[761px]:px-6 min-[761px]:py-5">
          <h1 className="sr-only">{d.crumbCurrent}</h1>
          {view === 'states' ? (
            <StatesBoard d={d} />
          ) : (
            <PrimitivesBoard d={d} />
          )}
        </main>
      </div>
    </div>
  )
}

/** The page with its view in the address (`?view=`), as the entry mounts it. */
export function ComponentsPageApp({
  d,
  initialView,
}: {
  d: DemoText
  initialView: PageView
}) {
  const [view, setView] = useState<PageView>(initialView)
  const onView = (next: PageView) => {
    const address = new URL(window.location.href)
    address.searchParams.set('view', next)
    window.history.replaceState(null, '', address)
    setView(next)
  }
  return <ComponentsPage d={d} view={view} onView={onView} />
}
