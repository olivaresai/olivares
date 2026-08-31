// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Un constructor de clave con `params?` tiene que devolver un PREFIJO cuando no se le pasan.
//
// ⛔ EL DEFECTO, VERIFICADO DE PUNTA A PUNTA. `invalidateQueries({ queryKey })` casa por PREFIJO.
// Un constructor que resuelve el argumento ausente a `null` produce una clave CONCRETA —
// `[…,'workspaces', null]`— que **no** es prefijo de la que usa la lista, `[…,'workspaces',{limit}]`.
// La invalidación no casa con nada.
//
// Medido en `agentops`: registrar un workspace devolvía **201** contra el motor, el diálogo se
// cerraba sin error —sólo se cierra en `onDone`, o sea con éxito— y la pantalla seguía diciendo
// «No workspaces registered». Ni toast, ni error de consola. Salió al intentar CAPTURAR el
// navegador de ficheros para la copy de lanzamiento: el arnés no llegaba, y no era culpa del arnés.
//
// ⛔⛔ Y LA REGLA DE LA PRIMERA VERSIÓN DE ESTA GUARDA ERA FALSA. Marcaba cualquier constructor con
// `params?` usado con un solo argumento, y eso acusaba a `console` —cuyos constructores **ya**
// devolvían el prefijo con `params === undefined`— de un defecto que no tenía. Clasificaba por la
// FIRMA en vez de por lo que el constructor DEVUELVE, que es lo único que decide si react-query
// casa. Cuatro entradas de la deuda declarada eran falsas.
//
// ⇒ Ahora la comprobación es sobre el CUERPO: `params === undefined ? prefijo : concreta`. Con esa
// regla salieron 73 constructores inseguros en 25 features y 31 invalidaciones rotas, y se
// convirtieron todos al idioma que `console/api.ts` ya usaba — un cambio por constructor deja
// correcta toda llamada presente y futura, en vez de parchear 31 llamantes.
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const RAIZ = resolve(__dirname)

/** Constructores que todavía resuelven `params` ausente a `null`. SÓLO PUEDE ENCOGER. */
const DEUDA = new Set<string>([])

function ficheros(dir: string, out: string[] = []): string[] {
  for (const n of readdirSync(dir)) {
    const p = join(dir, n)
    if (statSync(p).isDirectory()) ficheros(p, out)
    else if (/\.tsx?$/.test(n) && !n.includes('.test.')) out.push(p)
  }
  return out
}

/** Constructores con `params?` que NO devuelven un prefijo, por feature. */
function inseguros(): Map<string, Set<string>> {
  const m = new Map<string, Set<string>>()
  for (const f of ficheros(RAIZ).filter((p) => p.endsWith('/api.ts'))) {
    const feature = f.slice(RAIZ.length + 1).split('/')[0]
    const src = readFileSync(f, 'utf8')
    for (const b of src.matchAll(
      /^ {2}(\w+): \([^)]*params\?[^)]*\) =>\n((?:.*\n){0,5}?)(?=^ {2}\w+:|\Z)/gm,
    )) {
      if (b[2].includes('params === undefined')) continue
      if (!b[2].includes('params ?? null')) continue
      const set = m.get(feature) ?? new Set<string>()
      set.add(b[1])
      m.set(feature, set)
    }
  }
  return m
}

describe('claves de invalidación', () => {
  it('ningún constructor con `params?` resuelve el ausente a null', () => {
    const malos: string[] = []
    for (const [feature, nombres] of inseguros())
      for (const n of nombres) malos.push(`${feature} → ${n}`)
    const nuevos = malos.filter((x) => !DEUDA.has(x)).sort()
    expect(
      nuevos,
      'Estos constructores producen una clave CONCRETA cuando se les omiten los params, así que no ' +
        'sirven para invalidar: la mutación tiene éxito y la pantalla no se entera.\n  ' +
        nuevos.join('\n  ') +
        '\nUsa el idioma de console/api.ts: `params === undefined ? prefijo : concreta`.',
    ).toEqual([])
  })

  it('ninguna invalidación usa un constructor inseguro', () => {
    const mapa = inseguros()
    const hallados: string[] = []
    for (const f of ficheros(RAIZ)) {
      const rel = f.slice(RAIZ.length + 1)
      const nombres = mapa.get(rel.split('/')[0])
      if (!nombres?.size) continue
      const src = readFileSync(f, 'utf8')
      for (const bloque of src.matchAll(
        /invalidateKeys[^\n]*(?:\n[^\n]*){0,12}?\]/g,
      ))
        for (const n of nombres)
          if (
            new RegExp(
              `\\b\\w*Keys\\.${n}\\(\\s*[A-Za-z_$][\\w$.]*\\s*\\)`,
            ).test(bloque[0])
          )
            hallados.push(`${rel} → ${n}`)
    }
    expect([...new Set(hallados)].sort()).toEqual([])
  })

  // CONTROL QUE NO DEBE DISPARAR: la comprobación mira el CUERPO del constructor, así que el idioma
  // correcto tiene que pasar. Si esta celda se pusiera roja, la regla habría vuelto a juzgar por la
  // firma — que es exactamente el error que la primera versión cometió con `console`.
  it('el idioma correcto de console NO se marca', () => {
    const m = inseguros()
    expect(m.get('console') ?? new Set()).toEqual(new Set())
  })
})
