// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useStepUpOwner, type StepUpAttempt } from '@/stores/step-up'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { useAuth } from '@/lib/auth/context'
import { http } from '@/lib/api'
import { ApiError } from '@/lib/api/errors'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { ensureFolder } from '@/features/agentops/session-launch'
import type { RunDTO } from '@/features/agentops/types'
import { sessionsKeys } from './api'

interface Candidate {
  provider_profile_ref: string
  conversation_id: string
  folder: string
  title: string
  modified: string
}

export function ImportConversations({
  onClose,
  onImported,
}: {
  onClose: () => void
  onImported: (run: RunDTO) => void
}) {
  const { t } = useTranslation('sessions')
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const qc = useQueryClient()
  const [selected, setSelected] = useState<Candidate | null>(null)
  const [folder, setFolder] = useState('')
  const candidates = useQuery({
    queryKey: [
      ...sessionsKeys.boundaryScope(activeTenant, boundary.epoch),
      'imports',
    ],
    queryFn: ({ signal }) =>
      http.get<{ items: Candidate[] }>(
        '/v1/m/sessions/conversation-candidates',
        { tenant: activeTenant, signal },
      ),
  })
  const captureOwner = useStepUpOwner()
  const imported = useMutation({
    mutationKey: [
      ...agentOpsKeys.boundaryScope(activeTenant, boundary.epoch),
      'import',
    ],
    mutationFn: async (attempt: StepUpAttempt) => {
      if (!selected || !can('sessions:run:write'))
        throw new Error(t('import.failed'))
      const authority = {
        tenant: activeTenant,
        signal: attempt.signal,
        dispatchGuard: attempt.dispatchGuard,
      }
      const workspace = await ensureFolder(folder, authority)
      return agentOpsApi.createRun(
        {
          name: '',
          transport: 'stream-json',
          permission_mode: 'default',
          effort: '',
          isolation: 'native',
          env_allow: [],
          workspace_ref: workspace.workspace_ref,
          provider_profile_ref: selected.provider_profile_ref,
          import_conversation_id: selected.conversation_id,
        },
        authority,
      )
    },
    onSuccess: (run, attempt) => {
      if (!attempt.current()) return
      void qc.invalidateQueries({ queryKey: agentOpsKeys.all(activeTenant) })
      void qc.invalidateQueries({ queryKey: sessionsKeys.all(activeTenant) })
      onImported(run)
    },
  })
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !imported.isPending) onClose()
      }}
    >
      <DialogContent>
        <DialogTitle>{t('import.title')}</DialogTitle>
        <DialogDescription>{t('import.hint')}</DialogDescription>
        {candidates.isPending ? (
          <p role="status">{t('import.loading')}</p>
        ) : null}
        {candidates.isError ? (
          <p role="alert">
            {candidates.error instanceof ApiError
              ? candidates.error.message
              : t('import.failed')}
          </p>
        ) : null}
        {candidates.data?.items.length === 0 ? (
          <p>{t('import.empty')}</p>
        ) : null}
        <div className="max-h-72 space-y-2 overflow-y-auto">
          {candidates.data?.items.map((candidate) => (
            <Button
              key={`${candidate.provider_profile_ref}:${candidate.conversation_id}`}
              variant="outline"
              className="h-auto w-full flex-col items-start"
              aria-pressed={
                selected?.conversation_id === candidate.conversation_id &&
                selected?.provider_profile_ref ===
                  candidate.provider_profile_ref
              }
              disabled={imported.isPending}
              onClick={() => {
                setSelected(candidate)
                setFolder(candidate.folder)
              }}
            >
              <span>{candidate.title}</span>
              <span className="text-caption text-muted-foreground">
                {candidate.folder}
              </span>
            </Button>
          ))}
        </div>
        {selected ? (
          <Field label={t('import.folder')}>
            <Input
              disabled={imported.isPending}
              value={folder}
              onChange={(event) => setFolder(event.target.value)}
            />
          </Field>
        ) : null}
        {imported.isError ? (
          <p role="alert">
            {imported.error instanceof ApiError
              ? imported.error.message
              : t('import.failed')}
          </p>
        ) : null}
        <Button
          disabled={!selected || !folder.trim() || imported.isPending}
          onClick={() => imported.mutate(captureOwner().begin())}
        >
          {t('import.title')}
        </Button>
      </DialogContent>
    </Dialog>
  )
}
