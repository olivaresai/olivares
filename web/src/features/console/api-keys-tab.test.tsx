// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ⛔ ESTA LISTA ES DE CREDENCIALES VIVAS, y por eso su recorte silencioso es el más caro de la
// consola: un token que no se ve aquí SIGUE AUTENTICANDO, y no hay forma de revocarlo desde esta
// pantalla. `handleListTokens` (`core/api/handlers_core.go`) usa `parseListQuery` y publica
// `has_more`; sin `limit` el store paginaba a 100 y la pantalla se leía «éstas son nuestras
// claves». Lo señaló el contraste externo de como la siguiente superficie por coste.
//
// Este fichero no existía.
import type { ReactElement } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const { api, authState } = vi.hoisted(() => ({
  api: {
    listTokens: vi.fn(),
    createToken: vi.fn(),
    revokeToken: vi.fn(),
    rotateToken: vi.fn(),
  },
  authState: {
    activeTenant: 't1' as string | null,
    isSuperadmin: true,
    principal: { aal: 3 } as { aal?: number } | null,
    can: (_p: string): boolean => true,
  },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: React.ReactNode }) => (
    <>{children}</>
  ),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: { ...actual.consoleApi, ...api } }
})

import { consoleKeys } from './api'
import { ApiKeysTab } from './api-keys-tab'
import './i18n'

const token = {
  id: 'tok-1',
  name: 'ci',
  prefix: 'olv_ci',
  superadmin: false,
  revoked: false,
  created_at: '2026-07-01T00:00:00Z',
}

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  api.listTokens.mockResolvedValue({ items: [token], has_more: false })
})

describe('ApiKeysTab — el techo se pide y el recorte se declara', () => {
  /**
   * TESTIGO DE TRANSPORTE, y aquí importa el doble: el cuerpo de `listTokens` descartaba todo
   * salvo `include_revoked`, así que añadir `limit?` al TIPO no habría bastado — el techo se
   * habría quedado en la firma sin llegar a la URL.
   */
  it('el techo llega a la llamada', async () => {
    wrap(<ApiKeysTab />)
    await waitFor(() => expect(api.listTokens).toHaveBeenCalled())
    expect(api.listTokens).toHaveBeenLastCalledWith({ limit: 1000 })
  })

  it('declara el recorte con has_more y no sin él, con la cifra cargada', async () => {
    api.listTokens.mockResolvedValue({ items: [token], has_more: true })
    wrap(<ApiKeysTab />)
    expect(
      await screen.findByText('Loaded 1 keys; there are more'),
    ).toBeVisible()
    // ⛔ CARDINALIDAD EXACTA, no presencia: dos avisos que dicen lo mismo con palabras distintas
    //    pasan cualquier `findByText`. Contraste (F-01) contó 2 aquí.
    expect(screen.getAllByText(/there are more/i)).toHaveLength(1)

    cleanup()
    api.listTokens.mockResolvedValue({ items: [token], has_more: false })
    wrap(<ApiKeysTab />)
    await screen.findByText('ci')
    expect(screen.queryByText(/Loaded \d+ keys; there are more/i)).toBeNull()
  })

  /** «No pude leer» no es «hay más»: el aviso no se queda flotando sobre un error. */
  it('un fallo de lectura no pinta el aviso', async () => {
    api.listTokens.mockRejectedValue(new Error('boom'))
    wrap(<ApiKeysTab />)
    await waitFor(() => expect(api.listTokens).toHaveBeenCalled())
    expect(screen.queryByText(/Loaded \d+ keys; there are more/i)).toBeNull()
  })

  /**
   * ⛔ Y EL CASO QUE DE VERDAD EJERCE LA GUARDA. El de arriba rechaza desde el primer intento, así
   * que `data` es `undefined` y la casilla pasa SIN llegar al `!error`; lo cazó un mutante que lo
   * quitaba y escapaba. react-query CONSERVA el último dato bueno mientras marca el error: ése es
   * el escenario real, y sin la guarda quedaría un aviso de recorte flotando sobre un fallo.
   */
  it('un refetch fallido retira el aviso aunque el dato viejo diga has_more', async () => {
    const clave = consoleKeys.tokens('t1', { limit: 1000 })
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: 0 } },
    })
    qc.setQueryData(clave, { items: [token], has_more: true })
    api.listTokens.mockRejectedValue(new Error('boom'))
    render(
      <QueryClientProvider client={qc}>
        <ApiKeysTab />
      </QueryClientProvider>,
    )
    await waitFor(() => expect(api.listTokens).toHaveBeenCalled())
    await waitFor(() => expect(qc.getQueryState(clave)?.error).toBeTruthy())
    expect(screen.queryByText(/Loaded \d+ keys; there are more/i)).toBeNull()
  })
})
