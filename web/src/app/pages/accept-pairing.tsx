// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { CircleAlert } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AuthSheet, AuthShell } from '@/components/layout/auth-shell'
import { Button } from '@/components/ui/button'
import { Notice } from '@/components/ui/notice'
import { authApi } from '@/lib/api/endpoints'
import { useAuth } from '@/lib/auth/context'
import '@/features/identity/i18n'

export function AcceptPairingPage() {
  const { t } = useTranslation('identity')
  const navigate = useNavigate()
  const { adoptSession } = useAuth()
  const [token] = useState(
    () => new URLSearchParams(window.location.hash.slice(1)).get('pair') ?? '',
  )
  useEffect(() => {
    window.history.replaceState(window.history.state, '', '/pair')
  }, [])
  const pairing = useMutation({
    mutationFn: () => authApi.acceptPairing(token),
    onSuccess: async (session) => {
      await adoptSession(session)
      void navigate({ to: '/', replace: true })
    },
  })
  return (
    <AuthShell>
      <AuthSheet>
        <div className="flex flex-col gap-1">
          <h1 className="text-[1.375rem]/7 font-extrabold text-balance text-text">
            {t('sessions.pairTitle')}
          </h1>
          <p className="text-body text-pretty text-text-2">
            {t('sessions.acceptHint')}
          </p>
        </div>
        {!token || pairing.isError ? (
          <Notice tone="bad" icon={<CircleAlert />} role="alert">
            {t('sessions.pairInvalid')}
          </Notice>
        ) : null}
        <Button
          variant="primary"
          size="lg"
          disabled={!token || pairing.isPending || pairing.isSuccess}
          onClick={() => pairing.mutate()}
          className="mt-0.5 w-full"
        >
          {t('sessions.accept')}
        </Button>
      </AuthSheet>
    </AuthShell>
  )
}
