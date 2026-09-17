// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE NEXT ACTION (D21).
//
// D15 read T3 Code against our console and wrote down the finding that matters:
// "What the first screen offers — T3: a composer, always present: there is always
// something to DO. Olivares: six read-only KPI cards. **No action anywhere on the
// page**" (an internal design note (not shipped) §3.14). own sentence is the
// same defect from the other side: "I am not able to launch a session easily without
// filling in configuration by hand."
//
// ⛔ THESE ARE LINKS, AND CALLING THEM BUTTONS WOULD BE THE LIE. Adding a provider or
//    launching a run is a form with real inputs; a front door that pretended to do it
//    in one click would either fabricate the inputs or open a dialog that is a worse
//    copy of the screen that owns it. Lanes D17 (first hour) and D19 (providers,
//    agents, onboarding) own those flows. What this component owns is the SEAM: the
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
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {offered.map(({ step, view }) => {
          const Icon = view!.icon
          return (
            <Link
              key={step.id}
              to={view!.path as never}
              data-testid={`home-next-step-${step.id}`}
              className="group flex items-start gap-3 rounded-lg border border-border bg-surface p-4 shadow-xs outline-none transition hover:border-accent-line hover:bg-elevated focus-visible:ring-2 focus-visible:ring-ring"
            >
              <span className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-accent-soft text-accent-soft-foreground [&_svg]:size-5">
                <Icon />
              </span>
              <span className="min-w-0 flex-1">
                <span className="flex items-center gap-1.5 text-heading text-foreground">
                  {t(`next.${step.id}.title`)}
                  <ArrowRight
                    aria-hidden
                    className="size-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5"
                  />
                </span>
                <span className="mt-0.5 block text-body text-muted-foreground">
                  {t(`next.${step.id}.description`)}
                </span>
              </span>
            </Link>
          )
        })}
      </div>
    </section>
  )
}
