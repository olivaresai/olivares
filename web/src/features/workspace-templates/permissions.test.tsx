// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { TemplateDTO } from './types'
import './i18n'

/**
 * C-15 — las cinco acciones de plantilla NO comparten permiso.
 *
 * ⛔ QUÉ SE ARREGLÓ. `templates-view.tsx` importaba `useAuth` y sacaba de él ÚNICAMENTE
 *    `activeTenant`: no comprobaba un solo permiso. Un rol de sólo lectura veía crear, editar,
 *    duplicar, archivar y lanzar, y descubría que no podía por el 403 del motor — después de
 *    abrir el diálogo y rellenarlo.
 *
 * ⛔ EL REPARTO SE DERIVA DEL MOTOR, no del sentido común (`modules/sessions/templates.go:709-715`):
 *
 *      crear · editar · duplicar -> sessions:template:write
 *      archivar (DELETE)         -> sessions:template:admin
 *      LANZAR (apply)            -> sessions:template:read
 *
 *    Lanzar pide sólo LECTURA. Es contraintuitivo —lanzar una plantilla crea trabajo— y es
 *    exactamente por eso que se deriva: un reparto «razonable» habría exigido `write` para lanzar
 *    y le habría escondido el botón a quien el motor sí autoriza. Un permiso inventado en el
 *    cliente no da un error: da una consola que miente sobre lo que puedes hacer.
 *
 * ⛔ POR QUÉ UN CASO POR CAPACIDAD Y NO UNO DE «sólo lectura». Con un único caso de rol mínimo,
 *    una implementación que atara las cinco acciones al MISMO permiso pasaría igual. Cada caso
 *    concede UNA capacidad y comprueba que abre SU acción y ninguna otra.
 */

let permisos = new Set<string>()
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (p: string) => permisos.has(p),
  }),
}))

const template = (over: Partial<TemplateDTO> = {}): TemplateDTO =>
  ({
    id: 'tpl1',
    name: 'Plantilla',
    description: 'una plantilla',
    builtin: false,
    archived: false,
    ...over,
  }) as unknown as TemplateDTO

const { TemplateCard } = await import('./template-card')

function show() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <TemplateCard template={template()} onEdit={vi.fn()} onApply={vi.fn()} />
    </QueryClientProvider>,
  )
}

async function abrirMenu() {
  const botones = screen.getAllByRole('button')
  await userEvent.click(botones[botones.length - 1])
}

/** Un `DropdownMenuItem` deshabilitado lleva `aria-disabled` o `data-disabled`. */
function bloqueado(nombre: RegExp) {
  const el = screen.getByRole('menuitem', { name: nombre })
  return (
    el.getAttribute('aria-disabled') === 'true' ||
    el.hasAttribute('data-disabled') ||
    el.getAttribute('data-disabled') === ''
  )
}

beforeEach(() => {
  permisos = new Set<string>()
})

describe('C-15 · cada accion de plantilla exige SU permiso', () => {
  it('rol de SOLO LECTURA sin ningun permiso: las cuatro del menu bloqueadas', async () => {
    show()
    await abrirMenu()
    expect(bloqueado(/apply|lanzar|aplicar/i)).toBe(true)
    expect(bloqueado(/edit|editar/i)).toBe(true)
    expect(bloqueado(/duplic/i)).toBe(true)
    expect(bloqueado(/archiv/i)).toBe(true)
  })

  it('solo `read` abre LANZAR y nada mas — el motor pide read para apply', async () => {
    permisos = new Set(['sessions:template:read'])
    show()
    await abrirMenu()
    expect(bloqueado(/apply|lanzar|aplicar/i)).toBe(false)
    expect(bloqueado(/edit|editar/i)).toBe(true)
    expect(bloqueado(/duplic/i)).toBe(true)
    expect(bloqueado(/archiv/i)).toBe(true)
  })

  it('solo `write` abre EDITAR y DUPLICAR, y NO archivar ni lanzar', async () => {
    permisos = new Set(['sessions:template:write'])
    show()
    await abrirMenu()
    expect(bloqueado(/edit|editar/i)).toBe(false)
    expect(bloqueado(/duplic/i)).toBe(false)
    // archivar es admin, no write: un `write` que archivara seria una escalada silenciosa.
    expect(bloqueado(/archiv/i)).toBe(true)
    // y lanzar es READ: tener write no implica read en este modelo.
    expect(bloqueado(/apply|lanzar|aplicar/i)).toBe(true)
  })

  it('solo `admin` abre ARCHIVAR y NO las de write', async () => {
    permisos = new Set(['sessions:template:admin'])
    show()
    await abrirMenu()
    expect(bloqueado(/archiv/i)).toBe(false)
    expect(bloqueado(/edit|editar/i)).toBe(true)
    expect(bloqueado(/duplic/i)).toBe(true)
  })

  it('CONTROL: con los tres permisos NINGUNA queda bloqueada', async () => {
    // Calibra los casos de arriba: si algo bloqueara siempre —un `builtin` mal puesto, un
    // `disabled` heredado— todos pasarian sin medir el permiso.
    permisos = new Set([
      'sessions:template:read',
      'sessions:template:write',
      'sessions:template:admin',
    ])
    show()
    await abrirMenu()
    expect(bloqueado(/apply|lanzar|aplicar/i)).toBe(false)
    expect(bloqueado(/edit|editar/i)).toBe(false)
    expect(bloqueado(/duplic/i)).toBe(false)
    expect(bloqueado(/archiv/i)).toBe(false)
  })
})
