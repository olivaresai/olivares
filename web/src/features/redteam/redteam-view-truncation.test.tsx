// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import '@/features/_intel'
import './i18n'

/**
 * ⛔ ESTE FICHERO EXISTE POR UN ESCAPE QUE UN CONTRASTE CONSTRUYÓ, y es el límite de las sondas de
 *    fuente: `{false && <ListTruncationBadge … />}` satisface **las dos** —el trinquete lo ve
 *    escrito y atado a su consulta, y la comprobación de la etiqueta lo ve bien formado— y deja la
 *    lista recortada **sin aviso** en la pantalla. Ninguna lectura del texto puede probar que algo
 *    es ALCANZABLE: eso sólo lo prueba montarlo.
 *
 *    La batería vieja de redteam prueba componentes puros y no monta la vista, así que este hueco
 *    estaba abierto. Aquí se monta.
 */

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

const targetsMock = vi.fn()
const runsMock = vi.fn()
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return {
    ...actual,
    redteamApi: {
      ...actual.redteamApi,
      targets: (...a: unknown[]) => targetsMock(...a),
      runs: (...a: unknown[]) => runsMock(...a),
    },
  }
})

const { RedTeamView } = await import('./redteam-view')

/** Mil filas: es el ÚNICO estado con el que el motor enciende `has_more` bajo un techo de 1000. */
function mil<T>(hacer: (i: number) => T): T[] {
  return Array.from({ length: 1000 }, (_, i) => hacer(i))
}

const OBJETIVO = {
  id: 'tgt-0',
  agent_ref: 'agent-0',
  name: 'Objetivo 0',
  endpoint: 'https://agents.internal/a0',
  scope: 'input,output',
  authorized: true,
  authorized_by: 'security-lead@acme.test',
  authorized_at: '2026-06-01T10:00:00Z',
  status: 'authorized',
  created_by: 'security-lead@acme.test',
}

beforeEach(() => {
  targetsMock.mockReset().mockResolvedValue({ items: [], has_more: false })
  runsMock.mockReset().mockResolvedValue({ items: [], has_more: false })
})

describe('RedTeamView · el aviso de recorte es ALCANZABLE', () => {
  it('pinta el aviso de objetivos cuando el motor dice que hay más', async () => {
    targetsMock.mockResolvedValue({
      items: mil((i) => ({
        ...OBJETIVO,
        id: `tgt-${i}`,
        agent_ref: `agent-${i}`,
      })),
      has_more: true,
    })
    renderIntel(<RedTeamView />)
    expect(
      await screen.findByText('Loaded 1000 targets; there are more'),
    ).toBeInTheDocument()
  })

  it('CONTRAFACTUAL · sin recorte no hay aviso', async () => {
    targetsMock.mockResolvedValue({ items: [OBJETIVO], has_more: false })
    renderIntel(<RedTeamView />)
    // Cota: la pantalla pintó de verdad (si no, la ausencia del aviso no diría nada).
    expect(await screen.findByText('Objetivo 0')).toBeInTheDocument()
    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })
})
