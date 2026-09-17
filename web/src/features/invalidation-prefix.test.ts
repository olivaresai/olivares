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

// `\Z` no es un ancla en JavaScript: sin el flag `u` casa la LETRA `Z`, y con `u` no compila. Con
// `(?=^ {2}\w+:|\Z)` la alternativa «fin de fichero» no se cumplía nunca, así que el ÚLTIMO
// constructor de un `api.ts` era invisible y cualquier línea del cuerpo que empezase por `Z`
// truncaba el cuerpo antes del `params ?? null`.
//
// Fin de entrada es `(?![\s\S])`. `$` NO sirve: con el flag `m` casa también antes de cada `\n`.
const CONSTRUCTOR =
  /^ {2}(\w+): \([^)]*params\?[^)]*\) =>\n((?:.*\n){0,5}?)(?=^ {2}\w+:|(?![\s\S]))/gm

/**
 * Constructores con `params?` de UN fuente que NO devuelven un prefijo. Es la MISMA función que
 * consume el barrido del árbol; los casos del segundo `describe` la ejercitan con snippets.
 *
 * No examina, y por tanto no cubre: firmas que no caben en una línea `  nombre: (…params?…) =>`,
 * indentaciones distintas de dos espacios, un `)` dentro de la lista de parámetros, cuerpos de más
 * de cinco líneas (límite probado abajo) y otras formas inseguras (`params || null`,
 * `params ?? undefined`, `params ? x : null`). El censo medido está en el informe de la sesión.
 */
function constructoresInseguros(fuente: string): string[] {
  // El ancla sola no basta: el cuerpo se consume con `(?:.*\n){0,5}?` y una última línea sin `\n`
  // no la consume nadie, así que el terminal se escaparía. Normalizado, se ve igual con y sin él.
  const src = fuente.endsWith('\n') ? fuente : `${fuente}\n`
  const nombres: string[] = []
  for (const b of src.matchAll(CONSTRUCTOR)) {
    if (b[2].includes('params === undefined')) continue
    if (!b[2].includes('params ?? null')) continue
    nombres.push(b[1])
  }
  return nombres
}

/** Constructores con `params?` que NO devuelven un prefijo, por feature. */
function inseguros(): Map<string, Set<string>> {
  const m = new Map<string, Set<string>>()
  for (const f of ficheros(RAIZ).filter((p) => p.endsWith('/api.ts'))) {
    const feature = f.slice(RAIZ.length + 1).split('/')[0]
    for (const n of constructoresInseguros(readFileSync(f, 'utf8'))) {
      const set = m.get(feature) ?? new Set<string>()
      set.add(n)
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

// El barrido de arriba mide el ÁRBOL —«hoy no hay ninguno»— y con `\Z` también salía verde. Estos
// casos ejercitan `constructoresInseguros`, la misma función, sobre fuentes controlados: es lo que
// separa «no hay» de «no miro».

/** Un constructor con la firma que la guarda reconoce y el cuerpo que se le pase, con `\n` final. */
const bloque = (nombre: string, ...cuerpo: string[]) =>
  [`  ${nombre}: (params?: P) =>`, ...cuerpo, ''].join('\n')

const CUERPO_INSEGURO = ["    [BASE, 'x', params ?? null] as const,"]
const CUERPO_SEGURO = [
  '    params === undefined',
  "      ? ([BASE, 'x'] as const)",
  "      : ([BASE, 'x', params] as const),",
]

describe('el escáner de constructores', () => {
  it('ve un constructor inseguro TERMINAL con salto de línea final', () => {
    expect(
      constructoresInseguros(bloque('listar', ...CUERPO_INSEGURO)),
    ).toEqual(['listar'])
  })

  it('ve un constructor inseguro TERMINAL sin salto de línea final', () => {
    const sinSalto = bloque('listar', ...CUERPO_INSEGURO).replace(/\n$/, '')
    expect(sinSalto.endsWith('\n')).toBe(false)
    expect(constructoresInseguros(sinSalto)).toEqual(['listar'])
  })

  it('no marca el constructor seguro, esté o no al final del fuente', () => {
    const seguro = bloque('listar', ...CUERPO_SEGURO)
    expect(constructoresInseguros(seguro)).toEqual([])
    expect(constructoresInseguros(seguro.replace(/\n$/, ''))).toEqual([])
  })

  // El cuerpo de cada vecino se corta en la firma del siguiente: ninguno hereda el idioma del otro.
  // El segundo orden cubre además el caso terminal.
  it('juzga a dos vecinos por SU cuerpo, en cualquier orden', () => {
    const inseguroPrimero =
      bloque('listar', ...CUERPO_INSEGURO) + bloque('detalle', ...CUERPO_SEGURO)
    const seguroPrimero =
      bloque('detalle', ...CUERPO_SEGURO) + bloque('listar', ...CUERPO_INSEGURO)
    expect(
      constructoresInseguros(inseguroPrimero),
      'inseguro primero, seguro detrás',
    ).toEqual(['listar'])
    expect(
      constructoresInseguros(seguroPrimero),
      'seguro primero, inseguro TERMINAL detrás',
    ).toEqual(['listar'])
  })

  // Control causal del defecto: una línea que empieza por `Z` —aquí la continuación de un template
  // literal— satisfacía el viejo `\Z` y cortaba el cuerpo. Con `\Z` este caso devuelve `[]`.
  it('no toma una letra Z del cuerpo por un fin de fichero', () => {
    const conZ = [
      '  auditar: (params?: P) =>',
      '    [BASE, `Z',
      'Zona`, params ?? null] as const,',
      '',
    ].join('\n')
    expect(constructoresInseguros(conZ)).toEqual(['auditar'])
  })

  it('ve el cuerpo dentro del soporte real: de una a cinco líneas', () => {
    for (let n = 1; n <= 5; n++) {
      const relleno = Array.from(
        { length: n - 1 },
        (_, i) => `    // línea ${i + 1}`,
      )
      expect(
        constructoresInseguros(
          bloque('listar', ...relleno, ...CUERPO_INSEGURO),
        ),
        `cuerpo de ${n} línea(s)`,
      ).toEqual(['listar'])
    }
  })

  // Límite medido, no aceptado: la ventana del cuerpo es `{0,5}`, así que a la sexta línea el
  // constructor no se mira. Se prueba para dejar escrito el punto ciego y detectar si cambia.
  it('LÍMITE: un cuerpo de seis líneas queda fuera de la ventana del escáner', () => {
    const relleno = Array.from({ length: 5 }, (_, i) => `    // línea ${i + 1}`)
    expect(
      constructoresInseguros(bloque('listar', ...relleno, ...CUERPO_INSEGURO)),
    ).toEqual([])
  })
})
