// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-02 — ciclo de vida de tenant. Retirar y restaurar el servicio, desde la consola.
//
// ⛔ LA COPY ES LA MITAD DE ESTA PANTALLA, y no es adorno. El motor documenta que retirar el
// servicio mantiene TRES cosas a propósito —autenticación y rutas de sistema, exportación de los
// datos del propio tenant, y el trabajo custodial de la cadena— y esas tres son exactamente la
// diferencia entre «retirar el servicio» y «secuestrar los datos de un cliente». Un diálogo que
// sólo preguntara «¿suspender?» escondería la parte que un operador necesita para decidir, y la que
// un cliente necesita que sea cierta.
import './i18n'
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Building2, PauseCircle, PlayCircle, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { PageHeader } from '@/components/ui/page-header'
import { Spinner } from '@/components/ui/spinner'
import { ListTruncationBadge } from '@/features/_intel'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { useAuth } from '@/lib/auth/context'
import { estadoTenant, tenantKeys, tenantsApi, type TenantDTO } from './api'

/** El tono del badge por estado. `unknown` NO usa el tono neutro de «activo»: un estado que la
 *  consola no sabe leer es una advertencia, no un silencio. */
const TONO = {
  active: 'success',
  suspended: 'warning',
  unknown: 'outline',
} as const

/**
 * El tenant reservado del sistema. Copiado de `core/model/ids.go:29` (`SystemTenantID`), y citado
 * ahí porque es lo único que permite comprobarlo: el motor lo rechaza con **400** en
 * `handleDropOrg` (`core/api/handlers_core.go:668`, `tenant.IsSystem()`).
 *
 * ⚠ Se usa para DESHABILITAR el botón con su razón, no para esconderlo. Un botón ausente no enseña
 *   nada —el operador concluye que la consola no sabe borrar— mientras que uno deshabilitado que
 *   dice por qué se explica solo. Es la misma razón por la que un permiso no declarado es peor que
 *   uno denegado.
 */
const TENANT_SISTEMA = 'ffffffff-ffff-ffff-ffff-ffffffffffff'

type Accion =
  | { tipo: 'estado'; org: TenantDTO; destino: 'active' | 'suspended' }
  | { tipo: 'borrar'; org: TenantDTO }

export function TenantsView() {
  const { t } = useTranslation('tenants')
  const { can } = useAuth()
  const puede = can('system:admin')
  const [accion, setAccion] = useState<Accion | null>(null)

  const q = useQuery({
    queryKey: tenantKeys.list(),
    queryFn: () => tenantsApi.list(),
    enabled: puede,
  })

  const mutar = usePrivilegedMutation<
    { id: string; destino: 'active' | 'suspended'; name: string },
    TenantDTO
  >({
    mutationFn: (v) => tenantsApi.setStatus(v.id, v.destino),
    invalidateKeys: [tenantKeys.all()],
    successMessage: (_d, v) =>
      v.destino === 'suspended'
        ? t('suspended', { name: v.name })
        : t('restored', { name: v.name }),
    onDone: () => setAccion(null),
  })

  // ⛔ MUTACIÓN APARTE, no un `destino: 'deleted'` metido en la de estado. Las dos operaciones no
  //    son variantes de la misma: una cambia UNA columna y es reversible sin pérdida, la otra purga
  //    y no se deshace. Compartir la mutación las haría intercambiables en el sitio donde menos
  //    conviene que lo sean — el `mutationFn` — y un día alguien pasaría el destino equivocado.
  const borrar = usePrivilegedMutation<{ id: string; name: string }, void>({
    mutationFn: (v) => tenantsApi.remove(v.id),
    invalidateKeys: [tenantKeys.all()],
    successMessage: (_d, v) => t('deleteDone', { name: v.name }),
    onDone: () => setAccion(null),
  })

  if (!puede) return <ForbiddenState description={t('forbidden')} />

  return (
    <>
      <PageHeader
        icon={Building2}
        title={t('title')}
        description={t('description')}
      />
      <Card>
        <CardHeader>
          <CardTitle>{t('roster')}</CardTitle>
        </CardHeader>
        <CardContent>
          {/* El recorte, declarado. El TRIAJE DEL MOTOR —sondas, veredicto y file:line— vive en
              `./api.ts`, y NO aqui a proposito: el censo de recorte
              (`scripts/check-list-truncation-witness.sh`) lee los `.tsx` y solo descarta las lineas de
              comentario que empiezan por `*`, `//` o `/*`. Una explicacion en prosa dentro de una vista
              le colaria los literales que busca y daria la feature por cubierta aunque nadie pintara
              nada — el falso negativo que su propia cabecera nombra. Aqui, un puntero y nada mas. */}
          <ListTruncationBadge
            query={q}
            label={t('truncation.label', { n: (q.data?.items ?? []).length })}
            hint={t('truncation.hint')}
            className="px-0 pt-0 pb-3"
          />
          {q.isPending ? (
            <div role="status" className="flex justify-center py-8">
              <Spinner />
            </div>
          ) : q.isError ? (
            <ErrorState retry={() => void q.refetch()} />
          ) : (q.data?.items ?? []).length === 0 ? (
            <EmptyState
              icon={<Building2 />}
              title={t('empty')}
              description={t('emptyHint')}
            />
          ) : (
            <div className="flex flex-col gap-2">
              {(q.data?.items ?? []).map((org) => {
                const estado = estadoTenant(org.status)
                return (
                  <div
                    key={org.id}
                    className="flex flex-wrap items-center gap-3 rounded-md border border-border p-3"
                  >
                    <span className="font-medium text-foreground">
                      {org.name}
                    </span>
                    <span className="text-xs text-muted-foreground">
                      {org.slug}
                    </span>
                    <Badge variant={TONO[estado]}>
                      {t(`status.${estado}`)}
                    </Badge>
                    <span className="text-xs text-muted-foreground">
                      {org.data_region || t('unpinned')}
                    </span>
                    {estado === 'unknown' ? (
                      <p className="w-full text-xs text-muted-foreground">
                        {t('unknownHint')}
                      </p>
                    ) : null}
                    <div className="ml-auto flex items-center gap-2">
                      {estado === 'suspended' ? (
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={() =>
                            setAccion({
                              tipo: 'estado',
                              org,
                              destino: 'active',
                            })
                          }
                        >
                          <PlayCircle />
                          {t('restore')}
                        </Button>
                      ) : (
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={() =>
                            setAccion({
                              tipo: 'estado',
                              org,
                              destino: 'suspended',
                            })
                          }
                        >
                          <PauseCircle />
                          {t('suspend')}
                        </Button>
                      )}
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={org.tenant_id === TENANT_SISTEMA}
                        title={
                          org.tenant_id === TENANT_SISTEMA
                            ? t('deleteSystem')
                            : undefined
                        }
                        onClick={() => setAccion({ tipo: 'borrar', org })}
                      >
                        <Trash2 />
                        {t('delete')}
                      </Button>
                    </div>
                  </div>
                )
              })}
            </div>
          )}
        </CardContent>
      </Card>

      {accion ? (
        accion.tipo === 'borrar' ? (
          <ConfirmDialog
            open
            onOpenChange={(v) => {
              if (!v) setAccion(null)
            }}
            tone="danger"
            // La frase SIEMPRE, y aquí no hay debate: retirar el servicio la pide porque es una
            // lista donde la fila de al lado es otro cliente; borrar la pide por eso Y porque no
            // se puede deshacer.
            confirmPhrase={accion.org.slug}
            title={t('deleteTitle', { name: accion.org.name })}
            description={t('deleteDescription')}
            confirmLabel={t('confirmDelete')}
            pending={borrar.isPending}
            onConfirm={() =>
              borrar.mutate({
                id: accion.org.tenant_id,
                name: accion.org.name,
              })
            }
          >
            <div className="flex flex-col gap-2 text-sm text-muted-foreground">
              {/* ⛔ LAS DOS FRASES QUE HACEN HONESTO ESTE DIÁLOGO. La ficha OpenAPI de la ruta dice
                  «after the cloud grace period», y esa gracia es del plano CLOUD: este motor purga
                  al confirmar. Callarlo dejaría al operador creyendo que tiene 30 días. Y la
                  segunda ofrece la puerta segura, porque un diálogo destructivo que no nombra la
                  alternativa empuja a usarlo cuando no tocaba. */}
              <p className="font-medium text-foreground">{t('deleteGrace')}</p>
              <p>{t('deleteInstead')}</p>
            </div>
          </ConfirmDialog>
        ) : (
          <ConfirmDialog
            open={accion !== null}
            onOpenChange={(v) => {
              if (!v) setAccion(null)
            }}
            tone={accion.destino === 'suspended' ? 'danger' : 'default'}
            // ⛔ La frase a teclear es el SLUG, y sólo al retirar. Retirar el servicio afecta a un
            //    tenant entero y se hace desde una lista donde la fila de al lado es otro cliente:
            //    un clic de más en la fila equivocada no puede ser suficiente. Restaurar no la pide
            //    —restaurar no puede hacer daño— y exigirla ahí enseñaría a teclear sin leer.
            confirmPhrase={
              accion.destino === 'suspended' ? accion.org.slug : undefined
            }
            title={t(
              accion.destino === 'suspended' ? 'suspendTitle' : 'restoreTitle',
              { name: accion.org.name },
            )}
            description={t(
              accion.destino === 'suspended'
                ? 'suspendDescription'
                : 'restoreDescription',
            )}
            confirmLabel={t(
              accion.destino === 'suspended'
                ? 'confirmSuspend'
                : 'confirmRestore',
            )}
            pending={mutar.isPending}
            onConfirm={() =>
              mutar.mutate({
                id: accion.org.tenant_id,
                destino: accion.destino,
                name: accion.org.name,
              })
            }
          >
            {accion.destino === 'suspended' ? (
              <div className="flex flex-col gap-2 text-sm text-muted-foreground">
                <p className="font-medium text-foreground">{t('keeps')}</p>
                <ul className="list-disc pl-5">
                  <li>{t('keepsAuth')}</li>
                  <li>{t('keepsExport')}</li>
                  <li>{t('keepsCustody')}</li>
                </ul>
                <p>{t('notDelete')}</p>
              </div>
            ) : null}
          </ConfirmDialog>
        )
      ) : null}
    </>
  )
}

export default TenantsView
