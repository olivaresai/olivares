// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Plus, Trash2 } from 'lucide-react'
import { useEffect, useId, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { KvList, KvRow } from '@/components/ui/kv'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import { AuthorityLostError } from '@/features/agentops/auth-boundary'
import {
  communicationsKeys,
  createChannel,
  type ChannelCreateOutcome,
} from './api'
import type { CommunicationsScope } from './boundary'
import { Mono } from './content-blocks'
import { classifyFailure, type Failure } from './errors'
import { FailureNotice } from './failure-notice'
import { buildCreateIntent, useIntentGuard, type CreateIntent } from './intent'
import { SubjectPicker, type Me, type PickedSubject } from './subject-picker'
import {
  ACK_POLICIES,
  CHANNEL_KINDS,
  CHANNEL_SENSITIVITIES,
  CONTENT_PROTECTIONS,
  GRANTS_MAX,
  WAKE_POLICIES,
  type ChannelCreateInput,
  type ChannelGrantInput,
} from './types'

const SLUG = /^[a-z0-9][a-z0-9-]*$/

interface GrantRow {
  id: string
  subject: PickedSubject
  read: boolean
  write: boolean
  admin: boolean
  expiresAt: string
}

let grantSeq = 0
const newGrant = (subject: PickedSubject): GrantRow => ({
  id: `g${++grantSeq}`,
  subject,
  read: false,
  write: false,
  admin: false,
  expiresAt: '',
})

/** `datetime-local` value → RFC 3339 in UTC, or null when unreadable. */
export function localToIso(value: string): string | null {
  if (!value) return null
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? null : d.toISOString()
}

type Phase = 'edit' | 'submitting' | 'done' | 'unknown' | 'refused'

/**
 * ChannelCreateForm — every field of `POST /channels`, and INITIAL GRANTS THAT ARE
 * EXPLICIT. The operator adds each subject and ticks each of the three bits; "add
 * my account" appends a row for the acting user with every bit UNTICKED, so a
 * self-grant is always a visible decision. The receipt shows what was recorded now
 * and says what it is not: a durable list of who may read the channel later.
 *
 * The route has no idempotency key, so a transport failure is an UNKNOWN outcome:
 * the form does not retry, and it asks the operator to look at the catalog for the
 * slug before creating again.
 */
export function ChannelCreateForm({
  scope,
  canChannelRead,
  canChannelWrite,
  canUserRead,
  canAgentRead,
  me,
  onCheckCatalog,
}: {
  scope: CommunicationsScope
  canChannelRead: boolean
  canChannelWrite: boolean
  canUserRead: boolean
  canAgentRead: boolean
  me: Me
  /** Offered after an unknown outcome when the catalog may be read. */
  onCheckCatalog?: () => void
}) {
  const { t } = useTranslation('communications')
  const queryClient = useQueryClient()
  const idp = useId()
  const workspace = scope.workspace ?? ''

  const [slug, setSlug] = useState('')
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [kind, setKind] = useState<string>('coordination')
  const [sensitivity, setSensitivity] = useState<string>('internal')
  const [protection, setProtection] = useState<string>('application_sealed')
  const [ackPolicy, setAckPolicy] = useState<string>('none')
  const [ackTimeout, setAckTimeout] = useState('0')
  const [wake, setWake] = useState<string>('none')
  const [retention, setRetention] = useState('')
  const [maxFanout, setMaxFanout] = useState('1')
  const [maxDepth, setMaxDepth] = useState('0')
  const [grants, setGrants] = useState<GrantRow[]>([])
  const [errors, setErrors] = useState<string[]>([])
  const [phase, setPhase] = useState<Phase>('edit')
  const [outcome, setOutcome] = useState<ChannelCreateOutcome | null>(null)
  const [failure, setFailure] = useState<Failure | null>(null)
  const [lostCount, setLostCount] = useState(0)

  const guard = useIntentGuard({
    allowed: canChannelWrite,
    boundary: scope.key,
    permission: 'sessions:channel:write',
  })

  // Permission gone while a submission was pending: the act is over before this
  // render paints (adjust-during-render), and the request itself was aborted by the
  // guard's effect.
  if (!canChannelWrite && phase === 'submitting') {
    setPhase('edit')
    setLostCount((n) => n + 1)
  }
  useEffect(() => {
    if (lostCount > 0) toast.warning(t('authority.lost'))
  }, [lostCount, t])

  const create = useMutation<ChannelCreateOutcome, unknown, CreateIntent>({
    mutationFn: (i) => {
      const signal = guard.begin()
      if (!signal) throw new AuthorityLostError()
      return createChannel(
        i,
        { tenant: i.scope.tenant, guard: guard.check },
        signal,
      )
    },
    onSuccess: (result) => {
      setOutcome(result)
      setPhase('done')
      void queryClient.invalidateQueries({
        queryKey: communicationsKeys.workspaceScope(
          scope.tenant,
          scope.epoch,
          workspace,
        ),
      })
    },
    onError: (err) => {
      if (err instanceof AuthorityLostError) {
        setPhase('edit')
        setLostCount((n) => n + 1)
        return
      }
      const f = classifyFailure(err)
      if (f.kind === 'aborted') {
        setPhase('edit')
        return
      }
      setFailure(f)
      setPhase(f.kind === 'ambiguous' ? 'unknown' : 'refused')
    },
  })

  const validate = (): {
    body: ChannelCreateInput
    slugValue: string
  } | null => {
    const problems: string[] = []
    const slugValue = slug.trim()
    if (!SLUG.test(slugValue)) problems.push(t('create.validation.slug'))
    if (name.trim() === '') problems.push(t('create.validation.name'))
    if (grants.length === 0) problems.push(t('create.validation.grants'))
    if (grants.some((g) => g.subject.ref.trim() === ''))
      problems.push(t('create.validation.grantRef'))
    if (grants.some((g) => !g.read && !g.write && !g.admin))
      problems.push(t('create.validation.grantBits'))
    const whole = (v: string) => /^\d+$/.test(v.trim())
    if (!whole(ackTimeout) || !whole(maxDepth))
      problems.push(t('create.validation.number'))
    if (!whole(maxFanout) || Number(maxFanout) < 1)
      problems.push(t('create.validation.fanout'))
    setErrors(problems)
    if (problems.length > 0) return null
    const initial_grants: ChannelGrantInput[] = grants.map((g) => {
      const grant: ChannelGrantInput = {
        subject: { kind: g.subject.kind, ref: g.subject.ref.trim() },
        can_read: g.read,
        can_write: g.write,
        can_admin: g.admin,
      }
      const iso = localToIso(g.expiresAt)
      if (iso) grant.expires_at = iso
      return grant
    })
    const body: ChannelCreateInput = {
      workspace_id: workspace,
      slug: slugValue,
      name: name.trim(),
      kind: kind as ChannelCreateInput['kind'],
      sensitivity: sensitivity as ChannelCreateInput['sensitivity'],
      content_protection:
        protection as ChannelCreateInput['content_protection'],
      default_ack_policy: ackPolicy as ChannelCreateInput['default_ack_policy'],
      default_ack_timeout_ms: Number(ackTimeout),
      default_wake: wake as ChannelCreateInput['default_wake'],
      max_fanout: Number(maxFanout),
      max_automation_depth: Number(maxDepth),
      initial_grants,
    }
    if (description.trim() !== '') body.description = description.trim()
    if (retention.trim() !== '') body.retention_policy_ref = retention.trim()
    return { body, slugValue }
  }

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (phase === 'submitting' || !canChannelWrite || !workspace) return
    const v = validate()
    if (!v) return
    setFailure(null)
    setOutcome(null)
    setPhase('submitting')
    // The authority is captured NOW, at the operator's act, and frozen into the
    // intention the transport guard checks before each fetch.
    create.mutate(
      buildCreateIntent(
        { tenant: scope.tenant, workspace, boundary: scope.key },
        v.body,
      ),
    )
  }

  const resetAll = () => {
    setPhase('edit')
    setOutcome(null)
    setFailure(null)
  }

  const updateGrant = (id: string, patch: Partial<GrantRow>) =>
    setGrants((gs) => gs.map((g) => (g.id === id ? { ...g, ...patch } : g)))

  const selectField = (
    id: string,
    label: string,
    value: string,
    onChange: (v: string) => void,
    options: readonly string[],
    labelOf: (v: string) => string,
    description?: string,
  ) => (
    <Field label={label} htmlFor={id} description={description}>
      <Select
        value={value}
        onValueChange={onChange}
        disabled={phase !== 'edit'}
      >
        <SelectTrigger id={id} aria-label={label}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {options.map((o) => (
            <SelectItem key={o} value={o}>
              {labelOf(o)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </Field>
  )

  if (phase === 'done' && outcome) {
    const { result } = outcome
    return (
      <div className="flex flex-col gap-4" data-slot="channel-created">
        <div
          role="status"
          className="rounded-md border border-success-line bg-success-soft px-3 py-2 text-body text-success"
        >
          <p className="font-medium">{t('create.result.title')}</p>
          <p>{t('create.result.body')}</p>
        </div>
        <KvList>
          <KvRow label={t('channel.fields.id')} mono>
            {result.channel.id}
          </KvRow>
          <KvRow label={t('channel.fields.slug')} mono>
            {result.channel.slug}
          </KvRow>
          <KvRow label={t('channel.fields.name')}>{result.channel.name}</KvRow>
          <KvRow label={t('channel.fields.state')} mono>
            {result.channel.state}
          </KvRow>
          <KvRow label={t('channel.fields.version')} mono>
            {result.channel.version}
          </KvRow>
          <KvRow label={t('create.result.etag')} mono>
            {outcome.etag ?? result.etag}
          </KvRow>
          <KvRow label={t('create.result.auditSeq')} mono>
            {result.audit_seq}
          </KvRow>
        </KvList>
        <section aria-label={t('create.result.grants')}>
          <p className="mb-1 text-body font-medium">
            {t('create.result.grants')}
          </p>
          {result.grants && result.grants.length > 0 ? (
            <ul className="flex flex-col gap-1">
              {result.grants.map((g) => (
                <li
                  key={g.id}
                  className="flex flex-wrap items-center gap-1.5 text-caption"
                  data-slot="grant-recorded"
                >
                  <Badge variant="outline">
                    {t(`subject.kinds.${g.subject.kind}`)}
                  </Badge>
                  <Mono>{g.subject.ref}</Mono>
                  {g.can_read ? (
                    <Badge variant="info">{t('access.read')}</Badge>
                  ) : null}
                  {g.can_write ? (
                    <Badge variant="info">{t('access.write')}</Badge>
                  ) : null}
                  {g.can_admin ? (
                    <Badge variant="info">{t('access.admin')}</Badge>
                  ) : null}
                  {g.expires_at ? (
                    <span className="text-muted-foreground">
                      {t('create.grant.expiresAt')}: <Mono>{g.expires_at}</Mono>
                    </span>
                  ) : null}
                  <Mono>gen {g.generation}</Mono>
                </li>
              ))}
            </ul>
          ) : (
            <p className="text-caption text-muted-foreground">
              {t('create.result.noGrants')}
            </p>
          )}
        </section>
        {!canChannelRead ? (
          <p className="text-caption text-muted-foreground">
            {t('create.noReadHint')}
          </p>
        ) : null}
        <div>
          <Button type="button" variant="outline" size="sm" onClick={resetAll}>
            {t('actions.newChannel')}
          </Button>
        </div>
      </div>
    )
  }

  return (
    <form onSubmit={onSubmit} className="flex flex-col gap-4" noValidate>
      {!canChannelRead ? (
        <p className="text-caption text-muted-foreground">
          {t('create.noReadHint')}
        </p>
      ) : null}
      {phase === 'unknown' && failure ? (
        <div className="flex flex-col gap-2" data-slot="create-unknown">
          <FailureNotice failure={failure} title={t('create.unknown.title')} />
          <p className="text-body text-muted-foreground">
            {t('create.unknown.body', { slug: slug.trim() })}
          </p>
          <div className="flex flex-wrap gap-2">
            {canChannelRead && onCheckCatalog ? (
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={onCheckCatalog}
              >
                {t('actions.checkCatalog')}
              </Button>
            ) : null}
            <Button type="button" variant="ghost" size="sm" onClick={resetAll}>
              {t('actions.back')}
            </Button>
          </div>
        </div>
      ) : null}
      {phase === 'refused' && failure ? (
        <FailureNotice failure={failure} />
      ) : null}
      {errors.length > 0 ? (
        <ul role="alert" className="list-disc pl-5 text-body text-danger">
          {errors.map((e) => (
            <li key={e}>{e}</li>
          ))}
        </ul>
      ) : null}
      <div className="grid gap-3 sm:grid-cols-2">
        <Field
          label={t('create.fields.slug')}
          htmlFor={`${idp}-slug`}
          description={t('create.fields.slugHint')}
          required
        >
          <Input
            id={`${idp}-slug`}
            value={slug}
            onChange={(e) => setSlug(e.target.value)}
            autoComplete="off"
            mono
            disabled={phase !== 'edit'}
          />
        </Field>
        <Field label={t('create.fields.name')} htmlFor={`${idp}-name`} required>
          <Input
            id={`${idp}-name`}
            value={name}
            onChange={(e) => setName(e.target.value)}
            autoComplete="off"
            disabled={phase !== 'edit'}
          />
        </Field>
      </div>
      <Field
        label={t('create.fields.descriptionField')}
        htmlFor={`${idp}-description`}
      >
        <Textarea
          id={`${idp}-description`}
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          rows={2}
          disabled={phase !== 'edit'}
        />
      </Field>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {selectField(
          `${idp}-kind`,
          t('create.fields.kind'),
          kind,
          setKind,
          CHANNEL_KINDS,
          (v) => t(`kind.${v}`),
        )}
        {selectField(
          `${idp}-sensitivity`,
          t('create.fields.sensitivity'),
          sensitivity,
          setSensitivity,
          CHANNEL_SENSITIVITIES,
          (v) => t(`sensitivity.${v}`),
        )}
        {selectField(
          `${idp}-protection`,
          t('create.fields.protection'),
          protection,
          setProtection,
          CONTENT_PROTECTIONS,
          (v) => t(`protection.${v}`),
          t('create.fields.protectionHint'),
        )}
        {selectField(
          `${idp}-ack`,
          t('create.fields.ackPolicy'),
          ackPolicy,
          setAckPolicy,
          ACK_POLICIES,
          (v) => t(`ackPolicy.${v}`),
        )}
        <Field
          label={t('create.fields.ackTimeout')}
          htmlFor={`${idp}-ack-timeout`}
        >
          <Input
            id={`${idp}-ack-timeout`}
            value={ackTimeout}
            onChange={(e) => setAckTimeout(e.target.value)}
            inputMode="numeric"
            mono
            disabled={phase !== 'edit'}
          />
        </Field>
        {selectField(
          `${idp}-wake`,
          t('create.fields.wake'),
          wake,
          setWake,
          WAKE_POLICIES,
          (v) => t(`wake.${v}`),
        )}
        <Field
          label={t('create.fields.retention')}
          htmlFor={`${idp}-retention`}
        >
          <Input
            id={`${idp}-retention`}
            value={retention}
            onChange={(e) => setRetention(e.target.value)}
            autoComplete="off"
            mono
            disabled={phase !== 'edit'}
          />
        </Field>
        <Field label={t('create.fields.maxFanout')} htmlFor={`${idp}-fanout`}>
          <Input
            id={`${idp}-fanout`}
            value={maxFanout}
            onChange={(e) => setMaxFanout(e.target.value)}
            inputMode="numeric"
            mono
            disabled={phase !== 'edit'}
          />
        </Field>
        <Field
          label={t('create.fields.maxAutomationDepth')}
          htmlFor={`${idp}-depth`}
        >
          <Input
            id={`${idp}-depth`}
            value={maxDepth}
            onChange={(e) => setMaxDepth(e.target.value)}
            inputMode="numeric"
            mono
            disabled={phase !== 'edit'}
          />
        </Field>
      </div>

      <fieldset className="flex flex-col gap-3 rounded-md border border-border p-3">
        <legend className="px-1 text-body font-medium">
          {t('create.fields.grants')}
        </legend>
        <p className="text-caption text-muted-foreground">
          {t('create.fields.grantsHint')}
        </p>
        {grants.map((g, i) => (
          <div
            key={g.id}
            className="flex flex-col gap-3 rounded-md border border-border bg-surface p-3"
            data-slot="grant-row"
          >
            <SubjectPicker
              mode="grant"
              value={g.subject}
              onChange={(subject) => updateGrant(g.id, { subject })}
              scope={scope}
              canUserRead={canUserRead}
              canAgentRead={canAgentRead}
              me={me}
              idPrefix={`${idp}-grant-${g.id}`}
              disabled={phase !== 'edit'}
            />
            <div className="flex flex-wrap items-center gap-4">
              {(['read', 'write', 'admin'] as const).map((bit) => (
                <div key={bit} className="flex items-center gap-2">
                  <Checkbox
                    id={`${idp}-grant-${g.id}-${bit}`}
                    checked={g[bit]}
                    onCheckedChange={(c) =>
                      updateGrant(g.id, { [bit]: c === true })
                    }
                    disabled={phase !== 'edit'}
                  />
                  <Label htmlFor={`${idp}-grant-${g.id}-${bit}`}>
                    {t(`create.grant.${bit}`)}
                  </Label>
                </div>
              ))}
              <Field
                label={t('create.grant.expiresAt')}
                htmlFor={`${idp}-grant-${g.id}-expires`}
                description={t('create.grant.expiresHint')}
                className="min-w-56"
              >
                <Input
                  id={`${idp}-grant-${g.id}-expires`}
                  type="datetime-local"
                  value={g.expiresAt}
                  onChange={(e) =>
                    updateGrant(g.id, { expiresAt: e.target.value })
                  }
                  disabled={phase !== 'edit'}
                />
              </Field>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                aria-label={`${t('actions.removeGrant')} ${i + 1}`}
                onClick={() =>
                  setGrants((gs) => gs.filter((x) => x.id !== g.id))
                }
                disabled={phase !== 'edit'}
              >
                <Trash2 className="size-4" aria-hidden="true" />
                {t('actions.removeGrant')}
              </Button>
            </div>
          </div>
        ))}
        <div className="flex flex-wrap gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={phase !== 'edit' || grants.length >= GRANTS_MAX}
            onClick={() =>
              setGrants((gs) => [...gs, newGrant({ kind: 'user', ref: '' })])
            }
          >
            <Plus className="size-4" aria-hidden="true" />
            {t('actions.addGrant')}
          </Button>
          {me.userId ? (
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={phase !== 'edit' || grants.length >= GRANTS_MAX}
              onClick={() =>
                setGrants((gs) => [
                  ...gs,
                  newGrant({ kind: 'user', ref: me.userId as string }),
                ])
              }
            >
              <Plus className="size-4" aria-hidden="true" />
              {t('actions.addMe')}
            </Button>
          ) : null}
        </div>
      </fieldset>

      <div className="flex justify-end">
        <Button
          type="submit"
          variant="primary"
          disabled={phase !== 'edit' || !canChannelWrite || !workspace}
        >
          {phase === 'submitting' ? <Spinner className="size-3.5" /> : null}
          {phase === 'submitting' ? t('actions.creating') : t('actions.create')}
        </Button>
      </div>
    </form>
  )
}
