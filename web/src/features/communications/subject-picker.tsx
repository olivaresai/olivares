// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { communicationsKeys, listAgents, listMembers } from './api'
import type { CommunicationsScope } from './boundary'
import {
  GRANT_SUBJECT_KINDS,
  RECIPIENT_KINDS,
  type GrantSubjectKind,
} from './types'

export interface PickedSubject {
  kind: GrantSubjectKind
  ref: string
}

/** The acting principal as a candidate subject: its canonical user id when it is a
 * user, and no reference at all otherwise — an agent or session credential is not a
 * user and is not offered as one. */
export interface Me {
  userId: string | null
  label: string
}

/**
 * SubjectPicker — one subject (a grant's) or one recipient (a notice's): a kind and
 * a CANONICAL reference. The directory reads are offered only under their own
 * permissions (`user:read` for members, `agent:read` for agents) and fill the same
 * reference field the operator can also type into; a row in the directory is a
 * candidate, not an eligible recipient of the channel — the engine decides that on
 * the act, and its refusal is shown as it comes. Agents are addressed by their
 * IDENTITY id; an agent without one is listed as not addressable, never fabricated.
 */
export function SubjectPicker({
  mode,
  value,
  onChange,
  scope,
  canUserRead,
  canAgentRead,
  me,
  idPrefix,
  disabled = false,
}: {
  mode: 'grant' | 'recipient'
  value: PickedSubject
  onChange: (next: PickedSubject) => void
  scope: CommunicationsScope
  canUserRead: boolean
  canAgentRead: boolean
  me: Me
  idPrefix: string
  disabled?: boolean
}) {
  const { t } = useTranslation('communications')
  const kinds: readonly GrantSubjectKind[] =
    mode === 'recipient' ? RECIPIENT_KINDS : GRANT_SUBJECT_KINDS
  const tenant = scope.tenant
  const workspace = scope.workspace ?? ''

  const members = useQuery({
    queryKey: communicationsKeys.members(tenant, scope.epoch),
    queryFn: ({ signal }) => listMembers({ tenant }, signal),
    enabled: value.kind === 'user' && canUserRead,
  })
  const agents = useQuery({
    queryKey: communicationsKeys.agents(tenant, scope.epoch, workspace),
    queryFn: ({ signal }) => listAgents(workspace, { tenant }, signal),
    enabled: value.kind === 'agent' && canAgentRead && workspace !== '',
  })

  const refHint =
    value.kind === 'user'
      ? t('subject.userRefHint')
      : value.kind === 'agent'
        ? t('subject.agentRefHint')
        : value.kind === 'session'
          ? t('subject.sessionRefHint')
          : t('subject.groupRefHint')

  const directory =
    value.kind === 'user' ? members : value.kind === 'agent' ? agents : null
  const directoryOffered =
    (value.kind === 'user' && canUserRead) ||
    (value.kind === 'agent' && canAgentRead)
  const pickLabel =
    value.kind === 'user' ? t('subject.pickUser') : t('subject.pickAgent')

  return (
    <div className="grid gap-3 sm:grid-cols-[minmax(8rem,10rem)_1fr]">
      <Field label={t('subject.kind')} htmlFor={`${idPrefix}-kind`}>
        <Select
          value={value.kind}
          onValueChange={(kind) =>
            onChange({ kind: kind as GrantSubjectKind, ref: '' })
          }
          disabled={disabled}
        >
          <SelectTrigger id={`${idPrefix}-kind`} aria-label={t('subject.kind')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {kinds.map((k) => (
              <SelectItem key={k} value={k}>
                {t(`subject.kinds.${k}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      <div className="flex flex-col gap-2">
        {directoryOffered ? (
          <Field
            label={pickLabel}
            htmlFor={`${idPrefix}-directory`}
            description={
              directory?.data?.has_more ? t('subject.truncated') : undefined
            }
          >
            <Select
              value={value.ref}
              onValueChange={(ref) => onChange({ kind: value.kind, ref })}
              disabled={disabled || !directory?.data}
            >
              <SelectTrigger
                id={`${idPrefix}-directory`}
                aria-label={pickLabel}
              >
                <SelectValue
                  placeholder={
                    directory?.isLoading ? t('subject.loading') : pickLabel
                  }
                />
              </SelectTrigger>
              <SelectContent>
                {value.kind === 'user'
                  ? (members.data?.items ?? []).map((m) => (
                      <SelectItem key={m.user_id} value={m.user_id}>
                        {m.display_name ? `${m.display_name} · ` : ''}
                        {m.email} · {m.role}
                        {m.user_id === me.userId ? ` (${t('subject.me')})` : ''}
                      </SelectItem>
                    ))
                  : (agents.data?.items ?? []).map((a) =>
                      a.identity_id ? (
                        <SelectItem key={a.id} value={a.identity_id}>
                          {a.name} · {a.kind}
                        </SelectItem>
                      ) : (
                        <SelectItem
                          key={a.id}
                          value={`__none__${a.id}`}
                          disabled
                        >
                          {a.name} · {t('subject.noIdentity')}
                        </SelectItem>
                      ),
                    )}
              </SelectContent>
            </Select>
          </Field>
        ) : value.kind === 'user' || value.kind === 'agent' ? (
          <p className="text-xs text-muted-foreground">
            {t('subject.noDirectory')}
          </p>
        ) : null}
        <Field
          label={t('subject.manualRef')}
          htmlFor={`${idPrefix}-ref`}
          description={refHint}
        >
          {(control) => (
            <div className="flex gap-2">
              <Input
                {...control}
                value={value.ref}
                onChange={(e) =>
                  onChange({ kind: value.kind, ref: e.target.value })
                }
                autoComplete="off"
                mono
                disabled={disabled}
              />
              {value.kind === 'user' && me.userId ? (
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  disabled={disabled}
                  onClick={() =>
                    onChange({ kind: 'user', ref: me.userId as string })
                  }
                >
                  {t('actions.useMe')}
                </Button>
              ) : null}
            </div>
          )}
        </Field>
      </div>
    </div>
  )
}
