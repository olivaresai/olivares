// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useEffect, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ApiError } from '@/lib/api/errors'
import { queryKeys } from '@/lib/api/query'
import { workItemQueryKeys } from '@/lib/api/work-query-keys'
import type { Whoami } from '@/lib/api/types'
import { can } from '@/lib/auth/rbac'
import { useTenantStore } from '@/stores/tenant'
import { useSessionStore } from '@/stores/session'
import { useWorkspaceStore } from '@/stores/workspace'
import { useModulesStore } from '@/stores/modules'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  applyWork,
  buildIntent,
  classifyApplyFailure,
  getWorkItem,
  planWork,
  workErrorReason,
  type WorkIntent,
} from '@/features/work/api'
import type { Plan } from '@/features/work/types'
import type { EstateNode } from './types'
import { activeWorkDependencies } from './work-dependencies'

export interface DependencyChangeProps {
  tenant: string | null
  work: EstateNode
  dependency: EstateNode
  removeId?: string
  isCurrent: () => boolean
  onClose: () => void
  onConfirmed: () => void
  onRestoreFocus?: () => void
}

export function DependencyChange({
  tenant,
  work,
  dependency,
  removeId,
  isCurrent,
  onClose,
  onConfirmed,
  onRestoreFocus,
}: DependencyChangeProps) {
  const { t } = useTranslation('estate')
  const queryClient = useQueryClient()
  const controller = useRef<AbortController | null>(null)
  const [intent, setIntent] = useState<WorkIntent | null>(null)
  const [plan, setPlan] = useState<Plan | null>(null)
  const [phase, setPhase] = useState<
    'planning' | 'planned' | 'applying' | 'saved' | 'failed' | 'unknown'
  >('planning')
  const [message, setMessage] = useState('')
  const busy = useRef(false)

  function current(abort = controller.current) {
    if (!abort) throw new DOMException('Retired action', 'AbortError')
    if (
      !isCurrent() ||
      !can('sessions:work:write', {
        tenant,
        principal: queryClient.getQueryData<Whoami>(queryKeys.whoami) ?? null,
      })
    )
      abort.abort()
    abort.signal.throwIfAborted()
    return abort.signal
  }

  useEffect(() => {
    // Each effect owns its controller, including React StrictMode's first cancelled pass.
    const abort = new AbortController()
    controller.current = abort
    let cancelled = false
    const retire = () => {
      try {
        current(abort)
      } catch {
        if (!cancelled) {
          setMessage(t('stale'))
          setPhase('failed')
        }
      }
    }
    const unsubscribe = [
      useTenantStore.subscribe(retire),
      useSessionStore.subscribe(retire),
      useWorkspaceStore.subscribe(retire),
      useModulesStore.subscribe(retire),
      queryClient.getQueryCache().subscribe(retire),
    ]
    void (async () => {
      try {
        current(abort)
        const fresh = await getWorkItem(work.ref, { tenant }, abort.signal)
        current(abort)
        const present = activeWorkDependencies(fresh.snapshot).some(
          (row) => row.depends_on_id === dependency.ref,
        )
        // Reopening an ambiguous intention first resolves its current native state.
        if (present === !removeId) {
          if (!cancelled) {
            setPhase('saved')
            onConfirmed()
          }
          return
        }
        if (!fresh.etag) throw new Error(t('stale'))
        const next = buildIntent({
          tenant,
          command: removeId ? 'dependency.remove' : 'dependency.add',
          itemId: work.ref,
          dependencyId: removeId,
          etag: fresh.etag,
          body: removeId ? {} : { depends_on_id: dependency.ref },
        })
        const result = await planWork(next, abort.signal)
        current(abort)
        if (cancelled) return
        setIntent(next)
        setPlan(result)
        if (
          result.verdict !== 'LIMPIO' ||
          typeof result.plan_hash !== 'string' ||
          !result.plan_hash.trim()
        ) {
          setMessage(
            typeof result.code === 'string' && result.code
              ? `${t('invalidPlan')} (${result.code})`
              : t('invalidPlan'),
          )
          setPhase('failed')
        } else setPhase('planned')
      } catch (error) {
        if (cancelled || abort.signal.aborted) return
        setMessage(
          error instanceof TypeError
            ? t('workReadUnavailable')
            : workErrorReason(error) ||
                (error instanceof Error ? error.message : t('invalidPlan')),
        )
        setPhase('failed')
      }
    })()
    return () => {
      cancelled = true
      abort.abort()
      for (const stop of unsubscribe) stop()
    }
    // The parent keys one dialog by one intended relationship and authority lifetime.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  async function readBack() {
    const signal = current()
    const fresh = await getWorkItem(work.ref, { tenant }, signal)
    current()
    const present = activeWorkDependencies(fresh.snapshot).some(
      (row) => row.depends_on_id === dependency.ref,
    )
    if (present !== !removeId) return false
    setPhase('saved')
    void queryClient.invalidateQueries({
      queryKey: workItemQueryKeys.detail(tenant, work.ref),
    })
    void queryClient.invalidateQueries({
      queryKey: workItemQueryKeys.collection(tenant),
    })
    onConfirmed()
    return true
  }

  async function apply() {
    if (!intent || !plan || busy.current || phase !== 'planned') return
    busy.current = true
    setPhase('applying')
    try {
      const signal = current()
      const result = await applyWork(intent, plan.plan_hash, signal)
      current()
      if (result.result.verdict !== 'LIMPIO' || !(await readBack()))
        setPhase('unknown')
    } catch (error) {
      if (controller.current?.signal.aborted) {
        setMessage(t('stale'))
        setPhase('failed')
        return
      }
      const failure = classifyApplyFailure(error)
      if (!(error instanceof ApiError) || failure === 'unknown') {
        try {
          if (await readBack()) return
        } catch {
          /* Outcome remains unknown. No automatic retry. */
        }
        if (controller.current?.signal.aborted) return
        setPhase('unknown')
      } else {
        setMessage(
          failure === 'conflict-version' || failure === 'plan-changed'
            ? t('stale')
            : workErrorReason(error) || error.message,
        )
        setPhase('failed')
      }
    } finally {
      busy.current = false
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && phase !== 'applying') onClose()
      }}
    >
      <DialogContent
        hideClose={phase === 'applying'}
        onCloseAutoFocus={(event) => {
          if (onRestoreFocus) {
            event.preventDefault()
            onRestoreFocus()
          }
        }}
      >
        <DialogHeader>
          <DialogTitle>
            {removeId ? t('removeDependency') : t('addDependency')}
          </DialogTitle>
          <DialogDescription>
            {t('dependsOn', { work: work.label, dependency: dependency.label })}
          </DialogDescription>
        </DialogHeader>
        <div
          role={phase === 'failed' ? 'alert' : 'status'}
          aria-live="polite"
          className="text-body break-words"
        >
          {phase === 'planning'
            ? t('planning')
            : phase === 'applying'
              ? t('applying')
              : phase === 'saved'
                ? t('saved')
                : phase === 'unknown'
                  ? t('unknown')
                  : phase === 'failed'
                    ? message
                    : t('review')}
        </div>
        <DialogFooter>
          <Button disabled={phase === 'applying'} onClick={onClose}>
            {t('close')}
          </Button>
          {phase === 'planned' && (
            <Button variant="primary" onClick={() => void apply()}>
              {t('apply')}
            </Button>
          )}
          {phase === 'unknown' && (
            <Button
              onClick={() => void readBack().catch(() => setPhase('unknown'))}
            >
              {t('readBack')}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
