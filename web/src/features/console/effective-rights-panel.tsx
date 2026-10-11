// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
/**
 * EffectiveRightsPanel is the "why" read of the access review: which of the eight trustee
 * rights a subject holds at one node, and the path of containers the node sits on. The
 * engine asks each right of the Authorizer (GET /v1/auth/effective-rights), so the panel
 * only shows the answer; it computes nothing. Admin-only (authz:admin), read-only.
 */
import { useMutation } from '@tanstack/react-query'
import { ChevronRight } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { StaticTable } from '@/components/data/static-table'
import { Badge, type BadgeProps } from '@/components/ui/badge'
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
import { Spinner } from '@/components/ui/spinner'
import {
  consoleApi,
  type EffectiveRightState,
  type EffectiveRightsRequest,
  type EffectiveRightsResponse,
} from './api'

const STATE_VARIANT: Record<EffectiveRightState, BadgeProps['variant']> = {
  held: 'success',
  not_held: 'neutral',
  unknown: 'warning',
  not_applicable: 'outline',
}

export function EffectiveRightsPanel() {
  const { t } = useTranslation('console')
  const [subjectType, setSubjectType] =
    useState<EffectiveRightsRequest['subject_type']>('user')
  const [subjectId, setSubjectId] = useState('')
  const [kind, setKind] = useState<EffectiveRightsRequest['kind']>('agent')
  const [nodeId, setNodeId] = useState('')

  // A mutation, as the sibling searches: the answer is asked on demand and never cached
  // (the engine sends it no-store). The engine always sends the node's path and the eight
  // rights; a body without them is not an answer, and showing it would read as "holds nothing".
  const read = useMutation<
    EffectiveRightsResponse,
    Error,
    EffectiveRightsRequest
  >({
    mutationFn: async (input) => {
      const answer = await consoleApi.effectiveRights(input)
      if (!answer?.path?.length || !answer?.rights?.length) {
        throw new Error(t('granular.accessReview.loadError'))
      }
      return answer
    },
  })
  // An answer belongs to the question that produced it: editing the question clears it, so
  // one subject's rights never sit under another subject's id.
  const edit =
    <T,>(set: (v: T) => void) =>
    (v: T) => {
      read.reset()
      set(v)
    }

  const valid = subjectId.trim() !== '' && nodeId.trim() !== ''

  return (
    <div className="flex flex-col gap-4">
      <p className="max-w-2xl text-body text-muted-foreground">
        {t('granular.accessReview.why.hint')}
      </p>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <Field
          label={t('granular.accessReview.subjectType')}
          htmlFor="er-subtype"
        >
          <Select
            value={subjectType}
            onValueChange={edit((v: string) =>
              setSubjectType(v as EffectiveRightsRequest['subject_type']),
            )}
          >
            <SelectTrigger id="er-subtype">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="user">
                {t('granular.accessReview.subjectTypeUser')}
              </SelectItem>
              <SelectItem value="token">
                {t('granular.accessReview.subjectTypeToken')}
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>

        <Field
          label={t('granular.accessReview.subjectId')}
          htmlFor="er-subid"
          description={t('granular.accessReview.subjectIdHint')}
          required
        >
          <Input
            id="er-subid"
            value={subjectId}
            onChange={(e) => edit(setSubjectId)(e.target.value)}
            mono
          />
        </Field>

        <Field
          label={t('granular.accessReview.resourceType')}
          htmlFor="er-kind"
        >
          <Select
            value={kind}
            onValueChange={edit((v: string) =>
              setKind(v as EffectiveRightsRequest['kind']),
            )}
          >
            <SelectTrigger id="er-kind">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="agent">
                {t('granular.accessReview.resourceTypeAgent')}
              </SelectItem>
              <SelectItem value="session">
                {t('granular.accessReview.resourceTypeSession')}
              </SelectItem>
              <SelectItem value="resource">
                {t('granular.accessReview.resourceTypeResource')}
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>

        <Field
          label={t('granular.accessReview.resourceId')}
          htmlFor="er-nodeid"
          description={t('granular.accessReview.resourceIdHint')}
          required
        >
          <Input
            id="er-nodeid"
            value={nodeId}
            onChange={(e) => edit(setNodeId)(e.target.value)}
            mono
          />
        </Field>
      </div>

      <div>
        <Button
          onClick={() =>
            read.mutate({
              subject_type: subjectType,
              subject_id: subjectId.trim(),
              kind,
              id: nodeId.trim(),
            })
          }
          disabled={!valid || read.isPending}
        >
          {read.isPending && <Spinner size="sm" aria-hidden />}
          {t('granular.accessReview.why.run')}
        </Button>
      </div>

      {read.isError && (
        <p role="alert" className="text-body text-danger">
          {read.error.message || t('granular.accessReview.loadError')}
        </p>
      )}

      {read.data && <EffectiveRightsAnswer answer={read.data} />}
    </div>
  )
}

function EffectiveRightsAnswer({
  answer,
}: {
  answer: EffectiveRightsResponse
}) {
  const { t } = useTranslation('console')
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-col gap-1">
        <p className="text-caption font-medium text-muted-foreground">
          {t('granular.accessReview.why.path')}
        </p>
        <ol
          aria-label={t('granular.accessReview.why.path')}
          className="flex flex-wrap items-center gap-1"
        >
          {answer.path.map((step, i) => (
            <li
              key={`${step.kind}:${step.ref}:${i}`}
              className="flex items-center gap-1"
            >
              {i > 0 && (
                <ChevronRight
                  className="size-3 text-muted-foreground"
                  aria-hidden
                />
              )}
              <Badge variant="neutral">{step.kind}</Badge>
              <span className="font-mono text-caption text-foreground">
                {step.ref}
              </span>
              {step.workspace && (
                <span className="text-caption text-muted-foreground">
                  {t('granular.accessReview.why.inWorkspace', {
                    workspace: step.workspace,
                  })}
                </span>
              )}
            </li>
          ))}
        </ol>
      </div>

      <div className="overflow-hidden rounded-lg border border-border">
        <StaticTable>
          <thead>
            <tr>
              <th>{t('granular.accessReview.why.right')}</th>
              <th>{t('granular.accessReview.why.state')}</th>
            </tr>
          </thead>
          <tbody>
            {answer.rights.map((r) => (
              <tr key={r.name}>
                <td className="text-body text-foreground">{r.name}</td>
                <td>
                  <Badge variant={STATE_VARIANT[r.state] ?? 'warning'}>
                    {t(`granular.accessReview.why.states.${r.state}`, {
                      defaultValue: r.state,
                    })}
                  </Badge>
                </td>
              </tr>
            ))}
          </tbody>
        </StaticTable>
      </div>

      {answer.rights.some((r) => r.state === 'unknown') && (
        <p className="text-caption text-warning">
          {t('granular.accessReview.why.unknownHint')}
        </p>
      )}
    </div>
  )
}
