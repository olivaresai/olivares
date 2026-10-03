// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation } from '@tanstack/react-query'
import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from '@/components/ui/toaster'
import { isUnknownVerdict, workErrorCode } from '@/features/work/api'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { agentOpsApi } from './api'
import { AuthorityLostError, useAuthBoundary } from './auth-boundary'
import type { RunDTO } from './types'
import { currentControlFence, isWorkBound } from './work-fence'
import './i18n'

export interface TurnInterrupt {
  /** Shown: a bridged run and a person who may write to runs. */
  offered: boolean
  /** Pressable now: the run is running and a work-bound run carries its fence. */
  allowed: boolean
  /** A work-bound run without a usable fence: the control waits for a refresh. */
  fenceUnavailable: boolean
  pending: boolean
  interrupt: () => void
}

/**
 * INTERRUPT THE CURRENT TURN, keep the session. One control, used by the live console
 * and the session header (HU 025: the header offered only Stop, which ends the
 * session). Every bridged (stream-json) run can interrupt its turn: protocol drivers
 * through their own method, Claude Code through its stream-json control request.
 */
export function useTurnInterrupt(run: RunDTO | undefined): TurnInterrupt {
  const { t } = useTranslation('agentops')
  const { can } = useAuth()
  const boundary = useAuthBoundary()
  const workBound = !!run && isWorkBound(run)
  const workFenceValid =
    !workBound ||
    (Number.isSafeInteger(run?.work_lease_fence) &&
      (run?.work_lease_fence ?? 0) > 0)
  const offered =
    !!run && run.transport !== 'remote-control' && can('sessions:run:write')
  const allowed = offered && run?.state === 'running' && workFenceValid
  // Recheck the current permission and target at dispatch. An intent captured
  // before a tenant, credential, run or fence change must not address its successor.
  const intent = `${boundary.epoch}:${run?.run_ref ?? '-'}:${run?.work_lease_fence ?? '-'}`
  const isAuthorized = () => can('sessions:run:write')
  const current = useRef({ isAuthorized, allowed, intent })
  useEffect(() => {
    current.current = { isAuthorized, allowed, intent }
  })
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])
  const mutation = useMutation({
    mutationFn: async (asked: string) => {
      const stillAsked = () => {
        const now = current.current
        return (
          mounted.current &&
          now.allowed &&
          now.isAuthorized() &&
          now.intent === asked
        )
      }
      if (!run || !stillAsked()) throw new AuthorityLostError()
      const fence = workBound ? await currentControlFence(run) : undefined
      // The lease read is a round trip: the intent is checked again before the control goes.
      if (!stillAsked()) throw new AuthorityLostError()
      return agentOpsApi.interrupt(run.run_ref, fence)
    },
    onSuccess: (_, asked) => {
      if (mounted.current && current.current.intent === asked)
        toast.success(t('live.interrupted'))
    },
    onError: (err, asked) => {
      if (
        !mounted.current ||
        current.current.intent !== asked ||
        err instanceof AuthorityLostError
      )
        return
      if (isUnknownVerdict(err)) {
        toast.warning(t('live.interruptUnknown'))
        return
      }
      const code = workErrorCode(err)
      if (code === 'stale_fence' || code === 'dispatch_conflict') {
        toast.warning(t('live.interruptWorkConflict'))
        return
      }
      toast.error(
        err instanceof ApiError ? err.message : t('live.interruptFailed'),
      )
    },
  })
  return {
    offered,
    allowed,
    fenceUnavailable: offered && !workFenceValid,
    pending: mutation.isPending,
    interrupt: () => mutation.mutate(intent),
  }
}
