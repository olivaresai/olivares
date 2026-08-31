// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Batería de casos rojos para check-client-callers.mjs.
//
// ⛔ POR QUÉ EXISTE, y lo escribo tras encontrarle DOS defectos al trinquete en un solo día:
//
//   1. Medía el árbol del `cwd` DEL LLAMANTE, no el suyo: desde el clon compartido contestaba
//      «651 métodos y 87 huérfanos» y desde el worktree «711 y 137». Un trinquete que se equivoca
//      hacia ABAJO dice «aprieta la línea base» y la deja roja para siempre.
//   2. Era CIEGO a las llamadas partidas. El idioma de este repo rompe las cadenas largas
//      —`consoleApi\n  .rotateToken(id)`— y el patrón exigía el punto pegado, así que contaba
//      como huérfano un método con llamante completo. Cinco de los 110 lo eran.
//
//   Ninguno de los dos lo habría visto una batería que contase HALLAZGOS: el primero cambia el
//   total y el segundo también, y un total que cuadra por casualidad no dice nada. Por eso aquí
//   se afirma el CONJUNTO EXACTO de huérfanos, nunca su cardinal.
//
// ⚠ SIN PUERTA TRASERA. El trinquete se ancla a la ubicación de su propio fichero, así que la
//   batería lo COPIA dentro del árbol señuelo y lo ejecuta allí sin tocarlo. No se le añade
//   ningún parámetro de prueba: un gate con una puerta para su propia batería tiene una puerta.
//
// Ejecutar: node scripts/check-client-callers.selftest.mjs   (task lint:client-callers-selftest)
import { execFileSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const AQUI = path.dirname(fileURLToPath(import.meta.url))
const GUARDA = path.join(AQUI, 'check-client-callers.mjs')

/** Construye un árbol señuelo con el trinquete dentro y devuelve su raíz. */
function señuelo({ patchGuard } = {}) {
  const raiz = fs.mkdtempSync(path.join(os.tmpdir(), 'callers-'))
  const feat = path.join(raiz, 'web', 'src', 'features', 'demo')
  fs.mkdirSync(feat, { recursive: true })
  fs.mkdirSync(path.join(raiz, 'scripts'), { recursive: true })

  let guarda = fs.readFileSync(GUARDA, 'utf8')
  if (patchGuard) guarda = patchGuard(guarda)
  fs.writeFileSync(path.join(raiz, 'scripts', 'check-client-callers.mjs'), guarda)
  // La linea base viaja CON el senuelo. El guardia la exige y sale 2 si falta —eso es
  // deliberado, «no he podido mirar» no es un arbol limpio— asi que un fixture sin ella
  // mediria el fallo equivocado. Lleva los dos huerfanos que este mismo senuelo fabrica,
  // de modo que los casos de `--list` siguen viendo su salida y su codigo 0.
  fs.writeFileSync(
    path.join(raiz, 'scripts', 'client-callers-baseline.txt'),
    'demoApi.huerfano\ndemoApi.soloEnTest\n',
  )

  // Cuatro métodos, uno por cada forma que importa.
  fs.writeFileSync(
    path.join(feat, 'api.ts'),
    [
      'export const demoApi = {',
      '  pegado: () => http.get(`/a`),',
      '  partido: () => http.get(`/b`),',
      '  huerfano: () => http.get(`/c`),',
      '  soloEnTest: () => http.get(`/d`),',
      '}',
      '',
    ].join('\n'),
  )
  // La pantalla llama a dos: uno pegado y otro PARTIDO, que es el idioma real del repo.
  fs.writeFileSync(
    path.join(feat, 'view.tsx'),
    [
      "import { demoApi } from './api'",
      'export function View() {',
      '  demoApi.pegado()',
      '  demoApi',
      '    .partido()',
      '  return null',
      '}',
      '',
    ].join('\n'),
  )
  // Y una PRUEBA DE CONTRATO toca el cuarto: no cuenta como llamante, a propósito.
  fs.writeFileSync(
    path.join(feat, 'demo.test.ts'),
    ["import { demoApi } from './api'", 'demoApi.soloEnTest()', ''].join('\n'),
  )
  return raiz
}

function huerfanos(raiz, cwd) {
  const salida = execFileSync(
    process.execPath,
    [path.join(raiz, 'scripts', 'check-client-callers.mjs'), '--list'],
    { cwd, encoding: 'utf8' },
  )
  return new Set(
    salida
      .split('\n')
      .map((l) => l.trim())
      .filter((l) => l.startsWith('demoApi.')),
  )
}

let fallos = 0
function caso(nombre, real, esperado) {
  const a = [...real].sort().join(' ')
  const b = [...esperado].sort().join(' ')
  if (a === b) {
    console.log(`  ok    ${nombre}`)
  } else {
    fallos += 1
    console.error(`  FALLO ${nombre}\n        obtuvo: [${a}]\n        esperaba: [${b}]`)
  }
}

const ESPERADO = new Set(['demoApi.huerfano', 'demoApi.soloEnTest'])

// 1) El caso base: un llamante PEGADO y uno PARTIDO cuentan; el huérfano y el que sólo toca
//    una prueba de contrato, no.
{
  const raiz = señuelo()
  caso(
    'un llamante partido en dos líneas CUENTA como llamante',
    huerfanos(raiz, raiz),
    ESPERADO,
  )
  // 2) El anclaje: la respuesta NO puede depender del cwd desde el que se invoque.
  caso(
    'la respuesta no cambia con el cwd del llamante',
    huerfanos(raiz, os.tmpdir()),
    ESPERADO,
  )
  fs.rmSync(raiz, { recursive: true, force: true })
}

// 3) EL MUTANTE que prueba que la tolerancia al salto es LOAD-BEARING: con el punto exigido
//    pegado, `partido` reaparece como huérfano. Si esta cláusula dejara de funcionar, este caso
//    se pondría verde y lo veríamos aquí.
{
  const raiz = señuelo({
    patchGuard: (s) =>
      s.replace(
        'const pat = new RegExp(`\\\\b${obj}\\\\s*\\\\.\\\\s*${met}\\\\b`)',
        'const pat = new RegExp(`\\\\b${obj}\\\\.${met}\\\\b`)',
      ),
  })
  const real = huerfanos(raiz, raiz)
  caso(
    'MUTANTE «punto pegado» vuelve a perder el llamante partido',
    real,
    new Set([...ESPERADO, 'demoApi.partido']),
  )
  fs.rmSync(raiz, { recursive: true, force: true })
}

// 4) Y la salida 2 cuando el parser deja de reconocer la forma del cliente: no haber podido
//    mirar NUNCA es un árbol limpio.
{
  const raiz = señuelo()
  fs.writeFileSync(
    path.join(raiz, 'web', 'src', 'features', 'demo', 'api.ts'),
    'export const demoApi = new Map()\n',
  )
  let code = 0
  try {
    execFileSync(
      process.execPath,
      [path.join(raiz, 'scripts', 'check-client-callers.mjs')],
      { cwd: raiz, encoding: 'utf8', stdio: 'pipe' },
    )
  } catch (e) {
    code = e.status
  }
  if (code === 2) console.log('  ok    cero métodos reconocidos sale 2, no 0')
  else {
    fallos += 1
    console.error(`  FALLO cero métodos reconocidos salió ${code}, esperaba 2`)
  }
  fs.rmSync(raiz, { recursive: true, force: true })
}

if (fallos > 0) {
  console.error(`check-client-callers --self-test: ${fallos} caso(s) rojo(s)`)
  process.exit(1)
}
console.log('check-client-callers --self-test: OK — 4 casos')
