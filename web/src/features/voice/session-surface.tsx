// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// La superficie viva de UNA sesión de voz — las tres rutas que el motor sirve y la
// consola nunca pidió.
//
// `modules/voice/api.go:25-31` sirve OCHO rutas y la consola llamaba TRES: `/sessions`,
// `/sessions/{ref}` y `/policies`. Faltaban `GET /sessions/{ref}/stream`,
// `GET /sessions/{ref}/decisions` y `POST /sessions/open`, que juntas son lo que hace
// operable una sesión: verla en vivo, ver por qué se decidió lo que se decidió, y abrirla.
//
// CUATRO HECHOS DEL MOTOR MANDAN AQUÍ, Y NINGUNO ES UNA ELECCIÓN DE DISEÑO:
//
//   `/stream` ES SSE, NO JSON (`stream.go:117`, `text/event-stream`). Envolverlo con el
//   cliente HTTP compartido daría `undefined` en el ÉXITO — el cliente parsea JSON. Va por
//   `useLiveStream`, que existe justamente porque `EventSource` no puede mandar el bearer
//   ni la cabecera de tenant (`shared/sse.ts:11-12`).
//
//   EL ESTADO DE LA CONEXIÓN SE DIBUJA, NO SE FINGE. El hook devuelve
//   connecting|open|closed|error y lo dice: «it never fakes live». Un punto verde fijo
//   sobre una conexión caída es la clase de mentira que una consola de operación no puede
//   permitirse.
//
//   EL OPEN ES ADMIN Y TIENE CINCO DESENLACES, NO DOS (`policies.go:262-372`):
//     · 403 con cuerpo (`policy_verdict: denied`) → una DECISIÓN de política default-deny
//     · 202 (`op_status: requested` + `approval_ref`) → SEGUNDA FASE, hay que re-enviar
//     · `gate_status: no_gate` → NO hay puerta de aprobación cableada: un hueco del
//       patrimonio, que NO es lo mismo que «denegado»
//     · 502 «approval gate unavailable» → NO SE PUDO MIRAR
//     · `dispatch_ref` → abierta de verdad
//   Un 403 dibujado como «la petición falló» enseña a desconfiar de una frontera
//   deliberada; y colapsar `no_gate` en «denegado» esconde un hueco de despliegue.
//
//   Y EL CUERPO DEL 403 LLEGA: el cliente conserva `ApiError.body` a propósito para
//   «(status, approval_ref, detail) under 403/409/503» (`lib/api/errors.ts:23-30`). Sin
//   él, los cinco desenlaces se verían como dos.
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
      <p className="text-muted-foreground text-sm">{t('surface.loading')}</p>
    )
  if (q.isError)
    return (
      <p role="alert" className="text-danger text-sm">
        {t('surface.decisionsError')}
      </p>
    )
  if (items.length === 0)
    return (
      <p className="text-muted-foreground text-sm">
        {t('surface.noDecisions')}
      </p>
    )
  return (
    <ul>
      {items.map((d) => (
        <li
          key={d.id}
          className="border-border border-b py-1.5 text-sm last:border-b-0"
        >
          <div className="flex flex-wrap items-center gap-2">
            <Badge
              variant={d.policy_verdict === 'denied' ? 'danger' : 'success'}
            >
              {d.policy_verdict}
            </Badge>
            <Badge variant="outline">{d.op}</Badge>
            <Badge variant="outline">{d.gate_status}</Badge>
            <span className="font-mono text-xs break-all">
              {d.requested_model_ref}
            </span>
          </div>
          {/* `result` es `omitempty`: ausente no es «sin motivo», es que no lo hay escrito. */}
          <p className="text-muted-foreground text-xs">
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
        className="text-muted-foreground text-sm"
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
        <span className="text-muted-foreground text-xs">
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
        <p role="status" className="text-sm">
          {t(`surface.outcome.${outcome}`)}
          {outcome === 'approval' && m.data?.approval_ref ? (
            <>
              {' '}
              <span className="font-mono text-xs break-all">
                {m.data.approval_ref}
              </span>
            </>
          ) : null}
        </p>
      )}
    </div>
  )
}
