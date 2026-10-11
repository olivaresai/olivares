// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// PUBLISH WHAT A SESSION COMMITTED, from the session itself. A session whose workspace has a
// publication target offers the governed push with its own run: the engine fetches the commit
// from the session's folder into the managed repository (modules/gitpublish feed), then
// pushes it as the target's credential. The push, its answer and the draft pull request that
// can follow are the publication dialog's; nothing here sends a request of its own.
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { Button } from '@/components/ui/button'
import type { RunDTO } from '@/features/agentops/types'
import { useAuth } from '@/lib/auth/context'
import { useModuleOn } from '@/stores/modules'
import { gitpublishApi, gitpublishKeys } from './api'
import { IntentSheet } from './intent-sheet'
import { actionAuthority } from './model'
import { PublishDialog, type Draft } from './publish-dialog'
import type { PublicationEffect, PublicationTarget } from './types'
import './i18n'

export function SessionPublish({ run }: { run: RunDTO }) {
  const { t } = useTranslation('gitpublish')
  const { activeTenant, can } = useAuth()
  const moduleOn = useModuleOn('gitpublish')
  const workspace = run.authz_workspace_id
  // A cleaned session has no folder left to fetch from.
  const enabled =
    moduleOn &&
    can('gitpublish:target:read') &&
    can(actionAuthority('push').permission) &&
    !!workspace &&
    run.state !== 'cleaned'
  const targets = useQuery({
    queryKey: gitpublishKeys.targets(activeTenant),
    queryFn: () => gitpublishApi.targets(),
    enabled,
  })
  const [open, setOpen] = useState<{
    target: PublicationTarget
    effect: PublicationEffect
    initial: Draft
  } | null>(null)
  const [intentId, setIntentId] = useState<string | null>(null)

  // The engine reads the run in the TARGET's workspace, so only those targets can take it.
  const bound =
    targets.data?.items.filter((tg) => tg.workspace_id === workspace) ?? []
  if (!enabled) return null
  // A read that failed is said, never shown as "no target here".
  if (targets.isError)
    return (
      <QueryErrorState
        error={targets.error}
        subject={t('targets.title')}
        module="gitpublish"
        retry={() => void targets.refetch()}
      />
    )
  if (bound.length === 0) return null

  return (
    <section data-testid="session-publish" className="flex flex-col gap-2">
      <div>
        <h3 className="text-caption font-medium text-foreground">
          {t('session.title')}
        </h3>
        <p className="text-caption text-muted-foreground">
          {t('session.hint')}
        </p>
      </div>
      <ul className="flex flex-col gap-1">
        {bound.map((tg) => (
          <li key={tg.id} className="flex items-center justify-between gap-2">
            <code className="font-mono text-caption">{tg.push_prefix}</code>
            <Button
              size="sm"
              variant="outline"
              aria-label={
                bound.length > 1
                  ? t('session.publishTo', { prefix: tg.push_prefix })
                  : undefined
              }
              onClick={() =>
                setOpen({
                  target: tg,
                  effect: 'push',
                  initial: {
                    ref: startRef(tg.push_prefix, run.worktree_branch),
                  },
                })
              }
            >
              {t('session.publish')}
            </Button>
          </li>
        ))}
      </ul>

      {open ? (
        <PublishDialog
          // A new effect is a new decision: a fresh dialog, operation id and answer.
          key={open.effect}
          target={open.target}
          effect={open.effect}
          initial={open.initial}
          sessionRun={open.effect === 'push' ? run.run_ref : undefined}
          onClose={() => setOpen(null)}
          onOpenIntent={setIntentId}
          onPullRequest={(initial) =>
            setOpen({ target: open.target, effect: 'pull_request', initial })
          }
        />
      ) : null}
      {intentId ? (
        <IntentSheet
          intentId={intentId}
          onClose={() => setIntentId(null)}
          onOpenIntent={setIntentId}
        />
      ) : null}
    </section>
  )
}

/** The ref a session's push starts from: its worktree branch, under the target's push
 * prefix (the engine refuses any other), or the bare prefix for a session without one. */
function startRef(prefix: string, branch?: string): string {
  if (!branch) return `refs/heads/${prefix}`
  return `refs/heads/${branch.startsWith(prefix) ? branch : prefix + branch}`
}
