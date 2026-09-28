// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Create or replace a publication target. The engine checks, not this form, that both
// bindings are approved in the workspace and that the repository is inside the credential's
// owners; the form states those rules and shows the refusal code when one fails.
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
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
import { KvList, KvRow } from '@/components/ui/kv'
import { Textarea } from '@/components/ui/textarea'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { gitpublishApi, gitpublishKeys } from './api'
import { outcomeOf, parseMergeBases } from './model'
import { AuthorityNote, OutcomePanel } from './parts'
import type { PublicationTarget } from './types'
import './i18n'

export function TargetFormDialog({
  target,
  workspaceId,
  onClose,
  onOpenIntent = () => {},
}: {
  /** Absent to create a target in `workspaceId`. */
  target?: PublicationTarget
  workspaceId: string
  onClose: () => void
  onOpenIntent?: (id: string) => void
}) {
  const { t } = useTranslation('gitpublish')
  const { activeTenant } = useAuth()
  const [credential, setCredential] = useState('')
  const [repository, setRepository] = useState('')
  const [prefix, setPrefix] = useState(target?.push_prefix ?? '')
  const [bases, setBases] = useState(target?.merge_bases.join('\n') ?? '')

  const save = usePrivilegedMutation<void, PublicationTarget>({
    mutationFn: () => {
      const request = { tenant: activeTenant }
      const merge_bases = parseMergeBases(bases)
      return target
        ? gitpublishApi.updateTarget(
            target.id,
            {
              expected_version: target.version,
              credential_binding_id: credential.trim(),
              repository_binding_id: repository.trim(),
              push_prefix: prefix.trim(),
              merge_bases,
            },
            request,
          )
        : gitpublishApi.createTarget(
            {
              workspace_id: workspaceId,
              credential_binding_id: credential.trim(),
              repository_binding_id: repository.trim(),
              push_prefix: prefix.trim(),
              merge_bases,
            },
            request,
          )
    },
    invalidateKeys: (saved) => [
      gitpublishKeys.targets(activeTenant),
      gitpublishKeys.target(activeTenant, saved.id),
    ],
    successMessage: target ? t('target.updated') : t('target.created'),
    onDone: onClose,
    onError: () => true,
  })
  const failure =
    save.error &&
    !(save.error instanceof ApiError && save.error.isStepUpRequired)
      ? outcomeOf({ error: save.error })
      : null

  const prefixInvalid = prefix.trim() !== '' && !prefix.trim().endsWith('/')
  const valid =
    prefix.trim() !== '' &&
    !prefixInvalid &&
    (target !== undefined ||
      (credential.trim() !== '' && repository.trim() !== ''))

  return (
    <Dialog open onOpenChange={(open) => (open ? undefined : onClose())}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>
            {target
              ? t('targetForm.editTitle', { id: target.id })
              : t('targetForm.createTitle')}
          </DialogTitle>
          <DialogDescription>{t('targetForm.description')}</DialogDescription>
        </DialogHeader>
        <form
          id="gitpublish-target"
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault()
            if (valid && !save.isPending) save.mutate()
          }}
        >
          <KvList>
            <KvRow label={t('columns.workspace')} mono>
              {workspaceId}
            </KvRow>
            {target ? (
              <KvRow label={t('columns.version')} mono>
                {target.version}
              </KvRow>
            ) : null}
          </KvList>
          <Field
            label={t('target.credentialBinding')}
            description={
              target
                ? t('targetForm.keepBinding')
                : t('targetForm.credentialHint')
            }
            required={!target}
          >
            <Input
              mono
              value={credential}
              onChange={(e) => setCredential(e.target.value)}
            />
          </Field>
          <Field
            label={t('target.repositoryBinding')}
            description={
              target
                ? t('targetForm.keepBinding')
                : t('targetForm.repositoryHint')
            }
            required={!target}
          >
            <Input
              mono
              value={repository}
              onChange={(e) => setRepository(e.target.value)}
            />
          </Field>
          <Field
            label={t('columns.pushPrefix')}
            description={t('targetForm.prefixHint')}
            error={prefixInvalid ? t('targetForm.prefixSlash') : undefined}
            required
          >
            <Input
              mono
              value={prefix}
              placeholder="agents/"
              onChange={(e) => setPrefix(e.target.value)}
            />
          </Field>
          <Field
            label={t('columns.mergeBases')}
            description={t('targetForm.basesHint')}
          >
            <Textarea
              mono
              rows={3}
              value={bases}
              onChange={(e) => setBases(e.target.value)}
            />
          </Field>
          <AuthorityNote action="target" />
        </form>
        {failure ? (
          <OutcomePanel
            outcome={failure}
            onOpenIntent={onOpenIntent}
            canReconcile={false}
          />
        ) : null}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t('actions.cancel')}
          </Button>
          <Button
            type="submit"
            form="gitpublish-target"
            variant="primary"
            disabled={!valid || save.isPending}
          >
            {target ? t('targetForm.save') : t('targetForm.create')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
