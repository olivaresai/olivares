// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Voice (module XVI) endpoint wrappers + query keys. Thin `http.*` calls against the
// engine's `/v1/m/voice/…` routes (the web presents, never recomputes).
// Tenant-scoped keys include the active tenant so switching
// org refetches cleanly. Reads are RBAC-gated server-side con `voice:session:read`
// —CORREGIDO 2026-08-20: esta línea decía `'voice:read'`, que no es un permiso de este
// motor: los literales son `voice:session:read`, `voice:session:admin` y
// `voice:policy:admin` (`modules/voice/voice.go:30-32`). Este mismo fichero ya arrastra
// una nota de otro desajuste de permisos, así que el comentario que los documenta no
// puede nombrar uno inexistente—; the UI mirrors
// that and hides the policy write unless granted.
import { http } from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'
import type {
  VoiceDecision,
  VoiceOpenInput,
  VoiceOpenResponse,
  VoicePolicy,
  VoicePolicyInput,
  VoiceSession,
} from './types'

const BASE = '/v1/m/voice'

export interface SessionsParams {
  state?: string
  limit?: number
}

export const voiceApi = {
  sessions: (params?: SessionsParams) =>
    http.get<ListResponse<VoiceSession>>(`${BASE}/sessions`, {
      query: { ...params },
    }),
  session: (ref: string) => http.get<VoiceSession>(`${BASE}/sessions/${ref}`),
  policies: () => http.get<ListResponse<VoicePolicy>>(`${BASE}/policies`),
  // PUT /policies upserts the default-DENY policy set (a policy is what PERMITS opening).
  putPolicy: (body: VoicePolicyInput) =>
    http.put<VoicePolicy>(`${BASE}/policies`, body),
  // ⛔ EL LEDGER DEL TENANT NO ES UNA COMODIDAD SOBRE EL DE LA SESIÓN: es la ÚNICA
  //    superficie desde la que la consola puede leer una DENEGACIÓN.
  //
  //    Quien crea la fila de sesión es `markGovernedOpen` —«created here if telemetry
  //    has not yet arrived» (`modules/voice/policies.go`)— y **todas** las denegaciones
  //    vuelven ANTES de llegar a ella: sin política que lo permita, plan-hash que no
  //    casa, tope de presupuesto y kill switch. Así que un open denegado deja fila en el
  //    ledger y NINGUNA fila de sesión.
  //
  //    Medido, no deducido (`modules/voice/ledger_reachability_test.go`, con control
  //    positivo en la misma corrida): `GET /sessions/s-denied` → 404 mientras
  //    `GET /decisions` trae su fila con `op_status=blocked`.
  //
  //    ⚠ Y el límite honesto de esa frase: la ruta POR SESIÓN existe y responde para ese
  //    mismo ref. Lo que no existe es el camino de la consola hasta ella — sólo se llega
  //    desde una fila de la tabla de sesiones, y esa fila no se crea. El motor no
  //    esconde nada; la pantalla es la que no tenía por dónde entrar.
  //
  // ⛔ `limit` NO ES OPCIONAL DE HECHO. Sin él el repositorio genérico pagina a 100
  //    (`core/internal/store/sqlstore/generic.go`) y el handler hace UNA sola llamada a
  //    `repo.List` sin drenar el cursor (`modules/voice/sessions.go:262-286`), de modo
  //    que la respuesta llega recortada con `has_more: true` y sin decirlo por sí sola.
  allDecisions: (params?: { limit?: number }) =>
    http.get<ListResponse<VoiceDecision>>(`${BASE}/decisions`, {
      query: { ...params },
    }),
  // Las decisiones de UNA sesión. El motor exige el ref y devuelve 400 sin él
  // (`sessions.go:251`), así que la vista no consulta sin sesión elegida.
  decisions: (ref: string) =>
    http.get<ListResponse<VoiceDecision>>(
      `${BASE}/sessions/${encodeURIComponent(ref)}/decisions`,
    ),
  // ⛔ EL OPEN GOBERNADO, y NO tiene dos desenlaces sino cinco. Un 403 aquí NO es un
  //    fallo de red: llega con cuerpo —`op_status`, `policy_verdict`, `detail`— y es una
  //    DECISIÓN de política default-deny. El cliente compartido lo entrega como
  //    `ApiError`, y su `body` conserva ese cuerpo a propósito (lib/api/errors.ts:23-30),
  //    que es lo único que permite a la pantalla distinguir «denegado por política» de
  //    «no se pudo mirar» (502, la puerta de aprobación no responde).
  open: (body: VoiceOpenInput) =>
    http.post<VoiceOpenResponse>(`${BASE}/sessions/open`, body),
}

export const voiceKeys = {
  all: (tenant: string | null) => ['voice', tenant] as const,
  sessions: (tenant: string | null, params?: unknown) =>
    params === undefined
      ? (['voice', tenant, 'sessions'] as const)
      : (['voice', tenant, 'sessions', params] as const),
  session: (tenant: string | null, ref: string) =>
    ['voice', tenant, 'sessions', ref] as const,
  policies: (tenant: string | null) => ['voice', tenant, 'policies'] as const,
  decisions: (tenant: string | null, ref: string) =>
    ['voice', tenant, 'decisions', ref] as const,
  // ⛔ SEGMENTO PROPIO, NO `decisions/'all'`. La clave por sesión es
  //    `['voice', tenant, 'decisions', ref]`, así que con `'all'` de discriminante un
  //    ref llamado literalmente `all` compartiría entrada de caché con el ledger del
  //    estate. El motor no prohíbe ese ref: `session_ref` es texto libre recortado a
  //    `maxRefLen`.
  //
  //    ⚠ EL ALCANCE EXACTO, porque la primera versión de esta nota afirmaba de más:
  //    hoy la vista SIEMPRE pasa `{ limit }`, y con params la clave lleva un segmento
  //    más, así que las dos NO coinciden ni con el discriminante malo. La colisión es
  //    alcanzable por la rama SIN params — la que usaría cualquier `invalidateQueries`
  //    o una llamada futura sin límite. Se cierra en la fábrica, que es donde se puede
  //    cerrar de una vez, y su testigo mira la fábrica y no la pantalla: la versión de
  //    render de ese caso ESCAPABA al mutante que cambia el discriminante.
  ledger: (tenant: string | null, params?: unknown) =>
    params === undefined
      ? (['voice', tenant, 'ledger'] as const)
      : (['voice', tenant, 'ledger', params] as const),
}
