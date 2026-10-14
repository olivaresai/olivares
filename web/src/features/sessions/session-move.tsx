// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// "Move to…" on a stopped session: the same session continues on another account of its
// tool (a move), or a new, linked session continues in another tool from a summary the
// person reviews (a handoff). Both are the engine's own routes; this renders them.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useId, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { Field } from '@/components/ui/field'
import { Segmented } from '@/components/ui/segmented'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import {
  profileLabel,
  type HandoffRunResult,
  type RunDTO,
} from '@/features/agentops/types'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { sessionsKeys } from './api'
import './i18n'

const NONE = '__none__'

type Mode = 'move' | 'handoff'

/**
 * Mounted only while it is open (its owner renders it conditionally), so a card that
 * never opens it reads no profiles and no preview. `onClose` is the one way out.
 */
export function MoveSessionDialog({
  run,
  onClose,
}: {
  run: RunDTO
  onClose: () => void
}) {
  const { t } = useTranslation('sessions')
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const qc = useQueryClient()
  const summaryId = useId()
  const [mode, setMode] = useState<Mode>('move')
  const [chosen, setChosen] = useState(NONE)
  // The person's edit of the generated text; null means they left it as generated.
  const [edited, setEdited] = useState<string | null>(null)
  // A handoff that started but whose first message is not certainly there: the dialog
  // stays, with the text, rather than the person's reviewed words disappearing.
  const [result, setResult] = useState<HandoffRunResult | null>(null)

  const canListProfiles = can('sessions:profile:read')
  const profilesQuery = useQuery({
    queryKey: agentOpsKeys.profiles(activeTenant, boundary.epoch, {
      state: 'active',
    }),
    queryFn: ({ signal }) =>
      agentOpsApi.listProfiles({ state: 'active', limit: 200 }, { signal }),
    enabled: canListProfiles,
  })
  const choices = useMemo(
    () =>
      (profilesQuery.data?.items ?? []).filter(
        (p) =>
          p.state === 'active' &&
          p.local_environment &&
          p.operable &&
          p.profile_ref !== run.provider_profile_ref &&
          (mode === 'handoff' || p.driver === run.provider_driver),
      ),
    [profilesQuery.data, run.provider_profile_ref, run.provider_driver, mode],
  )
  const profile = choices.find((p) => p.profile_ref === chosen)

  const previewQuery = useQuery({
    // Under the boundary scope the auth boundary clears, so a conversation summary
    // never outlives the principal or credential that read it.
    queryKey: [
      ...agentOpsKeys.boundaryScope(activeTenant, boundary.epoch),
      'handoff-preview',
      run.run_ref,
    ],
    queryFn: ({ signal }) =>
      agentOpsApi.handoffPreview(run.run_ref, { signal }),
    enabled: mode === 'handoff',
    staleTime: 0,
  })
  const generated = previewQuery.data?.summary ?? ''
  const summary = edited ?? generated

  const done = () => {
    onClose()
    // After the close, so the preview query is already inactive and is not refetched.
    void qc.invalidateQueries({ queryKey: agentOpsKeys.all(activeTenant) })
    void qc.invalidateQueries({ queryKey: sessionsKeys.all(activeTenant) })
  }
  const onErr = (err: unknown) =>
    toast.error(err instanceof ApiError ? err.message : t('move.failed'))

  const move = useMutation({
    mutationFn: () => agentOpsApi.move(run.run_ref, chosen),
    onSuccess: () => {
      toast.success(
        t('move.moved', { name: profile ? profileLabel(profile) : '' }),
      )
      done()
    },
    onError: onErr,
  })
  const handoff = useMutation({
    // The text on screen is what is sent: the engine would regenerate it otherwise,
    // and a person who read one version would start from another.
    mutationFn: () =>
      agentOpsApi.handoff(run.run_ref, {
        provider_profile_ref: chosen,
        summary,
      }),
    onSuccess: (started) => {
      if (started.summary_state === 'delivered') {
        toast.success(t('move.started'))
        done()
        return
      }
      setResult(started)
      void qc.invalidateQueries({ queryKey: agentOpsKeys.all(activeTenant) })
      void qc.invalidateQueries({ queryKey: sessionsKeys.all(activeTenant) })
    },
    onError: onErr,
  })

  const pending = move.isPending || handoff.isPending

  if (result) {
    const message =
      result.summary_state === 'unknown'
        ? t('move.stateUnknown')
        : result.summary_state === 'waits_for_approval'
          ? t('move.stateWaits')
          : t('move.stateNotDelivered')
    return (
      <ConfirmDialog
        open
        onOpenChange={(o) => !o && done()}
        title={t('move.title')}
        description={message}
        confirmLabel={t('move.close')}
        cancelLabel={t('move.close')}
        hideAuditNotice
        onConfirm={done}
      >
        <Field label={t('move.firstTurn')} htmlFor={summaryId}>
          <Textarea
            id={summaryId}
            rows={9}
            readOnly
            value={result.first_turn ?? summary}
          />
        </Field>
      </ConfirmDialog>
    )
  }

  const profilesProblem = !canListProfiles
    ? t('move.profilesDenied')
    : profilesQuery.isError
      ? t('move.profilesFailed')
      : null
  const summaryEmpty = mode === 'handoff' && summary.trim() === ''
  const previewReady =
    mode !== 'handoff' || (!previewQuery.isPending && !previewQuery.isError)

  return (
    <ConfirmDialog
      open
      onOpenChange={(o) => !o && !pending && done()}
      title={t('move.title')}
      description={mode === 'move' ? t('move.moveHint') : t('move.handoffHint')}
      confirmLabel={
        mode === 'move' ? t('move.confirmMove') : t('move.confirmHandoff')
      }
      confirmDisabled={
        chosen === NONE ||
        profile === undefined ||
        !previewReady ||
        summaryEmpty
      }
      pending={pending}
      onConfirm={() => (mode === 'move' ? move.mutate() : handoff.mutate())}
    >
      <div className="flex flex-col gap-3">
        <Segmented<Mode>
          aria-label={t('move.mode')}
          value={mode}
          onValueChange={(next) => {
            if (pending) return
            setMode(next)
            setChosen(NONE)
          }}
          options={[
            { value: 'move', label: t('move.modeMove') },
            { value: 'handoff', label: t('move.modeHandoff') },
          ]}
        />
        <Field label={t('move.target')}>
          <Select
            value={chosen}
            disabled={pending}
            onValueChange={(v) => {
              if (choices.some((p) => p.profile_ref === v)) setChosen(v)
            }}
          >
            <SelectTrigger aria-label={t('move.target')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={NONE} disabled>
                {profilesQuery.isPending && canListProfiles
                  ? t('move.loading')
                  : choices.length === 0
                    ? t('move.noTargets')
                    : t('move.pick')}
              </SelectItem>
              {choices.map((p) => (
                <SelectItem
                  key={p.profile_ref}
                  value={p.profile_ref}
                  title={p.profile_ref}
                >
                  {profileLabel(p)} · {p.driver}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        {profilesProblem && (
          <p role="alert" className="text-caption text-danger">
            {profilesProblem}
          </p>
        )}
        {mode === 'handoff' && (
          <Field label={t('move.summary')} htmlFor={summaryId}>
            <Textarea
              id={summaryId}
              rows={9}
              value={summary}
              disabled={previewQuery.isPending || pending}
              onChange={(e) => setEdited(e.target.value)}
              placeholder={
                previewQuery.isPending ? t('move.loading') : undefined
              }
            />
          </Field>
        )}
        {mode === 'handoff' && previewQuery.isError && (
          <p role="alert" className="text-caption text-danger">
            {previewQuery.error instanceof ApiError
              ? previewQuery.error.message
              : t('move.failed')}
          </p>
        )}
        {mode === 'handoff' && previewQuery.data && (
          <p className="text-caption text-muted-foreground">
            {t('move.transcript', {
              turns: previewQuery.data.turns,
              bytes: previewQuery.data.transcript_bytes,
            })}
          </p>
        )}
      </div>
    </ConfirmDialog>
  )
}
