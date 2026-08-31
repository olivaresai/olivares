// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ApiError } from '@/lib/api/errors'
import { useStepUpStore } from '@/stores/step-up'
import { useTenantStore } from '@/stores/tenant'
import { usePrivilegedMutation } from './use-privileged-mutation'

// Capture toast calls without rendering the real sonner surface.
const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast }))

function makeWrapper(qc: QueryClient) {
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  )
}

describe('usePrivilegedMutation (confirm → mutate → invalidate → toast → done)', () => {
  beforeEach(() => {
    toast.success.mockClear()
    toast.error.mockClear()
    toast.warning.mockClear()
    useStepUpStore.setState({ request: null })
    useTenantStore.setState({ activeTenant: null })
  })

  it('invalidates the affected keys, toasts success and calls onDone', async () => {
    const qc = new QueryClient()
    const invalidate = vi.spyOn(qc, 'invalidateQueries')
    const onDone = vi.fn()
    const { result } = renderHook(
      () =>
        usePrivilegedMutation<number, number>({
          mutationFn: async (v) => v + 1,
          invalidateKeys: [['servers', 't1']],
          successMessage: 'Saved',
          onDone,
        }),
      { wrapper: makeWrapper(qc) },
    )

    result.current.mutate(1)

    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith('Saved', undefined),
    )
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['servers', 't1'] })
    expect(onDone).toHaveBeenCalledWith(2, 1)
    expect(toast.error).not.toHaveBeenCalled()
  })

  it('treats a 403 as a calm "not authorized" warning, not an error', async () => {
    const qc = new QueryClient()
    const { result } = renderHook(
      () =>
        usePrivilegedMutation({
          mutationFn: async () => {
            throw new ApiError(403, 'forbidden', 'nope')
          },
          successMessage: 'unused',
        }),
      { wrapper: makeWrapper(qc) },
    )

    result.current.mutate(undefined)

    await waitFor(() => expect(toast.warning).toHaveBeenCalledOnce())
    expect(toast.error).not.toHaveBeenCalled()
    expect(toast.success).not.toHaveBeenCalled()
  })

  // THE defect this suite exists for: the engine answers 403 with TWO different
  // codes and the hook branched on the STATUS, so an assurance demand was reported
  // as "your role can't perform this action" — an accusation against an operator
  // who holds the role, sending them to ask for a permission they already have.
  it('routes a step_up_required 403 to the ceremony, never to the role toast', async () => {
    const qc = new QueryClient()
    const { result } = renderHook(
      () =>
        usePrivilegedMutation({
          mutationFn: async () => {
            throw new ApiError(403, 'step_up_required', 'step-up required')
          },
          successMessage: 'unused',
          stepUpAction: 'console',
        }),
      { wrapper: makeWrapper(qc) },
    )

    result.current.mutate(undefined)

    await waitFor(() =>
      expect(useStepUpStore.getState().request).not.toBeNull(),
    )
    expect(useStepUpStore.getState().request?.action).toBe('console')
    // The lie is the assertion: no toast may claim this was a role problem, and
    // no generic red error either.
    expect(toast.warning).not.toHaveBeenCalled()
    expect(toast.error).not.toHaveBeenCalled()
  })

  // The non-firing direction. Without this, a hook that opened the ceremony for
  // EVERY 403 would pass the test above while destroying the role case — a
  // control that fires for everything measures nothing.
  it('leaves a plain forbidden 403 on the role toast and opens no ceremony', async () => {
    const qc = new QueryClient()
    const { result } = renderHook(
      () =>
        usePrivilegedMutation({
          mutationFn: async () => {
            throw new ApiError(403, 'forbidden', 'nope')
          },
          successMessage: 'unused',
        }),
      { wrapper: makeWrapper(qc) },
    )

    result.current.mutate(undefined)

    await waitFor(() => expect(toast.warning).toHaveBeenCalledOnce())
    expect(useStepUpStore.getState().request).toBeNull()
  })

  it('resumes the refused call once the backend has elevated the session', async () => {
    const qc = new QueryClient()
    const mutationFn = vi
      .fn<(v: number) => Promise<string>>()
      .mockRejectedValueOnce(
        new ApiError(403, 'step_up_required', 'step-up required'),
      )
      .mockResolvedValueOnce('ok')
    const { result } = renderHook(
      () =>
        usePrivilegedMutation<number, string>({
          mutationFn,
          successMessage: 'Saved',
        }),
      { wrapper: makeWrapper(qc) },
    )

    result.current.mutate(7)

    await waitFor(() =>
      expect(useStepUpStore.getState().request).not.toBeNull(),
    )
    // What the host does when the ceremony succeeds.
    useStepUpStore.getState().request?.retry?.()

    await waitFor(() => expect(toast.success).toHaveBeenCalledOnce())
    // Resumed with the SAME variables — the operator does not refill the form.
    // (react-query passes its own context as a second argument; only the vars
    // are ours to assert.)
    expect(mutationFn).toHaveBeenCalledTimes(2)
    expect(mutationFn.mock.calls[0][0]).toBe(7)
    expect(mutationFn.mock.calls[1][0]).toBe(7)
  })

  it('NO reanuda en otra organización — la ceremonia dura, y el selector sigue abierto', async () => {
    // ⛔ Lo señaló el contraste de. Reanudar es «la misma acción» por diseño, pero sólo
    //    conserva `vars`: si el operador cambia de organización mientras resuelve el step-up,
    //    repetir la llamada aquí aplicaría en la nueva lo que pidió en la vieja. `mutations.retry`
    //    es `false` y no protege, porque esto no es un reintento: es una llamada nueva.
    useTenantStore.setState({ activeTenant: 'org-A' })
    const qc = new QueryClient()
    const mutationFn = vi
      .fn<(v: number) => Promise<string>>()
      .mockRejectedValueOnce(
        new ApiError(403, 'step_up_required', 'step-up required'),
      )
      .mockResolvedValueOnce('ok')
    const { result } = renderHook(
      () =>
        usePrivilegedMutation<number, string>({
          mutationFn,
          successMessage: 'Saved',
        }),
      { wrapper: makeWrapper(qc) },
    )

    result.current.mutate(7)
    await waitFor(() =>
      expect(useStepUpStore.getState().request).not.toBeNull(),
    )
    // El operador cambia de organización MIENTRAS la ceremonia está abierta.
    useTenantStore.setState({ activeTenant: 'org-B' })
    useStepUpStore.getState().request?.retry?.()

    await waitFor(() => expect(toast.warning).toHaveBeenCalledOnce())
    // Cota: la primera llamada SÍ ocurrió (o sea, el caso llegó a la ceremonia) …
    expect(mutationFn.mock.calls[0][0]).toBe(7)
    // … y no hubo segunda.
    expect(mutationFn).toHaveBeenCalledTimes(1)
    expect(toast.success).not.toHaveBeenCalled()
  })

  it('CONTRAFACTUAL · con la MISMA organización la reanudación sigue funcionando', async () => {
    useTenantStore.setState({ activeTenant: 'org-A' })
    const qc = new QueryClient()
    const mutationFn = vi
      .fn<(v: number) => Promise<string>>()
      .mockRejectedValueOnce(
        new ApiError(403, 'step_up_required', 'step-up required'),
      )
      .mockResolvedValueOnce('ok')
    const { result } = renderHook(
      () =>
        usePrivilegedMutation<number, string>({
          mutationFn,
          successMessage: 'Saved',
        }),
      { wrapper: makeWrapper(qc) },
    )

    result.current.mutate(7)
    await waitFor(() =>
      expect(useStepUpStore.getState().request).not.toBeNull(),
    )
    useStepUpStore.getState().request?.retry?.()

    await waitFor(() => expect(toast.success).toHaveBeenCalledOnce())
    expect(mutationFn).toHaveBeenCalledTimes(2)
  })

  it('spends the automatic resume once, so a still-refusing engine cannot loop', async () => {
    const qc = new QueryClient()
    const mutationFn = vi
      .fn<(v: void) => Promise<string>>()
      .mockRejectedValue(
        new ApiError(403, 'step_up_required', 'step-up required'),
      )
    const { result } = renderHook(
      () =>
        usePrivilegedMutation<void, string>({
          mutationFn,
          successMessage: 'unused',
        }),
      { wrapper: makeWrapper(qc) },
    )

    result.current.mutate(undefined)
    await waitFor(() =>
      expect(useStepUpStore.getState().request).not.toBeNull(),
    )
    const first = useStepUpStore.getState().request
    expect(first?.retry).toBeDefined()
    useStepUpStore.setState({ request: null })
    first?.retry?.()

    // Second refusal: the ceremony opens again (the operator is told), but the
    // automatic resume is spent — nothing re-fires on its own.
    await waitFor(() =>
      expect(useStepUpStore.getState().request).not.toBeNull(),
    )
    expect(useStepUpStore.getState().request?.retry).toBeUndefined()
    expect(mutationFn).toHaveBeenCalledTimes(2)
  })

  //-CX-02 (Codex sol max): the resume budget belonged to the HOOK, not to the
  // action. Once one action had spent its resume, every LATER action from the same
  // component was refused a retry — the operator completed a ceremony and the
  // panel simply closed.
  it('gives a NEW action its own resume budget after an earlier one spent theirs', async () => {
    const qc = new QueryClient()
    const mutationFn = vi
      .fn<(v: number) => Promise<string>>()
      .mockRejectedValue(
        new ApiError(403, 'step_up_required', 'step-up required'),
      )
    const { result } = renderHook(
      () =>
        usePrivilegedMutation<number, string>({
          mutationFn,
          successMessage: 'ok',
        }),
      { wrapper: makeWrapper(qc) },
    )

    // Action 1 burns its automatic resume: refuse, resume, refuse again.
    result.current.mutate(1)
    await waitFor(() =>
      expect(useStepUpStore.getState().request?.retry).toBeDefined(),
    )
    const first = useStepUpStore.getState().request
    useStepUpStore.setState({ request: null })
    first?.retry?.()
    await waitFor(() =>
      expect(useStepUpStore.getState().request).not.toBeNull(),
    )
    expect(useStepUpStore.getState().request?.retry).toBeUndefined()
    useStepUpStore.setState({ request: null })

    // Action 2 is a fresh operator-initiated action. It must be resumable.
    result.current.mutate(2)
    await waitFor(() =>
      expect(useStepUpStore.getState().request).not.toBeNull(),
    )
    expect(useStepUpStore.getState().request?.retry).toBeDefined()
  })

  //-CX-03 (Codex sol max): a second demand arriving while a ceremony is open
  // was dropped on the floor — no panel, no toast, no retry. The action vanished.
  it('says so when a second demand cannot open its own ceremony', async () => {
    const qc = new QueryClient()
    const { result } = renderHook(
      () =>
        usePrivilegedMutation({
          mutationFn: async () => {
            throw new ApiError(403, 'step_up_required', 'step-up required')
          },
          successMessage: 'unused',
        }),
      { wrapper: makeWrapper(qc) },
    )

    // A ceremony from some OTHER action is already on screen.
    useStepUpStore.setState({ request: { action: 'other' } })

    result.current.mutate(undefined)

    await waitFor(() => expect(toast.warning).toHaveBeenCalledOnce())
    // The one on screen is untouched — swapping it would strand its retry.
    expect(useStepUpStore.getState().request?.action).toBe('other')
    expect(toast.error).not.toHaveBeenCalled()
  })

  it('surfaces the engine message on a real failure', async () => {
    const qc = new QueryClient()
    const { result } = renderHook(
      () =>
        usePrivilegedMutation({
          mutationFn: async () => {
            throw new ApiError(409, 'conflict', 'already exists')
          },
          successMessage: 'unused',
        }),
      { wrapper: makeWrapper(qc) },
    )

    result.current.mutate(undefined)

    await waitFor(() => expect(toast.error).toHaveBeenCalledOnce())
    expect(toast.error).toHaveBeenCalledWith(expect.any(String), {
      description: 'already exists',
    })
    expect(toast.warning).not.toHaveBeenCalled()
  })

  it('lets a feature claim an error it renders itself', async () => {
    const qc = new QueryClient()
    const claimed = vi.fn().mockReturnValue(true)
    const { result } = renderHook(
      () =>
        usePrivilegedMutation({
          mutationFn: async () => {
            throw new ApiError(409, 'pin_version_conflict', 'moved')
          },
          successMessage: 'unused',
          onError: claimed,
        }),
      { wrapper: makeWrapper(qc) },
    )

    result.current.mutate(undefined)

    await waitFor(() => expect(claimed).toHaveBeenCalledOnce())
    // The feature is rendering its own explanation; a generic red toast on top of it
    // would contradict the panel the operator is reading.
    expect(toast.error).not.toHaveBeenCalled()
    expect(toast.warning).not.toHaveBeenCalled()
  })

  it('NEVER lets a feature swallow the STEP-UP demand either', async () => {
    // El step-up es un 403 CON ceremonia detrás. Si el callback de la feature se
    // consulta antes de resolverlo, un `() => true` lo silencia y el operador ve la
    // acción privilegiada no hacer nada — que es el defecto que #703 cerró.
    toast.warning.mockClear()
    toast.error.mockClear()
    const claimsEverything = vi.fn().mockReturnValue(true)
    const { result } = renderHook(
      () =>
        usePrivilegedMutation({
          mutationFn: async () => {
            throw new ApiError(403, 'step_up_required', 'step-up required')
          },
          successMessage: 'unused',
          stepUpAction: 'console',
          onError: claimsEverything,
        }),
      { wrapper: makeWrapper(new QueryClient()) },
    )
    result.current.mutate(undefined)
    // ANCLA POSITIVA PRIMERO. Un `waitFor(() => expect(x).not.toHaveBeenCalled())`
    // se cumple en el PRIMER tick, antes de que la mutación falle siquiera: la
    // aserción sería vacua y la celda pasaría con el defecto puesto. Se espera a que
    // la ceremonia EXISTA y sólo entonces se afirma que el callback no la vio.
    await waitFor(() =>
      expect(useStepUpStore.getState().request).not.toBeNull(),
    )
    expect(claimsEverything).not.toHaveBeenCalled()
  })

  it('NEVER lets a feature swallow the authorization boundary', async () => {
    // The pathological caller: claims everything. A 401/403 must still surface, or an
    // operator sees a privileged action quietly do nothing when the answer was "you may
    // not" — and the shared hook is where that has to be unreachable, because the option
    // is new and nothing else can stop a `() => true` written in a hurry.
    for (const [status, code] of [
      [403, 'forbidden'],
      [401, 'unauthenticated'],
    ] as const) {
      toast.warning.mockClear()
      toast.error.mockClear()
      const claimsEverything = vi.fn().mockReturnValue(true)
      const { result } = renderHook(
        () =>
          usePrivilegedMutation({
            mutationFn: async () => {
              throw new ApiError(status, code, 'nope')
            },
            successMessage: 'unused',
            onError: claimsEverything,
          }),
        { wrapper: makeWrapper(new QueryClient()) },
      )

      result.current.mutate(undefined)

      await waitFor(() => expect(toast.warning).toHaveBeenCalledOnce())
      expect(toast.error).not.toHaveBeenCalled()
      // Not merely "the toast appeared": the callback must never have been consulted,
      // which is what makes the boundary non-delegable rather than merely defended.
      expect(claimsEverything).not.toHaveBeenCalled()
    }
  })
})
