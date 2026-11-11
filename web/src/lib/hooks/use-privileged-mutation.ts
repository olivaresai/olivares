// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  useMutation,
  useQueryClient,
  type MutationKey,
  type QueryKey,
  type MutateOptions,
  type UseMutateFunction,
  type UseMutateAsyncFunction,
} from '@tanstack/react-query'
import { createElement, useCallback, useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import {
  EditionLine,
  editionRefusal,
  type EditionRefusal,
} from '@/components/ui/edition-note'
import { toast } from '@/components/ui/toaster'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import {
  useStepUpOwner,
  useStepUpStore,
  type StepUpDemand,
  type StepUpOwner,
  type StepUpAttempt,
} from '@/stores/step-up'
import { useResumeGuard } from './use-resume-guard'

/** The edition line in a toast, for whoever reads it: the license tab's own gate decides
 * whether its second sentence offers a license or asks for an administrator. */
function EditionToast({ refusal }: { refusal: EditionRefusal }) {
  const { isSuperadmin } = useAuth()
  return createElement(EditionLine, { refusal, canAddLicense: isSuperadmin })
}

/**
 * Says a failed write's edition refusal as the reporter does: the one edition line, calm,
 * naming the edition whole; an add-on refusal that names no edition keeps the engine's
 * sentence, which names the add-on and says reads and exports are unaffected. Returns
 * whether it was one, so a write outside the reporter keeps its own words for the rest.
 */
export function reportEditionRefusal(err: unknown): boolean {
  const refusal = editionRefusal(err)
  if (!refusal) return false
  if (refusal.edition) toast.info(createElement(EditionToast, { refusal }))
  else toast.warning(refusal.message)
  return true
}

/** The message a failed request carries, or undefined when it carries none. */
export function failureText(err: unknown): string | undefined {
  return err instanceof Error && err.message ? err.message : undefined
}

/** One reporting policy: assurance demand, role refusal, or ordinary failure.
 * A published continuation belongs to its mounted owner and its original context.
 * Hand-rolled callers capture at reporting; usePrivilegedMutation captures at dispatch.
 */
export function useFailedActionReporter(
  stepUpAction?: string,
  enrollment?: StepUpDemand['enrollment'],
) {
  const { t } = useTranslation(['common', 'errors'])
  const captureOwner = useStepUpOwner()
  const guardResume = useResumeGuard()
  return useCallback(
    (err: unknown, retry?: () => void, actionOwner?: StepUpOwner) => {
      if (err instanceof ApiError && err.isStepUpRequired) {
        const owner = actionOwner ?? captureOwner()
        if (owner.current()) {
          const accepted = useStepUpStore.getState().require({
            action: stepUpAction ?? 'generic',
            owner,
            enrollment,
            retry: retry
              ? guardResume(() => {
                  if (owner.current()) retry()
                })
              : undefined,
          })
          if (!accepted) toast.warning(t('common:privileged.stepUp.busyToast'))
          return
        }
      }
      if (
        err instanceof ApiError &&
        err.isForbidden &&
        err.code === 'tenant_admission_required'
      ) {
        toast.warning(t('errors:tenantAdmission.title'), {
          description: t('errors:tenantAdmission.description'),
        })
        return
      }
      // An edition matter is not a role refusal.
      if (reportEditionRefusal(err)) return
      if (err instanceof ApiError && err.isForbidden) {
        toast.warning(t('common:privileged.notAuthorizedToast'))
        return
      }
      const description = failureText(err)
      toast.error(
        t('errors:generic'),
        description ? { description } : undefined,
      )
    },
    [t, stepUpAction, enrollment, captureOwner, guardResume],
  )
}

export interface PrivilegedMutationOptions<TVars, TData> {
  mutationFn: (
    vars: TVars,
    authority: Pick<StepUpAttempt, 'signal' | 'dispatchGuard'>,
  ) => Promise<TData>
  invalidateKeys?: QueryKey[] | ((data: TData, vars: TVars) => QueryKey[])
  successMessage: string | ((data: TData, vars: TVars) => string)
  successDescription?: string
  /** The toast's intent when the request worked but its answer is bad news (a
   *  connection test that came back refused). Defaults to success. */
  successIntent?: (data: TData, vars: TVars) => 'success' | 'warning'
  onDone?: (data: TData, vars: TVars) => void
  stepUpAction?: string
  /** Add Connector owns an inline demand in its existing dialog. No other console
   * action opts into enrollment merely by sharing its translated action name. */
  stepUpEnrollment?: StepUpDemand['enrollment']
  /** Feature-owned errors only. Authorization and assurance cannot be suppressed. */
  onError?: (err: unknown, vars: TVars) => boolean
  /** Names this action's mutations in TanStack's MutationCache, so their owner can find
   *  and remove them later (for example, what a retired authority boundary left behind).
   *  Without it the mutations carry no key, as before. */
  mutationKey?: MutationKey
}

type ActionContext = { owner: StepUpOwner; resumed: boolean }
interface Execution<TVars> {
  vars: TVars
  context: ActionContext
  attempt: StepUpAttempt
}

// Keep the public API in feature variables. TanStack still controls observer
// mount semantics; retired executions cannot notify a replacement action.
function featureCallbacks<TData, TVars>(
  options?: MutateOptions<TData, unknown, TVars, ActionContext>,
): MutateOptions<TData, unknown, Execution<TVars>, ActionContext> | undefined {
  return (
    options && {
      onSuccess: (data, action, context, client) => {
        if (action.attempt.current())
          options.onSuccess?.(data, action.vars, context, client)
      },
      onError: (err, action, context, client) => {
        if (action.attempt.current())
          options.onError?.(err, action.vars, context, client)
      },
      onSettled: (data, err, action, context, client) => {
        if (action.attempt.current())
          options.onSettled?.(data, err, action.vars, context, client)
      },
    }
  )
}

export function usePrivilegedMutation<TVars = void, TData = unknown>(
  opts: PrivilegedMutationOptions<TVars, TData>,
) {
  const { t } = useTranslation(['common', 'errors'])
  const queryClient = useQueryClient()
  const captureOwner = useStepUpOwner()
  const report = useFailedActionReporter(
    opts.stepUpAction,
    opts.stepUpEnrollment,
  )
  const request = useStepUpStore((s) => s.request)
  const mutateRef = useRef<((execution: Execution<TVars>) => void) | null>(null)
  const mutation = useMutation<TData, unknown, Execution<TVars>, ActionContext>(
    {
      mutationKey: opts.mutationKey,
      mutationFn: async ({ vars, attempt }) => {
        // TanStack may wait before onMutate and again before calling mutationFn.
        // This authority was captured before either queue, not reconstructed here.
        attempt.dispatchGuard()
        const data = await opts.mutationFn(vars, {
          signal: attempt.signal,
          dispatchGuard: attempt.dispatchGuard,
        })
        attempt.dispatchGuard()
        return data
      },
      onMutate: ({ context }) => context,
      onSuccess: async (data, { vars, attempt }) => {
        if (!attempt.current()) return
        const keys =
          typeof opts.invalidateKeys === 'function'
            ? opts.invalidateKeys(data, vars)
            : (opts.invalidateKeys ?? [])
        await Promise.all(
          keys.map((queryKey) => queryClient.invalidateQueries({ queryKey })),
        )
        if (!attempt.current()) return
        const message =
          typeof opts.successMessage === 'function'
            ? opts.successMessage(data, vars)
            : opts.successMessage
        toast[opts.successIntent?.(data, vars) ?? 'success'](
          message,
          opts.successDescription
            ? { description: opts.successDescription }
            : undefined,
        )
        opts.onDone?.(data, vars)
      },
      onError: (err, execution) => {
        if (!execution.attempt.current()) return
        const { vars, context } = execution
        const isStepUp = err instanceof ApiError && err.isStepUpRequired
        // Every 403 is the reporter's, an edition refusal as much as a role refusal:
        // a feature's own words cannot replace either.
        if (
          !isStepUp &&
          err instanceof ApiError &&
          (err.isForbiddenOrEdition || err.isUnauthenticated)
        ) {
          if (err.isForbiddenOrEdition) report(err, undefined, context.owner)
          else toast.warning(t('common:privileged.notAuthorizedToast'))
          return
        }
        if (!isStepUp && opts.onError?.(err, vars)) return
        // Consume once, carrying the SAME owner, attempt and input through the queue.
        // A new explicit operator action alone captures a new owner and budget.
        let spent = context.resumed
        report(
          err,
          isStepUp && !spent
            ? () => {
                if (spent || !execution.attempt.current()) return
                spent = true
                mutateRef.current?.({
                  ...execution,
                  context: { ...context, resumed: true },
                })
              }
            : undefined,
          context.owner,
        )
      },
    },
  )
  useEffect(() => {
    mutateRef.current = mutation.mutate
  }, [mutation.mutate])

  const execution = useCallback(
    (vars: TVars): Execution<TVars> => {
      const owner = captureOwner()
      return {
        vars,
        context: { owner, resumed: false },
        attempt: owner.begin(),
      }
    },
    [captureOwner],
  )
  const { mutate: dispatch, mutateAsync: dispatchAsync } = mutation
  const mutate = useCallback<
    UseMutateFunction<TData, unknown, TVars, ActionContext>
  >(
    (...args) =>
      dispatch(execution(args[0] as TVars), featureCallbacks(args[1])),
    [dispatch, execution],
  )
  const mutateAsync = useCallback<
    UseMutateAsyncFunction<TData, unknown, TVars, ActionContext>
  >(
    (...args) =>
      dispatchAsync(execution(args[0] as TVars), featureCallbacks(args[1])),
    [dispatchAsync, execution],
  )
  return {
    ...mutation,
    mutate,
    mutateAsync,
    variables: mutation.variables?.vars,
    stepUpRequest: request?.owner === mutation.context?.owner ? request : null,
  }
}
