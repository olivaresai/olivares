// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-03 — the compliance feature had NO contract mirror at all, so its hand-written types could
// drift from modules/compliance without anything noticing. This file starts one, and it starts
// with the two families the console did not call and with the two shapes that are NOT what they
// look like:
//
//   · FedRAMPKSIPack is not a DepthPack, although it lives under /depth/. It carries
//     system_name, impact_level, ksis, oscal_version and authorization_package, and it does NOT
//     carry pack_type or regulation. Reusing the sibling type would have compiled and lied.
//   · AimsIssue is not a DepthIssue. AIMSIssue has no `section` and its `field` is not omitempty,
//     so it always arrives; DepthIssue is the opposite on both. Sharing the type would have made
//     a field optional that the engine always sends.
//
// Both were measured in the engine BEFORE the types were written, which is the only reason they
// are right — the names invite the opposite conclusion.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const REPO = resolve(__dirname, '../../../..')
const goSource = (rel: string) => readFileSync(resolve(REPO, rel), 'utf8')

function goStructJSONFields(src: string, name: string): string[] {
  const start = src.indexOf(`type ${name} struct {`)
  if (start < 0)
    throw new Error(`Go struct ${name} not found — the anchor moved`)
  const body = src.slice(start, src.indexOf('\n}', start))
  const out: string[] = []
  for (const line of body.split('\n')) {
    const m = line.match(/json:"([^"]+)"/)
    if (!m) continue
    const tag = m[1].split(',')[0]
    // `json:"-"` is deliberately not on the wire (CommandResult.Replayed), so it must
    // not be expected in the TS interface either.
    if (tag === '' || tag === '-') continue
    out.push(tag)
  }
  return out.sort()
}

/** Pull the declared keys out of one TS interface in types.ts. */
function tsInterfaceKeys(src: string, name: string): string[] {
  // ⛔ SIGUE EL `extends`, y no es cosmetica: `AimsPack extends DepthPackBase` deja SIETE campos
  //    fuera de su cuerpo. Comparar solo el cuerpo daria un desacuerdo permanente con la struct
  //    Go, que si los lleva — es decir, un rojo que no es el que este fichero busca.
  //    `Omit<X, 'k'>` no se resuelve aqui a proposito: si alguien lo usa, este ayudante debe
  //    fallar ruidosamente en vez de comparar de menos.
  const vistos = new Set<string>()
  const recoge = (n: string): string[] => {
    if (vistos.has(n)) throw new Error(`TS interface cycle at ${n}`)
    vistos.add(n)
    const m = new RegExp(
      `interface ${n}(?: extends ([A-Za-z0-9_]+))?\\s*\\{`,
    ).exec(src)
    if (!m) throw new Error(`TS interface ${n} not found — the anchor moved`)
    const start = m.index + m[0].length
    let depth = 1
    let i = start
    while (i < src.length && depth > 0) {
      if (src[i] === '{') depth++
      else if (src[i] === '}') depth--
      i++
    }
    const cuerpo = src.slice(start, i - 1)
    const claves: string[] = []
    for (const raw of cuerpo.split('\n')) {
      const line = raw.trim()
      if (
        !line ||
        line.startsWith('//') ||
        line.startsWith('*') ||
        line.startsWith('/*')
      )
        continue
      const km = line.match(/^([a-z_][a-z0-9_]*)\??\s*:/i)
      if (km) claves.push(km[1])
    }
    if (m[1]) claves.push(...recoge(m[1]))
    return claves
  }
  // El override de un campo heredado (AimsPack.validation) aparece dos veces: se deduplica.
  return [...new Set(recoge(name))].sort()
}

const seam = goSource('modules/compliance/depthseam.go')
const aims = goSource('modules/compliance/aimspack.go')
const types = readFileSync(resolve(__dirname, 'types.ts'), 'utf8')

describe('the compliance mirror matches the engine', () => {
  // CONTROL: types.ts cannot drift from modules/compliance without this going red.
  // FIRES IF: a field is added, removed or renamed on either side.
  it.each([
    ['fedRAMPKSIDTO', 'FedrampKsiPack'],
    ['DepthIssue', 'DepthIssue'],
  ])('%s field names agree', (goName, tsName) => {
    expect(tsInterfaceKeys(types, tsName)).toEqual(
      goStructJSONFields(seam, goName),
    )
  })

  it.each([
    ['aimsPackDTO', 'AimsPack'],
    ['AIMSIssue', 'AimsIssue'],
  ])('%s field names agree', (goName, tsName) => {
    expect(tsInterfaceKeys(types, tsName)).toEqual(
      goStructJSONFields(aims, goName),
    )
  })

  // THE NON-FIRING DIRECTION, and the reason this file exists: the two pairs above must NOT be
  // interchangeable. If they ever become equal, someone has flattened a real difference and the
  // mirror rows above would pass while the console started sending the wrong shape.
  it('the two families are NOT the same shape', () => {
    expect(goStructJSONFields(seam, 'fedRAMPKSIDTO')).not.toEqual(
      goStructJSONFields(seam, 'depthPackDTO'),
    )
    expect(goStructJSONFields(aims, 'AIMSIssue')).not.toEqual(
      goStructJSONFields(seam, 'DepthIssue'),
    )
  })
})
