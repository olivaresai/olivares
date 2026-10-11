// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// One publication target: its binding (workspace, credential and repository bindings, push
// prefix and merge bases), the authority each action requires, the actions the caller holds,
// and the target's intents.
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AsyncSection } from '@/features/_intel'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { KvList, KvRow } from '@/components/ui/kv'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { gitpublishApi, gitpublishKeys } from './api'
import { actionAuthority, hasChangeAPI, outcomeOf } from './model'
import { AuthorityNote, IntentStateBadge, OutcomePanel } from './parts'
import { PublishDialog } from './publish-dialog'
import { TargetFormDialog } from './target-form'
import type { PublicationEffect } from './types'
import './i18n'

const EFFECTS: PublicationEffect[] = ['push', 'pull_request', 'merge']

export function TargetSheet({
  targetId,
  onClose,
  onOpenIntent,
}: {
  targetId: string
  onClose: () => void
  onOpenIntent: (id: string) => void
}) {
  const { t } = useTranslation('gitpublish')
  const { activeTenant, can } = useAuth()
  const target = useQuery({
    queryKey: gitpublishKeys.target(activeTenant, targetId),
    queryFn: () => gitpublishApi.target(targetId),
  })
  const intents = useQuery({
    queryKey: gitpublishKeys.intents(activeTenant, targetId),
    queryFn: () => gitpublishApi.intents(targetId),
  })
  const [publishing, setPublishing] = useState<PublicationEffect | null>(null)
  const [editing, setEditing] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const isAdmin = can(actionAuthority('target').permission)

  const remove = usePrivilegedMutation<void, void>({
    mutationFn: () =>
      gitpublishApi.deleteTarget(targetId, { tenant: activeTenant }),
    invalidateKeys: [gitpublishKeys.targets(activeTenant)],
    successMessage: t('target.deleted'),
    onDone: () => {
      setDeleting(false)
      onClose()
    },
    onError: () => true,
  })
  const removeFailure =
    remove.error &&
    !(remove.error instanceof ApiError && remove.error.isStepUpRequired)
      ? outcomeOf({ error: remove.error })
      : null

  return (
    <Sheet open onOpenChange={(open) => (open ? undefined : onClose())}>
      <SheetContent className="overflow-y-auto sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle>{t('target.title', { id: targetId })}</SheetTitle>
          <SheetDescription>{t('target.description')}</SheetDescription>
        </SheetHeader>
        <div className="space-y-5 px-4 pb-4">
          <AsyncSection query={target}>
            {(tg) => (
              <>
                <section className="space-y-1">
                  <h3 className="text-body font-medium text-foreground">
                    {t('target.binding')}
                  </h3>
                  <KvList>
                    <KvRow label={t('columns.workspace')} mono>
                      {tg.workspace_id}
                    </KvRow>
                    <KvRow label={t('target.credentialBinding')} mono>
                      {tg.credential_binding_id ?? <Withheld />}
                    </KvRow>
                    <KvRow label={t('target.repositoryBinding')} mono>
                      {tg.repository_binding_id ?? <Withheld />}
                    </KvRow>
                    <KvRow label={t('columns.pushPrefix')} mono>
                      {tg.push_prefix}
                    </KvRow>
                    <KvRow label={t('columns.mergeBases')}>
                      <span className="flex flex-wrap justify-end gap-1">
                        {tg.merge_bases.length === 0
                          ? t('target.noMergeBases')
                          : tg.merge_bases.map((b) => (
                              <Badge key={b} variant="outline">
                                {b}
                              </Badge>
                            ))}
                      </span>
                    </KvRow>
                    <KvRow label={t('columns.version')} mono>
                      {tg.version}
                    </KvRow>
                  </KvList>
                  <p className="text-caption text-muted-foreground">
                    {t('target.bindingHint')}
                  </p>
                </section>

                <section className="space-y-2">
                  <h3 className="text-body font-medium text-foreground">
                    {t('target.actions')}
                  </h3>
                  <ul className="space-y-2">
                    {EFFECTS.filter(
                      (effect) => effect === 'push' || hasChangeAPI(tg),
                    ).map((effect) => (
                      <li
                        key={effect}
                        className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border p-2"
                      >
                        <div className="min-w-0">
                          <p className="text-body text-foreground">
                            {t(`effects.${effect}`)}
                          </p>
                          <AuthorityNote action={effect} />
                        </div>
                        {can(actionAuthority(effect).permission) ? (
                          <Button
                            size="sm"
                            variant="secondary"
                            onClick={() => setPublishing(effect)}
                          >
                            {t(`actions.${effect}`)}
                          </Button>
                        ) : (
                          <span className="text-caption text-muted-foreground">
                            {t('target.notHeld')}
                          </span>
                        )}
                      </li>
                    ))}
                    <li className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border p-2">
                      <div className="min-w-0">
                        <p className="text-body text-foreground">
                          {t('target.administration')}
                        </p>
                        <AuthorityNote action="target" />
                      </div>
                      {isAdmin ? (
                        <div className="flex gap-2">
                          <Button
                            size="sm"
                            variant="secondary"
                            onClick={() => setEditing(true)}
                          >
                            {t('actions.edit')}
                          </Button>
                          <Button
                            size="sm"
                            variant="outline"
                            onClick={() => setDeleting(true)}
                          >
                            {t('actions.delete')}
                          </Button>
                        </div>
                      ) : (
                        <span className="text-caption text-muted-foreground">
                          {t('target.notHeld')}
                        </span>
                      )}
                    </li>
                  </ul>
                  {removeFailure ? (
                    <OutcomePanel
                      outcome={removeFailure}
                      onOpenIntent={onOpenIntent}
                      canReconcile={false}
                    />
                  ) : null}
                </section>

                {publishing ? (
                  <PublishDialog
                    target={tg}
                    effect={publishing}
                    onClose={() => setPublishing(null)}
                    onOpenIntent={onOpenIntent}
                  />
                ) : null}
                {editing ? (
                  <TargetFormDialog
                    target={tg}
                    workspaceId={tg.workspace_id}
                    onClose={() => setEditing(false)}
                  />
                ) : null}
              </>
            )}
          </AsyncSection>

          <section className="space-y-2">
            <h3 className="text-body font-medium text-foreground">
              {t('intents.title')}
            </h3>
            <AsyncSection query={intents} skeletonHeight={80}>
              {(list) =>
                list.items.length === 0 ? (
                  <p className="text-body text-muted-foreground">
                    {t('intents.empty')}
                  </p>
                ) : (
                  <ul className="space-y-2">
                    {list.items.map((intent) => (
                      <li
                        key={intent.id}
                        className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-surface p-2"
                      >
                        <IntentStateBadge state={intent.state} />
                        <span className="text-body">
                          {t(`effects.${intent.effect}`)}
                        </span>
                        <code className="min-w-0 flex-1 truncate font-mono text-caption">
                          {intent.operation_id}
                        </code>
                        <Button
                          size="sm"
                          variant="ghost"
                          aria-label={t('intents.open', { id: intent.id })}
                          onClick={() => onOpenIntent(intent.id)}
                        >
                          {t('actions.open')}
                        </Button>
                      </li>
                    ))}
                  </ul>
                )
              }
            </AsyncSection>
          </section>
        </div>

        <ConfirmDialog
          open={deleting}
          onOpenChange={setDeleting}
          tone="danger"
          title={t('target.deleteTitle', { id: targetId })}
          description={t('target.deleteDescription')}
          confirmLabel={t('actions.delete')}
          pending={remove.isPending}
          onConfirm={() => remove.mutate()}
        />
      </SheetContent>
    </Sheet>
  )
}

function Withheld() {
  const { t } = useTranslation('gitpublish')
  return (
    <span className="font-sans text-muted-foreground">
      {t('target.withheld')}
    </span>
  )
}
