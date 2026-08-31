// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Authenticator Assurance Level (AAL) gate for privileged identity views
// (NIST SP 800-63B-4). FAIL-CLOSED by construction: the gate reads
// the AAL the BACKEND asserted on the current session (Whoami.aal); an absent value
// is treated as AAL1, so the most sensitive surfaces (viewing the access/WIF graph,
// managing identity) are DENIED until a real phishing-resistant step-up elevates the
// session. The panel orchestrates the WebAuthn ceremony; the backend verifies it and
// sets the AAL — the panel NEVER fabricates an assurance the backend did not grant.
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Fingerprint, IdCard, ShieldAlert } from 'lucide-react'
import { useCallback, useState, type ReactNode, useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { SelfAuditNotice } from '@/features/_intel'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { queryKeys } from '@/lib/api'
import { useAuth } from '@/lib/auth/context'
import { cn } from '@/lib/utils'
import {
  identityApi,
  identityKeys,
  isContractPending,
  isNoWebAuthnCredential,
} from './api'
import { ContractPendingNotice } from './components'
// The gate is rendered by FOREIGN chunks — the first-boot wizard, the console tabs,
// residency, the support bundle, the inference proxy — none of which import the
// identity view. i18next resources are global but only exist once someone registers
// them, and every view is behind its own `lazy(() => import(…))`: relying on the
// caller to remember meant an operator who booted straight into "Get started" read
// `assurance.stepUpTitle` on a button he was required to press. The component that
// TRANSLATES carries its own namespace (the repo convention — see run-state-badge,
// signing-badge, live-dot), so no caller can forget it.
import './i18n'
import {
  decodeRequestOptions,
  encodeAssertion,
  isWebAuthnSupported,
} from './webauthn'

/** NIST SP 800-63B-4 Authenticator Assurance Levels used by the panel. */
export const AAL = {
  /** Memorized secret (password) only. */
  PASSWORD: 1,
  /** Multi-factor. */
  MFA: 2,
  /** Hardware-based, phishing-resistant (verified authenticator). */
  HARDWARE: 3,
} as const

/** The assurance of the CURRENT session, fail-closed to AAL1 when unknown. */
export function useAssurance(): { aal: number; amr: string[] } {
  const { principal } = useAuth()
  return {
    aal: typeof principal?.aal === 'number' ? principal.aal : AAL.PASSWORD,
    amr: principal?.amr ?? [],
  }
}

type StepUpStatus =
  'idle' | 'running' | 'pending' | 'unsupported' | 'unenrolled' | 'error'

/**
 * Gate a privileged subtree behind a minimum AAL. While the session AAL is below
 * `minAal`, the subtree is replaced by a clear step-up panel (never a generic
 * toast, never the gated content). docs/SECURITY-HARDENING.md,§4.
 */
export function RequireAssurance({
  minAal,
  action,
  children,
}: {
  minAal: number
  /** i18n key fragment naming the gated action (e.g. "wif", "identity"). */
  action: string
  children: ReactNode
}) {
  const { aal } = useAssurance()
  if (aal >= minAal) return <>{children}</>
  return <StepUpPanel minAal={minAal} currentAal={aal} action={action} />
}

/** The phishing-resistant step-up ceremony. WebAuthn AAL3; PIV/CAC is surfaced as a
 *  parallel route in the privileged-login tab. */
export function StepUpPanel({
  minAal,
  currentAal,
  action,
  className,
  onElevated,
}: {
  minAal: number
  currentAal: number
  action: string
  className?: string
  /** Ran after the BACKEND verified the ceremony and the new AAL was re-read.
   *  Used by the global host to resume the call the engine refused; the panel
   *  itself still asserts nothing about the assurance — it only reports that the
   *  engine granted one. */
  onElevated?: () => void
}) {
  const { t } = useTranslation(['identity', 'common'])
  const queryClient = useQueryClient()
  const [status, setStatus] = useState<StepUpStatus>('idle')

  // ⛔ LA CEREMONIA SOBREVIVE A QUIEN LA PIDIÓ, Y ÉSTE ES EL SITIO DONDE ARREGLARLO.
  //
  // Entre que se pulsa y se resuelve hay tres esperas —opciones por red, `navigator.credentials
  // .get` con el gesto del usuario, y la verificación más el `whoami`—, y en ese hueco el
  // componente que pidió la elevación puede desmontarse: se cierra el diálogo, se navega. Sin
  // esta guarda, al volver se hacía `setStatus` sobre un componente muerto y, peor, se llamaba
  // `onElevated?.()` INCONDICIONALMENTE, disparando la acción que el operador ya abandonó.
  //
  // Y no es inocuo por estar desmontado: el contraste Codex `sol max` lo REPRODUJO contra
  // query-core 5.101.4 —`destroy()` sólo retira listeners, y `refetch()` sigue llegando a
  // `Query.fetch`— con el resultado `query_fn_calls_after_observer_destroy=1`. La petición sale.
  //
  // Se arregla AQUÍ y no en los seis llamantes porque aquí es donde está el defecto: este panel
  // es el único punto por el que pasan todos ellos, y parchear las hojas dejaría fuera a
  // cualquier consumidor futuro. La misma clase que ya cerré para las mutaciones con
  // `use-resume-guard.ts` en la rama de #784; esto es su mitad de LECTURA, en la raíz.
  const montado = useRef(true)
  useEffect(() => {
    montado.current = true
    return () => {
      montado.current = false
    }
  }, [])

  const stepUp = useCallback(async () => {
    if (!isWebAuthnSupported()) {
      setStatus('unsupported')
      return
    }
    setStatus('running')
    try {
      const options = await identityApi.webauthnAuthOptions()
      const credential = (await navigator.credentials.get({
        publicKey: decodeRequestOptions(options.publicKey),
      })) as PublicKeyCredential | null
      if (!montado.current) return
      if (!credential) {
        setStatus('error')
        return
      }
      await identityApi.webauthnAuthenticate(encodeAssertion(credential))
      // Backend verified + elevated → re-read whoami so the new AAL lifts the gate.
      await queryClient.invalidateQueries({ queryKey: queryKeys.whoami })
      // ⛔ La elevación YA ocurrió en el motor —eso no se deshace ni se debe deshacer—, pero la
      // REANUDACIÓN es del componente que la pidió: si ya no está, no se reanuda nada.
      if (!montado.current) return
      setStatus('idle')
      onElevated?.()
    } catch (err) {
      if (!montado.current) return
      // Three honest failure states, never a fake elevation: the route is not
      // served yet (pending seam), the user has no passkey registered yet
      // (route them to enrollment —), or the ceremony genuinely failed.
      setStatus(
        isContractPending(err)
          ? 'pending'
          : isNoWebAuthnCredential(err)
            ? 'unenrolled'
            : 'error',
      )
    }
  }, [queryClient, onElevated])

  // ⛔ EL EMPALME QUE FALTABA (C07-05). La ceremonia ofrecía SÓLO WebAuthn, y PIV/CAC vivía
  //    «como ruta paralela en la pestaña de login privilegiado». Consecuencia concreta: a quien
  //    el motor le exige elevar y sólo tiene PIV **no se le ofrecía aquí** — tenía que abandonar
  //    la acción, irse a otra pestaña y volver. El motor lleva la ruta desde
  //    (`POST /v1/auth/piv/elevate`, `core/api/server.go:626`) y la consola sólo exponía
  //    `piv/status`.
  //
  // ⚠ LA PUERTA ES `presented`, NO «PIV configurado», y la diferencia es la que separa una
  //    opción real de una promesa falsa. El motor eleva con el certificado del apretón de manos
  //    TLS (`handlePIVElevate` → `peerCertificates(r)`), y un navegador sólo lo adjunta si el
  //    servidor lo pidió al abrir la conexión: **no se puede adjuntar después**. Un despliegue
  //    con PIV configurado pero sin certificado en ESTA conexión no puede elevar, así que
  //    ofrecer el botón ahí sería un botón que falla siempre.
  //
  //    `retry: false` porque el 501 `piv_not_configured` es un ESTADO conocido, no un fallo de
  //    red: reintentarlo tres veces sólo retrasa la pantalla.
  const piv = useQuery({
    queryKey: identityKeys.piv(null),
    queryFn: () => identityApi.pivStatus(),
    retry: false,
  })

  const [pivStatus, setPivStatus] = useState<StepUpStatus>('idle')
  const stepUpPiv = useCallback(async () => {
    setPivStatus('running')
    try {
      await identityApi.pivElevate()
      if (!montado.current) return
      await queryClient.invalidateQueries({ queryKey: queryKeys.whoami })
      if (!montado.current) return
      setPivStatus('idle')
      onElevated?.()
    } catch (err) {
      if (!montado.current) return
      setPivStatus(isContractPending(err) ? 'pending' : 'error')
    }
  }, [queryClient, onElevated])

  return (
    <Card className={cn('border-warning/40', className)}>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <ShieldAlert className="size-4 text-warning" aria-hidden />
          {t('assurance.stepUpTitle')}
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <p className="text-sm text-muted-foreground">
          {t('assurance.stepUpBody', {
            action: t(`assurance.actions.${action}`, {
              defaultValue: t('assurance.actions.generic'),
            }),
            required: aalLabel(minAal, t),
            current: aalLabel(currentAal, t),
          })}
        </p>
        <SelfAuditNotice />
        <div>
          <Button
            type="button"
            onClick={() => void stepUp()}
            disabled={status === 'running'}
          >
            <Fingerprint className="size-4" aria-hidden />
            {status === 'running'
              ? t('assurance.authenticating')
              : t('assurance.authenticate')}
          </Button>
        </div>
        {piv.data?.presented === true && (
          <div>
            <Button
              type="button"
              variant="outline"
              onClick={() => void stepUpPiv()}
              disabled={pivStatus === 'running'}
            >
              <IdCard className="size-4" aria-hidden />
              {pivStatus === 'running'
                ? t('assurance.authenticating')
                : t('assurance.authenticatePiv')}
            </Button>
          </div>
        )}
        {(status === 'pending' || pivStatus === 'pending') && (
          <ContractPendingNotice what={t('assurance.seamWhat')} />
        )}
        {status === 'unsupported' && (
          <p role="alert" className="text-sm text-warning">
            {t('assurance.unsupported')}
          </p>
        )}
        {status === 'unenrolled' && (
          <p role="alert" className="text-sm text-warning">
            {t('assurance.unenrolled')}
          </p>
        )}
        {status === 'error' && (
          <p role="alert" className="text-sm text-danger">
            {t('assurance.failed')}
          </p>
        )}
      </CardContent>
    </Card>
  )
}

/** Human label for an AAL level (no conformance claim — just the level name). */
export function aalLabel(
  level: number,
  t: (k: string, o?: Record<string, unknown>) => string,
): string {
  if (level >= AAL.HARDWARE) return t('assurance.aal.aal3')
  if (level >= AAL.MFA) return t('assurance.aal.aal2')
  return t('assurance.aal.aal1')
}
