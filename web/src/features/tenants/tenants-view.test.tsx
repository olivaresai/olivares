// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-02 — el ciclo de vida de tenant, PULSADO desde la vista.
//
// ⛔ QUÉ SE FIJA Y QUÉ NO. No basta con «el botón existe»: lo que puede salir mal aquí es que el
// botón llegue a la ruta equivocada, con el estado equivocado, o para el TENANT de la fila de al
// lado. Por eso cada celda afirma los ARGUMENTOS de la llamada, no que se llamara.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

// `vi.hoisted`, porque `vi.mock` se IZA por encima de las declaraciones del módulo: sin esto el
// factory se evalúa antes que el `const` y falla con «Cannot access 'api' before initialization».
const { api, authState } = vi.hoisted(() => ({
  api: { list: vi.fn(), setStatus: vi.fn(), remove: vi.fn() },
  authState: { can: (_p: string) => true } as { can: (p: string) => boolean },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, tenantsApi: api }
})

import { TenantsView } from './tenants-view'
import { estadoTenant } from './api'

const ORGS = [
  {
    id: 'o1',
    tenant_id: 't-acme',
    name: 'Acme',
    slug: 'acme',
    status: 'active',
    data_region: 'eu',
    created_at: '',
  },
  {
    id: 'o2',
    tenant_id: 't-globex',
    name: 'Globex',
    slug: 'globex',
    status: 'suspended',
    created_at: '',
  },
]

/**
 * El tenant RESERVADO del sistema (`core/model/ids.go:29`), APARTE de `ORGS` y no dentro.
 *
 * ⛔ Estuvo dentro y rompió tres celdas que no lo habían pedido: cuentan botones sobre la lista y
 *    un tercer «Withdraw service» las volvió ambiguas. Una fixture compartida que crece rompe a
 *    quien la comparte, así que la celda que necesita este sujeto compone su propia lista.
 */
const ORG_SISTEMA = {
  id: 'o3',
  tenant_id: 'ffffffff-ffff-ffff-ffff-ffffffffffff',
  name: 'System',
  slug: 'system',
  status: 'active',
  created_at: '',
}

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = () => true
  api.list.mockResolvedValue({ items: ORGS, has_more: false })
  api.setStatus.mockResolvedValue(ORGS[0])
})

describe('estadoTenant', () => {
  /**
   * ⛔ `""` NO ES «activo», y ésta es la celda que lo fija. El OpenAPI declara el enum sólo para lo
   * que se ESCRIBE; en la respuesta `status` es `type: string` sin enum, así que un tercer valor o
   * una cadena vacía de una fila antigua llegan igual.
   *
   * EL MUTANTE: colapsar lo desconocido a `active` (`s !== 'suspended' ? 'active' : ...`). La lista
   * pintaría como sano justo el caso en que nadie sabe qué pasa — el mismo defecto que medí en
   * `estadoCentro` de finops este mismo mes.
   */
  it('un estado desconocido no se colapsa a activo', () => {
    expect(estadoTenant('active')).toBe('active')
    expect(estadoTenant('suspended')).toBe('suspended')
    expect(estadoTenant('')).toBe('unknown')
    expect(estadoTenant(undefined)).toBe('unknown')
    expect(estadoTenant('draining')).toBe('unknown')
  })
})

describe('TenantsView', () => {
  it('cada fila ofrece la acción CONTRARIA a su estado', async () => {
    wrap(<TenantsView />)
    // Acme está activo → se le puede retirar; Globex suspendido → se le puede restaurar.
    // Un índice fijo haría verde a un mutante que sólo REORDENA la lista, así que cada fila se
    // busca por su nombre.
    const acme = (await screen.findByText('Acme')).closest('div.flex')!
      .parentElement as HTMLElement
    expect(
      within(acme).getByRole('button', { name: /Withdraw service/i }),
    ).toBeInTheDocument()
    const globex = screen.getByText('Globex').closest('div.flex')!
      .parentElement as HTMLElement
    expect(
      within(globex).getByRole('button', { name: /Restore service/i }),
    ).toBeInTheDocument()
  })

  it('retirar exige teclear el SLUG y llega con el tenant_id de SU fila', async () => {
    const user = userEvent.setup()
    wrap(<TenantsView />)
    await user.click(
      await screen.findByRole('button', { name: /Withdraw service/i }),
    )
    const dialog = await screen.findByRole('dialog')
    const confirmar = within(dialog).getByRole('button', {
      name: /^Withdraw service$/i,
    })
    // Sin la frase, el confirmar NO puede dispararse: retirar el servicio a un tenant entero desde
    // una lista donde la fila de al lado es otro cliente no puede ser un clic de más.
    expect(confirmar).toBeDisabled()
    await user.type(within(dialog).getByRole('textbox'), 'acme')
    await user.click(confirmar)
    await waitFor(() => expect(api.setStatus).toHaveBeenCalledTimes(1))
    // ⛔ LOS ARGUMENTOS SON LA ASERCIÓN. `tenant_id`, no `id`: son campos distintos del mismo DTO y
    //    la ruta lleva el tenant. Y `suspended`, no lo que toque: un botón cableado al estado
    //    contrario restaura un servicio que el operador quería retirar.
    expect(api.setStatus).toHaveBeenCalledWith('t-acme', 'suspended')
  })

  it('restaurar NO pide frase, y manda active', async () => {
    const user = userEvent.setup()
    wrap(<TenantsView />)
    await user.click(
      await screen.findByRole('button', { name: /Restore service/i }),
    )
    const dialog = await screen.findByRole('dialog')
    // Restaurar no puede hacer daño; exigir la frase aquí enseñaría a teclear sin leer, y esa
    // costumbre es justo la que vacía la guarda del caso que sí importa.
    expect(within(dialog).queryByRole('textbox')).toBeNull()
    await user.click(
      within(dialog).getByRole('button', { name: /^Restore service$/i }),
    )
    await waitFor(() => expect(api.setStatus).toHaveBeenCalledTimes(1))
    expect(api.setStatus).toHaveBeenCalledWith('t-globex', 'active')
  })

  it('el diálogo de retirada DICE las tres cosas que siguen funcionando', async () => {
    const user = userEvent.setup()
    wrap(<TenantsView />)
    await user.click(
      await screen.findByRole('button', { name: /Withdraw service/i }),
    )
    const dialog = await screen.findByRole('dialog')
    // ⛔ Esto no es copy decorativa: las tres continuaciones son la diferencia entre «retirar el
    //    servicio» y «secuestrar los datos de un cliente». La EXPORTACIÓN es la que más importa y
    //    la que más fácil se cae de un rediseño, así que se nombra explícitamente.
    expect(
      within(dialog).getByText(/export of the tenant's own data/i),
    ).toBeInTheDocument()
    expect(
      within(dialog).getByText(/authentication and the operator/i),
    ).toBeInTheDocument()
    expect(within(dialog).getByText(/checkpointed/i)).toBeInTheDocument()
    expect(
      within(dialog).getByText(/does not delete anything/i),
    ).toBeInTheDocument()
  })

  // CONTROL QUE NO DEBE DISPARAR con los mutantes de arriba: sin `system:admin` no hay roster que
  // pulsar, y esta celda tiene que seguir verde cuando se rompa cualquier argumento de la llamada.
  it('sin system:admin no se lista nada', async () => {
    authState.can = () => false
    wrap(<TenantsView />)
    await waitFor(() => expect(api.list).not.toHaveBeenCalled())
    expect(
      screen.queryByRole('button', { name: /Withdraw service/i }),
    ).toBeNull()
  })
})

describe('borrado de tenant', () => {
  // ⛔ EL ARGUMENTO, NO LA LLAMADA — y aquí el riesgo es concreto: `TenantDTO` lleva `id` Y
  // `tenant_id`, y son distintos. La ruta del motor toma el `tenant_id`; pasar el `id` daría 404 en
  // el mejor caso, y en un estate donde esos espacios de identificadores se solaparan, borraría otra
  // cosa. Esta celda fija CUÁL de los dos viaja.
  it('borra con el tenant_id de la fila, no con su id', async () => {
    api.list.mockResolvedValue({ items: ORGS })
    api.remove.mockResolvedValue(undefined)
    const u = userEvent.setup()
    wrap(<TenantsView />)

    const fila = await screen.findByText('Acme')
    await u.click(
      within(fila.parentElement as HTMLElement).getByRole('button', {
        name: /delete/i,
      }),
    )
    // La frase a teclear es el SLUG: sin ella el botón de confirmar no puede actuar.
    await u.type(await screen.findByRole('textbox'), 'acme')
    await u.click(screen.getByRole('button', { name: /delete permanently/i }))

    await waitFor(() => expect(api.remove).toHaveBeenCalledTimes(1))
    expect(api.remove).toHaveBeenCalledWith('t-acme')
  })

  // ⛔ LA DIRECCIÓN QUE IMPIDE EL ACCIDENTE. Sin esta celda, la de arriba la satisface un diálogo
  // que borre al primer clic — que es exactamente el fallo que la frase a teclear existe para
  // impedir en una lista donde la fila de al lado es otro cliente.
  it('no borra hasta que se teclea el slug', async () => {
    api.list.mockResolvedValue({ items: ORGS })
    const u = userEvent.setup()
    wrap(<TenantsView />)

    const fila = await screen.findByText('Acme')
    await u.click(
      within(fila.parentElement as HTMLElement).getByRole('button', {
        name: /delete/i,
      }),
    )
    const confirmar = await screen.findByRole('button', {
      name: /delete permanently/i,
    })
    expect(confirmar).toBeDisabled()
    // Y un slug PARECIDO tampoco vale: es la fila de al lado.
    await u.type(await screen.findByRole('textbox'), 'globex')
    expect(confirmar).toBeDisabled()
    expect(api.remove).not.toHaveBeenCalled()
  })

  // El tenant reservado: el motor lo rechaza con 400, así que la consola no puede ofrecerlo. Se
  // deshabilita CON su razón en vez de esconderlo — un botón ausente enseña que la consola no sabe
  // borrar; uno deshabilitado que dice por qué se explica solo.
  it('no ofrece borrar el tenant de sistema, y dice por qué', async () => {
    api.list.mockResolvedValue({ items: [...ORGS, ORG_SISTEMA] })
    wrap(<TenantsView />)

    const fila = await screen.findByText('System')
    const boton = within(fila.parentElement as HTMLElement).getByRole(
      'button',
      {
        name: /delete/i,
      },
    )
    expect(boton).toBeDisabled()
    expect(boton).toHaveAttribute(
      'title',
      'The system tenant cannot be deleted',
    )
    // Y el de un tenant normal NO está deshabilitado, o lo de arriba lo satisface una vista que
    // deshabilite el borrado entero.
    const otra = await screen.findByText('Acme')
    expect(
      within(otra.parentElement as HTMLElement).getByRole('button', {
        name: /delete/i,
      }),
    ).toBeEnabled()
  })

  // ⛔ LA HONESTIDAD DE LA COPY, FIJADA. La ficha OpenAPI de la ruta dice «after the cloud grace
  // period», y esa gracia es del plano CLOUD: este motor purga al confirmar. Si alguien «simplifica»
  // el diálogo quitando esa frase, un operador creerá que tiene 30 días para arrepentirse.
  it('dice que el periodo de gracia no es de este motor y ofrece la puerta segura', async () => {
    api.list.mockResolvedValue({ items: ORGS })
    const u = userEvent.setup()
    wrap(<TenantsView />)

    const fila = await screen.findByText('Acme')
    await u.click(
      within(fila.parentElement as HTMLElement).getByRole('button', {
        name: /delete/i,
      }),
    )
    const dialogo = await screen.findByRole('dialog')
    expect(dialogo).toHaveTextContent(
      /cloud control plane, not to this engine/i,
    )
    expect(dialogo).toHaveTextContent(/suspend instead/i)
  })
})
