// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Bring your folders: the folders the person's tools already worked in, read by the
// engine from those tools' own records of project paths and dates (never a credential,
// never conversation text). Nothing is registered until the person confirms; each
// confirmed folder goes through the one folder registration the New session form uses
// (ensureFolder), so importing never widens what a session can reach.
import { useQuery } from '@tanstack/react-query'
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { XScroll } from '@/components/data/scroll-edges'
import { StaticTable } from '@/components/data/static-table'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { PathText } from '@/components/ui/path-text'
import { Spinner } from '@/components/ui/spinner'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { ensureFolder } from '@/features/agentops/session-launch'
import type { FolderCandidateDTO } from '@/features/agentops/types'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { formatRelativeTime } from '@/lib/format'
import {
  failureText,
  usePrivilegedMutation,
} from '@/lib/hooks/use-privileged-mutation'
import { preferenceStorage } from '@/lib/preference-storage'
import './i18n'

const rowKey = (row: FolderCandidateDTO) => `${row.tool}\n${row.path}`

/** What became of one confirmed folder: `reason` is null when it was registered. */
interface Outcome {
  path: string
  reason: string | null
}

/** What the person's tools already worked in, read by the engine. */
function useFolderCandidates(enabled = true) {
  const boundary = useAuthBoundary()
  const candidatesKey = [
    ...agentOpsKeys.boundaryScope(boundary.tenant, boundary.epoch),
    'folder-candidates',
  ]
  const query = useQuery({
    queryKey: candidatesKey,
    queryFn: ({ signal }) =>
      agentOpsApi.listFolderCandidates({ tenant: boundary.tenant, signal }),
    enabled: enabled && !!boundary.tenant,
  })
  return { boundary, candidatesKey, query }
}

/** The folders offered, the person's picks, and the closing line, inside a dialog that
 * shows the title and what this is. `onSkip` leaves without registering anything. */
export function BringFolders({ onSkip }: { onSkip?: () => void }) {
  const { t } = useTranslation('firstHour')
  const { boundary, candidatesKey, query } = useFolderCandidates()
  const folders = query.data?.folders ?? []
  // Until the person picks, the selection is what the engine preselected.
  const [picked, setPicked] = useState<ReadonlySet<string> | null>(null)
  const selected =
    picked ??
    new Set(folders.filter((f) => f.preselected).map((f) => rowKey(f)))
  const toggle = (row: FolderCandidateDTO) => {
    const next = new Set(selected)
    if (!next.delete(rowKey(row))) next.add(rowKey(row))
    setPicked(next)
  }
  // One registration per path, however many tools used it.
  const paths = [
    ...new Set(
      folders.filter((f) => selected.has(rowKey(f))).map((f) => f.path),
    ),
  ]

  const [outcomes, setOutcomes] = useState<Outcome[] | null>(null)
  // Remembered across a step-up retry, which runs the mutation again: a folder already
  // registered in the first attempt is not registered a second time.
  const settled = useRef(new Map<string, string | null>())
  const register = usePrivilegedMutation<string[], Outcome[]>({
    mutationFn: async (chosen, authority) => {
      for (const path of chosen) {
        if (settled.current.has(path)) continue
        try {
          await ensureFolder(path, { tenant: boundary.tenant, ...authority })
          settled.current.set(path, null)
        } catch (err) {
          // A missing step-up, right or edition is the privileged mutation's to report,
          // and a retry resumes where this one stopped. Any other refusal is this folder's.
          if (
            err instanceof ApiError &&
            (err.isStepUpRequired ||
              err.isForbiddenOrEdition ||
              err.isUnauthenticated)
          )
            throw err
          settled.current.set(path, failureText(err) ?? t('bring.failed'))
        }
      }
      // The folders the engine left out are kept with the outcome: the list is read again
      // after the import, and the closing line must not change with it.
      const leftOut = new Map(
        folders
          .filter((f) => f.skip)
          .map((f) => [f.path, t(`bring.skip.${f.skip}`)] as const),
      )
      return [
        ...chosen.map((path) => ({
          path,
          reason: settled.current.get(path) ?? null,
        })),
        ...[...leftOut].map(([path, reason]) => ({ path, reason })),
      ]
    },
    // Read again, so the folders just registered are offered no more.
    invalidateKeys: () => [
      agentOpsKeys.workspaces(boundary.tenant),
      candidatesKey,
    ],
    successMessage: (done) =>
      t('bring.toast', { n: done.filter((o) => o.reason === null).length }),
    onDone: setOutcomes,
  })

  if (query.isLoading) return <Spinner className="size-4" />
  if (query.isError)
    return (
      <QueryErrorState
        error={query.error}
        retry={() => void query.refetch()}
        title={t('bring.error')}
      />
    )
  const tools = query.data?.tools ?? []
  const unreadable = tools.filter((tool) => !tool.readable)
  // Nothing to bring and nothing unreadable to explain: the step has nothing to say.
  if (folders.length === 0 && unreadable.length === 0) return null

  // What was left out, with the reason: what the engine skipped, and what failed here.
  const skipped = (outcomes ?? [])
    .filter((o) => o.reason !== null)
    .map((o) => [o.path, o.reason as string] as const)
  const imported = (outcomes ?? []).filter((o) => o.reason === null).length

  return (
    <div
      className="flex flex-col gap-3"
      aria-label={t('bring.title')}
      role="group"
    >
      {unreadable.map((tool) => (
        <p key={tool.tool} role="status" className="text-body text-text-2">
          {t('bring.unreadable', { tool: tool.name })}
        </p>
      ))}
      {folders.length > 0 && !outcomes && (
        <XScroll contentKey={folders.length}>
          <StaticTable oneLine>
            <thead>
              <tr>
                <th />
                <th>{t('bring.cols.folder')}</th>
                <th>{t('bring.cols.tool')}</th>
                <th>{t('bring.cols.lastUsed')}</th>
                <th className="text-right">{t('bring.cols.conversations')}</th>
              </tr>
            </thead>
            <tbody>
              {folders.map((row) => (
                <tr key={rowKey(row)}>
                  <td>
                    <Checkbox
                      checked={selected.has(rowKey(row))}
                      disabled={!!row.skip || register.isPending}
                      onCheckedChange={() => toggle(row)}
                      aria-label={t('bring.pick', {
                        folder: `${row.path} (${toolLabel(tools, row.tool)})`,
                      })}
                    />
                  </td>
                  <td className="w-full max-w-0">
                    <PathText value={row.path} copy={false} />
                    {row.skip && (
                      <span className="block text-caption text-text-2">
                        {t(`bring.skip.${row.skip}`)}
                      </span>
                    )}
                  </td>
                  <td>{toolLabel(tools, row.tool)}</td>
                  <td>{formatRelativeTime(row.last_used)}</td>
                  <td className="text-right">{row.conversations}</td>
                </tr>
              ))}
            </tbody>
          </StaticTable>
        </XScroll>
      )}
      {!outcomes && (
        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="primary"
            size="sm"
            disabled={paths.length === 0 || register.isPending}
            onClick={() => register.mutate(paths)}
          >
            {t('bring.confirm', { n: paths.length })}
          </Button>
          {onSkip && (
            <Button variant="ghost" size="sm" onClick={onSkip}>
              {t('bring.skipStep')}
            </Button>
          )}
        </div>
      )}
      {outcomes && (
        <div
          role="status"
          className="flex flex-col gap-1 text-body text-text-2"
        >
          <p>{t('bring.result', { imported, skipped: skipped.length })}</p>
          {skipped.length > 0 && (
            <ul className="flex flex-col gap-0.5">
              {skipped.map(([path, reason]) => (
                <li key={path}>
                  <PathText value={path} copy={false} /> — {reason}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  )
}

const toolLabel = (
  tools: readonly { tool: string; name: string }[],
  key: string,
) => tools.find((tool) => tool.tool === key)?.name ?? key

const SKIPPED_KEY = 'olivares.bring-folders.skipped'

/** The first-run offer: one line, the list behind Review. It is shown to a person who
 * may register folders until they skip it on this browser; the folders page offers it
 * again whenever they want. */
export function BringFoldersStep() {
  const { t } = useTranslation('firstHour')
  const { can } = useAuth()
  const [skipped, setSkipped] = useState<boolean>(() => {
    try {
      return preferenceStorage.getItem(SKIPPED_KEY) === 'true'
    } catch {
      return false
    }
  })
  const [open, setOpen] = useState(false)
  const offered = !skipped && can('sessions:workspace:admin')
  const { query } = useFolderCandidates(offered)
  const count = (query.data?.folders ?? []).filter((f) => !f.skip).length
  if (!offered) return null
  // A read that failed says so, with Retry: never nothing, never the offer's words. An
  // open list says its own failure inside the dialog.
  if (query.isError && !open)
    return (
      <QueryErrorState
        error={query.error}
        retry={() => void query.refetch()}
        title={t('bring.error')}
      />
    )
  // Open stays open: the list is read again after an import, and its result is on screen.
  if (count === 0 && !open) return null
  return (
    <>
      <p className="text-body text-text-2">
        {t('bring.offer', { count })}{' '}
        <Button variant="link" onClick={() => setOpen(true)}>
          {t('bring.review')}
        </Button>
      </p>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-w-3xl">
          <DialogHeader>
            <DialogTitle>{t('bring.title')}</DialogTitle>
            <DialogDescription>{t('bring.body')}</DialogDescription>
          </DialogHeader>
          <BringFolders
            onSkip={() => {
              try {
                preferenceStorage.setItem(SKIPPED_KEY, 'true')
              } catch {
                // localStorage unavailable: the choice still holds for this visit
              }
              setOpen(false)
              setSkipped(true)
            }}
          />
        </DialogContent>
      </Dialog>
    </>
  )
}
