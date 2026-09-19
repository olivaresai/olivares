// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE NEXT ACTION.
//
// A review of the console wrote down the finding that matters: a first screen should
// always offer something to DO, and this one offered "six read-only KPI cards. **No
// action anywhere on the page**". The operator's own sentence is the same defect from
// the other side: "I am not able to launch a session easily without filling in
// configuration by hand."
//
// ⛔ THESE ARE LINKS, AND CALLING THEM BUTTONS WOULD BE THE LIE. Adding a provider or
//    launching a run is a form with real inputs; a front door that pretended to do it
//    in one click would either fabricate the inputs or open a dialog that is a worse
//    copy of the screen that owns it. The first-hour flow and the provider, agent and
//    onboarding screens own those flows. What this component owns is the SEAM: the
//    verb is named, it is on the first screen, and it lands on the screen that
//    performs it.
//
// ⛔ THE PERMISSION IS THE **WRITE** ONE, NOT THE ROUTE'S READ PERMISSION. A card that
//    says "Add a provider" to a principal who may only read the provider plane is an
//    invitation to a 403. specification04 §1 states the rule the palette already
//    follows — "read permission for its page does not authorize it" — and this is the
//    same rule applied to an offer rather than to a command. A principal who holds no
//    write permission here sees no card row at all, which is the honest answer: there
//    is nothing for them to start.
//
// ⛔ AND THE PATHS COME FROM THE REGISTRY. `FEATURE_VIEWS` is the route table and
//    `route-census.json` pins it; a path typed here would be a second copy that goes
//    stale the day a route moves. `next-step.test.tsx` fails if an id stops resolving.
import { Link } from '@tanstack/react-router'
import { ArrowRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { useAuth } from '@/lib/auth/context'
import { NEXT_STEPS, stepView } from './next-step-catalog'
import './i18n'

export function NextStep() {
  const { t } = useTranslation('home')
  const { can } = useAuth()
  const offered = NEXT_STEPS.map((step) => ({
    step,
    view: stepView(step.viewId),
  })).filter((row) => row.view !== undefined && can(row.step.permission))

  if (offered.length === 0) return null

  return (
    <section aria-labelledby="home-next-step" data-testid="home-next-step">
      <h2
        id="home-next-step"
        className="mb-3 text-overline text-muted-foreground uppercase"
      >
        {t('next.title')}
      </h2>
      {/* ONE LINE PER VERB. Three 96 px cards — a 36 px icon chip beside a
          heading and a wrapping description — were 96 px of the front door spent on
          three links. One line each, 44 px, same three verbs, same three destinations,
          same permissions: the description follows the title in the muted register and
          truncates with it. The verb is what an operator reads; its explanation is
          what they read if the verb was not enough. */}
      <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
        {offered.map(({ step, view }) => {
          const Icon = view!.icon
          const full = `${t(`next.${step.id}.title`)} · ${t(
            `next.${step.id}.description`,
          )}`
          return (
            <Link
              key={step.id}
              to={view!.path as never}
              data-testid={`home-next-step-${step.id}`}
              title={full}
              className="group flex min-w-0 items-center gap-2 rounded-lg border border-border bg-surface px-3 py-2.5 outline-none transition hover:border-accent-line hover:bg-elevated focus-visible:ring-2 focus-visible:ring-ring"
            >
              {/* ⛔ THE ICON IS MUTED, AND IT WAS THE ACCENT. The bar spends the one
                  orange on three things only — selection, the primary action, and
                  links — and this glyph is none of them: it repeats what the verb
                  beside it already says. An accent that also decorates stops marking
                  anything, which is what an independent review measured here. */}
              <Icon
                aria-hidden
                className="size-4 shrink-0 text-muted-foreground"
              />
              <span className="min-w-0 flex-1 truncate text-body" title={full}>
                <span className="font-medium text-foreground">
                  {t(`next.${step.id}.title`)}
                </span>
                <span className="text-muted-foreground">
                  {' · '}
                  {t(`next.${step.id}.description`)}
                </span>
              </span>
              <ArrowRight
                aria-hidden
                className="size-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5"
              />
            </Link>
          )
        })}
      </div>
    </section>
  )
}
