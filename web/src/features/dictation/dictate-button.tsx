// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE COMPOSER'S MICROPHONE. Press to dictate, press again (or Esc) to stop; the words land
// in the field for the person to read and send. Recognition runs in this browser, so the
// voice reaches nobody, the session's provider included. A browser that cannot record, or
// that has blocked the microphone, shows no button at all.
import { LoaderCircle, Mic, Square } from 'lucide-react'
import { useEffect, useRef, useState, type RefObject } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { toast } from '@/components/ui/toaster'
import { cn } from '@/lib/utils'
import { canCapture, startCapture, type Capture } from './capture'
import { loadRecognizer, transcribe } from './recognizer'
import './i18n'

type Phase =
  'idle' | 'loading' | 'listening' | 'transcribing' | 'error' | 'blocked'

/** What the live region says in each phase; the outcomes are toasts, which speak too. */
const SPOKEN: Partial<Record<Phase, string>> = {
  loading: 'loading',
  listening: 'listening',
  transcribing: 'transcribing',
}

export function DictateButton({
  disabled = false,
  field,
  onText,
}: {
  disabled?: boolean
  /** The field the words go to; it takes the focus back when dictation ends or hides. */
  field?: RefObject<HTMLTextAreaElement | null>
  onText: (text: string) => void
}) {
  const { t, i18n } = useTranslation('dictation')
  const [phase, setPhase] = useState<Phase>(() =>
    canCapture() ? 'idle' : 'blocked',
  )
  const capture = useRef<Capture | null>(null)
  const mounted = useRef(true)
  const button = useRef<HTMLButtonElement>(null)

  // A microphone the browser has already blocked hides the button, and comes back with it.
  useEffect(() => {
    let status: PermissionStatus | null = null
    let live = true
    navigator.permissions?.query({ name: 'microphone' as PermissionName }).then(
      (answer) => {
        if (!live) return
        status = answer
        const apply = () => {
          if (
            answer.state === 'denied' &&
            field?.current &&
            button.current === document.activeElement
          )
            field.current.focus()
          setPhase((now) =>
            answer.state === 'denied'
              ? 'blocked'
              : now === 'blocked' && canCapture()
                ? 'idle'
                : now,
          )
        }
        apply()
        answer.onchange = apply
      },
      // Some browsers cannot be asked about the microphone; the first press asks instead.
      () => undefined,
    )
    return () => {
      live = false
      if (status) status.onchange = null
    }
  }, [field])

  // Leaving the composer drops the take and frees the microphone, even mid-press.
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
      capture.current?.cancel()
      capture.current = null
    }
  }, [])

  function fail(error: unknown) {
    setPhase('error')
    toast.error(t('failed'), {
      description: error instanceof Error ? error.message : String(error),
    })
  }

  // The model loads first, then the browser opens the microphone: it is never live while
  // the button still says it is loading.
  async function start() {
    setPhase('loading')
    let take: Capture
    try {
      await loadRecognizer()
      if (!mounted.current) return
      take = await startCapture()
    } catch (error) {
      if (!mounted.current) return
      // A refused or dismissed prompt: the button stays, and hides once the browser
      // records the microphone as blocked (the permission watch above).
      if (error instanceof DOMException && error.name === 'NotAllowedError') {
        setPhase('idle')
        toast.error(t('blocked'))
        return
      }
      return fail(error)
    }
    if (!mounted.current) return take.cancel()
    capture.current = take
    setPhase('listening')
  }

  async function stop() {
    const take = capture.current
    capture.current = null
    if (!take) return
    setPhase('transcribing')
    try {
      // The person speaks the console's language (the seven it ships are Whisper's too).
      const language = i18n.resolvedLanguage ?? 'en'
      const text = (await transcribe(await take.stop(), language)).trim()
      if (!mounted.current) return
      setPhase('idle')
      field?.current?.focus()
      if (text) onText(text)
      else toast.info(t('nothing'))
    } catch (error) {
      if (mounted.current) fail(error)
    }
  }

  // Esc stops a take from anywhere on the page, the field included, unless a control
  // already used it (a menu closing, an input method cancelling a composition).
  const stopRef = useRef(stop)
  useEffect(() => {
    stopRef.current = stop
  })
  useEffect(() => {
    if (phase !== 'listening') return
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== 'Escape' || event.defaultPrevented || event.isComposing)
        return
      event.preventDefault()
      void stopRef.current()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [phase])

  if (phase === 'blocked') return null

  const listening = phase === 'listening'
  const busy = phase === 'loading' || phase === 'transcribing'
  const spoken = SPOKEN[phase]
  return (
    <>
      <Button
        ref={button}
        type="button"
        variant={listening ? 'secondary' : 'ghost'}
        size="icon"
        // 32 px to the eye like Send beside it; the ::after ring makes the target 44 px.
        className={cn(
          "relative rounded-full after:absolute after:-inset-1.5 after:rounded-full after:content-['']",
          listening && 'text-danger',
        )}
        aria-label={t('dictate')}
        aria-pressed={listening}
        aria-busy={busy}
        title={phase === 'error' ? t('failed') : t('hint')}
        disabled={disabled && !listening}
        onClick={() => {
          if (listening) void stop()
          else if (!busy) void start()
        }}
        data-testid="composer-dictate"
      >
        {busy ? (
          <LoaderCircle className="animate-spin motion-reduce:animate-none" />
        ) : listening ? (
          <Square className="fill-current" />
        ) : (
          <Mic />
        )}
      </Button>
      <span role="status" className="sr-only">
        {spoken ? t(spoken) : ''}
      </span>
    </>
  )
}
