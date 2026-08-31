// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Global test setup for vitest (jsdom). Registers jest-dom matchers and stubs the
// browser APIs jsdom does not implement but the UI relies on (matchMedia for the
// theme bootstrap, ResizeObserver + pointer capture + scrollIntoView for Radix).
// We assign through loose Record casts to add APIs the DOM lib types claim already
// exist (so a plain `in`/property access would not type-narrow correctly).
import '@testing-library/jest-dom/vitest'
import { afterEach, vi } from 'vitest'
import { cleanup, configure } from '@testing-library/react'

// ⛔ EL LIMITE QUE DISPARA NO ES EL QUE SE VE. `vitest.config.ts` declara
// `testTimeout: 30_000`, y eso se lee como «hay treinta segundos». No los hay para un
// `findBy*`: testing-library trae su PROPIO reloj, `asyncUtilTimeout`, que por defecto es
// **1000 ms**, y es el que decide. Los dos numeros conviven, uno esta escrito y el otro no.
//
// Medido el 2026-08-19: el job `web` de mainline-ci fallo en
// access-map-view.test.tsx con
//
//   Unable to find an element with the text: /step-up|verification|verificación/i
//
// mientras el MISMO fichero pasa 37/37 en el hub en 7,9 s. El mensaje no menciona el tiempo
// por ninguna parte, asi que se lee como «esa pantalla no pinta la ceremonia» — un defecto de
// producto — cuando lo que pasa es que el reloj se agoto en una maquina cargada: los nueve
// runners de Hetzner comparten un solo host.
//
// 5 s en vez de 1 s, y sigue muy por debajo de los 30 s del test. Lo que cuesta es que un test
// que falle DE VERDAD tarde 5 s en fallar en vez de 1; lo que ahorra es no leer un flake de
// temporizacion como una regresion de la UI, que es exactamente el intercambio que este arbol
// midio cuatro veces la misma noche con otros cuatro limites mal puestos.
//
// Afecta a los 139 ficheros de prueba que usan findBy/waitFor: el defecto era de la clase, no
// de esta pantalla.
configure({ asyncUtilTimeout: 5_000 })
// Initialize i18n once so components using useTranslation render real (English) copy.
import '@/lib/i18n'

afterEach(() => {
  cleanup()
})

const win = window as unknown as Record<string, unknown>
const proto = Element.prototype as unknown as Record<string, unknown>

if (typeof win.matchMedia !== 'function') {
  win.matchMedia = (query: string) =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }) as MediaQueryList
}

if (typeof win.ResizeObserver !== 'function') {
  win.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

// Sigma.js subclasses WebGL2RenderingContext at module-load time to feature-detect
// the GL context. jsdom has no WebGL, so the symbol is undefined and merely
// IMPORTING sigma throws a ReferenceError. We stub the constructors so the module
// loads; we still never instantiate a Sigma renderer in jsdom (no real GL) — the
// renderer's tests exercise the pure graphology builder, not the canvas.
if (typeof win.WebGL2RenderingContext !== 'function') {
  win.WebGL2RenderingContext = class {}
}
if (typeof win.WebGLRenderingContext !== 'function') {
  win.WebGLRenderingContext = class {}
}

// jsdom has no layout engine; the UI calls these as no-ops.
if (typeof proto.scrollIntoView !== 'function') proto.scrollIntoView = vi.fn()
if (typeof proto.hasPointerCapture !== 'function') {
  proto.hasPointerCapture = vi.fn(() => false)
  proto.setPointerCapture = vi.fn()
  proto.releasePointerCapture = vi.fn()
}

// CodeMirror 6 measures text by calling getClientRects() on a Range; jsdom has no
// layout engine and ships no Range.getClientRects, so CM's measure pass throws
// `textRange(...).getClientRects is not a function`. The throw fires from a queued
// requestAnimationFrame that can outlive the test that mounted the editor, surfacing
// as an unhandled error that fails the whole run. Shim the two Range measurement
// methods to benign empties (same philosophy as the layout no-ops above) so CM's
// measure is a no-op under jsdom instead of a crash. We never assert pixel layout.
const rangeProto = Range.prototype as unknown as Record<string, unknown>
if (typeof rangeProto.getClientRects !== 'function') {
  const emptyRectList = {
    length: 0,
    item: () => null,
    *[Symbol.iterator]() {},
  } as unknown as DOMRectList
  rangeProto.getClientRects = () => emptyRectList
}
if (typeof rangeProto.getBoundingClientRect !== 'function') {
  rangeProto.getBoundingClientRect = () =>
    ({
      x: 0,
      y: 0,
      width: 0,
      height: 0,
      top: 0,
      right: 0,
      bottom: 0,
      left: 0,
      toJSON: () => ({}),
    }) as DOMRect
}
