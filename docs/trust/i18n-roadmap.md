<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Internationalization — current posture and roadmap

> Procurement asks two distinct questions here: *what languages does the product
> support today* (a fact) and *what is committed next* (a roadmap with an honest
> decision gate). Both below. Companion artifact: the accessibility ACR
> (`docs/accessibility/VPAT-olivares-admin.md`) — language and accessibility are
> assessed together in EU public procurement (EN 301 549 / EAA).

## Today (verified in-tree, 2026-08-01)

| Surface | Languages | Mechanism |
|---|---|---|
| Product console (`web/`) | **7 locales: English, Spanish, German, French, Japanese, Russian, Chinese** | Fully externalized strings: namespaced locale JSON (`web/src/lib/i18n/locales/{en,es,de,fr,ja,ru,zh}/`), i18next with browser language detection, persisted user choice, and `<html lang>` kept in sync (an accessibility requirement, WCAG 3.1.1 — see the ACR); cross-locale key parity is CI-gated (`scripts/check-i18n-parity.mjs`) |
| Product documentation site (`docs-site/`) | **English (authoritative) + 6 locales: es, de, fr, ja, ru, zh** | Astro/Starlight; a strict EN↔locale page-parity gate in CI (`scripts/check-docs-parity.mjs`); every translated page carries a banner disclosing machine translation and linking the authoritative English page |
| README | **7 languages: English + de, es, fr, ja, ru, zh** | `README.{de,es,fr,ja,ru,zh}.md`, per-locale in-page nav anchors verified |
| Public marketing website (separate repo) | **13 languages** | Per-locale static builds — separate repo; verify on the site's locale switcher |
| Trust & procurement package (this directory), SECURITY/SUPPORT | English (the lingua franca of security review) | Markdown |
| EU procurement aid | The MCC-AI clauses themselves are published by the Commission in **24 EU languages** — useful to buyers regardless of our doc languages ([mcc-ai-crosswalk.md](./mcc-ai-crosswalk.md)) | — |

Translation honesty: non-English docs-site pages are machine translations behind
an explicit banner (machine-translated · English is authoritative · native review
pending); the README translations carry no such banner today. Spanish had a full
native review pass over the 2026-06-29 snapshot (console and all docs-site pages
as of that date); pages edited since then — including the 2026-08-01 count
corrections — have not had a native pass, so the conservative Spanish banner
stays. Native-speaker review of de, fr, ja, ru and zh is still **pending** and
tracked internally per locale.

Architecture readiness: adding a console locale is **additive** — translate the
JSON namespaces and register the locale; no string extraction work remains,
because no UI strings are hard-coded (that work is done).

## Roadmap (demand-gated, honestly)

| Phase | Content | Status / trigger |
|---|---|---|
| 1 | Console EN/ES | **Shipped** |
| 2 | docs-site full Spanish locale + CI parity gate | **Shipped** (verified 2026-08-01; the parity gate now covers all 6 locales) |
| 3 | Console DE/FR/JA/RU/ZH; docs-site + README in de/fr/ja/ru/zh (machine-translated, banner-disclosed) | **Shipped** (verified 2026-08-01) — went ahead of the original demand gate; IT/PT from the original candidate set remain unshipped |
| 4 (pending) | Native-speaker review of the machine-translated locales: de, fr, ja, ru, zh (Spanish already reviewed) | Per-locale, contractable post-launch; suggested priority by expected traffic: de/fr → ja/zh → ru |
| 5 (candidate) | Console/docs **IT, PT**; localized VPAT/ACR summaries | First qualified deal/tender requiring it; per-locale cost is translation + ongoing parity maintenance, so speculative translation is waste |

Non-goals until demanded: RTL languages (would need a UI audit beyond
translation). The CJK locales (ja, zh) shipped as machine translation; dedicated
CJK typography tuning beyond the default font stack has not been done. Both are
listed so the roadmap is not read as "free".

> **Founder decision required:** contracting native-speaker review for the five
> machine-translated locales (translation spend + maintenance commitment), and
> activating any Phase-5 locale. The shipped locales carry the explicit
> machine-translation banner until reviewed, so publication is honest today.
