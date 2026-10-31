// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Laptop, LogOut } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { QueryErrorState } from '@/components/layout/query-error-state'
import { XScroll } from '@/components/data/scroll-edges'
import { StaticTable } from '@/components/data/static-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/field'
import { Spinner } from '@/components/ui/spinner'
import { toast } from '@/components/ui/toaster'
import { SectionCard } from '@/features/_intel'
import { ApiError } from '@/lib/api/errors'
import { formatDateTime } from '@/lib/format'
import { identityApi, identityKeys } from './api'
import type { AuthSessionItem } from './types'

export function AuthSessionsSection() {
  const { t } = useTranslation(['identity', 'common'])
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: identityKeys.authSessions(),
    queryFn: () => identityApi.authSessions(),
  })
  const [deviceName, setDeviceName] = useState('')
  const [pairLink, setPairLink] = useState('')
  const pairing = useMutation({
    mutationFn: () => identityApi.pairSession(deviceName.trim()),
    onSuccess: (result) => {
      setPairLink(
        `${window.location.origin}/pair#pair=${encodeURIComponent(result.token)}`,
      )
      setError(null)
      void queryClient.invalidateQueries({
        queryKey: identityKeys.authSessions(),
      })
    },
    onError: (err) => setError(sessionError(err, t)),
  })
  const [deleting, setDeleting] = useState<AuthSessionItem | null>(null)
  const [endAllOpen, setEndAllOpen] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const sessions = query.data?.items ?? []
  const otherCount = sessions.filter((session) => !session.current).length
  const queryKey = identityKeys.authSessions()
  const endMutation = useMutation({
    mutationFn: endSession,
    onSuccess: () => {
      setDeleting(null)
      setError(null)
      toast.success(t('identity:sessions.ended'))
    },
    onError: (err) => {
      // The dialog is modal: an alert behind it stays hidden from assistive tech.
      setDeleting(null)
      setError(sessionError(err, t))
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey }),
  })
  const endAllMutation = useMutation({
    mutationFn: async (ids: string[]) => {
      const results = await Promise.allSettled(ids.map(endSession))
      const failed = results.filter(
        (result) => result.status === 'rejected',
      ).length
      if (failed > 0) {
        throw new Error(t('identity:sessions.endAllFailed', { count: failed }))
      }
    },
    onSuccess: () => {
      setEndAllOpen(false)
      setError(null)
      toast.success(t('identity:sessions.ended'))
    },
    onError: (err) => {
      setEndAllOpen(false)
      setError(sessionError(err, t))
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey }),
  })

  return (
    <SectionCard
      title={t('identity:sessions.title')}
      description={t('identity:sessions.description')}
    >
      <form
        className="mt-3 flex flex-col gap-3"
        onSubmit={(event) => {
          event.preventDefault()
          setPairLink('')
          pairing.mutate()
        }}
      >
        <Field
          label={t('identity:sessions.deviceName')}
          htmlFor="pair-device-name"
        >
          <Input
            id="pair-device-name"
            value={deviceName}
            maxLength={80}
            required
            onChange={(event) => setDeviceName(event.target.value)}
          />
        </Field>
        {!deviceName.trim() ? (
          <p
            id="pair-device-name-required"
            className="text-caption text-text-2"
          >
            {t('identity:sessions.deviceName')}:{' '}
            {t('common:validation.required')}
          </p>
        ) : null}
        <Button
          type="submit"
          variant="outline"
          aria-describedby={
            !deviceName.trim() ? 'pair-device-name-required' : undefined
          }
          disabled={pairing.isPending || !deviceName.trim()}
        >
          {t('identity:sessions.pair')}
        </Button>
        {pairLink ? (
          <Field
            label={t('identity:sessions.pairLink')}
            htmlFor="pair-device-link"
            description={t('identity:sessions.pairHint')}
          >
            <Input
              id="pair-device-link"
              readOnly
              value={pairLink}
              onFocus={(event) => event.target.select()}
            />
          </Field>
        ) : null}
      </form>
      {otherCount > 0 && !query.isError ? (
        <div className="mt-3 flex justify-end">
          <Button
            variant="outline"
            onClick={() => setEndAllOpen(true)}
            disabled={endMutation.isPending || endAllMutation.isPending}
          >
            <LogOut aria-hidden />
            {t('identity:sessions.endAll')}
          </Button>
        </div>
      ) : null}
      {query.isLoading ? (
        <div className="flex justify-center py-8">
          <Spinner />
        </div>
      ) : query.isError ? (
        <QueryErrorState
          error={query.error}
          retry={() => void query.refetch()}
        />
      ) : sessions.length === 0 ? (
        <EmptyState
          icon={<Laptop />}
          title={t('identity:sessions.none')}
          description={t('identity:sessions.noneHint')}
        />
      ) : (
        <div className="mt-3 overflow-hidden rounded-lg border border-border">
          <XScroll accessibleLabel={t('identity:sessions.title')}>
            <StaticTable>
              <thead>
                <tr>
                  <th>{t('identity:sessions.client')}</th>
                  <th>{t('identity:sessions.lastUsed')}</th>
                  <th>{t('identity:sessions.created')}</th>
                  <th>{t('identity:sessions.expires')}</th>
                  <th className="text-right">
                    <span className="sr-only">
                      {t('identity:sessions.actionsColumn')}
                    </span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {sessions.map((session) => (
                  <tr key={session.id}>
                    <td>
                      {session.name ? (
                        <span className="mr-2">{session.name}</span>
                      ) : null}
                      <Badge variant="outline">
                        {t(`identity:sessions.kinds.${session.kind}`)}
                      </Badge>
                    </td>
                    <td>
                      {session.last_used_at ? (
                        <time dateTime={session.last_used_at}>
                          {formatDateTime(session.last_used_at)}
                        </time>
                      ) : (
                        '—'
                      )}
                    </td>
                    <td>
                      <time dateTime={session.created_at}>
                        {formatDateTime(session.created_at)}
                      </time>
                    </td>
                    <td>
                      <time dateTime={session.expires_at}>
                        {formatDateTime(session.expires_at)}
                      </time>
                    </td>
                    <td className="text-right">
                      {session.current ? (
                        <Badge variant="success">
                          {t('identity:sessions.current')}
                        </Badge>
                      ) : (
                        <Button
                          variant="ghost"
                          size="sm"
                          aria-label={t('identity:sessions.endLabel', {
                            kind: session.name
                              ? `${session.name} (${t(`identity:sessions.kinds.${session.kind}`)})`
                              : t(`identity:sessions.kinds.${session.kind}`),
                            created: formatDateTime(session.created_at),
                          })}
                          onClick={() => setDeleting(session)}
                          disabled={
                            endMutation.isPending || endAllMutation.isPending
                          }
                        >
                          <LogOut aria-hidden />
                          {t('identity:sessions.end')}
                        </Button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </StaticTable>
          </XScroll>
        </div>
      )}
      {error ? (
        <p role="alert" className="mt-3 text-body text-danger">
          {error}
        </p>
      ) : null}
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={t('identity:sessions.endTitle')}
        description={t('identity:sessions.endBody')}
        confirmLabel={t('identity:sessions.end')}
        tone="danger"
        pending={endMutation.isPending}
        onConfirm={() => deleting && endMutation.mutate(deleting.id)}
      />
      <ConfirmDialog
        open={endAllOpen}
        onOpenChange={setEndAllOpen}
        title={t('identity:sessions.endAllTitle')}
        description={t('identity:sessions.endAllBody', { count: otherCount })}
        confirmLabel={t('identity:sessions.endAll')}
        tone="danger"
        pending={endAllMutation.isPending}
        onConfirm={() =>
          endAllMutation.mutate(
            sessions
              .filter((session) => !session.current)
              .map((session) => session.id),
          )
        }
      />
    </SectionCard>
  )
}

// A session already gone (ended elsewhere, or expired) is the outcome asked for.
async function endSession(id: string): Promise<void> {
  try {
    await identityApi.authSessionDelete(id)
  } catch (err) {
    if (!(err instanceof ApiError && err.status === 404)) throw err
  }
}

function sessionError(err: unknown, t: (key: string) => string): string {
  return err instanceof Error && err.message
    ? err.message
    : t('common:errors.generic')
}
