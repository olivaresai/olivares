// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ApiError } from '@/lib/api/errors'
import type { ClaudeFilesInventory } from './types'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
  info: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

const api = vi.hoisted(() => ({
  claudeFiles: vi.fn(),
  eraseClaudeFile: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, complianceApi: { ...actual.complianceApi, ...api } }
})

import { ClaudeFilesPanel, eraseOutcome } from './claude-files-view'
import '@/features/_intel'
import './i18n'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const DISCLOSURE =
  'Anthropic Files store: workspace-scoped, SHARED across the workspace API keys, ' +
  'PERSISTENT, and NOT zero-data-retention. It carries no data-subject metadata.'

/** La costura SIN cablear: el motor manda el DTO con `wired:false` y la divulgación
 *  intacta — nunca un inventario vacío que se leería como «no hay nada que borrar». */
const unwired: ClaudeFilesInventory = {
  wired: false,
  count: 0,
  total_bytes: 0,
  disclosure: DISCLOSURE,
}

/** Cableada y con CERO ficheros. Es la otra mitad del par: si las dos pintaran igual,
 *  `wired` no sería portante y una lectura al revés no se vería desde aquí. */
const vacia: ClaudeFilesInventory = { ...unwired, wired: true }

const conFichero: ClaudeFilesInventory = {
  wired: true,
  count: 1,
  total_bytes: 2048,
  disclosure: DISCLOSURE,
  files: [
    {
      id: 'file_abc123',
      mime_type: 'application/pdf',
      size_bytes: 2048,
      created_at: '2026-08-01T10:00:00Z',
      scope_id: 'sess_9',
    },
  ],
}

beforeEach(() => {
  vi.clearAllMocks()
  api.claudeFiles.mockResolvedValue(conFichero)
  api.eraseClaudeFile.mockResolvedValue({
    status: 'deleted',
    file_id: 'file_abc123',
  })
})

describe('eraseOutcome — un no-2xx trae el veredicto, no un error', () => {
  it('lee el documento de dominio de ApiError.body', () => {
    const err = new ApiError(
      423,
      'locked',
      'locked',
      undefined,
      {},
      {
        status: 'held',
        file_id: 'file_abc123',
        holds: [{ id: 'h1', name: 'Litigio Acme' }],
      },
    )
    expect(eraseOutcome(err)?.status).toBe('held')
  })

  // Sin esto, cualquier objeto colgado de un error se leería como veredicto.
  it('no inventa un veredicto con un cuerpo que no lo es', () => {
    const err = new ApiError(500, 'boom', 'boom', undefined, {}, { oops: 1 })
    expect(eraseOutcome(err)).toBeNull()
    expect(eraseOutcome(new Error('red caída'))).toBeNull()
  })
})

describe('ClaudeFilesPanel — «sin cablear» no es «sin ficheros»', () => {
  it('pinta el aviso del MOTOR, literal', async () => {
    wrap(<ClaudeFilesPanel canAdmin canRead />)
    expect(
      await screen.findByText(/no data-subject metadata/i),
    ).toBeInTheDocument()
  })

  it('con la costura sin cablear lo dice, y NO dice que no haya ficheros', async () => {
    api.claudeFiles.mockResolvedValue(unwired)
    wrap(<ClaudeFilesPanel canAdmin canRead />)
    expect(await screen.findByText(/seam not configured/i)).toBeInTheDocument()
    expect(screen.queryByText(/no files enumerated/i)).toBeNull()
  })

  // El par: cableada y vacía dice lo CONTRARIO que sin cablear.
  it('cableada y con cero ficheros dice que no hay ficheros, no que falte configurar', async () => {
    api.claudeFiles.mockResolvedValue(vacia)
    wrap(<ClaudeFilesPanel canAdmin canRead />)
    expect(await screen.findByText(/no files enumerated/i)).toBeInTheDocument()
    expect(screen.queryByText(/seam not configured/i)).toBeNull()
  })

  it('sin permiso de lectura no llama al motor', async () => {
    wrap(<ClaudeFilesPanel canAdmin={false} canRead={false} />)
    expect(await screen.findByText(/do not have access/i)).toBeInTheDocument()
    expect(api.claudeFiles).not.toHaveBeenCalled()
  })

  it('sin permiso de admin no ofrece borrar', async () => {
    wrap(<ClaudeFilesPanel canAdmin={false} canRead />)
    await screen.findByText('file_abc123')
    expect(screen.queryByRole('button', { name: /erase/i })).toBeNull()
  })
})

describe('El borrado gobernado', () => {
  async function abrirDialogo() {
    const user = userEvent.setup()
    wrap(<ClaudeFilesPanel canAdmin canRead />)
    await screen.findByText('file_abc123')
    await user.click(screen.getByRole('button', { name: /erase/i }))
    return user
  }

  it('exige teclear el id antes de habilitar el borrado', async () => {
    const user = await abrirDialogo()
    const confirmar = await screen.findByRole('button', { name: /^erase$/i })
    expect(confirmar).toBeDisabled()
    fireEvent.change(document.querySelector('#confirm-phrase')!, {
      target: { value: 'file_abc123' },
    })
    await waitFor(() => expect(confirmar).toBeEnabled())
    await user.click(confirmar)
    await waitFor(() => expect(api.eraseClaudeFile).toHaveBeenCalledTimes(1))
  })

  it('manda el motivo cuando se escribe, y lo omite cuando no', async () => {
    const user = await abrirDialogo()
    fireEvent.change(await screen.findByLabelText(/reason/i), {
      target: { value: '  RTBF ticket 42  ' },
    })
    fireEvent.change(document.querySelector('#confirm-phrase')!, {
      target: { value: 'file_abc123' },
    })
    await user.click(screen.getByRole('button', { name: /^erase$/i }))
    await waitFor(() => expect(api.eraseClaudeFile).toHaveBeenCalledTimes(1))
    expect(api.eraseClaudeFile.mock.calls[0]).toEqual([
      'file_abc123',
      'RTBF ticket 42',
    ])
  })

  // El caso que da sentido a la pantalla: 423 NO es «ha fallado», es «hay una retención».
  it('un 423 con retención se enseña como RETENIDO, no como error', async () => {
    api.eraseClaudeFile.mockRejectedValue(
      new ApiError(
        423,
        'locked',
        'locked',
        undefined,
        {},
        {
          status: 'held',
          file_id: 'file_abc123',
          holds: [{ id: 'h1', name: 'Litigio Acme' }],
          detail: 'a legal hold covers this file',
        },
      ),
    )
    const user = await abrirDialogo()
    fireEvent.change(document.querySelector('#confirm-phrase')!, {
      target: { value: 'file_abc123' },
    })
    await user.click(screen.getByRole('button', { name: /^erase$/i }))

    expect(await screen.findByText(/held by a legal hold/i)).toBeInTheDocument()
    expect(screen.getByText('Litigio Acme')).toBeInTheDocument()
    expect(toast.error).not.toHaveBeenCalled()
    expect(toast.success).not.toHaveBeenCalled()
  })

  it('un 200 con `deleted` sí celebra', async () => {
    const user = await abrirDialogo()
    fireEvent.change(document.querySelector('#confirm-phrase')!, {
      target: { value: 'file_abc123' },
    })
    await user.click(screen.getByRole('button', { name: /^erase$/i }))
    await waitFor(() => expect(toast.success).toHaveBeenCalledTimes(1))
  })

  it('un error SIN documento de dominio sí es un error', async () => {
    api.eraseClaudeFile.mockRejectedValue(new Error('red caída'))
    const user = await abrirDialogo()
    fireEvent.change(document.querySelector('#confirm-phrase')!, {
      target: { value: 'file_abc123' },
    })
    await user.click(screen.getByRole('button', { name: /^erase$/i }))
    await waitFor(() => expect(toast.error).toHaveBeenCalledTimes(1))
  })
})
