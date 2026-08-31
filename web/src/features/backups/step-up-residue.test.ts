// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// ⛔ EL CIERRE DEL RESIDUO, Y LAS CUATRO FAMILIAS DE EMISORES BIEN CONTADAS.
//
// Escribí dos veces en esta campaña que los emisores de `step_up_required` eran dos conjuntos.
// **Son cuatro**, y el contraste me lo refutó:
//   · las 21 llamadas a `requireAAL3` de `core/api` (convergen en `middleware.go:298-300`);
//   · dos ESCRITURAS en `modules/governance` (`breakglass.go:187`, `approvals.go:579`);
//   · el `requireStepUp` PROPIO de `modules/deploy` (`helpers.go:73-76`), desde `handleApply`
//     y `handleRetire`;
//   · los retornos de `core/auth/webauthn.go` (`:234`, `:307`, `:473`), FUERA de `requireAAL3`.
//
// Ninguna de las NUEVE rutas de esta celda está en esas familias hoy, así que esto es **defensa
// en profundidad** — dicho de entrada, porque presentar como «camino vivo» lo que no lo es ya me
// costó dos refutaciones.
//
// Se arregla igual porque el defecto es de FORMA: `isForbidden` es SÓLO el status 403
// (`lib/api/errors.ts:59-61`) y la ceremonia se reconoce por el CÓDIGO (`:71-79`), así que el
// brazo de rol se la tragaría entera el día que el gate llegue a cualquiera de ellas.
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const AQUI = dirname(fileURLToPath(import.meta.url))
const FEATURES = join(AQUI, '..')

/** Fuente sin comentarios, conservando los saltos para que las posiciones valgan. */
const sinComentarios = (src: string): string[] => {
  let enBloque = false
  return src.split('\n').map((linea) => {
    let l = linea
    if (enBloque) {
      const fin = l.indexOf('*/')
      if (fin === -1) return ''
      l = l.slice(fin + 2)
      enBloque = false
    }
    const ini = l.indexOf('/*')
    if (ini !== -1) {
      const fin = l.indexOf('*/', ini + 2)
      if (fin === -1) {
        enBloque = true
        l = l.slice(0, ini)
      } else {
        l = l.slice(0, ini) + l.slice(fin + 2)
      }
    }
    const sl = l.indexOf('//')
    if (sl !== -1) l = l.slice(0, sl)
    return l
  })
}

// Los NUEVE del residuo. Se amplía la lista al arreglarlos, no después: una guarda que sólo
// mira los primeros cuatro daría verde sobre los otros cinco sin decir que no los miró.
const SUJETOS = [
  'automations/workflows/workflows-tab.tsx',
  'backups/restore-dialog.tsx',
  'claude-policy/cedar-opa-view.tsx',
  'compliance/nis2-view.tsx',
  'compliance/regops-view.tsx',
  'identity/nhi-actions.tsx',
  'inference-proxy/inference-proxy-view.tsx',
  'recordings/recording-config-panel.tsx',
  'session-viewer/session-viewer-page.tsx',
]

const leer = (rel: string) => sinComentarios(readFileSync(join(FEATURES, rel), 'utf8'))
/**
 * ⛔ POR IDENTIFICADOR, NO POR SUBCADENA. Buscaba con `includes` y un mutante sobrevivió:
 * renombrar `isStepUpRequired` a `isStepUpRequiredX` —que en ejecución no existe y deja la
 * ceremonia muerta— seguía casando, porque el nombre nuevo CONTIENE al viejo. Un literal se
 * satisface con un extraño si no se acota, y aquí el extraño era el propio nombre roto.
 */
const primera = (l: string[], a: string) => {
  const re = new RegExp(`\\b${a}\\b`)
  const i = l.findIndex((x) => re.test(x))
  return i === -1 ? Number.POSITIVE_INFINITY : i
}

/**
 * TODAS las líneas con un uso real del identificador, no sólo la primera.
 *
 * ⛔ Y los usos NEGADOS no cuentan como decisión de rol: `!error.isForbidden` no acusa a nadie,
 * excluye. En una exclusión el ORDEN no significa nada —los dos 403 se descartan de la misma
 * expresión— así que exigirle una ceremonia «por delante» es pedirle algo que no tiene sentido.
 * Lo destapó `recording-config-panel:244`, donde la ceremonia está en la línea siguiente porque
 * ahí es donde va.
 */
const todas = (l: string[], a: string, incluirNegados = true) => {
  const re = new RegExp(`\\b${a}\\b`)
  return l
    .map((x, i) => {
      if (!re.test(x)) return -1
      if (!incluirNegados) {
        const sinNegados = x.replace(new RegExp(`![\\s\\w.]*${a}\\b`, 'g'), '')
        if (!re.test(sinNegados)) return -1
      }
      return i
    })
    .filter((i) => i >= 0)
}

describe('el residuo de ceremonia: la acusación nunca va sin la salida delante', () => {
  it('CADA decisión de rol tiene su ceremonia delante, no sólo la primera', () => {
    // ⛔ ESTO COMPARABA LA PRIMERA CON LA PRIMERA, y un mutante sobrevivió porque un fichero
    //    puede tener DOS decisiones independientes: `session-viewer` decide el aviso de resumen
    //    y, aparte, el error de la consulta unificada. Cambiar sólo la SEGUNDA por `isNotFound`
    //    —con lo que un `step_up_required` acaba en `ForbiddenState`— dejaba la celda verde,
    //    porque la primera aparición seguía satisfaciendo el criterio. Reproducido contra el
    //    árbol antes de tocar nada.
    //
    //    Ahora se empareja CADA rol con la ceremonia de SU bloque: la más cercana por encima,
    //    dentro de una ventana. No es un ámbito —un texto no ve un ámbito— pero distingue «esta
    //    decisión tiene salida» de «alguien, en algún sitio del fichero, la tiene».
    const ALCANCE = 15
    for (const rel of SUJETOS) {
      const l = leer(rel)
      const roles = todas(l, 'isForbidden', false)
      const ceremonias = todas(l, 'isStepUpRequired')
      expect(roles.length, `${rel} ya no decide el rol — ¿sigue siendo sujeto?`)
        .toBeGreaterThan(0)

      // ⛔ LA DISTANCIA SE MIDE EN LÍNEAS CON CONTENIDO. Al despojar comentarios quedan huecos,
      //    y una cadena bien documentada separaba su ceremonia de su rol por 16 renglones de los
      //    que DIEZ eran prosa: la celda fallaba por la documentación, no por el código. Es el
      //    mismo error que ya cometí en la ventana de proximidad, dos guardas atrás.
      const conContenido = (a: number, b: number) =>
        l.slice(a, b).filter((x) => x.trim().length > 0).length

      for (const r of roles) {
        const conSalida = ceremonias.some(
          (c) => c < r && conContenido(c, r) <= ALCANCE,
        )
        expect(
          conSalida,
          `${rel}:${r + 1} decide el rol sin ninguna ceremonia en su bloque`,
        ).toBe(true)
      }
    }
  })

  it('y cada rama HACE algo con ella: delega o la pinta', () => {
    // ⛔ Sin esto, «menciona isStepUpRequired» se satisface con una rama vacía. Y se comprueba
    //    por VECINDAD, no por fichero: en `restore-dialog` hay cinco componentes y que OTRO
    //    delegue no dice nada de éste — es el defecto de contar ficheros que ya corregí dos
    //    veces en esta campaña.
    const VENTANA = 4
    for (const rel of SUJETOS) {
      const l = leer(rel)
      const i = primera(l, 'isStepUpRequired')
      // ⛔ La ventana cuenta líneas CON CONTENIDO. Al despojar comentarios quedan huecos, y una
      //    rama documentada empujaba su propio JSX fuera de la ventana: la guarda fallaba por la
      //    documentación, no por el código. Medirlo sobre lo que queda es lo que quería decir.
      const cerca = l
        .slice(i)
        .filter((x) => x.trim().length > 0)
        .slice(0, 1 + VENTANA)
        .join('\n')
      // ⛔ TRES respuestas legítimas, y la tercera apareció al medir, no al diseñar:
      //   · DELEGAR en la política común (`report(`), que abre el panel;
      //   · PINTAR el panel (`<StepUpRequiredState`);
      //   · dar la COPY propia del step-up cuando la rama no decide una pantalla sino un
      //     mensaje — es el caso del aviso de resumen en `session-viewer`, que elige entre
      //     «no configurado», «deshabilitado» y «hace falta elevar la sesión». Ahí delegar no
      //     procede: no hay acción que reanudar, hay un texto que acertar.
      //
      // Lo que NINGUNA de las tres permite es reconocer la ceremonia y seguir como si nada,
      // que es lo que esta celda existe para impedir.
      const HACE_ALGO = /report\(|<StepUpRequiredState|privileged\.stepUp/
      if (!HACE_ALGO.test(cerca)) {
        // ⛔ CUARTA FORMA, y también salió de medir: un BOOLEANO DERIVADO
        //    (`const necesitaCeremonia = …`) cuya respuesta se pinta más abajo, donde se usa el
        //    nombre. Ninguna ventana de líneas puede verla, así que se sigue el NOMBRE. Es la
        //    misma corrección que ya hice en la guarda de las hojas de detalle; aquí faltaba, y
        //    lo dice el fallo de `workflows-tab` al ampliar la lista a los nueve.
        // La declaración puede estar VARIAS líneas por encima: un predicado con tres
        // conjunciones ocupa cuatro renglones, y mirar sólo la anterior no la encuentra —
        // fallaba justo así en `workflows-tab`. Se busca hacia atrás dentro del mismo bloque.
        let nombre: string | undefined
        for (let k = i; k >= Math.max(0, i - 6); k--) {
          const m = l[k]?.match(/const\s+(\w+)\s*=\s*$/) ?? l[k]?.match(/const\s+(\w+)\s*=/)
          if (m) {
            nombre = m[1]
            break
          }
        }
        const usadoConRespuesta =
          nombre !== undefined &&
          l.some(
            (x, j) =>
              j > i &&
              x.includes(nombre) &&
              HACE_ALGO.test(
                l
                  .slice(j)
                  .filter((y) => y.trim().length > 0)
                  .slice(0, 1 + VENTANA)
                  .join('\n'),
              ),
          )
        expect(
          usadoConRespuesta,
          `${rel}:${i + 1} reconoce la ceremonia y no hace nada con ella`,
        ).toBe(true)
      }
      // Y si delega, SALE: reportar y seguir deja caer el control en la acusación de abajo.
      if (/report\(/.test(cerca)) {
        expect(
          /\breturn\b/.test(cerca),
          `${rel}:${i + 1} delega y NO sale — el control cae en la acusación`,
        ).toBe(true)
      }
    }
  })

  it('⛔ y ninguno decide un 403 leyendo `status === 403` a pelo', () => {
    // La forma que ningún barrido de `isForbidden` encuentra, y que EXISTÍA en este árbol
    // (`routine-policies-view` la tenía antes de).
    const culpables = SUJETOS.filter((rel) =>
      leer(rel).some((l) => /\.status\s*===\s*403/.test(l)),
    )
    expect(culpables).toEqual([])
  })

  it('y el barrido MIRÓ los NUEVE, con su decisión cada uno', () => {
    // Un cero sobre cero ficheros no es un cero.
    expect(SUJETOS.length).toBe(9)
    for (const rel of SUJETOS) {
      const l = leer(rel)
      expect(l.length, `${rel} vacío`).toBeGreaterThan(50)
      expect(
        primera(l, 'isForbidden'),
        `${rel} no decide el rol`,
      ).not.toBe(Number.POSITIVE_INFINITY)
    }
  })
})
