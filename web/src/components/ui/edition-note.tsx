// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A CAPABILITY THIS EDITION DOES NOT HOLD: one calm line in place of what was refused, never
// an error, a dead page or a toast. The engine refuses with 403 edition_required and, older,
// 501 business_required, 403 addon_requires_license and the other codes that send a person
// to an edition; each names the edition that holds the capability (`error.edition`), and
// edition_required its stable key (`error.capability`), which the console translates and
// never prints. The line shows the first time a person meets that refusal (the same memory
// as a dismissed notice); afterwards only the quiet edition tag says it.
import { useEffect, useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { AppLink } from '@/components/ui/app-link'
import { buttonVariants } from '@/components/ui/button'
import { Tag } from '@/components/ui/tag'
import { ApiError, isPaidEdition, type PaidEdition } from '@/lib/api/errors'
import { noticeSeen, rememberNotice } from '@/lib/notify'
import { cn } from '@/lib/utils'

export { isPaidEdition, type PaidEdition }
export type Edition = 'community' | PaidEdition

/** The editions by their whole names: names, never translated, never shortened. */
export const EDITION_NAMES: Readonly<Record<Edition, string>> = {
  community: 'Olivares AI Community',
  engineer: 'Olivares AI Engineer',
  business: 'Olivares AI Business',
  enterprise: 'Olivares AI Enterprise',
}

export const PRICING_URL = 'https://olivares.ai/pricing'
const EDITION_SETTINGS = '/settings?section=edition'

export interface EditionRefusal {
  /** The edition that holds the capability; absent when the answer names none. */
  edition?: PaidEdition
  /** The capability's stable key, for its words and its memory; never printed. */
  capability?: string
  /** The engine's own sentence: shown only when no edition is named. */
  message: string
}

/**
 * The edition refusal an answer carries, or null for any other answer (the decision is
 * ApiError.isEditionRefusal, the same one that keeps isForbidden for the role). A
 * 501 business_required that names no edition means Olivares AI Business, as it always has;
 * an add-on refusal that names none names none. Where the answer carries no capability,
 * `refusedKey` stands for it: the key of exactly what the caller asked for (approvalTiers),
 * never a page's or a module's, which is wider than what one answer refuses.
 */
export function editionRefusal(
  error: unknown,
  refusedKey?: string,
): EditionRefusal | null {
  if (!(error instanceof ApiError) || !error.isEditionRefusal) return null
  const named = error.detailString('edition')
  return {
    edition: isPaidEdition(named)
      ? named
      : error.code === 'business_required'
        ? 'business'
        : undefined,
    capability: error.detailString('capability') ?? refusedKey,
    message: error.message,
  }
}

/** The console's own words for a capability key: its note words, else the module's name. */
function useCapabilityName(key?: string): string | undefined {
  const { t, i18n } = useTranslation(['common', 'settings'])
  if (!key) return undefined
  const own = `common:edition.capabilities.${key}`
  const moduleName = `settings:modules.names.${key}`
  if (i18n.exists(own)) return t(own)
  if (i18n.exists(moduleName)) return t(moduleName)
  return undefined
}

/** The quiet tag a refused control carries once its line has been read. */
export function EditionTag({
  edition,
  className,
}: {
  edition: PaidEdition
  className?: string
}) {
  const { t } = useTranslation('common')
  const name = EDITION_NAMES[edition]
  return (
    <Tag className={className}>
      <span aria-hidden="true">{name}</span>
      <span className="sr-only">
        {t('edition.note.tag', { edition: name })}
      </span>
    </Tag>
  )
}

/**
 * The line itself: the capability, the edition's whole name, then the way on. Whoever can
 * add a license (the mount decides, by the license tab's own gate) reads its two ways on,
 * Settings > Edition and olivares.ai/pricing, as links where the place can hold them (a
 * toast closes by itself, so there they are plain words); anyone else reads «Ask an
 * administrator.», with no link to a place they cannot use. An answer that names no
 * edition keeps the engine's sentence.
 */
export function EditionLine({
  refusal,
  canAddLicense,
  links = false,
}: {
  refusal: EditionRefusal
  canAddLicense: boolean
  links?: boolean
}) {
  const { t } = useTranslation('common')
  const capability = useCapabilityName(refusal.capability)
  const { edition } = refusal
  if (!edition) return <>{refusal.message}</>
  const values = { capability, edition: EDITION_NAMES[edition] }
  if (!canAddLicense)
    return (
      <>
        {t(
          capability ? 'edition.note.lineAsk' : 'edition.note.lineUnnamedAsk',
          values,
        )}
      </>
    )
  const link = buttonVariants({ variant: 'link' })
  return (
    <Trans
      t={t}
      i18nKey={capability ? 'edition.note.line' : 'edition.note.lineUnnamed'}
      values={values}
      components={[
        links ? (
          <AppLink key="0" href={EDITION_SETTINGS} className={link} />
        ) : (
          <span key="0" />
        ),
        links ? (
          <a
            key="1"
            href={PRICING_URL}
            target="_blank"
            rel="noopener noreferrer"
            className={link}
          />
        ) : (
          <span key="1" />
        ),
      ]}
    />
  )
}

export interface EditionNoteProps {
  refusal: EditionRefusal
  /** Whether this person can add a license in Settings > Edition: decided where the line
   * is mounted, by the license tab's own gate, so this shared component reads no feature. */
  canAddLicense: boolean
  /** What is already stored stays readable and exportable below the edition. */
  readsBelow?: boolean
  /** The note stands in for a whole page (the route gate's): its whole line on every
   * visit, since a tag alone reads as an empty page. Without it, a note beside a control
   * or a refused read is told once and then only tagged. */
  whole?: boolean
  className?: string
}

export function EditionNote({
  refusal,
  canAddLicense,
  readsBelow = false,
  whole = false,
  className,
}: EditionNoteProps) {
  const { t } = useTranslation('common')
  const { edition } = refusal
  // One entry per capability with the person's dismissed notices, signed with its edition:
  // no secret, no words of the person, and told again if the capability moves. A refusal
  // that names no capability is told each time: it cannot speak for another.
  const told =
    edition && refusal.capability && `edition-note:${refusal.capability}`
  const [seen] = useState(() => !!told && noticeSeen(told, edition))
  useEffect(() => {
    if (told && !seen) rememberNotice(told, edition)
  }, [told, edition, seen])
  if (seen && edition && !whole)
    return <EditionTag edition={edition} className={className} />
  return (
    <div
      role="status"
      data-slot="edition-note"
      className={cn(
        'flex flex-col gap-1 py-3 text-body text-text-2',
        className,
      )}
    >
      <p>
        <EditionLine refusal={refusal} canAddLicense={canAddLicense} links />
      </p>
      {readsBelow ? <p>{t('edition.note.readsBelow')}</p> : null}
    </div>
  )
}
