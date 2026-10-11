// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The composer's microphone, with the microphone and the recognizer faked: idle, loading the
// model, listening, transcribing, a failure, and hidden where the browser cannot or will not
// give it a microphone. The text goes to the caller for review; nothing here sends it.
import axe from 'axe-core'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createRef } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import i18n from '@/lib/i18n'
import { DictateButton } from './dictate-button'
import { joinDictation } from './join'
import './i18n'

const mic = vi.hoisted(() => ({
  canCapture: vi.fn(() => true),
  startCapture: vi.fn(),
}))
vi.mock('./capture', () => mic)

const asr = vi.hoisted(() => ({
  loadRecognizer: vi.fn(),
  transcribe: vi.fn(),
}))
vi.mock('./recognizer', () => asr)

const toasts = vi.hoisted(() => ({ error: vi.fn(), info: vi.fn() }))
vi.mock('@/components/ui/toaster', () => ({ toast: toasts }))

/** A promise the test settles by hand, to hold the component in one state. */
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}

/** The browser's answer to permissions.query, which the test can change later. */
function permission(state: PermissionState) {
  const status = { state, onchange: null as null | (() => void) }
  vi.stubGlobal('navigator', {
    ...navigator,
    permissions: { query: vi.fn().mockResolvedValue(status) },
  })
  return status
}

const audio = new Float32Array(16_000)
let capture: {
  stop: ReturnType<typeof vi.fn>
  cancel: ReturnType<typeof vi.fn>
}

beforeEach(() => {
  capture = { stop: vi.fn().mockResolvedValue(audio), cancel: vi.fn() }
  mic.canCapture.mockReturnValue(true)
  mic.startCapture.mockReset().mockResolvedValue(capture)
  asr.loadRecognizer.mockReset().mockResolvedValue(undefined)
  asr.transcribe.mockReset().mockResolvedValue(' Hello there. ')
  toasts.error.mockReset()
  toasts.info.mockReset()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

const button = () => screen.getByRole('button', { name: 'Dictate' })
const status = () => screen.getByRole('status')

describe('DictateButton', () => {
  it('is idle: a named, unpressed button with an empty live region', () => {
    render(<DictateButton onText={vi.fn()} />)
    expect(button()).toHaveAttribute('aria-pressed', 'false')
    expect(status()).toHaveTextContent('')
  })

  it('loads the model before it opens the microphone, and says so', async () => {
    const load = deferred<void>()
    asr.loadRecognizer.mockReturnValue(load.promise)
    render(<DictateButton onText={vi.fn()} />)
    await userEvent.click(button())
    expect(status()).toHaveTextContent('Loading speech recognition')
    expect(button()).toHaveAttribute('aria-busy', 'true')
    expect(mic.startCapture).not.toHaveBeenCalled()

    // A second press while loading starts nothing more.
    await userEvent.click(button())
    await act(async () => load.resolve())
    expect(mic.startCapture).toHaveBeenCalledOnce()
    expect(status()).toHaveTextContent('Listening')
  })

  it('listens until pressed again, then hands the text over unsent and gives the field the focus', async () => {
    const onText = vi.fn()
    const heard = deferred<string>()
    asr.transcribe.mockReturnValue(heard.promise)
    const field = createRef<HTMLTextAreaElement>()
    render(
      <>
        <textarea ref={field} aria-label="Message" />
        <DictateButton field={field} onText={onText} />
      </>,
    )
    await userEvent.click(button())
    expect(button()).toHaveAttribute('aria-pressed', 'true')
    expect(status()).toHaveTextContent('Listening')

    await userEvent.click(button())
    expect(status()).toHaveTextContent('Transcribing')
    expect(capture.stop).toHaveBeenCalledOnce()
    await act(async () => heard.resolve(' Hello there. '))
    expect(asr.transcribe).toHaveBeenCalledWith(audio, 'en')
    expect(onText).toHaveBeenCalledWith('Hello there.')
    expect(button()).toHaveAttribute('aria-pressed', 'false')
    expect(status()).toHaveTextContent('')
    expect(field.current).toHaveFocus()
  })

  it('tells the recognizer the language of the console', async () => {
    await act(() => i18n.changeLanguage('es'))
    try {
      render(<DictateButton onText={vi.fn()} />)
      const dictar = screen.getByRole('button', { name: 'Dictar' })
      await userEvent.click(dictar)
      await userEvent.click(dictar)
      await waitFor(() =>
        expect(asr.transcribe).toHaveBeenCalledWith(audio, 'es'),
      )
    } finally {
      await act(() => i18n.changeLanguage('en'))
    }
  })

  it('stops listening on Escape, but not on an Escape a control already used', async () => {
    const onText = vi.fn()
    render(<DictateButton onText={onText} />)
    await userEvent.click(button())

    const used = new KeyboardEvent('keydown', {
      key: 'Escape',
      bubbles: true,
      cancelable: true,
    })
    used.preventDefault()
    document.body.dispatchEvent(used)
    expect(capture.stop).not.toHaveBeenCalled()

    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(onText).toHaveBeenCalledWith('Hello there.'))
  })

  it('says nothing was heard, where everyone can see it, rather than inserting nothing', async () => {
    const onText = vi.fn()
    asr.transcribe.mockResolvedValue('   ')
    render(<DictateButton onText={onText} />)
    await userEvent.click(button())
    await userEvent.click(button())
    await waitFor(() =>
      expect(toasts.info).toHaveBeenCalledWith('No speech heard.'),
    )
    expect(onText).not.toHaveBeenCalled()
  })

  it('reports a failed load with its cause, without opening the microphone, and can be pressed again', async () => {
    asr.loadRecognizer.mockRejectedValueOnce(new Error('model 404'))
    render(<DictateButton onText={vi.fn()} />)
    await userEvent.click(button())
    await waitFor(() =>
      expect(toasts.error).toHaveBeenCalledWith(
        'Dictation failed. Try again.',
        {
          description: 'model 404',
        },
      ),
    )
    expect(mic.startCapture).not.toHaveBeenCalled()
    expect(button()).toHaveAttribute('title', 'Dictation failed. Try again.')
    expect(button()).toHaveAttribute('aria-pressed', 'false')

    await userEvent.click(button())
    expect(status()).toHaveTextContent('Listening')
  })

  it('reports a failed transcription', async () => {
    asr.transcribe.mockRejectedValue(new Error('session lost'))
    render(<DictateButton onText={vi.fn()} />)
    await userEvent.click(button())
    await userEvent.click(button())
    await waitFor(() =>
      expect(toasts.error).toHaveBeenCalledWith(
        'Dictation failed. Try again.',
        {
          description: 'session lost',
        },
      ),
    )
  })

  it('keeps the button when the microphone fails for another reason', async () => {
    mic.startCapture.mockRejectedValue(
      new DOMException('Requested device not found', 'NotFoundError'),
    )
    render(<DictateButton onText={vi.fn()} />)
    await userEvent.click(button())
    await waitFor(() => expect(toasts.error).toHaveBeenCalled())
    expect(toasts.error.mock.calls[0][0]).toBe('Dictation failed. Try again.')
    expect(button()).toBeInTheDocument()
  })

  it('frees the microphone when the composer goes away', async () => {
    const view = render(<DictateButton onText={vi.fn()} />)
    await userEvent.click(button())
    expect(status()).toHaveTextContent('Listening')
    view.unmount()
    expect(capture.cancel).toHaveBeenCalledOnce()
  })

  it('never opens the microphone when the composer goes away while the model loads', async () => {
    const load = deferred<void>()
    asr.loadRecognizer.mockReturnValue(load.promise)
    const view = render(<DictateButton onText={vi.fn()} />)
    await userEvent.click(button())
    view.unmount()
    await act(async () => load.resolve())
    expect(mic.startCapture).not.toHaveBeenCalled()
  })

  it('is hidden when the browser lacks what dictation needs', () => {
    mic.canCapture.mockReturnValue(false)
    const { container } = render(<DictateButton onText={vi.fn()} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('is hidden when the browser has blocked the microphone', async () => {
    permission('denied')
    render(<DictateButton onText={vi.fn()} />)
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: 'Dictate' })).toBeNull(),
    )
  })

  it('is shown while the browser has yet to ask', async () => {
    const asked = permission('prompt')
    render(<DictateButton onText={vi.fn()} />)
    await waitFor(() => expect(asked.onchange).not.toBeNull())
    expect(button()).toBeInTheDocument()
  })

  it('says why a refusal stopped it, and hides once the browser blocks the microphone', async () => {
    const asked = permission('prompt')
    mic.startCapture.mockRejectedValue(
      new DOMException('Permission denied', 'NotAllowedError'),
    )
    const field = createRef<HTMLTextAreaElement>()
    render(
      <>
        <textarea ref={field} aria-label="Message" />
        <DictateButton field={field} onText={vi.fn()} />
      </>,
    )
    await waitFor(() => expect(asked.onchange).not.toBeNull())
    await userEvent.click(button())
    await waitFor(() =>
      expect(toasts.error).toHaveBeenCalledWith(
        'The browser blocked the microphone.',
      ),
    )
    // A dismissed prompt can be answered again on the next press.
    expect(button()).toHaveAttribute('aria-pressed', 'false')

    button().focus()
    asked.state = 'denied'
    act(() => asked.onchange!())
    expect(screen.queryByRole('button', { name: 'Dictate' })).toBeNull()
    expect(field.current).toHaveFocus()
  })

  it('passes axe while idle, loading and listening', async () => {
    const load = deferred<void>()
    asr.loadRecognizer.mockReturnValue(load.promise)
    const { container } = render(<DictateButton onText={vi.fn()} />)
    // jsdom has no layout, so contrast is the captures' to judge.
    const check = async () =>
      (
        await axe.run(container, {
          rules: { 'color-contrast': { enabled: false } },
        })
      ).violations.map((v) => `${v.id}: ${v.help}`)
    expect(await check()).toEqual([])
    await userEvent.click(button())
    expect(await check()).toEqual([])
    await act(async () => load.resolve())
    expect(button()).toHaveAttribute('aria-pressed', 'true')
    expect(await check()).toEqual([])
  })

  it('cannot start while the composer cannot take text', () => {
    render(<DictateButton disabled onText={vi.fn()} />)
    expect(button()).toBeDisabled()
  })
})

describe('joinDictation', () => {
  it('appends to what is already written, one space apart', () => {
    expect(joinDictation('', 'Hello.')).toBe('Hello.')
    expect(joinDictation('  ', 'Hello.')).toBe('Hello.')
    expect(joinDictation('Fix the build', 'and run the tests.')).toBe(
      'Fix the build and run the tests.',
    )
    expect(joinDictation('Line one\n', 'two')).toBe('Line one\ntwo')
  })
})
