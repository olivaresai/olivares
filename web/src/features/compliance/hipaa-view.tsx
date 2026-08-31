// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE HIPAA SECURITY RULE TECHNICAL-SAFEGUARDS GAP REPORT — 45 CFR §164.312.
//
// ⛔ ESTO NO ES EL FRAMEWORK `hipaa_clinical_ai` QUE LA CONSOLA YA LISTABA, y la confusión es el
// riesgo principal de esta pantalla. Medido el 2026-08-20: trece ficheros de `web/src` mencionan
// «hipaa», y ninguno alcanzaba este informe. Son dos documentos, con dos autoridades:
//
//   hipaa_clinical_ai          «HIPAA Clinical AI Overlay» — en el catálogo genérico
//                              (frameworks.go:2136), alcanzable por /frameworks/{id}
//   hipaa_technical_safeguards «HIPAA Security Rule — Technical Safeguards», 45 CFR §164.312,
//                              con CITA por control. `hipaaTechnicalFramework()` se usa en UN
//                              único sitio (hipaa.go:59) y NO está en el catálogo: no llega por
//                              la ruta genérica, ni por accidente.
//
// ⛔ Y EL DESCARGO SE PINTA SIEMPRE, INCONDICIONALMENTE. El motor lo manda con cada respuesta y
// dice, literal: «Technical mapping only; NOT a HIPAA compliance certification and NOT legal
// advice». NO se usa `FrameworkDisclaimerBanner`, que existe al lado y parece lo correcto:
// devuelve `null` salvo para crosswalks y frameworks en desarrollo (components.tsx:244-255), así
// que aquí **se tragaría el descargo en silencio**. Un informe de brecha regulatoria sin esa línea
// afirma exactamente lo que la línea niega. Hay un testigo que lo vigila.
import { useQuery } from '@tanstack/react-query'
import { ShieldQuestion } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { KvList, KvRow } from '@/components/ui/kv'
import { Skeleton } from '@/components/ui/skeleton'
import {
  ControlStatusBadge,
  DisclaimerNote,
  SectionCard,
} from '@/features/_intel'
import { useAuth } from '@/lib/auth/context'
import { complianceApi, complianceKeys } from './api'
import type { HipaaControlGap } from './types'

/**
 * One control of §164.312.
 *
 * ⛔ `gap` y `recommended_action` son `omitempty` EN EL MOTOR: su ausencia significa «no hay
 * brecha», no «campo sin rellenar». Pintar una fila vacía para ellos convertiría un control
 * satisfecho en uno que parece incompleto — la lectura contraria.
 */
function HipaaControlCard({ control }: { control: HipaaControlGap }) {
  const { t } = useTranslation('compliance')
  return (
    <div className="rounded-lg border border-border p-4">
      <div className="mb-2 flex flex-wrap items-center gap-2">
        {/* ⛔ LA CITA VA PRIMERO Y EN MONOESPACIADO. Es lo único que este informe tiene y la vista
            genérica de frameworks no; sin ella la pantalla es un duplicado peor de /gaps. */}
        <span className="font-mono text-sm font-medium text-foreground">
          {control.citation}
        </span>
        <span className="text-sm text-foreground">{control.title}</span>
        <ControlStatusBadge status={control.status} />
      </div>
      <KvList>
        <KvRow label={t('hipaa.requirement')} align="start">
          {control.requirement}
        </KvRow>
        <KvRow label={t('hipaa.criterion')} align="start">
          {control.criterion}
        </KvRow>
        {control.gap ? (
          <KvRow label={t('hipaa.gap')} align="start">
            <span className="text-warning">{control.gap}</span>
          </KvRow>
        ) : null}
        {control.recommended_action ? (
          <KvRow label={t('hipaa.recommended')} align="start">
            {control.recommended_action}
          </KvRow>
        ) : null}
      </KvList>
      <div className="mt-3 flex flex-wrap gap-1">
        {control.present_capabilities.map((k) => (
          <Badge key={`p-${k}`} variant="success">
            {k}
          </Badge>
        ))}
        {control.missing_capabilities.map((k) => (
          <Badge key={`m-${k}`} variant="warning">
            {k}
          </Badge>
        ))}
      </div>
    </div>
  )
}

/** The tab. Read-only: this report has no lever and the engine offers none. */
export function HipaaTab({ canRead }: { canRead: boolean }) {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  const q = useQuery({
    queryKey: complianceKeys.hipaaGapReport(activeTenant),
    queryFn: () => complianceApi.hipaaGapReport(),
    enabled: canRead,
  })

  if (!canRead) {
    return (
      <SectionCard title={t('hipaa.title')}>
        <EmptyState icon={<ShieldQuestion />} title={t('hipaa.forbidden')} />
      </SectionCard>
    )
  }
  if (q.isPending) return <Skeleton className="h-64 w-full" />
  // ⛔ EL ERROR SE DICE. Una pantalla de cumplimiento que se queda en blanco al fallar su consulta
  // es indistinguible de una sin brechas, y ésa es la lectura más cara de las dos.
  if (q.isError || !q.data) {
    return (
      <SectionCard title={t('hipaa.title')}>
        <p role="alert" className="text-sm text-warning">
          {t('hipaa.unreadable')}
        </p>
      </SectionCard>
    )
  }
  const r = q.data
  return (
    <SectionCard title={r.name} description={r.authority}>
      <div className="flex flex-col gap-4">
        {/* ⛔ INCONDICIONAL. Ver la cabecera del fichero: el banner compartido devolvería null. */}
        <DisclaimerNote text={r.disclaimer} />
        {/* ⛔ EL RESUMEN NO PUEDE SER «Brechas: {summary.gap}», Y ES EL DEFECTO MÁS CARO QUE TUVO
            ESTA PANTALLA. `StatusGap` exige `present == 0` (assess.go:32-35) y TODA capacidad
            ARQUITECTÓNICA se evalúa siempre presente por construcción (capabilities.go:431-440).
            Medido: los CINCO controles de §164.312 llevan al menos una —(a) access_control_rbac ·
            (b) audit_export · (c) audit_immutability · (d) access_control_rbac + secure_defaults ·
            (e) encryption_transit—, así que `summary.gap` es DETERMINÍSTICAMENTE 0 para este marco.
            Y mientras tanto las tarjetas SÍ pintan texto de brecha para cualquier control con
            capacidades ausentes (hipaa.go:79-89): la pantalla habría dicho «Brechas 0» arriba y
            enseñado brechas abajo, en un informe regulatorio y en la dirección que tranquiliza.

            Se pintan los CINCO estados que el motor mantiene separados, y el agregado se define
            como lo define él: `gapControls` cuenta partial + gap + unmapped Y TAMBIÉN by_design,
            «included as a (design-only) caveat» (assess.go:97-107). Lo cazó el contraste Codex sol
            max (hallazgo ALTO); verificado aquí antes de adoptarlo. */}
        <KvList>
          <KvRow label={t('hipaa.generatedAt')} mono>
            {r.generated_at}
          </KvRow>
          <KvRow label={t('hipaa.controls')} mono>
            {r.summary.total}
          </KvRow>
          <KvRow label={t('hipaa.satisfied')} mono>
            {r.summary.satisfied}
          </KvRow>
          <KvRow label={t('hipaa.notFullyBacked')} mono>
            <span
              className={
                r.summary.total - r.summary.satisfied > 0
                  ? 'font-medium text-warning'
                  : undefined
              }
            >
              {r.summary.total - r.summary.satisfied}
            </span>
          </KvRow>
          <KvRow label={t('hipaa.byDesign')} mono>
            {r.summary.by_design}
          </KvRow>
          <KvRow label={t('hipaa.partial')} mono>
            {r.summary.partial}
          </KvRow>
          <KvRow label={t('hipaa.gaps')} mono>
            {r.summary.gap}
          </KvRow>
          {r.summary.unmapped > 0 ? (
            <KvRow label={t('hipaa.unmapped')} mono>
              {r.summary.unmapped}
            </KvRow>
          ) : null}
        </KvList>
        {/* La nota que impide leer `by_design` como telemetría: es evidencia de DISEÑO, citada,
            no una medida del despliegue. El motor los separa por eso mismo. */}
        {r.summary.by_design > 0 ? (
          <p role="note" className="text-xs text-muted-foreground">
            {t('hipaa.byDesignNote')}
          </p>
        ) : null}
        {r.controls.length === 0 ? (
          <EmptyState icon={<ShieldQuestion />} title={t('hipaa.noControls')} />
        ) : (
          <div className="flex flex-col gap-3">
            {r.controls.map((c) => (
              <HipaaControlCard key={c.control_id} control={c} />
            ))}
          </div>
        )}
      </div>
    </SectionCard>
  )
}
