// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The passkey offer on the first screen: a person who signed in with a password and has
// no passkey is offered one, once, in one line with one action. It is drawn only where
// the engine says this address allows signing in with a passkey (an IP address does not)
// and the browser can make one, and never for a session a directory (SSO) signed in.
// «Add a passkey» runs the console's one registration; «Not now» is kept with the
// person's notices on the server, so another browser does not offer it again.
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { KeyRound } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { offersPasskey } from '@/app/pages/login-passkey'
import { Button } from '@/components/ui/button'
import { Notice } from '@/components/ui/notice'
import { toast } from '@/components/ui/toaster'
import { identityApi, identityKeys } from '@/features/identity/api'
import {
  canEnrollPasskey,
  enrollPasskey,
  RegistrationOutcomeUnknown,
} from '@/features/identity/enroll-passkey'
import '@/features/identity/i18n'
import { defaultPasskeyName } from '@/features/identity/passkey-name'
import { useAuth } from '@/lib/auth/context'
import { serverInfoQuery } from '@/lib/hooks/use-server-info'
import { noticeSeen, rememberNotice } from '@/lib/notify'
import { useStepUpOwner } from '@/stores/step-up'
import './i18n'

/** «Not now», in the person's notices. */
const OFFER = 'passkey-offer'

export function PasskeyOffer() {
  const { t } = useTranslation('firstHour')
  const { principal, activeTenant } = useAuth()
  const queryClient = useQueryClient()
  const captureOwner = useStepUpOwner()
  const [dismissed, setDismissed] = useState(() => noticeSeen(OFFER))
  const [pending, setPending] = useState(false)
  // A password sign-in; a directory's sign-in is the directory's to secure.
  const methods = principal?.amr ?? []
  const byPassword = methods.includes('pwd') && !methods.includes('sso')
  const asked = !dismissed && byPassword && canEnrollPasskey()
  const info = useQuery({ ...serverInfoQuery, enabled: asked })
  const allowed = asked && offersPasskey(info.data)
  const list = identityKeys.webauthnCredentials(activeTenant)
  const passkeys = useQuery({
    queryKey: list,
    queryFn: () => identityApi.webauthnCredentials(),
    enabled: allowed,
    retry: false,
  })

  async function add() {
    if (pending) return
    const attempt = captureOwner().begin()
    setPending(true)
    try {
      await enrollPasskey(defaultPasskeyName(t), attempt)
      attempt.dispatchGuard()
      toast.success(t('passkeyOffer.added'))
    } catch (err) {
      // Closing the browser's sheet is the person's choice: nothing to say.
      const closed =
        err instanceof DOMException &&
        (err.name === 'NotAllowedError' || err.name === 'AbortError')
      if (!attempt.current() || closed) return
      // A lost answer is not a refusal: the passkey may be saved.
      const unknown = err instanceof RegistrationOutcomeUnknown
      toast.error(t(unknown ? 'passkeyOffer.unknown' : 'passkeyOffer.failed'))
    } finally {
      // The list read again decides: a saved passkey, even one whose answer was
      // lost on the way, ends the offer.
      if (attempt.current()) {
        setPending(false)
        void queryClient.invalidateQueries({ queryKey: list })
      }
      attempt.retire()
    }
  }

  function notNow() {
    rememberNotice(OFFER)
    setDismissed(true)
  }

  // Only to a person whose passkeys were read and who has none.
  if (!allowed || passkeys.data?.items.length !== 0) return null
  return (
    <Notice icon={<KeyRound />}>
      <p>{t('passkeyOffer.body')}</p>
      <div className="mt-2 flex flex-wrap gap-2">
        <Button
          variant="secondary"
          size="sm"
          aria-busy={pending || undefined}
          aria-disabled={pending || undefined}
          onClick={() => void add()}
        >
          {t('passkeyOffer.add')}
        </Button>
        <Button variant="ghost" size="sm" onClick={notNow}>
          {t('passkeyOffer.notNow')}
        </Button>
      </div>
    </Notice>
  )
}
