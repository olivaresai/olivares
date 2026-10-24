// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useRef } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import '@/lib/i18n'
import { ResizeHandle, type ResizeHandleProps } from './resize-handle'

// jsdom has no layout: the pane is as wide as this variable says.
let paneWidth = 300

class ObserverAtOnce {
  private readonly callback: () => void
  constructor(callback: () => void) {
    this.callback = callback
  }
  observe() {
    this.callback()
  }
  unobserve() {}
  disconnect() {}
}

beforeEach(() => {
  paneWidth = 300
  vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(
    () => ({ width: paneWidth }) as DOMRect,
  )
  vi.stubGlobal('ResizeObserver', ObserverAtOnce)
})
afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

const BOUNDS = { min: 240, max: 560 }

function setup(over: Partial<ResizeHandleProps> = {}) {
  const calls = {
    onCommit: vi.fn(),
    onPreview: vi.fn(),
    onReset: vi.fn(),
  }
  function Harness() {
    const ref = useRef<HTMLDivElement>(null)
    return (
      <div ref={ref}>
        <ResizeHandle
          label="Resize the session list"
          target={ref}
          grow={1}
          bounds={() => BOUNDS}
          {...calls}
          {...over}
        />
      </div>
    )
  }
  render(<Harness />)
  return {
    ...calls,
    handle: screen.getByRole('separator', { name: 'Resize the session list' }),
  }
}

describe('ResizeHandle', () => {
  it('is a focusable separator that reports the width on screen and its limits', () => {
    const { handle } = setup()
    expect(handle).toHaveAttribute('aria-orientation', 'vertical')
    expect(handle).toHaveAttribute('tabindex', '0')
    expect(handle).toHaveAttribute('aria-valuenow', '300')
    expect(handle).toHaveAttribute('aria-valuemin', '240')
    expect(handle).toHaveAttribute('aria-valuemax', '560')
  })

  it('widens a right-edge handle with ArrowRight and narrows it with ArrowLeft', async () => {
    const user = userEvent.setup()
    const { onCommit } = setup()
    await user.tab()
    await user.keyboard('{ArrowRight}')
    expect(onCommit).toHaveBeenLastCalledWith(316)
    await user.keyboard('{ArrowLeft}')
    expect(onCommit).toHaveBeenLastCalledWith(284)
  })

  it('takes a larger step with Shift', async () => {
    const user = userEvent.setup()
    const { onCommit } = setup()
    await user.tab()
    await user.keyboard('{Shift>}{ArrowRight}{/Shift}')
    expect(onCommit).toHaveBeenLastCalledWith(364)
  })

  it('moves a left-edge handle the other way: ArrowLeft widens the pane', async () => {
    const user = userEvent.setup()
    const { onCommit } = setup({ grow: -1 })
    await user.tab()
    await user.keyboard('{ArrowLeft}')
    expect(onCommit).toHaveBeenLastCalledWith(316)
    await user.keyboard('{ArrowRight}')
    expect(onCommit).toHaveBeenLastCalledWith(284)
  })

  it('goes to the limits with Home and End and stops there', async () => {
    const user = userEvent.setup()
    const { onCommit } = setup()
    await user.tab()
    await user.keyboard('{Home}')
    expect(onCommit).toHaveBeenLastCalledWith(240)
    await user.keyboard('{End}')
    expect(onCommit).toHaveBeenLastCalledWith(560)
    onCommit.mockClear()
    paneWidth = 560
    await user.keyboard('{ArrowRight}')
    expect(onCommit).not.toHaveBeenCalled()
    paneWidth = 244
    await user.keyboard('{ArrowLeft}')
    expect(onCommit).toHaveBeenLastCalledWith(240)
  })

  it('leaves the browser shortcuts alone: Alt+Arrow is Back, Ctrl+Home is the page top', async () => {
    const user = userEvent.setup()
    const { onCommit } = setup()
    await user.tab()
    await user.keyboard('{Alt>}{ArrowLeft}{/Alt}')
    await user.keyboard('{Control>}{Home}{/Control}')
    await user.keyboard('{Meta>}{ArrowRight}{/Meta}')
    expect(onCommit).not.toHaveBeenCalled()
    const arrow = fireEvent.keyDown(screen.getByRole('separator'), {
      key: 'ArrowLeft',
      altKey: true,
    })
    expect(arrow).toBe(true) // not default-prevented
  })

  it('puts the pane back to its default on Enter and on double-click', async () => {
    const user = userEvent.setup()
    const { handle, onReset, onCommit } = setup()
    await user.tab()
    await user.keyboard('{Enter}')
    expect(onReset).toHaveBeenCalledTimes(1)
    await user.dblClick(handle)
    expect(onReset).toHaveBeenCalledTimes(2)
    expect(onCommit).not.toHaveBeenCalled()
  })

  it('previews a drag live and settles one width when the pointer is released', () => {
    const { handle, onPreview, onCommit } = setup()
    fireEvent.pointerDown(handle, { button: 0, clientX: 100, pointerId: 1 })
    fireEvent.pointerMove(handle, { clientX: 140, pointerId: 1 })
    expect(onPreview).toHaveBeenLastCalledWith(340)
    fireEvent.pointerMove(handle, { clientX: 160, pointerId: 1 })
    expect(onPreview).toHaveBeenLastCalledWith(360)
    expect(onCommit).not.toHaveBeenCalled()
    fireEvent.pointerUp(handle, { clientX: 160, pointerId: 1 })
    expect(onCommit).toHaveBeenCalledTimes(1)
    expect(onCommit).toHaveBeenCalledWith(360)
    // The layout takes the settled width back from the preview.
    expect(onPreview).toHaveBeenLastCalledWith(null)
  })

  it('keeps a drag inside the limits', () => {
    const { handle, onCommit } = setup()
    fireEvent.pointerDown(handle, { button: 0, clientX: 100, pointerId: 1 })
    fireEvent.pointerMove(handle, { clientX: 5000, pointerId: 1 })
    fireEvent.pointerUp(handle, { clientX: 5000, pointerId: 1 })
    expect(onCommit).toHaveBeenLastCalledWith(560)
    fireEvent.pointerDown(handle, { button: 0, clientX: 100, pointerId: 1 })
    fireEvent.pointerMove(handle, { clientX: -5000, pointerId: 1 })
    fireEvent.pointerUp(handle, { clientX: -5000, pointerId: 1 })
    expect(onCommit).toHaveBeenLastCalledWith(240)
  })

  it('widens a left-edge pane when dragged to the left', () => {
    const { handle, onCommit } = setup({ grow: -1 })
    fireEvent.pointerDown(handle, { button: 0, clientX: 500, pointerId: 1 })
    fireEvent.pointerMove(handle, { clientX: 450, pointerId: 1 })
    fireEvent.pointerUp(handle, { clientX: 450, pointerId: 1 })
    expect(onCommit).toHaveBeenCalledWith(350)
  })

  it('settles nothing when the pointer does not move or the drag is cancelled', () => {
    const { handle, onPreview, onCommit } = setup()
    fireEvent.pointerDown(handle, { button: 0, clientX: 100, pointerId: 1 })
    fireEvent.pointerUp(handle, { clientX: 100, pointerId: 1 })
    fireEvent.pointerDown(handle, { button: 0, clientX: 100, pointerId: 1 })
    fireEvent.pointerMove(handle, { clientX: 180, pointerId: 1 })
    fireEvent.pointerCancel(handle, { pointerId: 1 })
    expect(onCommit).not.toHaveBeenCalled()
    expect(onPreview).toHaveBeenLastCalledWith(null)
  })

  it('ignores a secondary button', () => {
    const { handle, onPreview } = setup()
    fireEvent.pointerDown(handle, { button: 2, clientX: 100, pointerId: 1 })
    fireEvent.pointerMove(handle, { clientX: 200, pointerId: 1 })
    expect(onPreview).not.toHaveBeenCalled()
  })

  it('reads the limits when the gesture starts, so they follow the layout', async () => {
    const user = userEvent.setup()
    let max = 560
    const { onCommit } = setup({ bounds: () => ({ min: 240, max }) })
    await user.tab()
    max = 310
    await user.keyboard('{End}')
    expect(onCommit).toHaveBeenLastCalledWith(310)
  })
})
