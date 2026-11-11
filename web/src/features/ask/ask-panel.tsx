// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Ask Olivares, the panel (docs/design/ask-olivares-agent-v1.md). Opened by a key and by ⌘K. It shows
// the model and what Ask may do (each tool with its permission) BEFORE the first turn, then the
// conversation. The engine runs each read and the panel prints its equivalent command; a write is a
// card bound to an approval: a person approves it with their own session, then the person runs it
// once. Everything the model says is rendered as text, never as HTML, and a link in it is not followed.
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useRef, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { CodeLine } from '@/components/ui/code-line'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { useAskStore } from '@/stores/ask'
import { useModuleOn } from '@/stores/modules'
import { useTenantStore } from '@/stores/tenant'
import { askApi, askKeys, type AskMessage, type AskProposal } from './ask'
import './i18n'

/** The command an administrator runs to turn Ask on. */
const TURN_ON = 'olivares models ask bind <routing-policy-id>'
/** How often an open proposal re-reads its approval while a person decides. */
const APPROVAL_POLL_MS = 5000

type Entry = { id: number } & (
  | { kind: 'user'; text: string }
  | { kind: 'step'; command: string }
  | { kind: 'answer'; text: string }
  | { kind: 'refused'; tool: string }
  | { kind: 'limit' }
  | { kind: 'error'; message: string }
  | { kind: 'proposal'; proposal: AskProposal }
)

type NewEntry = Entry extends infer E
  ? E extends { id: number }
    ? Omit<E, 'id'>
    : never
  : never

function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : ''
}

export function AskPanel() {
  const open = useAskStore((s) => s.open)
  const setOpen = useAskStore((s) => s.setOpen)
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent className="max-w-2xl" data-slot="ask-panel">
        {open ? <AskBody /> : null}
      </DialogContent>
    </Dialog>
  )
}

function AskBody() {
  const { t } = useTranslation('ask')
  // Ask is the models module's: off there, its view is not read and the panel says so.
  const modelsOn = useModuleOn('models')
  const activeTenant = useTenantStore((s) => s.activeTenant)
  const view = useQuery({
    queryKey: askKeys.view(activeTenant),
    queryFn: () => askApi.view(),
    enabled: modelsOn,
  })
  const [chosen, setChosen] = useState<string | null>(null)
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [entries, setEntries] = useState<Entry[]>([])
  const [messages, setMessages] = useState<AskMessage[]>([])
  const next = useRef(0)

  const model = chosen ?? view.data?.model ?? ''
  const push = (...added: NewEntry[]) =>
    setEntries((all) => [
      ...all,
      ...added.map((e) => ({ ...e, id: next.current++ }) as Entry),
    ])

  async function submit(event: FormEvent) {
    event.preventDefault()
    const question = text.trim()
    if (!question || !model || busy) return
    setText('')
    setBusy(true)
    push({ kind: 'user', text: question })
    const convo: AskMessage[] = [
      ...messages,
      { role: 'user', content: question },
    ]
    try {
      const reply = await askApi.turn(model, convo)
      push(
        ...reply.steps.map((s) => ({
          kind: 'step' as const,
          command: s.command,
        })),
      )
      if (reply.kind === 'answer') {
        push({ kind: 'answer', text: reply.text ?? '' })
        setMessages([
          ...convo,
          { role: 'assistant', content: reply.text ?? '' },
        ])
      } else if (reply.kind === 'refused') {
        push({ kind: 'refused', tool: reply.tool ?? '' })
      } else if (reply.kind === 'limit') {
        push({ kind: 'limit' })
      } else if (reply.proposal) {
        push({ kind: 'proposal', proposal: reply.proposal })
      }
    } catch (e) {
      push({ kind: 'error', message: messageOf(e) })
    } finally {
      setBusy(false)
    }
  }

  const header = (
    <DialogHeader>
      <DialogTitle>{t('title')}</DialogTitle>
      <DialogDescription>{t('description')}</DialogDescription>
    </DialogHeader>
  )
  if (!modelsOn || (view.data && !view.data.enabled)) {
    return (
      <>
        {header}
        <p className="text-body text-text-2" data-slot="ask-off">
          {t('off')}
        </p>
        <CodeLine command={TURN_ON} label={t('command')} />
      </>
    )
  }

  return (
    <>
      {header}
      <div className="flex min-w-0 flex-col gap-1.5">
        <label
          htmlFor="ask-model"
          className="text-caption font-medium text-text-2"
        >
          {t('model')}
        </label>
        <select
          id="ask-model"
          value={model}
          onChange={(e) => setChosen(e.target.value)}
          className="h-8 w-full rounded-ctl border border-ctl-border bg-canvas px-2 text-body text-text outline-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus"
        >
          {(view.data?.models ?? []).map((m) => (
            <option key={m.id} value={m.id}>
              {m.name}
            </option>
          ))}
        </select>
      </div>

      <section
        aria-labelledby="ask-tools"
        className="flex min-w-0 flex-col gap-1.5"
      >
        <h3 id="ask-tools" className="text-caption font-medium text-text-2">
          {t('tools')}
        </h3>
        <ul className="flex flex-col gap-1" data-slot="ask-tools">
          {(view.data?.tools ?? []).map((tool) => (
            <li
              key={tool.name}
              data-tool={tool.name}
              data-allowed={tool.allowed}
              className={
                tool.allowed
                  ? 'flex flex-wrap items-baseline gap-x-2 text-body text-text'
                  : 'flex flex-wrap items-baseline gap-x-2 text-body text-text-3'
              }
            >
              <span className="font-medium">{tool.summary}</span>
              <span className="text-caption">
                {tool.kind === 'write' ? t('toolWrite') : t('toolRead')}
              </span>
              <code className="font-mono text-caption">
                {tool.allowed
                  ? t('toolNeeds', { permission: tool.permission })
                  : t('toolMissing', { permission: tool.permission })}
              </code>
            </li>
          ))}
        </ul>
      </section>

      <div
        role="log"
        aria-live="polite"
        className="flex max-h-[40svh] min-w-0 flex-col gap-3 overflow-y-auto"
        data-slot="ask-log"
      >
        {entries.map((entry) => (
          <EntryView key={entry.id} entry={entry} />
        ))}
        {busy ? (
          <p className="text-caption text-text-2">{t('working')}</p>
        ) : null}
      </div>

      <form onSubmit={submit} className="flex min-w-0 items-end gap-2">
        <div className="flex min-w-0 flex-1 flex-col gap-1.5">
          <label
            htmlFor="ask-input"
            className="text-caption font-medium text-text-2"
          >
            {t('input')}
          </label>
          <Input
            id="ask-input"
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder={t('placeholder')}
            autoComplete="off"
          />
        </div>
        <Button
          type="submit"
          variant="primary"
          disabled={busy || !model || !text.trim()}
        >
          {t('send')}
        </Button>
      </form>
    </>
  )
}

function EntryView({ entry }: { entry: Entry }) {
  const { t } = useTranslation('ask')
  switch (entry.kind) {
    case 'user':
      return (
        <div data-entry="user" className="flex min-w-0 flex-col">
          <span className="text-caption font-medium text-text-2">
            {t('you')}
          </span>
          <p className="text-body break-words whitespace-pre-wrap text-text">
            {entry.text}
          </p>
        </div>
      )
    case 'answer':
      return (
        <div data-entry="answer" className="flex min-w-0 flex-col">
          <span className="text-caption font-medium text-text-2">
            {t('answer')}
          </span>
          <p className="text-body break-words whitespace-pre-wrap text-text">
            {entry.text}
          </p>
        </div>
      )
    case 'step':
      return (
        <p
          data-entry="step"
          className="min-w-0 font-mono text-caption break-words text-text-2"
        >
          = {entry.command}
        </p>
      )
    case 'refused':
      return (
        <p data-entry="refused" role="alert" className="text-body text-bad">
          {t('refused', { tool: entry.tool })}
        </p>
      )
    case 'limit':
      return (
        <p data-entry="limit" className="text-body text-text-2">
          {t('limit')}
        </p>
      )
    case 'error':
      return (
        <p data-entry="error" role="alert" className="text-body text-bad">
          {t('error', { message: entry.message })}
        </p>
      )
    case 'proposal':
      return <ProposalCard proposal={entry.proposal} />
  }
}

type CardState = 'open' | 'busy' | 'done' | 'dismissed'

/** A proposed write: the exact command, the approval it waits on, and the one next step. */
function ProposalCard({ proposal }: { proposal: AskProposal }) {
  const { t } = useTranslation('ask')
  const queryClient = useQueryClient()
  const [state, setState] = useState<CardState>('open')
  const [failure, setFailure] = useState('')
  const activeTenant = useTenantStore((s) => s.activeTenant)
  const approval = useQuery({
    queryKey: askKeys.approval(activeTenant, proposal.approval_id),
    queryFn: ({ signal }) => askApi.approval(proposal.approval_id, signal),
    enabled: state === 'open',
    refetchInterval: (q) =>
      q.state.data?.status === 'pending' ? APPROVAL_POLL_MS : false,
  })
  const status = approval.data?.status ?? proposal.status

  async function act(step: () => Promise<unknown>, after: CardState) {
    setState('busy')
    setFailure('')
    try {
      await step()
      setState(after)
    } catch (e) {
      setState('open')
      setFailure(messageOf(e))
    }
    await queryClient.invalidateQueries({
      queryKey: askKeys.approval(activeTenant, proposal.approval_id),
    })
  }

  return (
    <section
      data-entry="proposal"
      data-state={state}
      data-status={status}
      aria-label={t('proposal')}
      className="flex min-w-0 flex-col gap-2 rounded-ctl border border-ctl-border p-3"
    >
      <h4 className="text-body font-semibold text-text">{t('proposal')}</h4>
      <CodeLine command={proposal.command} label={t('command')} />
      <p className="text-caption text-text-2">
        <code className="font-mono">
          {t('toolNeeds', { permission: proposal.permission })}
        </code>
        {' · '}
        {t('proposalHint')}
      </p>
      {failure ? (
        <p role="alert" className="text-body text-bad">
          {t('error', { message: failure })}
        </p>
      ) : null}
      {state === 'done' ? (
        <p className="text-body text-text">{t('done')}</p>
      ) : state === 'dismissed' ? null : (
        <>
          {status === 'pending' && !approval.data?.may_decide ? (
            <p className="text-body text-text-2">{t('waiting')}</p>
          ) : null}
          {status !== 'pending' && status !== 'approved' ? (
            <p className="text-body text-text-2">{t('closed', { status })}</p>
          ) : null}
          <div className="flex gap-2">
            {status === 'pending' && approval.data?.may_decide ? (
              <Button
                variant="primary"
                disabled={state === 'busy'}
                onClick={() =>
                  act(() => askApi.approve(proposal.approval_id), 'open')
                }
              >
                {t('approve')}
              </Button>
            ) : null}
            {status === 'approved' ? (
              <Button
                variant="primary"
                disabled={state === 'busy'}
                onClick={() => act(() => askApi.run(proposal), 'done')}
              >
                {t('run')}
              </Button>
            ) : null}
            <Button
              variant="ghost"
              disabled={state === 'busy'}
              onClick={() => setState('dismissed')}
            >
              {t('dismiss')}
            </Button>
          </div>
        </>
      )}
    </section>
  )
}
