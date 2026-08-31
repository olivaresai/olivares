// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// launch-states.spec.ts — las capturas que la copy de lanzamiento referencia y NO existían.
//
// ⛔ POR QUÉ HACE FALTA OTRO ARNÉS, y está escrito en `docs/launch/assets/README.md`:
//    `docs-captures.spec.ts` captura RUTAS —navega y dispara—, y estas son ESTADOS. «El diálogo de
//    creación con los campos rellenos» y «el kill-switch ENGAGED con la sesión rechazada» no son
//    sitios a los que se navegue: son sitios a los que se LLEGA, con pasos. Medido el 2026-08-17:
//    la copy referenciaba 12 rutas de imagen inexistentes en 18 referencias.
//
// ⚠ LA REGLA QUE OBEDECE la escribió `reddit-pack.md` y es correcta: «Images must be real, hosted
//   captures — not placeholders». Así que aquí una captura entra sólo cuando **sostiene lo que su
//   texto alternativo afirma**. Y por eso NO se usa el atajo evidente: `/agentops` monta el mismo
//   componente y los mismos datos que `/sessions` —sólo cambian el `<h1>`, el subtítulo y el
//   resaltado del menú—, así que una foto de `/agentops` no es una foto de «operar una sesión».
//
// ⛔ ORDEN, Y NO ES ESTILO: las tres últimas MUTAN el estate sembrado (envían una línea a una
//    sesión, activan el kill-switch). El motor se siembra una vez por corrida, así que si fueran
//    antes cambiarían lo que ven las demás. Van al final y en este orden, tal y como el README lo
//    dejó escrito.
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { expect, test } from '@playwright/test'

const demoTenant = process.env.DEMO_TENANT ?? ''
// La raíz que el test REGISTRA y luego COMPRUEBA. Una sola fuente: si divergen, el test pasa
// verde comprobando una ruta que nadie registró.
const wsRoot = process.env.WS_ROOT ?? '/workspace'
const DEMO_EMAIL = 'demo@olivares.local'
const DEMO_PASSWORD = 'olivares-demo-estate'

/** Las capturas van al ÁRBOL, no a `playwright-report`: su consumidor es la copy de
 *  `docs/launch/`, que las referencia por ruta relativa.
 *
 *  ⚠ Se resuelve desde el cwd —Playwright corre desde `web/`, igual que el arnés hermano— y NO
 *  desde `__dirname`, que en ESM no existe: el primer intento reventó ahí y Playwright lo reportó
 *  como «No tests found», que es un mensaje que NO dice lo que pasó. La comprobación de abajo evita
 *  que un cwd distinto escriba las capturas en un sitio que nadie lee. */
const DESTINO = path.resolve(process.cwd(), '..', 'docs', 'launch', 'assets')
const EVIDENCIA = path.resolve(
  process.cwd(),
  'playwright-report',
  'launch-states',
)

async function entrar(page: import('@playwright/test').Page) {
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
      localStorage.setItem('olivares.theme', 'dark')
    },
    [demoTenant],
  )
  await page.goto('/login')
  await page.locator('#email').fill(DEMO_EMAIL)
  await page.locator('#password').fill(DEMO_PASSWORD)
  await page.getByRole('button', { name: /^sign in$/i }).click()
  await expect(page.getByRole('link', { name: 'Inventory' })).toBeVisible({
    timeout: 15_000,
  })
}

/** Dispara y deja constancia de QUÉ se fotografió, no sólo de que se fotografió algo.
 *
 * ⛔ La aserción va ANTES del disparo y sobre el contenido que el estado promete. Un arnés que sólo
 *    comprueba «hay algún encabezado» publicó una foto de «Page not found» como producto — pasó en
 *    este repositorio el 2026-08-17 y es la razón de que esta función exija un testigo. */
async function tomar(
  page: import('@playwright/test').Page,
  nombre: string,
  testigo: RegExp,
) {
  if (!existsSync(DESTINO)) mkdirSync(DESTINO, { recursive: true })
  const cuerpo = (await page.locator('body').innerText()).slice(0, 20_000)
  expect(
    cuerpo,
    `${nombre}: la pantalla no muestra lo que esta captura afirma (${testigo})`,
  ).toMatch(testigo)
  expect(cuerpo, `${nombre}: se fotografió la página de ERROR`).not.toMatch(
    /page not found|something went wrong/i,
  )
  await page.screenshot({ path: path.join(DESTINO, `${nombre}.png`) })
  // La evidencia NO va junto a las capturas: `docs/launch/assets/` es material publicado y su
  // README enumera lo que hay. Un `.json` colado ahí sería un fichero que nadie declara.
  if (!existsSync(EVIDENCIA)) mkdirSync(EVIDENCIA, { recursive: true })
  writeFileSync(
    path.join(EVIDENCIA, `${nombre}.evidence.json`),
    JSON.stringify(
      {
        nombre,
        testigo: String(testigo),
        viewport: '1440x900',
        destino: DESTINO,
      },
      null,
      2,
    ) + '\n',
  )
}

test.use({ viewport: { width: 1440, height: 900 } })
test.setTimeout(120_000)
test.describe.configure({ mode: 'serial' })

test.describe('Launch-copy STATES over real seeded data', () => {
  // ── Grupo que MUTA el estate. Va al final y en este orden, como el README exige: el motor se
  //    siembra una vez por corrida, así que lanzar una sesión o activar el kill-switch cambia lo
  //    que verían las capturas anteriores.

  test.skip(
    !demoTenant,
    'DEMO_TENANT not set — run via scripts/launch-state-captures.sh',
  )

  /** ⛔ AQUÍ IBAN `agentops-governance.png` y `agentops-governance-panel.png`, Y SE RETIRARON TRAS
   *  MIRARLAS. La captura salía correcta y honesta —la tarjeta de una sesión DESCUBIERTA, con
   *  «Observe only» y el motivo escrito: «Unavailable: no process: Olivares did not launch this
   *  session»— pero **no es lo que su copy afirma**.
   *
   *  `video-demo-script.md:85` dice que la imagen muestra el panel **«Governance posture»** y
   *  enumera sus filas: *Inline PEP (tool-calls)*, *Kill switch = «Clear — no active stop»*,
   *  *Human approval*, *Recording = «On — bridged I/O anchored as signed ledger evidence»*,
   *  *Budget*. Lo que se ve es la pestaña **Details** con «Engine: Not declared · Posture: Not
   *  declared», y la tarjeta sólo ofrece dos pestañas —Overview y Details—: **una sesión
   *  descubierta no tiene pestaña de Governance**, y es correcto que no la tenga.
   *
   *  ⇒ El panel pertenece a una sesión que Olivares LANZÓ. O sea: estas dos capturas pertenecen al
   *  grupo que MUTA el estate (crear y lanzar una sesión), no al de las que sólo navegan. El README
   *  las listaba como «abrir el panel», y abrir no basta.
   *
   *  Publicarlas igualmente habría sido exactamente el placeholder que `reddit-pack.md` prohíbe:
   *  *«Images must be real, hosted captures — not placeholders»*. Son reales, y aun así no valen,
   *  porque la regla no es «que sea de verdad» sino «que sostenga lo que su texto afirma».
   */

  test('agentops-create — el diálogo de creación con los campos puestos', async ({
    page,
  }) => {
    await entrar(page)
    await page.goto('/agentops')
    await page.getByRole('button', { name: /new session/i }).click()
    // El diálogo se espera por su CONTENIDO, no por un `waitForTimeout`: un sleep fijo fotografía
    // el spinner en una caja lenta, que es exactamente lo que pasó en el primer intento de este
    // arnés — el testigo casaba con la miga de pan y la captura salió con el cargador girando.
    await expect(page.getByRole('dialog')).toBeVisible({ timeout: 15_000 })
    await page.waitForTimeout(600)
    await tomar(page, 'agentops-create', /effort|model|workspace/i)
  })

  /** El navegador de ficheros de un workspace GOBERNADO — `video-demo-script.md:87`.
   *
   * ⚠ REGISTRA UN WORKSPACE SI NO HAY NINGUNO, y por eso va antes del kill-switch y después de las
   *   que sólo navegan. El estate sembrado trae workspaces de TENANT (los del conmutador de arriba),
   *   que son otra cosa: éstos son **directorios del host** que una sesión monta, y el navegador de
   *   ficheros cuelga de ellos. Si el sembrado ya trae uno, no se toca nada.
   *
   * ⛔ La raíz que se registra es el propio árbol del repositorio, en una instancia DESECHABLE
   *   (`mktemp -d` para los datos): da un árbol de ficheros realista sin inventar contenido, y el
   *   propio producto dice que la API de ficheros **enjaula** todo acceso a esa raíz.
   */
  test('agentops-workspace — el navegador de ficheros de un workspace', async ({
    page,
  }) => {
    await entrar(page)
    await page.goto('/agentops')
    await page.getByRole('tab', { name: /^Workspaces$/i }).click()
    // ⚠ La pestaña NO pinta un encabezado propio: «Workspaces» sólo existe como rótulo de la
    //    pestaña, así que esperar un `heading` con ese nombre falla aunque el panel esté delante —
    //    medido, con la captura del fallo. Se espera por el SUBTÍTULO del panel, que además dice lo
    //    que estos workspaces son y no se confunde con los del conmutador de tenant.
    await expect(
      page.getByText(/Host directories a session works in/i),
      'la pestaña Workspaces no abrió su panel',
    ).toBeVisible({ timeout: 15_000 })

    const vacio = await page.getByText(/No workspaces registered/i).count()
    if (vacio > 0) {
      await page.getByRole('button', { name: /Register workspace/i }).click()
      await expect(page.getByRole('dialog')).toBeVisible({ timeout: 10_000 })
      const dlg = page.getByRole('dialog')
      await dlg.getByLabel(/^Name$/i).fill('olivares-repo')
      // ⛔ LA RAÍZ SE CAPTURA EN UNA CONSTANTE Y SE COMPRUEBA CONTRA ELLA. Hasta el 2026-08-18 la
      //    aserción de más abajo llevaba escrita a mano la ruta del worktree de sesión de quien
      //    escribió el test, que (1) no coincidía con lo que ESTE test registra —así que comprobaba
      //    otra cosa— y (2) es una ruta interna con un token de sesión: `lint:export` la reportó
      //    como fuga y rechazó el push. Derivarla arregla las dos: deja de mentir y deja de filtrar.
      await dlg.getByLabel(/Root path/i).fill(wsRoot)
      // ⚠ El botón del DIÁLOGO se llama «Register»; «Register workspace» es el que lo ABRE. Con el
      //    nombre largo el `click` esperó los 120 s del test con el formulario relleno delante —
      //    visto en la captura del fallo, que es más rápido que deducirlo del selector.
      // El aviso se lee EN EL MOMENTO: un toast dura segundos y para cuando falla una aserción
      // posterior ya no está, así que el motivo real se pierde y sólo queda un timeout.
      const avisos: string[] = []
      page.on('console', (m) => {
        if (m.type() === 'error') avisos.push(m.text().slice(0, 200))
      })
      await dlg.getByRole('button', { name: /^Register$/i }).click()
      const toast = await page
        .locator('[data-sonner-toast], [role="status"], [role="alert"]')
        .first()
        .innerText()
        .catch(() => '')
      await expect(page.getByRole('dialog')).toBeHidden({ timeout: 20_000 })
      // ⛔ SE COMPRUEBA QUE QUEDÓ REGISTRADO, y no que el diálogo se cerró. Cerrarse es lo que hace
      //    también cuando la llamada falla, y sin esta aserción el test agotaba sus 120 s esperando
      //    un botón «Browse files» que no iba a existir — un timeout no dice POR QUÉ. El mensaje
      //    lleva el cuerpo de la pantalla para que el motivo salga en el fallo.
      const trasRegistrar = await page.locator('body').innerText()
      expect(
        trasRegistrar,
        `el workspace no quedó registrado.\n  aviso en pantalla: ${toast || '(ninguno)'}\n` +
          `  errores de consola: ${avisos.slice(0, 3).join(' | ') || '(ninguno)'}`,
      ).not.toMatch(/No workspaces registered/i)
    }

    // ⛔ El testigo NO es «existe el botón Browse files»: es que el navegador haya CARGADO. Un
    //    botón visible sólo prueba que hay una fila; la captura afirma un árbol de ficheros.
    await page
      .getByRole('button', { name: /Browse files/i })
      .first()
      .click()
    // ⚠ El testigo NO es la palabra «Files»: el panel se titula con el NOMBRE del workspace, no con
    //    esa etiqueta —visto en la captura del fallo—. Lo que demuestra que el navegador cargó es
    //    que enseñe la RAÍZ registrada y al menos una entrada del árbol; un título no prueba que
    //    haya listado nada.
    await expect(
      page.getByText(wsRoot).first(),
      'el navegador no muestra la raíz registrada',
    ).toBeVisible({ timeout: 20_000 })
    await expect(
      page.getByText('.github').first(),
      'el navegador abrió sin listar el árbol',
    ).toBeVisible({ timeout: 20_000 })
    await page.waitForTimeout(900)
    await tomar(page, 'agentops-workspace', /olivares-repo/)
  })

  /** El kill-switch ENGAGED, que es la única del grupo que muta y **no** exige lanzar una sesión.
   *
   * ⚠ SEPARADA A PROPÓSITO de las otras tres. La nota de abajo las mete a las cuatro en el mismo
   *   saco —«exigen una sesión que Olivares haya lanzado»— y eso es cierto para el panel de
   *   gobierno, la E/S en vivo y la sesión operada; **no** para ésta: el paro de emergencia se
   *   activa a nivel de estate y no necesita que nadie haya lanzado nada.
   *
   * ⛔ Y por eso el testigo es exigente: `video-demo-script.md:86` afirma **«Kill-switch engaged —
   *   session frozen»**, que son DOS cosas. Esta captura se guarda sólo si la pantalla sostiene el
   *   paro activo; si además sostiene lo de la sesión congelada se juzga mirándola, no aquí. El
   *   arnés no puede decidir si una copy sobre-afirma: puede impedir que se publique una foto que
   *   no muestra lo que dice.
   *
   * ⚠ MUTA, y va la última del fichero. El motor se siembra una vez por corrida (`mktemp -d` en el
   *   lanzador, así que el estate es desechable), pero un paro activo cambia lo que verían las
   *   capturas anteriores si ésta fuese antes.
   */
  test('agentops-killswitch — el paro de emergencia ACTIVO', async ({
    page,
  }) => {
    await entrar(page)
    await page.goto('/killswitch')
    await expect(
      page.getByRole('heading', { name: /^Kill switch$/ }),
    ).toBeVisible({
      timeout: 15_000,
    })
    // El motivo es OBLIGATORIO — «who reads this later: the post-review and the regulator» — así
    // que se rellena con uno que explique la captura a quien la audite, no con relleno.
    const motivo = page.getByRole('textbox').first()
    await motivo.fill(
      'Launch documentation capture — synthetic demo estate, disposable data dir.',
    )
    await page.getByRole('button', { name: /^Emergency stop$/ }).click()
    // ⛔ HAY UN DIÁLOGO DE CONFIRMACIÓN, y saltárselo fue el primer defecto de este test: la captura
    //    salió mostrando «Stop the entire estate?» con Cancel/Engage — el paro NO activado, con la
    //    página de detrás diciendo todavía «No active stops».
    await expect(page.getByRole('dialog')).toBeVisible({ timeout: 10_000 })
    await page.getByRole('button', { name: /^Engage kill switch$/ }).click()
    await expect(page.getByRole('dialog')).toBeHidden({ timeout: 20_000 })

    // ⛔⛔ Y EL TESTIGO NO PUEDE SER LA PALABRA «engaged», que es como pasó la primera versión: el
    //     diálogo la contiene al DESCRIBIR lo que va a ocurrir («Re-enabling requires approval…»).
    //     Un testigo satisfecho por el texto que EXPLICA el estado, en vez de por el estado, es la
    //     misma clase de defecto que este arnés existe para no cometer — y aquí lo cometí yo.
    //
    //     La evidencia va en las DOS direcciones: la frase de sano tiene que DESAPARECER y la fila
    //     del paro persistido tiene que APARECER. Una sola de las dos la satisface una pantalla a
    //     medio cargar.
    await expect(
      page.getByText(/no active stops/i),
      'la página sigue diciendo «No active stops»: el paro no llegó a activarse',
    ).toBeHidden({ timeout: 20_000 })
    await expect(
      page.getByText(/estate-wide/i).first(),
      'no aparece la fila del paro persistido con su ámbito',
    ).toBeVisible({ timeout: 20_000 })
    await page.waitForTimeout(800)
    const cuerpoKS = await page.locator('body').innerText()
    expect(
      cuerpoKS,
      'el cuerpo aún contiene «No active stops» al disparar',
    ).not.toMatch(/no active stops/i)
    await tomar(page, 'agentops-killswitch', /estate-wide/i)
  })

  /** ⛔ EL GRUPO QUE MUTA NO ENTRA EN ESTE ARNÉS TODAVÍA, y lo que sigue es lo MEDIDO, no lo
   *  supuesto (2026-08-18).
   *
   *  Se intentó: abrir **New session**, rellenar el nombre y pulsar **Create & launch**. El diálogo
   *  se cerró y la vista volvió a la lista — y ahí se acabó lo que puedo afirmar. Tras el clic, la
   *  tabla contó **0 filas** (antes había 7), «demo-launch» no aparecía, el único aviso decía
   *  «Loaded», y una espera posterior **agotó los 120 s del test con la página cerrada**.
   *
   *  ⚠ Lo que NO sé, y por eso no lo escribo como causa: si el motor llegó a arrancar un proceso
   *  `claude` de verdad. `claude` **sí está** en el PATH de esta caja
   *  (`/home/claude/.local/bin/claude`), así que es posible; el `trap` del lanzador mató el motor al
   *  salir y no quedó ningún proceso suelto, comprobado.
   *
   *  ⇒ La consecuencia sí es clara: las cuatro capturas que faltan —el panel de gobierno de una
   *  sesión LANZADA, la E/S en vivo, la sesión operada y el kill-switch ENGAGED— **exigen una sesión
   *  que Olivares haya lanzado**, y lanzarla desde un arnés de capturas significa **arrancar un
   *  agente real**. Eso no es un paso más: es una decisión con coste y efectos secundarios, y no la
   *  tomo yo dentro de un guion que sólo debía fotografiar.
   */
})
