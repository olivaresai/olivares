// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE EGRESS DESTINATION CONTROL, WHERE THE PERSON IT REFUSES CAN SEE IT.
//
// ⛔ ESTE PANEL EXISTE PORQUE UNA DECISIÓN DE SEGURIDAD SE APOYABA EN UNA PREMISA FALSA.
// `cmd/olivares/cmd_eventing_egress.go:28-34` razona —bien— que la PALANCA es una ceremonia de
// CLI: actuar la transición es un acto de operador de plataforma sobre un control que no está
// acotado por tenant, y una palanca alcanzable por HTTP habría que defenderla contra todo camino
// que llegue a HTTP. Pero ese razonamiento se sostiene sobre una frase que afirma como hecho:
//
//   «The console shows the state and the diff (GET /egress-policy, GET /egress-policy/compat);
//    it does not offer the lever…»
//
// Medido el 2026-08-20: CERO ficheros de `web/src` mencionaban `egress-policy`. La consola no
// enseñaba ninguna de las dos. ⇒ Se construyen las dos LECTURAS y la palanca no se toca: así la
// premisa pasa a ser cierta por el lado que no debilita nada.
//
// Y el motor dice por qué importa, en su propio fichero (`modules/eventing/egressapi.go:17-24`):
// «an author whose destination was refused could not tell an operator's rule from a typo, and an
// operator could not tell whether their file had been read at all — a boot log line is not an
// answer to "is it on right now"».
//
// ⛔ LO QUE ESTE PANEL NO HACE, y no es una omisión: no deduce nada de lo que NO llega. El motor
// documenta que el resumen de compatibilidad se sirve SÓLO a quien además puede leer el informe
// detallado, porque un conteo es un oráculo de pertenencia. Ausencia = «no se te sirve», jamás
// «cero».
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { KvList, KvRow } from '@/components/ui/kv'
import { Skeleton } from '@/components/ui/skeleton'
import { useAuth } from '@/lib/auth/context'
import { eventingApi, eventingKeys } from './api'
import type { EgressPolicyStatus, EgressWriterFence } from './types'

/**
 * The three-answer badge every field in this panel needs.
 *
 * ⛔ `unavailable` NO ES `!in_force`, y confundirlos invierte el consejo que el operador
 * recibe. El motor lo separa a propósito: una política que EXISTE y no se puede LEER hace que
 * las entregas se re-encolen, no que se rechacen (`egressapi.go:38-41`). «No hay política» dice
 * «escribe una»; «no la puedo leer» dice «mira el host». Son remedios distintos.
 */
function TriState({
  unavailable,
  on,
  labels,
}: {
  unavailable: boolean
  on: boolean
  labels: { unavailable: string; on: string; off: string }
}) {
  if (unavailable) {
    return <Badge variant="warning">{labels.unavailable}</Badge>
  }
  return (
    <Badge variant={on ? 'success' : 'neutral'}>
      {on ? labels.on : labels.off}
    </Badge>
  )
}

/**
 * The writer fence (unit H).
 *
 * Se pintan LAS DOS capacidades, la exigida y la del binario, porque el motor lo dice de sí
 * mismo: «an operator debugging a refusal needs the comparison, not the verdict»
 * (`egressapi.go:86-88`). Un panel que resolviera la comparación a un sí/no le quitaría al
 * operador justo el dato con el que depura.
 */
function WriterFence({ fence }: { fence: EgressWriterFence }) {
  const { t } = useTranslation('eventing')
  // ⛔ CON LA BARRERA ILEGIBLE NO HAY COMPARACIÓN QUE HACER. Ante un error de lectura el servidor
  //    conoce SÓLO la capacidad de este binario y deja el resto en el cero de Go
  //    (`egressapi.go:165-175`). La primera versión marcaba `armed` como desconocido —bien— y
  //    seguía pintando `required_capability` 0 como si fuera un requisito medido, y calculaba la
  //    suficiencia contra él: «este binario cumple de sobra» sobre un requisito que nadie leyó.
  //    `binary_capability` sí sobrevive: es el único lado local, inicializado antes de la lectura.
  const unknown = fence.unavailable === true
  const short = !unknown && fence.binary_capability < fence.required_capability
  return (
    <KvList>
      <KvRow label={t('egress.fence.armed')}>
        <TriState
          unavailable={fence.unavailable === true}
          on={fence.armed}
          labels={{
            unavailable: t('egress.unknown'),
            on: t('egress.fence.armedYes'),
            off: t('egress.fence.armedNo'),
          }}
        />
      </KvRow>
      {unknown || fence.mode ? (
        <KvRow label={t('egress.fence.mode')} mono>
          {unknown ? (
            <Badge variant="warning">{t('egress.unknown')}</Badge>
          ) : (
            fence.mode
          )}
        </KvRow>
      ) : null}
      <KvRow label={t('egress.fence.required')} mono>
        {unknown ? (
          <Badge variant="warning">{t('egress.unknown')}</Badge>
        ) : (
          fence.required_capability
        )}
      </KvRow>
      <KvRow label={t('egress.fence.binary')} mono>
        <span className={short ? 'font-medium text-warning' : undefined}>
          {fence.binary_capability}
        </span>
      </KvRow>
      {unknown ? (
        <KvRow label={t('egress.fence.generation')} mono>
          <Badge variant="warning">{t('egress.unknown')}</Badge>
        </KvRow>
      ) : fence.generation ? (
        <KvRow label={t('egress.fence.generation')} mono>
          {fence.generation}
        </KvRow>
      ) : null}
    </KvList>
  )
}

/**
 * The compatibility summary — only present when the caller may also read the itemized report.
 *
 * ⛔ `intact === false` NO SE PUEDE PINTAR COMO UN DETALLE. Es, textual, «the shape a partial
 * restore produces, and it is the more dangerous of the two: the report looks complete and
 * describes a set that has lost members» (`egressrollout.go:724-728`). Un lector que vea los dos
 * conteos sin ese aviso planifica una actuación contra un conjunto que ya no está entero.
 */
function CompatSummary({
  compat,
}: {
  compat: NonNullable<EgressPolicyStatus['compat']>
}) {
  const { t } = useTranslation('eventing')
  return (
    <div className="flex flex-col gap-2">
      {compat.seeded && !compat.intact ? (
        <p role="alert" className="text-sm font-medium text-warning">
          {t('egress.compat.notIntact')}
        </p>
      ) : null}
      <KvList>
        <KvRow label={t('egress.compat.seeded')}>
          <Badge variant={compat.seeded ? 'neutral' : 'warning'}>
            {compat.seeded
              ? t('egress.compat.drawn')
              : t('egress.compat.notDrawn')}
          </Badge>
        </KvRow>
        {/* Mismo motivo que en el informe: sin trazar, los conteos no describen nada. */}
        {compat.seeded ? (
          <>
            <KvRow label={t('egress.compat.recorded')} mono>
              {compat.recorded}
            </KvRow>
            <KvRow label={t('egress.compat.stillNeeded')} mono>
              <span
                className={
                  compat.still_needed > 0
                    ? 'font-medium text-warning'
                    : undefined
                }
              >
                {compat.still_needed}
              </span>
            </KvRow>
            {compat.unparsable > 0 ? (
              <KvRow label={t('egress.compat.unparsable')} mono>
                {compat.unparsable}
              </KvRow>
            ) : null}
          </>
        ) : null}
      </KvList>
    </div>
  )
}

/**
 * GET /egress-policy, rendered where a subscription author will meet its refusal.
 *
 * The lever is not here and must not arrive here: see the file header.
 */
export function EgressPolicyPanel({ canAdmin }: { canAdmin: boolean }) {
  const { t } = useTranslation('eventing')
  const { activeTenant } = useAuth()
  const q = useQuery({
    queryKey: eventingKeys.egressPolicy(activeTenant),
    queryFn: () => eventingApi.egressPolicyStatus(),
  })

  if (q.isPending) {
    return <Skeleton className="h-40 w-full" />
  }
  // ⛔ EL ERROR SE DICE. Un panel que se esconde cuando su consulta falla le enseña al operador
  // exactamente lo mismo que le enseñaba antes de existir, y encima con la apariencia de que no
  // hay nada que ver. «No he podido mirar» es una respuesta, y es la tercera.
  if (q.isError || !q.data) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>{t('egress.title')}</CardTitle>
          <CardDescription role="alert">
            {t('egress.unreadable')}
          </CardDescription>
        </CardHeader>
      </Card>
    )
  }
  const s = q.data
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('egress.title')}</CardTitle>
        <CardDescription>{t('egress.description')}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-6 md:grid-cols-2">
        <KvList>
          <KvRow label={t('egress.inForce')}>
            <TriState
              unavailable={s.unavailable === true}
              on={s.in_force}
              labels={{
                unavailable: t('egress.unreadablePolicy'),
                on: t('egress.inForceYes'),
                off: t('egress.inForceNo'),
              }}
            />
          </KvRow>
          {s.source ? (
            <KvRow label={t('egress.source')} mono>
              {s.source}
            </KvRow>
          ) : null}
          <KvRow label={t('egress.mode')} mono>
            {s.mode_unavailable ? (
              <Badge variant="warning">{t('egress.unknown')}</Badge>
            ) : (
              s.mode
            )}
          </KvRow>
          {/* ⛔ EL MODO CLASIFICADO VA AL LADO DEL VIGENTE, SIEMPRE QUE EL MOTOR LO MANDE, y no
              sólo cuando difieren: es «what lets a reader tell an inherited disposition from a
              chosen one» (egressapi.go:54-57). Pintarlo sólo al diferir haría que la ausencia
              del dato significase dos cosas. */}
          {/* ⛔ `mode_unavailable` NO ES SÓLO SOBRE `mode`. Si `resolveRollout` falla, el servidor
              marca esa bandera y deja `classified_mode` VACÍO y `enforcement_committed` en el cero
              de Go (`egressapi.go:176-182`). La primera versión aplicaba la tercera respuesta sólo
              al modo: ocultaba la clasificación —lo que hace que su ausencia signifique dos cosas—
              y afirmaba «Not committed», que es una CONCLUSIÓN sobre algo que no se pudo leer.
              No poder leer la disposición no autoriza a decir que no está comprometida. */}
          {s.mode_unavailable || s.classified_mode ? (
            <KvRow label={t('egress.classifiedMode')} mono>
              {s.mode_unavailable ? (
                <Badge variant="warning">{t('egress.unknown')}</Badge>
              ) : (
                s.classified_mode
              )}
            </KvRow>
          ) : null}
          <KvRow label={t('egress.committed')}>
            {s.mode_unavailable ? (
              <Badge variant="warning">{t('egress.unknown')}</Badge>
            ) : (
              <Badge variant={s.enforcement_committed ? 'success' : 'neutral'}>
                {s.enforcement_committed
                  ? t('egress.committedYes')
                  : t('egress.committedNo')}
              </Badge>
            )}
          </KvRow>
        </KvList>
        <div className="flex flex-col gap-4">
          <div>
            <h3 className="mb-1 text-sm font-medium text-foreground">
              {t('egress.fence.title')}
            </h3>
            <WriterFence fence={s.writer_fence} />
          </div>
          <div>
            <h3 className="mb-1 text-sm font-medium text-foreground">
              {t('egress.compat.title')}
            </h3>
            {/* ⛔ `canAdmin` AQUÍ NO ES REDUNDANTE CON EL SERVIDOR, y creerlo era el defecto.
                El motor sirve `compat` desde una ruta READ **sólo** si el principal además pasa
                `permSubAdmin` (`egressapi.go:67-74`, `:183-200`), así que la respuesta fresca ya
                viene acotada. Pero la clave de la consulta es `['eventing', tenant,
                'egress-policy']` — **sin principal ni tier** (`api.ts:121-124`): tras un paso de
                admin a sólo-lectura sobre el MISMO `QueryClient` y tenant, la respuesta admin
                cacheada tiene exactamente la misma clave que la que el servidor le negaría al
                llamante nuevo, y se pintaría mientras siga en caché.

                ⇒ **Una frontera de permiso que sólo vive en el servidor no sobrevive a una caché
                indexada por tenant.** Lo cazó el contraste Codex sol max (A1, alto); el informe
                itemizado de abajo ya se defendía así y el resumen no. */}
            {canAdmin && s.compat ? (
              <CompatSummary compat={s.compat} />
            ) : (
              // ⛔ AUSENCIA ≠ CERO, y tampoco «no tienes permiso». Decir «0 destinos» donde no
              //    hay dato afirma sobre lo no mirado; pero explicar TODA ausencia como falta de
              //    tier también afirma de más. El servidor omite el resumen además cuando
              //    `m.compat == nil`, cuando el informe falla y cuando la disposición no se pudo
              //    leer (`egressapi.go:176-200`). Sin tier la causa se sabe; CON tier, no — y ahí
              //    la pantalla dice que no llegó, no por qué.
              <p className="text-sm text-muted-foreground">
                {canAdmin
                  ? t('egress.compat.notProvided')
                  : t('egress.compat.notServed')}
              </p>
            )}
          </div>
        </div>
      </CardContent>
    </Card>
  )
}

/**
 * GET /egress-policy/compat — the itemized diff, ADMIN tier.
 *
 * ⛔ SE PIDE SÓLO SI EL LLAMANTE PUEDE. El motor la pone en tier admin «because it names hosts,
 * and because planning an actuation is an administrative act» (`egressapi.go:210-214`). Pedirla
 * sin permiso no filtra nada —el servidor la deniega— pero enseña un error que no es del
 * operador, y en una pantalla cuyo trabajo es distinguir la regla de un fallo propio eso es
 * exactamente el ruido que viene a quitar.
 *
 * ⛔ Y `covered` NO SE PINTA COMO «bien/mal». Es «survives enforcement on its own merits», o sea
 * lo que NO se rompe. Lo accionable es su complemento, y por eso la tabla ordena por él.
 */
export function EgressCompatReport({ canAdmin }: { canAdmin: boolean }) {
  const { t } = useTranslation('eventing')
  const { activeTenant } = useAuth()
  const q = useQuery({
    queryKey: eventingKeys.egressCompat(activeTenant),
    queryFn: () => eventingApi.egressCompatReport(),
    enabled: canAdmin,
  })

  if (!canAdmin) return null
  if (q.isPending) return <Skeleton className="h-32 w-full" />
  if (q.isError || !q.data) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>{t('egress.report.title')}</CardTitle>
          <CardDescription role="alert">
            {t('egress.report.unreadable')}
          </CardDescription>
        </CardHeader>
      </Card>
    )
  }
  const r = q.data
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('egress.report.title')}</CardTitle>
        <CardDescription>{t('egress.report.description')}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {/* ⛔ «Seeded pero NO intact» es la forma peligrosa y va ARRIBA, antes de cualquier
            cifra: el informe parece completo y describe un conjunto que ha perdido miembros.
            El motor manda además su propia nota; se pinta la suya, no una mía. */}
        {r.seeded && !r.intact ? (
          <p role="alert" className="text-sm font-medium text-warning">
            {t('egress.compat.notIntact')}
            {r.integrity_note ? ` — ${r.integrity_note}` : ''}
          </p>
        ) : null}
        {!r.seeded ? (
          <p role="alert" className="text-sm text-warning">
            {t('egress.report.notSeeded')}
          </p>
        ) : null}
        {/* ⛔ SIN TRAZAR, LOS CONTADORES NO SE PINTAN. Go dice de sí mismo que con `seeded=false`
            «the rest of the report describes nothing» y retorna ahí (`egressrollout.go:719-728`,
            `:829-830`). La primera versión avisaba —bien— y a continuación enseñaba
            `subscriptions 0`, `still_needed 0` y «No legacy destination»: el aviso evitaba la
            fusión total, pero esas tres celdas seguían colapsando «no se midió» con «se midió y
            dio cero», que es exactamente la distinción que este panel existe para sostener. */}
        {r.seeded ? (
          <KvList>
            <KvRow label={t('egress.report.subscriptions')} mono>
              {r.subscriptions}
            </KvRow>
            <KvRow label={t('egress.compat.stillNeeded')} mono>
              <span
                className={
                  r.still_needed > 0 ? 'font-medium text-warning' : undefined
                }
              >
                {r.still_needed}
              </span>
            </KvRow>
            {r.unparsed > 0 ? (
              <KvRow label={t('egress.report.unparsed')} mono>
                {r.unparsed}
              </KvRow>
            ) : null}
            {r.seeded_at ? (
              <KvRow label={t('egress.report.seededAt')} mono>
                {r.seeded_at}
              </KvRow>
            ) : null}
          </KvList>
        ) : null}
        {!r.seeded ? null : r.authorities.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            {t('egress.report.noAuthorities')}
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-sm">
              <thead className="text-xs uppercase text-muted-foreground">
                <tr>
                  <th className="px-3 py-2 font-medium">
                    {t('egress.report.authority')}
                  </th>
                  <th className="px-3 py-2 font-medium">
                    {t('egress.report.kind')}
                  </th>
                  <th className="px-3 py-2 font-medium">
                    {t('egress.report.uses')}
                  </th>
                  <th className="px-3 py-2 font-medium">
                    {t('egress.report.effect')}
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {[...r.authorities]
                  .sort((a, b) => Number(a.covered) - Number(b.covered))
                  .map((a) => (
                    <tr key={`${a.kind}:${a.authority}`}>
                      <td className="px-3 py-2 font-mono">{a.authority}</td>
                      <td className="px-3 py-2 text-muted-foreground">
                        {a.kind}
                      </td>
                      <td className="px-3 py-2 font-mono">{a.subscriptions}</td>
                      <td className="px-3 py-2">
                        <Badge variant={a.covered ? 'neutral' : 'warning'}>
                          {a.covered
                            ? t('egress.report.survives')
                            : t('egress.report.breaks')}
                        </Badge>
                      </td>
                    </tr>
                  ))}
              </tbody>
            </table>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
