// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { ApiError } from './errors'
import { QueryObserver } from '@tanstack/react-query'
import { createQueryClient } from './query'
import { isolateCacheOnTenantChange } from './tenant-cache-isolation'
import { useTenantStore } from '@/stores/tenant'

/**
 * Testigo del envenenamiento de caché entre inquilinos.
 *
 * La forma de cada caso reproduce el reparto REAL de responsabilidades: la CLAVE lleva el
 * inquilino que estaba activo al pintar (lo exige el trinquete de) y la RESPUESTA lleva el
 * que el cliente lee al enviar (`client.ts:213`, de un getter al store). Nada más hace falta para
 * que el defecto aparezca — y por eso el primer caso NO instala el arreglo: es el control
 * positivo que demuestra que la guarda sostiene algo.
 */

interface Aplazada<T> {
  promise: Promise<T>
  resolve: (v: T) => void
}
function aplazada<T>(): Aplazada<T> {
  let resolve!: (v: T) => void
  const promise = new Promise<T>((r) => {
    resolve = r
  })
  return { promise, resolve }
}

/** Cede el turno al bucle de eventos las veces que haga falta para que TanStack avance. */
async function turnos(n = 4): Promise<void> {
  for (let i = 0; i < n; i++) await Promise.resolve()
}

const CLAVE_A = ['probe', 'inquilino-A', 'items'] as const

let desuscribir: (() => void) | undefined

beforeEach(() => {
  useTenantStore.setState({ activeTenant: 'inquilino-A' })
})
afterEach(() => {
  desuscribir?.()
  desuscribir = undefined
  useTenantStore.setState({ activeTenant: null })
})

describe('aislamiento de caché al cambiar de inquilino', () => {
  it('CONTROL POSITIVO — sin la guarda, la respuesta del inquilino nuevo se guarda bajo la clave del viejo', async () => {
    const qc = createQueryClient()
    const puerta = aplazada<void>()
    let leidoAlEnviar: string | null = null

    const p = qc
      .fetchQuery({
        queryKey: CLAVE_A,
        queryFn: async () => {
          // El cliente real lee el inquilino AQUÍ, después de una posible espera
          // (`await refreshOnce()` en client.ts:206). La puerta representa esa espera.
          await puerta.promise
          leidoAlEnviar = useTenantStore.getState().activeTenant
          return { deInquilino: leidoAlEnviar }
        },
      })
      .catch(() => undefined)

    await turnos()
    useTenantStore.setState({ activeTenant: 'inquilino-B' }) // el operador cambia
    puerta.resolve()
    await p

    // Cotas: demuestran que el caso EJERCIÓ el camino, antes del veredicto.
    expect(leidoAlEnviar).toBe('inquilino-B')
    expect(qc.getQueryState(CLAVE_A)?.dataUpdatedAt).toBeGreaterThan(0)
    // Y el veredicto: dato de B bajo la clave de A. Esto es el defecto.
    expect(qc.getQueryData(CLAVE_A)).toEqual({ deInquilino: 'inquilino-B' })
  })

  it('con la guarda, esa misma respuesta NO llega a la caché', async () => {
    const qc = createQueryClient()
    desuscribir = isolateCacheOnTenantChange(qc)
    const puerta = aplazada<void>()
    let leidoAlEnviar: string | null = null

    const p = qc
      .fetchQuery({
        queryKey: CLAVE_A,
        queryFn: async () => {
          await puerta.promise
          leidoAlEnviar = useTenantStore.getState().activeTenant
          return { deInquilino: leidoAlEnviar }
        },
      })
      .catch(() => undefined)

    await turnos()
    useTenantStore.setState({ activeTenant: 'inquilino-B' })
    puerta.resolve()
    await p
    await turnos()

    // Cota: el mismo camino se ejerció (la petición SÍ llegó a leer el inquilino nuevo)…
    expect(leidoAlEnviar).toBe('inquilino-B')
    // …y aun así no escribió.
    expect(qc.getQueryData(CLAVE_A)).toBeUndefined()
  })

  it('el reintento posterior al cambio tampoco escribe — y el cliente de producción SÍ reintenta', async () => {
    // Cota primero: si el cliente real no reintentara, esta ventana no existiría y el caso
    // estaría midiendo una situación imposible.
    const reintentar = createQueryClient().getDefaultOptions().queries?.retry
    expect(typeof reintentar).toBe('function')
    const decide = reintentar as (n: number, e: Error) => boolean
    expect(decide(0, new ApiError(503, 'unavailable', 'boom'))).toBe(true)
    expect(decide(0, new ApiError(403, 'forbidden', 'no'))).toBe(false)

    const qc = createQueryClient()
    desuscribir = isolateCacheOnTenantChange(qc)
    let intentos = 0

    const p = qc
      .fetchQuery({
        queryKey: CLAVE_A,
        // retryDelay 0 sólo ACORTA la ventana; en producción el retardo la ensancha.
        retry: 1,
        retryDelay: 0,
        queryFn: async () => {
          intentos++
          if (intentos === 1) {
            // Entre el fallo y el reintento, el operador cambia de inquilino.
            useTenantStore.setState({ activeTenant: 'inquilino-B' })
            throw new ApiError(503, 'unavailable', 'boom')
          }
          return { deInquilino: useTenantStore.getState().activeTenant }
        },
      })
      .catch(() => undefined)

    await p
    await turnos()

    expect(intentos).toBeGreaterThanOrEqual(1)
    expect(qc.getQueryData(CLAVE_A)).toBeUndefined()
  })

  it('CONTRAFACTUAL EN LA OTRA DIRECCIÓN — sin cambio de inquilino la guarda no estorba', async () => {
    const qc = createQueryClient()
    desuscribir = isolateCacheOnTenantChange(qc)

    await qc.fetchQuery({
      queryKey: CLAVE_A,
      queryFn: async () => ({ deInquilino: 'inquilino-A' }),
    })

    expect(qc.getQueryData(CLAVE_A)).toEqual({ deInquilino: 'inquilino-A' })
  })

  it('escribir el MISMO inquilino no cancela nada (un set redundante no es un cambio)', async () => {
    const qc = createQueryClient()
    desuscribir = isolateCacheOnTenantChange(qc)
    const puerta = aplazada<void>()

    const p = qc.fetchQuery({
      queryKey: CLAVE_A,
      queryFn: async () => {
        await puerta.promise
        return { deInquilino: 'inquilino-A' }
      },
    })

    await turnos()
    useTenantStore.setState({ activeTenant: 'inquilino-A' }) // mismo valor
    puerta.resolve()
    await p

    expect(qc.getQueryData(CLAVE_A)).toEqual({ deInquilino: 'inquilino-A' })
  })

  it('tras un cambio real, un set redundante del NUEVO inquilino ya no cancela', async () => {
    // ⛔ ESTE CASO EXISTE POR UN MUTANTE QUE ESCAPÓ: si `previo` no se actualiza, la guarda
    //    sigue comparando contra el inquilino inicial y cancela en CADA escritura posterior,
    //    aunque no haya cambio. No pierde datos —cancela de más, nunca de menos—, pero mata
    //    consultas buenas del inquilino en el que el operador ya está trabajando, y ningún
    //    otro caso lo notaba.
    const qc = createQueryClient()
    desuscribir = isolateCacheOnTenantChange(qc)
    useTenantStore.setState({ activeTenant: 'inquilino-B' }) // cambio de verdad
    const CLAVE_B = ['probe', 'inquilino-B', 'items'] as const
    const puerta = aplazada<void>()

    const p = qc
      .fetchQuery({
        queryKey: CLAVE_B,
        queryFn: async () => {
          await puerta.promise
          return { deInquilino: useTenantStore.getState().activeTenant }
        },
      })
      .catch(() => undefined)

    await turnos()
    useTenantStore.setState({ activeTenant: 'inquilino-B' }) // redundante: no es un cambio
    puerta.resolve()
    await p
    await turnos()

    expect(qc.getQueryData(CLAVE_B)).toEqual({ deInquilino: 'inquilino-B' })
  })

  /** Suscribe un observador para que la consulta sea ACTIVA, como lo son las de React. */
  function observa(
    qc: ReturnType<typeof createQueryClient>,
    opciones: {
      queryKey: readonly unknown[]
      queryFn: () => Promise<unknown>
    },
  ): () => void {
    const obs = new QueryObserver(qc, {
      queryKey: opciones.queryKey as unknown[],
      queryFn: opciones.queryFn,
      retry: false,
    })
    return obs.subscribe(() => {})
  }

  it('CONTROL POSITIVO (OBSERVADA) — sin guarda, una consulta que React observa también se envenena', async () => {
    const qc = createQueryClient()
    const puerta = aplazada<void>()
    const baja = observa(qc, {
      queryKey: CLAVE_A,
      queryFn: async () => {
        await puerta.promise
        return { deInquilino: useTenantStore.getState().activeTenant }
      },
    })

    await turnos()
    useTenantStore.setState({ activeTenant: 'inquilino-B' })
    puerta.resolve()
    await turnos(12)
    baja()

    expect(qc.getQueryData(CLAVE_A)).toEqual({ deInquilino: 'inquilino-B' })
  })

  it('con la guarda, una consulta OBSERVADA tampoco escribe', async () => {
    // ⛔ ESTE CASO EXISTE POR EL CONTRASTE. Todos los demás usan `fetchQuery`, o sea consultas
    //    INACTIVAS, y un mutante `cancelQueries({ type: 'inactive' })` los dejaba a todos verdes
    //    dejando envenenada exactamente la clase de consulta que la consola usa: la observada.
    const qc = createQueryClient()
    desuscribir = isolateCacheOnTenantChange(qc)
    const puerta = aplazada<void>()
    const baja = observa(qc, {
      queryKey: CLAVE_A,
      queryFn: async () => {
        await puerta.promise
        return { deInquilino: useTenantStore.getState().activeTenant }
      },
    })

    await turnos()
    useTenantStore.setState({ activeTenant: 'inquilino-B' })
    puerta.resolve()
    await turnos(12)
    baja()

    expect(qc.getQueryData(CLAVE_A)).toBeUndefined()
  })

  it('una consulta GLOBAL en vuelo NO se cancela — la primera versión la dejaba en blanco', async () => {
    // ⛔ REGRESIÓN MEDIDA POR EL CONTRASTE sobre la primera versión (`cancelQueries()` sin filtro):
    //    una consulta global cancelada al vuelo queda en {pending, idle} y NO se reintenta sola.
    //    `isLoading` es falso ahí, así que `AsyncSection` pinta `null` y la sección se queda
    //    vacía mientras la vista siga montada. Curar el envenenamiento no puede costar eso.
    const qc = createQueryClient()
    desuscribir = isolateCacheOnTenantChange(qc)
    const CLAVE_GLOBAL = ['tenants', 'list'] as const // sin inquilino: es global
    const puerta = aplazada<void>()

    const p = qc
      .fetchQuery({
        queryKey: CLAVE_GLOBAL,
        queryFn: async () => {
          await puerta.promise
          return { global: true }
        },
      })
      .catch(() => undefined)

    await turnos()
    useTenantStore.setState({ activeTenant: 'inquilino-B' })
    puerta.resolve()
    await p
    await turnos()

    expect(qc.getQueryState(CLAVE_GLOBAL)?.fetchStatus).not.toBe('fetching')
    expect(qc.getQueryData(CLAVE_GLOBAL)).toEqual({ global: true })
  })

  it('el SEGUNDO cambio también protege — A→B, y al pasar a C se cancela lo de B', async () => {
    // ⛔ ESTE CASO EXISTE POR UN MUTANTE QUE ESCAPÓ DOS RONDAS: si `previo` no se actualiza, la
    //    guarda sigue cancelando las claves del inquilino INICIAL para siempre. El primer cambio
    //    sale bien y todos los demás quedan sin proteger — y ningún caso que sólo cambia una vez
    //    lo nota. La consola real cambia de inquilino muchas veces por sesión.
    const qc = createQueryClient()
    desuscribir = isolateCacheOnTenantChange(qc)
    useTenantStore.setState({ activeTenant: 'inquilino-B' }) // primer cambio: A → B

    const CLAVE_B = ['probe', 'inquilino-B', 'items'] as const
    const puerta = aplazada<void>()
    const p = qc
      .fetchQuery({
        queryKey: CLAVE_B,
        queryFn: async () => {
          await puerta.promise
          return { deInquilino: useTenantStore.getState().activeTenant }
        },
      })
      .catch(() => undefined)

    await turnos()
    useTenantStore.setState({ activeTenant: 'inquilino-C' }) // segundo cambio: B → C
    puerta.resolve()
    await p
    await turnos()

    expect(qc.getQueryData(CLAVE_B)).toBeUndefined()
  })

  it('la baja devuelta deja de cancelar (no queda suscripción viva tras desmontar)', async () => {
    const qc = createQueryClient()
    const baja = isolateCacheOnTenantChange(qc)
    baja()
    const puerta = aplazada<void>()

    const p = qc
      .fetchQuery({
        queryKey: CLAVE_A,
        queryFn: async () => {
          await puerta.promise
          return { deInquilino: useTenantStore.getState().activeTenant }
        },
      })
      .catch(() => undefined)

    await turnos()
    useTenantStore.setState({ activeTenant: 'inquilino-B' })
    puerta.resolve()
    await p

    // Vuelve a envenenar: prueba que la cancelación venía de la suscripción y no de otra cosa.
    expect(qc.getQueryData(CLAVE_A)).toEqual({ deInquilino: 'inquilino-B' })
  })
})

/**
 * ⛔ Y LA MITAD QUE MÁS VECES SE ME HA OLVIDADO: que PRODUCCIÓN lo llame.
 *
 * Una guarda correcta con su testigo verde y ningún llamante no protege a nadie. `Providers` es
 * el único sitio donde vive el `QueryClient` de la aplicación, así que es el único sitio donde
 * esta suscripción puede engancharse — la suscripción de módulo que ya había ahí no alcanza al
 * cliente porque nace dentro del componente.
 */
describe('la guarda está ENGANCHADA en producción', () => {
  it('Providers monta la suscripción con el cliente de la aplicación', async () => {
    const { readFileSync, existsSync } = await import('node:fs')
    const { resolve } = await import('node:path')
    // Dos raíces posibles según desde dónde se lance vitest. Si NINGUNA existe esto FALLA:
    // un `skip` aquí sería un pase silencioso, que es justo lo que este caso vigila.
    const candidatas = [
      resolve(process.cwd(), 'src/app/providers.tsx'),
      resolve(process.cwd(), 'web/src/app/providers.tsx'),
    ]
    const ruta = candidatas.find((c) => existsSync(c))
    expect(
      ruta,
      `no encuentro providers.tsx en ${candidatas.join(' ni ')}`,
    ).toBeTruthy()
    const bruto = readFileSync(ruta as string, 'utf8')

    // Fuera comentarios: un identificador citado en prosa no es una llamada.
    const src = bruto
      .replace(/\/\*[\s\S]*?\*\//g, '')
      .replace(/(^|[^:])\/\/[^\n]*/g, '$1')

    // Cotas: demuestran que se leyó el fichero de verdad y que quedó código tras limpiar.
    expect(bruto.length).toBeGreaterThan(2000)
    expect(src).toContain('QueryClientProvider')

    const llamadas = src
      .split('\n')
      .filter((l) => /isolateCacheOnTenantChange\s*\(/.test(l))
    expect(llamadas).toHaveLength(1)
    // Y con EL cliente de la app, no con uno recién fabricado.
    expect(llamadas[0]).toContain('queryClient')
    // ⛔ Y LA LLAMADA ES EL CUERPO DEL EFECTO, NO SU LIMPIEZA. El contraste señaló que
    //    `useEffect(() => () => { isolateCacheOnTenantChange(qc) }, [qc])` satisface un recuento
    //    de líneas y en producción instala la suscripción AL DESMONTAR, o sea nunca.
    expect(src).toMatch(
      /useEffect\(\s*\(\)\s*=>\s*isolateCacheOnTenantChange\(/,
    )
  })
})
