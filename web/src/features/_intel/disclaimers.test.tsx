// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, describe, expect, it } from 'vitest'
import i18n from 'i18next'
import { act, renderIntel, screen } from '@/test/intel'
import '@/features/_intel'
import { DisclaimerNote } from '@/features/_intel'
import { KNOWN_DISCLAIMERS, disclaimerKey } from './disclaimers'
import es from './i18n/es.json'
import de from './i18n/de.json'
import fr from './i18n/fr.json'
import ja from './i18n/ja.json'
import ru from './i18n/ru.json'
import zh from './i18n/zh.json'

// THE DEFECT. The Compliance console renders `response.disclaimer` — the
// engine's own words — verbatim. The engine speaks only English, so the sentence
// whose entire job is to say "this is NOT a certification and NOT legal advice"
// arrived, in English, underneath Spanish copy. The reader who most needs that
// sentence is precisely the one who does not read English.

/**
 * ⛔ SIN «(docs/SECURITY-HARDENING.md)», y la ausencia es deliberada. `9f6eb1dd6` —«internal doc refs out of audit
 *    deliverables»— retiró esa referencia de las SIETE traducciones porque un puntero a
 *    documentación interna no viaja en material que ve el cliente. El testigo se quedó con la
 *    cadena vieja, así que `main` llevaba tres casos en rojo: el inglés canónico, el español y el
 *    de reconocimiento exacto. Aquí sólo se alinea el testigo con una decisión YA TOMADA — el
 *    texto que manda es el del i18n, y `grep 'docs/SECURITY-HARDENING.md' i18n/*.json` no devuelve nada.
 *
 *    Si alguien vuelve a añadirla aquí, este fichero volverá a rojo contra las traducciones: es
 *    el mutante de este cambio y no hace falta escribirlo aparte.
 */
const REPORT_DISCLAIMER =
  'Technical control-status mapping derived from observed platform evidence. NOT a certification and NOT legal advice.'

afterEach(async () => {
  await i18n.changeLanguage('en')
})

describe('DisclaimerNote — the engine speaks English; the reader may not', () => {
  it('renders the ENGINE text unchanged in English', () => {
    renderIntel(<DisclaimerNote text={REPORT_DISCLAIMER} />)
    expect(screen.getByText(REPORT_DISCLAIMER)).toBeInTheDocument()
  })

  it('renders the SPANISH text for the same engine sentence', async () => {
    await act(async () => {
      await i18n.changeLanguage('es')
    })
    renderIntel(<DisclaimerNote text={REPORT_DISCLAIMER} />)

    const spanish = es.disclaimers.report
    expect(screen.getByText(spanish)).toBeInTheDocument()
    // And the English must be GONE from the rendered output — a translation that
    // leaves the original beside it has not solved anything.
    expect(screen.queryByText(REPORT_DISCLAIMER)).not.toBeInTheDocument()
  })

  it('keeps the canonical English reachable rather than destroying it', async () => {
    await act(async () => {
      await i18n.changeLanguage('es')
    })
    renderIntel(<DisclaimerNote text={REPORT_DISCLAIMER} />)
    // The authoritative legal wording is the engine's. The courtesy translation is
    // what the reader sees; the original stays on the element.
    expect(screen.getByTitle(REPORT_DISCLAIMER)).toBeInTheDocument()
  })

  it('passes an UNKNOWN disclaimer through untouched, in any language', async () => {
    // The rule that keeps the console from inventing legal text: anything the engine
    // sends that we do not recognise byte-for-byte renders exactly as sent.
    const unknown =
      'Provisional NIS 2 Directive significant-incident classification; not legal advice.'
    await act(async () => {
      await i18n.changeLanguage('es')
    })
    renderIntel(<DisclaimerNote text={unknown} />)
    expect(screen.getByText(unknown)).toBeInTheDocument()
  })

  it('does not half-translate a COMPOSED disclaimer', async () => {
    // The OSCAL export appends its own sentence to the report disclaimer. Matching a
    // prefix would produce one Spanish sentence followed by an English one; exact
    // matching leaves the whole thing in English, which is honest.
    const composed = `${REPORT_DISCLAIMER} OSCAL v1.2.2 export; satisfied is asserted ONLY for controls with live operational evidence.`
    await act(async () => {
      await i18n.changeLanguage('es')
    })
    renderIntel(<DisclaimerNote text={composed} />)
    expect(screen.getByText(composed)).toBeInTheDocument()
  })

  it('renders nothing at all for an absent disclaimer', () => {
    const { container } = renderIntel(<DisclaimerNote text={undefined} />)
    expect(container.querySelector('p')).toBeNull()
  })
})

describe('the canonical map', () => {
  it('recognises the engine sentence exactly, and only exactly', () => {
    expect(disclaimerKey(REPORT_DISCLAIMER)).toBe('report')
    // Trailing/leading whitespace is not a difference in meaning.
    expect(disclaimerKey(`  ${REPORT_DISCLAIMER}  `)).toBe('report')
    // A changed word is a different sentence and must NOT be silently translated.
    expect(
      disclaimerKey(
        REPORT_DISCLAIMER.replace('NOT a certification', 'a certification'),
      ),
    ).toBeNull()
    expect(disclaimerKey(undefined)).toBeNull()
    expect(disclaimerKey('')).toBeNull()
  })

  it.each([
    ['es', es, /\bno\b[^.]*\bcertificaci/i],
    ['de', de, /\bkeine\b[^.]*\bzertifizierung/i],
    ['fr', fr, /\bpas\b[^.]*\bcertification/i],
    // NOT \b for Cyrillic: JavaScript word boundaries are defined on [A-Za-z0-9_],
    // so \bне never matches. A regex that cannot match is a check that measures
    // nothing — it failed loudly here, which is the right failure mode.
    ['ru', ru, /(?:^|[\s.])не[^.]*сертификаци/iu],
    ['ja', ja, /認証ではなく|認証では ?ありません/],
    ['zh', zh, /不是认证|并非认证/],
  ])(
    'the %s translation NEGATES the certification claim, in that order',
    (_loc, catalog, negatedCertification) => {
      // WHY THE SHAPE OF THIS ASSERTION MATTERS. The first version checked three
      // things separately — that the text contained "NO", contained "certificaci",
      // and mentioned legal advice — and the sol-max contrast broke it in one line:
      // "ES una certificación; NO es asesoramiento jurídico" satisfies all three
      // while asserting the exact opposite of the product's position. Three true
      // facts about a sentence are not the sentence's meaning.
      //
      // So the negation and the noun are matched TOGETHER, in order, within the same
      // clause. Measured a cheaper translation route inverting deny-closed
      // behaviour on five pages of governed-data documentation; this sentence is the
      // same class of risk, and it is the one sentence whose whole content is a NOT.
      const text = catalog.disclaimers.report
      expect(text).toMatch(negatedCertification)
      expect(text).not.toBe(KNOWN_DISCLAIMERS[REPORT_DISCLAIMER])
      // And it must not merely PREFIX the English, which the gate would otherwise
      // accept as a translation.
      expect(text).not.toContain(REPORT_DISCLAIMER)
    },
  )
})
