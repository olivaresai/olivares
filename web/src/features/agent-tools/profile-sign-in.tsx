// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Sign a profile in with the tool's own official login, run on this server through
// Olivares (POST /v1/m/agenttools/sign-in). Olivares never sees a password: it relays the
// tool's link and code. Used by a row's Sign in button and by Add profile, which
// continues here in place.
import { PanelSlot } from '@/components/ui/trust-chrome'
import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { CodeLine } from '@/components/ui/code-line'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { AccountSignIn } from '@/features/agentops/provider-account-signin'
import type { ProviderAccountDTO } from '@/features/agentops/types'
import { PANEL_EXTENSIONS } from '@/features/extensions'
import { SignIn } from '@/features/first-hour/first-hour'
import {
  KeyInstead,
  useKeyBeingStored,
  useKeyKind,
  useKeyTriggerFocus,
} from '@/features/first-hour/key-instead'
import type { ProviderRecordDTO } from '@/features/providers/types'
import { firstHourKeys, type SignInTool } from '@/features/first-hour/api'
import { useAuth } from '@/lib/auth/context'
import { PHONE_SHEET } from './phone-sheet'
import './i18n'

/** The API key as a way in: a button that turns into the key form in place. */
function KeyOffer({
  driver,
  accountRef,
  addingKey,
  onAddingKeyChange,
  onCreated,
}: {
  driver: string
  accountRef?: string
  addingKey: boolean
  onAddingKeyChange: (adding: boolean) => void
  onCreated?: (record: ProviderRecordDTO) => void
}) {
  const { t: hour } = useTranslation('firstHour')
  // The organization's default login can run on an API key from Providers instead.
  const keyKind = useKeyKind(driver as SignInTool, accountRef)
  const keyTriggerRef = useKeyTriggerFocus(addingKey)
  if (!keyKind) return null
  return addingKey ? (
    <KeyInstead
      driver={driver as SignInTool}
      onCancel={() => onAddingKeyChange(false)}
      onCreated={onCreated}
    />
  ) : (
    <Button
      type="button"
      variant="link"
      className="self-start"
      ref={keyTriggerRef}
      onClick={() => onAddingKeyChange(true)}
    >
      {hour('actions.useApiKey')}
    </Button>
  )
}

/** The other ways to sign in: the same official login from a terminal, and what a paid
 * build adds through its extensions (nothing in the default console). */
function OtherWays({
  driver,
  name,
  accountRef,
  addingKey,
  keyOffered,
  onAddingKeyChange,
  onCreated,
}: {
  driver: string
  name: string
  accountRef?: string
  /** The key form is open in place of the trigger; the panel owns it. */
  addingKey: boolean
  /** The key is offered here; false where the panel already offered it first. */
  keyOffered: boolean
  onAddingKeyChange: (adding: boolean) => void
  /** After the key is stored: the caller shows what comes next. */
  onCreated?: (record: ProviderRecordDTO) => void
}) {
  const { t } = useTranslation('agentTools')
  const { can } = useAuth()
  const extra = (PANEL_EXTENSIONS.profileSignInWays ?? []).filter(
    (way) => way.permission === undefined || can(way.permission),
  )
  const command = accountRef
    ? `olivares tool login ${driver} --account ${name}`
    : `olivares tool login ${driver}`
  return (
    <details className="border-t border-line pt-3">
      <summary className="cursor-pointer text-body text-text-2">
        {t('signIn.otherWays')}
      </summary>
      <div className="mt-3 flex flex-col gap-3">
        <div className="flex flex-col gap-1.5">
          <span className="text-caption text-text-2">
            {t('signIn.fromTerminal')}
          </span>
          <CodeLine inline command={command} />
        </div>
        {keyOffered ? (
          <KeyOffer
            driver={driver}
            accountRef={accountRef}
            addingKey={addingKey}
            onAddingKeyChange={onAddingKeyChange}
            onCreated={onCreated}
          />
        ) : null}
        {extra.map((way) => (
          <PanelSlot key={way.id}>
            <way.Component
              driver={driver}
              accountRef={accountRef}
              {...(accountRef ? { accountName: name } : {})}
            />
          </PanelSlot>
        ))}
      </div>
    </details>
  )
}

/** The sign-in of one profile: the account's own home, or the organization's default
 * login. `onSignedIn` runs once the tool says it is signed in. */
export function ProfileSignInPanel({
  driver,
  name,
  account,
  tenant,
  autoStart = false,
  onSignedIn,
  onKeyCreated,
}: {
  driver: string
  name: string
  account?: ProviderAccountDTO
  tenant: string | null
  autoStart?: boolean
  onSignedIn?: () => void
  onKeyCreated?: (record: ProviderRecordDTO) => void
}) {
  const qc = useQueryClient()
  // The key form of Other ways, open over the sign-in: a login that completes under it
  // must not close the sheet over a key being typed. Its close re-renders the
  // sign-in, which reports the login again.
  const [addingKey, setAddingKey] = useState(false)
  const { t } = useTranslation('agentTools')
  // Gemini CLI no longer serves free personal Google accounts: say which accounts
  // work and offer the API key first. The person then picks; no Google login starts
  // under the key offer. Where no key can be added, the Google sign-in runs as before.
  const gemini = driver === 'gemini-cli'
  const keyKind = useKeyKind(driver as SignInTool, account?.account_ref)
  const keyFirst = gemini && keyKind !== undefined
  return (
    <div className="flex flex-col gap-4">
      {gemini ? (
        <p className="text-body text-text-2">{t('signIn.geminiAccounts')}</p>
      ) : null}
      {keyFirst ? (
        <KeyOffer
          driver={driver}
          addingKey={addingKey}
          onAddingKeyChange={setAddingKey}
          onCreated={onKeyCreated}
        />
      ) : null}
      {account && tenant ? (
        <AccountSignIn
          account={account}
          tenant={tenant}
          autoStart={autoStart}
          onSignedIn={onSignedIn}
          quiet
          explainRefusal
        />
      ) : tenant ? (
        <SignIn
          driver={driver as SignInTool}
          tenantId={tenant}
          autoStart={autoStart && !keyFirst}
          // The key form is offered once for this sheet: first for Gemini, else in Other ways.
          offerKey={false}
          onSignedIn={() => {
            void qc.invalidateQueries({ queryKey: firstHourKeys.all(tenant) })
            if (!addingKey) onSignedIn?.()
          }}
        />
      ) : null}
      <OtherWays
        driver={driver}
        name={name}
        accountRef={account?.account_ref}
        addingKey={addingKey}
        keyOffered={!keyFirst}
        onAddingKeyChange={setAddingKey}
        onCreated={onKeyCreated}
      />
    </div>
  )
}

/** Sign in again, or for the first time, from a row. Closes when the tool is signed in. */
export function ProfileSignInDialog({
  driver,
  name,
  account,
  tenant,
  onOpenChange,
}: {
  driver: string
  name: string
  account?: ProviderAccountDTO
  tenant: string | null
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation('agentTools')
  const qc = useQueryClient()
  // The key write must not be cut off by a Radix-initiated close (X, Escape, overlay).
  const keyBeingStored = useKeyBeingStored()
  // A login can outlive its dialog: the rows read the tool again on close, so one still
  // running shows "Signing in…" there instead of offering a second sign-in.
  const close = (open: boolean) => {
    if (!open) {
      void qc.invalidateQueries({ queryKey: firstHourKeys.all(tenant) })
      void qc.invalidateQueries({
        predicate: (query) => query.queryKey.includes('account-sign-in'),
      })
    }
    onOpenChange(open)
  }
  return (
    <Dialog
      open
      onOpenChange={(o) => (keyBeingStored() ? undefined : close(o))}
    >
      <DialogContent className={PHONE_SHEET}>
        <DialogHeader>
          <DialogTitle>{t('signIn.title', { name })}</DialogTitle>
          <DialogDescription>{t('signIn.description')}</DialogDescription>
        </DialogHeader>
        <ProfileSignInPanel
          driver={driver}
          name={name}
          account={account}
          tenant={tenant}
          autoStart
          onSignedIn={() => close(false)}
          // A stored key changes what the row shows: close the sheet, the card behind
          // re-reads and shows it ready.
          onKeyCreated={() => close(false)}
        />
      </DialogContent>
    </Dialog>
  )
}
