// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-07 — QUÉ HAY, A QUÉ SE TIENE DERECHO Y QUÉ ESTÁ ENCENDIDO: tres preguntas distintas.
//
// ⛔ LA ADJUDICACIÓN QUE HIZO POSIBLE ESTA PANTALLA (the integrator, 2026-08-17) fue **negar la
//    premisa de la fila**: no hay una sola autoridad sobre «qué módulos hay» y **no debe haberla**.
//    Buscar «la fuente» es justo el defecto que esta pantalla existe para no cometer. Así que la
//    consola COMPONE tres fuentes independientes y, cuando una falta, dice **«no se sabe» — nunca
//    «no»**.
//
//    La diferencia no es de matiz. «No» es una afirmación sobre el producto que quien la lee usa
//    para comprar, para escalar o para descartar una funcionalidad. «No se sabe» es una afirmación
//    sobre NUESTRA capacidad de medir. Confundirlas en la pantalla que responde «¿qué tengo?» es
//    el peor sitio del producto para hacerlo.
//
// LAS TRES FUENTES, con su `file:line` y su semántica exacta:
//
//   1. EN EL BINARIO — `LicenseStatus.Edition` (`core/api/license.go:73`) y
//      `ActivationStatusDTO.Edition` (`core/api/activation.go:46`). Es `community | enterprise` y
//      es **función del build tag**: «precisely what a restart-free hot-apply CANNOT change; only
//      a binary swap does».
//
//      ⛔ PERO ES POR ARTEFACTO, NO POR MÓDULO. El motor no publica qué módulo compiló cada
//      binario, así que **por add-on este eje es honestamente «no se sabe»**. Y hay una tentación
//      concreta que esta pantalla NO cede: `ActivationAddonDTO.Preset` dice qué NIVEL introduce
//      cada add-on, y sería fácil deducir «community ⇒ no está en el binario». Sería inventar la
//      fuente que falta: un preset es EMPAQUETADO y el build tag es COMPILACIÓN, y no son lo
//      mismo. Se declara el desconocido en vez de fabricar la respuesta.
//
//   2. CON DERECHO — `LicenseStatus.Features []string` (`license.go:92`), las reclamaciones
//      ATESTIGUADAS de la licencia. Es `omitempty` y sólo llega **con la licencia verificada**
//      (valid | expired | perpetual, `:88`).
//
//      ⛔ ⇒ **`Features` ausente NO es «sin derecho»: es «no se sabe»**, porque sin licencia
//      verificada no hay nada que atestigüe nada. Y si `Features` llega pero una entrada no casa
//      con ninguna clave de add-on, ese eje queda «no se sabe» para ese add-on — la lista es libre
//      y no está verificada contra un catálogo.
//
//   3. ACTIVADO — `ActivationAddonDTO.State` (`activation.go:60`): `active | pending | available |
//      console`. Éste **sí se sabe siempre**: es el estado que el motor publica por add-on.
import { useQuery } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import {
  consoleApi,
  consoleKeys,
  type ActivationStatusDTO,
  type LicenseStatusDTO,
} from './api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { CaveatNotice, SectionCard } from '@/features/_intel'
import { ApiError, isOpenCoreSeam } from '@/lib/api/errors'
import { cn } from '@/lib/utils'

const DISCLOSURE_SUMMARY_CLASS = cn(
  'cursor-pointer rounded-sm text-sm text-foreground',
  'outline-none focus-visible:ring-2 focus-visible:ring-ring',
  'focus-visible:ring-offset-2 focus-visible:ring-offset-background',
)

function OperatorDisclosure({
  slot,
  summary,
  children,
}: {
  slot: string
  summary: string
  children: ReactNode
}) {
  return (
    <details className="text-sm text-muted-foreground" data-slot={slot}>
      <summary className={DISCLOSURE_SUMMARY_CLASS}>{summary}</summary>
      <div className="mt-2 flex flex-col gap-2">{children}</div>
    </details>
  )
}

/** Los tres valores que una celda puede tomar. `unknown` NO es un tercer «no». */
type Eje = 'yes' | 'no' | 'unknown'

export interface AddonRow {
  key: string
  title?: string
  state: string
  preset?: string
  reason?: string
}

/**
 * ⛔ LA COMPOSICIÓN, COMO FUNCIÓN PURA, porque es la parte que se puede equivocar y debe poder
 *    probarse sin pintar nada.
 *
 *    `features === undefined` significa **licencia sin verificar** ⇒ derecho «no se sabe».
 *    `features` presente pero sin la clave ⇒ también «no se sabe»: la lista es libre y no está
 *    verificada contra el catálogo de add-ons, así que su silencio no es una negativa.
 */
export function ejeDerecho(
  addonKey: string,
  features: string[] | undefined,
): Eje {
  if (features === undefined) return 'unknown'
  return features.includes(addonKey) ? 'yes' : 'unknown'
}

/** El eje del binario es «no se sabe» por add-on, y el porqué está en la cabecera de este fichero. */
export function ejeBinario(): Eje {
  return 'unknown'
}

/** Activado sí se sabe: el motor publica el estado por add-on. */
export function ejeActivado(state: string): Eje {
  if (state === 'active') return 'yes'
  if (state === 'pending') return 'unknown'
  return 'no'
}

function Celda({ eje, texto }: { eje: Eje; texto: string }) {
  const variant =
    eje === 'yes' ? 'success' : eje === 'no' ? 'neutral' : 'warning'
  return <Badge variant={variant}>{texto}</Badge>
}

/** One matrix fact: table cell on wide layouts, labelled row on narrow ones. */
function MatrixFact({
  label,
  children,
}: {
  label: string
  children: ReactNode
}) {
  return (
    <td className="py-2 pr-4 max-md:flex max-md:flex-wrap max-md:items-center max-md:justify-between max-md:gap-2 max-md:border-t max-md:border-border/60 max-md:py-2 max-md:pr-0">
      <span
        className="text-xs text-muted-foreground md:hidden"
        aria-hidden="true"
      >
        {label}
      </span>
      <span className="min-w-0 max-md:shrink-0">{children}</span>
    </td>
  )
}

/** Why current entitlement is unknown. Failed/pending reads are not "no license". */
export type EntitlementUnknownReason =
  'no-verified' | 'refresh-failed' | 'unavailable' | 'loading'

export function EntitlementMatrix({
  addons,
  features,
  edition,
  entitlementUnknownReason = 'no-verified',
  lastSuccessfulLicense,
  licenseRetry,
  licenseErrorDetail,
}: {
  addons: AddonRow[]
  features?: string[]
  edition?: string
  entitlementUnknownReason?: EntitlementUnknownReason
  lastSuccessfulLicense?: Pick<LicenseStatusDTO, 'edition' | 'status'>
  licenseRetry?: () => void
  licenseErrorDetail?: CatalogueErrorDetail
}) {
  const { t } = useTranslation('console')
  const lastEdition = lastSuccessfulLicense?.edition
    ? t(`license.editions.${lastSuccessfulLicense.edition}`, {
        defaultValue: lastSuccessfulLicense.edition,
      })
    : t('entitlement.unknown')
  const lastStatus = lastSuccessfulLicense?.status
    ? t(`license.statuses.${lastSuccessfulLicense.status}`, {
        defaultValue: lastSuccessfulLicense.status,
      })
    : t('entitlement.unknown')
  const unknownCopy =
    entitlementUnknownReason === 'refresh-failed'
      ? t('entitlement.licenseRefreshFailed')
      : entitlementUnknownReason === 'unavailable'
        ? t('entitlement.licenseFactsUnavailable')
        : entitlementUnknownReason === 'loading'
          ? t('entitlement.licenseLoading')
          : t('entitlement.noVerifiedLicense')

  return (
    <SectionCard
      title={t('entitlement.title')}
      description={t('entitlement.description')}
    >
      {features === undefined ? (
        <div
          className="mb-3 flex flex-col gap-3"
          data-slot="license-read-state"
          data-kind={entitlementUnknownReason}
        >
          <CaveatNotice
            tone={entitlementUnknownReason === 'loading' ? 'info' : 'warning'}
          >
            {unknownCopy}
          </CaveatNotice>
          {entitlementUnknownReason === 'refresh-failed' &&
          (lastSuccessfulLicense?.edition || lastSuccessfulLicense?.status) ? (
            <p className="text-sm text-muted-foreground">
              {t('entitlement.licenseLastSuccessfulRead', {
                edition: lastEdition,
                status: lastStatus,
              })}
            </p>
          ) : null}
          {licenseRetry &&
          (entitlementUnknownReason === 'refresh-failed' ||
            entitlementUnknownReason === 'unavailable') ? (
            <>
              <p className="text-sm text-muted-foreground">
                {t('entitlement.licenseRefreshFailedAction')}
              </p>
              <LicenseRetry onRetry={licenseRetry} />
              <ReadTechnicalDetail
                endpoint={LICENSE_STATUS_PATH}
                status={licenseErrorDetail?.status}
                code={licenseErrorDetail?.code}
                requestId={licenseErrorDetail?.requestId}
              />
            </>
          ) : null}
        </div>
      ) : null}

      <div className="min-w-0" data-slot="entitlement-matrix-frame">
        <table
          className="w-full min-w-0 text-sm max-md:block"
          data-slot="entitlement-matrix-table"
        >
          <thead className="max-md:sr-only">
            <tr className="border-b text-left text-xs uppercase tracking-wider text-muted-foreground">
              <th scope="col" className="py-2 pr-4 font-medium">
                {t('entitlement.colAddon')}
              </th>
              <th scope="col" className="py-2 pr-4 font-medium">
                {t('entitlement.colBinary')}
              </th>
              <th scope="col" className="py-2 pr-4 font-medium">
                {t('entitlement.colEntitled')}
              </th>
              <th scope="col" className="py-2 pr-4 font-medium">
                {t('entitlement.colActivated')}
              </th>
            </tr>
          </thead>
          <tbody className="max-md:block">
            {addons.map((a) => {
              const derecho = ejeDerecho(a.key, features)
              const activado = ejeActivado(a.state)
              return (
                <tr
                  key={a.key}
                  className="border-b last:border-0 max-md:mb-3 max-md:block max-md:rounded-md max-md:border max-md:border-border max-md:p-3 max-md:last:border"
                >
                  {/* ⛔ SÓLO LA CLAVE, y no es estética: el título del add-on ya lo pinta la
                      tabla de activación de arriba, y repetirlo aquí hacía que
                      `getByText('WORM audit archive')` encontrara DOS nodos — rompiendo cuatro
                      casillas ajenas que eran correctas. La clave es además por lo que el motor
                      indexa (`ActivationAddonDTO.Key`), así que es la identidad buena para una
                      matriz de ejes. El título viaja en `title` para quien pase el ratón. */}
                  <th
                    scope="row"
                    className="py-2 pr-4 text-left font-normal max-md:block max-md:pb-2 max-md:pr-0"
                  >
                    <span
                      className="mb-1 block text-xs font-medium tracking-wide text-muted-foreground uppercase md:hidden"
                      aria-hidden="true"
                    >
                      {t('entitlement.colAddon')}
                    </span>
                    <span
                      className="block font-mono text-xs break-all"
                      title={a.title}
                    >
                      {a.key}
                    </span>
                  </th>
                  <MatrixFact label={t('entitlement.colBinary')}>
                    {/* Siempre «no se sabe»: la edición es por artefacto. */}
                    <Celda
                      eje={ejeBinario()}
                      texto={t('entitlement.unknown')}
                    />
                  </MatrixFact>
                  <MatrixFact label={t('entitlement.colEntitled')}>
                    <Celda
                      eje={derecho}
                      texto={
                        derecho === 'yes'
                          ? t('entitlement.entitled')
                          : t('entitlement.unknown')
                      }
                    />
                  </MatrixFact>
                  <MatrixFact label={t('entitlement.colActivated')}>
                    <Celda
                      eje={activado}
                      texto={t(`entitlement.state.${a.state}`, {
                        defaultValue: a.state,
                      })}
                    />
                  </MatrixFact>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>

      <div className="mt-3">
        <OperatorDisclosure
          slot="entitlement-source-help"
          summary={t('entitlement.helpSummary')}
        >
          <p>{t('entitlement.threeQuestions')}</p>
          <p>
            {t('entitlement.binaryUnknownWhy', {
              edition: edition || t('entitlement.unknown'),
            })}
          </p>
        </OperatorDisclosure>
      </div>
    </SectionCard>
  )
}

/** The activation GET used to compose the matrix — never a write. */
export const ACTIVATION_CATALOGUE_PATH = '/v1/console/activation'

/** The license GET used to compose entitlement — never a write. */
export const LICENSE_STATUS_PATH = '/v1/console/license'

/** Slice of a react-query result the classifier needs. Stale `data` is not current. */
export type ActivationQuerySlice = {
  isPending: boolean
  isError: boolean
  error: unknown
  data: ActivationStatusDTO | undefined
}

export type CatalogueErrorDetail = {
  status?: number
  code?: string
  requestId?: string
}

/**
 * Status/code/request-id only. Never the response body, error message, env or
 * secret-bearing fields — those are not operator-facing technical detail here.
 */
export function catalogueErrorDetail(error: unknown): CatalogueErrorDetail {
  if (error instanceof ApiError) {
    return {
      status: error.status,
      code: error.code || undefined,
      requestId: error.requestId,
    }
  }
  return {}
}

export type CatalogueRead =
  | { kind: 'loading' }
  | {
      kind: 'unavailable'
      status: number
      code?: string
      requestId?: string
      hadPriorData: boolean
    }
  | {
      kind: 'failed'
      status?: number
      code?: string
      requestId?: string
      hadPriorData: boolean
    }
  | { kind: 'empty' }
  | { kind: 'ready'; addons: AddonRow[] }

/**
 * ⛔ UNKNOWN MUST NOT BECOME EMPTY. `addons ?? []` collapsed a 501, a failed
 *    read, a missing/null `addons` field and a successful `[]` into one
 *    "no add-ons" card. Those are four different facts.
 *
 *    `addons: []` on a 2xx is a known empty catalogue.
 *    HTTP 501 (`isOpenCoreSeam`) is catalogue unavailable for this build.
 *    Any other error is a failed read.
 *    `addons` absent or not an array is not a known empty list.
 *    Stale successful data next to an error is not current truth.
 */
export function classifyActivationRead(q: ActivationQuerySlice): CatalogueRead {
  const hadPriorData = Array.isArray(q.data?.addons)
  if (q.isError) {
    const detail = catalogueErrorDetail(q.error)
    if (isOpenCoreSeam(q.error)) {
      return {
        kind: 'unavailable',
        status: detail.status ?? 501,
        code: detail.code,
        requestId: detail.requestId,
        hadPriorData,
      }
    }
    return {
      kind: 'failed',
      status: detail.status,
      code: detail.code,
      requestId: detail.requestId,
      hadPriorData,
    }
  }
  if (q.isPending && q.data === undefined) return { kind: 'loading' }
  if (q.data === undefined) return { kind: 'loading' }
  if (!Array.isArray(q.data.addons)) {
    return { kind: 'failed', hadPriorData: false }
  }
  if (q.data.addons.length === 0) return { kind: 'empty' }
  return { kind: 'ready', addons: q.data.addons }
}

/** Slice of a license query. Stale `data` after an error is last success, not current. */
export type LicenseQuerySlice = {
  isPending: boolean
  isError: boolean
  error: unknown
  data: LicenseStatusDTO | undefined
}

export type LicenseRead =
  | { kind: 'loading' }
  | {
      kind: 'failed'
      hadPriorData: boolean
      lastSuccess?: LicenseStatusDTO
      status?: number
      code?: string
      requestId?: string
    }
  | { kind: 'success'; license: LicenseStatusDTO }

/**
 * ⛔ A FAILED REFRESH IS NOT THE LAST SUCCESS. TanStack Query keeps prior
 *    `data` after an error. Feeding that payload to the matrix as current
 *    facts paints a green "entitled" cell and "license status Valid" after
 *    the read that would have to support those claims has failed.
 *
 *    Prior edition/status may be shown only as last successful read.
 *    Cached features must not supply current entitlement. No prior success
 *    means current facts are unavailable — not "no license".
 */
export function classifyLicenseRead(q: LicenseQuerySlice): LicenseRead {
  if (q.isError) {
    const detail = catalogueErrorDetail(q.error)
    return {
      kind: 'failed',
      hadPriorData: q.data !== undefined,
      lastSuccess: q.data,
      status: detail.status,
      code: detail.code,
      requestId: detail.requestId,
    }
  }
  if (q.data === undefined) return { kind: 'loading' }
  return { kind: 'success', license: q.data }
}

export function LicenseReadStatus({
  read,
  onRetry,
}: {
  read: LicenseRead
  onRetry: () => void
}) {
  const { t } = useTranslation('console')
  if (read.kind === 'loading') {
    return (
      <p
        className="text-sm text-muted-foreground"
        data-slot="license-read-state"
        data-kind="loading"
      >
        {t('entitlement.licenseLoading')}
      </p>
    )
  }
  if (read.kind === 'failed') {
    const edition = read.lastSuccess?.edition
      ? t(`license.editions.${read.lastSuccess.edition}`, {
          defaultValue: read.lastSuccess.edition,
        })
      : t('entitlement.unknown')
    const status = read.lastSuccess?.status
      ? t(`license.statuses.${read.lastSuccess.status}`, {
          defaultValue: read.lastSuccess.status,
        })
      : t('entitlement.unknown')
    return (
      <div
        className="flex flex-col gap-3"
        data-slot="license-read-state"
        data-kind="failed"
        data-had-prior={read.hadPriorData ? 'true' : 'false'}
      >
        <CaveatNotice tone="warning">
          {read.hadPriorData
            ? t('entitlement.licenseRefreshFailed')
            : t('entitlement.licenseFactsUnavailable')}
        </CaveatNotice>
        {read.hadPriorData &&
        (read.lastSuccess?.edition || read.lastSuccess?.status) ? (
          <p className="text-sm text-muted-foreground">
            {t('entitlement.licenseLastSuccessfulRead', { edition, status })}
          </p>
        ) : null}
        <p className="text-sm text-muted-foreground">
          {t('entitlement.licenseRefreshFailedAction')}
        </p>
        <LicenseRetry onRetry={onRetry} />
        <ReadTechnicalDetail
          endpoint={LICENSE_STATUS_PATH}
          status={read.status}
          code={read.code}
          requestId={read.requestId}
        />
      </div>
    )
  }
  const license = read.license
  if (!license.edition && !license.status) {
    return (
      <p
        className="text-sm text-muted-foreground"
        data-slot="license-read-state"
        data-kind="success"
      >
        {t('entitlement.knownFactsUnavailable')}
      </p>
    )
  }
  const edition = license.edition
    ? t(`license.editions.${license.edition}`, {
        defaultValue: license.edition,
      })
    : t('entitlement.unknown')
  const status = license.status
    ? t(`license.statuses.${license.status}`, {
        defaultValue: license.status,
      })
    : t('entitlement.unknown')
  return (
    <p
      className="text-sm text-foreground"
      data-slot="license-read-state"
      data-kind="success"
    >
      {t('entitlement.knownFacts', { edition, status })}
    </p>
  )
}

function ReadTechnicalDetail({
  endpoint,
  status,
  code,
  requestId,
}: CatalogueErrorDetail & { endpoint: string }) {
  const { t } = useTranslation('console')
  if (status === undefined && !code && !requestId) return null
  const codePart = code ? ` · ${code}` : ''
  const requestPart = requestId ? ` · ${requestId}` : ''
  return (
    <details className="text-xs text-muted-foreground">
      <summary className={DISCLOSURE_SUMMARY_CLASS}>
        {t('entitlement.technicalSummary')}
      </summary>
      <p className="mt-1 font-mono">
        {t('entitlement.technicalDetail', {
          method: 'GET',
          endpoint,
          status: status ?? '—',
          codePart,
          requestPart,
        })}
      </p>
    </details>
  )
}

function CatalogueTechnicalDetail(props: CatalogueErrorDetail) {
  return <ReadTechnicalDetail endpoint={ACTIVATION_CATALOGUE_PATH} {...props} />
}

function CatalogueSourceHelp() {
  const { t } = useTranslation('console')
  return (
    <OperatorDisclosure
      slot="entitlement-source-help"
      summary={t('entitlement.helpSummary')}
    >
      <p>{t('entitlement.threeQuestions')}</p>
      <p>{t('entitlement.catalogueSourceHelp')}</p>
    </OperatorDisclosure>
  )
}

function CatalogueRetry({ onRetry }: { onRetry: () => void }) {
  const { t } = useTranslation(['console', 'common'])
  return (
    <Button
      variant="secondary"
      size="sm"
      onClick={onRetry}
      data-slot="catalogue-retry"
    >
      {t('common:actions.retry')}
    </Button>
  )
}

function LicenseRetry({ onRetry }: { onRetry: () => void }) {
  const { t } = useTranslation('console')
  return (
    <Button
      variant="secondary"
      size="sm"
      onClick={onRetry}
      data-slot="license-retry"
    >
      {t('entitlement.licenseRetry')}
    </Button>
  )
}

/**
 * ⛔ EL CONTENEDOR TRAE LAS DOS FUENTES, y ésa es la razón de que exista: viven en componentes
 *    distintos de esta pestaña —la licencia en uno, la activación en otro— y la pregunta que esta
 *    pantalla contesta **sólo se puede contestar cruzándolas**. react-query deduplica por clave, así
 *    que reusar las mismas no cuesta una petición de más.
 *
 * ⛔ Y EL 501 DE LA ACTIVACIÓN NO ES UN ERROR NI UN CATÁLOGO VACÍO: un binario community o más
 *    antiguo lo contesta así (`core/api/handlers_activation.go`). Los hechos de edición/licencia
 *    siguen viniendo de GET /v1/console/license y se atribuyen aparte. Un `?? []` aquí convertiría
 *    «no se ha podido leer el catálogo» en «no hay add-ons».
 */
export function EntitlementMatrixCard() {
  const { t } = useTranslation('console')

  const licencia = useQuery({
    queryKey: consoleKeys.license(),
    queryFn: () => consoleApi.getLicense(),
  })
  const activacion = useQuery<ActivationStatusDTO>({
    queryKey: consoleKeys.activation(),
    queryFn: () => consoleApi.getActivation(),
    retry: false,
  })

  const read = classifyActivationRead({
    isPending: activacion.isPending,
    isError: activacion.isError,
    error: activacion.error,
    data: activacion.data,
  })
  const licenseRead = classifyLicenseRead({
    isPending: licencia.isPending,
    isError: licencia.isError,
    error: licencia.error,
    data: licencia.data,
  })
  const retry = () => {
    void activacion.refetch()
  }
  const retryLicense = () => {
    void licencia.refetch()
  }
  const currentFeatures =
    licenseRead.kind === 'success' ? licenseRead.license.features : undefined
  const entitlementUnknownReason: EntitlementUnknownReason =
    licenseRead.kind === 'failed'
      ? licenseRead.hadPriorData
        ? 'refresh-failed'
        : 'unavailable'
      : licenseRead.kind === 'loading'
        ? 'loading'
        : 'no-verified'
  const lastSuccessfulLicense =
    licenseRead.kind === 'failed' ? licenseRead.lastSuccess : undefined

  const shell = (node: ReactNode) => (
    <div data-slot="entitlement-catalogue">{node}</div>
  )

  if (read.kind === 'loading') {
    return shell(
      <SectionCard
        title={t('entitlement.title')}
        description={t('entitlement.description')}
      >
        <div className="flex justify-center py-6">
          <Spinner />
        </div>
        <p className="text-center text-sm text-muted-foreground">
          {t('entitlement.loading')}
        </p>
      </SectionCard>,
    )
  }

  if (read.kind === 'unavailable') {
    return shell(
      <SectionCard
        title={t('entitlement.title')}
        description={t('entitlement.description')}
      >
        <div className="flex flex-col gap-3" role="status">
          <CaveatNotice tone="warning">
            {t('entitlement.unavailable')}
          </CaveatNotice>
          <LicenseReadStatus read={licenseRead} onRetry={retryLicense} />
          {read.hadPriorData ? (
            <CaveatNotice tone="warning">
              {t('entitlement.staleNotCurrent')}
            </CaveatNotice>
          ) : null}
          <p className="text-sm text-muted-foreground">
            {t('entitlement.unavailableAction')}
          </p>
          <CatalogueRetry onRetry={retry} />
          <CatalogueTechnicalDetail
            status={read.status}
            code={read.code}
            requestId={read.requestId}
          />
          <CatalogueSourceHelp />
        </div>
      </SectionCard>,
    )
  }

  if (read.kind === 'failed') {
    return shell(
      <SectionCard
        title={t('entitlement.title')}
        description={t('entitlement.description')}
      >
        <div className="flex flex-col gap-3" role="alert">
          <CaveatNotice tone="warning">{t('entitlement.failed')}</CaveatNotice>
          <LicenseReadStatus read={licenseRead} onRetry={retryLicense} />
          {read.hadPriorData ? (
            <CaveatNotice tone="warning">
              {t('entitlement.staleNotCurrent')}
            </CaveatNotice>
          ) : null}
          <p className="text-sm text-muted-foreground">
            {t('entitlement.failedAction')}
          </p>
          <CatalogueRetry onRetry={retry} />
          <CatalogueTechnicalDetail
            status={read.status}
            code={read.code}
            requestId={read.requestId}
          />
          <CatalogueSourceHelp />
        </div>
      </SectionCard>,
    )
  }

  if (read.kind === 'empty') {
    return shell(
      <SectionCard
        title={t('entitlement.title')}
        description={t('entitlement.description')}
      >
        <div className="flex flex-col gap-3" role="status">
          <CaveatNotice tone="info">{t('entitlement.noAddons')}</CaveatNotice>
          <LicenseReadStatus read={licenseRead} onRetry={retryLicense} />
          <CatalogueSourceHelp />
        </div>
      </SectionCard>,
    )
  }

  return shell(
    <EntitlementMatrix
      addons={read.addons}
      // ⛔ `features` ausente se propaga como `undefined` A PROPÓSITO: es lo que distingue
      //    «sin derecho» de «no se sabe», y un `?? []` aquí borraría esa diferencia.
      //    A failed license read must not pass cached features as current.
      features={currentFeatures}
      edition={activacion.data?.edition}
      entitlementUnknownReason={entitlementUnknownReason}
      lastSuccessfulLicense={lastSuccessfulLicense}
      licenseRetry={licenseRead.kind === 'failed' ? retryLicense : undefined}
      licenseErrorDetail={
        licenseRead.kind === 'failed'
          ? {
              status: licenseRead.status,
              code: licenseRead.code,
              requestId: licenseRead.requestId,
            }
          : undefined
      }
    />,
  )
}
