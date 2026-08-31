// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { createHash } from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import { expect, test } from '@playwright/test'

/**
 * — real console captures for the public docs ("what you'll see in the
 * console" sections). Unlike the hermetic visual spec, this runs against the
 * REAL binary serving REAL seeded data (scripts/docs-captures.sh boots
 * `serve --insecure --seed-demo` and passes DEMO_TENANT), performs the real
 * login, and screenshots each view in light and dark at a fixed viewport.
 *
 * Output: web/playwright-report/docs/<view>-<theme>.png. The docs session
 * curates which captures are embedded under docs-site/src/assets/console/ —
 * nothing here is mocked, so every embedded capture shows the product as it
 * actually renders.
 */

const demoTenant = process.env.DEMO_TENANT ?? ''

// Todas las rutas de clave de los locales `en`, con y sin namespace: el universo contra el que se
// decide si un token de la pantalla es una clave SIN TRADUCIR o un dato con puntos. Se calcula una
// vez; son ~19 000 rutas y el coste es una lectura de ficheros al arrancar el fichero de test.
const CLAVES_I18N: string[] = (() => {
  const salida: string[] = []
  const recorre = (o: unknown, pre: string[]) => {
    if (o && typeof o === 'object' && !Array.isArray(o)) {
      for (const [k, v] of Object.entries(o as Record<string, unknown>)) {
        recorre(v, [...pre, k])
      }
    } else {
      salida.push(pre.join('.'))
    }
  }
  const dirs = [
    ...(fs.existsSync('src/features')
      ? fs
          .readdirSync('src/features')
          .map((f) => `src/features/${f}/i18n/en.json`)
      : []),
    ...(fs.existsSync('src/lib/i18n/locales/en')
      ? fs
          .readdirSync('src/lib/i18n/locales/en')
          .map((f) => `src/lib/i18n/locales/en/${f}`)
      : []),
  ]
  for (const f of dirs) {
    if (!fs.existsSync(f) || !f.endsWith('.json')) continue
    try {
      const d = JSON.parse(fs.readFileSync(f, 'utf8'))
      const ns = f.includes('/i18n/')
        ? f.split('/')[2]
        : path.basename(f, '.json')
      recorre(d, [])
      recorre(d, [ns])
    } catch {
      // Un locale ilegible NO se cuenta como "sin claves": se dice y se sigue, porque un
      // universo mas pequeno hace que la sonda vea MENOS de lo que hay.
      console.warn(
        `[i18n] no pude leer ${f} — la sonda de claves crudas mide de menos`,
      )
    }
  }
  return salida
})()
const DEMO_EMAIL = 'demo@olivares.local'
const DEMO_PASSWORD = 'olivares-demo-estate'

// One entry per view the docs reference. `settle` gives slow views (graph
// layout, charts) extra time after networkidle before the shot. `live` views
// poll continuously, so networkidle never fires — they settle on a timer only.
/**
 * ⛔ EL DEFECTO QUE `heading` EXISTE PARA CERRAR, medido el 2026-08-17 y probado con la imagen:
 *    la única aserción de este harness era **«hay algún `<h1>`»**, y eso lo cumple CUALQUIER página
 *    de esta app — incluida la de error. La entrada `/executive` llevaba dos capturas en verde que
 *    eran literalmente un **«Page not found»**, y el docs las trata como producto.
 *
 *    Es la familia de sonda que contesta lo mismo para cualquier entrada. Y el caso peligroso no es
 *    la ruta que no existe —ésa al menos se puede buscar en el registro—: es **una ruta que un día
 *    redirige a otra pantalla**. La captura saldría verde enseñando la pantalla EQUIVOCADA, y el
 *    material público mentiría sin que nada se pusiera rojo.
 *
 *    `heading` es el ORÁCULO POR VISTA: el texto que ese `<h1>` debe decir. Se toma **del producto
 *    corriendo** (lo emite `evidence.json`, no se adivina del fuente) y por eso distingue lo que
 *    importa: que el router sirvió el componente que se pidió.
 *
 * ⚠ SU COSTE, dicho para que nadie lo descubra a golpes: si una vista cambia su título, esta celda
 *   se pone roja y hay que actualizarla. Es el coste correcto para un harness cuya salida se
 *   publica — un título que cambia SÍ debe obligar a mirar las capturas otra vez.
 */
/**
 * ⛔ `prepara` ES EL SEMBRADO POR RUTA DE C10-02, y existe porque una lista de rutas no basta.
 *
 *    Medido: `/workspace` salía con **1 panel vacío y 114 caracteres** en su región principal —
 *    de lejos la captura más pobre de las 55. No era un hueco de DATOS: la vista pinta
 *    «selecciona un espacio de trabajo» mientras `activeWorkspace` esté vacío, y el arnés sembraba
 *    el TENANT y no el espacio. La documentación pública enseñaba el aviso de «elige algo» como si
 *    fuese el panel del producto.
 *
 * ⚠ Y NO se siembra escribiendo en `localStorage`, que era lo obvio: `stores/workspace.ts` dice
 *   que al cambiar de tenant el espacio **se limpia solo** (`providers.tsx` se suscribe al store de
 *   tenant y lo borra). Un seed puesto antes del arranque puede quedar barrido justo después, y el
 *   fallo sería una captura pobre otra vez, sin nada rojo. Se conduce la interfaz, que es lo que
 *   hace un cliente y lo que ninguna suscripción puede deshacer.
 */
/**
 * ⛔ `count()` NO ESPERA, y por eso hace falta esto ANTES de contar nada. Devuelve al instante,
 *    asi que ejecutado justo tras `goto` mide la pantalla MIENTRAS las consultas vuelan y
 *    acusaria de «hueco de sembrado» a un estate perfectamente sembrado — una causa FALSA, que
 *    es justo lo que la precondicion existia para evitar. Lo vio the reviewer al releer.
 *
 *    `DataTable` marca la espera con `aria-busy` (`components/data/data-table.tsx:600`) y
 *    mientras tanto pinta `SkeletonRows`, que son `<tr>` DE VERDAD: contar filas durante la
 *    carga cuenta el esqueleto, no los datos. `toHaveCount` si reintenta hasta su timeout.
 */
async function esperaTablasCargadas(page: import('@playwright/test').Page) {
  // ⛔⛔ Y SE ESPERA POR CSS (`locator('table')`), NO POR ROL. `getByRole('table')` NO CASA
  //     NINGUNA de estas tablas: `DataTable` pone `role={nav ? 'grid' : undefined}` y
  //     `gridNavigation` vale **true por defecto** (`components/data/data-table.tsx:166,288`),
  //     asi que su rol accesible es `grid` y el explicito pisa al implicito. Medido corriendo
  //     las tres escenas contra el motor real: fallaban a los 22,5 s —MI espera de 20 s, no el
  //     timeout de 30 del click— y habrian fallado en cualquier pantalla con DataTable.
  //     El elemento SI es un `<table>`; lo que varia es su rol.
  //
  // ⛔ PRIMERO QUE LA TABLA EXISTA, y no es un paso de mas: `aria-busy` es
  //    `isLoading || undefined`, asi que cuando NO carga el atributo **no esta**. Esperar solo
  //    a que `[aria-busy="true"]` valga 0 no distingue «ya termino» de «aun no ha empezado»
  //    —justo tras el `goto` puede no haberse puesto todavia— y saldriamos igual de pronto que
  //    sin esperar nada. Con la tabla presente, el 0 significa lo que queremos.
  // ⛔⛔ Y LA TABLA PUEDE NO EXISTIR. Esperarla 20 s como condicion DURA mato la vista paginada
  //     de `/work`, cuyo panel de decisiones no pinta ninguna `<table>`: 23,5 s y mi precondicion
  //     ni llego a ejecutarse. Es la TERCERA vez que supongo la forma de una pantalla en vez de
  //     derivarla —antes fue el rol `grid`, antes el `people` renombrado—, y la leccion es la
  //     misma: una espera generica no puede exigir una estructura concreta.
  //
  //     Se le da una espera CORTA y se tolera su ausencia: si hay tabla, sirve de guarda de
  //     arranque —el `aria-busy` es `isLoading || undefined` y sin ella no distingue «ya termino»
  //     de «aun no ha empezado»—; si no la hay, no es un fallo, es otra clase de pantalla.
  //
  //     ⚠ Limite declarado: en una pantalla SIN tabla, el `aria-busy` puede consultarse antes de
  //     que arranque ninguna consulta. Ahi esta espera vale menos, y quien dependa de un estado
  //     cargado en una superficie asi tiene que esperar a SU elemento, no a esto.
  await page
    .locator('table')
    .first()
    .waitFor({ timeout: 5_000 })
    .catch(() => {})
  await expect(page.locator('[aria-busy="true"]')).toHaveCount(0, {
    timeout: 20_000,
  })
}

const VIEWS: {
  id: string
  path: string
  settle?: number
  live?: boolean
  heading?: RegExp
  prepara?: (page: import('@playwright/test').Page) => Promise<void>
  // ⛔ FALTABA, Y LA USABAN DOS ENTRADAS. `despues` se invoca en `:908` y NO estaba declarada aqui;
  //    en TypeScript eso seria un error de compilacion si algun `tsconfig` mirase este fichero, y
  //    ninguno lo mira (`tsconfig.app.json` incluye solo `src`, `tsconfig.node.json` solo
  //    `vite.config.ts`, y nada menciona `e2e`). El dano no es el tipo desactualizado: es que un
  //    typo —`despuess`— no falla, sale falsa la condicion, no se pincha nada y la captura se
  //    guarda con la pestaña POR DEFECTO mientras su `id` promete el estado interno. Lo caza
  //    `scripts/check-capture-view-keys.py`, que es lo que `tsc` haria si mirase esto.
  despues?: (page: import('@playwright/test').Page) => Promise<void>
  // Ver la excepcion documentada en `tomar`: sólo para tomas cuyo sujeto ES un diálogo.
  modal?: boolean
  // Recorte DECLARADO (ver `tomar`): el disparo sigue siendo 1440x1000 @2x y sólo se guarda
  // el rectángulo que esta función devuelve, que además queda escrito en la evidencia.
  recorte?: (
    page: import('@playwright/test').Page,
  ) => Promise<{ x: number; y: number; width: number; height: number }>
}[] = [
  // ═══ LAS SEIS TOMAS QUE ENSEÑAN EL ESTADO INTERNO (R8-V3, criterio de VITRINA) ═══════════
  //
  // ⛔ EL CRITERIO ES «SI NO SE VE, NO CUENTA». Una toma de la RUTA no vale: los seis contratos de
  //    consola viven en un diálogo, un menú o una pestaña que no es la de por omisión, así que
  //    re-fotografiar su vista daría la misma pantalla con otro sha256. Cada una de éstas fuerza el
  //    estado y lo deja a la vista.
  //
  // ⛔ Y SE FUERZA INTERCEPTANDO, NO SEMBRANDO, donde se puede: una intercepción es determinista y
  //    no depende de que el estate traiga el dato justo. `prepara` corre ANTES de montar la vista
  //    (`:687`), que es exactamente donde una ruta interceptada tiene efecto.
  {
    id: 'work-decisions',
    path: '/work',
    heading: /^Work$/,
    settle: 800,
    // ⛔ `live` PORQUE EL PANEL SONDEA. El arnés espera `networkidle` salvo en las vistas
    //    marcadas así (`:578`), y el panel de decisiones mantiene consultas abiertas: la
    //    toma expiraba en la espera **con el estado YA ALCANZADO** — la instantánea del
    //    fallo enseña `tab "Decisions" [selected]` y su `tabpanel` renderizado.
    live: true,
    // La pestaña NO está en la URL (`work-view.tsx:146`, `defaultValue="items"`), así que se
    // pincha DESPUÉS de montar. Sólo se pinta con `sessions:decision:read` (`work-view.tsx:149`).
    despues: async (page) => {
      await page.getByRole('tab', { name: /^decisions$/i }).click()
    },
  },
  {
    id: 'work-decisions-paginated',
    path: '/work',
    heading: /^Work$/,
    settle: 800,
    // ⛔ `live` PORQUE EL PANEL SONDEA. El arnés espera `networkidle` salvo en las vistas
    //    marcadas así (`:578`), y el panel de decisiones mantiene consultas abiertas: la
    //    toma expiraba en la espera **con el estado YA ALCANZADO** — la instantánea del
    //    fallo enseña `tab "Decisions" [selected]` y su `tabpanel` renderizado.
    live: true,
    // C-13: al pasar de página las filas YA LEÍDAS siguen ahí. Una foto de la primera página no
    // lo demuestra — hay que pasar y ver que la lista CRECIÓ.
    despues: async (page) => {
      await page.getByRole('tab', { name: /^decisions$/i }).click()
      // ⛔ MISMA MEDICINA QUE LAS ESCENAS DE VIDEO: si no hay segunda pagina, «Load more» no
      //    existe y el click agota los 30 s del test sin nombrar ninguna causa. Medido en el
      //    ensayo del 2026-08-30 sobre `main`+#2134: esta fue la UNICA de las seis vistas nuevas
      //    que cayo, y a 30,3 s — las otras cinco capturaron en ~3 s. La razon esta escrita en
      //    la fila de SEMBRADO: sin B14 el estate no tiene decisiones suficientes para paginar.
      // ⛔ Y ESPERAR ANTES DE CONTAR: `count()` no reintenta, asi que aqui —justo tras pulsar
      //    la pestaña— podria devolver 0 con el panel a medio cargar y acusar de SEMBRADO a un
      //    estate sano. Es el defecto que ya me encontraron una vez esta noche.
      await esperaTablasCargadas(page)
      // ⛔⛔ SE ESPERA A SU PROPIO ELEMENTO, no a una demora ajena. `count()` MUESTREA UNA VEZ, y
      //     en `/work` no hay tabla, asi que el ayudante de arriba solo demora ~5 s: una carga
      //     sana pero lenta habria dado 0 y esta precondicion habria acusado de SEMBRADO a un
      //     estate correcto. Es EXACTAMENTE la clase que cure hace una hora en la fila de
      //     `Launched`, reaparecida en el boton — la arregle en un sitio y no barri su clase.
      //
      //     `waitFor` SI reintenta hasta su plazo, asi que el veredicto deja de depender de
      //     cuanto tarde otra cosa. Al vencer, se lanza el MISMO error nombrado: el negativo
      //     conserva su causa.
      const mas = page.getByRole('button', { name: /^load more$/i }).first()
      try {
        await mas.waitFor({ timeout: 8_000 })
      } catch {
        throw new Error(
          'docs-captures: no hay boton «Load more» en las decisiones de /work tras esperarlo ' +
            '8 s: el estate no tiene una segunda pagina. Es SEMBRADO (B14), no un selector roto.',
        )
      }
      await mas.click()
    },
  },
  {
    id: 'work-apply-refused',
    path: '/work',
    heading: /^Work$/,
    settle: 800,
    // `/work` sondea — la entrada preexistente `work` ya va con `live`. Sin esto la toma
    // expira en `networkidle` con el diálogo del rechazo ya en pantalla.
    live: true,
    // C-14: el motivo del motor, no una categoría. El sobre es el que `work_api.go writeWorkError`
    // emite —`{verdict, code, error:{message}}`— y el cliente lee el `message` ANIDADO
    // (`work/api.ts:127`). Interceptar es lo único determinista: un rechazo real depende del estado.
    prepara: async (page) => {
      await page.route('**/v1/m/sessions/work-items/**', async (route) => {
        if (route.request().method() !== 'POST') return route.fallback()
        await route.fulfill({
          status: 409,
          contentType: 'application/json',
          body: JSON.stringify({
            verdict: 'RECHAZADO',
            code: 'lease_held_elsewhere',
            error: {
              message:
                'the lease is held by session sesion-7 until 2026-08-30T09:00:00Z; take it over or wait',
            },
          }),
        })
      })
    },
  },
  {
    id: 'templates-readonly',
    path: '/workspace-templates',
    heading: /^Workspace Templates$/,
    settle: 800,
    // C-15: los permisos son POR ACCIÓN. `can()` es pertenencia al conjunto que devuelve
    // `/v1/auth/whoami` (`lib/auth/rbac.ts:13`), así que se le quita UNA concesión y se ve qué
    // acciones quedan deshabilitadas — que es lo que hay que documentar, no su ausencia.
    prepara: async (page) => {
      await page.route('**/v1/auth/whoami', async (route) => {
        const res = await route.fetch()
        const cuerpo = await res.json()
        const fuera = new Set([
          'sessions:template:write',
          'sessions:template:admin',
        ])
        if (Array.isArray(cuerpo?.permissions)) {
          cuerpo.permissions = cuerpo.permissions.filter(
            (p: string) => !fuera.has(p),
          )
        }
        await route.fulfill({ response: res, json: cuerpo })
      })
    },
  },
  {
    id: 'workflow-runs-error',
    path: '/automations',
    heading: /^Automations$/,
    settle: 800,
    // C-10: un fallo del historial NO es «no hay ejecuciones». Con la consulta en error la
    // pantalla debe decir «no se pudo cargar» y ofrecer reintentar.
    prepara: async (page) => {
      await page.route('**/v1/m/orchestration/**/runs**', async (route) => {
        await route.fulfill({
          status: 500,
          contentType: 'application/json',
          body: JSON.stringify({ error: { message: 'upstream unavailable' } }),
        })
      })
    },
  },
  {
    id: 'list-truncated',
    path: '/console',
    // El h1 real es «Control console», no «Console». Lo cazó el oráculo del propio arnés,
    // que existe para negarse a fotografiar una pantalla que no es la pedida.
    heading: /^Control console$/,
    settle: 800,
    // El aviso de recorte, que hoy no aparece en NINGUNA captura publicada. Se fuerza `has_more`
    // en una lista de consola: es la afirmación que el aviso hace, así que interceptarla la hace
    // cierta para esa respuesta en vez de fingirla.
    prepara: async (page) => {
      await page.route('**/v1/members**', async (route) => {
        const res = await route.fetch()
        const cuerpo = await res.json()
        cuerpo.has_more = true
        await route.fulfill({ response: res, json: cuerpo })
      })
    },
  },
  {
    id: 'access-map',
    path: '/access-map',
    settle: 1500,
    heading: /^Access map$/,
  },
  { id: 'inventory', path: '/inventory', heading: /^Inventory$/ },
  // K5 shipped this console screen with the protocol-binding kernel and nothing
  // photographed it, which is what lint:screenshot-coverage caught on the way in.
  {
    id: 'communications-protocol-bindings',
    path: '/communications/protocol-bindings',
    heading: /^Protocol bindings$/,
    // ⛔ ESTA VISTA EXIGE UN WORKSPACE CONCRETO, y sin el no enseña su contenido sino
    //    «Select a workspace — Protocol bindings require an explicit workspace scope». El arnes
    //    captura con «All workspaces» puesto, asi que la toma publicada era un cartel, no la
    //    pantalla. Lo vi ABRIENDO EL PNG de la corrida del 2026-08-30: ninguna sonda del
    //    manifiesto lo distingue de una pantalla vacia — `filas 0`, `tablas_vacias 0`, 253
    //    caracteres— y mi curador la clasifico como hueco de sembrado, que es lo que NO es.
    //
    //    Va en `despues` y no en `prepara` a proposito: `prepara` corre ANTES del `goto` (:820),
    //    asi que cambiar el ambito ahi no tendria efecto sobre una vista que aun no ha montado.
    despues: async (page) => {
      // ⛔ Y LA AUSENCIA DEL CONMUTADOR TAMBIEN SE NOMBRA. El producto NO lo monta con cero o
      //    un workspace (`components/layout/workspace-switcher.tsx:44`,
      //    `if (workspaces.length <= 1) return null`), asi que un click a secas moriria en un
      //    timeout generico —«esperando un boton»— y no en la causa. Que no haya conmutador es
      //    un dato de SEMBRADO, no un selector roto, y hay que poder leerlo del fallo.
      const conmutador = page.getByRole('button', { name: 'All workspaces' })
      // ⛔ EL `click` VA DENTRO DEL `try`, no sólo la espera. Un trigger que se desprende entre
      //    el `waitFor` y el `click` —re-render, lista que llega tarde— fallaria GENERICO justo
      //    despues de la guarda que existe para evitarlo. La guarda tiene que cubrir el ACTO, no
      //    su antesala: tercera vez hoy que envuelvo la mitad.
      try {
        await conmutador.waitFor({ timeout: 8_000 })
        await conmutador.click()
      } catch {
        throw new Error(
          'docs-captures: no se pudo abrir el conmutador de workspaces: el producto solo lo monta ' +
            'con MAS DE UNO (workspace-switcher.tsx:44). Con cero o uno no existe, y esta vista ' +
            'no puede elegir ambito. Es SEMBRADO, no un selector roto.',
        )
      }
      // Se elige POR LO QUE NO ES: cualquier entrada que no sea la de «todos». Elegir por
      // posicion —`.nth(1)`— es el error que ya me costo las escenas de video.
      const alguno = page
        .getByRole('menuitem')
        .filter({ hasNotText: 'All workspaces' })
        .first()
      try {
        await alguno.waitFor({ timeout: 8_000 })
      } catch {
        throw new Error(
          'docs-captures: el conmutador no ofrece ningun workspace concreto, asi que ' +
            '/communications/protocol-bindings solo puede enseñar «Select a workspace». Es ' +
            'SEMBRADO, no un selector roto.',
        )
      }
      await alguno.click()
    },
  },
  // Las dos patas que el REGISTRO DE FEATURES no conoce y el censo sí. Estaban montadas y sin
  // captura desde siempre: la guarda de cobertura leía el registro (53 rutas) y el árbol monta 58.
  { id: 'settings', path: '/settings', heading: /^Settings$/ },
  {
    id: 'status-page',
    path: '/status-page',
    heading: /^Olivares Control Plane — Status$/,
  },
  {
    id: 'sessions',
    path: '/sessions',
    live: true,
    settle: 2500,
    heading: /^Sessions$/,
    // ⛔ FILTRADA A «Launched», Y NO ES COSMETICA. Sin filtro la tabla la ocupan las sesiones
    //    de relleno del sembrador de volumen —`demo-mobile`/`demo-data` con `— · — · 0/0 ·
    //    $0.00`—, o sea una tabla de ceros en la vista que la guia de Claude Code usa para
    //    enseñar modelo, tokens y coste. Las que SI tienen esos datos son las que el plano
    //    LANZO (`sess-coder-*`), y «launched» es exactamente eso: «al menos un run enlaza con
    //    esta sesion» (`provenance.ts:26`). El filtro no maquilla nada — elige el subconjunto
    //    del que la guia habla. (Veredicto de the planner sobre las 142, 2026-08-31.)
    despues: async (page) => {
      await page.getByRole('combobox', { name: 'All sources' }).click()
      await page.getByRole('option', { name: 'Launched', exact: true }).click()
      // ⛔ EL ANILLO DE FOCO SALE EN LA FOTO. Radix devuelve el foco al disparador al cerrar
      //    el menu, asi que el desplegable queda con su anillo naranja — un estado que el
      //    lector no puede reproducir y que r4 marco en la revision A-F. Retirar el raton no
      //    basta: el foco es del TECLADO. Se suelta explicitamente.
      await page.evaluate(() =>
        (document.activeElement as HTMLElement | null)?.blur(),
      )
    },
  },
  { id: 'capabilities', path: '/capabilities', heading: /^MCP & skills$/ },
  {
    id: 'identity',
    path: '/identity',
    settle: 1500,
    heading: /^Identity & NHI$/,
  },
  { id: 'finops', path: '/finops', settle: 1000, heading: /^Cost & FinOps$/ },
  { id: 'security', path: '/security', heading: /^Security & forensics$/ },
  {
    id: 'dashboards',
    path: '/dashboards',
    settle: 1000,
    heading: /^Executive overview$/,
  },
  { id: 'killswitch', path: '/killswitch', heading: /^Kill switch$/ },
  {
    id: 'claude-policy',
    path: '/claude-policy',
    heading: /^Claude Code governance$/,
  },
  { id: 'models', path: '/models', heading: /^Models & providers$/ },
  {
    id: 'observability',
    path: '/observability',
    heading: /^Observability & interop$/,
    settle: 800,
    // ⛔ ENCUADRADA SOBRE LOS CONTADORES VIVOS, que es donde estan los datos. Arriba de la
    //    pagina hay dos cajas de aviso y la tabla «Per-standard ingestion health» con `—` en
    //    RECORDS y LAST SEEN en sus siete filas — y ese `—` es HONESTO, lo explica el propio
    //    UI («un valor ausente significa 'no atribuible con solidez', no cero»). Pero una
    //    captura que empieza ahi enseña avisos y guiones, y deja fuera de cuadro lo unico que
    //    prueba que el plano ingiere: `olivares.claude · 49 records · Edge 48 · Finding 1 ·
    //    otel 48`. Se baja hasta esa seccion antes de disparar. (Veredicto de the planner sobre
    //    las 142: «NO (encuadre)».)
    despues: async (page) => {
      // ⛔ `scrollIntoViewIfNeeded()` NO SIRVE AQUI, y lo comprobe gastando una corrida: la
      //    seccion ya asomaba por el borde inferior, asi que Playwright la considero visible
      //    y NO HIZO NADA — la captura salio identica salvo el reloj. El «IfNeeded» es
      //    exactamente el problema. Se pide el scroll explicito, alineando la seccion arriba.
      // ⛔ POR ROL Y NO POR TEXTO, y con el encabezado al BORDE SUPERIOR. Apuntando al nodo
      //    de texto la vista quedaba empezando en la COLA de la tabla de estandares, sin su
      //    titulo: parecia un recorte (veredicto A-F de r4). El encabezado es el ancla real
      //    de la seccion, y `scrollIntoView(true)` lo alinea arriba, con la tabla y su nota
      //    debajo dentro del alto de 2000 px.
      const anclaObs = page.getByRole('heading', {
        name: 'Live counters by bus source',
      })
      await anclaObs.evaluate((el) => el.scrollIntoView(true))
      // ⛔ Y ESTE ES EL TOPE REAL, MEDIDO — no se puede subir mas, asi que nadie lo intente:
      //      headingTop 406 · container `flex-1 overflow-y-auto` cTop 48 · scrollTop 425 ·
      //      maxScroll 425
      //    El contenedor esta EN SU MAXIMO (425 de 425). Llevar el encabezado al borde del
      //    contenedor (y=48) pediria 358 px MAS de desplazamiento, y no existen: esta seccion
      //    es el ultimo contenido de la pagina. A 1440x1000 el encabezado no puede subir de
      //    406 px, y lo que SI entra entero debajo es la tabla de nueve fuentes y su nota.
      //    (Antes medi `document.scrollingElement` y daba maxScroll 0 — engañoso: la pagina
      //    no desplaza la ventana, desplaza un contenedor interno. Medir el elemento
      //    equivocado da un cero que parece una respuesta.)
    },
  },
  // ⛔ LA SECCION DE CONTADORES, RECORTADA — decision de the planner tras medir que no cabe.
  //    `observability` entero no puede empezar en este titulo: el contenedor esta en su
  //    maximo (scrollTop 425 = maxScroll 425) y llevar el encabezado al borde pediria 358 px
  //    que no existen, porque la seccion es el ultimo contenido de la pagina. En vez de
  //    fingir un encuadre imposible, la imagen de la guia ES la seccion: se dispara igual
  //    que las demas (1440x1000 @2x, mismo instante para los dos temas) y se guarda el
  //    rectangulo, declarado en la evidencia.
  //    Los 16 px de aire arriba y abajo son de r4: sin ellos el recorte corta a ras y parece
  //    un error de captura en vez de un encuadre.
  {
    id: 'observability-counters',
    path: '/observability',
    heading: /^Observability & interop$/,
    settle: 800,
    despues: async (page) => {
      await page
        .getByRole('heading', { name: 'Live counters by bus source' })
        .evaluate((el) => el.scrollIntoView(true))
    },
    recorte: async (page) => {
      const titulo = await page
        .getByRole('heading', { name: 'Live counters by bus source' })
        .boundingBox()
      const nota = await page
        .getByText(/Counters are process-global/)
        .boundingBox()
      if (!titulo || !nota) {
        throw new Error(
          'observability-counters: no encuentro el titulo o la nota final — el recorte ' +
            'no se puede calcular y NO se guarda una imagen a ciegas',
        )
      }
      const y = Math.max(0, Math.round(titulo.y - 16))
      const abajo = Math.round(nota.y + nota.height + 16)
      return {
        x: 0,
        y,
        width: page.viewportSize()?.width ?? 1440,
        height: Math.max(1, abajo - y),
      }
    },
  },
  // Views added as the product grew (health catalog knowledge audit workspace backups); captured for docs/README curation.
  {
    id: 'knowledge',
    path: '/knowledge',
    settle: 1500,
    heading: /^Data, knowledge & context$/,
  },
  { id: 'catalog', path: '/catalog', settle: 1000, heading: /^Catalog$/ },
  {
    id: 'health',
    path: '/health',
    live: true,
    settle: 2500,
    heading: /^Health & SLA$/,
  },
  { id: 'audit', path: '/audit', settle: 1000, heading: /^Audit ledger$/ },
  {
    id: 'compliance',
    path: '/compliance',
    settle: 1000,
    heading: /^Compliance$/,
  },
  {
    id: 'workspace',
    path: '/workspace',
    settle: 1500,
    // ⛔ EL TESTIGO CAMBIA CON EL SEMBRADO, y descubrirlo es el mejor argumento para tenerlo.
    //    Era `/^Workspace overview$/`, que es el título del aviso «elige un espacio» — es decir,
    //    **el oráculo certificaba la pantalla vacía**: dos cosas de acuerdo sobre la captura
    //    equivocada. Con un espacio seleccionado el `<h1>` es su NOMBRE, y esta celda se puso roja
    //    diciendo exactamente eso («el <h1> dice “Billing”») en cuanto el sembrado empezó a
    //    funcionar. Un testigo que no cambia cuando cambia la pantalla no estaba midiendo nada.
    heading: /^Billing$/,
    // El selector del topbar sólo se renderiza con MÁS DE UN espacio: `workspace-switcher.tsx`
    // hace `if (workspaces.length <= 1) return null`. El estate demo siembra el predeterminado
    // MÁS uno llamado «Billing» (cmd/olivares/seed/seed.go:52), así que hay dos y aparece. Si un
    // día el sembrado se quedara en uno, este paso fallaría EN VOZ ALTA en vez de volver a
    // fotografiar el aviso de «elige un espacio».
    prepara: async (page) => {
      await page.getByRole('button', { name: /All workspaces/i }).click()
      await page.getByRole('menuitem', { name: /Billing/i }).click()
    },
  },
  { id: 'evals', path: '/evals', settle: 1000, heading: /^Evals$/ },
  {
    id: 'backups',
    path: '/backups',
    settle: 1000,
    heading: /^Backup & Restore$/,
  },
  // ⛔ LAS 31 QUE FALTABAN (2026-08-17). El backlog pedía «re-capturar las 52 rutas» y el harness
  //    llevaba 20: la cifra 52 no era una estimación, es `web/src/features/registry.tsx`, de donde
  //    `app/routes.tsx:89` GENERA una ruta por vista. Se añaden SIN `heading` a propósito — el
  //    oráculo se toma del producto corriendo, no se adivina—, pero el suelo del «page not found»
  //    ya las cubre desde la primera corrida.
  //
  //    `live` va en las de flujo vivo que el integrador midió (`/work`, `/agentops`, `/logs`, más
  //    `/sessions` y `/health` que ya lo tenían): en ellas `networkidle` NO se estabiliza nunca y
  //    la celda esperaría hasta agotar el tiempo.
  //
  //    ⚠ FUERA, CON MOTIVO: `/session-viewer/$id` lleva parámetro y sin una sesión sembrada
  //      concreta la captura sería de un estado de error. Capturarla exige elegir un id del estate
  //      sembrado; queda declarada como no cubierta en vez de fingida.
  { id: 'home', path: '/', settle: 1000, heading: /^Overview$/ },
  {
    id: 'onboarding',
    path: '/onboarding',
    settle: 1000,
    heading: /^Get started$/,
  },
  {
    id: 'console',
    path: '/console',
    settle: 1000,
    heading: /^Control console$/,
  },
  {
    id: 'permissions',
    path: '/permissions',
    settle: 1000,
    heading: /^Permissions$/,
  },
  {
    id: 'routine-policies',
    path: '/routine-policies',
    settle: 1000,
    heading: /^Routine policies$/,
  },
  {
    id: 'agentcore-export',
    path: '/agentcore-export',
    settle: 1000,
    heading: /^AgentCore export$/,
  },
  {
    id: 'deploy',
    path: '/deploy',
    settle: 1000,
    heading: /^Deployment & integration$/,
  },
  { id: 'work', path: '/work', settle: 1000, live: true, heading: /^Work$/ },
  {
    id: 'agentops',
    path: '/agentops',
    settle: 1000,
    live: true,
    heading: /^Claude Code$/,
  },
  {
    id: 'agent-artifacts',
    path: '/agent-artifacts',
    settle: 1000,
    heading: /^Agent Artifacts$/,
  },
  {
    id: 'workspace-templates',
    path: '/workspace-templates',
    settle: 1000,
    heading: /^Workspace Templates$/,
  },
  {
    id: 'eventing',
    path: '/eventing',
    settle: 1000,
    heading: /^Webhooks & event subscriptions$/,
  },
  {
    id: 'automations',
    path: '/automations',
    settle: 1000,
    heading: /^Automations$/,
  },
  {
    id: 'inference-proxy',
    path: '/inference-proxy',
    settle: 1000,
    heading: /^Inference proxy$/,
  },
  { id: 'alerting', path: '/alerting', settle: 1000, heading: /^Alerting$/ },
  {
    id: 'model-operations',
    path: '/model-operations',
    settle: 1000,
    heading: /^Model Operations$/,
  },
  {
    id: 'adoption',
    path: '/adoption',
    settle: 1000,
    heading: /^Claude Code Adoption$/,
    // ⛔ LA TENDENCIA NACE EN LA LENTE VACIA, Y NO ES UN HUECO DE SEMBRADO. Las dos lentes se
    //    pintan lado a lado (`adoption-view.tsx:103-116`), asi que las tarjetas SI traen datos;
    //    lo que arranca en `analytics` es la GRAFICA (`:232`, `useState<LensId>('analytics')`),
    //    y esa lente es la Admin Analytics API de Anthropic, que en una finca de demo se queda
    //    en cero POR DISENO. Sin este gancho la captura ensena una grafica plana y quien la vea
    //    concluira que falta sembrado: medido, con `?lens=telemetry` hay 28 dias de tendencia.
    despues: async (page) => {
      // ⛔ LA LENTE VIVE DENTRO DE UNA PESTANA, y esto lo corrige una corrida real: la primera
      //    version buscaba el selector nada mas montar y moria en su propia guarda. `/adoption`
      //    tiene CUATRO pestanas (`adoption-view.tsx:92-95`) y el selector es del panel `Trend`
      //    (`TrendTab`), asi que antes hay que abrirla. Lo vi ABRIENDO EL PNG del fallo.
      const pestana = page.getByRole('tab', { name: /^Trend$/ })
      try {
        await pestana.waitFor({ timeout: 8_000 })
        await pestana.click()
      } catch {
        throw new Error(
          'docs-captures: no se encontro la pestana `Trend` en /adoption ' +
            '(adoption-view.tsx:95). El selector de lente vive dentro de ella.',
        )
      }
      const selector = page.getByRole('combobox', { name: 'Lens' })
      try {
        await selector.waitFor({ timeout: 8_000 })
        await selector.click()
      } catch {
        throw new Error(
          'docs-captures: no se pudo abrir el selector de lente de /adoption (aria-label `Lens`, ' +
            'adoption-view.tsx:236). Si el control cambio de nombre, la captura seguiria saliendo ' +
            'con la lente `analytics`, que esta vacia POR DISENO y se lee como falta de sembrado.',
        )
      }
      // Se elige por el TEXTO de la lente viva, no por posicion: `.nth(1)` es el error que ya
      // costo las escenas de video en este mismo fichero.
      await page.getByRole('option', { name: /live telemetry/i }).click()
    },
  },
  {
    // ⛔ ESTA PESTANA NO ESTABA FOTOGRAFIADA, y son 142 capturas. La escena `adoption` de arriba
    //    navega a `Trend` para arreglar la grafica y AHI SE QUEDA, asi que `Overview` —la que
    //    contesta la pregunta del producto, «cuanto de lo que Claude propone se queda»— no aparece
    //    en NINGUNA toma del acto. No lo dijo un contador: lo vi ABRIENDO las dos imagenes, la
    //    publicada en `4cb5218d8` y la de la re-toma, y las dos ensenan `Trend`.
    //
    //    Y solo tiene sentido fotografiarla DESDE HOY. Hasta la cura del sembrado OTLP el plano de
    //    adopcion recibia CINCO de las SIETE metricas y ninguna con dimensiones, asi que la tasa de
    //    aceptacion salia sobre «0 de 0» y la baldosa de tiempo activo NI SE PINTABA
    //    (`components.tsx:114`, `active_time_ms > 0 ? … : null`). La foto habria sido de un hueco.
    //    Medido contra la API con el sembrado curado: 907 aceptadas de 1.026, 0,884 de tasa,
    //    63.085.000 ms de tiempo activo.
    //
    //    NO hay pestana que abrir: `Overview` es la de por defecto (`adoption-view.tsx:90`,
    //    `<Tabs defaultValue="overview">`) y pinta las DOS lentes lado a lado (`:97-116`), asi que
    //    aqui no hay selector que tocar — el de lente vive dentro de `Trend`, que es justo lo que
    //    la otra escena existe para resolver. El `despues` de abajo es de ENCUADRE, no de estado.
    //
    //    ⚠ En esta foto la tarjeta `analytics` sale A CERO, y es POR DISENO, no un hueco: esa lente
    //      es la Admin Analytics API de Anthropic y una finca de demo no la alimenta. Queda dicho
    //      aqui porque quien vea la captura va a preguntarselo, y la respuesta tiene que estar
    //      donde va a mirar.
    id: 'adoption-overview',
    path: '/adoption',
    settle: 1000,
    heading: /^Claude Code Adoption$/,
    // ⛔ SIN ESTE DESPLAZAMIENTO LA FOTO MIENTE POR ENCUADRE, y lo vi ABRIENDO EL PNG de la primera
    //    version: la toma es del VIEWPORT, no de la pagina entera, asi que salia la tarjeta
    //    `admin Analytics` —CERO POR DISENO— con su «TOOL ACCEPTANCE —, 0 of 0 accepted» bien
    //    grande, y la de telemetria CORTADA justo antes de su tasa. Es decir: la captura que existe
    //    para ensenar que el producto mide la aceptacion ensenaba un guion en el sitio de la cifra.
    //    Encuadre equivocado y dato correcto se ven igual de bien en verde.
    despues: async (page) => {
      const viva = page.getByText(/Per session \(live telemetry\)/i).first()
      try {
        await viva.waitFor({ timeout: 8_000 })
        // ⛔ `scrollIntoViewIfNeeded()` NO SIRVE AQUI, y lo aprendi comparando los dos PNG: no hace
        //    NADA si el elemento ya asoma, y la cabecera de esta tarjeta asomaba por el borde de
        //    abajo. La foto salio IDENTICA a la de antes del arreglo — un remedio que no se ejecuta
        //    y un remedio que no hace falta se ven exactamente igual en verde. `scrollIntoView`
        //    con `block: 'start'` desplaza SIEMPRE y deja la tarjeta arriba, con sus baldosas.
        await viva.evaluate((el) => el.scrollIntoView({ block: 'start' }))
      } catch {
        throw new Error(
          'docs-captures: no encuentro la tarjeta `Per session (live telemetry)` en /adoption ' +
            '(adoption-view.tsx:109). Sin ella la foto encuadra la tarjeta `analytics`, que esta ' +
            'a cero POR DISENO, y se lee como que el producto no mide la aceptacion.',
        )
      }
    },
  },
  {
    id: 'recordings',
    path: '/recordings',
    settle: 1000,
    heading: /^Recordings$/,
  },
  {
    id: 'posture-export',
    path: '/posture-export',
    settle: 1000,
    heading: /^Posture export$/,
    // La vista nace con los filtros y NADA mas: el resultado del export es lo que esta pantalla
    // existe para ensenar. El boton lo dispara (`posture-export-view.tsx:231`, que remata en
    // `toast.success(t('export.done'))`).
    despues: async (page) => {
      const boton = page.getByRole('button', { name: 'Export posture' })
      try {
        await boton.waitFor({ timeout: 8_000 })
        await boton.click()
      } catch {
        throw new Error(
          'docs-captures: no se encontro el boton `Export posture` en /posture-export ' +
            '(i18n `export.action`). Sin el, la captura sale con los filtros vacios y no ensena ' +
            'lo unico que esta vista tiene que ensenar.',
        )
      }
      // El toast confirma que el export TERMINO. Esperar al toast y no a un plazo fijo es la
      // diferencia entre capturar el resultado y capturar el estado intermedio.
      await page
        .getByText(/Posture export(ed|  failed)/i)
        .first()
        .waitFor({ timeout: 15_000 })
    },
  },
  {
    id: 'orchestration',
    path: '/orchestration',
    settle: 1000,
    heading: /^Orchestration & A2A$/,
  },
  { id: 'voice', path: '/voice', settle: 1000, heading: /^Voice & realtime$/ },
  {
    id: 'sandbox',
    path: '/sandbox',
    settle: 1000,
    heading: /^Testing sandbox$/,
  },
  { id: 'red-team', path: '/red-team', settle: 1000, heading: /^Red-team$/ },
  {
    id: 'team-costs',
    path: '/team-costs',
    settle: 1000,
    heading: /^Team Costs$/,
  },
  { id: 'reporting', path: '/reporting', settle: 1000, heading: /^Reports$/ },
  {
    id: 'platforms',
    path: '/platforms',
    settle: 1000,
    heading: /^Platforms & lifecycle$/,
  },
  {
    id: 'rate-limits',
    path: '/rate-limits',
    settle: 1000,
    heading: /^Rate Limits$/,
  },
  {
    id: 'attestation',
    path: '/attestation',
    settle: 1000,
    heading: /^Supply-chain attestation$/,
  },
  {
    id: 'api-playground',
    path: '/api-playground',
    settle: 1000,
    heading: /^API Playground$/,
    // ⛔ LA PESTANA `History` SOLA NO ARREGLA NADA, y por eso el gancho hace DOS cosas. El panel
    //    derecho nace en `Response` (`api-playground-view.tsx:40`) y el historial se alimenta de
    //    peticiones hechas EN LA SESION (`:50`, `history`), asi que saltar a la pestana sin haber
    //    enviado nada cambia una pantalla vacia por otra. Primero se envia, y entonces se mira.
    despues: async (page) => {
      // ⛔ EL BOTON `Send` NO EXISTE HASTA QUE HAY UN ENDPOINT ELEGIDO, y tambien lo corrige una
      //    corrida real: el panel central nace VACIO —lo vi en el PNG del fallo— porque
      //    `request-panel.tsx` solo monta con una seleccion. Se filtra primero para no pinchar a
      //    ciegas entre 765 endpoints, y se elige uno que devuelve datos SEMBRADOS, no `/healthz`:
      //    una captura de documentacion tiene que ensenar una respuesta de verdad.
      const filtro = page.getByPlaceholder('Filter endpoints…')
      try {
        await filtro.waitFor({ timeout: 8_000 })
        await filtro.fill('/v1/agents')
      } catch {
        throw new Error(
          'docs-captures: no se encontro el filtro de endpoints en /api-playground ' +
            '(i18n `filterPlaceholder`). Sin el, elegir un endpoint entre 765 es a ciegas.',
        )
      }
      // ⛔ ANCLADO A PROPOSITO. Playwright casa `name` por SUBCADENA y sin distinguir mayusculas,
      //    asi que `'GET /v1/agents'` casaba TAMBIEN `GET /v1/agents/{id}` y el localizador moria
      //    por modo estricto — dentro de mi propia guarda, que entonces culpaba al arbol. Lo vi en
      //    el PNG: el filtro habia funcionado y las cinco filas estaban ahi.
      const endpoint = page.getByRole('button', {
        name: /^GET\s+\/v1\/agents$/,
      })
      try {
        await endpoint.waitFor({ timeout: 8_000 })
        await endpoint.click()
      } catch {
        throw new Error(
          'docs-captures: no se pudo elegir `GET /v1/agents` en el arbol de endpoints ' +
            '(endpoint-tree.tsx:120). Sin seleccion no hay panel de peticion y no hay boton Send.',
        )
      }
      const enviar = page.getByRole('button', { name: 'Send' })
      try {
        await enviar.waitFor({ timeout: 8_000 })
        await enviar.click()
      } catch {
        throw new Error(
          'docs-captures: no se pudo enviar una peticion en /api-playground (boton `Send`, i18n ' +
            '`send`). Sin una peticion no hay historial: la pestana `History` saldria vacia y la ' +
            'captura no ensenaria la funcion que documenta.',
        )
      }
      const pestana = page.getByRole('tab', { name: /^History/ })
      try {
        await pestana.waitFor({ timeout: 8_000 })
        await pestana.click()
      } catch {
        throw new Error(
          'docs-captures: no se encontro la pestana `History` en /api-playground ' +
            '(api-playground-view.tsx:185).',
        )
      }
    },
  },
  {
    id: 'logs',
    path: '/logs',
    settle: 1000,
    live: true,
    heading: /^Log Viewer$/,
  },
  {
    // C07-02. El testigo es el h1 de la vista, no su id: una captura que se guardara con el
    // esqueleto puesto pasaría cualquier comprobación de «existe el fichero», y ya pasó una vez.
    id: 'tenants',
    path: '/tenants',
    heading: /^Tenants$/,
  },
  {
    id: 'residency',
    path: '/residency',
    settle: 1000,
    heading: /^Data residency$/,
  },
  // ⛔ AQUÍ HABÍA `{ id: 'executive', path: '/executive', settle: 1500 }`, con el comentario «the
  //    public marketing site also embeds this view (its public/product/ set)». RETIRADA el
  //    2026-08-17 porque **la ruta no existe y sus dos capturas eran un «Page not found»** — y
  //    salían VERDES.
  //
  //    Lo que hay de verdad: `web/src/features/executive/` no tiene entrada en el registro de
  //    features (de donde `app/routes.tsx:29` GENERA el árbol de rutas), y sus únicos importadores
  //    son los de `features/home/` — sus tiles se reusan en la portada `/`, exactamente como
  //    explica `features/executive/components.tsx:64`. `executive-view.tsx` existe y nadie lo
  //    monta.
  //
  //    ⇒ Si el set `public/product/` de la web comercial embebe «executive», está publicando la
  //    pantalla de error como si fuera producto. Es un repo separado y esta sesión no lo toca:
  //    queda dicho aquí y escalado al integrador.

  // ═══ LAS CUATRO TOMAS DE LAS GUIAS DE INTEGRACION ══════════════════════════════
  //
  // ⛔ Y SON CUATRO, NO NUEVE. Las guias de Claude Code, Codex y Grok marcaban nueve huecos,
  //    pero CINCO de ellos son vistas que este arnes YA fotografia — /sessions, /security,
  //    /finops, /observability y /health son justo las que la tabla «Control console» de cada
  //    guia manda abrir, y estan en el set de 71. Fabricar nueve ids nuevos habria producido
  //    cinco casi-duplicados con otro nombre, que es peor que no tenerlos: dos ficheros que se
  //    creen distintos derivan.
  //
  // ⛔ LO QUE DE VERDAD FALTABA es la pestaña Connectors. `console` fotografia la pestaña POR
  //    OMISION (Users & groups), y Connectors NO es ruta de primer nivel: es `?tab=connectors`
  //    del deep link de (`console-view.tsx`, TAB_VALUES). Sin esto las tres guias abren
  //    sobre una pantalla que el lector no puede reconocer.
  //
  // Los tres conectores los siembra `scripts/seed-guide-connectors.sh` antes de montar nada:
  // claude-code-prod, codex-enterprise y grok-demo son ejemplos de la PROSA de las guias y no
  // existen en ningun sitio del arbol, asi que sin sembrarlos esta pestaña sale vacia y la
  // captura diria que el producto no tiene conectores.
  {
    id: 'guias-connectors',
    path: '/console?tab=connectors',
    heading: /^Control console$/,
    settle: 800,
  },
  // ⛔ LAS TRES FICHAS DE CONFIGURACION NO SE PUEDEN FOTOGRAFIAR, Y NO ES UN FALLO DE SELECTOR.
  //    Estuvieron aqui y fallaron las seis (3 conectores x 2 temas). El clic en «Edit» FUNCIONA;
  //    lo que aparece detras es el dialogo del producto:
  //
  //      «Step-up authentication required — This action requires AAL3 (hardware,
  //       phishing-resistant). Your session is AAL1 (password). […]
  //       Privileged read — recorded in the audit ledger.»
  //
  //    Es decir: abrir la configuracion de un conector es una LECTURA PRIVILEGIADA que exige
  //    una llave hardware. Una sesion de Playwright es AAL1, asi que no hay forma de llegar a
  //    esa pantalla desde este arnes — y no debe haberla: el control esta haciendo su trabajo.
  //    El propio runner ya lo avisaba para la API («contesta step_up_required (AAL3)») y yo
  //    entre por la UI a la misma puerta.
  //
  //    ⇒ APROBADO POR the planner (2026-08-31): la imagen de los huecos 2/5/8 es EL PROPIO DIALOGO
  //      de step-up. Enseña la gobernanza en vez de describirla, y es literalmente lo que el
  //      lector con sesion de contraseña VERA cuando intente abrir la ficha. Sin cuenta AAL3
  //      falsa: la foto vale porque el control esta actuando de verdad.
  {
    id: 'guias-config-step-up',
    path: '/console?tab=connectors',
    settle: 800,
    modal: true,
    despues: async (page) => {
      await page
        .getByRole('row', { name: /claude-code-prod/ })
        .getByRole('button', { name: /^edit$/i })
        .click()
      await page.getByRole('dialog').waitFor({ state: 'visible' })
    },
  },
]

/**
 * La toma, compartida por las TRES familias de celdas (vistas, overlay del héroe y pestañas de
 * consola) para que ninguna se quede sin la comprobación ni sin evidencia.
 *
 * Emite un `<id>-<theme>.evidence.json` junto al PNG: qué ruta se pidió, qué `<h1>` se vio y el
 * sha256 de la imagen. `scripts/docs-captures.sh` los funde en `manifest.json` con el commit de la
 * toma — se hace por fichero y no appendando a uno solo **a propósito**: Playwright reparte las
 * celdas entre varios workers y dos escrituras concurrentes sobre el mismo fichero se pisan.
 */
async function tomar(
  page: import('@playwright/test').Page,
  {
    id,
    theme,
    ruta,
    heading,
    settle,
    live,
    recorte,
  }: {
    id: string
    theme: string
    ruta: string
    heading?: RegExp
    settle?: number
    live?: boolean
    recorte?: (
      page: import('@playwright/test').Page,
    ) => Promise<{ x: number; y: number; width: number; height: number }>
  },
) {
  // ⛔ BAJO UN DIALOGO MODAL NO HAY NINGUN HEADING DE NIVEL 1, y eso tumbaba tres escenas del
  //    guion de video. MEDIDO en el snapshot de accesibilidad del fallo, no deducido:
  //
  //        - dialog "acme-platform governed session":
  //          - heading "acme-platform governed session" [level=2]
  //
  //    El fondo queda `aria-hidden` mientras el dialogo esta abierto, asi que el arbol se reduce al
  //    dialogo y `getByRole('heading', { level: 1 })` no encuentra NADA. El h1 sigue en el DOM; lo
  //    que desaparece es del arbol de ACCESIBILIDAD, que es contra el que consulta `getByRole`.
  //
  //    Se espera a que haya ALGUN encabezado y se prefiere el de nivel 1 si existe. El suelo no se
  //    debilita: la pagina de error tiene su `<h1>` y sigue cayendo aqui — lo que se admite es que
  //    una vista con modal se acredite por el encabezado del modal, que es justo lo que la escena
  //    esta fotografiando.
  const alguno = page.getByRole('heading').first()
  await expect(alguno).toBeVisible({ timeout: 60_000 })
  const nivel1 = page.getByRole('heading', { level: 1 }).first()
  const h1 = (await nivel1.count()) > 0 ? nivel1 : alguno
  const texto = ((await h1.textContent()) ?? '').trim()

  // ⛔ EL SUELO QUE APLICA INCLUSO SIN ORÁCULO DECLARADO. La página de error tiene `<h1>` y por eso
  //    `/executive` pasaba en verde con un «Page not found». Este harness fija `olivares.lang` a
  //    `en` arriba, así que el literal es estable.
  expect(
    texto,
    `${id}: la ruta ${ruta} sirvió la página de ERROR, no la vista`,
  ).not.toMatch(/page not found/i)
  if (heading) {
    expect(
      texto,
      `${id}: el <h1> dice «${texto}» — el router no sirvió la vista pedida (${ruta})`,
    ).toMatch(heading)
  }

  if (!live) await page.waitForLoadState('networkidle')
  if (settle) await page.waitForTimeout(settle)

  // ⛔ LA ÚLTIMA COMPROBACIÓN VA AQUÍ, JUSTO ANTES DE DISPARAR, y no arriba con el testigo del h1.
  //    El `heading` se comprueba ANTES de `networkidle`, así que sólo dice «el router sirvió esta
  //    vista»: el h1 se pinta antes de que lleguen los datos. Entre esa comprobación y la foto
  //    pasan todas las peticiones — y una que conteste 4xx deja la página IDLE y con el error
  //    boundary puesto. Idle no es cargado.
  //
  //    Estas dos aserciones son lo que separa «se guardó un PNG» de «se guardó la pantalla». Una
  //    captura del esqueleto o del boundary pasa cualquier comprobación de existencia del fichero,
  //    y estas imágenes van a superficies PÚBLICAS: una vez publicadas, el defecto lo ve el cliente.
  // ⛔ AQUI `count()` ES LO CORRECTO, y la distincion importa porque barri su clase en las
  //    precondiciones y alguien podria «arreglar» estas por simetria. Las de arriba exigian
  //    PRESENCIA —«tiene que haber una fila `Launched`»— y ahi muestrear una vez puede acusar en
  //    falso a una carga lenta: necesitan `waitFor`, que reintenta. Estas dos exigen AUSENCIA
  //    —«no puede haber un boundary ni un spinner EN EL INSTANTE DE LA FOTO»— y reintentar seria
  //    PEOR: le daria tiempo al indicador a desaparecer y la aserción dejaria de comprobar lo
  //    unico que importa, que es el estado en ese instante.
  //
  //    ⇒ La sonda honesta no es «0 `count()`» sino «0 decisiones de PRESENCIA REQUERIDA por
  //      `count()`». Lo afirme de mas al barrer la clase; queda acotado aqui.
  const boundary = await page
    .getByText('This view crashed', { exact: false })
    .count()
  expect(
    boundary,
    `${id}: la vista cayó al error boundary DESPUÉS de cargar — la captura mostraría el fallo`,
  ).toBe(0)
  const cargando = await page.locator('[role="progressbar"]:visible').count()
  expect(
    cargando,
    `${id}: quedaba un indicador de carga visible al disparar — sube \`settle\` para esta vista`,
  ).toBe(0)

  // ⚠ SE REGISTRA, NO SE FALLA. Una pantalla que sale vacía puede ser correcta —el estate sembrado
  //    no puebla todo— y puede ser un hueco de sembrado que la documentación pública enseña como si
  //    fuese el producto. El arnés no sabe cuál de las dos es, así que **no adjudica**.
  //
  // ⛔ Y NO SE GUARDA UN BOOLEANO, porque un booleano MIENTE aquí. La primera versión guardaba
  //    «¿hay algún estado vacío?» y daba 22 vistas de 55 — un número que se lee como «22 pantallas
  //    vacías» y que es falso: `killswitch` entra en esa lista y muestra el formulario de paro
  //    ENTERO con un único panel vacío al final («No stops recorded»), que es el estado CORRECTO de
  //    un estate sano. Un panel vacío dentro de una pantalla llena no es un hueco de sembrado.
  //
  // ⇒ Se guardan las dos magnitudes que separan un caso del otro: cuántos paneles vacíos hay, y
  //    cuánto texto tiene la región principal. Una pantalla llena con un panel vacío tiene mucho
  //    texto; una que sólo pinta su hueco, muy poco. La triaje la hace una persona con las dos
  //    cifras delante, no el arnés con un booleano.
  const vacios = await page.locator('[data-slot="empty-state"]:visible').count()
  // ⛔ EL `.catch()` NO EVITA LA ESPERA — medido el 2026-08-18 al capturar las patas públicas.
  //    `innerText()` sobre un locator que no casa con nada **espera**, y sin `actionTimeout` espera
  //    hasta agotar el TIEMPO DEL TEST: cuatro mediciones de `/login`, `/setup`, `/accept-invite` y
  //    `/status-page` murieron a los 30 s. El `catch` recogía el error, pero para entonces el test ya
  //    estaba muerto, y el veredicto que salía era un **timeout pelado** — que no dice «esta página no
  //    tiene `<main>`», dice «algo tardó». Un fallo que no nombra su causa cuesta la sesión siguiente.
  //
  //    `count()` NO espera: devuelve 0 al instante. Por eso la guarda va delante y la lectura detrás.
  //
  // ⛔⛔ Y `hay_main` SE GUARDA APARTE, porque si no el arreglo de arriba crea el defecto que este
  //    fichero lleva medio fichero combatiendo: con la guarda puesta, `texto_main: 0` significa **dos
  //    cosas** —«hay región principal y está vacía» y «no hay región principal»— y sólo una de las dos
  //    es un hueco de sembrado. Un cero que colapsa dos estados no se puede triar.
  // ⛔⛔ Y `texto_main` TAMPOCO DISCRIMINA EN LA BANDA QUE IMPORTA — medido el 2026-08-18, y es la
  //    SEGUNDA vez que una métrica de esta función engaña. La primera fue un booleano que decía
  //    «22 vistas vacías de 55» contando `killswitch`, que está llena. La sustituí por estas dos
  //    cifras… y al mirar las IMÁGENES de las cinco peores —`catalog` 393, `eventing` 444,
  //    `model-operations` 459, `work` 464, `deploy` 468— resulta que **las cinco están ENTERAS
  //    vacías**: «No catalog entries created yet», «No deployments declared yet». Yo las había
  //    triado como «pantallas llenas con un panel vacío legítimo» leyendo sólo los números.
  //
  //    La causa es que **el cromo es verboso**: título, subtítulo, pestañas, filtros, cabeceras de
  //    columna y el propio texto del estado vacío suman ~400 caracteres SIN una sola fila de datos.
  //    Una pantalla con dos filas puntúa igual. El número no separa lo que hay que separar.
  //
  // ⇒ `filas` es la señal que sí: cuántas filas de DATOS hay, descontando la que ocupa el estado
  //   vacío. Cero filas con cabeceras presentes es un hueco de sembrado; no hace falta interpretar
  //   nada. Se sigue registrando, no fallando — el arnés no adjudica, sólo deja de mentir.
  //
  // ⛔⛔ Y `filas` SOLA TAMPOCO BASTA — tercera métrica de esta función con punto ciego, y salió en
  //    el mismo censo que la estrenó. `filas = 0` significa dos cosas: «la tabla está vacía» y
  //    **«no hay tabla»**. En el censo completo dieron cero filas 19 vistas, y entre ellas
  //    `killswitch` (1672 caracteres) e `inference-proxy` (2274), que son pantallas de FORMULARIO
  //    llenas de conmutadores y sin una sola tabla. Contarlas como huecos de sembrado sería repetir
  //    el error del booleano que empezó todo esto.
  //
  // ⇒ `tablas_vacias` separa los dos casos sin interpretar nada: tablas que TIENEN cabeceras de
  //   columna y NO tienen ni una fila de datos. Un formulario da 0 porque no hay tablas; un listado
  //   sin sembrar da ≥1.
  const { filas, tablasVacias } = await page.evaluate(() => {
    const raiz = document.querySelector('main') ?? document.body
    const esDato = (tr: Element) =>
      !tr.querySelector('[data-slot="empty-state"]')
    const filas = Array.from(raiz.querySelectorAll('tbody tr')).filter(
      esDato,
    ).length
    const tablasVacias = Array.from(raiz.querySelectorAll('table')).filter(
      (tabla) => {
        const cabeceras = tabla.querySelectorAll('thead th').length
        const datos = Array.from(tabla.querySelectorAll('tbody tr')).filter(
          esDato,
        ).length
        return cabeceras > 0 && datos === 0
      },
    ).length
    return { filas, tablasVacias }
  })
  // ⛔ CLAVES DE i18n SIN TRADUCIR, y esta sonda esta CALIBRADA — la primera version no lo estaba.
  //    `check-i18n-usage.mjs` declara fuera de alcance las claves dinamicas (`t(\`live.${estado}\`)`),
  //    asi que una que falte se pinta CRUDA en la foto y ningun gate lo dice. Hay que medir la
  //    PANTALLA.
  //
  // ⛔ PERO «PARECE UNA CLAVE» NO ES «ES UNA CLAVE», y esa confusion me costo una corrida entera.
  //    La primera version contaba tokens con FORMA de clave (`a.b.c`) y en las 122 capturas de B7
  //    saco **924**. Ninguno era una clave sin traducir: eran nombres de accion de auditoria y de
  //    eventos de bus que el producto ense~na COMO DATO —`governance.approval.create`,
  //    `org.create`, `edge.observed`, `cost.sampled`— y que tienen exactamente esa forma. Calibrado
  //    despues contra las rutas reales de los locales: **42 de 42 tokens muestreados eran falsos
  //    positivos, 0 eran claves**.
  //
  // ⇒ La logica se INVIERTE: no «tiene forma de clave» sino «ESTA en los locales». Un token solo
  //   cuenta si existe como ruta de clave real, con namespace o sin el; asi el vocabulario de
  //   dominio no puede disparar la sonda por mucho que se le parezca.
  const clavesCrudas = await page.evaluate((conocidas: string[]) => {
    const raiz = document.querySelector('main') ?? document.body
    const texto = (raiz as HTMLElement).innerText ?? ''
    const set = new Set(conocidas)
    // ⛔ TERCERA CALIBRACION, Y LAS DOS ANTERIORES SE SUMAN EN VEZ DE SUSTITUIRSE.
    //    v1 marcaba «tiene FORMA de clave» (`a.b.c`) y saco 924 falsos positivos: el
    //    vocabulario de dominio tambien lleva puntos (`cost.sampled`, `org.create`).
    //    v2 lo cambio por «ES una ruta de clave», y eso trae la familia contraria: de las
    //    ~9 300 rutas, muchas son hojas de UNA palabra corriente (`status`, `groups`,
    //    `empty`, `unavailable`), asi que una FRASE BIEN TRADUCIDA las dispara.
    //
    // ⚠ Medido el 2026-08-29 sobre las 126 tomas del dry-run: **252 marcas, y la pagina
    //   `rate-limits` esta integramente en ingles** — sus seis marcas (`status`,
    //   `unavailable` x3, `empty`, `groups`) son palabras dentro de oraciones correctas.
    //   El manifiesto declara que «cero es la unica cifra aceptable», y con v2 ese cero
    //   era INALCANZABLE: `api-playground` define la clave `beta` con el valor `"beta"`,
    //   asi que una insignia BIEN traducida es indistinguible de una clave cruda.
    //
    // ⇒ El predicado es la CONJUNCION: lleva punto **Y** es ruta de clave real. Una
    //   clave sin traducir que i18next pinta cruda conserva sus puntos; el vocabulario de
    //   dominio los lleva pero no esta en los locales; la prosa no los lleva. Verificado
    //   contra las dos familias: 0 de las 252 marcas de hoy y 0 de los 4 tokens de v1.
    return texto.split(/\s+/).filter((t) => t.includes('.') && set.has(t))
  }, CLAVES_I18N)

  const hayMain = (await page.locator('main').count()) > 0
  const textoMain = hayMain
    ? (
        (await page
          .locator('main')
          .first()
          .innerText()
          .catch(() => '')) ?? ''
      ).trim().length
    : 0

  // ⛔ EL ANILLO DE FOCO, AQUI Y NO EN CADA VISTA. Radix devuelve el foco al disparador al
  //    cerrar un menu o un select, asi que CUALQUIER vista cuyo `despues` pulse una pestaña,
  //    un select o un menu se fotografia con el anillo naranja — un estado que el lector no
  //    puede reproducir. Lo marco r4 en `sessions` revisando las doce.
  //    ⚠ APARCAR EL RATON NO LO QUITA: el arnes ya mueve el puntero a 0,0 y el anillo seguia,
  //      porque ese foco es de TECLADO. Son cosas distintas y hacen falta las dos.
  await page.evaluate(() =>
    (document.activeElement as HTMLElement | null)?.blur(),
  )

  const png = `playwright-report/docs/${id}-${theme}.png`
  // ⛔ RECORTE DECLARADO, NO OTRO VIEWPORT. Cuando una vista trae `recorte`, la toma se acota
  //    a ese rectangulo y el rectangulo QUEDA ESCRITO en la evidencia. La distincion importa:
  //    el disparo sigue siendo a 1440x1000 @2x como las demas (criterio F), y lo unico que
  //    cambia es que se guarda un trozo. Un viewport distinto cambiaria el LAYOUT; un recorte
  //    no toca nada de lo que se ve.
  const clip = recorte ? await recorte(page) : undefined
  await page.screenshot(clip ? { path: png, clip } : { path: png })
  const sha = createHash('sha256').update(fs.readFileSync(png)).digest('hex')
  fs.writeFileSync(
    path.join('playwright-report', 'docs', `${id}-${theme}.evidence.json`),
    `${JSON.stringify(
      {
        id,
        theme,
        route: ruta,
        h1: texto,
        vacios,
        filas,
        tablas_vacias: tablasVacias,
        hay_main: hayMain,
        texto_main: textoMain,
        claves_crudas: clavesCrudas.length,
        claves_crudas_vistas: clavesCrudas.slice(0, 8),
        // el recorte va DECLARADO en la evidencia: quien audite la imagen sabe que es un
        // trozo y de donde sale, sin tener que deducirlo del tamaño del PNG.
        recorte_declarado: clip ?? null,
        sha256: sha,
      },
      null,
      2,
    )}\n`,
  )

  // ⛔ UN PAR DE TEMAS IDENTICO ES UNA MENTIRA SILENCIOSA, y ningun `passed` lo delata: si el tema
  //    no llega a cambiar, se guardan DOS VECES LA MISMA FOTO con nombres distintos y la galeria
  //    publica un par que no es un par. Medido en `status-page`: su conmutador no existe —es una
  //    vista publica sin cabecera— y el clic expiraba; al curarlo, lo unico que probo que el tema
  //    HABIA cambiado fue comparar los dos `md5` A MANO. Esto lo hace el arnes, para TODAS.
  //
  //    Se compara el `sha256` que la evidencia ya calcula: coste cero, y cubre cualquier causa
  //    futura —un conmutador renombrado, un store que no repinta, una vista sin tokens de tema—
  //    sin tener que preverla.
  if (theme === 'dark') {
    const claro = path.join(
      'playwright-report',
      'docs',
      `${id}-light.evidence.json`,
    )
    if (fs.existsSync(claro)) {
      const otro = JSON.parse(fs.readFileSync(claro, 'utf8')) as {
        sha256?: string
      }
      expect(
        sha,
        `la toma \`dark\` de \`${id}\` es BYTE A BYTE IGUAL que su \`light\`: el tema no cambio, ` +
          'asi que el par publicado son dos copias de la misma foto. Mira si esa vista tiene ' +
          'conmutador de tema y si sus colores salen de tokens (variables CSS) o son fijos.',
      ).not.toBe(otro.sha256)
    }
  }
}

// VIEWPORT — 1440x1000 @2x, not 1440x900 @1x (criterion F of the planner's review of the 142
// console captures, 2026-08-31).
//
// The height is a DEFECT FIX, not a preference. At 900 the sidebar does not fit: the fixed
// "Settings" footer sits on top of "Setup wizard" in EVERY console capture — the approved
// ones included — which is pattern P10 of that review, and it is charged to the harness
// because nothing in the product is wrong. 1000 gives the nav its last row back, and the
// extra 100px also relieves P2 (framing that cuts a row or a panel in half) without
// re-framing anything on purpose.
//
// deviceScaleFactor: 2 renders at 2880x2000 and downsamples to a 1440-wide slot, which is
// what stops the small type in tables and badges from going soft on a HiDPI screen. It is
// the reason a screenshot of a dense console reads at all when a reader zooms.
//
// ⚠ It also makes every PNG roughly four times the bytes. That is the accepted cost of the
// criterion, but it is a real one on a repository that ships 142 of them, so it is written
// here rather than discovered later in a diff.
test.use({ viewport: { width: 1440, height: 1000 }, deviceScaleFactor: 2 })

test.describe('Docs captures over real seeded data', () => {
  test.skip(
    !demoTenant,
    'DEMO_TENANT not set — run via scripts/docs-captures.sh',
  )

  // ⛔ LOS DOS TEMAS SALEN DE LA MISMA CARGA — ES EL CRITERIO D, NO UNA OPTIMIZACION.
  //    Aqui habia DOS tests por vista (uno por tema), cada uno con su login y su `goto`: DOS
  //    INSTANTES DE DATOS. Lo delata cualquier cifra derivada del reloj — the planner midio en
  //    `finops` FORECAST $5.062,42 en light y $5.062,37 en dark, con el mismo total y los
  //    mismos tokens. Eso no es un par: son dos fotos de dos momentos.
  //    Ahora se carga UNA vez, se dispara light, se cambia el tema POR LA INTERFAZ y se dispara
  //    dark SIN volver a navegar: `setTheme` solo alterna la clase `dark` en `<html>`
  //    (`stores/theme.ts`), asi que se re-pinta sin pedir datos otra vez.
  for (const view of VIEWS) {
    {
      test(`capture ${view.id}`, async ({ page }) => {
        await page.addInitScript(
          ([tenant]) => {
            localStorage.setItem(
              'olivares.tenant',
              JSON.stringify({ state: { activeTenant: tenant }, version: 0 }),
            )
            localStorage.setItem(
              'olivares.lang',
              JSON.stringify({ state: { lang: 'en' }, version: 0 }),
            )
            // theme store reads this RAW (no JSON) before first paint.
            localStorage.setItem('olivares.theme', 'light')
          },
          [demoTenant],
        )

        // ⛔ `exact: true` NO ES COSMETICA — sin ella este oraculo es AMBIGUO y falla al azar.
        //    Medido en una corrida completa de 122 tomas el 2026-08-20: DOS tomas cayeron con
        //    «strict mode violation: getByRole('link', { name: 'Inventory' }) resolved to 2
        //    elements», y las dos eran vistas SANAS —cada una paso en el otro tema—. Los dos
        //    elementos son el enlace del `nav` lateral y la TARJETA del panel, que comparte
        //    `href="/inventory"` y cuyo nombre accesible es «Inventory 25 3 agents · 3».
        //
        //    Playwright nombra el remedio en su propio error («aka … exact: true»), y es el que
        //    ESTRECHA la comprobacion: `.first()` habria hecho pasar el test dejando la ambiguedad
        //    dentro, que es apagar el oraculo para que no moleste. El oraculo dice «el shell de la
        //    app monto su navegacion», y con `exact` sigue diciendo eso y solo eso.
        await page.goto('/login')
        await page.locator('#email').fill(DEMO_EMAIL)
        await page.locator('#password').fill(DEMO_PASSWORD)
        await page.getByRole('button', { name: /^sign in$/i }).click()
        await expect(
          page.getByRole('link', { name: 'Inventory', exact: true }),
        ).toBeVisible({
          timeout: 60_000,
        })

        // El sembrado por ruta va DESPUÉS del login y ANTES de navegar: necesita la sesión, y lo
        // que prepara tiene que estar puesto cuando la vista monte.
        if (view.prepara) await view.prepara(page)

        await page.goto(view.path)
        // El estado que sólo existe con la vista ya montada.
        if (view.despues) await view.despues(page)

        for (const theme of ['light', 'dark'] as const) {
          if (theme === 'dark') {
            // ⛔ UN MODAL TAPA EL CONMUTADOR. El dialogo pone `aria-hidden` sobre el fondo, asi
            //    que «Toggle theme» deja de ser alcanzable y el clic expira a los 30 s — medido
            //    en `guias-config-step-up`. Se cierra, se cambia el tema y se vuelve a abrir con
            //    el MISMO `despues`: seguimos sin navegar, o sea el mismo instante de datos.
            if (view.modal) {
              await page.keyboard.press('Escape')
              await expect(page.getByRole('dialog')).toBeHidden()
            }
            // Se cambia por el TOGGLE REAL y no escribiendo la clase a mano: hay componentes
            // que leen `resolved` del store (colores de grafico), y tocar solo la clase los
            // dejaria en el tema anterior — el par saldria incoherente de otra forma.
            //
            // ⛔ PERO NO TODAS LAS VISTAS TIENEN CROMO, y eso tumbaba la tanda entera. `/status-page`
            //    es PUBLICA: no monta la cabecera de la aplicacion, asi que «Toggle theme» no
            //    existe y el clic expiraba a los 30 s. Medido en el snapshot de accesibilidad del
            //    fallo: en esa pagina solo hay `heading "Olivares Control Plane — Status"` y un
            //    `button "Refresh"`. Un fallo, y el contrato de publicacion —con razon— no publica
            //    una tanda roja: el juego completo se quedaba sin salir por una vista sin cabecera.
            const conmutador = page.getByRole('button', {
              name: 'Toggle theme',
            })
            if ((await conmutador.count()) > 0) {
              await conmutador.click()
              await page
                .getByRole('menuitem', { name: 'Dark', exact: true })
                .click()
            } else {
              // Y AQUI SI SE ALTERNA LA CLASE A MANO, porque la razon de la prohibicion de arriba
              // NO APLICA — y esto se mide, no se supone: esa regla existe porque hay componentes
              // que leen `resolved` del store y quedarian en el tema anterior. Sobre
              // `web/src/features/health/status-page.tsx`: CERO usos de `useTheme`, `resolved`,
              // `Chart` o `recharts`. No hay nada que se quede descolgado.
              //
              // Y que la clase BASTE para repintarla tambien esta medido, no supuesto: esa vista
              // tiene CERO variantes `dark:` y CUATRO tokens de tema (`text-foreground`,
              // `border-border`, `text-muted-foreground`), o sea que su color sale de variables
              // CSS que la clase de `<html>` gobierna. Si usara colores fijos, los dos temas
              // saldrian iguales y este apaño seria una mentira silenciosa.
              //
              // ⚠ Y se sigue SIN NAVEGAR, que es lo que el criterio D protege: los dos temas
              //   salen de la MISMA carga y del mismo instante de datos. La regla no se revierte;
              //   se le extiende el universo a las vistas que no tienen cromo.
              //
              // ⚠ Si el tema NO cambiara, los dos temas saldrian IGUALES y el par seria una
              //   mentira silenciosa. Por eso el `expect` de abajo NO es decorativo: es la unica
              //   prueba de que el cambio ocurrio, y corre en las dos ramas.
              await page
                .locator('html')
                .evaluate((el) => el.classList.add('dark'))
            }
            await expect(page.locator('html')).toHaveClass(/dark/)
            if (view.modal && view.despues) await view.despues(page)
          }
          // ⛔ RATON FUERA DEL ENCUADRE. Playwright deja el puntero donde ocurrio el ultimo
          //    clic, y un `:hover` pintado es un estado que el lector no puede reproducir.
          await page.mouse.move(0, 0)
          await tomar(page, {
            id: view.id,
            theme,
            ruta: view.path,
            heading: view.heading,
            settle: view.settle,
            live: view.live,
            recorte: view.recorte,
          })
        }
      })
    }
  }

  // The drift overlay is the product's hero — capture it open, both themes.
  for (const theme of ['light', 'dark'] as const) {
    test(`capture access-map drift overlay — ${theme}`, async ({ page }) => {
      await page.addInitScript(
        ([tenant, th]) => {
          localStorage.setItem(
            'olivares.tenant',
            JSON.stringify({ state: { activeTenant: tenant }, version: 0 }),
          )
          localStorage.setItem(
            'olivares.lang',
            JSON.stringify({ state: { lang: 'en' }, version: 0 }),
          )
          localStorage.setItem('olivares.theme', th)
        },
        [demoTenant, theme],
      )

      await page.goto('/login')
      await page.locator('#email').fill(DEMO_EMAIL)
      await page.locator('#password').fill(DEMO_PASSWORD)
      await page.getByRole('button', { name: /^sign in$/i }).click()
      await expect(
        page.getByRole('link', { name: 'Inventory', exact: true }),
      ).toBeVisible({
        timeout: 60_000,
      })

      await page.goto('/access-map')
      await expect(page.locator('.react-flow__node').first()).toBeAttached({
        timeout: 60_000,
      })
      await page.getByRole('button', { name: /permitted vs observed/i }).click()
      // Esta celda YA discriminaba su pantalla —el nodo del grafo y el encabezado del overlay son
      // propios de ella—, así que `tomar` le añade el suelo del 404 y la evidencia.
      await expect(page.getByRole('heading', { name: /drift/i })).toBeVisible()
      await tomar(page, {
        id: 'access-map-drift',
        theme,
        ruta: '/access-map (overlay drift)',
        settle: 1500,
      })
    })
  }

  //the console's granularity surfaces are tabs, not routes — navigate to
  // the console and click the tab trigger before the shot.
  const CONSOLE_TABS = [
    { id: 'console-agents', trigger: /^agents$/i },
    { id: 'console-bindings', trigger: /^source bindings$/i },
  ]
  for (const theme of ['light', 'dark'] as const) {
    for (const tab of CONSOLE_TABS) {
      test(`capture ${tab.id} — ${theme}`, async ({ page }) => {
        await page.addInitScript(
          ([tenant, th]) => {
            localStorage.setItem(
              'olivares.tenant',
              JSON.stringify({ state: { activeTenant: tenant }, version: 0 }),
            )
            localStorage.setItem(
              'olivares.lang',
              JSON.stringify({ state: { lang: 'en' }, version: 0 }),
            )
            localStorage.setItem('olivares.theme', th)
          },
          [demoTenant, theme],
        )

        await page.goto('/login')
        await page.locator('#email').fill(DEMO_EMAIL)
        await page.locator('#password').fill(DEMO_PASSWORD)
        await page.getByRole('button', { name: /^sign in$/i }).click()
        await expect(
          page.getByRole('link', { name: 'Inventory', exact: true }),
        ).toBeVisible({
          timeout: 60_000,
        })

        await page.goto('/console')
        // ⚠ AQUÍ EL ORÁCULO NO ES EL `<h1>` Y NO DEBE SERLO: las dos pestañas viven en `/console`
        //   y comparten encabezado, así que un `heading` no las distinguiría. Lo que discrimina es
        //   este `click` — si el disparador de la pestaña no existe, la celda falla aquí.
        await page.getByRole('tab', { name: tab.trigger }).click()
        await tomar(page, {
          id: tab.id,
          theme,
          ruta: `/console (pestaña ${tab.id})`,
          settle: 1000,
        })
      })
    }
  }

  // ═══ C10-04 · las CUATRO escenas con interaccion del guion de video ═══════════════════════
  //
  // `docs/launch/video-demo-script.md` marca once escenas `CAPTURE-TODO` y dice literalmente
  // «real capture via scripts/docs-captures.sh»: es ESTE arnes, no otro. Siete se sirven con las
  // entradas de `VIEWS` de arriba (una ruta, una foto). Las cuatro de aqui NO: exigen interactuar
  // —pasar el raton por una arista, abrir una pestaña, abrir un panel— y por eso viven en su
  // propio bloque, igual que el overlay de deriva.
  //
  // ⛔ LOS SELECTORES SE VERIFICARON CONTRA EL CODIGO ANTES DE ESCRIBIRLOS, y no es celo: este
  //    bloque **no se puede ejecutar hasta que aterrice B3** (capturar la consola vieja es trabajo
  //    tirado), asi que un selector inventado no fallaria HOY — fallaria en la unica pasada, que es
  //    justo cuando no queremos descubrirlo. Verificacion, fichero por fichero:
  //      · arista del mapa   -> access-map-view.tsx:442 `edgeTypes={accessEdgeTypes}` (React Flow)
  //      · pestañas Live/Gov -> an internal design note (not shipped):284-296, que monta `LiveConsole` (:320)
  //                             y `GovernancePanel` (:333)
  //      · fila de sesion    -> sessions-workspace-view.tsx:551 `onRowClick` -> `setTarget(...)`
  //      · Browse files      -> agentops/workspaces-panel.tsx:152 `t('workspaces.open')`
  //
  // ⛔ Y UNA CORRECCION AL GUION QUE ESTE BLOQUE OBLIGA: las escenas 7 y 8 dicen «the detail
  //    sheet». Esa hoja —`agentops/run-detail.tsx` `RunDetailSheet`— esta EXPORTADA y **no la monta
  //    nadie** (cero importadores fuera de su fichero; `agentops/index.tsx` ni la exporta). Lo
  //    alcanzable es la TARJETA de sesion. El guion queda corregido en el mismo commit; la hoja
  //    muerta es una fila aparte y no se toca aqui.
  // ═══ /accept-invite: RETIRADA ENTERA el 2026-08-29 ═══════════════════════════════════════
  //
  // Aqui vivia la toma de una invitacion VIVA, sembrada conduciendo el producto: `/console` ->
  // «Users & groups» -> «Onboard user» -> el enlace de un solo uso -> navegar a el. Es el diseño
  // correcto y por eso se documenta su retirada en vez de borrarla en silencio.
  //
  // ⛔ NO SE PUEDE EJECUTAR: pulsar «Onboard user» abre «Step-up authentication required — AAL3
  //    (hardware, phishing-resistant). Your session is AAL1». Medido el 2026-08-29 en dos
  //    corridas completas: los dos temas agotan los 30 s ahi. Es el MISMO control que el
  //    manifiesto ya declara para `console` en `empty_by_control`.
  //
  // ⇒ No hay arreglo de arnes que no debilite ese control, y eso esta descartado de raiz.
  //   Adjudicado por the planner: se retira la toma ENTERA —esta y la de sin-token, que era la
  //   pantalla de error— y la ruta se declara como AUSENCIA CON RAZON en el manifiesto
  //   (`not_captured_by_control`). Un PNG que nunca podra refrescarse es una mina de reloj, y
  //   una pantalla de error publicada como si fuera la vista es autoridad falsa.
  //
  // Para reponerla hace falta que el arnes pueda satisfacer AAL3, no un selector nuevo.

  /**
   * Abre la tarjeta de una sesion OPERADA — la unica que tiene las pestañas condicionales.
   *
   * ⛔ Y SI NO HAY NINGUNA, LO DICE. Un `click` sobre un locator que no casa espera hasta agotar el
   *    test y el veredicto que sale es «timeout de 30 s», que no nombra ninguna causa: en el ensayo
   *    del 2026-08-30 costo leer seis fallos identicos para averiguar que la fila no tenia run. Con
   *    la comprobacion delante, el fallo dice QUE falta y en un segundo.
   */

  async function abreSesionOperada(page: import('@playwright/test').Page) {
    await esperaTablasCargadas(page)
    const fila = page.getByRole('row').filter({ hasText: 'Launched' }).first()
    try {
      await fila.waitFor({ timeout: 8_000 })
    } catch {
      throw new Error(
        'docs-captures: no hay ninguna sesion `Launched` en /agentops. Las pestañas Live, ' +
          'Governance y Lifecycle solo existen con `run` (sessions/session-card.tsx:285-295), ' +
          'asi que esto es un hueco de SEMBRADO, no un selector roto.',
      )
    }
    await fila.click()
  }

  const ESCENAS_VIDEO: {
    id: string
    escena: number
    ruta: string
    // ⛔ `live` MARCA LAS QUE NO PUEDEN QUEDARSE QUIETAS. `tomar()` espera `networkidle` salvo que
    //    se le diga, y la tarjeta de sesion de `/agentops` mantiene una conexion ABIERTA —hay
    //    telemetria en vivo— sea cual sea la pestaña: la red no llega a estar ociosa NUNCA y el
    //    test muere en `page.waitForLoadState`. MEDIDO: las escenas 7 y 8 fallaron con ese error
    //    exacto en la misma corrida, y son pestañas DISTINTAS de la MISMA tarjeta — o sea que no
    //    es la pestaña «Live»: es la tarjeta.
    //
    //    El tipo de las vistas normales ya tenia esta bandera y ocho la usan; a este bucle se le
    //    olvido, asi que le pedia quietud a lo unico que el producto promete que se mueve.
    live?: boolean
    prepara: (page: import('@playwright/test').Page) => Promise<void>
  }[] = [
    {
      id: 'video-05-edge-honesty',
      escena: 5,
      ruta: '/access-map',
      prepara: async (page) => {
        await page.goto('/access-map')
        // La arista es el sujeto: si no hay ninguna, el seed no trae grafo y la celda debe
        // fallar AQUI, no sacar una foto de un lienzo vacio y llamarla escena 5.
        const arista = page.locator('.react-flow__edge').first()
        await expect(arista).toBeAttached({ timeout: 60_000 })
        // ⛔ `hover()` APUNTA AL CENTRO DEL BOUNDING BOX, y en una arista SVG CURVA ese punto no cae
        //    sobre el trazo: Playwright dice «element is visible and stable», intenta el hover, no
        //    acierta, y REINTENTA hasta agotar el plazo. Medido en el log del fallo: «45 x waiting
        //    for element to be visible and stable / retrying hover action» sobre un
        //    `<path d="M212,180 C284.5,180 ...">` de casi dos mil pixeles de alto.
        //
        //    Y por eso subir el plazo no arreglaba nada —lo probe con 120 s esta mañana—: no
        //    esperaba a que algo llegara, REINTENTABA una accion que no puede acertar. Se despacha
        //    el evento sobre el elemento, que es lo que la escena necesita para que la arista se
        //    resalte; no se simula un raton que en una captura no existe.
        await arista.dispatchEvent('mouseover')
        await arista.dispatchEvent('mouseenter')
      },
    },
    // ⛔ LA FILA SE ELIGE POR LO QUE TIENE, NO POR SU POSICION. Estas dos escenas hacian
    //    `getByRole('row').nth(1)` —la primera fila, sea cual sea— y las pestañas que buscan
    //    despues son CONDICIONALES: `an internal design note (not shipped):285-295` solo monta `live`,
    //    `governance` y `lifecycle` cuando la sesion tiene `run`; `overview` y `details` salen
    //    siempre. Si la primera fila es una sesion sin run, la pestaña NO EXISTE y el `click`
    //    agota los 30 s del test.
    //
    //    Medido en el ensayo del 2026-08-30 sobre el candidato: SEIS fallos (escenas 7, 8 y 10 en
    //    claro y oscuro) con `waiting for getByRole('tab', { name: /^live$/i })`. Descartado que
    //    fuera la etiqueta (`card.tabs.live` ES 'Live') y que fuera sembrado (`agentops` capturo
    //    `filas: 6`): la tabla estaba llena y la fila elegida no tenia run.
    //
    //    `Launched` es la columna `Origin` («A launch record links this session to Olivares»), que
    //    es exactamente la condicion que hace existir la pestaña. `Discovered` es lo contrario.

    {
      id: 'video-07-live',
      live: true,
      escena: 7,
      ruta: '/agentops (tarjeta de sesion, pestaña Live)',
      prepara: async (page) => {
        await page.goto('/agentops')
        await abreSesionOperada(page)
        await page.getByRole('tab', { name: /^live$/i }).click()
      },
    },
    {
      id: 'video-08-governance',
      live: true,
      escena: 8,
      ruta: '/agentops (tarjeta de sesion, pestaña Governance)',
      prepara: async (page) => {
        await page.goto('/agentops')
        await abreSesionOperada(page)
        await page.getByRole('tab', { name: /^governance$/i }).click()
      },
    },
    {
      id: 'video-10-workspace-browser',
      live: true,
      escena: 10,
      ruta: '/agentops (pestaña Workspaces, Browse files)',
      prepara: async (page) => {
        await page.goto('/agentops')
        await page.getByRole('tab', { name: /^workspaces$/i }).click()
        // ⛔ MISMA RAZON QUE ARRIBA: sin workspaces registrados no hay ningun «Browse files», y
        //    un `click` a ciegas agota los 30 s sin nombrar la causa. La etiqueta esta bien
        //    (`agentops/i18n/en.json` → `workspaces.open` = 'Browse files'), asi que si no hay
        //    boton es que el panel esta VACIO — sembrado, no selector.
        // Misma razon: sin esperar, `count()` mide el panel a medio cargar.
        await esperaTablasCargadas(page)
        // ⛔ `.first()` ANTES DE ESPERAR. El panel pinta UN boton POR FILA
        //    (`agentops/workspaces-panel.tsx:144-153`, `cell: ({ row }) => <Button…>`), asi que
        //    con dos workspaces el locator es AMBIGUO: `waitFor` estricto lanza, mi `catch` lo
        //    recoge y lo rebautiza «panel vacio» — una CAUSA FALSA, que es justo el pecado que
        //    esta precondicion existe para evitar. La forma de la pantalla decide el selector.
        const abrir = page
          .getByRole('button', { name: /^browse files$/i })
          .first()
        try {
          await abrir.waitFor({ timeout: 8_000 })
        } catch {
          throw new Error(
            'docs-captures: la pestaña Workspaces de /agentops no tiene ningun «Browse files». ' +
              'El panel esta vacio (agentops/workspaces-panel.tsx usa `workspaces.empty`), asi ' +
              'que es un hueco de SEMBRADO, no un selector roto.',
          )
        }
        await abrir.click()
      },
    },
  ]
  for (const theme of ['light', 'dark'] as const) {
    for (const escena of ESCENAS_VIDEO) {
      test(`capture ${escena.id} (guion escena ${escena.escena}) — ${theme}`, async ({
        page,
      }) => {
        // ⛔ AQUI PUSE `test.setTimeout(120_000)` Y LO RETIRO, porque MEDIRLO lo desmintio y el
        //    error de diagnostico vale mas que el parche. El razonamiento original era correcto de
        //    forma: la escena 5 pide `toBeAttached({ timeout: 60_000 })` y el test corre con el
        //    plazo por defecto de Playwright —30 s, porque `playwright.config.ts` no fija
        //    `timeout`—, asi que ese 60_000 era decorativo. Cierto, y AUN ASI no era la causa: con
        //    120 s la escena sigue fallando y ahora tarda dos minutos en hacerlo.
        //
        //    La causa la enseño el PNG del fallo, no el log: con la finca sembrada `/access-map`
        //    pasa a 56 origenes y 18 recursos, react-flow hace auto-fit y el grafo queda una madeja
        //    microscopica. Las aristas SE PINTAN —`/v1/m/accessmap/graph` devuelve 66— pero a ese
        //    zoom son sub-pixel y el `hover` no llega a ser accionable nunca. NO ES TIEMPO: ES
        //    TAMAÑO.
        //
        //    Y el plazo se deja por defecto a proposito: subirlo hacia que seis tomas que no pueden
        //    pasar costaran 120 s cada una en vez de 30 —medido en la re-corrida: la escena 7 paso
        //    de 30 s a 1,1 min— sin arreglar ninguna. Un plazo mas largo sobre una espera imposible
        //    solo compra un fallo mas caro.
        await page.addInitScript(
          ([tenant, th]) => {
            localStorage.setItem(
              'olivares.tenant',
              JSON.stringify({ state: { activeTenant: tenant }, version: 0 }),
            )
            localStorage.setItem(
              'olivares.lang',
              JSON.stringify({ state: { lang: 'en' }, version: 0 }),
            )
            localStorage.setItem('olivares.theme', th)
          },
          [demoTenant, theme],
        )

        await page.goto('/login')
        await page.locator('#email').fill(DEMO_EMAIL)
        await page.locator('#password').fill(DEMO_PASSWORD)
        await page.getByRole('button', { name: /^sign in$/i }).click()
        await expect(
          page.getByRole('link', { name: 'Inventory', exact: true }),
        ).toBeVisible({ timeout: 60_000 })

        await escena.prepara(page)
        await tomar(page, {
          id: escena.id,
          theme,
          ruta: escena.ruta,
          settle: 1200,
          live: escena.live,
        })
      })
    }
  }
})

// ⛔ ESTAS DOS NO PUEDEN IR EN EL BUCLE DE ARRIBA, y no es organización: es que el bucle **se
//    autentica**, y una sesión iniciada hace que `/login` redirija a la aplicación. Capturarlas desde
//    allí guardaría la pantalla equivocada — el mismo fallo que el oráculo `heading` vino a cerrar.
//
//    Son el camino que recorre un cliente NUEVO antes de ver nada más, y hasta hoy la documentación
//    pública no enseñaba ninguna de las dos.
test.describe('Docs captures — las patas sin autenticar', () => {
  test.skip(
    !demoTenant,
    'DEMO_TENANT not set — run via scripts/docs-captures.sh',
  )

  const PUBLICAS: { id: string; path: string; heading: RegExp }[] = [
    { id: 'login', path: '/login', heading: /^Sign in$/ },
    // ⛔ AQUI ESTABA `accept-invite` SIN TOKEN, y su toma era la pantalla de ERROR
    //    «This invitation link is incomplete». RETIRADA el 2026-08-29 por adjudicacion de
    //    the planner: el estado CON token exige sembrar una invitacion viva, y eso choca con
    //    step-up AAL3 (`console` ya lo declara en `empty_by_control`). Debilitar el control
    //    por una foto esta descartado, y un PNG que NUNCA podra refrescarse es una mina.
    //    La ruta se declara como AUSENCIA con su razon en el manifiesto, no se fotografia.
  ]

  for (const theme of ['light', 'dark'] as const) {
    for (const vista of PUBLICAS) {
      test(`capture ${vista.id} — ${theme}`, async ({ page }) => {
        await page.addInitScript((th) => {
          localStorage.setItem(
            'olivares.lang',
            JSON.stringify({ state: { lang: 'en' }, version: 0 }),
          )
          localStorage.setItem('olivares.theme', th)
        }, theme)

        await page.goto(vista.path)
        // ⛔ EL TESTIGO QUE ESTE BLOQUE NECESITA Y EL DE ARRIBA NO: aquí lo que puede salir mal no es
        //    que el router falle, es que la página REDIRIJA por haber sesión. `heading` lo caza —
        //    `/login` y la app tienen h1 distintos— pero la URL lo dice antes y más claro.
        expect(
          new URL(page.url()).pathname,
          `${vista.id}: la navegación acabó en otra ruta — ¿había sesión iniciada?`,
        ).toBe(vista.path)
        await tomar(page, {
          id: vista.id,
          theme,
          ruta: vista.path,
          heading: vista.heading,
        })
      })
    }
  }
})

// ⛔ EL ASISTENTE DE PRIMER ARRANQUE NO SE PUEDE FOTOGRAFIAR CON EL MOTOR DE ARRIBA, Y ESO ERA UNA
//    EXCEPCION DECLARADA, NO UN OLVIDO. `registry.capture-coverage.test.ts` la llevaba escrita con
//    su medida: *«redirige a /login sobre un estate ya instalado (medido); exige un arranque sin
//    sembrar»*. `setup.tsx:68-69` es la razon en el codigo — *«Setup is a one-time door: once an
//    admin exists, it is closed»* — y devuelve `<Navigate to="/login" />`.
//
//    ⇒ La excepcion era CORRECTA mientras el arnes tuviera un solo motor. Lo que se hace aqui no es
//    levantarla: es quitarle el motivo. `scripts/docs-captures.sh` arranca un SEGUNDO motor con su
//    propio `--data-dir` y SIN `--seed-demo`, comprueba contra `/v1/server-info` que ese motor
//    responde `setup_required: true`, y pasa su URL en `SETUP_BASE_URL`.
//
// ⛔ Y EL CONTROL POSITIVO NO ES CEREMONIA, es la unica cosa que separa esta captura de la que el
//    test declarado predijo: si el segundo motor viniera instalado, la vista redirige y guardariamos
//    LA PANTALLA DE LOGIN etiquetada como el asistente de instalacion. Por eso se comprueba la
//    ruta final Y el h1 —«First-boot setup», que no se parece a «Sign in»—, y por eso el bloque se
//    SALTA en vez de pasar cuando la variable no viene: un skip dice «no lo he mirado» y un verde
//    sin motor diria «esta bien».
// ⛔ LA RUTA SE DECLARA CON LA MISMA FORMA QUE VE EL GUARDIAN, y esto NO es ceremonia.
//    `web/src/features/registry.capture-coverage.test.ts` empareja por el literal `path: '<ruta>'`
//    en ESTE fichero — no navega, no ejecuta: lee. Estos dos bloques no salen de `VIEWS`, asi que
//    sin el literal el guardian los daria por AUSENTES y exigiria devolverlos a `SIN_CAPTURA`,
//    justo despues de haberles quitado el motivo. Medido al escribirlo: sin esta linea, la celda
//    «toda ruta registrada tiene entrada» se pone roja nombrando /setup.
const VISTA_SETUP = {
  id: 'setup',
  path: '/setup',
  heading: /^First-boot setup$/,
}

test.describe('Docs captures — el asistente de primer arranque', () => {
  const setupBase = process.env.SETUP_BASE_URL
  test.skip(
    !setupBase,
    'SETUP_BASE_URL not set — run via scripts/docs-captures.sh (needs the UNSEEDED engine)',
  )

  for (const theme of ['light', 'dark'] as const) {
    test(`capture setup — ${theme}`, async ({ page }) => {
      await page.addInitScript((th) => {
        localStorage.setItem(
          'olivares.lang',
          JSON.stringify({ state: { lang: 'en' }, version: 0 }),
        )
        localStorage.setItem('olivares.theme', th)
      }, theme)

      await page.goto(`${setupBase}${VISTA_SETUP.path}`)
      expect(
        new URL(page.url()).pathname,
        'setup: la navegacion acabo en otra ruta — ¿el segundo motor venia ya instalado?',
      ).toBe(VISTA_SETUP.path)
      await tomar(page, {
        id: VISTA_SETUP.id,
        theme,
        ruta: VISTA_SETUP.path,
        heading: VISTA_SETUP.heading,
      })
    })
  }
})

// ⛔ LA RUTA PARAMETRICA, Y POR QUE NO ESTABA. `registry.capture-coverage.test.ts` la declaraba
//    —*«parametrica: exige sembrar una sesion y navegar a su id (C10-02)»*— y anadia *«cuando
//    C10-02 aterrice, esta entrada se borra»*. Esto es ese aterrizaje para esta ruta: el id NO se
//    codifica, lo resuelve `scripts/docs-captures.sh` contra el estate sembrado
//    (`GET /v1/m/recording/sessions`) y lo pasa en `DEMO_SESSION_ID`.
//
// ⚠ Un id CODIFICADO seria PEOR que no tener captura: el dia que el sembrado cambie de ids, la
//    vista serviria su estado de «no encontrada» y la foto saldria igual de verde. Por eso se
//    resuelve en vivo, y por eso el bloque se SALTA sin la variable en vez de pasar.
//
// ⛔ AQUI ESCRIBI QUE ESTA VISTA NO PODIA LLEVAR ORACULO `heading` «porque su h1 es el NOMBRE de la
//    sesion, que depende del estate». **ES FALSO, y lo refuto el contraste con el codigo delante**:
//    `viewer-header.tsx:94-97` pasa `title={t('title')}`, no el nombre; la cadena inglesa es el
//    literal estable `Session Recording Viewer` (`session-viewer/i18n/en.json:2`); y `PageHeader`
//    lo pinta como `h1` (`components/ui/page-header.tsx:43-46`).
//
// ⭐ Y el oraculo es MAS fuerte de lo que yo suponia, no menos: ese header **solo se monta despues
//    de resolver una sesion real** (`session-viewer-page.tsx:357-365`), asi que exigir ese h1 exacto
//    es a la vez testigo de identidad de pagina Y testigo de carga. Yo habia deducido el sujeto en
//    vez de abrirlo — la misma clase que este fichero lleva media pagina documentando.
//
// ⚠ Lo que este oraculo NO prueba, dicho para que nadie lea de mas: el pathname prueba QUE SE PIDIO,
//    y el h1 prueba QUE VISTA MONTO. Ninguno de los dos prueba que el backend devolviera ESA fila.
// Misma razon que arriba: el guardian lee el literal, no la navegacion. Aqui ademas el literal
// es la forma PARAMETRICA (`$id`), que es como la nombra `route-census.json`, mientras la
// navegacion usa el id resuelto en vivo. Las dos cosas conviven a proposito.
const VISTA_VISOR = { id: 'session-viewer', path: '/session-viewer/$id' }

test.describe('Docs captures — el visor de sesion (ruta parametrica)', () => {
  const sessionId = process.env.DEMO_SESSION_ID
  test.skip(
    !sessionId || !demoTenant,
    'DEMO_SESSION_ID not set — run via scripts/docs-captures.sh (resuelve el id del estate sembrado)',
  )

  for (const theme of ['light', 'dark'] as const) {
    test(`capture session-viewer — ${theme}`, async ({ page }) => {
      await page.addInitScript(
        ([tenant, th]) => {
          localStorage.setItem(
            'olivares.tenant',
            JSON.stringify({ state: { activeTenant: tenant }, version: 0 }),
          )
          localStorage.setItem(
            'olivares.lang',
            JSON.stringify({ state: { lang: 'en' }, version: 0 }),
          )
          localStorage.setItem('olivares.theme', th)
        },
        [demoTenant, theme],
      )
      await page.goto('/login')
      await page.locator('#email').fill(DEMO_EMAIL)
      await page.locator('#password').fill(DEMO_PASSWORD)
      await page.getByRole('button', { name: /^sign in$/i }).click()
      await expect(
        page.getByRole('link', { name: 'Inventory', exact: true }),
      ).toBeVisible({
        timeout: 60_000,
      })

      const ruta = `/session-viewer/${sessionId}`
      await page.goto(ruta)
      expect(
        new URL(page.url()).pathname,
        `session-viewer: la navegacion acabo en ${new URL(page.url()).pathname} — ¿el id ya no existe?`,
      ).toBe(ruta)
      await tomar(page, {
        id: VISTA_VISOR.id,
        theme,
        ruta,
        settle: 1500,
        heading: /^Session Recording Viewer$/,
      })
    })
  }
})
