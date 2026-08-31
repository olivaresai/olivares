// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Honest-by-design inline notices. These encode the product's truth-telling rules so
// every view applies them the same way: a `truncated` aggregate is flagged as
// partial (never a fake exact total); a privileged read says it is self-audited; a
// coverage caveat / not-yet-wired seam is shown, not hidden; a compliance disclaimer
// is always rendered. They are quiet by default — a hairline strip, not an alarm.
import type { ReactNode } from 'react'
import { Eye, Info, Plug, TriangleAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import { disclaimerKey } from './disclaimers'
// The `intel` namespace travels with the modules that translate: these are deep-
// imported across features (`@/features/_intel/notices`), where the barrel — and so
// the registration — is never in the chunk.
import './i18n'

type Tone = 'info' | 'warning' | 'neutral'

const TONE_STYLES: Record<Tone, string> = {
  info: 'border-info-line bg-info-soft text-info',
  warning: 'border-warning-line bg-warning-soft text-warning',
  neutral: 'border-border bg-muted text-muted-foreground',
}

/** The generic inline strip every specific notice composes. */
export function IntelNotice({
  tone = 'neutral',
  icon,
  children,
  className,
}: {
  tone?: Tone
  icon?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <div
      className={cn(
        'flex items-start gap-2 rounded-md border px-3 py-2 text-xs leading-relaxed',
        TONE_STYLES[tone],
        className,
      )}
    >
      {/* Notice icons are decorative — the text body conveys the message — so hide them
          from the a11y tree to avoid a redundant screen-reader announcement (WCAG 1.1.1). */}
      {icon ? (
        <span className="mt-px [&_svg]:size-3.5" aria-hidden>
          {icon}
        </span>
      ) : null}
      <div className="min-w-0 text-foreground">{children}</div>
    </div>
  )
}

/**
 * El aviso de LISTA recortada — el hermano de `TruncatedNotice`, y no el mismo: aquél habla de un
 * AGREGADO que alcanzó el tope de escaneo; éste, de una LISTA de filas que el motor devolvió
 * incompleta (`has_more`).
 *
 * ⛔ LA REGLA VIVE AQUÍ, EN UN SOLO SITIO, y ése es el motivo de que exista el componente. La
 * condición es `has_more && !error`, y las dos mitades cuestan:
 *
 *   · sin `has_more` el aviso sale siempre y declara un recorte que no existe;
 *   · sin `!error` se queda flotando sobre una pantalla que ya sólo enseña el fallo — react-query
 *     CONSERVA el último dato bueno mientras marca el error, así que el caso no es teórico: es una
 *     lista vieja y recortada bajo un aviso que parece describir lo que se está viendo.
 *
 * Llevo doce copias de esta condición escritas a mano en cuatro features y **cada una necesita su
 * propio testigo**; escribirla trece veces es garantizar que una envejezca distinto. Las doce
 * anteriores se unifican aquí en cuanto aterrice la pila.
 *
 * `label` lo compone el llamante porque la cifra que debe salir es la CARGADA —`items.length`—, no
 * el techo que se pidió: interpolar la constante convierte el aviso en una medida inventada.
 */
/**
 * ⛔ LA REGLA DEL RECORTE, EN UN SOLO SITIO. `has_more === true && !error`.
 *
 * Vive aquí, y no dentro del componente, porque hay DOS consumidores y escribirla dos veces es
 * como envejecen distinto: el aviso decide si se pinta, y la pantalla decide si su cifra puede
 * seguir llamándose «total». Lo señaló el contraste de Codex (F-01, 2026-08-23) al encontrar una
 * tarjeta que pintaba «hay más» y debajo «1000 total» — el recorte declarado y la cifra
 * desmintiéndolo, en el mismo sitio.
 *
 * `=== true` y no un truthy: el cast es una aserción de TypeScript, no una comprobación en
 * ejecución, así que un `has_more: "false"` del transporte contaría como verdadero por ser una
 * cadena no vacía. Lo señaló el contraste externo anterior.
 */
export function listaRecortada(query: {
  data?: unknown
  error?: unknown
}): boolean {
  const sobre = query.data as { has_more?: unknown } | undefined
  return sobre?.has_more === true && !query.error
}

export function ListTruncationBadge({
  query,
  label,
  hint,
  className,
  filas,
}: {
  /** El resultado de react-query tal cual. `data` va como `unknown` a propósito: algunas rutas
   *  del cliente están tipadas así —la residencia por workspace, por ejemplo— y un tipo más
   *  estrecho obligaría a castear en el llamante, que es justo donde no quiero decisiones. */
  query: { data?: unknown; error?: unknown }
  label: string
  hint: string
  className?: string
  /** ⛔ FILAS CARGADAS, OPCIONAL A PROPOSITO. Con `{items: [], has_more: true}` el aviso se
   *  pintaba ENCIMA del estado vacio: «No active members» y «Loaded 0 rows; there are more» a la
   *  vez, que es un mensaje que se contradice solo. Lo midio el contraste (F-04).
   *
   *  Va como prop opcional y NO como cambio de `listaRecortada` porque este componente tiene
   *  102 llamantes en `web/src` y no todos pasan un sobre con `items`: exigirlo dentro apagaria
   *  avisos ajenos EN SILENCIO, que es peor que el defecto que arregla. Quien puede decir cuantas
   *  filas hay, lo dice; quien no, se comporta como siempre. */
  filas?: number
}) {
  if (!listaRecortada(query)) return null
  if (filas !== undefined && filas <= 0) return null
  return (
    <div className={cn('px-3 pt-3', className)}>
      <Badge variant="warning" title={hint}>
        {label}
      </Badge>
    </div>
  )
}

/** Shown when an aggregate hit the scan ceiling (`truncated: true`). */
export function TruncatedNotice({ className }: { className?: string }) {
  const { t } = useTranslation('intel')
  return (
    <IntelNotice tone="warning" icon={<TriangleAlert />} className={className}>
      <span className="font-medium text-warning">{t('notices.truncated')}</span>{' '}
      <span className="text-muted-foreground">
        {t('notices.truncatedHint')}
      </span>
    </IntelNotice>
  )
}

/** Shown above a privileged, self-audited read (forensics, anomalies, evidence). */
export function SelfAuditNotice({ className }: { className?: string }) {
  const { t } = useTranslation('intel')
  return (
    <IntelNotice tone="info" icon={<Eye />} className={className}>
      <span className="text-muted-foreground">{t('notices.selfAudited')}</span>
    </IntelNotice>
  )
}

/** A free-text caveat (orchestration coverage, declared-pricing, honest gaps). */
export function CaveatNotice({
  children,
  tone = 'neutral',
  className,
}: {
  children: ReactNode
  tone?: Tone
  className?: string
}) {
  return (
    <IntelNotice tone={tone} icon={<Info />} className={className}>
      <span className="text-muted-foreground">{children}</span>
    </IntelNotice>
  )
}

/** A compliance / forecast disclaimer — always rendered, muted, never hidden.
 *
 *: it is also TRANSLATED, when we recognise it. The text arrives from the
 *  engine (`response.disclaimer`) and the engine speaks only English, so before this
 *  the one sentence whose whole job is to say "this is NOT a certification" reached a
 *  Spanish reader in English — and the reader who most needs that sentence is exactly
 *  the one who does not read English.
 *
 *  Recognition is EXACT (see disclaimers.ts) and the fallback is the engine's own
 *  words, so an unknown or composed disclaimer is passed through untouched rather
 *  than half-translated. The canonical English stays reachable on the element, so the
 *  authoritative wording is never destroyed by the courtesy translation. */
export function DisclaimerNote({
  text,
  className,
}: {
  text?: string
  className?: string
}) {
  const { t } = useTranslation('intel')
  if (!text) return null
  const key = disclaimerKey(text)
  // defaultValue is the engine's text: a missing catalog entry degrades to what the
  // server said, never to a raw `disclaimers.*` key.
  const shown = key ? t(`disclaimers.${key}`, { defaultValue: text }) : text
  const translated = key !== null && shown !== text
  // NO lang attribute. The first version tagged everything unrecognised as lang="en",
  // which was wrong about this component: several views already pass it text that
  // came out of t(...) — observability, executive, erasure, finops — so Spanish,
  // German and Japanese copy was being announced as English by screen readers. We
  // cannot know the language of an arbitrary string, and guessing it is worse than
  // saying nothing. Found by the sol-max contrast.
  return (
    <p
      className={cn('text-xs leading-relaxed text-muted-foreground', className)}
      title={translated ? text : undefined}
    >
      {shown}
    </p>
  )
}

/** A neutral "not wired yet" marker for an honest seam (a dependency not connected
 *  whose default is safe) — never dressed up as working. */
export function SeamBadge({ label }: { label?: string }) {
  const { t } = useTranslation('intel')
  return (
    <Badge variant="outline" className="gap-1">
      <Plug className="size-3" />
      {label ?? t('notices.seam')}
    </Badge>
  )
}
