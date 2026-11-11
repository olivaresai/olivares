// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The accounts sessions run as, on AI tools; the API keys are the Providers page beside
// it (one page per concept). The accounts were two pages more for the profiles and
// bindings an account is made of; an account's profile and bindings are now in its
// detail. Each part is shown under its own read tier and leaves the screen the moment
// that tier does.
import { useId, useState, type ReactNode } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { AppLink } from '@/components/ui/app-link'
import { BindingsTable } from '@/features/agentops/bindings-table'
import { ProfilesPanel } from '@/features/agentops/profiles-panel'
import { ProviderAccountsPanel } from '@/features/agentops/provider-accounts-panel'
import {
  KEY_REFERENCES_ANCHOR,
  ProviderKeyReferences,
} from '@/features/models/key-references'
import { useAuth } from '@/lib/auth/context'
import { useModuleOn } from '@/stores/modules'
import './i18n'

/** The accounts part of AI tools. */
export function AccountsAndKeys({ folded = false }: { folded?: boolean }) {
  const { t } = useTranslation(['agentTools', 'agentops'])
  const { can } = useAuth()
  const id = useId()
  // Folded, the accounts are read only once opened: each one is already a row of its
  // tool above, so the full list (details, labels, bindings) is one click away instead of
  // a second copy on the page and a second read of the same list.
  // Once opened it stays mounted, so closing it never drops an open detail or a draft.
  const [opened, setOpened] = useState(false)
  const accounts = can('sessions:account:read')
  const profileRead = can('sessions:profile:read')
  // An account's detail carries its profile, and the profile its bindings. A custom role
  // may grant the profile or binding tier without the one above it; that reader gets the
  // list on its own, as the /provider-profiles and /provider-bindings doors gave it.
  const profiles = profileRead && !accounts
  const bindings = can('sessions:profile-binding:read') && !profileRead
  // The governed references to keys held at the provider sit on Providers, beside the
  // API keys. A reader of them who cannot open Providers keeps them here, as before;
  // one who can finds a line that leads there, at the same anchor (the old Models keys
  // address lands on it).
  const providersRead = can('sessions:provider:read')
  const referencesRead = useModuleOn('models') && can('models:keys:read')
  const keyReferences = referencesRead && !providersRead
  // The tier left: the folded list is closed again, not read behind a closed fold.
  if (!accounts && opened) setOpened(false)
  // Deny closed, and said.
  if (!accounts && !profiles && !bindings && !keyReferences)
    return (
      <p role="status" className="text-caption text-text-2">
        {t('place.noAccess')}
      </p>
    )
  return (
    <section aria-labelledby={id} className="flex min-w-0 flex-col gap-6">
      <h2 id={id} className="text-heading text-text">
        {t('place.accounts')}
      </h2>
      {accounts && folded ? (
        <details
          className="rounded-md border border-border p-4"
          onToggle={(e) => {
            if (e.currentTarget.open) setOpened(true)
          }}
        >
          <summary className="cursor-pointer text-body font-medium">
            {t('place.allAccounts')}
          </summary>
          {opened ? (
            <div className="mt-3 flex min-w-0 flex-col gap-3">
              <p className="text-caption text-text-2">
                {t('agentops:accounts.subtitle')}
              </p>
              <ProviderAccountsPanel />
            </div>
          ) : null}
        </details>
      ) : accounts ? (
        <div className="flex min-w-0 flex-col gap-3">
          <p className="text-caption text-text-2">
            {t('agentops:accounts.subtitle')}
          </p>
          <ProviderAccountsPanel />
        </div>
      ) : null}
      {profiles ? (
        <Part
          title={t('agentops:profiles.view.profilesTitle')}
          hint={t('agentops:profiles.subtitle')}
        >
          <ProfilesPanel />
        </Part>
      ) : null}
      {bindings ? (
        <Part
          title={t('agentops:profiles.view.bindingsTitle')}
          hint={t('agentops:profiles.bindings.subtitle')}
        >
          <BindingsTable describe={false} />
        </Part>
      ) : null}
      {keyReferences ? <ProviderKeyReferences /> : null}
      {providersRead ? (
        <p id={KEY_REFERENCES_ANCHOR} className="text-caption text-text-2">
          <Trans
            t={t}
            i18nKey={
              referencesRead
                ? 'place.keysAndReferencesOnProviders'
                : 'place.keysOnProviders'
            }
            components={[
              <AppLink key="providers" href="/providers" className={LINK} />,
            ]}
          />
        </p>
      ) : null}
    </section>
  )
}

/** An in-app link reads as text with an underline: orange is the primary action. */
const LINK =
  'text-text underline decoration-text-3 underline-offset-[3px] hover:decoration-text'

/** One titled part of the section: its heading, the line that says what it is, its list. */
function Part({
  title,
  hint,
  children,
}: {
  title: string
  hint: string
  children: ReactNode
}) {
  const id = useId()
  return (
    <section aria-labelledby={id} className="flex min-w-0 flex-col gap-3">
      <div className="flex min-w-0 flex-col gap-0.5">
        <h3 id={id} className="text-body-l font-semibold text-text">
          {title}
        </h3>
        <p className="text-caption text-text-2">{hint}</p>
      </div>
      {children}
    </section>
  )
}
