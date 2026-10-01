// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useId, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { KvList, KvRow } from '@/components/ui/kv'
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { agentOpsApi, agentOpsKeys } from './api'
import { AuthorityLostError, useAuthBoundary } from './auth-boundary'
import type {
  ProviderProfileDTO,
  SessionWorkCapability,
  SessionWorkGrantRequest,
} from './types'

const ACTIONS: SessionWorkCapability[] = [
  'work.read',
  'work.create',
  'work.assign',
  'work.review',
  'decision.read',
  'decision.write',
]
const UUID_V7 =
  /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/

/** The profile sheet owns the point read and remounts on authority changes.
 * A grant revision closes drafts. The backend decides workspace delegation. */
export function ProfileWorkGrant({
  profile,
  readReady,
}: {
  profile: ProviderProfileDTO
  readReady: boolean
}) {
  const { t } = useTranslation('agentops')
  const { can, activeTenant } = useAuth()
  const boundary = useAuthBoundary()
  const allowed =
    readReady &&
    profile.state !== 'retired' &&
    can('sessions:profile:write') &&
    can('sessions:profile:admin')
  const grant = profile.session_work_grant
  const editTrigger = useRef<HTMLButtonElement>(null)
  const revision = `${profile.profile_ref}:${profile.updated_at ?? ''}:${grant?.grant_id ?? '-'}`
  const [seenRevision, setSeenRevision] = useState(revision)
  const [mode, setMode] = useState<'edit' | 'revoke' | null>(null)
  if (seenRevision !== revision) {
    setSeenRevision(revision)
    if (mode !== null) setMode(null)
  }
  const [seenAllowed, setSeenAllowed] = useState(allowed)
  if (seenAllowed !== allowed) {
    setSeenAllowed(allowed)
    if (!allowed) setMode(null)
  }
  const returnFocus = (event: Event) => {
    if (editTrigger.current?.isConnected) {
      event.preventDefault()
      editTrigger.current.focus()
    }
  }
  const mutation = usePrivilegedMutation<
    SessionWorkGrantRequest | null,
    ProviderProfileDTO
  >({
    mutationKey: agentOpsKeys.profile(
      activeTenant,
      boundary.epoch,
      profile.profile_ref,
    ),
    mutationFn: (value) => {
      if (!allowed) throw new AuthorityLostError()
      return agentOpsApi.patchProfile(profile.profile_ref, {
        session_work_grant: value,
      })
    },
    invalidateKeys: [
      agentOpsKeys.profiles(activeTenant, boundary.epoch),
      agentOpsKeys.profile(activeTenant, boundary.epoch, profile.profile_ref),
    ],
    successMessage: (_data, value) =>
      t(value ? 'profiles.workGrant.saved' : 'profiles.workGrant.revoked'),
    onDone: () => setMode(null),
    onError: (error) => {
      if (!(error instanceof AuthorityLostError)) return false
      setMode(null)
      toast.warning(t('profiles.authority.lost'))
      return true
    },
  })
  return (
    <section className="flex flex-col gap-3 border-t border-border pt-4">
      <h3 className="text-body font-medium">{t('profiles.workGrant.title')}</h3>
      <p className="text-caption text-muted-foreground">
        {t('profiles.workGrant.hint')}
      </p>
      {!readReady ? (
        <p role="status" className="text-caption text-muted-foreground">
          {t('profiles.workGrant.readPending')}
        </p>
      ) : grant ? (
        <KvList>
          <KvRow label={t('profiles.workGrant.workspace')} mono>
            {grant.workspace_id}
          </KvRow>
          <KvRow label={t('profiles.workGrant.actions')}>
            <ul className="flex flex-col gap-1">
              {grant.capabilities.map((action) => (
                <li key={action}>
                  {t(
                    `profiles.workGrant.capabilities.${action.replace('.', '_')}`,
                  )}
                </li>
              ))}
            </ul>
          </KvRow>
        </KvList>
      ) : (
        <p className="text-caption text-muted-foreground">
          {t('profiles.workGrant.none')}
        </p>
      )}
      {allowed && (
        <div className="flex flex-wrap gap-2">
          <Button
            ref={editTrigger}
            variant="secondary"
            size="sm"
            onClick={() => setMode('edit')}
          >
            {t('profiles.workGrant.edit')}
          </Button>
          {grant && (
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setMode('revoke')}
            >
              {t('profiles.workGrant.revoke')}
            </Button>
          )}
        </div>
      )}
      {mode === 'edit' && allowed && (
        <GrantEditor
          initial={grant}
          pending={mutation.isPending}
          onClose={() => setMode(null)}
          onSave={(value) => mutation.mutate(value)}
          onCloseAutoFocus={returnFocus}
        />
      )}
      <ConfirmDialog
        open={mode === 'revoke' && allowed}
        onOpenChange={(open) => {
          if (!open) setMode(null)
        }}
        title={t('profiles.workGrant.revoke')}
        description={t('profiles.workGrant.replaceHint')}
        confirmLabel={t('profiles.workGrant.revokeConfirm')}
        pending={mutation.isPending}
        onConfirm={() => mutation.mutate(null)}
        onCloseAutoFocus={returnFocus}
      />
    </section>
  )
}

function GrantEditor({
  initial,
  pending,
  onClose,
  onSave,
  onCloseAutoFocus,
}: {
  initial: ProviderProfileDTO['session_work_grant']
  pending: boolean
  onClose: () => void
  onSave: (value: SessionWorkGrantRequest) => void
  onCloseAutoFocus: (event: Event) => void
}) {
  const { t } = useTranslation('agentops')
  const prefix = useId()
  const [workspace, setWorkspace] = useState(initial?.workspace_id ?? '')
  const [actions, setActions] = useState<SessionWorkCapability[]>(
    initial?.capabilities ?? [],
  )
  const validWorkspace = UUID_V7.test(workspace.trim())
  return (
    <ConfirmDialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
      title={t('profiles.workGrant.edit')}
      description={t('profiles.workGrant.replaceHint')}
      confirmLabel={t('profiles.workGrant.save')}
      pending={pending}
      onCloseAutoFocus={onCloseAutoFocus}
      confirmDisabled={!validWorkspace || actions.length === 0}
      onConfirm={() =>
        onSave({
          role: 'orchestrator',
          workspace_id: workspace.trim(),
          capabilities: actions,
        })
      }
    >
      <div className="flex flex-col gap-4">
        <Field
          label={t('profiles.workGrant.workspace')}
          required
          error={
            workspace !== '' && !validWorkspace
              ? t('profiles.workGrant.invalidWorkspace')
              : undefined
          }
        >
          <Input
            value={workspace}
            onChange={(event) => setWorkspace(event.target.value)}
            disabled={pending}
            autoComplete="off"
            spellCheck={false}
            mono
          />
        </Field>
        <fieldset className="flex flex-col gap-3" disabled={pending}>
          <legend className="mb-3 text-caption font-medium">
            {t('profiles.workGrant.actions')}
          </legend>
          {ACTIONS.map((action) => (
            <label
              key={action}
              htmlFor={`${prefix}-${action}`}
              className="flex items-center gap-3 text-body"
            >
              <Checkbox
                id={`${prefix}-${action}`}
                checked={actions.includes(action)}
                onCheckedChange={(checked) =>
                  setActions((previous) =>
                    checked === true
                      ? [...previous, action]
                      : previous.filter((value) => value !== action),
                  )
                }
              />
              {t(`profiles.workGrant.capabilities.${action.replace('.', '_')}`)}
            </label>
          ))}
        </fieldset>
      </div>
    </ConfirmDialog>
  )
}
