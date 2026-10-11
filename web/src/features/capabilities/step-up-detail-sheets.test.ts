// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// This is defense in depth. Two earlier claims of live failures (and) were
// rejected by review because those paths were not live.
//
// The emitter inventory has four families, not the two originally claimed here:
//   - 21 `requireAAL3` calls in `core/api`, converging at `middleware.go:298-300`, handled
//     in #784's branch;
//   - two writes in `modules/governance` (`breakglass.go:187`, `approvals.go:579`), already
//     handled through `usePrivilegedMutation`;
//   - `modules/deploy`'s own `requireStepUp` (`helpers.go:73-76`), reached by `handleApply`
//     and `handleRetire` (`lifecycle.go:216,443`), already handled by the console and
//   documented
//     in `deploy/definition-detail.tsx:237-243`;
//   - `core/auth` returning `ErrStepUpRequired` outside `requireAAL3` during credential
//     operations (`webauthn.go:234,307,473`), a live defect fixed in
//   `identity/privileged-login.tsx`.
//
// These five detail sheets use module routes that do not currently emit that code.
// The pattern needs protection because `isForbidden` checks only HTTP 403
// (`lib/api/errors.ts:59-61`), while step-up is recognized by code (`:71-79`). This protects
// future gates rather than claiming an operator is currently affected.
//
// This textual guard has the limits listed below. Behavioral tests in `knowledge`,
// `governance`, and `console` verify handling when the error actually arrives.
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

const SUJETOS = [
  'capabilities/server-detail.tsx',
  'catalog/entry-detail.tsx',
  'catalog/instance-detail.tsx',
  'deploy/definition-detail.tsx',
  'deploy/revisions.tsx',
]

const leer = (rel: string) =>
  sinComentarios(readFileSync(join(FEATURES, rel), 'utf8'))

const primera = (lineas: string[], aguja: string) => {
  const i = lineas.findIndex((l) => l.includes(aguja))
  return i === -1 ? Number.POSITIVE_INFINITY : i
}

/**
 * Declared limits, shared with the earlier test groups:
 * 1. Only the first decision pair in a file is compared. Each of these five files currently
 *    has one pair; the assertion below fails if a second is added.
 * 2. A condition on one line passes in either order because its positions tie.
 * 3. String literals are not stripped.
 * 4. Only these two names are recognized. `status === 403` is invisible and existed in
 *    `routine-policies-view`; the assertion below explicitly forbids that form in these five
 *   files.
 */
describe('five detail sheets offer step-up before reporting a role denial', () => {
  it('cada una conoce la ceremonia, y ANTES que el rol', () => {
    for (const rel of SUJETOS) {
      const l = leer(rel)
      const rol = primera(l, 'isForbidden')
      const ceremonia = primera(l, 'isStepUpRequired')
      expect(
        rol,
        `${rel} ya no decide el rol — ¿sigue siendo sujeto?`,
      ).not.toBe(Number.POSITIVE_INFINITY)
      expect(ceremonia, `${rel} no conoce la ceremonia`).toBeLessThan(rol)
    }
  })

  it('y la ceremonia se PINTA, no sólo se menciona', () => {
    // Sin esto, «menciona isStepUpRequired» se satisface con un booleano derivado que nadie usa.
    for (const rel of SUJETOS) {
      const src = leer(rel).join('\n')
      expect(src, `${rel} nombra la ceremonia y no la pinta`).toContain(
        '<StepUpRequiredState',
      )
      // Y con reintento: el panel promete que la acción se reanuda (i18n common:
      // privileged.stepUp.description) y el host ejecuta `retry?.()`.
      expect(src, `${rel} pinta la ceremonia sin reintento`).toContain(
        'onElevated',
      )
    }
  })

  it('⛔ y ninguna decide un 403 leyendo `status === 403` a pelo', () => {
    // La forma que ningún barrido de `isForbidden` encuentra, y que EXISTÍA en este árbol.
    const culpables = SUJETOS.filter((rel) =>
      leer(rel).some((l) => /\.status\s*===\s*403/.test(l)),
    )
    expect(culpables).toEqual([])
  })

  it('checks five files with one decision each', () => {
    // Count decisions, not only files: if a file gained a second decision, limit 1 would
    // stop covering it. This assertion makes that gap visible.
    expect(SUJETOS.length).toBe(5)
    for (const rel of SUJETOS) {
      const usos = leer(rel).filter((l) => l.includes('isForbidden')).length
      expect(usos, `${rel} tiene ${usos} decisiones de rol, no 1`).toBe(1)
    }
  })
})
