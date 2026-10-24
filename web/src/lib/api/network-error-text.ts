// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ONE WORDING of a NetworkError. A browser gives a script the same opaque TypeError for a
// refused certificate, a stopped engine and a dropped link (the Fetch spec hides the reason on
// purpose), so the console cannot read the cause from the error. It can read how the page was
// opened. Over http: TLS is not involved, so the engine does not answer and the line stays
// "unreachable". Over https: a certificate the browser does not trust is a possible cause the
// user can fix and "unreachable" hides it, so the wording names the secure connection, the
// certificate and its next step, then the engine and the link (olivares#2781).
import type { TFunction } from 'i18next'

export interface NetworkErrorText {
  title: string
  description: string
  /** The text of a form line, which has no separate title: the description when it names the next step. */
  line: string
}

export function networkErrorText(t: TFunction): NetworkErrorText {
  const secure = window.location.protocol === 'https:'
  const key = secure ? 'networkSecure' : 'network'
  const title = t(`errors:${key}.title`)
  const description = t(`errors:${key}.description`, {
    origin: window.location.origin,
  })
  return { title, description, line: secure ? description : title }
}
