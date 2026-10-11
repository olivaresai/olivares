// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Field } from '@/components/ui/field'
import { getWorkItem } from '@/features/work/api'
import { DependencyChange } from './dependency-change'
import { EstateStatus } from './estate-status'
import { estateOwnerHref } from './owner-links'
import type { EstateNode } from './types'
import { activeWorkDependencies } from './work-dependencies'

export function EstateInspector({
  tenant,
  lifetime,
  node,
  nodes,
  sourceReadAt,
  canWriteWork,
  canOpenOwner,
  isCurrent,
  onClose,
  onRefresh,
  onRestoreFocus,
}: {
  tenant: string | null
  lifetime: number
  node: EstateNode
  nodes: EstateNode[]
  sourceReadAt?: string
  canWriteWork: boolean
  canOpenOwner: boolean
  isCurrent: () => boolean
  onClose: () => void
  onRefresh: () => void
  onRestoreFocus: () => void
}) {
  const { t, i18n } = useTranslation('estate')
  const [choice, setChoice] = useState('')
  const opener = useRef<HTMLButtonElement | null>(null)
  const [change, setChange] = useState<{
    dependency: EstateNode
    removeId?: string
  } | null>(null)
  const work = useQuery({
    queryKey: ['estate', tenant, lifetime, 'work-detail', node.ref],
    queryFn: async ({ signal }) => {
      const fresh = await getWorkItem(node.ref, { tenant }, signal)
      return {
        ...fresh,
        snapshot: {
          ...fresh.snapshot,
          dependencies: activeWorkDependencies(fresh.snapshot),
        },
      }
    },
    enabled: node.kind === 'work',
    retry: false,
    gcTime: 0,
  })
  const dependencies = work.error
    ? []
    : (work.data?.snapshot.dependencies ?? [])
  const related = nodes.filter(
    (other) =>
      (other.kind === 'folder' && other.ref === node.folderRef) ||
      (other.kind === 'workspace' && other.ref === node.workspaceId) ||
      (node.kind === 'workspace' && other.workspaceId === node.ref) ||
      (node.kind === 'folder' && other.folderRef === node.ref),
  )
  const visibleDependencies = dependencies.flatMap((row) => {
    const target = nodes.find(
      (other) => other.kind === 'work' && other.ref === row.depends_on_id,
    )
    return target ? [{ row, target }] : []
  })
  const candidates = nodes.filter(
    (other) =>
      other.kind === 'work' &&
      other.ref !== node.ref &&
      other.workspaceId === node.workspaceId &&
      !dependencies.some((row) => row.depends_on_id === other.ref),
  )
  const refresh = () => {
    void work.refetch()
    onRefresh()
  }

  return (
    <>
      <Sheet
        open
        onOpenChange={(open) => {
          if (!open) onClose()
        }}
      >
        <SheetContent
          className="overflow-y-auto"
          onCloseAutoFocus={(event) => {
            event.preventDefault()
            onRestoreFocus()
          }}
        >
          <SheetHeader>
            <SheetTitle className="break-words">{node.label}</SheetTitle>
            <SheetDescription>
              {t('kinds.' + node.kind)} · {t('configured')}
            </SheetDescription>
          </SheetHeader>
          <dl className="space-y-3 text-body">
            <div>
              <dt className="text-text-3">{t('reference')}</dt>
              <dd className="font-mono break-all">{node.ref}</dd>
            </div>
            <div>
              <dt className="text-text-3">{t('status')}</dt>
              <dd>
                <EstateStatus node={node} />
              </dd>
            </div>
          </dl>
          {sourceReadAt && (
            <p className="text-caption text-text-2">
              {t('readAt', {
                time: new Date(sourceReadAt).toLocaleString(i18n.language),
              })}
            </p>
          )}
          <h2 className="text-body font-semibold">{t('relationships')}</h2>
          <ul className="space-y-3 text-body">
            {related.map((other) => (
              <li key={other.kind + ':' + other.ref} className="break-words">
                {other.kind === 'folder' || node.kind === 'folder'
                  ? t('folderRelation', {
                      session:
                        node.kind === 'session' ? node.label : other.label,
                      folder: node.kind === 'folder' ? node.label : other.label,
                    })
                  : t('workspaceRelation', {
                      resource:
                        node.kind === 'workspace' ? other.label : node.label,
                      workspace:
                        node.kind === 'workspace' ? node.label : other.label,
                    })}
              </li>
            ))}
            {visibleDependencies.map(({ row, target }) => (
              <li key={row.id} className="flex flex-wrap items-center gap-2">
                <span className="min-w-0 break-words">
                  {t('dependsOn', {
                    work: node.label,
                    dependency: target.label,
                  })}
                </span>
                {canWriteWork && work.data?.etag && (
                  <Button
                    size="sm"
                    onClick={(event) => {
                      opener.current = event.currentTarget
                      setChange({ dependency: target, removeId: row.id })
                    }}
                  >
                    {t('removeDependency')}
                  </Button>
                )}
              </li>
            ))}
          </ul>
          {node.kind === 'session' && (
            <div className="space-y-2 text-body">
              <p>
                {node.peerMode === 'same-template'
                  ? t('peerRule')
                  : t('peerExplicit')}
              </p>
              <p className="text-text-2">{t('peerHelp')}</p>
            </div>
          )}
          {node.kind === 'work' && (
            <div className="space-y-3">
              {work.isFetching && <p role="status">{t('loading')}</p>}
              {work.error && <p role="alert">{t('workReadUnavailable')}</p>}
              {work.data && !work.error && visibleDependencies.length === 0 && (
                <p className="text-body text-text-2">{t('noDependencies')}</p>
              )}
              {canWriteWork &&
                work.isSuccess &&
                !work.error &&
                work.data.etag && (
                  <>
                    <Field
                      label={t('chooseDependency')}
                      htmlFor="estate-dependency"
                    >
                      <select
                        id="estate-dependency"
                        value={choice}
                        onChange={(event) => setChoice(event.target.value)}
                        className="min-h-11 w-full rounded-ctl border border-ctl-border bg-surface px-2 text-body"
                      >
                        <option value="">{t('chooseDependency')}</option>
                        {candidates.map((other) => (
                          <option key={other.ref} value={other.ref}>
                            {other.label}
                          </option>
                        ))}
                      </select>
                    </Field>
                    {candidates.length === 0 && (
                      <p className="text-caption text-text-2">
                        {t('noCandidates')}
                      </p>
                    )}
                    <Button
                      disabled={!choice}
                      onClick={(event) => {
                        const target = candidates.find(
                          (other) => other.ref === choice,
                        )
                        if (target) {
                          opener.current = event.currentTarget
                          setChange({ dependency: target })
                        }
                      }}
                    >
                      {t('addDependency')}
                    </Button>
                  </>
                )}
              <Button onClick={refresh}>{t('refresh')}</Button>
            </div>
          )}
          {related.length === 0 &&
            node.kind !== 'work' &&
            node.kind !== 'session' && (
              <p className="text-body text-text-2">{t('none')}</p>
            )}
          {canOpenOwner && (
            <Button asChild>
              <a href={estateOwnerHref(node)}>{t('openOwner')}</a>
            </Button>
          )}
        </SheetContent>
      </Sheet>
      {change && (
        <DependencyChange
          key={`${node.ref}:${change.dependency.ref}:${change.removeId ?? 'add'}`}
          tenant={tenant}
          work={node}
          dependency={change.dependency}
          removeId={change.removeId}
          isCurrent={isCurrent}
          onClose={() => setChange(null)}
          onConfirmed={refresh}
          onRestoreFocus={() => {
            if (opener.current?.isConnected) opener.current.focus()
            else document.getElementById('estate-dependency')?.focus()
          }}
        />
      )}
    </>
  )
}
