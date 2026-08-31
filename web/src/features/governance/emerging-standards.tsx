// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { Skeleton } from '@/components/ui/skeleton'
import { DisclaimerNote, SectionCard } from '@/features/_intel'
import { governanceApi, governanceKeys } from './api'
import type { EmergingStandard } from './types'

/** El registro «design-toward» de estándares emergentes de identidad de agente.
 *
 *  ⛔ ESTA PANTALLA EXISTE PARA NO PROMETER DE MÁS. El motor rastrea seis estándares que NO
 *  implementa, y su respuesta trae el aviso —«Design-toward only (IDN-12)»— y, por estándar,
 *  un `caveat` que explica por qué está seguido y no soportado. Mostrar nombre y estado sin
 *  esas dos cosas convertiría un registro de seguimiento en un catálogo de soporte, que es
 *  exactamente la afirmación que el motor se cuida de no hacer.
 *
 *  Por eso no hay filtro ni orden por «status»: invitaría a leer la lista como una tabla de
 *  madurez de producto. Se lee entera, con su aviso delante. */
export function EmergingStandardsPanel({ canRead }: { canRead: boolean }) {
  const { t } = useTranslation('governance')

  const q = useQuery({
    queryKey: governanceKeys.emergingStandards(),
    queryFn: () => governanceApi.emergingStandards(),
    enabled: canRead,
  })

  if (!canRead) {
    return (
      <SectionCard title={t('emerging.title')}>
        <EmptyState title={t('emerging.noAccess')} />
      </SectionCard>
    )
  }

  return (
    <SectionCard
      title={t('emerging.title')}
      description={t('emerging.description')}
    >
      {/* El aviso del MOTOR, literal y ANTES de la lista. */}
      <DisclaimerNote text={q.data?.disclaimer} className="mb-3" />

      {q.isLoading && <Skeleton className="h-40 w-full" />}

      {q.data && (
        <>
          {/* `verified_at` es GRUESO a propósito (mes): se enseña tal cual, sin convertirlo
              en una fecha exacta que el motor no afirma. */}
          <p className="mb-3 text-xs text-muted-foreground">
            {t('emerging.verifiedAt', { month: q.data.verified_at })}
          </p>
          {q.data.standards.length === 0 ? (
            <EmptyState title={t('emerging.empty')} />
          ) : (
            <ul className="flex flex-col gap-3">
              {q.data.standards.map((s) => (
                <StandardRow key={s.key} standard={s} />
              ))}
            </ul>
          )}
        </>
      )}
    </SectionCard>
  )
}

function StandardRow({ standard }: { standard: EmergingStandard }) {
  const { t } = useTranslation('governance')
  return (
    <li className="rounded-md border border-border px-3 py-2">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-medium text-foreground">
          {standard.name}
        </span>
        <Badge variant="neutral">{standard.body}</Badge>
        <Badge variant="warning">
          {t(`emerging.status.${standard.status}`, {
            defaultValue: standard.status,
          })}
        </Badge>
      </div>

      <dl className="mt-2 grid gap-x-6 gap-y-1 text-xs sm:grid-cols-2">
        <div>
          <dt className="inline text-muted-foreground">
            {t('emerging.spec')}:{' '}
          </dt>
          <dd className="inline font-mono break-all">{standard.spec}</dd>
        </div>
        <div>
          <dt className="inline text-muted-foreground">
            {t('emerging.revision')}:{' '}
          </dt>
          <dd className="inline font-mono">{standard.revision}</dd>
        </div>
        <div className="sm:col-span-2">
          {/* El SEAM es dónde encajaría, no dónde está. La etiqueta lo dice. */}
          <dt className="inline text-muted-foreground">
            {t('emerging.seam')}:{' '}
          </dt>
          <dd className="inline font-mono break-all">{standard.seam}</dd>
        </div>
      </dl>

      {/* La nota de honestidad, SIEMPRE visible: es la razón de que la fila exista. */}
      <p className="mt-2 border-l-2 border-warning pl-2 text-xs text-muted-foreground">
        {standard.caveat}
      </p>

      {standard.authority && (
        <p className="mt-1 text-xs">
          <a
            href={standard.authority}
            target="_blank"
            rel="noreferrer noopener"
            className="text-accent-text underline break-all"
          >
            {t('emerging.authority')}
          </a>
        </p>
      )}
    </li>
  )
}
