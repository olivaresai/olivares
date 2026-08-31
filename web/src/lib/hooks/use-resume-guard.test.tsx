// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// ⛔ EL REINTENTO NO PUEDE EJECUTAR LA ESCRITURA DE UN FORMULARIO MUERTO.
//
// El callback de reanudación no se queda en el componente: `useFailedActionReporter` lo mete en
// un store GLOBAL (`use-privileged-mutation.ts:33-46`) y el host vive junto a toda la aplicación
// (`app/providers.tsx:45-55`). Cerrar el diálogo o navegar **desmonta el formulario pero no el
// host**, así que al terminar la ceremonia se ejecutaría la acción que el operador ya abandonó.
//
// Y no es un `setState` inocuo: `useMutation` guarda un callback que llama a `observer.mutate`, y
// `MutationObserver.mutate` construye y ejecuta una `Mutation` nueva aunque haya perdido sus
// listeners — **la escritura sale**. Lo midió el contraste Codex `sol max` sobre las internals de
// query-core, no por deducción.
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useEffect, useState } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

import { useResumeGuard } from './use-resume-guard'

/** El formulario AVISA con su reintento ya envuelto; quien lo guarda es el test. */
type AlPublicar = (envuelto: () => void) => void

/**
 * ⛔ El formulario AVISA desde un EFFECT llamando a una prop; no asigna una variable de módulo
 * desde su cuerpo ni muta un objeto de props. Las dos formas anteriores las rechaza `react-hooks`
 * —«Cannot reassign variables declared outside of the component/hook» y `immutability`— y la
 * primera además hacía que TypeScript estrechara la variable a `null` en el punto de llamada.
 * Esta reproduce lo que pasa en producción —alguien externo se queda con el callback y lo invoca
 * más tarde— sin pelearse con ninguna de las dos herramientas.
 */
function Formulario({
  alReintentar,
  alPublicar,
}: {
  alReintentar: () => void
  alPublicar: AlPublicar
}) {
  const guardar = useResumeGuard()
  const envuelto = guardar(alReintentar)
  useEffect(() => {
    alPublicar(envuelto)
  }, [alPublicar, envuelto])
  return <p>formulario vivo</p>
}

/** El host sobrevive al formulario: exactamente la topología de producción. */
function Anfitrion({
  alReintentar,
  alPublicar,
}: {
  alReintentar: () => void
  alPublicar: AlPublicar
}) {
  const [montado, setMontado] = useState(true)
  return (
    <>
      {montado && (
        <Formulario alReintentar={alReintentar} alPublicar={alPublicar} />
      )}
      <button type="button" onClick={() => setMontado(false)}>
        cerrar
      </button>
    </>
  )
}

describe('useResumeGuard', () => {
  // La variable vive en el TEST y la asigna el callback: nadie escribe desde el cuerpo de un
  // componente (react-hooks lo prohíbe) ni muta un objeto de props (`immutability`).
  let envuelto: (() => void) | null = null
  const publicar: AlPublicar = (fn) => {
    envuelto = fn
  }
  beforeEach(() => {
    envuelto = null
    vi.clearAllMocks()
  })

  it('con el formulario MONTADO, el reintento corre', () => {
    const reintento = vi.fn()
    render(<Anfitrion alReintentar={reintento} alPublicar={publicar} />)

    // Ancla positiva: si el formulario no se hubiera montado, lo de abajo no diría nada.
    expect(screen.getByText('formulario vivo')).toBeInTheDocument()
    expect(envuelto).toBeTypeOf('function')

    envuelto?.()
    expect(reintento).toHaveBeenCalledTimes(1)
    expect(toast.warning).not.toHaveBeenCalled()
  })

  it('⛔ y DESMONTADO no ejecuta nada — pero lo dice', async () => {
    const user = userEvent.setup()
    const reintento = vi.fn()
    render(<Anfitrion alReintentar={reintento} alPublicar={publicar} />)
    expect(envuelto).toBeTypeOf('function')

    await user.click(screen.getByRole('button', { name: 'cerrar' }))
    expect(screen.queryByText('formulario vivo')).toBeNull()

    envuelto?.()
    // La escritura NO sale…
    expect(reintento).not.toHaveBeenCalled()
    // …y no se calla: el panel acaba de prometer «the action resumes», así que quedarse mudo
    // sería cambiar una promesa rota por otra.
    expect(toast.warning).toHaveBeenCalledTimes(1)
  })

  it('y el aviso usa una cadena traducida, no una clave cruda', async () => {
    // ⛔ Sin esto, el aviso podría pintar `privileged.stepUp.abandonedToast` literal en las siete
    //    lenguas y la celda de arriba seguiría verde: sólo cuenta llamadas, no contenido.
    const user = userEvent.setup()
    render(<Anfitrion alReintentar={vi.fn()} alPublicar={publicar} />)
    await user.click(screen.getByRole('button', { name: 'cerrar' }))
    envuelto?.()

    const [[texto]] = toast.warning.mock.calls as unknown as string[][]
    expect(texto).toBeTypeOf('string')
    expect(texto).not.toMatch(/^[\w.]+\.[\w.]+$/)
    expect(texto.length).toBeGreaterThan(10)
  })
})
