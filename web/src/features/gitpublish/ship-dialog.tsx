// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// SHIP A SESSION: commit its folder, push the commit and open the pull request, as one action.
// The engine does the three steps as the person who clicks, with the target's own credential
// (modules/gitpublish ship.go); this dialog only asks for the pull request's words and says
// how far the ship got.
//
// ⛔ ONE DECISION, ONE OPERATION ID. The id is minted when the dialog opens and again when the
//    words change, so shipping again with the same words replays what already landed and, after
//    a second person approved the push, goes on from there.
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { gitpublishApi, gitpublishKeys } from './api'
import { failureOf, newOperationId, safePullRequestUrl } from './model'
import { AuthorityNote, OutcomePanel } from './parts'
import type { PublicationTarget, ShipResult } from './types'
import './i18n'

export function ShipDialog({
  target,
  sessionRun,
  initialTitle,
  onClose,
  onOpenIntent,
}: {
  target: PublicationTarget
  /** The session run whose folder is shipped. */
  sessionRun: string
  /** The title the pull request starts with. */
  initialTitle: string
  onClose: () => void
  onOpenIntent: (id: string) => void
}) {
  const { t } = useTranslation('gitpublish')
  const { activeTenant } = useAuth()
  const [title, setTitle] = useState(initialTitle)
  const [message, setMessage] = useState('')
  const [body, setBody] = useState('')
  const [base, setBase] = useState(target.merge_bases[0] ?? '')
  const [draft, setDraft] = useState(false)
  const [operationId, setOperationId] = useState(newOperationId)
  const [result, setResult] = useState<ShipResult | null>(null)

  const request = usePrivilegedMutation<void, ShipResult>({
    mutationFn: () =>
      gitpublishApi.ship(
        target.id,
        {
          operation_id: operationId,
          session_run: sessionRun,
          // The commit message defaults to the title, as on the command line.
          message: message.trim() === '' ? title : message,
          title,
          body,
          base,
          draft,
        },
        { tenant: activeTenant },
      ),
    invalidateKeys: () => [gitpublishKeys.intents(activeTenant, target.id)],
    successMessage: (r) =>
      r.state === 'shipped'
        ? t('ship.shipped', { branch: r.branch })
        : t(
            r.state === 'awaiting_approval'
              ? 'ship.awaitingTitle'
              : 'ship.incompleteTitle',
          ),
    successIntent: (r) => (r.state === 'shipped' ? 'success' : 'warning'),
    onDone: (r) => setResult(r),
    // Refusals are said here with their code; the hook still owns the step-up ceremony.
    onError: () => true,
  })

  // A changed word is a new decision, so it gets a new operation id.
  const edit = (set: () => void) => {
    set()
    setOperationId(newOperationId())
    setResult(null)
    if (request.error) request.reset()
  }

  const failure = failureOf(request.error)
  const valid = title.trim() !== '' && base !== ''
  const done = result?.state === 'shipped'

  return (
    <Dialog open onOpenChange={(open) => (open ? undefined : onClose())}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('ship.title')}</DialogTitle>
          <DialogDescription>
            {t('ship.description', { prefix: target.push_prefix })}
          </DialogDescription>
        </DialogHeader>

        {!done ? (
          <form
            id="gitpublish-ship"
            className="space-y-3"
            onSubmit={(e) => {
              e.preventDefault()
              if (valid && !request.isPending) request.mutate()
            }}
          >
            <Field label={t('fields.title')} required>
              <Input
                value={title}
                maxLength={256}
                onChange={(e) => edit(() => setTitle(e.target.value))}
              />
            </Field>
            <Field label={t('fields.body')}>
              <Textarea
                value={body}
                maxLength={65536}
                rows={3}
                onChange={(e) => edit(() => setBody(e.target.value))}
              />
            </Field>
            <Field
              label={t('ship.message')}
              description={t('ship.messageHint')}
            >
              <Input
                value={message}
                maxLength={8192}
                onChange={(e) => edit(() => setMessage(e.target.value))}
              />
            </Field>
            {target.merge_bases.length > 1 ? (
              <Field label={t('fields.base')} required>
                <Select
                  value={base}
                  onValueChange={(v) => edit(() => setBase(v))}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {target.merge_bases.map((b) => (
                      <SelectItem key={b} value={b}>
                        {b}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            ) : null}
            <div className="flex items-center gap-2">
              <Checkbox
                id="gitpublish-ship-draft"
                checked={draft}
                onCheckedChange={(c) => edit(() => setDraft(c === true))}
              />
              <Label htmlFor="gitpublish-ship-draft">{t('fields.draft')}</Label>
            </div>
            <AuthorityNote action="push" />
          </form>
        ) : null}

        {result ? (
          <ShipOutcome result={result} onOpenIntent={onOpenIntent} />
        ) : null}
        {failure ? (
          <OutcomePanel
            outcome={failure}
            onOpenIntent={onOpenIntent}
            canReconcile={false}
          />
        ) : null}

        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t('actions.close')}
          </Button>
          {!done ? (
            <Button
              type="submit"
              form="gitpublish-ship"
              variant="primary"
              disabled={!valid || request.isPending}
            >
              {result && result.state !== 'shipped'
                ? t('ship.again')
                : t('ship.submit')}
            </Button>
          ) : null}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** How far the ship got, in the operator's terms. */
function ShipOutcome({
  result,
  onOpenIntent,
}: {
  result: ShipResult
  onOpenIntent: (id: string) => void
}) {
  const { t } = useTranslation('gitpublish')
  const url = safePullRequestUrl(result.pull_request_url)
  const number = result.pull_request?.observed.number
  if (result.state === 'shipped') {
    return (
      <section
        role="status"
        className="space-y-2 rounded-md border border-success-line bg-success-soft p-3 text-body"
      >
        <p className="text-foreground">
          {t('ship.shipped', { branch: result.branch })}
        </p>
        {url ? (
          <a
            href={url}
            target="_blank"
            rel="noreferrer noopener"
            className="font-medium text-primary underline"
          >
            {number
              ? t('ship.pullRequest', { number })
              : t('ship.pullRequestOpen')}
          </a>
        ) : number ? (
          <p className="text-foreground">{t('ship.pullRequest', { number })}</p>
        ) : null}
      </section>
    )
  }
  if (result.state === 'awaiting_approval') {
    return (
      <section
        role="status"
        className="space-y-1 rounded-md border border-warning-line bg-warning-soft p-3 text-body"
      >
        <h3 className="font-medium text-warning">{t('ship.awaitingTitle')}</h3>
        <p className="text-foreground">
          {t('ship.awaitingBody', { id: result.approval_id })}
        </p>
      </section>
    )
  }
  const push = result.push
  const pushState = push?.answer ?? push?.state
  const pushed = pushState === 'applied' || pushState === 'adopted'
  return (
    <section
      role="status"
      className="space-y-2 rounded-md border border-warning-line bg-warning-soft p-3 text-body"
    >
      <h3 className="font-medium text-warning">{t('ship.incompleteTitle')}</h3>
      <p className="text-foreground">
        {pushed
          ? t('ship.incompletePullRequest', {
              branch: result.branch,
              code: result.pull_request_error ?? '',
            })
          : t('ship.incompletePush', {
              branch: result.branch,
              state: t(`states.${pushState ?? 'uncertain'}`),
            })}
      </p>
      {push && !pushed ? (
        <Button
          size="sm"
          variant="outline"
          onClick={() => onOpenIntent(push.id)}
        >
          {t('intents.open', { id: push.id })}
        </Button>
      ) : null}
    </section>
  )
}
