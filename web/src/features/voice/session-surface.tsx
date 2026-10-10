// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// The live surface of one voice session: three engine routes the console never requested
//.
//
// `modules/voice/api.go:25-31` serves eight routes; the console called three:
// `/sessions`, `/sessions/{ref}`, and `/policies`. Missing were
// `GET /sessions/{ref}/stream`, `GET /sessions/{ref}/decisions`, and `POST /sessions/open`:
// the ability to watch a session live, inspect its decisions, and open it.
//
// Four engine facts govern this surface:
//
// `/stream` is SSE, not JSON (`stream.go:117`, `text/event-stream`). The shared HTTP client
// parses JSON and would return `undefined` on success. Use `useLiveStream`, which exists
// because `EventSource` cannot send the bearer or tenant header (`shared/sse.ts:11-12`).
//
// Render the real connection state. The hook returns connecting|open|closed|error and
// never fakes live status. A fixed green dot would conceal a disconnected session.
//
// Open requires admin and has five outcomes (`policies.go:262-372`):
//   - 403 with `policy_verdict: denied`: a default-deny policy decision;
//   - 202 with `op_status: requested` and `approval_ref`: a second phase requiring
//   resubmission;
//   - `gate_status: no_gate`: no approval gate is connected, a deployment gap rather than
//   denial;
//   - 502 with "approval gate unavailable": the check could not be completed;
//   - `dispatch_ref`: the session actually opened.
// A generic failure for 403 misrepresents a deliberate boundary; treating `no_gate` as
// denied hides a deployment gap.
//
// The 403 body is available: the client deliberately preserves `ApiError.body` for
// status, approval_ref, and detail under 403/409/503 (`lib/api/errors.ts:23-30`). Without
// it, the five outcomes would collapse to two.
import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useLiveStream } from '@/features/shared/sse'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { voiceApi, voiceKeys } from './api'
import type { VoiceDecision, VoiceOpenInput, VoiceOpenResponse } from './types'

const STREAM_VARIANT: Record<
  string,
  'success' | 'warning' | 'danger' | 'neutral'
> = {
  open: 'success',
  connecting: 'warning',
  closed: 'neutral',
  error: 'danger',
}

/** Los cinco desenlaces, derivados del cuerpo y del estado — no de «hubo error o no». */
export function classifyOpen(
  res: VoiceOpenResponse | null,
  err: unknown,
): 'opened' | 'approval' | 'denied' | 'noGate' | 'unavailable' | 'idle' {
  if (err instanceof ApiError) {
    const body = err.body as VoiceOpenResponse | null
    // `no_gate` primero: una respuesta puede venir denegada Y sin puerta, y decir sólo
    // «denegado» esconde que no hay nada que aprobar en este despliegue.
    if (body?.gate_status === 'no_gate') return 'noGate'
    if (body?.policy_verdict === 'denied') return 'denied'
    return 'unavailable'
  }
  if (!res) return 'idle'
  if (res.gate_status === 'no_gate') return 'noGate'
  if (res.dispatch_ref) return 'opened'
  if (res.op_status === 'requested') return 'approval'
  return 'idle'
}

function Decisions({ sessionRef }: { sessionRef: string }) {
  const { t } = useTranslation('voice')
  const { activeTenant } = useAuth()
  const q = useQuery({
    queryKey: voiceKeys.decisions(activeTenant, sessionRef),
    queryFn: () => voiceApi.decisions(sessionRef),
  })
  const items: VoiceDecision[] = q.data?.items ?? []
  if (q.isPending)
    return (
      <p className="text-muted-foreground text-body">{t('surface.loading')}</p>
    )
  if (q.isError)
    return (
      <p role="alert" className="text-danger text-body">
        {t('surface.decisionsError')}
      </p>
    )
  if (items.length === 0)
    return (
      <p className="text-muted-foreground text-body">
        {t('surface.noDecisions')}
      </p>
    )
  return (
    <ul>
      {items.map((d) => (
        <li
          key={d.id}
          className="border-border border-b py-1.5 text-body last:border-b-0"
        >
          <div className="flex flex-wrap items-center gap-2">
            <Badge
              variant={d.policy_verdict === 'denied' ? 'danger' : 'success'}
            >
              {d.policy_verdict}
            </Badge>
            <Badge variant="outline">{d.op}</Badge>
            <Badge variant="outline">{d.gate_status}</Badge>
            <span className="font-mono text-caption break-all">
              {d.requested_model_ref}
            </span>
          </div>
          {/* `result` es `omitempty`: ausente no es «sin motivo», es que no lo hay escrito. */}
          <p className="text-muted-foreground text-caption">
            {d.result || t('surface.noReason')} · {d.occurred_at}
          </p>
        </li>
      ))}
    </ul>
  )
}

export function VoiceSessionSurface({
  sessionRef,
}: {
  sessionRef: string | null
}) {
  const { t } = useTranslation('voice')
  const [frames, setFrames] = useState(0)
  const { status } = useLiveStream<unknown>({
    path: `/v1/m/voice/sessions/${encodeURIComponent(sessionRef ?? '')}/stream`,
    events: ['session'],
    enabled: Boolean(sessionRef),
    onSnapshot: () => setFrames((n) => n + 1),
  })

  if (!sessionRef)
    return (
      <p
        className="text-muted-foreground text-body"
        data-testid="voice-surface-idle"
      >
        {t('surface.chooseSession')}
      </p>
    )

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center gap-2">
        {/* El estado real de la conexión, no un punto verde fijo. */}
        <Badge variant={STREAM_VARIANT[status] ?? 'neutral'}>
          {t(`surface.stream.${status}`)}
        </Badge>
        <span className="text-muted-foreground text-caption">
          {t('surface.frames', { count: frames })}
        </span>
      </div>
      <Decisions sessionRef={sessionRef} />
    </div>
  )
}

export function GovernedOpen({ input }: { input: VoiceOpenInput }) {
  const { t } = useTranslation('voice')
  const { can } = useAuth()
  const m = useMutation({
    mutationFn: (body: VoiceOpenInput) => voiceApi.open(body),
  })
  // El literal EXACTO que gatea el motor (`voice.go:32`) y que su módulo DECLARA
  // (`api.go:18`): un permiso que el motor no declarara saldría false para todos y
  // ocultaría este botón sin un solo 403.
  if (!can('voice:session:admin')) return null

  const outcome = classifyOpen(m.data ?? null, m.error)
  return (
    <div className="flex flex-col gap-2">
      <Button
        size="sm"
        className="w-fit"
        disabled={m.isPending}
        onClick={() => m.mutate(input)}
      >
        {t('surface.open')}
      </Button>
      {outcome !== 'idle' && (
        <p role="status" className="text-body">
          {t(`surface.outcome.${outcome}`)}
          {outcome === 'approval' && m.data?.approval_ref ? (
            <>
              {' '}
              <span className="font-mono text-caption break-all">
                {m.data.approval_ref}
              </span>
            </>
          ) : null}
        </p>
      )}
    </div>
  )
}
