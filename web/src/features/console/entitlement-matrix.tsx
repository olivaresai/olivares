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
import { useTranslation } from 'react-i18next'
import { consoleApi, consoleKeys, type ActivationStatusDTO } from './api'
import { Badge } from '@/components/ui/badge'
import { CaveatNotice, SectionCard } from '@/features/_intel'

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

export function EntitlementMatrix({
  addons,
  features,
  edition,
}: {
  addons: AddonRow[]
  features?: string[]
  edition?: string
}) {
  const { t } = useTranslation('console')

  return (
    <SectionCard
      title={t('entitlement.title')}
      description={t('entitlement.description')}
    >
      {/* ⛔ VA ARRIBA porque condiciona la lectura de toda la tabla: dos de las tres columnas
          dirán «no se sabe» a menudo, y eso es el resultado correcto, no un fallo de carga. */}
      <CaveatNotice tone="info" className="mb-3">
        {t('entitlement.threeQuestions')}
      </CaveatNotice>

      {features === undefined ? (
        <CaveatNotice tone="warning" className="mb-3">
          {t('entitlement.noVerifiedLicense')}
        </CaveatNotice>
      ) : null}

      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b text-left text-xs uppercase tracking-wider text-muted-foreground">
              <th className="py-2 pr-4 font-medium">
                {t('entitlement.colAddon')}
              </th>
              <th className="py-2 pr-4 font-medium">
                {t('entitlement.colBinary')}
              </th>
              <th className="py-2 pr-4 font-medium">
                {t('entitlement.colEntitled')}
              </th>
              <th className="py-2 pr-4 font-medium">
                {t('entitlement.colActivated')}
              </th>
            </tr>
          </thead>
          <tbody>
            {addons.map((a) => {
              const derecho = ejeDerecho(a.key, features)
              const activado = ejeActivado(a.state)
              return (
                <tr key={a.key} className="border-b last:border-0">
                  {/* ⛔ SÓLO LA CLAVE, y no es estética: el título del add-on ya lo pinta la
                      tabla de activación de arriba, y repetirlo aquí hacía que
                      `getByText('WORM audit archive')` encontrara DOS nodos — rompiendo cuatro
                      casillas ajenas que eran correctas. La clave es además por lo que el motor
                      indexa (`ActivationAddonDTO.Key`), así que es la identidad buena para una
                      matriz de ejes. El título viaja en `title` para quien pase el ratón. */}
                  <td className="py-2 pr-4">
                    <span className="font-mono text-xs" title={a.title}>
                      {a.key}
                    </span>
                  </td>
                  <td className="py-2 pr-4">
                    {/* Siempre «no se sabe»: la edición es por artefacto. */}
                    <Celda
                      eje={ejeBinario()}
                      texto={t('entitlement.unknown')}
                    />
                  </td>
                  <td className="py-2 pr-4">
                    <Celda
                      eje={derecho}
                      texto={
                        derecho === 'yes'
                          ? t('entitlement.entitled')
                          : t('entitlement.unknown')
                      }
                    />
                  </td>
                  <td className="py-2 pr-4">
                    <Celda
                      eje={activado}
                      texto={t(`entitlement.state.${a.state}`, {
                        defaultValue: a.state,
                      })}
                    />
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>

      {/* ⛔ Y EL PORQUÉ DE LA COLUMNA QUE NUNCA SE SABE, dicho donde se lee y no en una nota al
          pie: sin esto, una columna entera de avisos parece un fallo de carga. */}
      <p className="mt-3 text-xs text-muted-foreground">
        {t('entitlement.binaryUnknownWhy', {
          edition: edition || t('entitlement.unknown'),
        })}
      </p>
    </SectionCard>
  )
}

/**
 * ⛔ EL CONTENEDOR TRAE LAS DOS FUENTES, y ésa es la razón de que exista: viven en componentes
 *    distintos de esta pestaña —la licencia en uno, la activación en otro— y la pregunta que esta
 *    pantalla contesta **sólo se puede contestar cruzándolas**. react-query deduplica por clave, así
 *    que reusar las mismas no cuesta una petición de más.
 *
 * ⛔ Y EL 501 DE LA ACTIVACIÓN NO ES UN ERROR: un binario community o más antiguo lo contesta así
 *    (`license-tab.tsx:565`). Sin add-ons no hay matriz que componer, y decirlo es la respuesta —
 *    pintar una tabla vacía sugeriría que no hay ningún add-on, que es una afirmación distinta.
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

  if (activacion.isLoading || licencia.isLoading) return null

  const addons = activacion.data?.addons ?? []
  if (addons.length === 0) {
    return (
      <SectionCard
        title={t('entitlement.title')}
        description={t('entitlement.description')}
      >
        <CaveatNotice tone="info">{t('entitlement.noAddons')}</CaveatNotice>
      </SectionCard>
    )
  }

  return (
    <EntitlementMatrix
      addons={addons as AddonRow[]}
      // ⛔ `features` ausente se propaga como `undefined` A PROPÓSITO: es lo que distingue
      //    «sin derecho» de «no se sabe», y un `?? []` aquí borraría esa diferencia.
      features={licencia.data?.features}
      edition={activacion.data?.edition ?? licencia.data?.edition}
    />
  )
}
