// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { readFileSync, existsSync } from 'node:fs'
import { resolve } from 'node:path'

/**
 * El trinquete del recorte, en UN sitio.
 *
 * ⛔ POR QUÉ COMPARTIDO Y NO COPIADO. La primera versión vivía dentro de la batería de finops. Al
 *    llegar la segunda feature iba a copiarla, y dos copias de un control **envejecen aparte**: la
 *    de finops ya se había arreglado dos veces —multiconjunto en vez de conjunto, e inventario
 *    derivado en vez de tecleado— y la copia habría nacido con los dos defectos.
 *
 * Se aplica por FEATURE a propósito: el censo cuenta ocho features con este defecto todavía
 * abierto, y un trinquete de árbol nacería rojo por deuda que esta PR no arregla. Un rojo que nadie
 * puede quitar hoy enseña a saltarse el gate.
 */

/** Resuelve una ruta del árbol web tanto si vitest corre desde `web/` como desde la raíz. */
export function rutaWeb(relativa: string): string {
  const candidatas = [
    resolve(process.cwd(), 'src', relativa),
    resolve(process.cwd(), 'web/src', relativa),
  ]
  const encontrada = candidatas.find((c) => existsSync(c))
  if (!encontrada)
    throw new Error(`no encuentro ${relativa} en ${candidatas.join(' ni ')}`)
  return encontrada
}

/**
 * Los métodos del cliente que devuelven `ListResponse`.
 *
 * El patrón está ATEMPERADO —no cruza a la entrada siguiente— porque una firma puede ocupar varias
 * líneas: en finops, `statements` lleva su objeto de parámetros desplegado y con un `[^\n]*` se
 * perdía. Lo cazó la cota, que exigía 7 y encontró 6.
 */
export function nombresDeLista(apiSrc: string): string[] {
  const fuera: string[] = []
  for (const m of apiSrc.matchAll(
    /^\s{2}(\w+):(?:(?!^\s{2}\w+:)[\s\S]){0,400}?http\.get<ListResponse</gm,
  )) {
    fuera.push(m[1])
  }
  // ⛔ Y la forma de MÉTODO ABREVIADO (`nombre(args) { … }`), que la primera versión no veía: un
  //    contraste construyó ese falso negativo. Sin esto, una ruta nueva escrita así no entra en el
  //    inventario y la vista que la pinte se queda fuera del trinquete.
  for (const m of apiSrc.matchAll(
    /^\s{2}(\w+)\((?:(?!^\s{2}\w+[:(]) [\s\S]){0,400}?http\.get<ListResponse</gm,
  )) {
    if (!fuera.includes(m[1])) fuera.push(m[1])
  }
  return fuera
}

/** ¿Lleva `limit:` dentro de la llamada que empieza en `desde`? Cuenta paréntesis. */
export function pideTecho(src: string, desde: number): boolean {
  let prof = 0
  let i = src.indexOf('(', desde)
  const inicio = i
  for (; i < src.length; i++) {
    if (src[i] === '(') prof++
    else if (src[i] === ')') {
      prof--
      if (prof === 0) break
    }
  }
  const cuerpo = src.slice(inicio, i + 1)
  // ⛔ `limit:` a secas no basta, y lo señaló un contraste: `{ limit: undefined }` y `{ limit: 0 }`
  //    pasaban la sonda y NO son techos —el primero no viaja y el segundo pide cero—. Se exige un
  //    valor que sea una constante nombrada o un entero positivo.
  const m = cuerpo.match(/\blimit:\s*([A-Za-z_$][\w$]*|\d+)/)
  if (!m) return false
  if (m[1] === 'undefined' || m[1] === 'null') return false
  if (/^\d+$/.test(m[1]) && Number(m[1]) <= 0) return false
  return true
}

/**
 * Rutas de lista sin techo, POR NOMBRE, saltando las que tienen razón escrita para no tenerlo.
 *
 * ⛔ LAS EXCLUSIONES LLEVAN SU MEDIDA, no una etiqueta. Hay handlers que **no paginan**: devuelven
 *    el conjunto completo drenando las páginas por dentro. Ahí pedir `limit` es decorativo y el
 *    aviso sería **inalcanzable** — un aviso que no puede aparecer no protege, sólo afirma que
 *    protege. Se excluyen nombrando el fichero del motor que lo demuestra.
 */
export function rutasSinTecho(
  apiSrc: string,
  noPaginan: Record<string, string> = {},
): { rutas: string[]; sinTecho: string[] } {
  const rutas = nombresDeLista(apiSrc)
  const sinTecho: string[] = []
  for (const nombre of rutas) {
    if (nombre in noPaginan) continue
    const i = apiSrc.indexOf(`${nombre}:`)
    const j = apiSrc.indexOf('http.get<ListResponse<', i)
    if (j < 0 || !pideTecho(apiSrc, j)) sinTecho.push(nombre)
  }
  return { rutas, sinTecho }
}

/** Consultas y avisos comparados POR FICHERO: la concatenación deja escapar un desvío compuesto. */
export function desajustesPorFichero(
  vistas: Array<{ nombre: string; src: string }>,
  listas: string[],
  cliente: string,
): { total: number; desajustes: string[] } {
  let total = 0
  const desajustes: string[] = []
  for (const v of vistas) {
    const { deLista, conAviso } = avisosPorConsulta(v.src, listas, cliente)
    total += deLista.length
    const a = JSON.stringify(cuenta(deLista))
    const b = JSON.stringify(cuenta(conAviso))
    if (a !== b) desajustes.push(`${v.nombre}: consultas ${a} vs avisos ${b}`)
  }
  return { total, desajustes }
}

/**
 * Consultas de lista y avisos, como MULTICONJUNTOS.
 *
 * ⛔ Multiconjunto y no conjunto, y esto lo encontró un contraste: dos componentes distintos
 *    pueden llamar `q` a su consulta. Con `Set`, borrar el aviso de uno dejaba el nombre del otro
 *    cubriéndolo y el mutante escapaba — una lista recortada, muda, con el gate en verde.
 */
export function avisosPorConsulta(
  vistaSrc: string,
  listas: string[],
  cliente: string,
): { deLista: string[]; conAviso: string[] } {
  const deLista: string[] = []
  const rx = new RegExp(
    `const (\\w+) = useQuery\\(\\{[\\s\\S]{0,240}?${cliente}\\.(\\w+)\\(`,
    'g',
  )
  for (const m of vistaSrc.matchAll(rx)) {
    if (listas.includes(m[2])) deLista.push(m[1])
  }
  const conAviso: string[] = []
  for (const m of vistaSrc.matchAll(
    /<ListTruncationBadge[\s\S]{0,120}?query=\{(\w+)\}/g,
  )) {
    conAviso.push(m[1])
  }
  return { deLista, conAviso }
}

/** Cuenta ocurrencias: la comparación honesta entre los dos multiconjuntos. */
export function cuenta(xs: string[]): Record<string, number> {
  return xs.reduce<Record<string, number>>(
    (a, x) => ({ ...a, [x]: (a[x] ?? 0) + 1 }),
    {},
  )
}

export function leer(relativa: string): string {
  return readFileSync(rutaWeb(relativa), 'utf8')
}

/**
 * El `label` de cada aviso interpola EL RECUENTO CARGADO, no una constante.
 *
 * ⛔ POR QUÉ ESTO Y NO UN CASO DE VISTA. Bajo el estado real —`has_more` sólo se enciende cuando el
 *    motor recorta, o sea con exactamente `limit` filas— «interpola el techo» e «interpola el
 *    recuento» dan LA MISMA CADENA, así que ningún caso de vista honesto puede distinguirlos. Lo
 *    que sí se puede fijar es la FORMA: que el número salga de la consulta y no de un literal.
 */
export function etiquetasConLiteral(vistaSrc: string): string[] {
  const malas: string[] = []
  let vistos = 0
  for (const m of vistaSrc.matchAll(
    /<ListTruncationBadge[\s\S]{0,120}?query=\{(\w+)\}[\s\S]{0,240}?label=\{([^\n]*(?:\n[^\n]*){0,3}?)\}\}?/g,
  )) {
    vistos++
    const [, variable, etiqueta] = m
    // ⛔ LA FORMA EXACTA, no «que mencione `.data`». Un contraste construyó dos escapes con la
    //    versión laxa: `n: q.data?.cursor?.length` —usa `.data` y cuenta otra cosa— y
    //    `(q.data, t(…, { n: 1000 }))`, que la menciona y la tira.
    if (!etiqueta.includes(`${variable}.data?.items?.length`))
      malas.push(`${variable}: ${etiqueta.trim().slice(0, 70)}`)
  }
  // ⛔ Y CERO COINCIDENCIAS NO ES «TODO BIEN». Si el patrón deja de casar —atributos en otro orden,
  //    otro formato—, la lista sale vacía y la casilla verde sobre una pantalla sin comprobar.
  const avisos = [...vistaSrc.matchAll(/<ListTruncationBadge\b/g)].length
  if (vistos !== avisos)
    malas.push(`la sonda vio ${vistos} etiquetas y hay ${avisos} avisos`)
  return malas
}
