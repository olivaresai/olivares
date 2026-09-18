// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useRouterState } from '@tanstack/react-router'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ForbiddenState } from '@/components/ui/error-state'
import { Spinner } from '@/components/ui/spinner'
import { useRouteAccess } from '@/features/navigation/authorization'
import { PermittedVisit } from '@/features/navigation/permitted-visit'
import type { FeatureView } from '@/features/registry'
// ⛔ THE MODULE THAT TRANSLATES REGISTERS ITS NAMESPACE. This gate renders four
//    `communications:capability.*` sentences and never loaded their bundle, so in a chunk
//    that had not already pulled the feature in they resolved to raw dotted keys — the
//    exact class `check-i18n-namespaces` exists to catch, and it was failing on this file
//    before this change: "namespace communications is never registered in 60 chunk(s)".
//    The four keys are the honest vocabulary of a capability answer and are not duplicated
//    into `errors`; the namespace comes to the gate instead.
import '@/features/communications/i18n'

/**
 * RequirePermission gates a route's content on the authority the ENGINE established for
 * this view. With nothing declared it renders the content; otherwise it renders what was
 * actually established, and only a positive lets the child mount.
 *
 * It receives the WHOLE VIEW rather than a permission string, and reads the location
 * itself, because a route's answer is not always one permission:
 *
 *   · an ordinary view still resolves `can(view.permission)` — deep-linking is RBAC-checked
 *     and not merely hidden in the nav, exactly as before;
 *   · a view that declares a capability asks the engine's registered question, and its url
 *     decides WHICH question: a valid entity deep link asks that entity's own operation and
 *     may render it while the collection is refused, without fetching the collection.
 *
 * ⛔ FOUR OUTCOMES, NOT TWO, AND UNKNOWN IS NEVER FORBIDDEN. Rendering "you do not have
 *    permission" for an answer the console could not establish is a false statement about
 *    the operator's authority: it sends someone to ask for access they may already hold.
 *    Pending says it is checking, an established refusal is the calm Forbidden this route
 *    always had, a concealed non-verdict is NEUTRAL — it asserts no denial, no missing
 *    target, no outage and no missing read — and a local failure is retryable.
 *
 * ⛔ AND THE NEUTRAL STATE DOES NOT REUSE THE EXISTING "unavailable" COPY, which says the
 *    engine could not look. For `undisclosed` the engine looked and declined to say; that
 *    text would be a diagnosis, and the concealment exists precisely so there is none.
 */
export function RequirePermission({
  view,
  children,
}: {
  view: FeatureView
  children: ReactNode
}) {
  const search = useRouterState({ select: (s) => s.location.searchStr })
  const access = useRouteAccess(view, search)
  // ⛔ ONE POSITION, ALWAYS THE SAME ONE, AND ALWAYS AROUND THE ANSWER THIS GATE ALREADY
  //    DECIDED. A view may declare a continuity boundary (registry `continuity`); it is
  //    mounted here, wrapping `body` below — the protected children when they are
  //    permitted, this gate's own notice in every other state. Three properties follow,
  //    and all three are why the wrapper sits OUTSIDE the decision rather than inside it:
  //
  //      · the boundary never receives the protected tree without an admission, so it
  //        cannot mount, reveal or resume it;
  //      · nothing can hand it a fabricated `permitted` — the decision is computed here,
  //        from the engine's own answer, and is passed to it as information only;
  //      · its element position does not change with the answer, so React keeps ONE
  //        instance across the teardown and rebuild that a budget edge causes. That is the
  //        entire point: the subtree dies every few seconds and the boundary does not.
  //
  //    A view that declares nothing renders exactly what it rendered before.
  const Continuity = view.continuity
  const body =
    access.kind === 'permitted' ? (
      <>
        {children}
        <PermittedVisit id={view.id} />
      </>
    ) : access.kind === 'checking' ? (
      <CheckingNotice />
    ) : access.kind === 'forbidden' ? (
      <ForbiddenNotice />
    ) : access.globalAccount ? (
      <GlobalAccountNotice />
    ) : (
      <NeutralNotice undisclosed={access.kind === 'undisclosed'} />
    )
  return Continuity ? (
    <Continuity admitted={access.kind === 'permitted'} access={access.observed}>
      {body}
    </Continuity>
  ) : (
    body
  )
}

/** No answer yet. It says so and keeps its place; it is never rendered as a refusal. */
function CheckingNotice() {
  const { t } = useTranslation('communications')
  return (
    <div
      data-slot="capability-checking"
      className="flex min-h-[60vh] items-center justify-center"
      role="status"
      aria-live="polite"
    >
      <Spinner />
      <span className="ml-3 text-body text-muted-foreground">
        {t('capability.checking')}
      </span>
    </div>
  )
}

/** The one case that may be shown as a refusal: the engine decided and said so. */
function ForbiddenNotice() {
  const { t } = useTranslation('errors')
  return (
    <div className="flex min-h-[60vh] items-center justify-center">
      <ForbiddenState
        title={t('forbidden.title')}
        description={t('forbidden.description')}
      />
    </div>
  )
}

/**
 * The known unsupported global-account family: the declared question was never submitted,
 * so there is no answer, no cadence and nothing to retry. It says which account can ask —
 * and nothing else. It does not claim a refusal, an outage, a retry in progress, or that
 * a member account would be admitted; whether it is, is the engine's to answer once a
 * member asks.
 */
function GlobalAccountNotice() {
  const { t } = useTranslation('communications')
  return (
    <div
      data-slot="capability-global-account"
      className="flex min-h-[60vh] items-center justify-center"
      role="status"
      aria-live="polite"
    >
      <div className="max-w-md space-y-2 text-center">
        <p className="text-body font-medium">
          {t('capability.globalAccountTitle')}
        </p>
        <p className="text-body text-muted-foreground">
          {t('capability.globalAccountBody')}
        </p>
      </div>
    </div>
  )
}

/**
 * `undisclosed` and `unavailable` share this shape, this cadence and this posture; they
 * differ only in whose limit is being reported — the engine declined to disclose, or this
 * console could not establish an answer. Neither names a cause.
 */
function NeutralNotice({ undisclosed }: { undisclosed: boolean }) {
  const { t } = useTranslation('communications')
  return (
    <div
      data-slot={
        undisclosed ? 'capability-undisclosed' : 'capability-unavailable'
      }
      className="flex min-h-[60vh] items-center justify-center"
      role="status"
      aria-live="polite"
    >
      <div className="max-w-md space-y-2 text-center">
        <p className="text-body font-medium">
          {t(
            undisclosed
              ? 'capability.undisclosedTitle'
              : 'capability.unavailableTitle',
          )}
        </p>
        <p className="text-body text-muted-foreground">
          {t(
            undisclosed
              ? 'capability.undisclosedBody'
              : 'capability.unavailableBody',
          )}
        </p>
        <p
          data-slot="capability-retry"
          className="text-caption text-muted-foreground"
        >
          {t('capability.retrying')}
        </p>
      </div>
    </div>
  )
}
